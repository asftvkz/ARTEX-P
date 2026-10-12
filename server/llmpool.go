package server

import (
	"context"
	"fmt"
	"iter"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/llmpool"
	"github.com/Autumn-27/norma/llm"
)

// 普通对话的全局继承按每次模型调用重读池路由；显式绑定不走此策略。
type globalConversationProvider struct{ s *Server }

func (p globalConversationProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	prov, _, ok := p.s.globalProviderContext(ctx, nil)
	if !ok {
		return llm.Message{}, "", llm.Usage{}, db.ErrLLMModelUnavailable
	}
	return prov.Complete(ctx, req)
}
func (p globalConversationProvider) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		prov, _, ok := p.s.globalProviderContext(ctx, nil)
		if !ok {
			yield(llm.StreamEvent{}, db.ErrLLMModelUnavailable)
			return
		}
		prov.Stream(ctx, req)(yield)
	}
}

// LLM 轮询(故障转移)的服务端接线。设计见 docs/LLM轮询设计.md：
//   - 全局激活配置这条路径(agent 未绑定、任务未 pin)才轮询;
//   - 绑定/pin 的路径默认独占该配置,失败即失败(可由 llm_pool_bind_fallback 打开兜底);
//   - 链序 = 激活配置 → 其余按 priority DESC,排除 pool_exclude 的;
//   - 熔断状态进程级共享(s.llmHealth),重建 pool 不清空。

// newLLMHealthRegistry builds the process-wide circuit-breaker registry, mirroring
// state into PG so a cooling-off window survives a restart.
// 状态变更和持久化按操作顺序串行，防止 reset 后旧异步写入使冷却复活。
func newLLMHealthRegistry(pg *db.DB) *llmpool.Registry {
	if pg == nil {
		return llmpool.NewRegistry(nil, nil)
	}
	persist := func(key string, st llmpool.State) {
		profileID, modelID, ok := parseLLMMemberKey(key)
		if !ok {
			return
		}
		h := db.LLMHealth{ProfileID: profileID, ModelID: modelID, Fails: st.Fails, Trips: st.Trips, LastError: st.LastError}
		if !st.OpenUntil.IsZero() {
			t := st.OpenUntil
			h.OpenUntil = &t
		}
		if err := pg.SaveLLMHealth(h); err != nil {
			log.Printf("[llmpool] 熔断状态落库失败: %v", err)
		}
	}
	forget := func(key string) {
		profileID, modelID, ok := parseLLMMemberKey(key)
		if ok {
			_ = pg.ClearLLMModelHealth(profileID, modelID)
		}
	}
	reg := llmpool.NewMemberRegistry(persist, forget)
	// Restore only windows that haven't expired (LoadLLMHealth filters), so a
	// profile that finished cooling while we were down comes back healthy.
	if rows, err := pg.LoadLLMHealth(); err == nil {
		for _, h := range rows {
			st := llmpool.State{Fails: h.Fails, Trips: h.Trips, LastError: h.LastError, LastAt: h.LastAt}
			if h.OpenUntil != nil {
				st.OpenUntil = *h.OpenUntil
			}
			reg.Restore(fmt.Sprintf("%d:%d", h.ProfileID, h.ModelID), st)
			log.Printf("[llmpool] 恢复熔断状态: 配置 #%d 冷却至 %s", h.ProfileID, st.OpenUntil.Format(time.RFC3339))
		}
	}
	return reg
}

// poolMember builds one chain member from a profile, reusing the per-profile
// provider cache so every agent pointing at the same profile shares one provider
// (hence one rate limiter). Returns nil when the profile can't be built.
func (s *Server) poolMember(p *db.LLMProfile, rank int, tasks ...*Task) *llmpool.Member {
	return s.poolMemberContext(context.Background(), p, rank, firstTask(tasks))
}

func (s *Server) poolMemberContext(ctx context.Context, p *db.LLMProfile, rank int, task *Task) *llmpool.Member {
	selection := s.poolModelSelection(p.ID)
	model, err := s.m.pg.ResolveLLMModel(context.Background(), selection)
	if err != nil {
		return nil
	}
	prov, cfg, ok := s.providerForTaskSelection(db.LLMModelSelection{ProfileID: p.ID, ModelMode: db.LLMModelModeFixed, ModelID: &model.ID}, task, ctx)
	if !ok {
		return nil
	}
	return &llmpool.Member{
		ID: p.ID, ModelID: model.ID, MemberKey: model.MemberKey(), Name: p.Name, Model: model.UpstreamModelID, Format: p.Format,
		Priority: p.Priority, Active: p.IsDefault, Rank: rank,
		WindowTokens: cfg.CompactionWindow(), Prov: prov,
	}
}

