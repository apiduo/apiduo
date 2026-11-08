#!/usr/bin/env python3
"""跨分组重试（auto 分组）：分组顺序、优先级优先、开关对照、预算收敛。

`service/channel_select.go` 的约定：每个分组先把自己的优先级用完，再切到下一个
分组；`cross_group_retry` 关掉时一个组都不切。这里用两台假上游分别代表
「总是失败」和「总是成功」，断言的是上游实际收到的请求数，不是网关日志。

顺带把令牌 API 的约束也测了：auto_groups 只能在用户可用分组内、不能重复、
不能超过 MaxTokenAutoGroups；非 auto 分组的令牌会被强制清空这两个字段。
"""
import json
import os
import time
import urllib.error
import urllib.request

BASE = os.environ.get("E2E_BASE", "http://127.0.0.1:39420")
RELAY = os.environ.get("E2E_RELAY_KEY", "")
ADMIN = (os.environ.get("E2E_ADMIN", "admin"), os.environ.get("E2E_ADMIN_PW", "apiduo-test-2026"))
UP1 = os.environ.get("E2E_UP1", "http://127.0.0.1:39401")  # "fail" 内容返回 500
UP2 = os.environ.get("E2E_UP2", "http://127.0.0.1:39404")  # 永远 200
H1 = os.environ.get("E2E_H1", "/tmp/apiduo-e2e/upstream-headers.log")
H2 = os.environ.get("E2E_H2", "/tmp/apiduo-e2e/upstream-headers-2.log")

OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))
FAILS = []
STEPS = []
JWT = ""


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


def set_option(key, value):
    st, res = call("PUT", "/api/option/", {"key": key, "value": value}, JWT)
    if st != 200 or not res.get("success"):
        raise SystemExit(f"set {key}={value} failed: HTTP {st} {res}")


def read_option(key):
    st, res = call("GET", "/api/option/", None, JWT)
    opts = {d.get("key"): d.get("value") for d in (res.get("data") or []) if isinstance(d, dict)}
    return opts.get(key)


def ensure_disk_headroom():
    set_option("performance_setting.monitor_disk_threshold", "99")
    if read_option("performance_setting.monitor_disk_threshold") != "99":
        raise SystemExit("disk threshold did not stick")


def set_retry(times):
    st, res = call("PATCH", "/api/option/request_policy", {"options": {"RetryTimes": str(times)}}, JWT)
    if st != 200 or not res.get("success"):
        raise SystemExit(f"set RetryTimes={times} failed: HTTP {st} {res}")


def channels():
    st, res = call("GET", "/api/channel/?p=0&page_size=100", None, JWT)
    return (res.get("data") or {}).get("items") or []


def drop(prefix="e2e-ag-"):
    for item in channels():
        if str(item.get("name", "")).startswith(prefix):
            call("DELETE", f"/api/channel/{item['id']}", None, JWT)


def create_channel(name, group, model, base=UP1, priority=0):
    channel = {"name": name, "type": 1, "key": "sk-e2e-upstream", "base_url": base,
               "models": model, "group": group, "status": 1, "priority": priority, "weight": 0,
               "auto_ban": 0}
    st, res = call("POST", "/api/channel/", {"mode": "single", "channel": channel}, JWT)
    if not (st == 200 and res.get("success")):
        raise SystemExit(f"create {name} failed: HTTP {st} {res}")
    time.sleep(2)


def make_token(name, group, auto_groups=None, cross=False):
    body = {"name": name, "remain_quota": 0, "unlimited_quota": True,
            "expired_time": -1, "group": group, "cross_group_retry": cross}
    if auto_groups is not None:
        body["auto_groups"] = auto_groups
    st, res = call("POST", "/api/token/", body, JWT)
    if not (st == 200 and res.get("success")):
        return None, res
    st, res = call("GET", "/api/token/?p=0&page_size=100", None, JWT)
    items = (res.get("data") or {}).get("items") or []
    tok = next((t for t in items if t.get("name") == name), None)
    if not tok:
        raise SystemExit(f"token {name} was not created")
    st, res = call("POST", f"/api/token/{tok['id']}/key", {}, JWT)
    return (res.get("data") or {}).get("key"), tok


