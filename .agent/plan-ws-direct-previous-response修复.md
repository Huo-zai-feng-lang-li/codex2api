# WS direct previous_response_id 修复计划

## 目标
- 保留 Codex 客户端每次新消息触发的 `gpt-5.4-mini effort=low` 轻量摘要/会话维护请求，不误杀正常后台行为。
- 修复 `GET /responses` 本地 WebSocket 入口在选中 direct OpenAI Responses API 账号时，把会话态 WS 请求降级为普通 HTTP `/v1/responses` 后触发 `previous_response_id is only supported on Responses WebSocket v2` 的问题。
- 避免单次新消息被 400/重连放大为多条上游请求，降低并发占用与 429 风险。

## 已验证事实
- 活动请求面板的 `模型` 字段来自客户端 payload 的 `model`，服务端没有把主模型自动改成 mini 再补发的代码路径。
- 最近日志和 `usage_logs` 显示 mini 请求多为 `gpt-5.4-mini effort=low` 且状态正常，符合客户端轻量后台任务特征。
- 错误放大链路集中在 direct API 分支：`GET /responses` WS 入站 -> 选中 direct API 账号 -> 转为 HTTP `/v1/responses` -> 上游返回 `previous_response_id is only supported on Responses WebSocket v2`。
- 官方 Responses WebSocket 模式使用 `wss://.../v1/responses`，本地 WS 入站继续走上游 WS 比降级成 HTTP 更符合协议边界。

## 修复边界
- 不修改模型调度策略。
- 不禁止 mini 请求。
- 不修改 API Key 限额、账号白名单、冷却策略。
- 只修改 direct OpenAI Responses API 账号在本地 WS 入站下的上游转发方式与对应测试。

## 执行步骤
1. 写失败测试：direct API 本地 WS 入站带 `previous_response_id` 时，上游应收到 WebSocket `response.create`，不能再以 HTTP body 触发 400。
2. 跑定向测试，确认当前实现失败。
3. 实现 direct API WS 上游执行器：按账号 `base_url` 构造 `ws/wss /v1/responses`，透传 Authorization 与必要组织/项目头，保持本地 WS turn 的上游 WS 语义。
4. 将 `proxy/responses_ws.go` direct API 分支从 HTTP/SSE 调用切换到 direct WS 执行器，并复用现有 SSE/WS 事件收口、usage 记录和 active request 展示。
5. 跑定向测试与相关回归测试。
6. 跑 `go test ./... -count=1`。
7. 重建 `codex2api.exe`，重启本机服务。
8. 用真实日志、`/health`、SQLite `usage_logs` 验证：mini 仍存在但不再伴随连续 `previous_response_id` 400。

## 风险控制
- 如果上游 direct API 不支持 WS 握手，修复应返回清晰 upstream error，不能循环重试打爆账号。
- 如果 WS 分支输出了正文，禁止透明换号，避免重复内容。
- 如果需要保留 HTTP direct 分支，普通 `POST /responses` 不受影响，仅本地 `GET /responses` WS 入站改走 WS。

## 阶段记录
- 2026-06-02：完成问题复盘与修复计划落盘，准备进入 TDD。
- 2026-06-02：已写失败测试，确认旧实现会对 direct API 上游发 HTTP，测试收到 `websocket required` 红灯。
- 2026-06-02：已实现 direct API WS 上游执行器，并将本地 WS 入站的 direct API 分支切换到上游 WS；定向测试和相关回归测试已通过。
- 2026-06-02：重启后真实上游返回 `426/404`，说明 direct API 上游并非全量支持 WS；修复策略收窄为“无 `previous_response_id` 走旧 HTTP；有 `previous_response_id` 先试 WS，WS 不支持再降级为去掉 `previous_response_id` 的 HTTP”。
- 2026-06-02：已补三类回归测试：首条 direct 请求保持 HTTP、带 `previous_response_id` 时可走上游 WS、上游 WS 不支持时降级 HTTP 且移除 `previous_response_id`；`go test ./... -count=1` 已通过。
- 2026-06-02：已重建并重启 `codex2api.exe`；`/health={"available":2,"status":"ok","total":3}`；真实本地 WS 双 turn 烟测 completed，第二轮携带 `previous_response_id` 后成功降级并返回 200。
- 2026-06-02：复查发现前次方案仍会在真实 `gpt-5.5` 工具续轮触发 `function_call_output requires item_reference ids matching each call_id on HTTP requests; continuation via previous_response_id is only supported on Responses WebSocket v2`，根因是 WS 不支持后裸删 `previous_response_id` 降级 HTTP，未注入缓存里的历史 `function_call`。
- 2026-06-02：已补红灯测试覆盖该场景：direct WS 上游返回 426 后，HTTP fallback body 必须删除 `previous_response_id`，同时从本地 response cache 注入上轮 `function_call`，使当前 `function_call_output.call_id` 可匹配。
- 2026-06-02：已修复 `prepareOpenAIResponsesHTTPBodyFromWebSocket`，fallback 前先调用 `expandPreviousResponse` 展开缓存，再删除 `previous_response_id` 并同步刷新 `expandedInputRaw`，避免后续缓存链路继续残缺。
- 2026-06-02：`go test ./proxy -run "TestResponsesWebSocket(DirectOpenAIResponses|UsesDirectOpenAIResponses|DirectOpenAIResponsesWithoutPrevious|DirectOpenAIResponsesPreviousFallsBack|RetriesWrappedNoAvailableAccount)|TestExpandPreviousResponse|TestCacheCompletedResponse" -count=1` 通过；`go test ./... -count=1` 通过。
- 2026-06-02：已重建 `codex2api.exe` 并重启本机服务，当前 `/health={"available":3,"status":"ok","total":3}`；还需要用户再发一条真实 `gpt-5.5` 消息后复查日志，确认不再出现新的 `function_call_output requires item_reference` 400。
