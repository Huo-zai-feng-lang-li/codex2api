# 上下文断连 Bug 取证笔记

## 已确认事实

### 本地请求链

- `proxy/handler.go` 的 OpenAI Responses 账号分支使用 `PrepareOpenAIResponsesBody` 和 `ExecuteOpenAIResponsesRequest`。
- `PrepareOpenAIResponsesBody` 不删除 `previous_response_id`。
- 普通 Codex 账号分支才会本地展开缓存并删除 `previous_response_id`；其缓存仅覆盖工具续链项，不是完整普通对话存储。
- 客户端断开后，本地处理器会继续读取上游直到终止事件，但 OpenAI Responses 账号分支依赖第三方保存响应状态。

### 本地调度

- 当前设置：`affinity_mode=bounded`、`max_retries=2`、`scheduler_mode=remaining_quota`。
- bounded 亲和在 5 分钟、50 次请求或账号不健康时解除。
- 首字超时和可重试错误会解除亲和并切换账号。
- 活跃 Responses 账号分布于 `vip-sg.freemodel.dev` 和 `kaiycb.com`，响应 ID 状态不能假定跨账号/跨中转域共享。

### 第三方中转站

- `kaiycb.com`：首次普通请求 200；同账号携带该响应 ID 的 HTTP 续接返回 400，明确声明仅支持 WebSocket v2。
- `kaiycb.com`：按官方路径和 Beta 头进行 WebSocket 握手返回 403。
- `vip-sg.freemodel.dev`：多个已配置账号的 WebSocket 握手返回 401。
- 历史日志中大量出现工具调用续链找不到、HTTP 不支持 `previous_response_id`、502 Bad Gateway。

### 本地代理最小复现

- 经 `http://127.0.0.1:18080/v1/responses` 首次请求返回 200，响应文本为测试标记 `CHARLIE`。
- 同一响应 ID 的第二次请求返回 400：`上游返回错误 (status 400): previous_response_id is only supported on Responses WebSocket v2`。
- 对应 usage 记录：账号 652，上游 `https://vip-sg.freemodel.dev/v1/responses`，错误类型为 client；说明本地代理没有伪造错误，而是把第三方能力限制透传给下游。

### 首次失败、后续恢复形态

- 多组 `previous_response_id` 400 后，紧接着出现同模型 200，成功请求输入量达到 5 万至 19 万 token。
- 该证据说明客户端具备重发完整历史的恢复路径；因并发会话存在，不能把每一组相邻记录强行认定为同一线程。
- 曾尝试用 PktMon 捕获 localhost:18080 的单次入站形状，但该环境未产生可解析的环回数据包；捕获已停止，临时文件已删除，因此未把这次尝试当作结论证据。

## 最终归因

- 第三方中转站协议不兼容是已证实的底层故障，信心 10/10。
- 本地 bounded 亲和与跨账号重试会放大服务端状态不可迁移问题，信心 9/10。
- 本地 OpenAI Responses HTTP 分支缺少能力探测/降级，是代理侧适配缺口，信心 9/10。
- “首次失败后客户端重建完整历史，导致第二次恢复”属于基于日志形态的 [Heuristic]，信心 8/10。

## 修复方向

1. 不要把第三方 Responses 账号当成官方完整协议实现；为每个 base URL 建能力画像。
2. HTTP `previous_response_id` 不可用时，代理应使用本地完整续接缓存重建 input，而不是直接跨账号重试。
3. 携带 `previous_response_id` 的请求必须绑定响应 ID 的原始账号/base URL；禁止 bounded 到期和首字超时透明迁移。
4. 无法恢复完整上下文时应返回明确 `previous_response_not_found`，禁止静默按新对话继续。
