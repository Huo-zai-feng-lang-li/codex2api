# Codex2API 项目执行规则

本文件约束本项目后续的开发、验证、部署与服务运维。事实以当前代码、运行进程和接口响应为准，`.agent/handoff.md` 仅作为接续参考。

## 1. 服务热替换

### 1.1 单实例安全替换

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

### 1.2 真正无感升级

严格零中断必须采用稳定入口代理和蓝绿双实例：

1. 入口代理固定监听对外端口。
2. 旧实例和新实例使用不同内部端口。
3. 先启动新实例并完成健康检查、关键接口冒烟和版本哈希核验。
4. 入口代理原子切流到新实例。
5. 旧实例停止接收新请求，等待在途请求排空后再优雅退出。
6. 新实例异常时立即切回旧实例。

未建立稳定入口代理前，不得把单进程停止、替换、重启描述为“无感热替换”。

### 1.3 快速单实例替换

- 必须先完成前端构建、EXE 候选构建、测试和候选哈希记录，运行中的正式 EXE 在此阶段不得停止。
- 停止、等待退出、备份旧 EXE、移动候选、启动新进程、轮询健康检查必须放在同一个本地脚本或同一次工具调用中连续完成，禁止拆成多个会话步骤。
- 用户明确允许客户端自动重链时，可在确认候选完整后直接触发优雅停止；若超时才允许强制结束，并必须保留旧 EXE 以便回滚。
- 新进程健康检查通过后立即核对 PID、监听端口和运行文件 SHA256；浏览器必须刷新页面，旧标签页不会自动加载新嵌入的前端资源。
- 单实例快速替换仍会有短暂断连；需要真正零中断时必须使用 1.2 的蓝绿双实例方案。

### 1.4 标准执行模板

后续 Agent 必须按以下两个阶段执行，不得边构建边停服务：

**阶段 A：在线准备（旧服务持续运行）**

1. 依次执行前端类型检查、前端生产构建、Go 测试和静态检查。
2. 使用 `CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o codex2api.new.exe .` 构建候选程序。
3. 计算并记录 `codex2api.new.exe` 的 SHA256；确认正式程序、候选程序和备份路径都位于项目根目录。
4. 连续读取 `/health`，确认服务正常且 `responses_memory.inflight_requests == 0` 后才进入替换窗口。

**阶段 B：单脚本替换（不可拆分）**

1. 从运行时安全来源读取管理密钥，只在内存中设置 `X-Admin-Key`，不得写入脚本、日志或接力文件。
2. 调用 `POST /api/admin/system/shutdown`，等待旧 PID 退出和程序文件句柄释放；仅在优雅退出明确超时后才允许强制结束旧 PID。
3. 将 `codex2api.exe` 改名为带时间戳的回滚文件，再将 `codex2api.new.exe` 原子移动为 `codex2api.exe`。
4. 使用 WMI/CIM `Win32_Process.Create`，指定项目根目录为 `CurrentDirectory`，脱离当前终端启动正式程序；禁止依赖会随工具会话结束的附着式后台进程。
5. 轮询新服务直到 `/health.status == ok`，随后核对监听地址 `127.0.0.1:18080`、新 PID、运行文件 SHA256、在途请求和续链持久化状态。
6. 若启动或健康检查失败，立即停止新 PID、恢复回滚文件并重新启动旧版本；若全部通过，删除回滚文件和临时部署材料。

替换成功的唯一判据是：候选 SHA256 与运行 EXE SHA256 一致、端口监听正常、`/health.status == ok`。只看到进程存在或页面能打开都不算闭环。

## 2. 服务运维语义

- “停止服务”仅表示优雅关闭应用进程，不等同于关闭 Windows 系统。
- “更新最新服务”必须同时满足：代码已构建、运行文件哈希匹配候选文件、健康检查通过；仅复制文件或仅看到进程启动均不算完成。
- 不得为了部署测试中断真实模型请求；先排空流量，再操作服务。
- 服务由任务临时启动时，交付前必须关闭；生产服务需要保留时，必须在交付结果中明确 PID、端口和健康状态。

## 3. 仪表盘实时刷新

