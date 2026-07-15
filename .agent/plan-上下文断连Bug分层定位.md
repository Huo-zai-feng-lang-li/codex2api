# 上下文断连 Bug 分层定位计划

## 目标

定位“中断/结束后首次发送继续丢历史，第二次恢复”的责任边界，严格区分：

1. Codex 桌面端首次续接请求是否携带完整历史或 `previous_response_id`。
2. 本地 codex2api 是否改写、删减或跨账号转发续接状态。
3. 第三方 Responses 中转站是否支持官方续接协议。

## 阶段

- [x] 阶段 1：代码拓扑、运行配置、历史错误取证
- [x] 阶段 2：官方协议与第三方中转站最小直连验证
- [x] 阶段 3：本地入口/上游边界载荷形状取证
- [x] 阶段 4：建立最小复现并完成分层归因
- [x] 阶段 5：输出修复方向、影响面和人工验收步骤

## 当前证据

- 官方 OpenAI Responses HTTP 支持 `previous_response_id`；WebSocket 缓存未命中应明确返回 `previous_response_not_found`。
- `kaiycb.com` 同账号 HTTP 续接实测返回 400：`previous_response_id is only supported on Responses WebSocket v2`。
- `kaiycb.com` WebSocket 握手实测返回 403；`vip-sg.freemodel.dev` WebSocket 握手实测返回 401。
- 本地 OpenAI Responses 账号分支保留 `previous_response_id` 并通过 HTTP 转发，故上述 400 来自第三方中转站，不是入口验证伪造。
- 本地 `affinity_mode=bounded`，5 分钟或 50 次请求后主动解绑；失败/首字超时也会解绑并换账号。账号池同时包含多个中转域名。
- 现有日志持续出现第三方 `No tool call found...`、`continuation via previous_response_id is only supported on Responses WebSocket v2`、502 和首字超时换号。

## 最终归因

- 主因：第三方中转站不支持官方 Responses HTTP `previous_response_id` 续接，且宣称需要的 WebSocket v2 入口实际不可用。
- 本地适配缺口：OpenAI Responses HTTP 分支只透传第三方 400，没有像 WebSocket 入口一样做能力降级和本地工具上下文展开。
- 放大因素：bounded 亲和及透明换号允许响应 ID 跨账号/跨中转域迁移，服务端状态不能保证可用。
- 桌面端不是当前首要责任：现有证据没有证明首次请求遗漏历史；错误后的成功请求包含大体量完整输入，更符合客户端重建/重发历史的恢复行为。

## 证据闭环

- 第三方直连：普通请求 200，同账号 HTTP 续接 400。
- 本地代理：普通请求 200，续接请求向第三方透传后返回同一 400。
- WebSocket 能力：`kaiycb.com` 握手 403，`vip-sg.freemodel.dev` 握手 401。
- 本地调度：当前 bounded 模式，5 分钟/50 次请求/账号异常会解除亲和；日志已出现首字超时换号和跨账号重试。
- 历史错误：持续出现 `No tool call found` 与“仅支持 WebSocket v2”。

## 修复设计（2026-07-15）

### 约束

- 同一 `response.id` 的增量续接必须优先使用生成它的账号和 base URL，不能被 bounded 亲和或轮询迁移。
- 原账号不可用、第三方不支持 HTTP 续接时，只有在本地拥有完整链路时才允许删除 `previous_response_id` 并重建 `input`。
- 缓存缺失时明确保留上游错误，禁止静默按新对话继续。
- 无 `previous_response_id` 的普通请求、现有 Codex 账号转换、SSE/usage、重试和安全审计行为保持不变。

### 实现

- 新增进程内 continuation registry：按响应 ID 保存原账号、base URL 和已物化的完整历史快照。
- registry 使用不可变深拷贝、1 小时 TTL、条目数和总字节双上限；仅 `response.completed`/完整 JSON 响应写入。
- 请求带 `previous_response_id` 时以响应归属收窄账号过滤器；第三方返回精确的 continuation 不支持错误时，仅重建并重试一次。
- 完整历史逐轮累积，移除响应 item 的 `id`，跳过不可安全回放的加密 reasoning；不写磁盘、不写 Redis。

### TDD 阶段

- [x] 红测：第三方 HTTP 拒绝续接后，应以本地完整历史重试并成功；修复前稳定返回 400。
- [x] 红测：关闭 session affinity 后，续接仍必须命中响应原账号；修复前稳定命中高分非归属账号并返回错误上下文。
- [x] 绿测：实现 registry、账号约束和一次性降级；覆盖非流式、流式、连续多轮、`store:false` 和缓存缺失保护。
- [x] 回归：定向测试、`go test ./proxy`、`go test ./...`、Windows 可执行文件构建均通过。
- [x] 运行态：新构建在隔离端口真实验证非流式与流式双轮续聊，均准确返回测试 token；日志确认触发本地历史回放且无运行异常。
- [ ] 部署：旧 PID 仍占用正式端口；磁盘 `codex2api.exe` 已更新，待旧进程真正退出后再启动即可生效。

## v2.2.6 发布（2026-07-16）

- [x] 将本轮修复归入 `v2.2.6` 变更日志。
- [x] 完成前后端构建、全量测试和新二进制哈希校验：`04C6891C1F5A49D4E1267C824122FE6E8FFFBD3827DB4AA37E0C684FC670003A`。
- [x] 隔离端口真实续链验收通过：首次续接准确返回 `RELEASE-226-Q5`；正式端口待热替换。
- [ ] 提交、打 `v2.2.6` 标签并通过 `127.0.0.1:51081` 代理推送。
