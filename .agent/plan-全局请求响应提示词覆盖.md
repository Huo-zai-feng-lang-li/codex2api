# 全局请求/响应提示词覆盖交付记录

## 目标
- 后台提供请求系统提示词和响应重写提示词配置。
- 请求经过反代时可覆盖 OpenAI Responses、Chat Completions、Anthropic Messages 的系统提示词入口。
- 非流式响应返回下游前可替换可见文本内容。
- 安全事件页新增两个 Tab，用于读取和修改这两类提示词配置。

## 已完成
- 新增系统设置字段：
  - `proxy_request_system_prompt_enabled`
  - `proxy_request_system_prompt`
  - `proxy_response_rewrite_enabled`
  - `proxy_response_rewrite_prompt`
- 后端设置 API 已支持加载、保存、回显四个字段。
- SQLite / PostgreSQL 迁移和默认值已补齐。
- Store 运行态配置已接入。
- 请求侧覆盖：
  - Responses 覆盖 `instructions`，移除同级 `system` / `developer`。
  - Chat Completions 去掉原 system/developer 消息，并在首位写入统一 system。
  - Anthropic Messages 覆盖 `system`。
- 响应侧覆盖：
  - Responses 替换 `output_text` 和 message content 文本。
  - Chat Completions 替换 `choices[].message.content`。
  - Anthropic Messages 替换 text content block。
  - 错误响应不改写。
- 安全事件页新增：
  - 请求系统提示词 Tab。
  - 响应重写提示词 Tab。
  - 两个 Tab 都可刷新、编辑、保存、回显。

## 验证结果
- `npm run typecheck`：通过。
- `npm run build`：通过。
- `go test ./...`：通过。

## 当前边界
- 第一版只处理全局配置，不做账号、模型、上游维度覆盖。
- 第一版只改写非流式响应；流式响应继续走现有扫描和取证链路。
- 本次按用户最新口径把配置入口放入安全事件页两个 Tab；没有额外新增独立审计表。

## 2026-06-12 追加：安全事件开关解耦确认
- 请求系统提示词和响应内容重写提示词都不依赖安全事件/上游防护开关。
- `proxy/prompt_rewrite_test.go` 已新增回归测试：`UpstreamGuardMode=off` 时，请求提示词仍覆盖 `instructions`，响应重写仍替换非流式 Responses 文本。
- 前端文案已明确响应侧是“响应内容重写”，不是发给上游模型的“响应系统提示词”。
