package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

type channelProxyResolver interface {
	ResolveLLMProxySelection(context.Context, *Task, string, string, *int64) (string, error)
}

func TestChannelProxyReferenceDTO(t *testing.T) {
	id := int64(9007199254740993)
	for _, p := range []*db.LLMProfile{{ID: 1, ProxyMode: "fixed", ProxyID: &id}, {ID: 2, ProxyMode: "inherit"}, {ID: 3, Proxy: "http://user:do-not-echo@127.0.0.1:1080"}} {
		data, err := json.Marshal(llmProfileDTO(p))
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		if err := json.Unmarshal(data, &out); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "do-not-echo") || out["proxy_mode"] != p.ProxyMode {
			t.Fatalf("渠道DTO泄露凭据或遗漏mode: %s", data)
		}
		v, present := out["proxy_id"]
		if !present {
			t.Fatalf("proxy_id必须保留null: %s", data)
		}
		if (p.ProxyID == nil && v != nil) || (p.ProxyID != nil && v != "9007199254740993") {
			t.Fatalf("渠道引用精度丢失: %s", data)
		}
	}
}

func TestProxyPGChannelReferencePriority(t *testing.T) {
	s, _ := proxyIntegration(t)
	resolver, ok := any(s.m).(channelProxyResolver)
	if !ok {
		t.Fatal("渠道中心引用解析入口缺失")
	}
	a := createRuntimeProxy(t, s, "http://127.0.0.1:19101")
	b := createRuntimeProxy(t, s, "http://user:synthetic@127.0.0.1:19102")
	if err := s.m.pg.SetGlobalProxyID(&a.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.m.pg.SetGlobalProxyID(nil) })
	task := createRuntimeTask(t, s, db.TaskProxyModeInherit, nil)
	if err := s.m.pg.SetGlobalProxyID(&b.ID); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, taskMode, channelMode, legacy, want string
		id                                        *int64
		task                                      bool
	}{
		{"任务快照", "inherit", "inherit", "http://legacy.invalid:1080", a.URL, nil, true},
		{"渠道中心覆盖快照", "inherit", "fixed", "", b.URL, &b.ID, true},
		{"渠道直连覆盖快照", "inherit", "direct", "", "", nil, true},
		{"任务固定覆盖渠道直连", "fixed", "direct", "", a.URL, nil, true},
		{"任务直连覆盖坏渠道", "direct", "fixed", "", "", nil, true},
		{"非任务继承当前全局", "", "inherit", "", b.URL, nil, false},
		{"兼容旧URL", "inherit", "", "http://legacy.invalid:1080", "http://legacy.invalid:1080", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var selected *Task
			if tc.task {
				copy := *task
				copy.ProxyMode = db.TaskProxyMode(tc.taskMode)
				selected = &copy
			}
			got, err := resolver.ResolveLLMProxySelection(t.Context(), selected, tc.legacy, tc.channelMode, tc.id)
			if err != nil || got != tc.want {
				t.Fatalf("出口优先级错误: got=%q want=%q err=%v", got, tc.want, err)
			}
		})
	}
	missing := int64(9999999999)
	for _, id := range []*int64{nil, &missing} {
		if _, err := resolver.ResolveLLMProxySelection(t.Context(), task, "", "fixed", id); err == nil {
			t.Fatal("失效中心引用静默回退")
		}
	}
	if err := s.m.pg.SetGlobalProxyID(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.m.pg.WithIdleProxy(b.ID, func(p *db.Proxy) error { p.Status = db.ProxyStatusStopped; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.ResolveLLMProxySelection(t.Context(), task, "", "fixed", &b.ID); !errors.Is(err, db.ErrProxyUnavailable) {
		t.Fatalf("停止中心代理未failclosed: %v", err)
	}
	resin, resinTask := lifecycleResin(t, s, nil)
	if _, err := resolver.ResolveLLMProxySelection(t.Context(), nil, "", "fixed", &resin.ID); err == nil || !strings.Contains(err.Error(), "当前不支持") {
		t.Fatalf("渠道Resin缺少独立租约仍被接受: %v", err)
	}
	if got, err := resolver.ResolveLLMProxySelection(t.Context(), resinTask, "", "direct", nil); err != nil || !strings.HasPrefix(got, "http://") {
		t.Fatalf("已有任务fixed Resin能力被渠道裁决改变: %v", err)
	}
}
