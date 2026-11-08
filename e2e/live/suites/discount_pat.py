#!/usr/bin/env python3
"""折扣 / PAT 作用域 / 审计端到端。

覆盖：折扣规则落库与生效（首条命中、不相乘）、按次定价对账、审计 before/after、
PAT 声明式作用域（含未声明路由 fail-closed）。"""
import json
import os
import time
import urllib.error
import urllib.request

BASE = os.environ.get("E2E_BASE", "http://127.0.0.1:39420")
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
        with OPENER.open(req, timeout=30) as r:
            raw = r.read()
            try:
                return r.status, json.loads(raw or b"{}")
            except Exception:
                return r.status, raw.decode("utf-8", "replace")
    except urllib.error.HTTPError as e:
        raw = e.read()
        try:
            return e.code, json.loads(raw or b"{}")
        except Exception:
            return e.code, raw.decode("utf-8", "replace")


def check(name, cond, detail=""):
    STEPS.append((name, bool(cond)))
    if not cond:
        FAILS.append(f"{name}: {detail}")
    print(("  PASS  " if cond else "  FAIL  ") + name + (f"  [{detail}]" if detail else ""))


def login(user, pw):
    st, res = call("POST", "/api/user/login", {"username": user, "password": pw})
    return res.get("data", {}).get("access_token") if isinstance(res, dict) else None


