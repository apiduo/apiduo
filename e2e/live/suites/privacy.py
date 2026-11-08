#!/usr/bin/env python3
"""Privacy check: a normal user's own log view must never carry trace_id.

The jump link is admin-only. The frontend gate is a second lock; the first is
that /api/log/self strips admin_info. This drives a real request as a
non-admin and inspects exactly what that user's session receives.
"""
import json
import os
import urllib.error
import urllib.request

BASE = os.environ.get("E2E_BASE", "http://127.0.0.1:39410")
OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))
FAILS = []


def call(method, path, body=None, token=None, raw=False):
    data = json.dumps(body).encode() if body is not None else None
    hdrs = {"Content-Type": "application/json"}
    if token:
        hdrs["Authorization"] = "Bearer " + token
    req = urllib.request.Request(BASE + path, data=data, headers=hdrs, method=method)
    try:
        with OPENER.open(req, timeout=30) as r:
            payload = r.read()
            return r.status, (payload if raw else json.loads(payload))
    except urllib.error.HTTPError as e:
        payload = e.read()
        try:
            return e.code, json.loads(payload)
        except Exception:
            return e.code, payload.decode("utf-8", "replace")


def check(name, cond, detail=""):
    print(("  PASS  " if cond else "  FAIL  ") + name + (f"  [{detail}]" if detail else ""))
    if not cond:
        FAILS.append(name)


def main():
    st, res = call("POST", "/api/user/login", {"username": "root", "password": "E2ePassw0rd!2026"})
    root = res["data"]["access_token"]

    st, res = call("GET", "/api/user/?p=0&page_size=50", token=root)
    users = res.get("data") or []
    if isinstance(users, dict):
        users = users.get("items") or users.get("data") or []
    exists = any(u.get("username") == "e2e-plain" for u in users)
    if not exists:
        st, res = call("POST", "/api/user/", {
            "username": "e2e-plain",
            "password": "PlainUser!2026",
            "display_name": "E2E Plain",
            "quota": 500000000,
            "group": "default",
        }, token=root)
        check("create non-admin user", st == 200 and res.get("success"), f"HTTP {st} {str(res)[:100]}")

    # The create-user payload does not carry the wallet; top it up explicitly
    # or the relay answers 403 "insufficient quota" before any log is written.
    st, res = call("GET", "/api/user/?p=0&page_size=50", token=root)
    users = res.get("data") or []
    if isinstance(users, dict):
        users = users.get("items") or users.get("data") or []
    plain_id = next(u["id"] for u in users if u.get("username") == "e2e-plain")
    st, res = call("POST", "/api/user/manage",
                   {"id": plain_id, "action": "add_quota", "mode": "add", "value": 500000000}, token=root)
    # Idempotent-ish: a second add_quota on an already funded wallet can
    # answer "Invalid parameters", so this is informational, not a gate.
    print(f"  ....  top up non-admin wallet  [HTTP {st} success={res.get('success')}]")

    st, res = call("POST", "/api/user/login", {"username": "e2e-plain", "password": "PlainUser!2026"})
    check("non-admin login", st == 200 and res.get("success"), f"HTTP {st}")
    plain = res["data"]["access_token"]

    st, res = call("POST", "/api/token/", {
        "name": "plain-key", "remain_quota": 0, "unlimited_quota": True,
        "expired_time": -1, "group": "default",
    }, token=plain)
    st, res = call("GET", "/api/token/?p=0&page_size=10", token=plain)
    items = res.get("data") or []
    if isinstance(items, dict):
        items = items.get("items") or items.get("data") or []
    tok = next((t["id"] for t in items if t.get("name") == "plain-key"), None)
    check("non-admin token", tok is not None, f"id={tok}")
    st, res = call("POST", f"/api/token/{tok}/key", {}, token=plain)
    sk = "sk-" + res["data"]["key"]

    # A real relay request from the non-admin (creates a log row with a trace).
    import urllib.request as u
    for tp in ("", "00-aaaabbbbccccddddeeeeffff00001111-1111222233334444-01"):
        body = json.dumps({"model": "gpt-4o-mini", "stream": False,
                           "messages": [{"role": "user", "content": "plain user request"}]}).encode()
        hdrs = {"Content-Type": "application/json", "Authorization": "Bearer " + sk}
        if tp:
            hdrs["traceparent"] = tp
        req = u.Request(BASE + "/v1/chat/completions", data=body, headers=hdrs, method="POST")
        with OPENER.open(req, timeout=30) as r:
            check("non-admin relay ok", r.status == 200, f"HTTP {r.status}")

    # Admin view: trace_id present (control).
    st, res = call("GET", "/api/log/?p=0&page_size=10&type=0", token=root)
    rows = res.get("data") or []
    if isinstance(rows, dict):
        rows = rows.get("items") or rows.get("data") or []
    admin_traced = [r for r in rows
                    if ((_other(r) or {}).get("admin_info") or {}).get("trace_id")]
    check("admin view carries trace_id", bool(admin_traced), f"{len(admin_traced)} rows")

    # Non-admin self view: must be stripped.
    st, res = call("GET", "/api/log/self/?p=0&page_size=10&type=0", token=plain)
    mine = res.get("data") or []
    if isinstance(mine, dict):
        mine = mine.get("items") or mine.get("data") or []
    check("non-admin self view returns rows", bool(mine), f"{len(mine)} rows")
    leaks = []
    admin_blocks = 0
    for r in mine:
        other = _other(r) or {}
        if "admin_info" in other:
            admin_blocks += 1
        if (other.get("admin_info") or {}).get("trace_id"):
            leaks.append(r.get("request_id"))
        if "trace_id" in other:
            leaks.append("top-level:" + str(r.get("request_id")))
    check("admin_info stripped for non-admin", admin_blocks == 0, f"{admin_blocks} rows had admin_info")
    check("no trace_id leaks to non-admin", not leaks, str(leaks[:3]))

    # And the config endpoint (which reveals the internal host) is admin-only.
    st, res = call("GET", "/api/apiduo/trace/config", token=plain)
    check("config endpoint denies non-admin", st in (401, 403), f"HTTP {st}")

    print(f"\n{'PASS' if not FAILS else 'FAIL'}  {len(FAILS)} failed")
    return 1 if FAILS else 0


def _other(row):
    o = row.get("other")
    if isinstance(o, str):
        try:
            return json.loads(o)
        except Exception:
            return {}
    return o or {}


if __name__ == "__main__":
    raise SystemExit(main())
