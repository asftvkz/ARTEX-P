#!/usr/bin/env node
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import crypto from "node:crypto";
import { createRequire } from "node:module";
import { spawnSync } from "node:child_process";
import { pathToFileURL } from "node:url";

export function browserConfig(env, executablePath, uid = process.getuid?.()) {
  const managed = env.ARTEX_PLAYWRIGHT_MANAGED === "1";
  const raw = managed ? env.ARTEX_PLAYWRIGHT_PROXY || "" : "";
  const config = { browser: { browserName: "chromium", isolated: true,
    launchOptions: { executablePath, headless: true, ...(uid === 0 ? { chromiumSandbox: false } : {}) },
    contextOptions: {} } };
  if (raw) {
    const proxy = new URL(raw);
    if (!["http:", "https:"].includes(proxy.protocol) || !proxy.hostname) throw new Error("unsupported proxy");
    config.browser.launchOptions.proxy = { server: `${proxy.protocol}//${proxy.host}`, bypass: "<-loopback>",
      ...(proxy.username ? { username: decodeURIComponent(proxy.username), password: decodeURIComponent(proxy.password) } : {}) };
    config.browser.launchOptions.args = ["--proxy-bypass-list=<-loopback>"];
    // 只在本任务录制 CA 存在时接受重签证书，不修改系统/其他浏览器信任。
    if (env.ARTEX_PLAYWRIGHT_CA) config.browser.contextOptions.ignoreHTTPSErrors = true;
  } else if (managed) config.browser.launchOptions.args = ["--no-proxy-server"];
  return config;
}

export function sessionName(cwd, env, name = "default") {
  // 每个任务工作目录和出口分别命名，代理切换后无法接入旧会话。
  return "artex-" + crypto.createHash("sha256").update(JSON.stringify([
    cwd, env.ARTEX_PLAYWRIGHT_PROXY || "", env.ARTEX_PLAYWRIGHT_CA || "", env.ARTEX_PLAYWRIGHT_RUN || "", name
  ])).digest("hex").slice(0, 28);
}

export function parseArgs(argv) {
  const args = []; let name = "default";
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    if (arg === "-s" || arg === "--session") { name = argv[++i]; if (!name || name.startsWith("-")) throw new Error("session missing"); }
    else if (arg.startsWith("-s=")) name = arg.slice(3);
    else if (arg.startsWith("--session=")) name = arg.slice(10);
    else args.push(arg);
  }
  if (!name) throw new Error("session missing");
  return { args, name };
}

export function redactOutput(output, rawProxy) {
  if (!rawProxy) return output;
  const proxy = new URL(rawProxy);
  for (const secret of [rawProxy, proxy.username, proxy.password, decodeURIComponent(proxy.username), decodeURIComponent(proxy.password)]) {
    if (secret) output = output.split(secret).join("***");
  }
  return output;
}

