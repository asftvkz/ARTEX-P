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
	p, err := s.performWarpAction(r.Context(), id, r.PathValue("action"), req.AcceptTerms)
	if err != nil {
		s.writeWarpFailure(w, id, err)
		return
	}
	writeJSON(w, 200, s.runtimeProxyDTO(p))
}

// 建档和旧 action 共用同一引用屏障；Create 已包含启动及出口探测，不再二次 Start。
func (s *Server) performWarpAction(ctx context.Context, id int64, action string, acceptTerms bool) (*db.Proxy, error) {
	runtime, err := s.m.proxyRuntime()
	if err != nil {
		return nil, err
	}
	var operationErr error
	p, err := s.m.pg.WithIdleProxy(id, func(p *db.Proxy) error {
		if p.Kind != db.ProxyKindWarp {
			return errors.New("不是 WARP 代理")
		}
		var profile egress.WarpProfile
		var err error
		switch action {
		case "register":
			profile, err = runtime.warp.Create(ctx, warpProfileID(id), p.Name, acceptTerms)
		case "start":
			profile, err = runtime.warp.Start(ctx, warpProfileID(id))
		case "stop":
			profile, err = runtime.warp.Stop(ctx, warpProfileID(id))
		case "recover":
			profile, err = runtime.warp.Resume(ctx, warpProfileID(id))
		default:
			return errors.New("未知 WARP 操作")
		}
		operationErr = err
		// 操作被拒绝不等于运行器故障；从实际档案刷新，避免毒化仍健康的出口。
		if current, readErr := runtime.warp.Get(warpProfileID(id)); readErr == nil {
			profile = current
		}
		p.Status = db.ProxyStatusStopped
		p.LastError = ""
		if profile.Phase == "ready" {
			connection, err := runtime.warp.Connection(warpProfileID(id))
			if err != nil {
				operationErr = err
			} else {
				p.Host, p.Port, p.Username, p.Password, p.Protocol = connection.Host, connection.Port, connection.Username, connection.Password, "socks5"
				p.URL = (&url.URL{Scheme: "socks5", Host: net.JoinHostPort(p.Host, strconv.Itoa(p.Port)), User: url.UserPassword(p.Username, p.Password)}).String()
				p.Status = db.ProxyStatusHealthy
			}
		}
		if operationErr == nil && action != "stop" && p.Status != db.ProxyStatusHealthy {
			operationErr = errors.New("WARP 尚未就绪")
		}
		if operationErr != nil && p.Status != db.ProxyStatusHealthy {
			p.Status = db.ProxyStatusFailed
			p.LastError = safeProxyRuntimeError(operationErr)
		}
		// 运行失败也持久化失败状态，不能回滚为旧 healthy。
		return nil
	})
	if err != nil {
		return p, err
	}
	return p, operationErr
}

func (s *Server) writeWarpFailure(w http.ResponseWriter, id int64, err error) {
	code := http.StatusBadGateway
	if errors.Is(err, db.ErrProxyInUse) {
		code = http.StatusConflict
	}
	p, _ := s.m.pg.GetProxy(id)
	writeJSON(w, code, map[string]any{"error": "WARP 操作失败：" + safeProxyRuntimeError(err), "proxy_id": id, "proxy": s.runtimeProxyDTO(p)})
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
