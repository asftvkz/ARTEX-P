//go:build proxybrowser

package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

// 使用显式测试标签运行真实 Chromium，不给没有浏览器的普通 Go 环境添加依赖。
func TestProxyPGBrowserChromium(t *testing.T) {
	s, _ := proxyIntegration(t)
	module, executable := os.Getenv("ARTEX_TEST_PLAYWRIGHT_MODULE"), os.Getenv("ARTEX_TEST_CHROMIUM")
	if module == "" || executable == "" {
		t.Fatal("真实浏览器测试必须配置本地 Playwright 模块和 Chromium 路径")
	}
	var direct atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { direct.Add(1); fmt.Fprint(w, "DIRECT") }))
	defer target.Close()
	proxy := func(label string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			want := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:secret"))
			if r.Header.Get("Proxy-Authorization") != want {
				w.Header().Set("Proxy-Authenticate", `Basic realm="task"`)
				w.WriteHeader(407)
				return
			}
			if r.Method == "CONNECT" {
				w.WriteHeader(502)
				return
			}
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, "<html><body>"+label+"</body></html>")
		}))
	}
	pa, pb := proxy("BROWSER_A"), proxy("BROWSER_B")
	defer pa.Close()
	defer pb.Close()
	var paths []string
	for _, upstream := range []*httptest.Server{pa, pb} {
		u, _ := url.Parse(upstream.URL)
		u.User = url.UserPassword("user", "secret")
		p := createRuntimeProxy(t, s, u.String())
		task := createRuntimeTask(t, s, db.TaskProxyModeFixed, &p.ID)
		id, _ := strconv.ParseInt(task.ID, 10, 64)
		ctx := agent.WithRunInfo(t.Context(), agent.RunInfo{TaskID: id})
		config, err := s.m.TaskMCPConfig(ctx, &db.MCPServer{Name: "browser", Transport: "stdio", Command: "local-browser", Args: json.RawMessage(`[]`), Env: json.RawMessage(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		args := jsonStrSlice(config.Args)
		paths = append(paths, args[len(args)-1])
	}
	script := `const fs=require('fs');const {chromium}=require(process.env.ARTEX_TEST_PLAYWRIGHT_MODULE);const input=JSON.parse(fs.readFileSync(0,'utf8'));(async()=>{const browsers=[];try{const results=await Promise.all(input.paths.map(async(path)=>{const c=JSON.parse(fs.readFileSync(path,'utf8')).browser;const b=await chromium.launch({...c.launchOptions,executablePath:process.env.ARTEX_TEST_CHROMIUM,headless:true,args:[...c.launchOptions.args,'--disable-background-networking','--disable-component-update']});browsers.push(b);const context=await b.newContext(c.contextOptions);const page=await context.newPage();try{await page.goto(input.target,{timeout:5000});return await page.textContent('body')}catch{if(input.allowFailure)return 'FAILED';throw Error('navigation failed')}}));process.stdout.write(JSON.stringify(results))}finally{await Promise.all(browsers.map(b=>b.close()))}})().catch(()=>{process.stderr.write('browser proxy validation failed');process.exitCode=1})`
	for _, failure := range []bool{false, true} {
		if failure {
			pa.Close()
		}
		input, _ := json.Marshal(map[string]any{"paths": paths, "target": target.URL, "allowFailure": failure})
		command := exec.CommandContext(t.Context(), "node", "-e", script)
		command.Stdin = strings.NewReader(string(input))
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("真实Chromium测试失败: %s %v", output, err)
		}
		want := `["BROWSER_A","BROWSER_B"]`
		if failure {
			want = `["FAILED","BROWSER_B"]`
		}
		if string(output) != want || direct.Load() != 0 {
			t.Fatalf("浏览器串线或直连: %s direct=%d", output, direct.Load())
		}
		t.Logf("双 Chromium failure=%t results=%s direct=%d", failure, output, direct.Load())
	}
}
