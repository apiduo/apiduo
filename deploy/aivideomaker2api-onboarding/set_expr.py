#!/usr/bin/env python3
"""给单个模型设置计费表达式（调试/验证用）。

用法:
    APIDUO_TOKEN=nap_... python3 set_expr.py --model ltx23 \
        --expr 'tier("request", u("count") * 0.1)' --apply

不加 --apply 只打印将要写入的内容。
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
    parser.add_argument("--model", required=True)
    parser.add_argument("--expr", required=True)
    parser.add_argument("--apply", action="store_true")
    args = parser.parse_args()

    token = os.environ.get("APIDUO_TOKEN")
    if not token:
        sys.exit("需要 APIDUO_TOKEN")
    headers = {"Authorization": f"Bearer {token}"}

    spec = call("GET", "/api/option/model_pricing", headers=headers)
    if not spec.get("success"):
        sys.exit(f"读取失败: {spec.get('message')}")
    data = spec["data"]
    entry = next((e for e in data["entries"] if e["model_name"] == args.model), None)
    version = (entry or {}).get("version") or data["empty_version"]

    change = {
        "model_name": args.model,
        "expected_version": version,
        "pricing": {
            "billing_setting.billing_mode": "tiered_expr",
            "billing_setting.billing_expr": args.expr,
        },
    }
    print(f"{args.model} → {args.expr}")
    if not args.apply:
        print("dry-run，加 --apply 写入")
        return

    result = call("PATCH", "/api/option/model_pricing", {"changes": [change]}, headers)
    if not result.get("success"):
        sys.exit(f"写入失败: {result.get('message')}")

    after = call("GET", "/api/option/model_pricing", headers=headers)
    row = next((e for e in after["data"]["entries"] if e["model_name"] == args.model), {})
    effective = row.get("effective") or {}
    print(f"✓ {args.model} mode={effective.get('billing_setting.billing_mode')} "
          f"expr={effective.get('billing_setting.billing_expr')}")


if __name__ == "__main__":
    main()
