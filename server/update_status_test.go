package server

import (
	"encoding/json"
	"github.com/Autumn-27/artex/selfupdate"
	"net/http/httptest"
	"testing"
)

func TestUpdateStatusSurvivesReleaseSourceFailure(t *testing.T) {
	previousHub, previousVersion, previousBoot := updHub, BuildVersion, bootUpdateState()
	t.Cleanup(func() { updHub = previousHub; BuildVersion = previousVersion; SetBootUpdateState(previousBoot) })
	BuildVersion = "2.2.3"
	updHub = &updateHub{subs: map[chan updateProgress]struct{}{}}
	updHub.begin("v2.2.4")
	updHub.publish(selfupdate.PhaseDownload, 42, "下载发布包")
	recorder := httptest.NewRecorder()
	(&Server{}).updateStatus(recorder, httptest.NewRequest("GET", "/api/update/status", nil))
	var state map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state["current"] != "2.2.3" || state["running"] != true || state["percent"] != float64(42) || state["message"] != "下载发布包" {
		t.Fatalf("状态错误: %+v", state)
	}
	updHub.finish(nil)
	SetBootUpdateState(selfupdate.State{RolledBack: true, Detail: "新版启动失败，已回滚"})
	recorder = httptest.NewRecorder()
	(&Server{}).updateStatus(recorder, httptest.NewRequest("GET", "/api/update/status", nil))
	json.Unmarshal(recorder.Body.Bytes(), &state)
	if state["rolled_back"] != true || state["message"] != "新版启动失败，已回滚" {
		t.Fatalf("回滚状态丢失: %+v", state)
	}
}
