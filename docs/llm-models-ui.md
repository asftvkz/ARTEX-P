# LLM 渠道多模型 · 前端实现说明

本文档对应 `docs/llm-models-contract.md` 的前端落地范围（仅 `web/**`）。后端契约见该文件；本文件只记录前端做了什么、为什么这么做，以及哪些点已被真实后端验证、哪些还没有。

> 本文档位于 `.gitignore` 忽略的 `/docs/` 下，`git status` 不会提示它的改动——提交时需显式 `git add -f` 或确认不包含。

## 已实现

### 1. 渠道下的模型成员管理（`system/llm`，渠道编辑抽屉）

- 新建渠道和编辑已有渠道共用同一套紧凑行 UI：「拉取模型」从 `POST /api/llm/models` 拉取上游模型列表，弹层逐项勾选、「添加所选」批量追加。
- 模型成员一行一个、紧凑布局：左 `upstream_model_id`（等宽、只读——改上游目标必须新建成员）；右为可编辑展示名输入框，默认等于上游 ID；最左为单选（`RadioGroup`）标记该渠道默认模型。
- 保存走 `GET/PUT /api/llm/profiles/{id}/models`。
- 新建渠道时序：先 `POST /api/llm/profiles`（`model` 送草稿默认行上游 ID，后端同事务种出首个成员）→ 拿 id → 立即 `PUT /api/llm/profiles/{id}/models` 写入完整草稿。省略 `id` 的条目按 `upstream_model_id` 复用已种成员，不需要额外 GET。
- **第二步失败的安全处理**：渠道 id 记入 `createdProfileId`，抽屉不关闭、不提示"已保存"；重试时普通字段走更新（带 id），绝不重复创建渠道（有专门行为测试）。
- **编辑已有渠道不提前改默认**：普通渠道设置的 PUT 仍送 `model` 字段（兼容端点签名），但服务端已忽略它、不据此改 `default_model_id`；真正切换默认只在 `PUT /models` 事务内完成。
- 渠道代理帮助文案已修正：不再写"留空 = 直连"，改为说明留空时不在此处直连——任务「指定代理」/「明确直连」以任务为准，「继承」时走任务创建时保存的出口快照；并注明对话等其它入口不经过任务快照。
- API Key 显式更换/取消保护完整保留（`autoComplete="new-password"`、`data-1p-ignore`、`data-lpignore`、`data-form-type="other"` 原样未动）。

### 2. 全局轮询池的渠道模型选择（`PoolSheet`）

- 渠道资格规则不变（已填 Key 且未 `pool_exclude` 按 priority 自动入池）；新增每渠道可选「跟随渠道默认」或「固定某成员」，走 `PUT /api/llm/pool/routes/{profile_id}`。
- 健康状态身份用 `member_key`（`"{profile_id}:{model_id}"`），不再是 `profile_id`。
- 「立即恢复」按钮改为**精确恢复**：单成员发 `POST /api/llm/pool/reset {member_key:"7:42"}`；不带参发 `{id:0}` 恢复全部。按渠道恢复的旧 `{id:7}` 语义仍保留（`poolResetBody` 有测试）。

### 3. 运行选择 UI（任务 / 对话 / Agent 绑定）

共用组件 `web/src/components/llm-selection.tsx`：`ModelSelect`（单行紧凑下拉）与 `ChannelModelChain`（有序候选 + 当前生效项 + 增删排序），三处复用同一套「渠道 + `channel-default` / `fixed-model`」语义。

- **任务创建** `POST /api/tasks`：同层发 `members`（**始终显式发送**，空数组 = 继承 Agent 绑定／全局）。第一项即生效项。
- **任务详情** `PUT /api/tasks/{id}/llm`：发 `members` + `active_position`（零起始下标）。
- **对话** `POST /api/conversations` 创建时同层带三元组；更新走 `PATCH /api/conversations/{id}/profile`，选渠道发 `{profile_id, model_mode, model_id}`，清除覆盖发 `{profile_id: null}`。
- **Agent 绑定** `PUT /api/agents/{key}/config`：新三元组与旧 `llm_profile_id` 同时下发，新字段优先；解绑发 `profile_id: null`；换渠道时把模型选择重置为"跟随该渠道默认"，避免把上一个渠道的固定成员 ID 带到新渠道。

#### 3.1 命名：展示名是主文案，身份永远是 ID

产品要求 `display_name` 就是系统展示名称——两个渠道的不同上游 ID 可以都展示为 `deepseek-chat`。因此：

