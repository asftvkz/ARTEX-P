# LLM 渠道多模型契约

## 目标与术语

既有的 `llm_profiles` 是一个 LLM 渠道（下文称“渠道”）。一个渠道可配置多个
模型成员；每个成员拥有稳定内部 ID。界面一行只表示一个模型：左侧显示不可编辑的
上游模型 ID，右侧编辑展示名称。

- `upstream_model_id`：实际发送给上游的模型 ID。
- `display_name`：仅供界面显示和人工识别，永远不参与等价判断、路由或回退。
- `model_id`：`llm_models.id`，跨 API 传递时使用字符串，避免 JavaScript 整数精度问题。
- `channel-default`：运行时读取当前渠道默认成员；该默认成员变化后，下一次解析随之变化。
- `fixed-model`：运行时只使用指定的模型成员 ID；渠道默认成员变化不会影响它。

两个渠道中的成员即使都显示为 `deepseek-chat`，也不是同一个模型成员。只有同时指定
`profile_id`（渠道）与 `model_id`（成员）才构成一个可运行的路由目标。

## 持久化模型

```sql
CREATE TABLE llm_models (
    id                BIGSERIAL PRIMARY KEY,
    profile_id        BIGINT NOT NULL REFERENCES llm_profiles(id) ON DELETE CASCADE,
    upstream_model_id TEXT NOT NULL,
    display_name      TEXT NOT NULL,
    enabled           BOOLEAN NOT NULL DEFAULT true,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (profile_id, upstream_model_id),
    UNIQUE (profile_id, id)
);

ALTER TABLE llm_profiles
    ADD COLUMN IF NOT EXISTS default_model_id BIGINT
    REFERENCES llm_models(id) ON DELETE SET NULL;

CREATE TABLE llm_pool_model_routes (
    profile_id BIGINT PRIMARY KEY REFERENCES llm_profiles(id) ON DELETE CASCADE,
    model_mode TEXT NOT NULL CHECK (model_mode IN ('channel-default','fixed-model')),
    model_id    BIGINT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (profile_id, model_id) REFERENCES llm_models(profile_id, id),
    CHECK (
        (model_mode = 'channel-default' AND model_id IS NULL) OR
        (model_mode = 'fixed-model' AND model_id IS NOT NULL)
    )
);
```

数据库无法仅用外键保证 `default_model_id` 属于同一渠道，因此所有写入都必须在一个
事务内锁定渠道行、校验成员归属，并拒绝跨渠道成员 ID。默认成员必须存在且已启用。
一个渠道至少保留一个成员。

旧的 `llm_profiles.model` 继续作为兼容执行器的物化字段，值始终等于默认成员的
`upstream_model_id`，绝不能写入 `display_name`。

`llm_pool_model_routes` 保存全局轮询池对每个渠道的模型选择。历史渠道没有行时按
`channel-default` 解析，因此旧的单模型轮询行为保持不变；它不会把一个渠道的所有启用
模型自动展开成多个池成员。

## 迁移规则

启动迁移对每一个历史渠道执行以下幂等规则：

1. 以历史 `llm_profiles.model` 创建一个启用的模型成员，两个名称均为该值。
2. 若该渠道没有 `default_model_id`，将其指向上述成员。
3. 不覆盖已有模型成员、展示名称或已选默认成员。

这样旧单模型渠道升级后仍向同一上游模型发请求，且默认模型行为不变。

## 管理 API

已有的渠道 CRUD 保持不变。模型成员使用下列资源；ID 全部序列化为字符串。

### `GET /api/llm/profiles/{profile_id}/models`

响应：

```json
{
  "profile_id": "7",
  "default_model_id": "42",
  "models": [
    {
      "id": "42",
      "profile_id": "7",
      "upstream_model_id": "deepseek-v3",
      "display_name": "DeepSeek Chat",
      "enabled": true,
      "is_default": true
    }
  ]
}
```

`is_default` 是 `default_model_id` 的派生字段，方便 UI 渲染；前端不得根据
`display_name` 合并、去重或推断成员等价。

### `PUT /api/llm/profiles/{profile_id}/models`

一次原子替换该渠道的已选成员和默认项。用于“获取模型列表”后勾选多项并批量保存。

```json
{
  "models": [
    {
      "id": "42",
      "upstream_model_id": "deepseek-v3",
      "display_name": "DeepSeek Chat",
      "enabled": true,
      "is_default": true
    },
    {
      "upstream_model_id": "deepseek-r1",
      "display_name": "deepseek-r1",
      "enabled": true,
      "is_default": false
    }
  ]
}
```

