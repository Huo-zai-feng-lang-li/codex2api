# 最新接续状态 (2026-11-24 23:05)

## 2026-11-24 Agnes 图片 400 回退

- **根因**：Agnes `/v1/responses` 不支持图片内容块（报 `type='unknown'`），同 Key 的 `/v1/chat/completions` 支持图片。
- **修复**：新增 `proxy/chat_completions_fallback.go`；`ChatCompletions` 在图片请求收到 Responses 400 时，保留原始 chat body 直打同账号 `/v1/chat/completions`，JSON/SSE 原样回传；非图片 400 不回退。
- **测试**：3 个新增测试（非流式/流式/纯文本不触发）全过；`go test ./...`、`go vet ./...` 全过。
- **真机**：Agnes 图片非流式 200“红色”，流式 200“绿色”，`data: [DONE]` 正常；`usage_logs` 显示 `account_id=867`、`upstream_endpoint=/v1/chat/completions`、200。
- **号池**：`856` 局域网账号已软删除；12 个官方 Key 已写入 `Agnes-官方-1..12`，全部 `/v1/models` 200。
- **提交**：`b530272 fix(proxy): fall back to chat completions for image 400`；**未推送**，本机 VPN `127.0.0.1:51081` 未启动，GitHub 直连被 reset。

## 历史进展（2026-11-05）
本轮完成**审计驱动的 8 项修复**（1 🔴 + 6 🟡 + 1 🟢），含独立子代理对抗性复核后追加的 2 项。全部通过编译/vet/测试/前端构建/真机两连包验证。代码已暂存（21 文件），**已发版 v2.2.9**。

## 本轮修复清单（审计报告：.agents/模型映射与限流冷却-审计.md）

| # | 级别 | 问题 | 修复 |
|---|---|---|---|
| 1 | 🔴 | ChatCompletions/Messages 对 Codex 原生账号也替换模型名 → 官方上游 400 | 调度过滤器第二参改 `modelIDInList(mappedModel, SupportedModelIDs(...))`；`replaceModelInJSONBody` 移入 `if account.IsOpenAIResponsesAPI()` 分支 |
| 2 | 🟡 | 模型级冷却写 `effectiveModel`、查 `mappedModel`，永不命中 | 新增 `cooldownModelFor(account, effectiveModel, mappedModel)`；替换全部调用点；`streamResponsesWSUpstream` 增传 `mappedModel` |
| 3 | 🟡 | 白名单前端文案撒谎（"允许调用"/"恢复默认 6 个"均不成立） | Settings.tsx description 改为「仅控制 /v1/models 列表展示；留空 = 不过滤；不影响请求校验」 |
| 4 | 🟡 | `insufficient_quota` 账号级欠费被当模型级冷却 | `modelScopedQuotaFailure` 移除账号级语义，保留 `insufficient user quota` / `insufficient_user_quota` / `not enabled for this group` / `billing+hard limit` |
| 5 | 🟡 | `SupportedModelIDs` 每次请求 2 次 SQL + 全量遍历，无缓存 | `model_registry.go` 加 15s TTL 缓存 + `InvalidateSupportedModelIDsCache()` |
| 6 | 🟡 | `GetSettings` 同步阻塞 `tasklist` 探测 cloudflared | 改后台 goroutine 周期刷新 + `atomic.Bool` 读 |
| 7 | 🟡 | （子代理发现）`supportedModelIDs` 直接 append 缓存切片 → 数据竞争 + 模型串写 | 先 `append([]string(nil), ...)` 拷贝；新增回归测试 |
| 8 | 🟢 | （子代理发现）`recheckAccountsAfterExhaustion` 只清 effectiveModel 冷却 | recheck 内对 responses_api 账号额外清 mappedModel 冷却 |
| 9 | 🟢 | `resolveTunnelDomain` 只剥 `https://` 前缀 | 同时剥 `http://` |

**未改（有意为之）**：`responses_ws.go` 的 Codex 分支仍替换模型名——该分支只能走到 Codex 账号，且调度侧 `modelIDInList` 已保证 mappedModel 属官方支持列表，属「官方别名」机制。已补注释说明与 chat/messages 的口径差异。

