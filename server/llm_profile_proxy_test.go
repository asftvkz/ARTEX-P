package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
)

func TestLLMProfileProxySelectionHTTPPersistence(t *testing.T) {
	s := newModelExecutionServer(t)
	proxy := createRuntimeProxy(t, s, "http://channel-user:channel-secret@127.0.0.1:19876")
	api := modelHTTPClient(t, s)
	out := api("POST", "/api/llm/profiles", fmt.Sprintf(`{"name":"channel-proxy-selection","format":"openai","model":"model-a","api_key":"test-key-must-not-echo","proxy_mode":"fixed","proxy_id":%q}`, i64s(proxy.ID)), 200)
	id := int64(out["id"].(float64))
	t.Cleanup(func() { _, _ = s.m.pg.Exec(`DELETE FROM llm_profiles WHERE id=$1`, id) })
	assertSelection := func(mode string, proxyID any) {
		t.Helper()
		rows := api("GET", "/api/llm/profiles", "", 200)["profiles"].([]any)
		for _, row := range rows {
			p := row.(map[string]any)
			if p["id"] == i64s(id) {
				if p["proxy_mode"] != mode || p["proxy_id"] != proxyID {
					t.Fatalf("渠道代理选择未持久化: mode=%v id=%v，期望 %s %v", p["proxy_mode"], p["proxy_id"], mode, proxyID)
				}
				if value, ok := p["proxy"]; ok && value != "" {
					t.Fatalf("中心引用不应回显 URL: %v", value)
				}
				return
			}
		}
		t.Fatal("未返回已创建渠道")
	}
	assertSelection("fixed", i64s(proxy.ID))
	api("POST", "/api/llm/profiles", fmt.Sprintf(`{"id":%d,"name":"renamed","format":"openai"}`, id), 200)
	assertSelection("fixed", i64s(proxy.ID))
	stored, err := s.m.pg.ProfileByID(id)
	if err != nil || stored.Proxy != "" || stored.APIKey != "test-key-must-not-echo" || stored.Model != "model-a" {
		t.Fatalf("普通保存丢失服务端字段或写入中心URL: %v", err)
	}
	for _, mode := range []string{"direct", "inherit"} {
		api("POST", "/api/llm/profiles", fmt.Sprintf(`{"id":%d,"name":"renamed","format":"openai","proxy_mode":%q,"proxy_id":null}`, id, mode), 200)
		assertSelection(mode, nil)
	}
}

func TestLLMProfileProxySelectionRejectsMalformedHTTP(t *testing.T) {
	s := newModelExecutionServer(t)
	api := modelHTTPClient(t, s)
	for _, fields := range []string{
		`"proxy_mode":null`, `"proxy_mode":""`, `"proxy_mode":"unexpected"`,
		`"proxy_mode":"fixed"`, `"proxy_mode":"fixed","proxy_id":null`,
		`"proxy_mode":"fixed","proxy_id":"999999999999999"`,
		`"proxy_mode":"fixed","proxy_id":"1.5"`,
		`"proxy_mode":"direct","proxy_id":"1"`, `"proxy_id":"1"`,
	} {
		body := `{"name":"bad-selection","format":"openai","provider":"openai","model":"a","api_key":"test-key-must-not-echo",` + fields + `}`
		for _, path := range []string{"/api/llm/profiles", "/api/llm/test", "/api/llm/models"} {
			api("POST", path, body, 400)
		}
	}
	// 新增 ID 必须按字符串原样传输，不经 float64。
	var value map[string]json.RawMessage
	if err := json.Unmarshal([]byte(`{"proxy_id":"9007199254740993"}`), &value); err != nil {
		t.Fatal(err)
	}
	api("POST", "/api/llm/profiles", `{"name":"big-id","format":"openai","model":"a","proxy_mode":"fixed","proxy_id":`+string(value["proxy_id"])+`}`, 400)
}

