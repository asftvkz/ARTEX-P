"use client";

import * as React from "react";

import { CheckIcon, ChevronsUpDownIcon, RadioTowerIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from "@/components/ui/command";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Label } from "@/components/ui/label";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { api } from "@/lib/api";
import { proxyErrorText, taskProxyLabel } from "@/lib/proxy-center";
import {
  proxyRevisionConflict,
  type TaskProxyDraft,
  TaskProxySubmission,
  taskProxyDraft,
  taskProxyInput,
  taskProxyOptionLabel,
  taskProxySwitchError,
  taskProxySwitchSelectable,
  validProxyRevision,
} from "@/lib/task-proxy";
import type { ProxyRecord, Task } from "@/lib/types";

export function TaskProxyControl({
  task,
  onUpdated,
  onSaving,
}: {
  task: Task;
  onUpdated: (task: Task) => void;
  onSaving: () => void;
}) {
  const [open, setOpen] = React.useState(false);
  const [selectOpen, setSelectOpen] = React.useState(false);
  const [draft, setDraft] = React.useState<TaskProxyDraft>(() => taskProxyDraft(task));
  const [items, setItems] = React.useState<ProxyRecord[]>([]);
  const [globalID, setGlobalID] = React.useState<number | null>(null);
  const [loading, setLoading] = React.useState(false);
  const [directoryError, setDirectoryError] = React.useState("");
  const [saveError, setSaveError] = React.useState("");
  const [saving, setSaving] = React.useState(false);
  const [needsReadback, setNeedsReadback] = React.useState(false);
  const [awaitingConfirmation, setAwaitingConfirmation] = React.useState(false);
  const [expectedRevision, setExpectedRevision] = React.useState(task.proxy_revision);
  const [submission] = React.useState(() => new TaskProxySubmission());
  const directorySequence = React.useRef(0);
  const mounted = React.useRef(true);

  React.useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      directorySequence.current++;
    };
  }, []);

  const reloadDirectory = async () => {
    const sequence = ++directorySequence.current;
    setLoading(true);
    setDirectoryError("");
    try {
      const result = await api.proxies();
      if (sequence !== directorySequence.current || !mounted.current) return;
      if (
        !Array.isArray(result.proxies) ||
        (result.global_proxy_id !== null &&
          (!Number.isSafeInteger(result.global_proxy_id) || result.global_proxy_id <= 0))
      ) {
        throw new Error("代理目录未返回有效的全局默认状态");
      }
      setItems(result.proxies);
      setGlobalID(result.global_proxy_id);
    } catch (error) {
      if (sequence === directorySequence.current && mounted.current) setDirectoryError(proxyErrorText(error));
    } finally {
      if (sequence === directorySequence.current && mounted.current) setLoading(false);
    }
  };

  const changeOpen = (next: boolean) => {
    if (submission.busy) return;
    setOpen(next);
    setSelectOpen(false);
    if (next) {
      if (!submission.needsReadback && !submission.awaitingConfirmation) {
        setDraft(taskProxyDraft(task));
        setExpectedRevision(task.proxy_revision);
        setSaveError("");
      }
      void reloadDirectory();
    } else directorySequence.current++;
  };

  const blocked = taskProxySwitchError({
    task: { ...task, proxy_revision: expectedRevision },
    draft,
    items,
    globalID,
    loading,
    error: directoryError,
  });
  const draftDisabled = saving || needsReadback || awaitingConfirmation;
  const selected = items.find((item) => String(item.id) === draft.id);
  const global = items.find((item) => item.id === globalID);
  let defaultLabel = "当前默认：直连";
  if (globalID !== null) defaultLabel = global ? taskProxyOptionLabel(global) : `代理 #${globalID}（记录缺失）`;
  if (loading || directoryError) defaultLabel = "当前全局默认尚未确认";
  const previousID = task.proxy_id ?? task.proxy?.id ?? null;
  const inheritedChanged = task.proxy_mode === "inherit" && !loading && !directoryError && previousID !== globalID;

  async function save() {
    if (submission.busy || (!submission.needsReadback && (blocked || submission.awaitingConfirmation))) return;
    setSaving(true);
    setSaveError("");
    onSaving();
    try {
      const result = await submission.save(task.id, taskProxyInput(draft, expectedRevision), {
        write: api.updateTaskProxy,
        read: api.task,
      });
      if (!result || !mounted.current) return;
      onUpdated(result);
      if (submission.writeConfirmed) {
        toast.success("任务代理已保存，并已回读当前选择");
        setOpen(false);
      } else {
        setSaveError("已核对服务端当前选择，草稿仍保留。如需再次切换，请明确确认新选择；不会自动重放旧请求。");
      }
    } catch (error) {
      if (!mounted.current) return;
      const status = (error as { status?: number })?.status;
      let message = `切换失败：${proxyErrorText(error)}。当前选择已保留。`;
      if (status === 409)
        message = `暂不能切换：${proxyErrorText(error)}。请先暂停任务并等待工具及其他在途操作退出；不会自动取消或重试。当前选择已保留。`;
      if (submission.needsReadback)
        message = `结果待核对：${proxyErrorText(error)}。可能存在执行封锁，请回读任务确认；不会自动重复保存。当前选择已保留。`;
      if (proxyRevisionConflict(error))
        message = `任务代理已被其他操作更新：${proxyErrorText(error)}。请回读当前状态，再明确确认新选择；草稿已保留，不会自动覆盖。`;
      setSaveError(message);
    } finally {
      if (mounted.current) {
        setSaving(false);
        setNeedsReadback(submission.needsReadback);
        setAwaitingConfirmation(submission.awaitingConfirmation);
      }
    }
  }

  let badge = taskProxyLabel(task);
  if (needsReadback) badge = "代理状态待确认";
  if (saving) badge = "正在核对代理…";
  let selectedLabel = "搜索并选择代理中心出口";
  if (draft.id) selectedLabel = `代理 #${draft.id}（原选择已不可用）`;
  if (selected) selectedLabel = taskProxyOptionLabel(selected);
  let saveLabel = needsReadback ? "回读任务核对" : "保存代理选择";
  if (saving) saveLabel = "正在保存并回读…";

  return (
    <Dialog open={open} onOpenChange={changeOpen}>
      <DialogTrigger asChild>
        <Button size="sm" variant="outline" title="查看或切换任务代理" aria-label="切换任务代理">
          <RadioTowerIcon data-icon="inline-start" />
          <span className="hidden lg:inline">{badge}</span>
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-xl" showCloseButton={!saving}>
        <DialogHeader>
          <DialogTitle>切换任务代理</DialogTitle>
          <DialogDescription>保存后更新此任务的出口快照。取消不会更改任务，不会自动暂停或恢复任务。</DialogDescription>
        </DialogHeader>
        <div className="space-y-2 rounded-lg border bg-muted/30 p-3 text-sm">
          <p className="font-medium">当前任务快照</p>
          {needsReadback ? (
            <p role="status">当前代理状态待确认，请先回读任务。</p>
          ) : (
            <>
              <p>
                {taskProxyLabel(task)}
                {task.proxy ? ` · ${task.proxy.name} · ${task.proxy.kind.toUpperCase()}` : ""}
              </p>
              <p className="text-muted-foreground text-xs">
                选择方式：
                {task.proxy_mode
                  ? { inherit: "继承全局默认", direct: "明确直连", fixed: "指定代理" }[task.proxy_mode]
                  : "旧任务未记录，请明确选择"}
              </p>
            </>
          )}
        </div>
        <div className="grid gap-2">
          <Label htmlFor="task-proxy-mode">连接方式</Label>
          <select
            id="task-proxy-mode"
            className="h-10 rounded-md border border-input bg-background px-3 text-sm"
            value={draft.mode}
            disabled={draftDisabled}
            onChange={(event) => setDraft({ ...draft, mode: event.target.value as TaskProxyDraft["mode"] })}
          >
            <option value="" disabled>
              请选择连接方式
            </option>
            <option value="inherit">继承当前全局默认</option>
            <option value="direct">明确直连</option>
            <option value="fixed">指定代理</option>
          </select>
        </div>
        {draft.mode === "inherit" && (
          <div className="space-y-2 text-sm">
            <p>{defaultLabel}</p>
            <p className="text-muted-foreground">
              保存时会重新选择当前全局默认，生成新的任务快照。仅打开或取消不会改变原快照。
            </p>
            {inheritedChanged && (
              <p role="status" className="font-medium">
                原快照与当前默认不同：{previousID ? `代理 #${previousID}` : "直连"} →{" "}
                {globalID ? `代理 #${globalID}` : "直连"}。保存才会切换。
              </p>
            )}
          </div>
        )}
        {draft.mode === "direct" && <p className="text-muted-foreground text-sm">明确直连，不使用代理中心出口。</p>}
        {draft.mode === "fixed" && (
          <div className="grid gap-2">
            <Label htmlFor="task-proxy-selection">代理中心出口</Label>
            <Popover open={selectOpen} onOpenChange={setSelectOpen}>
              <PopoverTrigger asChild>
                <Button
                  id="task-proxy-selection"
                  type="button"
                  variant="outline"
                  role="combobox"
                  aria-expanded={selectOpen}
                  disabled={draftDisabled || loading || !!directoryError}
                  className="h-auto min-h-10 justify-between whitespace-normal text-left"
                >
                  <span>{selectedLabel}</span>
                  <ChevronsUpDownIcon className="ml-2 size-4 shrink-0" />
                </Button>
              </PopoverTrigger>
              <PopoverContent align="start" className="w-[var(--radix-popover-trigger-width)] p-0">
                <Command>
                  <CommandInput placeholder="搜索名称、类型、编号或出口 IP…" />
                  <CommandList>
                    <CommandEmpty>没有匹配的代理</CommandEmpty>
                    <CommandGroup heading="代理中心">
                      {items.map((item) => (
                        <CommandItem
                          key={item.id}
                          value={taskProxyOptionLabel(item)}
                          disabled={!taskProxySwitchSelectable(item)}
                          onSelect={() => {
                            setDraft({ mode: "fixed", id: String(item.id) });
                            setSelectOpen(false);
                          }}
                        >
                          {draft.id === String(item.id) && <CheckIcon className="size-4" />}
                          <span>
                            {taskProxyOptionLabel(item)}
                            {!taskProxySwitchSelectable(item) && " · 不可用"}
                          </span>
                        </CommandItem>
                      ))}
                    </CommandGroup>
                  </CommandList>
                </Command>
              </PopoverContent>
            </Popover>
            {draft.id && (!selected || !taskProxySwitchSelectable(selected)) && (
              <p role="alert" className="text-destructive text-xs">
                原选择 #{draft.id} 已失效或未验证，已保留编号，请检测或明确选择其他出口。
              </p>
            )}
          </div>
        )}
        <div className="flex items-center justify-between gap-2 text-muted-foreground text-xs">
          <span>{loading ? "正在加载代理目录…" : "仅可选择已启用且健康的出口；选中失败不会回落直连。"}</span>
          <Button
            type="button"
            size="sm"
            variant="ghost"
            disabled={saving || loading}
            onClick={() => void reloadDirectory()}
          >
            刷新代理目录
          </Button>
        </div>
        {directoryError && (
          <p role="alert" className="text-destructive text-sm">
            代理目录加载失败：{directoryError}。原选择保持不变。
          </p>
        )}
        <p className="text-muted-foreground text-xs">
          运行或排队中的任务需先暂停并等待操作退出。暂停或任务结束不代表工具已退出，是否允许切换以服务端检查为准。
        </p>
        {blocked && !needsReadback && (
          <p role="status" className="text-destructive text-sm">
            {blocked}
          </p>
        )}
        {saveError && (
          <p role="alert" className="text-destructive text-sm">
            {saveError}
          </p>
        )}
        {awaitingConfirmation && !needsReadback && (
          <Button
            type="button"
            variant="outline"
            disabled={saving || !validProxyRevision(task.proxy_revision)}
            onClick={() => {
              if (!validProxyRevision(task.proxy_revision) || !submission.confirmNewIntent()) return;
              setExpectedRevision(task.proxy_revision);
              setAwaitingConfirmation(false);
              setSaveError("");
              void reloadDirectory();
            }}
          >
            确认以当前任务状态重新选择
          </Button>
        )}
        <DialogFooter>
          <Button type="button" variant="outline" disabled={saving} onClick={() => changeOpen(false)}>
            取消
          </Button>
          <Button
            type="button"
            disabled={saving || (!needsReadback && (!!blocked || awaitingConfirmation))}
            onClick={() => void save()}
          >
            {saveLabel}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
