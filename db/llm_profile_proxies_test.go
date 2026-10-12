package db

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestLLMProfileProxyPersistenceAndOmission(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := ConfigureProxySecrets(filepath.Join(t.TempDir(), "proxy.key")); err != nil {
		t.Fatal(err)
	}
	proxy, err := d.CreateProxy(&Proxy{Name: "channel-proxy", Kind: ProxyKindHTTP, URL: "http://user:secret@localhost:19876", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM outbound_proxies WHERE id=$1`, proxy.ID) })
	var p LLMProfile
	if err := json.Unmarshal([]byte(fmt.Sprintf(`{"name":%q,"format":"openai","model":"a","proxy_mode":"fixed","proxy_id":%d}`, fmt.Sprintf("proxy-model-%d", time.Now().UnixNano()), proxy.ID)), &p); err != nil {
		t.Fatal(err)
	}
	p.APIKey = "stored-key"
	id, err := d.SaveProfile(&p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM llm_profiles WHERE id=$1`, id) })
	assertFixed := func() {
		t.Helper()
		loaded, err := d.ProfileByID(id)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(loaded)
		var fields map[string]any
		_ = json.Unmarshal(raw, &fields)
		if fields["proxy_mode"] != "fixed" || fields["proxy_id"] != float64(proxy.ID) || loaded.Proxy != "" {
			t.Fatal("渠道中心代理引用未保存，或中心URL被写入渠道")
		}
	}
	assertFixed()
	if err := d.MigrateLLMProfileProxies(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertFixed()
	if _, err := d.SaveProfile(&LLMProfile{ID: id, Name: "renamed", Format: "openai"}); err != nil {
		t.Fatal(err)
	}
	assertFixed()
	if _, err := d.Exec(`DELETE FROM outbound_proxies WHERE id=$1`, proxy.ID); err == nil {
		t.Fatal("固定渠道引用未阻止删除代理")
	}
}

func TestLLMProfileProxyLegacyOmissionPreservesCredentials(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	id, err := d.SaveProfile(&LLMProfile{Name: fmt.Sprintf("legacy-proxy-%d", time.Now().UnixNano()), Format: "openai", Model: "a", Proxy: "http://user:secret@localhost:19876"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM llm_profiles WHERE id=$1`, id) })
	if _, err := d.SaveProfile(&LLMProfile{ID: id, Name: "renamed", Format: "openai"}); err != nil {
		t.Fatal(err)
	}
	p, err := d.ProfileByID(id)
	if err != nil || p.Proxy != "http://user:secret@localhost:19876" {
		t.Fatal("普通保存省略代理字段时丢失旧URL")
	}
}

func TestMigrateLLMProfileProxiesPreservesLegacyURL(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	var id int64
	if err := d.QueryRow(`INSERT INTO llm_profiles(name,format,model,proxy) VALUES($1,'openai','legacy-model','http://legacy:secret@localhost:19876') RETURNING id`, fmt.Sprintf("legacy-channel-proxy-%d", time.Now().UnixNano())).Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM llm_profiles WHERE id=$1`, id) })
	for i := 0; i < 2; i++ {
		if err := d.MigrateLLMProfileProxies(t.Context()); err != nil {
			t.Fatal(err)
		}
		p, err := d.ProfileByID(id)
		if err != nil || p.ProxyMode != "" || p.ProxyID != nil || p.Proxy != "http://legacy:secret@localhost:19876" {
			t.Fatal("重复迁移更改旧渠道代理")
		}
	}
}
