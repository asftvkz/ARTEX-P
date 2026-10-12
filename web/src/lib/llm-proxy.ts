import type { LLMProfile, ProxyRecord, TaskProxyMode } from "./types";

export type LLMProxyDraft = { mode: TaskProxyMode | "legacy"; id: string; changed: boolean };
export type LLMProxyRequest = { proxy_mode?: TaskProxyMode; proxy_id?: string | null; proxy?: string };

export function llmProxyDraft(profile?: Pick<LLMProfile, "proxy_mode" | "proxy_id" | "proxy"> | null): LLMProxyDraft {
  if (profile?.proxy && !profile.proxy_mode) return { mode: "legacy", id: "", changed: false };
  return { mode: profile?.proxy_mode || "inherit", id: profile?.proxy_id ?? "", changed: !profile };
}

// 保存、测试和拉取模型共用此结果。旧 URL 只由服务端按渠道 ID 读取，掩码绝不回传。
export function llmProxyRequest(draft: LLMProxyDraft): LLMProxyRequest {
  if (!draft.changed) return {};
  if (draft.mode === "legacy") throw new Error("旧自定义代理只能保留，请选择代理中心或继承/直连");
  if (draft.mode === "fixed" && !/^[1-9]\d*$/.test(draft.id)) throw new Error("请选择有效的代理编号");
  return { proxy_mode: draft.mode, proxy_id: draft.mode === "fixed" ? draft.id : null };
}

export function llmProxyLabel(draft: LLMProxyDraft, items: ProxyRecord[] = []): string {
  if (draft.mode === "legacy") return "旧自定义 URL（高级配置，保留）";
  if (draft.mode === "inherit") return "继承全局默认";
  if (draft.mode === "direct") return "明确直连";
  const item = items.find((p) => String(p.id) === draft.id);
  return item ? proxyOptionLabel(item) : `代理 #${draft.id || "未选择"}（请核对可用性）`;
}

export function proxyOptionLabel(item: ProxyRecord): string {
  const ip = item.egress_ip?.trim() ? item.egress_ip : "未验证";
  return `#${item.id} ${item.name} · ${item.kind.toUpperCase()} · 出口 ${ip}`;
}

// 此适配器只保留调用方显式给出的选择，不默认补 proxy:""，防止清除旧 URL。
export function llmProxyWire(input?: LLMProxyRequest | string): LLMProxyRequest {
  if (typeof input === "string") return { proxy: input };
  if (!input) return {};
  if (input.proxy_mode !== undefined) {
    return { proxy_mode: input.proxy_mode, proxy_id: input.proxy_id ?? null };
  }
  return input.proxy !== undefined ? { proxy: input.proxy } : {};
}
