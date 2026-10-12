# 代理中心与渠道代理前端合同

适用于 `feat/task-proxy-center` 前端；本次 WARP 幂等补丁基线 `b0ec701`。只修改 `web/**`，提交与部署由主脑统一处理。

## 一次确认新增 WARP

选择轻量 WARP 后只需填写名称。页面显示 Cloudflare 官方服务条款链接，点击“确认添加（同意条款）”发送：

```json
{"name":"出口名称","kind":"warp","accept_terms":true}
```

首次确认后，在发送前把规范化名称、`kind:warp`、明确的 `accept_terms:true` 和 UUID 一起写入 sessionStorage 并读回确认，不保存任何凭据。请求头 `Idempotency-Key` 使用该 UUID，API 客户端不生成 key。渲染、同一事件循环双击、网络重发和手动重试都使用同一 key 与载荷。普通代理不要求此头。

提交期间禁用名称、类型和按钮，同步锁阻止重复点击。成功必须同时满足启用、`warp_phase:ready`、`status:healthy` 和非空 `egress_ip`；成功后进入“已完成添加”，保留原意图，只有明确“新增另一个”才开启新意图。

- 202 或 502 的 `operation_status:pending` 优先于 DTO 的 phase：保留 key，不显示完成或提供 recover。用同 key 重放查询操作结果，每 2 秒最多 30 次；断网/超时后暂停，可手动继续。GET byID 只有运行 DTO，不能据此宣称创建操作已持久化完成。
- 502 failed 包的 `proxy_id` 与 `proxy.id` 一致时保留该 ID、表单和失败状态；恢复只操作原条目。
- 网络断开或未知结果保留原 key，用户可点击“使用原请求重试（同意条款）”。刷新只恢复意图，不发请求、不暗中接受条款；明确点击后才重放。名称锁定，不能换 payload。
- sessionStorage getter/读取/写入/校验异常时阻止新建，不能悄悄退回内存并生成新 key。只写入成功且读回一致才发首个请求。
- 409 异载荷与 410 tombstone 均保留原 key 并提示核对，不自动重新创建。201 重放若返回非 ready 当前 DTO，只保留现状，不自动注册。异步响应应用前检查所属 key，旧意图响应不能覆盖新意图。
- 只有旧的 `unregistered` 条目提供明确同意的手动注册修复。`registering`、`starting` 或未知 phase 不提供注册；`failed` 只提供原 ID 的恢复。
- 已停止/失败的条目隐藏旧 IP，出口验证成功才显示已连接。
- 前端稳定 key 与后端持久幂等共同防重；线上两条记录与两次 POST 只能证明重复请求，不能据此断言是浏览器重发或 UI 再次提交。上轮本地 socket destroy 是独立的故障注入复现场景。

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
node --import ./scripts/test-api-loader.mjs --test --test-timeout=60000 src/lib/warp-create-intent.test.mjs src/lib/warp-create-api.test.mjs src/lib/llm-proxy-api.test.mjs src/lib/proxy-center.test.mjs src/lib/proxy-warp-register.test.mjs src/lib/llm-models.test.mjs src/lib/llm-selection.test.mjs
npx tsc --noEmit --incremental false
$env:NEXT_EXPORT='1'
$env:NEXT_PUBLIC_MOCK='0'
npx next build --webpack
```

载荷测试调用生产 API 客户端并拦截 fetch，只证明请求载荷；不表示真实 HTTP 联调。真实本地 UI→Go API→PG、HTTP 合成上游出站及 DTO 展示证据保存在忽略提交的 `web/output/verification/`，详细结果、已知失败边界和冻结 SHA256 见该目录的交付报告与清单。JWT、初始化密码和运行密钥不写入证据。

恢复前端时可按冻结清单回退本轮文件；不得覆盖并行后端 owner 的改动。此文档不代表生产部署或真实外网 WARP 注册验收。
