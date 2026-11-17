#!/usr/bin/env bash
# 公告功能的端到端判据（API 层 + UI 层），一条命令判定。
#
# 退出码 0 = 全通过。任何一层失败立即停，不继续跑下一层。
# 跑完自动清理测试数据，可重复执行。
set -uo pipefail
cd "$(dirname "$0")/.." || exit 1

CDP_PORT="${CDP_PORT:-9241}"
PROFILE="/tmp/cdp-e2e-announcement"

cleanup() {
  python3 - "$@" <<'PY'
import json, sys, time, urllib.request, urllib.error
BASE = "http://127.0.0.1:39047"

def call(m, p, b=None, t=None):
    d = json.dumps(b).encode() if b is not None else None
    r = urllib.request.Request(BASE + p, data=d, method=m)
    r.add_header("Content-Type", "application/json")
    if t:
        r.add_header("Authorization", "Bearer " + t)
    try:
        with urllib.request.urlopen(r, timeout=10) as x:
            return x.status, json.loads(x.read().decode())
    except urllib.error.HTTPError as e:
        return e.code, json.loads(e.read().decode() or "{}")

s = None
for _ in range(5):
    c, b = call("POST", "/api/user/login", {"username": "preview", "password": "Passw0rd!2026"})
    if c == 200 and b.get("success"):
        s = b["data"]["access_token"]
        break
    time.sleep(7)
if not s:
    sys.exit(0)
for a in call("GET", "/api/announcements", None, s)[1].get("data") or []:
    if "E2E" in (a.get("title") or ""):
        call("DELETE", f"/api/announcements/{a['id']}", None, s)
PY
}
trap cleanup EXIT

echo "== API 层 =="
python3 .fixloop/e2e_announcement_api.py || { echo "FAIL: API 判据"; exit 1; }

echo
echo "== 准备 UI 数据 =="
IDS=$(python3 .fixloop/seed_announcement_ui.py) || { echo "FAIL: 造数据"; exit 1; }
echo "  created ids: $IDS"

echo
echo "== 浏览器 =="
if ! curl -s --max-time 3 "http://127.0.0.1:${CDP_PORT}/json/version" >/dev/null 2>&1; then
  rm -rf "$PROFILE"
  "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" \
    --headless=new --disable-gpu --no-sandbox \
    --remote-debugging-port="${CDP_PORT}" --user-data-dir="$PROFILE" \
    --hide-scrollbars --window-size=1440,1200 about:blank \
    >/tmp/cdp-e2e.log 2>&1 &
  for _ in $(seq 1 20); do
    curl -s --max-time 2 "http://127.0.0.1:${CDP_PORT}/json/version" >/dev/null 2>&1 && break
    sleep 0.5
  done
  echo "  已启动 headless chrome :${CDP_PORT}"
else
  echo "  复用已存在的 :${CDP_PORT}"
fi

echo
echo "== UI 层 =="
CDP_PORT="$CDP_PORT" node .fixloop/e2e_announcement_ui.mjs
RC=$?

echo
if [ $RC -eq 0 ]; then
  echo "== 全绿 =="
else
  echo "== 有失败项，见上 =="
fi
exit $RC
