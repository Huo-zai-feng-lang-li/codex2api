# 最新接续状态 (2026-07-23 13:51)

## 核心进展
- 仪表盘顶部账号卡、使用统计卡和健康延迟已统一接入 1 秒数字滚动；手动切换趋势范围不再先清空健康延迟为 `-`，核心文件为 `frontend/src/components/AnimatedMetricValue.tsx`、`frontend/src/components/UsageStatsSummary.tsx`、`frontend/src/pages/Dashboard.tsx`。
- 已构建并热替换 `C:\Users\Administrator\Desktop\codex2api\codex2api.exe`，当前 18080 新进程健康检查通过。
- 运行态发现 `usage_log.mode=off` 导致今日请求、Token、1 小时延迟不增长；已通过设置恢复为 `full`，恢复后接口与页面均显示统计增长。

## 变更决策
- 自动刷新和手动切换均采用 stale-while-revalidate，避免健康卡闪 `-`；不改变 15 秒频率，不增加接口请求。
- 手动切换趋势时间范围时保留旧值作为过渡态，并用 `chartDataRange` 标记当前数据归属范围，避免旧口径被当成新范围最终结果。
- 动画通过 `AnimatedMetricValue` 直接局部更新数字文本，默认 1 秒；尊重 `prefers-reduced-motion`，避免整张统计卡高频重绘。
- 统计不增长优先检查 `/api/admin/runtime-status` 的 `usage_log.enabled`；关闭日志写入时代理仍转发请求，但仪表盘统计不会落库增长。
- 单进程同端口替换只能缩短停机窗口，真正零中断需稳定入口代理加双端口蓝绿实例：先启动并探活新实例，再切流，最后排空旧实例。

## 待办事项 (Next Steps)
- [ ] 用户验收时重点看：1 小时趋势下健康首字/完成延迟有样本后显示数字；今日请求、今日 Token、TPM 在新模型请求完成后随 15 秒刷新增长并执行滚动。

## 关键上下文
- 目录: `C:\Users\Administrator\Desktop\codex2api`
- 主要文件: `frontend/src/components/AnimatedMetricValue.tsx`、`frontend/src/components/UsageStatsSummary.tsx`、`frontend/src/pages/Dashboard.tsx`