// poolChain reads the failover chain from the DB. headID/headProv/headCfg describe
// the member that must lead the chain — the globally active profile on the normal
// path, or an explicitly bound one when bound-profile fallback is on. The head is
// passed in (rather than resolved through the cache) so applyLLM's already-built
// provider is reused instead of duplicated.
//
// Returns nil when failover is off or the chain has fewer than two usable members
// — callers then use the bare provider, which is byte-for-byte the old behavior.
func (s *Server) poolChain(headID int64, headProv llm.Provider, headCfg agent.Config) *llmpool.Pool {
	return s.poolChainResolved(headID, headProv, headCfg, false)
}

func (s *Server) poolChainResolved(headID int64, headProv llm.Provider, headCfg agent.Config, routeHead bool, tasks ...*Task) *llmpool.Pool {
	return s.poolChainContext(context.Background(), headID, headProv, headCfg, routeHead, firstTask(tasks))
}

func (s *Server) poolChainContext(ctx context.Context, headID int64, headProv llm.Provider, headCfg agent.Config, routeHead bool, task *Task) *llmpool.Pool {
	if s.m == nil || s.m.pg == nil || !s.m.LLMPoolEnabled() {
		return nil
	}
	profs, err := s.m.pg.PoolProfiles()
	if err != nil {
		log.Printf("[llmpool] 读取轮询链失败: %v", err)
		return nil
	}
	var head *db.LLMProfile
	for _, p := range profs {
		if p.ID == headID {
			head = p
			break
		}
	}
	if head == nil { // head excluded from the chain (bound + pool_exclude) — still leads
		if p, err := s.m.pg.ProfileByID(headID); err == nil && p != nil {
			head = p
		} else {
			return nil
		}
	}
	var headMember *llmpool.Member
	if routeHead {
		headMember = s.poolMemberContext(ctx, head, llmpool.RankActive, task)
	} else {
		set, err := s.m.pg.ListLLMModels(head.ID)
		if err != nil {
			return nil
		}
		for _, model := range set.Models {
			if model.UpstreamModelID == headCfg.Model && model.Enabled {
				headMember = &llmpool.Member{
					ID: head.ID, Name: head.Name, Format: head.Format,
					ModelID: model.ID, MemberKey: model.MemberKey(), Model: headCfg.Model,
					Priority: head.Priority, Active: head.IsDefault, Rank: llmpool.RankActive,
					WindowTokens: headCfg.CompactionWindow(), Prov: headProv,
				}
				break
			}
		}
	}
	if headMember == nil {
		return nil
	}
	members := []*llmpool.Member{headMember}
	for _, p := range profs {
		if p.ID == headID {
			continue
		}
		// The globally active profile outranks the others when it isn't the head
		// (bound-fallback chains), so it stays the first fallback tried.
		rank := p.Priority
		if p.IsDefault {
			rank = llmpool.RankActive - 1
		}
		if m := s.poolMemberContext(ctx, p, rank, task); m != nil {
			members = append(members, m)
		}
	}
	return llmpool.New(members, s.llmHealth)
}

// poolForActive wraps the globally-active provider in the failover chain. Returns
// prov unchanged when failover is off or there's nothing to fall back to.
func (s *Server) poolForActive(activeID int64, prov llm.Provider, cfg agent.Config, tasks ...*Task) llm.Provider {
	pool := s.poolChainResolved(activeID, prov, cfg, true, tasks...)
	if pool == nil {
		return prov
	}
	names := make([]string, 0, len(pool.Members()))
	for _, m := range pool.Members() {
		names = append(names, m.Name+"/"+m.Model)
	}
	log.Printf("[llmpool] LLM 轮询已启用，链路(%d): %v", len(names), names)
	return pool
}

// poolForBinding wraps a BOUND profile's provider so it falls back to the chain.
// Only active when both the failover master switch and the bound-fallback switch
// are on; otherwise a bound profile stays exclusive (fails hard), which is the
// default and the documented precedence.
func (s *Server) poolForBinding(id int64, prov llm.Provider, cfg agent.Config, tasks ...*Task) llm.Provider {
	if s.m == nil || !s.m.LLMPoolEnabled() || !s.m.LLMPoolBindFallback() {
		return prov
	}
	if pool := s.poolChainResolved(id, prov, cfg, false, tasks...); pool != nil {
		return pool
	}
	return prov
}

