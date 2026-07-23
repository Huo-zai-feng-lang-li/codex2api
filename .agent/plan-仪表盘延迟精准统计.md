# 仪表盘延迟精准统计实施计划

> **执行约束：** 按 TDD 顺序完成；仅修改延迟统计与展示链路，不改变“今日请求/Token/缓存”等现有卡片口径。

**目标：** 仪表盘健康模块的首字延迟和完成延迟跟随“使用趋势”时间范围，只统计 `status_code = 200` 且延迟值大于 0 的模型调用。

**架构：** 复用使用趋势已有的图表聚合请求，在 `ChartAggregation` 中附带所选区间的两个延迟汇总，避免第二次完整使用统计查询，也避免把其他“今日”指标误改为所选区间。数据库的 `/usage/stats` 延迟字段同步修正为相同的 `200 + 有效值 + 当前查询区间` 口径，SQLite 与 PostgreSQL 保持一致。

**技术栈：** Go、database/sql、SQLite、PostgreSQL、React、TypeScript。

---

### 阶段 1：建立失败回归测试

**文件：**
- 修改：`database/sqlite_test.go`

- [x] 扩展 `TestUsageStatsLatencyCalculationConsistency`，加入带正延迟的非 200 请求，断言它不进入首字/完成延迟平均值。
- [x] 新增使用统计区间测试：区间外的 200 请求不进入 `AvgFirstTokenMs` 和 `AvgDurationMs`。
- [x] 新增图表聚合延迟测试：所选区间内只统计 200，请求状态 201/500 和区间外 200 均排除。
- [x] 修改日志清理基线测试：清空明细后，历史基线不能泄漏到当前区间延迟。
- [x] 运行定向测试并确认按预期失败。

### 阶段 2：修复数据库统计口径

**文件：**
- 修改：`database/sqlite.go`
- 修改：`database/postgres.go`

- [x] SQLite 使用统计仅在 `statusCode == 200 && latency > 0` 时累计两个延迟。
- [x] PostgreSQL 使用统计改为：

```sql
AVG(CASE WHEN status_code = 200 THEN NULLIF(first_token_ms, 0) END)
AVG(CASE WHEN status_code = 200 THEN NULLIF(duration_ms, 0) END)
```

- [x] 删除 `AvgFirstTokenMs` 被全历史和清理基线二次覆盖的逻辑，但保留总请求、Token、缓存率和计费基线行为。
- [x] 在 SQLite 图表单次扫描中累计所选区间 200 请求的首字/完成延迟。
- [x] 在 PostgreSQL 图表分桶查询中返回每桶延迟总和与样本数，并在 Go 层做加权汇总，避免“平均值再平均”的统计误差。
- [x] 运行定向数据库测试并确认通过。

### 阶段 3：绑定使用趋势时间范围

**文件：**
- 修改：`frontend/src/types.ts`
- 修改：`frontend/src/pages/Dashboard.tsx`
- 修改：`frontend/src/components/UsageStatsSummary.tsx`

- [x] 为 `ChartAggregation` 增加 `avg_first_token_ms`、`avg_duration_ms`。
- [x] 健康卡从当前 `chartData` 读取两个延迟；切换时间范围、数据加载期间显示 `-`，不短暂展示旧区间数据。
- [x] 保留 `UsageStatsSummary` 中请求、Token、缓存、错误率的原有数据来源和口径。

### 阶段 4：闭环验证

- [x] `gofmt` 格式化修改的 Go 文件。
- [x] 定向运行延迟和图表聚合测试。
- [x] 运行 `go test ./... -count=1`（本次相关包通过；仓库既有 `admin` 关机测试稳定触发 `os.Exit`，`proxy` 偶发失败后单包复跑通过）。
- [x] 运行前端类型检查与生产构建。
- [x] 运行 `go vet ./...`、`git diff --check` 并检查最终差异范围。

### 阶段 5：消除自动刷新闪烁

- [x] Playwright 跨越一个 15 秒刷新周期复现健康首字延迟短暂变为 `-`。
- [x] 自动刷新采用 stale-while-revalidate：请求期间保留旧值，响应成功后原位替换。
- [x] 首次加载和用户切换趋势时间范围时仍显示 `-`，避免把旧区间数据冒充新口径。
- [x] 首字延迟和完成延迟更新时执行 400ms 数字滚动，并尊重 `prefers-reduced-motion`。
- [x] 不改变 15 秒刷新频率，不增加接口请求，不影响流量、Token、缓存和错误率口径。
- [x] 热更新后 Playwright 连续采样 17 秒共 519 次，`dashSamples=0`；可控数据变化验证数字滚动路径生效。
