import type { UpdateStatus } from "./types";

/** 更新成功必须是准确目标且已通过启动确认；断线与回滚分别处理。 */
export async function waitForUpdate(
  target: string | undefined,
  from: string,
  read: () => Promise<UpdateStatus>,
  options: {
    attempts?: number;
    signal?: AbortSignal;
    delay?: () => Promise<void>;
    onStatus?: (status: UpdateStatus) => void;
  } = {},
): Promise<UpdateStatus> {
  const delay = options.delay ?? (() => new Promise<void>((resolve) => setTimeout(resolve, 2000)));
  const normalize = (version: string) => version.replace(/^v/, "");
  for (let attempt = 0; attempt < (options.attempts ?? 300); attempt++) {
    if (options.signal?.aborted) throw new Error("已取消等待更新");
    await delay();
    if (options.signal?.aborted) throw new Error("已取消等待更新");
    let status: UpdateStatus;
    try {
      status = await read();
    } catch {
      // 重启窗口内断线是正常情况，不能据此宣称成功或重新提交更新。
      continue;
    }
    if (options.signal?.aborted) throw new Error("已取消等待更新");
    options.onStatus?.(status);
    if (status.phase === "failed") throw new Error(status.error || "更新失败");
    if (status.rolled_back && !status.running) throw new Error(status.message || "新版启动失败，已回滚到上一版本");
    const reached = target
      ? normalize(status.current) === normalize(target)
      : normalize(status.current) !== normalize(from);
    if (reached && !status.pending && !status.running) return status;
  }
  throw new Error("等待更新超时，请查看服务日志；请勿重复提交更新");
}
