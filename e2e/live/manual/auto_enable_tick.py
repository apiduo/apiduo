#!/usr/bin/env python3
"""手动验证「自动启用」的两条路径 —— 不进 CI。

    E2E_BASE=http://127.0.0.1:39300 python3 e2e/live/manual/auto_enable_tick.py

为什么单独放：定时那条要等 scheduler（15s 一轮）+ Interval（默认 1 分钟），
实测 140s 才收敛；而且它依赖一个**活着**的假上游——假上游掉了会先把所有渠道
判成自动禁用，后面每一步都假红。这种前置条件不适合塞进 CI。

判据不看日志，看 system_tasks.result 的 JSON：
    {"tested":2,"succeeded":2,"failed":0,"disabled":0,"enabled":1}
`enabled` 计数就是 ShouldEnableChannel 真的返回 true 的次数。

前提：实例已 bootstrap（有管理员），假上游在 E2E_UP1 上；E2E_DEAD 指向一个
**没有监听**的端口（连接会被立刻拒绝，测试判定快）。
"""

import json
import subprocess
import time
import urllib.error
import urllib.request

import os

BASE = os.environ.get("E2E_BASE", "http://127.0.0.1:39300")
ADMIN = (os.environ.get("E2E_ADMIN", "root"), os.environ.get("E2E_ADMIN_PW", "E2ePassw0rd!2026"))
UP = os.environ.get("E2E_UP1", "http://127.0.0.1:39401")
DEAD = os.environ.get("E2E_DEAD", "http://127.0.0.1:39497")
NAME = "manual-tick-recover"
MODEL = os.environ.get("E2E_MODEL", "gpt-4o-mini")
# system_tasks 表的 result 是最直接的证据；目录跟 dev-e2e 的数据目录一致
DB = os.environ.get("E2E_DB", "/tmp/apiduo-dev-e2e/gw/one-api.db")

OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))
FAILS = []


def call(method, path, body=None, token=None):
    data = json.dumps(body).encode() if body is not None else None
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    req = urllib.request.Request(BASE + path, data=data, headers=headers, method=method)
    try:
        with OPENER.open(req, timeout=60) as resp:
            return resp.status, json.loads(resp.read() or b"{}")
    except urllib.error.HTTPError as err:
        raw = err.read()
        try:
            return err.code, json.loads(raw or b"{}")
        except Exception:
            return err.code, raw.decode("utf-8", "replace")


def db(sql):
    return subprocess.run(["sqlite3", DB, sql], capture_output=True, text=True).stdout.strip()


def stamp():
    return time.strftime("%H:%M:%S")


def channels(jwt):
    st, res = call("GET", "/api/channel/?p=0&page_size=100", None, jwt)
    return (res.get("data") or {}).get("items") or []


def ch(jwt):
    return next((c for c in channels(jwt) if c.get("name") == NAME), None)


def check(name, cond, detail=""):
    print(("  PASS  " if cond else "  FAIL  ") + name + (f"  [{detail}]" if detail else ""))
    if not cond:
        FAILS.append(name)


def last_result():
    return db("select substr(coalesce(result,''),1,90) from system_tasks order by id desc limit 1;")


def wait_status(jwt, want, timeout):
    t0 = time.time()
    while time.time() - t0 < timeout:
        cur = (ch(jwt) or {}).get("status")
        if cur == want:
            return cur, time.time() - t0
        time.sleep(1)
    return (ch(jwt) or {}).get("status"), time.time() - t0


def main():
    st, res = call("POST", "/api/user/login", {"username": ADMIN[0], "password": ADMIN[1]})
    jwt = res["data"]["access_token"]

    call("PATCH", "/api/option/request_policy", {"options": {
        "monitor_setting.auto_test_channel_enabled": "false",
        "AutomaticDisableChannelEnabled": "true",
        "AutomaticDisableStatusCodes": "500",
        "AutomaticEnableChannelEnabled": "true",
        "RetryTimes": "0"}}, jwt)

    cur = ch(jwt)
    if not cur:
        raise SystemExit("dev-iso-dead 不在了，先跑 iso_disable.py")
    check("起点是自动禁用（3）", cur.get("status") == 3, f"status={cur.get('status')}")

    # ---------- 阶段 A：手改 base_url + 手动批量测试 ----------
    st, res = call("PUT", "/api/channel/", {"id": cur["id"], "base_url": UP}, jwt)
    check("base_url 改到活上游", st == 200 and res.get("success"), f"HTTP {st} {str(res)[:80]}")
    time.sleep(2)
    after = ch(jwt)
    check("改完 base_url 已落库", after.get("base_url") == UP, str(after.get("base_url")))
    check("改完状态仍是 3", after.get("status") == 3, f"status={after.get('status')}")

    call("GET", "/api/channel/test", None, jwt)
    got, elapsed = wait_status(jwt, 1, 45)
    print(f"  ....  result={last_result()}")
    check(f"手动批量测试把渠道拉回启用（{elapsed:.0f}s）", got == 1, f"status={got}")

    # ---------- 阶段 B：弄回 3，交给定时任务 ----------
    call("PUT", "/api/channel/", {"id": cur["id"], "base_url": DEAD}, jwt)
    time.sleep(2)
    call("GET", "/api/channel/test", None, jwt)
    got, _ = wait_status(jwt, 3, 45)
    check("改回死上游后再次被禁用（3）", got == 3, f"status={got}")

    call("PUT", "/api/channel/", {"id": cur["id"], "base_url": UP}, jwt)
    time.sleep(2)
    opts = call("PATCH", "/api/option/request_policy", {"options": {
        "monitor_setting.auto_test_channel_enabled": "true",
        "monitor_setting.auto_test_channel_minutes": "1",
        "monitor_setting.channel_test_mode": "scheduled_all"}}, jwt)[1]
    got_opts = (opts.get("data") or {}).get("options") or {}
    check("定时任务开关已开", got_opts.get("monitor_setting.auto_test_channel_enabled") == "true",
          str(got_opts.get("monitor_setting.auto_test_channel_enabled")))

    print("  ....  等 scheduler（15s 一轮，间隔 1 分钟）建任务并执行")
    got, elapsed = wait_status(jwt, 1, 180)
    print(f"  ....  result={last_result()}")
    check(f"定时任务把渠道拉回启用（{elapsed:.0f}s）", got == 1, f"status={got}")

    call("PATCH", "/api/option/request_policy", {"options": {
        "monitor_setting.auto_test_channel_enabled": "false",
        "AutomaticDisableChannelEnabled": "false",
        "RetryTimes": "0"}}, jwt)
    call("DELETE", f"/api/channel/{cur['id']}", None, jwt)

    print(f"\n=== {len(FAILS)} 项失败 ===")
    for f in FAILS:
        print("  -", f)
    return 1 if FAILS else 0


if __name__ == "__main__":
    raise SystemExit(main())
