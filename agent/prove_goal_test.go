package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

func callProveGoal(t *testing.T, store *db.ExplorationStore, goalID, evidenceID int64) actool.Result {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"goal_id": goalID, "evidence_id": evidenceID, "reason": "测试证据与目标相符"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := NewToolSet(store, "planner").proveGoal().Call(context.Background(), raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func proveGoalFixture(t *testing.T, goalText string, evidencePayload map[string]any) (*db.ExplorationStore, int64, int64) {
	t.Helper()
	d := testDB(t)
	expID, err := d.CreateExploration("prove_goal 证据验证", "只访问隔离数据库")
	if err != nil {
		_ = d.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
		_ = d.Close()
	})
	store := d.Exploration(expID)
	goalID, err := store.AddGoal(map[string]any{"text": goalText}, "test")
	if err != nil {
		t.Fatal(err)
	}
	evidenceID, err := store.AddNode(db.KindFact, evidencePayload, 0, "confirmed", "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	return store, goalID, evidenceID
}

func TestProveGoalRejectsNoFlagEvidenceForFlagGoal(t *testing.T) {
	store, goalID, evidenceID := proveGoalFixture(t,
		"获取 flag 并完整记录证据",
		map[string]any{
			"summary":    "提权面已封死，flag 0 命中",
			"detail":     "所有已检查路径均为 EACCES，未发现 flag。",
			"evidence":   "uid1000；posix_seteuid(0)=false；flag hits=0",
			"confidence": "observed",
		},
	)
	result := callProveGoal(t, store, goalID, evidenceID)
	goal, err := store.GetNode(goalID)
	if err != nil {
		t.Fatal(err)
	}
	if goal.State != "open" {
		t.Fatalf("negative evidence changed goal state to %q: %s", goal.State, result.Flatten())
	}
	edges, err := store.EdgesTouching([]int64{goalID, evidenceID})
	if err != nil {
		t.Fatal(err)
	}
	for _, edge := range edges {
		if edge.From == evidenceID && edge.To == goalID && edge.Rel == db.RelProves {
			t.Fatalf("negative evidence created proves edge: %s", result.Flatten())
		}
	}
	if !result.IsError {
		t.Fatalf("negative evidence should return a visible tool error: %s", result.Flatten())
	}
}

func TestFlagGoalRequiresPositiveEvidenceOnlyForAcquisitionObjectives(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{name: "acquire flag", text: "获取 flag", want: true},
		{name: "analyze failure", text: "分析为何无法获取 flag", want: false},
		{name: "check possibility", text: "确认是否能获得 flag", want: false},
		{name: "target word is not get", text: "检查 target flag 接口是否配置正确", want: false},
		{name: "english get flag", text: "get flag from challenge", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload, err := json.Marshal(map[string]any{"text": tt.text})
			if err != nil {
				t.Fatal(err)
			}
			if got := flagGoalRequiresPositiveEvidence(payload); got != tt.want {
				t.Fatalf("flag goal classification for %q = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

func TestProveGoalAcceptsObservedFlagEvidence(t *testing.T) {
	store, goalID, evidenceID := proveGoalFixture(t,
		"获取 flag 并完整记录证据",
		map[string]any{
			"summary":         "通过授权测试环境返回 flag{synthetic_test_value_42}",
			"detail":          "原始响应中直接观察到 flag{synthetic_test_value_42}。",
			"evidence":        "HTTP 200 body: flag{synthetic_test_value_42}",
			"confidence":      "observed",
			"captured_flag":   "flag{synthetic_test_value_42}",
			"observed_output": "HTTP 200 body: flag{synthetic_test_value_42}",
		},
	)
	if result := callProveGoal(t, store, goalID, evidenceID); result.IsError {
		t.Fatalf("valid flag evidence rejected: %s", result.Flatten())
	}
	goal, err := store.GetNode(goalID)
	if err != nil {
		t.Fatal(err)
	}
	if goal.State != "met" {
		t.Fatalf("positive observed flag evidence left goal in %q", goal.State)
	}
}

func TestProveGoalRejectsPlaceholderOrUnobservedFlagEvidence(t *testing.T) {
	tests := []struct {
		name    string
		payload map[string]any
	}{
		{
			name: "placeholder in evidence",
			payload: map[string]any{
				"summary":         "文档格式示例，实际未执行提取",
				"detail":          "README 格式示例 flag{YOUR_FLAG_HERE}",
				"evidence":        "README 示例输出 flag{YOUR_FLAG_HERE}",
				"confidence":      "observed",
				"captured_flag":   "flag{YOUR_FLAG_HERE}",
				"observed_output": "README 示例输出 flag{YOUR_FLAG_HERE}",
			},
		},
		{
			name: "flag only in summary",
			payload: map[string]any{
				"summary":         "README 示例 flag{example}",
				"detail":          "未执行目标提取",
				"evidence":        "本轮没有实际读取目标输出",
				"confidence":      "observed",
				"captured_flag":   "flag{example}",
				"observed_output": "README 示例 flag{example}",
			},
		},
		{
			name: "not directly observed",
			payload: map[string]any{
				"summary":         "推测 flag 为 flag{synthetic_test_value_42}",
				"evidence":        "推断得到 flag{synthetic_test_value_42}",
				"confidence":      "inferred",
				"captured_flag":   "flag{synthetic_test_value_42}",
				"observed_output": "推断得到 flag{synthetic_test_value_42}",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, goalID, evidenceID := proveGoalFixture(t, "获取 flag 并完整记录证据", tt.payload)
			result := callProveGoal(t, store, goalID, evidenceID)
			goal, err := store.GetNode(goalID)
			if err != nil {
				t.Fatal(err)
			}
			if goal.State != "open" || !result.IsError {
				t.Fatalf("placeholder/unobserved evidence accepted: state=%q result=%s", goal.State, result.Flatten())
			}
		})
	}
}

func TestProveGoalAcceptsFlagAfterEarlierNegativeAttempt(t *testing.T) {
	store, goalID, evidenceID := proveGoalFixture(t,
		"获取 flag 并完整记录证据",
		map[string]any{
			"summary":         "先未发现 flag，之后从授权环境响应中读取到 flag{synthetic_test_value_42}",
			"detail":          "首次尝试没有发现 flag；随后 HTTP 200 原始响应正文包含 flag{synthetic_test_value_42}。",
			"evidence":        "HTTP 200 response body: flag{synthetic_test_value_42}",
			"confidence":      "observed",
			"captured_flag":   "flag{synthetic_test_value_42}",
			"observed_output": "HTTP 200 response body: flag{synthetic_test_value_42}",
		},
	)
	if result := callProveGoal(t, store, goalID, evidenceID); result.IsError {
		t.Fatalf("positive observed flag evidence after an earlier negative attempt was rejected: %s", result.Flatten())
	}
}

func TestProveGoalKeepsOrdinaryGoalEvidenceRules(t *testing.T) {
	store, goalID, evidenceID := proveGoalFixture(t,
		"证明指定端点返回服务版本",
		map[string]any{"summary": "响应头直接返回 Server: nginx", "confidence": "observed"},
	)
	if result := callProveGoal(t, store, goalID, evidenceID); result.IsError {
		t.Fatalf("ordinary evidence rejected: %s", result.Flatten())
	}
	goal, err := store.GetNode(goalID)
	if err != nil {
		t.Fatal(err)
	}
	if goal.State != "met" {
		t.Fatalf("ordinary goal evidence left goal in %q", goal.State)
	}
}
