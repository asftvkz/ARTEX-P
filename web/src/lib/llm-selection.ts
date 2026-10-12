// 渠道 + 模型选择的纯逻辑：配置态三元组 ↔ 请求载荷、候选链去重、旧字段兼容转换。
// 契约见 docs/llm-models-contract.md「运行选择契约」与「任务、对话及 Agent 的外层 API」。
//
// 设计要点：配置态（members）与实际解析结果（active_member）是两回事——配置为
// channel-default 时 model_id 为 null，但实际运行时仍会解析出某个 member_id。
// 前端不得用展示名判断"是否切换"，一切以 member_id 为准。

import type { LLMActiveMember, LLMModel, LLMModelMode, LLMSelection } from "./types";

export type { LLMSelection };

export type CreationMemberInput = { profile_id?: string | number; model_mode?: LLMModelMode; model_id?: string | null };

// 模式取值来自运行时数据（DTO/草稿/旧数据），一律按"不可信输入"处理：只有缺省
// （未提供 / null / 空串）才按 channel-default 兼容解释——那是历史只传渠道的请求语义。
// 给了值但不是契约里的两个之一，就是坏数据，必须能被识别出来并拒绝保存，不能当成
// channel-default 静默放过（会把"固定某成员"悄悄降级成"跟随渠道默认"）。
export function parseMode(mode?: string | null): LLMModelMode | null {
  if (mode === undefined || mode === null || mode === "") return "channel-default";
  if (mode === "channel-default" || mode === "fixed-model") return mode;
  return null;
}

// normalizeMode 仅用于"已经校验过、或只做展示"的路径（例如把 DTO 渲染成文案）。
// 校验路径必须用 parseMode + dedupeSelectionsWithReport，否则未知值会被洗成渠道默认。
export function normalizeMode(mode?: string | null): LLMModelMode {
  return parseMode(mode) ?? "channel-default";
}

export function isChannelDefault(sel: Pick<LLMSelection, "model_mode">): boolean {
  return sel.model_mode !== "fixed-model";
}

// selectionKey 是"完全相同的选择"判定键：同渠道 + 同模式 + (固定时)同成员。
// 同渠道的不同 model_id 是不同选择，允许同时存在；完全相同的选择不得重复。
export function selectionKey(sel: LLMSelection): string {
  const model = sel.model_mode === "fixed-model" ? (sel.model_id ?? "") : "";
  return `${sel.profile_id}|${sel.model_mode}|${model}`;
}

export type DropReason = "missing-channel" | "missing-member" | "unknown-mode" | "duplicate";

export type DedupeResult = {
  selections: LLMSelection[];
  // 被丢弃的条目及其原因。提交前必须检查 dropped 是否为空——若把无效行静默丢掉，
  // 用户改过的选择会变成空链（语义从"指定候选"翻转成"继承"），属于静默改语义。
  dropped: { input: CreationMemberInput; reason: DropReason }[];
};

// dedupeSelections 收敛候选链：同一渠道的不同选择都保留，完全相同的选择只留一个。
// 无效条目（缺渠道、固定模式缺成员、未知模式）不会被静默丢弃——记进 dropped 由调用方
// 阻止保存。
export function dedupeSelectionsWithReport(selections: CreationMemberInput[]): DedupeResult {
  const out: LLMSelection[] = [];
  const dropped: DedupeResult["dropped"] = [];
  const seen = new Set<string>();
  for (const s of selections) {
    const mode = parseMode(s.model_mode);
    if (mode === null) {
      // 未知模式：不猜、不降级，交给调用方拒绝。
      dropped.push({ input: s, reason: "unknown-mode" });
      continue;
    }
    const sel: LLMSelection = {
      profile_id: String(s.profile_id ?? "").trim(),
      model_mode: mode,
      model_id: mode === "fixed-model" ? (s.model_id ?? null) : null,
    };
    if (!sel.profile_id) {
      dropped.push({ input: s, reason: "missing-channel" });
      continue;
    }
    if (sel.model_mode === "fixed-model" && !sel.model_id) {
      dropped.push({ input: s, reason: "missing-member" });
      continue;
    }
    const key = selectionKey(sel);
    if (seen.has(key)) {
      dropped.push({ input: s, reason: "duplicate" });
      continue;
    }
    seen.add(key);
    out.push(sel);
  }
  return { selections: out, dropped };
}

