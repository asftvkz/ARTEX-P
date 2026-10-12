"use client";

// 渠道下的模型成员编辑器：拉取模型列表 → 勾选批量添加 → 一行一个模型（左上游 ID、
// 右可编辑展示名）→ 单选默认模型。新建渠道和编辑已有渠道共用同一套紧凑行 UI。
//
// display_name 仅供界面显示/人工识别，永远不参与路由或等价判断——两个渠道下的成员
// 即使 upstream_model_id 或 display_name 都相同，也不是同一个可运行目标，详见
// docs/llm-models-contract.md。纯逻辑（草稿 ↔ API 形状转换）在 @/lib/llm-models，
// 与本组件分开以便不依赖 DOM 的单测直接覆盖。

import * as React from "react";

import { Loader2Icon, PlusIcon, RefreshCwIcon, Trash2Icon } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import type { ModelRow } from "@/lib/llm-models";

export function ModelsEditor({
  rows,
  defaultKey,
  onRowsChange,
  onDefaultChange,
  onFetch,
  disabled = false,
}: {
  rows: ModelRow[];
  defaultKey: string;
  onRowsChange: (rows: ModelRow[]) => void;
  onDefaultChange: (key: string) => void;
  /** 实际发起拉取并返回模型 ID 列表；失败时返回空数组，错误提示由调用方处理。 */
  onFetch: () => Promise<string[]>;
  disabled?: boolean;
}) {
  const [pickerOpen, setPickerOpen] = React.useState(false);
  const [fetching, setFetching] = React.useState(false);
  const [fetchedModels, setFetchedModels] = React.useState<string[]>([]);
  const [checked, setChecked] = React.useState<Set<string>>(() => new Set());
  const existingIds = React.useMemo(() => new Set(rows.map((r) => r.upstreamId)), [rows]);
  // 拉取结果里还没加入列表的模型 ID——已在列表里的不重复勾选。
  const addable = React.useMemo(() => fetchedModels.filter((m) => !existingIds.has(m)), [fetchedModels, existingIds]);

  async function handleFetch() {
    if (fetching) return;
    setFetching(true);
    try {
      const result = await onFetch();
      setFetchedModels(result);
      setChecked(new Set());
      if (result.length > 0) setPickerOpen(true);
    } finally {
      setFetching(false);
    }
  }

  function toggleChecked(id: string, v: boolean) {
    setChecked((prev) => {
      const next = new Set(prev);
      if (v) next.add(id);
      else next.delete(id);
      return next;
    });
  }

  function addChecked() {
    const additions: ModelRow[] = [...checked].map((id) => ({
      key: id,
      upstreamId: id,
      displayName: id,
      enabled: true,
    }));
    if (additions.length === 0) return;
    const next = [...rows, ...additions];
    onRowsChange(next);
    if (!defaultKey && next.length > 0) onDefaultChange(next[0].key);
    setPickerOpen(false);
  }

  function removeRow(key: string) {
    if (rows.length <= 1) return; // 渠道至少保留一个成员
    const next = rows.filter((r) => r.key !== key);
    onRowsChange(next);
    if (defaultKey === key && next.length > 0) onDefaultChange(next[0].key);
  }

  function renameRow(key: string, name: string) {
    onRowsChange(rows.map((r) => (r.key === key ? { ...r, displayName: name } : r)));
  }

  return (
    <div className="grid gap-2 rounded-lg border p-3">
      <div className="flex items-center justify-between gap-2">
        <div className="grid gap-0.5">
          <Label className="text-sm">模型成员</Label>
          <p className="text-muted-foreground text-xs">
            左侧上游模型 ID 发给上游、不可编辑；右侧展示名仅用于识别，可随意改，不参与路由。
          </p>
        </div>
        <Popover open={pickerOpen} onOpenChange={setPickerOpen} modal>
          {/* modal: 弹层内容 portal 到 body，在 Sheet 的滚动锁之外，不加 modal 时
              列表能渲染却滚不动，让它自己持有最上层滚动锁。 */}
          <PopoverTrigger asChild>
            <Button
              type="button"
              variant="outline"
              size="sm"
              // biome-ignore lint/nursery/useNullishCoalescing: disabled 是可选 boolean，父级传 false 时仍要看 fetching，?? 会吞掉它
              disabled={disabled || fetching}
              onClick={handleFetch}
            >
              {fetching ? <Loader2Icon className="animate-spin" /> : <RefreshCwIcon />}
              拉取模型
            </Button>
          </PopoverTrigger>
          {fetchedModels.length > 0 && (
            <PopoverContent className="w-80 gap-0 p-0" align="end">
              <div className="max-h-64 overflow-y-auto overscroll-contain p-1">
                {addable.length === 0 ? (
                  <p className="p-3 text-center text-muted-foreground text-xs">拉取到的模型都已在列表中</p>
                ) : (
                  addable.map((id) => (
                    <div key={id} className="flex items-center gap-2 rounded-md px-2 py-1.5 text-xs hover:bg-accent">
                      <Checkbox
                        id={`fetch-pick-${id}`}
                        checked={checked.has(id)}
                        onCheckedChange={(v) => toggleChecked(id, v === true)}
                      />
                      <Label
                        htmlFor={`fetch-pick-${id}`}
                        className="min-w-0 flex-1 cursor-pointer truncate font-mono font-normal"
                      >
                        {id}
                      </Label>
                    </div>
                  ))
                )}
              </div>
              {addable.length > 0 && (
                <div className="flex justify-end border-t p-1.5">
                  <Button type="button" size="sm" disabled={checked.size === 0} onClick={addChecked}>
                    <PlusIcon /> 添加所选（{checked.size}）
                  </Button>
                </div>
              )}
            </PopoverContent>
          )}
        </Popover>
      </div>

      {rows.length === 0 ? (
        <p className="rounded-md border border-dashed p-3 text-center text-muted-foreground text-xs">
          还没有模型成员，先「拉取模型」勾选添加（至少一个）。
        </p>
      ) : (
        <RadioGroup value={defaultKey} onValueChange={onDefaultChange} className="gap-0 divide-y rounded-md border">
          {rows.map((row) => (
            <div key={row.key} className="flex items-center gap-2 px-2 py-1.5">
              <RadioGroupItem
                value={row.key}
                // biome-ignore lint/nursery/useNullishCoalescing: disabled 是可选 boolean，父级传 false 时仍要看 !row.enabled，?? 会吞掉它
                disabled={disabled || !row.enabled}
                aria-label={`设为默认模型：${row.upstreamId}`}
                title="设为该渠道默认模型"
              />
              <code className="min-w-0 flex-1 truncate font-mono text-xs" title={row.upstreamId}>
                {row.upstreamId}
              </code>
              <Input
                className="h-7 flex-1 text-xs"
                value={row.displayName}
                disabled={disabled}
                onChange={(e) => renameRow(row.key, e.target.value)}
                aria-label={`展示名：${row.upstreamId}`}
              />
              <Button
                type="button"
                variant="ghost"
                size="icon-sm"
                aria-label={`移除模型 ${row.upstreamId}`}
                title={rows.length <= 1 ? "渠道至少保留一个模型" : "移除"}
                // biome-ignore lint/nursery/useNullishCoalescing: disabled 是可选 boolean，父级传 false 时仍要看 rows.length<=1，?? 会吞掉它
                disabled={disabled || rows.length <= 1}
                onClick={() => removeRow(row.key)}
              >
                <Trash2Icon className="text-destructive" />
              </Button>
            </div>
          ))}
        </RadioGroup>
      )}
    </div>
  );
}
