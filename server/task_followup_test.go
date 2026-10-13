package server

import (
	"context"
	"fmt"
	"github.com/Autumn-27/artex/db"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTaskFollowupAuthoritativeRecords(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	current, err := m.CreateTask("追问任务", "保留结果", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := m.CreateTask("其他任务", "禁止读取", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = m.DeleteTask(current.ID, DeleteTaskOptions{})
		_, _ = m.DeleteTask(foreign.ID, DeleteTaskOptions{})
	})
	id, err := current.Store.AddNode(db.KindIntent, map[string]any{"summary": "原始目标"}, 1, "done", "human", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = current.Store.AppendActivity(db.Activity{NodeID: &id, Worker: "worker", Kind: "result", Detail: "原始权威结果" + strings.Repeat("中文记录", 7000)})
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"question", "execute"} {
		got, err := taskFollowupContext(current, &taskFollowup{IntentID: id, Mode: mode})
		if err != nil || !strings.Contains(got, "原始权威结果") || len(got) > 24500 || !utf8.ValidString(got) {
			t.Fatalf("结果上下文 mode=%s len=%d err=%v", mode, len(got), err)
		}
	}
	other, err := foreign.Store.AddNode(db.KindIntent, map[string]any{}, 1, "done", "human", nil)
	if err != nil {
		t.Fatal(err)
	}
	live, err := current.Store.AddNode(db.KindIntent, map[string]any{}, 1, "running", "human", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []*taskFollowup{{IntentID: other, Mode: "question"}, {IntentID: live, Mode: "execute"}, {IntentID: id, Mode: "unknown"}, {IntentID: 0, Mode: "question"}} {
		if _, err := taskFollowupContext(current, request); err == nil {
			t.Fatalf("无效引用未拒绝 %+v", request)
		}
	}
	node, err := current.Store.GetNode(id)
	if err != nil || node.State != "done" {
		t.Fatalf("追问改变旧执行 %v", err)
	}
}

func TestTaskFollowupHTTPQuestionDoesNotInjectIntent(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	task, err := m.CreateTask("已完成继续提问", "保留历史", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) })
	if err = m.SetTaskStatus(task.ID, "done"); err != nil {
		t.Fatal(err)
	}
	id, err := task.Store.AddNode(db.KindIntent, map[string]any{"summary": "原执行"}, 1, "done", "human", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = task.Store.AppendActivity(db.Activity{NodeID: &id, Worker: "worker", Kind: "result", Detail: "原 Worker 已完成"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// 显式耗尽本任务模型链，避免共享库的旧激活渠道让此规则模式用例访问网络。
	task.LLMProfileIDs = []int64{-1}
	task.ActiveLLMProfileID = nil
	s := &Server{m: m, engine: NewEngine(m), ctx: ctx, chatBusy: map[string]bool{}, chatCancel: map[string]context.CancelCauseFunc{}}
	before, err := task.Store.ListByKind(db.KindIntent, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"question", "execute"} {
		req := httptest.NewRequest("POST", "/api/chat?task="+task.ID, strings.NewReader(fmt.Sprintf(`{"message":"意图 不应在追问中执行","followup":{"intent_id":%d,"mode":%q}}`, id, mode)))
		response := httptest.NewRecorder()
		s.chat(response, req)
		if response.Code != 200 {
			t.Fatalf("%s HTTP %d %s", mode, response.Code, response.Body.String())
		}
		after, err := task.Store.ListByKind(db.KindIntent, 100)
		if err != nil {
			t.Fatal(err)
		}
		want := len(before)
		if mode == "execute" {
			want++
		}
		if len(after) != want {
			t.Fatalf("%s 意图数 %d want %d", mode, len(after), want)
		}
		old, err := task.Store.GetNode(id)
		if err != nil || old.State != "done" {
			t.Fatal("原 Worker 被改变")
		}
	}
}
