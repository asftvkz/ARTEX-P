package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	ErrProxyIntentConflict = errors.New("代理创建意图与原请求冲突")
	ErrProxyIntentGone     = errors.New("代理创建意图对应的代理已删除")
)

// 意图永久保留；删除代理后保留空引用，防止旧会话重放重新创建。
func (d *DB) MigrateProxyCreateIntents(ctx context.Context) error {
	_, err := d.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS proxy_create_intents (
 item_key uuid PRIMARY KEY,
 payload_cipher text NOT NULL CHECK (payload_cipher LIKE 'v1:%'),
 proxy_id bigint REFERENCES outbound_proxies(id) ON DELETE SET NULL,
 created_at timestamptz NOT NULL DEFAULT now()
 ); CREATE INDEX IF NOT EXISTS proxy_create_intents_proxy_idx ON proxy_create_intents(proxy_id);`)
	return err
}

// CreateProxyIntent 只登记已有入口，不访问供应商，也不创建 WARP。
// 白名单规范载荷使用现有代理 AES-GCM 密钥加密，避免无密钥摘要遭离线猜密。
// 不能直接序列化 Proxy：其密码字段有 json:"-"，会漏掉凭据冲突。
func (d *DB) CreateProxyIntent(ctx context.Context, key string, p *Proxy) (*Proxy, bool, error) {
	parsed, err := uuid.Parse(key)
	if err != nil || parsed == uuid.Nil || parsed.String() != key {
		return nil, false, errors.New("item_key 必须为规范 UUID")
	}
	if p == nil || p.Kind == ProxyKindWarp {
		return nil, false, ErrProxyInvalidKind
	}
	copy := *p
	if err = ValidateProxy(&copy); err != nil {
		return nil, false, err
	}
	payload, err := json.Marshal(struct {
		Version                                            int
		Name                                               string
		Kind                                               ProxyKind
		Protocol, URL, Host                                string
		Port                                               int
		Username, Password, Platform, AdminURL, AdminToken string
		Enabled                                            bool
	}{1, copy.Name, copy.Kind, copy.Protocol, copy.URL, copy.Host, copy.Port, copy.Username, copy.Password, copy.ResinPlatform, copy.ResinAdminURL, copy.ResinAdminToken, copy.Enabled})
	if err != nil {
		return nil, false, err
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, proxyLifecycleLock); err != nil {
		return nil, false, err
	}
	var previous string
	var id sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT payload_cipher,proxy_id FROM proxy_create_intents WHERE item_key=$1`, key).Scan(&previous, &id)
	if err == nil {
		plain, err := decryptProxySecret(previous)
		if err != nil {
			return nil, false, err
		}
		if plain != string(payload) {
			return nil, false, ErrProxyIntentConflict
		}
		if !id.Valid {
			return nil, false, ErrProxyIntentGone
		}
		out, err := getProxyTx(tx, id.Int64)
		if err != nil {
			return nil, false, err
		}
		return out, true, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	cipher, err := encryptProxySecret(string(payload))
	if err != nil {
		return nil, false, err
	}
	out, err := insertProxyTx(tx, &copy)
	if err != nil {
		return nil, false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO proxy_create_intents(item_key,payload_cipher,proxy_id) VALUES($1,$2,$3)`, key, cipher, out.ID); err != nil {
		return nil, false, err
	}
	if err = tx.Commit(); err != nil {
		return nil, false, err
	}
	return out, false, nil
}

// ListProxyPage 在 SQL 层限制数量，工具不会先解密整个代理库。
func (d *DB) ListProxyPage(ctx context.Context, after int64, limit int) ([]*Proxy, error) {
	if after < 0 || limit < 1 || limit > 100 {
		return nil, errors.New("分页参数无效")
	}
	rows, err := d.QueryContext(ctx, `SELECT id,name,kind,url,host,port,username,password,resin_platform,resin_admin_url,resin_admin_token,enabled,status,last_error,last_checked_at,created_at,updated_at,protocol FROM outbound_proxies WHERE id>$1 ORDER BY id LIMIT $2`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*Proxy, 0, limit)
	for rows.Next() {
		p, err := scanProxy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (d *DB) GetProxyContext(ctx context.Context, id int64) (*Proxy, error) {
	return scanProxy(d.QueryRowContext(ctx, `SELECT id,name,kind,url,host,port,username,password,resin_platform,resin_admin_url,resin_admin_token,enabled,status,last_error,last_checked_at,created_at,updated_at,protocol FROM outbound_proxies WHERE id=$1`, id))
}

// SaveProxyProbe 拒绝覆盖检测期间被编辑、删除或再次检测的档案。
func (d *DB) SaveProxyProbe(ctx context.Context, p *Proxy, status, message string, checked time.Time) error {
	result, err := d.ExecContext(ctx, `UPDATE outbound_proxies SET status=$2,last_error=$3,last_checked_at=$4 WHERE id=$1 AND updated_at=$5 AND last_checked_at IS NOT DISTINCT FROM $6`, p.ID, status, message, checked, p.UpdatedAt, p.LastCheckedAt)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return ErrProxyInUse
	}
	return err
}
