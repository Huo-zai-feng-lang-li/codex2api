# 账号真实可用状态闭环

## 目标
- “正常”Tab 只展示当前没有账号级禁用/错误/粘性冷却、且没有活跃模型 cooldown 的账号。
- 仪表盘“可用账号”与账号管理“正常”共用后端 `IsFullyAvailable()` 真值，禁止两套前端口径。
- `rate_limited` / `payment_required` 冷却到期后进入待恢复状态，只有恢复探针或明确成功事件才能重新调度。
- 普通、快速、Lazy、Token 刷新、数据库重载路径使用同一粘性冷却语义，避免业务请求充当恢复探针。
- 保留模型级按请求模型过滤：模型 A 限流不阻塞账号服务模型 B，但 UI 不再把其归入“正常”。

## 根因
- `Account.IsAvailable()`、Fast Scheduler、Lazy Scheduler 在 cooldown deadline 到期后直接放行。
- Token 后台刷新与数据库重载会把过期 `rate_limited/payment_required` 清为 Ready，但 Token 刷新不等于模型调用成功。
- Normal 数量使用减法推导且 Normal Tab 未检查 `model_cooldowns`，导致计数、列表和真实能力不一致。

## 实施阶段
1. RED：补齐 expired sticky cooldown 在普通/快速/Lazy/刷新/重启路径仍不可调度的回归测试。
2. GREEN：抽取统一粘性冷却判定，修复调度、刷新和加载路径；恢复探针成功后才清理。
3. RED→GREEN：前端抽取唯一 Normal 谓词，计数与过滤复用，并排除活跃模型 cooldown。
4. 后端账号响应增加统一可用布尔值；`AvailableCount()` 与 Normal Tab 共同使用，保证仪表盘和账号管理数量一致。
5. 定向测试、全量测试、类型检查、生产构建、静态检查与独立候选构建。
6. 合并 `main`，通过 `127.0.0.1:51081` 推送，并按项目规则排空流量后热替换。

## 验收
- expired `rate_limited/payment_required`：`IsAvailable=false`、`AvailableCount` 不计、普通/Fast/Lazy 均不选。
- Token 刷新或重启不会把粘性冷却误置 Ready；恢复探针成功后才恢复。
- 非粘性临时 cooldown 到期仍可自动恢复。
- Normal 数量等于 Normal Tab 实际行数；active/ready + 活跃模型 cooldown 不进入 Normal。
- `/health.available`、仪表盘可用账号、账号管理 Normal 数量三者完全一致。
- 现有模型级请求过滤保持通过，已知限流账号不再产生恢复性用户错误请求。

## 仓库边界
- 在独立 worktree `zk/account-verified-normal` 开发。
- 根工作区现有 `.agent/rules/README.md`、`frontend/src/components/AnimatedMetricValue.tsx`、`frontend/src/lib/metricAnimation.test.mjs` 改动保持原样，不混入本任务。

## 实施结果（2026-07-28）
- [x] 普通、Fast、Lazy 调度统一拦截粘性冷却；Fast 额外补齐 `Disabled` 门禁。
- [x] Token 刷新按最终冷却状态仲裁，刷新期间新产生的限流/付费状态不会被覆盖。
- [x] 恢复流程拆分 usage probe 与 Responses capability probe；只有最小 Responses 验证成功才恢复 Normal。
- [x] 管理端输出 `is_available`，仪表盘与账号列表共用 `IsFullyAvailable()`；前端只在旧接口缺字段时使用兼容谓词。
- [x] 前端测试、类型检查、生产构建、`go test ./...`、`go vet ./...`、`git diff --check` 全部通过。
