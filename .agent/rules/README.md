# Codex2API 项目执行规则

本文件约束本项目后续的开发、验证、部署与服务运维。事实以当前代码、运行进程和接口响应为准，`.agent/handoff.md` 仅作为接续参考。

---

## 1. 项目架构全景

### 1.1 系统定位

Codex2API 是一个 OpenAI API 反向代理网关，核心职责是将多个上游账号（Codex/OpenAI 等）聚合为一个统一的 OpenAI 兼容 API 端点对外提供服务。上游账号本身是无状态的，代理层承担"有状态会话保持"的全部责任。

### 1.2 模块依赖关系

```
main.go
├── config        —— .env 环境变量加载
├── database      —— 数据库抽象层（支持 PostgreSQL / SQLite）
├── cache         —— 缓存层（支持 Redis / 内存）
├── auth          —— 账号池管理、调度、会话亲和、限流
│   ├── store.go                —— 核心账号调度器
│   ├── fast_scheduler.go       —— 快速调度器
│   ├── refresh_scheduler.go    —— 账号健康探测与恢复
│   └── token.go                —— Token 管理
├── proxy         —— 请求处理核心
│   ├── handler.go              —— HTTP POST 统一入口（Chat/Responses/Images/Messages）
│   ├── responses_ws.go         —— WebSocket 流式入口（GET /responses）
│   ├── translator.go           —— 请求/响应格式转换（Codex ↔ OpenAI）
│   ├── executor.go             —— 上游 HTTP 执行器（含 SessionID 提取）
│   ├── responses_continuity.go —— Responses 续链系统（多轮对话上下文保持）
│   ├── responses_memory_governor.go —— 内存压力治理（inflight 计数/字节预算）
│   ├── response_cache.go       —— Codex 工具调用响应缓存
│   ├── anthropic.go / handler_anthropic.go —— Anthropic Messages API 适配
│   ├── images.go               —— 图像生成/编辑（DALL-E 适配）
│   ├── ratelimit.go            —— 全局 / 账号级限流
│   ├── upstream_guard.go       —— 上游内容审计守卫
│   └── device_profile.go       —— 设备指纹伪装
├── admin         —— 管理后台 HTTP API（账号、API Key、用量统计等）
├── security      —— 安全过滤（Prompt Filter、请求大小限制、日志脱敏）
├── maintenance   —— 数据保留清理 + 磁盘容量采样
└── frontend      —— 嵌入式 React 管理台（打包后通过 embed.FS 静态托管）
```

### 1.3 关键 API 路由（双前缀注册）

| HTTP 方法 | 路径 | 处理函数 | 说明 |
|-----------|------|---------|------|
| POST | `/v1/chat/completions` 或 `/chat/completions` | `h.ChatCompletions` | 标准 Chat 接口 |
| POST | `/v1/responses` 或 `/responses` | `h.Responses` | Responses API（HTTP） |
| **GET** | `/v1/responses` 或 `/responses` | `h.ResponsesWebSocket` | **Codex 桌面端走此路由（WebSocket 升级）** |
| POST | `/v1/responses/compact` | `h.ResponsesCompact` | 紧凑式 Responses |
| POST | `/backend-api/codex/responses` | `h.Responses` | Codex 直连路径 |
| GET | `/backend-api/codex/responses` | `h.ResponsesWebSocket` | Codex 直连 WebSocket |
| POST | `/v1/messages` 或 `/messages` | `h.Messages` | Anthropic Compatible |
| POST | `/v1/images/generations` 等 | `h.ImagesGenerations` | 图像生成 |
| GET | `/v1/models` 或 `/models` | `h.ListModels` | 模型列表 |
| GET | `/health` | 健康检查 | `responses_memory` 字段含在途请求统计 |

> **⚠ 关键陷阱**：`GET /responses` 和 `POST /responses` 是两条**完全独立**的处理路径，逻辑不共享。Codex 桌面端（Codex CLI）使用 WebSocket 升级，走 `GET /responses`。普通第三方 API 客户端走 `POST /responses`。**任何涉及 Responses API 的逻辑改动都必须同时覆盖两条路径。**

---

## 2. Responses 续链系统（上下文保持机制）

### 2.1 系统职责

本代理的上游账号不支持 `previous_response_id`（第三方中转站），由代理层在本地维护完整的多轮对话历史，在每次续问时自动将历史注入到 `input` 字段中发送给上游。

### 2.2 核心数据流（4 个生命周期阶段）

