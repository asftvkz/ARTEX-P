import assert from "node:assert/strict";
import test from "node:test";
import { api } from "./api.ts";

const originalFetch = globalThis.fetch;
test.afterEach(() => {
  globalThis.fetch = originalFetch;
});

test("回滚到旧版没有状态接口时读取健康版本，不重新提交操作", async () => {
  const requests = [];
  globalThis.fetch = async (url) => {
    requests.push(url);
    if (String(url).endsWith("/update/status")) return Response.json({ error: "旧版无此接口" }, { status: 404 });
    return Response.json({ version: "2.2.2" });
  };
  const state = await api.updateStatus();
  assert.equal(state.current, "2.2.2");
  assert.equal(state.pending, false);
  assert.deepEqual(requests, ["/api/update/status", "/api/health"]);
});

test("状态接口认证或暂时故障时不伪造稳定状态", async () => {
  let requests = 0;
  globalThis.fetch = async () => {
    requests++;
    return Response.json({ error: "暂时不可用" }, { status: 503 });
  };
  await assert.rejects(api.updateStatus());
  assert.equal(requests, 1);
});
