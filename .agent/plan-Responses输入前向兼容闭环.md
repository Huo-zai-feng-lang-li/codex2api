# Responses 输入前向兼容闭环实施计划

> 新窗口执行入口：先阅读本文件，再检查 `.agent/handoff.md` 和当前 `git status`。
> 实施方式：严格采用 TDD，先写失败测试并确认失败，再修改生产代码。

## 一、目标

解决 GPT-5.6 请求携带以下输入项时，被本地 `codex2api` 入口错误拒绝的问题：

```json
{
  "input": [
    {
      "type": "additional_tools"
    }
  ]
}
```

目标不是单独把 `additional_tools` 加入白名单，而是建立可持续升级的 Responses 输入兼容机制：

1. 当前 `additional_tools`、`compaction_trigger` 等新类型可正常进入转发链路。
2. 未来出现未知但结构合法的输入类型时，本地代理不再因静态枚举过期而误杀。
3. 已知需要转换的类型继续执行本地兼容转换。
4. 未知类型及其字段无损透传，由真实上游判断是否支持。
5. HTTP、WebSocket、Compact、OpenAI Responses 直连与 Codex 上游路径均有回归证据。
6. 上游不支持时返回真实上游错误，不再伪装成本地 `invalid_input_type`。

## 二、已完成取证

### 2.1 错误由本地代码生成

文件：`api/validation.go`

- `ValidateInput()` 使用封闭的 `validTypes` 静态白名单。
- `additional_tools` 不在白名单内。
- 未命中白名单时生成：

```text
Invalid input type 'additional_tools' at index 0
code: invalid_input_type
field: input.0.type
```

- `Validator.ToAPIError()` 将其包装为：

```text
code: invalid_parameter
type: invalid_request_error
```

该结构与用户收到的错误逐字段一致。

### 2.2 请求尚未到达上游

文件：`proxy/handler.go`

`Handler.Responses()` 的执行顺序是：

1. 读取请求体。
2. 执行 `ResponsesAPIValidationRulesForModel()`。
3. 校验失败立即 `SendError()` 并返回。
4. 校验通过后才执行请求体转换、账号选择和上游请求。

因此本次错误是本地入口校验错误；当前证据不能证明上游是否支持该类型，因为请求从未到达上游。

### 2.3 当前测试固化了错误策略

文件：`api/validation_test.go`

现有测试：

```go
func TestValidateResponsesAPIRequestRejectsUnknownInputType(t *testing.T)
```

该测试要求未知类型必须返回 `invalid_input_type`。完整兼容需要先将这一旧契约改为前向兼容契约。

### 2.4 官方协议已经继续扩展

2026-07-15 检索 OpenAI Responses API 官方参考，输入项已经出现：

- `additional_tools`
- `compaction_trigger`

结论：在代理入口复制完整枚举属于路径依赖错误，后续协议升级仍会重复故障。

## 三、架构决策

### 3.1 采用开放联合类型

Responses `input[]` 按开放联合类型处理：

- 本地校验“结构是否安全、基本类型是否正确”。
- 本地不再校验“type 是否属于一份固定完整枚举”。
- 已知类型由转换器按类型处理。
- 未知类型保持原样透传。
- 最终能力判断由选中的真实上游负责。

### 3.2 不采用的方案

#### 方案 A：只增加 `additional_tools`

不采用。下一次新增 `compaction_trigger` 或其他类型仍会再次中断。

#### 方案 B：运行时下载官方 Schema

不采用。会引入启动期网络依赖、文档可用性风险和动态行为漂移。

#### 方案 C：按模型维护多套类型白名单

不采用。类型能力不仅由模型决定，还与上游实现、账号类型和协议版本相关，维护成本高且仍会滞后。

## 四、兼容规则

### 4.1 `input` 为字符串

保持现有兼容行为，不改。

### 4.2 `input` 为数组

继续拒绝空数组。

每个数组元素：

1. 必须是 JSON 对象。
2. 没有 `type` 时继续兼容 message-style 输入，例如 `{role, content}`。
3. 存在 `type` 时必须是非空字符串。
4. 不再要求 `type` 命中本地固定枚举。
5. 不修改未知类型的附加字段。

### 4.3 已知转换

保留现有转换行为：

- `compaction` 继续转换为上游可接受的 developer message。
- 已知 content part、function tool、image generation 等继续走现有归一化。
- 不给未知类型增加猜测性转换。

### 4.4 上游错误

- 本地结构错误：继续返回明确的本地参数错误。
- 未知类型被真实上游拒绝：透传或归一化为真实上游错误。
- 不再由本地返回 `invalid_input_type`。

## 五、实施文件

### 必改

- `api/validation.go`
  - 将 `ValidateInput()` 从封闭枚举校验改为结构校验。

