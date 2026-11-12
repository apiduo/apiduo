#!/usr/bin/env python3
"""给 qwen2api 的 12 个模型补定价。

新模型默认落到全局兜底倍率 37.5（= OpenAI 基准的 37.5 倍），必须显式配置。
这里全部对齐现有同类：

- 文本 10 个    → ModelRatio 0.137 / CompletionRatio 4（同 deepseek-v4-1-flash，国内自建号池）
- qwen-image-3.0 → ModelPrice 0.01（同 nano-banana-2，按张）
- happyhorse11   → ModelPrice 0.05（同 hailuo-video，按次）

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
from pathlib import Path

BASE = os.environ.get("APIDUO_BASE", "https://apiduo.cn").rstrip("/")
HERE = Path(__file__).resolve().parent
OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))

TEXT_MODELS = [
    "qwen3.7-plus",
    "qwen4.0-max-preview",
    "qwen3.8-max",
    "qwen3.8-omni-flash",
    "qwen3.7-max",
    "qwen3.6-plus",
    "qwen3.5-plus",
    "qwen3.5-omni-plus",
    "qwen3.8-max-agent",
    "qwen3.8-flash-agent",
]

TEXT_PRICING = {"ModelRatio": 0.137, "CompletionRatio": 4}
PER_CALL = {"qwen-image-3.0": 0.01, "happyhorse11": 0.05, "happyhorse1.1": 0.05}


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
    configured = {e["model_name"]: e.get("configured") for e in data["entries"]}
    current = {m["model_name"]: m for m in (call("GET", "/api/pricing").get("data") or [])}

    changes = []
    for name in TEXT_MODELS:
        changes.append({"model_name": name, "expected_version": versions.get(name, empty_version), "pricing": TEXT_PRICING})
    for name, price in PER_CALL.items():
        changes.append({"model_name": name, "expected_version": versions.get(name, empty_version), "pricing": {"ModelPrice": price}})

    for change in changes:
        name = change["model_name"]
        row = current.get(name) or {}
        print(
            f"{name:<24} 现值 ratio={row.get('model_ratio')} price={row.get('model_price')} "
            f"type={row.get('quota_type')} → {json.dumps(change['pricing'], ensure_ascii=False)}"
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
            f"  {name:<24} ratio={row.get('model_ratio')} price={row.get('model_price')} "
            f"quota_type={row.get('quota_type')}"
        )


if __name__ == "__main__":
    main()