```
客户端请求
    ↓
【展发阶段】buildOpenAIResponsesContinuationFallback()
    ├─ 有 previous_response_id → 从注册表加载历史链，展开打平写入 input
    └─ 无 previous_response_id + 有 session_id → 从 sessionLatest 查最新 responseID → 展开
    ↓
【账号亲和】bindOpenAIResponsesContinuationOwner()
    └─ 优先将续问路由到与上轮相同的上游账号
    ↓
发送给上游 → 流式响应
    ↓
【Pending 注册】RegisterPendingOpenAIResponsesContinuation()  ← response.created 事件触发
    └─ 在响应完成前占位，防止并发请求读到不完整的历史
    ↓
【完成缓存】cacheOpenAIResponsesContinuation()  ← response.completed 事件触发
    └─ 将本轮 input + output 写入内存注册表 + SQLite 持久化
```

### 2.3 双通道对称性要求（血泪教训）

**事件**：2026-07-29，上下文丢失 Bug 修复历经 4 轮、覆盖 5 天仍未根治。

**根因**：续链系统的 4 个核心函数只注入了 HTTP POST 路径（`handler.go`），WebSocket 路径（`responses_ws.go`）中完全没有调用。而 Codex 桌面端恰恰使用 WebSocket（`GET /responses`），导致历史从未缓存、从不展发，模型每轮只看到当前一句话。

**排查盲点**：单元测试全部 PASS（因为测试覆盖的是 HTTP 路径），日志显示 `GET /responses` 而非 `POST /v1/responses`，但 4 轮修复都没有注意到这个差异。

**正确排查方式**：先看运行日志确认走的是 `GET /responses` 还是 `POST /v1/responses`，再决定修哪个文件。

#### 强制对称检查表

任何涉及以下功能的修改，**必须同时**在 `handler.go` 和 `responses_ws.go` 中实现，否则视为未完成：

| 功能 | HTTP 路径 (`handler.go`) | WebSocket 路径 (`responses_ws.go`) |
|------|-------------------------|-----------------------------------|
| 续链历史展发 | `buildOpenAIResponsesContinuationFallback` | 同左 ✅ 已注入 |
| 续链账号亲和 | `bindOpenAIResponsesContinuationOwner` | 同左 ✅ 已注入 |
| Pending 注册 | `RegisterPendingOpenAIResponsesContinuation` | 同左 ✅ 已注入 |
| 完成缓存 | `cacheOpenAIResponsesContinuation` | 同左 ✅ 已注入 |
| 加密内容剥离 | `stripInvalidEncryptedContentFromResponsesBody` | 同左 ✅ 已注入 |
| 安全审计 | `upstreamGuardAudit.InspectRequest/Response` | 同左 ✅ 已注入 |
| 用量日志 | `logUsageForRequest` | 同左 ✅ 已注入 |

### 2.4 根因链（四层缺陷记录）

| 层 | 问题 | 症状 | 修复位置 |
|----|------|------|---------|
| L1 通道缺失 | WebSocket 路径完全未接续链系统 | 模型完全不记得上文 | `proxy/responses_ws.go` |
| L2 Header 大小写 | `ResolveSessionID` 只匹配精确 key，不兼容 `session-id` / `Session_Id` 变体 | 每轮请求生成随机 UUID，会话被拆散 | `proxy/executor.go` |
| L3 function_call 缺失参数 | 历史中 `function_call` 项缺少 `arguments` 字段 | 上游 HTTP 400：`Missing required parameter: 'input[N].arguments'` | `proxy/responses_continuity.go` |
| L4 SQLite 迁移顺序 | `session_id` 索引创建早于列补齐 | 已有数据库启动崩溃 `no such column: session_id` | `database/sqlite.go` |

### 2.5 关键设计约束

- **`entry.parentID`**：只有当 `entry.replayable == true` 时才赋值，否则保持空字符串。这影响 `removeSubtreeLocked` 的子树驱逐行为——如果非 replayable 条目保留了 parentID，会导致正常的 LRU 驱逐时无法连带清理孤儿子节点，破坏 `TestContinuationRegistryNeverReplaysAfterParentEviction` 的不变式。

- **`sessionLatest` map**：在 `registry.mu` 锁内维护，记录每个 sessionID 对应的最新 responseID，用于无 `previous_response_id` 时的 session-level fallback。

- **`streamResponsesWSUpstream` 签名**：新增与续链相关的功能时，需要同步扩展此函数的参数（已含 `continuationCacheBody []byte, sessionID string`）。

