import {
  activeSelection,
  cachedModels,
  chainValidationError,
  dedupeSelections,
  dedupeSelectionsWithReport,
  displayLabel,
  emptyModelCache,
  formatMember,
  initialActivePosition,
  invalidateModels,
  isChannelDefault,
  memberKey,
  membersPresent,
  modelOptionLabel,
  needModels,
  normalizeMode,
  parseMode,
  poolResetBody,
  resolveActivePosition,
  selectionKey,
  selectionModeLabel,
  selectionsFromTask,
  selectionsFromTaskWithReport,
  shouldSendMembers,
  validationErrorFromDropped,
} from "./llm-selection.ts";
import assert from "node:assert/strict";
import test from "node:test";

// 模式是运行时数据（DTO/草稿），属不可信输入。缺省按渠道默认兼容；给了别的值就是坏
// 数据，必须能被识别并拒绝，绝不能降级成 channel-default 蒙混过关。
test("parseMode：缺省兼容 channel-default，未知模式返回 null 而不是降级", () => {
  assert.equal(parseMode("fixed-model"), "fixed-model");
  assert.equal(parseMode("channel-default"), "channel-default");
  assert.equal(parseMode(undefined), "channel-default");
  assert.equal(parseMode(null), "channel-default");
  assert.equal(parseMode(""), "channel-default");
  assert.equal(parseMode("bogus"), null);
  assert.equal(parseMode("FIXED-MODEL"), null);
  assert.equal(parseMode("fixed_model"), null);
});

test("normalizeMode 只用于展示/已校验路径，非固定值一律显示为渠道默认", () => {
  assert.equal(normalizeMode("fixed-model"), "fixed-model");
  assert.equal(normalizeMode("channel-default"), "channel-default");
  assert.equal(normalizeMode(undefined), "channel-default");
  assert.equal(normalizeMode(null), "channel-default");
  assert.equal(normalizeMode("bogus"), "channel-default");
  assert.equal(isChannelDefault({ model_mode: "channel-default" }), true);
  assert.equal(isChannelDefault({ model_mode: "fixed-model" }), false);
});

test("selectionKey：同渠道不同模式/不同成员是不同选择，固定模式的成员参与身份", () => {
  assert.notEqual(
    selectionKey({ profile_id: "7", model_mode: "channel-default", model_id: null }),
    selectionKey({ profile_id: "7", model_mode: "fixed-model", model_id: "42" }),
  );
  assert.notEqual(
    selectionKey({ profile_id: "7", model_mode: "fixed-model", model_id: "42" }),
    selectionKey({ profile_id: "7", model_mode: "fixed-model", model_id: "43" }),
  );
  // channel-default 下的残留 model_id 不参与身份。
  assert.equal(
    selectionKey({ profile_id: "7", model_mode: "channel-default", model_id: null }),
    selectionKey({ profile_id: "7", model_mode: "channel-default", model_id: "42" }),
  );
});

test("dedupeSelections：同渠道允许多个不同选择，完全相同的选择去重", () => {
  const out = dedupeSelections([
    { profile_id: "7", model_mode: "channel-default" },
    { profile_id: "7", model_mode: "channel-default" },
    { profile_id: "7", model_mode: "fixed-model", model_id: "42" },
    { profile_id: "7", model_mode: "fixed-model", model_id: "42" },
    { profile_id: "7", model_mode: "fixed-model", model_id: "43" },
    { profile_id: "8", model_mode: "channel-default" },
  ]);
  assert.equal(out.length, 4);
  assert.deepEqual(
    out.map((s) => `${s.profile_id}:${s.model_mode}:${s.model_id ?? "-"}`),
    ["7:channel-default:-", "7:fixed-model:42", "7:fixed-model:43", "8:channel-default:-"],
  );
});

test("dedupeSelections：channel-default 丢掉残留 model_id", () => {
  const out = dedupeSelections([{ profile_id: "7", model_mode: "channel-default", model_id: "42" }]);
  assert.deepEqual(out, [{ profile_id: "7", model_mode: "channel-default", model_id: null }]);
});

// 静默丢行会把"用户指定了候选"翻转成"继承"，必须能从报告里看出来。
test("dedupeSelectionsWithReport：无效行记进 dropped，不被静默吞掉", () => {
  const r = dedupeSelectionsWithReport([
    { profile_id: "7", model_mode: "fixed-model", model_id: null }, // 固定模式缺成员
    { profile_id: "", model_mode: "channel-default" }, // 缺渠道
    { profile_id: "7", model_mode: "channel-default" },
    { profile_id: "7", model_mode: "channel-default" }, // 完全重复
  ]);
  assert.deepEqual(r.selections, [{ profile_id: "7", model_mode: "channel-default", model_id: null }]);
  assert.deepEqual(
    r.dropped.map((d) => d.reason),
    ["missing-member", "missing-channel", "duplicate"],
  );
  assert.equal(dedupeSelectionsWithReport([]).dropped.length, 0);
});

