# 最新接续状态 (2026-08-02 00:28)

## 核心进展
- Bug 修复型：Responses 续链、切号和持久化容量治理已完成到源码与运行态。当前服务 PID `27124`，运行路径 `C:\Users\Administrator\Desktop\codex2api\codex2api.exe`，运行 EXE SHA256 为 `E17301BB23C6E301F4122C28B724F39278A7A8C5368452880536F68158CFD9F8`。
- `/health` 已复验：`status=ok`、`available=27/45`、`responses_memory.inflight_requests=0`、`continuity_persistent=true`、`continuity_persistence_failures=0`。
- 续链持久化垃圾已清理并压缩：不可回放终态 payload 已归零，悬空 `responses_continuity_heads` 已归零，SQLite 主库从约 85 MB 压缩到约 16 MB。

## 核心动机与背景 (Motivation & Background)
- 用户关注：账号 `rate_limited/payment_required/401/403/Disabled/错误状态` 时是否能丝滑换号；换号、WS 降级、重启恢复时是否还会静默丢上下文并诱发“当前会话未提供终端或工作区文件访问权限”。
- 根因分两层：
  - 调度层：严格会话绑定曾把长期不可调度状态当作普通等待，导致池里有号也可能卡住。
  - 续链层：跨账号续接不能依赖上游原 `previous_response_id`，必须由本地完整历史物化后再剥离指针；历史不完整时只能显式失败。
- 持久化层新增问题：旧库里大量不可回放终态节点占用配额，并存在悬空 head，影响重启恢复和清理效率。

## 关键设计与实现 (Implementation & Decisions)
- `auth/store.go`
  - `rate_limited`、`payment_required`、`StatusError`、Disabled、DispatchPaused、Banned、账号删除：不等待绑定账号，解绑后切换号池。
  - 普通短暂 Cooling、并发占满、短时不可调度：继续等待原绑定账号。
  - 严格会话无候选账号时不再空等 30 秒，会触发恢复探针并快速返回。
- `proxy/handler.go`、`proxy/responses_ws.go`、`proxy/retry_exclusions.go`
  - HTTP 与 WebSocket 对称使用严格亲和和失败账号排除；解绑后不会重新挑中刚失败账号。
  - owner 不可用、第三方无状态续链、WS 转 HTTP 时，必须先激活完整本地历史回放；失败返回 `continuation_context_unavailable`。
- `proxy/responses_continuity.go`
  - 只有完整历史和工具调用状态可验证时才物化历史并删除 `previous_response_id`。
  - 不可回放终态不再保留大体积 input/output。
  - 滑动 TTL 默认改为 24 小时，并支持 `CODEX_RESPONSES_CONTINUITY_TTL_HOURS`。
- `database/responses_continuity.go`
  - `PruneResponsesContinuations` / `TrimResponsesContinuations` 在事务内清理节点，并定向修复或删除悬空 head。
  - Trim 先瘦身遗留不可回放 payload，再按容量淘汰，避免无效数据挤占 64 MiB 配额。
- `.agent/rules/README.md`
  - 已固化 HTTP/WS 双通道、切号完整历史、持久化容量治理、TTL 配置、显式失败边界，防止后续 agent 改回危险语义。

## 验证证据
- `go test ./proxy -run 'TestResponses(NoAvailableAccountFailsFastWithoutCancelledContext|RequestTriggersRecoveryProbeWhenNoDispatchableAccount)$|TestPrepareOpenAIResponsesWebSocketContinuationKeepsOwnerThenReplayFallback' -count=1`：PASS。
- `go test ./database -run 'ResponsesContinuity|LatestReplayableResponse' -count=1`：PASS。
- `go test ./proxy -run '^TestOpenAIResponsesContinuity|^TestResponsesMemoryLimitsReadEnvironment$' -count=1`：PASS。
- `go test ./proxy -count=1`：PASS。
- `go test ./... -count=1`：PASS，全部包通过。
- `go vet ./...`：PASS。
- `git diff --check`：PASS，仅有既有 LF/CRLF 提示。
- 数据备份：`data\codex2api.db.pre-continuity-cleanup-20260802-001210.bak`。
- 数据清理后复验：
  - `terminal_non_replayable_rows=54`
  - `terminal_non_replayable_bytes=0`
  - `dangling_latest=0`
  - `dangling_replayable=0`
  - `heads=3`
  - `db_file_bytes=16003072`

## 待办事项 (Next Steps)
- [ ] 可选：用户用真实外部账号做一次 A -> B 两连包验收：首包拿 `response_id`，让账号 A 进入 `rate_limited/payment_required` 或不可用，再用同会话 `previous_response_id` 发续包，确认自动切到账号 B 且工具/终端/工作区上下文不断。
- [ ] 若真实验收仍出现“当前会话未提供终端或工作区文件访问权限”，优先查上游/客户端权限状态；代理层当前不会生成这句，也不会在历史不完整时静默新会话。

## 前端仪表盘请求明细 Tab (2026-08-02)
- 仪表盘请求记录与错误明细共用同一个列表卡片内的小 Tab，默认请求记录；错误明细嵌入时不显示系统运维顶部导航、页面副标题和摘要卡。
- 请求记录只在 Tab 激活、页面可见且存在活跃请求时每 3 秒静默刷新；错误明细仅在切换打开时加载，不启用自动轮询。
- 关键文件：`frontend/src/pages/Dashboard.tsx`、`frontend/src/components/UsageLogsPanel.tsx`、`frontend/src/pages/OperationsErrors.tsx`。

## 关键上下文
- 目录: `C:\Users\Administrator\Desktop\codex2api`
- 主要文件:
  - `C:\Users\Administrator\Desktop\codex2api\.agent\rules\README.md`
  - `C:\Users\Administrator\Desktop\codex2api\.agent\plan-Responses续链WS闭环.md`
  - `C:\Users\Administrator\Desktop\codex2api\auth\store.go`
  - `C:\Users\Administrator\Desktop\codex2api\database\responses_continuity.go`
  - `C:\Users\Administrator\Desktop\codex2api\database\responses_continuity_test.go`
  - `C:\Users\Administrator\Desktop\codex2api\proxy\responses_continuity.go`
  - `C:\Users\Administrator\Desktop\codex2api\proxy\responses_continuity_test.go`
  - `C:\Users\Administrator\Desktop\codex2api\proxy\responses_ws.go`
  - `C:\Users\Administrator\Desktop\codex2api\proxy\handler.go`
  - `C:\Users\Administrator\Desktop\codex2api\proxy\retry_exclusions.go`