func TestLLMProfileProxyLegacyHTTPPreservesMaskedAndClears(t *testing.T) {
	s := newModelExecutionServer(t)
	api := modelHTTPClient(t, s)
	legacy := "http://legacy-user:legacy-secret@127.0.0.1:19876"
	out := api("POST", "/api/llm/profiles", fmt.Sprintf(`{"name":"legacy-proxy","format":"openai","model":"a","proxy":%q}`, legacy), 200)
	id := int64(out["id"].(float64))
	t.Cleanup(func() { _, _ = s.m.pg.Exec(`DELETE FROM llm_profiles WHERE id=$1`, id) })
	listed := api("GET", "/api/llm/profiles", "", 200)
	raw, _ := json.Marshal(listed)
	if strings.Contains(string(raw), "legacy-secret") {
		t.Fatal("渠道列表泄露旧URL密码")
	}
	for _, fields := range []string{"", fmt.Sprintf(`,"proxy":%q`, maskProxyURL(legacy))} {
		api("POST", "/api/llm/profiles", fmt.Sprintf(`{"id":%d,"name":"legacy-edited","format":"openai"%s}`, id, fields), 200)
		p, err := s.m.pg.ProfileByID(id)
		if err != nil || p.Proxy != legacy || p.ProxyMode != "" || p.ProxyID != nil {
			t.Fatal("普通保存或掩码回传破坏旧URL")
		}
	}
	api("POST", "/api/llm/profiles", fmt.Sprintf(`{"id":%d,"name":"legacy-cleared","format":"openai","proxy":""}`, id), 200)
	p, err := s.m.pg.ProfileByID(id)
	if err != nil || p.Proxy != "" || p.ProxyMode != "inherit" || p.ProxyID != nil {
		t.Fatal("明确清空未恢复继承")
	}
	// 显式新模式优先于旧掩码，不把掩码存回数据库。
	api("POST", "/api/llm/profiles", fmt.Sprintf(`{"id":%d,"name":"legacy-direct","format":"openai","proxy_mode":"direct","proxy":%q}`, id, maskProxyURL(legacy)), 200)
	p, err = s.m.pg.ProfileByID(id)
	if err != nil || p.Proxy != "" || p.ProxyMode != "direct" {
		t.Fatal("显式直连仍保留旧URL")
	}
}

func TestLLMProfileProxyRejectsNewResinAndDisabledReference(t *testing.T) {
	s := newModelExecutionServer(t)
	api := modelHTTPClient(t, s)
	resin, err := s.m.pg.CreateProxy(&db.Proxy{Name: "channel-resin", Kind: db.ProxyKindResin, Host: "localhost", Port: 1080, Password: "private-password", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = s.m.pg.Exec(`DELETE FROM outbound_proxies WHERE id=$1`, resin.ID) })
	disabled := createRuntimeProxy(t, s, "http://localhost:19877")
	disabled.Enabled = false
	if _, err := s.m.pg.UpdateProxy(disabled); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{resin.ID, disabled.ID} {
		body := fmt.Sprintf(`{"name":"unavailable-channel","format":"openai","provider":"openai","model":"a","proxy_mode":"fixed","proxy_id":%q}`, i64s(id))
		for _, path := range []string{"/api/llm/profiles", "/api/llm/test", "/api/llm/models"} {
			api("POST", path, body, 400)
		}
	}
}

