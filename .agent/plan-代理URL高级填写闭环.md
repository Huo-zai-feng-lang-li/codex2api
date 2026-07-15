# 代理 URL 高级填写闭环

## 目标
- 保留单一真实值：账号/全局配置仍只保存 `proxy_url`，代理池仍只保存每条代理的 `url`。
- 前端提供可折叠“高级填写”：协议、主机、端口、用户名、密码。
- 高级字段实时拼接完整 URL，并保留预览。
- 仍支持直接粘贴完整 URL。
- 覆盖账号添加/编辑、OAuth、OpenAI Responses 账号、全局代理、代理池新增/编辑。

## 已完成
- 新增 `frontend/src/utils/proxyUrl.ts`：集中解析、拼接、校验代理 URL。
- 新增 `frontend/src/components/ProxyUrlInput.tsx`：单真值输入 + 高级填写 + 预览 + 密码显隐。
- 已接入账号页与系统设置页。
- 已接入代理池页：
  - 单条新增走 `ProxyUrlInput`。
  - 批量新增保留 textarea，用于一行一个完整 URL。
  - 编辑代理走 `ProxyUrlInput`。
- 已补齐中英文 `proxyInput` 文案。
- 已补代理 URL 单测，并完成红灯验证：缺少端口、用户名/密码不成对时测试失败。
- 2026-06-15 17:11 追加闭环：
  - 统一默认代理地址常量 `DEFAULT_PROXY_URL = http://127.0.0.1:51081`。
  - 代理池单条新增默认填入 `http://127.0.0.1:51081`；用户清空后再次打开新增面板也会恢复默认值。
  - 代理 URL 主输入框与右侧“高级填写”按钮高度统一为 `h-9`。
- 2026-06-15 17:30 修复高级区凭据误填：
  - URL 未解析出用户名/密码时，高级区用户名/密码保持空值。
  - 用户名 placeholder：`请填写代理 IP 用户名`。
  - 密码 placeholder：`请填写代理 IP 密码`。
  - 增加随机输入框 name、`one-time-code` 和未主动输入前忽略凭据变更，避免浏览器/密码管理器把后台登录账号密码填进代理凭据。

## 当前规则
- 必填：协议、主机、端口。
- 选填：用户名、密码。
- 用户名和密码必须同时填写或同时留空。
- 支持协议：`http` / `https` / `socks5` / `socks5h`。

## 待验证
- 已完成 `node --test src/utils/proxyUrl.test.mjs`：9/9 通过。
- 已完成 `npm run typecheck`：退出码 0。
- 已完成 `npm run build`：退出码 0。
- 已完成 `go test ./...`：退出码 0。
- 已完成构建并热替换重启：
  - 新 PID：`23852`
  - 新 exe SHA256：`416926976F5AB7E046B5689DB909FF1C47449B7069C0F4C9E623EB588B1AD948`
  - 备份：`codex2api.prev-20260615-173040.exe`
  - `/health`：`{"available":1,"status":"ok","total":4}`
- 浏览器验收：`/admin/proxies` 添加代理 -> 高级填写；默认 URL 为 `http://127.0.0.1:51081`，主机 `127.0.0.1`，端口 `51081`，用户名/密码值为空，placeholder 正确，输入框/高级按钮高度均为 36px。
