# 最新接续状态 (2026-08-03 17:20)

## 核心进展
- 已对当前最新运行服务完成只读验收：PID `33776`，二进制 SHA256 `CB592B25E060D241491D14571237438E823FBDF5809FECDB5C30BA63FDB08044`，监听 `127.0.0.1:18080` 与 `127.0.0.1:1455`；未修改代码。
- 真实 HTTP 机器人两连包通过：`POST /v1/responses` 首包与携带 `previous_response_id` 的续问均 HTTP 200，响应状态 `completed`。
- 本地 WS 入站握手成功，但当前运行期间未收到终止事件；上游反复 `websocket_unsupported`/400 后降级 HTTP，账号 652 多次出现上游流提前结束 598。
- 运行态续链持久化计数由 0 升至 23，出现 `context deadline exceeded`；当前 `/health` 仍 `status=ok`，但 `inflight_requests=1`、续链内存约 55 MB。
- 已完成本轮防封关键缺陷修复：WSS ALPN/ALPS 自洽、代理失败禁直连、代理拨号取消、账号画像隔离、HTTP/WS UA 与 Version 复用，以及启动脚本默认启用稳定画像/WS UA。
- 阶段计划已更新：`.agent/plan-账号防封全景端到端测试.md`。相关 uTLS/WS 文件原本已有用户暂存改动，本轮仅做增量修改，未重置或覆盖暂存内容。
- 上一轮运行服务 PID/哈希已被本轮构建替换；当前运行实例以本节首条 PID 与 SHA256 为准。
- 当前 `/health`：`status=ok`、`available=6`、`total=45`、`continuity_persistent=true`；`inflight_requests=1`，`continuity_persistence_failures=23`。数据库仍有 45 个未删除 Responses API 账号。

## 核心动机与背景 (Motivation & Background)
- 用户关心的不是“代码里是否用了 uTLS”，而是真实上游看到的跨层画像是否自洽、稳定并按账号隔离；本轮不评价所有账号共用同一出口 IP。
- 根因已数字化复现：
  - TLS 层使用 uTLS v1.8.2 的 `HelloChrome_Auto`，当前映射 Chrome 133；公开回显实测 TLS 1.3、HTTP/2，JA3=`1e0f7585d3e4a977ac6efe7021f88062`，JA4=`t13d1516h2_8daaf6152771_d8a2da3f94cd`。
  - HTTP/2 仍由 Go `x/net/http2` 发出，实测 SETTINGS 与伪头顺序为 Go 画像（Akamai 顺序 `a,m,p,s`），形成“Chrome TLS + Go H2”的跨层混合指纹。
  - WSS 原先用 Chrome ClientHello 报价 `h2,http/1.1`，gorilla/websocket 却执行 HTTP/1.1 Upgrade；现已用 WSS 专用 spec 固定 `http/1.1` 并移除 ALPS 新旧码点。
  - 启动脚本现已设置 `CODEX_WS_SEND_USER_AGENT=true` 与 `STABILIZE_DEVICE_PROFILE=true`；HTTP/WS 共用同一账号画像和版本解析。
  - uTLS 非空代理配置解析失败现显式报错；HTTP CONNECT/SOCKS/WSS 拨号尊重 context 取消，空代理仍直连。
- 真实链路取证：
  - 本地 `POST /v1/responses` 低频真实请求返回 HTTP 200、`completed`。
  - 上一轮本地 WebSocket 入站单轮与两连包曾收到 `response.completed`；本轮最新运行实例复测未收到终止事件，当前以本轮 WS 证据为准。
  - 最近一天已有 22 条 `wss://api.daseinai.xyz/v1/responses`、状态 598 的记录，与公开 WSS 协议复现方向一致。

## 关键设计与实现 (Implementation & Decisions)
- 修复目标必须是一个版本化、跨层一致的协议档案，而不是继续堆独立开关：
  - WSS：单独固定 ALPN 为 `http/1.1`，确保 gorilla Upgrade 与 TLS 协商一致；HTTP uTLS 仍允许 `h2`。
  - HTTP/2：先用回归测试锁定当前 SETTINGS/伪头顺序；若目标是完整 Chrome H2 画像，需要可控 SETTINGS、流控和 header order 的实现，不能仅靠 uTLS。
  - Header/设备画像：HTTP 与 WS 共用同一解析结果，保持 `User-Agent`、`Version`、`Originator`、`X-Stainless-*` 同账号稳定；禁止把下游任意 UA 直接拼成上游画像。
  - 账号隔离：连接池键与画像缓存必须包含账号/代理/会话维度；代理配置错误不得静默直连。
  - 续链/换号：任何传输修复都不能改变 `proxy/responses_ws.go` 的完整历史回放、owner 绑定、HTTP preference、失败账号排除与显式失败语义。
- 已按 TDD 完成 RED/GREEN：WSS ALPN、ALPS、无代理旁路、代理取消、账号画像隔离、HTTP/WS UA/Version 复用均有回归测试。
- 现有暂存改动涉及 `proxy/executor.go`、`proxy/usage_wham.go`、`proxy/utls_transport.go`、`proxy/wsrelay/manager.go`，以及其他前后端/启动脚本文件；不要重置、覆盖或重新暂存用户已有改动。
- 旧的 Responses 续链设计仍是强约束：HTTP/WS 双通道对称；owner 不可用或 WS 转 HTTP 前必须物化完整本地历史；历史不完整返回 `continuation_context_unavailable`；默认滑动 TTL 24 小时。

