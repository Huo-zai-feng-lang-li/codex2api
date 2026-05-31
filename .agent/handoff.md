# 最新接续状态 (2026-05-31 13:25)

## 账号池状态刷新口径取证 (2026-05-31)
- 管理台账号页状态来自 `/api/admin/accounts`，后端在 `admin/handler.go` 的 `ListAccounts` 合并内存账号后用 `acc.RuntimeStatus()` 覆盖 DB 状态；前端 `Accounts.tsx` 只按接口返回做统计和筛选。
- `封禁` 对应运行态 `unauthorized`，不是 DB 原始状态；`auth/store.go` 的 `RuntimeStatus()` 在 `health_tier=banned` 时直接返回 `unauthorized`。
- `可用` 计数来自 `Store.AvailableCount()`，只统计当前可调度账号；表格筛选里的“正常/可用”主要看 `status=active|ready` 且非限流、非错误、非禁用。
- 状态变更触发点：真实请求/测试连接遇到上游 401 会进入 unauthorized/banned，429 会进入限流冷却，402/非停用 403 会进入 payment_required；成功请求只更新调度评分，手动测试成功或恢复探针成功才会清错误/封禁并降回 warm/ready。
- 后台刷新周期到点会执行 token 刷新、用量探针、恢复探针；恢复探针默认按系统设置 `recovery_probe_interval_minutes` 控制，默认值 30 分钟，惰性模式会暂停主动探针。
- 前端账号页初次加载会拉一次账号列表；只有存在 `refreshing` 状态时才每 2 秒静默刷新。其他状态变化需要页面手动刷新、操作后 reload，或重新进入页面才能看到。
- 已给账号页接入现有 `useVisiblePolling`：页面可见/聚焦时每 15 秒静默刷新账号列表与运营概览，只读调用 `/api/admin/accounts`、`/api/admin/ops/overview`、`/api/admin/account-groups`，不触发测试连接或上游探测；页面隐藏时暂停，恢复可见时立即刷新一次。页头文案使用“页面刷新：HH:mm:ss”，避免和表格列“更新时间”（账号记录/用量更新时间）语义冲突。

## 当前阶段：payment_required 额度受限闭环
- 本轮已把上游 `402 Payment Required` / 非停用工作区 `403 Forbidden` 统一沉淀为账号运行态 `payment_required`，避免额度不足被误归成普通限流或只停留在探针日志里。
- 修复点：`proxy/handler.go` 新增 `ApplyUpstreamAccountFailure` 作为上游账号失败状态唯一写入口，统一处理 `429`、`401`、`402`、`403`；错误消息会带上上游 `error/detail/code`，便于页面和日志定位。
- 管理台手动测试连接、批量测试连接与用量探针已改为复用同一个失败写入口；`payment_required` 会进入 30 分钟额度受限冷却，停用工作区仍标记为错误。
- 前端账号页新增 `payment_required` 状态文案与橙色状态点，统计卡会把它单独列为“额度受限”，同时仍计入限流/受限总组。
- 当前运行服务已替换为新构建的 `codex2api.exe`，监听 `127.0.0.1:18080` 的 PID 为 `19664`，启动时间 `2026-05-31 13:23:45`；旧 exe 备份为 `codex2api.prev-20260531-132345.exe`。

## 当前阶段验证记录
- `go test ./admin -run TestBatchConnectionPaymentRequiredRecordsCooldown -count=1`：先红后绿，确认批量测试路径此前未沉淀 `payment_required`。
- `go test ./admin ./proxy -count=1`：通过。
- `go test ./... -count=1`：通过。
- `pnpm typecheck`：通过。
- `pnpm build`：通过。
- `go build -o codex2api.new.exe .`：通过，并已替换运行中的 `codex2api.exe`。
- `curl http://127.0.0.1:18080/health`：返回 `{"available":2,"status":"ok","total":8}`。
- 浏览器实看 `http://127.0.0.1:18080/admin/accounts?verify=payment_required`：账号页展示“额度受限：2”，账号行状态可见“额度受限”，本次加载新增控制台 errors=0、warnings=0。

## 当前阶段待办事项
- 如需提交代码，本轮相关文件是：`proxy/handler.go`、`admin/handler.go`、`admin/test_connection.go`、`admin/test_connection_test.go`、`admin/usage_probe.go`、`admin/usage_probe_test.go`、`frontend/src/components/StatusBadge.tsx`、`frontend/src/pages/Accounts.tsx`、`frontend/src/locales/en.json`、`frontend/src/locales/zh.json`、`.agent/handoff.md`。
- 如果后续仍出现额度类账号异常，优先看上游状态码和 `ErrorMsg` 中的 `code`；`deactivated_workspace` 应是错误态，普通余额/额度不足应是 `payment_required`。

## 上一阶段：账号恢复调度与 compact 修复

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
