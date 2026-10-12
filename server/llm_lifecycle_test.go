package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/traffic"
	actool "github.com/Autumn-27/norma/tool"
)

func TestLLMProxyReleaseWaitsForRunningTool(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(fmt.Sprintf("终态=%v", terminal), func(t *testing.T) {
			s := newModelExecutionServer(t)
			tr, err := traffic.Open(t.TempDir(), "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			s.m.traffic = tr
			s.m.trafficOn = true
			t.Cleanup(func() { _ = s.m.CloseProxyRuntime(); _ = tr.Close(); s.m.traffic = nil })
			task := executionTask(t, s, nil)
			started, unblock := make(chan struct{}), make(chan struct{})
			var once sync.Once
			t.Cleanup(func() { once.Do(func() { close(unblock) }) })
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-unblock; fmt.Fprint(w, "工具完成") }))
			defer target.Close()
			if !s.engine.beginTaskOperation(task.ID) {
				t.Fatal("未登记工具操作")
			}
			addr, ca, err := s.m.TaskToolProxy(t.Context(), task)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				input, _ := json.Marshal(map[string]any{"url": target.URL})
				res, err := actool.NewWebFetch(actool.WebFetchConfig{Proxy: addr, CACert: ca, StrictProxy: true}).Call(t.Context(), input, nil)
				if err == nil && (res.IsError || !strings.Contains(res.Flatten(), "工具完成")) {
					err = fmt.Errorf("工具失败: %s", res.Flatten())
				}
				s.engine.decInflight(task.ID)
				done <- err
			}()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				once.Do(func() { close(unblock) })
				t.Fatal("真实WebFetch未启动")
			}
			if terminal {
				if err := s.m.SetTaskStatus(task.ID, "done"); err != nil {
					t.Fatal(err)
				}
			} else {
				s.engine.Pause(task.ID, fmt.Errorf("测试暂停"))
			}
			// 模型调用已经结束，工具仍在使用出口，不能依据模型计数提前回收。
			s.engine.EndLLMCall(task.ID)
			rt, _ := s.m.proxyRuntime()
			rt.mu.Lock()
			present := rt.routes[task.ID] != nil
			rt.mu.Unlock()
			if !present {
				once.Do(func() { close(unblock) })
				<-done
				t.Fatal("工具退出前出口已被回收")
			}
			once.Do(func() { close(unblock) })
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("工具未完成")
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				rt.mu.Lock()
				present = rt.routes[task.ID] != nil
				rt.mu.Unlock()
				if !present {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("工具退出后未回收出口")
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}
