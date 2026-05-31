# 最新接续状态 (2026-05-31 12:07)

## GitHub 提交策略
- 本次提交目标仓库：`https://github.com/Huo-zai-feng-lang-li/codex2api.git`，直接推送 `main`。
- 提交范围只包含代码、测试、前端锁文件、启动脚本、`.gitignore` 和本 handoff；本机运行数据不进入 Git。
- 已确认需排除：`data/*.db`、`data/*.db-wal`、`data/*.db-shm`、`data/backups/`、`data/images/`、`data/backgrounds/`。
- 新用户拉取项目后不应获得本机账号数据库；SQLite 首次运行由迁移自动建空库和默认系统设置，账号/API key 由用户自行添加。

## 核心进展
- 已修复账号池等待路径在“仅剩错误/封禁账号”时直接返回 `no_available_account` 的问题。
- 根因是 `auth.Store.WaitForSessionAvailableWithFilter` 只判断“当前是否有可调度候选”，没有把“可通过恢复探针复活的候选”纳入等待条件，导致当前请求没机会等待探活完成。
- 修复点：`auth/store.go` 新增等待期恢复探针候选判断；当没有立即可调度账号但存在匹配 API key/filter/exclude 的恢复候选时，触发一次 `TriggerRecoveryProbeAsync` 并继续等待。
- 恢复探针成功后现在会清理 `Disabled`、`StatusError`/冷却态、`ErrorMsg`、`FailureStreak`，把 banned 账号降回 warm，并调用 `fastSchedulerUpdate` 刷新快速调度器索引。
- 回归测试：`auth/store_scheduler_test.go` 新增 `TestWaitForSessionAvailableTriggersRecoveryProbeForOnlyErroredBannedAccount`，覆盖唯一账号为 `StatusError + HealthTierBanned + Disabled` 时，等待请求触发探针并拿回该账号。
- 端到端回归：`proxy/handler_test.go` 新增 `TestResponsesRequestTriggersRecoveryProbeWhenNoDispatchableAccount`，用真实 Gin 路由、真实 `/v1/responses` handler、真实 store、隔离假上游验证“无可调度账号 -> 服务端恢复探针 -> 账号恢复 -> 上游请求 200 返回”的闭环。
- 已补充边界测试：`auth/manual_test_success_test.go` 的 `TestRecordManualTestSuccessDoesNotClearDispatchPaused` 确认手动测试连接成功只清运行时错误/封禁，不会解除用户手动禁用的 `DispatchPaused`。
- 已编译并替换本机服务，当前监听 `127.0.0.1:18080` 的最新 `codex2api.exe` PID 为 `27140`，启动时间 `2026-05-31 11:29:04`；旧 exe 备份为 `codex2api.prev-20260531-112904.exe`。
- 保留上一阶段 compact 修复：OpenAI Responses API 类型账号处理 `/responses/compact` 成功响应时，`proxy/handler.go` 直接返回上游 Responses 原始 JSON，避免把 `output` 翻译丢失为 chat completion `choices`。

## 关键证据
- 红测失败：`go test ./auth -run TestWaitForSessionAvailableTriggersRecoveryProbeForOnlyErroredBannedAccount -count=1` 初次返回 nil，复现等待路径提前退出。
- 手动禁用保护测试：`go test ./auth -run "TestDispatchPausedDoesNotBlockRecoveryProbe|TestRecordManualTestSuccess" -count=1` 通过，确认恢复探针/测试连接都不会清 `DispatchPaused`。
- 修复后同一测试通过，且 `go test ./auth`、`go test ./proxy`、`go test ./...` 全部通过。
- E2E 红绿记录：新增 `/v1/responses` 端到端测试初次失败在断言展示态 `RuntimeStatus=ready`，实际项目语义中可调度展示为 `active`；修正为断言底层 `StatusReady`、非 banned、`FailureStreak=0` 后通过。
- 上一阶段 compact 证据仍有效：Responses 对象需要 `object=response` 和顶层 `output`，本机 `/responses/compact` 最小请求已验证 `has_output=True`、`has_choices=False`。

