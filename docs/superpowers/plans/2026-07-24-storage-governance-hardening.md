# Storage Governance Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让日志清理在写库失败时不丢数据，让容量监控准确表达未知状态，并完成线程生命周期、接口契约、前端文案和全量回归闭环。

**Architecture:** 数据库层使用“暂取批次—持久化—失败重入队”的可靠刷新协议；维护层使用可取消、幂等的生命周期和具名快照；管理接口直接传递具名快照；前端通过纯展示函数处理四态状态与数据库类型。各模块独立测试后再进行全量构建、热替换和提交。

**Tech Stack:** Go 1.x、SQLite/PostgreSQL、React 19、TypeScript 5.9、Vite 8、Vitest、Windows PowerShell。

---

## 文件结构

- `database/postgres.go`：可靠刷新日志缓冲，提供严格错误返回。
- `database/retention.go`：清理前强制确认日志刷新成功。
- `database/retention_test.go`：覆盖刷新失败不清理、不丢缓冲。
- `maintenance/manager.go`：幂等生命周期、可取消清理、准确未知状态。
- `maintenance/manager_test.go`：覆盖 Stop、采样错误和目录错误。
- `admin/handler.go`、`admin/ops.go`、`main.go`：用具名快照替代位置元组。
- `admin/ops_test.go`：覆盖快照映射和未注入状态。
- `frontend/src/lib/storagePresentation.ts`：存储状态和展示值纯函数。
- `frontend/src/lib/storagePresentation.test.ts`：四态和空值测试。
- `frontend/src/pages/Operations.tsx`、`frontend/src/locales/{zh,en}.json`：准确状态与国际化展示。
- `frontend/package.json`、`frontend/package-lock.json`：增加 Vitest 测试入口。
- `proxy/responses_memory_governor*.go`：修复现有续链淘汰随机失败。
- `.agent/plan-存储治理闭环修复.md`、`.agent/handoff.md`：阶段状态与接续信息。

### Task 1: 日志缓冲可靠刷新

**Files:**
- Modify: `database/postgres.go:2172-2240`
- Modify: `database/retention.go:53-60,232-235`
- Test: `database/retention_test.go`

- [ ] **Step 1: 写失败测试**

新增同包测试，向 `db.logBuf` 放入一条记录，关闭底层连接后调用严格刷新，断言返回错误且记录仍在缓冲；再调用清理，断言清理返回错误。

```go
func TestFlushLogsStrictRequeuesBatchOnFailure(t *testing.T) {
	db := newRetentionTestDB(t)
	db.logBuf = append(db.logBuf, usageLogEntry{Model: "must-survive"})
	require.NoError(t, db.conn.Close())
	require.Error(t, db.flushLogsStrict())
	require.Len(t, db.logBuf, 1)
	require.Equal(t, "must-survive", db.logBuf[0].Model)
}
```

- [ ] **Step 2: 验证 RED**

Run: `go test ./database -run 'TestFlushLogsStrictRequeuesBatchOnFailure|TestPruneStopsWhenFlushFails' -count=1`

Expected: FAIL，提示 `flushLogsStrict` 不存在或清理未返回错误。

- [ ] **Step 3: 最小实现**

实现 `flushLogsStrict() error`：锁内取出批次并清空；持久化失败时锁内把失败批次放回当前新日志之前；原 `flushLogs()` 仅包装并记录错误。`pruneUsageLogs` 与 `PruneOperationalDataBefore` 必须直接返回严格刷新错误。

```go
func (db *DB) requeueLogs(batch []usageLogEntry) {
	db.logMu.Lock()
	db.logBuf = append(batch, db.logBuf...)
	db.logMu.Unlock()
}
```

- [ ] **Step 4: 验证 GREEN**

Run: `go test ./database -run 'TestFlushLogsStrictRequeuesBatchOnFailure|TestPrune' -count=1`

Expected: PASS。

### Task 2: 维护线程与容量状态

**Files:**
- Modify: `maintenance/manager.go:76-215,285-299`
- Test: `maintenance/manager_test.go`

- [ ] **Step 1: 写失败测试**

增加三个测试：未启动直接 Stop 返回、重复并发 Stop 不 panic、受管路径采样失败返回 `StatusUnknown`。非法路径使用 `string([]byte{0})`，避免依赖操作系统权限。

```go
func TestStopBeforeStartAndRepeatedStop(t *testing.T) {
	m := New(Config{})
	require.NotPanics(t, m.Stop)
	require.NotPanics(t, m.Stop)
}

func TestManagedCollectionFailureIsUnknown(t *testing.T) {
	m := New(Config{DBPath: string([]byte{0})})
	s := m.collectSnapshot()
	require.Equal(t, StatusUnknown, s.Status)
	require.NotEmpty(t, s.Error)
}
```

- [ ] **Step 2: 验证 RED**

Run: `go test ./maintenance -run 'TestStopBeforeStartAndRepeatedStop|TestManagedCollectionFailureIsUnknown' -count=1 -timeout=5s`

Expected: FAIL；现有 Stop 超时或重复关闭通道，采集错误仍为 normal。

- [ ] **Step 3: 最小实现**

以生命周期互斥锁保存 `started/stopped/cancel/done`；`Start` 创建上下文，`Stop` 只取消一次并按需等待。采样循环和清理循环分别运行，均监听同一上下文；清理调用使用该上下文。`collectSnapshot` 在 `diskErr != nil || managedErr != nil` 时返回 unknown。`dirSize` 返回首个遍历或文件信息错误。

