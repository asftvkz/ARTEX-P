package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/egress"
)

// 先验证真实任务路由，避免只有数据库辅助函数而未接入用户入口。
func TestProxyPGTaskSwitchRoute(t *testing.T) {
	s, _ := proxyIntegration(t)
	task := createRuntimeTask(t, s, "direct", nil)
	_, err := s.m.pg.Exec(`UPDATE tasks SET status='done' WHERE id=$1`, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.registerTaskProxyRoutes(mux)
	r := httptest.NewRequest("PUT", fmt.Sprintf("/api/tasks/%s/proxy", task.ID), strings.NewReader(`{"proxy_mode":"direct","expected_revision":0}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"proxy_revision":1`) {
		t.Fatalf("任务切换未接线：%d %s", w.Code, w.Body.String())
	}
}

func switchFixture(t *testing.T) (*Server, *http.ServeMux, *Task) {
	t.Helper()
	s, _ := proxyIntegration(t)
	task := createRuntimeTask(t, s, db.TaskProxyModeDirect, nil)
	if _, err := s.m.pg.Exec(`UPDATE tasks SET status='done' WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	s.m.tasks = map[string]*Task{task.ID: task}
	task.Status = "done"
	s.engine = NewEngine(s.m)
	mux := http.NewServeMux()
	s.registerTaskProxyRoutes(mux)
	return s, mux, task
}

func TestProxyPGTaskSwitchCASAndModes(t *testing.T) {
	s, mux, task := switchFixture(t)
	path := "/api/tasks/" + task.ID + "/proxy"
	for _, revision := range []string{"", `,"expected_revision":null`, `,"expected_revision":-1`, `,"expected_revision":0.1`, `,"expected_revision":9007199254740992`, `,"expected_revision":"0"`} {
		code, _, body := proxyAPI(t, mux, "PUT", path, `{"proxy_mode":"direct"`+revision+`}`)
		if code != 400 {
			t.Fatalf("无效revision: %d %s", code, body)
		}
	}
	for _, body := range []string{`{"proxy_mode":"fixed","expected_revision":0}`, `{"proxy_mode":"direct","proxy_id":1,"expected_revision":0}`, `{"proxy_mode":"inherit","proxy_id":1,"expected_revision":0}`} {
		code, _, out := proxyAPI(t, mux, "PUT", path, body)
		if code != 400 {
			t.Fatalf("无效绑定: %d %s", code, out)
		}
	}
	p1 := createRuntimeProxy(t, s, "http://alice:secret@127.0.0.1:18090")
	p2 := createRuntimeProxy(t, s, "http://127.0.0.1:18091")
	if err := s.m.pg.SetGlobalProxyID(&p1.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.m.pg.SetGlobalProxyID(nil) })
	code, out, body := proxyAPI(t, mux, "PUT", path, `{"proxy_mode":"inherit","expected_revision":0}`)
	if code != 200 || out["proxy_revision"] != float64(1) || out["proxy_id"] != float64(p1.ID) || strings.Contains(body, "secret") {
		t.Fatalf("inherit: %d %s", code, body)
	}
	if err := s.m.pg.SetGlobalProxyID(&p2.ID); err != nil {
		t.Fatal(err)
	}
	code, out, body = proxyAPI(t, mux, "PUT", path, `{"proxy_mode":"inherit","expected_revision":0}`)
	if code != 409 || out["code"] != "proxy_revision_conflict" || out["current_revision"] != float64(1) {
		t.Fatalf("CAS: %d %s", code, body)
	}
	row, _ := s.m.pg.GetTask(switchTaskID(t, task))
	if row.ProxySnapshot.ID != p1.ID {
		t.Fatal("旧revision重采样默认")
	}
	code, out, body = proxyAPI(t, mux, "PUT", path, `{"proxy_mode":"inherit","expected_revision":1}`)
	if code != 200 || out["proxy_id"] != float64(p2.ID) {
		t.Fatalf("重新保存inherit: %d %s", code, body)
	}
	code, _, body = proxyAPI(t, mux, "PUT", path, fmt.Sprintf(`{"proxy_mode":"fixed","proxy_id":%d,"expected_revision":2}`, p1.ID))
	if code != 200 {
		t.Fatalf("fixed: %d %s", code, body)
	}
	code, out, body = proxyAPI(t, mux, "PUT", path, `{"proxy_mode":"direct","proxy_id":null,"expected_revision":3}`)
	if code != 200 || out["proxy_revision"] != float64(4) || out["proxy"] != nil {
		t.Fatalf("direct: %d %s", code, body)
	}
}

func switchTaskID(t *testing.T, task *Task) int64 {
	t.Helper()
	id, err := strconv.ParseInt(task.ID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestProxyPGTaskSwitchDirectActivityAcrossManagers(t *testing.T) {
	s, mux, task := switchFixture(t)
	second := &Manager{pg: s.m.pg, dir: s.m.dir, tasks: map[string]*Task{task.ID: {ID: task.ID, ProxyMode: db.TaskProxyModeDirect}}}
	engine := NewEngine(second)
	ctx, cancel := context.WithCancel(t.Context())
	op, finish, ok := engine.beginTaskOperationContext(ctx, task.ID)
	if !ok {
		t.Fatal("direct准入失败")
	}
	defer finish()
	cancel()
	if second.TaskProxyOperationAdmitted(op, task.ID) {
		t.Fatal("取消凭证仍有效")
	}
	p := createRuntimeProxy(t, s, "http://127.0.0.1:18092")
	code, out, body := proxyAPI(t, mux, "PUT", "/api/tasks/"+task.ID+"/proxy", fmt.Sprintf(`{"proxy_mode":"fixed","proxy_id":%d,"expected_revision":0}`, p.ID))
	if code != 409 || out["code"] != "proxy_busy" {
		t.Fatalf("取消但未退出的direct可被切换: %d %s", code, body)
	}
	finish()
	finish()
	var count int64
	if err := s.m.pg.QueryRow(`SELECT active_count FROM task_operation_activity WHERE task_id=$1`, task.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("重复结束: %d %v", count, err)
	}
	code, _, body = proxyAPI(t, mux, "PUT", "/api/tasks/"+task.ID+"/proxy", fmt.Sprintf(`{"proxy_mode":"fixed","proxy_id":%d,"expected_revision":0}`, p.ID))
	if code != 200 {
		t.Fatalf("退出后切换: %d %s", code, body)
	}
}

func TestProxyPGTaskSwitchConcurrentCASAndNewEgress(t *testing.T) {
	s, mux, task := switchFixture(t)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "NEW-EGRESS") }))
	defer proxy.Close()
	p := createRuntimeProxy(t, s, proxy.URL)
	second := &Manager{pg: s.m.pg, dir: s.m.dir, tasks: map[string]*Task{task.ID: {ID: task.ID, ProxyMode: db.TaskProxyModeDirect}}}
	if err := second.InitProxyRuntime(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.CloseProxyRuntime() })
	engine := NewEngine(second)
	other := &Server{m: second, engine: engine}
	mux2 := http.NewServeMux()
	other.registerTaskProxyRoutes(mux2)
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for _, handler := range []http.Handler{mux, mux2} {
		wg.Go(func() {
			req := httptest.NewRequest("PUT", "/api/tasks/"+task.ID+"/proxy", strings.NewReader(fmt.Sprintf(`{"proxy_mode":"fixed","proxy_id":%d,"expected_revision":0}`, p.ID)))
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			codes <- w.Code
		})
	}
	wg.Wait()
	close(codes)
	success := 0
	for code := range codes {
		if code == 200 {
			success++
		} else if code != 409 {
			t.Fatalf("竞争返回%d", code)
		}
	}
	if success != 1 {
		t.Fatalf("CAS成功数=%d", success)
	}
	op, finish, ok := engine.beginTaskOperationContext(t.Context(), task.ID)
	if !ok {
		t.Fatal("新准入失败")
	}
	defer finish()
	stale, _ := second.Task(task.ID)
	addr, _, err := second.TaskToolProxy(op, stale)
	if err != nil {
		t.Fatal(err)
	}
	upstream, err := egress.ProxyURL(addr)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(upstream)}, Timeout: time.Second}
	resp, err := client.Get("http://unreachable.invalid/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "NEW-EGRESS" || stale.proxyState().Revision != 1 {
		t.Fatalf("旧Manager未用新出口: %s", b)
	}
}

func TestProxyPGTaskSwitchResinFailureAndRecovery(t *testing.T) {
	s, mux, _ := switchFixture(t)
	deletes := 0
	_, task := lifecycleResin(t, s, func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == "DELETE" {
			deletes++
			w.WriteHeader(500)
			return true
		}
		return false
	})
	if _, err := s.m.pg.Exec(`UPDATE tasks SET status='done' WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.m.TaskToolProxy(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	path := "/api/tasks/" + task.ID + "/proxy"
	code, _, body := proxyAPI(t, mux, "PUT", path, `{"proxy_mode":"direct","expected_revision":0}`)
	if code != 409 {
		t.Fatalf("回收失败未封锁: %d %s", code, body)
	}
	if _, err := s.m.pg.AcquireTaskOperation(switchTaskID(t, task)); !errors.Is(err, db.ErrProxySwitchRecovery) && !errors.Is(err, db.ErrProxyInUse) {
		t.Fatalf("回收失败可继续旧出口: %v", err)
	}
	code, _, body = proxyAPI(t, mux, "POST", path+"/recovery", `{"action":"retry","expected_revision":0}`)
	if code != 409 || deletes != 1 {
		t.Fatalf("uncertain重复DELETE: %d %d %s", code, deletes, body)
	}
	code, out, body := proxyAPI(t, mux, "POST", path+"/recovery", `{"action":"confirm_released","expected_revision":0}`)
	if code != 200 || out["proxy_revision"] != float64(1) || deletes != 1 {
		t.Fatalf("人工恢复: %d %s", code, body)
	}
}

func TestProxyPGTaskSwitchCanRecoverToDirectWhenTargetFails(t *testing.T) {
	s, mux, task := switchFixture(t)
	target := createRuntimeProxy(t, s, "http://127.0.0.1:18099")
	id := switchTaskID(t, task)
	executor, err := s.m.pg.AcquireTaskProxySwitchExecutor(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := executor.Begin(0, db.TaskProxyModeFixed, &target.ID)
	if err != nil {
		executor.Close()
		t.Fatal(err)
	}
	if _, err = s.m.pg.Exec(`UPDATE outbound_proxies SET status='failed' WHERE id=$1`, target.ID); err != nil {
		executor.Close()
		t.Fatal(err)
	}
	if _, err = executor.Complete(0, token); !errors.Is(err, db.ErrProxyUnavailable) {
		executor.Close()
		t.Fatalf("目标失效应保留人工恢复屏障: %v", err)
	}
	executor.Close()
	code, out, body := proxyAPI(t, mux, "POST", "/api/tasks/"+task.ID+"/proxy/recovery", `{"action":"abort_direct","expected_revision":0}`)
	if code != 200 || out["proxy_mode"] != "direct" || out["proxy_revision"] != float64(1) {
		t.Fatalf("无法安全退回direct: %d %s", code, body)
	}
	if _, err = s.m.pg.Exec(`UPDATE outbound_proxies SET status='healthy' WHERE id=$1`, target.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.m.pg.UpdateProxy(target); err != nil {
		t.Fatalf("屏障未解除，目标无法修复: %v", err)
	}
}

func TestProxyPGTaskSwitchRecoveryCannotStealLiveCleanup(t *testing.T) {
	s, _, _ := switchFixture(t)
	entered, unblock := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(unblock) }) })
	old, task := lifecycleResin(t, s, func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == "DELETE" {
			close(entered)
			<-unblock
			w.WriteHeader(204)
			return true
		}
		return false
	})
	if _, err := s.m.pg.Exec(`UPDATE tasks SET status='done' WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.m.TaskToolProxy(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	target := createRuntimeProxy(t, s, "http://127.0.0.1:18095")
	second := &Manager{pg: s.m.pg, dir: s.m.dir}
	if err := second.InitProxyRuntime(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.CloseProxyRuntime() })
	finished := make(chan error, 1)
	go func() {
		_, err := s.m.switchTaskProxy(t.Context(), task.ID, 0, db.TaskProxyModeFixed, &target.ID)
		finished <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("未进入远端回收")
	}
	_, err := second.recoverTaskProxy(t.Context(), task.ID, 0, "confirm_released")
	if !errors.Is(err, db.ErrProxyInUse) && !errors.Is(err, db.ErrProxySwitchRecovery) {
		t.Errorf("其他Manager抢走在途回收: %v", err)
	}
	if _, err := s.m.pg.UpdateProxy(old); !errors.Is(err, db.ErrProxyInUse) {
		t.Errorf("回收期旧proxy可修改: %v", err)
	}
	if _, err := s.m.pg.UpdateProxy(target); !errors.Is(err, db.ErrProxyInUse) {
		t.Errorf("回收期目标proxy可修改: %v", err)
	}
	if _, err := s.m.pg.AcquireTaskOperation(switchTaskID(t, task)); !errors.Is(err, db.ErrProxySwitchRecovery) && !errors.Is(err, db.ErrProxyInUse) {
		if err == nil {
			_ = s.m.pg.ReleaseTaskOperation(switchTaskID(t, task))
		}
		t.Errorf("回收期其他Manager准入: %v", err)
	}
	once.Do(func() { close(unblock) })
	if err := <-finished; err != nil {
		t.Errorf("原执行权未完成: %v", err)
	}
}

func TestProxyPGTaskSwitchClearsOnlyStaleOperations(t *testing.T) {
	s, _, task := switchFixture(t)
	id := switchTaskID(t, task)
	if _, err := s.m.pg.Exec(`INSERT INTO task_operation_activity(task_id,active_count) VALUES($1,1) ON CONFLICT(task_id) DO UPDATE SET active_count=1,switch_token=''`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.m.pg.AcquireTaskOperation(id); !errors.Is(err, db.ErrProxyInUse) && !errors.Is(err, db.ErrProxySwitchRecovery) {
		t.Fatalf("残留活动未failclosed: %v", err)
	}
	row, err := s.m.recoverTaskProxy(t.Context(), task.ID, 0, "clear_stale_activity")
	if err != nil || row.ProxyRevision != 0 {
		t.Fatalf("人工恢复: %+v %v", row, err)
	}
	state, err := s.m.pg.AcquireTaskOperation(id)
	if err != nil {
		t.Fatalf("恢复后不能准入: %v", err)
	}
	_ = state
	if _, err = s.m.pg.AcquireTaskProxySwitchExecutor(t.Context(), id); !errors.Is(err, db.ErrProxyInUse) {
		t.Fatalf("真实操作期间恢复锁未阻塞: %v", err)
	}
	if err = s.m.pg.ReleaseTaskOperation(id); err != nil {
		t.Fatal(err)
	}
	executor, err := s.m.pg.AcquireTaskProxySwitchExecutor(t.Context(), id)
	if err != nil {
		t.Fatalf("真实操作退出后恢复锁仍阻塞: %v", err)
	}
	executor.Close()
}

func TestProxyPGTaskSwitchRejectsUnownedPersistedActivity(t *testing.T) {
	s, mux, task := switchFixture(t)
	id := switchTaskID(t, task)
	if _, err := s.m.pg.Exec(`INSERT INTO task_operation_activity(task_id,active_count,operation_owners) VALUES($1,0,jsonb_build_object('999999',1)) ON CONFLICT(task_id) DO UPDATE SET active_count=0,operation_owners=EXCLUDED.operation_owners,switch_token=''`, id); err != nil {
		t.Fatal(err)
	}
	target := createRuntimeProxy(t, s, "http://127.0.0.1:18100")
	code, out, body := proxyAPI(t, mux, "PUT", "/api/tasks/"+task.ID+"/proxy", fmt.Sprintf(`{"proxy_mode":"fixed","proxy_id":%d,"expected_revision":0}`, target.ID))
	if code != 409 || out["code"] != "proxy_busy" {
		t.Fatalf("持久占用所有者不一致时必须failclosed: %d %s", code, body)
	}
	if _, err := s.m.recoverTaskProxy(t.Context(), task.ID, 0, "clear_stale_activity"); err != nil {
		t.Fatalf("人工清理无活动锁的陈旧占用: %v", err)
	}
	code, _, body = proxyAPI(t, mux, "PUT", "/api/tasks/"+task.ID+"/proxy", fmt.Sprintf(`{"proxy_mode":"fixed","proxy_id":%d,"expected_revision":0}`, target.ID))
	if code != 200 {
		t.Fatalf("人工清理后仍无法切换: %d %s", code, body)
	}
}

func TestProxyPGTaskSwitchFailedOperationFinishUnlocksManualRecovery(t *testing.T) {
	s, _, task := switchFixture(t)
	id := switchTaskID(t, task)
	if _, err := s.m.pg.AcquireTaskOperation(id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.m.pg.Exec(fmt.Sprintf(`ALTER TABLE task_operation_activity ADD CONSTRAINT task_switch_test_hold CHECK(task_id<>%d OR active_count<>0)`, id)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.m.pg.Exec(`ALTER TABLE task_operation_activity DROP CONSTRAINT IF EXISTS task_switch_test_hold`)
	})
	if err := s.m.pg.ReleaseTaskOperation(id); err == nil {
		t.Fatal("预期用约束注入持久计数解除失败")
	}
	if _, err := s.m.pg.Exec(`ALTER TABLE task_operation_activity DROP CONSTRAINT task_switch_test_hold`); err != nil {
		t.Fatal(err)
	}
	executor, err := s.m.pg.AcquireTaskProxySwitchExecutor(t.Context(), id)
	if err != nil {
		t.Fatalf("失败结束仍遗留会话锁，阻断人工恢复: %v", err)
	}
	executor.Close()
	if _, err = s.m.recoverTaskProxy(t.Context(), task.ID, 0, "clear_stale_activity"); err != nil {
		t.Fatalf("人工清理残留活动: %v", err)
	}
}

func TestProxyPGTaskSwitchSingleConnectionAndAuthoritativeGET(t *testing.T) {
	s, mux, task := switchFixture(t)
	stale := &Task{ID: task.ID, ProxyMode: db.TaskProxyModeDirect}
	other := &Server{m: &Manager{pg: s.m.pg, tasks: map[string]*Task{task.ID: stale}}, engine: NewEngine(nil)}
	getMux := http.NewServeMux()
	getMux.HandleFunc("GET /api/tasks/{id}", other.getTask)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "SINGLE-CONNECTION-EGRESS") }))
	defer proxy.Close()
	p := createRuntimeProxy(t, s, proxy.URL)
	s.m.pg.SetMaxOpenConns(1)
	code, _, body := proxyAPI(t, mux, "PUT", "/api/tasks/"+task.ID+"/proxy", fmt.Sprintf(`{"proxy_mode":"fixed","proxy_id":%d,"expected_revision":0}`, p.ID))
	if code != 200 {
		t.Fatalf("单连接切换: %d %s", code, body)
	}
	code, out, body := proxyAPI(t, getMux, "GET", "/api/tasks/"+task.ID, "")
	if code != 200 || out["proxy_revision"] != float64(1) || out["proxy_id"] != float64(p.ID) {
		t.Fatalf("旧Manager GET不是权威回读: %d %s", code, body)
	}
	if stale.proxyState().Revision != 0 {
		t.Fatal("GET修改运行缓存会跳过旧路由回收")
	}
	if _, err := s.m.pg.Exec(`UPDATE tasks SET status='running',paused=false WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	task.Status = "running"
	op, finish, ok := s.engine.beginTaskOperationContext(t.Context(), task.ID)
	if !ok {
		t.Fatal("单连接普通准入失败")
	}
	done := make(chan error, 1)
	go func() {
		addr, _, err := s.m.TaskToolProxy(op, task)
		if err == nil {
			var upstream *url.URL
			upstream, err = egress.ProxyURL(addr)
			if err == nil {
				client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(upstream)}, Timeout: time.Second}
				var resp *http.Response
				resp, err = client.Get("http://unreachable.invalid/")
				if err == nil {
					b, readErr := io.ReadAll(resp.Body)
					resp.Body.Close()
					err = readErr
					if string(b) != "SINGLE-CONNECTION-EGRESS" {
						err = fmt.Errorf("未使用新出口: %q", b)
					}
				}
			}
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(time.Second):
		t.Error("普通操作持锁耗尽单连接业务池，真实HTTP出口无法运行")
	}
	finish()
}

func TestProxyPGTaskSwitchPausedQueuedRunningAndChild(t *testing.T) {
	s, mux, task := switchFixture(t)
	p := createRuntimeProxy(t, s, "http://127.0.0.1:18098")
	path := "/api/tasks/" + task.ID + "/proxy"
	for _, query := range []string{`UPDATE tasks SET status='running',paused=false,queued=false WHERE id=$1`, `UPDATE tasks SET status='done',queued=true WHERE id=$1`} {
		if _, err := s.m.pg.Exec(query, task.ID); err != nil {
			t.Fatal(err)
		}
		code, _, body := proxyAPI(t, mux, "PUT", path, fmt.Sprintf(`{"proxy_mode":"fixed","proxy_id":%d,"expected_revision":0}`, p.ID))
		if code != 409 {
			t.Fatalf("queued/running允许切换: %d %s", code, body)
		}
	}
	if _, err := s.m.pg.Exec(`UPDATE tasks SET status='running',paused=true,queued=false WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	parentID := switchTaskID(t, task)
	child, err := s.m.pg.CreateTaskWithOptions("子任务", "", db.TaskCreateOptions{ProxyParentTaskID: &parentID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.m.pg.Exec(`DELETE FROM tasks WHERE id=$1`, child.ID)
		_, _ = s.m.pg.Exec(`DELETE FROM explorations WHERE id=$1`, child.ExplorationID)
	})
	code, _, body := proxyAPI(t, mux, "PUT", path, fmt.Sprintf(`{"proxy_mode":"fixed","proxy_id":%d,"expected_revision":0}`, p.ID))
	if code != 200 {
		t.Fatalf("idle paused不能切换: %d %s", code, body)
	}
	childAfter, err := s.m.pg.GetTask(child.ID)
	if err != nil || childAfter.ProxyMode != db.TaskProxyModeDirect || childAfter.ProxyRevision != 0 {
		t.Fatalf("子任务快照被改: %+v %v", childAfter, err)
	}
}
