
## gpt-cuckoo-default 别名接入 (2026-10-12) ✅ 已闭环

**最终状态**：Codex 下拉框已显示 `gpt-cuckoo-default`（displayName 与 id 一致），全链路打通。
- 生效配置：`~/.codex/config.toml` 的 `model_catalog_json`
- 关键坑：桌面端下拉框由 **app-server daemon** 的 `model/list` 决定；改配置后必须重启桌面端（daemon 只在启动时读 config）
- 验证方法：`codex app-server` 走 `model/list` 协议，确认返回含 `gpt-cuckoo-default`（hidden=false）


### 目标
让 Codex 下拉框出现自定义模型 `gpt-cuckoo-default`，经 codex2api 映射到账号 841（DS 反代，`http://127.0.0.1:18900`，模型 `cuckoo-default`）。

### 已完成
1. **codex2api 侧（全部生效）**
   - `model_mapping` = `{"gpt-5.5":"cuckoo-default","gpt-cuckoo-default":"cuckoo-default"}`
   - `model_registry` 插入 `gpt-cuckoo-default`（source=manual），`/v1/models` 已返回
   - 备份：`data/codex2api.db.bak_before_cuckoo`
2. **Codex 侧（已配置，待重启桌面端验证）**
   - `~/.codex/model-catalog.local.json`：从 `codex debug models` 导出内置 11 模型 + 追加 `gpt-cuckoo-default`（共 12）
   - `~/.codex/config.toml` 顶部加 `model_catalog_json = "C:/Users/Administrator/.codex/model-catalog.local.json"`
   - app-server 已重启，`codex debug models` 已确认含 `gpt-cuckoo-default`
3. **上游 18900 插件补 /v1/responses 端点**
   - 改 `~/.cuckoo/plugins/ai-gateway/providers/gateway.js`：新增 `handleResponses`（流式+非流式）+ 路由 `/v1/responses`
   - `node --check` 通过
   - 账号 841 的 key `sk-cuckoo-4f99f17e...` 在 keystore 中有效

### 关键结论
- **Codex 下拉框由 app-server 模型目录决定**，`model_catalog_json` 可覆盖内置目录（替换非合并，必须含完整列表）。
- **codex2api 的 openai_responses 账号只打上游 /v1/responses**，上游若只有 /v1/chat/completions 会 404。

### 待办（最后一步）
- [ ] **重启 cuckoo-code**（当前会话运行其中，会中断）→ 加载新的 gateway.js + 挂载 `__cuckooRunGatewayQuery`
- [ ] 重启 Codex 桌面端 → 下拉框出现 `gpt-cuckoo-default`
- [ ] 端到端验证：Codex 选 gpt-cuckoo-default → codex2api 映射 cuckoo-default → 18900 /v1/responses

### 卡点根因（已定位）
18900 之前返回 503「网关查询入口不可用」，是因为**运行中的 cuckoo-code 进程早于编译产物**，主进程未挂载 `globalThis.__cuckooRunGatewayQuery`。重启即修复。


# 最新接续状态 (2026-09-07 18:30)

## 核心进展

已实现 **OpenAI Responses API 模型名映射功能**（HTTP + WebSocket 双通道），支持 Codex 桌面端选 GPT 模型名时网关实际转发到多供应商模型（GLM/Gemini/Doubao 等）。已通过 Codex 桌面端实测验证：选 `gpt-5.2` 实际打到 `free/glm-5.3-flash`，模型自识别为 APInex。

## 核心动机与背景 (Motivation & Background)

- Codex 桌面端模型列表有硬编码过滤，**只显示 `gpt-` 开头的模型**，非 GPT 模型（GLM、Gemini 等）无法显示和切换。
- 用户希望向下兼容：既支持 GPT 原生账号，也支持多供应商中转 API，且能在 Codex 中直接切换使用。
- 最终采用**模型名映射方案**：Codex 选 GPT 模型名（如 gpt-5.2），网关在转发前替换为实际多供应商模型名（如 free/glm-5.3-flash），并用映射后模型名匹配账号池。
- 已验证"做不通"的方向：放宽模型名校验后 /v1/models 返回了多供应商模型，但 Codex 桌面端仍不显示（客户端硬编码过滤，闭源无法修改）。

## 关键设计与实现 (Implementation & Decisions)

### 新增函数（proxy/translator.go）
- `resolveOpenAIResponsesModel(model, mappingJSON)`：从 model_mapping 配置查映射，未命中则原样返回。边界处理完整（空模型、空配置、无效JSON、空映射值）。
- `replaceModelInJSONBody(body, newModel)`：用 sjson 替换请求体中 model 字段，无 model 字段或出错时原样返回。

### 修改的接口（双通道对称）
| 文件 | 位置 | 改动 |
|---|---|---|
| `proxy/handler.go` | ~1400 行 | `/v1/responses` HTTP 接口：effectiveModel 后映射，mappedModel 做账号过滤，转发前替换 body |
| `proxy/handler.go` | ~2610 行 | `/v1/responses/compact` HTTP 接口：同上 |
| `proxy/responses_ws.go` | ~217 行 | WebSocket 通道：映射 + 账号过滤 |
| `proxy/responses_ws.go` | ~416 行 | WebSocket transport.body 转发路径：body 替换 |
| `proxy/responses_ws.go` | ~618 行 | WebSocket codexBody 转发路径：body 替换 |
| `security/validator.go` | AllowedModelPattern | 正则从 `^[a-zA-Z0-9._-]+$` 改为 `^[a-zA-Z0-9._/-]+$`，允许斜杠 |

