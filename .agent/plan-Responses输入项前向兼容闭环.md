# Responses 输入项前向兼容闭环 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 `/v1/responses` 对 `additional_tools`、`compaction_trigger` 及未来新增的结构合法输入项保持前向兼容，同时继续拦截畸形输入，并覆盖 HTTP、WebSocket、Compact 与转换链路。

**Architecture:** 本地入口只验证 Responses 输入项的 JSON 包络结构，不维护会随上游演进而过期的封闭类型白名单。已知兼容转换继续由 `proxy/translator.go` 按类型处理，未知类型及其字段无损透传，最终能力判定交给真实上游。

**Tech Stack:** Go、Gin、gjson、标准库 `testing`、现有 `httptest` 上游模拟器。

---

### Task 1: 用失败测试定义开放类型契约

**Files:**
- Modify: `api/validation_test.go`
- Test: `api/validation_test.go`

- [ ] **Step 1: 将“未知类型必须拒绝”改为“结构合法的新类型必须接受”**

新增表驱动用例，至少覆盖：

```go
tests := []string{
    "additional_tools",
    "compaction_trigger",
    "future_response_item_v1",
}
```

每个用例调用 `ValidateResponsesAPIRequest`，断言 `result.Valid == true`。

- [ ] **Step 2: 增加畸形包络拒绝用例**

覆盖数组元素不是对象、显式 `type` 不是字符串、显式 `type` 为空字符串；断言返回稳定的本地结构错误，而不是依赖具体已知类型枚举。

- [ ] **Step 3: 运行测试确认 RED**

Run:

```powershell
go test ./api -run "TestValidateResponsesAPIRequestAllowsFutureInputTypes|TestValidateResponsesAPIRequestRejectsMalformedInputItems" -count=1
```

Expected: 新类型兼容测试失败，证明旧白名单是触发点。

### Task 2: 将输入校验改为开放世界结构校验

**Files:**
- Modify: `api/validation.go:647`
- Test: `api/validation_test.go`

- [ ] **Step 1: 删除 `validTypes` 封闭枚举**

`ValidateInput` 保留空数组检查；逐项要求数组元素为 JSON 对象。未提供 `type` 时继续允许 `{role, content}` 消息式输入；提供 `type` 时只要求它是去空格后非空的字符串。

- [ ] **Step 2: 保持错误字段精确**

畸形元素错误定位到 `input.N`，畸形类型错误定位到 `input.N.type`，继续使用标准 `ValidationError`，不再对未知但合法的类型返回 `invalid_input_type`。

- [ ] **Step 3: 运行 API 测试确认 GREEN**

Run:

```powershell
go test ./api -count=1
```

Expected: `api` 包全部通过。

### Task 3: 锁定转换层无损透传

**Files:**
- Modify: `proxy/translator_test.go`
- Test: `proxy/translator_test.go`

- [ ] **Step 1: 添加 Codex 请求体保真测试**

构造含 `additional_tools`、`compaction_trigger`、`future_response_item_v1` 及自定义嵌套字段的请求，调用 `PrepareResponsesBody`，断言每个未知项的 `type` 和载荷字段仍存在且值不变。

- [ ] **Step 2: 添加 OpenAI Responses 请求体保真测试**

对同一请求调用 `PrepareOpenAIResponsesBody`，断言未知项未被删除、重命名或扁平化。

- [ ] **Step 3: 运行测试确认现有转换是否已经满足契约**

Run:

```powershell
go test ./proxy -run "TestPrepareResponsesBodyPreservesFutureInputItems|TestPrepareOpenAIResponsesBodyPreservesFutureInputItems" -count=1
```

Expected: 若当前转换已无损则直接通过；若失败，只在对应已知归一化函数中做最小修复。

### Task 4: 覆盖入口、路由与真实上游边界

**Files:**
- Modify: `proxy/handler_test.go`
- Modify: `proxy/responses_ws_test.go`（仅在现有 WebSocket 测试结构需要独立文件时）
- Test: `proxy/handler_test.go`
- Test: `proxy/responses_ws_test.go`

- [ ] **Step 1: HTTP `/v1/responses` 转发测试**

使用现有 OpenAI Responses 测试账号与 `httptest.Server`，发送首项为 `additional_tools` 的请求；断言本地不返回 `invalid_input_type`，模拟上游实际收到原始类型与载荷。

- [ ] **Step 2: Compact 入口兼容测试**

发送包含未来类型的 compact 请求，断言通过本地校验并进入既有路由/上游处理，不在入口被白名单拦截。

- [ ] **Step 3: WebSocket 入口兼容测试**

复用现有 WebSocket 请求校验路径，断言未来类型不触发本地 `invalid_input_type`；HTTP 回退体仍保留该项。

- [ ] **Step 4: 运行入口契约测试**

Run:

```powershell
go test ./proxy -run "TestResponses.*FutureInput|TestResponsesCompact.*FutureInput|TestResponsesWebSocket.*FutureInput" -count=1
```

Expected: 全部通过，且模拟上游断言收到原始输入项。

### Task 5: 全量验证与影响审计

**Files:**
- Modify: `.agent/plan-Responses输入项前向兼容闭环.md`

- [ ] **Step 1: 运行格式化和静态检查**

Run:

```powershell
gofmt -w api/validation.go api/validation_test.go proxy/translator_test.go proxy/handler_test.go proxy/responses_ws_test.go
go vet ./...
```

Expected: 退出码 0；不存在新增诊断。

- [ ] **Step 2: 运行全量测试**

Run:

```powershell
go test ./... -count=1
```

Expected: 全部通过。

- [ ] **Step 3: 构建正式二进制**

Run:

```powershell
go build -o codex2api.new.exe .
```

Expected: 退出码 0。

- [ ] **Step 4: 检查最终差异**

确认只修改 Responses 兼容校验、相关契约测试和本计划；不覆盖当前工作区其他未提交改动，不引入模型名称硬编码，不改数据库结构。

- [ ] **Step 5: 更新计划结果**

在本文件追加实际 RED/GREEN、全量测试、构建结果和影响范围，形成可跨会话复验的闭环记录。