// LLMPoolMemberStatus is one chain entry as shown in the UI.
type LLMPoolMemberStatus struct {
	ProfileID       string `json:"profile_id"`
	ModelMode       string `json:"model_mode"`
	ModelID         string `json:"model_id"`
	UpstreamModelID string `json:"upstream_model_id"`
	MemberKey       string `json:"member_key"`
	Name            string `json:"name"`
	Model           string `json:"model"`
	Format          string `json:"format"`
	Priority        int    `json:"priority"`
	Active          bool   `json:"active"`   // is the globally-activated profile
	Excluded        bool   `json:"excluded"` // pool_exclude — not a failover target
	// Health: state is "ok" | "degraded" (failing but not tripped) | "tripped".
	State        string `json:"state"`
	Fails        int    `json:"fails"`
	Trips        int    `json:"trips"`
	CooldownSecs int    `json:"cooldown_secs"` // remaining cooling-off seconds; 0 = none
	LastError    string `json:"last_error,omitempty"`
	LastAt       string `json:"last_at,omitempty"`
}

// llmPoolStatus reports the whole picture for the LLM page: whether failover is
// on, the resolved chain order, and each profile's breaker state. Excluded
// profiles are listed too (flagged), so the user can see why one isn't in line.
func (s *Server) llmPoolStatus() map[string]any {
	out := map[string]any{
		"enabled":       false,
		"bind_fallback": false,
		"chain":         []LLMPoolMemberStatus{},
	}
	if s.m == nil || s.m.pg == nil {
		return out
	}
	out["enabled"] = s.m.LLMPoolEnabled()
	out["bind_fallback"] = s.m.LLMPoolBindFallback()

	all, err := s.m.pg.ListProfiles()
	if err != nil {
		return out
	}
	health := s.llmHealth.Snapshot()
	now := time.Now()
	// Chain order: active first, then priority DESC / id ASC — the same ordering
	// PoolProfiles applies, recomputed here so excluded ones can be shown in place.
	chain := make([]LLMPoolMemberStatus, 0, len(all))
	for _, p := range all {
		selection := s.poolModelSelection(p.ID)
		model, err := s.m.pg.ResolveLLMModel(context.Background(), selection)
		if err != nil {
			continue
		}
		st := health[model.MemberKey()]
		m := LLMPoolMemberStatus{
			ProfileID: i64s(p.ID), Name: p.Name, Model: model.UpstreamModelID, Format: p.Format,
			ModelMode: selection.ModelMode, ModelID: i64s(model.ID), UpstreamModelID: model.UpstreamModelID, MemberKey: model.MemberKey(),
			Priority: p.Priority, Active: p.IsDefault, Excluded: p.PoolExclude,
			State: "ok", Fails: st.Fails, Trips: st.Trips,
			LastError: st.LastError,
		}
		if st.Open() {
			m.State = "tripped"
			m.CooldownSecs = int(st.OpenUntil.Sub(now).Seconds()) + 1
		} else if st.Fails > 0 {
			m.State = "degraded"
		}
		if !st.LastAt.IsZero() {
			m.LastAt = st.LastAt.Format(time.RFC3339)
		}
		chain = append(chain, m)
	}
	sortPoolStatus(chain)
	out["chain"] = chain
	return out
}

func parseLLMMemberKey(key string) (int64, int64, bool) {
	parts := strings.Split(key, ":")
	if len(parts) != 2 {
		return 0, 0, false
	}
	p, e1 := strconv.ParseInt(parts[0], 10, 64)
	m, e2 := strconv.ParseInt(parts[1], 10, 64)
	return p, m, e1 == nil && e2 == nil && p > 0 && m > 0 && key == fmt.Sprintf("%d:%d", p, m)
}

func (s *Server) poolModelSelection(profileID int64) db.LLMModelSelection {
	selection := db.LLMModelSelection{ProfileID: profileID, ModelMode: db.LLMModelModeChannelDefault}
	if routes, err := s.m.pg.ListLLMPoolModelRoutes(context.Background()); err == nil {
		for _, route := range routes {
			if route.ProfileID == profileID {
				return route
			}
		}
	} else {
		log.Printf("[llmpool] 读取模型路由失败: %v", err)
		selection.ModelMode = "unavailable"
	}
	return selection
}

// sortPoolStatus orders the status rows exactly like the live chain: active first,
// then priority DESC, then id ASC (the incoming slice is already id-ordered, so a
// stable insertion sort on the first two keys is enough).
func sortPoolStatus(in []LLMPoolMemberStatus) {
	for i := 1; i < len(in); i++ {
		for j := i; j > 0 && poolLess(in[j], in[j-1]); j-- {
			in[j], in[j-1] = in[j-1], in[j]
		}
	}
}

func poolLess(a, b LLMPoolMemberStatus) bool {
	if a.Active != b.Active {
		return a.Active
	}
	return a.Priority > b.Priority
}
