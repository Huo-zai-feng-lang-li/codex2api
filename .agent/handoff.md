# 最新接续状态 (2026-07-27)

## 核心进展
- 错误统计与上游降级闭环已完成并部署：业务统计只计 `status_code <> 499 AND is_retry_attempt = false`，重试 attempt 独立留痕且不占用户限额。
- 使用统计启用 baseline v2，返回 `stats_version=2`、`accurate_since`、`legacy_baseline_available`；旧 baseline 保留审计但不进入主统计。
- Responses WSS 可恢复失败会在同账号内降级 HTTP，并按规范化端点缓存 30 分钟/2 分钟能力；fallback 失败保留真实终态错误。
- 运维错误汇总新增 `terminal_errors`、`retry_errors`，旧 `total_errors` 保持 attempt 级语义；前端已区分“错误请求”和“错误尝试”。

## 生产验证
- 正式服务监听 `127.0.0.1:18080`；最终 PID 与运行 EXE SHA256 以本轮交付时的实时校验为准。
- 两个功能分支及本 handoff 提交完成后重新构建候选并执行热替换，运行文件与最终 `main` 源码一致。
- `/health` 连续 3 次 `status=ok`、`responses_memory.inflight_requests=0`。
- `/api/admin/usage/stats` 返回 `stats_version=2`；运维错误汇总四个兼容字段齐全；前端入口和构建资产均为 HTTP 200。
- `npm --prefix frontend run typecheck/build`、`go test ./... -count=1`、`go vet ./...`、`git diff --check` 全部通过。

## Git 集成
- `zk/error-stats-upstream-fallback` 已由功能提交 `5bd3b9a` 合并到 `main`，合并提交为 `4102115`。
- `zk/dashboard-usage-range` 已提交并合并到 `main`，合并提交为 `52aaeb8`；`scripts/batch_add_accounts.py` 的 `hhttps://` 拼写已在 `aba7515` 修正。

## 仓库边界
- 临时候选、验证、回滚和失败程序均已清理；仅正式服务保留运行。
- PostgreSQL 路径已通过编译与方言契约测试；本机未配置真实 PostgreSQL 实例，运行态证据来自 SQLite/正式服务。