// 未知模式若被 normalizeMode 洗成 channel-default，就会静默通过校验并保存——等于把
// 坏数据当成"跟随渠道默认"，用户看到的选择和实际存下的不是一回事。
test("dedupeSelectionsWithReport：未知模式记 unknown-mode 并拒绝，绝不降级成渠道默认", () => {
  const r = dedupeSelectionsWithReport([
    { profile_id: "7", model_mode: "bogus" },
    { profile_id: "7", model_mode: "channel-default" },
  ]);
  assert.deepEqual(r.selections, [{ profile_id: "7", model_mode: "channel-default", model_id: null }]);
  assert.deepEqual(
    r.dropped.map((d) => d.reason),
    ["unknown-mode"],
  );
  // 缺省（未提供）仍然是合法兼容路径，不进 dropped。
  const ok = dedupeSelectionsWithReport([{ profile_id: "7" }]);
  assert.deepEqual(ok.selections, [{ profile_id: "7", model_mode: "channel-default", model_id: null }]);
  assert.equal(ok.dropped.length, 0);
  // 全坏行 → 空结果，调用方必须能看到 dropped 非空（否则会被当成"继承"存回去）。
  const allBad = dedupeSelectionsWithReport([{ profile_id: "7", model_mode: "weird" }]);
  assert.deepEqual(allBad.selections, []);
  assert.equal(allBad.dropped.length, 1);
});

test("chainValidationError：未知模式必须阻止保存", () => {
  const msg = chainValidationError([{ profile_id: "7", model_mode: "bogus" }]);
  assert.notEqual(msg, "");
  assert.match(msg, /模式/);
  // 缺省 mode 仍放行。
  assert.equal(chainValidationError([{ profile_id: "7" }]), "");
});

test("chainValidationError：固定模式缺成员必须阻止保存，完整链放行", () => {
  assert.equal(chainValidationError([{ profile_id: "7", model_mode: "fixed-model", model_id: null }]).length > 0, true);
  assert.match(chainValidationError([{ profile_id: "", model_mode: "channel-default" }]), /缺少所属渠道/);
  assert.match(
    chainValidationError([
      { profile_id: "7", model_mode: "channel-default" },
      { profile_id: "7", model_mode: "channel-default" },
    ]),
    /完全相同/,
  );
  assert.equal(chainValidationError([{ profile_id: "7", model_mode: "fixed-model", model_id: "42" }]), "");
  // 空链是合法语义（继承），不是错误。
  assert.equal(chainValidationError([]), "");
});

// 读取路径同样不能吞掉坏 DTO：净化后的链可能正好变空，而空链表示"继承"。若不报告
// dropped，坏数据会被当成正常继承写回，用户点保存就算"成功"。
test("selectionsFromTaskWithReport：报告坏 DTO 行，且空 members 来源可区分", () => {
  const bad = selectionsFromTaskWithReport({
    members: [{ profile_id: "7", model_mode: "bogus" }],
    llm_profile_ids: [8, 9],
  });
  assert.deepEqual(bad.selections, []);
  assert.equal(bad.dropped.length, 1);
  assert.equal(bad.dropped[0].reason, "unknown-mode");
  assert.equal(bad.source, "members");
  assert.equal(bad.membersField, true, "members 字段存在即权威，绝不能回退旧链掩盖坏行");

  const clean = selectionsFromTaskWithReport({
    members: [{ profile_id: "7", model_mode: "fixed-model", model_id: "42" }],
  });
  assert.equal(clean.dropped.length, 0);
  assert.equal(clean.source, "members");

  // 显式空数组：合法继承，不是坏行，也不回退旧链。
  const inherit = selectionsFromTaskWithReport({ members: [], llm_profile_ids: [8, 9] });
  assert.deepEqual(inherit.selections, []);
  assert.equal(inherit.dropped.length, 0);
  assert.equal(inherit.membersField, true);

  // 缺省 members 才走旧字段兼容，不产生 dropped。
  const legacy = selectionsFromTaskWithReport({ llm_profile_ids: [8, 9] });
  assert.equal(legacy.source, "legacy");
  assert.equal(legacy.dropped.length, 0);
  assert.equal(legacy.selections.length, 2);

  const none = selectionsFromTaskWithReport({});
  assert.equal(none.source, "none");
  assert.deepEqual(none.selections, []);
});

test("validationErrorFromDropped：坏行给出可操作的阻止保存提示", () => {
  assert.equal(validationErrorFromDropped([]), "");
  const msg = validationErrorFromDropped([{ reason: "unknown-mode" }, { reason: "missing-member" }]);
  assert.match(msg, /2 行无效/);
});

