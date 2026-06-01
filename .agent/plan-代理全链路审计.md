# 代理全链路审计

## 目标
- 账号相关上游请求遵守：账号代理 > 代理池 > 全局代理 > 直连。
- 未配置代理时显式直连，不读取系统 `HTTP_PROXY/HTTPS_PROXY`。
- 保留系统后台流量边界，不把账号代理池强行套到模型同步、订阅导入、reset radar、Resin 注册等系统面请求上。

## 已完成取证
- `auth.Store.ResolveProxyForAccount` 已承载账号/代理池/全局/直连优先级。
- `/v1/responses`、compact、image、anthropic、usage probe 主链路均通过 `resolveProxyForAttempt` 或 `ResolveProxyForAccount` 传入有效代理。
- OpenAI Responses 拉 `/v1/models` 原先只使用表单/账号行 `proxy_url`，未覆盖全局/代理池 fallback。
- `proxy/wsrelay.Manager.createConnection` 创建拨号器副本时未显式继承直连 `NetDialContext`，缺少抗环境代理回归测试。

## 已完成修复
- `admin.FetchOpenAIResponsesModels` 改为通过 `resolveOpenAIResponsesModelsProxy` 解析有效代理。
- `fetchOpenAIResponsesModelIDs` 显式 `transport.Proxy = nil`，空代理时强制直连。
- `proxy/wsrelay.Manager.createConnection` 的临时 dialer 显式设置 `NetDialContext`，避免无代理时落回默认环境代理。
- 新增测试覆盖：OpenAI Responses 拉模型空代理直连、显式 HTTP 代理、请求代理优先、运行时账号代理 fallback、全局代理 fallback、全空直连；WebSocket 空代理抗环境代理、显式 HTTP 代理握手。

## 验证记录
- 已通过：`go test ./admin ./auth ./proxy ./proxy/wsrelay -run "Proxy|OpenAIResponses|Websocket|Wham|CodexStandardTransport|ResolveOpenAIResponsesModelsProxy|FetchOpenAIResponsesModelIDs" -count=1`
- 待最终执行：`go test ./... -count=1`
