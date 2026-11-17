#!/usr/bin/env bash
# 判据：公告详情的可访问性结构（bsk 的 VOM，即屏幕阅读器视角）。
#
# 与 CDP 判据的分工：CDP 读 innerText，能验证「文字出现」；
# 这里验证的是「语义结构」—— dialog 的可访问名、时间线是不是真正的 list。
# 一个用 div 堆出来的时间线在 innerText 上完全一样，但屏幕阅读器读不出层级。
#
# 前置：seed_announcement_ui.py 已造好数据，浏览器已登录且弹窗已打开。
set -uo pipefail
export PATH="$HOME/.local/bin:$PATH"

TITLE='E2E 界面公告'
SNAP="${FIXLOOP_SNAP:-/tmp/bsk-dialog-a11y.txt}"

if ! bsk snapshot > "$SNAP" 2>/dev/null; then
  echo "FAIL[env]: bsk snapshot 失败"
  exit 2
fi

if ! grep -q '^    dialog "' "$SNAP"; then
  echo "FAIL[env]: 页面上没有打开的 dialog，先点开公告"
  exit 2
fi

fail=0

# 1. 弹窗的可访问名必须是公告标题，而不是「公告详情」
#    这条对应 9fd89edec 修的问题：读屏播报「公告详情」时，读者不知道是哪一条。
if grep -qF "dialog \"$TITLE\"" "$SNAP"; then
  echo "PASS  弹窗可访问名 = 公告标题"
else
  echo "FAIL  dialog 的可访问名不是「${TITLE}」"
  grep -m1 '^    dialog "' "$SNAP"
  fail=1
fi

# 2. 时间线必须是 list + listitem，不是一堆 div
if grep -qE '^      list "' "$SNAP"; then
  items=$(grep -cE '^        listitem "' "$SNAP")
  if [ "$items" -eq 3 ]; then
    echo "PASS  时间线是 list，含 3 个 listitem"
  else
    echo "FAIL  时间线 listitem 数量是 ${items}，期望 3"
    fail=1
  fi
else
  echo "FAIL  时间线不是 list 语义结构"
  fail=1
fi

# 3. 「当前」必须挂在第二个 listitem 上
current_line=$(grep -nE '^        listitem "' "$SNAP" | sed -n '2p')
if echo "$current_line" | grep -q '当前'; then
  echo "PASS  「当前」在第二个 listitem 上"
else
  echo "FAIL  第二个 listitem 没有「当前」"
  echo "      ${current_line:-<空>}"
  fail=1
fi

# 4. 之后的 listitem 不带「当前」
if grep -E '^        listitem "' "$SNAP" | sed -n '3,$p' | grep -q '当前'; then
  echo "FAIL  第三个及之后的 listitem 也带了「当前」"
  fail=1
else
  echo "PASS  之后的 listitem 不带「当前」"
fi

exit "$fail"
