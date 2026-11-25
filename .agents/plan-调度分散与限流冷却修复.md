# 调度分散与限流冷却修复（v2.4.1）

## 问题
1. `affinity_mode=off` 后请求仍长期落在同一账号（用户实测 6 条全 `Agnes-官方-12`）。
2. `scheduler_mode`（轮询/剩余额度）在默认调度路径里从未生效。
3. 上游域名冷却规则（默认 `agnes-ai.cn` 120s）、RPM/TPM 突发兜底标记为 `rate_limited`，属于粘性冷却，到点不会自动恢复。

## 根因（取证）
- 主链路 `proxy/retry_exclusions.go:145` 走 `NextForStrictSessionExcludingWithFilter`，该入口只读绑定、不看 `affinity_mode`（`auth/store.go` 原 3445-3463）。
- 打分类调度 `SuccessBonus = min(streak*2, 12)`（`auth/store.go` 647），连续成功账号分数永久高于其他账号，形成"越用越粘"。
- `cooldownBlocksDispatch` 对 `rate_limited` / `payment_required` 直接返回阻塞，忽略 `CooldownUntil`。

## 改动
- `auth/store.go`
  - `affinity_mode=off` 时 `NextForStrictSessionExcludingWithFilter` 跳过绑定。
  - 新增分散挑号：`nextSpreadAccount` → `nextRoundRobinAccount` / `nextLowestUsageAccount`（最高健康档内轮询；剩余额度取最低用量档，5 个百分点内同档轮换）。
  - 新增 `CooldownReasonTransientRateLimit = "rate_limited_short"`，到期自动恢复，冷却期内仍展示为 `rate_limited`。
- `proxy/handler.go`：上游规则、RPM/TPM 突发、未知 429 兜底改用 `rate_limited_short`。
- 版本 2.4.0 → 2.4.1；CHANGELOG 增补。

## 验证
- `go test ./auth ./proxy -count=1` 全过；新增 5 个用例（轮询/剩余额度分散、sticky 排除、strict 入口忽略绑定、短时限流到期恢复、上游规则 reason）。
- 线上：`affinity=off`、`remaining_quota`，8 条请求均匀落到 `Agnes-官方-1`…`8`，全 200；公网 `/health` ok。

## 遗留
- 配额类（daily/weekly/insufficient）仍为粘性/长冷却，需要充值或恢复探测（默认 30 分钟）。
- 前端本次未重建（无 UI 改动，`build-and-restart.bat` 的 npm build 因内存瞬时不足失败）。