export async function main(argv = process.argv.slice(2), env = process.env) {
  const cleanupScope = path.join(os.tmpdir(), sessionName(process.cwd(), env, "") + "-sessions");
  if (argv[0] === "cleanup" && (!fs.existsSync(cleanupScope) || fs.readdirSync(cleanupScope).length === 0)) return;
  const root = env.ARTEX_PLAYWRIGHT_ROOT || "/opt/artex-playwright";
  const require = createRequire(path.join(root, "package.json"));
  const executable = require("playwright").chromium.executablePath();
  const cli = require.resolve("@playwright/cli/playwright-cli.js");
  const managed = env.ARTEX_PLAYWRIGHT_MANAGED === "1";
  let { args, name } = parseArgs(argv);
  const session = sessionName(process.cwd(), env, name);
  const dir = path.join(os.tmpdir(), session);
  fs.mkdirSync(dir, { recursive: true, mode: 0o700 });
  const statePath = path.join(dir, "video.json");
  const command = args.find((arg) => !arg.startsWith("-"));
  if (managed && command && args.slice(0,args.indexOf(command)).some(arg=>!["--json","--raw"].includes(arg))) throw new Error("managed global option denied");
  if (managed && ["attach", "kill-all", "delete-data", "close-all"].includes(command)) {
    throw new Error("managed session operation denied");
  }
  const childEnv = { ...env, PLAYWRIGHT_CLI_SESSION: session };
  // 不能让共享 MCP 环境覆盖任务选择或录制信任范围。
  for (const key of Object.keys(childEnv)) if (key.startsWith("PLAYWRIGHT_MCP_")) delete childEnv[key];
  delete childEnv.PLAYWRIGHT_CLI_DAEMON_SESSION_NAME;
  if (managed && !env.ARTEX_PLAYWRIGHT_CA) delete childEnv.NODE_EXTRA_CA_CERTS;
  const scope = sessionName(process.cwd(), env, "");
  const scopeDir = path.join(os.tmpdir(), scope + "-sessions");
  fs.mkdirSync(scopeDir, { recursive: true, mode: 0o700 });
  if (command === "cleanup") {
    for (const filename of fs.readdirSync(scopeDir)) {
      if (!/^artex-[a-f0-9]{28}$/.test(filename)) continue;
      const closed = spawnSync(process.execPath, [cli, "close", `--session=${filename}`], { env: childEnv, timeout: 4000 });
      if (closed.error || closed.status !== 0) throw new Error("session close failed");
      fs.rmSync(path.join(scopeDir, filename), { force: true });
      fs.rmSync(path.join(os.tmpdir(), filename, "config.json"), { force: true });
    }
    return;
  }
  if (command === "open") {
    if (!fs.existsSync(executable)) throw new Error("browser missing");
    let userConfig = {};
    for (let i = 0; i < args.length; i++) {
      if (args[i] === "--config" || args[i].startsWith("--config=")) {
        const configPath = args[i] === "--config" ? args[++i] : args[i].slice(9);
        userConfig = JSON.parse(fs.readFileSync(configPath, "utf8"));
      }
    }
    args = args.filter((arg, i) => arg !== "--config" && args[i - 1] !== "--config" && !arg.startsWith("--config="));
    // 容器使用已匹配的无头浏览器；外接/持久配置不能绕开任务会话及出口。
    if (managed && args.some(arg => /^(--(browser|profile|persistent|headed|extension|cdp|endpoint|idle-timeout))(=|$)/.test(arg))) throw new Error("managed browser override denied");
    const config = browserConfig(env, executable);
    config.browser.contextOptions = { ...userConfig.browser?.contextOptions, ...config.browser.contextOptions };
    if (managed) { delete config.browser.contextOptions.proxy;
      if (!env.ARTEX_PLAYWRIGHT_CA) delete config.browser.contextOptions.ignoreHTTPSErrors; }
    const configPath = path.join(dir, "config.json");
    fs.writeFileSync(configPath, JSON.stringify(config), { mode: 0o600 });
    args.push(`--config=${configPath}`, "--idle-timeout=60000");
    fs.writeFileSync(path.join(scopeDir, session), "", { mode: 0o600 });
  }
  if (command === "video-start") {
    const i = args.indexOf(command);
    const filename = args[i + 1] && !args[i + 1].startsWith("-") ? args[i + 1] : "video.webm";
    const videoPath = path.resolve(filename);
    // 不把已有旧录像当成此次生成成功，也不覆盖用户文件。
    if (fs.existsSync(videoPath)) throw new Error("video already exists");
    fs.mkdirSync(path.dirname(videoPath), { recursive: true });
    if (args[i + 1] === filename) args[i + 1] = videoPath; else args.splice(i + 1, 0, videoPath);
    fs.writeFileSync(statePath, JSON.stringify({ videoPath }), { mode: 0o600 });
  }
  const result = spawnSync(process.execPath, [cli, ...args], { env: childEnv, encoding: "utf8", timeout: 55000, maxBuffer: 8 * 1024 * 1024 });
  if (command === "video-stop" && result.status === 0) {
    const { videoPath } = JSON.parse(fs.readFileSync(statePath, "utf8"));
    for (let retry = 0; retry < 50 && (!fs.existsSync(videoPath) || fs.statSync(videoPath).size < 4); retry++) {
      await new Promise(resolve => setTimeout(resolve, 100));
    }
    const header = Buffer.alloc(4);
    const fd = fs.openSync(videoPath, "r");
    fs.readSync(fd, header, 0, 4, 0); fs.closeSync(fd);
    if (!header.equals(Buffer.from([0x1a, 0x45, 0xdf, 0xa3]))) throw new Error("video missing or invalid");
  }
  if (result.stdout) process.stdout.write(redactOutput(result.stdout, env.ARTEX_PLAYWRIGHT_PROXY));
  if (result.stderr) process.stderr.write(redactOutput(result.stderr, env.ARTEX_PLAYWRIGHT_PROXY));
  if (result.error) throw new Error("browser command failed");
  if (command === "close" && result.status === 0) {
    fs.rmSync(path.join(dir, "config.json"), { force: true });
    fs.rmSync(path.join(scopeDir, session), { force: true });
  }
  process.exitCode = result.status ?? 1;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main().catch(() => { console.error("[artex] 浏览器命令失败；请核对工具包、任务出口及生成文件，未确认成功。"); process.exitCode = 1; });
}
