# 最新接续状态 (2026-08-01 16:20)

## 核心进展
- Bug 修复型：Responses 续链已收紧为“无完整历史不剥离 `previous_response_id`”。当前关键修复在 `proxy/responses_ws.go`，待处理 WS 回归用例后再完整验证、构建和热更新。

## 核心动机与背景 (Motivation & Background)
- 审计发现 WS 上游不支持或缺少终止事件时会转 HTTP；旧逻辑调用 `expandPreviousResponse` 后无条件删除 `previous_response_id`。
- `responseCache` 仅缓存工具调用需要的局部上下文，不能证明完整会话历史；缓存未命中或祖先消息缺失时会静默发送无指针 HTTP 请求，造成续链上下文丢失。
- 此错误会放大为上游的会话/终端/工作区权限类错误。代理未硬编码该错误文本，且会透传上游错误，因此不能宣称已消除。
- 项目规则要求 HTTP/WS 对称、原账号 Cooling 时等待不盲换、只在 Disabled/401/403/删除后安全切号、禁止 409 死锁、真实 A->B 两连包均 200 才可验收。

## 关键设计与实现 (Implementation & Decisions)
- `proxy/translator.go`、`proxy/executor.go`：已移除普通 Codex 请求路径对 `previous_response_id` 的无条件删除。
- `proxy/responses_continuity.go`：Codex/第三方有显式 `previous_response_id` 时必须先走本地完整历史回放；历史不可验证时返回显式 `continuation_context_unavailable`，不再当作新会话发送。
- `auth/store.go`、`proxy/retry_exclusions.go`、`proxy/handler.go`、`proxy/responses_ws.go`：已新增严格会话亲和选择和等待。账号未 Disabled 时保持绑定；401/403 或 Disabled 才解绑。HTTP/WS 首字失败回归测试已覆盖同账号重试。
- 当前 WS 修复：`prepareOpenAIResponsesHTTPBodyFromWebSocket` 改为三返回值，只有 `buildOpenAIResponsesContinuationFallback` 从续链注册表成功物化完整历史时才删除指针；否则调用方写 `response.failed`，错误码 `continuation_context_unavailable`，且不请求 HTTP 上游。
- 当前 WS 修复：`prepareOpenAIResponsesWebSocketContinuation` 不再在账号选择前预先回放；先保持原生 WS，只有需要降级到 HTTP/切号时才要求完整历史物化。
- 新增回归：`TestResponsesWebSocketFallbackWithoutContinuationHistoryFailsExplicitly`。旧代码红灯证据：WS 400 后实际请求 HTTP 且下游收到 `response.completed`；修复目标为 HTTP 调用数 0、下游 `response.failed` 503。

## 验证证据
- 红灯：`go test ./proxy -run '^TestResponsesWebSocketFallbackWithoutContinuationHistoryFailsExplicitly$' -count=1`，旧逻辑失败，日志显示 WS 400 后 HTTP fallback，断言实际收到 `response.completed`。
- 中间定向测试曾通过：未知历史显式失败、未来工具字段保留、WS 空 400 回退；该结果发生在后续“只信任完整续链注册表”收紧之前，不能作为最终验收。
- 当前验证：`go test ./proxy -run '^TestResponsesWebSocket' -count=1` 仍失败。需先按新安全语义调整/补齐下列测试及对应逻辑：
  - `TestResponsesWebSocketFallbackRequestErrorPreservesTransportFailureWithoutAccountRetry`
  - `TestResponsesWebSocketOtherCloseAndEOFDoNotUseShortHTTPFallback`
  - `TestResponsesWebSocketOwnerUnavailableReportsIncompleteContinuation`（此前也有 2 秒读超时记录，需单独定位）
- 更新后的运行实例：`codex2api.exe` PID `36488`，监听 `127.0.0.1:18080`，`GET /health` 返回 200。该实例早于当前未构建的源代码修改，不能视为本轮修复已上线。
- `GET /v1/models` 无认证返回 401；未读取或暴露任何 API Key，因此尚不能对真实服务执行授权 A->B 两连包端到端测试。

## 待办事项 (Next Steps)
- [ ] 逐个定位并修正当前失败的 WS 回归用例；区分应改为“历史缺失 503”的旧预期与真实实现缺陷。
- [ ] 补充真实 WS 两连包机器人：包 A 获得 `response_id`，包 B 携带该 ID；分别覆盖完整历史安全降级 200 和未知历史 503/HTTP 调用数 0。
- [ ] 运行 `go test ./proxy -count=1`、`go test ./... -count=1`、`go vet ./...`、`git diff --check`。
- [ ] 构建并热更新新二进制前，确认 `/health` 的 `responses_memory.inflight_requests == 0`；热更新后用授权测试 Key 执行真实 A->B 两连包，并核验两条 200 使用日志及错误日志为零。

## 关键上下文
- 目录: `C:\Users\Administrator\Desktop\codex2api`
- 主要文件:
  - `C:\Users\Administrator\Desktop\codex2api\.agent\rules\README.md`
  - `C:\Users\Administrator\Desktop\codex2api\proxy\responses_ws.go`：`fallbackToHTTP`、`prepareOpenAIResponsesHTTPBodyFromWebSocket`、`forwardResponsesWebSocketTurn`
  - `C:\Users\Administrator\Desktop\codex2api\proxy\responses_continuity.go`：`activateOpenAIResponsesContinuationFallback`、`buildOpenAIResponsesContinuationFallback`、`prepareOpenAIResponsesWebSocketContinuation`
  - `C:\Users\Administrator\Desktop\codex2api\proxy\handler.go`：HTTP Responses、`writeContinuationContextUnavailable`
  - `C:\Users\Administrator\Desktop\codex2api\auth\store.go`：`NextForStrictSessionWithFilter`、`WaitForStrictSessionAvailableWithFilter`
  - `C:\Users\Administrator\Desktop\codex2api\proxy\responses_transport_cache_test.go`：WS fallback 安全回归