---

## 3. 服务热替换

### 3.1 单实例安全替换

当前服务若只有一个进程监听生产端口，只能做到安全短暂停顿，禁止宣称严格零中断。

每次替换必须依次完成：

1. 运行前端类型检查和生产构建；Go 程序嵌入 `frontend/dist`，因此必须先构建前端，再构建 EXE。
2. 将新程序构建为候选文件，不直接覆盖正在运行的 EXE；记录候选文件 SHA256。
3. 检查 `/health`，至少连续 3 次确认 `responses_memory.inflight_requests == 0`。
4. 当前 Agent/模型调用可能就是在途请求。若请求无法归零，禁止强停；应在当前响应结束后执行替换，或明确等待用户停止流量。
5. 通过系统运维的优雅停止接口关闭服务，等待进程完全退出和文件句柄释放；禁止默认使用 `Stop-Process -Force`、`taskkill /F`。
6. 旧 EXE 先改名为回滚文件，再把候选文件移动到正式路径；启动时必须设置正确工作目录。
7. 新进程启动后核对：监听端口、PID、运行 EXE SHA256、`/health.status == ok`、续链持久化状态及失败计数。
8. 任一检查失败必须停止新进程、恢复旧 EXE 并重新启动旧服务；回滚成功前不得宣布部署完成。
9. 验证完成后删除候选文件、回滚文件、临时密钥读取脚本和部署日志，避免敏感信息或废弃程序残留。

### 3.2 真正无感升级

严格零中断必须采用稳定入口代理和蓝绿双实例：

1. 入口代理固定监听对外端口。
2. 旧实例和新实例使用不同内部端口。
3. 先启动新实例并完成健康检查、关键接口冒烟和版本哈希核验。
4. 入口代理原子切流到新实例。
5. 旧实例停止接收新请求，等待在途请求排空后再优雅退出。
6. 新实例异常时立即切回旧实例。

未建立稳定入口代理前，不得把单进程停止、替换、重启描述为"无感热替换"。

### 3.3 标准执行模板

**阶段 A：在线准备（旧服务持续运行）**

1. 依次执行前端类型检查、前端生产构建、Go 测试和静态检查。
2. 使用 `CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o codex2api.new.exe .` 构建候选程序。
3. 计算并记录 `codex2api.new.exe` 的 SHA256；确认正式程序、候选程序和备份路径都位于项目根目录。
4. 连续读取 `/health`，确认服务正常且 `responses_memory.inflight_requests == 0` 后才进入替换窗口。

**阶段 B：单脚本替换（不可拆分）**

1. 从运行时安全来源读取管理密钥，只在内存中设置 `X-Admin-Key`，不得写入脚本、日志或接力文件。
2. 调用 `POST /api/admin/system/shutdown`，等待旧 PID 退出和程序文件句柄释放；仅在优雅退出明确超时后才允许强制结束旧 PID。
3. 将 `codex2api.exe` 改名为带时间戳的回滚文件，再将 `codex2api.new.exe` 原子移动为 `codex2api.exe`。
4. 使用 WMI/CIM `Win32_Process.Create`，指定项目根目录为 `CurrentDirectory`，脱离当前终端启动正式程序；或使用已有的 `start-codex2api.vbs` 启动脚本。
5. 轮询新服务直到 `/health.status == ok`，随后核对监听地址 `127.0.0.1:18080`、新 PID、运行文件 SHA256、在途请求和续链持久化状态。
6. 若启动或健康检查失败，立即停止新 PID、恢复回滚文件并重新启动旧版本；若全部通过，删除回滚文件和临时部署材料。

---

## 4. 验证与交付

代码变更完成后至少执行：

1. `go test ./... -count=1`（禁止使用缓存结果）
2. `go vet ./...`
3. `go build -o codex2api.new.exe`（确认编译无误）

涉及前端（管理台）的改动还必须：

1. `npm --prefix frontend run typecheck`
2. `npm --prefix frontend run build`

涉及 Responses API 续链的改动，交付前必须逐项确认：

- [ ] `handler.go` 中的改动是否在 `responses_ws.go` 中有对应实现？
- [ ] `responses_ws.go` 中的改动是否在 `handler.go` 中有对应实现？
- [ ] `streamResponsesWSUpstream` 的参数签名是否需要扩展？
- [ ] 运行日志是否覆盖了 `POST /v1/responses` 和 `GET /responses` 两种请求？
- [ ] 是否在真实客户端（Codex 桌面端）验证了多轮对话上下文保持？