- 仪表盘 `1h` 实时范围在页面可见时每 15 秒刷新一次；其他趋势范围不启用自动轮询。
- 15 秒是数据拉取周期，不是动画时长。仪表盘可见数字仅在新旧值变化时执行 3 秒线性纯数字滚动。
- 自动刷新采用 stale-while-revalidate：请求期间保留旧值，新数据成功返回后原位更新，禁止先清空为 `-`。
- 首次加载且无历史值时允许显示 `-`；用户切换趋势时间范围时必须保留旧值作为过渡态，新范围数据返回后滚动替换，禁止闪 `-`。
- 首次加载和手动切换趋势时间时从 0 滚动到目标值；15 秒自动刷新从旧值滚动到新值，禁止周期性回零。
- 数字动画不得使用上下位移、透明度变化或闪烁；健康延迟无样本显示 `-`，动画起点显示 `0ms`。
- 复合指标必须使用等宽数字，并同时预留旧值和目标值的最大宽度，禁止 Token、计费、RPM/TPM 在动画期间推动相邻文本产生抖动。
- 仪表盘数字动画独立于系统 `prefers-reduced-motion` 设置；只要新旧值发生变化就执行滚动。其他界面动画仍尊重系统动态效果设置。
- 刷新动画不得增加接口请求、改变延迟统计口径或触发整张统计卡的高频重绘。
- 首字延迟和完成延迟必须使用当前趋势时间范围内状态码 200 的有效模型调用样本；非 200、无效延迟和区间外样本必须排除。
- 今日请求、Token、TPM、首字延迟等统计依赖 `usage_log_mode != off`。若运行态 `/api/admin/runtime-status` 返回 `usage_log.enabled=false`，模型请求仍会转发，但仪表盘统计不会增长；应先恢复请求日志写入，再排查前端刷新。

## 4. 验证与交付

代码变更完成后至少执行：

1. `npm --prefix frontend run typecheck`
2. `npm --prefix frontend run build`
3. `go test ./... -count=1`
4. `go vet ./...`
5. `git diff --check`

涉及仪表盘刷新时，还必须使用浏览器跨越至少一个完整轮询周期验证：

- 自动刷新期间健康延迟不出现瞬时 `-`。
- 数值变化时滚动动画结束于接口返回的精确值。
- 页面隐藏后不继续轮询，切换非 `1h` 范围后不自动轮询。

涉及服务替换时，交付结果必须包含：提交号、PID、监听端口、运行 EXE SHA256、健康检查结果和工作区状态。所有结论必须来自本轮实际命令或接口响应，禁止引用过期记录代替验证。

## 5. 安全与仓库边界

- 管理密钥、令牌、账号凭证不得写入 Git、日志、接力文件或最终回复。
- 临时脚本只能放在项目明确的临时位置，使用后立即删除。
- 不覆盖、不回滚用户无关改动；提交前检查暂存区，只提交本任务文件。
- `.agent/handoff.md` 始终覆盖更新，不创建按日期散落的接力文件。

## 6. 双通道对称性（HTTP / WebSocket）

本项目的 `/v1/responses` 端点同时支持 HTTP POST（`handler.go`）和 WebSocket（`responses_ws.go`）两条独立处理路径。两条路径各自拥有完整的账号选择、重试、上游转发和流式读取逻辑。

### 6.1 强制对称检查

任何涉及以下功能的修改，必须同时检查并同步到两条路径，否则视为未完成：

| 功能类别 | HTTP 路径 (`handler.go`) | WebSocket 路径 (`responses_ws.go`) |
|---------|-------------------------|-----------------------------------|
| 续链历史展发 | `buildOpenAIResponsesContinuationFallback` | 同左 |
| 续链账号亲和 | `bindOpenAIResponsesContinuationOwner` | 同左 |
| Pending 注册 | `RegisterPendingOpenAIResponsesContinuation` | 同左 |
| 完成缓存 | `cacheOpenAIResponsesContinuation` | 同左 |
| 上游错误分类与重试 | `shouldRetryHTTPStatus` / `shouldRetryRequestError` | 同左 |
| 加密内容剥离 | `stripInvalidEncryptedContentFromResponsesBody` | 同左 |
| 安全审计 | `upstreamGuardAudit` | 同左 |
| 用量日志记录 | `logUsageForRequest` | 同左 |

### 6.2 Responses 续链系统架构与上下文丢失修复全记录

#### 问题现象

用户在 Codex 桌面端进行多轮对话时，模型从第二轮开始丢失所有历史上下文。例如：第一轮告诉模型"每次给数字加5"，模型正确回答；第二轮发送数字，模型不知道要加5，只是回声。Agent 场景下（如"修复Bug"→"测试一下"），第二轮指令中 Agent 完全不记得之前做了什么。

#### 续链系统工作原理

