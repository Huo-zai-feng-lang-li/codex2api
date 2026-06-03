# 账号列表请求/用量展示优化计划

## 目标
- 核验账号列表中“请求(7D)”和“用量”的数据来源是否准确。
- 在不改后端统计口径的前提下，优化前端展示，让成功率、失败、retry、429、并发、5h/7d 用量、费用、重置时间更易扫读。

## 取证结论
- `admin/handler.go` 账号列表响应已返回 `success_requests`、`error_requests`、`retry_error_requests`、`rate_limit_attempts`、`usage_percent_5h/7d`、`usage_5h/7d_detail`、`billed_5h/7d`。
- `usage_5h/7d_detail` 来自 `GetAccountTimeRangeUsage`，费用窗口来自 `GetAccountBilledSince`。
- 当前问题不是字段错位，而是前端把健康、请求量、费用和窗口状态混在一起，读起来不直观。

## 实施范围
- 只改 `frontend/src/pages/Accounts.tsx` 中账号列表请求/用量单元格展示。
- 需要时补充 `frontend/src/locales/zh.json`、`frontend/src/locales/en.json` 文案。
- 不改后端接口和统计 SQL。

## 验证
- `pnpm typecheck`
- `pnpm build`
- 浏览器打开管理台账号页，确认列宽、文字、tooltip、移动端卡片不重叠。

## 二次反馈修正
- 表格模式中用量模块不能做大卡片，需压缩成两行窗口摘要，避免撑宽横向滚动。
- 卡片模式中的“成本”不能只依赖 `billed_5h/7d`，需要从 `usage_5h/7d_detail.account_billed/user_billed` 兜底展示真实费用。
- 保持数据真实性：不前端估算百分比、不伪造剩余额度；没有 `usage_percent_5h/7d` 时只展示请求/token/费用统计。
