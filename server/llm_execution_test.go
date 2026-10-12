package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/llmpool"
	"github.com/Autumn-27/norma/llm"
)

func newModelExecutionServer(t *testing.T) *Server {
	t.Helper()
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip("未配置隔离测试数据库")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err = pg.EnsureLLMRecordsTable(); err != nil {
		t.Fatal(err)
	}
	if err = pg.EnsureLLMUsageTable(); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := db.ConfigureProxySecrets(filepath.Join(root, "proxy.key")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARTEX_WARP_DIR", filepath.Join(root, "warp"))
	m := &Manager{pg: pg, dir: root, assets: pg.Assets(), tasks: map[string]*Task{}}
	if err := m.InitProxyRuntime(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	s := &Server{m: m, ctx: t.Context(), jwtKey: []byte("isolated-test-signing-key-32-bytes"), llmHealth: newLLMHealthRegistry(pg), chatBusy: map[string]bool{}, chatCancel: map[string]context.CancelCauseFunc{}}
	s.engine = NewEngine(m)
	return s
}

func executionModels(t *testing.T, s *Server, endpoint string) (int64, db.LLMModelSet) {
	t.Helper()
	id, err := s.m.pg.SaveProfile(&db.LLMProfile{Name: fmt.Sprintf("execution-%d", time.Now().UnixNano()), Format: "openai", Model: "real-a", BaseURL: endpoint, APIKey: "test-key-must-not-echo", Retry: db.RetryOverride{Connect: db.RetryRule{Attempts: -1}, Empty: db.RetryRule{Attempts: -1}, Stream: db.RetryRule{Attempts: -1}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = s.m.pg.Exec(`DELETE FROM llm_profiles WHERE id=$1`, id) })
	set, err := s.m.pg.ReplaceLLMModels(id, []db.LLMModelInput{{UpstreamModelID: "real-a", DisplayName: "deepseek-chat", Enabled: true, Default: true}, {UpstreamModelID: "real-b", DisplayName: "deepseek-chat", Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	return id, set
}

func executionTask(t *testing.T, s *Server, members []db.LLMModelSelection) *Task {
	t.Helper()
	task, err := s.m.CreateTaskWithOptions("多模型执行测试", "", db.TaskCreateOptions{LLMMembers: members, ProxyMode: db.TaskProxyModeDirect})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { id, _ := parseTaskID(task.ID); _ = s.m.pg.DeleteTask(id) })
	return task
}

func switchExecutionDefault(t *testing.T, s *Server, id int64, set db.LLMModelSet, index int) {
	t.Helper()
	inputs := make([]db.LLMModelInput, 0, len(set.Models))
	for i, m := range set.Models {
		inputs = append(inputs, db.LLMModelInput{ID: m.ID, UpstreamModelID: m.UpstreamModelID, DisplayName: m.DisplayName, Enabled: true, Default: i == index})
	}
	if _, err := s.m.pg.ReplaceLLMModels(id, inputs); err != nil {
		t.Fatal(err)
	}
}

func completeExecution(t *testing.T, runtime *taskLLMRuntime) {
	t.Helper()
	_, _, _, err := runtime.Complete(t.Context(), llm.CompletionRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentBlock{{Type: llm.BlockText, Text: "test"}}}}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLLMExecutionDefaultFixedCacheAndSameChannelFailover(t *testing.T) {
	s := newModelExecutionServer(t)
	s.m.llmRecOn = true
	var mu sync.Mutex
	var requested []string
	failA := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		requested = append(requested, body.Model)
		fail := failA && body.Model == "real-a"
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if fail {
			w.WriteHeader(402)
			_, _ = w.Write([]byte(`{"error":{"message":"insufficient_quota"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()
	id, set := executionModels(t, s, upstream.URL)
	a, b := set.Models[0].ID, set.Models[1].ID
	defaultSel := db.LLMModelSelection{ProfileID: id}
	fixedA := db.LLMModelSelection{ProfileID: id, ModelMode: db.LLMModelModeFixed, ModelID: &a}
	pDefault, _, ok := s.providerForSelection(defaultSel)
	if !ok {
		t.Fatal("默认provider失败")
	}
	pFixed, _, ok := s.providerForSelection(fixedA)
	if !ok || pFixed != pDefault {
		t.Fatal("同一实际成员未共享provider")
	}
	dtask := executionTask(t, s, []db.LLMModelSelection{defaultSel})
	ftask := executionTask(t, s, []db.LLMModelSelection{fixedA})
	dr := &taskLLMRuntime{s: s, taskID: dtask.ID, agentKey: "execution-unbound"}
	fr := &taskLLMRuntime{s: s, taskID: ftask.ID, agentKey: "execution-unbound"}
	completeExecution(t, dr)
	switchExecutionDefault(t, s, id, set, 1)
	completeExecution(t, dr)
	completeExecution(t, fr)
	pNew, _, ok := s.providerForSelection(defaultSel)
	if !ok || pNew == pFixed {
		t.Fatal("默认切换复用了旧模型provider")
	}
	pAgain, _, ok := s.providerForSelection(fixedA)
	if !ok || pAgain != pFixed {
		t.Fatal("固定模型缓存被默认变更覆盖")
	}
	chain := executionTask(t, s, []db.LLMModelSelection{fixedA, {ProfileID: id, ModelMode: db.LLMModelModeFixed, ModelID: &b}})
	mu.Lock()
	failA = true
	mu.Unlock()
	completeExecution(t, &taskLLMRuntime{s: s, taskID: chain.ID, agentKey: "execution-unbound"})
	mu.Lock()
	got := strings.Join(requested, ",")
	mu.Unlock()
	if got != "real-a,real-b,real-a,real-a,real-b" {
		t.Fatalf("真实请求模型或链顺序错误: %s", got)
	}
	for _, entry := range []struct {
		task     string
		ids      []int64
		models   []string
		statuses []string
	}{
		{dtask.ID, []int64{a, b}, []string{"real-a", "real-b"}, []string{"ok", "ok"}},
		{ftask.ID, []int64{a}, []string{"real-a"}, []string{"ok"}},
		{chain.ID, []int64{a, b}, []string{"real-a", "real-b"}, []string{"error", "ok"}},
	} {
		for _, table := range []string{"llm_usage", "llm_records"} {
			rows, err := s.m.pg.Query(`SELECT profile_id,model_id,member_key,upstream_model_id,model,status FROM `+table+` WHERE task_id=$1 ORDER BY id`, entry.task)
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for rows.Next() {
				var pid, mid int64
				var key, upstream, model, status string
				if err := rows.Scan(&pid, &mid, &key, &upstream, &model, &status); err != nil {
					t.Fatal(err)
				}
				if count >= len(entry.ids) || pid != id || mid != entry.ids[count] || key != fmt.Sprintf("%d:%d", id, mid) || upstream != entry.models[count] || model != upstream || status != entry.statuses[count] {
					t.Fatalf("%s 持久化调用身份错误: %d %d %s %s %s %s", table, pid, mid, key, upstream, model, status)
				}
				count++
			}
			err = rows.Err()
			rows.Close()
			if err != nil || count != len(entry.ids) {
				t.Fatalf("%s 调用审计遗漏: %d %v", table, count, err)
			}
		}
	}
	rows, err := s.m.pg.Query(`SELECT metadata FROM activity WHERE exploration_id=$1 AND metadata::text LIKE '%llm_transition%'`, chain.ExpID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var audit llmActivityMetadata
		if err := json.Unmarshal(raw, &audit); err != nil {
			t.Fatal(err)
		}
		if audit.LLMTransition.Previous != nil && audit.LLMTransition.Next != nil && audit.LLMTransition.Previous.MemberKey == fmt.Sprintf("%d:%d", id, a) && audit.LLMTransition.Next.MemberKey == fmt.Sprintf("%d:%d", id, b) {
			found = true
		}
	}
	if !found {
		t.Fatal("未持久化同渠道真实成员切换审计")
	}
	state, err := s.m.pg.GetTask(func() int64 { n, _ := parseTaskID(chain.ID); return n }())
	if err != nil || state.ActiveLLMPosition == nil || *state.ActiveLLMPosition != 1 {
		t.Fatalf("同渠道链未推进: %+v %v", state, err)
	}
}

func TestLLMExecutionLateQuotaUsesNewDefault(t *testing.T) {
	s := newModelExecutionServer(t)
	started, released := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		if body.Model == "real-a" {
			close(started)
			<-released
			w.WriteHeader(402)
			_, _ = w.Write([]byte(`{"error":{"message":"insufficient_quota"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()
	id, set := executionModels(t, s, upstream.URL)
	task := executionTask(t, s, []db.LLMModelSelection{{ProfileID: id}})
	runtime := &taskLLMRuntime{s: s, taskID: task.ID, agentKey: "execution-unbound"}
	done := make(chan error, 1)
	go func() { _, _, _, err := runtime.Complete(context.Background(), llm.CompletionRequest{}); done <- err }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(released)
		t.Fatal("调用未启动")
	}
	switchExecutionDefault(t, s, id, set, 1)
	close(released)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("迟到失败未完成")
	}
	entries, err := s.m.pg.TaskLLMProfiles(func() int64 { n, _ := parseTaskID(task.ID); return n }())
	if err != nil || len(entries) != 1 || entries[0].Status != "ready" {
		t.Fatalf("迟到失败耗尽了新默认: %+v %v", entries, err)
	}
}

func TestLLMPoolMemberHealthPersistenceAndExactReset(t *testing.T) {
	s := newModelExecutionServer(t)
	id, set := executionModels(t, s, "http://127.0.0.1:1")
	a, b := set.Models[0], set.Models[1]
	s.llmHealth.Trip(a.MemberKey(), "insufficient_quota", true)
	if s.llmHealth.Get(b.MemberKey()).Open() {
		t.Fatal("同渠道不同模型共享冷却")
	}
	loaded := newLLMHealthRegistry(s.m.pg)
	if !loaded.Get(a.MemberKey()).Open() || loaded.Get(b.MemberKey()).Open() {
		t.Fatal("成员健康持久化身份错误")
	}
	s.llmHealth = loaded
	w := httptest.NewRecorder()
	s.pgLLMPoolReset(w, httptest.NewRequest("POST", "/api/llm/pool/reset", strings.NewReader(fmt.Sprintf(`{"member_key":%q}`, b.MemberKey()))))
	if w.Code != 200 || !loaded.Get(a.MemberKey()).Open() {
		t.Fatalf("精确reset影响了另一个成员: %d", w.Code)
	}
	w = httptest.NewRecorder()
	s.pgLLMPoolReset(w, httptest.NewRequest("POST", "/api/llm/pool/reset", strings.NewReader(fmt.Sprintf(`{"id":%d}`, id))))
	if w.Code != 200 || loaded.Get(a.MemberKey()).Open() {
		t.Fatalf("旧渠道reset未覆盖模型成员: %d", w.Code)
	}
	if reg := newLLMHealthRegistry(s.m.pg); reg.Get(a.MemberKey()).Open() {
		t.Fatal("reset后的持久化状态复活")
	}
	reg := llmpool.NewMemberRegistry(nil, nil)
	reg.Trip(a.MemberKey(), "bad", true)
	reg.Pass(b.MemberKey())
	if !reg.IsOpen(a.MemberKey()) {
		t.Fatal("其他成员成功清除了A熔断")
	}
}
