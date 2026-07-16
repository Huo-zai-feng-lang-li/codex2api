# 最新接续状态 (2026-07-16)

## Responses 续链磁盘持久化
- Responses 父指针与本轮 input/output 增量已写入现有业务数据库；SQLite 继续使用 WAL，无新增数据库文件或常驻服务。
- 服务重启/热替换后，首次携带 `previous_response_id` 的请求会从磁盘有界加载父链并回填内存；正常热路径仍优先使用内存。
- 磁盘与内存共用 1 小时 TTL、2000 条、400 items/链、4 MiB/链、64 MiB 总量；过期和容量数据自动清理。
- 数据库操作使用 250 ms 短超时并 fail-open，失败只失去磁盘恢复能力，不改变普通请求结果；`/health.responses_memory` 增加持久化状态和失败计数。
- 隔离端口真实验收已通过：首轮响应落盘，测试服务重启后首次续接准确返回随机 token，持久化失败计数为 0。
- 正式服务当前使用最终新 EXE，PID `27832`，SHA256 `933765CB43C24B551DBFDA81D5F1E8FDCAA81A07DDA6ED0C5631CDF686BF95B3`，`http://127.0.0.1:18080/health` 为 `ok`。
- 全量测试、go vet、定向 10 轮、磁盘容量/TTL/损坏降级/超时降级测试和基准均通过；SQLite 写入约 0.11-0.35 ms/op。
- 功能提交 `14864a0` 已推送到 `origin/main`。

## Responses 高并发内存治理
- C1 真实新线程测试发现第三方中转会接受 `previous_response_id` 并返回 200，但实际忘记首轮 token；现已对非官方 Responses 中转在首次续接时主动发送受限的本地完整历史。修复后真实双轮随机 token 首次续接匹配通过。
- 可通过 `CODEX_RESPONSES_CONTINUITY_MODE=upstream` 显式信任已验证具备有状态续接能力的中转；默认 `auto` 仅信任官方 `api.openai.com`。
- 连续性缓存已从“每个响应保存完整历史”改为“父响应指针 + 本轮 input/output 增量”，长链与分叉不再重复复制公共历史。
- `/v1/responses` 已增加进程级在途请求数、请求体字节和本地完整历史回退并发上限；默认分别为 64、256 MiB、4，饱和时返回明确的 `local_memory_pressure` 或 `local_continuation_busy`。
- `/health` 已增加 `responses_memory`，可查看在途请求、请求体字节、回退数、拒绝计数、连续性缓存条目/字节/驱逐数及配置上限。
- 新服务已热替换运行：PID `21740`，SHA256 `B3F955C3BACA25152919BB6E2682E056FF967AB1D6309005497345F9D0A25084`，`/health` 返回 `ok`。
- 验证通过：定向测试重复 10 轮、`go test ./... -count=1`、`go vet ./...`、release build、隔离端口 1 MiB 压力拒绝测试。Windows 当前无 CGO/GCC，`go test -race` 未执行。
- 基准：请求准入/释放约 79-84 ns、32 B、1 alloc；20 轮历史物化约 10.9-11.3 us、12.8 KiB、53 alloc。

## 核心进展
- `v2.2.6` 已完成 Responses 上下文断连修复：响应 ID 绑定生成账号；第三方不支持 HTTP 续链、原账号传输失败或账号不可用时，仅在本地完整历史可重放时删除 `previous_response_id` 并重建 `input`。
- Codex 与 OpenAI Responses 两类账号的成功响应都会登记进程内完整历史；普通请求、未知 Responses 输入项、安全审计与现有工具续链缓存保持原行为。
- 真实中转双轮验收通过：首轮记忆 `RELEASE-226-Q5`，首次续接准确返回同一 token；日志确认触发 `response_owner_http_failure` 本地历史回放。
- 发布验证通过：前端 typecheck、代理 URL 单测、`VITE_APP_VERSION=v2.2.6` 生产构建、`go test ./... -count=1`、`git diff --check` 和 Go release build 均成功。
- 发布产物：`codex2api.v2.2.6.exe`，SHA256 `04C6891C1F5A49D4E1267C824122FE6E8FFFBD3827DB4AA37E0C684FC670003A`。

## 变更决策
- 续链是响应归属状态，不依赖普通 session affinity；携带旧响应 ID 时只能选择原 owner。
- 换账号前必须确认本地历史完整并转为无 `previous_response_id` 的完整输入；缓存缺失时保留原错误，禁止静默按新对话继续。
- 历史仅保存在进程内，TTL 1 小时、单链 400 items/4 MiB、全局 2000 entries/64 MiB，不写磁盘或 Redis。
- `v2.2.6` 前端版本由构建变量注入，仓库发布版本由 `v2.2.6` Git 标签确定。

## 验收入口
- 健康检查：`http://127.0.0.1:18080/health`。
- 人工场景：同一线程发送随机 token，结束或中断后首次发送“继续并复述 token”，应直接准确返回。
- 关键日志：`OpenAI Responses 续链切换为本地完整历史回放`。

## 关键文件
- `proxy/responses_continuity.go`
- `proxy/responses_continuity_test.go`
- `proxy/handler.go`
- `CHANGELOG.md`
