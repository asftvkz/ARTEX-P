package agent

import (
	"strings"
	"testing"
)

func TestTaskProxyEnvironmentClearsBypassAndDirect(t *testing.T) {
	for _, addr := range []string{"http://proxy.test:8080", ""} {
		env := strings.Join(taskProxyEnv(addr, "", true), "\n")
		for _, key := range []string{"HTTP_PROXY=", "HTTPS_PROXY=", "ALL_PROXY=", "http_proxy=", "https_proxy=", "all_proxy=", "NO_PROXY=", "no_proxy="} {
			if !strings.Contains(env, key) {
				t.Fatalf("缺少环境覆盖 %s", key)
			}
		}
		if !strings.Contains(env, "ARTEX_PLAYWRIGHT_MANAGED=1") || !strings.Contains(env, "ARTEX_PLAYWRIGHT_PROXY="+addr) {
			t.Fatal("浏览器未接入正式任务出口")
		}
	}
}
