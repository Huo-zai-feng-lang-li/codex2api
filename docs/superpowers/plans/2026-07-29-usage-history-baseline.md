# Usage History Baseline Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将实际运行数据库的历史累计基线设置为 50 亿 Token 与 10000 USD，同时保持请求日志和近期统计不变。

**Architecture:** 先识别运行实例的数据源并记录原始基线与日志摘要，再在单事务中仅更新 `usage_stats_baseline_v2.total_tokens` 和 `user_billed`。最后通过数据库查询及管理统计接口交叉验证，失败时恢复原值。

**Tech Stack:** PowerShell 7、SQLite/PostgreSQL、Go 管理 API

---

### Task 1: 识别数据源并建立回滚证据

**Files:**
- Inspect: `config/config.go`
- Inspect: `.env`
- Inspect: `codex2api.db`

- [ ] **Step 1: 检查运行进程、端口和数据库配置**

Run: `Get-CimInstance Win32_Process | Where-Object { $_.Name -eq 'codex2api.exe' }`，并检查配置中的数据库驱动与连接地址。

Expected: 唯一确定当前运行实例实际使用 SQLite 或 PostgreSQL。

- [ ] **Step 2: 记录变更前证据**

读取 `usage_stats_baseline_v2` 目标行、`usage_logs` 行数及 Token/费用聚合，并保存数据库备份或可回滚原值。

Expected: 获得原始 `total_tokens`、`user_billed` 和日志摘要。

### Task 2: 事务更新累计基线

**Files:**
- Modify data: active database table `usage_stats_baseline_v2`

- [ ] **Step 1: 在事务中更新目标字段**

```sql
UPDATE usage_stats_baseline_v2
SET total_tokens = 5000000000,
    user_billed = 10000
WHERE id = 1;
```

Expected: 恰好影响 1 行；其他字段不变。

- [ ] **Step 2: 事务内核对并提交**

Expected: 目标字段分别为 `5000000000` 与 `10000`，随后提交事务。

### Task 3: 闭环验证

**Files:**
- Inspect data: active database
- Inspect API: `/api/admin/usage/stats`

- [ ] **Step 1: 核对日志未变**

Expected: `usage_logs` 行数和聚合摘要与变更前一致。

- [ ] **Step 2: 核对累计统计语义**

Expected: 最终累计值等于新基线加当前可见日志聚合值；今日、趋势和 API Key 窗口不变。

- [ ] **Step 3: 清理临时进程和敏感临时文件**

Expected: 无本次任务启动的后台进程或残留凭据文件。
