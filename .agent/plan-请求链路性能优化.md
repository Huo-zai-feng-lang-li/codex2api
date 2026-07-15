# 请求链路性能优化

更新时间：2026-06-12 20:32:00 +08:00

## 目标

- 压低反代请求链路自身开销，让默认安全能力不拖慢正常请求。
- 保留命中风险后的安全事件与原文取证闭环。

## 取证结论

- 直连 key 少一次本地反代跳转，理论上天然更快；本项目优化目标是把反代自身的额外开销压到很低。
- 请求提示词改写是一次 JSON 改写，不是主要慢点。
- 请求前 DLP 扫描是一次性同步扫描，不是持续慢点。
- 流式响应路径之前每个 SSE 片段都会走安全扫描，并且 `hit_raw` 模式下无命中也会累计响应体，容易在长流式响应中放大 CPU/内存开销。
- 默认内置上游安全扫描之前也走 `ScanTimeout` goroutine/channel 包装；对本地纯 CPU 扫描属于多余热路径成本。

## 已完成修复

- `proxy/upstream_guard.go`
  - `hit_raw` 默认模式下，无命中流式响应不再累计完整 SSE 响应体。
  - 命中风险或 `full_raw` 模式仍保留响应体取证能力。
  - 默认内置扫描器直跑，避免每次扫描创建 goroutine/channel。
  - 自定义扫描器继续保留超时保护，慢扫描仍会记录 scanner error 并放行。
  - `security_audit_enabled=false` 时不再复制请求体、不再缓存响应体。
  - `upstream_guard_mode=off` 且非 `full_raw` 时不再复制请求体。

- `proxy/upstream_guard_test.go`
  - 新增 `TestUpstreamGuardAuditDoesNotBufferAllowedHitRawStream`。
  - 新增 `TestUpstreamGuardDefaultScannerBypassesTimeoutWrapper`。
  - 新增 `TestUpstreamGuardAuditDisabledDoesNotCopyRequestBody`。
  - 新增 `TestUpstreamGuardAuditOffHitRawDoesNotCopyRequestBody`。
  - 保留并验证命中后完整取证、最终 JSON 取证、自定义扫描超时保护。

- `security/promptfilter/filter.go`
  - 提示词过滤未启用时，直接返回 allow，不再提取/遍历 JSON 文本。

- `security/promptfilter/filter_test.go`
  - 新增 `TestInspectSkipsInvalidJSONWhenDisabled`，覆盖默认关闭时不做文本提取。

## 真实接口测速

- 本地反代：`http://127.0.0.1:18080`，`/health` 正常，可用账号 2/4。
- 当前线上设置：`prompt_filter_enabled=0`、`security_audit_enabled=0`、`upstream_guard_mode=warn`、`security_capture_mode=hit_raw`。
- 用户修正测试口径：当前直连 API 与反代内配置的上游是同一个 API，本轮以反代入口自身稳定性为主，不把账号池中的另一个上游当作公平直连对照。
- 反代入口复测：
  - `/health` 5 次全部 200，平均 8.4ms，p50 8.1ms。
  - `/v1/models` 5 次全部 200，平均 9.7ms，p50 12.5ms。
  - `/v1/responses` 非流式小输出 8 次全部 200，平均 2342.5ms，p50 2048.2ms，最小 1409.0ms，最大 3711.8ms。
  - `/v1/responses` 流式首行 5 次全部 200，平均 6334.4ms，p50 2803.8ms，最小 1527.7ms，最大 19233.6ms；平均值被单次上游抖动明显拉高。
- 用户提供直连上游 `https://vip-sg.freemodel.dev` 后复测：
  - 本地库中账号 553 与该 host 一致，但存储的上游 key 与用户本轮提供的直连 key 不一致；本轮不修改线上配置，只做现状测速。
  - 反代本轮新增 12 条生成日志全部命中账号 553，`upstream_endpoint=https://vip-sg.freemodel.dev/v1/responses`，没有路由到另一个 host。
  - 直连 `/v1/models` 5 次全部 200，平均 594.4ms，p50 668.6ms；反代 `/v1/models` 5 次全部 200，平均 16.6ms，p50 15.5ms。
  - 直连 `/v1/responses` 非流式小输出 8 次全部 200，平均 4665.3ms，p50 4012.6ms，最小 1689.9ms，最大 9993.6ms。
  - 反代 `/v1/responses` 非流式小输出 8 次全部 200，平均 2181.1ms，p50 2045.3ms，最小 1561.1ms，最大 3560.9ms。
  - 直连 `/v1/responses` 流式首行 5 次全部 200，平均 2935.8ms，p50 2755.7ms。
  - 反代 `/v1/responses` 流式首行 5 次全部 200，平均 1808.6ms，p50 1817.4ms。
  - 反代服务端日志显示 12 次 vip-sg 生成请求服务端耗时平均 2621.8ms，p50 2220.5ms，first token p50 1658ms。
