"use client";

import * as React from "react";

import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { api } from "@/lib/api";
import {
  buildProxyPayload,
  proxyEndpoint as endpoint,
  proxyErrorText as errorText,
  type ProxyForm as Form,
  proxyFromFailure,
  proxySelectable as selectable,
  WARP_PHASE_LABEL,
  WARP_REGISTER_TERMS,
  WARP_TERMS_URL,
  warpActionNeedsTerms,
  warpAllowedActions,
  warpReady,
} from "@/lib/proxy-center";
import type { ProxyKind, ProxyRecord } from "@/lib/types";

const KINDS: Array<{ value: ProxyKind; label: string }> = [
  { value: "http", label: "HTTP" },
  { value: "https", label: "HTTPS" },
  { value: "socks5", label: "SOCKS5" },
  { value: "warp", label: "轻量 WARP" },
  { value: "resin", label: "Resin 动态代理" },
];
const STATUS: Record<string, string> = {
  unknown: "未检测",
  healthy: "可用",
  failed: "失败",
  starting: "启动中",
  stopped: "已停止",
};
const freshForm = (): Form => ({
  name: "",
  kind: "http",
  protocol: "socks5",
  url: "",
  host: "",
  port: "",
  password: "",
  platform: "Default",
  adminURL: "",
  adminToken: "",
  enabled: true,
  clearPassword: false,
  clearAdminToken: false,
});
function badgeVariant(status: string): "default" | "secondary" | "destructive" {
  if (status === "failed") return "destructive";
  if (status === "healthy") return "default";
  return "secondary";
}

