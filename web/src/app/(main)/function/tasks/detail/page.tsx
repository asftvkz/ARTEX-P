"use client";

import * as React from "react";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";

import {
  ArchiveIcon,
  ArrowLeftIcon,
  BrainIcon,
  CheckIcon,
  CircleAlertIcon,
  PauseIcon,
  PlayIcon,
  RadioTowerIcon,
} from "lucide-react";
import { toast } from "sonner";

import { ChannelModelChain } from "@/components/llm-selection";
import { StatusBadge } from "@/components/status-badge";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import {
  Popover,
  PopoverContent,
  PopoverDescription,
  PopoverHeader,
  PopoverTitle,
  PopoverTrigger,
} from "@/components/ui/popover";
import { Separator } from "@/components/ui/separator";
import { SidebarTrigger } from "@/components/ui/sidebar";
import { Spinner } from "@/components/ui/spinner";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { api } from "@/lib/api";
import {
  chainValidationError,
  emptyModelCache,
  formatMember,
  initialActivePosition,
  needModels,
  resolveActivePosition,
  selectionsFromTask,
  selectionsFromTaskWithReport,
  validationErrorFromDropped,
} from "@/lib/llm-selection";
import { proxyEndpoint, taskProxyLabel } from "@/lib/proxy-center";
import type { LLMModel, LLMProfile, LLMSelection, Task } from "@/lib/types";

import { AssetsTab } from "./_tabs/assets-tab";
import { BroadcastTab } from "./_tabs/broadcast-tab";
import { CoverageGraphTab } from "./_tabs/coverage-graph-tab";
import { FindingsTab } from "./_tabs/findings-tab";
import { GraphTab } from "./_tabs/graph-tab";
import { InterceptTab } from "./_tabs/intercept-tab";
import { OverviewTab } from "./_tabs/overview-tab";
import { ReportTab } from "./_tabs/report-tab";
import { RetestsTab } from "./_tabs/retests-tab";
import { SessionsTab } from "./_tabs/sessions-tab";

const TABS = [
  { value: "sessions", label: "会话" },
  { value: "overview", label: "总览" },
  { value: "graph", label: "探索链路" },
  { value: "broadcast", label: "播报板" },
  { value: "findings", label: "发现" },
  { value: "retests", label: "复测" },
  { value: "assets", label: "测试资产" },
  { value: "coverage", label: "资产覆盖图" },
  { value: "intercept", label: "拦截审批" },
  { value: "report", label: "报告" },
];