## 验证证据
- 最新服务只读探针：`GET /health` HTTP 200，`available=6`、`total=45`、`continuity_persistent=true`；当前 `continuity_persistence_failures=23`。
- 真实 HTTP A/B：首包与续问均 HTTP 200、`completed`；本地 `/v1/models` 带 API Key 返回 HTTP 200（33 个模型）。
- 真实 WS：本地握手 101；测试窗口内未收到 `response.completed`，连接最终 keepalive ping timeout；日志与 usage 记录显示上游 WS 400/598，实际走 HTTP fallback。
- 当前 `logs/start.err.log` 非空（约 104 KB，包含启动/请求日志）；`logs/start.out.log` 为 0 字节。
- 公开 uTLS 回显：HTTP 200；TLS 1.3；`RESP_PROTO=HTTP/2.0`；上述 JA3/JA4 已记录。
- 修复后公开 WSS 回显（经 `127.0.0.1:51081`）：HTTP `101`，ALPN=`http/1.1`。
- `go test ./... -count=1`：全部包 PASS；`go vet ./...`：无输出；`git diff --check`：无空白错误，仅 LF/CRLF 提示。
- 验证构建 SHA256：`2CECF500C07A374C39718E6A53D7D2C1A872AE48103D141CE98757D1B3236930`；验证文件已删除。
- 真实 HTTP：200/`completed`；本轮真实本地 WS 握手 101 但未收到终止事件，日志与 usage 显示 HTTP fallback/上游 WS 400/598。
- CodeGraph：338 个文件、8571 个节点、23437 条边；关键调用链为 `ResponsesWebSocket -> forwardResponsesWebSocketTurn -> ExecuteOpenAIResponsesWebSocketRequest/HTTP fallback`。
- 外网工具状态：`agent-reach` CLI 本机缺失；内置 web 搜索返回 404。已切换为 `127.0.0.1:51081` 代理直取、公开协议回显、本地依赖源码与真实链路交叉取证。

## 待办事项 (Next Steps)
- [ ] 修复续链 SQLite 持久化超时，先将 `continuity_persistence_failures` 稳定保持为 0，再复测高并发场景。
- [ ] 定位上游 WSS 400/598：确认 endpoint 的 WS 能力与 ALPN/协议协商，确保本地 WS 收到终止事件后再宣称 WSS 闭环。
- [ ] 待 `inflight_requests=0` 后再次执行 HTTP A/B 与 WS A/B，并核对 `usage_logs` 200 流水和运行日志。
- [x] WSS uTLS 固定 HTTP/1.1，移除 h2 ALPS，并覆盖两条 WSS 调用链。
- [x] 代理配置错误显式失败，代理拨号支持 context 取消；空代理保持直连。
- [x] HTTP/WS 共用账号画像、UA 与 Version；跨账号缓存按上游账号身份隔离。
- [x] 全量测试、vet、diff、构建和公开 WSS 探针通过；无测试孤儿进程或临时产物。
- [x] 用户已替换为新构建；本轮已执行真实 HTTP A/B 与本地 WS 探针，WS/续链持久化仍有运行态问题。
- [ ] 若继续追求“完整 Chrome HTTP/2”，需引入可控 SETTINGS/流控/伪头顺序实现；当前仍是 Chrome TLS + Go H2，不作完整 Chrome 声明。

## 关键上下文
- 目录: `C:\Users\Administrator\Desktop\codex2api`
- 主要文件:
  - `C:\Users\Administrator\Desktop\codex2api\.agent\rules\README.md`
  - `C:\Users\Administrator\Desktop\codex2api\.agent\plan-账号防封全景端到端测试.md`
  - `C:\Users\Administrator\Desktop\codex2api\proxy\utls_transport.go`：`NewUTLSTransport`、`NewUTLSNetDialTLSContext`、`IsUTLSEnabled`
  - `C:\Users\Administrator\Desktop\codex2api\proxy\executor.go`：`newCodexTransport`、`applyCodexRequestHeaders`、`ExecuteOpenAIResponsesWebSocketRequest`
  - `C:\Users\Administrator\Desktop\codex2api\proxy\device_profile.go`：`ResolveDeviceProfile`、`ApplyDeviceProfileHeaders`
  - `C:\Users\Administrator\Desktop\codex2api\proxy\wsrelay\executor.go`：`prepareWebsocketHeaders`
  - `C:\Users\Administrator\Desktop\codex2api\proxy\wsrelay\manager.go`：`createConnection`
  - `C:\Users\Administrator\Desktop\codex2api\proxy\responses_ws.go`：`forwardResponsesWebSocketTurn`、transport preference/fallback
  - `C:\Users\Administrator\Desktop\codex2api\auth\utls_client.go`：auth uTLS + Go HTTP/2 混合画像
  - `C:\Users\Administrator\Desktop\codex2api\proxy\executor_test.go`
  - `C:\Users\Administrator\Desktop\codex2api\proxy\device_profile_test.go`
  - `C:\Users\Administrator\Desktop\codex2api\proxy\wsrelay\executor_test.go`