- 新成员省略 `id`，服务端分配稳定 ID。
- 省略 `id` 但同一渠道已存在相同 `upstream_model_id` 时，复用该成员 ID；创建渠道后的
  第二次保存无需先 GET 对齐 ID。重试同一批量请求也不会重复创建成员。
- `id` 存在时必须属于 URL 指定的渠道；不允许转移到其他渠道。
- 已有成员的 `upstream_model_id` 固定；添加新的上游 ID 应创建新成员，防止固定路由
  在保留内部 ID 的同时悄悄改变真实目标。
- `upstream_model_id`、`display_name` 去除首尾空白后均不能为空。
- 同一渠道内 `upstream_model_id` 唯一；`display_name` 可以重复。
- 恰好一个条目 `is_default=true`，且它必须 `enabled=true`。
- 不在请求中的旧成员会被移除。若已有固定路由引用它，服务端返回 `409`，不会静默改为
  同展示名称或同上游名称的另一个成员。
- 成功响应与 `GET` 相同，并同步 `llm_profiles.model` 为默认成员的
  `upstream_model_id`。

### `POST /api/llm/models`

沿用已有拉取接口。前端编辑已保存渠道时只提交：

```json
{"profile_id": 7}
```

服务端从已保存渠道读取 API Key，响应中不得回显该 Key。拉取结果勾选并保存到上述
`PUT` 接口时，初始值为：

```json
{
  "upstream_model_id": "<拉取到的模型ID>",
  "display_name": "<拉取到的模型ID>",
  "enabled": true,
  "is_default": false
}
```

普通渠道设置更新仍维持现有语义：请求省略或传空 API Key 时，服务端保留已保存 Key；
所有读取响应只可返回 `api_key_hint`。

### 渠道创建与普通设置保存

渠道创建继续使用现有 `POST /api/llm/profiles`，`model` 为草稿默认行的上游 ID。
创建渠道和创建该 ID 对应的单个默认成员在同一事务中完成。

随后前端直接 `PUT /api/llm/profiles/{id}/models` 保存完整多模型草稿。服务端按同渠道
相同上游 ID 复用已创建成员，所以不要求额外 GET。第二步失败时，前端保留第一步返回
的渠道 ID，重试模型保存或更新已有渠道，避免再次创建渠道。

编辑普通渠道字段时，`SaveProfile` 保留服务端已有 `default_model_id`，并以其上游 ID
维持 legacy `model`。请求体的 `model` 不再是更新默认模型的权威来源，不能提前应用尚未
保存的草稿默认模型。展示名称同样不会被普通渠道更新覆盖。真正切换默认模型只在
`PUT /models` 的事务内完成。

### 全局轮询池模型路由

全局池仍以“所有已填写 Key 且未设置 `pool_exclude` 的渠道自动参与”为渠道资格规则。
`priority` 与 `is_default` 的现有排序语义保持不变。模型路由只决定每个已入池渠道贡献
哪一个模型成员，所以每个渠道在全局池中恰好一项。

池候选不是任务侧 `members` 那种显式列表，前端无需配置渠道的加入、排序或排除；继续
使用既有渠道设置。前端只为每个渠道保存下列模型选择。

#### `GET /api/llm/pool/routes`

返回全部渠道的显式或兼容默认路由：

```json
{
  "routes": [
    {"profile_id": "7", "model_mode": "fixed-model", "model_id": "42"},
    {"profile_id": "8", "model_mode": "channel-default", "model_id": null}
  ]
}
```

#### `PUT /api/llm/pool/routes/{profile_id}`

固定一个成员：

```json
{"model_mode": "fixed-model", "model_id": "42"}
```

恢复为随渠道默认模型变化的选择：

```json
{"model_mode": "channel-default", "model_id": null}
```

固定成员必须属于 URL 的渠道且已启用。`channel-default` 需要该渠道当前有已启用默认成员。
保存后重建全局池；正在进行的调用仍使用它启动时已解析的成员。

当模型成员批量保存试图删除一个被固定池路由引用的成员时，服务端返回 `409`，不会改选
同展示名称、同上游 ID 或渠道默认成员。

#### `GET /api/llm/pool` 响应扩展

既有 `chain` / `LLMPoolMember` 项保留全部现有字段及其含义，并新增以下字段：

```json
{
  "profile_id": "7",
  "name": "渠道 A",
  "model": "deepseek-v3",
  "format": "openai",
  "priority": 10,
  "active": true,
  "excluded": false,
  "state": "ok",
  "fails": 0,
  "trips": 0,
  "cooldown_secs": 0,
  "model_mode": "fixed-model",
  "model_id": "42",
  "upstream_model_id": "deepseek-v3",
  "member_key": "7:42"
}
```

