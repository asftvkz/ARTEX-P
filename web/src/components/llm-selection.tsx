"use client";

// 渠道 + 模型选择的共用 UI：一行一个成员，每行左侧固定显示渠道、右侧用紧凑下拉选
// 「跟随渠道默认模型」或该渠道下的某个固定模型成员。任务候选链、对话覆盖、Agent 绑定
// 三处共用同一套语义（见 docs/llm-models-contract.md「运行选择契约」）。
//
// 语义必须清楚：channel-default 随渠道当前默认成员变化；fixed-model 固定绑定某个
// 成员，渠道切换默认不影响它。主文案以 display_name 为主（它是系统展示名称，两个渠道
// 的不同上游 ID 可以都叫 deepseek-chat）；上游 ID 与成员键只作身份补充，绝不按名字
// 合并成员或判断"是否切换"。

import * as React from "react";

import { ArrowDownIcon, ArrowUpIcon, XIcon } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import {
  activeSelection,
  dedupeSelections,
  formatMember,
  initialActivePosition,
  modelOptionLabel,
  selectionKey,
  selectionModeLabel,
} from "@/lib/llm-selection";
import type { LLMActiveMember, LLMModel, LLMProfile, LLMSelection } from "@/lib/types";

export { normalizeMode, selectionModeLabel } from "@/lib/llm-selection";

const CHANNEL_DEFAULT = "__channel_default__";

export function pickerValue(sel: Pick<LLMSelection, "model_mode" | "model_id">): string {
  return sel.model_mode === "fixed-model" && sel.model_id ? sel.model_id : CHANNEL_DEFAULT;
}

// modelOptionLabel 由 lib 统一实现（展示名为主、上游 ID 补充），此处仅做再导出，
// 让下拉与其它位置永远用同一套命名规则。
export { modelOptionLabel };

// ModelSelect 是单行的紧凑选择器；任务候选链、对话、Agent 绑定都用它。
export function ModelSelect({
  value,
  models,
  modelsLoading,
  onNeedModels,
  onChange,
  disabled,
  size = "sm",
  className,
}: {
  value: LLMSelection;
  models: LLMModel[] | undefined;
  modelsLoading: boolean;
  onNeedModels: () => void;
  onChange: (next: LLMSelection) => void;
  disabled?: boolean;
  size?: "sm" | "default";
  className?: string;
}) {
  const current = pickerValue(value);
  const list = models ?? [];
  // 固定模式选中的成员已不在列表里（渠道改名/被删）时，保留一个占位项，避免
  // Select 显示成空白而让人以为已跟随渠道默认。
  const missing = value.model_mode === "fixed-model" && value.model_id && !list.some((m) => m.id === value.model_id);
  const selected = list.find((m) => m.id === value.model_id);
  // 收起态展示的文案：跟随默认 → 固定成员时优先展示名，成员尚未加载到就先用内部 ID。
  const pickerText =
    value.model_mode === "fixed-model" && value.model_id
      ? selected
        ? modelOptionLabel(selected)
        : `成员 #${value.model_id}`
      : undefined;

  return (
    <Select
      value={current}
      // biome-ignore lint/nursery/useNullishCoalescing: disabled 是可选 boolean，父级传 false 时仍要看 modelsLoading，?? 会吞掉它
      disabled={disabled || modelsLoading}
      onValueChange={(next) => {
        if (next === CHANNEL_DEFAULT) onChange({ ...value, model_mode: "channel-default", model_id: null });
        else onChange({ ...value, model_mode: "fixed-model", model_id: next });
      }}
    >
      <SelectTrigger
        size={size}
        className={className}
        aria-label="选择模型成员"
        // 打开时就近拉一次该渠道的成员列表（缓存由调用方持有，重复打开不重复请求）。
        onClick={onNeedModels}
      >
        {/* 收起的触发器直接显示选中成员的展示名（产品要求的主文案），而不是留空或只显示
            "跟随渠道默认"——固定成员时用户必须一眼看到自己选的是哪个模型。 */}
        <SelectValue placeholder={modelsLoading ? "加载模型…" : "跟随渠道默认"}>{pickerText}</SelectValue>
      </SelectTrigger>
      <SelectContent>
        <SelectGroup>
          <SelectItem value={CHANNEL_DEFAULT}>跟随渠道默认模型</SelectItem>
          {list.map((m) => (
            <SelectItem key={m.id} value={m.id} disabled={!m.enabled}>
              {modelOptionLabel(m)}
            </SelectItem>
          ))}
          {missing && value.model_id ? (
            <SelectItem value={value.model_id}>成员 #{value.model_id}（不在当前列表中）</SelectItem>
          ) : null}
        </SelectGroup>
      </SelectContent>
    </Select>
  );
}

