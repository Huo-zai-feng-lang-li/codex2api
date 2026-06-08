# 本地中转服务第三方上游安全防护计划

## 当前状态

- 创建时间：2026-06-08 12:35:32 +08:00
- 当前阶段：方案重整完成，等待实现
- 用户最新要求：重新整理计划，重点保证安全性、准确性，支持用户选择告警、拦截等策略
- 实施原则：先观测、低误报、可解释、可回滚；默认告警不阻断业务

## 核心目标

为 `codex2api` 增加第三方上游安全防护能力，把第三方 API Key / base_url 返回的数据视为不可信输入，防止源码/密钥外泄、响应注入、工具调用伪造、异常字段污染，同时避免把正常代码助手输出误判成攻击。

## 二次审查结论

原计划方向正确，但仍缺少几类会影响“安全性、准确性、完善度”的关键边界：

- 缺少误报校准闭环：需要样本集、规则版本、人工标记、抑制规则。
- 缺少第三方 API 传输安全：需要 TLS、重定向 allowlist、超时、响应大小上限。
- 缺少流式拦截边界说明：已发送给客户端的 token 无法撤回，严格拦截必须接受延迟闸门。
- 缺少日志治理：需要保留期限、容量上限、导出脱敏、日志注入防护。
- 缺少扫描失败策略：安全模块异常时到底放行、告警还是拦截必须按模式明确。
- 缺少性能防护：大响应、base64 图片、压缩膨胀、长 SSE 不能拖垮服务。

本计划已补齐这些边界。

## 事实依据

- OWASP API10:2023 Unsafe Consumption of APIs：第三方 API 数据不能默认信任，需要校验清洗；不要盲目跟随重定向；需要限制第三方响应资源消耗；需要超时。
  - `https://owasp.org/API-Security/editions/2023/en/0xaa-unsafe-consumption-of-apis/`
- OWASP LLM Prompt Injection Prevention：Prompt Injection 可能导致数据外泄、系统提示泄露、未授权工具/API 行为；建议输入校验、输出监控、远程内容清洗、最小权限、人工确认。
  - `https://cheatsheetseries.owasp.org/cheatsheets/LLM_Prompt_Injection_Prevention_Cheat_Sheet.html`
- OWASP Logging Cheat Sheet：日志不能写入敏感信息，日志内容需要防注入和防伪造。
  - `https://cheatsheetseries.owasp.org/cheatsheets/Logging_Cheat_Sheet.html`

## 架构洞察

不能把“关键词命中”当成攻击结论。代码助手正常输出里天然会出现 `ignore previous instructions`、`.env`、`BEGIN PRIVATE KEY`、`tool_calls` 等安全敏感文本。真正可靠的判断必须结合方向、上下文、结构位置、账号来源、是否请求用户上传/泄露、是否试图触发工具、是否包含真实密钥格式。

因此本方案采用“分层评分 + 可解释规则 + 策略模式”，而不是粗暴黑名单。

## 准确性校准机制

新增规则不得只靠正则命中定级，必须有样本校准。

### 样本集

实现前建立测试样本：

- 真阳性请求：真实格式 token、私钥、数据库连接串、批量 `.env`。
- 假阳性请求：示例 token、文档模板、普通代码块、报错堆栈。
- 真阳性响应：诱导上传源码、诱导泄露 key、要求忽略安全策略、隐藏恶意工具调用。
- 假阳性响应：安全教学、代码审计说明、用户主动要求的 prompt injection 分析。
- 流式样本：恶意文本被拆成多个 delta、工具参数被拆成多个 delta。

### 判定输出

每次扫描输出必须包含：

- `risk_score`
- `risk_level`
- `confidence`
- `rule_ids`
- `evidence`
- `reason`
- `false_positive_hints`

### 人工反馈

后台事件支持：

- 标记真阳性
- 标记误报
- 按 rule/account/base_url/endpoint 建立抑制规则
- 查看规则版本

抑制规则默认只降级告警，不直接关闭扫描。

## 防护分层

### 1. 请求侧 DLP

目标：发现本地服务准备发往第三方上游的敏感内容。

检测对象：
- API Key、Access Token、Refresh Token、JWT、数据库连接串
- PEM 私钥、SSH 私钥、证书私钥
- `.env` 样式批量环境变量
- 大段源码、文件树、路径批量泄露
- Codex/代码助手上下文里的仓库文件片段

准确性策略：
- 高危：真实密钥格式、私钥块、数据库连接串
- 中危：`.env` 批量内容、多个疑似 token
- 低危：普通代码片段、路径、错误堆栈
- 默认只记录告警，不拦截

