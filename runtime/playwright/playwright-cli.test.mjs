import test from "node:test";
import assert from "node:assert/strict";
import { browserConfig, sessionName, parseArgs, redactOutput } from "./playwright-cli.mjs";

test("容器 root 默认无头且使用匹配浏览器，不找 Google Chrome", () => {
  const c = browserConfig({}, "/controlled/chromium", 0);
  assert.equal(c.browser.launchOptions.executablePath, "/controlled/chromium");
  assert.equal(c.browser.launchOptions.chromiumSandbox, false);
  assert.equal(c.browser.launchOptions.headless, true);
});
test("认证代理和录制信任局限于任务，直连没有代理或证书豁免", () => {
  const env = { ARTEX_PLAYWRIGHT_MANAGED: "1", ARTEX_PLAYWRIGHT_PROXY: "http://fixture-user:fixture-pass@127.0.0.1:1234", ARTEX_PLAYWRIGHT_CA: "/task/ca" };
  const c = browserConfig(env, "/chromium", 0);
  assert.deepEqual(c.browser.launchOptions.proxy, { server: "http://127.0.0.1:1234", bypass: "<-loopback>", username: "fixture-user", password: "fixture-pass" });
  assert.equal(c.browser.contextOptions.ignoreHTTPSErrors, true);
  const direct = browserConfig({ ARTEX_PLAYWRIGHT_MANAGED:"1" }, "/chromium", 1);
  assert.equal(direct.browser.launchOptions.proxy, undefined);
  assert.equal(direct.browser.contextOptions.ignoreHTTPSErrors, undefined);
  assert.deepEqual(direct.browser.launchOptions.args, ["--no-proxy-server"]);
});
test("代理、工作目录或命名会话变化都产生不同的浏览器身份", () => {
  const env = {ARTEX_PLAYWRIGHT_PROXY:"http://a:80"};
  const a = sessionName("/task/a",env);
  assert.notEqual(a, sessionName("/task/b",env));
  assert.notEqual(a, sessionName("/task/a",{ARTEX_PLAYWRIGHT_PROXY:"http://b:80"}));
  assert.notEqual(a, sessionName("/task/a",env,"other"));
  for (const option of [["-s=demo"],["--session=demo"],["-s","demo"],["--session","demo"]]) {
    assert.deepEqual(parseArgs(["open",...option,"https://example.invalid"]), {args:["open","https://example.invalid"],name:"demo"});
  }
});
test("输出过滤代理秘密，录像报错不会泄漏代理凭据", () => {
  const proxy="http://fixture-user:fixture-pass@localhost:80";
  const output=redactOutput(`${proxy} fixture-user fixture-pass`,proxy);
  assert.equal(output,"*** *** ***");
});
