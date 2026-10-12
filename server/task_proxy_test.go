package server

import (
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestResolveTaskProxyPriority(t *testing.T) {
	global := &db.Proxy{ID: 7, Name: "global", Kind: db.ProxyKindResin, Enabled: true, Status: db.ProxyStatusHealthy, URL: "socks5://resin.example:1080"}
	fixed := &db.Proxy{ID: 9, Name: "fixed", Kind: db.ProxyKindWarp, Enabled: true, Status: db.ProxyStatusHealthy, URL: "socks5://127.0.0.1:4009"}

	got, err := ResolveTaskProxy(db.TaskProxyModeFixed, fixed, global)
	if err != nil || got == nil || got.ID != fixed.ID {
		t.Fatalf("fixed proxy must win: got=%+v err=%v", got, err)
	}
	got, err = ResolveTaskProxy(db.TaskProxyModeInherit, nil, global)
	if err != nil || got == nil || got.ID != global.ID {
		t.Fatalf("inherit must use global: got=%+v err=%v", got, err)
	}
	got, err = ResolveTaskProxy(db.TaskProxyModeDirect, fixed, global)
	if err != nil || got != nil {
		t.Fatalf("direct must bypass proxies: got=%+v err=%v", got, err)
	}
}

func TestResolveTaskProxyFailsClosed(t *testing.T) {
	for _, proxy := range []*db.Proxy{
		{ID: 1, Enabled: false, Status: db.ProxyStatusHealthy, URL: "http://127.0.0.1:1"},
		{ID: 2, Enabled: true, Status: db.ProxyStatusFailed, URL: "http://127.0.0.1:2"},
		{ID: 3, Enabled: true, Status: db.ProxyStatusHealthy, URL: ""},
	} {
		if _, err := ResolveTaskProxy(db.TaskProxyModeFixed, proxy, nil); err == nil {
			t.Fatalf("proxy %+v must fail closed", proxy)
		}
	}
	if got, err := ResolveTaskProxy(db.TaskProxyModeInherit, nil, nil); err != nil || got != nil {
		t.Fatalf("legacy inherit without global proxy should remain direct: got=%+v err=%v", got, err)
	}
}

func TestValidateProxyKind(t *testing.T) {
	valid := []db.ProxyKind{db.ProxyKindHTTP, db.ProxyKindHTTPS, db.ProxyKindSOCKS5, db.ProxyKindWarp, db.ProxyKindResin}
	for _, kind := range valid {
		if err := db.ValidateProxyKind(kind); err != nil {
			t.Fatalf("kind %q should be valid: %v", kind, err)
		}
	}
	if err := db.ValidateProxyKind("unknown"); err == nil {
		t.Fatal("unknown proxy kind must be rejected")
	}
}
