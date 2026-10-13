package db

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func TestProxyCreateIntentAtomicReplay(t *testing.T) {
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("需要隔离的本地 PostgreSQL")
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Host != "127.0.0.1:25432" || u.Path != "/artex_proxy_verify_test" || u.RawQuery != "sslmode=disable" {
		t.Fatal("拒绝非本地隔离测试库")
	}
	d, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d2, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	old := proxySecretsPath
	t.Cleanup(func() { proxySecretsPath = old })
	if err := ConfigureProxySecrets(filepath.Join(t.TempDir(), "key")); err != nil {
		t.Fatal(err)
	}
	key := uuid.NewString()
	p := Proxy{Name: "同名入口", Kind: ProxyKindHTTP, URL: "http://user:synthetic-secret@127.0.0.1:8080", Enabled: true}
	var wg sync.WaitGroup
	ids := make(chan int64, 16)
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		store := d
		if i%2 == 1 {
			store = d2
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			copy := p
			out, _, err := store.CreateProxyIntent(t.Context(), key, &copy)
			if err != nil {
				errs <- err
				return
			}
			ids <- out.ID
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var id int64
	for v := range ids {
		if id != 0 && id != v {
			t.Fatal("相同意图创建了多个代理")
		}
		id = v
	}
	defer d.Exec(`DELETE FROM proxy_create_intents WHERE item_key=$1`, key)
	defer d.Exec(`DELETE FROM outbound_proxies WHERE id=$1`, id)
	var payload string
	if err = d.QueryRow(`SELECT payload_cipher FROM proxy_create_intents WHERE item_key=$1`, key).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(payload, "v1:") || strings.Contains(payload, "synthetic-secret") {
		t.Fatal("意图凭据未加密")
	}
	copy := p
	d.SetMaxOpenConns(1)
	copy.URL = "http://user:changed-secret@127.0.0.1:8080"
	if _, _, err = d.CreateProxyIntent(t.Context(), key, &copy); !errors.Is(err, ErrProxyIntentConflict) {
		t.Fatalf("异载荷未拒绝: %v", err)
	}
	// 同名但不同意图必须独立创建。
	key2 := uuid.NewString()
	copy = p
	other, _, err := d.CreateProxyIntent(t.Context(), key2, &copy)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM outbound_proxies WHERE id=$1`, other.ID)
	defer d.Exec(`DELETE FROM proxy_create_intents WHERE item_key=$1`, key2)
	if other.ID == id {
		t.Fatal("错误地按名称去重")
	}
	if _, err = d.Exec(`DELETE FROM outbound_proxies WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	copy = p
	if _, _, err = d.CreateProxyIntent(t.Context(), key, &copy); !errors.Is(err, ErrProxyIntentGone) {
		t.Fatal("已删除意图被重新创建")
	}
	if _, err = d.Exec(`DELETE FROM proxy_create_intents WHERE item_key=$1`, key); err != nil {
		t.Fatal(err)
	}
	// 取消不得留下注册意图或代理。
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	copy = p
	if _, _, err = d.CreateProxyIntent(ctx, uuid.NewString(), &copy); err == nil {
		t.Fatal("已取消请求仍提交")
	}
	// 代理 INSERT 已成功、意图 INSERT 被数据库约束拒绝时，两者必须共同回滚。
	failedKey := uuid.NewString()
	if _, err = d.Exec(fmt.Sprintf(`ALTER TABLE proxy_create_intents ADD CONSTRAINT test_intent_failure CHECK(item_key <> '%s'::uuid)`, failedKey)); err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`ALTER TABLE proxy_create_intents DROP CONSTRAINT IF EXISTS test_intent_failure`)
	copy = p
	copy.Name = "事务回滚-" + failedKey
	if _, _, err = d.CreateProxyIntent(t.Context(), failedKey, &copy); err == nil {
		t.Fatal("未触发意图持久化失败")
	}
	var count int
	if err = d.QueryRow(`SELECT count(*) FROM outbound_proxies WHERE name=$1`, copy.Name).Scan(&count); err != nil || count != 0 {
		t.Fatal("失败事务留下孤立代理")
	}
	if err = d.QueryRow(`SELECT count(*) FROM proxy_create_intents WHERE item_key=$1`, failedKey).Scan(&count); err != nil || count != 0 {
		t.Fatal("失败事务留下意图")
	}
	if _, err = d.Exec(`ALTER TABLE proxy_create_intents DROP CONSTRAINT test_intent_failure`); err != nil {
		t.Fatal(err)
	}
	recovered, _, err := d.CreateProxyIntent(t.Context(), failedKey, &copy)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM outbound_proxies WHERE id=$1`, recovered.ID)
	defer d.Exec(`DELETE FROM proxy_create_intents WHERE item_key=$1`, failedKey)
	// 新建连接池模拟服务重启后仍返回相同映射。
	replayed, wasReplay, err := d2.CreateProxyIntent(t.Context(), failedKey, &copy)
	if err != nil || !wasReplay || replayed.ID != recovered.ID {
		t.Fatal("独立连接池重放失败")
	}
}
