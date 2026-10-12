package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

func TestLLMGlobalConversationUsesPoolSelectionAndFailover(t *testing.T) {
	s := newModelExecutionServer(t)
	old, _ := s.m.pg.ActiveProfile()
	t.Cleanup(func() {
		if old != nil {
			_ = s.m.pg.SetActiveProfile(old.ID)
		}
	})
	for _, key := range []string{settingLLMPoolOn, settingLLMPoolBindFallback} {
		value, exists, err := s.m.pg.GetSetting(key)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if exists {
				_ = s.m.pg.SetSetting(key, value)
			} else {
				_, _ = s.m.pg.Exec(`DELETE FROM settings WHERE key=$1`, key)
			}
		})
	}
	if err := s.m.pg.SetSetting(settingLLMPoolOn, "true"); err != nil {
		t.Fatal(err)
	}
	if err := s.m.pg.SetSetting(settingLLMPoolBindFallback, "false"); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var seen []string
	failB := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		seen = append(seen, body.Model)
		fail := failB && body.Model == "real-b"
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if fail {
			w.WriteHeader(402)
			fmt.Fprint(w, `{"error":{"message":"insufficient_quota"}}`)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()
	id, set := executionModels(t, s, upstream.URL)
	other, _ := executionModels(t, s, upstream.URL)
	if _, err := s.m.pg.ReplaceLLMModels(other, []db.LLMModelInput{{UpstreamModelID: "real-c", DisplayName: "deepseek-chat", Enabled: true, Default: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.m.pg.Exec(`UPDATE llm_profiles SET priority=100000 WHERE id=$1`, other); err != nil {
		t.Fatal(err)
	}
	if err := s.m.pg.SetActiveProfile(id); err != nil {
		t.Fatal(err)
	}
	b := set.Models[1].ID
	if err := s.m.pg.SaveLLMPoolModelRoute(t.Context(), db.LLMModelSelection{ProfileID: id, ModelMode: db.LLMModelModeFixed, ModelID: &b}); err != nil {
		t.Fatal(err)
	}
	// 生产已配置标记；每个对话轮次仍须重新解析全局实际成员。
	s.llmOn = true
	s.chatAgent = agent.NewChatAgent(&scriptedLLMProvider{}, "ready", s.m.dir, nil, 10000)
	oldAugment := agent.ToolAugment
	agent.ToolAugment = nil
	t.Cleanup(func() { agent.ToolAugment = oldAugment })
	run := func(c *db.Conversation) {
		t.Helper()
		ca := s.resolveChatAgent(c)
		if ca == nil {
			t.Fatal("对话模型不可用")
		}
		_, err := ca.Chat(context.Background(), "execution-unbound", fmt.Sprintf("pool-chat-%d", time.Now().UnixNano()), "test", 1, 5*time.Second, false, func(db.Activity) {})
		if err != nil {
			t.Fatal(err)
		}
	}
	run(&db.Conversation{AgentKey: "execution-unbound"})
	mu.Lock()
	failB = true
	mu.Unlock()
	run(&db.Conversation{AgentKey: "execution-unbound"})
	a := set.Models[0].ID
	run(&db.Conversation{AgentKey: "execution-unbound", LLMProfileID: &id, LLMModelMode: db.LLMModelModeFixed, LLMModelID: &a})
	mu.Lock()
	got := strings.Join(seen, ",")
	mu.Unlock()
	if got != "real-b,real-b,real-c,real-a" {
		t.Fatalf("全局池与显式绑定真实请求错误: %s", got)
	}
}
