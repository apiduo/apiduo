#!/usr/bin/env python3
"""公告功能的 API 层端到端判据。

只做断言，不做修复。每条用例独立、可重跑、跑完自清理。
退出码：0 = 全部通过，1 = 有失败。
"""
import json
import sys
import time
import urllib.error
import urllib.request
from datetime import datetime, timedelta, timezone

BASE = "http://127.0.0.1:39047"
TZ = timezone(timedelta(hours=8))
RESULTS = []


def call(method, path, body=None, token=None, timeout=10):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(BASE + path, data=data, method=method)
    req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return r.status, json.loads(r.read().decode())
    except urllib.error.HTTPError as e:
        raw = e.read().decode()
        try:
            return e.code, json.loads(raw)
        except Exception:
            return e.code, {"success": False, "message": raw[:200]}


def ts(days=0):
    return (datetime.now(TZ) + timedelta(days=days)).replace(microsecond=0).isoformat()


def check(name, ok, detail=""):
    RESULTS.append({"name": name, "ok": bool(ok), "detail": detail})
    print(f"{'PASS' if ok else 'FAIL'}  {name}" + (f"  << {detail}" if not ok and detail else ""))


def login():
    for _ in range(6):
        code, body = call("POST", "/api/user/login",
                          {"username": "preview", "password": "Passw0rd!2026"})
        if code == 200 and body.get("success"):
            return body["data"]["access_token"]
        time.sleep(7)
    sys.exit("login failed")


session = login()

# 幂等：清掉上一次中断留下的 E2E 条目，否则重跑会累积孤儿数据
for stale in call("GET", "/api/announcements", None, session)[1].get("data") or []:
    if "E2E" in (stale.get("title") or ""):
        call("DELETE", f"/api/announcements/{stale['id']}", None, session)

created = []


def create(payload):
    code, body = call("POST", "/api/announcements", payload, session)
    if body.get("success"):
        aid = (body.get("data") or {}).get("id")
        if aid:
            created.append(aid)
        return body.get("data") or {}, body
    return None, body


# ---- 1. 创建：完整字段 ----
item, body = create({
    "title": "E2E 完整字段",
    "category": "announcement",
    "content": "带全部字段的公告",
    "publishDate": ts(0),
    "expiresAt": ts(2),
    "type": "warning",
    "extra": "备注",
    "phases": [
        {"at": ts(0), "title": "阶段一", "desc": "描述一"},
        {"at": ts(1), "title": "阶段二"},
    ],
})
check("创建完整字段公告", item is not None, body.get("message", ""))
if item:
    check("  标题落库", item.get("title") == "E2E 完整字段")
    check("  级别落库", item.get("type") == "warning")
    check("  阶段落库 2 个", len(item.get("phases") or []) == 2)
    check("  中文未转义", "带全部字段" in (item.get("content") or ""))

# ---- 2. 校验 ----
for name, payload, expect in [
    ("非法 category 被拒", {"category": "news", "content": "x", "publishDate": ts(0)}, "invalid category"),
    ("非法 kind 被拒", {"category": "changelog", "kind": "breaking", "content": "x", "publishDate": ts(0)}, "invalid kind"),
    ("title 超 60 字被拒", {"content": "x", "publishDate": ts(0), "title": "标" * 61}, "title cannot exceed"),
    ("content 超 500 字被拒", {"content": "字" * 501, "publishDate": ts(0)}, "content"),
    ("过期早于发布被拒", {"content": "x", "publishDate": ts(0), "expiresAt": ts(-1)}, "expiry must be later"),
    ("阶段超过 8 个被拒", {"content": "x", "publishDate": ts(0),
                          "phases": [{"at": ts(0), "title": f"p{i}"} for i in range(9)]}, "8"),
    ("阶段缺时间被拒", {"content": "x", "publishDate": ts(0), "phases": [{"title": "无时间"}]}, "missing the at field"),
]:
    code, body = call("POST", "/api/announcements", payload, session)
    ok = body.get("success") is False and expect in (body.get("message") or "")
    check(name, ok, f"success={body.get('success')} msg={(body.get('message') or '')[:60]}")

# ---- 3. 读者侧时间窗口 ----
future, _ = create({"content": "未来公告", "title": "E2E 未来", "publishDate": ts(3)})
past, _ = create({"content": "过期公告", "title": "E2E 过期", "publishDate": ts(-5), "expiresAt": ts(-4)})
now_item, _ = create({"content": "当前公告", "title": "E2E 当前", "publishDate": ts(0)})

reader = call("GET", "/api/status")[1]["data"].get("announcements") or []
titles = [a.get("title") for a in reader]
check("未来公告不进读者侧", "E2E 未来" not in titles)
check("过期公告不进读者侧", "E2E 过期" not in titles)
check("当前公告进读者侧", "E2E 当前" in titles)

admin = call("GET", "/api/announcements", None, session)[1]["data"]
admin_titles = [a.get("title") for a in admin]
check("管理侧看得到未来公告", "E2E 未来" in admin_titles)
check("管理侧看得到过期公告", "E2E 过期" in admin_titles)

# ---- 4. 功能更新不进公告面板 ----
cl, _ = create({"category": "changelog", "kind": "feature", "title": "E2E 更新",
                "content": "一条功能更新", "publishDate": ts(0)})
reader = call("GET", "/api/status")[1]["data"].get("announcements") or []
cl_in_reader = [a for a in reader if a.get("category") == "changelog"]
check("功能更新下发给前端（由前端分流）", any(a.get("title") == "E2E 更新" for a in cl_in_reader))

# ---- 5. PATCH 局部更新 ----
target = item.get("id")
code, body = call("PATCH", f"/api/announcements/{target}", {"title": "E2E 改过"}, session)
after = body.get("data") or {}
check("PATCH 改标题", after.get("title") == "E2E 改过")
check("PATCH 未动其他字段", after.get("type") == "warning" and len(after.get("phases") or []) == 2)

# ---- 6. 404 ----
code, body = call("PATCH", "/api/announcements/999999", {"title": "x"}, session)
check("PATCH 不存在的 id 返回 404", code == 404, f"code={code}")
code, body = call("DELETE", "/api/announcements/999999", None, session)
check("DELETE 不存在的 id 返回 404", code == 404, f"code={code}")

# ---- 7. 清理 ----
failed_del = 0
for aid in created:
    code, body = call("DELETE", f"/api/announcements/{aid}", None, session)
    if not body.get("success"):
        failed_del += 1
check("清理测试数据", failed_del == 0, f"{failed_del} 条删除失败")

final = call("GET", "/api/announcements", None, session)[1]["data"]
check("清理后不含 E2E 条目",
      not any("E2E" in (a.get("title") or "") for a in final),
      f"残留 {[a.get('title') for a in final if 'E2E' in (a.get('title') or '')]}")

# ---- 汇总 ----
total = len(RESULTS)
passed = sum(1 for r in RESULTS if r["ok"])
print(f"\n{passed}/{total} passed")
if passed != total:
    print("失败项：")
    for r in RESULTS:
        if not r["ok"]:
            print(f"  - {r['name']}: {r['detail']}")
    sys.exit(1)
