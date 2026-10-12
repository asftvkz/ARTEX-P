package db

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestMigrateLLMModelsCreatesDefaultFromLegacyModel(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })

	var profileID int64
	if err := d.QueryRow(`INSERT INTO llm_profiles(name,format,model) VALUES($1,'openai','legacy-model') RETURNING id`,
		fmt.Sprintf("legacy-model-test-%d", time.Now().UnixNano())).Scan(&profileID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM llm_profiles WHERE id=$1`, profileID) })
	if err := d.MigrateLLMModels(t.Context()); err != nil {
		t.Fatal(err)
	}

	set, err := d.ListLLMModels(profileID)
	if err != nil {
		t.Fatal(err)
	}
	if set.DefaultModelID == nil || len(set.Models) != 1 {
		t.Fatalf("migration result = %+v, want one default member", set)
	}
	model := set.Models[0]
	if model.ID != *set.DefaultModelID || model.UpstreamModelID != "legacy-model" || model.DisplayName != "legacy-model" || !model.Enabled {
		t.Fatalf("migrated member = %+v", model)
	}
	set, err = d.ReplaceLLMModels(profileID, []LLMModelInput{
		{ID: model.ID, UpstreamModelID: "legacy-model", DisplayName: "保留的展示名称", Enabled: true},
		{UpstreamModelID: "new-default", DisplayName: "新的默认展示名称", Enabled: true, Default: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	defaultID := *set.DefaultModelID
	if err := d.MigrateLLMModels(t.Context()); err != nil {
		t.Fatal(err)
	}
	again, err := d.ListLLMModels(profileID)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Models) != 2 || again.DefaultModelID == nil || *again.DefaultModelID != defaultID ||
		again.Models[0].ID != model.ID || again.Models[0].DisplayName != "保留的展示名称" ||
		again.Models[1].ID != defaultID || again.Models[1].DisplayName != "新的默认展示名称" {
		t.Fatalf("二次迁移覆盖了已保存成员: %+v", again)
	}
}

func TestLLMModelSelectionSeparatesDefaultFixedAndDisplayName(t *testing.T) {
	firstID, secondID := int64(11), int64(12)
	set := LLMModelSet{ProfileID: 7, DefaultModelID: &firstID, Models: []LLMModel{
		{ID: firstID, ProfileID: 7, UpstreamModelID: "provider-a-v3", DisplayName: "deepseek-chat", Enabled: true},
		{ID: secondID, ProfileID: 7, UpstreamModelID: "provider-a-r1", DisplayName: "deepseek-chat", Enabled: true},
	}}
	fixed := LLMModelSelection{ProfileID: 7, ModelMode: LLMModelModeFixed, ModelID: &firstID}
	set.DefaultModelID = &secondID
	resolved, err := ResolveLLMModelSelection(fixed, set)
	if err != nil || resolved.ID != firstID || resolved.UpstreamModelID != "provider-a-v3" || resolved.MemberKey() != "7:11" {
		t.Fatalf("固定模型随默认变化: %+v, %v", resolved, err)
	}
	resolved, err = ResolveLLMModelSelection(LLMModelSelection{ProfileID: 7}, set)
	if err != nil || resolved.ID != secondID || resolved.UpstreamModelID != "provider-a-r1" {
		t.Fatalf("渠道默认未跟随默认变化: %+v, %v", resolved, err)
	}
	foreignID := int64(99)
	set.Models = append(set.Models, LLMModel{ID: foreignID, ProfileID: 8, DisplayName: "deepseek-chat", Enabled: true})
	if _, err := ResolveLLMModelSelection(LLMModelSelection{ProfileID: 7, ModelMode: LLMModelModeFixed, ModelID: &foreignID}, set); !errors.Is(err, ErrLLMModelUnavailable) {
		t.Fatalf("跨渠道同展示名被当作等价模型: %v", err)
	}
	set.Models[0].Enabled = false
	if _, err := ResolveLLMModelSelection(fixed, set); !errors.Is(err, ErrLLMModelUnavailable) {
		t.Fatalf("禁用固定成员发生静默回退: %v", err)
	}
}

func TestLLMModelSelectionRejectsAmbiguousMode(t *testing.T) {
	id := int64(11)
	for _, selection := range []LLMModelSelection{
		{ProfileID: 7, ModelMode: LLMModelModeChannelDefault, ModelID: &id},
		{ProfileID: 7, ModelMode: LLMModelModeFixed},
		{ProfileID: 7, ModelMode: "display-name"},
		{ProfileID: 0, ModelMode: LLMModelModeChannelDefault},
	} {
		if err := selection.Validate(); !errors.Is(err, ErrLLMModelInvalid) {
			t.Fatalf("歧义选择被接受: %+v, %v", selection, err)
		}
	}
}

func TestLLMModelsCreateUpsertAndSettingsKeepDefaultAndKey(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	profileID := createLLMModelTestProfile(t, d, "first")
	initial, err := d.ListLLMModels(profileID)
	if err != nil || len(initial.Models) != 1 || initial.DefaultModelID == nil {
		t.Fatalf("创建没有原子种默认成员: %+v, %v", initial, err)
	}
	firstID := initial.Models[0].ID
	input := []LLMModelInput{
		{UpstreamModelID: "first", DisplayName: "自定义名称", Enabled: true},
		{UpstreamModelID: "second", DisplayName: "第二模型", Enabled: true, Default: true},
	}
	set, err := d.ReplaceLLMModels(profileID, input)
	if err != nil {
		t.Fatal(err)
	}
	if set.Models[0].ID != firstID {
		t.Fatalf("无 ID 相同上游成员未复用稳定 ID: %+v", set)
	}
	defaultID := *set.DefaultModelID
	set, err = d.ReplaceLLMModels(profileID, input)
	if err != nil || len(set.Models) != 2 || *set.DefaultModelID != defaultID {
		t.Fatalf("重试模型保存重复建成员: %+v, %v", set, err)
	}
	p, err := d.ProfileByID(profileID)
	if err != nil {
		t.Fatal(err)
	}
	p.APIKey, p.Model, p.Name = "", "unsaved-draft-default", p.Name+"-edited"
	if _, err := d.SaveProfile(p); err != nil {
		t.Fatal(err)
	}
	p, err = d.ProfileByID(profileID)
	if err != nil || p.Model != "second" || p.DefaultModelID == nil || *p.DefaultModelID != defaultID || p.APIKey != "test-key" {
		t.Fatalf("普通设置提前覆盖模型或清空 Key: %+v, %v", p, err)
	}
	set, err = d.ListLLMModels(profileID)
	if err != nil || set.Models[0].DisplayName != "自定义名称" {
		t.Fatalf("普通设置覆盖展示名称: %+v, %v", set, err)
	}
}

func TestLLMPoolModelRoutePinsMemberAndProtectsReferences(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	profileID := createLLMModelTestProfile(t, d, "first")
	set, err := d.ReplaceLLMModels(profileID, []LLMModelInput{
		{UpstreamModelID: "first", DisplayName: "same", Enabled: true, Default: true},
		{UpstreamModelID: "second", DisplayName: "same", Enabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	fixedID := set.Models[1].ID
	if err := d.SaveLLMPoolModelRoute(t.Context(), LLMModelSelection{ProfileID: profileID, ModelMode: LLMModelModeFixed, ModelID: &fixedID}); err != nil {
		t.Fatal(err)
	}
	routes, err := d.ListLLMPoolModelRoutes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, route := range routes {
		if route.ProfileID == profileID {
			found = true
			if route.ModelMode != LLMModelModeFixed || route.ModelID == nil || *route.ModelID != fixedID {
				t.Fatalf("固定路由未持久化: %+v", route)
			}
			resolved, err := d.ResolveLLMModel(t.Context(), route)
			if err != nil || resolved.UpstreamModelID != "second" {
				t.Fatalf("固定池路由错误: %+v, %v", resolved, err)
			}
		}
	}
	if !found {
		t.Fatal("池路由读取缺少渠道")
	}
	if _, err := d.ReplaceLLMModels(profileID, []LLMModelInput{{UpstreamModelID: "first", DisplayName: "same", Enabled: true, Default: true}}); !errors.Is(err, ErrLLMModelReferenced) {
		t.Fatalf("删除固定路由引用成员没有冲突: %v", err)
	}
	if _, err := d.ReplaceLLMModels(profileID, []LLMModelInput{
		{UpstreamModelID: "first", DisplayName: "same", Enabled: true, Default: true},
		{UpstreamModelID: "second", DisplayName: "same", Enabled: false},
	}); !errors.Is(err, ErrLLMModelReferenced) {
		t.Fatalf("禁用固定路由引用成员没有冲突: %v", err)
	}
	unchanged, err := d.ListLLMModels(profileID)
	if err != nil || len(unchanged.Models) != 2 || !unchanged.Models[1].Enabled {
		t.Fatalf("冲突未原子回滚: %+v, %v", unchanged, err)
	}
	if err := d.SaveLLMPoolModelRoute(t.Context(), LLMModelSelection{ProfileID: profileID, ModelMode: LLMModelModeChannelDefault}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ReplaceLLMModels(profileID, []LLMModelInput{{UpstreamModelID: "first", DisplayName: "same", Enabled: true, Default: true}}); err != nil {
		t.Fatalf("取消固定路由后成员仍无法移除: %v", err)
	}
}

func TestReplaceLLMModelsKeepsDisplayNamesSeparateFromUpstreamIDs(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })

	profileID := createLLMModelTestProfile(t, d, "deepseek-v3")
	if err := d.MigrateLLMModels(t.Context()); err != nil {
		t.Fatal(err)
	}
	initial, err := d.ListLLMModels(profileID)
	if err != nil {
		t.Fatal(err)
	}

	set, err := d.ReplaceLLMModels(profileID, []LLMModelInput{
		{
			ID:              initial.Models[0].ID,
			UpstreamModelID: "deepseek-v3",
			DisplayName:     "deepseek-chat",
			Enabled:         true,
			Default:         true,
		},
		{
			UpstreamModelID: "deepseek-r1",
			DisplayName:     "deepseek-chat",
			Enabled:         true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if set.DefaultModelID == nil || len(set.Models) != 2 {
		t.Fatalf("replacement result = %+v", set)
	}
	if set.Models[0].DisplayName != "deepseek-chat" || set.Models[0].UpstreamModelID != "deepseek-v3" {
		t.Fatalf("default model fields were conflated: %+v", set.Models[0])
	}
	if set.Models[1].DisplayName != "deepseek-chat" || set.Models[1].UpstreamModelID != "deepseek-r1" {
		t.Fatalf("duplicate display name should retain independent upstream ID: %+v", set.Models[1])
	}
	profile, err := d.ProfileByID(profileID)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Model != "deepseek-v3" {
		t.Fatalf("legacy execution model = %q, want default upstream ID", profile.Model)
	}
}

func TestReplaceLLMModelsRejectsInvalidDefaultMembership(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })

	firstID := createLLMModelTestProfile(t, d, "first-legacy")
	secondID := createLLMModelTestProfile(t, d, "second-legacy")
	if err := d.MigrateLLMModels(t.Context()); err != nil {
		t.Fatal(err)
	}
	first, err := d.ListLLMModels(firstID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := d.ListLLMModels(secondID)
	if err != nil {
		t.Fatal(err)
	}

	_, err = d.ReplaceLLMModels(firstID, []LLMModelInput{{
		ID:              first.Models[0].ID,
		UpstreamModelID: first.Models[0].UpstreamModelID,
		DisplayName:     first.Models[0].DisplayName,
		Enabled:         true,
		Default:         true,
	}})
	if err != nil {
		t.Fatalf("enabled default should be accepted: %v", err)
	}

	_, err = d.ReplaceLLMModels(firstID, []LLMModelInput{{
		ID:              first.Models[0].ID,
		UpstreamModelID: first.Models[0].UpstreamModelID,
		DisplayName:     first.Models[0].DisplayName,
		Enabled:         false,
		Default:         true,
	}})
	if !errors.Is(err, ErrLLMModelDefaultDisabled) {
		t.Fatalf("disabled default error = %v, want %v", err, ErrLLMModelDefaultDisabled)
	}

	_, err = d.ReplaceLLMModels(firstID, []LLMModelInput{{
		ID:              second.Models[0].ID,
		UpstreamModelID: second.Models[0].UpstreamModelID,
		DisplayName:     second.Models[0].DisplayName,
		Enabled:         true,
		Default:         true,
	}})
	if !errors.Is(err, ErrLLMModelWrongProfile) {
		t.Fatalf("cross-profile member error = %v, want %v", err, ErrLLMModelWrongProfile)
	}
}

func createLLMModelTestProfile(t *testing.T, d *DB, model string) int64 {
	t.Helper()
	id, err := d.SaveProfile(&LLMProfile{
		Name:   fmt.Sprintf("llm-model-test-%d", time.Now().UnixNano()),
		Format: "openai",
		Model:  model,
		APIKey: "test-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM llm_profiles WHERE id=$1`, id)
	})
	return id
}
