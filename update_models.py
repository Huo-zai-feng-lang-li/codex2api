#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
codex2api 模型列表更新脚本
==========================
链路：下游客户端 -> 本地 codex2api(127.0.0.1:18080) -> 上游 responses_api 号池

两步：
  1. 同步官方目录    POST /api/admin/models/sync；官方页 403/超时则保留本地并警告
  2. 更新号池模型    逐个 responses_api 账号拉取上游 /v1/models 并保存（默认只增不删）

Codex 桌面端已卸载，脚本不再执行 `winget upgrade Codex` 与 `app-server` 重启；
旧参数 --skip-update / --skip-restart 仍被接受，仅作兼容占位。

退出码：存在硬性失败 -> 1；仅警告 -> 0。日志在 logs/update-models-<时间戳>.log。

常用参数：
  --skip-sync / --skip-refresh
  --prune       号池精确同步（删除上游本次未列出的模型）；默认只增不删
  --dry-run     号池只拉取、比对，不写入
  --keep-all    含危险字符的模型也原样提交（默认跳过并记录）
"""
import argparse
import json
import os
import re
import sys
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

# 步骤三态
OK, WARN, FAIL = "ok", "warn", "fail"


# ----------------------------- 基础工具 -----------------------------
class Logger:
    def __init__(self):
        os.makedirs(os.path.join(ROOT, "logs"), exist_ok=True)
        stamp = datetime.now().strftime("%Y%m%d-%H%M%S")
        self.path = os.path.join(ROOT, "logs", f"update-models-{stamp}.log")
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


# ----------------------------- 步骤 1：同步官方目录 -----------------------------
def step_sync_models(client, lg):
    lg.log("[1/2] 同步官方模型目录 (POST /api/admin/models/sync) ...")
    try:
        resp = client.call("POST", "/api/admin/models/sync", {}, timeout=60)
        lg.log("    [OK] " + json.dumps(resp, ensure_ascii=False)[:300])
        return OK
    except Exception as e:
        if is_temporary(e) or "403" in str(e):
            lg.log(f"    [警告] 官方模型页暂时不可用，已保留本地模型列表（{e}）")
            return WARN
        lg.log(f"    [失败] sync: {e}")
        return FAIL


# ----------------------------- 步骤 2：号池更新 -----------------------------
def step_refresh_pool(client, lg, keep_all, dry_run, prune):
    label = "[2/2] 号池模型更新（dry-run，不写入）" if dry_run else "[2/2] 号池模型更新"
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
    targets = [a for a in accounts
               if a.get("openai_responses_api") and a.get("enabled", True)
               and str(a.get("status") or "").lower() != "deleted"]
    lg.log(f"    发现 {len(targets)} 个 responses_api 账号")

    n_ok = n_retry = n_fail = 0
    added_all = []
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

            if not dry_run and (added or removed):
                patch = {"name": name, "base_url": base_url, "api_key": "",
                         "models": final, "proxy_url": proxy_url}
                client.call("PATCH", f"/api/admin/accounts/{aid}/openai-responses",
                            patch, timeout=30)
            n_ok += 1
            added_all.extend(added)
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

    unique_added = sorted({m for m in added_all}, key=str.lower)
    lg.log("    ---- 号池汇总 ----")
    lg.log(f"    成功 {n_ok} / 待重试 {n_retry} / 失败 {n_fail}")
    if unique_added:
        lg.log(f"    本次新增模型 {len(unique_added)} 个：{', '.join(unique_added)}")
    if n_fail:
        return FAIL
    if n_retry:
        lg.log("    [警告] 部分账号因上游限流/额度暂时未刷新，额度恢复后重跑本脚本即可")
        return WARN
    return OK


# ----------------------------- 主流程 -----------------------------
def main():
    ap = argparse.ArgumentParser(
        description="codex2api 模型列表更新：同步官方目录 + 号池模型拉取（不再处理 Codex 客户端）")
    ap.add_argument("--skip-sync", action="store_true")
    ap.add_argument("--skip-refresh", action="store_true")
    ap.add_argument("--prune", action="store_true")
    ap.add_argument("--dry-run", action="store_true")
    ap.add_argument("--keep-all", action="store_true")
    # 兼容旧脚本参数：Codex 相关步骤已移除，这里仅接受不报错
    ap.add_argument("--skip-update", action="store_true", help=argparse.SUPPRESS)
    ap.add_argument("--skip-restart", action="store_true", help=argparse.SUPPRESS)
    args = ap.parse_args()

    lg = Logger()
    lg.log("=" * 60)
    lg.log("codex2api 模型列表更新  " + datetime.now().strftime("%Y-%m-%d %H:%M:%S"))
    lg.log("项目根: " + ROOT)
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
    if not args.skip_sync:
        results["同步官方目录"] = step_sync_models(client, lg)
    if not args.skip_refresh:
        results["号池模型更新"] = step_refresh_pool(
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
