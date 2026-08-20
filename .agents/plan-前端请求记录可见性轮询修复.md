# 前端请求记录可见性轮询修复

## 最终目标
- 浏览器页签隐藏时停止 `/api/admin/usage/logs` 请求与轮询定时器。
- 页签恢复可见时立即刷新一次。
- 存在活跃请求时，以 3 秒间隔持续刷新请求记录。

## 影响范围
- `frontend/src/hooks/useVisiblePolling.ts`
- `frontend/src/pages/Dashboard.tsx`
- `frontend/src/pages/UsageLogsMigration.test.mjs`
- 前端构建产物与根目录 `codex2api.exe`

## 阶段状态
1. [x] 取证：确认请求链路、现有 Page Visibility 实现、构建脚本与工作区状态。
2. [x] RED：将请求记录轮询契约收紧为“仅活跃请求存在时 3 秒轮询”，确认 3 项断言失败。
3. [x] GREEN：隐藏时销毁定时器，恢复可见时立即刷新并重建定时器。
4. [x] 验证：57/57 前端测试通过；TypeScript 类型检查通过；根目录构建批处理退出码 0；新服务健康状态 `ok`；浏览器模拟隐藏 7 秒期间 `/usage/logs` 增量为 0，恢复可见后立即恢复请求。

## 构建证据
- `codex2api.exe`：2026-08-03 18:38:43，SHA256 `B4DE7834079871F913D5068EE9562BAC7721A7DA7D4EC77B0E58FAC3025C491F`。
- 前端入口：`frontend/dist/assets/index-xowaUKhF.js`，2026-08-03 18:38:39。
- 服务已按构建脚本保留运行，供人工验收。

## 完成标准
- 回归测试锁定活跃请求条件、3 秒周期、隐藏暂停、恢复立即刷新。
- `node --test frontend/src/**/*.test.mjs`、`npm run typecheck` 通过。
- `build-and-restart.bat` 退出码 0，服务健康检查成功。
- 无残留测试脚本或临时服务进程。
