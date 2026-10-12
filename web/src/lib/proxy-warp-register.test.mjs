import { proxyFromFailure, WARP_TERMS_URL, warpAllowedActions, warpReady, warpRegisterBody } from "./proxy-center.ts";
import assert from "node:assert/strict";
import test from "node:test";

const item = (extra = {}) => ({
  id: 31,
  name: "合成 WARP",
  kind: "warp",
  enabled: true,
  status: "healthy",
  warp_phase: "ready",
  egress_ip: "203.0.113.42",
  ...extra,
});

test("仅真实 ready/healthy/启用/出口 IP 同时成立才显示连接成功", () => {
  assert.equal(warpReady(item()), true);
  for (const patch of [
    { status: "failed" },
    { warp_phase: "stopped" },
    { warp_phase: "failed" },
    { egress_ip: "" },
    { enabled: false },
  ])
    assert.equal(warpReady(item(patch)), false);
});

test("注册不确定、启动中、未知 phase 禁止重注册；失败只能恢复同一条目", () => {
  for (const phase of ["registering", "starting", "future-phase", undefined])
    assert.deepEqual(warpAllowedActions(item({ warp_phase: phase })), []);
  assert.deepEqual(warpAllowedActions(item({ warp_phase: "failed", status: "failed" })), ["recover"]);
  assert.deepEqual(warpAllowedActions(item({ warp_phase: "unregistered" })), ["register"]);
  assert.deepEqual(warpAllowedActions(item({ warp_phase: "stopped" })), ["start", "recover"]);
  assert.deepEqual(warpAllowedActions(item()), ["stop"]);
});

test("已建档失败只接受与 proxy_id 一致的正数 ID；网络失败不按名称猜 ID", () => {
  const proxy = item({ status: "failed", warp_phase: "failed" });
  assert.deepEqual(proxyFromFailure({ payload: { proxy_id: 31, proxy } }), proxy);
  for (const error of [new TypeError("fetch failed"), { payload: { proxy_id: 99, proxy } }, { payload: { proxy } }])
    assert.equal(proxyFromFailure(error), null);
});

test("旧未注册条目保留明确同意修复，官方条款链接使用 Cloudflare 域名", () => {
  assert.equal(warpRegisterBody(false), null);
  assert.deepEqual(warpRegisterBody(true), { accept_terms: true });
  assert.equal(new URL(WARP_TERMS_URL).hostname, "www.cloudflare.com");
});
