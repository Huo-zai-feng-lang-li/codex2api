# Codex2API 项目 AI 一次性精准修改与闭环铁律

本规则约束本项目后续的所有开发、修改与运维。核心目标：**确保 AI 每次修改代码一次修改好，一枪十环，严禁二次返工！**

---

## 1. 五大硬性执行铁律（一次改好标准）

### 1.1 双通道绝对对称铁律（HTTP ↔ WebSocket）
Codex 桌面端走 `GET /responses` (WebSocket)，其他客户端走 `POST /v1/responses` (HTTP)。两条路径完全独立。
- **强制规则**：任何涉及 Responses 续链、账号亲和、安全审计、日志脱敏的改动，**必须同时修改** `handler.go` 和 `responses_ws.go`！漏改任意一边视为未完成。

### 1.2 账号死守不盲换铁律（保上下文强粘性）
- **强制规则**：只要上轮绑定的原账号在数据库中存在且未被强制禁用（`Disabled != 0`），即使因探针/并发进入短 Cooling，`WaitForSessionAvailableWithFilter` 必须退避等待原账号恢复。**严禁因临时 Cooling 盲目换号并剥离上下文指针！**
- **切号前提**：原账号明确 `Disabled`、删除、401/403、`payment_required`、账号级 `rate_limited`、`StatusError` 或本地派发凭据失效时，允许启动无缝切号降级；切号前必须确认本地完整历史可回放，否则显式返回 `continuation_context_unavailable`。

### 1.3 续链自愈零 409 铁律（封死死锁回路）
- **强制规则**：一旦降级逻辑已激活（`alreadyActivated == true`），系统直接放行当前已自愈处理的 `body`。**绝对禁止二次触发 `writeContinuationContextIncomplete` 抛出 409 冲突错！**

### 1.4 【核心铁律】机器人模拟真实链路发包闭环（拒绝单单元自嗨）
- **强制规则**：以后的所有测试与修改，**必须写一个测试机器人/脚本模拟真实客户端发包链路**才能完成闭环！
- **必须模拟的完整链路**：测试机器人必须真实向接口发起连贯发包（`首包 A 拿到 response_id -> 携带 previous_response_id 发起续问包 B`）。
- **硬性闭环门槛**：绝不能单靠 `go test` 单元断言自嗨。**必须由发包机器人实测包 A 和包 B 均返回 `HTTP 200 OK`，且系统控制台 0 报错、无 409 死锁，才算真正改好闭环！**

### 1.5 优雅部署与在途流量排空铁律
- **强制规则**：热替换新二进制前，必须核对 `/health` 的 `responses_memory.inflight_requests == 0`；优雅关闭旧 PID 后再原子替换 EXE 并拉起新服务，禁止强杀导致在途连接断裂。

### 1.6 续链持久化容量治理铁律
- **强制规则**：`responses_continuity` 仅允许 replayable 节点保存完整 input/output；failed、cancelled 等不可回放节点只能保留路由和状态元数据，禁止大体积垃圾占用续链配额。
- **清理一致性**：Prune/Trim 删除续链节点后，必须在同一事务内修复或删除悬空的 `responses_continuity_heads`；禁止留下指向不存在节点的会话头。
- **保留时间**：续链采用最后访问时间驱动的滑动 TTL，默认 24 小时，可通过 `CODEX_RESPONSES_CONTINUITY_TTL_HOURS` 调整。历史无法完整恢复时必须显式失败，禁止静默按新会话续接。

---

## 2. 零容忍红线（杜绝反复修、假宣称修好）

1. **【红线一】严禁未验证先叫“修好了”**：
   - 严禁在未运行发包机器人完成两连包 `HTTP 200 OK` 真实测试之前，向用户发表“已修复/修好了/去测试吧”等口头承诺。
   - 凡没有出示发包机器人运行结果或数据库物理流水的“修好了”，一律视为假闭环！
2. **【红线二】物理事实取证高于一切**：
   - 宣称上线或完成前，必须出示物理证据：数据库 `usage_logs` 真实 200 流水记录、`start.err.log` 为 0 字节证据。
   - 禁止凭脑补和理论推断进行交付！

---

## 3. 关键架构速查与路由映射

```
main.go (入口)
├── proxy/handler.go          —— HTTP POST 请求统一处理
├── proxy/responses_ws.go      —— WebSocket 升级处理 (Codex 桌面端)
├── proxy/responses_continuity.go —— Responses 续链历史展发与注册表
└── auth/store.go             —— 账号调度、Session 亲和性与冷却等待
```

| 请求路径 | 通道类型 | 关键处理函数 |
|---|---|---|
| `POST /v1/responses` | HTTP | `h.Responses` |
| `GET /v1/responses` | WebSocket | `h.ResponsesWebSocket` |
| `POST /backend-api/codex/responses` | HTTP 直连 | `h.Responses` |
| `GET /backend-api/codex/responses` | WS 直连 | `h.ResponsesWebSocket` |

---

## 4. 交付前自检清单（Checklist）

在向用户宣布完成前，AI 必须逐项自检：
- [ ] `handler.go` 与 `responses_ws.go` 的改动是否双向对称？
- [ ] 原账号在 Cooling 时是否退避等待而没有盲目切号？
- [ ] `alreadyActivated == true` 时是否安全放行而未触发 409？
- [ ] **【硬性门槛】是否已编写发包机器人模拟真实链路（首包 -> 续问包），并实测获得两连包 HTTP 200 OK？**
- [ ] `go test ./... -count=1` 与 `go vet ./...` 是否全过？

---

> **核心哲学**：哪里需要改，一枪过去就是十环！全链路闭环，改一处不影响其他功能，一次修改好！
