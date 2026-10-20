package db

import (
	"context"
	"testing"
	"time"
)

func taskCompletionDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestCompleteTaskIfIdleClassifiesUnsuccessfulWork(t *testing.T) {
	d := taskCompletionDB(t)
	cases := []struct {
		name       string
		intent     string
		wantStatus string
		wantWon    bool
	}{
		{name: "goalless blocked intent fails task", intent: "blocked", wantStatus: "failed", wantWon: true},
		{name: "goalless exhausted intent fails task", intent: "exhausted", wantStatus: "failed", wantWon: true},
		{name: "clean goalless task completes", wantStatus: "done", wantWon: true},
		{name: "open intent prevents settlement", intent: "open", wantStatus: "running", wantWon: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task, err := d.CreateTask("完成门禁回归", "隔离测试目标", nil, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
			store := d.Exploration(task.ExplorationID)
			if tc.intent != "" {
				if _, err := store.AddNode(KindIntent, map[string]any{"summary": "终态分类测试"}, 0, tc.intent, "test", nil); err != nil {
					t.Fatal(err)
				}
			}
			if err := d.SetStatus(task.ID, "running"); err != nil {
				t.Fatal(err)
			}

			won, status, err := d.CompleteTaskIfIdleStatus(task.ID)
			if err != nil {
				t.Fatal(err)
			}
			got, err := d.GetTask(task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if won != tc.wantWon || status != tc.wantStatus || got.Status != tc.wantStatus {
				t.Fatalf("settlement=(%v,%q), persisted=%q want=(%v,%q)", won, status, got.Status, tc.wantWon, tc.wantStatus)
			}
		})
	}
}

func TestCompleteTaskIfIdleAllowsValidProofDespiteUnrelatedFailure(t *testing.T) {
	d := taskCompletionDB(t)
	task, err := d.CreateTask("有效证明不受旁支失败影响", "普通目标", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	store := d.Exploration(task.ExplorationID)
	goalID, err := store.AddGoal(map[string]any{"text": "测试目标已有正向证明"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	evidenceID, err := store.AddNode(KindFact, map[string]any{"summary": "观察到目标已达成"}, 0, "confirmed", "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ProveGoal(evidenceID, goalID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddNode(KindIntent, map[string]any{"summary": "旁支失败"}, 0, "blocked", "test", nil); err != nil {
		t.Fatal(err)
	}
	if err := d.SetStatus(task.ID, "running"); err != nil {
		t.Fatal(err)
	}
	won, status, err := d.CompleteTaskIfIdleStatus(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !won || status != "done" {
		t.Fatalf("valid proved goal did not complete despite unrelated failed intent: won=%v status=%q", won, status)
	}
}

func TestCompleteTaskIfIdleReopensMetGoalWithoutValidProof(t *testing.T) {
	d := taskCompletionDB(t)
	task, err := d.CreateTask("无效证明回归", "获取 flag", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	store := d.Exploration(task.ExplorationID)
	goalID, err := store.AddGoal(map[string]any{"text": "获取 flag"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	evidenceID, err := store.AddNode(KindFact, map[string]any{
		"summary":    "README 格式示例 flag{YOUR_FLAG_HERE}",
		"evidence":   "README 示例 flag{YOUR_FLAG_HERE}",
		"confidence": "observed",
	}, 0, "confirmed", "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO exploration_edges(exploration_id,src_id,rel,dst_id) VALUES($1,$2,'proves',$3)`, task.ExplorationID, evidenceID, goalID); err != nil {
		t.Fatal(err)
	}
	if err := store.SetNodeState(goalID, "met"); err != nil {
		t.Fatal(err)
	}
	if err := d.SetStatus(task.ID, "running"); err != nil {
		t.Fatal(err)
	}
	won, status, err := d.CompleteTaskIfIdleStatus(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	goal, err := store.GetNode(goalID)
	if err != nil {
		t.Fatal(err)
	}
	if won || status != "running" || goal.State != "open" {
		t.Fatalf("invalid proof was treated as completion: won=%v status=%q goal=%q", won, status, goal.State)
	}
}

func TestCompleteTaskIfIdlePreservesHistoricalTerminalTask(t *testing.T) {
	d := taskCompletionDB(t)
	task, err := d.CreateTask("历史错误收尾恢复", "获取 flag", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	store := d.Exploration(task.ExplorationID)
	goalID, err := store.AddGoal(map[string]any{"text": "获取 flag"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	evidenceID, err := store.AddNode(KindFact, map[string]any{
		"summary":    "提权权限受限，没有找到 flag",
		"evidence":   "读取 /flag 返回 EACCES；flag 命中 0",
		"confidence": "observed",
	}, 0, "confirmed", "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO exploration_edges(exploration_id,src_id,rel,dst_id) VALUES($1,$2,'proves',$3)`, task.ExplorationID, evidenceID, goalID); err != nil {
		t.Fatal(err)
	}
	if err := store.SetNodeState(goalID, "met"); err != nil {
		t.Fatal(err)
	}
	if err := d.SetStatus(task.ID, "done"); err != nil {
		t.Fatal(err)
	}
	won, status, err := d.CompleteTaskIfIdleStatus(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	gotTask, err := d.GetTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	goal, err := store.GetNode(goalID)
	if err != nil {
		t.Fatal(err)
	}
	if won || status != "done" || gotTask.Status != "done" || goal.State != "met" {
		t.Fatalf("historical terminal task changed: won=%v status=%q persisted=%q goal=%q", won, status, gotTask.Status, goal.State)
	}
}

func TestCompleteTaskIfIdleAllowsCancelAuditWhileWaitingForIntent(t *testing.T) {
	d := taskCompletionDB(t)
	task, err := d.CreateTask("取消审计与完成并发", "验证完成事务不会阻塞取消审计", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	intentID, err := d.Exploration(task.ExplorationID).AddNode(KindIntent, map[string]any{"summary": "已退出的工作"}, 0, "done", "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetStatus(task.ID, "running"); err != nil {
		t.Fatal(err)
	}

	completionDB, completionPID := openSingleConnectionTestDB(t, testDSN(t))
	cancelTx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer cancelTx.Rollback()
	if _, err := cancelTx.Exec(`SELECT id FROM exploration_nodes WHERE id=$1 FOR UPDATE`, intentID); err != nil {
		t.Fatal(err)
	}
	var won bool
	var status string
	completionDone := make(chan error, 1)
	go func() {
		var completionErr error
		won, status, completionErr = completionDB.CompleteTaskIfIdleStatus(task.ID)
		completionDone <- completionErr
	}()
	if err := waitForBackendBlock(d, completionPID, completionDone); err != nil {
		t.Fatal(err)
	}

	// 模拟 CancelIntent 持意图行锁后插入 Token 审计：探索外键需要 KEY SHARE。
	// 收尾虽然持有探索根锁并等待意图，也不能阻止取消事务取得这把外键锁。
	if _, err := cancelTx.Exec(`SELECT id FROM explorations WHERE id=$1 FOR KEY SHARE NOWAIT`, task.ExplorationID); err != nil {
		t.Fatalf("完成事务等待意图期间阻塞了取消审计所需的探索外键锁: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := cancelTx.ExecContext(ctx, `INSERT INTO activity(exploration_id,worker,kind,summary) VALUES($1,'token-ledger','result','取消意图的 Token 计量')`, task.ExplorationID); err != nil {
		t.Fatalf("完成事务等待意图期间无法插入取消审计: %v", err)
	}
	if err := cancelTx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := waitForMutationResult(completionDone); err != nil {
		t.Fatalf("取消审计退出后完成事务未能收尾: %v", err)
	}
	if !won || status != "done" {
		t.Fatalf("取消审计退出后收尾结果错误: won=%v status=%q", won, status)
	}
	var audits int
	if err := d.QueryRow(`SELECT count(*) FROM activity WHERE exploration_id=$1 AND worker='token-ledger'`, task.ExplorationID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("取消审计没有独立提交: count=%d", audits)
	}
}
