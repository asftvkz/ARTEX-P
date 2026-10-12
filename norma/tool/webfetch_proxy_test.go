package tool

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestWebFetchStrictProxyNeverRetriesDirect(t *testing.T) {
	var hits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); fmt.Fprint(w, "direct") }))
	defer target.Close()
	input, _ := json.Marshal(map[string]any{"url": target.URL})
	for _, addr := range []string{"http://127.0.0.1:1", "://bad"} {
		res, err := webFetchRunner(WebFetchConfig{Proxy: addr, StrictProxy: true})(t.Context(), input, nil)
		if err == nil && !res.IsError {
			t.Fatal("故障或无效代理未失败")
		}
	}
	if hits.Load() != 0 {
		t.Fatal("严格代理泄漏直连")
	}
	p := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "proxy-egress") }))
	defer p.Close()
	t.Setenv("NO_PROXY", "*")
	res, err := webFetchRunner(WebFetchConfig{Proxy: p.URL, StrictProxy: true})(t.Context(), input, nil)
	if err != nil || !strings.Contains(res.Flatten(), "proxy-egress") || hits.Load() != 0 {
		t.Fatal("NO_PROXY 绕过了指定出口")
	}
}
