package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/enrich"
	"github.com/Autumn-27/artex/traffic"
	"github.com/Autumn-27/norma/llm"
)

func TestProxyPGAssetProbeTaskRoutes(t *testing.T) {
	for _, capture := range []bool{false, true} {
		t.Run(fmt.Sprint(capture), func(t *testing.T) {
			s, _ := proxyIntegration(t)
			s.engine = NewEngine(s.m)
			if capture {
				tr, err := traffic.Open(t.TempDir(), "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				s.m.traffic = tr
				s.m.trafficOn = true
				t.Cleanup(func() { s.m.CloseProxyRuntime(); tr.Close() })
			}
			var hitsA, hitsB, direct atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { direct.Add(1); fmt.Fprint(w, "<title>direct</title>") }))
			defer target.Close()
			makeProxy := func(label string, hits *atomic.Int32) *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodGet {
						hits.Add(1)
					}
					fmt.Fprintf(w, "<title>%s</title>", label)
				}))
			}
			pa, pb := makeProxy("A", &hitsA), makeProxy("B", &hitsB)
			defer pa.Close()
			defer pb.Close()
			a, b := createRuntimeProxy(t, s, pa.URL), createRuntimeProxy(t, s, pb.URL)
			if err := s.m.pg.SetGlobalProxyID(&a.ID); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.m.pg.SetGlobalProxyID(nil) })
			inherited := createRuntimeTask(t, s, db.TaskProxyModeInherit, nil)
			if err := s.m.pg.SetGlobalProxyID(&b.ID); err != nil {
				t.Fatal(err)
			}
			fixed := createRuntimeTask(t, s, db.TaskProxyModeFixed, &b.ID)
			directTask := createRuntimeTask(t, s, db.TaskProxyModeDirect, nil)
			engine := enrich.New(s.m.pg.Assets(), s.m.taskEnrichRoute, 2)
			defer engine.Close()
			for i, pair := range []struct {
				task  *Task
				title string
			}{{inherited, "A"}, {fixed, "B"}, {directTask, "direct"}} {
				id, _ := strconv.ParseInt(pair.task.ID, 10, 64)
				ctx, finish, ok := s.engine.beginTaskOperationContext(t.Context(), pair.task.ID)
				if !ok {
					t.Fatal("未准入")
				}
				func() {
					defer finish()
					url := fmt.Sprintf("%s/probe-%d", target.URL, i)
					asset, err := s.m.pg.Assets().UpsertHTTPService(db.UpsertHTTPServiceReq{URL: url, TaskID: id})
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { s.m.pg.Exec(`DELETE FROM assets WHERE id=$1`, asset) })
					scope := engine.ForTask(ctx, id)
					scope.ProbeSite(asset, url)
					scope.Close()
					var title string
					if err := s.m.pg.QueryRow(`SELECT COALESCE(page_title,'') FROM assets WHERE id=$1`, asset).Scan(&title); err != nil || title != pair.title {
						t.Fatalf("任务出口错误: title=%q want=%q err=%v", title, pair.title, err)
					}
				}()
			}
			if hitsA.Load() != 1 || hitsB.Load() != 1 || direct.Load() != 1 {
				t.Fatalf("出口计数: A=%d B=%d direct=%d", hitsA.Load(), hitsB.Load(), direct.Load())
			}
			if capture {
				var n int
				s.m.traffic.DB().QueryRow(`SELECT count(*) FROM exchanges`).Scan(&n)
				if n < 3 {
					t.Fatalf("探测未录制: %d", n)
				}
			}
			if _, err := s.m.taskEnrichRoute(context.Background(), enrichTestTaskID(t, inherited)); err == nil {
				t.Fatal("无正式凭证仍可探测")
			}
		})
	}
}

