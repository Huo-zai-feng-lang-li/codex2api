import sqlite3
import json
import sys
from datetime import datetime, timezone, timedelta

# Ensure UTF-8 output
sys.stdout.reconfigure(encoding='utf-8')

conn = sqlite3.connect('data/codex2api.db')
conn.row_factory = sqlite3.Row
cur = conn.cursor()

# 1. Print account details for id=825 or name contains suanzhou
print("=== ACCOUNT DETAILS ===")
accs = cur.execute("SELECT * FROM accounts WHERE id = 825 OR name LIKE '%suanzhou%'").fetchall()
for acc in accs:
    d = dict(acc)
    if isinstance(d.get('credentials'), bytes):
        try:
            d['credentials'] = d['credentials'].decode('utf-8')
        except:
            d['credentials'] = str(d['credentials'])
    print(json.dumps(d, ensure_ascii=False, indent=2))

# 2. Inspect usage_logs schema
print("\n=== USAGE_LOGS SCHEMA ===")
cols = cur.execute("PRAGMA table_info(usage_logs)").fetchall()
for c in cols:
    print(dict(c))

# 3. Check latest records in usage_logs to determine time format
print("\n=== LATEST USAGE_LOGS RECORDS ===")
latest_logs = cur.execute("SELECT * FROM usage_logs ORDER BY id DESC LIMIT 5").fetchall()
for l in latest_logs:
    print(dict(l))

# 4. Check latest records for account_id = 825
print("\n=== LATEST USAGE_LOGS FOR ACCOUNT 825 ===")
acc_logs = cur.execute("SELECT * FROM usage_logs WHERE account_id = 825 ORDER BY id DESC LIMIT 5").fetchall()
print(f"Total rows for account 825: {cur.execute('SELECT COUNT(*) FROM usage_logs WHERE account_id = 825').fetchone()[0]}")
for l in acc_logs:
    print(dict(l))

