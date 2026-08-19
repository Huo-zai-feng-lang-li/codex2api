# Responses 线程上下文续链修复说明

## 1. 结论

原始问题是 OpenAI Responses 多轮线程续问时，上游或中转服务丢失 `previous_response_id` 对应的历史，导致新请求虽然可能返回 HTTP 200，但实际已经变成没有前文的新会话。

该问题的代码修复已经完成，并且相关 commit 已进入本地 `main` 和 `origin/main`：

| Commit | 内容 | 状态 |
|---|---|---|
| `14864a0` | 将 Responses 续链上下文持久化到 SQLite/PostgreSQL | 已提交并已推送 |
| `4d24310` | 增加 `session_id` 级回退、历史展开和 `previous_response_id` 丢失修复 | 已提交并已推送 |
| `bd7f4d2` | 修复续链 409 冲突和切号接管链路 | 已提交并已推送 |
| `b40d16d` | 发布 v2.2.7 | 已提交并已推送 |

但当前不能把运行态称为完全闭环：最近一次运行记录显示续链持久化出现 `context deadline exceeded`，`continuity_persistence_failures=23`。因此，原始“线程丢上下文”代码缺陷已提交，SQLite 持久化超时仍是后续运行态问题。

## 2. 原始根因

### 2.1 只相信上游 `previous_response_id`

部分第三方中转会接受 `previous_response_id`，但服务端没有真正保存对应历史。它可能返回 200，却把请求当成新会话处理，形成静默上下文丢失。

### 2.2 本地只保存单个 response，不能按线程回溯

早期逻辑主要围绕单个 `response_id` 保存历史，无法在以下场景稳定恢复：

- 请求缺少 `previous_response_id`，但仍属于同一个线程；
- 上游拒绝 HTTP 续链；
- 上游连接中断，原账号没有及时返回完整结果；
- 服务重启导致进程内缓存清空；
- `session_id` 的大小写、下划线和破折号差异导致同一线程被拆成多个随机会话。

### 2.3 失败重试可能重复触发 409

续链已经完成本地历史展开后，旧逻辑仍可能再次进入“上下文不完整”分支，造成二次 409，而不是继续发送已经修复过的请求。

## 3. 修复后的数据模型

每个 Responses 响应保存为一条父子链节点：

```text
session_id
  └── response_id A
        └── response_id B
              └── response_id C
```

每个节点包含：

- `response_id`：客户端后续续问使用的响应 ID；
- `parent_id`：上一轮响应节点；
- `session_id`：线程级会话标识；
- `input/output`：可回放的当前轮消息增量；
- `account_id/base_url`：账号和上游归属，防止跨账号串链；
- `replayable/state`：是否允许历史回放以及当前操作状态；
- `created_at/accessed_at`：创建时间和滑动访问时间。

保存增量而不是每轮复制完整历史，避免线程越长内存和数据库体积线性膨胀过快。需要回放时，再沿 `parent_id` 向上物化完整历史。

## 4. 请求处理链路

### 4.1 首包

1. 解析客户端请求并识别 `session_id`。
2. 上游返回 `response_id` 后，注册当前节点及其父节点。
3. 将可回放的 `input/output` 写入内存注册表。
4. 异步或短超时写入 `responses_continuity` 数据表。
5. 更新该线程的最新节点和最新可回放节点。

### 4.2 续问

1. 优先使用请求中的 `previous_response_id`。
2. 如果缺失，则按 `session_id` 找到最新可回放节点。
3. 绑定创建该响应的账号，优先等待原账号，避免无条件切号造成上下文断裂。
4. 官方 OpenAI 上游且支持可靠续链时，保留 `previous_response_id`。
5. 第三方无状态中转，或上游拒绝/丢失续链时，沿父链展开本地完整历史。
6. 删除上游不认识的 `previous_response_id`，将展开后的历史放入当前请求并重试一次。
7. 历史无法完整恢复时，显式返回 `continuation_context_unavailable`，禁止静默当作新会话。

### 4.3 服务重启

