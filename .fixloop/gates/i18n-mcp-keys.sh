#!/usr/bin/env bash
# 判据：7 个 apiduo 语言包都带 `mcp.` 前缀的新键。
# 前缀是硬约定 —— useMcpCopy 会给每个 key 加 `mcp.`，少一层就永远命中不了。
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1
fail=0
for f in web/src/i18n/apiduo/*.json; do
  out=$(python3 - "$f" <<'PY'
import json, sys
path = sys.argv[1]
d = json.load(open(path, encoding="utf-8"))["translation"]
keys = ["mcp.Default priority", "mcp.Cost note (optional)",
        "mcp.Higher wins when the caller omits model. Competes inside the same output kind; ties fall back to id.",
        "mcp.$0.01/run — shown to agents verbatim",
        "mcp.Free text: fixed price, ratio, or an expression. Never parsed into a number."]
miss = [k for k in keys if k not in d]
if miss:
    print("MISSING %d in %s: %s" % (len(miss), path, miss[0][:40]))
    sys.exit(1)
print("ok %s" % path)
PY
) || fail=1
  echo "$out"
done
exit "$fail"
