# 原文取证与全量审计执行计划

## 目标
- 当前默认关闭上游安全审计；开启后默认仅命中规则时取证。
- 未配置合法非 C 盘原文存储路径时，回退到当前本机数据库内联保存请求/响应原文，并受单条字节上限与 1 天保留策略约束。
- 原文默认保留 1 天，过期后由后台清理任务自动删除。
- 安全事件详情页要能顺滑查看、搜索、复制、下载和格式化大文本。

## 阶段状态
- [x] 阶段 1：读取 `.agent/handoff.md`，确认当前安全事件只保存脱敏 preview 与 hash。
- [x] 阶段 2：先补数据库、设置、代理抓取测试。
- [x] 阶段 3：实现原文审计表、设置项、抓取链路与后台接口。
- [x] 阶段 4：优化前端设置与安全事件详情交互。
- [x] 阶段 5：运行后端、前端验证并记录结果。
- [x] 阶段 6：修复命中 SSE 片段被当作“原文详情”的问题，改为请求级聚合 request/response 后落库。
- [x] 阶段 7：优化安全事件审阅体验，详情页首屏展示命中规则、字段、命中值，原文支持 JSON/SSE/JSONL/嵌套 JSON 审阅格式。
- [x] 阶段 8：默认改为全量原文取证，保留 1 天并在服务启动后按日自动清理过期原文。
- [x] 阶段 9：高流量收口，默认关闭安全审计，默认命中后取证；无非 C 盘路径时回退本机数据库内联保存。

## 关键决策
- 运行时默认 `upstream_guard_mode = off`，作为当前安全审计总开关，避免高流量默认扫描与写审计记录。
- 默认取证模式为 `hit_raw`：只有规则命中后才尝试取证；全量 `full_raw` 仅适合短期排查。
- 默认原文保留时间为 1 天；单条最大字节为 1MB。
- 启动时如发现旧默认组合 `hit_raw + 7 天 + 1MB`，自动升级为新默认组合，避免老库继续停留在旧保留策略。
- 清理不进入请求热路径：服务启动时补清一次，之后每小时删除 `expires_at < now` 的原文记录，并按 2GB 默认总量上限删除最旧原文。
- 普通 `tool_call` / `function_call` 只作为原文审计标记，不单独生成安全事件；若同时命中注入、泄露、敏感路径、外传等风险规则，仍生成安全事件。
- `capture_error` 仅用于真实写入失败或路径不可用等异常；未配置非 C 盘路径的正常场景不再写错误。
- 历史 inline 原文仍可读取；新请求在未配置非 C 盘路径时继续 inline 保存，后续磁盘抽象完成后再优先写入非 C 盘。
- 全量模式不改变阻断策略，只改变留证范围；主要拦截目标仍是第三方上游恶意响应。
- 旧记录如果只保存了单个 SSE 命中片段，数据库里没有更多原文，不能事后补全；本轮修复仅对新请求生效。
- 流式响应保存完整 SSE 流；非流式聚合响应只保存最终返回 JSON，中间 SSE 仅用于扫描和拦截判断。
- 新命中的安全事件规则证据会额外保存 `field` 和 `match`；旧记录无字段时，前端基于原文和规则类型做即时反查。
- 原文详情默认展示“审阅格式”，会展开嵌套 JSON 字符串并折叠超长 base64/二进制正文；“原文/复制/下载”仍保留完整未处理正文。

## 待验证
- [x] 命中风险规则后能保存完整原文。
- [x] 全量模式下正常流量也能保存原文。
- [x] 关闭原文取证后不保存原文。
- [x] 流式响应按完整 SSE 流保存，不再只保存命中事件片段。
- [x] 非流式聚合响应保存最终 JSON，不混入中间 SSE 片段。
- [x] 前端构建与服务热替换。
- [x] 详情页能显示规则、字段、命中值，旧记录可从原文反查字段。
- [x] 原文审阅格式能格式化 JSON/SSE/JSONL/嵌套 JSON，且不会把普通长文本误折叠。
- [x] 默认配置为安全审计关闭、命中后取证、保留 1 天、单条 1MB。
- [x] 旧默认组合会在启动时升级到新保留策略。
- [x] 配置非法值与未显式传 `expires_at` 的 capture 写入也回落到新默认：`hit_raw + 1 天 + 1MB`。
- [x] 普通工具调用不再单独生成安全事件，但原文 capture 保留 `tool_call=true`。
- [x] 过期原文 capture 可按 `expires_at` 清理，超出 2GB 默认总量上限时按最旧记录滚动删除。
- [x] 无非 C 盘存储路径时，capture 回退本机数据库内联保存；原始大小和 hash 保留完整正文元数据，实际正文按单条上限截断。

