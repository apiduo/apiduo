#!/usr/bin/env bash
# 判据：MCP 能力注册面板能编辑 priority / cost，且文案本地化。
# 只读断言，不改任何数据。退出码 0 = 全绿。
set -uo pipefail
export PATH="$HOME/.local/bin:$PATH"
export BSK_SCOPE=e2e

URL="https://apiduo.cn/system-settings/site/mcp"
TARGET="textin-demoire"
fail=0
say() { printf '%s\n' "$*"; }

bsk open "$URL" >/dev/null 2>&1
sleep 2
SNAP=$(bsk sync 2>&1)

# 选中目标模型：ref 每次都会变，按文本反查
PATTERN="@e[0-9]+ button \"${TARGET}"
REF=$(printf '%s' "$SNAP" | grep -oE "$PATTERN" | head -1 | cut -d' ' -f1)
if [ -z "$REF" ]; then
  say "FAIL G0: 列表里找不到 $TARGET"
  exit 1
fi
bsk click "$REF" >/dev/null 2>&1
sleep 1
SNAP=$(bsk sync 2>&1)

check() {
  local id="$1" pattern="$2" desc="$3"
  if printf '%s' "$SNAP" | grep -qF "$pattern"; then
    say "PASS $id  $desc"
  else
    say "FAIL $id  $desc"
    fail=1
  fi
}

check G1 'LabelText "默认优先级"'                    '优先级 label 为中文'
check G2 'LabelText "价格说明（可选）"'              '价格 label 为中文'
check G3 'spinbutton "24"'                           'priority 显示 24'
check G4 '="免费额度池（13 出口）"'                  'cost 显示库中值'

exit "$fail"
