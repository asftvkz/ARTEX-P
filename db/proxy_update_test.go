package db

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestProxySecretUpdatePresence(t *testing.T) {
	old := &Proxy{Name: "p", Kind: ProxyKindHTTP, URL: "http://alice:old@localhost:8080", Username: "alice", Password: "old", Enabled: true, ResinAdminToken: "admin"}
	for _, tc := range []struct {
		name            string
		opts            ProxyUpdateOptions
		password, token string
	}{
		{"省略保留", ProxyUpdateOptions{}, "old", "admin"},
		{"密码覆盖URL", ProxyUpdateOptions{Password: proxyString("new")}, "new", "admin"},
		{"明确清除", ProxyUpdateOptions{ClearPassword: true, ClearAdminToken: true}, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := *old
			if err := mergeProxySecrets(&p, old, tc.opts); err != nil {
				t.Fatal(err)
			}
			if err := ValidateProxy(&p); err != nil {
				t.Fatal(err)
			}
			u, _ := url.Parse(p.URL)
			password, _ := u.User.Password()
			if password != tc.password || p.Password != tc.password || p.ResinAdminToken != tc.token {
				t.Fatal("凭据合并或URL凭据不匹配")
			}
		})
	}
}

func TestProxySecretsInitializationFailsClosed(t *testing.T) {
	old := proxySecretsPath
	t.Cleanup(func() { _ = ConfigureProxySecrets(old) })
	dir := t.TempDir()
	path := filepath.Join(dir, "key")
	if err := os.WriteFile(path, []byte("damaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ConfigureProxySecrets(path); err != nil {
		t.Fatal(err)
	}
	if InitializeProxySecrets() == nil {
		t.Fatal("损坏密钥未阻止初始化")
	}
	if err := ConfigureProxySecrets(filepath.Join(path, "child")); err != nil {
		t.Fatal(err)
	}
	if InitializeProxySecrets() == nil {
		t.Fatal("不可写目录未阻止初始化")
	}
	if err := ConfigureProxySecrets(filepath.Join(dir, "private", "key")); err != nil {
		t.Fatal(err)
	}
	if err := InitializeProxySecrets(); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "private", "key")); err != nil || len(raw) != 32 {
		t.Fatal("初始化没有创建独立AES密钥")
	}
}

func proxyString(v string) *string { return &v }

func TestResinRejectsMaskedCredentials(t *testing.T) {
	for _, p := range []*Proxy{
		{Name: "p", Kind: ProxyKindResin, Host: "localhost", Port: 1080, Password: "***"},
		{Name: "p", Kind: ProxyKindResin, Host: "localhost", Port: 1080, ResinAdminToken: "•••"},
	} {
		if ValidateProxy(p) == nil {
			t.Fatal("掩码不能保存为凭据")
		}
	}
}
