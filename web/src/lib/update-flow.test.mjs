import assert from "node:assert/strict";
import test from "node:test";
import { waitForUpdate } from "./update-flow.ts";

const stable = (current) => ({ current, pending: false, running: false, rolled_back: false });

test("升级只接受准确目标且稳定，不把旧进程或其他版本当成功", async () => {
  const states = [stable("2.2.2"), stable("2.9.0"), { ...stable("2.2.3"), pending: true }, stable("2.2.3")];
  let reads = 0;
  const result = await waitForUpdate("v2.2.3", "2.2.2", async () => states[reads++], {
    attempts: 4,
    delay: async () => {},
  });
  assert.equal(result.current, "2.2.3");
  assert.equal(reads, 4);
});

test("断线后继续读取持久状态，失败回滚必须报失败", async () => {
  let reads = 0;
  await assert.rejects(
    waitForUpdate(
      "2.2.3",
      "2.2.2",
      async () => {
        if (reads++ === 0) throw new TypeError("连接中断");
        return { ...stable("2.2.2"), rolled_back: true, message: "新版启动失败，已回滚" };
      },
      { attempts: 3, delay: async () => {} },
    ),
    /已回滚/,
  );
  assert.equal(reads, 2);
});

test("下载或校验失败立即结束，不会永远等待版本变化", async () => {
  await assert.rejects(
    waitForUpdate("2.2.3", "2.2.2", async () => ({ ...stable("2.2.2"), phase: "failed", error: "SHA256 不匹配" }), {
      attempts: 2,
      delay: async () => {},
    }),
    /SHA256/,
  );
});

test("主动回滚兼容旧备份无版本记录，但仍等待稳定", async () => {
  const result = await waitForUpdate(undefined, "2.2.3", async () => stable("2.2.2"), {
    attempts: 1,
    delay: async () => {},
  });
  assert.equal(result.current, "2.2.2");
});

test("未完成时超时如实失败，卸载后不再发请求", async () => {
  await assert.rejects(
    waitForUpdate("2.2.3", "2.2.2", async () => stable("2.2.2"), { attempts: 1, delay: async () => {} }),
    /超时/,
  );
  const controller = new AbortController();
  controller.abort();
  let reads = 0;
  await assert.rejects(
    waitForUpdate(
      "2.2.3",
      "2.2.2",
      async () => {
        reads++;
        return stable("2.2.3");
      },
      { signal: controller.signal, delay: async () => {} },
    ),
    /取消/,
  );
  assert.equal(reads, 0);
});