- `api/validation_test.go`
  - 删除或改写“拒绝未知类型”的旧契约。
  - 增加官方新类型、未来类型和畸形结构测试。

### 按测试结果补充

- `proxy/translator_test.go`
  - 验证 `PrepareResponsesBody()`、`PrepareOpenAIResponsesBody()` 对未知类型字段无损保留。

- `proxy/handler_test.go`
  - 验证 HTTP `/v1/responses` 不在本地拒绝 `additional_tools`。
  - 使用 mock upstream 验证请求确实到达上游。
  - 验证上游拒绝时返回上游错误，而不是本地 `invalid_input_type`。

- `proxy/responses_ws_test.go`
  - 验证 WebSocket 请求中的未来输入类型可进入转发准备链路。

如果现有测试文件已覆盖对应 helper，应优先复用，禁止为测试方便增加生产代码专用入口。

## 六、TDD 执行顺序

### 阶段 1：入口验证契约

#### RED

在 `api/validation_test.go` 增加：

```go
func TestValidateResponsesAPIRequestAllowsAdditionalToolsInputType(t *testing.T)
```

请求包含 `type: "additional_tools"`，期望 `result.Valid == true`。

增加：

```go
func TestValidateResponsesAPIRequestAllowsFutureInputType(t *testing.T)
```

请求包含例如：

```json
{
  "type": "future_protocol_item",
  "future_field": {
    "enabled": true
  }
}
```

期望通过本地验证。

增加：

```go
func TestValidateResponsesAPIRequestRejectsNonObjectInputItem(t *testing.T)
```

输入数组包含数字、布尔值或数组，期望返回结构错误。

增加：

```go
func TestValidateResponsesAPIRequestRejectsNonStringInputType(t *testing.T)
```

`type` 为对象、数字或布尔值，期望返回类型错误。

运行并确认前两个测试因旧白名单失败：

```powershell
go test ./api -run "TestValidateResponsesAPIRequestAllowsAdditionalToolsInputType|TestValidateResponsesAPIRequestAllowsFutureInputType|TestValidateResponsesAPIRequestRejectsNonObjectInputItem|TestValidateResponsesAPIRequestRejectsNonStringInputType" -count=1 -v
```

#### GREEN

修改 `ValidateInput()`：

- 删除 `validTypes` 完整枚举。
- 遍历数组元素并校验元素为对象。
- `type` 不存在时放行 message-style 对象。
- `type` 存在时只校验其 JSON 类型为字符串且内容非空。
- 未知字符串类型直接放行。

重新运行阶段测试并确认通过。

### 阶段 2：转换层字段无损

#### RED

在 `proxy/translator_test.go` 增加测试，输入包含：

```json
{
  "type": "additional_tools",
  "tools": [
    {
      "name": "future_tool",
      "metadata": {
        "version": 2
      }
    }
  ],
  "future_field": "preserve-me"
}
```

分别调用：

- `PrepareResponsesBody()`
- `PrepareOpenAIResponsesBody()`
- 存在现成入口时覆盖 `PrepareResponsesWebSocketBody()`

断言：

- `type` 保留。
- `tools` 保留。
- 嵌套 `metadata.version` 保留。
- `future_field` 保留。
- 不允许仅比较字符串顺序，应使用 JSON 解析后按字段断言。

运行：

```powershell
go test ./proxy -run "TestPrepare.*PreservesFutureInputItem" -count=1 -v
```

如果测试直接通过，说明转换层已天然无损；保留测试作为升级契约，不额外修改生产代码。

### 阶段 3：HTTP 转发闭环

#### RED

在 `proxy/handler_test.go` 使用现有 mock upstream/account helper：

1. 发送包含 `additional_tools` 的 `/v1/responses` 请求。
2. mock upstream 捕获请求体。
3. 断言请求不返回本地 400。
4. 断言 mock upstream 收到完整输入项。

增加上游拒绝场景：

1. mock upstream 返回 400 和自定义错误码。
2. 断言下游收到上游错误。
3. 断言响应中不包含本地 `invalid_input_type`。

运行：

```powershell
go test ./proxy -run "TestResponses.*AdditionalTools|TestResponses.*FutureInputType" -count=1 -v
```

### 阶段 4：WebSocket 与 Compact

验证：

- WebSocket 入口复用 `ResponsesAPIValidationRulesForModel()` 后不会拒绝未来类型。
- WebSocket HTTP fallback 准备体保留未来类型。
- `/responses/compact` 经过相同入口规则时不产生本地 `invalid_input_type`。

只添加能验证真实公共行为的测试，不为追求测试数量重复同一 helper 的断言。

## 七、全量验证

按顺序执行：

```powershell
gofmt -w api/validation.go api/validation_test.go proxy/translator_test.go proxy/handler_test.go proxy/responses_ws_test.go
```

