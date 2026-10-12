package server

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/egress"
	"github.com/google/uuid"
)

type proxyRequest struct {
	AcceptTerms     bool         `json:"accept_terms"`
	Name            string       `json:"name"`
	Kind            db.ProxyKind `json:"kind"`
	Protocol        string       `json:"protocol"`
	URL             *string      `json:"url"`
	Host            string       `json:"host"`
	Port            int          `json:"port"`
	Username        string       `json:"username"`
	Password        *string      `json:"password"`
	ResinPlatform   string       `json:"resin_platform"`
	ResinAdminURL   string       `json:"resin_admin_url"`
	ResinAdminToken *string      `json:"resin_admin_token"`
	ClearPassword   bool         `json:"clear_password"`
	ClearAdminToken bool         `json:"clear_resin_admin_token"`
	Enabled         *bool        `json:"enabled"`
}

func (s *Server) listProxies(w http.ResponseWriter, r *http.Request) {
	items, err := s.m.pg.ListProxies()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	out := make([]*ProxyDTO, 0, len(items))
	for _, item := range items {
		out = append(out, s.runtimeProxyDTO(item))
	}
	global, _ := s.m.pg.GlobalProxyID()
	writeJSON(w, 200, map[string]any{"proxies": out, "global_proxy_id": global})
}

func (s *Server) createProxy(w http.ResponseWriter, r *http.Request) {
	var raw json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	var req proxyRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	p := proxyFromRequest(req)
	if p.Kind == db.ProxyKindWarp {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			writeErr(w, 400, "WARP 创建请求必须为 JSON 对象")
			return
		}
		for field := range fields {
			switch field {
			case "name", "kind", "accept_terms", "enabled":
			default:
				writeErr(w, 400, "WARP 创建只接受名称、类型、条款确认和启用状态")
				return
			}
		}
		if !req.AcceptTerms {
			writeErr(w, 400, "请先阅读并接受 Cloudflare WARP 条款")
			return
		}
		if len(p.Name) > 80 || !p.Enabled {
			writeErr(w, 400, "WARP 名称不能超过 80 字节，创建时必须启用")
			return
		}
		keys := r.Header.Values("Idempotency-Key")
		if len(keys) != 1 {
			writeErr(w, 400, "WARP 创建需要 UUID Idempotency-Key")
			return
		}
		key := strings.TrimSpace(keys[0])
		parsed, err := uuid.Parse(key)
		if err != nil || parsed.String() != strings.ToLower(key) {
			writeErr(w, 400, "WARP 创建需要 UUID Idempotency-Key")
			return
		}
		s.createWarpProxy(w, r, p, req, parsed.String())
		return
	}
	created, err := s.m.pg.CreateProxy(p)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, s.runtimeProxyDTO(created))
}

func (s *Server) createWarpProxy(w http.ResponseWriter, r *http.Request, p *db.Proxy, req proxyRequest, key string) {
	payload, err := json.Marshal(struct {
		Name        string       `json:"name"`
		Kind        db.ProxyKind `json:"kind"`
		AcceptTerms bool         `json:"accept_terms"`
		Enabled     bool         `json:"enabled"`
	}{Name: p.Name, Kind: p.Kind, AcceptTerms: req.AcceptTerms, Enabled: p.Enabled})
	if err != nil {
		writeErr(w, 400, "WARP 创建请求无效")
		return
	}
	hash := sha256.Sum256(payload)
	created, intent, err := s.m.pg.CreateWarpProxyOnce(key, hash, p)
	if err != nil {
		switch {
		case errors.Is(err, db.ErrWarpCreateConflict), errors.Is(err, db.ErrProxyInUse):
			writeErr(w, 409, "Idempotency-Key 已用于不同的 WARP 创建请求")
		case errors.Is(err, db.ErrWarpCreateGone):
			writeErr(w, 410, "该 WARP 创建记录已删除，请使用新的创建意图")
		default:
			writeErr(w, 400, err.Error())
		}
		return
	}
	if !intent.Created {
		switch intent.State {
		case "succeeded":
			writeJSON(w, 201, s.runtimeProxyDTO(created))
		case "failed":
			writeJSON(w, 502, map[string]any{"error": "WARP 创建未成功；请核对原档案，不要重复注册", "proxy_id": intent.ProxyID, "proxy": s.runtimeProxyDTO(created), "operation_status": "failed"})
		default:
			writeJSON(w, 202, map[string]any{"proxy_id": intent.ProxyID, "proxy": proxyDTO(created), "operation_status": "pending"})
		}
		return
	}

	// 注册只属于首次创建者；数据库事务已提交，网络操作不占生命周期锁。
	rt, err := s.m.proxyRuntime()
	var connection *db.Proxy
	if err == nil {
		_, err = rt.warp.Create(r.Context(), warpProfileID(created.ID), created.Name, req.AcceptTerms)
		if err == nil {
			c, connectionErr := rt.warp.Connection(warpProfileID(created.ID))
			err = connectionErr
			if err == nil {
				connection = &db.Proxy{Host: c.Host, Port: c.Port, Username: c.Username, Password: c.Password}
				connection.URL = (&url.URL{Scheme: "socks5", Host: net.JoinHostPort(c.Host, strconv.Itoa(c.Port)), User: url.UserPassword(c.Username, c.Password)}).String()
			}
		}
	}
	failure := ""
	if err != nil {
		failure = safeProxyRuntimeError(err)
	}
	completed, completeErr := s.m.pg.CompleteWarpProxyCreate(key, created.ID, intent.ExecutionToken, connection, failure)
	if completeErr != nil {
		writeJSON(w, 502, map[string]any{"error": "WARP 创建结果未能持久化；请核对原档案，不要重新注册", "proxy_id": created.ID, "proxy": proxyDTO(created), "operation_status": "pending"})
		return
	}
	if err != nil {
		writeJSON(w, 502, map[string]any{"error": "WARP 创建未成功；请核对原档案，不要重复注册", "proxy_id": completed.ID, "proxy": s.runtimeProxyDTO(completed), "operation_status": "failed"})
		return
	}
	writeJSON(w, 201, s.runtimeProxyDTO(completed))
}

