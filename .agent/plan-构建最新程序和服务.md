# 任务：构建最新程序和服务

## 核心目标
- 构建 frontend 最新产物
- 构建 Go 后端最新二进制
- 在当前无 Docker 环境下，尽可能以本机进程方式启动服务并验证健康检查

## 关键取证
- README 明确本地开发路径：frontend 先 build，backend 再 go run/build
- 当前环境存在 .env、Go 1.26.3、Node 22、pnpm 10
- 当前环境不存在 docker/docker compose，无法走 compose 服务构建

## 执行阶段
- [x] 读取 handoff/README/构建入口
- [x] 核验本机运行时与 Docker 可用性
- [ ] 构建 frontend
- [ ] 构建 backend
- [ ] 启动本机服务并验证