- [ ] **Step 4: 验证 GREEN 与竞态**

Run: `go test -race ./maintenance -count=20`

Expected: PASS，无竞态、panic 或超时。

### Task 3: 具名接口契约

**Files:**
- Modify: `admin/handler.go:44-63,209-214`
- Modify: `admin/ops.go:279-305`
- Modify: `main.go:216-222`
- Test: `admin/ops_test.go`

- [ ] **Step 1: 写失败测试**

让 Handler 注入 `func() *maintenance.StorageSnapshot`，构造所有字段值不同的快照，断言 JSON 响应字段逐一对应；未注入时断言 `status=unknown`。

```go
h.SetStorageSnapshot(func() *maintenance.StorageSnapshot {
	return &maintenance.StorageSnapshot{
		Status: maintenance.StatusWarning,
		Disk: maintenance.DiskInfo{TotalBytes: 11, UsedBytes: 7, FreeBytes: 4},
		Managed: maintenance.ManagedInfo{DatabaseBytes: 1, LogsBytes: 2, ImagesBytes: 3, TotalBytes: 6},
	}
})
```

- [ ] **Step 2: 验证 RED**

Run: `go test ./admin -run TestBuildStorageResponse -count=1`

Expected: FAIL，因为 Setter 仍要求 12 个位置返回值。

- [ ] **Step 3: 最小实现并验证**

将 Handler 字段和 Setter 改为具名快照提供器；`buildStorageResponse` 从结构字段映射；main 直接返回 `maintMgr.Snapshot()`。

Run: `go test ./admin -run TestBuildStorageResponse -count=1`

Expected: PASS。

### Task 4: 前端四态与国际化

**Files:**
- Create: `frontend/src/lib/storagePresentation.ts`
- Create: `frontend/src/lib/storagePresentation.test.ts`
- Modify: `frontend/src/pages/Operations.tsx:267,390-490`
- Modify: `frontend/src/locales/zh.json:858-866`
- Modify: `frontend/src/locales/en.json:858-866`
- Modify: `frontend/package.json`
- Modify: `frontend/package-lock.json`

- [ ] **Step 1: 安装测试工具并写失败测试**

Run: `npm --prefix frontend install --save-dev vitest`

添加 `test` 脚本并测试：unknown 映射为 info；零总容量返回占位符；normal/warning/critical 分别映射 normal/warning/danger；数据库标签按 sqlite/postgres 返回翻译键。

```ts
expect(getStorageTone("unknown")).toBe("info")
expect(getStorageTone("critical")).toBe("danger")
expect(getDatabaseStorageKey("postgres")).toBe("ops.storagePostgresExternal")
```

- [ ] **Step 2: 验证 RED**

Run: `npm --prefix frontend test -- --run`

Expected: FAIL，因为展示纯函数尚不存在。

- [ ] **Step 3: 最小实现**

创建纯函数模块；Operations 使用四态函数和新的翻译键。unknown 使用 info 样式和“数据不可用”，容量为零且状态 unknown 时显示 `--`；移除 `盘已用`、`SQLite`、`日志`硬编码。

- [ ] **Step 4: 验证 GREEN**

Run: `npm --prefix frontend test -- --run && npm --prefix frontend run typecheck && npm --prefix frontend run build`

Expected: 全部 PASS。

### Task 5: 修复续链淘汰随机测试

**Files:**
- Modify: `proxy/responses_memory_governor.go` 或实际淘汰实现文件
- Test: `proxy/responses_memory_governor_test.go:200-245`

- [ ] **Step 1: 稳定复现并定位**

Run: `go test ./proxy -run TestContinuationRegistryNeverReplaysAfterParentEviction -count=100`

Expected: 当前版本随机失败；记录失败次数。检查相同时间戳下的淘汰顺序和父子链失效传播。

- [ ] **Step 2: 添加确定性失败测试**

使用可控时钟或稳定序号构造父节点与子节点，断言父节点淘汰后所有依赖子链均不可回放。

- [ ] **Step 3: 最小修复**

淘汰父节点时同步标记或删除其所有后代，或者按单调访问序号确定淘汰顺序；禁止依赖 map 遍历顺序和相同时间戳比较。

- [ ] **Step 4: 压测验证**

Run: `go test ./proxy -run TestContinuationRegistryNeverReplaysAfterParentEviction -count=200`

Expected: 200/200 PASS。

### Task 6: 全量验证、热替换与提交

**Files:**
- Modify: `.agent/plan-存储治理闭环修复.md`
- Modify: `.agent/handoff.md`

- [ ] **Step 1: 全量验证**

Run: `go test ./... -count=1`

Run: `go test -race ./maintenance ./database ./admin -count=1`

Run: `go vet ./...`

Run: `npm --prefix frontend test -- --run`

Run: `npm --prefix frontend run typecheck`

Run: `npm --prefix frontend run build`

Expected: 全部退出码 0。

- [ ] **Step 2: 构建候选 EXE**

按 `.agent/rules/README.md` 的项目命令构建候选文件，比较当前运行 EXE 与候选 SHA256，确认候选包含当前 HEAD。

- [ ] **Step 3: 热替换**

优雅停止当前服务，原子替换 EXE，使用 WMI 分离启动；验证监听端口、`/health`、管理接口存储状态和前端资源哈希。失败则恢复旧 EXE。

- [ ] **Step 4: 提交**

仅暂存本计划涉及文件，检查 `git diff --cached --check` 后提交：

```text
fix(storage): harden retention and capacity monitoring
```

