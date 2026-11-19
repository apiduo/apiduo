#!/usr/bin/env python3
"""给高级异步渠道补 token 提取路径（Seedance canonical 契约）。

上游按 seedance 协议回包时，任务对象里有：

    "usage": {"completion_tokens": 411300, "total_tokens": 411300}

视频不统计输入 token，两者相等（见 video-adapter skill §4.5）。
不配这两条路径，ParseTaskResult 提取不到 token，u("tokens") 恒为 0，
按 token 的表达式会永远走兜底分支。

用法:
    APIDUO_TOKEN=nap_... python3 update_token_fields.py --channel 18 --apply
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import urllib.error
import urllib.request

BASE = os.environ.get("APIDUO_BASE", "https://apiduo.cn").rstrip("/")
OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))

WRITABLE = (
    "name", "type", "key", "base_url", "models", "model_mapping",
    "group", "priority", "weight", "auto_ban", "settings", "other",
    "setting", "remark", "tag", "test_model", "status_code_mapping",
)

TOKEN_PATHS = {
    "completion_tokens_path": ["usage", "completion_tokens"],
    "total_tokens_path": ["usage", "total_tokens"],
}


def call(method: str, path: str, body: dict | None = None, headers: dict | None = None) -> dict:
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(f"{BASE}{path}", data=data, method=method)
    req.add_header("Content-Type", "application/json")
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
            return {"success": False, "message": f"HTTP {err.code}: {raw[:300]}"}


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--channel", type=int, required=True)
    parser.add_argument("--apply", action="store_true")
    args = parser.parse_args()

    token = os.environ.get("APIDUO_TOKEN")
    if not token:
        sys.exit("需要 APIDUO_TOKEN")
    headers = {"Authorization": f"Bearer {token}"}

    got = call("GET", f"/api/channel/{args.channel}", headers=headers)
    if not got.get("success"):
        sys.exit(f"读取渠道失败: {got.get('message')}")
    row = got["data"]
    settings = json.loads(row["settings"]) if isinstance(row["settings"], str) else (row["settings"] or {})
    advanced = settings.setdefault("advanced_async", {})
    fields = advanced.setdefault("fields", {})

    print(f"#{args.channel} {row['name']} 现有 fields: {json.dumps(fields, ensure_ascii=False)}")
    before = dict(fields)
    fields.update(TOKEN_PATHS)
    print(f"改为 fields: {json.dumps(fields, ensure_ascii=False)}")

    if not args.apply:
        print("dry-run，加 --apply 写入")
        return

    payload = {k: row.get(k) for k in WRITABLE if k in row}
    payload["settings"] = json.dumps(settings, ensure_ascii=False)
    payload["id"] = row["id"]
    result = call("PUT", "/api/channel/", payload, headers)
    if not result.get("success"):
        sys.exit(f"写入失败: {result.get('message')}")

    after = call("GET", f"/api/channel/{args.channel}", headers=headers)["data"]
    saved = json.loads(after["settings"]) if isinstance(after["settings"], str) else after["settings"]
    print("回读 fields:", json.dumps(saved["advanced_async"]["fields"], ensure_ascii=False))
    print("changed" if before != saved["advanced_async"]["fields"] else "unchanged")


if __name__ == "__main__":
    main()
