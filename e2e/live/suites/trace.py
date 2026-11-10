#!/usr/bin/env python3
"""trace 端到端：真 relay 流量 → 假上游 → 假 Langfuse OTLP sink。

断言三件事：span 形状（usage/输入输出/失败态）、日志里的 trace_id 与 span 一一对应、
客户端 traceparent 能被采纳成同一条 trace。
"""
import hashlib
import json
import os
import sys
import time
import urllib.error
import urllib.request

BASE = os.environ.get("E2E_BASE", "http://127.0.0.1:39410")
SINK = os.environ.get("E2E_SINK", "http://127.0.0.1:39402")
SINK_DIR = os.environ.get("E2E_SINK_DIR", "/tmp/apiduo-e2e/sink")
# Channel base_url is resolved inside the gateway container, so on a remote
# run this is a docker network name, not loopback.
UPSTREAM = os.environ.get("E2E_UPSTREAM", "http://127.0.0.1:39401")
PROJECT_ID = os.environ.get("E2E_PROJECT_ID", "e2e-project")
HOST_HINT = os.environ.get("E2E_HOST_HINT", "39402")

# Never let an env proxy intercept loopback calls.
OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))

FAILS = []
STEPS = []


def call(method, path, body=None, token=None, raw=False, headers=None):
    url = path if path.startswith("http") else BASE + path
    data = None
    hdrs = {"Content-Type": "application/json"}
    if body is not None:
        data = json.dumps(body).encode()
    if token:
        hdrs["Authorization"] = "Bearer " + token
    if headers:
        hdrs.update(headers)
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
    STEPS.append((name, bool(cond), detail))
    if not cond:
        FAILS.append(f"{name}: {detail}")
    print(("  PASS  " if cond else "  FAIL  ") + name + (f"  [{detail}]" if detail else ""))


