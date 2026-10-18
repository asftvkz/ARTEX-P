package db

import (
	"encoding/json"
	"regexp"
	"strings"
)

var (
	goalFlagPattern                = regexp.MustCompile(`(?i)(^|[^a-z])(get|obtain|capture|retrieve|acquire|find|extract|recover|steal)\s+(?:the\s+)?flag([^a-z]|$)`)
	observedFlagPattern            = regexp.MustCompile(`(?i)flag\{[^}\r\n]{1,256}\}`)
	httpEvidenceStatusPattern      = regexp.MustCompile(`(?i)\bHTTP(?:/[0-9]+(?:\.[0-9]+)?)?\s+([1-5][0-9]{2})\b`)
	evidenceResultSeparatorPattern = regexp.MustCompile(`\s+(?:->|=>|→)\s*`)
	evidenceRequestPattern         = regexp.MustCompile(`(?i)^(?:(?:\$|>)\s+|curl\s|wget\s|cat\s|echo\s|printf\s|(?:GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\s+\S|(?:HTTP\s+)?request(?:\s+body)?\s*[:=])`)
	flagOutputPrefixPattern        = regexp.MustCompile(`(?i)(?:\b(?:response(?:\s+body)?|body|stdout|output)\s*(?:[:=]|contains\b)|响应(?:体|内容)?\s*[:：=]|输出\s*[:：=])`)
)

// GoalProofRequiresCapturedFlag 判断子目标是否明确要求取得 flag。
// 仅调查获取失败原因或获取可能性的目标不适用此要求。
func GoalProofRequiresCapturedFlag(goalPayload json.RawMessage) bool {
	var goal struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(goalPayload, &goal) != nil {
		return false
	}
	return explicitlyAcquiresFlag(goal.Text)
}

func explicitlyAcquiresFlag(text string) bool {
	text = strings.ToLower(strings.TrimSpace(text))
	if !strings.Contains(text, "flag") {
		return false
	}
	for _, verb := range []string{"获取", "取得", "拿到", "获得", "找到", "提取", "捕获", "取出", "解出", "拿下"} {
		if index := strings.Index(text, verb); index >= 0 {
			prefix := []rune(text[:index])
			if containsAny(string(prefix[max(0, len(prefix)-12):]), "无法", "不能", "未能", "未", "没有", "能否", "是否", "为何", "为什么", "原因") {
				continue
			}
			return true
		}
	}
	match := goalFlagPattern.FindStringSubmatchIndex(text)
	if match == nil {
		return false
	}
	prefix := text[:match[2]]
	return !containsAny(prefix[max(0, len(prefix)-24):], "unable", "cannot", "can't", "whether", "why", "not", "never")
}

// GoalProofEvidenceValid 供目标证明与任务收尾共用验收条件。
// 获取 flag 的目标需要观测输出中的实际值，摘要不作为证明。
func GoalProofEvidenceValid(goalPayload, evidencePayload json.RawMessage) bool {
	if !GoalProofRequiresCapturedFlag(goalPayload) {
		return true
	}
	return PositiveFlagEvidence(evidencePayload)
}

// PositiveFlagEvidence 校验实际观测到的 flag 输出，供工具层与完成门禁共用。
// 新证据采用独立字段；旧事实的 evidence 和漏洞的 evidence.poc 仍可使用。
func PositiveFlagEvidence(evidencePayload json.RawMessage) bool {
	var object map[string]any
	if json.Unmarshal(evidencePayload, &object) != nil || object == nil {
		return false
	}
	if rawConfidence, exists := object["confidence"]; exists {
		confidence, ok := rawConfidence.(string)
		if !ok || !strings.EqualFold(strings.TrimSpace(confidence), "observed") {
			return false
		}
	} else if vulnclass, ok := object["vulnclass"].(string); !ok || strings.TrimSpace(vulnclass) == "" {
		return false
	}
	rawFlag, flagExists := object["captured_flag"]
	rawOutput, outputExists := object["observed_output"]
	if flagExists || outputExists {
		capturedFlag, flagOK := rawFlag.(string)
		observedOutput, outputOK := rawOutput.(string)
		capturedFlag = strings.TrimSpace(capturedFlag)
		// 新字段不完整或互相矛盾时直接拒绝，不能再借旧字段绕过校验。
		return flagOK && outputOK && validCapturedFlag(capturedFlag) && observedFlagMatchesOutput(observedOutput, capturedFlag, true)
	}
	var output string
	switch evidence := object["evidence"].(type) {
	case string:
		output = evidence
	case map[string]any:
		output, _ = evidence["poc"].(string)
	}
	for _, capturedFlag := range observedFlagPattern.FindAllString(output, -1) {
		if validCapturedFlag(capturedFlag) && observedFlagMatchesOutput(output, capturedFlag, false) {
			return true
		}
	}
	return false
}

func validCapturedFlag(value string) bool {
	return observedFlagPattern.FindString(value) == value && value != "" && !isFlagPlaceholder(value[5:len(value)-1])
}

func observedFlagMatchesOutput(output, capturedFlag string, rawOutput bool) bool {
	responseFailed := false
	responseObserved := false
	requestPayload := false
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		// 命令与结果在同一行时，只检查箭头右侧，避免把请求参数当成输出。
		if separators := evidenceResultSeparatorPattern.FindAllStringIndex(line, -1); len(separators) > 0 {
			line = strings.TrimSpace(line[separators[len(separators)-1][1]:])
			responseFailed = false
			responseObserved = false
			requestPayload = false
		}
		if evidenceRequestPattern.MatchString(line) {
			responseFailed = false
			responseObserved = false
			requestPayload = true
			continue
		}
		if statuses := httpEvidenceStatusPattern.FindAllStringSubmatch(line, -1); len(statuses) > 0 {
			responseFailed = false
			responseObserved = true
			requestPayload = false
			for _, status := range statuses {
				if status[1][0] != '2' {
					responseFailed = true
				}
			}
		}
		lower := strings.ToLower(line)
		if containsAny(lower, "eacces", "permission denied", "access denied") {
			responseFailed = true
		}
		index := strings.Index(line, capturedFlag)
		if index >= 0 && flagOutputPrefixPattern.MatchString(line[:index]) {
			requestPayload = false
		}
		if index < 0 || responseFailed || requestPayload || containsAny(lower, "readme", "示例", "example", "sample", "推测", "猜测") {
			continue
		}
		// 新字段本身声明为实际输出，允许 JSON/HTML 原文；旧证据需有输出上下文。
		if rawOutput || responseObserved || strings.Trim(line, "`\"' \t\r") == capturedFlag || flagOutputPrefixPattern.MatchString(line[:index]) {
			return true
		}
	}
	return false
}

func containsAny(text string, values ...string) bool {
	for _, value := range values {
		if strings.Contains(text, value) {
			return true
		}
	}
	return false
}

func isFlagPlaceholder(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return true
	}
	return containsAny(value, "your_flag_here", "your-flag-here", "replace_me", "replace-me", "changeme", "redacted", "placeholder", "example", "sample", "xxx", "...")
}