export function dedupeSelections(selections: CreationMemberInput[]): LLMSelection[] {
  return dedupeSelectionsWithReport(selections).selections;
}

// membersPresent：判断任务/对话/Agent DTO 里"新数组字段"是否真的存在。
// 存在即权威——显式空数组等于"恢复继承"，绝不能再回退旧字段，否则继承语义会被
// 历史链悄悄覆盖。只有字段缺省（老后端 / 老数据）才做旧字段兼容转换。
export function membersPresent<T>(members: T[] | null | undefined): members is T[] {
  return members !== null && members !== undefined;
}

export type TaskSelectionsSource = "members" | "legacy" | "none";

export type TaskSelectionsResult = DedupeResult & {
  source: TaskSelectionsSource;
  // members 字段是否存在（含空数组）。空数组合法表示"继承"，不是错误。
  membersField: boolean;
};

// selectionsFromTaskWithReport 读取任务 DTO 的候选链，并如实报告被丢弃的坏行。
//
// 为什么必须报告：DTO 来自后端/持久化，属运行时不可信输入。若这里静默把坏行丢掉，
// 链会缩短甚至变空——"空链"在契约里表示继承，于是坏数据会把"指定了候选"悄悄翻转成
// "继承"，而且用户点保存时校验已经是净化后的空链，坏数据就这么被固化。所以读取路径
// 也要把 dropped 交给调用方（编辑器据此禁用保存并说明原因），而不是替用户做决定。
//
// 规则：
//   1. members 字段存在（含空数组）→ 它就是唯一权威，空数组表示继承；
//   2. members 缺省 → 回退旧 llm_profile_ids / llm_profile_id，每项按渠道默认模式转换
//      （这是契约允许的兼容路径，不产生 dropped）。
export function selectionsFromTaskWithReport(task: {
  members?: CreationMemberInput[] | null;
  llm_profile_ids?: number[] | null;
  llm_profile_id?: number | null;
}): TaskSelectionsResult {
  if (membersPresent(task.members)) {
    const r = dedupeSelectionsWithReport(task.members);
    return { ...r, source: "members", membersField: true };
  }
  const legacy = task.llm_profile_ids && task.llm_profile_ids.length > 0 ? task.llm_profile_ids : [];
  if (legacy.length > 0) {
    const r = dedupeSelectionsWithReport(
      legacy.map((id) => ({ profile_id: String(id), model_mode: "channel-default" })),
    );
    return { ...r, source: "legacy", membersField: false };
  }
  if (task.llm_profile_id) {
    const r = dedupeSelectionsWithReport([{ profile_id: String(task.llm_profile_id), model_mode: "channel-default" }]);
    return { ...r, source: "legacy", membersField: false };
  }
  return { selections: [], dropped: [], source: "none", membersField: false };
}

export function selectionsFromTask(task: {
  members?: CreationMemberInput[] | null;
  llm_profile_ids?: number[] | null;
  llm_profile_id?: number | null;
}): LLMSelection[] {
  return selectionsFromTaskWithReport(task).selections;
}

// 候选链的 DTO 形态：members 存在（含空数组）时它权威；否则只能靠旧整数链判断。
export function taskMemberCount(task: {
  members?: CreationMemberInput[] | null;
  llm_profile_ids?: number[] | null;
  llm_profile_id?: number | null;
}): number {
  return selectionsFromTask(task).length;
}

// resolveActivePosition：把"后端给的当前候选下标"换算成配置态下标。
// 越界、非整数、缺省都返回 null——绝不回落第一项：链已耗尽（后端给 null）时，
// 第一项只是编辑器的草稿起点，把它显示成"当前生效"就是在撒谎。
export function resolveActivePosition(position: number | null | undefined, length: number): number | null {
  if (length === 0) return null;
  if (position === null || position === undefined) return null;
  if (!Number.isInteger(position) || position < 0 || position >= length) return null;
  return position;
}

// initialActivePosition：编辑器打开时"当前生效候选"下拉的初始选中项。
// 与 resolveActivePosition 的区别必须保留——这里是纯界面草稿，回落到第一项是为了
// 让下拉有可选项；而"实际生效"另由 DTO 的 active_member 表示，两者不得混用。
export function initialActivePosition(position: number | null | undefined, length: number): number | null {
  if (length === 0) return null;
  const resolved = resolveActivePosition(position, length);
  return resolved ?? 0;
}

