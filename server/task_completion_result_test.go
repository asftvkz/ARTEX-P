package server

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
)

func TestGoallessFailedIntentSettlesTaskAsFailedWithErrorActivity(t *testing.T) {
	for _, state := range []string{"blocked", "exhausted"} {
		t.Run(state, func(t *testing.T) {
			s, task := taskCompletionResultFixture(t)
			if _, err := task.Store.AddNode(db.KindIntent, map[string]any{"summary": "最终执行失败"}, 1, state, "planner", nil); err != nil {
				t.Fatal(err)
			}
			s.engine.Run(t.Context(), task)

			waitTaskCompletionResultStatus(t, s, task, "failed")
			result := waitTaskCompletionSystemActivity(t, task)
			if result == nil {
				t.Fatal("没有持久化 system 收尾结果")
			}
			if strings.Contains(result.Summary, "目标已全部达成") || strings.Contains(result.Summary, "任务完成") {
				t.Fatalf("失败任务的 system 结果错误声称目标达成: %q", result.Summary)
			}
			if !result.IsError {
				t.Fatalf("失败收尾的 system 结果应标记 IsError=true: %+v", *result)
			}
		})
	}
}

func TestMetGoalAllowsCompletionDespiteUnrelatedFailedIntent(t *testing.T) {
	s, task := taskCompletionResultFixture(t)
	goalID, err := task.Store.AddNode(db.KindGoal, map[string]any{"text": "已证明的目标"}, 0, "open", "human", nil)
	if err != nil {
		t.Fatal(err)
	}
	if goalID == 0 {
		t.Fatal("目标没有落库")
	}
	evidenceID, err := task.Store.AddNode(db.KindFact, map[string]any{"summary": "目标正向证据"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := task.Store.ProveGoal(evidenceID, goalID); err != nil {
		t.Fatal(err)
	}
	if _, err := task.Store.AddNode(db.KindIntent, map[string]any{"summary": "无关失败旁支"}, 1, "blocked", "planner", nil); err != nil {
		t.Fatal(err)
	}
	s.engine.Run(t.Context(), task)
	waitTaskCompletionResultStatus(t, s, task, "done")
}

func TestOpenGoalWithFailedIntentStillRunsPlanner(t *testing.T) {
	s, task := taskCompletionResultFixture(t)
	if _, err := task.Store.AddNode(db.KindGoal, map[string]any{"text": "仍待完成的目标"}, 0, "open", "human", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := task.Store.AddNode(db.KindIntent, map[string]any{"summary": "失败的旧方向"}, 1, "exhausted", "planner", nil); err != nil {
		t.Fatal(err)
	}
	called := make(chan struct{}, 1)
	var calls atomic.Int32
	planner := agent.NewPlanner(retestProvider{complete: func(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
		if calls.Add(1) == 1 {
			called <- struct{}{}
		}
		return followupText("已读取未完成目标，准备继续规划")
	}}, "completion-gate-fixture", s.m.dir, nil, 100000, 1)
	planner.SetNonStreaming(func() bool { return true })
	s.engine.SetAuthoritativeAgentResolver(func(*Task) (*agent.Planner, *agent.Worker) {
		return planner, new(agent.Worker)
	})
	s.engine.Run(t.Context(), task)
	select {
	case <-called:
	case <-time.After(3 * time.Second):
		t.Fatal("存在开放目标时，失败意图没有触发真实 Planner 模型请求")
	}
	if calls.Load() == 0 {
		t.Fatal("fake provider 没有被调用")
	}
	waitTaskCompletionResultStatus(t, s, task, "running")
	goals, err := task.Store.ListByKind(db.KindGoal, 10)
	if err != nil || len(goals) != 1 || goals[0].State != "open" {
		t.Fatalf("开放目标在 Planner 尚未证明前被改变: goals=%+v err=%v", goals, err)
	}
}

func TestInvalidMetFlagGoalReopensAndWakesPlannerWithoutLosingProof(t *testing.T) {
	s, task := taskCompletionResultFixture(t)
	goalID, err := task.Store.AddNode(db.KindGoal, map[string]any{"text": "获取 flag 并记录证据"}, 0, "met", "planner", nil)
	if err != nil {
		t.Fatal(err)
	}
	evidenceID, err := task.Store.AddNode(db.KindFact, map[string]any{
		"summary":    "提权权限受限，flag 0 命中",
		"evidence":   "读取 /flag 返回 EACCES；posix_seteuid(0)=false；flag hits=0",
		"confidence": "observed",
	}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	// 合成旧版本已写入的错误证明，不能通过修复后的证明接口制造该历史状态。
	if _, err := s.m.pg.Exec(`INSERT INTO exploration_edges(exploration_id,src_id,rel,dst_id) VALUES($1,$2,'proves',$3)`, task.Store.ID(), evidenceID, goalID); err != nil {
		t.Fatal(err)
	}
	if _, err := task.Store.AddNode(db.KindIntent, map[string]any{"summary": "最后方向模型响应头超时"}, 1, "blocked", "planner", nil); err != nil {
		t.Fatal(err)
	}
	called := make(chan struct{}, 1)
	planner := agent.NewPlanner(retestProvider{complete: func(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
		select {
		case called <- struct{}{}:
		default:
		}
		return followupText("负证据不能证明获取成功，继续规划")
	}}, "completion-proof-recovery-fixture", s.m.dir, nil, 100000, 1)
	planner.SetNonStreaming(func() bool { return true })
	s.engine.SetAuthoritativeAgentResolver(func(*Task) (*agent.Planner, *agent.Worker) {
		return planner, new(agent.Worker)
	})
	s.engine.Run(t.Context(), task)
	select {
	case <-called:
	case <-time.After(3 * time.Second):
		t.Fatal("错误证明重开目标后，没有再次唤醒真实 Planner 模型请求")
	}
	goal, err := task.Store.GetNode(goalID)
	if err != nil || goal == nil || goal.State != "open" {
		t.Fatalf("负证据对应目标没有重开: goal=%+v err=%v", goal, err)
	}
	waitTaskCompletionResultStatus(t, s, task, "running")
	activity := waitTaskCompletionSystemActivity(t, task)
	if activity == nil || !activity.IsError || !strings.Contains(activity.Summary, "无效目标证明") {
		t.Fatalf("无效证明重开没有保留明确 system 审计: %+v", activity)
	}
	var proofs int
	if err := s.m.pg.QueryRow(`SELECT count(*) FROM exploration_edges WHERE exploration_id=$1 AND src_id=$2 AND rel='proves' AND dst_id=$3`, task.Store.ID(), evidenceID, goalID).Scan(&proofs); err != nil || proofs != 1 {
		t.Fatalf("无效历史证明应保留用于审计: proofs=%d err=%v", proofs, err)
	}
}

func taskCompletionResultFixture(t *testing.T) (*Server, *Task) {
	t.Helper()
	s := newTriggerRegressionServer(t)
	s.engine.debounce = time.Millisecond
	task, err := s.m.CreateTaskWithOptions("任务完成结果门禁回归", "", db.TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.m.SetTaskStatus(task.ID, "running"); err != nil {
		t.Fatal(err)
	}
	planner := agent.NewPlanner(retestProvider{complete: func(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
		return followupText("继续等待")
	}}, "completion-gate-fixture", s.m.dir, nil, 100000, 1)
	planner.SetNonStreaming(func() bool { return true })
	s.engine.SetAuthoritativeAgentResolver(func(*Task) (*agent.Planner, *agent.Worker) {
		return planner, new(agent.Worker)
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil {
			t.Errorf("等待测试 Engine 退出: %v", err)
		}
		id, parseErr := parseTaskID(task.ID)
		if parseErr != nil {
			t.Errorf("解析测试任务 id: %v", parseErr)
			return
		}
		if err := s.m.pg.DeleteTask(id); err != nil {
			t.Errorf("清理测试任务: %v", err)
		}
	})
	return s, task
}

func waitTaskCompletionResultStatus(t *testing.T, s *Server, task *Task, want string) {
	t.Helper()
	id, err := parseTaskID(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		persisted, dbErr := s.m.pg.GetTask(id)
		if dbErr != nil {
			t.Fatal(dbErr)
		}
		if persisted != nil && persisted.Status == want && s.m.TaskStatus(task.ID) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	persisted, _ := s.m.pg.GetTask(id)
	t.Fatalf("任务状态未同步进入 %s: manager=%q persisted=%+v", want, s.m.TaskStatus(task.ID), persisted)
}

func waitTaskCompletionSystemActivity(t *testing.T, task *Task) *db.Activity {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		activities, _, err := task.Store.ActivityList(nil, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		for i := range activities {
			if activities[i].Worker == "system" && activities[i].Kind == "text" {
				result := activities[i]
				return &result
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}
