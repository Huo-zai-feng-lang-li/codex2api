# 计划：公网隧道（Cloudflare Tunnel）开机自启开关

## 背景事实（已取证）

- 公网域名 `codex2api.ai95.indevs.in` 的 DNS CNAME 指向隧道 `a3adcb37-c179-43b6-b1ec-0f9cb8d12ca6`（name=codex2api）。
- 该隧道长时间 `status=down / conns=0` → 公网访问 530。本机 18080 `/health` 正常。
- `~/.cloudflared/config.yml` 引用的 `a3adcb37...json` 凭据文件**不存在**，config 模式无法启动（实测报错）。
- 可用路径：用 API 取隧道 token，`cloudflared tunnel run --token` 启动；远端 ingress 已存在（`http://127.0.0.1:18080`）。
- 实测：token 模式启动后 4 条 QUIC 连接注册成功，公网 `/health` 200（1171ms，含冷启握手）。
- 机器上另有 Cuckoo-Code 拉起的 cloudflared（隧道 `c62b7d95`），旧探测 `tasklist` 只看进程名会误报"隧道在跑"。
- 旧探测方式每次 `tasklist` 实测 171ms，每 10s 一次。

## 目标

1. 管理台新增"公网隧道"卡片：显示域名、运行状态、**开机自启开关**、立即启动按钮。
2. 自启实现：登录时用 Startup 目录的隐藏 VBS 拉起 `cloudflared tunnel run --token`，不依赖本服务是否启动。
3. 进程守护：服务内 goroutine 每 15s 检查就绪状态，掉线自动重启。
4. 状态探测改为 metrics 端口 `/ready`（127.0.0.1:20260，~1ms），替代 171ms 的 tasklist，同时消除误报。
5. token 存放：`data/tunnel/tunnel-token.txt`（gitignore 已覆盖 `/data/`），支持 `CODEX_TUNNEL_TOKEN` 覆盖。

## 改动清单

- 新增 `admin/tunnel_windows.go`：token 读取、cloudflared 定位、启动、就绪探测、自启 VBS 增删、守护 goroutine。
- 新增 `admin/tunnel_other.go`：非 Windows 桩实现（自启不支持，探测走 /ready）。
- `admin/handler.go`：删旧 `probeTunnelProcess/tasklist` 逻辑；新增 `tunnel_autostart`/`tunnel_ready` 字段；新增 3 个路由。
- 前端：`api.ts`、`types.ts`、`Settings.tsx` 隧道卡片、`locales/zh.json`、`locales/en.json`。
- 文档：`docs/ARCHITECTURE.md` 补一节、`CHANGELOG.md` v2.4.0、`frontend/package.json`、`api/middleware.go`、git tag。

## 验收（必须物理取证）

- `go build ./...`、`go vet ./...`、`go test ./... -count=1` 全过。 → **通过**（`go test ./... -count=1 -p 2` 16 包全 ok；`go vet ./admin ./api` 通过；`go test ./admin` 含新增 2 用例）
- 前端 `npm run build` 通过。 → **通过**（`npx tsc --noEmit` 0 错误，`build-and-restart.bat` 内 `npm run build` 成功）
- 开关打开后：`data/tunnel/Codex2apiTunnel.vbs` 出现在 Startup 目录；公网 `/health` 200；关掉后 VBS 消失。 → **通过**（VBS 543B；`/api/admin/tunnel` → `autostart:true,ready:true`；公网 200；关闭后 VBS + 标记文件均消失）
- `build-and-restart.bat` 热替换成功，`/health` ok。 → **通过**（PID 18420，本地 200）

## 追加发现（实施中）

1. `--metrics` / `--logfile` 必须位于 `run` 之前；顺序写错 → cloudflared exit 0 静默退出。
2. 隧道作为服务子进程时，`taskkill /T` 会连带杀掉 → 改为经立即退出的 wscript 拉起成孤儿进程。
3. 服务启动即武装守护（不等前端打开设置页），热替换后 3 秒内恢复公网。
4. 公网 TTFB 抖动（3.3s / 12.9s / 122s 偶发）属网络层，非隧道逻辑；autostart 只消除 530 冷启动。