## 验证记录
- `go test ./auth -run TestWaitForSessionAvailableTriggersRecoveryProbeForOnlyErroredBannedAccount -count=1`：先红后绿。
- `go test ./auth -run "TestDispatchPausedDoesNotBlockRecoveryProbe|TestRecordManualTestSuccess" -count=1`：通过。
- `go test ./auth`：通过。
- `go test ./proxy`：通过。
- `go test ./...`：通过。
- `go build -o codex2api.new.exe .`：通过，并已替换运行中的 `codex2api.exe`。
- 接续复验：`go test ./auth -count=1`、`go test ./proxy -count=1`、`go test ./... -count=1` 均通过。
- 接续复验：`go build -o $env:TEMP\codex2api-verify.exe .` 通过，临时产物已删除。
- 接续复验：`curl http://127.0.0.1:18080/health` 返回 `{"available":4,"status":"ok","total":6}`。
- E2E 复验：`go test ./proxy -run TestResponsesRequestTriggersRecoveryProbeWhenNoDispatchableAccount -count=1` 通过。
- E2E 后全量复验：`go test ./proxy -count=1`、`go test ./auth -count=1`、`go test ./... -count=1` 均通过。

## 待办事项
- 如需提交代码，无号恢复闭环相关文件是：`auth/store.go`、`auth/store_scheduler_test.go`、`auth/manual_test_success_test.go`、`proxy/handler_test.go`、`.agent/handoff.md`；上一阶段 compact 相关文件是 `proxy/handler.go`、`proxy/handler_test.go`，不要回滚工作树中其他历史改动。
- 如果后续仍出现 `503 no_available_account`，优先区分：模型白名单不匹配、账号确实无恢复凭据、恢复探针失败、或上游余额/资格问题。

## 上一阶段 compact 修复详情
- 已定位并修复 Codex 自动压缩报错 `Error running remote compact task: stream disconnected before completion: missing field output`。
- 根因是 `OpenAI Responses API` 类型账号处理 `/responses/compact` 成功响应时，把上游 Responses 对象错误翻译成 `chat.completion` 结构，返回体只有 `choices`，没有 Codex compact 客户端需要的顶层 `output`。
- 修复点：`proxy/handler.go` 的 OpenAI Responses compact 成功路径直接返回上游 Responses 原始 JSON，不再调用 `TranslateCompactResponse`。
- 回归测试：`proxy/handler_test.go` 的 `TestResponsesCompactUsesOpenAIResponsesAPIAccount` 已改为断言 `object=response`、保留顶层 `output`、且不再依赖 `choices.0.message.content`。
- 当时已重建并替换本机服务，`127.0.0.1:18080` 的 `codex2api.exe` PID 曾为 `14232`；该记录已被上方 11:29 的最新服务状态覆盖。

### 关键证据
- Context7/OpenAI API Reference 显示 Responses 对象示例包含 `object: "response"` 和顶层 `output` 数组；Chat Completions 对象才是 `object: "chat.completion"` 和 `choices`。
- 失败前的本地回归测试输出为 `{"object":"chat.completion","choices":[...]}`，能直接复现 `missing field output` 的结构性原因。
- 修复后本机真实 `/responses/compact` 最小请求返回：`object=response`、`has_output=True`、`output_count=2`、`has_choices=False`、`status=completed`。
- 数据库最新 compact 记录：`/v1/responses/compact` -> `https://api.freemodel.dev/v1/responses`，`gpt-5.4-mini`，`status_code=200`，说明反代路径和 upstream endpoint 正常。

### 验证记录
- `go test ./proxy -run TestResponsesCompactUsesOpenAIResponsesAPIAccount -count=1`：先红后绿。
- `go test ./proxy`：通过。
- `go test ./api -run TestValidateResponsesAPIRequestAllowsCompactionInputType -count=1`：通过。
- `go test ./...`：通过。
- `go build -o codex2api.new.exe .`：通过。
- `curl http://127.0.0.1:18080/health`：`{"available":1,"status":"ok","total":6}`。

### 待办事项
- 如果后续仍有 compact 报错，优先区分两类：`missing field output` 是本次 schema 问题；402/余额不足或 available 过低是账号池问题。
- 如需提交代码，只阶段化本轮相关文件：`proxy/handler.go`、`proxy/handler_test.go`、`.agent/handoff.md`；不要回滚工作树中其他历史改动。
