package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// CompleteTaskIfIdle 保留旧调用契约；新调用方需要同时取得实际终态时使用
// CompleteTaskIfIdleStatus。
func (d *DB) CompleteTaskIfIdle(id int64) (bool, error) {
	won, _, err := d.CompleteTaskIfIdleStatus(id)
	return won, err
}

// CompleteTaskIfIdleStatus 在任务和探索根锁内重新核验目标证明及活动意图。
// 没有有效证明的 met 目标会恢复为 open；失败意图已排空且没有未完成目标时，
// 只有目标全部有正向证明才标 done，否则失败意图将任务标为 failed。
func (d *DB) CompleteTaskIfIdleStatus(id int64) (bool, string, error) {
	tx, err := d.Begin()
	if err != nil {
		return false, "", err
	}
	defer tx.Rollback()
	var expID int64
	var currentStatus string
	var paused, queued bool
	if err := tx.QueryRow(`SELECT tasks.exploration_id,tasks.status,tasks.paused,tasks.queued FROM tasks WHERE id=$1 FOR UPDATE`, id).Scan(&expID, &currentStatus, &paused, &queued); err != nil {
		return false, "", err
	}
	var root int64
	// 根锁串行化图写入，但保留外键 KEY SHARE，避免与持意图锁的取消审计互等。
	if err := tx.QueryRow(`SELECT id FROM explorations WHERE id=$1 FOR NO KEY UPDATE`, expID).Scan(&root); err != nil {
		return false, "", err
	}
	if paused || queued || currentStatus == "done" || currentStatus == "failed" || currentStatus == "timeout" {
		return false, currentStatus, nil
	}
	// 状态重开/领取也会更新这些行；先锁住现有 goal/intent，避免计数后并发
	// 重开一个 blocked intent，却仍按旧快照把任务收尾。新节点受探索根锁保护。
	lockedRows, err := tx.Query(`SELECT id FROM exploration_nodes WHERE exploration_id=$1 AND kind IN ('goal','intent') ORDER BY id FOR UPDATE`, expID)
	if err != nil {
		return false, "", err
	}
	for lockedRows.Next() {
		var nodeID int64
		if err := lockedRows.Scan(&nodeID); err != nil {
			lockedRows.Close()
			return false, "", err
		}
	}
	if err := lockedRows.Err(); err != nil {
		lockedRows.Close()
		return false, "", err
	}
	lockedRows.Close()

	// 先校验 met 目标的 proves 边及证据内容。无效历史标记会被恢复为
	// open，保留原 proves 边供审计；Planner 下一轮可继续处理它。
	invalidGoals, validMetGoals, err := invalidMetGoalIDs(tx, expID)
	if err != nil {
		return false, "", err
	}
	for _, goalID := range invalidGoals {
		if _, err := tx.Exec(`UPDATE exploration_nodes SET state='open',content_version=content_version+1,blocked_reason=NULL WHERE id=$1 AND exploration_id=$2 AND kind='goal' AND state='met'`, goalID, expID); err != nil {
			return false, "", err
		}
	}
	if len(invalidGoals) > 0 {
		if err := appendInvalidGoalProofActivity(tx, expID, invalidGoals); err != nil {
			return false, "", err
		}
	}
	var openGoals, activeIntents, failedIntents int
	if err := tx.QueryRow(`SELECT
		COUNT(*) FILTER (WHERE kind='goal' AND state='open'),
		COUNT(*) FILTER (WHERE kind='intent' AND state IN ('open','running')),
		COUNT(*) FILTER (WHERE kind='intent' AND state IN ('blocked','exhausted','stopped'))
		FROM exploration_nodes WHERE exploration_id=$1`, expID).Scan(&openGoals, &activeIntents, &failedIntents); err != nil {
		return false, "", err
	}
	if openGoals > 0 || activeIntents > 0 {
		if len(invalidGoals) > 0 {
			if err := tx.Commit(); err != nil {
				return false, "", err
			}
		}
		return false, currentStatus, nil
	}
	status := "done"
	if failedIntents > 0 && validMetGoals == 0 {
		status = "failed"
	}
	res, err := tx.Exec(`UPDATE tasks SET status=$2,completed_at=COALESCE(completed_at,now())
		WHERE id=$1 AND status NOT IN ('done','failed','timeout') AND NOT paused AND NOT queued
		AND NOT EXISTS (SELECT 1 FROM exploration_nodes node WHERE node.exploration_id=$3 AND
			((node.kind='goal' AND node.state='open') OR (node.kind='intent' AND node.state IN ('open','running'))))`, id, status, expID)
	if err != nil {
		return false, "", err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, "", err
	}
	if err := tx.Commit(); err != nil {
		return false, "", err
	}
	return n == 1, status, nil
}

