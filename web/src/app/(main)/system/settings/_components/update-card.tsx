"use client";

import * as React from "react";

import {
  CheckCircle2Icon,
  DownloadIcon,
  ExternalLinkIcon,
  RefreshCwIcon,
  RotateCcwIcon,
  TriangleAlertIcon,
} from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Progress } from "@/components/ui/progress";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { api } from "@/lib/api";
import { waitForUpdate } from "@/lib/update-flow";
import type { UpdateCheck, UpdateProgress } from "@/lib/types";

function humanSize(n?: number): string {
  if (!n || n <= 0) return "";
  const units = ["B", "KB", "MB", "GB"];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

export function UpdateCard() {
  const [info, setInfo] = React.useState<UpdateCheck | null>(null);
  const [checking, setChecking] = React.useState(true);
  const [progress, setProgress] = React.useState<UpdateProgress | null>(null);
  // 暂存后的重启窗口单独展示，继续轮询实际版本和持久确认状态。
  const [restarting, setRestarting] = React.useState(false);
  const [busy, setBusy] = React.useState(false);
  const [confirmation, setConfirmation] = React.useState<"update" | "rollback" | null>(null);
  const locked = React.useRef(false);
  const waiting = React.useRef<AbortController | null>(null);

  // quiet 同时决定要不要绕过后端缓存：进页面时的自动检查用缓存（顶栏刚查过），
  // 用户手动点「检查更新」则强制回源，否则刚发布的版本要等缓存过期才看得到。
  const check = React.useCallback((quiet = false) => {
    setChecking(true);
    api
      .checkUpdate(!quiet)
      .then((r) => {
        setInfo(r);
        if (!quiet) {
          if (r.error) toast.error("检查更新失败：" + r.error);
          else if (r.has_update) toast.success(`发现新版本 ${r.latest}`);
          else if (r.comparable) toast.success("当前已是最新版本");
        }
      })
      .catch((e) => {
        if (!quiet) toast.error("检查更新失败：" + (e as Error).message);
      })
      .finally(() => setChecking(false));
  }, []);

  React.useEffect(() => {
    check(true);
  }, [check]);

  const monitor = React.useCallback(async (target: string | undefined, from: string) => {
    const controller = new AbortController();
    waiting.current = controller;
    try {
      const result = await waitForUpdate(target, from, api.updateStatus, {
        signal: controller.signal,
        onStatus: (status) => {
          setRestarting(status.pending || status.phase === "staged");
          setProgress({
            phase: status.phase ?? "idle",
            percent: status.percent ?? -1,
            message: status.message || "正在应用更新…",
            error: status.error,
          });
        },
      });
      toast.success(`当前版本 ${result.current} 已稳定运行，正在重新加载页面`);
      await sleep(800);
      if (!controller.signal.aborted) window.location.reload();
    } catch (error) {
      if (!controller.signal.aborted) toast.error((error as Error).message);
    } finally {
      if (!controller.signal.aborted) {
        locked.current = false;
        setBusy(false);
        setRestarting(false);
      }
    }
  }, []);

  React.useEffect(() => {
    let active = true;
    // 刷新后恢复监测；重启后的状态不依赖已经断开的 SSE 或 GitHub 查询。
    api
      .updateStatus()
      .then((status) => {
        if (active && !locked.current && status.running) {
          locked.current = true;
          setBusy(true);
          void monitor(status.version || status.current, status.current);
        }
      })
      .catch(() => {});
    return () => {
      active = false;
      waiting.current?.abort();
    };
  }, [monitor]);

  const perform = async (action: "update" | "rollback") => {
    if (!info || locked.current) return;
    locked.current = true; // 同一事件循环的重复点击也不能重复提交。
    setConfirmation(null);
    setBusy(true);
    setProgress({ phase: "downloading", percent: 0, message: "准备中…" });
    try {
      if (action === "update") {
        const result = await api.applyUpdate();
        await monitor(result.target, info.current);
      } else {
        const result = await api.rollbackUpdate();
        await monitor(result.target || undefined, info.current);
      }
    } catch (error) {
      // 响应丢失时先查执行状态，不能立刻让用户重复提交。
      try {
        const status = await api.updateStatus();
        if (status.running || status.current !== info.current) {
          await monitor(status.version || (action === "update" ? info.latest : undefined), info.current);
          return;
        }
      } catch {
        /* 保留原始提交错误。 */
      }
      locked.current = false;
      setBusy(false);
      setProgress(null);
      toast.error((action === "update" ? "启动更新失败：" : "回滚失败：") + (error as Error).message);
    }
  };

  const phase = progress?.phase;
  const showProgress = busy || restarting;
  // 只有下载阶段拿得到真实百分比（按 Content-Length 算）。校验/解压/等待重启都是
  // 时长不可知的阶段，进度条填满并加个脉冲动画表示"在忙但说不准还要多久"。
  const downloading = !restarting && phase === "downloading";
  const pct = downloading ? Math.max(progress?.percent ?? 0, 0) : 100;

  return (
    // 设置页是多列瀑布流布局，卡片自己负责行间距并禁止跨列断开（见 page.tsx 的注释）。
    <Card className="mb-4 break-inside-avoid md:mb-6">
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <DownloadIcon className="size-4" />
          版本与更新
        </CardTitle>
        <CardDescription>从 GitHub 检查并安装新版本。更新会重启程序，正在运行的任务会被中断。</CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <span className="text-muted-foreground">当前版本</span>
          <Badge variant="secondary" className="font-mono">
            {info?.current ?? "…"}
          </Badge>
          {info && (
            <>
              <Badge variant="outline" className="font-mono">
                {info.os}/{info.arch}
              </Badge>
              <Badge variant="outline">{info.mode === "docker" ? "Docker" : "独立程序"}</Badge>
            </>
          )}
          {info?.latest && (
            <>
              <span className="text-muted-foreground">最新版本</span>
              <Badge variant={info.has_update ? "default" : "secondary"} className="font-mono">
                {info.latest}
              </Badge>
            </>
          )}
          {info?.html_url && (
            <a
              href={info.html_url}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1 text-xs text-muted-foreground underline-offset-4 hover:underline"
            >
              更新日志 <ExternalLinkIcon className="size-3" />
            </a>
          )}
        </div>

        {info?.boot_notice && (
          <p className="flex items-start gap-2 rounded-md border border-amber-500/40 bg-amber-500/10 p-2 text-xs text-amber-700 dark:text-amber-400">
            <TriangleAlertIcon className="mt-0.5 size-3.5 shrink-0" />
            {info.boot_notice}
          </p>
        )}

        {info?.error && (
          <p className="flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/10 p-2 text-xs text-destructive">
            <TriangleAlertIcon className="mt-0.5 size-3.5 shrink-0" />
            无法连接 GitHub：{info.error}
            {"　"}可在上方配置全局代理后重试。
          </p>
        )}

        {info && !info.comparable && info.reason && <p className="text-xs text-muted-foreground">{info.reason}</p>}

        {info?.has_update && info.asset_available === false && (
          <p className="flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/10 p-2 text-xs text-destructive">
            <TriangleAlertIcon className="mt-0.5 size-3.5 shrink-0" />
            {info.latest} 没有提供 {info.os}/{info.arch} 的发布包（缺少 {info.asset}），无法自动更新。
          </p>
        )}

        {info?.has_update && info.asset_available !== false && (
          <p className="text-xs text-muted-foreground">
            将下载 <span className="font-mono">{info.asset}</span>
            {info.size ? `（${humanSize(info.size)}）` : ""}，校验 SHA256 并冒烟测试后才会替换，失败自动保留当前版本。
          </p>
        )}

        {info && !info.has_update && info.comparable && !info.error && (
          <p className="flex items-center gap-2 text-xs text-muted-foreground">
            <CheckCircle2Icon className="size-3.5 text-emerald-600" />
            当前已是最新版本。
          </p>
        )}

        {info?.mode === "docker" && info.persistent_update === false && (
          <p className="text-xs text-destructive">
            此容器仍使用旧版启动器，需先部署支持持久更新的镜像；此后可直接在页面升级。
          </p>
        )}
        {info?.mode === "docker" && info.persistent_update !== false && info.has_update && (
          <p className="text-xs text-muted-foreground">
            新版程序（含页面）和上一版备份保存在数据卷。重启或重建容器仍保留已更新版本，启动失败会自动回滚。
            系统工具由镜像提供，工具链升级需更新镜像。
          </p>
        )}

        {showProgress && (
          <div className="space-y-1.5">
            <Progress value={pct} className={downloading ? undefined : "animate-pulse"} />
            <p className="text-xs text-muted-foreground">
              {restarting ? "正在重启并应用新版本，请稍候（页面会自动刷新）…" : progress?.message}
            </p>
          </div>
        )}

        <div className="flex flex-wrap gap-2">
          <Button variant="outline" size="sm" onClick={() => check(false)} disabled={checking || busy || restarting}>
            <RefreshCwIcon className={checking ? "size-4 animate-spin" : "size-4"} />
            检查更新
          </Button>
          <Button
            size="sm"
            onClick={() => setConfirmation("update")}
            disabled={
              busy ||
              restarting ||
              !info?.has_update ||
              info?.asset_available === false ||
              (info?.mode === "docker" && info.persistent_update === false)
            }
          >
            <DownloadIcon className="size-4" />
            {info?.has_update ? `更新到 ${info.latest}` : "立即更新"}
          </Button>
          {info?.has_backup && (
            <Button variant="ghost" size="sm" onClick={() => setConfirmation("rollback")} disabled={busy || restarting}>
              <RotateCcwIcon className="size-4" />
              回滚到上一版本
            </Button>
          )}
        </div>

        {info?.mode !== "docker" && (
          <p className="text-xs text-muted-foreground">
            一键更新依赖守护脚本重启程序。请通过 <span className="font-mono">start.sh</span>（Windows 为
            <span className="font-mono"> start.bat</span>）启动 ARTEX；直接运行 artex 本体时，程序退出后不会被自动拉起。
          </p>
        )}
        <AlertDialog
          open={confirmation !== null}
          onOpenChange={(open) => {
            if (!open) setConfirmation(null);
          }}
        >
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>
                {confirmation === "rollback" ? "回滚到上一版本？" : `更新到 ${info?.latest ?? "新版本"}？`}
              </AlertDialogTitle>
              <AlertDialogDescription>
                {confirmation === "rollback"
                  ? "程序会重启，正在运行的任务会被中断。数据库不会回退，请确认上一版本兼容当前数据。"
                  : info?.mode === "docker"
                    ? "将下载并校验新版程序，保存到数据卷后自动重启。正在运行的任务会被中断；数据和配置保留，启动失败自动回滚。"
                    : "将下载并校验新版程序后自动重启。正在运行的任务会被中断，启动失败自动回滚。"}
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>取消</AlertDialogCancel>
              <AlertDialogAction
                onClick={() => {
                  if (confirmation) void perform(confirmation);
                }}
                disabled={busy}
              >
                {confirmation === "rollback" ? "确认回滚" : "确认更新"}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </CardContent>
    </Card>
  );
}