- 模型选择下拉项、收起态触发器、任务详情「实际生效」行、对话顶部覆盖文案、池路由下拉，**均以展示名为主文案**（`displayLabel` / `modelOptionLabel` / `formatMember` / `selectionModeLabel`）。
- 上游 ID 与成员键作为**身份补充**显示（如 `deepseek-chat（上游 synth-model-2）`、`… · 1:3`），这样同名不同上游的两个成员仍可区分。
- **同名绝不合并**：等价判断、去重、路由、回退一律只认 `model_id` / `member_key`，绝不按名字判等或据此判断"是否切换"。
- 模型管理编辑行保持用户要求的「左 upstream ID、右 display name」，未改。

#### 3.2 未知模式与坏 DTO：拒绝而不是降级

模式与成员来自运行时数据（DTO、草稿、持久化），按**不可信输入**处理：

- `parseMode`：只有**缺省**（`undefined` / `null` / `""`）才按 `channel-default` 兼容解释（历史只传渠道的请求即此语义）。给了其它值一律返回 `null`，记入 `dropped` 原因 `unknown-mode`，**绝不静默降级成渠道默认**——那会把"固定某成员"悄悄变成"跟随渠道默认"。
- `chainValidationError` 覆盖四类无效行（缺渠道、固定模式缺成员、未知模式、完全重复），任一条命中即阻止保存并给出可操作提示。
- **读取路径同样不吞坏行**：`selectionsFromTaskWithReport` 在从 DTO 读取时也报告被丢弃的行（含 `source` / `membersField` 标记）。原因：净化后的链可能正好变空，而"空链"在契约里表示继承——若不报告，坏数据会被当成正常继承写回。任务详情编辑器据此禁用保存按钮并显示原因。
- `members` 字段**存在即权威**（含显式空数组），只有字段缺省才回退旧 `llm_profile_ids` / `llm_profile_id`。避免"恢复继承"被历史链顶回去。

#### 3.3 配置态与实际生效分开显示

`resolveActivePosition` 在链耗尽（后端 `active_position=null`）或下标越界时返回 `null`，**不回落第一项**；`activeSelection` 随之返回 `null`，界面显示"当前无生效成员"。仅编辑器草稿位置（`initialActivePosition`）为让下拉可用才回落到 0，两者严格分开，绝不把草稿位置说成"当前生效"。

### 4. WARP 注册条款确认（代理中心）

- 只有「注册」需要先确认条款；`start/stop/recover` 不涉及，不带请求体。
- 点「注册」弹确认框：条款说明列表（文案取自 `docs/proxy-runtime.md`，**不编造外部链接**）+ **默认不勾选**的同意勾选框 + 「同意并注册」按钮。
- 取消不发请求；确认后才发 `{accept_terms:true}`；接口失败保留确认框与勾选状态可重试（有 7 项行为测试）。
- `accept_terms` 语义与后端一致：缺失/false 一律拒绝（mock 里也照此规则，避免前端漏传却"看起来通过"）。
- **联调范围限制**：本次真实联调**未实际点击注册**（不在沙箱里对外发起 WARP 注册）。验证限于「未确认/取消不发请求、请求体形状固定为 `{accept_terms:true}`」的纯逻辑与 UI 路径。

### 5. 模型成员按需缓存

`needModels` 的 `pending` 存的是 **Promise 而非布尔**：同一渠道并发打开下拉时共享同一在途请求，不会在请求完成前返回空数组（否则界面会把"在途"误判成"这个渠道没有模型"）。`force=true` 用于已知成员集合变化时刷新；失败不缓存、可重试。

## 测试与验证

### 已实际执行

- `node --test --test-timeout=60000 src/lib/llm-selection.test.mjs src/lib/llm-models.test.mjs src/lib/proxy-center.test.mjs src/lib/proxy-warp-register.test.mjs`：**48 项全过，0 失败 0 跳过**（llm-selection 22、llm-models 10、proxy-center 9、proxy-warp-register 7）。覆盖：草稿 ↔ API 形状转换、API Key 显式更换保护、两段保存失败重试防重复建渠道、选择三元组归一/去重/旧字段兼容、**未知模式拒绝**、**坏 DTO 行阻止保存**、`members` 存在即权威（含空数组）、耗尽态不冒充第一项、展示名主文案与同名不同上游可区分、`member_key` 精确 reset、`needModels` 并发共享在途请求、WARP 注册未确认不发请求。
- `npm exec -- tsc --noEmit`：通过，无类型错误。
- `npm exec -- biome check`（本 scope 文件）：干净。残留的风格类提示（`useSortedClasses` / `noNestedTernary` / `useTemplate` / `noEmptyBlockStatements` 等）均为改动前既有，未新增。
- 非 Mock 生产静态构建（PowerShell）：
  ```
  $env:NEXT_EXPORT='1'; $env:NEXT_PUBLIC_MOCK='0'; npm exec -- next build --webpack
  ```
  **成功**，全部路由 `○ Static` prerendered（含 `/system/proxies`、`/system/llm`、`/chat`、`/system/agents`），产物 `web/out`。
  构建后核验 `MOCK` 被内联为 `"0"===e`（即 false），mock 处理器成为不可达死代码。
