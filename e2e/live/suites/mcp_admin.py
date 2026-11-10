#!/usr/bin/env python3
"""MCP 控制面端到端：管理 API 全接口 + 权限边界 + 面板页面。

这些接口是「能力声明」的唯一写入口，错一处就等于让 agent 拿着错误的能力表去猜参数。
所以每条都断言到字段级，包括三态（nil / 0 / N）与「更新不改 model_id」这类不可逆约定。
"""

import json
import os
import sys
import time

import httpx

BASE = os.environ.get("E2E_BASE", "http://127.0.0.1:39420").rstrip("/")
ADMIN = (os.environ.get("E2E_ADMIN", "admin"), os.environ.get("E2E_ADMIN_PW", "apiduo-test-2026"))
USER = ("mcp-e2e-user", "MCP-e2e-Passw0rd!")
MODEL = "mcp-admin-model"
MODEL2 = "mcp-admin-model-2"

rows = []


def check(name, ok, detail=""):
    rows.append((ok, name, detail))
    print(("PASS " if ok else "FAIL ") + name + ((" | " + detail) if detail else ""))


def client():
    return httpx.Client(trust_env=False, timeout=60)


def login(user, pw):
    with client() as c:
        r = c.post(f"{BASE}/api/user/login", json={"username": user, "password": pw})
        if r.status_code != 200:
            return None
        return (r.json().get("data") or {}).get("access_token")


def api(at, method, path, **kw):
    headers = {"Authorization": f"Bearer {at}"} if at else {}
    with client() as c:
        return c.request(method, f"{BASE}{path}", headers=headers, **kw)


def body(r):
    try:
        return r.json()
    except Exception:
        return {"__raw": r.text[:300]}


def ensure_user():
    """普通用户只用来验证权限边界；已存在时直接登录。"""
    at = login(*USER)
    if at:
        return at
    with client() as c:
        c.post(f"{BASE}/api/user/register", json={
            "username": USER[0], "password": USER[1], "password2": USER[1],
            "email": "mcp-e2e@example.test", "verification_code": ""})
    return login(*USER)


