# 计划文档：账号池多上游智能限流策略优化与自愈闭环

## 任务背景与核心目标
针对系统接入的多家第三方上游提供商（如 `daseinai.xyz`、`suanzhou.top`、`aerolink`、`quan2go` 等）在配额超限或并发频控时返回的不同错误格式，建立统一的「多上游智能语义分类引擎」，消除无效轮询探测，实现精准的动态冷却与秒级自愈。

## 架构与核心改动
1. **多上游智能语义分类引擎（`matchGenericUpstreamRateLimit`）**：
   - **日配额超限（Daily Limit）**：识别 `daily`、`daily_limit`、`日额度`、`今日用量` 等关键词，动态计算并锁定冷却至 **次日 00:00:05**（避免无效 5 分钟轮询重试）。
   - **周配额超限（Weekly Limit）**：识别 `weekly`、`周额度` 等，锁定冷却至 **7 天（`7*24h`）**。
   - **欠费 / 额度耗尽（Insufficient Quota）**：识别 `insufficient_user_quota`、`余额不足`、`额度用完` 等，归类为 `payment_required` 深度休眠 24 小时。
   - **短时频控（RPM / TPM）**：识别 `requests per minute`、`rpm`、`tpm` 等，快速退避 1 分钟。
   - **通用兜底**：未知 429 维持 5 分钟指数退避，保持官方 Codex Header 双窗口机制 100% 兼容。
2. **403 穿透归因修复**：
   - 在 `ApplyUpstreamAccountFailure` 中拦截返回 403 的 `daily usage limit` 报文，将其正确对齐至次日午夜。
3. **全链路测试与版本发布**：
   - 增加全场景单元测试用例（覆盖 Daily Limit、Weekly Limit、欠费、RPM、403）。
   - 版本升级至 `v2.2.8`，更新 `CHANGELOG.md`、`package.json`，并通过 `build-and-restart.bat` 热更新。

## 关键依赖文件
- `proxy/handler.go`：核心限流、上游失败拦截与智能语义匹配逻辑。
- `proxy/handler_test.go`：全维度 429 与午夜对齐单元测试。
- `CHANGELOG.md`：版本变更记录。
- `frontend/package.json`：前端版本管理。
- `build-and-restart.bat`：前端打包、后端编译及零停机热更脚本。

## 验证与验收标准
- [x] `go test ./...` 100% 通过。
- [x] `build-and-restart.bat` 成功编译并热重启服务。
- [x] `/health` 健康检查正常返回 `status: ok`。
- [x] Git Tag `v2.2.8` 打标并提交仓库。
