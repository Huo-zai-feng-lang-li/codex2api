# 最新接续状态 (2026-06-01 20:50)

## 核心进展
- 已完成小账号池首字慢与本地吞吐保护闭环：`/responses` 首字超时改为动态阈值 + 单请求最多切换 2 个账号 + 忙时排队限流 + 队列满返回 `429/Retry-After`，并已重构 `codex2api.exe`、重启服务生效。

## 变更决策
- 首字慢只做软失败与有限换号，不再无限切号/循环连接，避免把一个慢请求放大成重试风暴。
- 调度队列上限加入系统设置，`0` 表示自动模式（按可调度账号数 × 3，最小 3），`>0` 表示固定上限。
- 区分硬失败与软失败：401/403/明确 429 走硬排除，首字慢/首包前断流走软排除并回收；已输出正文后禁止透明重试，只做稳定失败收口。
- TTFT/首字表现进入调度评分，优先把请求分给更快的账号，减少小池子抖动。

## 待办事项 (Next Steps)
- [ ] 观察真实流量下 `dispatch_queue_limit=0` 的自动队列长度是否过大，必要时把后台默认值收紧到更适合小池子的固定值。
- [ ] 继续监控首字超时、队列等待和 `429` 触发频率，确认没有新的循环重连或假性“无账号可用”。

## 关键上下文
- 目录: C:\Users\Administrator\Desktop\codex2api
- 主要文件: C:\Users\Administrator\Desktop\codex2api\proxy\handler.go, C:\Users\Administrator\Desktop\codex2api\auth\store.go, C:\Users\Administrator\Desktop\codex2api\admin\handler.go
