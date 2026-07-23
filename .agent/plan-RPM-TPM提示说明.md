# RPM / TPM 提示说明 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在仪表盘 `RPM / TPM` 标签后增加黄色圆形问号，并用可悬停、可聚焦的提示说明统计口径。

**Architecture:** 复用项目 Radix Tooltip 组件，提示入口使用原生按钮保证键盘访问；中英文文案进入现有 dashboard locale。只改标签展示，不改变 RPM、TPM、活跃请求或轮询口径。

**Tech Stack:** React 19、TypeScript、Radix Tooltip、i18next、Playwright

---

### Task 1: 回归测试与实现

- [x] 添加失败测试，约束中英文说明、可访问名称和黄色圆形入口。
- [x] 扩展 `MetricLine` 标签类型并接入 Tooltip。
- [x] 运行前端测试、类型检查和生产构建。

### Task 2: 浏览器与服务闭环

- [x] 浏览器验证鼠标悬停、键盘聚焦和提示内容。
- [x] 运行 Go 全量测试、`go vet` 和差异检查。
- [x] 构建候选 EXE，按项目快速替换规则部署并核对哈希与健康。
- [x] 更新 `.agent/handoff.md`，提交本任务文件。