func (s *Server) getProxy(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "invalid proxy id")
		return
	}
	p, err := s.m.pg.GetProxy(id)
	if err != nil {
		writeErr(w, 404, "代理不存在")
		return
	}
	writeJSON(w, 200, s.runtimeProxyDTO(p))
}

func (s *Server) updateProxy(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "invalid proxy id")
		return
	}
	var req proxyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	p := proxyFromRequest(req)
	p.ID = id
	updated, err := s.m.pg.UpdateProxyWithOptions(p, db.ProxyUpdateOptions{URL: req.URL, Password: req.Password, AdminToken: req.ResinAdminToken, ClearPassword: req.ClearPassword, ClearAdminToken: req.ClearAdminToken, Enabled: req.Enabled})
	if err != nil {
		if errors.Is(err, db.ErrProxyInUse) {
			writeErr(w, 409, "代理正在创建或被引用")
		} else {
			writeErr(w, 400, err.Error())
		}
		return
	}
	writeJSON(w, 200, s.runtimeProxyDTO(updated))
}

func (s *Server) deleteProxy(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "invalid proxy id")
		return
	}
	if err := s.m.pg.DeleteProxyWithCleanup(id, func(p *db.Proxy) error {
		if p.Kind != db.ProxyKindWarp {
			return nil
		}
		rt, err := s.m.proxyRuntime()
		if err != nil {
			return err
		}
		profile, err := rt.warp.Get(warpProfileID(id))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return errors.New("WARP 档案不可读")
		}
		if profile.Desired {
			return errors.New("请先停止 WARP 出口")
		}
		return rt.warp.Delete(r.Context(), warpProfileID(id))
	}); err != nil {
		if errors.Is(err, db.ErrProxyInUse) {
			writeErr(w, 409, "代理正在被任务使用")
		} else {
			writeErr(w, 500, err.Error())
		}
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": id})
}

func (s *Server) testProxy(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "invalid proxy id")
		return
	}
	p, err := s.m.pg.GetProxy(id)
	if err != nil {
		writeErr(w, 404, "代理不存在")
		return
	}
	if p.Kind == db.ProxyKindWarp {
		pending, err := s.m.pg.WarpCreatePending(id)
		if err != nil {
			writeErr(w, 500, "无法读取 WARP 创建状态")
			return
		}
		if pending {
			writeErr(w, 409, "WARP 创建尚未完成，请使用原 Idempotency-Key 查询结果")
			return
		}
	}
	var probeErr error
	if p.Kind == db.ProxyKindWarp {
		rt, runtimeErr := s.m.proxyRuntime()
		probeErr = runtimeErr
		if runtimeErr == nil {
			_, probeErr = rt.warp.Probe(r.Context(), warpProfileID(id))
		}
	} else if p.Kind == db.ProxyKindResin {
		probeErr = newResinAdapter().probe(r.Context(), p)
	} else {
		probeErr = egress.ProbeProxy(r.Context(), p.URL, "", p.Kind == db.ProxyKindWarp)
	}
	status := db.ProxyStatusHealthy
	message := ""
	if probeErr != nil {
		status = db.ProxyStatusFailed
		message = probeErr.Error()
	}
	now := time.Now()
	p.Status, p.LastError, p.LastCheckedAt = status, message, &now
	// Preserve secrets by using the store's update path with the existing values.
	if err := s.m.pg.SetProxyStatus(id, status, message, now); err != nil {
		if errors.Is(err, db.ErrProxyInUse) {
			writeErr(w, 409, "WARP 创建状态已改变，请刷新后重试")
		} else {
			writeErr(w, 500, err.Error())
		}
		return
	}
	if probeErr != nil {
		writeErr(w, 502, message)
		return
	}
	writeJSON(w, 200, s.runtimeProxyDTO(p))
}

func (s *Server) setGlobalProxy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProxyID *int64 `json:"proxy_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	if req.ProxyID != nil {
		p, err := s.m.pg.GetProxy(*req.ProxyID)
		if err != nil {
			writeErr(w, 404, "代理不存在")
			return
		}
		if _, err := ResolveTaskProxy(db.TaskProxyModeFixed, p, nil); err != nil {
			writeErr(w, 400, "代理不可用")
			return
		}
	}
	if err := s.m.pg.SetGlobalProxyID(req.ProxyID); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"global_proxy_id": req.ProxyID})
}

func proxyFromRequest(req proxyRequest) *db.Proxy {
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	value := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	return &db.Proxy{Name: strings.TrimSpace(req.Name), Kind: req.Kind, Protocol: req.Protocol, URL: strings.TrimSpace(value(req.URL)), Host: strings.TrimSpace(req.Host), Port: req.Port, Username: strings.TrimSpace(req.Username), Password: value(req.Password), ResinPlatform: strings.TrimSpace(req.ResinPlatform), ResinAdminURL: strings.TrimSpace(req.ResinAdminURL), ResinAdminToken: value(req.ResinAdminToken), Enabled: enabled}
}

// RegisterProxyActionRoutes 供共享路由装配调用；响应统一使用前端代理 DTO。
func (s *Server) RegisterProxyActionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/proxies/{id}", s.getProxy)
	mux.HandleFunc("POST /api/proxies/{id}/warp/{action}", s.warpAction)
	mux.HandleFunc("POST /api/proxies/{id}/resin/release", s.releaseResin)
}