1. 进程内缓存未命中时，根据线程头或 `response_id` 查询数据库。
2. 只在需要时懒加载父链节点到内存。
3. 恢复成功后继续沿同一 `session_id` 续问。
4. 数据库繁忙时使用短超时并降级为内存路径，避免数据库阻塞正常请求；但会记录持久化失败计数。

## 5. TTL 与容量治理

当前实现采用最后访问时间驱动的滑动 TTL：

- 当前默认值：`24` 小时；
- 可配置项：`CODEX_RESPONSES_CONTINUITY_TTL_HOURS`；
- 如需使用之前记忆的 12 小时：

```env
CODEX_RESPONSES_CONTINUITY_TTL_HOURS=12
```

同时限制：

- 最大续链节点数；
- 单条历史链最大体积；
- 所有续链上下文最大总字节数；
- 单节点可保存的消息数量和大小。

过期节点按 `accessed_at` 清理，磁盘清理与内存限制保持一致。不可回放的 failed、cancelled 节点只保留必要状态元数据，不能占满历史配额。

## 6. HTTP 与 WebSocket 范围

同一套续链注册表和历史物化逻辑同时服务：

- HTTP：`POST /v1/responses`；
- WebSocket：`GET /v1/responses`。

WebSocket 转 HTTP fallback 前必须先物化完整本地历史；不能因为切换传输方式就丢失线程上下文。

## 7. 已提交代码与当前仓库状态

### 已提交内容

- 续链上下文数据库表和索引；
- `session_id` 线程级头节点；
- 父链增量保存和完整历史物化；
- 上游无状态时的本地回放；
- 上游拒绝 `previous_response_id` 时的单次恢复重试；
- 账号归属绑定和切号接管；
- 409 二次触发保护；
- 滑动 TTL、容量上限、过期清理和 `/health` 运行指标。

### 当前工作区

当前 `HEAD` 为 `dd65430`，`origin/main` 为 `9bc4963`。续链修复 commit 均在两者共同历史中。工作区仍有其他未提交的账号、前端、诊断文件和数据库备份改动；这些不是本说明文档或原始续链修复产生的内容，禁止一并回滚。

本说明文档是新建文件，当前只写入工作区，未单独创建新的 git commit。

## 8. 已有验证证据

历史验证记录包含：

- `go test ./... -count=1` 通过；
- `go vet ./...` 无输出；
- `git diff --check` 无空白错误；
- 真实 HTTP 两连包：首包获取 `response_id`，续问包携带 `previous_response_id`，均返回 HTTP 200 和 `completed`；
- 服务重启后的数据库懒恢复路径已覆盖持久化测试；
- `/health` 已暴露续链内存、持久化开关和持久化失败计数。

这些证据证明原始代码路径已经覆盖，但最近运行态仍出现数据库持久化超时，不能据此宣称当前部署的持久化健康状态已经完全达标。

## 9. 后续闭环标准

续链持久化问题关闭前必须同时满足：

- 真实机器人完成首包 A -> 续问包 B，两包均 HTTP 200；
- 服务控制台无 409 死锁和上下文丢失错误；
- 重启服务后同一线程仍能继续对话；
- `/health` 的 `continuity_persistent=true`；
- `continuity_persistence_failures` 连续验证保持为 0；
- SQLite/PostgreSQL 中存在对应的 `responses_continuity` 节点和线程头；
- HTTP 与 WebSocket 两条链路都通过同样的回放验证。

## 10. 相关文件

- `proxy/responses_continuity.go`：续链注册表、父链物化、TTL、持久化和恢复；
- `proxy/responses_ws.go`：WebSocket 续链和 HTTP fallback；
- `proxy/handler.go`：HTTP Responses 请求入口和续链调度；
- `database/responses_continuity.go`：续链数据表读写、线程头和清理；
- `database/sqlite.go`：SQLite 表结构和索引迁移；
- `database/postgres.go`：PostgreSQL 表结构和索引迁移；
- `proxy/responses_continuity_test.go`：续链回放和失败恢复测试；
- `proxy/responses_continuity_persistence_test.go`：数据库持久化和重启恢复测试；
- `proxy/responses_memory_governor_test.go`：TTL、容量和内存治理测试。
