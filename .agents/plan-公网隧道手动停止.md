# 公网隧道手动停止

## 问题

隧道只有「开机自启」和「立即启动」，没有停止入口：

- 关自启只删启动项，不杀当前进程；
- 手动 `taskkill` 后，只要自启开着，守护协程 15 秒内会把 cloudflared 拉回来；
- 想临时下线只能杀进程再关自启，步骤多且容易漏。

## 方案

- 新增 `data/tunnel/stopped.flag` 作为「手动停止」标记。
- `POST /api/admin/tunnel/stop`：先落标记，再按 metrics 端口 `127.0.0.1:20260` 定位本服务的 cloudflared 并 `taskkill /F`；标记优先于杀进程，避免守护抢先重启。
- `POST /api/admin/tunnel/start`：清除标记后再拉起，等边缘就绪。
- 守护协程和启动目录 VBS 都尊重标记：暂停期间不自动拉起，下次登录也不启动。
- 管理台新增「停止隧道」按钮和「已手动停止」状态。

## 改动

- `admin/tunnel_windows.go`：停止标记读写、按 metrics 端口找 PID、`stopTunnelProcess`、守护/VBS 判断标记。
- `admin/tunnel_other.go`：非 Windows 平台桩实现。
- `admin/handler.go`：`/tunnel/stop` 路由、`StopTunnel`、响应补 `paused`、设置接口补 `tunnel_paused`。
- 前端：`api.ts`、`types.ts`、`Settings.tsx`、`locales/zh|en.json`。
- 版本 `v2.4.2`，CHANGELOG 记录。

## 验证

- `go test ./admin -run Tunnel -count=1` 通过（停止标记往返 + VBS 尊重标记 + 启动参数顺序）。
- 实测（自启开启状态）：停止后 `running=false,paused=true`，`metrics 20260` 无监听；等待 17 秒守护未拉起；启动后 `ready=true`，公网 `/health` 200。
- 实测停止期间公网不可达（`http=000`），符合预期。

## 遗留

- 「停止」只影响当前运行与登录自启；要连自启一起关，仍需操作「开机自启」开关。
- 机器上另有独立 cloudflared 实例（不同 tunnel id），停止接口只按 20260 端口定位，不会误杀。
