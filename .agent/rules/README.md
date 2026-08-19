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
- **强制规则**：热替换新二进制前，必须先完成新产物构建，再核对 `/health` 的 `responses_memory.inflight_requests == 0`。Windows 本地热替换允许在排空后强制终止当前项目路径对应的精确 PID，禁止按进程名批量强杀；随后必须原子替换 EXE、拉起新服务，并确认健康响应端口归属新 PID，失败时恢复旧二进制。
- **Windows 本地热替换入口**：确认项目根目录、构建依赖与配置路径无误后，可以在项目根目录执行 `cmd /c build-and-restart.bat`。脚本负责先构建临时产物、排空在途请求、精确停止旧 PID、替换与失败回滚；命令执行结束后仍须核验退出码、新进程、`GET http://127.0.0.1:18080/health`、前端构建产物及 `logs/start.err.log`。

### 1.6 续链持久化容量治理铁律
- **强制规则**：`responses_continuity` 仅允许 replayable 节点保存完整 input/output；failed、cancelled 等不可回放节点只能保留路由和状态元数据，禁止大体积垃圾占用续链配额。
- **清理一致性**：Prune/Trim 删除续链节点后，必须在同一事务内修复或删除悬空的 `responses_continuity_heads`；禁止留下指向不存在节点的会话头。
- **保留时间**：续链采用最后访问时间驱动的滑动 TTL，默认 24 小时，可通过 `CODEX_RESPONSES_CONTINUITY_TTL_HOURS` 调整。历史无法完整恢复时必须显式失败，禁止静默按新会话续接。

### 1.7 语义化版本与发版四位一体铁律
- **版本源权威性**：系统版本严格遵循语义化版本号（SemVer）。主干版本以 `git tag`（如 `git describe --tags`）与根目录 `CHANGELOG.md` 为唯一权威事实源，**严禁闭门造车或随意重置版本号！**
- **四位一体联动**：凡涉及功能发版或版本号升级，**必须同时联动更新以下 4 处**，漏更任意一处视为发布未完成：
  1. `CHANGELOG.md`：新增对应版本号（如 `## v2.2.8 - YYYY-MM-DD`）及改动明细。
  2. `frontend/package.json`：同步更新 `"version": "X.Y.Z"`。
  3. `api/middleware.go`：同步更新 `CurrentVersion = Version{Major: X, Minor: Y, Patch: Z}`。
  4. `git tag -a vX.Y.Z`：打上带注释的规范 Git 版本标签。

### 1.8 架构文档与计划归档同步铁律 (Doc-as-Code)
- **先计划后动手**：进行多步骤重构、新功能开发或复杂 Bug 修复前，**必须先在 `.agent/` 目录下创建 `plan-任务名称(中文).md`** 梳理核心目标与实施步骤。
- **架构文档强一致**：任何涉及核心机制（如限流冷却策略、调度评分算法、代理池路由、续链持久化）的改动，**必须同步更新 `docs/ARCHITECTURE.md` 等对应设计文档**，杜绝“代码已改、文档落后”。
- **跨会话接续归档**：任务收尾或阶段性交付时，**必须即时更新 `.agent/handoff.md`**，记录最新代码状态、测试凭证与后续待办。

### 1.9 多上游错误语义与时钟对齐铁律
- **杜绝盲目固定退避**：处理第三方中转或聚合网关（OneAPI / NewAPI 等）报错时，严禁使用盲目固定的 5 分钟短时退避导致大量无效探测轮询。
- **时钟与语义对齐标准**：
  - **日配额超限（Daily Limit）**：动态计算并精准锁定冷却至 **次日午夜 00:00:05**。
  - **周配额超限（Weekly Limit）**：锁定冷却至 **7 天（`7*24h`）**。
  - **欠费 / 额度耗尽（Insufficient Quota）**：标记 `payment_required` 并休眠 **24 小时**。
  - **短时高频（RPM / TPM）**：执行 **1 分钟** 快速自愈退避。
  - **通用/未知 429 报错**：维持安全兜底（5 分钟指数退避），确保与官方 Codex Header 机制 100% 兼容。

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
- [ ] 版本号是否与 `git tag` & `CHANGELOG.md` 顺延对齐？（四位一体：`CHANGELOG.md` + `package.json` + `middleware.go` + `git tag`）
- [ ] `docs/ARCHITECTURE.md` 是否同步补充了最新机制与设计改动？
- [ ] `.agent/plan-*.md` 计划文档与 `.agent/handoff.md` 是否已完成归档？
- [ ] 代码修复好了之后，如果想要更新服务和exe，直接执行这个脚本就可以build-and-restart.bat

---

> **核心哲学**：哪里需要改，一枪过去就是十环！全链路闭环，改一处不影响其他功能，一次修改好！
