"""
====== 批量添加账号提示词（直接复制本段发送给 AI 即可） ======

添加账号-API Key-账号名称填写https://api.daseinai.xyz-13递增，Base URL统一填写https://api.daseinai.xyz

下面是key：1. 
==============================================================
"""

import os
import sys
import json
import sqlite3
import urllib.request
import urllib.error
import argparse

# 默认配置
DEFAULT_DB_PATH = "data/codex2api.db"
DEFAULT_INPUT_FILE = "accounts_to_add.json"
BASE_URL = "https://kaiycb.com"
API_HOST = "http://127.0.0.1:18080"


def get_admin_secret(db_path):
    """自动从 SQLite 数据库提取管理密钥，避免硬编码"""
    if not os.path.exists(db_path):
        print(f"[Error] Database path not found: {db_path}")
        return None
    try:
        conn = sqlite3.connect(db_path)
        cur = conn.cursor()
        cur.execute("SELECT admin_secret FROM system_settings LIMIT 1")
        row = cur.fetchone()
        conn.close()
        if row and row[0]:
            return row[0]
        print("[Warning] No admin_secret found in database, check bootstrap settings.")
        return None
    except Exception as e:
        print(f"[Error] Failed to read database: {e}")
        return None


def make_request(url, admin_secret, data_dict):
    """通用 API 请求客户端"""
    payload = json.dumps(data_dict).encode("utf-8")
    req = urllib.request.Request(
        url,
        data=payload,
        headers={
            "X-Admin-Key": admin_secret,
            "Content-Type": "application/json"
        },
        method="POST"
    )
    try:
        with urllib.request.urlopen(req, timeout=30) as f:
            res = f.read().decode("utf-8")
            return json.loads(res), None
    except urllib.error.HTTPError as e:
        err_msg = e.read().decode("utf-8")
        try:
            err_json = json.loads(err_msg)
            return None, err_json.get("error", err_msg)
        except Exception:
            return None, err_msg
    except Exception as e:
        return None, str(e)


def process_batch(input_path, db_path, base_url, api_host):
    """批量添加流程主函数"""
    # 1. 读取账户队列数据
    if not os.path.exists(input_path):
        print(f"[Error] Input data file not found: {input_path}")
        return False

    try:
        with open(input_path, "r", encoding="utf-8") as f:
            accounts_to_add = json.load(f)
    except Exception as e:
        print(f"[Error] Failed to parse input JSON: {e}")
        return False

    if not isinstance(accounts_to_add, list):
        print("[Error] Input data must be a list of accounts.")
        return False

    # 2. 自动拉取系统密钥
    admin_secret = get_admin_secret(db_path)
    if not admin_secret:
        return False

    print(f"\nLoaded {len(accounts_to_add)} accounts to process.")
    print("Initializing batch account injection...")

    success_count = 0
    failed_keys = []

    # 3. 递推处理
    for idx, acc in enumerate(accounts_to_add, 1):
        name = acc.get("name")
        api_key = acc.get("api_key")
        curr_base_url = acc.get("base_url", base_url)
        proxy_url = acc.get("proxy_url", "")

        if not name or not api_key:
            print(f"  [{idx}] [Skip] Account missing 'name' or 'api_key'")
            continue

        print(f"\n[{idx}/{len(accounts_to_add)}] Processing: {name}")

        # Step 1: 拉取模型列表
        print(f"  -> Pulling model list from {curr_base_url}...")
        models_url = f"{api_host}/api/admin/accounts/openai-responses/models"
        models_data = {
            "base_url": curr_base_url,
            "api_key": api_key,
            "proxy_url": proxy_url
        }
        models_res, err = make_request(models_url, admin_secret, models_data)

        if err:
            print(f"  [Error] Failed to fetch models for {name}: {err}")
            failed_keys.append((name, f"Fetch models fail: {err}"))
            continue

        models = models_res.get("models", [])
        if not models:
            print(f"  [Warning] Fetched models list is empty for {name}!")
            failed_keys.append((name, "No models returned"))
            continue

        print(f"  -> Successfully fetched {len(models)} models.")

        # Step 2: 保存账号
        print(f"  -> Inserting account into system...")
        add_url = f"{api_host}/api/admin/accounts/openai-responses"
        add_data = {
            "name": name,
            "base_url": curr_base_url,
            "api_key": api_key,
            "models": models,
            "proxy_url": proxy_url,
            "tags": acc.get("tags", [])
        }

        add_res, add_err = make_request(add_url, admin_secret, add_data)
        if add_err:
            if "已存在" in str(add_err) or "exists" in str(add_err).lower():
                print(f"  [Info] Account {name} already exists (skipped).")
                success_count += 1
            else:
                print(f"  [Error] Failed to save {name}: {add_err}")
                failed_keys.append((name, f"Save fail: {add_err}"))
        else:
            print(f"  [Success] Account {name} added successfully! ID: {add_res.get('id')}")
            success_count += 1

    print("\n================== Batch Result Summary ==================")
    print(f"Success/Exist: {success_count} / {len(accounts_to_add)}")
    print(f"Failed: {len(failed_keys)}")
    if failed_keys:
        for name, reason in failed_keys:
            print(f"  - {name}: {reason}")
        return False
    print("All tasks executed successfully.")
    return True


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="Codex2api Batch Account Injected Script")
    parser.add_argument("--input", default=DEFAULT_INPUT_FILE, help="Path to input accounts JSON file")
    parser.add_argument("--db", default=DEFAULT_DB_PATH, help="Path to codex2api.db")
    parser.add_argument("--base-url", default=BASE_URL, help="Fallback Base URL if not specified in JSON")
    parser.add_argument("--api-host", default=API_HOST, help="API Host Address")
    args = parser.parse_args()

    success = process_batch(args.input, args.db, args.base_url, args.api_host)
    sys.exit(0 if success else 1)
