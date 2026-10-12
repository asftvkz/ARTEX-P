package db

import (
	"errors"
	"testing"
	"time"
)

func TestLLMSelectionsPersistAndProtectFixedReferences(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	p := createLLMModelTestProfile(t, d, "selection-a")
	set, err := d.ReplaceLLMModels(p, []LLMModelInput{
		{UpstreamModelID: "selection-a", DisplayName: "同名", Enabled: true, Default: true},
		{UpstreamModelID: "selection-b", DisplayName: "同名", Enabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	a, b := set.Models[0].ID, set.Models[1].ID
	fixed := LLMModelSelection{ProfileID: p, ModelMode: LLMModelModeFixed, ModelID: &b}
	conv, err := d.CreateConversationSelection("selection-test", "模型选择", &fixed)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM conversations WHERE id=$1`, conv.ID) })
	got, err := d.GetConversation(conv.ID)
	if err != nil || got.LLMModelID == nil || *got.LLMModelID != b || got.LLMModelMode != LLMModelModeFixed {
		t.Fatalf("对话固定选择未持久化: %+v %v", got, err)
	}
	if _, err := d.ReplaceLLMModels(p, []LLMModelInput{{ID: a, UpstreamModelID: "selection-a", DisplayName: "同名", Enabled: true, Default: true}}); !errors.Is(err, ErrLLMModelReferenced) {
		t.Fatalf("删除固定引用应失败: %v", err)
	}
	if err := d.UpdateConversationSelection(conv.ID, nil); err != nil {
		t.Fatal(err)
	}
	task, err := d.CreateTaskWithOptions("选择测试", "", TaskCreateOptions{LLMMembers: []LLMModelSelection{{ProfileID: p}, fixed}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	gotTask, err := d.GetTask(task.ID)
	if err != nil || len(gotTask.LLMMembers) != 2 || gotTask.LLMMembers[1].ModelID == nil || *gotTask.LLMMembers[1].ModelID != b {
		t.Fatalf("任务成员未持久化: %+v %v", gotTask, err)
	}
	// 默认变更后的迟到额度错误不能消耗新默认，也不能推进链。
	_, err = d.ReplaceLLMModels(p, []LLMModelInput{{ID: a, UpstreamModelID: "selection-a", DisplayName: "同名", Enabled: true}, {ID: b, UpstreamModelID: "selection-b", DisplayName: "同名", Enabled: true, Default: true}})
	if err != nil {
		t.Fatal(err)
	}
	tr, err := d.MarkTaskLLMModelQuotaExhausted(task.ID, 0, a, gotTask.LLMChainRevision, "insufficient_quota")
	if err != nil || !tr.Stale || tr.Advanced {
		t.Fatalf("迟到默认模型失败改变了链: %+v %v", tr, err)
	}
	tr, err = d.MarkTaskLLMModelQuotaExhausted(task.ID, 0, b, gotTask.LLMChainRevision, "insufficient_quota")
	if err != nil || !tr.Advanced || tr.NextPosition == nil || *tr.NextPosition != 1 {
		t.Fatalf("同渠道不同成员无法切换: %+v %v", tr, err)
	}
}

func TestLLMHealthLegacyMigrationDoesNotReplay(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	p := createLLMModelTestProfile(t, d, "health-a")
	set, err := d.ReplaceLLMModels(p, []LLMModelInput{{UpstreamModelID: "health-a", DisplayName: "A", Enabled: true, Default: true}, {UpstreamModelID: "health-b", DisplayName: "B", Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	a, b := set.Models[0].ID, set.Models[1].ID
	if _, err := d.Exec(`INSERT INTO llm_profile_health(profile_id,fails,trips,open_until,last_error,last_at) VALUES($1,0,1,$2,'旧冷却',now())`, p, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := d.MigrateLLMModels(t.Context()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := d.QueryRow(`SELECT count(*) FROM llm_model_health WHERE profile_id=$1 AND model_id=$2`, p, a).Scan(&count); err != nil || count != 1 {
		t.Fatalf("旧冷却未迁移到A: %d %v", count, err)
	}
	if err := d.ClearLLMModelHealth(p, a); err != nil {
		t.Fatal(err)
	}
	if err := d.MigrateLLMModels(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT count(*) FROM llm_model_health WHERE profile_id=$1`, p).Scan(&count); err != nil || count != 0 {
		t.Fatalf("恢复后冷却复活: %d %v", count, err)
	}
	// 再迁入一份旧状态到A，随后变更默认，验证旧健康绝不进入B。
	if _, err := d.Exec(`INSERT INTO llm_profile_health(profile_id,fails,trips,open_until,last_error,last_at) VALUES($1,0,1,$2,'旧冷却',now()) ON CONFLICT(profile_id) DO UPDATE SET open_until=EXCLUDED.open_until`, p, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := d.MigrateLLMModels(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ReplaceLLMModels(p, []LLMModelInput{{ID: a, UpstreamModelID: "health-a", DisplayName: "A", Enabled: true}, {ID: b, UpstreamModelID: "health-b", DisplayName: "B", Enabled: true, Default: true}}); err != nil {
		t.Fatal(err)
	}
	if err := d.MigrateLLMModels(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT count(*) FROM llm_model_health WHERE profile_id=$1 AND model_id=$2`, p, b).Scan(&count); err != nil || count != 0 {
		t.Fatalf("旧冷却污染新默认B: %d %v", count, err)
	}
}
