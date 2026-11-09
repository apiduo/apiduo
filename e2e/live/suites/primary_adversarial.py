#!/usr/bin/env python3
"""主端点 / 图片协议对抗套件：专测「保存期该拒绝的」与「运行期不许静默改道」。

与 image_primary.py 的分工：那套证明主路径能跑，这套证明坏配置进不来、
配置与行为不漂移。所有断言仍以记录式假上游实际收到的请求为准。
"""

import json
import os
import time

import httpx

BASE = os.environ.get("E2E_BASE", "http://127.0.0.1:39420")
RELAY = os.environ.get("E2E_RELAY_KEY", "")
ADMIN = (os.environ.get("E2E_ADMIN", "admin"), os.environ.get("E2E_ADMIN_PW", "apiduo-test-2026"))
UPSTREAM = os.environ.get("E2E_UPSTREAM", "http://127.0.0.1:39403")
LOG = os.environ.get("E2E_IMAGE_LOG", "/tmp/apiduo-e2e/image.log")

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


def relay(method, path, **kw):
    with httpx.Client(trust_env=False, timeout=120) as c:
        return c.request(method, f"{BASE}{path}", headers={"Authorization": f"Bearer {RELAY}"}, **kw)


def log_count():
    with open(LOG, encoding="utf-8") as fh:
        return sum(1 for _ in fh)


def log_last():
    last = None
    with open(LOG, encoding="utf-8") as fh:
        for line in fh:
            line = line.strip()
            if line:
                last = json.loads(line)
    return last


def create(at, name, model, settings):
    body = {"mode": "single", "channel": {
        "name": name, "type": 58, "key": "sk-e2e", "base_url": UPSTREAM,
        "models": model, "group": "default", "priority": 0, "weight": 0, "status": 1,
        "settings": json.dumps(settings, ensure_ascii=False)}}
    return api(at, "POST", "/api/channel/", json=body)


def channels(at):
    r = api(at, "GET", "/api/channel/?p=0&page_size=100")
    return {it["name"]: it for it in r.json()["data"]["items"]}


def stored(at, name):
    return json.loads(channels(at)[name]["settings"])


def drop(at, names):
    table = channels(at)
    for name in names:
        if name in table:
            api(at, "DELETE", f"/api/channel/{table[name]['id']}")


def wait_visible(at, model, seconds=30):
    for _ in range(seconds):
        r = api(at, "GET", "/api/channel/?p=0&page_size=100")
        models = set()
        for it in (r.json().get("data") or {}).get("items") or []:
            models.update(m.strip() for m in (it.get("models") or "").split(",") if m.strip())
        if model in models:
            time.sleep(2)
            return True
        time.sleep(1)
    return False


def update(at, name, mutate):
    """按 PatchChannel 契约更新：字段在顶层，且不允许带 status。"""
    item = channels(at)[name]
    settings = json.loads(item["settings"])
    mutate(settings)
    body = {k: item[k] for k in ("id", "name", "type", "key", "base_url", "models", "group", "priority", "weight")}
    body["settings"] = json.dumps(settings, ensure_ascii=False)
    return api(at, "PUT", "/api/channel/", json=body)


def error_of(resp):
    try:
        return json.loads(resp.text).get("message") or ""
    except Exception:
        return resp.text[:200]