export function ProxyCenter() {
  const [items, setItems] = React.useState<ProxyRecord[]>([]);
  const [globalID, setGlobalID] = React.useState<number | null>(null);
  const [form, setForm] = React.useState<Form>(freshForm);
  const [editing, setEditing] = React.useState<ProxyRecord | null>(null);
  const [loading, setLoading] = React.useState(true);
  const [loadError, setLoadError] = React.useState("");
  const [busy, setBusy] = React.useState("");
  const mutationLock = React.useRef(false);
  const [saveError, setSaveError] = React.useState("");
  const [createUncertain, setCreateUncertain] = React.useState(false);
  const [registrationUncertain, setRegistrationUncertain] = React.useState<number[]>([]);
  // WARP 注册条款确认：registering 记录待确认的代理编号；acceptTerms 必须由用户
  // 手动勾选（每次打开都重置为 false，绝不预选）。
  const [registering, setRegistering] = React.useState<number | null>(null);
  const [acceptTerms, setAcceptTerms] = React.useState(false);

  const reload = React.useCallback(async () => {
    const result = await api.proxies();
    setItems(result.proxies ?? []);
    setGlobalID(result.global_proxy_id ?? null);
    setLoadError("");
  }, []);
  React.useEffect(() => {
    let active = true;
    api
      .proxies()
      .then((result) => {
        if (!active) return;
        setItems(result.proxies ?? []);
        setGlobalID(result.global_proxy_id ?? null);
      })
      .catch((error) => {
        if (active) setLoadError(errorText(error));
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, []);
  const reset = () => {
    if (mutationLock.current) return;
    setEditing(null);
    setForm(freshForm());
    setSaveError("");
    setCreateUncertain(false);
  };
  const edit = (item: ProxyRecord) => {
    if (mutationLock.current) return;
    setEditing(item);
    setSaveError("");
    setCreateUncertain(false);
    setForm({
      name: item.name,
      kind: item.kind,
      protocol: item.protocol === "http" ? "http" : "socks5",
      // 脱敏 URL 绝不能被当作真实凭据回写；留空让后端保留旧值。
      url: "",
      host: item.host ?? "",
      port: item.port ? String(item.port) : "",
      password: "",
      platform: item.resin_platform ?? "Default",
      adminURL: item.resin_admin_url ?? "",
      adminToken: "",
      enabled: item.enabled,
      clearPassword: false,
      clearAdminToken: false,
    });
    window.scrollTo({ top: 0, behavior: "smooth" });
  };

  async function refreshAfterMutation() {
    try {
      await reload();
    } catch (error) {
      setLoadError(errorText(error));
      toast.error(`操作已返回，但最新状态加载失败：${errorText(error)}。请刷新核对。`);
    }
  }

  async function save() {
    if (mutationLock.current || createUncertain) return;
    let data: ReturnType<typeof buildProxyPayload>;
    try {
      data = buildProxyPayload(form, !!editing);
    } catch (error) {
      toast.error(errorText(error));
      return;
    }
    mutationLock.current = true;
    setBusy("save");
    setSaveError("");
    try {
      const result = editing ? await api.updateProxy(editing.id, data) : await api.createProxy(data);
      if (!editing && form.kind === "warp" && !warpReady(result)) {
        setEditing(result);
        setSaveError(`WARP #${result.id} 已建档，但尚未连接成功。请核对状态并恢复该条目。`);
      } else {
        let message = "代理已加入中心";
        if (editing) message = "代理配置已保存";
        else if (form.kind === "warp") message = `WARP 已连接 · 出口 ${result.egress_ip}`;
        toast.success(message);
        setEditing(null);
        setForm(freshForm());
      }
      await refreshAfterMutation();
    } catch (error) {
      const retained = proxyFromFailure(error);
      if (retained) {
        setEditing(retained);
        setItems((prev) => [retained, ...prev.filter((item) => item.id !== retained.id)]);
        setSaveError(`WARP #${retained.id} 已保留：${errorText(error)}。请恢复此条目，勿重复添加。`);
      } else {
        const status = (error as { status?: number }).status;
        const uncertain = !editing && form.kind === "warp" && (!status || status >= 500);
        setCreateUncertain(uncertain);
        setSaveError(
          uncertain
            ? `添加结果不确定：${errorText(error)}。请刷新列表核对后编辑已有条目，不要重复提交。`
            : `保存失败：${errorText(error)}`,
        );
      }
      toast.error(errorText(error));
    } finally {
      mutationLock.current = false;
      setBusy("");
    }
  }
  async function choose(id: number | null) {
    setBusy("global");
    try {
      await api.setGlobalProxy(id);
      toast.success(id === null ? "全局默认已设为直连" : `全局默认已设为代理 #${id}`);
      await refreshAfterMutation();
    } catch (error) {
      toast.error(`设置失败：${errorText(error)}`);
    } finally {
      setBusy("");
    }
  }
  async function test(id: number) {
    setBusy(`test:${id}`);
    try {
      const result = await api.testProxy(id);
      await refreshAfterMutation();
      if (result.status === "healthy") toast.success(`代理 #${id} 检测可用`);
      else toast.info(`代理 #${id} 检测完成：${STATUS[result.status] ?? result.status}`);
    } catch (error) {
      try {
        await reload();
      } catch {
        /* 保留检测错误 */
      }
      toast.error(`代理 #${id} 检测失败：${errorText(error)}`);
    } finally {
      setBusy("");
    }
  }
  // 注册必须由用户先确认条款：只有确认后才会带着 accept_terms:true 调用；
  // start/stop/recover 不涉及条款，也不带请求体。
  async function warp(id: number, action: "register" | "start" | "stop" | "recover") {
    if (mutationLock.current) return;
    const current = items.find((item) => item.id === id) ?? (editing?.id === id ? editing : null);
    if (
      !current ||
      !warpAllowedActions(current).includes(action) ||
      (action === "register" && registrationUncertain.includes(id))
    )
      return;
    if (warpActionNeedsTerms(action) && acceptTerms !== true) return;
    mutationLock.current = true;
    if (action === "register") setRegistrationUncertain((prev) => [...prev, id]);
    setBusy(`warp:${id}`);
    try {
      const result = await api.proxyWarpAction(id, action, warpActionNeedsTerms(action) ? true : undefined);
      setItems((prev) => prev.map((item) => (item.id === id ? result : item)));
      if (editing?.id === id) setEditing(result);
      await refreshAfterMutation();
      if (warpReady(result)) {
        toast.success(`WARP #${id} 已连接 · 出口 ${result.egress_ip}`);
        if (editing?.id === id) setSaveError("");
      } else toast.info(`WARP #${id} 当前状态：${WARP_PHASE_LABEL[result.warp_phase ?? ""] ?? "状态待核对"}`);
      setRegistering(null);
      setAcceptTerms(false);
    } catch (error) {
      const retained = proxyFromFailure(error);
      if (retained) {
        setItems((prev) => prev.map((item) => (item.id === id ? retained : item)));
        if (editing?.id === id) setEditing(retained);
      }
      try {
        await reload();
      } catch {
        /* 保留操作错误 */
      }
      toast.error(`WARP #${id} 操作失败：${errorText(error)}`);
      // 注册失败或响应丢失均不重注册；后续仅按服务端 phase 恢复原 ID。
      setRegistering(null);
      setAcceptTerms(false);
    } finally {
      mutationLock.current = false;
      setBusy("");
    }
  }
  async function release(id: number) {
    if (!window.confirm(`确认请求释放 Resin #${id} 的租约？`)) return;
    setBusy(`resin:${id}`);
    try {
      await api.releaseResin(id);
      await refreshAfterMutation();
      toast.info(`Resin #${id} 释放请求已返回，请核对运行状态`);
    } catch (error) {
      toast.error(`Resin #${id} 释放失败：${errorText(error)}`);
    } finally {
      setBusy("");
    }
  }
  async function remove(item: ProxyRecord) {
    if (!window.confirm(`确认删除代理 #${item.id}「${item.name}」？任务占用时服务端会拒绝。`)) return;
    setBusy(`delete:${item.id}`);
    try {
      await api.deleteProxy(item.id);
      if (editing?.id === item.id) reset();
      toast.success(`代理 #${item.id} 已删除`);
      await refreshAfterMutation();
    } catch (error) {
      toast.error(`删除失败：${errorText(error)}`);
    } finally {
      setBusy("");
    }
  }

  const global = items.find((item) => item.id === globalID);
  let saveLabel = "添加代理";
  if (!editing && form.kind === "warp") saveLabel = "确认添加（同意条款）";
  if (editing) saveLabel = "保存修改";
  if (busy === "save") saveLabel = "保存中…";
  if (busy === "save" && form.kind === "warp" && !editing) saveLabel = "正在添加 / 连接…";
  let globalLabel = "直连";
  if (globalID) globalLabel = `#${globalID}（记录未返回）`;
  if (global) globalLabel = `#${global.id} ${global.name}`;
  if (loading || loadError) globalLabel = "正在核对或状态未知";
  return (
    <div className="flex flex-1 flex-col gap-5">
      <div>
        <h1 className="font-semibold text-xl tracking-tight">代理中心</h1>
        <p className="text-muted-foreground text-sm">
          普通代理、轻量 WARP 与 Resin 共享编号。任务可继承默认、明确直连或指定编号。
        </p>
      </div>
      <Card>
        <CardHeader>
          <CardTitle>{editing ? `编辑代理 #${editing.id}` : "新增代理"}</CardTitle>
          <CardDescription>
            {form.kind === "warp"
              ? "输入名称并确认添加，系统自动注册、启动和验证出口。"
              : "凭据不回显；编辑时秘密字段留空表示保留，勾选清除才会删除。"}
          </CardDescription>
        </CardHeader>
        <CardContent className="grid gap-4 md:grid-cols-2">
          <div className="grid gap-2">
            <Label htmlFor="proxy-name">名称</Label>
            <Input
              id="proxy-name"
              disabled={!!busy || createUncertain}
              value={form.name}
              onChange={(e) => setForm({ ...form, name: e.target.value })}
            />
          </div>
          <div className="grid gap-2">
            <Label htmlFor="proxy-kind">类型</Label>
            <select
              id="proxy-kind"
              className="h-10 rounded-md border border-input bg-background px-3 text-sm"
              value={form.kind}
              disabled={!!editing || !!busy || createUncertain}
              onChange={(e) => setForm({ ...form, kind: e.target.value as ProxyKind })}
            >
              {KINDS.map((kind) => (
                <option key={kind.value} value={kind.value}>
                  {kind.label}
                </option>
              ))}
            </select>
          </div>
          {form.kind !== "warp" && form.kind !== "resin" && (
            <div className="grid gap-2 md:col-span-2">
              <Label htmlFor="proxy-url">代理 URL {editing ? "（留空保留原地址）" : ""}</Label>
              {editing && <p className="break-all text-muted-foreground text-xs">当前：{endpoint(editing)}</p>}
              <Input
                id="proxy-url"
                autoComplete="off"
                value={form.url}
                onChange={(e) => setForm({ ...form, url: e.target.value })}
                placeholder={`${form.kind}://user@host:port`}
              />
              <Label htmlFor="ordinary-password">认证密码 {editing?.has_password ? "（已保存）" : ""}</Label>
              <Input
                id="ordinary-password"
                type="password"
                autoComplete="new-password"
                value={form.password}
                disabled={form.clearPassword}
                onChange={(e) => setForm({ ...form, password: e.target.value })}
                placeholder={editing ? "留空保留原密码" : "可选，也可包含在 URL 中"}
              />
              {editing && (
                <label className="flex gap-2 text-xs">
                  <input
                    type="checkbox"
                    checked={form.clearPassword}
                    onChange={(e) => setForm({ ...form, clearPassword: e.target.checked, password: "" })}
                  />
                  明确清除认证密码
                </label>
              )}
            </div>
          )}
          {form.kind === "warp" && (
            <div className="space-y-2 text-muted-foreground text-sm md:col-span-2">
              {!editing && (
                <p>
                  点击“确认添加”表示同意{" "}
                  <a href={WARP_TERMS_URL} target="_blank" rel="noreferrer" className="underline">
                    Cloudflare WARP 服务条款
                  </a>
                  ，并申请注册。轻量 WARP 提供本机 SOCKS5 出口。
                </p>
              )}
              {editing && (
                <p>
                  代理 #{editing.id} · {WARP_PHASE_LABEL[editing.warp_phase ?? ""] ?? "状态待核对"} · 出口 IP：
                  {warpReady(editing) ? editing.egress_ip : "未验证"}
                </p>
              )}
            </div>
          )}
          {form.kind === "resin" && (
            <>
              <div className="grid gap-2">
                <Label htmlFor="proxy-host">数据面 Host</Label>
                <Input id="proxy-host" value={form.host} onChange={(e) => setForm({ ...form, host: e.target.value })} />
              </div>
              <div className="grid gap-2">
                <Label htmlFor="proxy-port">数据面 Port</Label>
                <Input
                  id="proxy-port"
                  inputMode="numeric"
                  value={form.port}
                  onChange={(e) => setForm({ ...form, port: e.target.value })}
                />
              </div>
              <div className="grid gap-2">
                <Label htmlFor="proxy-protocol">数据面协议</Label>
                <select
                  id="proxy-protocol"
                  className="h-10 rounded-md border border-input bg-background px-3 text-sm"
                  value={form.protocol}
                  onChange={(e) => setForm({ ...form, protocol: e.target.value as Form["protocol"] })}
                >
                  <option value="socks5">SOCKS5</option>
                  <option value="http">HTTP</option>
                </select>
              </div>
              <div className="grid gap-2">
                <Label htmlFor="resin-platform">Platform</Label>
                <Input
                  id="resin-platform"
                  value={form.platform}
                  onChange={(e) => setForm({ ...form, platform: e.target.value })}
                />
                <p className="text-muted-foreground text-xs">任务 Account 按任务编号生成，不在此设置。</p>
              </div>
              <div className="grid gap-2 md:col-span-2">
                <Label htmlFor="resin-admin-url">Admin URL（可选）</Label>
                <Input
                  id="resin-admin-url"
                  value={form.adminURL}
                  onChange={(e) => setForm({ ...form, adminURL: e.target.value })}
                  placeholder="https://resin.example.com"
                />
              </div>
              <div className="grid gap-2">
                <Label htmlFor="proxy-password">Proxy Token {editing?.has_password ? "（已保存）" : ""}</Label>
                <Input
                  id="proxy-password"
                  type="password"
                  autoComplete="new-password"
                  value={form.password}
                  disabled={form.clearPassword}
                  onChange={(e) => setForm({ ...form, password: e.target.value })}
                  placeholder={editing ? "留空保留" : "输入 Token"}
                />
                {editing && (
                  <label className="flex gap-2 text-xs">
                    <input
                      type="checkbox"
                      checked={form.clearPassword}
                      onChange={(e) => setForm({ ...form, clearPassword: e.target.checked, password: "" })}
                    />
                    明确清除 Proxy Token
                  </label>
                )}
              </div>
              <div className="grid gap-2">
                <Label htmlFor="resin-admin-token">
                  Admin Token {editing?.has_resin_admin_token ? "（已保存）" : ""}
                </Label>
                <Input
                  id="resin-admin-token"
                  type="password"
                  autoComplete="new-password"
                  value={form.adminToken}
                  disabled={form.clearAdminToken}
                  onChange={(e) => setForm({ ...form, adminToken: e.target.value })}
                  placeholder="留空保留（新建可选）"
                />
                {editing && (
                  <label className="flex gap-2 text-xs">
                    <input
                      type="checkbox"
                      checked={form.clearAdminToken}
                      onChange={(e) => setForm({ ...form, clearAdminToken: e.target.checked, adminToken: "" })}
                    />
                    明确清除 Admin Token
                  </label>
                )}
              </div>
            </>
          )}
          {(form.kind !== "warp" || editing) && (
            <div className="flex items-center gap-3 md:col-span-2">
              <Switch
                id="proxy-enabled"
                checked={form.enabled}
                onCheckedChange={(enabled) => setForm({ ...form, enabled })}
              />
              <Label htmlFor="proxy-enabled">启用此代理</Label>
            </div>
          )}
          {saveError && (
            <p role="alert" className="text-destructive text-sm md:col-span-2">
              {saveError}
            </p>
          )}
          <div className="flex gap-2 md:col-span-2">
            <Button disabled={!!busy || createUncertain} onClick={() => void save()}>
              {saveLabel}
            </Button>
            {editing?.kind === "warp" && warpAllowedActions(editing).includes("recover") && (
              <Button variant="outline" disabled={!!busy} onClick={() => void warp(editing.id, "recover")}>
                恢复此出口 #{editing.id}
              </Button>
            )}
            {editing && (
              <Button variant="outline" disabled={!!busy} onClick={reset}>
                取消编辑
              </Button>
            )}
          </div>
        </CardContent>
      </Card>
      <Card>
        <CardHeader>
          <CardTitle>代理列表</CardTitle>
          <CardDescription>
            全局默认：{globalLabel}
            。更改仅影响之后创建的任务快照。
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          <div className="flex gap-2">
            <Button
              variant={globalID === null ? "default" : "outline"}
              disabled={!!busy || loading || !!loadError || globalID === null}
              onClick={() => void choose(null)}
            >
              全局默认直连
            </Button>
            <Button
              variant="outline"
              disabled={!!busy || loading}
              onClick={() => {
                setLoading(true);
                void reload()
                  .catch((error) => setLoadError(errorText(error)))
                  .finally(() => setLoading(false));
              }}
            >
              刷新状态
            </Button>
          </div>
          {loading && <p className="text-muted-foreground text-sm">正在加载代理…</p>}
          {!loading && loadError && (
            <p role="alert" className="text-destructive text-sm">
              代理列表加载失败：{loadError}
            </p>
          )}
          {!loading && !loadError && items.length === 0 && <p className="text-muted-foreground text-sm">暂无代理。</p>}
          {!loading &&
            !loadError &&
            items.map((item) => (
              <div key={item.id} className="space-y-2 rounded-lg border p-3">
                <div className="flex flex-wrap items-start justify-between gap-3">
                  <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-2">
                      <strong>
                        #{item.id} {item.name}
                      </strong>
                      <Badge variant={badgeVariant(item.status)}>{STATUS[item.status] ?? item.status}</Badge>
                      {!item.enabled && <Badge variant="outline">已停用</Badge>}
                      {globalID === item.id && <Badge variant="outline">全局默认</Badge>}
                    </div>
                    <p className="text-muted-foreground text-xs">
                      {KINDS.find((kind) => kind.value === item.kind)?.label ?? item.kind} ·{" "}
                      <span className="break-all font-mono">{endpoint(item)}</span>
                    </p>
                    {item.kind === "resin" && (
                      <p className="text-muted-foreground text-xs">
                        Platform：{item.resin_platform || "Default"} · Proxy Token：
                        {item.has_password ? "已保存" : "未确认"} · Admin Token：
                        {item.has_resin_admin_token ? "已保存" : "未确认"}
                      </p>
                    )}
                    {item.last_checked_at && (
                      <p className="text-muted-foreground text-xs">
                        最近检测：{new Date(item.last_checked_at).toLocaleString()}
                      </p>
                    )}
                    {item.last_error && (
                      <p className="break-all text-destructive text-xs">诊断：{errorText(item.last_error)}</p>
                    )}
                  </div>
                  <div className="flex flex-wrap gap-2">
                    <Button
                      size="sm"
                      variant={globalID === item.id ? "default" : "outline"}
                      disabled={!!busy || !selectable(item) || globalID === item.id}
                      onClick={() => void choose(item.id)}
                    >
                      {globalID === item.id ? "全局默认" : "设为默认"}
                    </Button>
                    <Button size="sm" variant="outline" disabled={!!busy} onClick={() => edit(item)}>
                      编辑
                    </Button>
                    <Button size="sm" variant="outline" disabled={!!busy} onClick={() => void test(item.id)}>
                      {busy === `test:${item.id}` ? "检测中…" : "检测"}
                    </Button>
                    <Button size="sm" variant="destructive" disabled={!!busy} onClick={() => void remove(item)}>
                      删除
                    </Button>
                  </div>
                </div>
                {item.kind === "warp" && (
                  <div className="flex flex-wrap gap-2 border-t pt-2">
                    <p className="w-full text-muted-foreground text-xs">
                      {WARP_PHASE_LABEL[item.warp_phase ?? ""] ?? "状态待核对"} · 出口 IP：
                      {warpReady(item) ? item.egress_ip : "未验证"}
                    </p>
                    {warpAllowedActions(item).map((action) => (
                      <Button
                        key={action}
                        size="sm"
                        variant="outline"
                        disabled={
                          !!busy ||
                          (action === "register" && registrationUncertain.includes(item.id)) ||
                          (action === "start" && item.status === "healthy") ||
                          (action === "stop" && item.status === "stopped")
                        }
                        onClick={() => {
                          if (warpActionNeedsTerms(action)) {
                            // 注册：先弹确认框，用户明确同意后才发请求。
                            setAcceptTerms(false);
                            setRegistering(item.id);
                            return;
                          }
                          void warp(item.id, action);
                        }}
                      >
                        {{ register: "注册", start: "启动", stop: "停止", recover: "恢复" }[action]}
                      </Button>
                    ))}
                    {(item.warp_phase === "registering" || registrationUncertain.includes(item.id)) && (
                      <p className="text-muted-foreground text-xs">
                        注册结果可能不确定，请核对原条目；不会自动重复注册。
                      </p>
                    )}
                  </div>
                )}
                {item.kind === "resin" && (
                  <div className="border-t pt-2">
                    <Button size="sm" variant="outline" disabled={!!busy} onClick={() => void release(item.id)}>
                      释放租约
                    </Button>
                  </div>
                )}
              </div>
            ))}
        </CardContent>
      </Card>

      {/* WARP 注册条款确认：勾选项默认不选中；取消不发请求；确认后才带 accept_terms:true。 */}
      <Dialog
        open={registering !== null}
        onOpenChange={(open) => {
          if (mutationLock.current) return;
          if (!open) {
            setRegistering(null);
            setAcceptTerms(false);
          }
        }}
      >
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>注册 WARP 出口 #{registering}</DialogTitle>
            <DialogDescription>注册前请阅读并确认以下说明；未确认不会发起任何注册请求。</DialogDescription>
          </DialogHeader>
          <ul className="list-disc space-y-2 pl-5 text-muted-foreground text-sm">
            {WARP_REGISTER_TERMS.map((line) => (
              <li key={line}>{line}</li>
            ))}
          </ul>
          <a href={WARP_TERMS_URL} target="_blank" rel="noreferrer" className="text-sm underline">
            Cloudflare WARP 官方服务条款
          </a>
          <Label htmlFor="warp-accept-terms" className="flex items-start gap-2 font-normal text-sm">
            <Checkbox
              id="warp-accept-terms"
              checked={acceptTerms}
              onCheckedChange={(checked) => setAcceptTerms(checked === true)}
            />
            <span>我已阅读并同意上述注册说明，确认现在向 Cloudflare 申请注册。</span>
          </Label>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              disabled={!!busy}
              onClick={() => {
                setRegistering(null);
                setAcceptTerms(false);
              }}
            >
              取消
            </Button>
            <Button
              type="button"
              disabled={!acceptTerms || !!busy}
              onClick={() => {
                if (registering === null) return;
                void warp(registering, "register");
              }}
            >
              {busy ? "注册中…" : "同意并注册"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