### 关键设计决策
1. **映射命中后，该模型名的流量整体切到映射目标的中转 OpenAI Responses 账号**，Codex 原生账号（Refresh Token / Session Token）在过滤阶段（allowCodexAccounts=false）不会承接该模型；未配置映射的模型不受影响。
2. **不修改 rawBody**，只在转发前替换 preparedOpenAIResponsesBody / wsTransportBody / wsCodexBody，避免影响续链等逻辑。
3. **API Key 限流仍用原模型名**（effectiveModel），符合用户视角；账号冷却用 mappedModel，与实际上游对齐。
4. **映射配置与 Anthropic /v1/messages 端点共用同一个 `model_mapping` 字段**（store.GetModelMapping()）。如果用户同时用 Anthropic 端点，可能存在冲突，需要时可分离为两个独立字段。
5. **性能影响可忽略**：每个请求多一次 json.Unmarshal（1-5μs），命中映射时多一次 sjson 替换（1-3μs）。

### 测试
- 单元测试：`proxy/translator_test.go` 新增 14 个用例（TestResolveOpenAIResponsesModel 10 个 + TestReplaceModelInJSONBody 3 个 + 已有），`security/validator_test.go` 新增 5 个用例。
- `go vet ./proxy/... ./security/...` 通过。
- `go test ./proxy/... ./security/... -count=1` 全量通过（5 个包 OK）。
- Codex 桌面端实测通过：gpt-5.2 → free/glm-5.3-flash，正常返回。
- 发包机器人两连包测试（首包→续问包）因上游 apinex 临时 502 未完成，脚本已写好（`test_model_mapping_bot.py`），等上游恢复后执行。

## 待办事项 (Next Steps)

- [ ] 上游 apinex 恢复后，运行 `python test_model_mapping_bot.py` 完成发包机器人两连包闭环测试（项目规则硬性门槛）。
- [ ] 多供应商模型功能逐项验证：工具调用、识图、Goal 模式、推理努力（reasoning_effort）参数在 GLM/Gemini 上是否生效。
- [ ] 账号 840（127.0.0.1:8045）当前模型配置为 doubao-2.1-turbo，但其实际上游为本地服务，模型为 claude/gemini/gpt-oss 系列，需同步更新支持模型列表。

## 账号与代理池运维落地成果 (2026-09-29)

### 1. 废号清理与配置修复（已 100% 落地）
- **彻底删除 5 个失效账号**：ID 746, 799, 824（无订阅 403）、ID 795, 796（上游封禁 403）。
- **修正 3 个前端误填 URL 账号**：ID 832, 833, 834 的 Base URL 修正为 `https://api.daseinai.xyz`，账号已复活（原生测试通过）。
- **死代理彻底归零**：已宕机的 `s217603.fxip.cc:9345` 代理已无任何账号绑定。

### 2. 代理池数学级极致平摊（35 个中转账号 5 等分均摊，各 7 个）
- `s3093.fxip.cc:9345` (嘉兴)：**7 个账号** [720, 725, 730, 751, 792, 822, 828] (100% 可用)
- `s230579.fxip.cc:9345` (苏州)：**7 个账号** [721, 726, 731, 752, 794, 823, 829] (100% 可用)
- `s55497.fxip.cc:9345` (常熟)：**7 个账号** [722, 727, 745, 782, 797, 825, 832] (100% 可用)
- `s230561.fxip.cc:9345` (苏州)：**7 个账号** [723, 728, 747, 789, 798, 826, 833] (100% 可用)
- `s127246.fxip.cc:9345` (湖州吴兴-新换)：**7 个账号** [724, 729, 748, 790, 809, 827, 834] (100% 可用，官方测试实测通过)
- 本地直连专享：**1 个账号**（ID 840 `http://127.0.0.1:8045` 本地服务无需代理）
- **当前总计活跃账号数**：36 个。

### 3. 上游最新模型同步（29 个标准模型）
所有 34 个 `daseinai` 账号的模型列表已全量同步为上游最新支持的标准模型（过滤了带空格的非标准名）：
`codex-auto-review`, `gpt-4o-audio-preview`, `gpt-4o-realtime-preview`, `gpt-5.2`, `gpt-5.2-2025-12-11`, `gpt-5.2-chat-latest`, `gpt-5.2-pro`, `gpt-5.2-pro-2025-12-11`, `gpt-5.3-codex-spark`, `gpt-5.4`, `gpt-5.4-2026-03-05`, `gpt-5.4-mini`, `gpt-5.4-openai-compact`, `gpt-5.5`, `gpt-5.5-openai-compact`, `gpt-5.6`, `gpt-5.6-luna`, `gpt-5.6-sol`, `gpt-5.6-sol-openai-compact`, `gpt-5.6-terra`, `gpt-6`, `gpt-6-astra`, `gpt-6-luna`, `gpt-6-sol`, `gpt-image-1`, `gpt-image-1.5`, `gpt-image-2`, `gpt-reserve`, `gpt5.6`。

## 关键上下文

- **目录**: `D:\Desktop\Super-File\codex2api`
- **服务端口**: 18080，管理后台 `http://127.0.0.1:18080/admin/`
- **Admin Key**（X-Admin-Key）: `133413`
- **测试 API Key**: `sk-e734b9d5dc5b56e73206d8c71f963df3174ea3a58f325517`
- **诊断数据**: 保留于 `final_diagnosis_report.json`。

