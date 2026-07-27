# 最新接续状态 (2026-07-27 23:39)

## 核心进展
- 错误统计与上游降级闭环已完成、提交、合并 `main` 并热替换：业务统计统一使用 `status_code <> 499 AND is_retry_attempt = false`，baseline v2、WSS→HTTP fallback、终态/重试错误分栏均已上线。
- `zk/error-stats-upstream-fallback` 已由 `4102115` 合并；`zk/dashboard-usage-range` 已由 `52aaeb8` 合并；当前 `main` 为 `c49f0ee`，运行服务 PID `34956`，EXE SHA256 为 `7CAFDC09FC4EF9F536568A13FCB3A30A14DCC8BBDBAD157D549145F098869453`。
- 账号可用数波动已定位：`/health.available` 使用 `Store.AvailableCount()` 的模型无关口径；当前运行态实际为 active=11、rate_limited=19、unauthorized=11、payment_required=4，但 `/health.available=29`，因此“可用”会包含部分仅特定模型冷却/已到期冷却的账号。

## 变更决策
- 用户统计、限额和计费仅使用终态请求；内部 retry attempt 保留取证并单独统计，账号上游实际成本仍保留全部 attempt。
- 限流账号采用冷却到期与恢复探针机制：普通冷却到期后 `Account.IsAvailable()` 自动重新纳入；模型冷却按 `reset_at` 失效；恢复探针当前间隔 30 分钟，后台刷新间隔 2 分钟。
- 当前“账号可用数”是全局可调度近似值，不等于目标模型实时可用数；下一轮应改为模型感知指标或并列展示 `active / model_available / rate_limited`，避免管理界面误导。

## 待办事项 (Next Steps)
- [ ] 修正账号可用统计口径：禁止将仍处于目标模型 cooldown 的账号计入该模型可用数，并补充 runtime/API/UI 回归测试。
- [ ] 通过已恢复的 `http://127.0.0.1:51081` VPN 代理推送本地 `main` 到 `origin/main`，推送后核对远端提交。
- [ ] 有真实 PostgreSQL 实例时补做 baseline v2、retention 与统计谓词运行态验证。

## 关键上下文
- 目录: `C:\Users\Administrator\Desktop\codex2api`
- 主要文件: `auth/store.go`、`database/usage_attempts.go`、`proxy/responses_ws.go`
