# HTTP2 上游流重置归因与修复

## 目标
- 排查 `stream disconnected before completion: 上游流读取失败: stream error: stream ID 17; INTERNAL_ERROR; received from peer`。
- 区分官方/第三方上游账号或网关问题与本地系统代码问题。
- 确保本地代码不主动制造或放大这类错误，也不把底层 HTTP/2 帧错误原样暴露给反代客户端。

## 取证结论
- 底层错误是 HTTP/2 流被对端 `RST_STREAM(INTERNAL_ERROR)` 重置。
- 这不是 400 参数错误，也不是直接的账号订阅错误；更接近上游网关、出口网络、HTTP/2 连接状态或上游服务异常。
- 本地代码原先存在两个放大点：
  - `shouldRecyclePooledClient` 未识别 `stream error: stream ID ... INTERNAL_ERROR`，坏连接可能不被主动回收。
  - Codex 上游请求手动设置 `Connection: Keep-Alive`，这是连接级头，对 HTTP/2 上游不应发送。
- 本地代码原先还会把 `stream ID ... INTERNAL_ERROR` 原样包装进 `response.failed`，导致反代客户端看到底层传输细节。

## 已修改
- `proxy/executor.go`
  - 将 `stream error: stream id`、`internal_error`、`http2:` 纳入连接池回收判断。
  - 移除上游 Codex 请求头 `Connection: Keep-Alive`。
- `proxy/handler.go`
  - 新增流式错误对外归一化：底层 HTTP/2 `INTERNAL_ERROR` 对客户端显示为“上游 HTTP/2 流被对端重置，请重试或切换上游账号/出口”。
  - 日志与 usage 仍保留归类为上游传输失败。
- `proxy/executor_test.go`
  - 覆盖 HTTP/2 stream reset 应触发连接回收。
  - 覆盖上游请求不再设置 `Connection` 头。
- `proxy/handler_test.go`
  - 覆盖反代客户端不再收到 `stream ID 17` / `INTERNAL_ERROR` 原文。

## 验证
- `go test ./proxy -run "TestShouldRecyclePooledClient|TestResponsesStreamMasksHTTP2InternalStreamError|TestApplyCodexRequestHeadersUsesMinimalFallbackByDefault" -count=1`：通过。
- `go test ./proxy -count=1`：通过。
- `go test ./proxy/... -count=1`：通过。
- `go test ./... -count=1`：通过。

## 2026-06-28 17:44 批次 503 追加取证
- 截图中的 17:44 左右错误不是 `stream ID ... INTERNAL_ERROR`，而是大量上游 WebSocket 503。
- `usage_logs` 原始摘要显示：
  - `wss://api.freemodel.dev/v1/responses`：多账号返回 `ChatGPT Service Unavailable`。
  - `wss://vip-sg.freemodel.dev/v1/responses`：多账号返回 `HTTP 503`。
  - `wss://bmapi.020212.xyz/v1/responses`：大量 `websocket_missing_terminal`，随后部分账号出现 `403 payment_required`。
  - 另有 `429 rate_limited_model · Concurrency limit exceeded for user`。
- 结论：
  - 503/`ChatGPT Service Unavailable` 是上游服务或第三方网关/账号可用性问题，不是本地请求参数错误。
  - 本地代码侧已修复“把 HTTP/2 底层 `stream ID ... INTERNAL_ERROR` 原样透给客户端”和“坏连接不回收”的问题。
  - 如果 503 持续出现，应优先禁用对应上游账号/网关或降低并发，而不是继续改请求体。

## 影响范围
- 不改变 400/401/402/403/429 的账号、权限、额度、参数错误判定。
- 只处理传输层 HTTP/2 流重置类错误的连接回收和对外展示。
- 如果上游持续主动断流，客户端仍会收到 `response.failed`，但不会再看到底层 `stream ID ... INTERNAL_ERROR` 原文；应继续查上游账号、出口代理或第三方网关稳定性。
