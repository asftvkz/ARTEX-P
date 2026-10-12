package server

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/egress"
)

type resinAdapter struct{ target string }

func newResinAdapter() *resinAdapter {
	return &resinAdapter{}
}

func (a *resinAdapter) probe(ctx context.Context, p *db.Proxy) error {
	if p == nil || p.Kind != db.ProxyKindResin {
		return errors.New("不是 Resin 代理")
	}
	if err := resinConfig(p).Probe(ctx); err != nil {
		return err
	}
	return egress.ProbeProxy(ctx, resinTaskProxyURL(p, "probe_"+strconv.FormatInt(p.ID, 10)), a.target, false)
}

func resinConfig(p *db.Proxy) egress.ResinConfig {
	return egress.ResinConfig{AdminURL: p.ResinAdminURL, AdminToken: p.ResinAdminToken, Platform: p.ResinPlatform}
}

func resinTaskProxyURL(p *db.Proxy, taskID string) string {
	if p == nil || p.Kind != db.ProxyKindResin || p.Host == "" || p.Port <= 0 {
		return ""
	}
	platform := p.ResinPlatform
	if platform == "" {
		platform = "Default"
	}
	protocol := p.Protocol
	if protocol == "" {
		protocol = "socks5"
	}
	u := &url.URL{Scheme: protocol, Host: net.JoinHostPort(p.Host, strconv.Itoa(p.Port)), User: url.UserPassword(platform+".artex_task_"+taskID, p.Password)}
	return u.String()
}

func (s *Server) releaseResin(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "invalid proxy id")
		return
	}
	p, err := s.m.pg.GetProxy(id)
	if err != nil || p.Kind != db.ProxyKindResin {
		writeErr(w, 404, "Resin 代理不存在")
		return
	}
	var req struct {
		TaskID string `json:"task_id"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "bad json")
			return
		}
	}
	// 空请求回收该代理的全部已结束任务，运行中任务不允许管理端回收。
	tasks, err := s.m.pg.ProxyTerminalTasks(id, strings.TrimSpace(req.TaskID))
	if err != nil {
		writeErr(w, 409, "代理租约仍被任务使用或任务不存在")
		return
	}
	for _, row := range tasks {
		t := &Task{ID: strconv.FormatInt(row.ID, 10), ProxyMode: row.ProxyMode, ProxySnapshot: row.ProxySnapshot}
		if err := s.m.ReleaseTaskProxyWhenIdle(r.Context(), t); err != nil {
			code := http.StatusBadGateway
			if errors.Is(err, db.ErrProxyInUse) {
				code = http.StatusConflict
			}
			writeErr(w, code, err.Error())
			return
		}
	}
	writeJSON(w, 200, proxyDTO(p))
}