// ChainMemberRow 是候选链里的一行：序号 + 渠道名 + 模型选择 + 角色徽标 + 排序/移除。
export function ChainMemberRow({
  index,
  total,
  profileName,
  value,
  models,
  modelsLoading,
  onNeedModels,
  onChange,
  onMove,
  onRemove,
  role,
  disabled,
  extra,
}: {
  index: number;
  total: number;
  profileName: string;
  value: LLMSelection;
  models: LLMModel[] | undefined;
  modelsLoading: boolean;
  onNeedModels: () => void;
  onChange: (next: LLMSelection) => void;
  onMove?: (offset: -1 | 1) => void;
  onRemove: () => void;
  role: React.ReactNode;
  disabled?: boolean;
  extra?: React.ReactNode;
}) {
  return (
    <div className="flex flex-col gap-1.5 px-2.5 py-2">
      <div className="flex min-w-0 flex-wrap items-center gap-2">
        <span className="w-5 shrink-0 text-center font-mono text-muted-foreground text-xs tabular-nums">
          {index + 1}
        </span>
        <span className="min-w-0 flex-1 truncate text-sm">{profileName}</span>
        <ModelSelect
          value={value}
          models={models}
          modelsLoading={modelsLoading}
          onNeedModels={onNeedModels}
          onChange={onChange}
          disabled={disabled}
          className="w-56"
        />
        {role}
        <div className="flex shrink-0 items-center gap-1">
          {onMove && (
            <>
              <Button
                type="button"
                variant="ghost"
                size="icon-sm"
                aria-label="上移候选"
                // biome-ignore lint/nursery/useNullishCoalescing: disabled 是可选 boolean，父级传 false 时仍要看 index===0，?? 会吞掉它
                disabled={disabled || index === 0}
                onClick={() => onMove(-1)}
              >
                <ArrowUpIcon />
              </Button>
              <Button
                type="button"
                variant="ghost"
                size="icon-sm"
                aria-label="下移候选"
                // biome-ignore lint/nursery/useNullishCoalescing: disabled 是可选 boolean，父级传 false 时仍要看 index===total-1，?? 会吞掉它
                disabled={disabled || index === total - 1}
                onClick={() => onMove(1)}
              >
                <ArrowDownIcon />
              </Button>
            </>
          )}
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            aria-label="移除候选"
            disabled={disabled}
            onClick={onRemove}
          >
            <XIcon />
          </Button>
        </div>
      </div>
      {extra}
    </div>
  );
}

