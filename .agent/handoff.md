# 最新接续状态 (2026-07-16 19:25)

## 核心进展
- 已定位并修复同一 Codex Desktop 线程偶发返回 `Ready.` / `What would you like to work on?` 的上下文断连：根因是第三方 Responses 中转可能返回 200 但不保存 `previous_response_id` 状态，以及服务重启会清空原进程内续链。修复集中于 `proxy/responses_continuity.go`、`database/responses_continuity.go`、`main.go`。
- 非官方 Responses 中转默认在首次续接前使用本地完整历史回放；携带旧响应 ID 时优先绑定原响应账号，原账号失败或后台轮换账号时，仅在本地历史完整可重放时删除 `previous_response_id` 并重建 `input`，禁止把仅有“继续”的请求静默当成新对话。
- 续链改为内存 L1 + 现有业务数据库 L2：只保存父响应指针和本轮 input/output 增量。SQLite 使用现有 `data/codex2api.db` 与 WAL；服务重启后首次读取按父链有界恢复并回填内存。PostgreSQL 同步提供迁移表和数据接口。
- 高并发治理已生效：默认最多 64 个在途 Responses 请求、256 MiB 请求体预算、4 个本地历史回放；续链限制为 1 小时 TTL、2000 条、单链 400 items/4 MiB、内存与磁盘总量 64 MiB。每次成功落盘后校验磁盘硬上限，数据库操作 250 ms 超时后 fail-open 到内存。
- `/health.responses_memory` 已暴露在途数量/字节、回放数、拒绝数、缓存条目/字节/驱逐数，以及 `continuity_persistent` 和 `continuity_persistence_failures`。
- 真实隔离端口验收通过：首轮响应落盘，测试服务重启后首次续接准确复述随机 token；定向测试连续 10 轮、`go test ./... -count=1`、`go vet ./...`、`git diff --check` 均通过。最终 SQLite 写入基准约 0.13-0.20 ms/op。
- 正式服务当前运行最终 EXE：监听 `127.0.0.1:18080`，本次检查 PID `23500`，SHA256 `F3A4256F86F9E0A1A0EAADEE87F3C7C92D96DBBB3C4A9EC227ECEB3E75F3E923`；`/health` 为 `ok`，持久化启用且失败计数为 0。
- Git 已闭环：`14864a0` 实现持久化，`5ee1a1e` 强制每次落盘校验磁盘容量；均通过 `http://127.0.0.1:51081` 推送到 `origin/main`，当前 HEAD 为 `5ee1a1e72a079c9ea2c22052386bf9f37c549c1d`。

## 变更决策
- 续链属于 response ID 的归属与历史状态，不依赖普通 session affinity；账号切换本身不会降低回答质量，真正风险是切换时只传递 `previous_response_id` 而新账号/中转没有对应状态，因此必须优先本地完整历史回放。
- 磁盘缓存复用现有业务数据库，不新增独立 SQLite 文件、Redis 或常驻进程；热路径仍优先内存，磁盘主要承担重启恢复。
- 数据库异常不得破坏普通请求：写入、读取、触摸、清理失败只记录指标并降级，不篡改当前响应。
- 磁盘中保存的是近期对话增量明文；容量上限约 64 MiB，按滑动访问时间保留 1 小时，过期与容量超限自动删除。若后续需要更高隐私级别，应单独设计静态加密和密钥生命周期，不能仅做字符串混淆。
- 热替换必须先确认 `/health.responses_memory.inflight_requests=0`，调用管理 API 优雅关闭，等待数据库句柄释放，再替换 EXE；Windows 后台启动使用 WMI `Win32_Process.Create` 脱离当前终端，禁止再用会被工具会话等待/终止的普通 `Start-Process -PassThru` 方案。

