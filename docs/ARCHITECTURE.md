# Codex2API 架构设计文档

本文档描述 Codex2API 的整体架构设计、核心组件及数据流。

## 目录

- [架构概览](#架构概览)
- [核心组件](#核心组件)
- [数据流](#数据流)
- [调度系统](#调度系统)
- [存储层](#存储层)
- [安全设计](#安全设计)

---

## 架构概览

### 整体架构图

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              客户端 (Client)                                 │
│  - Web 浏览器 (/admin)                                                       │
│  - API 调用 (/v1/*)                                                         │
└─────────────────────────────────────────────────────────────────────────────┘
                                       │
                                       ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                              入口层 (Ingress)                                │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │  Nginx / 负载均衡器                                                  │   │
│  │  - SSL 终止                                                        │   │
│  │  - 静态资源缓存                                                    │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────────────────┘
                                       │
                                       ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                           Codex2API 服务 (Go + Gin)                          │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │  HTTP 路由层                                                        │   │
│  │  ├── /admin/*     → React SPA (go:embed)                          │   │
│  │  ├── /api/admin/* → 管理 API (admin.Handler)                       │   │
│  │  ├── /v1/*        → 公共 API (proxy.Handler)                       │   │
│  │  └── /health      → 健康检查                                       │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│                                       │                                     │
│  ┌────────────────────────────────────┼─────────────────────────────────┐  │
│  │           中间件层 (Middleware)    │                                  │  │
│  │  ┌────────────────┐  ┌───────────┐ │  ┌────────────────────────────┐ │  │
│  │  │ Recovery       │  │ Logger    │ │  │ API Key Auth              │ │  │
│  │  │ ( panic 恢复 )  │  │ (请求日志) │ │  │ (公共 API 鉴权)            │ │  │
│  │  └────────────────┘  └───────────┘ │  └────────────────────────────┘ │  │
│  │  ┌────────────────┐  ┌───────────┐ │  ┌────────────────────────────┐ │  │
│  │  │ Admin Auth     │  │ RPM Limit │ │  │ CORS (开发环境)             │ │  │
│  │  │ (管理 API 鉴权) │  │ (全局限流) │ │  │                           │ │  │
│  │  └────────────────┘  └───────────┘ │  └────────────────────────────┘ │  │
│  └────────────────────────────────────┼─────────────────────────────────┘  │
│                                       │                                     │
│  ┌────────────────────────────────────▼─────────────────────────────────┐  │
│  │                         业务逻辑层                                    │  │
│  │  ┌──────────────────────┐  ┌──────────────────────────────────────┐ │  │
│  │  │   管理后台逻辑        │  │         代理转发逻辑                  │ │  │
│  │  │   (admin.Handler)    │  │        (proxy.Handler)               │ │  │
│  │  │  • 账号 CRUD         │  │                                     │ │  │
│  │  │  • 用量统计          │  │  • 账号选择 (auth.Store)            │ │  │
│  │  │  • 系统设置          │  │  • 请求翻译 (translator)            │ │  │
│  │  │  • 代理池管理        │  │  • 上游请求 (executor)              │ │  │
│  │  └──────────────────────┘  │  • 流式处理 (SSE)                   │ │  │
│  │                            └──────────────────────────────────────┘ │  │
│  └──────────────────────────────────────────────────────────────────────┘  │
│                                       │                                     │
│  ┌────────────────────────────────────▼─────────────────────────────────┐  │
│  │                          数据访问层                                   │  │
│  │  ┌──────────────────┐  ┌──────────────┐  ┌──────────────────────┐   │  │
│  │  │   Database       │  │    Cache     │  │      Token Store     │   │  │
│  │  │  (PostgreSQL/    │  │ (Redis/      │  │  (内存账号池状态)      │   │  │
│  │  │   SQLite)        │  │  Memory)     │  │                      │   │  │
│  │  │                  │  │              │  │  • 调度评分           │   │  │
│  │  │ • Accounts       │  │ • Session    │  │  • 并发计数           │   │  │
│  │  │ • Usage Logs     │  │ • Rate Limit │  │  • 健康状态           │   │  │
│  │  │ • Settings       │  │              │  │  • 冷却时间           │   │  │
│  │  └──────────────────┘  └──────────────┘  └──────────────────────┘   │  │
│  └──────────────────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────────────────┘
                                       │
                                       ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                           上游服务 (Codex/OpenAI)                            │
│  - api.openai.com                                                          │
│  - api.codex.openai.com                                                    │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## 核心组件

### 1. 路由处理器 (Handler)

#### proxy.Handler - 公共 API 处理器

```go
type Handler struct {
    store      *auth.Store          // 账号池
    configKeys map[string]bool      // 静态 API Keys
    db         *database.DB         // 数据库
    dbKeys     map[string]bool      // 动态 API Keys 缓存
    dbKeysUntil time.Time           // 缓存过期时间
}
```

**职责:**
- `/v1/chat/completions` - Chat Completions 接口
- `/v1/responses` - Responses 接口
- `/v1/models` - 模型列表
- API Key 认证
- 请求翻译 (OpenAI → Codex)
- 响应翻译 (Codex → OpenAI)

#### admin.Handler - 管理后台处理器

```go
type Handler struct {
    store          *auth.Store      // 账号池
    cache          cache.TokenCache // 缓存
    db             *database.DB     // 数据库
    rateLimiter    *proxy.RateLimiter // 限流器
    cpuSampler     *cpuSampler      // CPU 采样器
    // ... 其他字段
}
```

**职责:**
- 账号管理 (CRUD、导入导出)
- 用量统计与日志
- 系统设置管理
- 代理池管理
- 运维监控数据

### 2. 账号调度器 (auth.Store)

```go
type Store struct {
    accounts      []*Account        // 账号列表
    idx           uint64            // 轮询索引
    maxConcurrency int              // 最大并发
    globalRPM     int               // 全局 RPM
    // ... 其他配置
}

type Account struct {
    DBID          int64             // 数据库 ID
    RefreshToken  string            // 刷新令牌
    AccessToken   string            // 访问令牌
    Email         string            // 邮箱
    PlanType      string            // 套餐类型

    // 运行时状态
    activeReqs    int64             // 活跃请求数
    totalReqs     int64             // 总请求数
    healthTier    HealthTier        // 健康层级
    schedulerScore float64          // 调度分数

    // 同步机制
    mu            sync.RWMutex      // 读写锁
}
```

**调度流程:**

```
┌─────────────────────────────────────────────────────────────┐
│                      账号选择流程                            │
└─────────────────────────────────────────────────────────────┘
                            │
                            ▼
┌─────────────────────────────────────────────────────────────┐
│  1. 过滤阶段                                                │
│     • 排除 Status = error 的账号                            │
│     • 排除 HealthTier = banned 的账号                       │
│     • 排除处于冷却期的账号                                   │
│     • 排除 AccessToken 为空的账号                           │
└─────────────────────────────────────────────────────────────┘
                            │
                            ▼
┌─────────────────────────────────────────────────────────────┐
│  2. 评分阶段                                                │
│     • 计算健康层级 (healthy/warm/risky/banned)              │
│     • 计算调度分数 (100 基线 + 各种惩罚/奖励)                │
│     • 计算动态并发限制                                       │
└─────────────────────────────────────────────────────────────┘
                            │
                            ▼
┌─────────────────────────────────────────────────────────────┐
│  3. 选择阶段                                                │
│     • 排除已达并发上限的账号                                 │
│     • 按健康层级排序 (healthy > warm > risky)               │
│     • 同层级按调度分数排序                                   │
│     • 15% 概率随机打散                                       │
└─────────────────────────────────────────────────────────────┘
                            │
                            ▼
┌─────────────────────────────────────────────────────────────┐
│  4. 使用阶段                                                │
│     • 增加活跃请求计数                                       │
│     • 执行上游请求                                           │
│     • 减少活跃请求计数                                       │
│     • 报告成功/失败                                          │
└─────────────────────────────────────────────────────────────┘
```

### 3. 请求执行器 (proxy.Executor)

```go
// ExecuteRequest 执行上游请求
func ExecuteRequest(ctx context.Context, account *auth.Account,
    body []byte, sessionID, proxyURL string) (*http.Response, error)
```

**功能:**
- TLS 指纹伪装
- User-Agent 随机化
- 代理支持（全局/账号/代理池）
- 连接池复用
- SSE 流读取

### 4. 协议翻译器 (proxy.Translator)

```go
// TranslateRequest OpenAI Chat → Codex Responses
func TranslateRequest(body []byte) ([]byte, error)

// TranslateStreamChunk Codex SSE → OpenAI SSE
func TranslateStreamChunk(data []byte, model, chunkID string) ([]byte, bool)
```

**转换内容:**
- `messages` → `input`
- `max_tokens/temperature` → 删除（Codex 不支持）
- `reasoning_effort` → `reasoning.effort`
- Anthropic `/v1/messages` 的 `speed:"fast"` → Codex `service_tier:"priority"`（Anthropic 入参 `service_tier` 为 Priority Tier，不参与 fast mode 映射）
- SSE 事件类型转换

---

## 数据流

### 请求处理流程

```
┌─────────┐     ┌─────────────┐     ┌─────────────┐     ┌─────────────┐
│ Client  │────▶│   Router    │────▶│  Middleware │────▶│   Handler   │
└─────────┘     └─────────────┘     └─────────────┘     └──────┬──────┘
                                                               │
                    ┌──────────────────────────────────────────┼──────────┐
                    │                                          │          │
                    ▼                                          ▼          ▼
            ┌──────────────┐                         ┌──────────────┐  ┌─────────┐
            │ Account Store│                         │   Executor   │  │  Usage  │
            │   (Select)   │────────────────────────▶│   (Upstream) │  │ Logger  │
            └──────────────┘                         └──────┬───────┘  └─────────┘
                                                            │
                                                            ▼
                                                    ┌──────────────┐
                                                    │    Codex     │
                                                    │    API       │
                                                    └──────────────┘
```

### 账号池状态管理

```
┌─────────────────────────────────────────────────────────────────────────┐
│                          账号状态机                                      │
│                                                                         │
│    ┌──────────┐                                                        │
│    │  Initial │                                                        │
│    └────┬─────┘                                                        │
│         │ Import/Add                                                    │
│         ▼                                                               │
│    ┌──────────┐     ┌──────────┐     ┌──────────┐                      │
│    │  ready   │────▶│ cooldown │────▶│  ready   │                      │
│    └────┬─────┘     └──────────┘     └──────────┘                      │
│         │          (429/401 冷却)       (冷却结束)                       │
│         │                                                               │
│         │ Error                                                        │
│         ▼                                                               │
│    ┌──────────┐     ┌──────────┐                                       │
│    │  error   │────▶│ deleted  │                                       │
│    └──────────┘     └──────────┘                                       │
│                    (软删除)                                              │
└─────────────────────────────────────────────────────────────────────────┘
```

### 冷却机制与多上游语义分类 (v2.2.8+)

```go
// 冷却类型与时长
type CooldownRule struct {
    RateLimited  time.Duration  // 429 限流: 从响应头解析、语义分类或按套餐推断
    Unauthorized time.Duration  // 401 未授权: 5分钟 ~ 24小时
    Timeout      time.Duration  // 超时: 15分钟
    ServerError  time.Duration  // 5xx 错误: 15分钟
}

// 官方 Codex 套餐冷却策略
const (
    FreeCooldown     = 7 * 24 * time.Hour  // Free 套餐 429: 7天
    TeamCooldown5h   = 5 * time.Hour       // Team 5h 窗口用完: 5小时
    TeamCooldown7d   = 7 * 24 * time.Hour  // Team 7d 窗口用完: 7天
)

// 第三方/聚合上游通用语义分类策略 (matchGenericUpstreamRateLimit)
// 1. 日配额耗尽 (Daily Limit): 对齐至次日午夜 00:00:05，彻底消除每 5 分钟无效轮询探测
// 2. 周配额耗尽 (Weekly Limit): 7 天 (7*24h) 深度冷却
// 3. 欠费/额度耗尽 (Insufficient Quota): 标记 payment_required，休眠 24 小时
// 4. 短时频控 (RPM/TPM): 1 分钟快速自愈退避
// 5. 兜底未知 429: 5 分钟指数退避
```

---

## 调度系统

### 健康层级 (Health Tier)

```go
type HealthTier int

const (
    Healthy HealthTier = iota  // 健康
    Warm                        // 温热（近期有小问题）
    Risky                       // 风险（近期有大问题）
    Banned                      // 封禁
)
```

**层级判定:**

| 层级 | 条件 | 动态并发 |
|------|------|----------|
| Healthy | 无近期错误 | MaxConcurrency |
| Warm | 有轻微错误 | MaxConcurrency / 2 |
| Risky | 有严重错误 | 1 |
| Banned | 401 未授权 | 0 |

### 调度分计算

```go
type SchedulerScore struct {
    BaseScore float64 = 100

    // 惩罚项 (P)
    UnauthorizedPenalty float64  // 401: -50, 24h 衰减
    RateLimitPenalty    float64  // 429: -22, 1h 衰减
    TimeoutPenalty      float64  // 超时: -18, 15min 衰减
    ServerPenalty       float64  // 5xx: -12, 15min 衰减
    FailurePenalty      float64  // 连续失败: -6/次, 最多 -24
    UsagePenalty7d      float64  // 7d 用量: ≥70% -8, ≥100% -40
    LatencyPenalty      float64  // 延迟: ≥5s -4, ≥20s -15
    SuccessRatePenalty  float64  // 成功率: <75% -8, <50% -15

    // 奖励项 (B)
    SuccessBonus        float64  // 连续成功: +2/次, 最多 +12
}

// 最终分数 = BaseScore + ΣB - ΣP
```

### 调度算法伪代码

```
function SelectAccount():
    candidates = []

    for account in accounts:
        // 1. 基础过滤
        if account.Status == "error": continue
        if account.HealthTier == Banned: continue
        if account.IsInCooldown(): continue
        if account.AccessToken == "": continue

        // 2. 计算动态限制
        concurrencyLimit = calculateDynamicLimit(account.HealthTier)
        if account.ActiveRequests >= concurrencyLimit: continue

        // 3. 计算调度分
        account.SchedulerScore = calculateScore(account)

        candidates.append(account)

    if candidates is empty:
        return null

    // 4. 排序
    sort candidates by:
        - HealthTier (Healthy > Warm > Risky)
        - SchedulerScore (desc)
        - ActiveRequests (asc)

    // 5. 随机打散 (15% 概率)
    if random() < 0.15 and len(candidates) > 1:
        return candidates[random(0, min(3, len(candidates)))]

    return candidates[0]
```


### 模型映射与模型级冷却（v2.2.9）

**模型名映射的双路径差异**（重要，改动时勿混用）：

| 入口 | 是否对 Codex 原生账号替换模型名 | 原因 |
|------|------------------------------|------|
| `/v1/responses`（HTTP + WS，responses_api 分支） | 是 | 目标是第三方上游，必须换成上游认的模型名 |
| `/v1/chat/completions` | **否** | 翻译入口，映射目标通常是第三方名；换给官方上游必 400 |
| `/v1/messages` | **否** | 同上 |
| `/v1/responses`（WS，Codex 分支） | 是 | 只有 `modelIDInList(mappedModel, SupportedModelIDs(...))` 命中才会走到，属官方别名场景 |

调度侧对应约束：`accountFilterForResponsesModel(model, allowCodexAccounts)` 的第二参必须由 `modelIDInList(mappedModel, SupportedModelIDs(ctx, db))` 决定，不能硬编码 `true`。硬编码 `true` 会让 Codex 账号接收第三方模型名的请求。

### Chat 图片请求的 Responses→Chat 回退

部分第三方上游（如 Agnes）的 `/v1/responses` 不支持图片内容块，但同一账号的 `/v1/chat/completions` 支持。为避免把 Cuckoo 的标准 chat 图片请求翻译成 Responses 后丢图，`ChatCompletions` 在以下条件全部满足时回退：

- `responses_api` 账号；
- 原始下游 chat body 内含 `image_url`；
- `/v1/responses` 返回 400，且错误体含 `unknown` / `image` / `vision` / `multimodal` 关键词。

回退保留原始 chat body（含 `stream`、`tools`、`max_tokens`、`reasoning_effort`），只替换模型映射，直接 POST 同一账号的 `/v1/chat/completions`。成功时 JSON/SSE 原样回传，并按 chat `usage` 记用量；失败时继续原有 400 错误、重试与冷却路径。非图片 400 不触发回退，避免普通参数错误被悄悄改协议重试。

**模型级冷却的 key 一致性**：

冷却写入与调度过滤必须使用同一个 key，否则冷却永不命中。统一由 `cooldownModelFor(account, effectiveModel, mappedModel)` 决定：

- `account.IsOpenAIResponsesAPI()` 且 `mappedModel` 非空 → 返回 `mappedModel`（实际转发给上游的名字）
- 其余（Codex 原生账号）→ 返回 `effectiveModel`

所有 `applyCooldownForModel` / `ClearModelCooldown` 调用点都必须走这个 helper。`recheckAccountsAfterExhaustion` 额外对 responses_api 账号清一次 `resolveOpenAIResponsesModel(model, mapping)`，覆盖调用方只传 effectiveModel 的场景。

**账号级 vs 模型级的错误语义边界**：

`modelScopedQuotaFailure` 只认「分组 / 用户维度」的额度语义：

| 语义 | 归类 | 冷却范围 |
|------|------|----------|
| `insufficient user quota` / `insufficient_user_quota` | 模型级（分组额度） | 单模型 30 分钟 |
| `not enabled for this group` | 模型级（分组未开通） | 单模型 30 分钟 |
| `billing` + `hard limit` | 模型级（账单硬上限） | 单模型 30 分钟 |
| `insufficient_quota` / `insufficient quota` / `exceeded your current quota` | **账号级**（billing 耗尽） | 整账号 6 小时 |

误把账号级当模型级，会让账号其它模型继续被调度并反复撞墙。

**模型注册表缓存**：

`SupportedModelIDs(ctx, db)` 带 15 秒 TTL 缓存（`sync.RWMutex` + `time.Time`）。它在每个 chat/completions、messages、responses、`/v1/models` 请求里都会被调用，无缓存时每次触发两次 SQL（`ListModelRegistry` + `GetModelRegistrySyncState`）。

- 注册表写入后必须调用 `InvalidateSupportedModelIDsCache()`（`ApplyOfficialCodexModelSync` 已覆盖）
- **返回的切片是缓存的内部数组，调用方禁止原地修改或直接 append**。`enabledModelIDs` 用 `make([]string, 0, len(items))` 分配，存在 `cap > len` 的余量，直接 append 会写入空闲槽位并污染缓存。`supportedModelIDs` 已改为先 `append([]string(nil), ...)` 拷贝再追加。

**对外模型白名单（exposed_models）**：

只影响 `/v1/models` 的列表输出，**不影响请求校验**（校验走全量 `supportedModelIDs`，避免白名单过窄堵死账号实际能用的模型）。语义约定：

- 空配置 = 不启用过滤，输出全量
- 显式配置 = 只输出白名单 ∩ 实际可用模型的交集
- 默认 6 个模型仅在进程首次 `init()` 装载一次，保存过设置后不再回归默认值

**上游域名级 429 冷却规则（upstream_cooldown_rules）**：

`classify429RateLimit` 的三级优先级：强语义（日/周配额/欠费）→ 域名规则 → burst（RPM/TPM）兜底。域名规则让用户在设置页配的 `agnes-ai.cn = 120s` 能压过固定 1 分钟兜底；若上游 `Retry-After` 更长则取较大值。

---

### 公网隧道托管（v2.4.0）

公网域名由 Cloudflare Tunnel（named tunnel）暴露到本机 `18080`。隧道**不是**由服务进程拉起的外部依赖，而是由服务托管：

| 关注点 | 实现 |
|---|---|
| 凭据 | 隧道 token：`CODEX_TUNNEL_TOKEN` 环境变量 > `data/tunnel/tunnel-token.txt`（`/data/` 已在 gitignore） |
| 启动 | `cloudflared tunnel --metrics 127.0.0.1:20260 --logfile … run --token …`，`CREATE_NO_WINDOW` 脱控制台拉起 |
| 开机自启 | 启动目录写入隐藏 VBS `Codex2apiTunnel.vbs` + 标记文件 `data/tunnel/autostart.enabled`；删除两者即关闭 |
| 守护 | 服务内 goroutine 每 15s 巡检，自启开启且进程掉线时自动重启 |
| 状态探测 | 拨测本实例独占的 metrics 端口 `127.0.0.1:20260/ready`（返回 `readyConnections`），约 1ms |

**为什么删掉 tasklist 探针**：旧实现每 10 秒 spawn 一次 `tasklist`（实测 171ms ≈ 1.7% 单核常驻），且只看进程名，会把机器上其他 cloudflared 实例（例如 Cuckoo 自带隧道）误判成"我们这条隧道在跑"。现在只认 `20260` 这个端口——它由本实例独占，天然隔离。

**参数位置坑**：`--metrics` / `--logfile` 是 tunnel 级参数，必须写在 `run` 之前；`--token` 是 `run` 的子命令参数。写成 `tunnel run --token … --metrics …` 会被 cloudflared 判为 `flag provided but not defined` 并直接退出（exit 0，无 stderr），表现为"启动即消失"。

**协议选择**：cloudflared 启动 precheck 会自己判定 QUIC 是否可用，不可用时打印 `degraded transport` 并回落 `http2`。不要硬编码 `--protocol`，否则会牺牲掉可用的 QUIC 连接。

接口：`GET /api/admin/tunnel`、`POST /api/admin/tunnel/autostart {enabled}`、`POST /api/admin/tunnel/start`。

---

## 存储层

### 数据库 Schema

```sql
-- 账号表
CREATE TABLE accounts (
    id BIGSERIAL PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    status VARCHAR(50) DEFAULT 'ready',
    credentials JSONB DEFAULT '{}',      -- email, refresh_token, access_token, etc.
    proxy_url VARCHAR(500),
    cooldown_until TIMESTAMP,
    cooldown_reason VARCHAR(50),
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW(),
    deleted_at TIMESTAMP                 -- 软删除
);

-- 用量日志表
CREATE TABLE usage_logs (
    id BIGSERIAL PRIMARY KEY,
    account_id BIGINT REFERENCES accounts(id),
    account_email VARCHAR(255),
    endpoint VARCHAR(100),
    model VARCHAR(100),
    status_code INTEGER,
    duration_ms INTEGER,
    first_token_ms INTEGER,
    prompt_tokens INTEGER DEFAULT 0,
    completion_tokens INTEGER DEFAULT 0,
    total_tokens INTEGER DEFAULT 0,
    reasoning_effort VARCHAR(20),
    stream BOOLEAN DEFAULT FALSE,
    service_tier VARCHAR(20),
    created_at TIMESTAMP DEFAULT NOW()
);

-- 系统设置表
CREATE TABLE system_settings (
    id INTEGER PRIMARY KEY DEFAULT 1,
    max_concurrency INTEGER DEFAULT 2,
    global_rpm INTEGER DEFAULT 0,
    test_model VARCHAR(100) DEFAULT 'gpt-5.5',
    test_concurrency INTEGER DEFAULT 50,
    proxy_url VARCHAR(500),
    pg_max_conns INTEGER DEFAULT 50,
    redis_pool_size INTEGER DEFAULT 30,
    auto_clean_unauthorized BOOLEAN DEFAULT FALSE,
    auto_clean_rate_limited BOOLEAN DEFAULT FALSE,
    admin_secret VARCHAR(255)
);

-- API Keys 表
CREATE TABLE api_keys (
    id BIGSERIAL PRIMARY KEY,
    name VARCHAR(255),
    key_hash VARCHAR(255) UNIQUE NOT NULL,
    created_at TIMESTAMP DEFAULT NOW()
);

-- 代理池表
CREATE TABLE proxies (
    id BIGSERIAL PRIMARY KEY,
    url VARCHAR(500) NOT NULL,
    label VARCHAR(255),
    enabled BOOLEAN DEFAULT TRUE,
    last_tested_at TIMESTAMP,
    last_test_result VARCHAR(50),
    latency_ms INTEGER
);

-- 账号事件表（用于趋势分析）
CREATE TABLE account_events (
    id BIGSERIAL PRIMARY KEY,
    account_id BIGINT REFERENCES accounts(id),
    event_type VARCHAR(50),  -- added, deleted, status_changed
    event_data JSONB,
    created_at TIMESTAMP DEFAULT NOW()
);
```

### 缓存策略

```go
// Redis 缓存结构
const (
    // 限流计数器
    KeyRateLimit = "ratelimit:{window}"

    // 会话缓存
    KeySession = "session:{session_id}"

    // 用量统计缓存
    KeyUsageStats = "usage:stats:{date}"

    // 账号 Token 缓存
    KeyAccessToken = "token:{account_id}"
)

// 缓存 TTL
type CacheTTL struct {
    RateLimit    time.Duration = 1 * time.Minute
    Session      time.Duration = 1 * time.Hour
    UsageStats   time.Duration = 5 * time.Minute
    AccessToken  time.Duration = 30 * time.Minute
}
```

---

## 安全设计

### 认证机制

```
┌─────────────────────────────────────────────────────────────┐
│                      认证层级                                │
├─────────────────────────────────────────────────────────────┤
│  Level 1: 传输层                                             │
│  • HTTPS/TLS 加密                                           │
│  • HSTS 头部                                                │
├─────────────────────────────────────────────────────────────┤
│  Level 2: API 认证                                           │
│  • API Key Bearer Token                                     │
│  • 5分钟缓存，减少 DB 查询                                   │
├─────────────────────────────────────────────────────────────┤
│  Level 3: Admin 认证                                         │
│  • X-Admin-Key 请求头                                       │
│  • 支持环境变量或数据库配置                                  │
│  • 所有管理操作审计日志                                      │
└─────────────────────────────────────────────────────────────┘
```

### 敏感数据处理

```go
// Token 存储
func StoreToken(db *DB, accountID int64, token string) {
    // 数据库只存储加密后的 token
    encrypted := encrypt(token, encryptionKey)
    db.Exec("UPDATE accounts SET credentials = jsonb_set(credentials, '{access_token}', $1) WHERE id = $2",
        encrypted, accountID)

    // 同时缓存到 Redis（加密存储）
    redis.Set(ctx, fmt.Sprintf("token:%d", accountID), encrypted, 30*time.Minute)
}

// 日志脱敏
func SanitizeLog(msg string) string {
    // 隐藏 token 详情
    msg = regexp.MustCompile(`token=[^\s]+`).ReplaceAllString(msg, "token=***")
    msg = regexp.MustCompile(`Bearer [^\s]+`).ReplaceAllString(msg, "Bearer ***")
    return msg
}
```

### 限流与防护

| 层级 | 机制 | 说明 |
|------|------|------|
| 全局 | RPM 限流 | 基于令牌桶算法 |
| 账号 | 并发限制 | 动态调整 |
| IP | 连接限制 | 防止单 IP 过多连接 |
| 应用 | 请求大小 | 限制请求体大小 |

---

## 扩展性设计

### 水平扩展

```
┌─────────────────────────────────────────────────────────────┐
│                      负载均衡器                              │
│                   (Nginx / HAProxy)                         │
└─────────────────────────────────────────────────────────────┘
          │              │              │
          ▼              ▼              ▼
┌─────────────────┐ ┌─────────────────┐ ┌─────────────────┐
│  Codex2API #1   │ │  Codex2API #2   │ │  Codex2API #3   │
│                 │ │                 │ │                 │
│  + PostgreSQL   │ │  + PostgreSQL   │ │  + PostgreSQL   │
│  + Redis        │ │  + Redis        │ │  + Redis        │
└─────────────────┘ └─────────────────┘ └─────────────────┘
          │              │              │
          └──────────────┼──────────────┘
                         ▼
              ┌─────────────────┐
              │   共享 Redis    │
              │  (Session/锁)   │
              └─────────────────┘
```

### 组件替换

| 组件 | 当前实现 | 可替换为 |
|------|----------|----------|
| 数据库 | PostgreSQL | MySQL, TiDB |
| 缓存 | Redis | Memcached, 本地缓存 |
| HTTP | Gin | Echo, Fiber |
| 前端 | React | Vue, Svelte |
