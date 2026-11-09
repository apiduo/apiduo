#!/usr/bin/env python3
"""亲和规则的参数覆盖模板（param_override_template）：合并语义与生效边界。

三件事只有看上游报文才能确认：
1. 渠道自己设过的键，模板补不进去（合并是「只补空缺」，不是覆盖）
2. 模板里的 `operations` 在渠道静态值之后执行，所以它**能**改掉渠道的键
3. 规则没命中（这里用缺少 session 头）时模板完全不参与，渠道自身覆盖照常生效

所以断言全部读假上游记下的请求体，网关日志只用来核对 admin_info 里的标记。
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
BODIES = os.environ.get("E2E_BODIES", "/tmp/apiduo-e2e/upstream-bodies.log")

MODEL = "ovr-model"
CHANNEL_OVERRIDE = {"temperature": 0.11, "top_p": 0.22}
TEMPLATE = {
    "temperature": 0.99,
    "max_tokens": 123,
    "operations": [{"path": "temperature", "mode": "set", "value": 0.77}],
}

OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))
FAILS = []
STEPS = []
JWT = ""


def call(method, path, body=None, token=None, extra_headers=None):
    data = json.dumps(body).encode() if body is not None else None
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    if extra_headers:
        headers.update(extra_headers)
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


def set_policy(options):
    st, res = call("PATCH", "/api/option/request_policy", {"options": options}, JWT)
    if st != 200 or not res.get("success"):
        raise SystemExit(f"set policy failed: HTTP {st} {res}")
    st, res = call("GET", "/api/option/request_policy", None, JWT)
    return (res.get("data") or {}).get("options") or {}


def channels():
    st, res = call("GET", "/api/channel/?p=0&page_size=100", None, JWT)
    return (res.get("data") or {}).get("items") or []


def drop(prefix="e2e-ovr-"):
    for item in channels():
        if str(item.get("name", "")).startswith(prefix):
            call("DELETE", f"/api/channel/{item['id']}", None, JWT)


def create_channel(name, model, param_override):
    channel = {"name": name, "type": 1, "key": "sk-e2e-upstream", "base_url": UP1,
               "models": model, "group": "default", "status": 1, "priority": 0,
               "weight": 0, "auto_ban": 0,
               "param_override": json.dumps(param_override)}
    st, res = call("POST", "/api/channel/", {"mode": "single", "channel": channel}, JWT)
    if not (st == 200 and res.get("success")):
        raise SystemExit(f"create {name} failed: HTTP {st} {res}")
    time.sleep(2)


def relay(content, session=None):
    headers = {"X-Session": session} if session else None
    return call("POST", "/v1/chat/completions",
                {"model": MODEL, "messages": [{"role": "user", "content": content}]},
                "sk-" + RELAY, headers)


def last_body(model=MODEL):
    """假上游收到的、该模型最新一条请求体。"""
    try:
        with open(BODIES, encoding="utf-8") as fh:
            lines = fh.read().splitlines()
    except FileNotFoundError:
        return None, ""
    for line in reversed(lines):
        try:
            rec = json.loads(line)
        except Exception:
            continue
        if rec.get("model") != model:
            continue
        try:
            return json.loads(rec.get("body") or "{}"), rec.get("body") or ""
        except Exception:
            return None, rec.get("body") or ""
    return None, ""


def model_log_rows(model=MODEL, page_size=100):
    st, res = call("GET", f"/api/log/?p=0&page_size={page_size}&type=2", None, JWT)
    rows = (res.get("data") or {}).get("items") or []
    return [row for row in rows if row.get("model_name") == model]


def admin_info_of(row):
    other = row.get("other")
    if isinstance(other, str):
        try:
            other = json.loads(other)
        except Exception:
            other = {}
    return (other or {}).get("admin_info") or {}


def wait_admin_info(before, model=MODEL, tries=25, delay=0.2):
    """等本模型多出一条日志，再取最新那条的 admin_info。

    日志是异步批量落库的（writer 2s 一轮，空载也要 100-300ms），请求返回时这一行
    常常还不在表里。直接读第一页会拿到上一条，四条日志断言于是整体错位一格 ——
    单跑可能侥幸通过，全量跑（前面十几套压着日志写入）几乎必现。
    """
    rows = model_log_rows(model)
    for _ in range(tries):
        if len(rows) > before:
            return admin_info_of(rows[0])
        time.sleep(delay)
        rows = model_log_rows(model)
    return admin_info_of(rows[0]) if rows else {}


def main():
    global JWT
    if not RELAY:
        raise SystemExit("E2E_RELAY_KEY is required (run via run_all.sh)")
    JWT = admin_token()
    drop()
    set_option("performance_setting.monitor_disk_threshold", "99")
    set_option("SelfUseModeEnabled", "true")
    set_option("AutomaticDisableChannelEnabled", "false")

    create_channel("e2e-ovr-ch", MODEL, CHANNEL_OVERRIDE)
    st, res = call("GET", "/api/channel/?p=0&page_size=100", None, JWT)
    items = (res.get("data") or {}).get("items") or []
    ch = next((i for i in items if i.get("name") == "e2e-ovr-ch"), None)
    check("渠道参数覆盖已落库", bool(ch) and bool(ch.get("param_override")),
          str((ch or {}).get("param_override"))[:80])

    rule = json.dumps([{
        "name": "ovr", "model_regex": [MODEL], "path_regex": [".*"],
        "user_agent_include": [],
        "key_sources": [{"type": "request_header", "key": "X-Session"}],
        "value_regex": "", "ttl_seconds": 60,
        "param_override_template": TEMPLATE,
        "skip_retry_on_failure": False, "session_mode": "inherit",
        "include_using_group": False, "include_model_name": False, "include_rule_name": False,
    }])
    opts = set_policy({
        "channel_affinity_setting.enabled": "true",
        "channel_affinity_setting.rules": rule,
        "channel_affinity_setting.session_mode": "prefer",
        "channel_affinity_setting.max_entries": "1000",
    })
    check("亲和开关已开", opts.get("channel_affinity_setting.enabled") == "true",
          str(opts.get("channel_affinity_setting.enabled")))
    check("模板已写进规则", "max_tokens" in str(opts.get("channel_affinity_setting.rules")),
          str(opts.get("channel_affinity_setting.rules"))[:120])

    # ---------- 1. 规则命中：模板只补空缺，operations 才能改渠道值 ----------
    body, raw = last_body()
    before = len(open(BODIES, encoding="utf-8").read().splitlines()) if os.path.exists(BODIES) else 0
    logs_before = len(model_log_rows())
    st, res = relay("override hit", session="sess-ovr-1")
    body, raw = last_body()
    check("命中规则的请求 200", st == 200, f"HTTP {st} {str(res)[:100]}")
    check("上游确实收到了请求体", body is not None, raw[:80])
    body = body or {}
    check("模板补上空缺键（max_tokens=123）", body.get("max_tokens") == 123, str(body.get("max_tokens")))
    check("渠道自己的键不被模板覆盖（top_p=0.22）", body.get("top_p") == 0.22, str(body.get("top_p")))
    check("模板 operations 在渠道静态值之后执行（temperature=0.77）",
          body.get("temperature") == 0.77, str(body.get("temperature")))

    info = wait_admin_info(logs_before)
    aff = info.get("channel_affinity") or {}
    tpl = aff.get("override_template") or {}
    check("日志标记模板已应用", tpl.get("applied") is True, str(tpl))
    check("日志记下命中的规则名", tpl.get("rule_name") == "ovr", str(tpl.get("rule_name")))
    check("日志记下模板键数（3）", tpl.get("param_override_keys") == 3, str(tpl.get("param_override_keys")))

    # ---------- 2. 规则未命中：模板完全不参与，渠道覆盖照常 ----------
    logs_before = len(model_log_rows())
    st, res = relay("override miss")
    body, raw = last_body()
    check("未命中的请求也 200", st == 200, f"HTTP {st} {str(res)[:100]}")
    body = body or {}
    check("未命中时没有 max_tokens（模板没生效）", "max_tokens" not in body, str(body)[:140])
    check("未命中时渠道覆盖仍然生效（temperature=0.11）",
          body.get("temperature") == 0.11, str(body.get("temperature")))
    check("未命中时 top_p 仍是渠道值", body.get("top_p") == 0.22, str(body.get("top_p")))

    info = wait_admin_info(logs_before)
    aff = info.get("channel_affinity") or {}
    check("未命中时日志没有 override_template", "override_template" not in aff, str(aff)[:140])

    # ---------- 3. 关掉亲和：模板不再参与，渠道覆盖不变 ----------
    set_policy({"channel_affinity_setting.enabled": "false"})
    st, _ = relay("override disabled", session="sess-ovr-1")
    body, _ = last_body()
    body = body or {}
    check("关闭亲和后仍 200", st == 200, f"HTTP {st}")
    check("关闭亲和后模板不生效", "max_tokens" not in body, str(body)[:140])
    check("关闭亲和后渠道覆盖仍生效", body.get("temperature") == 0.11, str(body.get("temperature")))
    set_policy({"channel_affinity_setting.enabled": "false", "channel_affinity_setting.rules": "[]"})

    drop()
    print("\n=== summary ===")
    print(f"{len(STEPS) - len(FAILS)}/{len(STEPS)} passed")
    for f in FAILS:
        print("  FAILED:", f)
    return 1 if FAILS else 0


if __name__ == "__main__":
    raise SystemExit(main())
