#!/usr/bin/env python3
"""给一个全新的 e2e 实例做初始化，并把中继 key 打到 stdout 最后一行。

为什么需要它：套件跑的是真网关，但它默认自己只是「发请求的人」——建管理员、
放开磁盘水位、关掉自用模式、建渠道与中继令牌这些前置动作，每个套件都写一遍就会漂。
这里按 env 做一次，run_all.sh 逐个实例调用。

env：
  E2E_BASE / E2E_UPSTREAM            必填
  E2E_ADMIN / E2E_ADMIN_PW           默认 root / E2ePassw0rd!2026
  E2E_CHANNEL / E2E_MODEL            默认 e2e-openai / gpt-4o-mini
  E2E_CHANNEL_SETTINGS               渠道 settings 的 JSON（图片/主端点用例需要）
  E2E_TOKEN                          默认 e2e-relay
  E2E_SELF_USE                       默认 false；true 时不校验模型定价
"""
import json
import os
import sys
import time
import urllib.error
import urllib.request

BASE = os.environ["E2E_BASE"].rstrip("/")
UPSTREAM = os.environ["E2E_UPSTREAM"]
ADMIN = os.environ.get("E2E_ADMIN", "root")
ADMIN_PW = os.environ.get("E2E_ADMIN_PW", "E2ePassw0rd!2026")
CHANNEL = os.environ.get("E2E_CHANNEL", "e2e-openai")
MODEL = os.environ.get("E2E_MODEL", "gpt-4o-mini")
TOKEN = os.environ.get("E2E_TOKEN", "e2e-relay")
SELF_USE = os.environ.get("E2E_SELF_USE", "false") == "true"
CHANNEL_SETTINGS = os.environ.get("E2E_CHANNEL_SETTINGS", "")

OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))


def call(method, path, body=None, token=None):
    data = json.dumps(body).encode() if body is not None else None
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    req = urllib.request.Request(BASE + path, data=data, headers=headers, method=method)
    try:
        with OPENER.open(req, timeout=30) as resp:
            raw = resp.read()
    except urllib.error.HTTPError as err:
        raw = err.read()
    try:
        return json.loads(raw or b"{}")
    except Exception:
        return {"raw": raw.decode("utf-8", "replace")}


def log(msg):
    print(f"  [bootstrap] {msg}", file=sys.stderr)


def wait_ready(timeout=120):
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            if call("GET", "/api/status").get("success"):
                return
        except Exception:
            pass
        time.sleep(1)
    raise SystemExit(f"gateway at {BASE} never became ready")


def ensure_admin():
    res = call("POST", "/api/setup", {"username": ADMIN, "password": ADMIN_PW,
                                      "confirmPassword": ADMIN_PW})
    if not res.get("success"):
        log(f"setup: {res.get('message')} (沿用已有库)")
    res = call("POST", "/api/user/login", {"username": ADMIN, "password": ADMIN_PW})
    token = (res.get("data") or {}).get("access_token")
    if not token:
        raise SystemExit(f"login failed: {res}")
    return token


def set_option(jwt, key, value):
    res = call("PUT", "/api/option/", {"key": key, "value": value}, jwt)
    if not res.get("success"):
        raise SystemExit(f"set option {key} failed: {res}")
    # 写接口不校验取值范围，回读才算数
    options = {d.get("key"): d.get("value") for d in (call("GET", "/api/option/", None, jwt).get("data") or [])
               if isinstance(d, dict)}
    if str(options.get(key)) != str(value):
        raise SystemExit(f"option {key} did not persist: {options.get(key)!r}")


def ensure_channel(jwt):
    listing = call("GET", "/api/channel/?p=0&page_size=100", None, jwt).get("data") or {}
    for item in listing.get("items") or []:
        if item.get("name") == CHANNEL:
            call("DELETE", f"/api/channel/{item['id']}", None, jwt)
    channel = {"name": CHANNEL, "type": 1, "key": "sk-e2e-upstream", "base_url": UPSTREAM,
               "models": MODEL, "group": "default", "status": 1, "priority": 0, "weight": 0}
    if CHANNEL_SETTINGS:
        channel["settings"] = CHANNEL_SETTINGS
    res = call("POST", "/api/channel/", {"mode": "single", "channel": channel}, jwt)
    if not res.get("success"):
        raise SystemExit(f"create channel failed: {res}")
    # 内存缓存开启时选路读的是内存里的能力表，SYNC_FREQUENCY=1 也要等一个周期，
    # 否则紧接着发的第一批请求会 503「No available channel」。
    time.sleep(2)


def ensure_token(jwt):
    call("POST", "/api/token/", {"name": TOKEN, "remain_quota": 0, "unlimited_quota": True,
                                 "expired_time": -1, "group": "default"}, jwt)
    listing = call("GET", "/api/token/?p=0&page_size=20", None, jwt).get("data") or {}
    items = listing.get("items") or []
    tok = next((t for t in items if t.get("name") == TOKEN), None)
    if not tok:
        raise SystemExit("relay token was not created")
    res = call("POST", f"/api/token/{tok['id']}/key", {}, jwt)
    key = (res.get("data") or {}).get("key")
    if not key:
        raise SystemExit(f"cannot read token key: {res}")
    return key


def main():
    wait_ready()
    jwt = ensure_admin()
    # 本地盘常年 97%，不放开就是全站 503
    set_option(jwt, "performance_setting.monitor_disk_threshold", "99")
    set_option(jwt, "SelfUseModeEnabled", "true" if SELF_USE else "false")
    ensure_channel(jwt)
    key = ensure_token(jwt)
    log(f"ready: admin={ADMIN} channel={CHANNEL} model={MODEL}")
    print(key)


if __name__ == "__main__":
    main()
