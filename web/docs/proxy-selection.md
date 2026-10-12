# 代理中心与渠道代理前端合同

适用于本次 `feat/task-proxy-center` 前端，基线 `913d7ad`。本轮只修改 `web/**`，未提交、推送或部署。

## 一次确认新增 WARP

选择轻量 WARP 后只需填写名称。页面显示 Cloudflare 官方服务条款链接，点击“确认添加（同意条款）”发送：

```json
{"name":"出口名称","kind":"warp","accept_terms":true}
```

提交期间禁用名称、类型和按钮，同步锁阻止重复点击。成功必须同时满足启用、`warp_phase:ready`、`status:healthy` 和非空 `egress_ip`。

- 502 错误包的 `proxy_id` 与 `proxy.id` 一致时保留该 ID、表单和失败状态，不重新创建。
- 网络断开、超时或没有可识别 ID 的服务端失败提示结果待确认，并禁用原提交；先刷新目录核对，再编辑已建条目。
- 只有旧的 `unregistered` 条目提供明确同意的手动注册修复。`registering`、`starting` 或未知 phase 不提供注册；`failed` 只提供原 ID 的恢复。
- 已停止/失败的条目隐藏旧 IP，出口验证成功才显示已连接。
- 前端不自动重 POST，但不能替代服务端幂等。浏览器或代理在响应完全丢失时可能重发请求；本轮没有新增服务端持久幂等机制。

## 渠道代理选择

下拉只读取脱敏的 `GET /api/proxies`，按名称、类型、编号和出口 IP 搜索。提供继承全局默认、明确直连与固定代理；HTTP/HTTPS/SOCKS5/WARP 可选。Resin 可见但禁选，并提示“暂不支持渠道绑定”。

保存、连接测试和拉取模型共同调用 `llmProxyRequest`：

| 选择 | 发送内容 |
| --- | --- |
| 原选择未改变，包括旧自定义 URL | 省略 `proxy_mode`、`proxy_id`、`proxy`，服务端按渠道 ID 保留/读取 |
| 继承默认 | `proxy_mode:inherit`、`proxy_id:null` |
| 明确直连 | `proxy_mode:direct`、`proxy_id:null` |
| 固定中心代理 | `proxy_mode:fixed`、`proxy_id:"正数字符串"` |

旧自定义 URL 仅以高级选项识别和保留，掩码不得进入请求。新显式模式不发送旧 `proxy` 文本，不拼接代理中心的凭据 URL。`proxy_mode` 不发送空字符串或 null；明确清除使用 inherit/null。

使用的接口：GET/POST `/api/llm/profiles`、POST `/api/llm/test`、POST `/api/llm/models`。已保存配置测试/拉模型带 `profile_id`；保存沿用既有 `id`。新代理 ID 在前端始终按字符串传递。API Key 继续由 `resolveApiKeyToSend` 保护，未明确更换时仅使用已保存 Key。

## 验证与证据

在 `web` 目录执行：

```powershell
node --import ./scripts/test-api-loader.mjs --test --test-timeout=60000 src/lib/llm-proxy-api.test.mjs src/lib/proxy-center.test.mjs src/lib/proxy-warp-register.test.mjs src/lib/llm-models.test.mjs src/lib/llm-selection.test.mjs
npx tsc --noEmit --incremental false
$env:NEXT_EXPORT='1'
$env:NEXT_PUBLIC_MOCK='0'
npx next build --webpack
```

载荷测试调用生产 API 客户端并拦截 fetch，只证明请求载荷；不表示真实 HTTP 联调。真实本地 UI→Go API→PG、HTTP 合成上游出站及 DTO 展示证据保存在忽略提交的 `web/output/verification/`，详细结果、已知失败边界和冻结 SHA256 见该目录的交付报告与清单。JWT、初始化密码和运行密钥不写入证据。

恢复前端时可按冻结清单回退本轮文件；不得覆盖并行后端 owner 的改动。此文档不代表生产部署或真实外网 WARP 注册验收。
