package server

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/egress"
)

func warpProfileID(id int64) string { return "proxy_" + strconv.FormatInt(id, 10) }
func (s *Server) warpAction(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "invalid proxy id")
		return
	}
	var req struct {
		AcceptTerms bool `json:"accept_terms"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "bad json")
			return
		}
	}
	runtime, err := s.m.proxyRuntime()
	if err != nil {
		writeErr(w, 503, "代理运行时未初始化")
		return
	}
	p, err := s.m.pg.WithIdleProxy(id, func(p *db.Proxy) error {
		if p.Kind != db.ProxyKindWarp {
			return errors.New("不是 WARP 代理")
		}
		var profile egress.WarpProfile
		var err error
		switch r.PathValue("action") {
		case "register":
			profile, err = runtime.warp.Create(r.Context(), warpProfileID(id), p.Name, req.AcceptTerms)
		case "start":
			profile, err = runtime.warp.Start(r.Context(), warpProfileID(id))
		case "stop":
			profile, err = runtime.warp.Stop(r.Context(), warpProfileID(id))
		case "recover":
			profile, err = runtime.warp.Resume(r.Context(), warpProfileID(id))
		default:
			return errors.New("未知 WARP 操作")
		}
		if err != nil {
			return err
		}
		p.Status = db.ProxyStatusStopped
		if profile.Phase == "ready" {
			connection, err := runtime.warp.Connection(warpProfileID(id))
			if err != nil {
				return err
			}
			p.Host, p.Port, p.Username, p.Password, p.Protocol = connection.Host, connection.Port, connection.Username, connection.Password, "socks5"
			p.URL = (&url.URL{Scheme: "socks5", Host: net.JoinHostPort(p.Host, strconv.Itoa(p.Port)), User: url.UserPassword(p.Username, p.Password)}).String()
			p.Status = db.ProxyStatusHealthy
		}
		p.LastError = ""
		return nil
	})
	if err != nil {
		code := 502
		if errors.Is(err, db.ErrProxyInUse) {
			code = 409
		}
		writeErr(w, code, "WARP 操作失败："+safeProxyRuntimeError(err))
		return
	}
	writeJSON(w, 200, proxyDTO(p))
}

func safeProxyRuntimeError(err error) string {
	if errors.Is(err, db.ErrProxyInUse) {
		return "代理被任务或全局默认引用"
	}
	if errors.Is(err, context.Canceled) {
		return "操作已取消"
	}
	return "运行器未完成操作，请检查档案状态；不确定注册禁止重试"
}