// 旧实现只在 members.length>0 时才采信新字段，显式空数组会被历史链顶回去——
// 那正是"恢复继承"存不进界面的原因。存在即权威。
test("selectionsFromTask：members 字段存在即权威，显式空数组表示继承而非回退旧链", () => {
  assert.deepEqual(selectionsFromTask({ members: [], llm_profile_ids: [8, 9], llm_profile_id: 9 }), []);
  assert.equal(membersPresent([]), true);
  assert.equal(membersPresent(null), false);
  assert.equal(membersPresent(undefined), false);

  // 缺省（老后端/老数据）才走旧字段兼容。
  assert.deepEqual(selectionsFromTask({ llm_profile_ids: [8, 9], active_llm_profile_id: 9 }), [
    { profile_id: "8", model_mode: "channel-default", model_id: null },
    { profile_id: "9", model_mode: "channel-default", model_id: null },
  ]);
  // 新字段优先于同时存在的旧字段。
  assert.deepEqual(
    selectionsFromTask({
      members: [{ profile_id: "7", model_mode: "fixed-model", model_id: "42" }],
      llm_profile_ids: [8],
    }),
    [{ profile_id: "7", model_mode: "fixed-model", model_id: "42" }],
  );
});

test("selectionsFromTask：旧单渠道字段与空任务都能读出合理结果", () => {
  assert.deepEqual(selectionsFromTask({ llm_profile_id: 7 }), [
    { profile_id: "7", model_mode: "channel-default", model_id: null },
  ]);
  assert.deepEqual(selectionsFromTask({}), []);
});

test("resolveActivePosition：链耗尽(null)不冒充第一项，越界同样返回 null", () => {
  assert.equal(resolveActivePosition(0, 3), 0);
  assert.equal(resolveActivePosition(2, 3), 2);
  assert.equal(resolveActivePosition(null, 3), null);
  assert.equal(resolveActivePosition(undefined, 3), null);
  assert.equal(resolveActivePosition(9, 3), null);
  assert.equal(resolveActivePosition(-1, 3), null);
  assert.equal(resolveActivePosition(1.5, 3), null);
  assert.equal(resolveActivePosition(0, 0), null);
});

test("activeSelection：耗尽态没有生效项，不能被第一项顶替", () => {
  const members = [
    { profile_id: "7", model_mode: "channel-default", model_id: null },
    { profile_id: "8", model_mode: "fixed-model", model_id: "43" },
  ];
  assert.equal(activeSelection(members, null), null);
  assert.equal(activeSelection(members, 9), null);
  assert.deepEqual(activeSelection(members, 1), members[1]);
  assert.equal(activeSelection([], 0), null);
});

test("initialActivePosition：仅编辑草稿回落到第一项，与生效判定分开", () => {
  const members = [{ profile_id: "7", model_mode: "channel-default", model_id: null }];
  assert.equal(initialActivePosition(null, 1), 0);
  assert.equal(initialActivePosition(5, 1), 0);
  assert.equal(initialActivePosition(1, 3), 1);
  assert.equal(initialActivePosition(0, 0), null);
  // 同输入下两者结论不同，正是"草稿位置 ≠ 实际生效"的证据。
  assert.equal(resolveActivePosition(null, 1), null);
  assert.equal(initialActivePosition(null, 1), 0);
  assert.notEqual(activeSelection(members, null), members[0]);
});

// 产品要求：display_name 就是给人看的系统展示名称，主文案以它为主；上游 ID 与成员键
// 只作身份补充（同名不代表同一成员，绝不按名字合并或据此判断"是否切换"）。
test("formatMember：展示名为主，上游 ID 与成员键作为身份补充", () => {
  assert.equal(
    formatMember({
      profile_id: "7",
      model_mode: "fixed-model",
      model_id: "42",
      upstream_model_id: "deepseek-v3",
      display_name: "DeepSeek Chat",
      member_key: "7:42",
    }),
    "DeepSeek Chat（上游 deepseek-v3） · 7:42",
  );
  // 展示名与上游 ID 相同时不重复罗列。
  assert.equal(
    formatMember({
      profile_id: "7",
      model_mode: "channel-default",
      model_id: "42",
      upstream_model_id: "deepseek-v3",
      display_name: "deepseek-v3",
      member_key: "7:42",
    }),
    "deepseek-v3 · 7:42",
  );
  // 没有展示名时回落上游 ID，不显示空。
  assert.equal(
    formatMember({
      profile_id: "7",
      model_mode: "fixed-model",
      model_id: "42",
      upstream_model_id: "deepseek-v3",
      display_name: "",
      member_key: "7:42",
    }),
    "deepseek-v3 · 7:42",
  );
  assert.equal(formatMember(null), "未解析出可用成员");
});

