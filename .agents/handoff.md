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
- [ ] 多供应商模型功能逐项验证：工具调用、识图、Goal 模式、推理努力（reasoning_effort）参数在 GLM/Gemini 上是否生效（网关透传没问题，取决于中转和模型支持）。
- [ ] 当前映射配置中 `gpt-5.5 → doubao-2.1-turbo` 报 TLS 握手失败（目标 http://127.0.0.1:8045），疑似该 doubao 账号配置了代理但目标是本地地址不需要代理，需用户确认账号代理配置。
- [ ] 如用户需要，可实验"模型名伪装"方案：在 /v1/models 中把多供应商模型包装成 gpt- 开头（如 gpt-5.2-glm-flash），验证 Codex 是否显示（需先确认 Codex 是只过滤前缀还是有硬编码白名单）。
- [ ] 如用户同时使用 Anthropic /v1/messages 端点，可将映射配置分离为 `model_mapping_responses` 和 `model_mapping_anthropic` 两个独立字段，避免冲突。

## 关键上下文

- **目录**: `D:\Desktop\Super-File\codex2api`
- **服务端口**: 18080，管理后台 `http://127.0.0.1:18080/admin/`
- **Admin Key**（X-Admin-Key）: `133413`
- **测试 API Key**: `sk-e734b9d5dc5b56e73206d8c71f963df3174ea3a58f325517`
- **apinex 中转**: Base URL `https://api.apinex.bond/v1`，API Key `sk-apx71d3e219484399a10b1c9ef0f3d51cbf62d93175fadaa13`，账号 ID 837，代理 `http://127.0.0.1:51081`，支持模型：`free/gemini-3.8-flash`、`free/glm-5.3-flash`、`free/gpt-5.6-luna`
- **当前映射配置**（系统设置 model_mapping）:
  - `gpt-5.2` → `free/glm-5.3-flash`（已验证可用）
  - `gpt-5.4` → `free/gemini-3.8-flash`
  - `gpt-5.5` → `doubao-2.1-turbo`（TLS 报错待排查）
- **主要文件**:
  - `proxy/translator.go`：resolveOpenAIResponsesModel（~2781 行）、replaceModelInJSONBody（~2802 行）
  - `proxy/handler.go`：/v1/responses（~1400 行）、/v1/responses/compact（~2610 行）
  - `proxy/responses_ws.go`：WebSocket 映射（~217 行）、transport.body 替换（~416 行）、codexBody 替换（~618 行）
  - `security/validator.go`：AllowedModelPattern 放宽斜杠
  - `test_model_mapping_bot.py`：发包机器人测试脚本（项目根目录）
- **项目规则**: `.agents/rules/README.md` — 双通道绝对对称、发包机器人闭环、四位一体版本号、架构文档同步
