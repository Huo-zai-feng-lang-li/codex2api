# 最新接续状态 (2026-07-23 18:15)

## 核心进展
- 仪表盘 `19` 个关键数字已统一为 3 秒纯数字滚动：首次加载和手动趋势切换从 0 到目标值，15 秒自动刷新从旧值衔接到新值；无上下位移、透明度变化或闪烁，核心文件为 `frontend/src/components/AnimatedMetricValue.tsx`、`frontend/src/lib/metricAnimation.ts`、`frontend/src/pages/Dashboard.tsx`。
- `RPM / TPM` 标签后已增加黄色圆形问号；鼠标悬停和键盘聚焦均会说明 RPM/TPM 是最近 60 秒已完成请求口径，与当前活跃请求数无需一致。
- 浏览器实测时间切换首字/完成延迟从 `0ms` 开始，数字与父级透明度始终为 `1`、`transform=none`；Token、两项计费、RPM、TPM 在 3 秒滚动期间宽度变化均为 `0px`。
- 已热替换 `codex2api.exe`：PID `18220`，监听 `127.0.0.1:18080`，SHA256 `2C37A4BC8DDDF041C196E8A23E46F0A5CF4F20CE76FA305FD482315D53F494A9`，健康状态 `ok`。
- 今日缓存率偏低已取证：样本时全部请求命中率 `46.52%`，成功请求命中率 `96.42%`，缓存 Token 占输入 Token `89.45%`；低值由大量非 200 请求进入分母造成，未擅自修改统计口径。

## 变更决策
- 动画使用 32ms 定时器和 `performance.now()` 计算 3 秒线性进度，不依赖当前环境约 1 秒一次的 `requestAnimationFrame`；只更新文本节点，不增加接口请求或 React 高频重渲染。
- 动画组件预计算整段格式化路径中的最宽文本，并使用等宽数字预留宽度，解决 Token 跨“万/亿”单位和复合计费文本的布局抖动。
- RPM/TPM 说明复用项目 Radix Tooltip，入口使用原生按钮并提供中英文可访问名称；不改变任何统计接口或计算口径。
- `chartDataRange` 作为延迟动画重置键，只有新范围数据实际到达时才从 0 开始；自动刷新保持旧值连续性。
- 快速热替换规则已补全到 `.agent/rules/README.md`：候选先在线构建并校验，停止、备份、替换、WMI/CIM 脱离启动、健康检查和失败回滚必须在同一脚本内连续完成；主 `README.md` 已增加说明入口。
- 缓存命中率当前定义仍为 `cached_tokens > 0 的请求数 / 全部非 499 请求数`；若要反映缓存能力，应另行确认是否改为仅统计成功请求或同时展示 Token 缓存率。

## 待办事项 (Next Steps)
- [ ] 用户刷新管理后台后验收 3 秒纯数字滚动，重点检查时间切换、今日请求和 Token。
- [ ] 如需调整缓存指标，优先评估展示“成功请求缓存命中率”和“缓存 Token 占比”，避免与历史口径静默混用。
- [ ] `proxy` 的续链淘汰测试曾在定向 `-count=10` 中偶发失败 2 次，但随后全量测试通过；可单独排查共享状态测试稳定性。

## 关键上下文
- 目录: `C:\Users\Administrator\Desktop\codex2api`
- 主要文件: `frontend/src/components/UsageStatsSummary.tsx`、`frontend/src/locales/zh.json`、`frontend/src/locales/en.json`
