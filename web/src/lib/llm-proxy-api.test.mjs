import { APIError, api } from "./api.ts";
import { resolveApiKeyToSend } from "./llm-models.ts";
import { llmProxyDraft, llmProxyRequest } from "./llm-proxy.ts";
import { buildProxyPayload, proxyFromFailure } from "./proxy-center.ts";
import assert from "node:assert/strict";
import test from "node:test";

const calls = [];
const originalFetch = globalThis.fetch;
globalThis.fetch = async (url, init) => {
  calls.push({ url, body: init.body ? JSON.parse(init.body) : {}, method: init.method });
  return Response.json({ id: 7, ok: true, models: ["synthetic-model"] });
};
test.after(() => {
  globalThis.fetch = originalFetch;
});
const fields = { id: 7, name: "合成渠道", format: "openai", model: "synthetic-model" };
const proxyKeys = (body) =>
  Object.fromEntries(Object.entries(body).filter(([k]) => ["proxy", "proxy_mode", "proxy_id"].includes(k)));

async function submitThree(draft, key = "") {
  const selection = llmProxyRequest(draft);
  await api.saveLLMProfile({ ...fields, ...selection, api_key: key });
  await api.testLLM("openai", "synthetic-model", "", key, selection, "", "", "7");
  await api.fetchLLMModels("openai", "", key, selection, "7");
  return calls.splice(0);
}

test("API 客户端请求载荷 保存/测试/拉模型：legacy 未改仅按渠道 ID 读取代理，三个选择字段全部省略", async () => {
  const requests = await submitThree(
    llmProxyDraft({ proxy_mode: "", proxy_id: null, proxy: "http://***@127.0.0.1:1080" }),
  );
  assert.deepEqual(
    requests.map((r) => r.url),
    ["/api/llm/profiles", "/api/llm/test", "/api/llm/models"],
  );
  for (const r of requests) {
    assert.deepEqual(proxyKeys(r.body), {});
    assert.equal(r.body.profile_id ?? r.body.id, r.body.id ? 7 : "7");
    assert.equal(JSON.stringify(r).includes("***"), false);
  }
});

for (const mode of ["inherit", "direct", "fixed"]) {
  test(`API 客户端请求载荷 三路径：显式 ${mode} 只发送 mode/id，清旧 URL；ID 无浮点转换`, async () => {
    const id = "9007199254740993";
    const requests = await submitThree({ mode, id, changed: true });
    for (const r of requests)
      assert.deepEqual(proxyKeys(r.body), { proxy_mode: mode, proxy_id: mode === "fixed" ? id : null });
  });
}

test("API 客户端请求载荷 三路径：未明确更换的浏览器残留 Key 不进入请求；显式替换才发送新 Key", async () => {
  for (const replace of [false, true]) {
    const key = resolveApiKeyToSend(false, replace, "fake-autofill-key");
    const requests = await submitThree({ mode: "direct", id: "", changed: true }, key);
    for (const r of requests) assert.equal(r.body.api_key, replace ? "fake-autofill-key" : "");
  }
});

test("API 客户端请求载荷 适配器显式模式忽略旧 URL；legacy 省略与明确 proxy 空值不混同", async () => {
  await api.saveLLMProfile({ ...fields, proxy_mode: "direct", proxy_id: null, proxy: "http://fake-secret@proxy" });
  await api.testLLM("openai", "m", "", "", { proxy_mode: "fixed", proxy_id: "42", proxy: "http://fake-secret@proxy" });
  await api.fetchLLMModels("openai", "", "", undefined, "7");
  assert.deepEqual(
    calls.splice(0).map((r) => proxyKeys(r.body)),
    [{ proxy_mode: "direct", proxy_id: null }, { proxy_mode: "fixed", proxy_id: "42" }, {}],
  );
});

test("非法 fixed ID 提交前拒绝，保持原选择不降级", () => {
  for (const id of ["", "0", "-1", "1.2", "1e3", "null"]) {
    assert.throws(() => llmProxyRequest({ mode: "fixed", id, changed: true }));
  }
});

test("API 客户端请求载荷 WARP 新建只发名称/类型/条款接受，不带上个表单的 URL/秘密", async () => {
  await api.createProxy(
    buildProxyPayload(
      { name: " 合成 WARP ", kind: "warp", enabled: false, password: "fake", adminToken: "fake" },
      false,
    ),
  );
  assert.deepEqual(calls.splice(0)[0], {
    url: "/api/proxies",
    method: "POST",
    body: { name: "合成 WARP", kind: "warp", accept_terms: true },
  });
});

test("API 客户端请求载荷 502 保留已建档 DTO/ID；GET 和恢复使用同 ID，错误不吞响应包", async () => {
  const item = { id: 31, name: "失败合成", kind: "warp", warp_phase: "failed", enabled: true, status: "failed" };
  const capture = globalThis.fetch;
  globalThis.fetch = async () => Response.json({ error: "连接失败", proxy_id: 31, proxy: item }, { status: 502 });
  try {
    await assert.rejects(api.createProxy({ name: "失败合成", kind: "warp", accept_terms: true }), (e) => {
      assert.ok(e instanceof APIError);
      assert.deepEqual(proxyFromFailure(e), item);
      return true;
    });
  } finally {
    globalThis.fetch = capture;
  }
  await api.proxyWarpAction(31, "recover");
  const request = calls.splice(0)[0];
  assert.equal(request.url, "/api/proxies/31/warp/recover");
  assert.equal("accept_terms" in request.body, false);
});
