# 账号池指纹风险治理执行计划

> 本计划只做合规风险识别、行为收敛和可审计治理；不实现绕过服务端风控、不伪造身份、不提供规避检测策略。

## 目标

判断并治理反代账号池在上游服务端视角下可能呈现的“同出口、同传输、同客户端画像、批量切号/探针”风险，最终交付一个本地可解释的风险自检能力和保守调度策略。

## 用户目标复述

用户关心：当前反代项目会不会因为多个账号共用相近指纹，被服务端识别成同一个客户端在批量轮询账号。计划目标不是“怎么躲检测”，而是把系统改成行为更像正常、稳定、低频、可解释的单会话使用方式。

## 架构洞察

[Heuristic] 真正的风险不在单个 `User-Agent` 或 TLS 指纹，而在“行为图谱”：同一出口短时间命中多个账号、同一会话频繁换号、批量探针、首字超时后隐式横跳、多个账号共享同一代理且请求节奏相似。高级治理方向是减少批量特征，而不是继续堆伪装层。

## 当前源码事实

- `proxy/executor.go:194` 的 `getPooledClient()` 按 `account.ID + proxyURL + transportMode` 复用 HTTP client，账号和代理组合会形成稳定连接池。
- `proxy/executor.go:645` 的 `applyCodexRequestHeaders()` 写入上游请求头，包括 `Authorization`、`User-Agent`、`Originator`、`Session_id`、`Chatgpt-Account-Id` 等。
- `proxy/useragent.go:113` 的 `ProfileForAccount()` 按账号 ID 确定性映射客户端画像；同账号稳定，不是每次随机。
- `auth/store.go:3188` 的 `NextForSessionWithFilter()` 先尝试 session affinity，失败或触发 bounded 条件后才重新挑号。
- `auth/store.go:1657` 默认 session affinity TTL 是 `1h`。
- `auth/store.go:1661` bounded affinity 默认最多复用 `50` 次请求。
- `auth/store.go:1662` bounded affinity 默认最长复用 `5min`。
- `proxy/retry_exclusions.go:102` 的 `nextRetryAccountForSession()` 会在失败/超时场景下重新挑账号。
- `proxy/utls_transport.go:40` 提供可选 `utls` Chrome TLS 传输模式；默认 `CODEX_TRANSPORT_MODE` 为空时走标准 Go transport。

## 非目标

- 不做批量账号轮询优化。
- 不做自动规避上游风控。
- 不做随机化指纹池来制造“看起来不一样”的请求。
- 不把风险结论写死为拦截，默认只告警和给出可解释建议。

## 风险模型

风险维度：

- 出口集中度：多个账号是否共用空代理、同一代理、同一出口类型。
- 会话换号率：同一 continuity/session/API key 是否短时间跨多个账号。
- 探针密度：账号测试、订阅刷新、用量探测是否短时间覆盖多个账号。
- 失败横跳：首字超时、401、429、5xx 后是否触发多账号连续尝试。
- 客户端画像集中度：账号画像是否过少、过旧、或所有账号被强制成同一 header。
- 传输模式集中度：同一出口下是否全部使用相同 transport。

风险等级：

- `low`：单账号或少量账号、会话粘性稳定、无批量探针。
- `medium`：多个账号共用出口，但请求稀疏且 session affinity 有效。
- `high`：同出口下短时间多账号切换、批量探针、或失败后横跳明显。
- `critical`：自动测试/刷新/业务请求叠加，造成同出口高频覆盖账号池。

默认处置：

- `low/medium`：只展示建议。
- `high`：管理端告警，建议降低并发、关闭批量探针或补充账号代理分组。
- `critical`：默认仍不拦截业务流量，但暂停非必要后台批量探针，避免自激化。

## 阶段状态

- [x] 阶段 0：初步源码取证，确认账号池、session affinity、请求头、transport、client pool 的关键链路。
- [ ] 阶段 1：补账号池指纹风险纯函数测试。
- [ ] 阶段 2：实现本地风险评估器，不接触上游。
- [ ] 阶段 3：接入账号/代理/session 调度快照。
- [ ] 阶段 4：增加后台探针与失败横跳的保守治理开关。
- [ ] 阶段 5：管理端展示风险解释和验收建议。
- [ ] 阶段 6：全量验证、计划归档、影响范围复核。

## 文件规划

### 新增

- `auth/egress_risk.go`
  - 纯本地风险评估器。
  - 输入账号出口、请求统计、探针统计、会话换号统计。
  - 输出风险等级、命中规则、证据计数、建议动作。

- `auth/egress_risk_test.go`
  - 覆盖出口集中、会话换号、批量探针、失败横跳、低风险正常流量。

- `admin/egress_risk.go`
  - 管理端只读接口，返回当前风险快照。
  - 不触发上游请求。

- `admin/egress_risk_test.go`
  - 验证接口只读、字段完整、风险解释稳定。

### 修改

- `auth/store.go`
  - 增加账号出口快照方法。
  - 增加会话 affinity 命中/逃逸统计的读取入口。

- `proxy/retry_exclusions.go`
  - 将首字超时横跳计数暴露为本地统计，不改变当前业务行为。

- `admin/handler.go`
  - 注册风险快照接口。

- `frontend/src/types.ts`
  - 增加风险快照类型。

- `frontend/src/api.ts`
  - 增加管理端风险快照请求函数。

