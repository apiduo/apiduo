#!/usr/bin/env python3
"""高级异步（type 8001）与方舟任务别名：专测保存期校验、计费配置落库、别名路由归属。

为什么要单独一套：
- 这套配置有 12 个必填/可选字段，任何一处填错都只会在「出任务那一刻」才炸，
  而 e2e 里根本走不到那一刻（没有真任务上游）；
- 别名入口 `/v1/contents/generations/tasks` 与插件的 `/api/v3/...` 是两条路，
  归属判定错了会静默把请求交给插件，客户端只看到一句不相干的 404。

本套**不提交任何任务**（不扣费、不出站）：只建/改渠道配置，再用一个没有渠道
认领的模型去打两条入口，确认「路由存在且交给了中继」。
"""

import json
import os

import httpx

BASE = os.environ.get("E2E_BASE", "http://127.0.0.1:39420")
RELAY = os.environ.get("E2E_RELAY_KEY", "")
ADMIN = (
    os.environ.get("E2E_ADMIN", "admin"),
    os.environ.get("E2E_ADMIN_PW", "apiduo-test-2026"),
)
UPSTREAM = os.environ.get("E2E_UPSTREAM", "http://127.0.0.1:39403")

rows = []


def check(name, ok, detail=""):
    rows.append((ok, name, detail))
    print(("PASS " if ok else "FAIL ") + name + ((" | " + detail) if detail else ""))


def admin():
    with httpx.Client(trust_env=False, timeout=30) as c:
        r = c.post(f"{BASE}/api/user/login", json={"username": ADMIN[0], "password": ADMIN[1]})
        r.raise_for_status()
        return r.json()["data"]["access_token"]


def api(at, method, path, **kw):
    with httpx.Client(trust_env=False, timeout=60) as c:
        return c.request(method, f"{BASE}{path}", headers={"Authorization": f"Bearer {at}"}, **kw)


def channels(at):
    r = api(at, "GET", "/api/channel/?p=0&page_size=100")
    return {it["name"]: it for it in r.json()["data"]["items"]}


def drop(at, names):
    table = channels(at)
    for name in names:
        if name in table:
            api(at, "DELETE", f"/api/channel/{table[name]['id']}")


def create(at, name, model, config, channel_type=8001):
    body = {"mode": "single", "channel": {
        "name": name, "type": channel_type, "key": "sk-e2e", "base_url": UPSTREAM,
        "models": model, "group": "default", "priority": 0, "weight": 0, "status": 1,
        "settings": json.dumps({"advanced_async": config}, ensure_ascii=False)}}
    return api(at, "POST", "/api/channel/", json=body)


def stored(at, name):
    return json.loads(channels(at)[name]["settings"]).get("advanced_async") or {}


def error_of(resp):
    try:
        return (resp.json() or {}).get("message") or ""
    except Exception:
        return resp.text or ""


def rejected(resp, needle):
    """项目约定：业务拒绝是 HTTP 200 + success:false（common.ApiError）。
    别把状态码当判据，否则这条断言永远绿、永远没用。"""
    try:
        body = resp.json() or {}
    except Exception:
        return False
    return body.get("success") is False and needle in (body.get("message") or "")


def base_config(**overrides):
    config = {
        "incoming_paths": ["/v1/video/generations"],
        "submit": {"upstream_path": "/v1/fake/tasks", "method": "POST", "content_type": "json"},
        "query": {"upstream_path": "/v1/fake/tasks/{task_id}", "method": "GET"},
        "fields": {"task_id_path": ["id"], "status_path": ["status"]},
        "billing": {"mode": "per_call"},
    }
    config.update(overrides)
    return config