func TestLLMProfileProxyReferenceHTTPExecutionAndCache(t *testing.T) {
	s := newModelExecutionServer(t)
	api := modelHTTPClient(t, s)
	var direct, viaA, viaB atomic.Int32
	respond := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/models") {
			fmt.Fprint(w, `{"data":[{"id":"served-through-proxy"}]}`)
			return
		}
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model != "real-a" {
			t.Error("模型调用没有使用真实上游ID")
		}
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { direct.Add(1); respond(w, r) }))
	defer upstream.Close()
	proxyServer := func(counter *atomic.Int32) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			counter.Add(1)
			if !r.URL.IsAbs() || r.Header.Get("Proxy-Authorization") != "Basic dXNlcjpwcml2YXRlLXByb3h5LXNlY3JldA==" {
				t.Error("请求未使用中心代理或丢失中心认证")
			}
			respond(w, r)
		}))
	}
	a, b := proxyServer(&viaA), proxyServer(&viaB)
	defer a.Close()
	defer b.Close()
	center := createRuntimeProxy(t, s, strings.Replace(a.URL, "http://", "http://user:private-proxy-secret@", 1))
	id, _ := executionModels(t, s, upstream.URL)
	profile, err := s.m.pg.ProfileByID(id)
	if err != nil {
		t.Fatal(err)
	}
	profile.ProxyMode, profile.ProxyID = "fixed", &center.ID
	if _, err := s.m.pg.SaveProfile(profile); err != nil {
		t.Fatal(err)
	}
	request := fmt.Sprintf(`{"profile_id":%q}`, i64s(id))
	if out := api("POST", "/api/llm/models", request, 200); out["ok"] != true {
		t.Fatalf("拉模型失败: %v", out)
	}
	if out := api("POST", "/api/llm/test", request, 200); out["ok"] != true {
		t.Fatalf("测试连接失败: %v", out)
	}
	selection := db.LLMModelSelection{ProfileID: id}
	prov, _, ok := s.providerForSelection(selection)
	if !ok {
		t.Fatal("中心引用未解析")
	}
	complete := func(p llm.Provider) {
		t.Helper()
		_, _, _, err := p.Complete(t.Context(), llm.CompletionRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentBlock{{Type: llm.BlockText, Text: "test"}}}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	complete(prov)
	chatA := s.chatAgentForSelection(selection)
	if chatA == nil {
		t.Fatal("对话引用未解析")
	}
	if direct.Load() != 0 || viaA.Load() != 3 {
		t.Fatalf("三条出站未均走中心代理: direct=%d A=%d", direct.Load(), viaA.Load())
	}
	// 草稿明确直连只影响这次拉取，不回写固定引用。
	if out := api("POST", "/api/llm/models", fmt.Sprintf(`{"profile_id":%d,"proxy_mode":"direct","proxy_id":null}`, id), 200); out["ok"] != true {
		t.Fatal("草稿直连拉取失败")
	}
	persisted, err := s.m.pg.ProfileByID(id)
	if err != nil || persisted.ProxyMode != "fixed" || persisted.ProxyID == nil || *persisted.ProxyID != center.ID {
		t.Fatal("临时拉取改变渠道代理选择")
	}
	// 不修改渠道、不手动失效缓存：中心URL更新必须在下一次调用重新解析。
	center.URL = strings.Replace(b.URL, "http://", "http://user:private-proxy-secret@", 1)
	if _, err := s.m.pg.UpdateProxy(center); err != nil {
		t.Fatal(err)
	}
	next, cfg, ok := s.providerForSelection(selection)
	if !ok || next == prov {
		t.Fatal("中心出口更新未刷新provider")
	}
	complete(next)
	if ca := s.chatAgentForSelection(selection); ca == nil || ca == chatA {
		t.Fatal("中心出口更新未刷新ChatAgent")
	}
	// 任务inherit即使已有别的快照，也使用渠道固定引用。
	snapshotSource := *center
	snapshotSource.URL = strings.Replace(a.URL, "http://", "http://user:private-proxy-secret@", 1)
	snapshotA, err := snapshotSource.SnapshotEncrypted()
	if err != nil {
		t.Fatal(err)
	}
	inherited := &Task{ID: "test-inherit", ProxyMode: db.TaskProxyModeInherit, ProxyID: &center.ID, ProxySnapshot: snapshotA}
	taskProvider, _, ok := s.providerForTaskSelection(selection, inherited, t.Context())
	if !ok {
		t.Fatal("任务渠道引用不可用")
	}
	complete(taskProvider)
	// 非任务轮询成员也走同一渠道引用解析入口。
	member := s.poolMember(persisted, 0)
	if member == nil {
		t.Fatal("全局池未生成渠道引用成员")
	}
	complete(member.Prov)
	fixedTask := &Task{ID: "test-fixed", ProxyMode: db.TaskProxyModeFixed, ProxyID: &center.ID, ProxySnapshot: snapshotA}
	fixedProvider, _, ok := s.providerForTaskSelection(selection, fixedTask, t.Context())
	if !ok {
		t.Fatal("任务固定快照不可用")
	}
	complete(fixedProvider)
	// direct任务覆盖渠道固定代理，且不会取环境代理。
	t.Setenv("HTTP_PROXY", a.URL)
	directTask := &Task{ID: "test-direct", ProxyMode: db.TaskProxyModeDirect}
	directProvider, _, ok := s.providerForTaskSelection(selection, directTask, t.Context())
	if !ok {
		t.Fatal("任务直连不可用")
	}
	complete(directProvider)
	if direct.Load() != 2 || viaA.Load() != 4 || viaB.Load() != 3 {
		t.Fatalf("运行优先级错误: direct=%d A=%d B=%d", direct.Load(), viaA.Load(), viaB.Load())
	}
	// 已缓存provider仍不能绕过新停用状态；三条HTTP和运行解析均拒绝。
	center.Enabled = false
	if _, err := s.m.pg.UpdateProxy(center); err != nil {
		t.Fatal(err)
	}
	api("POST", "/api/llm/models", request, 400)
	api("POST", "/api/llm/test", request, 400)
	if _, _, ok := s.providerForSelection(selection); ok {
		t.Fatal("停用引用仍返回缓存provider")
	}
	if _, _, err := s.applyTaskLLMProxy(t.Context(), inherited, next, cfg, id); err == nil {
		t.Fatal("任务代理接线忽略停用引用")
	}
	if direct.Load() != 2 || viaA.Load() != 4 || viaB.Load() != 3 {
		t.Fatal("停用后仍发出请求或静默直连")
	}
	raw, _ := json.Marshal(api("GET", "/api/llm/profiles", "", 200))
	if strings.Contains(string(raw), "private-proxy-secret") {
		t.Fatal("列表泄露中心凭据")
	}
}
