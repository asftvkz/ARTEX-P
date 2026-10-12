// Package egress 管理出站代理运行器；控制面与任务数据面相互独立。
package egress

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type ResinConfig struct {
	AdminURL   string
	AdminToken string `json:"-"`
	Platform   string
}

func (c ResinConfig) client() (*http.Client, *url.URL, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(c.AdminURL), "/") + "/")
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, nil, errors.New("Resin 管理地址必须是无凭据、查询和片段的 HTTP(S) 地址")
	}
	tr := &http.Transport{Proxy: nil, ResponseHeaderTimeout: 8 * time.Second}
	return &http.Client{Transport: tr, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, u, nil
}

func (c ResinConfig) request(ctx context.Context, client *http.Client, base *url.URL, method, path string, auth bool, target any) (int, error) {
	ref, err := url.Parse(path)
	if err != nil {
		return 0, errors.New("Resin 控制面路径无效")
	}
	req, err := http.NewRequestWithContext(ctx, method, base.ResolveReference(ref).String(), nil)
	if err != nil {
		return 0, errors.New("Resin 控制面请求无效")
	}
	if auth && c.AdminToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.AdminToken)
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, errors.New("Resin 控制面连接失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("Resin 控制面返回 HTTP %d", resp.StatusCode)
	}
	if target != nil {
		body, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
		if err != nil || len(body) > 1<<20 {
			return resp.StatusCode, errors.New("Resin 控制面响应读取失败或超过 1 MiB")
		}
		if json.Unmarshal(body, target) != nil {
			return resp.StatusCode, errors.New("Resin 控制面响应格式无效")
		}
	}
	return resp.StatusCode, nil
}

func (c ResinConfig) platformID(ctx context.Context, client *http.Client, base *url.URL) (string, error) {
	name := strings.TrimSpace(c.Platform)
	if name == "" {
		name = "Default"
	}
	for page := 0; page < 100; page++ {
		var result struct {
			Items []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"items"`
			Total *int `json:"total"`
		}
		path := fmt.Sprintf("api/v1/platforms?keyword=%s&limit=100&offset=%d", url.QueryEscape(name), page*100)
		if _, err := c.request(ctx, client, base, http.MethodGet, path, true, &result); err != nil {
			return "", err
		}
		if result.Items == nil {
			return "", errors.New("Resin 平台查询缺少 items 数组")
		}
		for _, item := range result.Items {
			if strings.EqualFold(item.Name, name) && item.ID != "" {
				return item.ID, nil
			}
		}
		if len(result.Items) < 100 || (result.Total != nil && (page*100+len(result.Items)) >= *result.Total) {
			return "", nil
		}
	}
	return "", errors.New("Resin 平台查询超过 10000 条上限")
}

// Probe 检查外部 Resin 控制面；未配置控制面时由调用方独立验证数据面。
func (c ResinConfig) Probe(ctx context.Context) error {
	if strings.TrimSpace(c.AdminURL) == "" {
		return nil
	}
	client, base, err := c.client()
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	if _, err = c.request(ctx, client, base, http.MethodGet, "healthz", false, nil); err != nil {
		return err
	}
	var info struct {
		AuthVersion string `json:"auth_version"`
	}
	if _, err = c.request(ctx, client, base, http.MethodGet, "api/v1/system/info", true, &info); err != nil {
		return err
	}
	if info.AuthVersion != "" && !strings.EqualFold(info.AuthVersion, "V1") {
		return errors.New("Resin 账号粘性接入要求 RESIN_AUTH_VERSION=V1")
	}
	id, err := c.platformID(ctx, client, base)
	if err != nil {
		return err
	}
	if id == "" {
		return errors.New("Resin 平台不存在")
	}
	return nil
}

// Release 回收指定粘性身份，平台或租约不存在时幂等成功。
// 未配置管理地址或令牌时返回错误，避免把未回收误报为成功。
func (c ResinConfig) Release(ctx context.Context, accountKey string) error {
	if strings.TrimSpace(accountKey) == "" {
		return errors.New("Resin 账号标识不能为空")
	}
	if c.AdminURL == "" || c.AdminToken == "" {
		return errors.New("回收 Resin 租约需要管理地址和管理令牌")
	}
	client, base, err := c.client()
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	id, err := c.platformID(ctx, client, base)
	if err != nil {
		return err
	}
	if id == "" {
		return nil
	}
	status, err := c.request(ctx, client, base, http.MethodDelete, "api/v1/platforms/"+url.PathEscape(id)+"/leases/"+url.PathEscape(accountKey), true, nil)
	if status == http.StatusNotFound {
		return nil
	}
	return err
}
