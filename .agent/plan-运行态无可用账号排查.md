# 运行态无可用账号排查

## 目标
- 复现并定位新开 Codex 对话出现 `Reconnecting... 4/5` 与 `stream disconnected before completion: 无可用账号，请稍后重试` 的根因。
- 区分服务版本、账号池可用性、模型白名单、排队/限流、上游硬失败与流式首包软失败。

## 取证计划
- 检查本机服务进程、端口与 `/health`。
- 检查最近服务日志里的 `no_available_account`、`stream disconnected`、队列、首字超时、账号排除原因。
- 检查配置与数据库中账号状态、模型字段、速率限制/删除状态。
- 如确认是代码缺陷，再做最小修复并执行验证链。

## 阶段记录
- 2026-06-02：开始取证。
- 2026-06-02：确认当前服务非未启动问题，`/health={"available":2,"status":"ok","total":3}`；活跃账号为 551、569，553 为 `enabled=0`。
- 2026-06-02：定位新开对话走 `GET /responses` WebSocket 路径；WebSocket 路径未按 direct OpenAI Responses API 账号走 `/v1/responses` HTTP/SSE，上游返回的 `无可用账号` 可能透传到下游。
- 2026-06-02：已修复 WebSocket direct OpenAI Responses 分支，剥离 WS-only `type` 字段；`go test ./... -count=1` 通过；重建并重启 `codex2api.exe`。
- 2026-06-02：真实本地 WebSocket 请求验证通过，收到 `response.completed`。
