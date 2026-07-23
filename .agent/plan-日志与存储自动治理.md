# 日志与存储自动治理 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为可再生日志增加安全保留、为错误文件日志增加轮转，并在系统运维展示统一容量告警。

**Architecture:** 数据库负责原子汇总与删除，独立维护包负责定时编排和容量采样，运维接口只读取缓存快照，前端复用现有指标卡展示。`/health` 与用户图片生命周期保持不变。

**Tech Stack:** Go、database/sql、Gin、React、TypeScript、SQLite、PostgreSQL、gopsutil。

---

### Task 1: 数据库原子保留

**Files:**
- Create: `database/retention.go`
- Modify: `database/sqlite_test.go`
- Modify: `database/security_events_test.go`

- [x] **Step 1: 写失败测试**

新增测试覆盖 `PruneUsageLogsBefore` 的总量守恒、499 排除、仅 200 缓存分母、幂等和事务回滚，并覆盖安全事件、Prompt 日志、账号事件截止时间清理。

- [x] **Step 2: 验证 RED**

Run: `go test ./database -run "TestPrune(UsageLogsBefore|OperationalDataBefore)" -count=1`

Expected: FAIL，提示保留方法不存在或旧数据未删除。

- [x] **Step 3: 最小实现**

实现：

```go
type RetentionResult struct {
    UsageLogs       int64
    SecurityEvents  int64
    PromptFilterLogs int64
    AccountEvents   int64
}

func (db *DB) PruneUsageLogsBefore(ctx context.Context, cutoff time.Time) (int64, error)
func (db *DB) PruneOperationalDataBefore(ctx context.Context, policy RetentionPolicy, now time.Time) (RetentionResult, error)
```

SQLite 与 PostgreSQL 在单事务内汇总并删除相同截止范围；`ClearUsageLogs` 复用原子核心。

- [x] **Step 4: 验证 GREEN**

Run: `go test ./database -run "TestPrune(UsageLogsBefore|OperationalDataBefore)" -count=1`

Expected: PASS。

### Task 2: 错误日志轮转

**Files:**
- Modify: `proxy/error_logger.go`
- Modify: `proxy/error_logger_test.go`

- [ ] **Step 1: 写失败测试**

用临时目录和小阈值验证当前文件、`.1` 到 `.5` 的轮转顺序、最老备份删除、敏感正文截断和并发写入。

- [ ] **Step 2: 验证 RED**

Run: `go test ./proxy -run "TestFileLoggerRotation" -count=1`

Expected: FAIL，当前 logger 不轮转。

- [ ] **Step 3: 最小实现**

为 `fileLogger` 增加 `maxSize`、`maxBackups`、互斥锁和 `rotateLocked`，默认 50 MiB × 5，并支持运维环境变量覆盖。

- [ ] **Step 4: 验证 GREEN**

Run: `go test ./proxy -run "TestFileLoggerRotation" -count=1`

Expected: PASS。

### Task 3: 后台维护与容量快照

**Files:**
- Create: `maintenance/manager.go`
- Create: `maintenance/manager_test.go`
- Modify: `main.go`

- [ ] **Step 1: 写失败测试**

覆盖启动即执行、按周期执行、停止后无泄漏、目录大小统计、80/90 边界和采样失败 `unknown`。

- [ ] **Step 2: 验证 RED**

Run: `go test ./maintenance -count=1`

Expected: FAIL，包或类型不存在。

- [ ] **Step 3: 最小实现**

实现 `Manager.Start/Stop/Snapshot`，清理每小时、采样每分钟；`main` 注入当前数据库和本地目录，并保证关闭时停止任务。

- [ ] **Step 4: 验证 GREEN**

Run: `go test ./maintenance -count=1`

Expected: PASS。

### Task 4: 运维接口与前端展示

**Files:**
- Modify: `admin/handler.go`
- Modify: `admin/responses.go`
- Modify: `admin/ops_overview_test.go`
- Modify: `frontend/src/types.ts`
- Modify: `frontend/src/pages/Operations.tsx`
- Modify: `frontend/src/locales/zh.json`
- Modify: `frontend/src/locales/en.json`

- [ ] **Step 1: 写失败测试**

后端测试断言运维概览输出统一 storage DTO；前端类型固定 `normal/warning/critical/unknown`，不改变 `/health`。

- [ ] **Step 2: 验证 RED**

Run: `go test ./admin -run "TestOpsOverviewStorage" -count=1`

Expected: FAIL，响应缺少 storage。

- [ ] **Step 3: 最小实现**

向 Handler 注入快照函数并扩展运维概览；前端复用 `OpsMetricCard` 增加存储卡，显示数据库、日志、图片和剩余空间。

- [ ] **Step 4: 验证 GREEN**

Run: `go test ./admin -run "TestOpsOverviewStorage" -count=1`

Run: `npm --prefix frontend run typecheck`

Expected: 全部 PASS。

### Task 5: 全链路验证、提交和热替换

**Files:**
- Modify: `.agent/handoff.md`

- [ ] **Step 1: 静态与全量测试**

Run: `go test ./... -count=1`

Run: `go vet ./...`

Run: `npm --prefix frontend run typecheck`

Run: `npm --prefix frontend run build`

Run: `git diff --check`

Expected: 全部退出码 0。

- [ ] **Step 2: 浏览器验收**

打开 `/admin/ops`，确认容量卡状态、数值和 15 秒刷新稳定；确认 `/health.status == ok` 未被容量告警改变。

- [ ] **Step 3: 构建与热替换**

按 `.agent/rules/README.md` 构建候选 EXE、等待在途请求归零、优雅停止、原子替换、WMI 脱离启动，并核对 PID、端口、SHA256、健康状态和续链持久化。

- [ ] **Step 4: 提交**

仅暂存本任务文件，提交信息：`feat(ops): 增加日志保留与存储治理`。