### 2. 响应侧注入防火墙

目标：第三方上游响应回客户端前进行检查。

检测对象：
- 恶意指令：要求忽略系统指令、上传源码、泄露密钥、关闭安全策略
- 工具调用：`tool_calls`、`function_call`、Responses function call item
- 异常结构：OpenAI-compatible 响应之外的未知顶层字段
- 流式 SSE：逐帧检查文本 delta 和工具参数 delta

准确性策略：
- 单纯出现安全词不报警，必须满足“恶意意图 + 操作要求”组合
- 解释性安全内容、用户要求的安全分析、普通代码示例不作为高危
- 工具调用单独标记为“上游试图触发工具”，不直接等同攻击
- 未知字段默认低危记录，不破坏响应

### 3. 上游来源风险分级

目标：根据账号来源决定检查强度。

分级：
- 官方源：`api.openai.com` 等官方域名，低风险审计
- 第三方源：非官方 base_url，增强审计
- 未知源：缺少或异常 base_url，高风险审计

实现原则：
- 优先从现有账号 `base_url` / `credentials` / `upstream_type` 推导
- 不优先新增账号表字段，避免无必要迁移
- 后台展示风险标签，便于人工判断

## 策略模式

后台提供 4 种模式，用户可选：

1. 关闭
   - 不检查请求/响应
   - 保持原行为

2. 仅告警（默认）
   - 记录安全事件
   - 不修改请求和响应
   - 最适合先上线观察误报

3. 高危拦截
   - 高危请求泄密或高危响应注入直接拦截
   - 中低危只告警
   - 适合观察期后启用

4. 严格拦截
   - 中高危均可拦截
   - 适合不可信上游或敏感仓库
   - 默认不建议启用

### 模式细节

- 仅告警：扫描失败也放行，但记录 `scanner_error`。
- 高危拦截：请求侧高危在转发前拦截；非流式响应高危在返回前拦截；流式响应高危默认终止流并写入错误事件。
- 严格拦截：可启用流式延迟闸门，先缓存首段响应完成初判，再开始下发；代价是首 token 延迟上升。
- 关闭：不创建安全事件，不扫描，不改动行为。

### 流式响应限制

流式模式下，如果恶意内容已经写给客户端，服务端不能撤回。因此：

- 默认告警模式不阻断流式。
- 高危拦截模式可在检测到高危后终止流，但已发送内容无法撤回。
- 严格拦截模式可以开启“小窗口延迟闸门”，先扫描首批数据再释放。
- 后台必须明确展示该限制，避免用户误以为流式拦截可做到完全回滚。

## 事件与告警

新增 `security_events`，保存脱敏后的安全事件。

字段建议：
- `id`
- `created_at`
- `direction`：request / response
- `action`：allow / warn / block
- `risk_level`：low / medium / high / critical
- `risk_score`
- `endpoint`
- `model`
- `account_id`
- `account_name`
- `base_url`
- `source_type`：official / third_party / unknown
- `stream`：是否流式
- `tool_call`：是否涉及工具调用
- `rules`：命中规则 JSON
- `preview`：脱敏预览
- `content_hash`：内容 hash
- `request_id`

日志原则：
- 不保存完整源码
- 不保存完整 prompt
- 不保存完整响应
- 不保存 API Key / Token 原文
- preview 必须脱敏并截断
- 对换行、控制字符、ANSI 转义做清理，防止日志注入
- 默认保留 30 天或最多 10000 条，先到先清
- 导出安全事件时二次脱敏
- 只有管理员接口可读写安全事件

## 传输与资源安全

第三方上游不只会注入文本，还可能通过传输层和资源消耗攻击本地服务。

必须补充：

- 仅允许 HTTPS 上游进入“可信/第三方增强审计”路径；HTTP 上游标记高风险。
- HTTP 重定向不得盲目跟随，跨 host 重定向默认拒绝或记录高危。
- 上游请求必须设置连接、首字节、整体超时。
- 上游响应大小设置扫描上限和硬上限，避免超大响应耗尽内存。
- 压缩响应解压后也要计入大小上限。
- base64 图片、二进制大块默认不做全文扫描，只做元数据和大小检查。
- WebSocket/Responses websocket 转 SSE 路径也必须纳入响应侧检查。

## 扫描失败策略

安全模块异常不能静默失败。

- 关闭模式：不扫描，不产生日志。
- 仅告警：扫描失败放行，写 `scanner_error` 事件。
- 高危拦截：请求侧扫描失败默认放行并告警；响应侧扫描失败默认放行并告警，避免误伤。
- 严格拦截：扫描失败可配置为拦截，默认只对未知第三方上游启用。

