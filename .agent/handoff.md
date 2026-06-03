# 最新接续状态 (2026-06-02)

## 核心进展
- 已完成小账号池首字慢与本地吞吐保护闭环：`/responses` 首字超时改为动态阈值 + 单请求最多切换 2 个账号 + 忙时排队限流 + 队列满返回 `429/Retry-After`，并已重构 `codex2api.exe`、重启服务生效。
- 已修复新开 Codex 对话首次 `Reconnecting... / 无可用账号`：`GET /responses` WebSocket 入口现在能正确识别 direct OpenAI Responses API 账号，并转发到该账号自己的 `/v1/responses` HTTP/SSE 上游，不再错误走 Codex WS 通道。

## 当前完整请求流程
- Codex 客户端新开对话通常先连本服务 `GET /responses` WebSocket；普通 HTTP 调用走 `POST /responses` 或 `/v1/responses`。
- 服务先按请求模型、API Key 限制、账号状态、模型白名单、冷却状态筛选账号；当前运行态可用账号以 `/health` 的 `available/total` 为准。
- 如果选中的是 direct OpenAI Responses API 账号，服务会把 WebSocket 入站消息转换为普通 `/v1/responses` 请求，删除 WS 专用的 `type=response.create` 字段，再发给 direct 上游。
- 如果选中的是 Codex/OAuth 账号，服务走 Codex 上游通道；WS 缺少终止事件、连接异常、上游 `no_available_account` 等会在未输出正文前触发重试/换号。
- `POST /responses` 对 direct OpenAI Responses API 账号有首字超时保护：超过阈值仍没有首个有效事件，会软排除当前账号并静默切到下一个账号。默认首字超时切号预算很小，避免两个小账号互相重试打爆。
- `GET /responses` WebSocket 当前会对上游 HTTP 错误、连接异常、`no_available_account` 走重试/换号；direct API 分支主要依赖上游返回错误或断流触发重试，没有单独的“定时首字超时切号”。
- 如果只是上游慢但最终正常返回 `200/response.completed`，服务不会强行切号；只有慢到超时、断流、上游报错、账号不可用时才切。

## 变更决策
- 首字慢只做软失败与有限换号，不再无限切号/循环连接，避免把一个慢请求放大成重试风暴。
- 调度队列上限加入系统设置，`0` 表示自动模式（按可调度账号数 × 3，最小 3），`>0` 表示固定上限。
- 区分硬失败与软失败：401/403/明确 429 走硬排除，首字慢/首包前断流走软排除并回收；已输出正文后禁止透明重试，只做稳定失败收口。
- TTFT/首字表现进入调度评分，优先把请求分给更快的账号，减少小池子抖动。

## 待办事项 (Next Steps)
- [ ] 观察真实流量下 `dispatch_queue_limit=0` 的自动队列长度是否过大，必要时把后台默认值收紧到更适合小池子的固定值。
- [ ] 继续监控首字超时、队列等待和 `429` 触发频率，确认没有新的循环重连或假性“无账号可用”。

## 关键上下文
- 目录: C:\Users\Administrator\Desktop\codex2api
- 主要文件: C:\Users\Administrator\Desktop\codex2api\proxy\handler.go, C:\Users\Administrator\Desktop\codex2api\auth\store.go, C:\Users\Administrator\Desktop\codex2api\admin\handler.go
