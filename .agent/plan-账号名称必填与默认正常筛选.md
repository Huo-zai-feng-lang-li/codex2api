# 账号名称必填与默认正常筛选

## 目标
- 账号管理页面默认选中“正常”筛选。
- 手动新增账号的 5 个入口统一要求填写账号名称：Refresh Token、Session Token、Access Token、API Key、OAuth 授权。
- 新增/编辑 API Key 类型账号后，请求记录继续通过账号 `name` 展示来源账号。

## 已完成
- `frontend/src/pages/Accounts.tsx`
  - `statusFilter` 默认值从 `all` 改为 `normal`。
  - RT/ST/AT/API Key/OAuth 新增表单均展示 `账号名称 *`。
  - RT/ST/AT/API Key/OAuth 提交按钮均在账号名称为空时禁用。
  - API Key 编辑页账号名称也保持必填。
- `frontend/src/types.ts`、`frontend/src/api.ts`
  - 手动新增和 OAuth 请求类型收紧为必传 `name`。
- `admin/handler.go`、`admin/oauth.go`
  - 后端拒绝空账号名称，防止绕过前端直接写入默认名或空名。
  - OAuth 生成授权链接阶段保存账号名称，兑换阶段继承并校验名称。
- `admin/handler_test.go`、`admin/oauth_test.go`
  - 覆盖 RT/ST、AT、OpenAI Responses API、OAuth 空名称拒绝。

## 验证
- `go test ./admin -count=1` 通过。
- `pnpm typecheck` 通过。
- `pnpm build` 通过。
- 浏览器实看 `http://127.0.0.1:5173/admin/accounts`：
  - 默认高亮“正常 2”，列表显示 `显示 1-2 / 共 2 条`。
  - 添加账号弹窗 5 个 tab 均出现 `账号名称 *`。

## 边界
- 文件导入、远程迁移、Sub2API 导入未强制新增账号名称；本次只覆盖截图里的手动新增入口。
