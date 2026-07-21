# 最新接续状态 (2026-07-16 23:57)

## 核心进展
- 已修复账号状态分类错误：`rate_limited/payment_required` 不再仅因冷却时间到期自动显示为正常，必须由账号维护测试或真实客户端请求获得成功响应后才恢复正常；核心实现位于 `auth/store.go`，回归测试位于 `auth/runtime_status_test.go`、`auth/premium_rate_limit_test.go`。
- 正常账号不参与恢复探测；到期的限流/额度受限、封禁和错误账号复用现有低成本探针。优先调用 wham 零 Token 用量接口，必要时回退最小 Responses 请求，恢复探测并发为 2。
- 429 继续遵循上游 `Retry-After/resets_at/resets_in_seconds`；402 未提供重置时间时默认退避 6 小时。客户端真实成功响应会清除旧 `rate_limited/payment_required` 状态并异步持久化。
- 已修复 Responses 对话续接磁盘清理偶发 `context deadline exceeded`：旧逻辑把关键落盘、TTL 清理和全表容量检查放入同一个 250ms 请求上下文，并且每次成功响应都执行 Trim。现在仅同步执行 Upsert 与祖先访问时间更新，Prune/Trim 使用独立 2 秒预算的后台单飞合并任务；核心实现位于 `proxy/responses_continuity.go`，测试位于 `proxy/responses_continuity_persistence_test.go`。
- Responses 续链使用本机内存 L1 + `data/codex2api.db` SQLite L2，只保存近期父响应关系和本轮 input/output 增量，用于服务重启后恢复。默认 TTL 1 小时、最多 2000 条、单链 400 items/4 MiB、内存与磁盘目标上限 64 MiB；磁盘 JSON 当前未加密。
- SQLite store 基准中位数由 `401103 ns/op, 72 allocs/op` 降至 `82602 ns/op, 46 allocs/op`，约降低 79%。流式请求在 `response.completed` 阶段落盘，不影响首字时间；非流式正常落盘约 0.083ms，不是每次增加几百毫秒。SQLite 极端繁忙时关键写最多等待 250ms，随后 fail-open 到内存。
- TDD、`go test ./... -count=1`、`go vet ./...`、`git diff --check`、release 参数构建均通过；异步清理定向测试连续 20 轮通过。本机 `CGO_ENABLED=0`，未执行 `go test -race`。
- 已完成构建和热替换。当前正式服务监听 `127.0.0.1:18080`，PID `27260`，EXE SHA256 `988490A89F73D5B38D4687DFE1B1CCFC5CD5E2D34A5BECE589471BF211E2E67F`；`/health=ok`、`continuity_persistent=true`、`continuity_persistence_failures=0`。
- 已使用代理 `http://127.0.0.1:51081` 推送 GitHub：仓库 `https://github.com/Huo-zai-feng-lang-li/codex2api.git`，提交 `0f686fc0555adff726b9798a016b00083761b98f`，`main` 与 `origin/main` 一致。

## 变更决策
- 展示分类与调度冷却解耦：冷却到期可以重新进入实际调度/恢复探测，但 UI 分类保持限流或额度受限，直到真实成功证据出现，禁止靠时间推断账号已恢复。
- 不对正常账号周期性发送模型探测请求，避免无意义 Token 和流量；真实客户端请求本身就是最准确的分类信号，非正常账号才进行低成本恢复探测。
- Responses 关键持久化仍在响应完成路径同步执行，确保服务重启可恢复；全表扫描、过期删除和容量修剪属于维护任务，必须后台执行，不能阻塞用户请求。
- 后台清理采用单飞合并：同一时刻最多一个任务运行，突发触发只保留最新待执行任务，避免无界 goroutine 和重复 SQLite 扫描。
- 续链磁盘缓存复用现有业务数据库，不新增 Redis、独立 SQLite 或常驻服务。数据库异常只增加观测计数并降级内存，不破坏当前响应。
- 热替换必须等待 `/health.responses_memory.inflight_requests=0`，通过管理 API 优雅关闭后再替换 EXE；Windows 启动直接使用 WMI `Win32_Process.Create`，避免普通终端子进程被会话结束连带终止。

## 待办事项 (Next Steps)
- [ ] 用户验收账号分类：确认限流/额度受限账号冷却到期后仍留在对应 Tab；执行账号维护测试或产生真实成功请求后才进入正常 Tab。
- [ ] 用户验收续链：同一 Codex 线程发送随机 token，重启服务后发送“继续并复述 token”，确认可正确恢复；同时检查 `/health` 的 `continuity_persistence_failures` 保持 0。
- [ ] 如需量化代理相对直连 API 的总延迟，执行同模型、同输入、同网络的端到端 A/B 压测；当前 0.083ms 仅代表续链 SQLite store，不代表账号选择、请求转换和网络链路总耗时。
- [ ] 可选安全增强：为 `responses_continuity` 的 input/output 增加磁盘静态加密、密钥轮换和安全清理。
- [ ] 本次覆盖写入 `handoff.md` 后工作区会产生文档改动；如需保持 Git 干净，下一轮单独提交该接力文件。

## 关键上下文
- 目录: `C:\Users\Administrator\Desktop\codex2api`
- 主要文件: `auth/store.go`、`proxy/handler.go`、`proxy/responses_continuity.go`
- 核心测试: `auth/runtime_status_test.go`、`auth/premium_rate_limit_test.go`、`proxy/responses_continuity_persistence_test.go`
- 详细计划: `.agent/plan-账号状态自动分类与恢复探测.md`、`.agent/plan-Responses续链异步清理性能闭环.md`
- 数据库: `data/codex2api.db`，续链表 `responses_continuity`
- 服务验收: `http://127.0.0.1:18080/health`
- GitHub: `https://github.com/Huo-zai-feng-lang-li/codex2api.git`
