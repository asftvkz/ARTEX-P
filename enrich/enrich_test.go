package enrich

import (
	"context"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestProbeFailsClosed(t *testing.T) {
	t.Setenv("NO_PROXY", "*")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:2")
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer target.Close()
	for _, tc := range []struct {
		name, proxy string
		err         error
	}{
		{"unavailable", "", errors.New("no route")},
		{"invalid", "bad://secret@127.0.0.1", nil},
		{"offline", "http://127.0.0.1:1", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := New(nil, func(context.Context, int64) (Route, error) { return Route{ProxyURL: tc.proxy}, tc.err }, 1)
			defer e.Close()
			scope := e.ForTask(t.Context(), 1)
			scope.ProbeSite(1, target.URL)
			scope.Close()
			if hits.Load() != 0 {
				t.Fatal("代理失败后直连目标")
			}
		})
	}
}

func TestSOCKS5ProbeResolvesTargetAtProxy(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmtBody := "via-warp-socks"; io.WriteString(w, fmtBody) }))
	defer target.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	result := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			result <- "accept failed"
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		head := make([]byte, 2)
		if _, err = io.ReadFull(conn, head); err != nil {
			result <- "greeting failed"
			return
		}
		methods := make([]byte, int(head[1]))
		io.ReadFull(conn, methods)
		conn.Write([]byte{5, 0})
		command := make([]byte, 4)
		if _, err = io.ReadFull(conn, command); err != nil || command[3] != 3 {
			result <- "target was locally resolved"
			return
		}
		size := make([]byte, 1)
		io.ReadFull(conn, size)
		domain := make([]byte, int(size[0]))
		io.ReadFull(conn, domain)
		port := make([]byte, 2)
		io.ReadFull(conn, port)
		if string(domain) != "warp-probe.invalid" || binary.BigEndian.Uint16(port) != 80 {
			result <- "wrong target"
			return
		}
		upstream, err := net.Dial("tcp", target.Listener.Addr().String())
		if err != nil {
			result <- "upstream failed"
			return
		}
		defer upstream.Close()
		conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 80})
		finished := make(chan struct{})
		go func() { io.Copy(upstream, conn); upstream.Close(); close(finished) }()
		io.Copy(conn, upstream)
		conn.Close()
		<-finished
		result <- "ok"
	}()
	client, err := buildClient(Route{ProxyURL: "socks5://" + listener.Addr().String()}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	response, err := client.Get("http://warp-probe.invalid")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || string(body) != "via-warp-socks" {
		t.Fatal("SOCKS 未返回目标响应", err)
	}
	select {
	case got := <-result:
		if got != "ok" {
			t.Fatal(got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SOCKS 请求未退出")
	}
}

func TestDNSRejectsMismatchedReplyAndDoesNotFallback(t *testing.T) {
	var direct atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { direct.Add(1) }))
	defer target.Close()
	for _, mode := range []string{"offline", "wrong-question", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "redirect" {
					http.Redirect(w, r, target.URL, 302)
					return
				}
				raw, _ := io.ReadAll(r.Body)
				q := new(dns.Msg)
				q.Unpack(raw)
				answer := new(dns.Msg)
				answer.SetReply(q)
				answer.Question[0].Name = "unrelated.invalid."
				packed, _ := answer.Pack()
				w.Header().Set("Content-Type", "application/dns-message")
				w.Write(packed)
			}))
			defer upstream.Close()
			route := upstream.URL
			if mode == "offline" {
				upstream.Close()
			}
			client, err := buildClient(Route{ProxyURL: route}, true)
			if err != nil {
				t.Fatal(err)
			}
			defer client.CloseIdleConnections()
			if _, err := queryDNS(t.Context(), client, target.URL, "example.invalid"); err == nil {
				t.Fatal("异常 DNS 响应被接受")
			}
			if direct.Load() != 0 {
				t.Fatal("DNS 失败或重定向造成直连")
			}
		})
	}
}

