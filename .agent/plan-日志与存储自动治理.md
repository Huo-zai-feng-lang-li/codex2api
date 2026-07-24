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

- [x] **Step 1: 写失败测试**
- [x] **Step 2: 验证 RED**
- [x] **Step 3: 最小实现**
- [x] **Step 4: 验证 GREEN**

### Task 3: 后台维护与容量快照

- [x] **Step 1: 写失败测试**
- [x] **Step 2: 验证 RED**
- [x] **Step 3: 最小实现**
- [x] **Step 4: 验证 GREEN**

### Task 4: 运维接口与前端展示

- [x] **Step 1: 写失败测试**
- [x] **Step 2: 验证 RED**
- [x] **Step 3: 最小实现**
- [x] **Step 4: 验证 GREEN**


- [x] **Step 1: 全量回归测试**
- [x] **Step 2: 前端打包构建验证 (`npm run build`)**
- [x] **Step 3: 后台线程独立运行无阻塞确认**
`git diff --check`

Expected: 全部退出码 0。

- [ ] **Step 2: 浏览器验收**

打开 `/admin/ops`，确认容量卡状态、数值和 15 秒刷新稳定；确认 `/health.status == ok` 未被容量告警改变。

- [ ] **Step 3: 构建与热替换**

按 `.agent/rules/README.md` 构建候选 EXE、等待在途请求归零、优雅停止、原子替换、WMI 脱离启动，并核对 PID、端口、SHA256、健康状态和续链持久化。

- [ ] **Step 4: 提交**

仅暂存本任务文件，提交信息：`feat(ops): 增加日志保留与存储治理`。
