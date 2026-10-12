"use client";

import * as React from "react";

import { CheckIcon, ChevronsUpDownIcon } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from "@/components/ui/command";
import { Label } from "@/components/ui/label";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { type LLMProxyDraft, llmProxyLabel, proxyOptionLabel } from "@/lib/llm-proxy";
import { proxyErrorText, proxySelectable } from "@/lib/proxy-center";
import type { ProxyRecord } from "@/lib/types";

export function LLMProxySelect({
  value,
  onChange,
  items,
  loading,
  error,
  onReload,
  legacy,
  disabled,
}: {
  value: LLMProxyDraft;
  onChange: (v: LLMProxyDraft) => void;
  items: ProxyRecord[];
  loading: boolean;
  error: string;
  onReload: () => void;
  legacy: string;
  disabled: boolean;
}) {
  const [open, setOpen] = React.useState(false);
  function select(mode: LLMProxyDraft["mode"], id = "") {
    // 切回“保留旧配置”恢复省略字段的语义，而非把掩码当新 URL 发送。
    onChange({ mode, id, changed: mode !== "legacy" });
    setOpen(false);
  }
  return (
    <div className="grid gap-2">
      <Label htmlFor="p-proxy">代理（可选）</Label>
      <Popover open={open} onOpenChange={setOpen}>
        <PopoverTrigger asChild>
          <Button
            id="p-proxy"
            type="button"
            variant="outline"
            role="combobox"
            aria-expanded={open}
            disabled={disabled}
            className="h-auto min-h-10 w-full justify-between whitespace-normal text-left"
          >
            <span>{llmProxyLabel(value, items)}</span>
            <ChevronsUpDownIcon className="ml-2 size-4 shrink-0" />
          </Button>
        </PopoverTrigger>
        <PopoverContent align="start" className="w-[var(--radix-popover-trigger-width)] p-0">
          <Command>
            <CommandInput placeholder="搜索名称、类型、编号或出口 IP…" />
            <CommandList>
              <CommandEmpty>{loading ? "正在加载代理…" : "没有匹配的代理"}</CommandEmpty>
              <CommandGroup heading="连接方式">
                {(["inherit", "direct"] as const).map((mode) => (
                  <CommandItem
                    key={mode}
                    value={mode === "inherit" ? "继承全局默认" : "明确直连"}
                    onSelect={() => select(mode)}
                  >
                    {value.mode === mode && <CheckIcon className="size-4" />}
                    {mode === "inherit" ? "继承全局默认" : "明确直连"}
                  </CommandItem>
                ))}
                {legacy && (
                  <CommandItem value="旧自定义 URL 高级配置 保留" onSelect={() => select("legacy")}>
                    {value.mode === "legacy" && <CheckIcon className="size-4" />}旧自定义 URL（高级配置，保留）
                  </CommandItem>
                )}
              </CommandGroup>
              <CommandGroup heading="代理中心">
                {items.map((item) => (
                  <CommandItem
                    key={item.id}
                    value={proxyOptionLabel(item)}
                    disabled={loading || !!error || item.kind === "resin" || !proxySelectable(item)}
                    onSelect={() => select("fixed", String(item.id))}
                  >
                    {value.mode === "fixed" && value.id === String(item.id) && <CheckIcon className="size-4" />}
                    <span>
                      {proxyOptionLabel(item)}
                      {item.kind === "resin" ? " · 暂不支持渠道绑定" : !proxySelectable(item) && " · 不可用"}
                    </span>
                  </CommandItem>
                ))}
              </CommandGroup>
            </CommandList>
          </Command>
        </PopoverContent>
      </Popover>
      <div className="flex items-center justify-between gap-2 text-muted-foreground text-xs">
        <span>{loading ? "正在加载代理目录…" : "代理凭据由服务端读取，选中失败不会回落直连。"}</span>
        <Button type="button" size="sm" variant="ghost" disabled={loading || disabled} onClick={onReload}>
          刷新代理
        </Button>
      </div>
      {error && (
        <p role="alert" className="text-destructive text-xs">
          代理目录加载失败：{error}。请刷新重试，当前选择保持不变。
        </p>
      )}
      {value.mode === "legacy" && (
        <p className="break-all text-muted-foreground text-xs">
          旧自定义 URL（高级配置）：{proxyErrorText(legacy)}。保持原配置时不会回传地址；更换请在代理中心配置后选择。
        </p>
      )}
      <p className="text-muted-foreground text-xs">
        任务的指定代理或明确直连优先；继承模式使用任务创建时的全局出口快照，对话等入口使用当前全局默认。
      </p>
    </div>
  );
}