// ChannelModelChain 是任务候选链的编辑器：有序候选 + 当前生效项 + 增删排序。
// 每行左侧渠道、右侧模型选择，一行一个候选。
//
// 两个"当前"必须分开表达：
//   - 编辑草稿位置（activePosition，后端 DTO 值）：保存后从第几项开始尝试；
//   - 实际生效成员（activeMember）：后端解析出的真实身份。
// 链已耗尽时后端给 active_position=null，此时草稿位置回落到第一项只是为了下拉可用，
// 绝不能把它说成"当前生效"。
export function ChannelModelChain({
  profiles,
  members,
  activePosition,
  activeMember,
  onMembersChange,
  onActivePositionChange,
  modelsByProfile,
  onNeedModels,
  loadingProfiles,
  disabled,
  inputId,
}: {
  profiles: LLMProfile[];
  members: LLMSelection[];
  activePosition: number | null;
  /** 后端解析出的实际生效成员；null = 当前无生效成员，undefined = 尚未保存（纯新建草稿）。 */
  activeMember?: LLMActiveMember | null;
  onMembersChange: (next: LLMSelection[]) => void;
  onActivePositionChange?: (position: number | null) => void;
  modelsByProfile: Record<string, LLMModel[] | undefined>;
  onNeedModels: (profileId: string) => void;
  loadingProfiles?: Record<string, boolean>;
  disabled?: boolean;
  inputId?: string;
}) {
  const profilesByID = React.useMemo(() => new Map(profiles.map((p) => [p.id, p])), [profiles]);
  // 草稿位置：仅用于"保存后从哪一项开始"，与下面的实际生效成员是两回事。
  const draftIndex = initialActivePosition(activePosition, members.length);
  const draft = activeSelection(members, draftIndex);
  // 草稿项的展示名从该渠道已加载的成员列表里查；查不到就退回内部 ID。
  const draftMember =
    draft?.model_mode === "fixed-model" && draft.model_id
      ? (modelsByProfile[draft.profile_id]?.find((m) => m.id === draft.model_id) ?? null)
      : null;

  function addChannel(profileID: string) {
    const value = dedupeSelections([...members, { profile_id: profileID, model_mode: "channel-default" }]);
    onMembersChange(value);
    if (value.length === 1) onActivePositionChange?.(0);
  }

  function updateMember(index: number, next: LLMSelection) {
    const value = [...members];
    value[index] = next;
    onMembersChange(value);
  }

  function removeMember(index: number) {
    const value = members.filter((_, i) => i !== index);
    onMembersChange(value);
    if (value.length === 0) {
      onActivePositionChange?.(null);
      return;
    }
    if (activePosition !== null && index < activePosition) onActivePositionChange?.(activePosition - 1);
    else if (activePosition !== null && activePosition >= value.length) onActivePositionChange?.(value.length - 1);
  }

  function move(index: number, offset: -1 | 1) {
    const target = index + offset;
    if (target < 0 || target >= members.length) return;
    const value = [...members];
    [value[index], value[target]] = [value[target], value[index]];
    onMembersChange(value);
    // 跟着移动当前生效项，避免交换后语义被默默改掉。
    if (activePosition === index) onActivePositionChange?.(target);
    else if (activePosition === target) onActivePositionChange?.(index);
  }

  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-center gap-2">
        {/* biome-ignore lint/nursery/useNullishCoalescing: disabled 是可选 boolean，父级传 false 时仍要看 profiles.length===0，?? 会吞掉它 */}
        <Select value="" onValueChange={(id) => addChannel(id)} disabled={disabled || profiles.length === 0}>
          <SelectTrigger size="sm" id={inputId} className="w-56" aria-label="添加渠道候选">
            <SelectValue placeholder={profiles.length === 0 ? "暂无可用渠道" : "添加渠道候选"} />
          </SelectTrigger>
          <SelectContent>
            <SelectGroup>
              {profiles.map((p) => (
                <SelectItem key={p.id} value={p.id}>
                  {p.name}
                </SelectItem>
              ))}
            </SelectGroup>
          </SelectContent>
        </Select>
      </div>

      {members.length === 0 ? (
        <p className="text-muted-foreground text-xs">未指定候选时，任务跟随 Agent 绑定或全局激活配置。</p>
      ) : (
        <div className="flex flex-col divide-y rounded-lg border">
          {members.map((sel, index) => (
            <ChainMemberRow
              key={selectionKey(sel)}
              index={index}
              total={members.length}
              profileName={profilesByID.get(sel.profile_id)?.name ?? `渠道 #${sel.profile_id}`}
              value={sel}
              models={modelsByProfile[sel.profile_id]}
              modelsLoading={!!loadingProfiles?.[sel.profile_id]}
              onNeedModels={() => onNeedModels(sel.profile_id)}
              onChange={(next) => updateMember(index, next)}
              onMove={(offset) => move(index, offset)}
              onRemove={() => removeMember(index)}
              disabled={disabled}
              role={
                draftIndex === index ? (
                  <Badge variant="default">当前</Badge>
                ) : (
                  <Badge variant="secondary">备用 {index - (draftIndex ?? 0)}</Badge>
                )
              }
            />
          ))}
        </div>
      )}

      {onActivePositionChange && members.length > 1 && (
        <div className="flex flex-col items-stretch gap-2 sm:flex-row sm:items-center sm:justify-between sm:gap-3">
          <Label className="text-sm">当前生效候选</Label>
          <Select
            value={String(draftIndex ?? 0)}
            onValueChange={(v) => onActivePositionChange(Number(v))}
            disabled={disabled}
          >
            <SelectTrigger size="sm" className="w-full sm:w-56">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectGroup>
                {members.map((sel, index) => (
                  <SelectItem key={selectionKey(sel)} value={String(index)}>
                    {`${index + 1}. ${profilesByID.get(sel.profile_id)?.name ?? `渠道 #${sel.profile_id}`}`}
                  </SelectItem>
                ))}
              </SelectGroup>
            </SelectContent>
          </Select>
        </div>
      )}

      <ChainEffectiveStatus
        members={members}
        activeMember={activeMember}
        draft={draft}
        // draftMember：草稿项的展示名，从该渠道已加载的成员列表里查；查不到就退回内部 ID。
        draftMember={draftMember}
      />
    </div>
  );
}

// ChainEffectiveStatus 只讲事实：后端解析出的实际生效成员优先；链空=继承；链非空但
// 没有实际生效成员（已耗尽）=如实说明，绝不用第一项充数。
export function ChainEffectiveStatus({
  members,
  activeMember,
  draft,
  draftMember,
}: {
  members: LLMSelection[];
  activeMember?: LLMActiveMember | null;
  draft: LLMSelection | null;
  /** 草稿项的成员信息：用来把"固定成员 #ID"说成它的展示名。 */
  draftMember?: LLMModel | null;
}) {
  if (activeMember) {
    return (
      <p className="text-muted-foreground text-xs">
        实际生效：{formatMember(activeMember)} · 配置{" "}
        {activeMember.model_mode === "fixed-model" ? "固定成员" : "跟随渠道默认"}
      </p>
    );
  }
  if (activeMember === null) {
    return (
      <p className="text-destructive text-xs">
        当前无生效成员（候选链已耗尽或渠道无可用默认模型）；保存后从所选候选重新开始。
      </p>
    );
  }
  if (members.length === 0) return null;
  return (
    <p className="text-muted-foreground text-xs">
      保存后生效：渠道 #{draft?.profile_id ?? "?"} · {draft ? selectionModeLabel(draft, draftMember) : "无"}
    </p>
  );
}
