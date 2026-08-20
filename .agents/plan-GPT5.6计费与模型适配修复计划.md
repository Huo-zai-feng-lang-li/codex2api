# GPT-5.6 系列计费与模型注册表官方适配与修复计划

## 1. 核心修复目标
解决 `codex2api` 中缺乏 GPT-5.6 系列（Luna, Terra, Sol）官方计费规则的问题，消除由于全局 `gpt-5` 模糊归一化通配导致的严重多扣费（如 Luna 被高收 12.5 倍），实现全链路 100% 对齐 OpenAI 2026年7月30日公布的最新官方定价。

---

## 2. 事实取证与逻辑推演

### 2.1 官方最新定价 (2026-07-30)
- **GPT-5.6 Luna**: Input $0.20/1M, Output $1.20/1M, Cache $0.02/1M (优先层 2x: Input $0.40, Output $2.40)
- **GPT-5.6 Terra**: Input $2.00/1M, Output $12.00/1M, Cache $0.20/1M (长上下文 >272K: Input $4.00, Output $18.00)
- **GPT-5.6 Sol**: Input $3.00/1M, Output $15.00/1M, Cache $0.30/1M (长上下文 >272K: Input $6.00, Output $22.50)
- **GPT-5.6 (通配标准项)**: 映射至 `gpt-5.6-sol` 定价逻辑

### 2.2 影响链路与涉及文件
1. `database/billing.go`
   - `normalizeCodexBillingModel()`: 添加 `gpt-5.6-luna`, `gpt-5.6-terra`, `gpt-5.6-sol`, `gpt-5.6` 的优先精准识别分支，放置在通用 `gpt-5` 通配规则之前。
   - `modelPricingRules`: 添加 `gpt-5.6-luna`, `gpt-5.6-terra`, `gpt-5.6-sol`, `gpt-5.6` 四套定价规则。
2. `proxy/model_registry.go`
   - `builtinModelInfos`: 注册 `gpt-5.6-sol`, `gpt-5.6-terra`, `gpt-5.6-luna`, `gpt-5.6` 内置模型定义。
3. `database/billing_test.go`
   - 添加包含 `gpt-5.6-luna`, `gpt-5.6-terra`, `gpt-5.6-sol` 及其变体名称、长上下文和服务级别的计费断言单测。

---

## 3. 极简执行步骤

### 阶段一：更新 `database/billing.go` 计费引擎
- 在 `modelPricingRules` 中注入全套 GPT-5.6 价格定义。
- 修正 `normalizeCodexBillingModel` 函数，前置归一化 `gpt-5.6` 及其子型号。

### 阶段二：更新 `proxy/model_registry.go` 内置模型注册表
- 在 `builtinModelInfos` 中添加 GPT-5.6 系列模型。

### 阶段三：强化单元测试并校验
- 在 `database/billing_test.go` 增加断言测试。
- 运行 `go test ./...` 校验全工程通过。
