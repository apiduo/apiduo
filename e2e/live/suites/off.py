#!/usr/bin/env python3
"""Default-off equivalence: tracing disabled must leave no trace behind.

Three separate claims are checked, because "off" has three surfaces:
  1. nothing is exported (no OTLP POST reaches the sink)
  2. no trace_id is written into log metadata (no dangling jump links)
  3. the outbound request is byte-identical — traceparent is NOT forwarded,
     even when the client sent one

Claim 3 is the one the decoupling rules care about most: with the feature off
the gateway must behave exactly like upstream new-api.
"""
import json
import os
import urllib.error
import urllib.request

BASE = os.environ.get("E2E_BASE", "http://127.0.0.1:39440")
SINK = os.environ.get("E2E_SINK", "http://127.0.0.1:39402")
HEADER_LOG = os.environ.get("E2E_HEADERS", "/tmp/apiduo-e2e/upstream-headers.log")
OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))
FAILS = []


def call(method, path, body=None, token=None, raw=False):
    data = json.dumps(body).encode() if body is not None else None
    hdrs = {"Content-Type": "application/json"}
    if token:
        hdrs["Authorization"] = "Bearer " + token
    url = path if path.startswith("http") else BASE + path
    req = urllib.request.Request(url, data=data, headers=hdrs, method=method)
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
    if os.path.exists(HEADER_LOG):
        os.remove(HEADER_LOG)
    before = int(call("GET", SINK + "/count", raw=True)[1])

    st, res = call("POST", "/api/user/login", {"username": "root", "password": "E2ePassw0rd!2026"})
    root = res["data"]["access_token"]
    st, res = call("GET", "/api/token/?p=0&page_size=10", token=root)
    items = res.get("data") or []
    if isinstance(items, dict):
        items = items.get("items") or items.get("data") or []
    tok = next(t["id"] for t in items if t.get("name") == "e2e-relay")
    st, res = call("POST", f"/api/token/{tok}/key", {}, token=root)
    sk = "sk-" + res["data"]["key"]

    tp = "00-1234567890abcdef1234567890abcdef-fedcba9876543210-01"
    body = json.dumps({"model": "gpt-4o-mini", "stream": False,
                       "messages": [{"role": "user", "content": "disabled mode"}]}).encode()
    req = urllib.request.Request(BASE + "/v1/chat/completions", data=body, method="POST",
                                 headers={"Content-Type": "application/json",
                                          "Authorization": "Bearer " + sk,
                                          "traceparent": tp})
    with OPENER.open(req, timeout=30) as r:
        check("relay works with tracing off", r.status == 200, f"HTTP {r.status}")
        rid = r.headers.get("X-Oneapi-Request-Id") or ""
    check("request id issued", bool(rid), rid)

    # 日志写入到可查有 100-300ms 延迟：按 request_id 轮询收敛，别取 rows[0]
    import time as _t
    leaked = []
    seen = False
    deadline = _t.time() + 5
    while _t.time() < deadline and not seen:
        st, res = call("GET", "/api/log/?p=0&page_size=10&type=0", token=root)
        rows = res.get("data") or []
        if isinstance(rows, dict):
            rows = rows.get("items") or rows.get("data") or []
        for r in rows:
            if r.get("request_id") != rid:
                continue
            seen = True
            o = r.get("other")
            if isinstance(o, str):
                try:
                    o = json.loads(o)
                except Exception:
                    o = {}
            if ((o or {}).get("admin_info") or {}).get("trace_id"):
                leaked.append(r.get("request_id"))
        if not seen:
            _t.sleep(0.3)
    check("our row is in the log table", seen, rid)
    check("no trace_id written when off", not leaked, f"{len(leaked)} rows")

    got = int(call("GET", SINK + "/count", raw=True)[1])
    check("nothing exported when off", got == before, f"sink {before} -> {got}")

    with open(HEADER_LOG) as f:
        lines = [ln.strip() for ln in f if ln.strip()]
    check("upstream saw the request", bool(lines), f"{len(lines)} calls")
    forwarded = [ln for ln in lines if "traceparent=\"\"" not in ln]
    check("traceparent NOT forwarded when off", not forwarded, str(forwarded[:2]))

    # Control: with tracing on (previous runs) the same header was forwarded.
    print("\nPASS" if not FAILS else f"\nFAIL {FAILS}")
    return 1 if FAILS else 0


if __name__ == "__main__":
    raise SystemExit(main())
