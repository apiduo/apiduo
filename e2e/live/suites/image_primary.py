#!/usr/bin/env python3
"""主端点 + 图片协议端到端：落库收敛、三入口落点、载体闸门零出站。

渠道全部指向记录式假上游，断言的是「上游实际收到什么路径 / 什么 body」，
不看网关自己的日志——那只能证明它以为自己在干什么。
"""

import base64
import json
import os

import httpx

BASE = os.environ.get("E2E_BASE", "http://127.0.0.1:39420")
RELAY = os.environ.get("E2E_RELAY_KEY", "")
ADMIN = (os.environ.get("E2E_ADMIN", "admin"), os.environ.get("E2E_ADMIN_PW", "apiduo-test-2026"))
LOG = os.environ.get("E2E_IMAGE_LOG", "/tmp/apiduo-e2e/image.log")
REF = "/tmp/apiduo-img-test/ref.png"
# 参考图由套件自己造：以前依赖一个手工留在 /tmp 的 ref.png，换台机器/清过 /tmp
# 就整条 T3 崩在 FileNotFoundError 上，而不是报一个有意义的结果。
PNG_1X1 = base64.b64decode(
    "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="
)

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


def create(at, name, models, settings):
    body = {"mode": "single", "channel": {
        "name": name, "type": 58, "key": "sk-e2e", "base_url": os.environ.get("E2E_UPSTREAM", "http://127.0.0.1:39403"),
        "models": models, "group": "default", "priority": 0, "weight": 0, "status": 1,
        "settings": json.dumps(settings, ensure_ascii=False)}}
    return api(at, "POST", "/api/channel/", json=body)


def channels(at):
    r = api(at, "GET", "/api/channel/?p=0&page_size=100")
    return {it["name"]: it for it in r.json()["data"]["items"]}


def drop(at, names):
    table = channels(at)
    for name in names:
        if name in table:
            api(at, "DELETE", f"/api/channel/{table[name]['id']}")
            print(f"  cleaned {name}")


def stored(at, name):
    return json.loads(channels(at)[name]["settings"])


def wait_visible(at, model, seconds=40):
    """渠道生效判据：管理面能查到该模型 + 过一个同步周期（SYNC_FREQUENCY=1s）。

    不用 /v1/models：全新库没有模型定价，无价模型不会出现在列表里；
    也不拿某个具体入口探测：不同用例的渠道只开了部分路由（图片协议 / 显式覆盖）。
    """
    import time
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