def main():
    if not RELAY:
        raise SystemExit("E2E_RELAY_KEY is required (run via run_all.sh)")
    at = admin()
    api(at, "PUT", "/api/option/", json={"key": "performance_setting.monitor_disk_threshold", "value": "99"})
    names = ["adv-a", "adv-b", "adv-c", "adv-routeonly", "adv-override", "adv-resp"]
    drop(at, names)

    # ---------- A1：既无主端点又无 upstream，创建和更新都必须拒绝 ----------
    bad = {"advanced_custom": {"advanced_routes": [{"incoming_path": "/v1/images/generations"}]}}
    r = create(at, "adv-a", "adv-a", bad)
    msg = error_of(r)
    check("A1 创建拒绝半套配置", "upstream_path is required" in msg, msg[:90])

    create(at, "adv-b", "adv-b", {
        "primary_endpoint": "/v1/images/generations",
        "advanced_custom": {"advanced_routes": [{"incoming_path": "/v1/images/generations"}]}})
    r = update(at, "adv-b", lambda s: s.pop("primary_endpoint", None))
    msg = error_of(r)
    check("A1 更新拒绝「删主端点但路由仍空」", "upstream_path is required" in msg, msg[:90])
    check("A1 拒绝后未落库", "primary_endpoint" in stored(at, "adv-b"))

    # ---------- A2：图片协议配置在保存期就要校验 ----------
    r = create(at, "adv-a", "adv-a", {
        "primary_endpoint": "/v1/images/generations",
        "image_protocol": {"enabled": True, "upstream": "bogus"},
        "advanced_custom": {"advanced_routes": [{"incoming_path": "/v1/images/generations"}]}})
    msg = error_of(r)
    check("A2 非法 upstream 保存即拒", "image_protocol.upstream must be" in msg, msg[:90])

    for label, path in (
        ("协议相对地址", {"primary_path": "//evil.example/x"}),
        ("带查询串", {"edit_path": "/v1/images/edits?x=1"}),
        ("管理面路径", {"primary_path": "/api/advanced-custom/models"}),
    ):
        cfg = {"enabled": True, "upstream": "seedream", **path}
        r = create(at, "adv-a", "adv-a", {
            "primary_endpoint": "/v1/images/generations",
            "image_protocol": cfg,
            "advanced_custom": {"advanced_routes": [{"incoming_path": "/v1/images/generations"}]}})
        msg = error_of(r)
        check(f"A2 图片路径{label}保存即拒", "image_protocol." in msg, msg[:90])
    check("A2 非法协议配置未落库", "adv-a" not in channels(at))

    # ---------- A3：尾斜杠归一，且上游真的收到归一后的路径 ----------
    r = create(at, "adv-c", "adv-c", {
        "primary_endpoint": "/v1/images/generations/",
        "advanced_custom": {"advanced_routes": [{"incoming_path": "/v1/images/generations"}]}})
    s = stored(at, "adv-c")
    check("A3 尾斜杠落库前去掉", s.get("primary_endpoint") == "/v1/images/generations",
          repr(s.get("primary_endpoint")))
    wait_visible(at, "adv-c")
    before = log_count()
    r = relay("POST", "/v1/images/generations", json={"model": "adv-c", "prompt": "slash"})
    entry = log_last() if log_count() > before else None
    check("A3 上游收到无尾斜杠路径",
          r.status_code == 200 and (entry or {}).get("path") == "/v1/images/generations",
          f"status={r.status_code} path={(entry or {}).get('path')}")

    # ---------- A4：换主端点只改一个字段，空 upstream 跟随新值 ----------
    r = update(at, "adv-c", lambda s: s.__setitem__("primary_endpoint", "/v1/images/moved"))
    check("A4 更新成功", r.status_code == 200 and json.loads(r.text).get("success") is True,
          (r.text or "")[:90])
    s = stored(at, "adv-c")
    routes = (s.get("advanced_custom") or {}).get("advanced_routes") or []
    check("A4 路由仍是跟随（未写死）", bool(routes) and not routes[0].get("upstream_path"),
          json.dumps(routes[:1], ensure_ascii=False))
    wait_visible(at, "adv-c")
    before = log_count()
    r = relay("POST", "/v1/images/generations", json={"model": "adv-c", "prompt": "moved"})
    entry = log_last() if log_count() > before else None
    check("A4 请求跟随新主端点",
          r.status_code == 200 and (entry or {}).get("path") == "/v1/images/moved",
          f"status={r.status_code} path={(entry or {}).get('path')}")

    # ---------- A5：显式覆盖 —— 关协议按覆盖走，开协议折叠优先 ----------
    r = create(at, "adv-override", "adv-override", {
        "primary_endpoint": "/v1/images/primary",
        "advanced_custom": {"advanced_routes": [
            {"incoming_path": "/v1/images/generations", "upstream_path": "/v1/images/pinned"}]}})
    check("A5 建渠道（覆盖不等于主端点，保留）",
          r.status_code == 200 and stored(at, "adv-override")["advanced_custom"]["advanced_routes"][0]
          .get("upstream_path") == "/v1/images/pinned", error_of(r)[:110])
    wait_visible(at, "adv-override")
    before = log_count()
    r = relay("POST", "/v1/images/generations", json={"model": "adv-override", "prompt": "pin"})
    entry = log_last() if log_count() > before else None
    check("A5 未开协议时按显式覆盖走",
          (entry or {}).get("path") == "/v1/images/pinned", (entry or {}).get("path", "-"))

    r = update(at, "adv-override", lambda s: s.__setitem__(
        "image_protocol", {"enabled": True, "upstream": "seedream"}))
    check("A5 打开协议", r.status_code == 200 and json.loads(r.text).get("success") is True,
          (r.text or "")[:80])
    wait_visible(at, "adv-override")
    before = log_count()
    r = relay("POST", "/v1/images/generations", json={"model": "adv-override", "prompt": "folded"})
    entry = log_last() if log_count() > before else None
    check("A5 开协议后折叠优先于逐条覆盖（协议是入口属性）",
          (entry or {}).get("path") == "/v1/images/primary", (entry or {}).get("path", "-"))

    # ---------- A6：协议接管 generations，即使路由表只声明了 chat ----------
    r = create(at, "adv-routeonly", "adv-routeonly", {
        "primary_endpoint": "/v1/images/generations",
        "image_protocol": {"enabled": True, "upstream": "seedream"},
        "advanced_custom": {"advanced_routes": [{"incoming_path": "/v1/chat/completions"}]}})
    check("A6 建渠道（只声明对话路由）", r.status_code == 200, error_of(r)[:90])
    wait_visible(at, "adv-routeonly")
    before = log_count()
    r = relay("POST", "/v1/images/generations", json={"model": "adv-routeonly", "prompt": "no-route"})
    entry = log_last() if log_count() > before else None
    check("A6 没写图片路由也能被选中（ServesPath 放行）",
          r.status_code == 200 and (entry or {}).get("path") == "/v1/images/generations",
          f"status={r.status_code} path={(entry or {}).get('path')}")

    # ---------- A7：对话入口的爆炸半径（设计行为，显式回归） ----------
    before = log_count()
    r = relay("POST", "/v1/chat/completions", json={
        "model": "adv-routeonly",
        "messages": [{"role": "user", "content": "hello, pure text, no image"}]})
    entry = log_last() if log_count() > before else None
    body = (entry or {}).get("json") or {}
    check("A7 纯文本对话在协议渠道上被转成出图任务（渠道必须图片专用）",
          r.status_code == 200 and (entry or {}).get("path") == "/v1/images/generations"
          and body.get("prompt") == "hello, pure text, no image",
          f"status={r.status_code} path={(entry or {}).get('path')} prompt={body.get('prompt')!r}")

    # ---------- A8：Responses 入口不参与折叠，按自己的路由走 ----------
    r = create(at, "adv-resp", "adv-resp", {
        "primary_endpoint": "/v1/images/generations",
        "image_protocol": {"enabled": True, "upstream": "seedream"},
        "advanced_custom": {"advanced_routes": [
            {"incoming_path": "/v1/images/generations"},
            {"incoming_path": "/v1/responses", "upstream_path": "/v1/responses"}]}})
    check("A8 建渠道（含 responses 路由）", r.status_code == 200, error_of(r)[:90])
    wait_visible(at, "adv-resp")
    before = log_count()
    r = relay("POST", "/v1/responses", json={"model": "adv-resp", "input": "draw a cat"})
    entry = log_last() if log_count() > before else None
    check("A8 responses 不被折叠，仍打自己的上游",
          (entry or {}).get("path") == "/v1/responses",
          f"status={r.status_code} path={(entry or {}).get('path')}")

    # ---------- A9：未声明且协议不接管的入口，明确 503 且零出站 ----------
    before = log_count()
    r = relay("POST", "/v1/embeddings", json={"model": "adv-resp", "input": "x"})
    check("A9 embeddings 被选择器拒绝（503）且零出站",
          r.status_code == 503 and log_count() == before,
          f"status={r.status_code} delta={log_count() - before}")

    drop(at, names)
    api(at, "PUT", "/api/option/", json={"key": "performance_setting.monitor_disk_threshold", "value": "99"})

    failed = [r for r in rows if not r[0]]
    print(f"\n共 {len(rows)} 项，失败 {len(failed)}")
    return 1 if failed else 0


if __name__ == "__main__":
    raise SystemExit(main())
