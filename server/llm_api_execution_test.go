package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

func modelHTTPClient(t *testing.T, s *Server) func(string, string, string, int) map[string]any {
	t.Helper()
	host := httptest.NewServer(s.Handler())
	t.Cleanup(host.Close)
	token, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatal(err)
	}
	return func(method, path, body string, want int) map[string]any {
		t.Helper()
		req, err := http.NewRequest(method, host.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := host.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != want {
			t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, data)
		}
		if strings.Contains(string(data), "test-key-must-not-echo") {
			t.Fatal("响应泄露Key")
		}
		var out map[string]any
		if err := json.Unmarshal(data, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
}

func TestLLMOuterAPIJWTSelectionsAndProfileOnlyModelList(t *testing.T) {
	s := newModelExecutionServer(t)
	s.m.assets = s.m.pg.Assets()
	seen := make(chan string, 2)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.URL.String() + "|" + r.Header.Get("Authorization")
		fmt.Fprint(w, `{"data":[{"id":"remote-model"},{"id":"test-key-must-not-echo"}],"api_key":"test-key-must-not-echo"}`)
	}))
	defer proxy.Close()
	id, set := executionModels(t, s, "http://model-list.invalid/v1")
	// 此夹具模拟旧URL渠道；显式inherit按新合同会忽略旧URL。
	if _, err := s.m.pg.Exec(`UPDATE llm_profiles SET proxy=$1,proxy_mode='',proxy_id=NULL WHERE id=$2`, proxy.URL, id); err != nil {
		t.Fatal(err)
	}
	api := modelHTTPClient(t, s)
	for _, path := range []string{fmt.Sprintf("/api/llm/profiles/%d/models", id), "/api/llm/pool/routes"} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 401 {
			t.Fatalf("新路由无JWT未拒绝: %d", w.Code)
		}
	}
	out := api("POST", "/api/llm/models", fmt.Sprintf(`{"profile_id":%d}`, id), 200)
	if fmt.Sprint(out["models"]) != "[remote-model]" {
		t.Fatalf("模型列表错误: %v", out)
	}
	select {
	case got := <-seen:
		if got != "http://model-list.invalid/v1/models|Bearer test-key-must-not-echo" {
			t.Fatalf("仅profile_id未补齐保存配置: %s", got)
		}
	case <-time.After(time.Second):
		t.Fatal("未请求保存的代理")
	}
	key := fmt.Sprintf("model_api_%d", time.Now().UnixNano())
	a, err := s.m.pg.CreateAgent(key, key, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = s.m.pg.Exec(`DELETE FROM agents WHERE id=$1`, a.ID) })
	selection := fmt.Sprintf(`{"profile_id":%q,"model_mode":"fixed-model","model_id":%q}`, i64s(id), i64s(set.Models[0].ID))
	conv := api("POST", "/api/conversations", fmt.Sprintf(`{"agent_key":%q,"title":"model-test","profile_id":%q,"model_mode":"fixed-model","model_id":%q}`, key, i64s(id), i64s(set.Models[0].ID)), 200)
	cid := int64(conv["id"].(float64))
	t.Cleanup(func() { _ = s.m.pg.DeleteConversation(cid) })
	if conv["profile_id"] != i64s(id) || conv["model_id"] != i64s(set.Models[0].ID) {
		t.Fatalf("对话ID未按新契约序列化: %v", conv)
	}
	path := fmt.Sprintf("/api/conversations/%d/profile", cid)
	api("PATCH", path, `{}`, 200)
	c, _ := s.m.pg.GetConversation(cid)
	if c.LLMModelID == nil || *c.LLMModelID != set.Models[0].ID {
		t.Fatal("省略选择清除了固定成员")
	}
	api("PUT", "/api/agents/"+key+"/config", selection, 200)
	api("PUT", "/api/agents/"+key+"/config", `{"max_turns":2}`, 200)
	out = api("GET", "/api/agents/"+key, "", 200)
	if out["agent"].(map[string]any)["model_id"] != i64s(set.Models[0].ID) {
		t.Fatalf("Agent固定成员未保留: %v", out)
	}
	modelPath := fmt.Sprintf("/api/llm/profiles/%d/models", id)
	api("PUT", modelPath, fmt.Sprintf(`{"models":[{"id":%q,"upstream_model_id":"real-b","display_name":"deepseek-chat","enabled":true,"is_default":true}]}`, i64s(set.Models[1].ID)), 409)
	api("PATCH", path, `{"profile_id":null}`, 200)
	api("PUT", "/api/agents/"+key+"/config", `{"profile_id":null}`, 200)
	c, _ = s.m.pg.GetConversation(cid)
	a, _ = s.m.pg.GetAgentByKey(key)
	if c.LLMProfileID != nil || a.LLMProfileID != nil || c.LLMModelID != nil || a.LLMModelID != nil {
		t.Fatal("null没有清除继承覆盖")
	}
	api("PATCH", path, fmt.Sprintf(`{"llm_profile_id":%d}`, id), 200)
	c, _ = s.m.pg.GetConversation(cid)
	if c.LLMModelMode != db.LLMModelModeChannelDefault || c.LLMModelID != nil {
		t.Fatal("旧请求未转换渠道默认")
	}
	task := executionTask(t, s, []db.LLMModelSelection{{ProfileID: id}})
	out = api("PUT", "/api/tasks/"+task.ID+"/llm", fmt.Sprintf(`{"members":[%s,{"profile_id":%q,"model_mode":"fixed-model","model_id":%q}],"active_position":1}`, selection, i64s(id), i64s(set.Models[1].ID)), 200)
	actual := out["active_member"].(map[string]any)
	if actual["model_id"] != i64s(set.Models[1].ID) || actual["upstream_model_id"] != "real-b" {
		t.Fatalf("任务实际成员响应错误: %v", out)
	}
	api("PUT", modelPath, fmt.Sprintf(`{"models":[{"id":%q,"upstream_model_id":"real-b","display_name":"deepseek-chat","enabled":true,"is_default":true}]}`, i64s(set.Models[1].ID)), 409)
	api("PUT", "/api/tasks/"+task.ID+"/llm", `{"members":[]}`, 200)
	row, err := s.m.pg.GetTask(func() int64 { n, _ := strconv.ParseInt(task.ID, 10, 64); return n }())
	if err != nil || len(row.LLMMembers) != 0 {
		t.Fatalf("清空任务链失败: %v", err)
	}
	// 事务仍由数据库解析选择，HTTP无外层代理快照。
	api("POST", "/api/tasks", `{"description":"invalid proxy","proxy_mode":"fixed","proxy_id":999999999}`, 400)
}