def main():
    admin = login(os.environ.get("E2E_ADMIN", "admin"), os.environ.get("E2E_ADMIN_PW", "apiduo-test-2026"))
    check("admin 登录", bool(admin))

    # 磁盘水位：本地盘常年 97%+，不放开会被 503 全站拦掉
    call("PUT", "/api/option/", {"key": "performance_setting.monitor_disk_threshold", "value": "99"}, admin)
    st, res = call("GET", "/api/option/", None, admin)
    thr = {d.get("key"): d.get("value") for d in (res.get("data") or []) if isinstance(d, dict)}
    check("磁盘阈值已放宽", thr.get("performance_setting.monitor_disk_threshold") == "99",
          f"got {thr.get('performance_setting.monitor_disk_threshold')}")

    # 关掉自用模式，定价才有意义；给测试模型一个确定的按次价格 0.02
    call("PUT", "/api/option/", {"key": "SelfUseModeEnabled", "value": "false"}, admin)
    st, res = call("GET", "/api/option/", None, admin)
    opts = {d.get("key"): d.get("value") for d in (res.get("data") or []) if isinstance(d, dict)}
    prices = json.loads(opts.get("ModelPrice") or "{}")
    prices["disc-model"] = 0.02
    st, res = call("PUT", "/api/option/", {"key": "ModelPrice", "value": json.dumps(prices)}, admin)
    check("定价写入成功", st == 200 and res.get("success"), f"HTTP {st}")

    # ---------- 1. 建测试用户 ----------
    uname, upass = "e2edisc", "E2eDiscPass!2026"
    st, res = call("POST", "/api/user/register",
                   {"username": uname, "password": upass, "password2": upass,
                    "email": "e2edisc@example.com", "aff_code": ""})
    if not (st == 200 and res.get("success")):
        # 重跑时用户已存在，属于幂等场景
        check("注册普通用户（已存在可复用）", "already exists" in str(res.get("message", "")), f"{str(res)[:120]}")

    # 给额度
    st, res = call("GET", "/api/user/?p=1&page_size=100", None, admin)
    items = (res.get("data") or {}).get("items") or []
    uid = next((u["id"] for u in items if u.get("username") == uname), None)
    check("能查到测试用户", uid is not None, f"id={uid}")
    st, res = call("POST", "/api/user/manage",
                   {"id": uid, "action": "add_quota", "mode": "add", "value": 5000000}, admin)
    check("给用户加额度", st == 200 and res.get("success"), f"HTTP {st} {str(res)[:100]}")

    # ---------- 2. 建渠道 ----------
    chname = "e2e-disc-channel"
    st, res = call("GET", "/api/channel/?p=0&page_size=100", None, admin)
    for it in (res.get("data") or {}).get("items") or []:
        if it.get("name") == chname:
            call("DELETE", f"/api/channel/{it['id']}", None, admin)
    settings = {"primary_endpoint": "/v1/images/generations",
                "image_protocol": {"enabled": True, "upstream": "seedream"},
                "advanced_custom": {"advanced_routes": [{"incoming_path": "/v1/images/edits"}]}}
    st, res = call("POST", "/api/channel/", {"mode": "single", "channel": {
        "name": chname, "type": 58, "key": "sk-e2e", "base_url": os.environ.get("E2E_UPSTREAM", "http://127.0.0.1:39403"),
        "models": "disc-model", "group": "default", "priority": 0, "weight": 0, "status": 1,
        "settings": json.dumps(settings)}}, admin)
    check("建测试渠道", st == 200 and res.get("success"), f"HTTP {st} {str(res)[:120]}")
    time.sleep(3)

    # ---------- 3. 用户令牌 + 基准价 ----------
    utok = login(uname, upass)
    check("普通用户登录", bool(utok))
    call("POST", "/api/token/", {"name": "disc", "remain_quota": 0, "unlimited_quota": True,
                                 "expired_time": -1, "group": "default"}, utok)
    st, res = call("GET", "/api/token/?p=0&page_size=10", None, utok)
    titems = res["data"]["items"] if isinstance(res.get("data"), dict) else res.get("data")
    tid = next((t["id"] for t in titems if t.get("name") == "disc"), None)
    st, res = call("POST", f"/api/token/{tid}/key", None, utok)
    rkey = res["data"]["key"] if isinstance(res.get("data"), dict) else res.get("data")
    check("拿到中继 key", bool(rkey))

    def gen(tag):
        st, res = call("POST", "/v1/images/generations",
                       {"model": "disc-model", "prompt": tag}, "sk-" + str(rkey))
        return st, res

    def rows_now(n=5):
        st, res = call("GET", "/api/log/?p=0&page_size=%d&type=2" % n, None, admin)
        return res["data"]["items"] if isinstance(res.get("data"), dict) else (res.get("data") or [])

    def newest_id():
        rows = rows_now(1)
        return rows[0]["id"] if rows else 0

    def wait_new_row(prev_id, timeout=5.0):
        """日志写入与查询之间有短暂可见性延迟，按 id 收敛，别拿 rows[0] 猜。"""
        deadline = time.time() + timeout
        while time.time() < deadline:
            rows = rows_now(3)
            for r in rows:
                if int(r.get("id") or 0) > int(prev_id or 0):
                    return r
            time.sleep(0.2)
        return None

    def gen_and_quota(tag):
        prev = newest_id()
        st, res = gen(tag)
        if st != 200:
            return None, st, res
        return wait_new_row(prev), st, res

    # 先清掉上一轮残留的折扣，基准价才有意义
    call("PUT", f"/api/user/{uid}/discounts", {"rules": []}, admin)

    row, st, res = gen_and_quota("base")
    check("基准请求 200", st == 200, f"HTTP {st} {str(res)[:120]}")
    base_quota = row.get("quota") if row else None
    check("基准扣费 = 0.02 * 500000", base_quota == 10000, f"quota={base_quota}")

    # ---------- 4. 折扣：覆盖式写入 + 生效 ----------
    st, res = call("PUT", f"/api/user/{uid}/discounts",
                   {"rules": [{"groups": "*", "models": "*", "rate": 0.5}]}, admin)
    check("写入折扣 0.5", st == 200 and res.get("success"), f"HTTP {st} {str(res)[:120]}")

    st, res = call("GET", f"/api/user/{uid}/discounts", None, admin)
    rules = (res.get("data") or {}).get("rules") or []
    check("回读折扣规则", len(rules) == 1 and abs(float(rules[0].get("rate")) - 0.5) < 1e-9, json.dumps(rules)[:120])
    check("返回可用分组", bool((res.get("data") or {}).get("usable_groups")), str((res.get("data") or {}).get("usable_groups"))[:80])

    row, st, _ = gen_and_quota("discounted")
    check("折扣后扣费 = 5000", row and row.get("quota") == 5000, f"quota={(row or {}).get('quota')}")

    # 两条规则：首条命中即停，且不相乘
    st, res = call("PUT", f"/api/user/{uid}/discounts",
                   {"rules": [{"groups": "*", "models": "disc-model", "rate": 0.4},
                              {"groups": "*", "models": "*", "rate": 0.9}]}, admin)
    check("写入两条规则", st == 200 and res.get("success"), f"HTTP {st}")
    row, st, _ = gen_and_quota("first-match")
    check("首条命中且不相乘（0.4 -> 4000）", row and row.get("quota") == 4000, f"quota={(row or {}).get('quota')}")

    # 清空折扣
    st, res = call("PUT", f"/api/user/{uid}/discounts", {"rules": []}, admin)
    check("清空折扣", st == 200 and res.get("success"), f"HTTP {st}")
    row, st, _ = gen_and_quota("cleared")
    check("清空后回到原价", row and row.get("quota") == 10000, f"quota={(row or {}).get('quota')}")

    # ---------- 5. 审计 ----------
    st, res = call("GET", "/api/audit/?p=0&page_size=20", None, admin)
    logs = (res.get("data") or {}).get("items") or []
    disc = [l for l in logs if l.get("action") == "user.discount_update"]
    check("审计记录 user.discount_update", len(disc) >= 3, f"got {len(disc)}")
    if disc:
        row0 = disc[0]
        other = row0.get("other") or {}
        op = other.get("op") or other
        params = op.get("params") if isinstance(op, dict) else None
        check("审计含 before/after",
              isinstance(params, dict) and ("before" in params or "after" in params),
              json.dumps(params, ensure_ascii=False)[:140])
        check("审计含操作人", bool(row0.get("username")) and bool(row0.get("user_id")),
              f"{row0.get('username')} / {row0.get('user_id')}")
        check("审计标了鉴权方式", row0.get("auth_method") in ("session", "access_token"),
              str(row0.get("auth_method")))

    # ---------- 6. PAT 作用域 ----------
    st, res = call("POST", "/api/user/access_tokens",
                   {"name": "e2e-pat", "scopes": ["user:read", "user:write", "log:read"],
                    "expires_at": 0}, admin)
    pat_body = res
    if st != 200 or not (res.get("success") if isinstance(res, dict) else False):
        # 需要二次验证：先拿 proof_token
        st2, proof = call("POST", "/api/verify",
                          {"method": "password", "scope": "access_token.generate",
                           "context": {"scopes": ["user:read", "user:write", "log:read"], "expires_at": 0},
                           "password": os.environ.get("E2E_ADMIN_PW", "apiduo-test-2026")}, admin)
        ptoken = (proof.get("data") or {}).get("proof_token") if isinstance(proof, dict) else None
        check("拿到二次验证 proof", bool(ptoken), f"HTTP {st2} {str(proof)[:100]}")
        st, res = call("POST", "/api/user/access_tokens",
                       {"name": "e2e-pat", "scopes": ["user:read", "user:write", "log:read"],
                        "expires_at": 0}, admin,
                       headers={"X-Security-Proof": ptoken} if ptoken else None)
    pat = (res.get("data") or {}).get("token") or (res.get("data") or {}).get("key") if isinstance(res, dict) else None
    check("创建 PAT", st == 200 and bool(pat), f"HTTP {st} {str(res)[:160]}")

    if pat:
        pat = str(pat)
        st, res = call("GET", f"/api/user/{uid}/discounts", None, pat)
        check("PAT 读折扣（user:read）", st == 200 and res.get("success"), f"HTTP {st} {str(res)[:100]}")
        st, res = call("PUT", f"/api/user/{uid}/discounts",
                       {"rules": [{"groups": "*", "models": "*", "rate": 0.7}]}, pat)
        check("PAT 写折扣（user:write）", st == 200 and res.get("success"), f"HTTP {st} {str(res)[:100]}")
        st, res = call("GET", "/api/apiduo/trace/config", None, pat)
        check("PAT 读 trace 配置（log:read）", st == 200 and res.get("success"), f"HTTP {st} {str(res)[:100]}")
        # 未声明路由必须 fail-closed
        st, res = call("GET", "/api/channel/?p=0&page_size=1", None, pat)
        check("PAT 打未声明路由 -> 403", st == 403, f"HTTP {st} {str(res)[:120]}")
        st, res = call("GET", "/api/audit/?p=0&page_size=1", None, pat)
        check("PAT 打未声明路由（审计）-> 403", st == 403, f"HTTP {st} {str(res)[:120]}")

        # 越权：只读 scope 不能写
        st, res = call("POST", "/api/user/access_tokens",
                       {"name": "e2e-readonly", "scopes": ["user:read"], "expires_at": 0}, admin)
        ro = (res.get("data") or {}).get("token") if isinstance(res, dict) else None
        if ro:
            st, res = call("PUT", f"/api/user/{uid}/discounts", {"rules": []}, str(ro))
            check("只读 scope 写折扣 -> 403", st == 403, f"HTTP {st} {str(res)[:100]}")

    print("\n=== summary ===")
    print(f"{len(STEPS) - len(FAILS)}/{len(STEPS)} passed")
    for f in FAILS:
        print("  FAILED:", f)
    return 1 if FAILS else 0


if __name__ == "__main__":
    raise SystemExit(main())