// activeSelection 取当前配置态生效项；链耗尽/越界时返回 null（调用方须显示"无生效项"）。
export function activeSelection(members: LLMSelection[], position: number | null | undefined): LLMSelection | null {
  const idx = resolveActivePosition(position, members.length);
  if (idx === null) return null;
  return members[idx] ?? null;
}

// displayLabel 是"这个成员叫什么"的主文案：display_name 优先（它就是系统展示名称，
// 两个渠道的不同上游 ID 可以都叫 deepseek-chat，这是产品要求），缺省时回落上游 ID。
// 注意：主文案相同不代表同一成员，身份判定一律走 model_id / member_key，绝不按名字合并。
export function displayLabel(model: {
  display_name?: string | null;
  upstream_model_id?: string | null;
  model_id?: string | null;
}): string {
  const upstream = model.upstream_model_id || model.model_id || "";
  return model.display_name?.trim() || upstream || "?";
}

// modelOptionLabel：模型选择下拉里的一行。展示名为主，上游 ID 不同名时补在括号里，
// 这样"展示名相同、上游不同"的两个成员仍能被区分开（身份不靠名字）。
export function modelOptionLabel(model: {
  display_name?: string | null;
  upstream_model_id?: string | null;
  model_id?: string | null;
}): string {
  const primary = displayLabel(model);
  const upstream = model.upstream_model_id || model.model_id || "";
  if (upstream && upstream !== primary) return `${primary}（上游 ${upstream}）`;
  return primary;
}

// formatMember：给"实际生效"的一行做展示。展示名为主（产品要求），上游 ID 与成员键
// 作为身份补充——展示名相同不代表同一成员，绝不能只显示展示名。
export function formatMember(member: LLMActiveMember | null | undefined): string {
  if (!member) return "未解析出可用成员";
  const primary = displayLabel(member);
  const upstream = member.upstream_model_id || member.model_id || "";
  const label = upstream && upstream !== primary ? `${primary}（上游 ${upstream}）` : primary;
  return `${label} · ${member.member_key}`;
}

// memberKey：池成员的稳定身份键 "{profile_id}:{model_id}"。熔断/失败计数/冷却时间
// 均以它为身份存储，所以精确恢复必须用它，而不是渠道编号。
export function memberKey(profileId: string | number, modelId: string | number | null | undefined): string {
  return `${profileId}:${modelId ?? ""}`;
}

// PoolResetTarget 表达"恢复哪个池成员"的三种粒度。
// 精确恢复用 member_key；旧的按渠道恢复保持原样（{id:7} 恢复该渠道全部成员）。
export type PoolResetTarget =
  | { kind: "member"; member_key: string } // 精确恢复单个池成员
  | { kind: "channel"; profile_id: number } // 恢复该渠道全部成员状态
  | { kind: "all" }; // 恢复全部

// poolResetBody：把粒度翻译成后端请求体。
// 精确恢复发 {member_key:"7:42"}；其余沿用既有 {id} 语义（0 = 全部）。
export function poolResetBody(target: PoolResetTarget): { member_key: string } | { id: number } {
  if (target.kind === "member") return { member_key: target.member_key };
  if (target.kind === "channel") return { id: target.profile_id };
  return { id: 0 };
}

// shouldSendMembers：创建/更新任务时是否改用新的 members 形态。空数组是有效语义
// （恢复继承），所以"显式提供了 members"（哪怕为空）就发送；仅当未提供时才回退旧字段。
export function shouldSendMembers(members: LLMSelection[] | undefined): members is LLMSelection[] {
  return members !== undefined;
}

// selectionModeLabel：把配置态三元组说成人话。固定成员优先显示该成员的展示名
// （产品要求：展示名就是给人看的名字），拿不到名字时才退回内部成员 ID。
// 身份仍然是 model_id——名字只负责"好认"，不参与路由或等价判断。
export function selectionModeLabel(
  sel: Pick<LLMSelection, "model_mode" | "model_id">,
  member?: { display_name?: string | null; upstream_model_id?: string | null } | null,
): string {
  if (sel.model_mode !== "fixed-model") return "跟随渠道默认模型";
  const named = member
    ? displayLabel({ display_name: member.display_name, upstream_model_id: member.upstream_model_id })
    : "";
  if (named && named !== "?") return `固定 ${named}`;
  return `固定成员 #${sel.model_id ?? "?"}`;
}

