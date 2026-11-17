#!/usr/bin/env python3
"""为 UI 判据准备数据：一条带三段式时间线的公告 + 一条功能更新。

用法：python3 seed_announcement_ui.py   → 打印创建出的 id
"""
import json
import sys
import time
import urllib.error
import urllib.request
from datetime import datetime, timedelta, timezone

BASE = "http://127.0.0.1:39047"
TZ = timezone(timedelta(hours=8))


def call(method, path, body=None, token=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(BASE + path, data=data, method=method)
    req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    try:
        with urllib.request.urlopen(req, timeout=10) as r:
            return r.status, json.loads(r.read().decode())
    except urllib.error.HTTPError as e:
        return e.code, json.loads(e.read().decode() or "{}")


def ts(minutes=0):
    return (datetime.now(TZ) + timedelta(minutes=minutes)).replace(microsecond=0).isoformat()


session = None
for _ in range(6):
    code, body = call("POST", "/api/user/login",
                      {"username": "preview", "password": "Passw0rd!2026"})
    if code == 200 and body.get("success"):
        session = body["data"]["access_token"]
        break
    time.sleep(7)
if not session:
    sys.exit("login failed")

# 先清掉上次残留
for stale in call("GET", "/api/announcements", None, session)[1].get("data") or []:
    if "E2E" in (stale.get("title") or ""):
        call("DELETE", f"/api/announcements/{stale['id']}", None, session)

ids = []

_, body = call("POST", "/api/announcements", {
    "title": "E2E 界面公告",
    "content": "这条公告用于验证列表、详情弹窗与时间线的渲染。",
    "publishDate": ts(-180),
    "type": "warning",
    "extra": "备注：验证用，稍后删除",
    "phases": [
        {"at": ts(-120), "title": "准备", "desc": "资源就绪"},
        {"at": ts(-10), "title": "切换中", "desc": "连接短暂重连"},
        {"at": ts(120), "title": "收尾", "desc": "回归检查"},
    ],
}, session)
if body.get("success"):
    ids.append((body.get("data") or {}).get("id"))

_, body = call("POST", "/api/announcements", {
    "category": "changelog",
    "kind": "feature",
    "title": "E2E 功能更新",
    "content": "这条用于验证顶部横幅的类型标签与 NEW 标记。",
    "publishDate": ts(0),
}, session)
if body.get("success"):
    ids.append((body.get("data") or {}).get("id"))

print(json.dumps(ids))