扫描失败包括：
- JSON 解析异常
- SSE 帧解析异常
- 规则编译异常
- 数据库写事件失败
- 扫描超时

数据库写事件失败不得阻塞主请求。

## 后台能力

设置页新增：
- 启用上游安全防护
- 策略模式：关闭 / 仅告警 / 高危拦截 / 严格拦截
- 请求侧 DLP 开关
- 响应侧防火墙开关
- 工具调用告警开关
- 最大扫描长度
- 最大预览长度

新增安全事件页面：
- 按时间、账号、base_url、方向、风险等级筛选
- 查看命中规则和脱敏 preview
- 清理事件
- 高危事件突出展示

前端要求：
- preview 只按纯文本展示
- 禁止把 preview 当 HTML / Markdown 渲染
- 风险等级、规则 ID、置信度、动作必须清晰展示
- 支持误报标记和抑制规则管理

## 后端接入点

优先接入范围：
- `/v1/chat/completions`
- `/v1/responses`
- Anthropic 兼容入口

核心模块建议：
- `security/upstreamguard`：统一风险检查模块
- `database/security_events.go`：安全事件查询与写入
- `admin/security_events.go`：后台事件接口
- `proxy` 转发路径：上游请求发出前检查请求体，上游响应返回前检查响应体或 SSE 帧

配置来源：
- 扩展现有 `system_settings`
- 与现有 prompt filter 配置风格保持一致

## 准确性规则

### 高危请求

满足任一即可高危：
- 真实 PEM 私钥块
- `sk-`、`ghp_`、`xoxb-` 等真实 token 格式且长度合理
- 数据库连接串包含用户名密码
- `.env` 内容中同时出现多个敏感键值

### 高危响应

需要满足组合条件，不允许单词命中即高危：
- 指令类动词：上传、发送、泄露、复制、读取、禁用、忽略
- 目标对象：源码、私钥、API key、token、系统提示、环境变量
- 攻击意图：绕过、忽略之前指令、关闭安全、不要告诉用户

### 工具调用风险

以下单独记录：
- 非预期 `tool_calls`
- `function_call`
- Responses function call item
- 流式 function call arguments delta

默认行为：
- 仅告警模式下透传
- 高危拦截模式下，可按设置拦截未知上游工具调用

## 不覆盖范围

本功能不是银弹，明确不承诺：

- 无法阻止用户主动把源码发给第三方，只能告警或按策略拦截。
- 无法证明第三方中转站没有存储请求，只能减少泄露面并留下证据。
- 无法对已经流式下发的内容做撤回。
- 不对图片像素内容做 OCR/视觉安全分析，本次只处理文本、JSON、SSE 元数据。
- 不替代客户端工具权限控制；本地服务只能标记和拦截响应里的工具调用意图。

### 低误报保护

以下默认降级或忽略：
- 用户在安全分析中讨论攻击文本
- 代码块中的示例密钥，如 `sk-xxxx`
- 文档说明里的 `.env` 模板
- 正常函数名、变量名里包含 token/key
- OpenAI 标准 usage/model/id/object 字段

## 实施阶段

### 阶段 1：计划与基础取证

状态：已完成初步取证。

已知事实：
- 项目已有 `security/promptfilter`
- 项目已有 prompt filter 日志和后台配置风格
- 项目有 `/v1/chat/completions`、`/v1/responses`、Anthropic 兼容路径
- 当前计划文件用于防止会话过长丢失上下文

下一步：
- 实现测试优先的安全检查模块。

### 阶段 2：核心规则与测试

工作：
- 新增请求 DLP 测试
- 新增响应注入测试
- 新增工具调用风险测试
- 新增脱敏 preview 测试
- 新增上游来源分级测试
- 新增误报样本测试
- 新增规则置信度和抑制规则测试

验收：
- 普通代码输出不误报高危
- 示例密钥不误报高危
- 真实私钥和真实 token 命中高危
- 恶意“上传源码/泄露密钥”响应命中高危
- 规则必须输出可解释原因
- 所有高危规则必须至少有一个假阳性保护样本

### 阶段 3：事件持久化

工作：
- 新增 `security_events` 表
- SQLite/Postgres 同步
- 写入、分页、清理接口

验收：
- 能写入脱敏事件
- 能分页查询
- 能按风险等级、方向、账号筛选
- 能清理事件

### 阶段 4：代理链路接入

工作：
- 请求上游前执行 DLP
- 非流式响应返回前执行响应防火墙
- 流式 SSE 逐帧执行检查
- 默认仅告警不阻断
- 增加重定向检查、响应大小上限、扫描超时
- WebSocket 转 SSE 路径纳入检查

