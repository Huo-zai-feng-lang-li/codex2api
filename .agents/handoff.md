# 最新接续状态 (2026-11-25 12:45)

## 2026-11-25 公网隧道托管 + 开机自启开关（v2.4.0）

- **根因**：公网域名 DNS CNAME 指向隧道 `a3adcb37`（name=codex2api），该隧道长期 `status=down / conns=0` → 公网 530。`~/.cloudflared/config.yml` 引用的 `a3adcb37...json` 凭据文件不存在，config 模式根本起不来。
- **修复路径**：改用 token 模式（CF API 取 tunnel token），远端 ingress 已存在（`http://127.0.0.1:18080`），实测 4 条连接注册、公网 200。
- **新增能力**：
  - 管理台「系统设置 → 公网隧道」卡片：域名 + 状态徽标 + **开机自启开关** + 「立即启动隧道」。
  - 自启 = 启动目录 `Codex2apiTunnel.vbs`（隐藏窗口）+ 标记文件 `data/tunnel/autostart.enabled`，登录即拉起，不依赖服务是否启动。
  - 服务内守护协程每 15s 巡检，自启开启且进程掉线时自动重启；服务启动即武装，不等前端打开页面。
  - 隧道进程经立即退出的 `wscript` 拉起成孤儿进程，`taskkill /T` 不再连带杀隧道。
  - 状态探测改为拨测独占 metrics 端口 `127.0.0.1:20260/ready`（~1ms），替代每 10s 一次 `tasklist`（实测 171ms/次 ≈ 1.7% 单核），并消除"机器上其他 cloudflared 被误判为我们的隧道"。
- **参数坑（重要）**：`--metrics` / `--logfile` 必须写在 `run` 之前，`--token` 写在 `run` 之后。写错会被 cloudflared 判 `flag provided but not defined` 并以 **exit 0 静默退出**（只往 stdout 打 usage），表现为"启动即消失"。
- **协议**：不再硬编码 `--protocol http2`。cloudflared precheck 自身判定：本机 QUIC region2 失败 → 打印 `degraded transport` 并回落 http2。
- **接口**：`GET /api/admin/tunnel`、`POST /api/admin/tunnel/autostart {enabled}`、`POST /api/admin/tunnel/start`。
- **凭据**：`CODEX_TUNNEL_TOKEN` 环境变量 > `data/tunnel/tunnel-token.txt`（`/data/` 已在 gitignore）。

### 验证凭证（全部实测）

- `go build ./...` / `go vet ./admin ./api` 通过；`go test ./admin` 通过（含 2 个新增 `tunnel_windows_test.go` 用例）。
- `build-and-restart.bat` 热替换成功（PID 18420，`/health` 200）。
- 开关打开 → VBS 落地 + 标记文件存在 → `GET /api/admin/tunnel` 返回 `{"autostart":true,"ready":true}`。
- 关闭 → VBS 与标记文件双双消失。
- 单独执行 VBS（模拟登录）→ cloudflared 起来、公网 200。
- 杀进程 → 守护 15s 内拉起、公网 200（实测 4s 生效）。
- 热替换后 cloudflared 的 `ParentProcessId` 已死（孤儿进程），不再被进程树连带杀掉。
- 公网实测：本机 21ms；公网 TTFB 0.9–1.2s 常见，偶发 3.3s / 12.9s / 122s（网络层抖动，非隧道逻辑）。

### 提交

- `ca49195 feat(tunnel): 公网隧道托管 + 开机自启开关，tasklist 探针改为 metrics /ready`，tag `v2.4.0`，**未推送**（需 VPN）。

## 待办

- [ ] 用户侧：确认「开机自启」保持开启（当前为开）。
- [ ] 若要压低公网 TTFB：本机到 CF 边缘 RTT ~0.25s、CF 静态页 TTFB ~0.75–1.1s，瓶颈在链路；迁到海外 VPS 才是根治。

## 关键上下文

- **目录**: `D:\Desktop\Super-File\codex2api`
- **服务**: `http://127.0.0.1:18080`，CF 公网 `https://codex2api.ai95.indevs.in`
- **当前版本**: v2.4.0
- **Admin 密钥**: `.env` 中 `ADMIN_SECRET=133413`（请求头 `X-Admin-Key`）
- **隧道**: id `a3adcb37-c179-43b6-b1ec-0f9cb8d12ca6`，token 存 `data/tunnel/tunnel-token.txt`
- **cloudflared**: `D:\cloudflared\cloudflared.exe`（v2026.10.7）
- **CF 账号**: `6b14a138558986705be9edd377d7d092`；另一条隧道 `c62b7d95`（antigravity-gateway）由 Cuckoo-Code 自己拉起
- **MCP**: `C:\Users\Administrator\.agents\mcp.json` 已加 `cloudflare`（mcp-cloudflare-lite，含 ACCOUNT_ID / API_TOKEN），`initialize` 实测通过
- **本轮核心文件**:
  - `admin/tunnel_windows.go` — token 读取、进程拉起（孤儿化）、`/ready` 探测、VBS 生成、守护协程
  - `admin/tunnel_other.go` — 非 Windows 桩
  - `admin/tunnel_windows_test.go` — 启动脚本参数顺序 / 隐藏窗口回归测试
  - `admin/handler.go` — 三路由、`tunnel_state`，删 tasklist 探针
  - `frontend/src/pages/Settings.tsx`、`api.ts`、`types.ts`、`locales/*.json`
  - `docs/ARCHITECTURE.md` — 新增「公网隧道托管（v2.4.0）」

---

**注意**：本文件由 A-handoff 技能覆盖写入，仅保留最新接续状态。历史细节请查阅 git log 与 `.agents/plan-*.md`。
