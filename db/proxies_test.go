package db

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProxySnapshotJSONEncryptsAllSecrets(t *testing.T) {
	old := proxySecretsPath
	t.Cleanup(func() { proxySecretsPath = old })
	proxySecretsPath = filepath.Join(t.TempDir(), "proxy-secrets.key")
	s := ProxySnapshot{ID: 4, Kind: ProxyKindHTTPS, URL: "https://user:pass@example.test", Password: "password-secret", AdminToken: "admin-token"}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, secret := range []string{"user:pass", "password-secret", "admin-token"} {
		if strings.Contains(text, secret) {
			t.Fatalf("snapshot JSON leaked %q: %s", secret, text)
		}
	}
	if !strings.Contains(text, "v1:") {
		t.Fatalf("snapshot JSON did not contain ciphertext: %s", text)
	}
	var got ProxySnapshot
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.URL == s.URL || got.Password == s.Password || got.AdminToken == s.AdminToken {
		t.Fatal("raw snapshot unexpectedly decrypted")
	}
	got, err = (&DB{}).ResolveProxySnapshot(got)
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != s.URL || got.Password != s.Password || got.AdminToken != s.AdminToken {
		t.Fatalf("round trip mismatch: got=%+v want=%+v", got, s)
	}
}

func TestProxySnapshotJSONRejectsMalformedCiphertext(t *testing.T) {
	old := proxySecretsPath
	t.Cleanup(func() { proxySecretsPath = old })
	proxySecretsPath = filepath.Join(t.TempDir(), "proxy-secrets.key")
	var got ProxySnapshot
	if err := json.Unmarshal([]byte(`{"url":"v1:zz"}`), &got); err == nil {
		t.Fatal("malformed ciphertext must fail closed")
	}
	if err := json.Unmarshal([]byte(`{"url":"v1:00"}`), &got); err != nil {
		t.Fatal(err)
	}
	if _, err := (&DB{}).ResolveProxySnapshot(got); err == nil {
		t.Fatal("truncated ciphertext must fail closed")
	}
}

func TestConfigureProxySecretsDoesNotCreateUntilUsed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "proxy.key")
	if err := ConfigureProxySecrets(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("ConfigureProxySecrets unexpectedly wrote key: %v", err)
	}
}
