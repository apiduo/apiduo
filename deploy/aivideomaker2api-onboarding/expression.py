#!/usr/bin/env python3
"""把 aivideomaker2api 的 10 个视频模型切到计费表达式（tiered_expr）。

旧的 ModelPrice 按次定价对新版 apiduo 已标记弃用；这条脚本把计费口径
整体搬到表达式，并复用适配器新暴露的用量事实：

    count                    恒为 1            → 按次
    seconds                  请求时长（兜底 5） → 按秒
    tokens                   上游回包的 token 数 → 按 token（avm 恒为 0）
    factor                   渠道 factors 各倍率之积 → 分辨率加价

价格口径（2026-11-19 用户定）：480p 0.01 美元/秒，720p 0.02；
1080p 与未识别分辨率落 factor 默认值 ×2。

表达式对单个模型生效，`u("tokens")` 在当前上游恒为 0，所以按 token 只是
"可用但不出账"；接真正返回 usage 的上游时同一套 facts 即可结算。

用法:
    APIDUO_TOKEN=nap_... python3 expression.py --dry-run
    APIDUO_TOKEN=nap_... python3 expression.py --apply
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

# 每秒基础价 0.01；分辨率由 facts 的 factor（渠道因子之积）乘上来。
EXPR = 'tier("base", u("seconds") * u("factor") * 0.01)'
MODE = "tiered_expr"


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
    entries = {e["model_name"]: e for e in data["entries"]}
    versions = {name: (entries.get(name) or {}).get("version") or empty_version for name in MODELS}

    changes = [
        {
            "model_name": name,
            "expected_version": versions[name],
            "pricing": {
                "billing_setting.billing_mode": MODE,
                "billing_setting.billing_expr": EXPR,
            },
        }
        for name in MODELS
    ]

    for name in MODELS:
        entry = entries.get(name) or {}
        current = (entry.get("effective") or {})
        print(
            f"{name:<16} 现值 mode={current.get('billing_setting.billing_mode') or '-'} "
            f"expr={str(current.get('billing_setting.billing_expr') or '-')[:48]}"
        )

    print(f"\n目标表达式: {EXPR}")

    if not args.apply:
        print(f"\n共 {len(changes)} 条。加 --apply 写入 {BASE}")
        return

    result = call("PATCH", "/api/option/model_pricing", {"changes": changes}, headers)
    if not result.get("success"):
        sys.exit(f"写入失败: {result.get('message')}")
    print(f"\n✓ 已更新 {len(result['data']['updated_models'])} 个模型")

    after = call("GET", "/api/option/model_pricing", headers=headers)
    rows = {e["model_name"]: e for e in after["data"]["entries"]}
    print("\n回读:")
    for name in MODELS:
        effective = (rows.get(name) or {}).get("effective") or {}
        print(
            f"  {name:<16} mode={effective.get('billing_setting.billing_mode')} "
            f"expr={str(effective.get('billing_setting.billing_expr'))[:60]}"
        )


if __name__ == "__main__":
    main()
