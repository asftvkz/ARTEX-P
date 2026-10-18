package db

import (
	"encoding/json"
	"testing"
)

func TestGoalProofRequiresCapturedFlagOnlyForAcquisition(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{name: "acquire", text: "获取 flag", want: true},
		{name: "investigate failure", text: "分析为何无法获取 flag", want: false},
		{name: "check possibility", text: "确认是否能获得 flag", want: false},
		{name: "check possibility with longer prefix", text: "确认是否确实能够获得 flag", want: false},
		{name: "target is not get", text: "检查 target flag 接口是否配置正确", want: false},
		{name: "find flag", text: "找到 flag", want: true},
		{name: "extract flag", text: "提取 flag", want: true},
		{name: "english", text: "get flag from challenge", want: true},
		{name: "english investigation", text: "investigate why we cannot get flag", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload, _ := json.Marshal(map[string]string{"text": tt.text})
			if got := GoalProofRequiresCapturedFlag(payload); got != tt.want {
				t.Fatalf("GoalProofRequiresCapturedFlag(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

func TestFlagAcquisitionPreservesRelatedSubgoalMeaning(t *testing.T) {
	for _, text := range []string{"验证提权链路", "读取 /flag 文件", "确认管理员权限", "检查 flag 接口是否配置正确", "分析为何无法获取 flag"} {
		payload, _ := json.Marshal(map[string]string{"text": text})
		if GoalProofRequiresCapturedFlag(payload) {
			t.Errorf("flag acquisition must not expand the evidence contract for subgoal %q", text)
		}
		if !GoalProofEvidenceValid(payload, json.RawMessage(`{"confidence":"observed","evidence":"HTTP 403"}`)) {
			t.Errorf("ordinary subgoal %q must retain its original evidence contract", text)
		}
	}
	payload := json.RawMessage(`{"text":"获取 flag"}`)
	if !GoalProofRequiresCapturedFlag(payload) {
		t.Fatal("an explicit acquisition subgoal must require a captured flag")
	}
	if GoalProofEvidenceValid(payload, json.RawMessage(`{"confidence":"observed","evidence":"HTTP 403，未获取到 flag"}`)) {
		t.Fatal("an explicit acquisition subgoal must reject failure evidence")
	}
}

func TestPositiveFlagEvidenceRequiresObservedOutput(t *testing.T) {
	tests := []struct {
		name    string
		payload map[string]any
		want    bool
	}{
		{
			name: "placeholder example rejected",
			payload: map[string]any{
				"summary":         "README 格式示例 flag{YOUR_FLAG_HERE}",
				"evidence":        "README 示例 flag{YOUR_FLAG_HERE}",
				"confidence":      "observed",
				"captured_flag":   "flag{YOUR_FLAG_HERE}",
				"observed_output": "README 示例输出 flag{YOUR_FLAG_HERE}",
			},
		},
		{
			name: "summary only rejected",
			payload: map[string]any{
				"summary":         "flag{example}",
				"detail":          "文档示例，不是目标输出",
				"evidence":        "未执行目标提取",
				"confidence":      "observed",
				"captured_flag":   "flag{example}",
				"observed_output": "文档示例 flag{example}",
			},
		},
		{
			name: "inferred rejected",
			payload: map[string]any{
				"summary":         "推测值 flag{synthetic_test_value_42}",
				"evidence":        "推测得到 flag{synthetic_test_value_42}",
				"confidence":      "inferred",
				"captured_flag":   "flag{synthetic_test_value_42}",
				"observed_output": "推测得到 flag{synthetic_test_value_42}",
			},
		},
		{
			name: "request parameter is not observed output",
			payload: map[string]any{
				"confidence":      "observed",
				"captured_flag":   "flag{synthetic_test_value_42}",
				"observed_output": "curl --data 'flag{synthetic_test_value_42}' https://target/flag -> HTTP 403",
			},
		},
		{
			name: "legacy observed fact accepted",
			payload: map[string]any{
				"confidence": "observed",
				"summary":    "已取得成果",
				"evidence":   "HTTP 200 body: flag{synthetic_test_value_42}",
			},
			want: true,
		},
		{
			name: "legacy finding output accepted",
			payload: map[string]any{
				"vulnclass": "CTF",
				"summary":   "已取得成果",
				"evidence":  map[string]any{"poc": "response body contains flag{synthetic_test_value_42}"},
			},
			want: true,
		},
		{
			name: "legacy summary alone rejected",
			payload: map[string]any{
				"confidence": "observed",
				"summary":    "flag{synthetic_test_value_42}",
				"evidence":   "HTTP 403，未获取到 flag",
			},
		},
		{
			name: "legacy inferred fact rejected",
			payload: map[string]any{
				"confidence": "inferred",
				"evidence":   "HTTP 200 body: flag{synthetic_test_value_42}",
			},
		},
		{
			name: "legacy request input rejected",
			payload: map[string]any{
				"confidence": "observed",
				"evidence":   "curl --data 'flag{synthetic_test_value_42}' https://target/flag -> HTTP 403",
			},
		},
		{
			name: "request body is not response body",
			payload: map[string]any{
				"confidence": "observed",
				"evidence":   "request body: flag{synthetic_test_value_42}\nHTTP 200 body: ok",
			},
		},
		{
			name: "raw output accepted",
			payload: map[string]any{
				"confidence":      "observed",
				"captured_flag":   "flag{synthetic_test_value_42}",
				"observed_output": "flag{synthetic_test_value_42}",
			},
			want: true,
		},
		{
			name: "raw JSON output accepted",
			payload: map[string]any{
				"confidence":      "observed",
				"captured_flag":   "flag{synthetic_test_value_42}",
				"observed_output": `{"flag":"flag{synthetic_test_value_42}"}`,
			},
			want: true,
		},
		{
			name: "raw HTML output accepted",
			payload: map[string]any{
				"confidence":      "observed",
				"captured_flag":   "flag{synthetic_test_value_42}",
				"observed_output": `<html><body>flag{synthetic_test_value_42}</body></html>`,
			},
			want: true,
		},
		{
			name: "HTTP response with raw JSON output accepted",
			payload: map[string]any{
				"confidence":      "observed",
				"captured_flag":   "flag{synthetic_test_value_42}",
				"observed_output": "HTTP/1.1 200 OK\nContent-Type: application/json\n\n{\"flag\":\"flag{synthetic_test_value_42}\"}",
			},
			want: true,
		},
		{
			name: "legacy HTTP response with raw JSON output accepted",
			payload: map[string]any{
				"confidence": "observed",
				"evidence":   "HTTP/1.1 200 OK\nContent-Type: application/json\n\n{\"flag\":\"flag{synthetic_test_value_42}\"}",
			},
			want: true,
		},
		{
			name: "raw request body rejected",
			payload: map[string]any{
				"confidence":      "observed",
				"captured_flag":   "flag{synthetic_test_value_42}",
				"observed_output": "request body:\n{\"flag\":\"flag{synthetic_test_value_42}\"}\nHTTP/1.1 200 OK\n{}",
			},
		},
		{
			name: "failed raw JSON response rejected",
			payload: map[string]any{
				"confidence":      "observed",
				"captured_flag":   "flag{synthetic_test_value_42}",
				"observed_output": "HTTP/1.1 403 Forbidden\n{\"flag\":\"flag{synthetic_test_value_42}\"}",
			},
		},
		{
			name: "command result accepted",
			payload: map[string]any{
				"confidence": "observed",
				"evidence":   "cat /flag -> flag{synthetic_test_value_42}",
			},
			want: true,
		},
		{
			name: "failure followed by actual output accepted",
			payload: map[string]any{
				"confidence": "observed",
				"evidence":   "HTTP/1.1 403 Forbidden\nHTTP 200 response body: flag{synthetic_test_value_42}",
			},
			want: true,
		},
		{
			name: "failed response body rejected",
			payload: map[string]any{
				"confidence": "observed",
				"evidence":   "HTTP/1.1 403 Forbidden\nbody: flag{synthetic_test_value_42}",
			},
		},
		{
			name: "legacy documentation example rejected",
			payload: map[string]any{
				"confidence": "observed",
				"evidence":   "README 示例 response body contains flag{synthetic_test_value_42}",
			},
		},
		{
			name: "new structure cannot fall back to legacy evidence",
			payload: map[string]any{
				"confidence":      "observed",
				"evidence":        "HTTP 200 body: flag{synthetic_test_value_42}",
				"captured_flag":   "flag{different_value}",
				"observed_output": "HTTP 200 body: flag{synthetic_test_value_42}",
			},
		},
		{
			name: "partial new structure rejected",
			payload: map[string]any{
				"confidence":    "observed",
				"evidence":      "HTTP 200 body: flag{synthetic_test_value_42}",
				"captured_flag": "flag{synthetic_test_value_42}",
			},
		},
		{
			name: "malformed confidence rejected for finding",
			payload: map[string]any{
				"vulnclass":  "CTF",
				"confidence": 1,
				"evidence":   map[string]any{"poc": "response body contains flag{synthetic_test_value_42}"},
			},
		},
		{
			name: "later observed value accepted despite prior miss",
			payload: map[string]any{
				"summary":         "先未发现 flag，之后从响应中读取到目标值",
				"detail":          "最初读取失败，重试后得到了实际输出",
				"evidence":        "HTTP 200 body: flag{synthetic_test_value_42}",
				"confidence":      "observed",
				"captured_flag":   "flag{synthetic_test_value_42}",
				"observed_output": "HTTP 200 response body: flag{synthetic_test_value_42}",
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload, err := json.Marshal(tt.payload)
			if err != nil {
				t.Fatal(err)
			}
			if got := PositiveFlagEvidence(payload); got != tt.want {
				t.Fatalf("PositiveFlagEvidence() = %v, want %v; payload=%s", got, tt.want, payload)
			}
		})
	}
}
