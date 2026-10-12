import { resolveApiKeyToSend, rowsFromResponse, rowsToPayload, saveChannelWithModels } from "./llm-models.ts";
import assert from "node:assert/strict";
import test from "node:test";

const model = (extra = {}) => ({
  id: "1",
  profile_id: "7",
  upstream_model_id: "deepseek-v3",
  display_name: "DeepSeek Chat",
  enabled: true,
  is_default: true,
  ...extra,
});

test("rowsFromResponse 按 upstream_model_id 建 key，识别默认成员", () => {
  const { rows, defaultKey } = rowsFromResponse([
    model({ id: "1", upstream_model_id: "deepseek-v3", is_default: true }),
    model({ id: "2", upstream_model_id: "deepseek-r1", display_name: "deepseek-r1", is_default: false }),
  ]);
  assert.equal(rows.length, 2);
  assert.equal(defaultKey, "deepseek-v3");
  assert.deepEqual(
    rows.map((r) => r.key),
    ["deepseek-v3", "deepseek-r1"],
  );
  assert.equal(rows[0].id, "1");
  assert.equal(rows[1].displayName, "deepseek-r1");
});

test("rowsFromResponse 没有 is_default 时回退到第一项，避免无默认项", () => {
  const { defaultKey } = rowsFromResponse([model({ id: "9", upstream_model_id: "x", is_default: false })]);
  assert.equal(defaultKey, "x");
});

test("rowsToPayload 批量添加多个模型时，只有一个 is_default=true 且是正确的那个", () => {
  const rows = [
    { key: "a", upstreamId: "a", displayName: "A", enabled: true },
    { key: "b", upstreamId: "b", displayName: "b", enabled: true },
    { key: "c", upstreamId: "c", displayName: "c", enabled: true },
  ];
  const payload = rowsToPayload(rows, "b");
  assert.equal(payload.length, 3);
  assert.deepEqual(
    payload.map((p) => p.is_default),
    [false, true, false],
  );
  // 新增(未保存)成员省略 id，交由服务端分配。
  assert.ok(!("id" in payload[0]));
});

test("rowsToPayload 保留已保存成员的 id，展示名去除首尾空白、留空回退到上游 ID", () => {
  const rows = [{ key: "x", id: "42", upstreamId: "x", displayName: "  ", enabled: true }];
  const payload = rowsToPayload(rows, "x");
  assert.equal(payload[0].id, "42");
  assert.equal(payload[0].display_name, "x");
});

test("resolveApiKeyToSend：新建渠道总是发送输入值；编辑渠道只有明确更换才发送", () => {
  assert.equal(resolveApiKeyToSend(true, false, "sk-new"), "sk-new");
  assert.equal(resolveApiKeyToSend(false, false, "sk-leftover"), "");
  assert.equal(resolveApiKeyToSend(false, true, "sk-new"), "sk-new");
  // 取消更换后即便输入框还没清空，也不能把残留值发出去。
  assert.equal(resolveApiKeyToSend(false, false, "sk-should-not-leak"), "");
});

test("saveChannelWithModels 新建：一次不带 id 的创建 + 一次模型列表保存", async () => {
  const calls = { saveProfile: [], saveModels: [] };
  const deps = {
    saveProfile: async (p) => {
      calls.saveProfile.push(p);
      return { id: 101 };
    },
    saveModels: async (id, models) => {
      calls.saveModels.push({ id, models });
      return {};
    },
  };
  const rows = [
    { key: "a", upstreamId: "a", displayName: "A", enabled: true },
    { key: "b", upstreamId: "b", displayName: "B", enabled: true },
  ];
  const result = await saveChannelWithModels(deps, undefined, { name: "渠道A", format: "openai" }, rows, "b");

  assert.deepEqual(result, { ok: true, profileId: 101 });
  assert.equal(calls.saveProfile.length, 1);
  assert.ok(!("id" in calls.saveProfile[0]), "新建不应该带 id");
  assert.equal(calls.saveProfile[0].model, "b", "legacy model 字段用草稿默认行的上游 ID");
  assert.equal(calls.saveModels.length, 1);
  assert.equal(calls.saveModels[0].id, 101);
  assert.equal(calls.saveModels[0].models.length, 2);
});

test("saveChannelWithModels 新建但模型列表保存失败：保留已创建的渠道 id，不判定为整体成功", async () => {
  const deps = {
    saveProfile: async () => ({ id: 202 }),
    saveModels: async () => {
      throw new Error("网络错误");
    },
  };
  const rows = [{ key: "a", upstreamId: "a", displayName: "A", enabled: true }];
  const result = await saveChannelWithModels(deps, undefined, { name: "渠道B", format: "openai" }, rows, "a");

  assert.equal(result.ok, false);
  assert.equal(result.profileId, 202);
  assert.match(result.modelsError, /网络错误/);
});

test("saveChannelWithModels 第二步失败后重试：必须带着保留的 id 更新，不会重复新建渠道", async () => {
  let profileCreates = 0;
  let profileUpdates = 0;
  let modelAttempts = 0;
  const deps = {
    saveProfile: async (p) => {
      if (p.id) profileUpdates += 1;
      else profileCreates += 1;
      return { id: p.id ?? 303 };
    },
    saveModels: async () => {
      modelAttempts += 1;
      if (modelAttempts === 1) throw new Error("第一次失败");
      return {};
    },
  };
  const rows = [{ key: "a", upstreamId: "a", displayName: "A", enabled: true }];
  const fields = { name: "渠道C", format: "openai" };

  const first = await saveChannelWithModels(deps, undefined, fields, rows, "a");
  assert.equal(first.ok, false);
  assert.equal(first.profileId, 303);

  // 调用方必须把第一次返回的 profileId 当作 existingProfileId 传入重试。
  const retry = await saveChannelWithModels(deps, first.profileId, fields, rows, "a");
  assert.equal(retry.ok, true);
  assert.equal(retry.profileId, 303);

  assert.equal(profileCreates, 1, "整个序列里只应该有一次不带 id 的创建");
  assert.equal(profileUpdates, 1, "重试应该带 id 走更新，而不是再次创建");
});

test("saveChannelWithModels 编辑已有渠道：legacy model 送草稿默认项，真正切换默认留给模型列表 PUT", async () => {
  const calls = [];
  const deps = {
    saveProfile: async (p) => {
      calls.push(p);
      return { id: p.id };
    },
    saveModels: async () => ({}),
  };
  const rows = [
    { key: "old-default", id: "1", upstreamId: "old-default", displayName: "旧默认", enabled: true },
    { key: "new-pick", id: "2", upstreamId: "new-pick", displayName: "新选择", enabled: true },
  ];
  // 用户把默认单选切到了 new-pick，但这只是草稿。按契约服务端会忽略渠道普通设置
  // 接口里的 model 字段、不会据此提前改默认——真正切换要靠随后那次模型列表 PUT。
  // 这里只验证我们确实把当前草稿默认项传给了它（不是回退到旧值）。
  await saveChannelWithModels(deps, 7, { name: "渠道D", format: "openai" }, rows, "new-pick");

  assert.equal(calls[0].id, 7);
  assert.equal(calls[0].model, "new-pick");
});

test("saveChannelWithModels 没有任何模型时拒绝保存", async () => {
  const deps = {
    saveProfile: async () => ({ id: 1 }),
    saveModels: async () => ({}),
  };
  await assert.rejects(() => saveChannelWithModels(deps, undefined, { name: "x", format: "openai" }, [], ""));
});
