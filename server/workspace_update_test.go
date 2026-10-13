package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceProtectsPersistentUpdateRuntime(t *testing.T) {
	root := t.TempDir()
	runtime := filepath.Join(root, "self-update")
	t.Setenv("ARTEX_SELFUPDATE_ROOT", runtime)
	os.MkdirAll(runtime, 0700)
	binary := filepath.Join(runtime, "artex")
	os.WriteFile(binary, []byte("verified-program"), 0700)
	s := &Server{m: &Manager{dir: root}}
	if _, ok := s.wsResolve("self-update/artex"); ok {
		t.Fatal("工作区允许读写更新程序")
	}
	r := httptest.NewRecorder()
	s.wsWrite(r, httptest.NewRequest("POST", "/api/workspace/write", strings.NewReader(`{"path":"self-update/artex","content":"bad"}`)))
	if r.Code != 400 {
		t.Fatalf("允许覆写程序: %d", r.Code)
	}
	if b, _ := os.ReadFile(binary); string(b) != "verified-program" {
		t.Fatal("更新程序已被改写")
	}
	r = httptest.NewRecorder()
	s.wsList(r, httptest.NewRequest("GET", "/api/workspace/list", nil))
	var response struct {
		Entries []wsEntry `json:"entries"`
	}
	json.Unmarshal(r.Body.Bytes(), &response)
	for _, e := range response.Entries {
		if e.Name == "self-update" {
			t.Fatal("更新程序出现在任务工作区")
		}
	}
	if _, ok := s.wsResolve("report.txt"); !ok {
		t.Fatal("普通任务文件不可用")
	}
	if err := os.Symlink(runtime, filepath.Join(root, "alias")); err == nil {
		if _, ok := s.wsResolve("alias/artex"); ok {
			t.Fatal("符号链接绕过运行程序保护")
		}
		if _, ok := s.wsResolve("alias/new/file"); ok {
			t.Fatal("新路径绕过运行程序保护")
		}
	}
}
