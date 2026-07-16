# Responses 续链异步清理性能闭环实施计划

> **执行要求：** 按 TDD 顺序逐项闭环，关键写入保持同步，维护性清理不得阻塞请求。

**目标：** 消除 Responses 续链磁盘容量清理在请求链路中的偶发超时，同时保持 SQLite/PostgreSQL 持久化、TTL 和容量上限语义。

**架构：** `Upsert` 与祖先访问时间更新继续使用 250ms 同步预算，保证成功响应返回前已完成关键持久化。TTL 裁剪与容量修剪改为独立 2s 预算的后台单飞任务；并发触发只保留最新待执行任务，最多一个清理协程运行，避免请求阻塞和清理风暴。

**技术栈：** Go、SQLite/PostgreSQL、`context`、`sync`、Go testing/benchmark。

---

## 阶段 1：失败回归测试与基线

- [x] 记录当前 SQLite 落盘基线：`186185/543262/401103 ns/op`，`72 allocs/op`。
- [x] 将“每次 store 同步 Trim”旧测试替换为“清理阻塞时 store 立即返回”。
- [x] 增加突发触发只合并为当前任务加一个待执行任务的测试。
- [x] 增加清理失败计数、下个周期可重试的测试。
- [x] 运行定向测试，确认旧实现按预期失败。

## 阶段 2：最小实现

- [x] 在 `proxy/responses_continuity.go` 增加后台清理请求和单飞状态。
- [x] 保留同步 `UpsertResponsesContinuation` 与 `TouchResponsesContinuations`。
- [x] 仅在清理周期到期或内存驱逐时投递后台清理。
- [x] 后台清理使用独立 2s 上下文，先 Prune、后 Trim；失败保持 fail-open 并计数。
- [x] 后台任务运行期间覆盖待执行请求，不创建无界协程。

## 阶段 3：验证与部署

- [x] 定向测试通过；本机 Go 因 `CGO_ENABLED=0` 无法启用 `-race`，以互斥/原子覆盖测试和全量测试验证。
- [x] SQLite 基准复测：中位数由 `401103 ns/op, 72 allocs/op` 降至 `82602 ns/op, 46 allocs/op`。
- [x] `go test ./... -count=1`、`go vet ./...`、`git diff --check` 全部通过。
- [x] CodeGraph 影响面审查和关键实现原文回看完成；多 Agent 审查通道两次未收到任务内容，未采用无证据结论。
- [x] 构建新 EXE；等待 `/health.responses_memory.inflight_requests=0` 后优雅停服、备份、替换和启动。
- [x] 新服务 PID `18956`，EXE SHA256 `988490A89F73D5B38D4687DFE1B1CCFC5CD5E2D34A5BECE589471BF211E2E67F`；连续 70 秒真实流量和跨清理周期观测中 `/health=ok`、持久化失败计数保持 `0`。

## 影响边界

- 只改变续链缓存的磁盘维护调度，不改变请求协议、账号调度、内存缓存和数据库表结构。
- 服务启动时的首次 Prune/Trim 保持同步，确保启动后的磁盘状态已收敛。
- SQLite 与 PostgreSQL 共用现有持久化接口，无迁移和配置变更。