func TestProbeTaskIsolationAndScopeDrain(t *testing.T) {
	entered := make(chan int64, 4)
	release := make(chan struct{})
	var a, b atomic.Int32
	proxy := func(id int64, n *atomic.Int32) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n.Add(1)
			entered <- id
			<-release
			w.WriteHeader(502)
		}))
	}
	pa, pb := proxy(1, &a), proxy(2, &b)
	defer pa.Close()
	defer pb.Close()
	defer close(release)
	e := New(nil, func(ctx context.Context, id int64) (Route, error) {
		if id == 1 {
			return Route{ProxyURL: pa.URL}, nil
		}
		return Route{ProxyURL: pb.URL}, nil
	}, 2)
	defer e.Close()
	sa, sb := e.ForTask(t.Context(), 1), e.ForTask(t.Context(), 2)
	sa.ProbeSite(9, "http://asset.invalid")
	sb.ProbeSite(9, "http://asset.invalid")
	for range 2 {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("两个任务没有各用自己的出口")
		}
	}
	done := make(chan struct{})
	go func() { sa.Close(); sb.Close(); close(done) }()
	select {
	case <-done:
		t.Fatal("网络未退出就结束 scope")
	case <-time.After(30 * time.Millisecond):
	}
	e.Close() // 关闭引擎取消在途网络，并排空队列。
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("关闭未释放 scope")
	}
	if a.Load() != 1 || b.Load() != 1 {
		t.Fatal("不同任务被同资产冷却合并")
	}
	sa.ProbeSite(10, "http://asset.invalid")
}

func TestDNSUsesProxyAndValidatesTLS(t *testing.T) {
	var queries, connects atomic.Int32
	resolver := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/dns-message" {
			t.Error("DNS 请求格式错误")
		}
		raw, _ := io.ReadAll(r.Body)
		q := new(dns.Msg)
		if q.Unpack(raw) != nil {
			w.WriteHeader(400)
			return
		}
		queries.Add(1)
		answer := new(dns.Msg)
		answer.SetReply(q)
		h := dns.RR_Header{Name: q.Question[0].Name, Rrtype: q.Question[0].Qtype, Class: dns.ClassINET, Ttl: 60}
		switch q.Question[0].Qtype {
		case dns.TypeA:
			answer.Answer = []dns.RR{&dns.A{Hdr: h, A: net.ParseIP("192.0.2.8")}}
		case dns.TypeAAAA:
			answer.Answer = []dns.RR{&dns.AAAA{Hdr: h, AAAA: net.ParseIP("2001:db8::8")}}
		case dns.TypeCNAME:
			answer.Answer = []dns.RR{&dns.CNAME{Hdr: h, Target: "edge.example."}}
		}
		packed, _ := answer.Pack()
		w.Header().Set("Content-Type", "application/dns-message")
		w.Write(packed)
	}))
	defer resolver.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			w.WriteHeader(400)
			return
		}
		connects.Add(1)
		up, err := net.Dial("tcp", resolver.Listener.Addr().String())
		if err != nil {
			w.WriteHeader(502)
			return
		}
		downstream, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			up.Close()
			return
		}
		defer up.Close()
		defer downstream.Close()
		io.WriteString(downstream, "HTTP/1.1 200 Connection Established\r\n\r\n")
		finished := make(chan struct{})
		go func() { io.Copy(up, downstream); up.Close(); close(finished) }()
		io.Copy(downstream, up)
		downstream.Close()
		<-finished
	}))
	defer proxy.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: resolver.Certificate().Raw}), 0600)
	client, err := buildClient(Route{ProxyURL: proxy.URL, CACertPath: ca}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	records, err := queryDNS(t.Context(), client, resolver.URL+"/dns-query", "probe.example")
	if err != nil {
		t.Fatal(err)
	}
	if connects.Load() != 3 || queries.Load() != 3 || len(records.A) != 1 || len(records.AAAA) != 1 || len(records.CNAME) != 1 {
		t.Fatalf("DNS 未完整经代理: %+v queries=%d connects=%d", records, queries.Load(), connects.Load())
	}
	bad, _ := buildClient(Route{ProxyURL: proxy.URL}, true)
	defer bad.CloseIdleConnections()
	if _, err := queryDNS(t.Context(), bad, resolver.URL, "probe.example"); err == nil {
		t.Fatal("DNS 接受了未信任证书")
	}
	if queries.Load() != 3 {
		t.Fatal("TLS 校验失效")
	}
}

func TestCancelledAndClosedScopesDoNotSend(t *testing.T) {
	var calls atomic.Int32
	e := New(nil, func(context.Context, int64) (Route, error) { calls.Add(1); return Route{}, errors.New("closed") }, 1)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	s := e.ForTask(ctx, 1)
	s.ResolveDomain(1, "example.invalid")
	s.ProbeSite(1, "http://example.invalid")
	s.Close()
	e.Close()
	s = e.ForTask(t.Context(), 1)
	s.ResolveDomain(2, "example.invalid")
	s.Close()
	if calls.Load() != 0 {
		t.Fatal("已取消或已关闭仍访问出口")
	}
}
