package egress

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

// HTTPBridge 让浏览器经本机 HTTP 入口使用带认证 SOCKS5；不录制、不改变共享配置。
type HTTPBridge struct {
	server    *http.Server
	addr      string
	transport *http.Transport
	mu        sync.Mutex
	conns     map[net.Conn]bool
	closed    bool
}

func (b *HTTPBridge) ProxyAddr() string { return b.addr }
func (b *HTTPBridge) Close() error {
	b.mu.Lock()
	b.closed = true
	for c := range b.conns {
		c.Close()
	}
	b.mu.Unlock()
	b.transport.CloseIdleConnections()
	return b.server.Close()
}

func NewHTTPBridge(upstream string) (*HTTPBridge, error) {
	u, err := ProxyURL(upstream)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	var key [24]byte
	if _, err = rand.Read(key[:]); err != nil {
		ln.Close()
		return nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(key[:])
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("task:"+token))
	b := &HTTPBridge{transport: &http.Transport{Proxy: http.ProxyURL(u), ResponseHeaderTimeout: 10 * time.Second}, conns: map[net.Conn]bool{}}
	b.addr = (&url.URL{Scheme: "http", Host: ln.Addr().String(), User: url.UserPassword("task", token)}).String()
	b.server = &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Proxy-Authorization")), []byte(want)) != 1 {
			http.Error(w, "任务出口身份无效", 407)
			return
		}
		r.Header.Del("Proxy-Authorization")
		if r.Method == http.MethodConnect {
			remote, err := dialProxy(r.Context(), u, r.Host)
			if err != nil {
				http.Error(w, "任务代理连接失败", 502)
				return
			}
			client, rw, err := w.(http.Hijacker).Hijack()
			if err != nil {
				remote.Close()
				return
			}
			b.mu.Lock()
			if b.closed {
				b.mu.Unlock()
				client.Close()
				remote.Close()
				return
			}
			b.conns[client] = true
			b.conns[remote] = true
			b.mu.Unlock()
			defer func() {
				client.Close()
				remote.Close()
				b.mu.Lock()
				delete(b.conns, client)
				delete(b.conns, remote)
				b.mu.Unlock()
			}()
			rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
			if rw.Flush() != nil {
				return
			}
			go func() { io.Copy(remote, rw); remote.Close() }()
			io.Copy(client, remote)
			return
		}
		if r.URL.Scheme != "http" && r.URL.Scheme != "https" {
			http.Error(w, "目标地址无效", 400)
			return
		}
		request := r.Clone(r.Context())
		request.RequestURI = ""
		request.Header.Del("Proxy-Connection")
		resp, err := b.transport.RoundTrip(request)
		if err != nil {
			http.Error(w, "任务代理请求失败", 502)
			return
		}
		defer resp.Body.Close()
		for k, values := range resp.Header {
			for _, v := range values {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	})}
	go b.server.Serve(ln)
	return b, nil
}

type bufferedProxyConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedProxyConn) Read(p []byte) (int, error) { return c.reader.Read(p) }
func dialProxy(ctx context.Context, u *url.URL, target string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 8 * time.Second}
	if u.Scheme == "socks5" {
		d, err := proxy.FromURL(u, dialer)
		if err != nil {
			return nil, errors.New("SOCKS5 配置无效")
		}
		return d.(proxy.ContextDialer).DialContext(ctx, "tcp", target)
	}
	addr := u.Host
	if u.Port() == "" {
		port := "80"
		if u.Scheme == "https" {
			port = "443"
		}
		addr = net.JoinHostPort(u.Hostname(), port)
	}
	var c net.Conn
	var err error
	if u.Scheme == "https" {
		c, err = (&tls.Dialer{NetDialer: dialer, Config: &tls.Config{ServerName: u.Hostname()}}).DialContext(ctx, "tcp", addr)
	} else {
		c, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, errors.New("上游代理连接失败")
	}
	c.SetDeadline(time.Now().Add(10 * time.Second))
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: target}, Host: target, Header: make(http.Header)}
	if u.User != nil {
		pw, _ := u.User.Password()
		req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(u.User.Username()+":"+pw)))
	}
	if err = req.Write(c); err != nil {
		c.Close()
		return nil, errors.New("上游代理握手失败")
	}
	reader := bufio.NewReader(c)
	resp, err := http.ReadResponse(reader, req)
	if err != nil || resp.StatusCode != 200 {
		c.Close()
		return nil, errors.New("上游代理拒绝连接")
	}
	c.SetDeadline(time.Time{})
	return &bufferedProxyConn{Conn: c, reader: reader}, nil
}