本项目作为反代，上游账号是无状态的第三方 API（不支持 `previous_response_id`）。续链系统的职责是：

1. **缓存**：每轮 `response.completed` 时，将本轮的 request input + response output 存入内存+SQLite（由 `cacheOpenAIResponsesContinuation` 完成）。
2. **注册**：流式开始 `response.created` 时，立即注册 Pending 锁（由 `RegisterPendingOpenAIResponsesContinuation` 完成），防止并发请求在响应未完成时就查询到不完整的历史。
3. **展发**：下一轮请求到来时，如果携带 `previous_response_id` 或同一 `session_id`，从缓存中按 parent chain 拼接出完整历史，注入到 `input` 字段中发给上游（由 `buildOpenAIResponsesContinuationFallback` 完成）。
4. **亲和**：尽量将续问路由到同一个上游账号（由 `bindOpenAIResponsesContinuationOwner` 完成）。

#### 根因链（共 4 层）

| 层级 | 根因 | 影响 | 修复 |
|------|------|------|------|
| **L1 通道缺失** | 续链系统全部 4 个函数仅注入了 HTTP POST 路径（`handler.go`），WebSocket 路径（`responses_ws.go`）中完全没有调用 | Codex 桌面端走 WebSocket（`GET /responses`），历史从未缓存、从未展发，模型每轮只看到当前一句话 | 在 `responses_ws.go` 的 `forwardResponsesWebSocketTurn` 和 `streamResponsesWSUpstream` 中注入完整续链生命周期 |
| **L2 Header 大小写** | `ResolveSessionID` 只匹配精确的 `session_id` Header key，不兼容 `Session_id`、`session-id`、`x-session-id` 等变体 | 不同客户端实现使用不同 Header 拼写，导致每轮请求生成随机 UUID 作为 sessionID，会话被拆散 | 在 `executor.go` 中实现大小写不敏感、分隔符不敏感的 Header 匹配 |
| **L3 function_call 参数缺失** | 长对话历史中 `function_call` 类型 item 缺少必填的 `arguments` 字段 | 上游返回 HTTP 400：`Missing required parameter: 'input[141].arguments'`，整个续链回放失败 | 在 `replayableOpenAIResponsesItem` 中检测并自动补齐 `"arguments": "{}"` |
| **L4 SQLite 迁移顺序** | `session_id` 列通过 `ensureSQLiteColumn` 动态添加，但对应 index 的 CREATE 语句放在了列添加之前 | 已有数据库启动时报 `no such column: session_id` 崩溃 | 将 index 创建移至 `indexStatements` 阶段（在列补齐之后执行） |

#### 排查过程教训

1. **先看日志，再看代码**：日志显示 `GET /responses`（WebSocket）而非 `POST /v1/responses`（HTTP），但排查时只盯着 HTTP handler 改了 4 轮。如果第一时间检查运行日志中请求走的实际路径，一轮就能定位。
2. **单元测试通过 ≠ 线上正确**：HTTP 路径的续链单元测试 100% 通过，但 WebSocket 路径没有对应测试，且线上流量走的恰恰是 WebSocket。
3. **不要只修表象**：前几轮修了 Header 大小写、function_call 补齐等细节，这些确实是真实 Bug，但不是"模型不记得上下文"的主因。主因是 WebSocket 通道从未接入续链系统。

#### 涉及文件

| 文件 | 修改内容 |
|------|---------|
| `proxy/responses_ws.go` | 注入续链 4 个生命周期函数，扩展 `streamResponsesWSUpstream` 签名 |
| `proxy/handler.go` | 续链 fallback 触发、`continuationOwnerBound` 逻辑 |
| `proxy/responses_continuity.go` | session_id fallback 查找、function_call arguments 补齐、非破坏性历史提取 |
| `proxy/executor.go` | Header 大小写不敏感匹配 |
| `database/sqlite.go` | 迁移顺序修正 |

### 6.3 验证清单

修改涉及 Responses API 处理逻辑时，交付前必须逐项确认：

- [ ] `handler.go` 中的改动是否在 `responses_ws.go` 中有对应实现？
- [ ] `responses_ws.go` 中的改动是否在 `handler.go` 中有对应实现？
- [ ] `streamResponsesWSUpstream` 的参数签名是否需要扩展？
- [ ] 运行日志中同时覆盖了 `POST /v1/responses` 和 `GET /responses` 两种请求？
- [ ] 修改续链相关逻辑后，是否在真实客户端（而非仅单元测试）验证了多轮对话上下文保持？
