# 账号防封全景端到端测试计划

## 目标与边界

- 目标：验证真实请求从本地入口到 OpenAI 上游的 TLS、HTTP/2、HTTP/WS 请求头、设备画像、会话与账号隔离链路，并给出可复验风险评级。
- 本轮不评估同出口 IP；不改账号代理配置。
- 真实账号请求必须串行、低频、最小 token，不做并发压测或诱发封禁实验。
- 不输出 access token、API key、账号名、代理凭据或完整请求正文。
- 已按用户确认进入修复闭环；所有生产改动先写失败测试，再做最小修复。

## 测试矩阵

1. 协议层：uTLS ClientHello、ALPN、TLS 版本/套件/扩展、JA3/JA4 回显、HTTP/2 SETTINGS/伪头与连接复用。
2. 应用层：HTTP 与 WebSocket 的 `User-Agent`、`Version`、`Originator`、`X-Stainless-*`、会话头及头部一致性。
3. 稳定性：同账号同配置画像稳定、不同账号画像隔离、重启后稳定、异常回退不泄漏环境代理。
4. 调度层：账号亲和、失败账号排除、连接池键、HTTP/WS 切号与续链对称性。
5. 真实链路：低频 HTTP、WebSocket、连续两包、可控换号；记录状态码、上游 request id、账号 ID 哈希和时序，不记录凭据。
6. 回归层：相关定向测试、`go test ./... -count=1`、`go vet ./...`、`git diff --check`。

## 阶段状态

### 阶段 1：现状基线（已完成）

- CodeGraph 已初始化：338 个文件、8571 个节点、23437 条边。
- 当前服务 PID 23724，监听 `127.0.0.1:18080` 与 `127.0.0.1:1455`。
- SQLite `accounts` 共 253 行；仅做了字段与数量取证，未读取或输出凭据。
- 代码存在 uTLS、HTTP/2、设备画像、HTTP/WS 头处理与账号/代理维度连接池实现。
- 工作树已有用户变更且相关文件处于暂存区；本轮不覆盖这些改动。
- 运行二进制为 dirty 构建，源码与运行版本不完全一致；本轮以源码测试和独立公网探针验证，不热替换现有服务。

### 阶段 2：协议层实测（已完成）

- 公网回显基线：TLS 1.3、HTTP/2，JA3 `1e0f7585d3e4a977ac6efe7021f88062`，JA4 `t13d1516h2_8daaf6152771_d8a2da3f94cd`。
- 根因确认：WSS 的 Chrome ClientHello 同时报价 `h2,http/1.1`，服务端选中 h2 后 gorilla/websocket 仍按 HTTP/1.1 Upgrade 解析，稳定复现 malformed response。
- TDD 修复：WSS 专用 ClientHello 只报价 `http/1.1`，同时移除 ALPS 新旧码点；HTTP uTLS 路径继续使用 h2。
- 公网 VPN 代理验证：`wss://echo.websocket.org/` 返回 `101`，协商 ALPN=`http/1.1`。
- 已知边界：HTTP uTLS 仍为 Chrome TLS ClientHello + Go `x/net/http2` 帧特征，不宣称完整 Chrome HTTP/2 指纹。

### 阶段 3：应用层与隔离实测（已完成）

- 设备画像缓存键改为上游账号身份优先，调用方 API key 仅在账号身份缺失时兜底，消除多账号共用 API key 时的画像串用。
- WS 头构建改为传递真实账号对象，与 HTTP 路径复用同一账号画像缓存；UA 与 `Version` 使用同一画像解析结果。
- 两个 Windows 启动脚本均启用 `CODEX_WS_SEND_USER_AGENT=true` 和 `STABILIZE_DEVICE_PROFILE=true`。
- 非空代理配置解析失败时显式报错，不再静默直连；WSS 的 TCP/HTTP CONNECT/SOCKS 拨号尊重请求取消与超时；空代理仍按本轮边界直连。

### 阶段 4：真实上游 E2E（已完成基线，待用户用新构建验收）

- 修复前已完成真实 HTTP、客户端 WebSocket、连续两包；均成功但连续两包被 HTTP 偏好/回退掩盖，不能作为 WSS 修复证据。
- 修复后已完成独立公网 WSS 101/ALPN 探针；现有 18080 服务未热替换，避免中断用户当前进程。

### 阶段 5：回归与结论（已完成）

- `go test ./... -count=1`：全部包通过。
- `go vet ./...`：通过，无输出。
- `git diff --check`：无空白错误，仅 Git 的 LF/CRLF 提示。
- 新增协议隔离、无代理旁路、账号画像隔离、HTTP/WS 画像复用回归测试。

## 判定标准

- 通过：有运行结果或抓取回显证明，且 HTTP/WS、同账号稳定性与跨账号隔离均符合预期。
- 部分通过：源码/单测成立但缺少真实网络证据，或运行二进制与源码版本不一致。
- 失败：指纹随机漂移、HTTP/WS 画像不一致、跨账号共享不应共享的状态、回退到 Go 默认指纹、凭据/环境代理泄漏，或真实链路出现可复现异常。