## 关键设计说明

- **`cooldownModelFor`**：responses_api 账号返回 `mappedModel`，Codex 账号返回 `effectiveModel`。写入与调度过滤的 key 必须一致。
- **`SupportedModelIDs` 缓存**：15s TTL + 同步后失效。返回切片是缓存内部数组，**调用方禁止原地修改或直接 append**（`enabledModelIDs` 用 `make(0, len)` 有余量）。
- **额度语义边界**：只有分组/用户维度才走模型级 30min 冷却；账号级欠费（`insufficient_quota` 等）回归账号级 6h `payment_required`。

## 验证凭证（全部通过）

- `go build ./...` / `go vet ./...` / `go test ./... -count=1` **16 包全过**
- `npx tsc --noEmit` / `npm run build`
- 新增回归测试 6 个全 PASS（含 `TestSupportedModelIDsCacheNotCorruptedByAppend`）
- `build-and-restart.bat` 热替换成功，`/health` = ok，新 PID 39168
- **两连包**：`/v1/responses` 首包 A `status=completed` → 续问包 B（带 `previous_response_id`）`status=completed`
- **P0 双通道**：映射 `gpt-5.5 → gpt-6-astra`，`/v1/chat/completions` 200、`/v1/messages` 200（测完已恢复原映射）
- `/v1/models` 白名单精确返回 6 个模型
- 日志：409=0，panic=0，continuation_context_unavailable=0

## 待办事项

- [x] 发版 v2.2.9 四位一体（CHANGELOG / package.json / middleware.go / git tag）
- [x] 更新 docs/ARCHITECTURE.md（新增「模型映射与模型级冷却」章节）
- [ ] 用户侧待处理：Agnes 10 个账号 pro/image 组额度为 $0.000000，需去 apihub.agnes-ai.cn 充值

## 关键上下文

- **目录**: `D:\Desktop\Super-File\codex2api`
- **二进制**: `codex2api.exe`（23:52 构建，PID 39168）
- **服务**: `http://127.0.0.1:18080`，CF 公网 `https://codex2api.ai95.indevs.in`
- **当前版本**: v2.2.9
- **Admin 密钥**: `.env` 中 `ADMIN_SECRET=133413`（请求头 `X-Admin-Key`，注意不是 `X-Admin-Secret`）
- **Admin 路由前缀**: `/api/admin/*`（不是 `/admin/api/*`）
- **测试 API Key**: `sk-36970501a8e0faad9c6428ec42e6a486eec32233f3a3af21`
- **模型映射当前值**: `{"gpt-5.5":"cuckoo-default","gpt-cuckoo-default":"cuckoo-default"}`
- **本轮核心文件**:
  - `proxy/handler.go` — `cooldownModelFor`（新增）、`modelScopedQuotaFailure`（改）、ChatCompletions 过滤器（改）、`supportedModelIDs`（改拷贝）
  - `proxy/handler_anthropic.go` — Messages 过滤器（改）
  - `proxy/responses_ws.go` — `streamResponsesWSUpstream` 增 `mappedModel` 参数
  - `proxy/model_registry.go` — `SupportedModelIDs` 缓存
  - `proxy/retry_exclusions.go` — recheck 清 mappedModel 冷却
  - `admin/handler.go` — `checkTunnelRunning` 异步化、`resolveTunnelDomain` 前缀修复
  - `proxy/exposed_models_test.go` — 6 个回归测试
  - `docs/ARCHITECTURE.md` — 新增「模型映射与模型级冷却（v2.2.9）」章节

## 历史接续（归档）

前序工作见 git log 与 `.agents/plan-*.md`。上一轮完成了模型白名单、429 冷却三层、responses_api 全端点打通、Agnes function_call 兼容、模型级额度隔离、CF 隧道修复。

---

**注意**：本文件由 A-handoff 技能覆盖写入，仅保留最新接续状态。历史细节请查阅 git log 与 `.agents/plan-*.md`。
