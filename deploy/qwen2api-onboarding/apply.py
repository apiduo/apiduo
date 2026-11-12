#!/usr/bin/env python3
"""把 qwen2api 的三条渠道建到 apiduo.cn。

用法:
    APIDUO_USER=apiduo APIDUO_PASS='...' python3 apply.py --dry-run
    APIDUO_USER=apiduo APIDUO_PASS='...' python3 apply.py --apply

幂等：同名渠道已存在时走 PUT 更新，不重复创建。
只用标准库 —— 沙箱 python 没有 requests，而且 HTTP_PROXY 会把带
Authorization 的 POST 静默吞掉，所以这里显式不读代理环境变量。
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import urllib.error
import urllib.request
from pathlib import Path

BASE = os.environ.get("APIDUO_BASE", "https://apiduo.cn").rstrip("/")
HERE = Path(__file__).resolve().parent

# 空 ProxyHandler 等价于 trust_env=False
OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))


def call(method: str, path: str, body: dict | None = None, headers: dict | None = None) -> dict:
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(f"{BASE}{path}", data=data, method=method)
    req.add_header("Content-Type", "application/json")
    req.add_header("Accept", "application/json")
    for key, value in (headers or {}).items():
        req.add_header(key, value)
    try:
        with OPENER.open(req, timeout=30) as resp:
            return json.loads(resp.read().decode())
    except urllib.error.HTTPError as err:
        raw = err.read().decode(errors="replace")
        try:
            return json.loads(raw)
        except json.JSONDecodeError:
            return {"success": False, "message": f"HTTP {err.code}: {raw[:200]}"}


def login(user: str, password: str) -> dict[str, str]:
    body = call("POST", "/api/user/login", {"username": user, "password": password})
    if not body.get("success"):
        sys.exit(f"登录失败: {body.get('message')}")
    data = body.get("data") or {}
    token = data.get("access_token")
    if not token:
        sys.exit("登录成功但没有 access_token")
    identity = data.get("user") or {}
    print(f"登录 OK: {identity.get('username')} (role={identity.get('role')})")
    return {"Authorization": f"Bearer {token}"}


def list_channels(headers: dict[str, str]) -> list[dict]:
    body = call("GET", "/api/channel/?p=0&page_size=200", headers=headers)
    if not body.get("success"):
        sys.exit(f"渠道列表读取失败: {body.get('message')}")
    data = body.get("data")
    if isinstance(data, dict):
        data = data.get("items") or data.get("records") or []
    return data or []


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--apply", action="store_true", help="真正写入")
    parser.add_argument("--dry-run", action="store_true", help="只打印将要提交的内容")
    args = parser.parse_args()

    spec = json.loads((HERE / "channels.json").read_text(encoding="utf-8"))
    wanted = spec["channels"]

    if not args.apply:
        for item in wanted:
            ch = item["channel"]
            print(f"[dry-run] {ch['name']:<18} type={ch['type']:<5} models={ch['models']}")
        print(f"\n共 {len(wanted)} 条。加 --apply 写入 {BASE}")
        return

    # 优先用 dashboard 访问令牌（nap_ 前缀的 PAT）；没有才走账号密码登录。
    token = os.environ.get("APIDUO_TOKEN")
    if token:
        headers = {"Authorization": f"Bearer {token}"}
        identity = call("GET", "/api/user/self", headers=headers)
        name = (identity.get("data") or {}).get("display_name", "?")
        print(f"令牌 OK: {name}")
    else:
        user = os.environ.get("APIDUO_USER")
        password = os.environ.get("APIDUO_PASS")
        if not user or not password:
            sys.exit("需要 APIDUO_TOKEN，或 APIDUO_USER / APIDUO_PASS")
        headers = login(user, password)
    existing = {c["name"]: c for c in list_channels(headers)}
    print(f"线上现有 {len(existing)} 条渠道")

    for item in wanted:
        ch = dict(item["channel"])
        name = ch["name"]
        if name in existing:
            # 更新走 PUT，且不能带 status（服务端会直接回 Invalid parameters）。
            # 只带可写字段，从线上行起底再覆盖，避免把没提到的字段冲成零值。
            row = existing[name]
            writable = (
                "name", "type", "key", "base_url", "models", "model_mapping",
                "group", "priority", "weight", "auto_ban", "settings", "other",
                "setting", "remark", "tag", "test_model", "status_code_mapping",
            )
            payload = {k: row.get(k) for k in writable if k in row}
            payload.update({k: v for k, v in ch.items() if k in writable})
            payload["id"] = row["id"]
            result = call("PUT", "/api/channel/", payload, headers)
            action = f"更新 #{row['id']}"
        else:
            result = call("POST", "/api/channel/", {"mode": "single", "channel": ch}, headers)
            action = "新建"
        if result.get("success"):
            print(f"  ✓ {action} {name}")
        else:
            print(f"  ✗ {action} {name}: {result.get('message')}")

    final = {c["name"]: c for c in list_channels(headers)}
    print("\n回读:")
    for item in wanted:
        name = item["channel"]["name"]
        row = final.get(name)
        if not row:
            print(f"  ✗ {name} 未找到")
            continue
        print(
            f"  ✓ #{row['id']:<3} {name:<18} type={row['type']} "
            f"status={row['status']} models={row['models']}"
        )


if __name__ == "__main__":
    main()
