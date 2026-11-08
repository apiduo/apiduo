#!/usr/bin/env python3
"""渠道健康：自动禁用、自动启用、批量测试任务。

三种状态的语义容易搞混：1=启用，2=人工禁用，3=自动禁用。
自动启用只对 3 生效（人工禁用不该被系统悄悄翻回来），这条也用对照组钉住。
"""
import json
import os
import time
import urllib.error
import urllib.request

BASE = os.environ.get("E2E_BASE", "http://127.0.0.1:39420")
RELAY = os.environ.get("E2E_RELAY_KEY", "")
ADMIN = (os.environ.get("E2E_ADMIN", "admin"), os.environ.get("E2E_ADMIN_PW", "apiduo-test-2026"))
UP1 = os.environ.get("E2E_UP1", "http://127.0.0.1:39401")
DEAD = "http://127.0.0.1:39497"

STATUS_ENABLED = 1
STATUS_AUTO_DISABLED = 3

OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))
FAILS = []
STEPS = []


def call(method, path, body=None, token=None):
    data = json.dumps(body).encode() if body is not None else None
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    req = urllib.request.Request(BASE + path, data=data, headers=headers, method=method)
    try:
        with OPENER.open(req, timeout=90) as resp:
            return resp.status, json.loads(resp.read() or b"{}")
    except urllib.error.HTTPError as err:
        raw = err.read()
        try:
            return err.code, json.loads(raw or b"{}")
        except Exception:
            return err.code, raw.decode("utf-8", "replace")


def check(name, cond, detail=""):
    STEPS.append((name, bool(cond)))
    if not cond:
        FAILS.append(f"{name}: {detail}")
    print(("  PASS  " if cond else "  FAIL  ") + name + (f"  [{detail}]" if detail else ""))


def admin_token():
    st, res = call("POST", "/api/user/login", {"username": ADMIN[0], "password": ADMIN[1]})
    if st != 200 or not res.get("success"):
        raise SystemExit(f"admin login failed: HTTP {st} {res}")
    return res["data"]["access_token"]


def set_policy(jwt, options):
    st, res = call("PATCH", "/api/option/request_policy", {"options": options}, jwt)
    if st != 200 or not res.get("success"):
        raise SystemExit(f"set policy {options} failed: HTTP {st} {res}")
    st, res = call("GET", "/api/option/request_policy", None, jwt)
    return ((res.get("data") or {}).get("options") or {})


def channels(jwt):
    st, res = call("GET", "/api/channel/?p=0&page_size=100", None, jwt)
    return ((res.get("data") or {}).get("items")) or []


def find(jwt, name):
    return next((c for c in channels(jwt) if c.get("name") == name), None)


def drop(jwt, prefix="e2e-health-"):
    for item in channels(jwt):
        if str(item.get("name", "")).startswith(prefix):
            call("DELETE", f"/api/channel/{item['id']}", None, jwt)


def create(jwt, name, model, base_url):
    st, res = call("POST", "/api/channel/", {"mode": "single", "channel": {
        "name": name, "type": 1, "key": "sk-e2e-upstream", "base_url": base_url,
        "models": model, "group": "default", "status": 1, "priority": 0, "weight": 1}}, jwt)
    if not (st == 200 and res.get("success")):
        raise SystemExit(f"create {name} failed: HTTP {st} {res}")
    time.sleep(2.5)
    item = find(jwt, name)
    return item["id"] if item else None


def status_of(jwt, channel_id):
    st, res = call("GET", f"/api/channel/{channel_id}", None, jwt)
    if st == 200 and isinstance(res.get("data"), dict):
        return res["data"].get("status")
    item = next((c for c in channels(jwt) if c.get("id") == channel_id), None)
    return item.get("status") if item else None


def wait_status(jwt, channel_id, want, timeout=8.0):
    deadline = time.time() + timeout
    last = None
    while time.time() < deadline:
        last = status_of(jwt, channel_id)
        if last == want:
            return last
        time.sleep(0.3)
    return last


def relay(model, content="hello"):
    st, res = call("POST", "/v1/chat/completions",
                   {"model": model, "messages": [{"role": "user", "content": content}]},
                   "sk-" + RELAY)
    return st, res


def ensure_disk_headroom(jwt):
    """本地盘常年 98%，阈值不放开（默认 95）会被 503 全站拦掉。

    每个套件自己设一次并回读：不要依赖上一个套件留下的实例状态。
    """
    call("PUT", "/api/option/", {"key": "performance_setting.monitor_disk_threshold", "value": "99"}, jwt)
    st, res = call("GET", "/api/option/", None, jwt)
    opts = {d.get("key"): d.get("value") for d in (res.get("data") or []) if isinstance(d, dict)}
    got = opts.get("performance_setting.monitor_disk_threshold")
    if got != "99":
        raise SystemExit(f"disk threshold did not stick: {got!r}")


