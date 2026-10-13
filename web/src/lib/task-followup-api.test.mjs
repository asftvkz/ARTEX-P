import { api } from "./api.ts";
import assert from "node:assert/strict";
import test from "node:test";

const original = globalThis.fetch;
test.afterEach(() => { globalThis.fetch = original; });
for (const mode of ["question", "execute"]) {
  test(`完成后${mode}沿用任务与原执行身份`, async () => {
    let calls = 0;
    globalThis.fetch = async (url, init) => {
      calls++;
      assert.equal(url, "/api/chat?task=12");
      assert.deepEqual(JSON.parse(init.body), { message: "继续处理", seg: 2, followup: { intent_id: 44, mode } });
      return Response.json({ ok: true });
    };
    await api.chat("继续处理", "12", undefined, 2, { intent_id: 44, mode });
    assert.equal(calls, 1);
  });
}
