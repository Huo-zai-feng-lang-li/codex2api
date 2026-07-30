# Task: 实现 Responses 跨账号无缝自动接管机制

## 目标
当 Responses API 的原账号不可用（429 Cooldown / 离线 / 配额耗尽）且会话上下文存在悬空（未闭环）Tool Call 时，系统能够自动补全/规整上下文，使账号池中的其他上游账号可以 100% 成功接管并流畅回复，消除 409 Conflict 阻断。

## 任务拆解
- [x] 1. 分析 `proxy/responses_continuity.go` 中 `normalizeMatchedOpenAIResponsesToolOutputs` 的缺口逻辑
- [x] 2. 设计未闭环 Tool Call 哑输出（Synthetic Tool Output）自动补齐与容错机制
- [x] 3. 编写 TDD 测试用例（验证包含悬空 function_call/mcp_tool_call/custom_tool_call 时跨账号自动接管成功）
- [x] 4. 实现自动接管补齐逻辑，并保持已有模式（如完整严苛校验）兼容性
- [x] 5. 运行完整单元测试与集成测试，验证全链路闭环
