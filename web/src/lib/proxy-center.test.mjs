import {
  buildProxyPayload,
  proxyEndpoint,
  proxyErrorText,
  proxySelectable,
  taskProxyError,
  taskProxyLabel,
} from "./proxy-center.ts";
import assert from "node:assert/strict";
import test from "node:test";

const form = (extra = {}) => ({
  name: "出口一",
  kind: "http",
  protocol: "socks5",
  url: "http://user@proxy.example:8080",
  host: "resin.example",
  port: "1080",
  password: "",
  platform: "Default",
  adminURL: "",
  adminToken: "",
  enabled: true,
  clearPassword: false,
  clearAdminToken: false,
  ...extra,
});
const proxy = (extra = {}) => ({ id: 3, name: "出口一", kind: "http", enabled: true, status: "healthy", ...extra });
const selection = (extra = {}) => ({
  loading: false,
  error: "",
  mode: "inherit",
  globalID: 3,
  items: [proxy()],
  ...extra,
});

test("编辑不把脱敏地址或空秘密回写，保留服务器凭据", () => {
  assert.deepEqual(buildProxyPayload(form({ url: "" }), true), { name: "出口一", kind: "http", enabled: true });
  const payload = buildProxyPayload(form({ kind: "resin", url: "" }), true);
  assert.equal(payload.protocol, "socks5");
  assert.equal("password" in payload, false);
  assert.equal("resin_admin_token" in payload, false);
});

test("显式清除优先于残留输入，普通密码和 Resin 两种秘密都可单独清除", () => {
  const payload = buildProxyPayload(
    form({
      kind: "resin",
      password: "fake-proxy",
      adminToken: "fake-admin",
      clearPassword: true,
      clearAdminToken: true,
    }),
    true,
  );
  assert.equal(payload.clear_password, true);
  assert.equal(payload.clear_resin_admin_token, true);
  assert.equal("password" in payload, false);
  assert.equal("resin_admin_token" in payload, false);
  assert.equal(buildProxyPayload(form({ clearPassword: true }), true).clear_password, true);
});

test("新秘密按原值发送，不裁剪有效认证字符", () => {
  const payload = buildProxyPayload(
    form({ kind: "resin", protocol: "http", password: " fake-proxy ", adminToken: " fake-admin " }),
    false,
  );
  assert.equal(payload.protocol, "http");
  assert.equal(payload.password, " fake-proxy ");
  assert.equal(payload.resin_admin_token, " fake-admin ");
});

test("WARP 创建不要求 URL 且不携带上个表单的凭据", () => {
  assert.deepEqual(buildProxyPayload(form({ kind: "warp", password: "fake", adminToken: "fake" }), false), {
    name: "出口一",
    kind: "warp",
    accept_terms: true,
  });
});

test("无效协议、端口、名称或 Resin Token 在提交前拒绝", () => {
  for (const value of [
    form({ kind: "socks5" }),
    form({ kind: "resin", port: "70000", password: "fake" }),
    form({ kind: "resin", port: "1.5", password: "fake" }),
    form({ name: " " }),
    form({ kind: "resin" }),
    form({ url: "" }),
  ]) {
    assert.throws(() => buildProxyPayload(value, false));
  }
});

test("目录加载或失败会禁止所有任务模式，成功重试后恢复", () => {
  for (const mode of ["direct", "inherit", "fixed"]) {
    assert.notEqual(taskProxyError(selection({ mode, id: 3, loading: true })), "");
    assert.notEqual(taskProxyError(selection({ mode, id: 3, error: "failed" })), "");
    assert.equal(taskProxyError(selection({ mode, id: 3 })), "");
  }
});

test("已删除、停用、失败或停止的编号绝不降级为直连", () => {
  for (const items of [
    [],
    [proxy({ enabled: false })],
    [proxy({ status: "failed" })],
    [proxy({ status: "stopped" })],
    [proxy({ status: "starting" })],
    [proxy({ status: "future-status" })],
  ]) {
    assert.notEqual(taskProxyError(selection({ mode: "fixed", id: 3, items })), "");
    assert.notEqual(taskProxyError(selection({ items })), "");
    assert.equal(taskProxyError(selection({ mode: "direct", items })), "");
  }
  assert.notEqual(taskProxyError(selection({ mode: "fixed" })), "");
  assert.equal(taskProxyError(selection({ globalID: null, items: [] })), "");
  assert.equal(proxySelectable(proxy({ status: "unknown" })), true);
});

test("显示快照并区分默认直连与缺失快照", () => {
  assert.equal(taskProxyLabel({ proxy_mode: "inherit", proxy: proxy({ id: 8 }) }), "代理 #8");
  assert.equal(taskProxyLabel({ proxy_mode: "inherit", proxy_id: 0 }), "默认直连");
  assert.equal(taskProxyLabel({ proxy_mode: "fixed", proxy_id: 8 }), "代理快照缺失");
  assert.equal(taskProxyLabel({}), "代理快照缺失");
  assert.equal(taskProxyLabel({ proxy_mode: "direct" }), "直连");
});

test("URL、诊断和无效地址均不泄露示例凭据", () => {
  const endpoint = proxyEndpoint(
    proxy({ url: "http://fake-user:fake-password@proxy.example:8080?token=fake-secret#fake-secret" }),
  );
  assert.equal(endpoint.includes("fake-"), false);
  assert.equal(proxyEndpoint(proxy({ url: "fake-password" })), "地址格式未知");
  const error = proxyErrorText(
    new Error(
      "http://fake-user:fake-password@proxy.example Bearer fake-bearer token=fake-token password:fake-password",
    ),
  );
  assert.equal(error.includes("fake-"), false);
});
