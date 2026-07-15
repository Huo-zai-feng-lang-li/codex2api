# 非 C 盘原文取证与高流量安全审计计划

## 目标
- 安全事件与原文取证增加总开关，默认关闭，避免高流量下默认消耗 CPU、DB 与磁盘。
- request / response 原文后续优先写入用户配置的非 C 盘目录；当前未配置时先回退到本机数据库 inline 保存。
- 已废弃“不允许回退 C 盘导致原文缺失”的旧策略；回退 inline 必须受单条大小、保留天数和容量清理约束。
- 安全事件页仍能展示扫描命中的恶意提示、可疑脚本行为、工具调用和对应原文。

## 核心判断
- 高流量场景下，数据库不适合保存大段原文正文；DB 应只保存索引、证据、hash、路径和摘要。
- 非 C 盘落盘必须是显式配置；未配置时不自动写临时目录，而是使用当前数据库 inline 兼容路径保存证据。
- 轻量行为规则可以进实时链路；YARA 只适合异步或手动深度扫描，不进默认实时链路。

## 推荐默认值
- `security_audit_enabled = false`
- `upstream_guard_mode = off`
- `security_capture_mode = hit_raw`
- `security_capture_storage = inline_fallback`
- `security_capture_storage_root = ""`
- `security_capture_retention_days = 1`
- `security_capture_max_body_bytes = 1048576`
- `security_capture_max_storage_bytes = 2147483648`
- `security_script_scan_enabled = true`，但只在总开关打开后生效。
- `security_yara_enabled = false`

## 数据落盘设计

### 目录结构
示例路径由用户设置，例如 `D:\codex2api-security-captures`。

```text
D:\codex2api-security-captures\
  2026-06-09\
    ab\
      req_xxx.request.json.gz
      req_xxx.response.sse.gz
      req_xxx.meta.json
```

设计原则：
- 按日期和 request_id hash 前缀分片，避免单目录文件过多。
- 正文用 gzip 压缩保存，API 读取时解压展示；hash 仍按原始未压缩 bytes 计算。
- 文件先写 `.tmp`，成功后原子 rename，避免半文件被详情页读取。
- 文件名不直接信任 request_id，必须清洗为安全字符或使用 hash。

### 数据库字段
保留旧 `body` 字段兼容历史记录，但新增磁盘字段：
- `body_storage`：`inline` / `disk`
- `body_path`：相对 storage root 的路径，不存绝对路径给前端
- `body_compressed`：是否 gzip
- `body_bytes`：原始字节数
- `stored_bytes`：磁盘实际字节数
- `body_hash`
- `truncated`
- `expires_at`

安全事件列表只查 DB 元数据；只有打开详情或下载时才读取磁盘文件。

## 非 C 盘约束

