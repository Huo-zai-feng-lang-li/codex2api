#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
Codex 一键更新脚本
==================
链路：Codex 桌面端 -> 本地 codex2api(127.0.0.1:18080) -> 上游 responses_api 号池

默认依次执行：
  1. 更新 Codex        商店版优先 winget，回退 codex update；商店本就自动更新，失败仅警告
  2. 同步模型          POST /models/sync 拉官方目录；官方页 403/超时则保留本地并警告
  3. 重启 app-server   非提升终端用 daemon restart；提升终端改为重启桌面端（连带 app-server）
  4. 号池更新          逐个 responses_api 账号拉取上游 /v1/models 并保存（默认只增不删）

退出码：存在硬性失败 -> 1；仅警告 -> 0。日志在 logs/update-<时间戳>.log。

常用参数：
  --skip-update / --skip-sync / --skip-restart / --skip-refresh
  --prune       号池精确同步（删除上游本次未列出的模型）；默认只增不删
  --dry-run     号池只拉取、比对，不写入
  --keep-all    含危险字符的模型也原样提交（默认跳过并记录）
"""
import argparse
import ctypes
import json
import os
import re
import sys
import subprocess
import time
import unicodedata
import urllib.request
import urllib.error
from datetime import datetime

try:
    sys.stdout.reconfigure(encoding="utf-8")
    sys.stderr.reconfigure(encoding="utf-8")
except Exception:
    pass

ROOT = os.path.dirname(os.path.abspath(__file__))

# 商店包信息
STORE_PRODUCT_ID = "9PLM9XGG6VKS"
CODEX_AUMID = "OpenAI.Codex_2p2nqsd0c76g0!App"

# 步骤三态
OK, WARN, FAIL = "ok", "warn", "fail"


# ----------------------------- 基础工具 -----------------------------
class Logger:
    def __init__(self):
        os.makedirs(os.path.join(ROOT, "logs"), exist_ok=True)
        stamp = datetime.now().strftime("%Y%m%d-%H%M%S")
        self.path = os.path.join(ROOT, "logs", f"update-{stamp}.log")
        self.fp = open(self.path, "w", encoding="utf-8")

    def log(self, msg=""):
        line = str(msg)
        print(line)
        self.fp.write(line + "\n")
        self.fp.flush()

    def close(self):
        try:
            self.fp.close()
        except Exception:
            pass


def load_env():
    env = {}
    p = os.path.join(ROOT, ".env")
    if os.path.isfile(p):
        with open(p, encoding="utf-8") as f:
            for raw in f:
                line = raw.strip()
                if line and not line.startswith("#") and "=" in line:
                    k, v = line.split("=", 1)
                    env[k.strip()] = v.strip()
    return env


def find_codex_exe():
    """返回 bin 下修改时间最新的 codex.exe（更新后可能出现新的 hash 目录）。"""
    root = os.path.join(os.environ.get("LOCALAPPDATA", ""), "OpenAI", "Codex", "bin")
    candidates = []
    if os.path.isdir(root):
        for d in os.listdir(root):
            p = os.path.join(root, d, "codex.exe")
            if os.path.isfile(p):
                candidates.append((os.path.getmtime(p), p))
    if candidates:
        candidates.sort(reverse=True)
        return candidates[0][1]
    return None


def is_elevated():
    try:
        return ctypes.windll.shell32.IsUserAnAdmin() != 0
    except Exception:
        return False


# ----------------------------- HTTP -----------------------------
class AdminClient:
    def __init__(self, base, secret):
        self.base = base
        self.secret = secret

    def call(self, method, path, body=None, timeout=30):
        url = self.base + path
        data = json.dumps(body).encode("utf-8") if body is not None else None
        req = urllib.request.Request(url, data=data, method=method)
        req.add_header("X-Admin-Key", self.secret)
        if data is not None:
            req.add_header("Content-Type", "application/json")
        try:
            with urllib.request.urlopen(req, timeout=timeout) as r:
                raw = r.read().decode("utf-8")
                return json.loads(raw) if raw else {}
        except urllib.error.HTTPError as e:
            detail = ""
            try:
                detail = e.read().decode("utf-8")[:300]
            except Exception:
                pass
            raise RuntimeError(f"HTTP {e.code} {e.reason} {detail}")

    def health(self, timeout=5):
        try:
            with urllib.request.urlopen(self.base + "/health", timeout=timeout) as r:
                return json.loads(r.read().decode("utf-8"))
        except Exception:
            return None


# ----------------------------- 模型清洗 -----------------------------
_FORBIDDEN_CHARS = set('"\'`\\<>')
_TEMP_PATTERN = re.compile(
    r"429|403|408|500|502|503|504|529|limit|rate|timeout|超时|overload|capacity|unavailable|额度|try again|busy",
    re.IGNORECASE)


def is_forbidden_model(model):
    for ch in model:
        if ch in _FORBIDDEN_CHARS or ch.isspace():
            return True
        if unicodedata.category(ch).startswith("C"):
            return True
    return False


def is_temporary(msg):
    return bool(_TEMP_PATTERN.search(str(msg)))


def sanitize_models(models, keep_all):
    seen, clean, skipped = set(), [], []
    for m in models or []:
        m = m.strip()
        if not m:
            continue
        key = m.lower()
        if key in seen:
            continue
        if is_forbidden_model(m) and not keep_all:
            skipped.append(m)
            continue
        seen.add(key)
        clean.append(m)
    return clean, skipped


def union_models(old, new):
    """合并去重，保留 old 全部 + new 新增（只增不删）。"""
    seen, final = set(), []
    for m in list(old) + list(new):
        m = m.strip()
        if not m:
            continue
        key = m.lower()
        if key in seen:
            continue
        seen.add(key)
        final.append(m)
    return final


# ----------------------------- 命令执行 -----------------------------
def run_command(args, timeout, lg):
    lg.log("    $ " + " ".join(f'"{a}"' if " " in a else a for a in args))
    try:
        proc = subprocess.run(args, capture_output=True, text=True,
                              encoding="utf-8", errors="replace", timeout=timeout)
        out = ((proc.stdout or "") + (proc.stderr or "")).strip()
        for line in out.splitlines():
            lg.log("    | " + line)
        return proc.returncode == 0, out
    except subprocess.TimeoutExpired:
        return False, f"超时（>{timeout}s）"
    except Exception as e:
        return False, str(e)


def process_exists(proc_name, lg):
    ps = f"if (Get-Process -Name '{proc_name}' -ErrorAction SilentlyContinue) {{exit 0}} else {{exit 1}}"
    ok, _ = run_command(["powershell", "-NoProfile", "-Command", ps], 20, lg)
    return ok


# ----------------------------- 步骤 1：更新 -----------------------------
def step_codex_update(lg):
    lg.log("[1/4] 更新 Codex ...")
    exe = find_codex_exe()
    common = ["--accept-source-agreements", "--accept-package-agreements",
              "--disable-interactivity", "--silent"]
    strategies = [
        ["winget", "upgrade", "-e", "--id", STORE_PRODUCT_ID, "--source", "msstore"] + common,
        ["winget", "upgrade", "-e", "--id", STORE_PRODUCT_ID] + common,
    ]
    if exe:
        strategies.append([exe, "update"])

    for cmd in strategies:
        ok, out = run_command(cmd, 300, lg)
        low = out.lower()
        if ok:
            lg.log("    [OK] Codex 已更新 / 已是最新")
            return OK
        if re.search(r"no available upgrade|no applicable|already|latest|已安装|最新|没有可用", low):
            lg.log("    [OK] 已是最新版本，无需更新")
            return OK
    lg.log("    [警告] 自动更新未成功。商店应用通常由 Microsoft Store 自动更新；")
    lg.log("           也可打开『Microsoft Store → 库 → 获取更新』手动检查。")
    return WARN


# ----------------------------- 步骤 2：同步模型 -----------------------------
def step_sync_models(client, lg):
    lg.log("[2/4] 同步官方模型 (POST /models/sync) ...")
    try:
        resp = client.call("POST", "/api/admin/models/sync", {}, timeout=60)
        lg.log("    [OK] " + json.dumps(resp, ensure_ascii=False)[:300])
        return OK
    except Exception as e:
        if is_temporary(e):
            lg.log(f"    [警告] 官方模型页暂时不可用，已保留本地模型列表（{e}）")
            return WARN
        lg.log(f"    [失败] sync: {e}")
        return FAIL


# ----------------------------- 步骤 3：重启 app-server -----------------------------
def kill_appserver(lg):
    ps = ("Get-CimInstance Win32_Process -Filter \"Name='codex.exe'\" | "
          "Where-Object { $_.CommandLine -match 'app-server' } | "
          "ForEach-Object { Stop-Process -Id $_.ProcessId -Force }")
    run_command(["powershell", "-NoProfile", "-Command", ps], 30, lg)


def restart_desktop(lg):
    lg.log("    通过重启桌面端来重启 app-server ...")
    run_command(["taskkill", "/F", "/IM", "ChatGPT.exe"], 30, lg)
    kill_appserver(lg)
    time.sleep(3)
    run_command(["explorer.exe", f"shell:AppsFolder\\{CODEX_AUMID}"], 30, lg)
    time.sleep(6)
    if process_exists("ChatGPT", lg):
        lg.log("    [OK] 桌面端已重启，app-server 已随之重新拉起")
        return OK
    lg.log("    [警告] 已发出重启指令，但未在限定时间内检测到桌面端，请手动确认")
    return WARN


def step_restart_appserver(lg, elevated):
    lg.log("[3/4] 重启 app-server ...")
    if not elevated:
        exe = find_codex_exe()
        if exe:
            ok, _ = run_command([exe, "app-server", "daemon", "restart"], 90, lg)
            if ok:
                lg.log("    [OK] app-server daemon 已重启")
                return OK
            lg.log("    daemon restart 未成功，改为重启桌面端 ...")
    else:
        lg.log("    当前为管理员(elevated)终端，daemon 出于安全拒绝在此启动；改为重启桌面端 ...")
    return restart_desktop(lg)


# ----------------------------- 步骤 4：号池更新 -----------------------------
def step_refresh_pool(client, lg, keep_all, dry_run, prune):
    label = "[4/4] 号池更新（dry-run，不写入）" if dry_run else "[4/4] 号池更新"
    mode = "精确同步(prune)" if prune else "只增不删(merge)"
    lg.log(f"{label}  模式：{mode} ...")
    try:
        resp = client.call("GET", "/api/admin/accounts")
    except Exception as e:
        lg.log(f"    [失败] 无法获取账号列表: {e}")
        return FAIL

    if isinstance(resp, dict):
        accounts = resp.get("accounts") or resp.get("data") or []
    elif isinstance(resp, list):
        accounts = resp
    else:
        accounts = []
    targets = [a for a in accounts if a.get("openai_responses_api")]
    lg.log(f"    发现 {len(targets)} 个 responses_api 账号")

    n_ok = n_retry = n_fail = 0
    for a in targets:
        aid = a.get("id")
        name = a.get("name") or f"ID {aid}"
        old = a.get("models") or []
        base_url = (a.get("base_url") or "").strip()
        proxy_url = a.get("proxy_url") or ""
        try:
            fetched = client.call(
                "POST", "/api/admin/accounts/openai-responses/models",
                {"account_id": aid}, timeout=30)
            upstream = fetched.get("models") or []
            clean, skipped = sanitize_models(upstream, keep_all)
            if not clean:
                raise RuntimeError("上游未返回任何可用模型")
            if not base_url:
                raise RuntimeError("账号缺少 base_url，无法保存")

            if prune:
                final = clean
                removed = [m for m in old if m.lower() not in {x.lower() for x in clean}]
            else:
                final = union_models(old, clean)
                removed = []
            added = [m for m in final if m.lower() not in {x.lower() for x in old}]

            if not dry_run:
                patch = {"name": name, "base_url": base_url, "api_key": "",
                         "models": final, "proxy_url": proxy_url}
                client.call("PATCH", f"/api/admin/accounts/{aid}/openai-responses",
                            patch, timeout=30)
            n_ok += 1
            lg.log(f"    [{aid}] {name}: {len(old)} -> {len(final)} "
                   f"新增{len(added)} 移除{len(removed)} 跳过{len(skipped)} [OK]")
            for m in added:
                lg.log(f"        + {m}")
            for m in removed:
                lg.log(f"        - {m}")
            for m in skipped:
                lg.log(f"        ? 跳过(危险字符): {m}")
        except Exception as e:
            if is_temporary(e):
                n_retry += 1
                lg.log(f"    [{aid}] {name}: [待重试] {e}（保留原有 {len(old)} 个模型，稍后重跑）")
            else:
                n_fail += 1
                lg.log(f"    [{aid}] {name}: [失败] {e}")

    lg.log("    ---- 号池汇总 ----")
    lg.log(f"    成功 {n_ok} / 待重试 {n_retry} / 失败 {n_fail}")
    if n_fail:
        return FAIL
    if n_retry:
        lg.log("    [警告] 部分账号因上游限流/额度暂时未刷新，额度恢复后重跑本脚本即可")
        return WARN
    return OK


# ----------------------------- 主流程 -----------------------------
def main():
    ap = argparse.ArgumentParser(
        description="Codex 一键更新：更新 Codex + 同步模型 + 重启 app-server + 号池拉取")
    ap.add_argument("--skip-update", action="store_true")
    ap.add_argument("--skip-sync", action="store_true")
    ap.add_argument("--skip-restart", action="store_true")
    ap.add_argument("--skip-refresh", action="store_true")
    ap.add_argument("--prune", action="store_true")
    ap.add_argument("--dry-run", action="store_true")
    ap.add_argument("--keep-all", action="store_true")
    args = ap.parse_args()

    lg = Logger()
    lg.log("=" * 60)
    lg.log("Codex 一键更新  " + datetime.now().strftime("%Y-%m-%d %H:%M:%S"))
    lg.log("项目根: " + ROOT)
    elevated = is_elevated()
    lg.log("权限:   " + ("管理员(elevated)" if elevated else "普通用户"))
    lg.log("日志:   " + lg.path)
    lg.log("=" * 60)

    env = load_env()
    port = env.get("CODEX_PORT", "18080")
    secret = env.get("ADMIN_SECRET", "")
    client = AdminClient(f"http://127.0.0.1:{port}", secret)

    if not secret:
        lg.log("[警告] .env 未配置 ADMIN_SECRET，管理接口调用将失败")

    health = client.health()
    if not health or health.get("status") != "ok":
        lg.log(f"[失败] codex2api 未在 127.0.0.1:{port} 正常运行（/health 异常）。")
        lg.log("       请先启动服务（双击 start-codex2api.bat）后再运行。")
        lg.close()
        return 1
    lg.log(f"[预检] codex2api 健康 OK，端口 {port}")

    results = {}
    if not args.skip_update:
        results["更新 Codex"] = step_codex_update(lg)
    if not args.skip_sync:
        results["同步模型"] = step_sync_models(client, lg)
    if not args.skip_restart:
        results["重启 app-server"] = step_restart_appserver(lg, elevated)
    if not args.skip_refresh:
        results["号池更新"] = step_refresh_pool(
            client, lg, args.keep_all, args.dry_run, args.prune)

    lg.log("=" * 60)
    lg.log("执行结果：")
    icons = {OK: "[OK]  ", WARN: "[警告]", FAIL: "[失败]"}
    for k, v in results.items():
        lg.log(f"  {icons[v]} {k}")
    lg.log("=" * 60)
    lg.close()

    hard = [k for k, v in results.items() if v == FAIL]
    warns = [k for k, v in results.items() if v == WARN]
    if hard:
        print("存在失败步骤: " + "、".join(hard))
        print(f"详见日志: {lg.path}")
        return 1
    if warns:
        print("已完成，但有警告: " + "、".join(warns))
        print(f"详见日志: {lg.path}")
        return 0
    print("全部完成。日志: " + lg.path)
    return 0


if __name__ == "__main__":
    sys.exit(main())