## 待办事项 (Next Steps)
- [ ] 用户自行验收：同一线程发送随机 token，正常重启服务，首次发送“继续并复述 token”，应准确返回；检查 `/health` 中 `continuity_persistent=true`、`continuity_persistence_failures=0`。
- [ ] 持续观察磁盘表：当前 `responses_continuity` 为 87 条、65,444,029 字节，低于 64 MiB (`67,108,864` 字节)；若失败计数增加，先查 SQLite 锁等待/磁盘 I/O，再判断是否需要异步单写协程。
- [ ] 可选隐私增强：为续链 input/output 增加磁盘静态加密、密钥轮换和安全清理；这不是当前功能正确性的阻塞项。
- [ ] 可选发布动作：如需对外发版，决定是否从 `v2.2.6` 升补丁版本并补充 release tag；当前代码和服务已更新，但未创建新的版本标签。

## 关键上下文
- 目录: `C:\Users\Administrator\Desktop\codex2api`
- 主要文件: `proxy/responses_continuity.go`、`database/responses_continuity.go`、`proxy/responses_continuity_persistence_test.go`
- 数据库迁移: `database/sqlite.go`、`database/postgres.go`
- 启动接入与观测: `main.go`、`proxy/responses_memory_governor.go`
- 验收入口: `http://127.0.0.1:18080/health`

## 账号状态自动分类与恢复探测（2026-07-16）
- 已修复限流/额度受限账号仅因冷却到期就显示为正常的问题；展示分类保持到真实成功响应。
- 客户端真实请求成功会清除旧 `rate_limited/payment_required` 冷却并异步持久化；429/402/401 继续按真实上游响应分类。
- 正常账号不进入恢复探测；到期的 `rate_limited/payment_required`、封禁和错误账号复用现有低成本探针，优先 wham 零 Token 用量接口，必要时回退最小 Responses 请求，并发为 2。
- 402 无明确重置时间时默认退避 6 小时；429 继续尊重 `Retry-After/resets_at/resets_in_seconds`。
- 全量测试、vet、diff 检查通过并完成热替换。首次 WMI 启动进程收到外部 `signal:terminated` 后优雅退出，用户手动重启了同一新 EXE；当前 PID `4548`，EXE SHA256 `55424EEBDE86F374BA5433AC6FC6A21CC02961E9C31EC12BD4CC43EB966B38C5`，备份 `codex2api.prev-20260716-202207.exe`。
- 当前 `/health`：`status=ok`、`available=4`、`total=28`、`continuity_persistent=true`、`continuity_persistence_failures=1`；该计数来自一次续链磁盘容量清理超时，服务请求仍正常完成。
- 详细计划与证据：`.agent/plan-账号状态自动分类与恢复探测.md`。

## Responses 续链磁盘清理性能修复（2026-07-16）
- 根因确认：成功响应的关键落盘、TTL 清理和全表容量检查共用 250ms 请求上下文，且旧逻辑每次 `store` 都执行 `TrimResponsesContinuations`，SQLite 争用时产生 `context deadline exceeded`。
- 关键 `Upsert` 与祖先访问时间更新继续同步；Prune/Trim 改为独立 2 秒预算的后台单飞任务，突发触发只保留“当前执行 + 最新待执行”两次，不产生无界协程。
- 非清理周期内不再执行磁盘 Trim；仍在 1 分钟周期到期或内存驱逐时触发，失败保持 fail-open 并在下个周期重试。
- TDD 红灯证据：旧实现清理阻塞 `510.4956ms`、突发触发 `6` 次 Trim、普通 store 也触发 `1` 次 Trim；新实现对应测试通过并连续 20 轮无抖动。
- SQLite store 基准中位数由 `401103 ns/op, 72 allocs/op` 降至 `82602 ns/op, 46 allocs/op`；全量测试、vet、diff 检查和 release 参数构建均通过。`-race` 因本机 `CGO_ENABLED=0` 无法启用。
- 已热替换：备份 `codex2api.prev-20260716-223053.exe`；当前 PID `18956`，EXE SHA256 `988490A89F73D5B38D4687DFE1B1CCFC5CD5E2D34A5BECE589471BF211E2E67F`。
- 新服务连续 70 秒真实流量并跨过清理周期：`/health=ok`、`continuity_persistent=true`、`continuity_persistence_failures=0`，进程 PID 未变化；磁盘续链表从启动前 `75|66789892` 收敛后持续正常写入。
- 详细计划与证据：`.agent/plan-Responses续链异步清理性能闭环.md`。