验收：
- 正常请求响应不变
- 命中风险时产生事件
- 流式响应不中断
- 扫描失败有事件记录但不静默
- 超大响应不会造成内存暴涨
- 跨 host 重定向不会带着敏感请求体盲目转发

### 阶段 5：后台配置与事件页面

工作：
- 设置页加入策略配置
- 新增安全事件页面或扩展现有安全页面
- 账号列表展示来源风险标签
- 增加误报标记、抑制规则、日志保留配置

验收：
- 用户可选择关闭/告警/高危拦截/严格拦截
- 事件页面能看到脱敏 preview 和命中规则
- 前端不渲染不可信 HTML/Markdown
- 用户能查看置信度和规则解释
- 用户能把误报降级

### 阶段 6：整体验证

工作：
- Go 单元测试
- 前端类型检查
- 关键代理路径回归
- 手工模拟第三方恶意响应
- 压测长 SSE 和大响应
- 校准误报/漏报样本集

验收：
- 关闭模式完全回到旧行为
- 告警模式不阻断业务
- 高危拦截模式能拦截真实高危样本
- 无明显误报高危
- 误报样本不得被判为 high/critical
- 真实密钥样本不得低于 high
- 安全事件不包含原始密钥
- 流式严格模式的延迟影响可观测

## 影响范围

可能修改：
- `security/`
- `database/`
- `admin/`
- `proxy/`
- `frontend/src/types.ts`
- `frontend/src/api.ts`
- `frontend/src/pages/Settings.tsx`
- 新增安全事件页面组件

不应影响：
- 账号新增/编辑名称逻辑
- 使用统计现有展示
- 当前 prompt filter 对用户请求的原有行为
- 正常 OpenAI-compatible 响应透传

## 验证命令

后端：

```powershell
go test ./security/... ./database/... ./admin/... ./proxy/...
```

前端：

```powershell
cd frontend
pnpm typecheck
```

全量：

```powershell
go test ./...
```

## 阶段更新模板

每完成一个阶段，在本文件追加：

```markdown
## 阶段更新：阶段 N

- 时间：
- 已完成：
- 验证：
- 风险：
- 下一步：
```

---

## 阶段更新：阶段 2

- 时间：2026-06-08 13:20:34 +08:00
- 已完成：新增 `security/upstreamguard` 规则引擎和测试，覆盖请求 DLP、响应注入、工具调用风险、脱敏 preview、上游来源分级、误报样本、规则置信度和抑制降级；默认模式为仅告警。
- 验证：`go test ./security/upstreamguard` 通过；`go test ./security/...` 通过。
- 风险：当前仅完成核心规则层，尚未写入 `security_events`，尚未接入代理链路，不会影响现有请求/响应透传。
- 下一步：阶段 3，新增脱敏安全事件持久化与分页/筛选/清理接口。

---

## 阶段更新：阶段 3

- 时间：2026-06-08 13:44:06 +08:00
- 已完成：新增 `security_events` SQLite/Postgres 表、索引、写入、分页筛选、清理方法；新增后台 `GET/DELETE /api/admin/security-events`；写入侧会清理控制字符并脱敏 token preview，写库失败接口可由调用侧忽略。
- 验证：`go test ./database -run SecurityEvents` 通过；`go test ./admin -run SecurityEventsRoutes` 通过；`go test ./database ./admin` 通过。
- 风险：事件接口已可用，但代理链路尚未调用 `recordSecurityEvent` / DB 写入；前端页面与配置尚未接入。
- 下一步：阶段 4，接入代理请求/响应/SSE 告警路径，保持默认仅告警不阻断。

---

## 阶段更新：阶段 4 配置链路红灯

- 时间：2026-06-08 14:05:00 +08:00
- 已完成：确认代理检查点与事件写入已存在，但 `currentUpstreamGuardConfig()` 仍固定返回默认配置；`system_settings`、`auth.Store`、后台设置 API 尚未贯通 upstream guard 模式。
- 验证：待先写失败测试，覆盖 SQLite 设置持久化、Store 运行态读取、代理关闭模式不扫描不写事件。
- 风险：若只在 proxy 层硬编码 off/warn，会导致后台配置和真实代理行为脱节。
- 下一步：按 TDD 补配置链路测试，再实现数据库字段、Store getter/setter、后台 GET/PUT 字段和 proxy 读取。

---

## 阶段更新：阶段 4 配置链路绿灯