- 注：`npm run build:static` 脚本内嵌 `NEXT_EXPORT=1 cmd` 是 POSIX 写法，在 Windows 的 cmd.exe 下会报 `'NEXT_EXPORT' is not recognized`（与本次改动无关，未动 package.json）；用上面 PowerShell 显式设环境变量绕开。

### 真实后端联调（已执行）

后端：`go run ./cmd/artex -addr 127.0.0.1:18787 -data <独立临时目录> -proxy ''`，`ARTEX_PG_DSN` 指向**本机独立库** `artex_web_fe_verify_20261012`（未使用生产 DSN，未复用后端测试库 `artex_models*` / `artex_proxy*`）。前端走 `next dev -p 4173`，`AUTOPENTEST_API=http://127.0.0.1:18787`，**未设** `NEXT_PUBLIC_MOCK`（即真实 API，非 mock）。数据为合成渠道/成员/任务，临时管理员密码随机生成、经回环助手直接注入浏览器，未进入命令输出或对话文本，验证后已删除。

浏览器（真实 UI）覆盖：

| 路径 | 结果 |
|---|---|
| 登录 → 进入 `/system/llm` | 读到真实库新建渠道；卡片显示 `synth-model-2`（默认成员上游 ID，legacy 字段同步） |
| 编辑抽屉打开 | 左 upstream ID（`synth-model-1/2/3`）× 右展示名输入框；默认单选正确落在成员 3（含此前 UI 保存结果，确认持久化） |
| 改展示名 + 切默认 → 保存 | 真实 `PUT /api/llm/profiles/1/models`，body 中 id 复用、上游 ID 未变、`display_name` 与 `is_default` 按编辑发送 |
| 任务详情 → 打开 LLM 面板 | 显示「跟随默认配置」（即先前 `members:[]` 写入的继承态，未被旧链顶回） |
| 加渠道 + 选固定成员 | 下拉项为 `deepseek-chat（上游 synth-model-2）`；保存发出真实 `PUT /api/tasks/1/llm`，body `{"members":[{"profile_id":"1","model_mode":"fixed-model","model_id":"3"}],"active_position":0}` |
| 后端回读 `GET /api/tasks/1` | `members` / `active_position` / `active_member{model_id:"3", upstream_model_id:"synth-model-2", display_name:"deepseek-chat", member_key:"1:3"}` 与 UI 提交一致 |

另以 HTTP 直连真实验证（非 UI，同库）：模型 upsert 幂等（同上游无 id 复用）、legacy `model` 同步为默认成员上游 ID、JWT 401、对话创建带三元组 / `PATCH` 切换 / `{profile_id:null}` 清除、Agent 固定绑定 / 仅模型字段改当前绑定 / DTO 三字段回读、池 `GET/PUT routes`、池成员 `member_key` 与 `POST /api/llm/pool/reset {member_key:"1:4"}`、任务侧 `members:[]` 继承不回退旧链、无效固定 / 跨渠道成员 / 完全重复均 400。

## 未验证 / 限制（不得当作已生效）

- **WARP 真实注册未执行**：确认框路径与请求体形状已验证，但未真正向 Cloudflare 发起注册（避免在验证环境对外产生副作用）。真实注册的端到端结果未验证。
- **真实 LLM 出站未验证**：合成渠道指向不可达地址，未验证"展示名仅用于显示、实际请求发送 `upstream_model_id`"的真机行为——这一点由契约与后端实现保证，前端已确保不发送展示名。
- **渠道代理文案与任务 inherit 快照的联动未实机验证**：文案已按契约修正，但"任务 inherit → 渠道留空 → 走任务创建时快照"的实际出站路径未在本次联调中观测。
- **其他未跑的浏览器路径**：对话顶部 `LLMProfileRow` 的三元组切换、Agent 编辑器模型下拉的实际点击保存、全局池「轮询配置」抽屉的固定/跟随切换与单成员「立即恢复」，本次以纯逻辑测试 + HTTP 直连验证覆盖，未逐一点击 UI。
- 静态构建产物 `web/out` 已就绪，但**未部署**、未在 nginx 下实测。
- mock 数据仅用于演示与本地界面验证，**不能作为真实接口连通的证据**（本次真实结论均来自上文非 mock 联调）。
