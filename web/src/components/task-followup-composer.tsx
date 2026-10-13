"use client";

import * as React from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";

// 结束的是上一轮执行，追问仍归属原任务；不会重新运行原 Worker。
export function TaskFollowupComposer({ taskId, intentId, mainSeg, onSent }: {
  taskId: string; intentId: number; mainSeg: number; onSent: () => void;
}) {
  const [mode, setMode] = React.useState<"question" | "execute">("question");
  const [text, setText] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const [unknown, setUnknown] = React.useState(false);
  const sending = React.useRef(false);
  async function send() {
    if (sending.current || unknown || !text.trim()) return;
    sending.current = true;
    setBusy(true);
    try {
      await api.chat(text.trim(), taskId, undefined, mainSeg, { intent_id: intentId, mode });
      setText("");
      onSent();
      toast.success("已交给主 Agent，保留原任务与执行记录");
    } catch (error) {
      const status = (error as { status?: number }).status;
      // 无确定响应时禁止重发，先查看主会话是否已经接收。
      if (!status || status >= 500) setUnknown(true);
      toast.error(`发送失败：${(error as Error).message || "请稍后核对主 Agent 会话"}`);
    } finally {
      sending.current = false;
      setBusy(false);
    }
  }
  return <div className="space-y-2 border-t p-3">
    <div className="flex gap-2">
      <Button type="button" size="sm" variant={mode === "question" ? "secondary" : "ghost"}
        disabled={busy || unknown} onClick={() => setMode("question")}>继续追问</Button>
      <Button type="button" size="sm" variant={mode === "execute" ? "secondary" : "ghost"}
        disabled={busy || unknown} onClick={() => setMode("execute")}>追加执行</Button>
    </div>
    <p className="text-xs text-muted-foreground">{mode === "question"
      ? "主 Agent 会结合这次执行记录回答，不重新运行原 Worker。"
      : "主 Agent 会结合这次结果安排后续工作，原记录保留。"}</p>
    <Textarea aria-label="完成后继续提问" placeholder={mode === "question" ? "继续询问这次结果…" : "说明需要补充执行的工作…"}
      value={text} maxLength={12000} disabled={busy || unknown} onChange={(event) => setText(event.target.value)} />
    <div className="flex justify-end gap-2">
      {unknown ? <Button type="button" variant="outline" onClick={onSent}>结果待确认，查看主 Agent</Button>
        : <Button type="button" size="sm" disabled={busy || !text.trim()} onClick={() => void send()}>
          {busy ? "正在发送…" : mode === "question" ? "发送追问" : "提交追加工作"}
        </Button>}
    </div>
  </div>;
}