// chainValidationError：提交前的候选链校验。返回空串表示可以提交。
// 固定模式缺成员、缺渠道这类"半成品"行必须挡住保存——放着不管就会被 dedupe 丢掉，
// 用户的链路会静默变成继承（语义完全翻转），这比报错难查得多。
export function chainValidationError(members: CreationMemberInput[]): string {
  const { dropped } = dedupeSelectionsWithReport(members);
  if (dropped.length === 0) return "";
  const missingMember = dropped.filter((d) => d.reason === "missing-member").length;
  const missingChannel = dropped.filter((d) => d.reason === "missing-channel").length;
  const unknownMode = dropped.filter((d) => d.reason === "unknown-mode").length;
  const duplicate = dropped.filter((d) => d.reason === "duplicate").length;
  const parts: string[] = [];
  if (missingMember > 0) parts.push(`${missingMember} 行固定模式还没选具体模型`);
  if (missingChannel > 0) parts.push(`${missingChannel} 行缺少所属渠道`);
  if (unknownMode > 0) parts.push(`${unknownMode} 行的模型模式不是「跟随渠道默认」或「固定成员」`);
  if (duplicate > 0) parts.push(`${duplicate} 行与其它候选完全相同`);
  return `${parts.join("；")}。请修正后再保存（完全相同的候选不能重复）。`;
}

// validationErrorFromDropped：把读取路径（DTO）发现的坏行也变成同样的提示。
// 编辑器打开时若发现坏行，必须挡住保存并说明原因，否则"净化后的空链"会被当成
// 合法的继承语义写回去，坏数据就被固化下来了。
export function validationErrorFromDropped(dropped: { reason: DropReason }[]): string {
  if (dropped.length === 0) return "";
  return `读取到的 LLM 候选配置里有 ${dropped.length} 行无效（模式或成员不合法），已阻止保存。请重新选择后再保存。`;
}

// ─────────────────────────────────────────────────────────────────────────────
// 渠道 → 模型成员列表的按需缓存
// ─────────────────────────────────────────────────────────────────────────────

// ModelCache 记住每个渠道的成员列表与在途请求。pending 存的是 promise 而不是布尔：
// 同渠道并发打开下拉时必须等待同一个请求，不能在请求完成前返回空列表——那会让界面
// 把"还没拉到"误判成"这个渠道没有模型"，用户会看到空目录而以为渠道没配模型。
export type ModelCache = {
  data: Record<string, LLMModel[]>;
  pending: Record<string, Promise<LLMModel[]>>;
};

export function emptyModelCache(): ModelCache {
  return { data: {}, pending: {} };
}

export function cachedModels(cache: ModelCache, profileID: string): LLMModel[] | undefined {
  return cache.data[profileID];
}

export function invalidateModels(cache: ModelCache, profileID?: string): void {
  if (profileID === undefined) {
    cache.data = {};
    return;
  }
  delete cache.data[profileID];
}

// needModels 返回该渠道的成员列表；未缓存则拉取一次并缓存，并发调用共享同一请求。
// force=true 用于已知成员集合可能变化（比如刚保存过渠道）时强制刷新。
export async function needModels(
  cache: ModelCache,
  profileID: string,
  fetchModels: (profileId: string) => Promise<LLMModel[]>,
  force = false,
): Promise<LLMModel[]> {
  if (!force && cache.data[profileID]) return cache.data[profileID];
  const inflight = cache.pending[profileID];
  if (inflight && !force) return inflight;

  const request = fetchModels(profileID).then(
    (models) => {
      cache.data[profileID] = models;
      // 只清理自己这一笔——force 期间可能有更新的请求接管了这个槽位。
      if (cache.pending[profileID] === request) delete cache.pending[profileID];
      return models;
    },
    (error) => {
      if (cache.pending[profileID] === request) delete cache.pending[profileID];
      throw error;
    },
  );
  cache.pending[profileID] = request;
  return request;
}