// invalidMetGoalIDs 查找缺少本任务或直接关联任务有效证据的 met 目标。
func invalidMetGoalIDs(tx *sql.Tx, expID int64) ([]int64, int, error) {
	rows, err := tx.Query(`SELECT id,payload FROM exploration_nodes WHERE exploration_id=$1 AND kind='goal' AND state='met' ORDER BY id`, expID)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	type goalProof struct {
		id      int64
		payload json.RawMessage
	}
	var goals []goalProof
	for rows.Next() {
		var goal goalProof
		if err := rows.Scan(&goal.id, &goal.payload); err != nil {
			rows.Close()
			return nil, 0, err
		}
		goals = append(goals, goal)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, 0, err
	}
	rows.Close()
	var invalid []int64
	valid := 0
	for _, goal := range goals {
		proofRows, err := tx.Query(`
SELECT proof.state,proof.payload
FROM exploration_edges edge
JOIN exploration_nodes proof ON proof.id=edge.src_id AND proof.kind IN ('fact','finding')
WHERE edge.exploration_id=$1 AND edge.dst_id=$2 AND edge.rel='proves'
AND (proof.exploration_id=$1 OR EXISTS (
	SELECT 1 FROM task_relations relation
	JOIN tasks owner ON owner.id=relation.task_id AND owner.deleted_at IS NULL
	JOIN tasks source ON source.id=relation.source_task_id AND source.deleted_at IS NULL
	WHERE owner.exploration_id=$1 AND source.exploration_id=proof.exploration_id
	))`, expID, goal.id)
		if err != nil {
			return nil, 0, err
		}
		validProof := false
		for proofRows.Next() {
			var state string
			var evidence json.RawMessage
			if err := proofRows.Scan(&state, &evidence); err != nil {
				proofRows.Close()
				return nil, 0, err
			}
			if state == "confirmed" && GoalProofEvidenceValid(goal.payload, evidence) {
				validProof = true
			}
		}
		if err := proofRows.Err(); err != nil {
			proofRows.Close()
			return nil, 0, err
		}
		proofRows.Close()
		if validProof {
			valid++
		} else {
			invalid = append(invalid, goal.id)
		}
	}
	return invalid, valid, nil
}

func appendInvalidGoalProofActivity(tx *sql.Tx, expID int64, goalIDs []int64) error {
	if len(goalIDs) == 0 {
		return nil
	}
	ids := make([]string, len(goalIDs))
	for i, id := range goalIDs {
		ids[i] = fmt.Sprintf("#%d", id)
	}
	detail := "无有效 proves 关系或证据不满足目标要求；旧 proves 关系保留用于审计。目标已重新打开，Planner 将重新核验。"
	_, err := tx.Exec(`INSERT INTO activity(exploration_id,worker,kind,is_error,summary,detail)
VALUES($1,'system','text',true,'检测到无效目标证明，已恢复目标并继续规划',$2)`, expID, fmt.Sprintf("重新打开的目标：%s。%s", strings.Join(ids, "、"), detail))
	return err
}
