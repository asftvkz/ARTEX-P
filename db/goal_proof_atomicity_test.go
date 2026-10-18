package db

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestProveGoalRollsBackProofEdgeWhenGoalUpdateFails(t *testing.T) {
	d := taskCompletionDB(t)
	task, err := d.CreateTask("目标证明原子性", "验证证明关系与目标状态一起提交", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	store := d.Exploration(task.ExplorationID)
	goalID, err := store.AddGoal(map[string]any{"text": "观察到测试目标达成"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	evidenceID, err := store.AddNode(KindFact, map[string]any{"summary": "已观察到目标达成", "confidence": "observed"}, 0, "confirmed", "test", nil)
	if err != nil {
		t.Fatal(err)
	}

	// 只拒绝当前测试目标的 met 更新，让证明边写入成功后第二步失败。
	constraint := fmt.Sprintf("test_proof_update_failure_%d", goalID)
	if _, err := d.Exec(fmt.Sprintf(`ALTER TABLE exploration_nodes ADD CONSTRAINT %s CHECK (id <> %d OR state <> 'met') NOT VALID`, constraint, goalID)); err != nil {
		t.Fatal(err)
	}
	dropConstraint := fmt.Sprintf(`ALTER TABLE exploration_nodes DROP CONSTRAINT IF EXISTS %s`, constraint)
	t.Cleanup(func() { _, _ = d.Exec(dropConstraint) })
	err = store.ProveGoal(evidenceID, goalID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != constraint {
		t.Fatalf("目标更新没有命中预设约束: %v", err)
	}
	goal, err := store.GetNode(goalID)
	if err != nil {
		t.Fatal(err)
	}
	var edges int
	if err := d.QueryRow(`SELECT count(*) FROM exploration_edges WHERE exploration_id=$1 AND src_id=$2 AND rel='proves' AND dst_id=$3`, task.ExplorationID, evidenceID, goalID).Scan(&edges); err != nil {
		t.Fatal(err)
	}
	if goal.State != "open" || edges != 0 {
		t.Fatalf("目标更新失败后遗留了部分证明: state=%q edges=%d", goal.State, edges)
	}

	if _, err := d.Exec(dropConstraint); err != nil {
		t.Fatal(err)
	}
	if err := store.ProveGoal(evidenceID, goalID); err != nil {
		t.Fatalf("解除故障后无法重新证明: %v", err)
	}
	goal, err = store.GetNode(goalID)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT count(*) FROM exploration_edges WHERE exploration_id=$1 AND src_id=$2 AND rel='proves' AND dst_id=$3`, task.ExplorationID, evidenceID, goalID).Scan(&edges); err != nil {
		t.Fatal(err)
	}
	if goal.State != "met" || edges != 1 {
		t.Fatalf("重新证明未原子提交: state=%q edges=%d", goal.State, edges)
	}
}