def token_row(name):
    st, res = call("GET", "/api/token/?p=0&page_size=100", None, JWT)
    items = (res.get("data") or {}).get("items") or []
    return next((t for t in items if t.get("name") == name), None)


def relay(model, key, content="cross-group"):
    return call("POST", "/v1/chat/completions",
                {"model": model, "messages": [{"role": "user", "content": content}]},
                "sk-" + key)


def count(path, model):
    n = 0
    try:
        with open(path, encoding="utf-8") as fh:
            for line in fh:
                if f'model="{model}"' in line:
                    n += 1
    except FileNotFoundError:
        pass
    return n


def counts(model):
    return count(H1, model), count(H2, model)


def main():
    global JWT
    if not RELAY:
        raise SystemExit("E2E_RELAY_KEY is required (run via run_all.sh)")
    JWT = admin_token()
    drop()
    ensure_disk_headroom()
    set_option("SelfUseModeEnabled", "true")
    set_option("AutomaticDisableChannelEnabled", "false")
    set_option("MaxTokenAutoGroups", "5")

    # 分组可用性：vip 必须同时在「用户可用分组」和「分组倍率」里，否则令牌存不下来
    # auto 本身也必须在「用户可用分组」里：令牌分组校验走的是同一张表，
    # 缺了它请求会被 403「No permission to access group auto」挡在选路之前。
    set_option("UserUsableGroups", json.dumps({"default": "默认分组", "vip": "VIP 分组", "auto": "自动分组"}))
    set_option("GroupRatio", json.dumps({"default": 1, "vip": 1}))
    check("vip 分组已可用", "vip" in (read_option("UserUsableGroups") or ""), str(read_option("UserUsableGroups")))

    # ---------- 1. 基础用例：default 全挂，切到 vip ----------
    m = "ag-cross"
    create_channel("e2e-ag-default", "default", m, UP1)
    create_channel("e2e-ag-vip", "vip", m, UP2)
    t_cross, _ = make_token("e2e-ag-cross", "auto", ["default", "vip"], cross=True)
    t_nocross, _ = make_token("e2e-ag-nocross", "auto", ["default", "vip"], cross=False)
    t_order, _ = make_token("e2e-ag-order", "auto", ["vip", "default"], cross=True)
    check("auto 令牌建好且拿到了 key", all([t_cross, t_nocross, t_order]),
          f"{bool(t_cross)}/{bool(t_nocross)}/{bool(t_order)}")

    set_retry(1)
    base1, base2 = counts(m)
    st, res = relay(m, t_cross, content="fail on purpose")
    a1, a2 = counts(m)
    d1, d2 = a1 - base1, a2 - base2
    check("跨分组重试后返回 200", st == 200, f"HTTP {st} {str(res)[:120]}")
    check("default 组用完自己的重试（2 次）", d1 == 2, f"up1={d1}")
    check("随后切到 vip 组（1 次）", d2 == 1, f"up2={d2}")

    # ---------- 2. 对照：关掉 cross_group_retry 就一个组都不切 ----------
    base1, base2 = counts(m)
    st, res = relay(m, t_nocross, content="fail on purpose")
    a1, a2 = counts(m)
    d1, d2 = a1 - base1, a2 - base2
    check("关闭跨分组后失败如实返回 5xx", st >= 500, f"HTTP {st} {str(res)[:100]}")
    check("仍然只在 default 组重试（2 次）", d1 == 2, f"up1={d1}")
    check("vip 组一次都没被调用", d2 == 0, f"up2={d2}")

    # ---------- 3. 顺序：auto_groups 的顺序就是分组顺序 ----------
    base1, base2 = counts(m)
    st, _ = relay(m, t_order, content="fail on purpose")
    a1, a2 = counts(m)
    d1, d2 = a1 - base1, a2 - base2
    check("顺序反转后首发就是 vip（200）", st == 200, f"HTTP {st}")
    check("default 组零调用", d1 == 0, f"up1={d1}")
    check("vip 组只调用一次", d2 == 1, f"up2={d2}")

    # ---------- 4. 分组内优先级先用完，再切分组 ----------
    mp = "ag-prio"
    create_channel("e2e-ag-p10", "default", mp, UP1, priority=10)  # 高优先级但会失败
    create_channel("e2e-ag-p0", "default", mp, UP2, priority=0)    # 低优先级会成功
    create_channel("e2e-ag-vipfail", "vip", mp, UP1)               # 若切到 vip 就会失败
    t_prio, _ = make_token("e2e-ag-prio", "auto", ["default", "vip"], cross=True)
    check("优先级用例令牌就绪", bool(t_prio))
    base1, base2 = counts(mp)
    st, res = relay(mp, t_prio, content="fail on purpose")
    a1, a2 = counts(mp)
    d1, d2 = a1 - base1, a2 - base2
    check("组内降级后成功（200）", st == 200, f"HTTP {st} {str(res)[:120]}")
    check("高优先级失败 1 次", d1 == 1, f"up1={d1}")
    check("低优先级接住 1 次", d2 == 1, f"up2={d2}")
    check("没有切到 vip（否则会多一次 up1 调用）", d1 == 1, f"up1={d1}")

    # ---------- 5. 所有分组都失败：报错且预算收敛，不无限重试 ----------
    mf = "ag-fail"
    create_channel("e2e-ag-f1", "default", mf, UP1)
    create_channel("e2e-ag-f2", "vip", mf, UP1)
    t_fail, _ = make_token("e2e-ag-fail", "auto", ["default", "vip"], cross=True)
    check("全失败用例令牌就绪", bool(t_fail))
    base1, _ = counts(mf)
    st, res = relay(mf, t_fail, content="fail on purpose")
    d1 = counts(mf)[0] - base1
    check("全部组都失败时返回 5xx", st >= 500, f"HTTP {st} {str(res)[:100]}")
    # 重试预算是「每个分组各算一遍」：RetryTimes=1 时每组 2 次，两个组共 4 次，
    # 然后选路返回 nil 结束。有界、不循环，但确实比单组多一倍。
    check("尝试次数有界（每组 RetryTimes+1，两组共 4 次）", d1 == 4, f"attempts={d1}")

    # ---------- 6. 令牌 API 的约束 ----------
    set_retry(0)
    _, res = make_token("e2e-ag-badgroup", "auto", ["nope"], cross=True)
    check("未授权分组被拒绝", not (res or {}).get("success"), str(res)[:120])
    _, res = make_token("e2e-ag-dup", "auto", ["default", "default"], cross=True)
    check("重复分组被拒绝", not (res or {}).get("success"), str(res)[:120])

    set_option("MaxTokenAutoGroups", "1")
    _, res = make_token("e2e-ag-toomany", "auto", ["default", "vip"], cross=True)
    check("超过 MaxTokenAutoGroups 被拒绝", not (res or {}).get("success"), str(res)[:120])
    set_option("MaxTokenAutoGroups", "5")

    t_plain, _ = make_token("e2e-ag-plain", "default", ["vip"], cross=True)
    row = token_row("e2e-ag-plain") or {}
    check("非 auto 分组强制关闭跨分组重试", row.get("cross_group_retry") is False,
          str(row.get("cross_group_retry")))
    check("非 auto 分组清空 auto_groups", not row.get("auto_groups"), str(row.get("auto_groups")))

    drop()
    set_retry(0)
    print("\n=== summary ===")
    print(f"{len(STEPS) - len(FAILS)}/{len(STEPS)} passed")
    for f in FAILS:
        print("  FAILED:", f)
    return 1 if FAILS else 0


if __name__ == "__main__":
    raise SystemExit(main())