func TestProxyPGWorkerAssetProbeDrainsBeforeFinish(t *testing.T) {
	s := newModelExecutionServer(t)
	s.m.enrich = enrich.New(s.m.assets, s.m.taskEnrichRoute, 1)
	started, release, modelDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce, modelOnce sync.Once
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		fmt.Fprint(w, "<title>worker-proxy</title>")
	}))
	defer proxy.Close()
	defer releaseOnce.Do(func() { close(release) })
	p := createRuntimeProxy(t, s, proxy.URL)
	task, err := s.m.CreateTaskWithOptions("资产出口测试", "", db.TaskCreateOptions{ProxyMode: db.TaskProxyModeFixed, ProxyID: &p.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.m.pg.DeleteTask(enrichTestTaskID(t, task)) })
	target := "http://worker-probe.enrich-test.invalid"
	t.Cleanup(func() {
		s.m.pg.Exec(`DELETE FROM assets WHERE domain='worker-probe.enrich-test.invalid' OR root_domain='enrich-test.invalid'`)
	})
	input, _ := json.Marshal(map[string]any{"assets": []any{map[string]any{"type": "service", "url": target}}})
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&request)
		used := false
		for _, m := range request.Messages {
			used = used || m.Role == "tool"
		}
		message := map[string]any{"role": "assistant", "content": "finished"}
		reason := "stop"
		if !used {
			message = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "call_asset", "type": "function", "function": map[string]any{"name": "insert_assets", "arguments": string(input)}}}}
			reason = "tool_calls"
		} else {
			modelOnce.Do(func() { close(modelDone) })
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message, "finish_reason": reason}}})
	}))
	defer model.Close()
	provider, err := (agent.Config{Format: llm.FormatOpenAI, BaseURL: model.URL + "/v1", APIKey: "synthetic", Model: "test"}).NewProvider()
	if err != nil {
		t.Fatal(err)
	}
	worker := agent.NewWorker(provider, "test", t.TempDir(), nil, 20000, 4)
	worker.SetNonStreaming(func() bool { return true })
	iid, err := task.Store.AddIntent(map[string]any{"summary": "登记一个 HTTP 资产"}, 1, nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := task.Store.ClaimIntent(iid, "test")
	if err != nil || !claimed {
		t.Fatal("意图未领取", err)
	}
	intent, err := task.Store.GetNode(iid)
	if err != nil {
		t.Fatal(err)
	}
	ctx, finish, ok := s.engine.beginTaskOperationContext(t.Context(), task.ID)
	if !ok {
		t.Fatal("未准入")
	}
	defer finish()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer finish()
		s.engine.runIntent(ctx, task, "test", worker, intent, "", "")
	}()
	for _, ch := range []chan struct{}{started, modelDone} {
		select {
		case <-ch:
		case <-time.After(4 * time.Second):
			t.Fatal("Worker 没有完成登记与发起探测")
		}
	}
	select {
	case <-done:
		t.Fatal("模型结束时探测被提前取消")
	case <-time.After(40 * time.Millisecond):
	}
	if err := s.m.ReleaseTaskProxyWhenIdle(t.Context(), task); !errors.Is(err, db.ErrProxyInUse) {
		t.Fatal("Worker 补全期间未保护出口", err)
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Worker 未排空探测")
	}
	var title string
	if err := s.m.pg.QueryRow(`SELECT page_title FROM assets WHERE url=$1`, target).Scan(&title); err != nil || title != "worker-proxy" {
		t.Fatalf("完整调用链未回写: %q %v", title, err)
	}
	if s.engine.inflightCount(task.ID) != 0 {
		t.Fatal("操作引用未解除")
	}
}

func enrichTestTaskID(t *testing.T, task *Task) int64 {
	t.Helper()
	id, err := strconv.ParseInt(task.ID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestProxyPGAssetProbeHoldsReleaseBarrier(t *testing.T) {
	s, _ := proxyIntegration(t)
	s.engine = NewEngine(s.m)
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		fmt.Fprint(w, "<title>finished</title>")
	}))
	defer proxy.Close()
	// 确保失败路径也先解除服务端阻塞，再关闭 httptest。
	defer once.Do(func() { close(release) })
	p := createRuntimeProxy(t, s, proxy.URL)
	task := createRuntimeTask(t, s, db.TaskProxyModeFixed, &p.ID)
	id := enrichTestTaskID(t, task)
	engine := enrich.New(s.m.pg.Assets(), s.m.taskEnrichRoute, 1)
	defer engine.Close()
	ctx, finish, ok := s.engine.beginTaskOperationContext(t.Context(), task.ID)
	if !ok {
		t.Fatal("准入失败")
	}
	defer finish()
	url := "http://barrier.enrich-test.invalid"
	asset, err := s.m.pg.Assets().UpsertHTTPService(db.UpsertHTTPServiceReq{URL: url, TaskID: id})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.m.pg.Exec(`DELETE FROM assets WHERE domain='barrier.enrich-test.invalid' OR root_domain='enrich-test.invalid'`)
	})
	scope := engine.ForTask(ctx, id)
	scope.ProbeSite(asset, url)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("探测未启动")
	}
	if _, err := s.m.pg.Exec(`UPDATE tasks SET status='done' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { scope.Close(); finish(); close(done) }()
	if err := s.m.ReleaseTaskProxyWhenIdle(t.Context(), task); !errors.Is(err, db.ErrProxyInUse) {
		t.Fatalf("在途回收未拒绝: %v", err)
	}
	if _, err := s.m.pg.UpdateProxy(p); !errors.Is(err, db.ErrProxyInUse) {
		t.Fatalf("在途修改未拒绝: %v", err)
	}
	once.Do(func() { close(release) })
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("探测未退出")
	}
	if err := s.m.ReleaseTaskProxyWhenIdle(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	var title string
	s.m.pg.QueryRow(`SELECT page_title FROM assets WHERE id=$1`, asset).Scan(&title)
	if title != "finished" {
		t.Fatal("探测结果未写回")
	}
}