- 既有 `model` 保持为实际本次池成员使用的上游模型 ID，兼容旧前端。
- `model_mode` 是已保存选择；无历史路由行时返回 `channel-default`。
- `model_id` 是实际解析的成员 ID。即使保存的是 `channel-default`，响应也应返回当前解析到
  的默认成员 ID；渠道尚无可用默认模型时为 `null`，该渠道不产生可调用池成员。
- `upstream_model_id` 与 `model` 在当前版本值相同，单独保留该字段以避免前端将展示名称或
  将来的兼容字段误用于请求模型。
- `member_key` 固定为 `"{profile_id}:{model_id}"`。熔断、失败次数、冷却时间均以它为身份
  存储和展示。不得再仅以 `profile_id` 复用状态；`channel-default` 的默认成员变化后会得到
  新的 `member_key` 与独立状态。

`POST /api/llm/pool/reset` 同时接受 `{"member_key":"7:42"}` 精确恢复该成员；既有
`{"id":7}` 恢复该渠道全部成员状态，`{"id":0}` 或空对象恢复全部状态，维持旧客户端兼容。

## 运行选择契约

任务、对话、Agent 绑定及轮询成员都使用下列结构。历史只传 `profile_id` 的请求按
`channel-default` 解释，以保留既有行为。

```json
{
  "profile_id": "7",
  "model_mode": "channel-default",
  "model_id": null
}
```

```json
{
  "profile_id": "7",
  "model_mode": "fixed-model",
  "model_id": "42"
}
```

校验要求：

- `channel-default` 必须没有 `model_id`，解析时读取该渠道当前默认成员。
- `fixed-model` 必须有 `model_id`，且成员属于 `profile_id` 并已启用。
- 不允许仅传模型名称，也不允许通过 `display_name` 查找模型成员。
- 每次运行记录实际解析出的 `profile_id`、`model_id`、`upstream_model_id`，以便审计。

任务的轮询/故障转移候选为显式的渠道加模型选择列表，例如：

```json
{
  "members": [
    {"profile_id": "7", "model_mode": "fixed-model", "model_id": "42"},
    {"profile_id": "8", "model_mode": "channel-default", "model_id": null}
  ]
}
```

轮询只能在该列表中推进，不能把一个渠道的全部已启用模型自动扩展为候选项。任意一次
回退都以成员 ID 为准；显示名称相同不构成回退或替换依据。

全局轮询池先按现有 `is_default`、`priority`、`pool_exclude` 计算渠道顺序，再为每个渠道
解析一条池路由。最终成员身份是 `profile_id + model_id`，不能只使用渠道 ID、模型名称或
展示名称。

## 主脑接线清单

### 任务、对话及 Agent 的外层 API（阶段 2）

以下新增 ID 请求同时接受十进制字符串和整数，响应统一使用字符串；既有
`llm_profile_id`、`llm_profile_ids`、`active_llm_profile_id` 保留原来的整数类型。
`model_id` 仅在固定模式中保存；实际成员身份由运行解析字段表示。

#### 任务

`POST /api/tasks` 在现有创建字段同层加入 `members`：

```json
{"description":"任务描述","goal":"目标","members":[{"profile_id":"7","model_mode":"channel-default","model_id":null},{"profile_id":"7","model_mode":"fixed-model","model_id":"42"}]}
```

创建时第一项生效。`members:[]` 表示继承 Agent 绑定／全局配置；省略 `members`
时接受旧 `llm_profile_ids` 或单个 `llm_profile_id`，每项按渠道默认模式转换。
同渠道允许多个不同选择，完全相同的选择不得重复。不自动添加其他模型。

`PUT /api/tasks/{id}/llm` 原子替换并重置链：

```json
{"members":[{"profile_id":"7","model_mode":"fixed-model","model_id":"42"},{"profile_id":"8","model_mode":"channel-default","model_id":null}],"active_position":0}
```

`active_position` 是零起始候选下标，省略时选第一项；空列表恢复继承。
旧请求 `{"llm_profile_ids":[7,8],"active_llm_profile_id":8}` 继续有效。
响应保留旧字段及 `reopened_intents`，新增 `members`、`active_position` 和
`active_member`。任务列表／详情 DTO 同样返回这三个字段。

`members` 是配置态三元组列表；`active_member` 为当前实际解析结果或 `null`：

```json
{"profile_id":"7","model_mode":"fixed-model","model_id":"42","upstream_model_id":"deepseek-v3","display_name":"DeepSeek Chat","member_key":"7:42"}
```

默认模式的 `active_member.model_id` 同样为实际 ID。当前候选位置与实际身份分别显示，
不能以展示名判断是否切换。`GET /api/tasks/{id}/llm-resolution` 的角色结果保留旧字段，
新增 `model_mode/model_id/upstream_model_id/display_name/member_key`；
切换活动 `llm_transition.previous/next` 也包含实际模型 ID、上游 ID 和成员键。

