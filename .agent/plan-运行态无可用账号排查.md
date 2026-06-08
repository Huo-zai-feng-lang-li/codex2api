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
- 2026-06-08：继续排查“历史成功账号未参与本次调度”。本轮先按真实数据库和 API Key 规则模拟候选账号过滤，再决定是否需要代码修复。
- 2026-06-08：当前 API Key `id=1` 未设置 `allowed_group_ids`，因此本轮不是 API Key 分组限制导致。`/health` 使用 `.env` 中端口 `18080`；账号池排查期间持续变化，最新取证返回 `available=6,total=22`。
- 2026-06-08：按 `api_key=1 + model=gpt-5.5` 模拟过滤：最新通过调度过滤的是账号 `551、582、583、602、603、604`；`576` 仍被 `payment_required` 冷却排除，`585-598` 多数被 `rate_limited` 冷却排除，`553` 因 `enabled=0` 排除，`599、601` 已删除。
- 2026-06-08：近期日志里的 `no available account` 主要来自上游 WebSocket 返回 `1013 no available account` 或本地候选池被冷却压缩后的重试链，不是本地调度器漏扫历史成功账号。

## 当前完整流程说明
- 客户端入口：
  - `GET /responses`：Codex 客户端 WebSocket 新会话入口。
  - `POST /responses` 或 `/v1/responses`：普通 HTTP/SSE Responses 入口。
- 账号选择：
  - 先按模型、API Key、账号启用状态、删除状态、模型白名单、冷却状态筛选。
  - direct OpenAI Responses API 账号只在请求模型属于它的 `models` 白名单时可被选中。
  - OAuth/Codex 账号按 Codex 模型目录和账号健康状态参与调度。
- direct API WebSocket 流程：
  - `GET /responses` 收到 WS JSON 后先规范化为 `response.create`。
  - 如果选中 direct OpenAI Responses API 账号，转发前删除 WS 专用 `type` 字段。
  - 然后用该账号的 `base_url + /v1/responses` 和 `api_key` 发普通 HTTP/SSE 请求。
  - 上游 SSE 事件再被逐条写回给本地 WebSocket 客户端。
- Codex/OAuth WebSocket 流程：
  - 如果选中 Codex/OAuth 账号，继续走 Codex 上游 WS/HTTP 通道。
  - 如果 WS 上游缺少 `response.completed/response.failed`、连接断开、返回 `no_available_account`，且还没向客户端输出正文，会重试或切换账号。
  - 如果已经向客户端输出了正文，不再透明重试，避免重复输出内容。
- 首字慢与切号：
  - `POST /responses` direct API 分支有首字超时保护；超过阈值还没有首个有效事件，会软排除当前账号并切到另一个账号。
  - 首字超时默认只给 1 次切号预算；小账号池里不会无限切号。
  - `GET /responses` direct API 分支当前没有独立定时首字超时切号；它会在上游报错、断流、502、`no_available_account` 时重试/换号。
  - 6 到 14 秒这种慢但最终成功的请求不会切号，因为服务已经拿到了正常结果。
- 判断标准：
  - `/health` 很快、日志最终 `200` 或 `response.completed`，只是耗时长：主要是上游模型/账号慢。
  - 日志出现 `首字超时，静默切换一次账号`：项目触发了首字超时切号。
  - 日志出现 `no_available_account/无可用账号/503`：继续查账号调度、模型白名单、账号启用状态。
  - 日志出现队列满：说明本地并发保护触发，需要看 `dispatch_queue_limit` 和可用账号数。