- 2026-06-12 22:49 重启替换服务：
  - 旧进程 PID 20428，旧 exe 已备份为 `codex2api.prev-20260612-224906.exe`。
  - 新进程 PID 1472，新 exe 时间 `2026-06-12 22:48:35`，大小 67667968。
  - `/health` 5 次全部 200，平均 4.0ms，p50 2.2ms。
  - `/v1/models` 5 次全部 200，平均 2.5ms，p50 2.2ms。
  - `/v1/responses` 非流式小输出 10 次全部 200，平均 4679.9ms，p50 3519.8ms，最小 2242.8ms，最大 13474.7ms。
  - `/v1/responses` 流式首行 6 次全部 200，平均 4719.6ms，p50 4251.7ms，最小 1461.2ms，最大 8508.0ms。
  - 最近 25 条 `gpt-5.4-mini` 日志里，账号 553/vip-sg 7 条 p50 1857ms，账号 622/api.freemodel.dev 18 条 p50 3497.5ms；生成慢点主要来自上游账号/线路波动。
- `/v1/models`：
  - 反代 3 次全部 200，平均 8.8ms，p50 4.0ms。
  - 直连账号 551 上游 3 次全部 200，平均 467.1ms，p50 406.4ms。
- `/v1/responses`，模型 `gpt-5.4-mini`，小输出非流式：
  - 账号 551 直连返回 402，用量已达上限，不能作为有效速度对照。
  - 账号 553 直连成功 3 次，平均 7097.9ms，p50 8591.1ms。
  - 反代成功 3 次，平均 3846.1ms，p50 3672.7ms。
- 说明：本次接口测速连接的是当前运行中的 `codex2api.exe`。源码优化已完成并通过测试，但未在本轮重启替换线上进程；要验证优化后的真实线上延迟，需要重新构建并重启服务后再跑同一组测速。

## 验证结果

- `go test ./proxy -run 'TestUpstreamGuard(AuditDoesNotBufferAllowedHitRawStream|AuditCapturesWholeRequestAndStreamAfterHit|AuditCapturesFinalJSONWithoutIntermediateSSE|ScannerTimeoutRecordsEventAndAllows|DefaultScannerBypassesTimeoutWrapper)' -count=1 -v`：通过。
- `go test ./proxy -count=1`：通过。
- `go test ./security/upstreamguard -count=1`：通过。
- `go test ./security/promptfilter -count=1`：通过。
- `go test ./auth ./admin ./database -count=1`：通过。
- `go test ./... -count=1`：通过。

## 后续建议

- 若继续追求极限延迟，下一步应加请求链路分段耗时日志：入站解析、账号选择、请求改写、上游请求、首 token、响应扫描、落库。
- 不建议关闭安全能力换速度；正确方向是让安全能力按风险付费，正常流量低成本通过。

## 2026-07-15 Responses 热路径二次优化

### 目标

- 保持 Responses HTTP、Compact、WebSocket 和重试语义不变，压低项目自身的请求前置开销。
- 提升突发并发后的上游连接复用率，避免项目侧重复建连和 TLS 握手。

### 取证结论

- `Handler.Responses` 与 `Handler.ResponsesCompact` 在账号选出前同时构建 Codex 和 OpenAI 两份请求体；Codex 账号请求会白做一次 OpenAI JSON 解析、规范化和序列化。
- `newCodexStandardTransport` 每账号、每代理、每传输模式只保留 1 条空闲连接且 30 秒回收；Go 官方 `net/http.Transport` 文档确认连接池可并发复用，并由 `MaxIdleConnsPerHost`、`IdleConnTimeout` 控制。
- 采用按需构建并缓存 OpenAI 请求体；只有实际选中 OpenAI Responses 账号时才支付转换成本，重试继续复用，源请求变化时显式失效。
- 标准传输连接池调整为每上游保留最多 16 条空闲连接、空闲 90 秒回收；仍保持账号隔离和 5 分钟客户端 TTL 淘汰。

### 验证门槛

- 新增请求体转换基准，记录优化前后 `ns/op`、`B/op`、`allocs/op`。
- 新增延迟构建缓存契约和连接池配置测试。
- 通过 `go test ./proxy -count=1`、`go test ./... -count=1`、`go build`、热替换后 `/health`。

### 最终闭环结果

- `go test ./... -count=1`：通过，覆盖全部 Go 包。
- `go test ./proxy -run '^$' -bench 'BenchmarkPrepare(ResponsesBody|OpenAIResponsesBody)$' -benchmem -count=3`：通过；`PrepareResponsesBody` 为 `58952-64769 ns/op`、约 `20282-20291 B/op`、`434 allocs/op`，`PrepareOpenAIResponsesBody` 为 `47487-48789 ns/op`、约 `17161-17164 B/op`、`384 allocs/op`。
- `git diff --check`：无空白错误，仅工作区既有 LF/CRLF 提示。
- `go build -o codex2api.new.exe .`：通过；新产物 SHA256 为 `8C816593AEE71EA2202A9B71A27FE917FB5BC1D6FD62729B88DACF3BEF3F0B8F`。
- 已热替换运行服务：停止旧 PID `10480`，启动新 PID `19996`，`codex2api.exe` SHA256 为 `8C816593AEE71EA2202A9B71A27FE917FB5BC1D6FD62729B88DACF3BEF3F0B8F`。
- 热替换后 `/health` 返回 HTTP 200、`{"available":4,"status":"ok","total":21}`，监听 `127.0.0.1:18080` 的 PID 为 `19996`。
