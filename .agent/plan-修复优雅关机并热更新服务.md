# 修复优雅关机并热更新服务实施计划

> **执行方式：** 当前会话内按 TDD 执行；部署阶段只操作当前 `codex2api` 服务，在在途请求归零后进行优雅热替换。

**目标：** 恢复后台关机接口的优雅关闭契约，修复 `admin` 全量测试，并将包含仪表盘延迟修复的新版程序安全热更新到运行服务后提交。

**架构：** `admin.ShutdownSystem` 只调用注入的 `RequestShutdown`；真正的 HTTP Server 生命周期继续由 `main` 管理。首次请求返回 200，重复请求返回 202；Handler 不再直接调用 `os.Exit`。部署遵循“构建新文件 → 等待在途归零 → 管理接口优雅关机 → 等待句柄释放 → 原子替换 → WMI 脱离启动 → 健康检查”。

**技术栈：** Go、Gin、Windows PowerShell、WMI、Git。

---

### 阶段 1：TDD 修复关机契约

**文件：**
- 修改：`admin/system.go`
- 验证：`admin/system_test.go`

- [x] 运行现有两个关机测试，确认失败原因为未调用 `shutdownFunc`、重复请求错误返回 200。
- [x] 删除 Handler 内的 `os.Exit` 和 100ms goroutine。
- [x] 首次关机调用 `h.RequestShutdown("admin_api:" + c.ClientIP())` 并返回 200。
- [x] 重复/无法启动关机时记录审计日志并返回 202。
- [x] 定向测试转绿。

### 阶段 2：全链路验证与构建

- [x] 运行 `go test ./admin -count=1`。
- [x] 运行 `go test ./... -count=1`。
- [x] 运行 `go vet ./...`。
- [x] 运行前端类型检查与生产构建。
- [x] 运行 `git diff --check`。
- [x] 构建 `codex2api.new.exe` 并记录 SHA256：`FD52C3527F990441D400EE2025205FD0E79B6FAB9E7289547AB87D8C4E077710`。

### 阶段 3：生产安全热替换

- [x] 读取当前监听 PID、命令行、工作目录、可执行文件路径和 `/health`。
- [x] 等待 `/health.responses_memory.inflight_requests = 0`。
- [x] 调用后台关机接口并等待进程及 SQLite 句柄释放（旧版本为直接退出；新版本已恢复优雅关机）。
- [x] 备份旧 EXE，替换为新 EXE。
- [x] 使用 WMI `Win32_Process.Create` 在原工作目录脱离启动。
- [x] 验证新 PID `11904`、监听端口、EXE SHA256、`/health=ok`、持久化状态和在途请求。

### 阶段 4：提交

- [x] 检查最终差异仅包含本次延迟与优雅关机相关文件。
- [x] 提交全部相关改动。
- [x] 验证提交后工作区干净并记录提交哈希。