def main():
    # ---------- 1. login ----------
    st, res = call("POST", "/api/user/login",
                   {"username": "root", "password": "E2ePassw0rd!2026"})
    check("admin login", st == 200 and res.get("success"), f"HTTP {st}")
    token = res["data"]["access_token"]

    # ---------- 2. channel ----------
    st, res = call("GET", "/api/channel/?p=0&page_size=10", token=token)
    chans = res.get("data") or []
    if isinstance(chans, dict):
        chans = chans.get("items") or chans.get("data") or []
    chan_id = None
    for c in chans:
        if c.get("name") == "e2e-openai":
            chan_id = c["id"]
    if chan_id is None:
        st, res = call("POST", "/api/channel/", {
            "mode": "single",
            "channel": {
                "name": "e2e-openai",
                "type": 1,
                "key": "sk-upstream-fake",
                "base_url": UPSTREAM,
                "models": "gpt-4o-mini",
                "group": "default",
                "status": 1,
            },
        }, token=token)
        check("create channel", st == 200 and res.get("success"), f"HTTP {st} {res}")
        st, res = call("GET", "/api/channel/?p=0&page_size=10", token=token)
        chans = res.get("data") or []
        if isinstance(chans, dict):
            chans = chans.get("items") or chans.get("data") or []
        for c in chans:
            if c.get("name") == "e2e-openai":
                chan_id = c["id"]
    check("channel exists", chan_id is not None, f"id={chan_id}")

    st, res = call("GET", f"/api/channel/test/{chan_id}?model=gpt-4o-mini", token=token)
    check("channel test hits fake upstream", st == 200, f"HTTP {st} {str(res)[:120]}")

    # The channel test writes a consume log from a request the relay never saw
    # (gin.CreateTestContext). It used to land with an empty request_id, so the
    # log page had nothing to derive a trace from and the row was unopenable.
    # Pin both halves here: an id on the row, and the trace id derived from it.
    chan_rid = chan_tid = ""
    deadline = time.time() + 10
    while time.time() < deadline and not chan_tid:
        st, res = call("GET", "/api/log/?p=0&page_size=20&type=2", token=token)
        rows = res.get("data") or []
        if isinstance(rows, dict):
            rows = rows.get("items") or rows.get("data") or []
        for r in rows:
            if r.get("token_name") != "Model test":
                continue
            other = r.get("other")
            if isinstance(other, str):
                try:
                    other = json.loads(other)
                except Exception:
                    other = {}
            tid = ((other or {}).get("admin_info") or {}).get("trace_id") or ""
            if r.get("request_id") and tid:
                chan_rid, chan_tid = r["request_id"], tid
                break
        if not chan_tid:
            time.sleep(0.3)
    check("channel test log carries a request id", bool(chan_rid), chan_rid or "empty")
    expected = hashlib.sha256(("apiduo-trace\x00" + chan_rid).encode()).hexdigest()[:32]
    check("channel test log points at the trace derived from it",
          bool(chan_rid) and chan_tid == expected, f"{chan_tid or '-'} vs {expected}")

    # ---------- 3. token ----------
    st, res = call("POST", "/api/token/", {
        "name": "e2e-relay",
        "remain_quota": 0,
        "unlimited_quota": True,
        "expired_time": -1,
        "model_limits_enabled": False,
        "group": "default",
    }, token=token)
    st, res = call("GET", "/api/token/?p=0&page_size=10", token=token)
    items = res.get("data") or []
    if isinstance(items, dict):
        items = items.get("items") or items.get("data") or []
    tok_id = None
    for t in items:
        if t.get("name") == "e2e-relay":
            tok_id = t["id"]
    check("relay token exists", tok_id is not None, f"id={tok_id}")
    st, res = call("POST", f"/api/token/{tok_id}/key", {}, token=token)
    raw = (res.get("data") or {}).get("key") if isinstance(res.get("data"), dict) else res.get("data")
    # The dashboard returns the bare key; clients send it with the sk- prefix.
    sk = raw if str(raw).startswith("sk-") else "sk-" + str(raw)
    check("relay key readable", bool(raw), f"HTTP {st}")

    # ---------- 4. traffic ----------
    before = int(call("GET", SINK + "/count", raw=True)[1])

    seen_rids = []

    def chat(stream, text, traceparent=None, model="gpt-4o-mini"):
        body = {
            "model": model,
            "messages": [{"role": "user", "content": text}],
            "stream": stream,
        }
        hdrs = {"Authorization": "Bearer " + sk, "Content-Type": "application/json"}
        if traceparent:
            hdrs["traceparent"] = traceparent
        req = urllib.request.Request(BASE + "/v1/chat/completions",
                                     data=json.dumps(body).encode(), headers=hdrs, method="POST")
        try:
            with OPENER.open(req, timeout=60) as r:
                st, payload = r.status, r.read()
                seen_rids.append(r.headers.get("X-Oneapi-Request-Id") or "")
        except urllib.error.HTTPError as e:
            st, payload = e.code, e.read()
            seen_rids.append(e.headers.get("X-Oneapi-Request-Id") or "")
        return st, payload

    st1, body1 = chat(False, "用三句话解释 OTEL")
    check("non-stream relay 200", st1 == 200, f"HTTP {st1}")
    nonstream = json.loads(body1) if isinstance(body1, bytes) else body1
    check("non-stream body is completion",
          nonstream.get("choices", [{}])[0].get("message", {}).get("content", "").startswith("北京明天"),
          str(nonstream)[:90])

    st2, body2 = chat(True, "写一首七言绝句")
    text2 = body2.decode() if isinstance(body2, bytes) else str(body2)
    check("stream relay 200", st2 == 200, f"HTTP {st2}")
    check("stream emitted SSE frames", "data:" in text2 and "[DONE]" in text2,
          f"{len(text2)} bytes")

    TP = "4bf92f3577b34da6a3ce929d0e0e4736"
    SP = "00f067aa0ba902b7"
    st3, body3 = chat(True, "带 traceparent 的请求", traceparent=f"00-{TP}-{SP}-01")
    check("traceparent relay 200", st3 == 200, f"HTTP {st3}")

    st4, body4 = chat(True, "fail please")
    check("upstream 500 surfaces as error", st4 >= 400, f"HTTP {st4}")

    # ---------- 5. sink ----------
    deadline = time.time() + 20
    total = before
    while time.time() < deadline:
        total = int(call("GET", SINK + "/count", raw=True)[1])
        if total - before >= 2:
            break
        time.sleep(0.3)
    check("sink received OTLP exports", total > before, f"{total - before} batches")

    spans = []
    for fn in sorted(os.listdir(SINK_DIR)):
        if not fn.endswith(".json"):
            continue
        with open(os.path.join(SINK_DIR, fn)) as f:
            doc = json.load(f)
        for rs in doc.get("resourceSpans", []):
            for ss in rs.get("scopeSpans", []):
                spans.extend(ss.get("spans", []))
    print(f"  ---- {len(spans)} spans received ----")
    for sp in spans:
        print("   ", sp.get("traceId"), sp.get("spanId"), sp.get("parentSpanId") or "-",
              _attrs(sp).get("langfuse.observation.model.name", "?"),
              _attrs(sp).get("langfuse.observation.level", "?"))

    def by_attr(key, val):
        return [s for s in spans if _attrs(s).get(key) == val]

    # a) 4 spans, one per request
    check("one span per request", len(spans) >= 4, f"got {len(spans)}")

    # b) parsed output: text + tool_calls + finish_reason
    outs = [_attrs(s).get("langfuse.observation.output", "") for s in spans]
    # Non-stream responses travel a different carrier (respBody, not SSE
    # frames); a missing output here means the whole completion was dropped.
    nonstream_out = [o for o in outs if "北京明天多云转晴" in o and "tool_calls" not in o]
    check("non-stream completion captured as output", bool(nonstream_out),
          (nonstream_out or [""])[0][:60])
    stream_out = [o for o in outs if "北京明天多云转晴" in o and "tool_calls" in o]
    check("stream output parsed into text+tool_calls", bool(stream_out), "")
    if stream_out:
        parsed = json.loads(stream_out[0])
        check("parsed text has no raw SSE envelope",
              "data:" not in stream_out[0] and parsed.get("text", "").startswith("北京明天多云转晴"),
              stream_out[0][:80])
        check("finish_reason captured", parsed.get("finish_reason") == "stop",
              str(parsed.get("finish_reason")))
        tc = parsed.get("tool_calls") or []
        check("tool_call name+arguments reassembled",
              tc and tc[0].get("name") == "weather_lookup" and "北京" in (tc[0].get("arguments") or ""),
              json.dumps(tc, ensure_ascii=False)[:100])

    # c) redaction
    leaks = [o for o in outs if "sk-live-SECRETVALUE0123456789" in o]
    check("api key redacted in exported content", not leaks,
          f"{len(leaks)} span(s) still leak the key")
    redacted = [o for o in outs if "REDACTED:api_key" in o]
    check("redaction marker present", bool(redacted), "")

    # d) traceparent honored
    tp_spans = [s for s in spans if s.get("traceId") == TP]
    check("client traceparent adopted as traceId", bool(tp_spans), TP)
    if tp_spans:
        check("parentSpanId set from traceparent",
              tp_spans[0].get("parentSpanId") == SP, str(tp_spans[0].get("parentSpanId")))

    # e) failure path
    err_spans = by_attr("langfuse.observation.level", "ERROR")
    check("failed request still produced a span", bool(err_spans), f"{len(err_spans)}")
    if err_spans:
        a = _attrs(err_spans[0])
        check("failure span carries status message",
              "500" in (a.get("langfuse.observation.status_message", "") or ""),
              a.get("langfuse.observation.status_message", "")[:80])
        check("failure span keeps the request body (what did we send)",
              "fail please" in (a.get("langfuse.observation.input", "") or ""),
              (a.get("langfuse.observation.input", "") or "")[:70])
        check("failure span sets OTLP status message",
              "500" in str(err_spans[0].get("status", {}).get("message", "")),
              str(err_spans[0].get("status", {}))[:80])
        check("failure span has no fake usage",
              not a.get("gen_ai.usage.input_tokens"), str(a.get("gen_ai.usage.input_tokens")))

    # f) parent span id absent for client-less requests
    plain = [s for s in spans if not s.get("parentSpanId") and s.get("traceId") != TP]
    check("derived spans have no parentSpanId", bool(plain), f"{len(plain)}")

    # The settlement-style span for the channel test: no collector existed, so
    # the fallback snapshot is the only chance to show this request in Langfuse.
    check("channel test produced a span the log row points at",
          bool(chan_tid) and any(s.get("traceId") == chan_tid for s in spans),
          chan_tid or "no trace id")

    # ---------- 6. log API ----------
    st, res = call("GET", "/api/log/?p=0&page_size=20&type=0", token=token)
    rows = res.get("data") or []
    if isinstance(rows, dict):
        rows = rows.get("items") or rows.get("data") or []
    check("log rows written", len(rows) >= 4, f"{len(rows)} rows")

    # Join direction that matters: for each span we emitted, the log row with
    # the same request_id must carry that span's trace id. Driving the assert
    # from spans (not from whatever rows the table happens to hold) keeps it
    # immune to log rows left by earlier runs.
    rows_by_req = {}
    for r in rows:
        other = r.get("other")
        if isinstance(other, str):
            try:
                other = json.loads(other)
            except Exception:
                other = {}
        tid = ((other or {}).get("admin_info") or {}).get("trace_id")
        if tid:
            rows_by_req[r.get("request_id")] = tid
    check("log rows carry admin_info.trace_id", len(rows_by_req) >= 3, f"{len(rows_by_req)} rows")

    span_by_req = {}
    for sp in spans:
        rid = _attrs(sp).get("apiduo.request_id")
        if rid:
            span_by_req[rid] = sp
    matched = 0
    mismatched = []
    for rid, sp in span_by_req.items():
        if sp.get("status", {}).get("code") == 2:
            continue  # failed requests write no consume log by design
        if rows_by_req.get(rid) == sp.get("traceId"):
            matched += 1
        else:
            mismatched.append((rid, rows_by_req.get(rid), sp.get("traceId")))
    check("every success span has a log row pointing at it", matched >= 3, f"{matched} matched")
    check("no log row points at the wrong trace", not mismatched, str(mismatched[:3]))
    check("traceparent request log id == client trace id",
          any(tid == TP for tid in rows_by_req.values()), f"{list(rows_by_req.values())[:4]}")

    # ---------- 7. config endpoint ----------
    st, res = call("GET", "/api/apiduo/trace/config", token=token)
    cfg = res.get("data") if isinstance(res.get("data"), dict) else res
    check("trace config endpoint reachable", st == 200 and res.get("success"), f"HTTP {st} {res}")
    check("config exposes host+project_id (frontend contract)",
          HOST_HINT in str(cfg.get("host", "")) and cfg.get("project_id") == PROJECT_ID,
          json.dumps(cfg)[:140])
    check("config never leaks secret key",
          os.environ.get("E2E_SECRET_KEY", "sk-lf-e2e") not in json.dumps(cfg), "")

    st, res = call("GET", "/api/apiduo/trace/config")
    check("config requires auth", st in (401, 403), f"HTTP {st}")

    # ---------- summary ----------
    # Hand the request ids to whatever verifies the downstream (real Langfuse).
    print("REQUEST_IDS=" + json.dumps([r for r in seen_rids if r]))

    print("\n=== summary ===")
    print(f"{len(STEPS) - len(FAILS)}/{len(STEPS)} passed")
    for f in FAILS:
        print("  FAILED:", f)
    return 1 if FAILS else 0


def _attrs(span):
    out = {}
    for a in span.get("attributes") or []:
        v = a.get("value") or {}
        out[a.get("key")] = (v.get("stringValue") or v.get("intValue")
                             or v.get("boolValue") or v.get("doubleValue"))
    return out


if __name__ == "__main__":
    sys.exit(main())
