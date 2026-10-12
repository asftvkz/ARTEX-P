package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/egress"
)

type proxyRequest struct {
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
		out = append(out, proxyDTO(item))
	}
	global, _ := s.m.pg.GlobalProxyID()
	writeJSON(w, 200, map[string]any{"proxies": out, "global_proxy_id": global})
}

func (s *Server) createProxy(w http.ResponseWriter, r *http.Request) {
	var req proxyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	p := proxyFromRequest(req)
	created, err := s.m.pg.CreateProxy(p)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, proxyDTO(created))
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
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, proxyDTO(updated))
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
	var probeErr error
	if p.Kind == db.ProxyKindResin {
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
		writeErr(w, 500, err.Error())
		return
	}
	if probeErr != nil {
		writeErr(w, 502, message)
		return
	}
	writeJSON(w, 200, proxyDTO(p))
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
	mux.HandleFunc("POST /api/proxies/{id}/warp/{action}", s.warpAction)
	mux.HandleFunc("POST /api/proxies/{id}/resin/release", s.releaseResin)
}
