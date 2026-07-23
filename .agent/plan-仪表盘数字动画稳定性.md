# 仪表盘数字动画稳定性 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让仪表盘所有变化数字在 `requestAnimationFrame` 被降至约 1 FPS 时，仍以约 30 FPS 完成 3 秒平滑滚动；首次加载和手动时间切换从 0 开始，自动刷新从旧值衔接，健康数字保持原色。

**Architecture:** 将动画时钟从浏览器绘制帧解耦为独立的 32ms 定时调度器，使用 `performance.now()` 计算线性进度，只改变数字文本，不使用位移、透明度或闪烁效果。组件使用隐藏的旧值与目标值共同预留最大宽度，并启用等宽数字，避免 Token、计费和 TPM 在滚动期间推动相邻内容。范围数据通过 `chartDataRange` 作为动画重置键，仅在新范围数据真正到达时从 0 开始；默认动画时长统一为 3 秒，自动刷新沿用旧值。

**Tech Stack:** React 19、TypeScript、Node.js 内置测试、Vite、Playwright

---

### Task 1: 动画调度器回归测试

**Files:**
- Create: `frontend/src/lib/metricAnimation.ts`
- Create: `frontend/src/lib/metricAnimation.test.mjs`

- [x] 编写可注入时钟的失败测试，验证 32ms 连续中间值和最终值。
- [x] 运行 `node --test frontend/src/lib/metricAnimation.test.mjs`，确认因实现缺失而失败。
- [x] 实现 `startMetricAnimation`，返回取消函数并保证最终值精确落点。
- [x] 重跑定向测试，确认通过。

### Task 2: 接入数字组件

**Files:**
- Modify: `frontend/src/components/AnimatedMetricValue.tsx`

- [x] 用 `startMetricAnimation` 替换递归 `requestAnimationFrame`。
- [x] 移除健康延迟加载态的透明度变化，仅保留无障碍忙碌状态。
- [x] 保留缓存、无效值处理、减少动态效果和卸载清理语义。
- [x] 运行前端类型检查和生产构建。

### Task 3: 页面与服务闭环

**Files:**
- Modify: `.agent/handoff.md`
- Modify: `.agent/plan-仪表盘数字动画稳定性.md`

- [x] 浏览器切换 `1h/6h/24h/7d/30d`，采样首字和完成延迟的多个中间值。
- [x] 跨一个 15 秒轮询周期，确认其他变化指标同样滚动且不闪 `-`。
- [x] 执行 Go 全量测试、静态检查和差异检查。
- [x] 构建候选 EXE，排空请求后安全热替换并核对哈希、PID、端口和健康状态。
- [x] 更新接力文件，仅提交本任务文件。