仅对实际修改且存在的文件执行格式化。

```powershell
go test ./api -count=1
```

```powershell
go test ./proxy -count=1
```

```powershell
go test ./proxy/... -count=1
```

```powershell
go test ./... -count=1
```

```powershell
go build -o codex2api.new.exe .
```

完成前检查：

```powershell
git diff --check
git diff -- api/validation.go api/validation_test.go proxy/translator_test.go proxy/handler_test.go proxy/responses_ws_test.go
git status --short
```

## 八、验收标准

必须同时满足：

1. `additional_tools` 不再被本地入口拒绝。
2. `compaction_trigger` 和模拟未来类型可通过本地验证。
3. 未知类型字段经过请求转换后无损保留。
4. 非对象 input item、非字符串 type 等结构错误仍会被本地拒绝。
5. 已有 `compaction`、消息输入、工具调用输入测试不回归。
6. HTTP、WebSocket、Compact 至少通过共享验证契约和关键转发测试覆盖。
7. 上游不支持时错误归因属于上游，不再出现本地 `invalid_input_type`。
8. `go test ./... -count=1` 退出码为 0。
9. `go build -o codex2api.new.exe .` 退出码为 0。
10. 不覆盖或回退当前工作区已有的 HTTP/2、提示词、安全取证、代理 URL 等未提交改动。

## 九、当前工作区注意事项

当前工作区已有大量未提交修改，并且以下文件与本任务存在重叠：

- `proxy/handler.go`
- `proxy/handler_test.go`

实施时必须：

1. 先检查这些文件的 staged 与 unstaged diff。
2. 只追加本任务测试或最小必要修改。
3. 禁止执行 `git reset --hard`、`git checkout --` 或覆盖整个文件。
4. `.codegraph/daemon.pid` 属于工具痕迹，不应混入业务提交。
5. 未经用户明确要求，不提交、不推送、不热替换正式服务。

## 十、建议的新窗口开场指令

```text
阅读 .agent/plan-Responses输入前向兼容闭环.md 和 .agent/handoff.md，
按 TDD 顺序直接实施完整兼容闭环。保留当前工作区全部已有改动，
不要提交、不要推送、不要热替换服务；完成后运行全量测试和构建。
```

## 十一、实施结果（2026-07-15）

### 11.1 已完成改动

- `api/validation.go`
  - 删除 `ValidateInput()` 的封闭 `validTypes` 白名单。
  - 数组元素必须是 JSON 对象。
  - 显式 `type` 必须是非空字符串。
  - 未知但结构合法的类型直接放行。

- `api/validation_test.go`
  - 覆盖 `additional_tools`、`compaction_trigger`、模拟未来类型。
  - 覆盖 null、布尔值、数组等非对象元素。
  - 覆盖 null、对象、布尔值、数字、空白字符串等非法 `type`。

- `proxy/translator_test.go`
  - 覆盖实际 `input[0].type = "additional_tools"`。
  - 覆盖 `compaction_trigger`、模拟未来类型及嵌套字段。
  - 覆盖 Responses、Responses WebSocket、Compact、OpenAI Responses、OpenAI Responses WebSocket 准备函数。

- `proxy/responses_future_input_test.go`
  - 覆盖 OpenAI Responses HTTP、Compact、原生 WebSocket 上游到达。
  - 覆盖 Codex `PrepareResponsesBody -> ExecuteRequest` 准备链路。
  - 覆盖 WebSocket 转 HTTP fallback。
  - 覆盖上游 400 错误归因，确认不再返回本地 `invalid_input_type`。

### 11.2 TDD 证据

在未修改生产代码的 `HEAD` 临时工作树中运行新测试：

- `additional_tools`、`compaction_trigger`、模拟未来类型均被旧白名单拒绝。
- 非对象 input item 未被旧实现拦截。
- HTTP、Compact、WebSocket 请求均在本地返回 `invalid_input_type`，未到达 mock upstream。
- 转换层字段保真测试在旧实现上已通过，因此未修改转换生产代码，只保留契约测试。

当前工作树中全部聚焦测试已转绿。

### 11.3 验证结果

```text
go test ./api -count=1          exit 0
go test ./proxy -count=1        exit 0
go test ./proxy/... -count=1    exit 0
go test ./... -count=1          exit 0
go build -o codex2api.new.exe . exit 0
git diff --check                exit 0
```

构建产物 SHA256：

```text
D2FE79F9BF05F7BE296E0269636890954DFAEC077365CC351ED90C07E8A15CBB
```

### 11.4 交付边界

- 未提交。
- 未推送。
- 未热替换或重启正式服务。
- 当前工作区其他 HTTP/2、提示词、安全取证、代理 URL 等改动未被回退或覆盖。
