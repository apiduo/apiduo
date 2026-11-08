#!/usr/bin/env python3
"""多渠道选择：优先级分层、同优先级权重分布、失败降级。

生产开的是内存缓存选路（`MEMORY_CACHE_ENABLED=true`），权重与优先级只在
`GetRandomSatisfiedChannel` 里生效；这条路径和「无缓存直查库」不是同一份代码，
所以必须在缓存开着的实例上测。

两台假上游各自记录一行调用（含 model），断言的是「谁真的被选中了几次」，
不看网关自报。
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
DEAD = "http://127.0.0.1:39498"  # 没有监听：连接被拒，用来模拟「优先级最高的渠道挂了」

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


def set_retry(jwt, times):
    st, res = call("PATCH", "/api/option/request_policy", {"options": {"RetryTimes": str(times)}}, jwt)
    if st != 200 or not res.get("success"):
        raise SystemExit(f"set RetryTimes={times} failed: HTTP {st} {res}")
    st, res = call("GET", "/api/option/request_policy", None, jwt)
    got = ((res.get("data") or {}).get("options") or {}).get("RetryTimes")
    if str(got) != str(times):
        raise SystemExit(f"RetryTimes did not persist: {got!r}")


def channels(jwt):
    st, res = call("GET", "/api/channel/?p=0&page_size=100", None, jwt)
    return ((res.get("data") or {}).get("items")) or []


def drop(jwt, prefix="e2e-sel-"):
    for item in channels(jwt):
        if str(item.get("name", "")).startswith(prefix):
            call("DELETE", f"/api/channel/{item['id']}", None, jwt)


def create(jwt, name, model, priority, weight, base_url):
    channel = {"name": name, "type": 1, "key": "sk-e2e-upstream", "base_url": base_url,
               "models": model, "group": "default", "status": 1,
               "priority": priority, "weight": weight}
    st, res = call("POST", "/api/channel/", {"mode": "single", "channel": channel}, jwt)
    if not (st == 200 and res.get("success")):
        raise SystemExit(f"create {name} failed: HTTP {st} {res}")


def relay(model, n=1, content="channel-select"):
    codes = []
    for i in range(n):
        st, res = call("POST", "/v1/chat/completions",
                       {"model": model, "messages": [{"role": "user", "content": content}]},
                       "sk-" + RELAY)
        codes.append((st, res))
    return codes


def counts(model):
    """两台假上游各自收到多少次该模型的请求。"""
    out = []
    for path in (H1, H2):
        n = 0
        try:
            with open(path, encoding="utf-8") as fh:
                for line in fh:
                    if f'model="{model}"' in line:
                        n += 1
        except FileNotFoundError:
            pass
        out.append(n)
    return out


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
    set_retry(jwt, 0)
    check("基线 RetryTimes=0", True)

    # ---------- 1. 优先级分层：高优先级独占 ----------
    m = "sel-priority"
    create(jwt, "e2e-sel-hi", m, 10, 1, UP1)
    create(jwt, "e2e-sel-lo", m, 0, 1, UP2)
    time.sleep(3)
    codes = relay(m, 20)
    ok = all(st == 200 for st, _ in codes)
    a, b = counts(m)
    check("优先级请求全部 200", ok, f"{sorted({c for c, _ in codes})}")
    check("高优先级独占（20/0）", (a, b) == (20, 0), f"up1={a} up2={b}")

    # ---------- 2. 同优先级按权重 3:1 ----------
    drop(jwt)
    m = "sel-weight"
    create(jwt, "e2e-sel-w3", m, 0, 3, UP1)
    create(jwt, "e2e-sel-w1", m, 0, 1, UP2)
    time.sleep(3)
    codes = relay(m, 200)
    ok = all(st == 200 for st, _ in codes)
    a, b = counts(m)
    total = a + b
    share = b / total if total else 0
    check("权重请求全部 200", ok, f"{sorted({c for c, _ in codes})}")
    check("200 次全部由两台承接", total == 200, f"{a}+{b}")
    check("权重 3:1 近似成立（低权占 15%-38%）", 0.15 <= share <= 0.38, f"up2 占比 {share:.2%}（{a}/{b}）")

    # ---------- 3. 权重全 0 退化成均分 ----------
    drop(jwt)
    m = "sel-zero"
    create(jwt, "e2e-sel-z1", m, 0, 0, UP1)
    create(jwt, "e2e-sel-z2", m, 0, 0, UP2)
    time.sleep(3)
    relay(m, 100)
    a, b = counts(m)
    check("权重全 0 时均分（30-70 各半）", 30 <= a <= 70 and 30 <= b <= 70, f"up1={a} up2={b}")

    # ---------- 4. RetryTimes>0 时降级到下一优先级 ----------
    drop(jwt)
    set_retry(jwt, 2)
    m = "sel-failover"
    create(jwt, "e2e-sel-dead", m, 10, 1, DEAD)
    create(jwt, "e2e-sel-live", m, 0, 1, UP1)
    time.sleep(3)
    st, res = relay(m, 1)[0]
    a, _ = counts(m)
    check("最高优先级挂掉时仍返回 200", st == 200, f"HTTP {st} {str(res)[:120]}")
    check("请求确实降级到次优先级渠道", a == 1, f"up1={a}")

    # ---------- 5. 对照：RetryTimes=0 不降级 ----------
    drop(jwt)
    set_retry(jwt, 0)
    m = "sel-norety"
    create(jwt, "e2e-sel-dead2", m, 10, 1, DEAD)
    create(jwt, "e2e-sel-live2", m, 0, 1, UP1)
    time.sleep(3)
    st, res = relay(m, 1)[0]
    a, _ = counts(m)
    check("RetryTimes=0 时不降级（返回 5xx）", st >= 500, f"HTTP {st} {str(res)[:120]}")
    check("次优先级渠道没有被调用", a == 0, f"up1={a}")

    # ---------- 6. 全部渠道不可用：明确报错，不静默成功 ----------
    drop(jwt)
    st, res = call("POST", "/v1/chat/completions",
                   {"model": "sel-missing", "messages": [{"role": "user", "content": "x"}]},
                   "sk-" + RELAY)
    check("无可用渠道返回 5xx", st >= 500, f"HTTP {st} {str(res)[:100]}")

    drop(jwt)
    set_retry(jwt, 0)
    print("\n=== summary ===")
    print(f"{len(STEPS) - len(FAILS)}/{len(STEPS)} passed")
    for f in FAILS:
        print("  FAILED:", f)
    return 1 if FAILS else 0


if __name__ == "__main__":
    raise SystemExit(main())
