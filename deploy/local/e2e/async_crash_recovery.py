#!/usr/bin/env python3
"""进程被杀后的自愈验证（约 7 分钟，零成本）。

SIGKILL 应用 → 任务行停在 IN_PROGRESS → 重启 → reaper 在超时窗口后判失败并退款。
断言三条：任务行到 FAILURE、原因是回收、用户额度回到提交前的值。

    python3 deploy/local/e2e/async_crash_recovery.py
"""

import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request

BASE = "http://127.0.0.1:39047"
TOKEN = os.environ.get("APIDUO_TOKEN", "")


def token():
    """本地测试令牌：优先环境变量，其次从本地库取，不把密钥写进仓库。"""
    global TOKEN
    if not TOKEN:
        rows = db("SELECT `key` FROM tokens WHERE status=1 ORDER BY id LIMIT 1;")
        TOKEN = rows[0][0] if rows else ""
    return TOKEN


def db(sql):
    password = subprocess.run(
        ["grep", "^MYSQL_ROOT_PASSWORD=", "deploy/local/.env"],
        capture_output=True, text=True,
    ).stdout.strip().split("=", 1)[1]
    out = subprocess.run(
        ["docker", "exec", "-i", "apiduo-local-mysql", "mysql", "-uroot", f"-p{password}",
         "-N", "--batch", "apiduo_local"],
        input=sql, capture_output=True, text=True,
    )
    return [line.split("\t") for line in out.stdout.splitlines() if line]


def call(method, path, body=None, timeout=60):
    data = None if body is None else json.dumps(body).encode()
    request = urllib.request.Request(BASE + path, data=data, method=method)
    request.add_header("Authorization", "Bearer " + token())
    if data is not None:
        request.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            return response.status, json.loads(response.read() or b"{}")
    except urllib.error.HTTPError as error:
        return error.code, json.loads(error.read() or b"{}")


def main():
    quota_before = db("SELECT quota FROM users WHERE id=1;")[0][0]

    status, body = call("POST", "/async/v1/images/generations",
                        {"model": "slow-test", "prompt": "crash recovery"})
    task_id = body.get("task_id")
    if status != 202 or not task_id:
        print(f"submit failed: {status} {body}")
        return 1
    print(f"submitted {task_id}")

    time.sleep(4)
    subprocess.run(["docker", "kill", "apiduo-local-app"], capture_output=True)
    row = db(f"SELECT status FROM tasks WHERE task_id='{task_id}';")
    print(f"after SIGKILL: {row}")
    killed_cleanly = row and row[0][0] == "IN_PROGRESS"

    subprocess.run(["docker", "start", "apiduo-local-app"], capture_output=True)
    time.sleep(15)
    print("restarted")

    deadline = time.time() + 480
    final = None
    while time.time() < deadline:
        row = db(f"SELECT status, left(fail_reason, 60) FROM tasks WHERE task_id='{task_id}';")
        if row and row[0][0] in ("FAILURE", "SUCCESS"):
            final = row[0]
            break
        time.sleep(20)

    quota_after = db("SELECT quota FROM users WHERE id=1;")[0][0]
    print(f"final: {final}")
    print(f"quota before/after: {quota_before} {quota_after}")

    ok = killed_cleanly
    ok &= final is not None and final[0] == "FAILURE"
    ok &= "stopped before it finished" in (final[1] if final else "")
    ok &= quota_before == quota_after
    print("PASS" if ok else "FAIL")
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
