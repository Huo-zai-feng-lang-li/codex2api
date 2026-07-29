# 线程续问上下文修复

## 目标
修复同一显式线程标识下，客户端遗漏 `previous_response_id` 时被调度到新账号而丢失上下文的问题。

## 取证
- `proxy/handler.go` 仅以请求自带 `previous_response_id` 绑定响应所属账号。
- 续链缓存已保存响应 ID、所属账号和可回放历史，但未建立线程标识到最新响应 ID 的索引。
- 重编辑重发能够恢复上下文，表明客户端重放路径与普通续问携带的续链信息存在差异；服务端缺少兼容兜底。

## 实施
1. 修正 `ResolveSessionID`：`Idempotency-Key` 是单请求去重键，不再充当线程身份。
2. 仅以显式 `Session_id`、`Conversation_id`、`prompt_cache_key` 作为线程键，缺省时回退 API Key 稳定键。
3. 保持客户端完整历史和现有 Responses 续链缓存原样，避免服务端猜测线程并造成串话。
4. 增加回归测试，验证不同幂等键不会拆分同一 API Key 会话。

## 验证
- `go test ./proxy -run ThreadContinuation`
- `go test ./... -count=1`
- `go vet ./...`
- `git diff --check`
