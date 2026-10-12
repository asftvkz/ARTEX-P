package server

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

func TestLLMOperationTicketProtectsTerminalTaskAndExpires(t *testing.T) {
	s := newModelExecutionServer(t)
	p, task := lifecycleResin(t, s, nil)
	s.m.tasks[task.ID] = task
	parent, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx, finish, ok := s.engine.beginTaskOperationContext(parent, task.ID)
	if !ok {
		t.Fatal("操作未准入")
	}
	defer finish()
	if !s.m.TaskProxyOperationAdmitted(context.WithValue(ctx, struct{}{}, "child"), task.ID) || s.m.TaskProxyOperationAdmitted(context.Background(), task.ID) || s.m.TaskProxyOperationAdmitted(ctx, "999999") {
		t.Fatal("准入凭证未按context/task隔离")
	}
	if _, _, err := s.m.TaskToolProxy(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := s.m.SetTaskStatus(task.ID, "done"); err != nil {
		t.Fatal(err)
	}
	if err := s.m.ReleaseTaskProxyWhenIdle(t.Context(), task); !errors.Is(err, db.ErrProxyInUse) {
		t.Fatalf("在途人工回收未拒绝: %v", err)
	}
	if _, err := s.m.pg.UpdateProxy(p); !errors.Is(err, db.ErrProxyInUse) {
		t.Fatalf("终态在途代理可修改: %v", err)
	}
	if err := s.m.pg.DeleteProxy(p.ID); !errors.Is(err, db.ErrProxyInUse) {
		t.Fatalf("终态在途代理可删除: %v", err)
	}
	if _, err := s.m.pg.WithIdleProxy(p.ID, func(*db.Proxy) error { t.Error("在途允许停止WARP"); return nil }); !errors.Is(err, db.ErrProxyInUse) {
		t.Fatal(err)
	}
	cancel()
	if s.m.TaskProxyOperationAdmitted(ctx, task.ID) {
		t.Fatal("取消后凭证仍有效")
	}
	if err := s.m.pg.DeleteProxy(p.ID); !errors.Is(err, db.ErrProxyInUse) {
		t.Fatal("取消但工具未退出时活动引用已解除")
	}
	finish()
	finish()
	deadline := time.Now().Add(3 * time.Second)
	for {
		b := s.m.taskProxyBarrier(task.ID)
		b.mu.Lock()
		busy := b.pending || b.releasing
		b.mu.Unlock()
		if !busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("终态回收未结束")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if s.m.TaskProxyOperationAdmitted(ctx, task.ID) || s.engine.inflightCount(task.ID) != 0 {
		t.Fatal("结束凭证有效或重复结束重复扣减")
	}
	// 终态仍允许用户发起 MainAgent 会话，新凭证才能重新取得 released 租约。
	if _, _, err := s.m.TaskToolProxy(context.Background(), task); err == nil {
		t.Fatal("无准入凭证重新取得租约")
	}
	next, closeNext, ok := s.engine.beginTaskOperationContext(t.Context(), task.ID)
	if !ok {
		t.Fatal("终态MainAgent不能正式准入")
	}
	if _, _, err := s.m.TaskToolProxy(next, task); err != nil {
		closeNext()
		t.Fatal(err)
	}
	closeNext()
	waitModelTaskProxyIdle(t, s, task.ID)
}

func waitModelTaskProxyIdle(t *testing.T, s *Server, id string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		b := s.m.taskProxyBarrier(id)
		b.mu.Lock()
		busy := b.pending || b.releasing
		b.mu.Unlock()
		if !busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("出口回收未完成")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestLLMSpawnInheritsParentProxySnapshotInTransaction(t *testing.T) {
	s := newModelExecutionServer(t)
	old, err := s.m.pg.GlobalProxyID()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.m.pg.SetGlobalProxyID(old) })
	a := createRuntimeProxy(t, s, "http://127.0.0.1:19001")
	b := createRuntimeProxy(t, s, "http://127.0.0.1:19002")
	if err := s.m.pg.SetGlobalProxyID(&a.ID); err != nil {
		t.Fatal(err)
	}
	parent, err := s.m.pg.CreateTaskWithOptions("父任务", "", db.TaskCreateOptions{ProxyMode: db.TaskProxyModeInherit})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.m.pg.DeleteTask(parent.ID) })
	if err := s.m.pg.SetGlobalProxyID(&b.ID); err != nil {
		t.Fatal(err)
	}
	child, err := s.m.pg.CreateTaskWithOptions("子任务", "", db.TaskCreateOptions{ProxyParentTaskID: &parent.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.m.pg.DeleteTask(child.ID) })
	if child.ProxyMode != db.TaskProxyModeInherit || child.ProxyID == nil || *child.ProxyID != a.ID || child.ProxySnapshot != parent.ProxySnapshot {
		t.Fatal("子任务继承时改用当前全局或重新生成快照")
	}
}

func TestLLMProxyReleaseSerializesNewAdmissionWithoutLifecycleLock(t *testing.T) {
	s := newModelExecutionServer(t)
	entered, unblock := make(chan struct{}), make(chan struct{})
	var enterOnce, unblockOnce sync.Once
	_, task := lifecycleResin(t, s, func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method != http.MethodDelete {
			return false
		}
		enterOnce.Do(func() { close(entered) })
		<-unblock
		w.WriteHeader(204)
		return true
	})
	t.Cleanup(func() { unblockOnce.Do(func() { close(unblock) }) })
	s.m.tasks[task.ID] = task
	ctx, finish, ok := s.engine.beginTaskOperationContext(t.Context(), task.ID)
	if !ok {
		t.Fatal("未准入")
	}
	if _, _, err := s.m.TaskToolProxy(ctx, task); err != nil {
		finish()
		t.Fatal(err)
	}
	finish()
	done := make(chan error, 1)
	go func() { done <- s.m.ReleaseTaskProxyWhenIdle(t.Context(), task) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		unblockOnce.Do(func() { close(unblock) })
		t.Fatal("未进入回收网络请求")
	}
	admitted := make(chan func(), 1)
	go func() {
		_, finish, ok := s.engine.beginTaskOperationContext(t.Context(), task.ID)
		if !ok {
			admitted <- nil
			return
		}
		admitted <- finish
	}()
	select {
	case finish := <-admitted:
		if finish != nil {
			finish()
		}
		unblockOnce.Do(func() { close(unblock) })
		t.Fatal("回收未结束就准入新操作")
	case <-time.After(50 * time.Millisecond):
	}
	changed := make(chan error, 1)
	go func() { changed <- s.m.SetTaskStatus(task.ID, "running") }()
	select {
	case err := <-changed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		unblockOnce.Do(func() { close(unblock) })
		t.Fatal("远端回收持有taskStateMu")
	}
	other := executionTask(t, s, nil)
	otherCtx, otherFinish, ok := s.engine.beginTaskOperationContext(t.Context(), other.ID)
	if !ok {
		t.Fatal("其他任务无法准入")
	}
	if _, _, err := s.m.TaskToolProxy(otherCtx, other); err != nil {
		otherFinish()
		t.Fatal(err)
	}
	otherFinish()
	unblockOnce.Do(func() { close(unblock) })
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case finish := <-admitted:
		if finish == nil {
			t.Fatal("回收后未准入")
		}
		finish()
	case <-time.After(time.Second):
		t.Fatal("回收后新操作仍阻塞")
	}
}
