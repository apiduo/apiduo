#!/usr/bin/env bash
# 从 docs/apiduo/brand/*.svg 重新生成站点与 Electron 的全部图标位图。
#
#   bash docs/apiduo/brand/rebuild.sh
#
# 为什么要 4× 再缩：直接按 16px 栅格化时，中间那道 1px 的缝会断、边缘会毛；
# 先按目标尺寸的 4 倍渲染，再用 LANCZOS 缩下来，小尺寸才稳。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
BRAND="$ROOT/docs/apiduo/brand"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

need() { command -v "$1" >/dev/null 2>&1 || { echo "缺少 $1"; exit 1; }; }
need node
need python3

PUPPETEER="$ROOT/web/node_modules/puppeteer-core"
[ -d "$PUPPETEER" ] || { echo "缺少 web/node_modules/puppeteer-core，先在 web/ 里 bun install"; exit 1; }

echo "→ 渲染 4× 位图"
BRAND="$BRAND" TMP="$TMP" PUPPETEER="$PUPPETEER" node --input-type=module -e '
import fs from "node:fs";
import { createRequire } from "node:module";
const require = createRequire(process.cwd() + "/");
const puppeteer = require(process.env.PUPPETEER);
const BRAND = process.env.BRAND, TMP = process.env.TMP;
const color = fs.readFileSync(BRAND + "/apiduo-mark-icon.svg", "utf8");
const mono  = fs.readFileSync(BRAND + "/apiduo-mark-mono.svg", "utf8")
  .replace(/currentColor/g, "#000").replace(/opacity="\.55"/g, "opacity=\"1\"");

const jobs = [
  ["logo-256", 256, color], ["electron-icon-512", 512, color],
  ["tray-windows-32", 32, color],
  ["tray-template-22", 22, mono], ["tray-template-44", 44, mono],
  ...[256, 128, 64, 48, 32, 16].map(s => ["fav-" + s, s, color]),
];

const browser = await puppeteer.launch({
  executablePath: process.env.CHROME || "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
  headless: "new",
  args: ["--no-proxy-server", "--force-device-scale-factor=1"],
});

for (const [name, size, svg] of jobs) {
  const R = size * 4;
  const page = await browser.newPage();
  await page.setViewport({ width: R, height: R, deviceScaleFactor: 1 });
  await page.setContent(`<!DOCTYPE html><style>html,body{margin:0;background:transparent}svg{display:block;width:${R}px;height:${R}px}</style>${svg}`);
  await new Promise(r => setTimeout(r, 120));
  await page.screenshot({ path: `${TMP}/${name}@4x.png`, omitBackground: true, clip: { x: 0, y: 0, width: R, height: R } });
  await page.close();
}
await browser.close();
'

echo "→ 缩放并合成 ico"
TMP="$TMP" ROOT="$ROOT" python3 - <<'PY'
import os, pathlib
from PIL import Image

tmp = pathlib.Path(os.environ["TMP"])
root = pathlib.Path(os.environ["ROOT"])
jobs = [("logo-256", 256), ("electron-icon-512", 512), ("tray-windows-32", 32),
        ("tray-template-22", 22), ("tray-template-44", 44),
        ("fav-256", 256), ("fav-128", 128), ("fav-64", 64), ("fav-48", 48),
        ("fav-32", 32), ("fav-16", 16)]
for name, size in jobs:
    im = Image.open(tmp / f"{name}@4x.png").convert("RGBA")
    im.resize((size, size), Image.LANCZOS).save(tmp / f"{name}.png")

base = Image.open(tmp / "fav-256.png")
base.save(tmp / "favicon.ico", format="ICO",
          sizes=[(16, 16), (32, 32), (48, 48), (64, 64), (128, 128), (256, 256)])

targets = {
    "logo-256.png": "web/public/logo.png",
    "favicon.ico": "web/public/favicon.ico",
    "electron-icon-512.png": "electron/icon.png",
    "tray-windows-32.png": "electron/tray-icon-windows.png",
    "tray-template-22.png": "electron/tray-iconTemplate.png",
    "tray-template-44.png": "electron/tray-iconTemplate@2x.png",
}
for src, dst in targets.items():
    out = root / dst
    out.write_bytes((tmp / src).read_bytes())
    print(f"  {dst:44} {(tmp / src).stat().st_size:>7} bytes")

# 矢量源同步一份到 public，前端可直接引用
(root / "web/public/logo.svg").write_text(
    (root / "docs/apiduo/brand/apiduo-mark.svg").read_text(encoding="utf-8"), encoding="utf-8")
print(f"  {'web/public/logo.svg':44} {(root / 'web/public/logo.svg').stat().st_size:>7} bytes")
PY

echo "✓ 完成。改动文件："
cd "$ROOT" && git status --short -- web/public electron | sed 's/^/  /'
