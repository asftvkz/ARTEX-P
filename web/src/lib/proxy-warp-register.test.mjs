import { WARP_REGISTER_TERMS, warpActionNeedsTerms, warpRegisterBody } from "./proxy-center.ts";
import assert from "node:assert/strict";
import test from "node:test";

// 把"注册确认"这段交互单独提出来测：确认框只是 UI 状态，真正的契约是
// "未确认/取消 => 一次请求都不发；确认 => 带 accept_terms:true"。
// 这里用一个极小的、与 React 无关的状态机复刻该行为，直接数请求次数。
function makeRegisterController(submit) {
  const state = { open: false, accepted: false, submitted: 0, cancelled: 0, failed: false };
  return {
    state,
    openDialog() {
      state.open = true;
      state.accepted = false; // 每次打开都必须回到未勾选，绝不预选
      state.failed = false;
    },
    cancel() {
      state.open = false;
      state.accepted = false;
      state.cancelled += 1;
    },
    async confirm() {
      const body = warpRegisterBody(state.accepted);
      if (body === null) return; // 未勾选确认：不发送请求
      state.submitted += 1;
      try {
        await submit(body);
        state.open = false;
        state.failed = false;
      } catch {
        // 失败保留可重试状态：确认框与勾选都留着，用户可再次确认
        state.failed = true;
      }
    },
  };
}

test("未勾选确认时确认动作不发送任何请求", async () => {
  const calls = [];
  const c = makeRegisterController(async (body) => {
    calls.push(body);
  });
  c.openDialog();
  assert.equal(c.state.accepted, false, "打开确认框不得预选同意");
  await c.confirm();
  assert.equal(calls.length, 0);
  assert.equal(c.state.submitted, 0);
});

test("取消注册：请求数为 0，且不残留已同意状态", async () => {
  const calls = [];
  const c = makeRegisterController(async (body) => {
    calls.push(body);
  });
  c.openDialog();
  c.state.accepted = true; // 先勾上再取消
  c.cancel();
  await c.confirm();
  assert.equal(calls.length, 0);
  assert.equal(c.state.cancelled, 1);
  assert.equal(c.state.open, false);
  assert.equal(c.state.accepted, false);
});

test("确认后只发送 accept_terms:true，且请求体不含其它字段", async () => {
  const calls = [];
  const c = makeRegisterController(async (body) => {
    calls.push(body);
  });
  c.openDialog();
  c.state.accepted = true;
  await c.confirm();
  assert.equal(calls.length, 1);
  assert.deepEqual(calls[0], { accept_terms: true });
  assert.deepEqual(Object.keys(calls[0]), ["accept_terms"]);
});

test("接口失败：保留可重试状态，重试成功才算完成", async () => {
  let attempt = 0;
  const sent = [];
  const c = makeRegisterController(async (body) => {
    sent.push(body);
    attempt += 1;
    if (attempt === 1) throw new Error("网络错误");
  });
  c.openDialog();
  c.state.accepted = true;
  await c.confirm();
  assert.equal(c.state.failed, true);
  assert.equal(c.state.open, true, "失败后确认框应保留，允许重试");
  assert.equal(c.state.accepted, true, "失败后勾选状态应保留，不必重新确认");

  await c.confirm();
  assert.equal(sent.length, 2);
  assert.equal(c.state.failed, false);
  assert.equal(c.state.open, false);
});

test("只有注册需要条款确认，start/stop/recover 不涉及", () => {
  assert.equal(warpActionNeedsTerms("register"), true);
  assert.equal(warpActionNeedsTerms("start"), false);
  assert.equal(warpActionNeedsTerms("stop"), false);
  assert.equal(warpActionNeedsTerms("recover"), false);
});

test("未确认返回 null（而不是 accept_terms:false）", () => {
  assert.equal(warpRegisterBody(false), null);
  assert.deepEqual(warpRegisterBody(true), { accept_terms: true });
});

test("注册条款说明不虚构外部链接", () => {
  assert.ok(WARP_REGISTER_TERMS.length >= 3);
  for (const line of WARP_REGISTER_TERMS) {
    assert.equal(/https?:\/\//.test(line), false, `条款文案不应包含凭空编造的链接：${line}`);
  }
});