def main():
    os.makedirs(os.path.dirname(REF), exist_ok=True)
    if not os.path.exists(REF):
        with open(REF, "wb") as fh:
            fh.write(PNG_1X1)

    if not RELAY:
        raise SystemExit("E2E_RELAY_KEY is required (run via run_all.sh)")
    at = admin()
    api(at, "PUT", "/api/option/", json={"key": "performance_setting.monitor_disk_threshold", "value": "99"})

    names = ["e2e-primary", "e2e-norm", "e2e-chatok", "e2e-chatprimary", "e2e-explicit"]
    drop(at, names)

    # ---------- C1：主端点 + 空 upstream 跟随 + 显式覆盖 ----------
    c1 = {"primary_endpoint": " /v1/images/custom-gen ",
          "image_protocol": {"enabled": True, "upstream": "seedream"},
          "advanced_custom": {"advanced_routes": [
              {"incoming_path": "/v1/images/edits"},
              {"incoming_path": "/v1/chat/completions", "upstream_path": "/v1/chat/completions"},
          ]}}
    r = create(at, "e2e-primary", "e2e-primary", c1)
    check("C1 建渠道 200", r.status_code == 200, f"status={r.status_code} {r.text[:120]}")

    s1 = stored(at, "e2e-primary")
    check("C1 primary_endpoint 落库并去空白",
          s1.get("primary_endpoint") == "/v1/images/custom-gen", repr(s1.get("primary_endpoint")))
    routes = (s1.get("advanced_custom") or {}).get("advanced_routes") or []
    check("C1 空 upstream 保持跟随（不发散成绝对路径）",
          bool(routes) and not routes[0].get("upstream_path"), json.dumps(routes[:1], ensure_ascii=False))
    check("C1 显式覆盖保留",
          len(routes) > 1 and routes[1].get("upstream_path") == "/v1/chat/completions",
          json.dumps(routes[1:2], ensure_ascii=False))

    check("C1 渠道生效（/v1/models 可见）", wait_visible(at, "e2e-primary"))

    # T2 generations -> 主端点
    before = log_count()
    r = relay("POST", "/v1/images/generations", json={"model": "e2e-primary", "prompt": "primary-generations"})
    entry = log_last() if log_count() > before else None
    check("T2 generations 200", r.status_code == 200, f"status={r.status_code} {r.text[:140]}")
    check("T2 上游命中主端点", entry is not None and entry.get("path") == "/v1/images/custom-gen",
          (entry or {}).get("path", "-"))

    # T3 edits 文件 -> 折叠后仍打主端点
    before = log_count()
    with open(REF, "rb") as fh:
        r = relay("POST", "/v1/images/edits",
                  data={"model": "e2e-primary", "prompt": "primary-edits"},
                  files=[("image", ("ref.png", fh.read(), "image/png"))])
    entry = log_last() if log_count() > before else None
    check("T3 edits 文件 200", r.status_code == 200, f"status={r.status_code} {r.text[:140]}")
    check("T3 上游命中主端点", entry is not None and entry.get("path") == "/v1/images/custom-gen",
          (entry or {}).get("path", "-"))
    j = (entry or {}).get("json") or {}
    check("T3 折叠成 JSON 且带图",
          (entry or {}).get("content_type") == "application/json" and j.get("image_count") == 1,
          f"ctype={(entry or {}).get('content_type')} image_count={j.get('image_count')}")

    # T3.5 协议开启时 chat 的显式覆盖不生效：请求被折叠成出图任务打在主端点。
    # C1 的 chat 明明配了 upstream_path=/v1/chat/completions，但 prepareImageProtocol
    # 只要 image_protocol.enabled 就接管（RelayModeChatCompletions 分支），完全不看
    # 路由的 upstream_path —— UI 上「图片协议接管 / 覆盖不生效」那行字说的就是它。
    before = log_count()
    r = relay("POST", "/v1/chat/completions",
              json={"model": "e2e-primary",
                    "messages": [{"role": "user", "content": "chat-folds"}]})
    entry = log_last() if log_count() > before else None
    check("T3.5 协议开启时 chat 200", r.status_code == 200, f"status={r.status_code} {r.text[:140]}")
    check("T3.5 chat 覆盖被协议接管（打主端点，不是 /v1/chat/completions）",
          entry is not None and entry.get("path") == "/v1/images/custom-gen",
          (entry or {}).get("path", "-"))
    j = (entry or {}).get("json") or {}
    check("T3.5 chat 请求体折叠成出图任务（有 prompt、无 messages）",
          j.get("prompt") == "chat-folds" and "messages" not in (j.get("keys") or []),
          json.dumps({k: j.get(k) for k in ("prompt", "keys")}, ensure_ascii=False)[:160])

    # T4/T5 载体闸门
    before = log_count()
    r = relay("POST", "/v1/images/edits",
              data={"model": "e2e-primary", "prompt": "x", "image": "https://cdn.example.com/a.png"})
    check("T4 edits 文本引用 400", r.status_code == 400, f"status={r.status_code} {r.text[:110]}")
    check("T4 零出站", log_count() == before, f"delta={log_count() - before}")

    before = log_count()
    r = relay("POST", "/v1/images/edits",
              json={"model": "e2e-primary", "prompt": "x", "image": "https://cdn.example.com/a.png"})
    check("T5 edits JSON 体 400", r.status_code == 400, f"status={r.status_code} {r.text[:110]}")
    check("T5 零出站", log_count() == before, f"delta={log_count() - before}")

    # ---------- C2：显式覆盖的上游路径（渠道不启用图片协议） ----------
    c2 = {"primary_endpoint": "/v1/images/generations",
          "advanced_custom": {"advanced_routes": [
              {"incoming_path": "/v1/images/edits", "upstream_path": "/v1/images/generations"},
              {"incoming_path": "/v1/chat/completions", "upstream_path": "/v1/chat/completions"},
          ]}}
    r = create(at, "e2e-chatok", "e2e-chatok", c2)
    check("C2 建渠道 200", r.status_code == 200, f"status={r.status_code}")
    s2 = stored(at, "e2e-chatok")
    routes2 = (s2.get("advanced_custom") or {}).get("advanced_routes") or []
    check("C2 等于主端点的路径收敛成空",
          bool(routes2) and not routes2[0].get("upstream_path"), json.dumps(routes2[:1], ensure_ascii=False))
    check("C2 主端点原样保留", s2.get("primary_endpoint") == "/v1/images/generations",
          repr(s2.get("primary_endpoint")))
    check("C2 渠道生效", wait_visible(at, "e2e-chatok"))

    before = log_count()
    r = relay("POST", "/v1/chat/completions",
              json={"model": "e2e-chatok", "messages": [{"role": "user", "content": "hi"}]})
    entry = log_last() if log_count() > before else None
    check("T6 chat 200", r.status_code == 200, f"status={r.status_code} {r.text[:110]}")
    check("T6 上游命中显式覆盖", entry is not None and entry.get("path") == "/v1/chat/completions",
          (entry or {}).get("path", "-"))

    # ---------- C3：聊天主端点不抢图片协议 ----------
    c3 = {"primary_endpoint": "/v1/chat/completions",
          "image_protocol": {"enabled": True, "upstream": "seedream"},
          "advanced_custom": {"advanced_routes": [{"incoming_path": "/v1/images/generations"}]}}
    r = create(at, "e2e-chatprimary", "e2e-chatprimary", c3)
    check("C3 建渠道 200", r.status_code == 200, f"status={r.status_code}")
    check("C3 渠道生效", wait_visible(at, "e2e-chatprimary"))
    before = log_count()
    r = relay("POST", "/v1/images/generations", json={"model": "e2e-chatprimary", "prompt": "chat-primary"})
    entry = log_last() if log_count() > before else None
    check("C3 图片请求仍走默认 generations（聊天端点不抢）",
          entry is not None and entry.get("path") == "/v1/images/generations", (entry or {}).get("path", "-"))

    # ---------- C4：显式 primary_path 优先于主端点 ----------
    c4 = {"primary_endpoint": "/v1/images/primary-x",
          "image_protocol": {"enabled": True, "upstream": "seedream", "primary_path": "/v1/images/explicit-y"},
          "advanced_custom": {"advanced_routes": [{"incoming_path": "/v1/images/generations"}]}}
    r = create(at, "e2e-explicit", "e2e-explicit", c4)
    check("C4 建渠道 200", r.status_code == 200, f"status={r.status_code}")
    check("C4 渠道生效", wait_visible(at, "e2e-explicit"))
    before = log_count()
    r = relay("POST", "/v1/images/generations", json={"model": "e2e-explicit", "prompt": "explicit"})
    entry = log_last() if log_count() > before else None
    check("C4 协议 primary_path 覆盖主端点",
          entry is not None and entry.get("path") == "/v1/images/explicit-y", (entry or {}).get("path", "-"))

    # ---------- C5：非法主端点（管理面 / 完整 URL）被拒且不落库 ----------
    for label, bad in (
        ("完整 URL", "https://x.example/v1/images/generations"),
        ("管理面", "/api/advanced-custom/routes"),
    ):
        r = create(at, "e2e-badprimary", "e2e-badprimary",
                   {"primary_endpoint": bad,
                    "advanced_custom": {"advanced_routes": [{"incoming_path": "/v1/images/generations"}]}})
        body = r.text
        check(f"C5 {label} 被拒", "primary_endpoint must" in body, f"status={r.status_code} {body[:110]}")
    check("C5 非法渠道未落库", "e2e-badprimary" not in channels(at))

    drop(at, names + ["e2e-badprimary"])
    api(at, "PUT", "/api/option/", json={"key": "performance_setting.monitor_disk_threshold", "value": "99"})

    failed = [r for r in rows if not r[0]]
    print(f"\n共 {len(rows)} 项，失败 {len(failed)}")
    return 1 if failed else 0


if __name__ == "__main__":
    raise SystemExit(main())