def main():
    if not RELAY:
        raise SystemExit("E2E_RELAY_KEY is required (run via run_all.sh)")
    jwt = admin_token()
    drop(jwt)
    ensure_disk_headroom(jwt)
    set_policy(jwt, {"RetryTimes": "0"})

    # ---------- 1. 对照组：开关关掉时，同样的 500 不应该禁用渠道 ----------
    opts = set_policy(jwt, {"AutomaticDisableChannelEnabled": "false"})
    check("自动禁用开关已关", opts.get("AutomaticDisableChannelEnabled") == "false",
          str(opts.get("AutomaticDisableChannelEnabled")))
    ctl_id = create(jwt, "e2e-health-ctl", "health-ctl", UP1)
    st, _ = relay("health-ctl", "fail please")
    check("对照组请求返回 500", st == 500, f"HTTP {st}")
    check("开关关闭时渠道保持启用", wait_status(jwt, ctl_id, STATUS_ENABLED, 3) == STATUS_ENABLED,
          f"status={status_of(jwt, ctl_id)}")

    # ---------- 2. 自动禁用：500 命中禁用状态码 → status=3 ----------
    opts = set_policy(jwt, {
        "AutomaticDisableChannelEnabled": "true",
        "AutomaticDisableStatusCodes": "500",
    })
    check("自动禁用开关已开", opts.get("AutomaticDisableChannelEnabled") == "true",
          str(opts.get("AutomaticDisableChannelEnabled")))
    chan_id = create(jwt, "e2e-health-a", "health-a", UP1)
    st, _ = relay("health-a", "fail please")
    check("触发请求返回 500", st == 500, f"HTTP {st}")
    got = wait_status(jwt, chan_id, STATUS_AUTO_DISABLED, 6)
    check("渠道被标记为自动禁用（3）", got == STATUS_AUTO_DISABLED, f"status={got}")
    st, res = relay("health-a")
    check("禁用后该模型无可用渠道（503）", st == 503, f"HTTP {st} {str(res)[:100]}")

    # ---------- 3. 自动启用：批量测试成功 → 自动禁用的渠道回到启用 ----------
    # 注意：单渠道测试（/channel/test/:id）只测不启，自动启用只在批量/定时测试里做，
    # 所以这里必须走 /channel/test 这条路径。
    opts = set_policy(jwt, {"AutomaticEnableChannelEnabled": "true"})
    check("自动启用开关已开", opts.get("AutomaticEnableChannelEnabled") == "true",
          str(opts.get("AutomaticEnableChannelEnabled")))
    st, res = call("GET", "/api/channel/test", None, jwt)
    check("批量测试任务入队成功（自动启用）", st == 200 and res.get("success"), f"HTTP {st} {str(res)[:120]}")
    got = wait_status(jwt, chan_id, STATUS_ENABLED, 45)
    check("批量测试成功后渠道恢复启用（1）", got == STATUS_ENABLED, f"status={got}")
    # 状态刚翻回启用时能力表可能还没刷新，给一个短重试窗口
    st = None
    for _ in range(10):
        st, _ = relay("health-a")
        if st == 200:
            break
        time.sleep(0.5)
    check("恢复后请求 200", st == 200, f"HTTP {st}")

    # ---------- 4+5. 同一次批量测试：死渠道判死，人工禁用不被翻回 ----------
    call("POST", f"/api/channel/{chan_id}/status", {"status": 2}, jwt)
    time.sleep(1)
    check("人工禁用后 status=2", status_of(jwt, chan_id) == 2, f"status={status_of(jwt, chan_id)}")
    dead_id = create(jwt, "e2e-health-dead", "health-dead", DEAD)
    st, res = call("GET", "/api/channel/test", None, jwt)
    check("批量测试任务再次入队", st == 200 and res.get("success"), f"HTTP {st} {str(res)[:120]}")
    got = wait_status(jwt, dead_id, STATUS_AUTO_DISABLED, 45)
    check("批量测试把死渠道判为自动禁用", got == STATUS_AUTO_DISABLED, f"status={got}")
    check("人工禁用不会被批量测试改写", status_of(jwt, chan_id) == 2, f"status={status_of(jwt, chan_id)}")

    drop(jwt)
    set_policy(jwt, {
        "AutomaticDisableChannelEnabled": "false",
        "AutomaticEnableChannelEnabled": "false",
        "RetryTimes": "0",
    })
    print("\n=== summary ===")
    print(f"{len(STEPS) - len(FAILS)}/{len(STEPS)} passed")
    for f in FAILS:
        print("  FAILED:", f)
    return 1 if FAILS else 0


if __name__ == "__main__":
    raise SystemExit(main())
