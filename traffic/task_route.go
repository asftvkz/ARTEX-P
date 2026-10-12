package traffic

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"sync"
	"time"

	"github.com/Autumn-27/artex/egress"
	mproxy "github.com/lqqyt2423/go-mitmproxy/proxy"
)

// TaskRoute 使用独立监听和不可变上游，录制数据仍写入已有 Traffic 存储。
type TaskRoute struct {
	proxy  *mproxy.Proxy
	addr   string
	once   sync.Once
	mu     sync.Mutex
	conns  map[net.Conn]bool
	closed bool
}

func (r *TaskRoute) ProxyAddr() string { return r.addr }
func (r *TaskRoute) Close() error {
	var err error
	r.once.Do(func() {
		r.mu.Lock()
		r.closed = true
		var conns []net.Conn
		for c := range r.conns {
			conns = append(conns, c)
		}
		r.mu.Unlock()
		err = r.proxy.Close()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	return err
}

func (t *Traffic) NewTaskRoute(ctx context.Context, upstream string) (*TaskRoute, error) {
	var target *url.URL
	var err error
	if upstream != "" {
		target, err = egress.ProxyURL(upstream)
		if err != nil {
			return nil, err
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	addr := listener.Addr().String()
	listener.Close()
	p, err := mproxy.NewProxy(&mproxy.Options{Addr: addr, SslInsecure: true, CaRootPath: filepath.Join(t.dir, "_ca")})
	if err != nil {
		return nil, errors.New("任务录制代理初始化失败")
	}
	var key [24]byte
	if _, err = rand.Read(key[:]); err != nil {
		return nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(key[:])
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("task:"+token))
	p.SetAuthProxy(func(w http.ResponseWriter, r *http.Request) (bool, error) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Proxy-Authorization")), []byte(want)) != 1 {
			return false, errors.New("任务录制代理身份无效")
		}
		r.Header.Del("Proxy-Authorization")
		return true, nil
	})
	p.SetUpstreamProxy(func(*http.Request) (*url.URL, error) { return target, nil })
	// 协议兼容的透明隧道仍使用同一不可变上游，不构成失败直连。
	p.SetShouldInterceptRule(func(req *http.Request) bool { _, pass := t.pass.Load(hostOnly(req.Host)); return !pass })
	p.AddAddon(&sink{t: t})
	route := &TaskRoute{proxy: p, addr: (&url.URL{Scheme: "http", Host: addr, User: url.UserPassword("task", token)}).String(), conns: map[net.Conn]bool{}}
	p.AddAddon(&taskRouteConnections{route: route})
	done := make(chan error, 1)
	go func() { done <- p.Start() }()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case <-done:
			route.Close()
			return nil, errors.New("任务录制代理监听失败")
		case <-ctx.Done():
			route.Close()
			return nil, ctx.Err()
		case <-deadline.C:
			route.Close()
			return nil, errors.New("任务录制代理启动超时")
		default:
		}
		conn, e := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if e == nil {
			conn.Close()
			return route, nil
		}
		select {
		case <-ctx.Done():
			route.Close()
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

type taskRouteConnections struct {
	mproxy.BaseAddon
	route *TaskRoute
}

func (a *taskRouteConnections) ClientConnected(c *mproxy.ClientConn) {
	a.route.mu.Lock()
	closed := a.route.closed
	if !closed {
		a.route.conns[c.Conn] = true
	}
	a.route.mu.Unlock()
	if closed {
		c.Conn.Close()
	}
}
func (a *taskRouteConnections) ClientDisconnected(c *mproxy.ClientConn) {
	a.route.mu.Lock()
	delete(a.route.conns, c.Conn)
	a.route.mu.Unlock()
}
