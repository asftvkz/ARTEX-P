package server

import (
	"encoding/json"
	"testing"
)

func TestFlagEvidenceSchemaRefreshPreservesCustomizations(t *testing.T) {
	s := newTriggerRegressionServer(t)
	const flag = "record_fact_flag_evidence_fields_v1"
	if err := s.m.pg.SetSetting(flag, "false"); err != nil {
		t.Fatal(err)
	}
	if err := s.m.pg.SeedTool("record_fact", "自定义说明", json.RawMessage(`{"type":"object","properties":{"summary":{"type":"string","description":"保留说明"}},"required":["summary"]}`), json.RawMessage(`["custom-worker"]`)); err != nil {
		t.Fatal(err)
	}
	original, err := s.m.pg.GetTool("record_fact")
	if err != nil || original == nil {
		t.Fatalf("读取原工具: %v", err)
	}
	t.Cleanup(func() {
		_ = s.m.pg.UpdateTool(original.Key, original.Description, original.Schema, mustJSON(original.Agents), original.Enabled)
		_ = s.m.pg.SetSetting(flag, "false")
	})
	customSchema := json.RawMessage(`{"type":"object","properties":{"summary":{"type":"string","description":"保留说明"}},"required":["summary"]}`)
	if err := s.m.pg.UpdateTool("record_fact", "自定义说明", customSchema, json.RawMessage(`["custom-worker"]`), false); err != nil {
		t.Fatal(err)
	}
	s.refreshFlagEvidenceFields()
	tool, err := s.m.pg.GetTool("record_fact")
	if err != nil || tool == nil {
		t.Fatalf("读取更新工具: %v", err)
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(tool.Schema, &schema); err != nil {
		t.Fatal(err)
	}
	if tool.Description != "自定义说明" || tool.Enabled || len(tool.Agents) != 1 || tool.Agents[0] != "custom-worker" {
		t.Fatalf("用户描述或控制选择被覆盖: %+v", tool)
	}
	if len(schema.Required) != 1 || schema.Required[0] != "summary" || len(schema.Properties["captured_flag"]) == 0 || len(schema.Properties["observed_output"]) == 0 {
		t.Fatalf("新增字段或原有必填项不正确: %s", tool.Schema)
	}
	var summary struct {
		Description string `json:"description"`
	}
	if err := json.Unmarshal(schema.Properties["summary"], &summary); err != nil || summary.Description != "保留说明" {
		t.Fatalf("原参数说明被覆盖: %s", tool.Schema)
	}
}
