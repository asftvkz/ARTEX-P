package db

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	ErrWarpCreateConflict = errors.New("WARP 创建键对应不同请求")
	ErrWarpCreateGone     = errors.New("WARP 创建键对应的代理已删除")
)

type WarpCreateIntent struct {
	ProxyID        int64
	State          string // pending | succeeded | failed
	LastError      string
	Created        bool
	ExecutionToken string
}

// 意图不自动过期：异常退出或删除后也不能让相同 key 再次注册。
func (d *DB) MigrateWarpCreateRequests(ctx context.Context) error {
	_, err := d.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS warp_proxy_create_requests (
request_key uuid PRIMARY KEY,
payload_hash bytea NOT NULL CHECK (octet_length(payload_hash)=32),
proxy_id bigint REFERENCES outbound_proxies(id) ON DELETE SET NULL,
state text NOT NULL CHECK (state IN ('pending','succeeded','failed','deleted')),
execution_token uuid,
last_error text NOT NULL DEFAULT '',
created_at timestamptz NOT NULL DEFAULT now(),
updated_at timestamptz NOT NULL DEFAULT now(),
CONSTRAINT warp_proxy_create_requests_live CHECK (state='deleted' OR proxy_id IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS warp_proxy_create_requests_proxy_idx ON warp_proxy_create_requests(proxy_id);
ALTER TABLE warp_proxy_create_requests ADD COLUMN IF NOT EXISTS execution_token uuid;`)
	return err
}

// CreateWarpProxyOnce 与常规代理创建共锁；事务提交后调用方才可进行外部注册。
func (d *DB) CreateWarpProxyOnce(key string, hash [32]byte, p *Proxy) (*Proxy, WarpCreateIntent, error) {
	var intent WarpCreateIntent
	if p == nil || p.Kind != ProxyKindWarp {
		return nil, intent, ErrProxyInvalidKind
	}
	if err := ValidateProxy(p); err != nil {
		return nil, intent, err
	}
	var out *Proxy
	err := d.withProxyLifecycle(func(tx *sql.Tx) error {
		var previous []byte
		var id sql.NullInt64
		err := tx.QueryRow(`SELECT payload_hash,proxy_id,state,last_error FROM warp_proxy_create_requests WHERE request_key=$1`, key).Scan(&previous, &id, &intent.State, &intent.LastError)
		if err == nil {
			if !bytes.Equal(previous, hash[:]) {
				return ErrWarpCreateConflict
			}
			if intent.State == "deleted" || !id.Valid {
				return ErrWarpCreateGone
			}
			intent.ProxyID = id.Int64
			out, err = getProxyTx(tx, intent.ProxyID)
			if errors.Is(err, ErrProxyUnavailable) {
				return ErrWarpCreateGone
			}
			return err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		out, err = insertProxyTx(tx, p)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE outbound_proxies SET status=$2 WHERE id=$1`, out.ID, ProxyStatusStarting); err != nil {
			return err
		}
		out.Status = ProxyStatusStarting
		token, err := uuid.NewRandom()
		if err != nil {
			return err
		}
		intent = WarpCreateIntent{ProxyID: out.ID, State: "pending", Created: true, ExecutionToken: token.String()}
		_, err = tx.Exec(`INSERT INTO warp_proxy_create_requests(request_key,payload_hash,proxy_id,state,execution_token) VALUES($1,$2,$3,'pending',$4)`, key, hash[:], out.ID, token.String())
		return err
	})
	return out, intent, err
}

// CompleteWarpProxyCreate 原子提交运行结果与代理状态；pending 遗留绝不触发重注册。
func (d *DB) CompleteWarpProxyCreate(key string, id int64, token string, connection *Proxy, failure string) (*Proxy, error) {
	var out *Proxy
	err := d.withProxyLifecycle(func(tx *sql.Tx) error {
		var state string
		var bound sql.NullInt64
		var executionToken sql.NullString
		if err := tx.QueryRow(`SELECT state,proxy_id,execution_token FROM warp_proxy_create_requests WHERE request_key=$1 FOR UPDATE`, key).Scan(&state, &bound, &executionToken); err != nil {
			return err
		}
		if state != "pending" || !bound.Valid || bound.Int64 != id || !executionToken.Valid || token == "" || executionToken.String != token {
			return ErrWarpCreateConflict
		}
		p, err := getProxyTx(tx, id)
		if err != nil {
			return err
		}
		status, nextState := "failed", "failed"
		if connection != nil {
			if connection.URL == "" || connection.Host == "" || connection.Port <= 0 || connection.Password == "" {
				return ErrProxyUnavailable
			}
			p.URL, p.Host, p.Port, p.Username, p.Password, p.Protocol = connection.URL, connection.Host, connection.Port, connection.Username, connection.Password, "socks5"
			status, nextState, failure = "healthy", "succeeded", ""
		}
		u, err := encryptProxySecret(p.URL)
		if err != nil {
			return err
		}
		pw, err := encryptProxySecret(p.Password)
		if err != nil {
			return err
		}
		now := time.Now()
		out, err = scanProxy(tx.QueryRow(`UPDATE outbound_proxies SET url=$2,host=$3,port=$4,username=$5,password=$6,protocol=$7,status=$8,last_error=$9,last_checked_at=$10 WHERE id=$1
RETURNING id,name,kind,url,host,port,username,password,resin_platform,resin_admin_url,resin_admin_token,enabled,status,last_error,last_checked_at,created_at,updated_at,protocol`, id, u, p.Host, p.Port, p.Username, pw, p.Protocol, status, failure, now))
		if err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE warp_proxy_create_requests SET state=$2,last_error=$3,updated_at=now() WHERE request_key=$1`, key, nextState, failure)
		return err
	})
	return out, err
}

func warpCreatePendingTx(tx *sql.Tx, id int64) (bool, error) {
	var pending bool
	err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM warp_proxy_create_requests WHERE proxy_id=$1 AND state='pending')`, id).Scan(&pending)
	return pending, err
}

func (d *DB) WarpCreatePending(id int64) (bool, error) {
	var pending bool
	err := d.QueryRow(`SELECT EXISTS(SELECT 1 FROM warp_proxy_create_requests WHERE proxy_id=$1 AND state='pending')`, id).Scan(&pending)
	return pending, err
}
