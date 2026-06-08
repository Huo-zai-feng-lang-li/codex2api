# OAuth 回调有效页面修复计划

## 目标
- 保持 OAuth 注册回调地址 `http://localhost:1455/auth/callback` 不变。
- 服务运行在 `18080` 时，额外提供 `127.0.0.1:1455` 本地回调服务。
- 用户授权后看到项目自己的有效页面，而不是 Chrome 的连接失败页。
- `1455` 不承载后台页面；误访问 `/admin/accounts` 时显示说明页并引导回 `18080` 后台。

## 方案
1. [已实施] 新增 OAuth 回调专用 router：
   - `/auth/callback` 复用现有 `admin.Handler.OAuthCallback`。
   - `/` 和其他路径返回 OAuth 回调说明页。
2. [已实施] 主服务启动后，若主端口不是 `1455`，额外监听 `127.0.0.1:1455`。
3. [已实施] 退出时同时优雅关闭主服务和 OAuth 回调服务。
4. [已实施] 前端接入已有 `poll-callback` 接口，自动感知回调完成并刷新账号列表。
5. [已实施] 先补测试再实现：
   - `/admin/accounts` 在 OAuth 回调端口返回说明页。
   - `/auth/callback` 仍进入现有 OAuth 回调处理。
   - 自动回调已完成后，手动粘贴同一 URL 不再二次兑换 code。

## 验证
- `go test ./admin ./...` 中至少覆盖 OAuth 和主包新增测试。
- 本地启动后访问：
  - `http://127.0.0.1:18080/admin/`
  - `http://localhost:1455/`
  - `http://localhost:1455/auth/callback`