## 本轮验证记录
- `go test ./proxy -run "UpstreamGuard|ResponsesCompact|ResponsesWebSocket"`：通过。
- `go test ./...`：通过。
- `node frontend/src/utils/securityRawBody.test.mjs`：通过。
- `npm run typecheck`：通过。
- `npm run build`：通过。
- `go build -o codex2api.new.exe .`：通过。
- 热替换后健康检查：`http://127.0.0.1:18080/health` 返回 200。
- 当前运行 `codex2api.exe` SHA256：`1ABD089EB9C0A38483D4156381601EE85EA46A15BE46FF288AEF5B7C506169DA`。
- 2026-06-09 本轮定向验证：`go test ./database ./auth ./proxy -run 'TestDefaultSystemSettingsUseFullRawOneDayRetention|TestPruneSecurityCapturesBeforeRemovesExpiredRows|TestStoreDefaultsUpstreamGuardConfigToWarnMode|TestUpstreamGuardToolCallOnlyDoesNotRecordSecurityEvent'`：通过。
- 2026-06-09 设置接口回归：`go test ./admin ./database ./auth -run 'TestSettingsRoutesExposeAndUpdateSecurityCaptureConfig|TestSystemSettingsPersistUpstreamGuardRetentionAndSuppressions|TestStoreLoadsUpstreamGuardConfigFromSystemSettings|TestStoreDefaultsUpstreamGuardConfigToWarnMode'`：通过。
- 2026-06-09 收口验证：加入旧默认升级逻辑后，`go test ./...`、`npm run typecheck`、`npm run build`：通过。
- 2026-06-09 Review 修复：加入 2GB 总量上限和每小时清理，定向验证 `go test ./database ./proxy -run 'TestPruneSecurityCapturesToMaxBytesRemovesOldestRows|TestSecurityCaptureCleanupIntervalIsHourly|TestRunSecurityCaptureCleanupPrunesExpiredAndOverLimit'`：通过。
- 2026-06-09 旧默认漏网修复：`NormalizeConfig` 非法 capture mode 不再回退 `hit_raw`，`InsertSecurityCapture` 未传过期时间不再保留 7 天；验证 `go test ./database ./security/upstreamguard ./proxy -run 'TestInsertSecurityCaptureDefaultsToOneDayExpiry|TestNormalizeConfigInvalidCaptureModeUsesFullRawDefault|TestPruneSecurityCapturesToMaxBytesRemovesOldestRows|TestRunSecurityCaptureCleanupPrunesExpiredAndOverLimit|TestUpstreamGuardToolCallOnlyDoesNotRecordSecurityEvent'`、`go test ./...`、`npm run typecheck`、`npm run build`：通过。
- 2026-06-09 高流量收口：默认 `upstream_guard_mode=off`、默认 `security_capture_mode=hit_raw`、新增 `capture_error`；后续按用户纠偏，未配置非 C 盘路径时回退本机数据库 inline 保存原文。
- 2026-06-09 回退重构与热替换：无非 C 盘路径时原文 inline 保存，完整 hash/大小保留，正文按 `security_capture_max_body_bytes` 截断；验证 `go test ./proxy ./database ./auth ./admin ./security/upstreamguard -run "UpstreamGuard|SecurityCapture|SettingsRoutesExposeAndUpdateSecurityCaptureConfig|StoreDefaults|NormalizeConfigInvalidCaptureMode|DefaultSystemSettings|UpgradeLegacy|SystemSettingsPersistUpstreamGuardMode"`、`go test ./...`、`npm run typecheck`、`npm run build` 均通过；当前运行 PID=1348，SHA256=`32061C7B0FE66AC3BD710836CD784D8E75254300A5FBCC41547FFC07352E0735`，`/health` 返回 200。