- 时间：2026-06-08 15:31:25 +08:00
- 已完成：新增 `upstream_guard_mode` 设置字段，默认 `warn`，支持 `off/warn/high_block/strict`；SQLite/Postgres 迁移、`SystemSettings` 读写、`auth.Store` 热更新、proxy 读取 Store 快照、后台 `/api/admin/settings` GET/PUT、前端设置类型默认值均已接入。
- 验证：`go test ./database -run UpstreamGuardMode` 通过；`go test ./auth -run UpstreamGuardConfig` 通过；`go test ./proxy -run "UpstreamGuard(UsesStoreOffMode|OffMode)"` 通过；`go test ./admin -run UpstreamGuardMode` 通过；`go test ./security/upstreamguard ./database ./auth ./admin ./proxy` 通过；`go test ./...` 通过；`npm run typecheck` 通过。
- 风险：当前只打通配置与 API，前端尚未新增可视化模式选择控件；设置页保存时会保留并回传字段。
- 下一步：阶段 5 可继续做前端安全事件页面与模式选择 UI。

---

## 阶段更新：阶段 5 预览可读性优化

- 时间：2026-06-08 17:31:20 +08:00
- 已完成：安全事件页面新增 preview 摘要解析，把 Responses `function_call` / `response.output_item.done` 这类 JSON 展示为工具、命令、目录、状态、调用 ID 等结构化字段；列表只展示摘要，详情弹窗保留完整纯文本/JSON 预览和命中规则，不改变后端存储与扫描策略。
- 验证：针对 `shell_command` + `npm run build` 样本的本地解析验证通过；`npm run typecheck` 通过；普通沙箱构建因 Windows 原生依赖/子进程权限失败，提权后 `npm run build` 通过。
- 风险：仅影响后台安全事件查看体验；不会改变代理转发、事件写入、风险评分或告警/阻断策略。
- 下一步：继续阶段 5 剩余项，补设置页可视化模式选择与安全事件页面的浏览器验收。

---

## 阶段更新：阶段 5 前端验收绿灯

- 时间：2026-06-08 17:40:53 +08:00
- 已完成：确认设置页已接入 `upstream_guard_mode` 可视化选择、`security_event_retention_days` 保留天数和 `upstream_guard_suppressions` 误报抑制输入；安全事件页通过浏览器 mock 验收，`npm run build` 工具调用样本在列表显示为可读摘要，详情弹窗保留完整 JSON 和结构化字段。
- 验证：Playwright 断言通过：安全事件列表摘要、详情命令/原始事件类型、设置页上游防护模式/仅告警/误报抑制均可见，新增控制台错误 0；`npm run typecheck` 通过；`npm run build` 通过；`go test ./security/... ./database/... ./admin/... ./proxy/...` 通过；`go test ./...` 通过。
- 风险：前端 dev server 仅用于本地验收；安全事件浏览器验收使用 mock API，不依赖真实后端数据。当前仍保持默认“仅告警”，未改变正常代理透传。
- 下一步：阶段 6，按计划做最终完成审计：关闭模式完全回旧行为、告警模式不阻断、高危/严格阻断只拦真实高危样本，并复核完整影响范围。

---

## 阶段更新：阶段 6 完成审计

- 时间：2026-06-08 17:44:51 +08:00
- 已完成：补齐阶段 6 回归证据：新增代理路径测试证明 `off` 模式风险请求仍到上游且不记安全事件、`warn` 模式风险请求仍到上游且只记告警；新增规则层测试证明 `strict` 模式会阻断 medium 级来源风险，而默认 `warn` 仅告警。复核现有测试已覆盖真实密钥/私钥/数据库连接串高危、误报样本降级、工具调用低危告警、scanner_error 放行记事件、高危请求和非流响应阻断、blocked 响应不泄露上游高危内容。
- 验证：`go test ./proxy -run "UpstreamGuard|ResponsesCompact(Off|Warn|HighBlock)"` 通过；`go test ./security/upstreamguard` 通过；`go test ./security/... ./database/... ./admin/... ./proxy/...` 通过；`go test ./...` 通过；`npm run typecheck` 通过；`npm run build` 通过；Playwright 安全事件/设置页断言通过且新增控制台错误 0。
- 风险：流式严格模式没有实现“缓存首段再释放”的延迟闸门，本轮采用逐块扫描，检测到阻断后终止流并写安全事件；已发送给客户端的流式内容仍无法撤回，这一点与计划边界一致。流式首包影响可通过现有 `first_token_ms` / Usage 首 token 延迟观测。图片 OCR/视觉内容安全不在本轮范围。
- 下一步：进入人工验收和后续策略调优；如需把 `strict` 做成真正的首段延迟闸门，应单独立项，明确可接受的首 token 延迟预算。
