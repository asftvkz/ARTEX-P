# 已有任务切换代理

前端基线 `9e32af7` / v2.2.4。任务详情顶栏“切换任务代理”打开现有风格的 Dialog；仅编辑任务代理，不改变 WARP 生命周期或模型渠道页面。

## 交互与状态

- 当前任务快照单独展示，表单回填原模式。inherit 保留原快照编号作为说明；当前默认与原快照不同时明确显示差异，只有保存才重新取当前默认。
- 三模式：继承当前全局默认、明确直连、指定代理。代理中心按名称/类型/编号/真实出口 IP 搜索；仅 enabled 且 healthy 可选，WARP 还要求 ready 与出口 IP。未知、停用、失败、缺失条目不回落直连；原无效选择保留编号并显示原因。
- 目录读取失败阻止保存，包括 direct；用户可重试目录，草稿不丢失。普通代理缺少出口 IP 显示未验证，不推测地址。
- 运行/排队需先暂停，已知在途操作需等待。暂停或 terminal 都不能证明真实工具退出，409 才是服务端权威判断；前端不自动取消工具、暂停或恢复任务。
- 取消只关闭弹窗，不发送 PUT。保存以同步锁防双击，失败保持选择并显示错误。409 不自动重试；500/网络未知提示可能执行封锁，先回读核对，不自动重复 PUT。
- PUT 返回后仍 GET Task（cache:no-store），徽标与当前快照只采用回读值。回读失败显示状态待确认，再试只 GET。详情页保存前开始的迟到轮询不得覆盖保存后的权威回读。
- 弹窗打开时固定 TaskDTO.proxy_revision 作为本次意图的 expected_revision，不随后台轮询更新。版本缺失/null/非法时禁止保存，不能当作0。旧任务必须由服务端明确返回0。
- revision冲突与网络/500未知结果先GET核对，不把新版本自动写入旧意图。核对后保留草稿，用户必须点击“确认以当前任务状态重新选择”才能启用下一次保存；关闭再打开也不能绕过确认。确认按钮本身不发PUT。

## HTTP

`PUT /api/tasks/{id}/proxy`：

```json
{"proxy_mode":"fixed","proxy_id":4,"expected_revision":0}
```

inherit/direct 显式发送 null，fixed 正整数；expected_revision 必填非负安全整数，不发送代理 URL、凭据或本地拼装快照。200 完整 TaskDTO，proxy_revision递增1，随后 GET `/api/tasks/{id}`；400 无效/不可用，404 无任务，409 生命周期或在途屏障拒绝，500 持久异常。

版本冲突409：`{error,code:"proxy_revision_conflict",current_revision:整数}`；在途409：`{error,code:"proxy_busy"}`。其他409 code按通用冲突展示；不能仅凭current_revision重新PUT，必须先GET核对并由用户明确确认新意图。

## 验证

```powershell
node --import ./scripts/test-api-loader.mjs --test --test-timeout=60000 src/lib/task-proxy.test.mjs src/lib/task-proxy-api.test.mjs src/lib/proxy-center.test.mjs src/lib/proxy-warp-register.test.mjs
npx tsc --noEmit --incremental false
$env:NEXT_EXPORT='1'
$env:NEXT_PUBLIC_MOCK='0'
npx next build --webpack
```

API 客户端测试拦截 fetch，仅证明载荷与状态处理。`scripts/verify-task-proxy-server.mjs` 配合 `verify-task-proxy-browser.mjs` / `verify-task-proxy-boundaries.mjs` / `verify-task-proxy-stale-poll.mjs` / `verify-task-proxy-cas.mjs` 提供真实 Chromium 与本机合成 HTTP 验收，不能当作真实 Go/PG 证据。

证据输出在忽略提交目录 `output/verification/task-proxy-switch/`；生产部署由主脑统一处理。
