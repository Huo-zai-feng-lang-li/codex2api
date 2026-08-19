# 最新接续状态 (2026-08-17 18:12)

## 核心进展
- 已修复 Responses WebSocket 上游 `400 empty_request_body` 反复失败：在 [proxy/responses_ws.go](../proxy/responses_ws.go) 的 `isOpenAIResponsesWebSocketUnsupported` 中识别结构化 `error.code=empty_request_body`，复用现有 HTTPS fallback 与 endpoint 偏好缓存。
- 已执行 `build-and-restart.bat` 更新服务；当前进程 PID `23372`，二进制 SHA256 `94619CBE183EEFD2987996AE1BF8E5EB3A9A3AC520AC2CDF6B37CD729CE9A288`，端口 `18080` 归属正确。
- 当前 `/health`：`status=ok`、`inflight_requests=0`、`continuity_persistence_failures=0`、`available=1`、`total=39`。

## 核心动机与背景 (Motivation & Background)
- 任务类型：Bug 修复型。
- `usage_logs.id=13350` 及同类记录来自 `wss://cgapi.aerolink.lat/v1/responses`；另有 `wss://api.freemodel.dev/v1/responses` 同样返回 `400 empty_request_body`。
- 根因不是本地请求体被清空：WSS 客户端先完成 `DialContext` 握手，只有收到 `101` 后才调用 `conn.WriteMessage(wsBody)`；该 400 在请求帧发送前由上游握手阶段返回。
- 上游 HTTPS 请求可正常返回 200；本地每账号并发上限为 2，与该错误无直接关系。

## 关键设计与实现 (Implementation & Decisions)
- 只对 HTTP 400 且 JSON `error.code == "empty_request_body"` 判定为 WSS 不支持；其他 400 保持原语义，不误降级。
- 复用已有 `responses_transport_fallback`、HTTP body 转换和 endpoint 缓存，不新增抽象、不改变并发、429 重试或请求体处理。
- 回归测试位于 [proxy/responses_transport_cache_test.go](../proxy/responses_transport_cache_test.go)，覆盖 JSON `empty_request_body`、空白 400、明确 WSS 不支持和普通业务 400。
- 验证证据：定向测试通过；`go test ./... -count=1` 全部通过；`go vet ./...` 通过；`git diff --check` 通过；BAT 使用 Node `v24.14.0` 构建前端并成功替换、启动、健康检查。
- 首次 BAT 失败原因是系统 Node `v18.20.8` 不支持依赖所需的 `node:util.styleText`；未停止旧服务。随后仅在部署进程 PATH 前置工作区 Node `v24.14.0` 重试成功。

## 待办事项 (Next Steps)
- [ ] 观察 `cgapi` 与 `freemodel` 后续 WSS 请求是否直接命中 HTTPS fallback，并确认不再重复产生 `empty_request_body` 终态记录。
- [ ] 若继续做真实 WSS 闭环，单独验证上游是否支持终止事件；不要把公开 WSS 探针结果当作业务上游闭环证据。

## 关键上下文
- 目录: `D:\Desktop\Super-File\codex2api`
- 主要文件: `proxy/responses_ws.go` (`isOpenAIResponsesWebSocketUnsupported`、Responses WSS/HTTPS fallback)；`proxy/executor.go` (`ExecuteOpenAIResponsesWebSocketRequest`)；`proxy/responses_transport_cache_test.go` (回归测试)；`build-and-restart.bat` (构建与热替换)。
- 当前工作区保留用户原有未提交改动，未执行 reset/checkout/clean/revert。