### 路径校验
保存路径必须满足：
- Windows 下盘符不能等于系统盘 `%SystemDrive%`，通常是 `C:`。
- 必须是绝对路径。
- 不能在项目目录、前端静态目录、`Temp`、`.git`、`node_modules` 内。
- 目录不存在时允许创建，但创建失败则禁用原文保存并记录告警。
- 路径必须 canonicalize，防止 `..\`、软链接、junction 绕回 C 盘。

### 失败策略
- 总开关关闭：不扫描、不事件、不原文。
- 总开关打开但未配置路径：继续扫描和安全事件，原文回退到数据库 inline 保存。
- 显式配置路径但路径非法或写入失败：继续扫描和安全事件，事件标记 `capture_error`，后续可按策略回退 inline 或停止原文保存。
- 写文件失败：不阻断用户请求；只记录轻量错误计数和最近错误。
- 严格拦截模式：拦截策略不依赖原文保存成功。

## 高流量性能策略

### 实时链路
- 总开关关闭时，完全跳过安全扫描和原文取证。
- 总开关打开时，只扫描前 `MaxScanBytes`，大响应不全量正则扫描。
- 原文保存走磁盘顺序写，避免把大正文塞 DB。
- 流式响应按请求聚合后落盘，避免每个 SSE delta 都写一次 DB 或文件。
- 原文写入可以先同步保证完整性；后续如 QPS 很高，再升级成 bounded queue 异步写。

### 清理链路
- 启动后补清一次，之后每小时清理。
- 先按 `expires_at` 删除 DB 元数据与磁盘文件。
- 再按 `security_capture_max_storage_bytes` 从最旧记录开始删。
- 额外扫描磁盘孤儿文件：文件存在但 DB 无记录，超过 2 小时删除。
- 删除失败不阻塞服务，保留错误计数，下一轮继续。

### 容量控制
- 默认全量保存关闭。
- 命中后保存默认单条 1MB。
- 全量保存只能手动开启，设置页显示“高流量风险”提示。
- 支持磁盘剩余空间低水位，例如小于 5GB 自动停止原文保存。

## 恶意程序 / 脚本行为展示

### 轻量规则
新增 `script_behavior` 类规则，实时识别：
- `curl | sh`、`wget | bash`
- PowerShell `-EncodedCommand`
- `certutil`、`bitsadmin` 下载执行
- 读取 `.env`、SSH key、API key 后上传
- 反连 shell
- 持久化启动项 / cron / systemd
- 删除日志、关闭防护

展示为“疑似恶意脚本行为”，不宣称杀毒结论。

### YARA
不默认进入实时链路。

可选实现：
- 管理员手动对某条 capture 触发 YARA。
- 后台异步低优先级扫描最近命中原文。
- 设置 timeout、文件大小上限、规则集热加载。
- YARA 结果作为补充证据展示，不作为唯一拦截依据。

## 前端交互
- 设置页新增“安全审计总开关”，默认关闭。
- 总开关关闭时，扫描模式、原文保存、脚本规则、YARA 区域置灰。
- 原文保存路径输入框必须显示“不能是 C 盘”。
- 路径检测按钮：显示盘符、可写性、剩余空间、当前目录大小。
- 安全事件详情页：
  - 展示命中规则、字段、片段、行为标签。
  - 原文为磁盘文件时，通过后端 API 按需读取。
  - 文件丢失时显示“原文已清理或路径不可用”，事件证据仍保留。

## API 与后端接口
- `GET /api/admin/settings` 增加总开关和磁盘字段。
- `PUT /api/admin/settings` 校验非 C 盘路径。
- `GET /api/admin/security-captures/:id/body` 按权限读取磁盘正文。
- `GET /api/admin/security-capture-storage/status` 返回路径、可写性、剩余空间、占用。
- `POST /api/admin/security-captures/:id/yara-scan` 可选手动深度扫描。

## 迁移策略
- 旧 DB inline 原文继续可读，不强制迁移。
- 新记录在磁盘存储抽象完成前默认 inline 保存；如果后续配置合法非 C 盘路径，则优先写磁盘。
- 提供一次性迁移命令可选把旧 `body` 导出到非 C 盘，并把 DB 改成 `disk` 引用。
- 迁移完成前不清空旧 body；迁移验证 hash 后才允许清理 DB 大字段。

## 实施阶段

### 阶段 1：配置与总开关
- [x] 新增独立 `security_audit_enabled = false` 总开关，关闭时不扫描、不事件、不原文。
- [x] 修改默认 capture mode 为 `hit_raw`，避免高流量默认全量。
- [x] 未配置非 C 盘存储路径时回退本机数据库 inline 保存原文，保留完整 hash、大小和事件证据。
- [x] 设置页新增独立总开关与置灰逻辑。
- [x] 测试默认关闭时不扫描、不事件；开启扫描但无非 C 盘路径时回退数据库 inline 保存原文。

### 阶段 2：磁盘存储抽象
- 新增 `SecurityCaptureStorage` 接口。
- 实现 `DiskSecurityCaptureStorage`。
- DB 新增路径与压缩字段。
- 详情读取支持 inline / disk 双模式。

### 阶段 3：非 C 盘路径校验
- 实现路径 canonicalize 与系统盘检测。
- 设置接口拒绝 C 盘、项目目录、临时目录。
- 状态接口返回可写性和剩余空间。

### 阶段 4：清理与容量治理
- 清理 DB 过期记录时同步删磁盘文件。
- 总量超限按最旧记录删除。
- 孤儿文件清理。
- 磁盘低空间保护。

### 阶段 5：脚本恶意行为规则
- [x] 增加轻量 `script_behavior` 规则集，识别 `curl|wget` 管道执行、PowerShell EncodedCommand、certutil/bitsadmin 下载执行。
- [x] 安全事件通过规则 ID、字段和命中片段展示疑似脚本行为。
- [x] 加入真阳性/假阳性样本，避免安全教学内容误报。

### 阶段 6：YARA 可选深度扫描
- 只做手动或异步，不进默认实时链路。
- 增加规则目录、编译缓存、timeout、大小上限。
- 结果写入事件扩展证据。

### 阶段 7：验证
- 后端：`go test ./...`
- 前端：`npm run typecheck`、`npm run build`
- 人工验收：
  - 默认关闭时高流量请求不产生安全事件和原文文件。
  - 配置 C 盘路径会被拒绝。
  - 配置 D/E 盘路径后命中事件能落盘并在详情页展示。
  - 清理后详情页能优雅提示原文已清理。
  - 大响应不会写爆 DB。

## 架构洞察
高流量下，真正危险的不是“扫不扫”，而是把每个请求响应都同步塞进数据库。安全审计必须拆成三层：实时轻量判定、磁盘证据留存、异步深度分析。三层共享 request_id 和 hash，但不能互相拖垮。默认关闭总开关是保护业务；非 C 盘落盘是保护系统盘；YARA 异步化是保护实时延迟。

## 2026-06-09 执行记录
- 已完成独立安全审计总开关：数据库、管理接口、Store 运行态、代理扫描入口和设置页均接入 `security_audit_enabled`，默认关闭。
- 总开关关闭时，扫描器不会执行，安全事件和原文记录均不会产生；`upstream_guard_mode` 仅保留为开启后的告警/拦截策略。
- 已增加响应侧轻量脚本行为规则 `script_behavior`，实时链路只做低成本正则，不引入 YARA 默认扫描。
- 已通过目标测试：`go test ./security/upstreamguard ./database ./auth ./admin ./proxy -run "TestInspectResponseFlagsScriptBehavior|TestInspectResponseDoesNotFlagScriptBehaviorDocumentation|TestDefaultSystemSettingsDisableGuardAndUseHitRawOneDayRetention|TestStoreLoadsUpstreamGuardConfigFromSystemSettings|TestStoreDefaultsUpstreamGuardConfigToOffMode|TestSettingsRoutesExposeAndUpdateUpstreamGuardMode|TestUpstreamGuardDisabledSkipsScanEventsAndCaptures"`。
- 已通过全量验证：`go test ./...`、`npm run typecheck`、`npm run build`。
- 已按最新口径重构无非 C 盘路径回退：命中/全量原文回退数据库 inline 保存，`body_hash` 和 `body_bytes` 仍按完整正文计算，`body` 按 `security_capture_max_body_bytes` 截断，正常回退不写 `capture_error`。
- 已修正设置页文案：保留 1 天是数据生命周期；清理任务是启动后立即检查，之后定期检查过期数据，不代表每小时清空。
- 已通过回归验证：新增红绿测试 `TestUpstreamGuardInlineCaptureTruncatesToConfiguredLimit`，并通过 `go test ./proxy ./database ./auth ./admin ./security/upstreamguard -run "UpstreamGuard|SecurityCapture|SettingsRoutesExposeAndUpdateSecurityCaptureConfig|StoreDefaults|NormalizeConfigInvalidCaptureMode|DefaultSystemSettings|UpgradeLegacy|SystemSettingsPersistUpstreamGuardMode"`、`go test ./...`、`npm run typecheck`、`npm run build`。
- 已构建并热替换：备份 `codex2api.prev-20260609-113538.exe`，当前 `codex2api.exe` SHA256 为 `32061C7B0FE66AC3BD710836CD784D8E75254300A5FBCC41547FFC07352E0735`，当前运行进程 PID=1348，`/health` 返回 200。
