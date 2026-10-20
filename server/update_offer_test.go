package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Autumn-27/artex/selfupdate"
)

// 同时覆盖检查和执行入口；用已有更新占用阻止真正下载，409 表明已通过版本门禁。
func TestUpdateOfferCheckAndApply(t *testing.T) {
	oldVersion, oldCache, oldHub := BuildVersion, relCache, updHub
	t.Cleanup(func() { BuildVersion, relCache, updHub = oldVersion, oldCache, oldHub })
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARTEX_SELFUPDATE_ROOT", filepath.Dir(exe))
	for _, tc := range []struct {
		name, current, target                string
		draft, prerelease, missingAsset      bool
		available, comparable, releaseSwitch bool
	}{
		{name: "候选切换", current: "dev-a4363546726e", target: "v2.2.17", available: true, releaseSwitch: true},
		{name: "候选无法比较先后", current: "dev-a4363546726e", target: "v2.2.16", available: true, releaseSwitch: true},
		{name: "正式升级", current: "2.2.16", target: "v2.2.17", available: true, comparable: true},
		{name: "相同版本", current: "v2.2.17", target: "v2.2.17", comparable: true},
		{name: "正式降级", current: "2.2.18", target: "v2.2.17", comparable: true},
		{name: "裸开发版", current: "dev", target: "v2.2.17"},
		{name: "未知提交", current: "dev-not-a-sha", target: "v2.2.17"},
		{name: "脏工作树", current: "dev-a4363546726e-dirty", target: "v2.2.17"},
		{name: "描述版本", current: "2.2.16-2-gabc1234", target: "v2.2.17"},
		{name: "预发布", current: "dev-a4363546726e", target: "v2.2.17", prerelease: true},
		{name: "草稿", current: "dev-a4363546726e", target: "v2.2.17", draft: true},
		{name: "非稳定目标", current: "dev-a4363546726e", target: "v2.2.17-rc1"},
		{name: "缺平台包", current: "dev-a4363546726e", target: "v2.2.17", available: true, releaseSwitch: true, missingAsset: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			BuildVersion = tc.current
			rel := &selfupdate.Release{TagName: tc.target, Draft: tc.draft, Prerelease: tc.prerelease}
			if !tc.missingAsset {
				rel.Assets = []selfupdate.Asset{{Name: selfupdate.AssetName(tc.target, runtime.GOOS, runtime.GOARCH)}}
			}
			relCache = newTestCache(func(context.Context, *http.Client) (*selfupdate.Release, error) { return rel, nil })
			updHub = &updateHub{running: true, subs: map[chan updateProgress]struct{}{}}
			s := &Server{m: &Manager{}}
			check := httptest.NewRecorder()
			s.updateCheck(check, httptest.NewRequest("GET", "/api/update/check", nil))
			var out struct {
				Available      bool `json:"has_update"`
				Comparable     bool `json:"comparable"`
				ReleaseSwitch  bool `json:"release_switch"`
				AssetAvailable bool `json:"asset_available"`
			}
			if err := json.Unmarshal(check.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			if out.Available != tc.available || out.Comparable != tc.comparable || out.ReleaseSwitch != tc.releaseSwitch || out.AssetAvailable == tc.missingAsset {
				t.Fatalf("检查结果不符: %+v", out)
			}
			apply := httptest.NewRecorder()
			s.updateApply(apply, httptest.NewRequest("POST", "/api/update/apply", nil))
			want := http.StatusBadRequest
			if tc.available && !tc.missingAsset {
				want = http.StatusConflict
			}
			if apply.Code != want {
				t.Fatalf("执行门禁 = %d, 期望 %d: %s", apply.Code, want, apply.Body.String())
			}
		})
	}
}

func TestUpdateOfferKeepsVersionComparisonStrict(t *testing.T) {
	if _, ok := selfupdate.CompareVersions("dev-a4363546726e", "v2.2.17"); ok {
		t.Fatal("候选切换不能改变正式版本比较语义")
	}
}

func TestUpdateOfferRequiresPersistentDockerLauncher(t *testing.T) {
	if !selfupdate.InDocker() {
		t.Skip("仅容器环境可验证旧启动器限制")
	}
	t.Setenv("ARTEX_SELFUPDATE_ROOT", "")
	r := httptest.NewRecorder()
	(&Server{}).updateApply(r, httptest.NewRequest("POST", "/api/update/apply", nil))
	if r.Code != http.StatusConflict {
		t.Fatalf("旧启动器未拒绝更新: %d %s", r.Code, r.Body.String())
	}
}
