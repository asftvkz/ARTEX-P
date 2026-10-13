// Package browserruntime 把浏览器包装器和固定依赖随程序发布，供容器内持久更新。
package browserruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

//go:embed package.json package-lock.json playwright-cli.mjs
var assets embed.FS

// Prepare 只在容器内写数据目录，不操作宿主机、Docker socket 或系统全局包。
// 工具按内容版本分目录；新版失败不会破坏旧版回滚所需的依赖。
func Prepare(dataDir string) (string, error) {
	lock, _ := assets.ReadFile("package-lock.json")
	wrapper, _ := assets.ReadFile("playwright-cli.mjs")
	versionHash := sha256.Sum256(append(append([]byte(nil), lock...), wrapper...))
	root := filepath.Join(dataDir, "browser-runtime", hex.EncodeToString(versionHash[:8]))
	if runtime.GOOS != "linux" {
		return "", nil
	}
	if _, err := os.Stat("/.dockerenv"); err != nil {
		return "", nil
	}
	if _, err := exec.LookPath("node"); err != nil {
		return "", errors.New("浏览器工具需要容器内的 Node")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0700); err != nil {
		return "", err
	}
	for _, name := range []string{"package.json", "package-lock.json", "playwright-cli.mjs"} {
		content, _ := assets.ReadFile(name)
		if err := os.WriteFile(filepath.Join(root, name), content, 0600); err != nil {
			return "", err
		}
	}
	// 已匹配的镜像依赖可直接复用，包装器仍随本次程序更新。
	dependencyRoot := root
	if baked, err := os.ReadFile("/opt/artex-playwright/package-lock.json"); err == nil && bytes.Equal(baked, lock) {
		dependencyRoot = "/opt/artex-playwright"
	}
	launchers := map[string]string{
		"playwright-cli":       filepath.Join(root, "playwright-cli.mjs"),
		"artex-playwright-cli": filepath.Join(root, "playwright-cli.mjs"),
		"playwright-mcp":       filepath.Join(dependencyRoot, "node_modules", "@playwright", "mcp", "cli.js"),
		"playwright":           filepath.Join(dependencyRoot, "node_modules", "playwright", "cli.js"),
	}
	for name, target := range launchers {
		quoted := "'" + strings.ReplaceAll(target, "'", "'\"'\"'") + "'"
		if err := os.WriteFile(filepath.Join(root, "bin", name), []byte("#!/bin/sh\nexec node "+quoted+" \"$@\"\n"), 0700); err != nil {
			return "", err
		}
	}
	if err := os.Setenv("ARTEX_PLAYWRIGHT_ROOT", dependencyRoot); err != nil {
		return "", err
	}
	if dependencyRoot == root {
		if err := os.Setenv("PLAYWRIGHT_BROWSERS_PATH", filepath.Join(root, "browsers")); err != nil {
			return "", err
		}
	}
	if err := os.Setenv("PATH", filepath.Join(root, "bin")+string(os.PathListSeparator)+os.Getenv("PATH")); err != nil {
		return "", err
	}
	return dependencyRoot, nil
}

// Executable 返回与锁文件匹配的 Chromium，不依赖不存在的 Google Chrome channel。
func Executable(ctx context.Context) (string, error) {
	root := os.Getenv("ARTEX_PLAYWRIGHT_ROOT")
	if root == "" {
		return "", nil
	}
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(bounded, "node", "-e", `process.stdout.write(require(process.argv[1]).chromium.executablePath())`, filepath.Join(root, "node_modules", "playwright"))
	output, err := cmd.Output()
	if err != nil {
		return "", errors.New("浏览器工具包尚未就绪")
	}
	path := string(output)
	if _, err = os.Stat(path); err != nil {
		return "", errors.New("匹配的 Chromium 尚未就绪")
	}
	return path, nil
}

// Install 缺少依赖时后台补齐；每一步有独立超时，失败不会回落到版本不匹配的旧 CLI。
func Install(ctx context.Context, root string) error {
	if root == "" || root == "/opt/artex-playwright" {
		return nil
	}
	if _, err := os.Stat(filepath.Join(root, "ready")); err == nil {
		return nil
	}
	steps := [][]string{
		{"npm", "ci", "--prefix", root, "--ignore-scripts", "--registry=https://registry.npmjs.org"},
		{"node", filepath.Join(root, "node_modules", "playwright", "cli.js"), "install", "chromium", "ffmpeg"},
	}
	for _, step := range steps {
		bounded, cancel := context.WithTimeout(ctx, 55*time.Second)
		command := exec.CommandContext(bounded, step[0], step[1:]...)
		command.Env = downloadEnvironment(root)
		command.Stdout, command.Stderr = nil, nil
		err := command.Run()
		cancel()
		if err != nil {
			return errors.New("浏览器工具包安装未完成，可重启后重试；不会使用旧版本代替")
		}
	}
	if err := os.WriteFile(filepath.Join(root, "ready"), []byte("ready\n"), 0600); err != nil {
		return err
	}
	log.Print("[browser] 固定版本 CLI、MCP、Chromium 及录像依赖已就绪")
	return nil
}

func downloadEnvironment(root string) []string {
	blocked := map[string]bool{
		"HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true, "NO_PROXY": true,
		"http_proxy": true, "https_proxy": true, "all_proxy": true, "no_proxy": true,
		"NODE_EXTRA_CA_CERTS":      true,
		"PLAYWRIGHT_DOWNLOAD_HOST": true, "PLAYWRIGHT_CHROMIUM_DOWNLOAD_HOST": true,
		"PLAYWRIGHT_FIREFOX_DOWNLOAD_HOST": true, "PLAYWRIGHT_WEBKIT_DOWNLOAD_HOST": true,
		"PLAYWRIGHT_MCP_SESSION": true, "PLAYWRIGHT_MCP_SESSION_DIR": true,
		"ARTEX_PLAYWRIGHT_PROXY": true, "ARTEX_PLAYWRIGHT_CA": true,
		"ARTEX_PLAYWRIGHT_MANAGED": true, "ARTEX_PLAYWRIGHT_RUN": true,
		"ARTEX_PLAYWRIGHT_ROOT": true,
	}
	out := make([]string, 0, len(os.Environ())+3)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !blocked[key] {
			out = append(out, entry)
		}
	}
	out = append(out,
		"npm_config_registry=https://registry.npmjs.org",
		"PLAYWRIGHT_BROWSERS_PATH="+filepath.Join(root, "browsers"),
		"ARTEX_PLAYWRIGHT_ROOT="+root,
	)
	return out
}