---

## 5. 数据库与迁移

### 5.1 双数据库支持

- **PostgreSQL**：生产推荐，使用 `database/postgres.go`
- **SQLite**：轻量单机部署，使用 `database/sqlite.go`，数据库文件默认为 `codex2api.db`

### 5.2 迁移顺序约束

SQLite 的列添加通过 `ensureSQLiteColumn` 动态执行，在服务启动时幂等地补充新列。**新建列的索引必须放在 `indexStatements` 阶段**（即列补齐执行之后），绝不能放在 `CREATE TABLE` 的初始块中。

违反此规则的表现：已有数据库启动时报 `no such column: xxx`，新建数据库则正常——因为新建时列和索引是同时创建的。

### 5.3 续链持久化表

`responses_continuity` 表结构关键字段：

| 字段 | 说明 |
|------|------|
| `response_id` | 主键，上游返回的 response ID |
| `parent_id` | 前一轮 response ID，构成链式结构 |
| `session_id` | 客户端会话标识，用于无 `previous_response_id` 时的 fallback |
| `account_id` | 绑定的上游账号，用于账号亲和校验 |
| `replayable` | 是否可回放，`false` 时 input/output 为空 |
| `accessed_at` | LRU 时间戳，用于老化驱逐 |

---

## 6. 环境变量与配置

核心环境变量（详见 `.env.example`）：

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `CODEX_PORT` | `8080` | HTTP 监听端口 |
| `CODEX_BIND` | `0.0.0.0` | 监听地址 |
| `ADMIN_SECRET` | 空（首次访问浏览器初始化） | 管理台登录密钥 |
| `CODEX_ALLOW_ANONYMOUS` | `false` | `/v1/*` 匿名访问开关 |
| `DATABASE_DRIVER` | `postgres` | 数据库类型：`postgres` / `sqlite` |
| `CACHE_DRIVER` | `redis` | 缓存类型：`redis` / `memory` |
| `CODEX_RESPONSES_CONTINUITY_MODE` | `auto` | 续链模式：`auto`（本地展发）/ `upstream`（依赖上游） |
| `CODEX_RESPONSES_CONTINUITY_MAX_ENTRIES` | `2000` | 内存续链条目上限 |
| `CODEX_RESPONSES_MAX_INFLIGHT_REQUESTS` | `64` | 最大并发在途 Responses 请求数 |
| `CODEX_UPSTREAM_TRANSPORT` | `http` | 上游传输协议：`http` / `ws` |

---

## 7. 服务运维语义

- "停止服务"仅表示优雅关闭应用进程，不等同于关闭 Windows 系统。
- "更新最新服务"必须同时满足：代码已构建、运行文件哈希匹配候选文件、健康检查通过；仅复制文件或仅看到进程启动均不算完成。
- 不得为了部署测试中断真实模型请求；先排空流量，再操作服务。
- 服务由任务临时启动时，交付前必须关闭；生产服务需要保留时，必须在交付结果中明确 PID、端口和健康状态。

---

## 8. 仪表盘实时刷新

- 仪表盘 `1h` 实时范围在页面可见时每 15 秒刷新一次；其他趋势范围不启用自动轮询。
- 15 秒是数据拉取周期，不是动画时长。仪表盘可见数字仅在新旧值变化时执行 3 秒线性纯数字滚动。
- 自动刷新采用 stale-while-revalidate：请求期间保留旧值，新数据成功返回后原位更新，禁止先清空为 `-`。
- 首次加载且无历史值时允许显示 `-`；用户切换趋势时间范围时必须保留旧值作为过渡态，新范围数据返回后滚动替换，禁止闪 `-`。
- 首次加载和手动切换趋势时间时从 0 滚动到目标值；15 秒自动刷新从旧值滚动到新值，禁止周期性回零。
- 数字动画不得使用上下位移、透明度变化或闪烁；健康延迟无样本显示 `-`，动画起点显示 `0ms`。

---

## 9. 安全与仓库边界

- 管理密钥、令牌、账号凭证不得写入 Git、日志、接力文件或最终回复。
- 临时脚本只能放在项目明确的临时位置，使用后立即删除。
- 不覆盖、不回滚用户无关改动；提交前检查暂存区，只提交本任务文件。
- `.agent/handoff.md` 始终覆盖更新，不创建按日期散落的接力文件。
