#!/usr/bin/env python3
"""组合场景：auto 分组 + 多 key 渠道。

两条独立机制叠在一起时，最容易出的问题是「重试预算互相抢」——
key 级重试会不会顺手把跨分组预算也花掉？分组切换时 tried 集合会不会串味？
这里用两组渠道分别验证：
- 组内只有一把坏 key：换 key 就能解决，绝不该切组
- 组内两把 key 全坏：key 重试用尽后才切组，切过去要能正常选 key
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
UP2 = os.environ.get("E2E_UP2", "http://127.0.0.1:39404")
H1 = os.environ.get("E2E_H1", "/tmp/apiduo-e2e/upstream-headers.log")
H2 = os.environ.get("E2E_H2", "/tmp/apiduo-e2e/upstream-headers-2.log")

MODEL = "cb-model"
MODEL_DEAD = "cb-dead"

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


def set_retry(times):
    st, res = call("PATCH", "/api/option/request_policy", {"options": {"RetryTimes": str(times)}}, JWT)
    if st != 200 or not res.get("success"):
        raise SystemExit(f"set RetryTimes failed: HTTP {st} {res}")


def channels():
    st, res = call("GET", "/api/channel/?p=0&page_size=100", None, JWT)
    return (res.get("data") or {}).get("items") or []


def drop(prefix="e2e-cb-"):
    for item in channels():
        if str(item.get("name", "")).startswith(prefix):
            call("DELETE", f"/api/channel/{item['id']}", None, JWT)


def create_single(name, group, model, base):
    channel = {"name": name, "type": 1, "key": "sk-e2e-upstream", "base_url": base,
               "models": model, "group": group, "status": 1, "priority": 0,
               "weight": 0, "auto_ban": 0}
    st, res = call("POST", "/api/channel/", {"mode": "single", "channel": channel}, JWT)
    if not (st == 200 and res.get("success")):
        raise SystemExit(f"create {name} failed: HTTP {st} {res}")
    time.sleep(2)


def create_multikey(name, group, model, keys):
    body = {
        "mode": "multi_to_single", "multi_key_mode": "sticky",
        "channel": {"name": name, "type": 1, "key": "\n".join(keys), "base_url": UP1,
                    "models": model, "group": group, "status": 1, "auto_ban": 0},
    }
    st, res = call("POST", "/api/channel/", body, JWT)
    if not (st == 200 and res.get("success")):
        raise SystemExit(f"create {name} failed: HTTP {st} {res}")
    time.sleep(2)


def make_token(name, auto_groups):
    body = {"name": name, "remain_quota": 0, "unlimited_quota": True, "expired_time": -1,
            "group": "auto", "auto_groups": auto_groups, "cross_group_retry": True}
    st, res = call("POST", "/api/token/", body, JWT)
    if not (st == 200 and res.get("success")):
        raise SystemExit(f"create token {name} failed: HTTP {st} {res}")
    tok = find_token(name)
    if not tok:
        raise SystemExit(f"token {name} was not created")
    st, res = call("POST", f"/api/token/{tok['id']}/key", {}, JWT)
    return (res.get("data") or {}).get("key")


def find_token(name, page_size=100, max_pages=20):
    """按名字找令牌，必须翻页：一页只有 100 条，后面套件建的令牌会落到第二页。"""
    for page in range(max_pages):
        st, res = call("GET", f"/api/token/?p={page}&page_size={page_size}", None, JWT)
        data = res.get("data") or {}
        items = data.get("items") or []
        row = next((t for t in items if t.get("name") == name), None)
        if row:
            return row
        if len(items) < page_size:
            break
    return None


def relay(model, key):
    return call("POST", "/v1/chat/completions",
                {"model": model, "messages": [{"role": "user", "content": "combo"}]},
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


def admin_info(model):
    st, res = call("GET", "/api/log/?p=0&page_size=20&type=2", None, JWT)
    rows = (res.get("data") or {}).get("items") or []
    for row in rows:
        if row.get("model_name") != model:
            continue
        other = row.get("other")
        if isinstance(other, str):
            try:
                other = json.loads(other)
            except Exception:
                other = {}
        return (other or {}).get("admin_info") or {}
    return None


def main():
    global JWT
    if not RELAY:
        raise SystemExit("E2E_RELAY_KEY is required (run via run_all.sh)")
    JWT = admin_token()
    drop()
    set_option("performance_setting.monitor_disk_threshold", "99")
    set_option("SelfUseModeEnabled", "true")
    set_option("AutomaticDisableChannelEnabled", "false")
    set_option("UserUsableGroups", json.dumps({"default": "默认分组", "vip": "VIP 分组", "auto": "自动分组"}))
    set_option("GroupRatio", json.dumps({"default": 1, "vip": 1}))
    set_option("MaxTokenAutoGroups", "5")
    set_retry(1)

    # 组内：两把 key 一好一坏；组外：单 key 一定成功
    create_multikey("e2e-cb-default", "default", MODEL, ["sk-e2e-mk-alpha", "sk-e2e-mk-bad"])
    create_single("e2e-cb-vip", "vip", MODEL, UP2)
    # 组内两把 key 全坏
    create_multikey("e2e-cb-dead", "default", MODEL_DEAD, ["sk-e2e-mk-bad1", "sk-e2e-mk-bad2"])
    create_single("e2e-cb-vip2", "vip", MODEL_DEAD, UP2)

    tokens = [make_token(f"e2e-cb-t{i}", ["default", "vip"]) for i in range(12)]
    check("12 个 auto 令牌就绪", all(tokens), str(sum(1 for t in tokens if t)))

    # ---------- 1. 组内换 key 能解决时，绝不切组 ----------
    base1, base2 = count(H1, MODEL), count(H2, MODEL)
    codes = [relay(MODEL, key)[0] for key in tokens]
    d1 = count(H1, MODEL) - base1
    d2 = count(H2, MODEL) - base2
    check("全部请求 200", all(c == 200 for c in codes), str(sorted(set(codes))))
    check("vip 组零调用（key 级重试没有触发切组）", d2 == 0, f"up2={d2}")
    check("组内重试确实发生过（调用数 > 请求数）", d1 > len(tokens), f"calls={d1} reqs={len(tokens)}")

    # ---------- 2. 组内全坏才切组，切过去要能选 key ----------
    base1, base2 = count(H1, MODEL_DEAD), count(H2, MODEL_DEAD)
    st, res = relay(MODEL_DEAD, tokens[0])
    d1 = count(H1, MODEL_DEAD) - base1
    d2 = count(H2, MODEL_DEAD) - base2
    check("全坏时最终 200（切到 vip）", st == 200, f"HTTP {st} {str(res)[:100]}")
    check("default 用满两把 key（2 次）", d1 == 2, f"up1={d1}")
    check("vip 接住 1 次", d2 == 1, f"up2={d2}")

    info = admin_info(MODEL_DEAD) or {}
    used = info.get("use_channel") or []
    # use_channel 是「每次尝试一个 id」：组内 2 次 + 切组后 1 次 = 3 条
    check("日志记下 3 次尝试（组内 2 + vip 1）", len(used) == 3, str(used))
    check("尝试跨越两个渠道", len(set(used)) == 2, str(used))
    # 最后落到单 key 渠道，日志就不能再声称多 key —— 否则 UI 会显示一个
    # 不存在的 key 序号。这是「日志描述最后一次尝试」的直接结果，不是丢失信息。
    check("最终落到单 key 渠道时不标记多 key", info.get("is_multi_key") is not True,
          str(info.get("is_multi_key")))

    drop()
    set_retry(0)
    print("\n=== summary ===")
    print(f"{len(STEPS) - len(FAILS)}/{len(STEPS)} passed")
    for f in FAILS:
        print("  FAILED:", f)
    return 1 if FAILS else 0


if __name__ == "__main__":
    raise SystemExit(main())