def main():
    at = login(*ADMIN)
    if not at:
        raise SystemExit("管理员登录失败")

    # ── 1. 权限边界 ──────────────────────────────────────────────
    r = api(None, "GET", "/api/mcp/overview")
    check("无凭证访问管理 API 被拒", r.status_code in (401, 403), f"status={r.status_code}")

    ut = ensure_user()
    check("普通用户创建/登录成功", bool(ut))
    if ut:
        for path in ("/api/mcp/overview", "/api/mcp/profiles", "/api/mcp/error-rules",
                     "/api/mcp/candidates"):
            r = api(ut, "GET", path)
            check(f"普通用户被拒 {path}", r.status_code in (401, 403), f"status={r.status_code}")
        r = api(ut, "POST", "/api/mcp/profiles", json={"model_id": "x", "kind": "image"})
        check("普通用户不能写声明", r.status_code in (401, 403), f"status={r.status_code}")

        r = api(ut, "GET", "/mcp/admin")
        check("面板页面对普通用户开放（纯静态、无密钥）", r.status_code == 200, f"status={r.status_code}")

    # ── 2. profiles CRUD ────────────────────────────────────────
    items = ((body(api(at, "GET", "/api/mcp/profiles")).get("data") or {}).get("items") or [])
    for it in items:
        if it["model_id"] in (MODEL, MODEL2):
            api(at, "DELETE", f"/api/mcp/profiles/{it['id']}")

    r = api(at, "POST", "/api/mcp/profiles", json={
        "model_id": MODEL, "display_name": "控制面模型", "kind": "image",
        "protocol": "openai_image", "async": False, "enabled": True,
        "max_ref_images": 3, "max_prompt_chars": 800,
        "parameters": json.dumps({"size": {"enum": ["1K", "2K"], "default": "1K"}}),
        "summary": "面板测试", "tags": "a,b"})
    doc = body(r)
    ok = r.status_code == 200 and doc.get("success")
    check("新建声明", ok, r.text[:160])
    pid = (doc.get("data") or {}).get("id")
    check("新建返回 id", isinstance(pid, int) and pid > 0, str(pid))

    r = api(at, "GET", f"/api/mcp/profiles/{pid}")
    doc = body(r)
    d = doc.get("data") or {}
    check("详情返回 model_card（派生视图）", isinstance(d.get("model_card"), dict),
          json.dumps(d.get("model_card"), ensure_ascii=False)[:200])
    check("详情原样回显声明", d.get("max_ref_images") == 3 and d.get("max_prompt_chars") == 800,
          json.dumps({k: d.get(k) for k in ("max_ref_images", "max_prompt_chars")}))

    # 三态：0 与「未声明」必须能分开
    r = api(at, "POST", "/api/mcp/profiles", json={
        "model_id": MODEL2, "display_name": "零参考图", "kind": "image",
        "protocol": "openai_image", "enabled": True, "max_ref_images": 0})
    pid2 = (body(r).get("data") or {}).get("id")
    check("新建第二条", bool(pid2), r.text[:120])
    r = api(at, "GET", f"/api/mcp/profiles/{pid2}")
    card = (body(r).get("data") or {}).get("model_card") or {}
    check("max_ref_images=0 输出成 0 而不是缺键",
          (card.get("input") or {}).get("max_reference_images") == 0,
          json.dumps(card, ensure_ascii=False)[:200])

    r = api(at, "POST", "/api/mcp/profiles", json={
        "model_id": "mcp-admin-nil", "display_name": "未声明", "kind": "image",
        "protocol": "openai_image", "enabled": True})
    pid3 = (body(r).get("data") or {}).get("id")
    r = api(at, "GET", f"/api/mcp/profiles/{pid3}")
    card = (body(r).get("data") or {}).get("model_card") or {}
    check("未声明时不输出 max_reference_images（否则 agent 会读成 0）",
          "max_reference_images" not in (card.get("input") or {}),
          json.dumps(card, ensure_ascii=False)[:200])
    api(at, "DELETE", f"/api/mcp/profiles/{pid3}")

    # ── 3. 更新语义 ─────────────────────────────────────────────
    r = api(at, "PUT", f"/api/mcp/profiles/{pid}", json={
        "model_id": "hijacked-name", "display_name": "改名后", "kind": "image",
        "protocol": "openai_image", "enabled": True, "max_ref_images": 1,
        "parameters": json.dumps({"size": {"enum": ["1K"]}})})
    check("更新成功", r.status_code == 200 and body(r).get("success"), r.text[:160])
    echoed = (body(r).get("data") or {}).get("model_id")
    check("更新响应体回的也是库里的 model_id（不是请求里那个）", echoed == MODEL, str(echoed))
    r = api(at, "GET", f"/api/mcp/profiles/{pid}")
    d = body(r).get("data") or {}
    check("更新不改 model_id（改名等于换模型）", d.get("model_id") == MODEL, str(d.get("model_id")))
    check("更新落库生效", d.get("display_name") == "改名后" and d.get("max_ref_images") == 1,
          json.dumps({k: d.get(k) for k in ("display_name", "max_ref_images")}, ensure_ascii=False))

    # 保存声明不能顺手改启停：bool 零值是 false，漏传 enabled 会把模型静默停用
    r = api(at, "PUT", f"/api/mcp/profiles/{pid}", json={
        "model_id": MODEL, "display_name": "改名后", "kind": "image",
        "protocol": "openai_image", "max_ref_images": 1})
    d = body(api(at, "GET", f"/api/mcp/profiles/{pid}")).get("data") or {}
    check("更新不带 enabled 时保持启用", d.get("enabled") is True, str(d.get("enabled")))

    # 只改显示名：不能顺手把异步模型改成同步（行为变了，界面却只看得到「保存成功」）
    r = api(at, "POST", "/api/mcp/profiles", json={
        "model_id": "mcp-admin-async", "display_name": "异步", "kind": "video",
        "protocol": "openai_video", "async": True, "enabled": True})
    aid = (body(r).get("data") or {}).get("id")
    check("建一条异步声明", bool(aid), r.text[:120])
    api(at, "PUT", f"/api/mcp/profiles/{aid}", json={"display_name": "异步改名"})
    d = body(api(at, "GET", f"/api/mcp/profiles/{aid}")).get("data") or {}
    check("只改显示名不动 async", d.get("async") is True, str(d.get("async")))
    check("只改显示名不动 enabled", d.get("enabled") is True, str(d.get("enabled")))
    check("只改显示名的字段生效", d.get("display_name") == "异步改名", str(d.get("display_name")))

    # 显式 null 是「清空成未声明」，与「没传」不同
    api(at, "PUT", f"/api/mcp/profiles/{aid}", json={"max_ref_images": 3})
    api(at, "PUT", f"/api/mcp/profiles/{aid}", json={"max_ref_images": None})
    d = body(api(at, "GET", f"/api/mcp/profiles/{aid}")).get("data") or {}
    check("显式 null 清成未声明", d.get("max_ref_images") is None, str(d.get("max_ref_images")))
    api(at, "DELETE", f"/api/mcp/profiles/{aid}")

    # toggle 只翻开关，不要求重传整份声明
    r = api(at, "POST", f"/api/mcp/profiles/{pid}/toggle", json={"enabled": False})
    check("toggle 成功", r.status_code == 200 and body(r).get("success"), r.text[:160])
    d = body(api(at, "GET", f"/api/mcp/profiles/{pid}")).get("data") or {}
    check("toggle 后 enabled=false", d.get("enabled") is False, str(d.get("enabled")))
    check("toggle 不碰其他字段", d.get("max_ref_images") == 1 and d.get("display_name") == "改名后",
          json.dumps({k: d.get(k) for k in ("display_name", "max_ref_images")}, ensure_ascii=False))
    api(at, "POST", f"/api/mcp/profiles/{pid}/toggle", json={"enabled": True})

    # ── 4. 入参校验 ─────────────────────────────────────────────
    cases = [
        ("空 model_id", {"model_id": "", "kind": "image"}, "不能为空"),
        ("非法 kind", {"model_id": "mcp-bad-kind", "kind": "audio"}, "不合法"),
        ("参数不是合法 JSON", {"model_id": "mcp-bad-json", "kind": "image", "parameters": "{oops"}, "JSON"),
    ]
    for name, payload, want in cases:
        r = api(at, "POST", "/api/mcp/profiles", json=payload)
        msg = body(r).get("message") or ""
        check(f"拒绝{name}", want in msg, msg[:120])

    r = api(at, "GET", "/api/mcp/profiles/999999")
    check("不存在的 id 报错而不是空对象", body(r).get("success") is False, r.text[:120])

    r = api(at, "GET", "/api/mcp/profiles/abc")
    check("非数字 id 被拒", body(r).get("success") is False, r.text[:120])

    # ── 5. overview / candidates ────────────────────────────────
    d = body(api(at, "GET", "/api/mcp/overview")).get("data") or {}
    check("overview 返回计数", isinstance(d.get("profiles"), int) and isinstance(d.get("enabled"), int),
          json.dumps(d, ensure_ascii=False))
    check("overview 标注日志库类型（面板据此解释空白）", d.get("log_db") in ("sql", "clickhouse"),
          str(d.get("log_db")))
    check("kinds 是对象", isinstance(d.get("kinds"), dict), json.dumps(d.get("kinds"), ensure_ascii=False))

    cand = body(api(at, "GET", "/api/mcp/candidates")).get("data") or {}
    cand_items = cand.get("items") or []
    check("candidates 不包含已声明的模型", MODEL not in cand_items, str(cand_items[:8]))

    # ── 6. error-rules ──────────────────────────────────────────
    rules = body(api(at, "GET", "/api/mcp/error-rules")).get("data") or {}
    items = rules.get("items") or []
    check("全局规则已播种", len(items) >= 7, f"{len(items)} 条")
    check("默认规则覆盖 429 / 5xx / 401 / 402",
          {"429", "5xx", "401", "402"}.issubset({i.get("match") for i in items}),
          str(sorted(i.get("match") for i in items)))
    default_retry = {i["match"]: i["retryable"] for i in items}
    check("401/402 默认不可重试（重试的代价是计费）",
          default_retry.get("401") is False and default_retry.get("402") is False,
          str(default_retry))
    check("429/5xx 默认可重试", default_retry.get("429") is True and default_retry.get("5xx") is True,
          str(default_retry))

    gid = next(i["id"] for i in items if i["match"] == "429")
    r = api(at, "PUT", f"/api/mcp/error-rules/{gid}", json={
        "match": "被篡改的 match", "retryable": True, "retry_action": "wait",
        "retry_after_ms": 1234, "hint": "改过的提示", "enabled": True, "sort": 50})
    check("更新规则成功", r.status_code == 200 and body(r).get("success"), r.text[:160])
    rules = body(api(at, "GET", "/api/mcp/error-rules")).get("data") or {}
    g = next(i for i in (rules.get("items") or []) if i["id"] == gid)
    check("全局规则不允许改 match（它是识别依据）", g["match"] == "429", g["match"])
    check("其余字段照改", g["retry_after_ms"] == 1234 and g["hint"] == "改过的提示",
          json.dumps({k: g[k] for k in ("retry_after_ms", "hint")}, ensure_ascii=False))
    # 改回默认值，避免影响后续套件
    api(at, "PUT", f"/api/mcp/error-rules/{gid}", json={
        "retryable": True, "retry_action": "wait", "retry_after_ms": 3000,
        "hint": "被限流，参数不用改，退避后再发", "enabled": True, "sort": 50})

    # 只改提示语：不能把「可重试」改成「不可重试」——那会让 agent 直接放弃
    api(at, "PUT", f"/api/mcp/error-rules/{gid}", json={"hint": "只改提示"})
    rules2 = body(api(at, "GET", "/api/mcp/error-rules")).get("data") or {}
    g2 = next(i for i in (rules2.get("items") or []) if i["id"] == gid)
    check("只改 hint 不动可重试语义",
          g2["retryable"] is True and g2["retry_action"] == "wait" and g2["enabled"] is True,
          json.dumps({k: g2[k] for k in ("retryable", "retry_action", "enabled")}, ensure_ascii=False))

    r = api(at, "PUT", "/api/mcp/error-rules/999999", json={"retryable": True})
    check("更新不存在的规则报错", body(r).get("success") is False, r.text[:120])

    r = api(at, "GET", "/api/mcp/error-rules?model_id=" + MODEL)
    check("按模型过滤规则可用", r.status_code == 200 and body(r).get("success"), r.text[:120])

    # ── 7. 观察接口的边界 ──────────────────────────────────────
    for days in ("0", "-1", "999", "abc"):
        r = api(at, "GET", f"/api/mcp/profiles/{pid}/observations?days={days}")
        ok = r.status_code == 200 and body(r).get("success")
        check(f"observations days={days} 不炸", ok, r.text[:120])

    r = api(at, "GET", f"/api/mcp/profiles/{pid}/audit?days=7")
    d = body(r).get("data") or {}
    check("audit 返回 issues 与 counts", isinstance(d.get("issues"), list) and isinstance(d.get("counts"), dict),
          json.dumps(d.get("counts"), ensure_ascii=False))

    # ── 8. 面板页面 ────────────────────────────────────────────
    with client() as c:
        r = c.get(f"{BASE}/mcp/admin")
    text = r.text
    check("面板页面 200", r.status_code == 200, f"status={r.status_code}")
    check("面板是 HTML", "text/html" in (r.headers.get("content-type") or ""), r.headers.get("content-type"))
    check("面板标题正确", "MCP 能力注册" in text, text[:80])
    # 面板里有 'Bearer ' 这种拼接代码是正常的；要排除的是真的凭证字面量：
    # OpenAI 风格 key、JWT（面板拿到的 access_token 一定是 eyJ 开头）。
    import re as _re
    leaked = _re.findall(r"sk-[A-Za-z0-9_-]{16,}", text) + _re.findall(r"eyJ[A-Za-z0-9_-]{20,}", text)
    check("面板不内联任何凭证字面量", not leaked, str(leaked[:2]))
    check("面板不缓存", "no-store" in (r.headers.get("cache-control") or ""),
          r.headers.get("cache-control"))
    with client() as c:
        r2 = c.get(f"{BASE}/mcp/admin/")
    check("带尾斜杠同样可达", r2.status_code == 200 and r2.text == text, f"status={r2.status_code}")

    # ── 9. 清理 ────────────────────────────────────────────────
    for target in (pid, pid2):
        if target:
            api(at, "DELETE", f"/api/mcp/profiles/{target}")
    left = ((body(api(at, "GET", "/api/mcp/profiles")).get("data") or {}).get("items") or [])
    check("删除后查不到", all(i["id"] not in (pid, pid2) for i in left),
          str([i["model_id"] for i in left]))

    # ── 10. 控制面审计 ─────────────────────────────────────────
    # 鉴权链路有 generic 兜底，所以「审计里有记录」不是充分条件。要证明的是：
    # action 落到具体动作、params 带 before/after，否则「谁把参考图上限从 3
    # 改成未声明」在审计里查不出来 —— 那正是这次要修的东西。
    items = []
    for _ in range(10):
        # 不带尾斜杠：/api/audit/ 会 301 到 /api/audit，而 httpx 默认不跟随，
        # 拿到的是 HTML 重定向页，解析出来永远是空列表 —— 会误判成「审计没写」。
        audit_items = ((body(api(at, "GET", "/api/audit?p=0&page_size=100")).get("data") or {}).get("items") or [])
        items = audit_items
        if any(str(i.get("action", "")).startswith("mcp.") for i in items):
            break
        time.sleep(0.5)

    def op_params(item):
        other = item.get("other") or {}
        op = other.get("op") if isinstance(other, dict) else None
        return (op.get("params") or {}) if isinstance(op, dict) else {}

    mcp_rows = [i for i in items if str(i.get("action", "")).startswith("mcp.")]
    actions = {i.get("action") for i in mcp_rows}
    for want_action in ("mcp.profile_create", "mcp.profile_update",
                        "mcp.profile_toggle", "mcp.profile_delete"):
        check(f"审计有 {want_action}", want_action in actions, str(sorted(actions))[:180])

    generic_mcp = [i for i in items
                   if i.get("action") == "generic" and str(i.get("route", "")).startswith("/api/mcp")]
    # generic 兜底本身是设计行为：校验不过的请求在 handler 里提前 return，
    # 只有兜底看得到，那些失败也应该留痕。真正要证明的是不重复 ——
    # 同一个请求不能既落一条 generic、又落一条精细记录。
    kinds_by_request = {}
    for row in mcp_rows + generic_mcp:
        kind = "generic" if row.get("action") == "generic" else "precise"
        kinds_by_request.setdefault(row.get("request_id"), set()).add(kind)
    doubled = [rid for rid, kinds in kinds_by_request.items() if len(kinds) > 1]
    check("同一请求不会既记 generic 又记精细", not doubled, str(doubled[:3]))

    updates = [i for i in mcp_rows if i.get("action") == "mcp.profile_update"]
    changes = [op_params(i).get("changed") for i in updates]
    changes = [c for c in changes if isinstance(c, dict)]

    name_diffs = [c["display_name"] for c in changes if isinstance(c.get("display_name"), dict)]
    check("改名留痕带具体前后值",
          any(d.get("before") == "控制面模型" and d.get("after") == "改名后" for d in name_diffs),
          json.dumps(name_diffs[:3], ensure_ascii=False))

    # 三态是这套声明的核心语义：3 -> 未声明 必须能与 3 -> 0 区分开。
    tri_diff = next((c["max_ref_images"] for c in changes
                     if "max_ref_images" in c and c["max_ref_images"].get("before") == 3
                     and c["max_ref_images"].get("after") is None), None)
    check("三态留痕：3 -> 未声明 可分辨", isinstance(tri_diff, dict),
          json.dumps(tri_diff, ensure_ascii=False))

    toggles = [op_params(i).get("enabled") for i in mcp_rows if i.get("action") == "mcp.profile_toggle"]
    toggles = [t for t in toggles if isinstance(t, dict)]
    check("启停留痕带前后状态",
          any(t.get("before") is not None and t.get("after") is not None and t["before"] != t["after"]
              for t in toggles),
          json.dumps(toggles[:2], ensure_ascii=False))

    # 删除留痕的关键是「删掉之后还能知道删了什么」：model_id 在 params 顶层，
    # 完整旧声明在 before 里，两者都要有。
    delete_params = [op_params(i) for i in mcp_rows if i.get("action") == "mcp.profile_delete"]
    check("删除留痕保留旧声明",
          any(p.get("model_id") in (MODEL, MODEL2)
              and isinstance(p.get("before"), dict) and p["before"].get("display_name")
              for p in delete_params),
          json.dumps(delete_params[:2], ensure_ascii=False)[:180])

    check("审计归属到操作者",
          bool(mcp_rows) and all(i.get("username") for i in mcp_rows), str(len(mcp_rows)))
    check("审计标了鉴权方式",
          bool(mcp_rows) and all(i.get("auth_method") in ("session", "access_token") for i in mcp_rows),
          str({i.get("auth_method") for i in mcp_rows}))

    print()
    if any(not ok for ok, _, _ in rows):
        failed = [n for ok, n, _ in rows if not ok]
        print(f"MCP 控制面套件：{len(rows) - len(failed)} 通过 / {len(failed)} 失败")
        for n in failed:
            print("  失败：" + n)
        return 1
    print(f"MCP 控制面套件：{len(rows)} 条全通过")
    return 0


if __name__ == "__main__":
    sys.exit(main())
