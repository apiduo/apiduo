#!/usr/bin/env python3
"""给可观测性浏览器套件准备两个非 root 账号：mgr（管理员）与 alice（普通用户）。

创建走真实 API（先建 role=1），提权走 SQLite —— 生产提管理员要二次验证，
测试环境没有那套凭据；而这套要验的是「普通管理员能不能看 trace」，
不是提权流程本身。

env：
  E2E_BASE  网关地址，默认 http://127.0.0.1:39510
  E2E_DB    SQLite 路径，默认 /tmp/apiduo-observe/gw/one-api.db
幂等：账号已存在就跳过创建，只把 mgr 的 role 重新钉成 10。
"""
import json
import os
import sqlite3
import urllib.error
import urllib.request

BASE = os.environ.get("E2E_BASE", "http://127.0.0.1:39510")
PASS = "E2ePassw0rd!2026"
DB = os.environ.get("E2E_DB", "/tmp/apiduo-observe/gw/one-api.db")
OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))


def call(method, path, body=None, token=None):
    data = json.dumps(body).encode() if body is not None else None
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    req = urllib.request.Request(BASE + path, data=data, headers=headers, method=method)
    try:
        with OPENER.open(req, timeout=30) as resp:
            raw = resp.read()
    except urllib.error.HTTPError as err:
        raw = err.read()
    try:
        return json.loads(raw or b"{}")
    except Exception:
        return {}


jwt = (call("POST", "/api/user/login", {"username": "root", "password": PASS}).get("data") or {}).get("access_token")
if not jwt:
    raise SystemExit("root login failed")

users = call("GET", "/api/user/?p=0&page_size=100", None, jwt).get("data") or {}
existing = {u["username"] for u in users.get("items") or []}
ids = {u["username"]: u["id"] for u in users.get("items") or []}

for name in ("mgr", "alice"):
    if name in existing:
        continue
    res = call("POST", "/api/user/", {"username": name, "password": PASS, "display_name": name, "role": 1}, jwt)
    if not res.get("success"):
        raise SystemExit(f"create {name} failed: {res}")

users = call("GET", "/api/user/?p=0&page_size=100", None, jwt).get("data") or {}
ids = {u["username"]: u["id"] for u in users.get("items") or []}

conn = sqlite3.connect(DB)
conn.execute("UPDATE users SET role=? WHERE username=?", (10, "mgr"))
conn.commit()
roles = conn.execute("SELECT username, role FROM users ORDER BY id").fetchall()
conn.close()

print(json.dumps({"ids": ids, "roles": roles}, ensure_ascii=False))
