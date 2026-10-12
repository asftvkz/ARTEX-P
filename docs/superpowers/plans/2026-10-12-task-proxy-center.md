# ARTEX 任务代理中心 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为 ARTEX 增加统一代理中心、轻量 WARP、Resin 外部代理和任务级并发安全代理路由。

**Architecture:** 在数据库中增加统一代理实体与任务代理选择字段；服务端在创建任务时解析并固定代理快照，向 Worker、Planner、MainAgent、浏览器 MCP 和 LLM Transport 注入同一出口上下文。普通代理直接使用 URL，WARP 由本地 wireproxy 提供 SOCKS5，Resin 通过现有外部服务的数据面和控制面协议提供粘性出口；录制 MITM 使用任务身份选择上游，禁止切换共享全局上游。

**Tech Stack:** Go 1.26、PostgreSQL schema migration、go-mitmproxy、现有 norma agent 工具链、Next.js/React/TypeScript、wgcf/wireproxy 子进程。

**Spec:** `docs/superpowers/specs/2026-10-12-task-proxy-center-design.md`

## Global Constraints

- 任务代理覆盖任务工具与任务 LLM API；系统自身出站保持独立。
- 支持 `inherit`、`direct`、`fixed` 三种任务代理模式。
- 指定代理不可用时失败关闭，不静默直连。
- 普通代理、WARP、Resin 使用同一代理列表和稳定编号。
- Resin 不由 ARTEX 部署；Token、密码、Admin Token 不得回显。
- 不通过共享 `SetUpstreamProxy` 切换并发任务出口。

---

### Task 1: 数据模型、迁移与代理解析器

**Files:**
- Create: `db/proxies.go`, `db/proxies_test.go`
- Modify: `db/schema.sql`, `db/tasks.go`, `db/task_metadata.go`, `db/db.go`
- Create: `server/task_proxy.go`, `server/task_proxy_test.go`

- [ ] **Step 1: Write failing tests** for proxy CRUD, task mode validation, snapshot resolution, credential redaction, legacy `global_proxy` migration, and fixed/direct/inherit priority.
- [ ] **Step 2: Run the focused Go tests and confirm the missing model/parser failures.**
- [ ] **Step 3: Add schema and stores** for proxy records, task selection fields, immutable task proxy snapshots, and active-task reference checks.
- [ ] **Step 4: Implement resolver interfaces** returning a task-scoped `ProxyContext` with sanitized environment values and LLM transport URL.
- [ ] **Step 5: Run focused tests, then commit the database and resolver slice.**

### Task 2: Resin adapter and WARP lifecycle

**Files:**
- Create: `server/proxy_resin.go`, `server/proxy_resin_test.go`
- Create: `server/proxy_warp.go`, `server/proxy_warp_test.go`
- Modify: `server/manager.go`, `Dockerfile`, `docker-compose.yml`, `db/schema.sql`

- [ ] **Step 1: Write failing adapter tests** for Resin health/auth/platform lookup, stable task Account key, inherit-lease, idempotent lease release, and redacted errors.
- [ ] **Step 2: Write failing lifecycle tests** for WARP start, readiness, stop, restart recovery and occupied-proxy deletion guards.
- [ ] **Step 3: Implement Resin as an external adapter** using data-plane proxy credentials and control-plane Admin URL/token without logging secrets.
- [ ] **Step 4: Implement WARP profiles** as local wireproxy SOCKS5 processes with persisted materials and bounded readiness probes.
- [ ] **Step 5: Run focused adapter/lifecycle tests and commit.**

### Task 3: Task and agent route propagation

**Files:**
- Modify: `server/server.go`, `server/manager.go`, `server/orchestration.go`, `server/task_llm.go`
- Modify: `agent/worker.go`, `agent/planner.go`, `agent/mainagent.go`, `agent/chat.go`, `agent/webfetch` integration
- Modify: `traffic/traffic.go`, `server/customtool.go`
- Create: `server/task_proxy_routing_test.go`, `traffic/task_upstream_test.go`

- [ ] **Step 1: Write failing concurrency tests** proving two tasks retain different proxy contexts while the shared MITM remains unchanged.
- [ ] **Step 2: Implement task-scoped route propagation** for Bash, WebFetch, browser MCP, custom HTTP tools and LLM Transport.
- [ ] **Step 3: Add request identity routing** at the MITM boundary; reject missing or invalid task identities instead of falling back to direct.
- [ ] **Step 4: Remove WebFetch error-path direct retry and inherited `NO_PROXY` leakage for fixed task proxies.**
- [ ] **Step 5: Run route and concurrency tests and commit.**

### Task 4: APIs and frontend proxy center

**Files:**
- Modify: `server/server.go`, `server/dto.go`, `web/src/lib/api.ts`, `web/src/lib/types.ts`
- Create: `web/src/app/(main)/system/proxies/page.tsx`
- Modify: `web/src/app/(main)/function/tasks/page.tsx`, `web/src/navigation/sidebar/sidebar-items.ts`
- Create: `web/src/app/(main)/system/proxies/proxy-center.test.tsx`, `web/src/app/(main)/function/tasks/task-proxy-selection.test.tsx`

- [ ] **Step 1: Write failing API/UI tests** for unified listing, masked credentials, default selection and task mode controls.
- [ ] **Step 2: Implement authenticated proxy CRUD, health/test endpoints and global-default endpoints.**
- [ ] **Step 3: Add the proxy center page** with ordinary/WARP/Resin forms and stable `#编号` rows.
- [ ] **Step 4: Add task creation selection** for inherit, direct and fixed proxy, including disabled/unhealthy states.
- [ ] **Step 5: Run frontend tests and commit.**

### Task 5: Documentation, migration and release verification

**Files:**
- Modify: `README.md`, `.env.example`, `docs/README.md`
- Create: `docs/proxy-center.md`, `docs/resin-integration.md`, `docs/wgcf-wireproxy.md`

- [ ] **Step 1: Document configuration, credential handling, task precedence and unsupported raw-network tools.**
- [ ] **Step 2: Add migration notes for existing `global_proxy` and rollback behavior.**
- [ ] **Step 3: Run Go tests, frontend tests, build, config validation and a local two-task proxy smoke test.**
- [ ] **Step 4: Review the complete diff, check for secret leakage, and prepare a PR with validation evidence.**
