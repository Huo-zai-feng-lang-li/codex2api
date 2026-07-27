# 最新接续状态 (2026-07-28 02:55)

## 核心进展
- 账号真实可用状态闭环已合并 `main` 并热替换：`auth/store.go` 统一粘性冷却、恢复探针和严格可用谓词，`admin/handler.go` 输出 `is_available`，`frontend/src/pages/accountAvailability.ts` 让账号管理 Normal 与仪表盘共用后端真值。
- 功能提交 `f5be241`，合并提交 `6874fae`；正式服务 PID `44924`，监听 `127.0.0.1:18080`，EXE SHA256 `7F884EAF3B9C129888B6571C503BBCEFC87675961C0C7E0FE059DD348D3D0C56`。
- 生产验收：`/health.available=7`、`/api/admin/stats.available=7`、账号列表 `is_available=true` 行数 `7`，三套口径一致；服务状态 `ok`、在途请求 `0`。

## 变更决策
- `rate_limited` / `payment_required` 到期只进入恢复候选，不因时间或 Token 刷新自动回到 Normal；必须通过最小 Responses 能力探针或明确成功事件恢复。
- wham/usage 200 只更新用量，不能证明模型调用能力；Fast Scheduler、普通调度和 Lazy 调度共同拦截粘性冷却，Fast 额外检查 `Disabled`。
- UI 的 `is_available` 是唯一权威字段；仅兼容旧后端缺少该字段时才使用前端状态/model cooldown 推导。

## 待办事项 (Next Steps)
- [ ] 观察一轮自动恢复周期，确认受限账号仅在 Responses 探针成功后进入 Normal。
- [ ] 有真实 PostgreSQL 实例时补做账号状态持久化与恢复探针运行态验证。

## 关键上下文
- 目录: `C:\Users\Administrator\Desktop\codex2api`
- 主要文件: `auth/store.go`、`admin/usage_probe.go`、`frontend/src/pages/accountAvailability.ts`
