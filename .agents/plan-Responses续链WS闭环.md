# Responses 续链 WS 闭环实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 保证 Responses 的同一请求在首字前可重试时保持原账号，而本地已失效的派发凭据可安全切换，并且 WS 到 HTTP 的续链降级只使用完整历史。

**Architecture:** 选择键在单个 Responses 请求内始终绑定到本次解析出的会话键；严格亲和账号忽略临时排除，而解绑后必须尊重排除列表。失效凭据属于未实际发起上游请求的本地派发失败，直接删除该账号的亲和绑定后才允许切换。HTTP/WS 共用选择与解绑逻辑。

**Tech Stack:** Go, Gin, Gorilla WebSocket, Go testing.

## Global Constraints

- HTTP `handler.go` 与 WS `responses_ws.go` 调度语义对称。
- 仅完整续链注册表可移除 `previous_response_id`；未知历史返回 503 且不得请求 HTTP 上游。
- 原账号 Cooling 或普通传输失败时保持亲和；401、403、Disabled、删除或本地凭据缺失才允许解绑。
- 修改后执行定向测试、`go test ./... -count=1`、`go vet ./...`、`git diff --check`。

---

### Task 1: 锁定调度与续链回归

**Files:**
- Modify: `auth/session_affinity_test.go`
- Modify: `proxy/handler_test.go`

- [x] 写出并复现 WS 首字前重试必须复用原账号的失败用例。
- [x] 写出并复现 WS 到 HTTP 降级缺少完整注册历史必须拒绝的失败用例。
- [ ] 补充严格亲和解绑后必须尊重本次排除列表的测试。
- [x] 将 HTTP fallback 成功用例改为注册完整续链历史，而非旧的局部工具缓存。

### Task 2: 最小调度修复

**Files:**
- Modify: `auth/store.go`
- Modify: `proxy/retry_exclusions.go`
- Modify: `proxy/handler.go`
- Modify: `proxy/responses_ws.go`

**Interfaces:**
- Produce: `NextForStrictSessionExcludingWithFilter(key string, apiKeyID int64, exclude map[int64]bool, filter AccountFilter) (*Account, string)`
- Produce: `WaitForStrictSessionAvailableExcludingWithFilter(ctx context.Context, key string, timeout time.Duration, apiKeyID int64, exclude map[int64]bool, filter AccountFilter) (*Account, string)`

- [ ] 严格亲和存在时继续选原账号；无绑定时向普通选号传递排除列表。
- [ ] `nextRetryAccountPickForSession` 使用排除感知的严格选择与等待。
- [x] HTTP/WS 在 `ErrNoAvailableAccount` 时直接删除该账号当前选择键的绑定，再重试其他账号。
- [ ] 新请求也以本次会话键维持请求内重试，续链历史降级后清空选择键以允许安全切号。

### Task 3: 验证与归档

**Files:**
- Modify: `.agent/handoff.md`
- Modify: `.agent/plan-Responses续链WS闭环.md`

- [x] 运行定向 auth、HTTP、WS 与未知历史降级测试。
- [ ] 运行 `go test ./proxy -count=1`、`go test ./... -count=1`、`go vet ./...`、`git diff --check`。
- [x] 修复 `rate_limited` / `payment_required` 严格绑定仍死守额度耗尽账号的问题；按用户要求暂不运行新增回归。

### Task 4: 续链持久化容量治理

- [x] 审计重启恢复、TTL、容量上限和并发会话头更新。
- [x] 现场确认 `data/codex2api.db` 已启用续链持久化：165 个节点、78 个会话头。
- [x] 确认治理设计：不可回放节点只保留路由元数据；滑动 TTL 可配置且默认延长；容量清理必须同步修复会话头；历史不完整继续显式失败，禁止静默续接。
- [x] 非 replayable 完成节点不再持久化大体积 input/output；Trim 会事务化瘦身历史遗留 payload，避免无效数据挤占 64 MiB 配额。
- [x] Prune/Trim 删除节点时仅修复受损的 `responses_continuity_heads`，并以 operation sequence 条件删除防止并发新 head 被误删。
- [x] 滑动 TTL 改为 `CODEX_RESPONSES_CONTINUITY_TTL_HOURS` 可配置，安全默认值延长为 24 小时。
- [x] 补充 TTL、容量淘汰、遗留 payload 瘦身和悬空 head 回归测试。
- [x] 修复严格会话无候选账号时的 30 秒空等，复用恢复探针并快速返回。
- [x] 新服务已运行并复验 `/health`：`inflight_requests=0`、`continuity_persistent=true`、`continuity_persistence_failures=0`。
- [x] 已备份并压缩 `data/codex2api.db`，不可回放终态 payload 归零，悬空 head 归零。
- [ ] 可选真实账号验收：A -> B 两连包与数据库流水仍依赖可用测试 Key。
