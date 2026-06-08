# 生图稳定性优化计划

## 目标
- 让生图工作台尽可能命中可生图账号，减少随机挑到无权限或异常账号导致的失败。
- 不新增数据库字段，优先复用现有账号错误信息与模型冷却机制，降低迁移风险。

## 取证结论
- 工作台 #18/#19/#20 均失败，历史任务确实没有成功图片。
- 兼容接口 `/v1/images/generations` 曾成功生成 1 张 `1024x1024` JPEG，说明底层转换和保存链路不是整体损坏。
- 账号 `582/583` 明确返回 `Image generation is not enabled for this group`，属于不具备生图权限。
- 账号 `592/586` 出现 `upstream did not return image output`，属于本次上游未产图，需要短期避开继续换号。
- 工作台 #21 重启后仍失败，但 usage log 显示已按预期换号：PNG 尝试 `551/576/582/583/588`，JPEG 尝试 `587/586/598`；失败集中在 `image_no_output` 和 `image_not_enabled`。
- 官方 Responses 图像工具文档说明，包含 `image_generation` tool 时模型可自行决定是否调用；若要强制调用，应设置 `tool_choice: {"type":"image_generation"}`。当前工作台请求没有该字段，能解释“上游完成但没有图片输出”。
- 2026-06-08 最新真实验证：`POST /v1/images/generations` 请求 `gpt-image-2` 返回 `503 no_available_account`；usage log 显示已进入生图链路并连续换号，账号 `551` 返回额度 `402`，`583` 返回 `Image generation is not enabled for this group`，`603/604` 返回上游 `server_error`。当前结论是账号/上游能力不足，不是本地生图代码未生效。

## 方案
1. [x] 生图调度过滤明确无生图权限的账号。
2. [x] 生图请求遇到 403 且错误包含无生图权限时，对 `gpt-image-2` 记长模型冷却。
3. [x] 生图请求遇到 `upstream did not return image output` 时，对该账号的图像模型记短模型冷却，并继续换号。
4. [x] 等待账号分支使用生图专用过滤器，避免把不支持生图主模型的 OpenAI Responses 账号捞回来。
5. [x] 读响应失败时写入 usage log，记录 attempt、是否重试、错误类型和错误消息。
6. [x] 成功产图后清除该账号图像模型冷却。
7. [x] 工作台 `/v1/images/generations` / edits 请求强制 `tool_choice.type=image_generation`，避免上游返回纯文本/空输出而不调用生图工具。

## 验证
- [x] 新增失败测试确认等待分支会误选生图不可用账号。
- [x] 新增失败测试确认 `upstream did not return image output` 重试前必须落 usage log。
- [x] `go test ./proxy -run "ForwardImagesRequestWaitUsesImageEligibleFilter|ForwardImagesRequestLogsImageNoOutputRetryAttempt"`
- [x] `go test ./proxy -run "Image|Images"`
- [x] `go test ./proxy`
- [x] `go test ./...`
- [x] 新增失败测试确认工作台请求必须携带 `tool_choice.type=image_generation`。

## 待人工验收
- 在工作台用同一个 API Key 重新生成一张简单图片，观察历史任务是否从失败转为成功。
- 若仍失败，到“使用统计”查看 `upstream_error_kind=image_no_output` 或 `image_not_enabled`，确认是否所有可调度账号都不具备生图能力。
- 当前需要补充至少 1 个确认具备 `gpt-image-2` 生图权限且未触发额度/上游错误的账号，再重试验收。
