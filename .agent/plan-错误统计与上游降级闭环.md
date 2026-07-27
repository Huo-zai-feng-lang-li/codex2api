# 错误统计与上游降级闭环

## 目标

- 业务请求统一按 `status_code <> 499 AND is_retry_attempt = false` 统计。
- 内部重试保留取证，但不占用户请求数、错误数、Token、计费与限额。
- `account_billed` 保留全部真实上游成本，`user_billed` 仅统计终态请求。
- 启用准确基线 v2，旧基线保留为 legacy。
- WSS 可恢复失败自动降级 HTTP，并按端点缓存已验证的降级能力。

## 阶段

1. TDD 修正数据库统计、限额、图表、流量快照与 retention 基线。
2. TDD 增加基线 v2 迁移、准确起点与兼容字段。
3. TDD 修复 WSS 空 400、1008/1013 的 HTTP fallback 与能力缓存。
4. 扩展运维错误汇总，区分终态错误与重试错误。
5. 完成前端类型检查/构建、Go 全量测试、vet、diff 检查、候选构建和隔离验证。
6. 排空正式服务流量后安全热替换，核对 PID、端口、EXE 哈希与健康状态。
7. 复查现网错误归因与全部回归测试；通过后分离提交既有暂存内容和本任务改动，合并 `main` 并再次热替换。

## 验收

- `attempt_index > 0 && is_retry_attempt = false` 仍计为终态。
- 内部 598 重试不触发 API Key 本地限额。
- 运维日志仍可查询全部 attempt。
- WSS 空 400 后 HTTP 成功，后续请求命中端点能力缓存。
- fallback 失败时保留真实上游终态错误。
- SQLite/PostgreSQL 口径一致，retention 前后累计统计不跳变。

## 仓库边界

- 保留任务开始前暂存区全部内容，不覆盖、不回滚、不混入本任务提交。
- 测试服务和临时脚本在交付前全部清理；正式 `18080` 服务仅在验证闭环阶段替换。

## 执行进度

- [x] 阶段 1：终态统计、API Key 限额、图表/流量聚合完成 RED→GREEN。
- [x] 阶段 2：基线 v2、legacy 隔离、retention/clear 口径完成 RED→GREEN。
- [x] 阶段 3：WSS 空 400、1008/1013 fallback 与端点能力缓存完成 RED→GREEN。
- [x] 阶段 4：运维摘要终态/重试字段与前端卡片完成 RED→GREEN。
- [x] 阶段 5：规格审查、代码质量审查和全量验证。
- [x] 阶段 6：候选构建、隔离验证与正式服务安全替换。
- [x] 阶段 7：复查、提交、合并主分支与最终热替换。

### 已完成证据

- `go test ./database -count=1`：通过。
- `go test ./proxy -count=1`：通过。
- `go test ./admin -count=1`：通过。
- 前端定向测试、类型检查、生产构建：通过。
- database/proxy/admin 定向 `go vet` 与 `git diff --check`：通过。
- `npm --prefix frontend run typecheck`：通过。
- `npm --prefix frontend run build`：通过，2593 modules。
- `go test ./... -count=1`：全部包通过。
- `go vet ./...`：通过。
- 最终规格与质量复审：P0-P2 为 0；残余为 PostgreSQL 实例验证边界及既有复杂函数技术债。
- 候选程序与独立复建 SHA256 均为 `FA4BE00DE5133AEBFFF3DAF66A31F816014C97AB86B2F3FF964D288CECA835A5`。
- 隔离实例验证：退出码 0，`stats_version=2`，统计与错误汇总兼容字段齐全。
- 正式服务替换：旧 PID `17440` 优雅退出，新 PID `44512` 监听 `127.0.0.1:18080`；连续 3 次 `/health.status=ok` 且 `inflight_requests=0`。
- 生产接口验证：v2 基线已初始化，legacy 基线保留；`terminal_errors`、`retry_errors`、`retry_attempts` 正常返回；内嵌前端入口与构建资产均为 HTTP 200。
- 暂存区 patch-id 仍为 `ff722bcbcd3561e730d4a4a82584e744052a0421`；候选、回滚、验证和失败产物均已清理。
- 最终复查：前端测试/typecheck/build、`go test ./... -count=1`、`go vet ./...`、`git diff --check` 全部通过；三路独立审查无功能阻塞项。
- 功能分支 `zk/error-stats-upstream-fallback` 与原工作分支 `zk/dashboard-usage-range` 均已提交并合并 `main`；任务外脚本 URL 拼写在合并前修正。
- 最终热替换：运行 PID `23316`，监听 `127.0.0.1:18080`，EXE SHA256 为 `102FB08B3843D6F3F5CC524FA5C12CAE150E7C4597C0EEF622D2CF89EE6FB4E4`，`/health.status=ok` 且 `inflight_requests=0`。