// TaskLLMControl 编辑任务的渠道+模型候选链：
//   - 配置态 members 从 DTO 的 members 读（存在即权威，空数组=继承）；
//   - 实际生效项来自 DTO 的 active_member，链耗尽时为 null，绝不拿第一项充数；
//   - 保存走 PUT /tasks/{id}/llm，members 始终显式发送（空数组=恢复继承）。
function TaskLLMControl({ task, profiles, onUpdated }: { task: Task; profiles: LLMProfile[]; onUpdated: () => void }) {
  const [open, setOpen] = React.useState(false);
  const [members, setMembers] = React.useState<LLMSelection[]>(() => selectionsFromTask(task));
  // 编辑草稿的"生效候选"位置：仅决定保存后从第几项开始尝试，不代表当前实际生效。
  const [activePosition, setActivePosition] = React.useState<number | null>(() =>
    initialActivePosition(task.active_position ?? null, selectionsFromTask(task).length),
  );
  const [modelsByProfile, setModelsByProfile] = React.useState<Record<string, LLMModel[] | undefined>>({});
  const [loadingProfiles, setLoadingProfiles] = React.useState<Record<string, boolean>>({});
  const modelCacheRef = React.useRef(emptyModelCache());
  const [saving, setSaving] = React.useState(false);
  const popoverContentRef = React.useRef<HTMLDivElement>(null);

  const chainMembers = selectionsFromTask(task);
  // DTO 里若有坏行（未知模式、缺成员等），读取时就被丢掉——这里必须记住这件事并阻止
  // 保存：净化后的链可能正好变空，而"空链"表示继承，坏数据会被当成正常继承写回去。
  const dtoDropped = React.useMemo(() => selectionsFromTaskWithReport(task).dropped, [task]);
  const exhausted = task.llm_failover_state === "chain_exhausted";
  // 任何状态都可以改链,终态也不例外:任务结束后主 Agent 对话仍走这条链,
  // 链上模型出问题时必须能换掉,否则已完成任务就没法继续交互。
  const terminal = ["done", "failed", "timeout"].includes(task.status);
  // 实际生效项只认后端解析结果（active_member）：配置态是 channel-default 时它也带
  // 真实 model_id，成员键才是身份。没有它就说"无生效项"，不拿第一项充数。
  const liveMember = task.active_member ?? undefined;
  const currentLabel = liveMember
    ? liveMember.display_name || liveMember.upstream_model_id
    : exhausted
      ? "配置链已耗尽"
      : chainMembers.length === 0
        ? "跟随默认配置"
        : "无生效成员";
  const backupCount = Math.max(
    0,
    chainMembers.length - (resolveActivePosition(task.active_position ?? null, chainMembers.length) ?? 0) - 1,
  );
  const currentTitle = [
    currentLabel,
    liveMember ? formatMember(liveMember) : "",
    backupCount > 0 ? `${backupCount} 个备用` : "",
  ]
    .filter(Boolean)
    .join(" · ");
  let editorDescription = "调整顺序或当前配置后，将从下一次 LLM 调用开始生效。";
  if (terminal) editorDescription = "任务已结束，改动只影响后续的主 Agent 对话。";
  let saveLabel = "保存";
  if (exhausted) saveLabel = "保存并重置";
  if (saving) saveLabel = "保存中";

  const loadProfileModels = React.useCallback(async (profileID: string) => {
    setLoadingProfiles((prev) => ({ ...prev, [profileID]: true }));
    try {
      const models = await needModels(modelCacheRef.current, profileID, async (id) => {
        const r = await api.llmModels(Number(id));
        return r.models;
      });
      setModelsByProfile((prev) => ({ ...prev, [profileID]: models }));
    } catch (e) {
      toast.error(`读取渠道 #${profileID} 的模型列表失败：${(e as Error).message}`);
    } finally {
      setLoadingProfiles((prev) => ({ ...prev, [profileID]: false }));
    }
  }, []);

  const syncDraft = React.useCallback(() => {
    const next = selectionsFromTask(task);
    setMembers(next);
    setActivePosition(initialActivePosition(task.active_position ?? null, next.length));
  }, [task]);

  const handleOpenChange = (next: boolean) => {
    setOpen(next);
    if (next) syncDraft();
  };

  const chainError = chainValidationError(members);
  // DTO 坏行与客户端草稿校验合并成同一条阻止保存的理由。
  const saveBlockedReason = chainError || validationErrorFromDropped(dtoDropped);

  const save = async () => {
    if (saveBlockedReason) {
      toast.error(saveBlockedReason);
      return;
    }
    setSaving(true);
    try {
      const result = await api.updateTaskLLMMembers(
        task.id,
        members,
        // members 为空(继承)时不下发位置；否则发草稿位置（null 时也省略，后端选第一项）。
        members.length > 0 && activePosition !== null ? activePosition : undefined,
      );
      if (result.switch_event) {
        toast.info(result.switch_event.summary, { id: `task-${task.id}-llm-${result.switch_event.seq}` });
      } else {
        toast.success(
          result.reopened_intents > 0
            ? `LLM 配置已更新，并恢复 ${result.reopened_intents} 条额度阻塞意图`
            : "LLM 配置已更新",
        );
      }
      setOpen(false);
      onUpdated();
    } catch (error) {
      toast.error("更新失败：" + (error as Error).message);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Popover open={open} onOpenChange={handleOpenChange}>
      <PopoverTrigger asChild>
        <Button
          size="sm"
          variant={exhausted ? "destructive" : "outline"}
          aria-label="查看或切换任务 LLM 配置"
          title={currentTitle}
        >
          <BrainIcon data-icon="inline-start" />
          <span className="hidden max-w-36 truncate lg:inline">{currentLabel}</span>
          {backupCount > 0 && <span className="hidden text-muted-foreground xl:inline">+{backupCount}</span>}
        </Button>
      </PopoverTrigger>
      <PopoverContent ref={popoverContentRef} align="start" className="w-[min(28rem,calc(100vw-2rem))] gap-4 p-4">
        <PopoverHeader>
          <PopoverTitle>任务 LLM 配置链</PopoverTitle>
          <PopoverDescription>{editorDescription}</PopoverDescription>
        </PopoverHeader>

        {exhausted && (
          <Alert variant="destructive">
            <CircleAlertIcon />
            <AlertTitle>配置链额度已耗尽</AlertTitle>
            <AlertDescription>
              {task.llm_failover_reason ?? "所有已选配置均被判定为额度不足。保存配置链可重置故障状态。"}
            </AlertDescription>
          </Alert>
        )}

        <ChannelModelChain
          profiles={profiles}
          members={members}
          activePosition={activePosition}
          activeMember={liveMember}
          onMembersChange={setMembers}
          onActivePositionChange={setActivePosition}
          modelsByProfile={modelsByProfile}
          onNeedModels={(id) => void loadProfileModels(id)}
          loadingProfiles={loadingProfiles}
          disabled={saving}
          inputId="task-llm-profiles"
        />
        {chainError && (
          <p role="alert" className="text-destructive text-xs">
            {chainError}
          </p>
        )}
        {!chainError && dtoDropped.length > 0 && (
          <p role="alert" className="text-destructive text-xs">
            {validationErrorFromDropped(dtoDropped)}
          </p>
        )}

        <div className="flex justify-end gap-2">
          <Button type="button" variant="outline" size="sm" onClick={() => setOpen(false)}>
            关闭
          </Button>
          <Button type="button" size="sm" onClick={save} disabled={saving || !!saveBlockedReason}>
            {saving && <Spinner data-icon="inline-start" />}
            {saveLabel}
          </Button>
        </div>
      </PopoverContent>
    </Popover>
  );
}

function TaskDetailInner() {
  const router = useRouter();
  const searchParams = useSearchParams();
  const id = searchParams.get("id") ?? "";
  const [task, setTask] = React.useState<Task | null>(null);
  const [paused, setPaused] = React.useState(false);
  const [loaded, setLoaded] = React.useState(false);
  const [tab, setTab] = React.useState("sessions");
  const [interceptPendingCount, setInterceptPendingCount] = React.useState(0);
  const [profiles, setProfiles] = React.useState<LLMProfile[]>([]);
  const [archiving, setArchiving] = React.useState(false);

  React.useEffect(() => {
    api
      .llmProfiles()
      .then(setProfiles)
      .catch(() => setProfiles([]));
  }, []);

  React.useEffect(() => {
    let alive = true;
    const load = () =>
      api
        .interceptTask(id)
        .then((rows) => {
          if (alive) setInterceptPendingCount(rows.filter((r) => r.status === "pending").length);
        })
        .catch(() => {
          // Polling is best-effort; the next interval retries automatically.
        });
    void load();
    const t = setInterval(load, 5000);
    return () => {
      alive = false;
      clearInterval(t);
    };
  }, [id]);

  const taskLoadInFlight = React.useRef<string | null>(null);
  const load = React.useCallback(() => {
    if (taskLoadInFlight.current === id) return;
    taskLoadInFlight.current = id;
    Promise.all([api.task(id), api.stats(id).catch(() => null)])
      .then(([task, s]) => {
        if (taskLoadInFlight.current !== id) return;
        const base = { ...task };
        const at = s?.active_task;
        if (at) {
          base.in_flight = at.in_flight;
          base.goals_total = at.goals_total;
          base.goals_met = at.goals_met;
          base.engine_mode = s?.engine_mode ?? at.engine_mode;
          base.paused = at.paused;
        }
        setTask(base);
        setPaused(at?.paused ?? base.paused ?? false);
      })
      .catch(() => {
        // Keep the last rendered task state during a transient poll failure.
      })
      .finally(() => {
        if (taskLoadInFlight.current === id) {
          taskLoadInFlight.current = null;
          setLoaded(true);
        }
      });
  }, [id]);
  React.useEffect(() => {
    load();
    const timer = setInterval(load, 5000);
    return () => clearInterval(timer);
  }, [load]);

  async function togglePause() {
    if (task && ["done", "failed", "timeout"].includes(task.status)) return;
    const next = !paused;
    try {
      await api.controlTask(id, next ? "pause" : "resume");
      setPaused(next);
      toast.success(next ? "已暂停探索" : "已恢复探索");
    } catch (e) {
      toast.error("操作失败：" + (e as Error).message);
    }
  }

  async function archiveTask() {
    if (!task || archiving) return;
    setArchiving(true);
    try {
      await api.archiveTask(task.id);
      toast.success("任务已加入归档队列");
      router.push("/function/tasks");
    } catch (error) {
      toast.error(`归档失败：${(error as Error).message}`);
      setArchiving(false);
    }
  }

  if (!task) {
    return (
      <div className="flex flex-1 flex-col items-center justify-center gap-3 p-10 text-center">
        <p className="text-muted-foreground">{loaded ? `任务 ${id} 已被删除、归档或不存在` : "加载中…"}</p>
        {loaded && (
          <Button asChild variant="outline">
            <Link href="/function/tasks">
              <ArrowLeftIcon /> 返回任务列表
            </Link>
          </Button>
        )}
      </div>
    );
  }

  const completed = task.status === "done";
  const terminal = ["done", "failed", "timeout"].includes(task.status);
  const archiveLifecycleEligible = terminal || paused || task.status === "paused";
  const canArchive = archiveLifecycleEligible && !task.archive_blocked_by_task_id;
  let archiveDisabledReason = task.queued ? "排队中的任务必须先暂停" : "运行中的任务必须先暂停";
  if (archiveLifecycleEligible && task.archive_blocked_by_task_id) {
    archiveDisabledReason = `任务被未归档任务 #${task.archive_blocked_by_task_id} 直接继承，请先归档依赖任务`;
  }
  const engineMode = paused ? "paused" : (task.engine_mode ?? "idle");
  let controlVariant: "default" | "secondary" | "outline" = "outline";
  let controlIcon = <PauseIcon data-icon="inline-start" />;
  let controlLabel = "暂停";
  if (terminal) {
    controlVariant = "secondary";
    controlIcon = <CheckIcon data-icon="inline-start" />;
    controlLabel = completed ? "已完成" : "已结束";
  } else if (paused) {
    controlVariant = "default";
    controlIcon = <PlayIcon data-icon="inline-start" />;
    controlLabel = "恢复";
  }
  const archiveTrigger = (
    <Button
      size="icon-sm"
      variant="ghost"
      disabled={!canArchive || archiving}
      aria-label={canArchive ? "归档任务" : archiveDisabledReason}
    >
      {archiving ? <Spinner /> : <ArchiveIcon />}
    </Button>
  );

  return (
    <Tabs value={tab} onValueChange={setTab} className="flex flex-1 flex-col gap-0">
      {/* Top fixed area */}
      <header className="sticky top-0 z-10 flex flex-col gap-2 border-b bg-background/95 px-4 py-2.5 backdrop-blur lg:px-6">
        <div className="flex items-center gap-2">
          <SidebarTrigger className="-ml-1" />
          <Button asChild variant="ghost" size="icon" className="size-7">
            <Link href="/function/tasks">
              <ArrowLeftIcon />
            </Link>
          </Button>
          <h1 className="min-w-0 flex-1 truncate text-sm font-semibold" title={task.name || task.description}>
            {task.name || task.description}
          </h1>
          <code className="hidden rounded bg-muted px-1.5 py-0.5 font-mono text-xs text-muted-foreground sm:inline">
            {task.id}
          </code>
          <Separator orientation="vertical" className="mx-1 hidden h-4 sm:block" />
          <TaskLLMControl task={task} profiles={profiles} onUpdated={load} />
          <Popover>
            <PopoverTrigger asChild>
              <Button size="sm" variant="outline" title="任务代理快照" aria-label="查看任务代理快照">
                <RadioTowerIcon data-icon="inline-start" />
                <span className="hidden lg:inline">{taskProxyLabel(task)}</span>
              </Button>
            </PopoverTrigger>
            <PopoverContent align="end" className="w-[min(24rem,calc(100vw-2rem))] gap-3 p-4">
              <PopoverHeader>
                <PopoverTitle>任务代理快照</PopoverTitle>
                <PopoverDescription>创建时保存的出口，同时用于任务工具和任务 LLM 请求。</PopoverDescription>
              </PopoverHeader>
              <p className="text-sm">
                选择方式：
                {task.proxy_mode
                  ? { inherit: "继承全局默认", direct: "明确直连", fixed: "指定编号" }[task.proxy_mode]
                  : "旧任务未记录"}
              </p>
              {task.proxy ? (
                <div className="space-y-2 text-sm">
                  <p>
                    编号：#{task.proxy.id} · {task.proxy.name}
                  </p>
                  <p>类型：{task.proxy.kind}</p>
                  <p className="break-all font-mono text-xs">出口：{proxyEndpoint(task.proxy)}</p>
                  {task.proxy.resin_platform && <p>Platform：{task.proxy.resin_platform}</p>}
                </div>
              ) : (
                <p className="text-sm">{taskProxyLabel(task)}</p>
              )}
              <p className="text-muted-foreground text-xs">
                全局默认或代理记录后续变更不会改写此快照；当前健康状态请到代理中心检测。
              </p>
            </PopoverContent>
          </Popover>
          <StatusBadge domain={terminal ? "task" : "engine"} value={terminal ? task.status : engineMode} dot />
          {canArchive ? (
            <AlertDialog>
              <AlertDialogTrigger asChild>{archiveTrigger}</AlertDialogTrigger>
              <AlertDialogContent>
                <AlertDialogHeader>
                  <AlertDialogTitle>归档任务 #{task.id}？</AlertDialogTitle>
                  <AlertDialogDescription>
                    任务图谱、关联记录、独占资产与流量、工作文件和 LLM
                    历史将压缩到冷存储。归档完成后可在任务列表的“已归档”页还原。
                  </AlertDialogDescription>
                </AlertDialogHeader>
                <AlertDialogFooter>
                  <AlertDialogCancel>取消</AlertDialogCancel>
                  <AlertDialogAction onClick={() => void archiveTask()}>确认归档</AlertDialogAction>
                </AlertDialogFooter>
              </AlertDialogContent>
            </AlertDialog>
          ) : (
            <Tooltip>
              <TooltipTrigger asChild>
                <span className="inline-flex">{archiveTrigger}</span>
              </TooltipTrigger>
              <TooltipContent>{archiveDisabledReason}</TooltipContent>
            </Tooltip>
          )}
          <Button size="sm" variant={controlVariant} onClick={togglePause} disabled={terminal}>
            {controlIcon}
            {controlLabel}
          </Button>
        </div>
        <p className="truncate text-xs text-muted-foreground">{task.goal}</p>
        {/* Tabs */}
        <div className="no-scrollbar min-w-0 overflow-x-auto">
          <TabsList variant="default" className="min-w-max">
            {TABS.map((t) => (
              <TabsTrigger key={t.value} value={t.value}>
                {t.label}
                {t.value === "intercept" && interceptPendingCount > 0 && (
                  <span className="ml-1.5 inline-flex h-4 min-w-[16px] items-center justify-center rounded-full bg-amber-500 px-1 text-[10px] font-semibold leading-none text-white">
                    {interceptPendingCount > 99 ? "99+" : interceptPendingCount}
                  </span>
                )}
              </TabsTrigger>
            ))}
          </TabsList>
        </div>
      </header>

      {/* Tab content */}
      <div className="flex-1 p-4 lg:p-6">
        <TabsContent value="sessions" className="mt-0">
          <SessionsTab taskId={id} />
        </TabsContent>
        <TabsContent value="overview" className="mt-0">
          <OverviewTab taskId={id} />
        </TabsContent>
        <TabsContent value="graph" className="mt-0">
          <GraphTab taskId={id} />
        </TabsContent>
        <TabsContent value="broadcast" className="mt-0">
          <BroadcastTab taskId={id} />
        </TabsContent>
        <TabsContent value="findings" className="mt-0">
          <FindingsTab taskId={id} />
        </TabsContent>
        <TabsContent value="retests" className="mt-0">
          <RetestsTab key={id} taskId={id} />
        </TabsContent>
        <TabsContent value="assets" className="mt-0">
          <AssetsTab taskId={id} />
        </TabsContent>
        <TabsContent value="coverage" className="mt-0">
          <CoverageGraphTab taskId={id} coverageEnabled={task?.coverage_enabled !== false} />
        </TabsContent>
        <TabsContent value="intercept" className="mt-0">
          <InterceptTab taskId={id} />
        </TabsContent>
        <TabsContent value="report" className="mt-0">
          <ReportTab taskId={id} />
        </TabsContent>
      </div>
    </Tabs>
  );
}

// useSearchParams must sit under a Suspense boundary for static export.
export default function TaskDetailPage() {
  return (
    <React.Suspense fallback={null}>
      <TaskDetailInner />
    </React.Suspense>
  );
}