def main():
    at = admin()
    names = ["e2e-async", "e2e-async-token"]
    drop(at, names)

    # ---------- A1 缺整块配置 ----------
    r = api(at, "POST", "/api/channel/", json={"mode": "single", "channel": {
        "name": "e2e-async", "type": 8001, "key": "sk-e2e", "base_url": UPSTREAM,
        "models": "async-model", "group": "default", "priority": 0, "weight": 0, "status": 1}})
    check("A1 缺 advanced_async 保存即拒", rejected(r, "advanced_async is required"),
          f"status={r.status_code} {error_of(r)[:90]}")

    # ---------- A2 半套配置 ----------
    half = base_config()
    half["query"] = {"upstream_path": "", "method": "GET"}
    r = create(at, "e2e-async", "async-model", half)
    check("A2 缺查询上游路径保存即拒", rejected(r, "query.upstream_path is required"),
          f"status={r.status_code} {error_of(r)[:90]}")

    # ---------- A3 未知计费模式 ----------
    bad_mode = base_config(billing={"mode": "per_call", "models": {"async-model": "per_hour"}})
    r = create(at, "e2e-async", "async-model", bad_mode)
    check("A3 billing.models 未知模式保存即拒", rejected(r, "is not supported"),
          f"status={r.status_code} {error_of(r)[:90]}")

    # ---------- A4 入站路径与 task_id 位置 ----------
    r = create(at, "e2e-async", "async-model", base_config(incoming_paths=["v1/video/generations"]))
    check("A4 入站路径缺前导斜杠被拒", rejected(r, "must start with /"),
          f"status={r.status_code} {error_of(r)[:90]}")

    path_loc = base_config(query={"upstream_path": "/v1/fake/tasks", "method": "GET", "task_id_location": "path"})
    r = create(at, "e2e-async", "async-model", path_loc)
    check("A4 task_id 放路径却没占位符被拒", rejected(r, "{task_id}"),
          f"status={r.status_code} {error_of(r)[:90]}")

    # ---------- A5 完整配置 + 按秒计费 ----------
    per_second = base_config(
        incoming_paths=["/v1/video/generations", "/v1/videos"],
        billing={"mode": "per_call", "models": {"ASYNC-MODEL": "per_second"},
                 "duration_path": ["parameters", "duration"], "default_seconds": 4},
        fields={"task_id_path": ["id"], "status_path": ["status"],
                "result_url_path": ["output", "url"], "completion_tokens_path": ["usage", "completion_tokens"]},
    )
    r = create(at, "e2e-async", "async-model", per_second)
    check("A5 完整配置保存成功", r.status_code == 200, f"status={r.status_code} {r.text[:110]}")

    s = stored(at, "e2e-async")
    check("A5 入站路径原样落库", s.get("incoming_paths") == ["/v1/video/generations", "/v1/videos"],
          json.dumps(s.get("incoming_paths"), ensure_ascii=False))
    check("A5 按模型覆盖的模式落库（大小写原样保留）",
          (s.get("billing") or {}).get("models", {}).get("ASYNC-MODEL") == "per_second",
          json.dumps(s.get("billing"), ensure_ascii=False))
    check("A5 duration_path 与 default_seconds 落库",
          (s.get("billing") or {}).get("duration_path") == ["parameters", "duration"]
          and (s.get("billing") or {}).get("default_seconds") == 4,
          json.dumps(s.get("billing"), ensure_ascii=False))
    check("A5 结果/token 路径落库",
          (s.get("fields") or {}).get("completion_tokens_path") == ["usage", "completion_tokens"],
          json.dumps(s.get("fields"), ensure_ascii=False))

    # ---------- A6 改成按 token + 兜底价 ----------
    token_cfg = dict(per_second)
    token_cfg["billing"] = {"mode": "per_token", "token_fallback_price": 0.25}
    item = channels(at)["e2e-async"]
    body = {k: item[k] for k in ("id", "name", "type", "key", "base_url", "models", "group", "priority", "weight")}
    body["settings"] = json.dumps({"advanced_async": token_cfg}, ensure_ascii=False)
    # PatchChannel 契约：字段在顶层、状态另外走开关接口，带 status 会被拒。
    r = api(at, "PUT", "/api/channel/", json=body)
    check("A6 改按 token 计费成功", r.status_code == 200, f"status={r.status_code} {r.text[:110]}")
    billing = (stored(at, "e2e-async").get("billing") or {})
    check("A6 模式与兜底价落库",
          billing.get("mode") == "per_token" and billing.get("token_fallback_price") == 0.25,
          json.dumps(billing, ensure_ascii=False))

    # ---------- B1 别名入口确实由中继接住 ----------
    # 用一个没有任何渠道认领的模型：命中中继就会是 503（无可用渠道），
    # 没命中则是 gin 的 404 文本。
    if not RELAY:
        raise SystemExit("E2E_RELAY_KEY is required (run via run_all.sh)")
    oai = {"Authorization": f"Bearer {RELAY}"}
    with httpx.Client(trust_env=False, timeout=30) as c:
        r = c.post(f"{BASE}/v1/contents/generations/tasks", headers=oai,
                   json={"model": "e2e-async-unclaimed", "content": []})
        check("B1 /v1/contents/generations/tasks 由中继处理（503 而非 404）",
              r.status_code == 503, f"status={r.status_code} {r.text[:90]}")

        r = c.post(f"{BASE}/api/v3/contents/generations/tasks", headers=oai,
                   json={"model": "e2e-async-unclaimed", "content": []})
        check("B1 插件原生入口在无人认领时也转交中继",
              r.status_code == 503, f"status={r.status_code} {r.text[:90]}")

        r = c.get(f"{BASE}/v1/contents/generations/tasks/nope-task", headers=oai)
        body = {}
        try:
            body = r.json() or {}
        except Exception:
            pass
        # 中继的取任务应答是 {"code":"task_not_exist"}（没有 success 字段），
        # 关键证据是「网关认得这条路径并去查了任务」，而不是 gin 的文本 404。
        check("B1 别名查询由中继应答（task_not_exist 而非 gin 文本 404）",
              (r.headers.get("content-type") or "").startswith("application/json")
              and (body.get("code") or "") == "task_not_exist",
              f"status={r.status_code} body={json.dumps(body, ensure_ascii=False)[:80]}")

        r = c.get(f"{BASE}/v1/no-such-path/nope", headers=oai)
        check("B1 对照：随机路径仍是 404", r.status_code == 404,
              f"status={r.status_code}")

    # ---------- 收尾 ----------
    drop(at, names)
    check("收尾：测试渠道已删除",
          all(name not in channels(at) for name in names))

    failed = [row for row in rows if not row[0]]
    print()
    print(f"共 {len(rows)} 项，失败 {len(failed)}")
    if failed:
        raise SystemExit(1)


if __name__ == "__main__":
    main()
