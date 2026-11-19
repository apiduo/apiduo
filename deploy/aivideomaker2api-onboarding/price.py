#!/usr/bin/env python3
"""给 aivideomaker2api 的 10 个视频模型配按秒基础价。

口径（用户 2026-11-19 定）：
    480p 0.01 美元/秒，720p 0.02 —— 分辨率通过渠道 settings 的
    billing.factors 乘价，ModelPrice 只写 480p 那一档。
    1080p 暂不单列，落 default 因子 ×2（等同 720p 价）。

因此这里每个模型都写 ModelPrice=0.01（quota_type=1 按次计价位），
真正的每秒金额 = ModelPrice × duration × resolution_factor。

用法:
    APIDUO_TOKEN=nap_... python3 price.py --dry-run
    APIDUO_TOKEN=nap_... python3 price.py --apply
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

MODELS = [
    "minimaxH3",
    "seedance20",
    "HappyHorse",
    "seedance25",
    "ltx23",
    "t2v",
    "i2v",
    "t2v_v3",
    "i2v_v3",
    "wan30",
]

# 480p 基准价；720p = ×2，1080p = ×3 由渠道 factors 完成。
BASE_PER_SECOND = 0.01


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
            return {"success": False, "message": f"HTTP {err.code}: {raw[:200]}"}


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--apply", action="store_true")
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()

    token = os.environ.get("APIDUO_TOKEN")
    if not token:
        sys.exit("需要 APIDUO_TOKEN")
    headers = {"Authorization": f"Bearer {token}"}

    spec = call("GET", "/api/option/model_pricing", headers=headers)
    if not spec.get("success"):
        sys.exit(f"读取定价配置失败: {spec.get('message')}")
    data = spec["data"]
    empty_version = data["empty_version"]
    versions = {e["model_name"]: e["version"] for e in data["entries"]}
    current = {m["model_name"]: m for m in (call("GET", "/api/pricing").get("data") or [])}

    changes = [
        {
            "model_name": name,
            "expected_version": versions.get(name, empty_version),
            "pricing": {"ModelPrice": BASE_PER_SECOND},
        }
        for name in MODELS
    ]

    for change in changes:
        name = change["model_name"]
        row = current.get(name) or {}
        print(
            f"{name:<16} 现值 ratio={row.get('model_ratio')} price={row.get('model_price')} "
            f"type={row.get('quota_type')} → ModelPrice={BASE_PER_SECOND}"
        )

    if not args.apply:
        print(f"\n共 {len(changes)} 条。加 --apply 写入 {BASE}")
        return

    result = call("PATCH", "/api/option/model_pricing", {"changes": changes}, headers)
    if not result.get("success"):
        sys.exit(f"写入失败: {result.get('message')}")
    print(f"\n✓ 已更新 {len(result['data']['updated_models'])} 个模型")

    after = {m["model_name"]: m for m in (call("GET", "/api/pricing").get("data") or [])}
    print("\n回读:")
    for change in changes:
        name = change["model_name"]
        row = after.get(name) or {}
        print(
            f"  {name:<16} ratio={row.get('model_ratio')} price={row.get('model_price')} "
            f"quota_type={row.get('quota_type')}"
        )


if __name__ == "__main__":
    main()