- `frontend/src/pages/Accounts.tsx`
  - 账号列表增加“出口集中风险”提示。

- `frontend/src/pages/Settings.tsx`
  - 安全/账号调度区域展示风险自检结果和建议。

- `frontend/src/locales/zh.json`
  - 中文文案。

- `frontend/src/locales/en.json`
  - 英文文案。

## 执行步骤

### 阶段 1：测试先行定义风险模型

- [ ] 新增 `auth/egress_risk_test.go`。
- [ ] 写测试：多个账号空代理直连时，出口集中度风险至少为 `medium`。
- [ ] 写测试：同一 session 在短窗口跨账号数超过阈值时，风险为 `high`。
- [ ] 写测试：批量账号测试覆盖账号池比例过高时，风险为 `high`。
- [ ] 写测试：单账号、低频、稳定 affinity 时，风险为 `low`。
- [ ] 运行：`go test ./auth -run EgressRisk`。
- [ ] 预期：测试先失败，失败原因是类型/函数不存在。

### 阶段 2：实现纯本地风险评估器

- [ ] 新增 `auth/egress_risk.go`。
- [ ] 定义 `EgressRiskInput`、`EgressRiskVerdict`、`EgressRiskRuleHit`。
- [ ] 实现 `EvaluateEgressRisk(input EgressRiskInput) EgressRiskVerdict`。
- [ ] 所有规则必须只基于本地统计，不读取 token，不请求上游。
- [ ] 运行：`go test ./auth -run EgressRisk`。
- [ ] 预期：通过。

### 阶段 3：账号出口与调度快照

- [ ] 在 `auth/store.go` 增加只读快照方法。
- [ ] 快照字段包含账号数、代理分组、直连账号数、同代理账号数、当前活跃请求数。
- [ ] 增加测试，构造多个账号共用代理与空代理场景。
- [ ] 运行：`go test ./auth -run "EgressRisk|SessionAffinity|Store"`.
- [ ] 预期：通过，且不改变原有调度选择结果。

### 阶段 4：失败横跳与探针统计

- [ ] 在 `proxy/retry_exclusions.go` 或相关调用处记录本地计数。
- [ ] 区分业务请求失败重试、后台探针、账号批量测试。
- [ ] 默认只统计，不改变请求结果。
- [ ] 对 `critical` 风险仅暂停非必要后台批量探针，不拦截用户业务请求。
- [ ] 写测试覆盖：高风险下后台探针被跳过，业务请求不被拦截。
- [ ] 运行：`go test ./proxy ./auth -run "EgressRisk|FirstToken|Retry|Probe"`。

### 阶段 5：管理端只读接口

- [ ] 新增 `admin/egress_risk.go`。
- [ ] 注册 `GET /api/admin/security/egress-risk`。
- [ ] 返回字段：
  - `risk_level`
  - `score`
  - `rules`
  - `evidence`
  - `recommendations`
  - `updated_at`
- [ ] 写接口测试，确认不会触发上游请求。
- [ ] 运行：`go test ./admin -run EgressRisk`。

### 阶段 6：前端展示

- [ ] 在 `frontend/src/types.ts` 增加类型。
- [ ] 在 `frontend/src/api.ts` 增加请求函数。
- [ ] 在账号页展示出口集中风险。
- [ ] 在设置页展示风险等级、证据、建议。
- [ ] 文案必须说清楚“本地风险自检，不代表服务端已检测或处罚”。
- [ ] 运行：`npm run typecheck`。
- [ ] 运行：`npm run build`。

### 阶段 7：全链路验证与归档

- [ ] 运行：`go test ./auth ./proxy ./admin`。
- [ ] 运行：`npm run typecheck`。
- [ ] 运行：`npm run build`。
- [ ] 若根目录测试需要嵌入前端产物，先确保 `frontend/dist` 已生成，再运行 `go test ./...`。
- [ ] 更新本计划的阶段状态和验证记录。
- [ ] 复核影响范围：账号调度、上游请求、后台探针、管理端接口、前端页面。

## 验收标准

- 风险评估器可在无网络、无上游调用的情况下运行。
- 默认不改变业务请求路径。
- 默认不拦截账号请求，只给出风险解释。
- 高风险时只限制非必要后台批量探针。
- 管理端能看到风险原因，而不是只看到一个模糊等级。
- 测试能证明不会因为单账号正常使用误报为高风险。
- 所有新增规则都有低误报用例。

## 关键验证命令

```powershell
go test ./auth -run EgressRisk
go test ./proxy ./auth -run "EgressRisk|FirstToken|Retry|SessionAffinity"
go test ./admin -run EgressRisk
Set-Location frontend; npm run typecheck
Set-Location frontend; npm run build
go test ./...
```

## 影响范围

- 账号调度：只新增观测与风险评估，默认不改变选号。
- 后台探针：后续实现 `critical` 风险时可能暂停非必要批量探针。
- 管理端：新增风险自检展示，不改变账号增删改。
- 上游请求：计划内不新增请求头、不改变 token、不增加上游调用。

## 回滚策略

- 风险评估器是独立纯函数，可删除 `auth/egress_risk.go` 和测试回滚。
- 管理端接口是只读接口，可单独移除路由。
- 前端展示可单独回滚，不影响后端业务。
- 探针保守治理必须由配置开关控制，关闭后恢复旧行为。

## 本轮计划写入记录

- 2026-06-10：创建计划文件，基于当前源码取证结果确定治理范围。