// 两个渠道的不同上游 ID 可以都叫 deepseek-chat：主文案会相同，这是允许的。
// 身份因此必须靠 model_id / member_key 区分，绝不能用名字判等。
test("displayLabel/modelOptionLabel：同名不同上游仍可区分", () => {
  const a = { display_name: "deepseek-chat", upstream_model_id: "deepseek-v3", model_id: "42" };
  const b = { display_name: "deepseek-chat", upstream_model_id: "deepseek-r1", model_id: "77" };
  assert.equal(displayLabel(a), "deepseek-chat");
  assert.equal(displayLabel(b), "deepseek-chat");
  assert.equal(displayLabel(a), displayLabel(b), "展示名允许相同——这正是映射需求");
  // 但下拉项必须能区分出它们不是同一个成员。
  assert.equal(modelOptionLabel(a), "deepseek-chat（上游 deepseek-v3）");
  assert.equal(modelOptionLabel(b), "deepseek-chat（上游 deepseek-r1）");
  assert.notEqual(modelOptionLabel(a), modelOptionLabel(b));
  // 名称缺省时回落上游 ID。
  assert.equal(displayLabel({ display_name: "", upstream_model_id: "x" }), "x");
  assert.equal(modelOptionLabel({ display_name: "x", upstream_model_id: "x" }), "x");
});

test("shouldSendMembers：只有显式提供了 members（含空数组）才改走新形态", () => {
  assert.equal(shouldSendMembers([]), true);
  assert.equal(shouldSendMembers([{ profile_id: "7", model_mode: "channel-default", model_id: null }]), true);
  assert.equal(shouldSendMembers(undefined), false);
});

test("memberKey 与池恢复请求体：精确恢复按成员键，旧渠道/全部语义不变", () => {
  assert.equal(memberKey("7", "42"), "7:42");
  assert.equal(memberKey(7, 42), "7:42");
  assert.deepEqual(poolResetBody({ kind: "member", member_key: "7:42" }), { member_key: "7:42" });
  assert.deepEqual(poolResetBody({ kind: "channel", profile_id: 7 }), { id: 7 });
  assert.deepEqual(poolResetBody({ kind: "all" }), { id: 0 });
});

test("selectionModeLabel：固定成员优先显示展示名，拿不到名字才退回内部 ID", () => {
  assert.equal(selectionModeLabel({ model_mode: "channel-default", model_id: null }), "跟随渠道默认模型");
  // 有成员信息 → 用展示名（产品要求的映射名），不再只丢一个内部编号给用户看。
  assert.equal(
    selectionModeLabel(
      { model_mode: "fixed-model", model_id: "42" },
      { display_name: "DeepSeek Chat", upstream_model_id: "deepseek-v3" },
    ),
    "固定 DeepSeek Chat",
  );
  // 没名字 → 退回 ID；身份始终是 model_id，不因名字改变。
  assert.equal(selectionModeLabel({ model_mode: "fixed-model", model_id: "42" }), "固定成员 #42");
});

// 并发打开下拉时若在请求完成前返回 []，界面会把"在途"误判成"该渠道没有模型"。
test("needModels：并发调用共享同一在途请求，不返回空数组", async () => {
  const cache = emptyModelCache();
  let resolveFetch;
  let calls = 0;
  const fetchModels = () => {
    calls += 1;
    return new Promise((r) => {
      resolveFetch = r;
    });
  };
  const first = needModels(cache, "7", fetchModels);
  const second = needModels(cache, "7", fetchModels);
  assert.equal(calls, 1, "同渠道并发只应发一次请求");
  assert.equal(cachedModels(cache, "7"), undefined);
  resolveFetch([
    {
      id: "42",
      profile_id: "7",
      upstream_model_id: "deepseek-v3",
      display_name: "ds",
      enabled: true,
      is_default: true,
    },
  ]);
  const [a, b] = await Promise.all([first, second]);
  assert.equal(a.length, 1);
  assert.deepEqual(b, a);
  assert.equal(calls, 1);
  // 缓存命中不再请求。
  await needModels(cache, "7", fetchModels);
  assert.equal(calls, 1);
  // 失效后重新拉取。
  invalidateModels(cache, "7");
  assert.equal(cachedModels(cache, "7"), undefined);
  const third = needModels(cache, "7", fetchModels);
  assert.equal(calls, 2);
  resolveFetch([]);
  assert.deepEqual(await third, []);
  // 失败不缓存，且能重试。
  invalidateModels(cache);
  const failing = () => Promise.reject(new Error("boom"));
  await assert.rejects(() => needModels(cache, "8", failing));
  await assert.rejects(() => needModels(cache, "8", failing));
});
