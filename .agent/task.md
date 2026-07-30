# Task: 修复 responses 历史丢失场景下的优雅降级切号

## 目标
解决服务重启后，老对话带有不存在的 `previous_response_id` 且原账号失效时无法切号的问题，通过剥离失效 Response ID，使账号池新账号能 100% 成功接收最新输入并畅通回复。

## 任务拆解
- [x] 1. 定位 `canBuildOpenAIResponsesContinuationFallback` 在 `materialize` 返回 false 时的断流链
- [ ] 2. 增加针对失效/未知 `previous_response_id` 的安全剥离降级逻辑（Stripped Previous ID Fallback）
- [ ] 3. 编写 TDD 单元测试验证老对话丢失历史时仍能 100% 无缝切号
- [ ] 4. 重新编译生成 `codex2api.exe` 并进行全量测试校验
