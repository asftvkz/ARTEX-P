package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

func TestLLMModelManagementAPI(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip("未配置隔离测试数据库")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	id, err := pg.SaveProfile(&db.LLMProfile{Name: fmt.Sprintf("models-api-%d", time.Now().UnixNano()), Format: "openai", Model: "first", APIKey: "never-return-this-key"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pg.Exec(`DELETE FROM llm_profiles WHERE id=$1`, id) })
	s := &Server{m: &Manager{pg: pg}}
	mux := http.NewServeMux()
	s.RegisterLLMModelRoutes(mux)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if strings.Contains(w.Body.String(), "never-return-this-key") {
			t.Fatalf("模型 API 回显了 Key: %s", w.Body.String())
		}
		return w
	}
	path := fmt.Sprintf("/api/llm/profiles/%d/models", id)
	w := call(http.MethodGet, path, "")
	if w.Code != 200 {
		t.Fatalf("读取成员: %d %s", w.Code, w.Body.String())
	}
	var initial LLMModelSetDTO
	if err := json.Unmarshal(w.Body.Bytes(), &initial); err != nil {
		t.Fatal(err)
	}
	if len(initial.Models) != 1 || initial.Models[0].ID == "" || initial.Models[0].UpstreamModelID != "first" || initial.DefaultModelID == nil {
		t.Fatalf("创建成员响应错误: %+v", initial)
	}
	body := `{"models":[{"upstream_model_id":"first","display_name":"deepseek-chat","enabled":true,"is_default":false},{"upstream_model_id":"second","display_name":"deepseek-chat","enabled":true,"is_default":true}]}`
	w = call(http.MethodPut, path, body)
	if w.Code != 200 {
		t.Fatalf("保存成员: %d %s", w.Code, w.Body.String())
	}
	var saved LLMModelSetDTO
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Models) != 2 || saved.Models[0].ID != initial.Models[0].ID || saved.Models[1].DisplayName != "deepseek-chat" || saved.Models[1].UpstreamModelID != "second" {
		t.Fatalf("批量保存成员 ID 或名称错误: %+v", saved)
	}
	poolPath := fmt.Sprintf("/api/llm/pool/routes/%d", id)
	w = call(http.MethodPut, poolPath, fmt.Sprintf(`{"model_mode":"fixed-model","model_id":%q}`, saved.Models[0].ID))
	if w.Code != 200 {
		t.Fatalf("保存固定池路由: %d %s", w.Code, w.Body.String())
	}
	w = call(http.MethodGet, "/api/llm/pool/routes", "")
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"model_mode":"fixed-model"`)) {
		t.Fatalf("读取池路由: %d %s", w.Code, w.Body.String())
	}
	w = call(http.MethodPut, path, `{"models":[{"upstream_model_id":"second","display_name":"second","enabled":true,"is_default":true}]}`)
	if w.Code != 409 {
		t.Fatalf("删除固定成员应冲突: %d %s", w.Code, w.Body.String())
	}
	w = call(http.MethodPut, poolPath, `{"model_mode":"fixed-model","model_id":null}`)
	if w.Code != 400 {
		t.Fatalf("无 ID 固定路由应拒绝: %d %s", w.Code, w.Body.String())
	}
	w = call(http.MethodGet, "/api/llm/profiles/0/models", "")
	if w.Code != 400 {
		t.Fatalf("无效渠道 ID 应拒绝: %d %s", w.Code, w.Body.String())
	}
	w = call(http.MethodPut, path, `{"models":[]}`)
	if w.Code != 400 {
		t.Fatalf("空列表应拒绝: %d %s", w.Code, w.Body.String())
	}
}

func TestLLMModelRequestIDPreservesIntegerPrecision(t *testing.T) {
	for _, input := range []string{`"9007199254740993"`, `9007199254740993`} {
		var id llmModelRequestID
		if err := json.Unmarshal([]byte(input), &id); err != nil || int64(id) != 9007199254740993 {
			t.Fatalf("ID 精度丢失: %s -> %d (%v)", input, id, err)
		}
	}
	for _, input := range []string{`1.5`, `"bad"`, `"-1"`, `true`, `""`, `9223372036854775808`} {
		var id llmModelRequestID
		if err := json.Unmarshal([]byte(input), &id); err == nil {
			t.Fatalf("无效 ID 被接受: %s", input)
		}
	}
}
