#!/usr/bin/env python3
"""检查几个模型的计费模式（回归用）。"""

import json
import os
import sys
import urllib.request

BASE = os.environ.get("APIDUO_BASE", "https://apiduo.cn").rstrip("/")
OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))

WANT = ["hailuo-2.3", "hailuo-video", "hailuo-video-sec", "happyhorse11", "happyhorse1.1"]


def call(path, headers):
    req = urllib.request.Request(f"{BASE}{path}")
    for key, value in headers.items():
        req.add_header(key, value)
    with OPENER.open(req, timeout=30) as resp:
        return json.loads(resp.read().decode())


def main():
    token = os.environ.get("APIDUO_TOKEN")
    if not token:
        sys.exit("需要 APIDUO_TOKEN")
    data = call("/api/option/model_pricing", {"Authorization": f"Bearer {token}"})["data"]
    rows = {e["model_name"]: e for e in data["entries"]}
    for name in WANT:
        effective = (rows.get(name) or {}).get("effective") or {}
        mode = effective.get("billing_setting.billing_mode") or "-"
        expr = effective.get("billing_setting.billing_expr") or "-"
        print(
            f"{name:<18} mode={mode:<12} "
            f"ModelPrice={effective.get('ModelPrice')} ModelRatio={effective.get('ModelRatio')} "
            f"expr={str(expr)[:40]}"
        )


if __name__ == "__main__":
    main()
