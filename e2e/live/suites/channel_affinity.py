#!/usr/bin/env python3
"""渠道亲和（sticky）：同一个会话键始终落同一条渠道。

为什么单独测：优先级/权重是「分散」，亲和是「不分散」，两者共用选路但结论相反。
亲和按 `channel_affinity_setting.rules` 里的 key_sources 取键（这里用请求头），
命中后把 key → channel 记住一段时间（ttl），期间不再参与加权随机。
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

OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))
FAILS = []
STEPS = []


def call(method, path, body=None, token=None, headers=None):
    data = json.dumps(body).encode() if body is not None else None
    hdrs = {"Content-Type": "application/json"}
    if token:
        hdrs["Authorization"] = "Bearer " + token
    if headers:
        hdrs.update(headers)
    req = urllib.request.Request(BASE + path, data=data, headers=hdrs, method=method)
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


def set_policy(jwt, options):
    st, res = call("PATCH", "/api/option/request_policy", {"options": options}, jwt)
    if st != 200 or not res.get("success"):
        raise SystemExit(f"set policy {options} failed: HTTP {st} {res}")
    st, res = call("GET", "/api/option/request_policy", None, jwt)
    return ((res.get("data") or {}).get("options") or {})


def channels(jwt):
    st, res = call("GET", "/api/channel/?p=0&page_size=100", None, jwt)
    return ((res.get("data") or {}).get("items")) or []


def drop(jwt, prefix="e2e-aff-"):
    for item in channels(jwt):
        if str(item.get("name", "")).startswith(prefix):
            call("DELETE", f"/api/channel/{item['id']}", None, jwt)


def create(jwt, name, model, base_url, priority=0, weight=1):
    st, res = call("POST", "/api/channel/", {"mode": "single", "channel": {
        "name": name, "type": 1, "key": "sk-e2e-upstream", "base_url": base_url,
        "models": model, "group": "default", "status": 1,
        "priority": priority, "weight": weight}}, jwt)
    if not (st == 200 and res.get("success")):
        raise SystemExit(f"create {name} failed: HTTP {st} {res}")


def send(model, session, n=1):
    """返回 (状态码列表, 第一个非 200 的响应体)，失败时能直接看出原因。"""
    codes, first_bad = [], None
    for _ in range(n):
        st, res = call("POST", "/v1/chat/completions",
                       {"model": model, "messages": [{"role": "user", "content": "affinity"}]},
                       "sk-" + RELAY, {"X-Session": session})
        codes.append(st)
        if st != 200 and first_bad is None:
            first_bad = str(res)[:140]
    return codes, first_bad


def hit(model):
    """两台假上游各自收到了多少次该模型的请求。"""
    counts = []
    for path in (H1, H2):
        n = 0
        try:
            with open(path, encoding="utf-8") as fh:
                for line in fh:
                    if f'model="{model}"' not in line:
                        continue
                    n += 1
        except FileNotFoundError:
            pass
        counts.append(n)
    return counts


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

    model = "aff-model"
    create(jwt, "e2e-aff-1", model, UP1)
    create(jwt, "e2e-aff-2", model, UP2)
    time.sleep(3)

    # 规则要写全：配置是按字段合并进结构体的，省略 path_regex 会继承默认规则
    # （codex 的 /v1/responses），于是这条规则永远匹配不上。
    rule = json.dumps([{
        "name": "session", "model_regex": [".*"], "path_regex": [".*"],
        "user_agent_include": [],
        "key_sources": [{"type": "request_header", "key": "X-Session"}],
        "value_regex": "", "ttl_seconds": 300,
        "param_override_template": {},
        "skip_retry_on_failure": False, "session_mode": "inherit",
        "include_using_group": False, "include_model_name": False, "include_rule_name": False,
    }])
    opts = set_policy(jwt, {
        "channel_affinity_setting.enabled": "true",
        "channel_affinity_setting.rules": rule,
        "channel_affinity_setting.session_mode": "prefer",
        "channel_affinity_setting.default_ttl_seconds": "300",
    })
    check("亲和开关已生效", opts.get("channel_affinity_setting.enabled") == "true", str(opts.get("channel_affinity_setting.enabled")))
    time.sleep(1)

    # 同一会话键：先热身一次让绑定落盘，再测 12 次
    # （绑定是在响应返回后由分发中间件写入的，紧接着发下一批会撞上竞态）
    send(model, "sess-alpha", 1)
    time.sleep(0.2)
    codes, bad = send(model, "sess-alpha", 12)
    check("亲和请求全部 200", all(c == 200 for c in codes), bad or str(sorted(set(codes))))
    before = hit(model)
    codes, _ = send(model, "sess-alpha", 12)
    after = hit(model)
    delta = [after[0] - before[0], after[1] - before[1]]
    check("同一 session 全部落同一渠道", max(delta) == 12 and min(delta) == 0, f"delta={delta}")

    # 另一个会话键：也要各自粘住，但允许落在另一条渠道
    send(model, "sess-beta", 1)
    time.sleep(0.2)
    codes, bad = send(model, "sess-beta", 8)
    check("第二个 session 请求 200", all(c == 200 for c in codes), bad or str(sorted(set(codes))))
    a1 = hit(model)
    codes, _ = send(model, "sess-beta", 8)
    a2 = hit(model)
    delta2 = [a2[0] - a1[0], a2[1] - a1[1]]
    check("第二个 session 也粘住", max(delta2) == 8 and min(delta2) == 0, f"delta={delta2}")

    # 关掉亲和：同样的请求头不再固定，40 次应分散到两台上游
    set_policy(jwt, {"channel_affinity_setting.enabled": "false"})
    time.sleep(1)
    b1 = hit(model)
    codes, _ = send(model, "sess-alpha", 40)
    b2 = hit(model)
    delta3 = [b2[0] - b1[0], b2[1] - b1[1]]
    check("关掉亲和后恢复分散", all(c == 200 for c in codes) and min(delta3) > 0, f"delta={delta3}")

    drop(jwt)
    set_policy(jwt, {"channel_affinity_setting.enabled": "false"})
    print("\n=== summary ===")
    print(f"{len(STEPS) - len(FAILS)}/{len(STEPS)} passed")
    for f in FAILS:
        print("  FAILED:", f)
    return 1 if FAILS else 0


if __name__ == "__main__":
    raise SystemExit(main())