#### 对话

`POST /api/conversations` 在 `agent_key/title` 同层加入
`profile_id/model_mode/model_id`。`PATCH /api/conversations/{id}/profile` 使用同一选择三元组。
例如固定选择：

```json
{"profile_id":"7","model_mode":"fixed-model","model_id":"42"}
```

`{"profile_id":null}` 清除对话覆盖，继承 Agent 绑定，随后回退全局。
更新请求不包含 `profile_id` 或旧 `llm_profile_id` 时保留当前覆盖；只更新
`model_mode/model_id` 可调整当前已绑定渠道。未绑定渠道时仅传模型字段返回 400。
旧 `llm_profile_id` 整数／null 仍分别表示渠道默认／清除；新增 `profile_id` 优先。
创建、列表及详情保留旧 `llm_profile_id`，新增配置态字符串 `profile_id`、
`model_mode`、`model_id`；默认模式 `model_id:null`。更新返回 `{"ok":true}`。

#### Agent

`PUT /api/agents/{key}/config` 在现有配置字段同层使用同一选择三元组；
`{"profile_id":null}` 清除绑定，跟随任务／对话覆盖，再回退全局。
选择字段全部省略则保留绑定；只传模型字段可修改当前渠道的选择。
旧 `llm_profile_id` 三态语义保持：省略不改，null 解绑，整数为渠道默认。
新增字段优先于旧字段。Agent DTO 新增配置态 `profile_id/model_mode/model_id`，
与对话同样使用字符串／null。配置更新仍返回 `{"ok":true}` 及既有相关配置字段。

所有固定成员必须属于该渠道且已启用；删除或禁用任务／对话／Agent／全局池固定引用
的成员返回 409。默认变化不会改变固定选择，迟到额度失败同时校验链版本和实际成员。

阶段 1 在 `db/llm_models.go` 完整封装幂等 DDL 与迁移，`db/db.go` 已获授权并在 schema
成功后、种子数据前调用 `MigrateLLMModels`。阶段 2 已完成以下接线：

1. `db/schema.sql`：当前无需重复新表 DDL；所有启动入口均通过 `db.Open` 中的模型迁移。
   若后续希望裸 SQL 初始化也支持模型功能，须同步复合唯一约束与池路由外键定义。
2. `db/db.go`：已接线，模型迁移使用独立事务锁 `7337741003`，避免重取 schema 锁自锁。
3. `server/server.go`：在既有 API mux 中调用 `s.RegisterLLMModelRoutes(mux)`，同时注册
   模型成员和全局池路由四个端点，沿用既有 JWT 鉴权。
4. `server/dto.go`：为渠道、任务和对话 DTO 增加默认成员及模型选择字段。
5. `server/manager.go`、`server/task_llm.go`：将任务成员从“仅渠道”升级为显式
   `profile_id + model_mode + model_id`，并在实际调用前解析为上游 ID。
6. `server/llmpool.go`：全局池保留既有渠道顺序，但每个渠道经池路由解析为一个成员；任务池
   只从任务声明的候选成员建立轮询；成员身份键改为渠道 ID 加模型成员 ID。
7. 对话和 Agent 绑定的更新处理：保存与解析同样使用 `model_mode` / `model_id`；
   历史空值按 `channel-default` 处理。

## 执行审计与出口生命周期

`llm_usage` 和 `llm_records` 每次保存实际调用的 `profile_id/model_id/member_key/upstream_model_id`；
旧 `model` 仍为上游 ID。历史记录缺少稳定 ID 时保留空值，不按名称猜测历史成员。
`NewManager` 初始化两张表及新增列；迁移失败关闭数据库并返回启动错误。
模型执行测试夹具同样调用真实初始化函数，记录功能保持开启并检查数据库内容。

普通对话未绑定渠道时，每次调用动态读取全局池路由和故障转移链；显式对话或 Agent
绑定使用绑定策略，只有 `llm_pool_bind_fallback` 开启时加入全局候选。

任务操作在数据库同一代理生命周期锁内登记活动引用，实际工具与 MCP 清理退出后解除。
暂停、终态和人工回收等待／检查真实操作屏障，网络回收不持任务生命周期锁。
正式操作凭证绑定任务和上下文，结束或取消后失效；终态 MainAgent 的新准入可以重新
取得已确认回收的租约。异常退出遗留活动引用保留保护，不在启动时清空。
编排子任务在创建事务内继承父任务已持久化代理模式和快照，后续全局代理变化不改写该快照。
