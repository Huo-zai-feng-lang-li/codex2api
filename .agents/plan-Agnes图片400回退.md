# Agnes 图片 400 回退计划

## 目标

修复 `agnes-3.0-flash` 通过 codex2api 上传图片时返回 400 的问题，同时保留文本请求的既有 Responses 链路。

## 根因

- Cuckoo 发标准 OpenAI `/v1/chat/completions`，图片字段是 `image_url`。
- codex2api 把 `responses_api` 账号统一翻译成 `/v1/responses`，图片被转成 `input_image`。
- Agnes 的 `/v1/responses` 不识别图片内容块，错误把块类型标为 `unknown`，返回 400；同一 Key 的 `/v1/chat/completions` 支持图片并返回 200。
- 因此不是 Cuckoo 参数错误，而是网关对所有 `responses_api` 账号强制走 Responses 协议。

## 实施

1. 新增 `proxy/chat_completions_fallback.go`：
   - `chatRequestHasImage`：只识别原始 chat body 的 `image_url`。
   - `isImageContentRejection`：只接受 400，且错误体含 `unknown` / `image` / `vision` / `multimodal`。
   - `tryChatCompletionsFallback`：保留原始 chat body（含 `stream`、`tools`、`max_tokens`），只替换模型映射，直打同账号 `/v1/chat/completions`。
   - 成功时原样回传 JSON/SSE；失败时返回 false，继续原有 400 错误/重试/冷却路径。
2. `handler.go` 在 400 错误分支、释放账号之前调用回退；成功则释放账号并直接返回。
3. 计费日志按 chat `usage` 解析，标记为 retry attempt，上游端点记为 `/v1/chat/completions`。

## 验证

- 新增 3 个测试：非流式回退、流式 SSE 原样透传、纯文本 400 不触发回退。
- `go test ./...` 全过；`go vet ./...` 全过。
- 构建并热替换成功，`/health` ok。
- 真实 Agnes 非流式图片请求：200，回复“红色”，`image_tokens=64`。
- 真实 Agnes 流式图片请求：200，`text/event-stream`，回复“绿色”，`data: [DONE]` 正常。
- 号池：12 个官方 Key 全部 `/v1/models` 200；局域网账号 `856` 已软删除。

## 待办

- GitHub push 被本机 VPN `127.0.0.1:51081` 未启动阻断；开启代理后 `git push origin main`。
