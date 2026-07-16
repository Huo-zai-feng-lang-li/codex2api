# 账号状态自动分类与恢复探测实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让账号状态始终由真实上游响应驱动：真实请求成功自动恢复正常；无客户端流量的限流账号在冷却到期后由后台低成本探针验证。

**Architecture:** 保持“展示分类”和“调度可用性”解耦。客户端真实成功响应负责清除过期限流状态；后台复用现有 `ProbeUsageSnapshot`，优先调用零 Token 的 wham 用量接口，必要时才回退最小 Responses 探针。正常账号不进入恢复探测，限流、额度受限、封禁和错误账号按现有后台刷新循环及状态冷却时间受控探测。

**Tech Stack:** Go、现有 `auth.Store` 调度状态机、Admin 用量探针、SQLite/PostgreSQL 状态持久化。

## 任务

- [x] 在 `auth/runtime_status_test.go` 增加真实客户端请求成功后从 `rate_limited` 恢复为 `active` 的失败测试。
- [x] 在 `auth/runtime_status_test.go` 增加恢复探测候选测试：正常账号不探测、冷却中的账号不探测、冷却到期的 `rate_limited/payment_required` 账号可探测。
- [x] 在 `proxy/handler_test.go` 增加 402 额度受限长退避测试，确保无明确重置时间时不会每 30 分钟消耗探针。
- [x] 修改 `auth/store.go`：真实请求成功时清除旧冷却/错误状态，并仅在状态发生恢复时异步持久化，避免污染正常请求热路径。
- [x] 修改 `auth/store.go`：将到期的 `rate_limited/payment_required` 纳入恢复探测候选；保持正常账号和仍在冷却期的账号不探测。
- [x] 修改 `proxy/handler.go`：402/普通额度受限默认冷却调整为 6 小时；429 继续尊重现有 `Retry-After/reset_at` 精确时间。
- [x] 运行定向测试、`go test ./... -count=1`、`go vet ./...`、`git diff --check`。
- [x] 构建 `codex2api.new.exe`，确认 `/health.responses_memory.inflight_requests=0` 后优雅关闭旧服务，备份并替换 `codex2api.exe`。
- [x] 使用 WMI 脱离终端启动新服务，验证新 PID、EXE SHA256、`/health.status=ok`、续链持久化状态和账号统计接口。

## 验收标准

- 客户端真实请求成功后，旧的 `rate_limited/payment_required` 状态自动清除并进入正常分类。
- 没有客户端流量时，正常账号不探测；限流账号只在冷却到期后进入低并发恢复探测。
- 402 额度受限默认至少退避 6 小时；429 继续按上游重置时间恢复。
- 热替换不丢失当前 Responses 续链，服务恢复后健康检查通过。

## 实施结果

- 定向回归、`go test ./... -count=1`、`go vet ./...`、`git diff --check` 均通过。
- 旧 PID `23500` 已通过管理 API 优雅关闭；替换前确认在途 Responses 请求为 `0`。
- 首次 WMI 启动的 PID `28704` 在 `20:24:22` 收到外部 `signal:terminated` 后优雅退出，并非程序 panic；用户随后手动启动同一新 EXE，当前 PID `4548`。
- 当前 EXE SHA256 `55424EEBDE86F374BA5433AC6FC6A21CC02961E9C31EC12BD4CC43EB966B38C5`。
- 旧 EXE 备份：`codex2api.prev-20260716-202207.exe`。
- 当前 `/health` 返回 `status=ok`、`available=4`、`total=28`、`continuity_persistent=true`；续链磁盘容量清理曾超时一次，`continuity_persistence_failures=1`，请求仍正常完成。
- 管理接口返回 28 个账号：`active=4`、`payment_required=21`、`unauthorized=3`；后台刷新间隔 2 分钟，恢复探测最小间隔 30 分钟。
