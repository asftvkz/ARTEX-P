// LLM 渠道模型成员——纯逻辑部分，供 UI 层（system/llm 页面）与测试共用。
// 字段/端点形状见 docs/llm-models-contract.md。

import type { api } from "./api";
import type { LLMModel } from "./types";

// ModelRow 是模型成员在表单里的草稿形态。key 用 upstream_model_id：渠道内唯一
// （契约保证），新增成员还没有服务端 id 时也能稳定标识一行、匹配勾选去重。
export type ModelRow = {
  key: string;
  id?: string; // 已保存成员的服务端 id；新增(未保存)成员省略，由服务端分配
  upstreamId: string;
  displayName: string;
  enabled: boolean;
};

export function rowsFromResponse(models: LLMModel[]): { rows: ModelRow[]; defaultKey: string } {
  const rows = models.map((m) => ({
    key: m.upstream_model_id,
    id: m.id,
    upstreamId: m.upstream_model_id,
    displayName: m.display_name,
    enabled: m.enabled,
  }));
  const def = models.find((m) => m.is_default);
  return { rows, defaultKey: def?.upstream_model_id ?? rows[0]?.key ?? "" };
}

export type ModelPayloadItem = {
  id?: string;
  upstream_model_id: string;
  display_name: string;
  enabled: boolean;
  is_default: boolean;
};

// rowsToPayload 把草稿还原成 PUT /llm/profiles/{id}/models 要求的形状。
export function rowsToPayload(rows: ModelRow[], defaultKey: string): ModelPayloadItem[] {
  return rows.map((r) => ({
    ...(r.id ? { id: r.id } : {}),
    upstream_model_id: r.upstreamId,
    display_name: r.displayName.trim() || r.upstreamId,
    enabled: r.enabled,
    is_default: r.key === defaultKey,
  }));
}

// resolveApiKeyToSend：编辑已有渠道时，只有明确点击「更换 API Key」才允许把输入框的值
// 用于保存 / 测试连接 / 拉取模型；没有这层保护，残留输入或浏览器密码管理器的自动填充
// 可能悄悄顶掉已保存的有效 Key。新建渠道没有「已保存的 Key」可言，总是直接发送当前输入。
export function resolveApiKeyToSend(isNew: boolean, replaceApiKey: boolean, apiKey: string): string {
  return isNew || replaceApiKey ? apiKey : "";
}

export type SaveProfilePayload = Parameters<typeof api.saveLLMProfile>[0];
export type SaveModelsPayload = Parameters<typeof api.saveLLMModels>[1];

export interface SaveChannelDeps {
  saveProfile: (payload: SaveProfilePayload) => Promise<{ id: number }>;
  saveModels: (profileId: number, models: SaveModelsPayload) => Promise<unknown>;
}

export interface SaveChannelOutcome {
  ok: boolean;
  profileId: number;
  modelsError?: string;
}

// saveChannelWithModels 编排"保存渠道字段 + 保存模型列表"这两步：
//
// - existingProfileId 非空(编辑已有渠道，或新建后重试)时，saveProfile 带上 id(更新)；
//   否则不带 id(创建)。整个调用序列里 saveProfile 只会有恰好一次不带 id 的调用——
//   调用方必须在第一次调用之后，把返回的 profileId 当作 existingProfileId 用于任何
//   重试，绝不能重复触发"不带 id"的创建。
// - legacy model 字段固定送草稿默认行的上游 ID：新建时用它在同一事务里种出首个模型
//   成员；编辑已有渠道时服务端会忽略这个值、保留当前 default_model_id 不变——真正的
//   默认切换只在 saveModels 这一步的事务内发生，这里送什么都不会提前改掉已持久化的
//   默认模型（即便 saveModels 随后失败）。
// - saveModels 失败时返回 ok:false 但仍带上 profileId：调用方必须保留它用于下一次
//   重试（走更新分支），不能再次以不带 id 的方式调用 saveProfile，否则会重复新建渠道。
export async function saveChannelWithModels(
  deps: SaveChannelDeps,
  existingProfileId: number | undefined,
  profileFields: Omit<SaveProfilePayload, "id" | "model">,
  modelRows: ModelRow[],
  defaultKey: string,
): Promise<SaveChannelOutcome> {
  const defaultRow = modelRows.find((r) => r.key === defaultKey) ?? modelRows[0];
  if (!defaultRow) throw new Error("请至少添加一个模型");

  const { id } = await deps.saveProfile({
    ...(existingProfileId ? { id: existingProfileId } : {}),
    ...profileFields,
    model: defaultRow.upstreamId,
  } as SaveProfilePayload);

  try {
    await deps.saveModels(id, rowsToPayload(modelRows, defaultKey));
    return { ok: true, profileId: id };
  } catch (e) {
    return { ok: false, profileId: id, modelsError: (e as Error).message };
  }
}
