# 最新接续状态 (2026-07-23 19:27)

## 核心进展
- 仪表盘 `19` 个关键数字已统一为 3 秒纯数字滚动：首次加载和手动趋势切换从 0 到目标值，15 秒自动刷新从旧值衔接到新值；无上下位移、透明度变化或闪烁，核心文件为 `frontend/src/components/AnimatedMetricValue.tsx`、`frontend/src/lib/metricAnimation.ts`、`frontend/src/pages/Dashboard.tsx`。
- `RPM / TPM` 标签后已增加黄色圆形问号；鼠标悬停和键盘聚焦均会说明 RPM/TPM 是最近 60 秒已完成请求口径，与当前活跃请求数无需一致。
- 今日与总缓存命中率已改为仅以 `status_code = 200` 请求为分母；错误日志和透明重试仍完整保留。浏览器实测新口径今日 `93.9%`、总计 `95.0%`。
- 浏览器在新服务上实测 `19` 个动画节点存在，切换趋势时间后首字延迟在约 3 秒内经过多个中间值到达目标；动画未被缓存口径修改破坏。
- 已热替换 `codex2api.exe`：PID `25156`，监听 `127.0.0.1:18080`，SHA256 `62973A93ED85786C47B73963E7BA2C41395CFFEE8417519BF7FDF892CB19A90D`，健康状态 `ok`。

## 变更决策
- 动画使用 32ms 定时器和 `performance.now()` 计算 3 秒线性进度，不依赖当前环境约 1 秒一次的 `requestAnimationFrame`；只更新文本节点，不增加接口请求或 React 高频重渲染。
- 动画组件预计算整段格式化路径中的最宽文本，并使用等宽数字预留宽度，解决 Token 跨“万/亿”单位和复合计费文本的布局抖动。
- RPM/TPM 说明复用项目 Radix Tooltip，入口使用原生按钮并提供中英文可访问名称；不改变任何统计接口或计算口径。
- `chartDataRange` 作为延迟动画重置键，只有新范围数据实际到达时才从 0 开始；自动刷新保持旧值连续性。
- 快速热替换规则已补全到 `.agent/rules/README.md`：候选先在线构建并校验，停止、备份、替换、WMI/CIM 脱离启动、健康检查和失败回滚必须在同一脚本内连续完成；主 `README.md` 已增加说明入口。
- SQLite/PostgreSQL 使用独立成功分母 `cache_rate_requests`；旧 SQLite 基线首次迁移用历史 `total_requests` 初始化，后续清空日志只累计 `200` 请求，避免再被失败请求污染。
- `598` 主要是 `kaiycb.com` WebSocket 在 `response.completed` 前关闭，`487/488` 条属于透明重试尝试；`400` 中 `411/413` 条来自 `vip-sg.freemodel.dev` 的上游响应。反代负责重试和记录，但错误源集中在第三方上游。

## 待办事项 (Next Steps)
- [ ] 用户刷新管理后台后验收成功请求缓存命中率与 3 秒纯数字滚动。
- [ ] 评估把仪表盘错误率拆成“客户端最终错误率”和“上游尝试错误率”，避免透明重试放大表面错误率。
- [ ] 针对 `vip-sg.freemodel.dev` 的空响应体 `HTTP 400` 增加安全请求指纹取证，进一步区分上游参数限制与请求兼容问题。

## 关键上下文
- 目录: `C:\Users\Administrator\Desktop\codex2api`
- 主要文件: `database/postgres.go`、`database/sqlite.go`、`database/sqlite_test.go`
