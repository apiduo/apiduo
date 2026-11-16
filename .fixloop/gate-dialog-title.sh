#!/usr/bin/env bash
# 判据：公告详情弹窗内必须显示该公告自己的标题。
#
# 现场由 prepare.sh 准备好（打开 dashboard → 点第一条公告）。
# 本脚本只做断言，因为 bsk 的点击带 intent-WAL 对账，
# 把「准备」和「断言」混在一起会让失败原因不可定位。
set -uo pipefail
export PATH="$HOME/.local/bin:$PATH"

TITLE='计费单位调整为按 token 精确计费'
SNAP="${FIXLOOP_SNAP:-/tmp/fixloop-snap.txt}"
DIALOG=/tmp/fixloop-dialog.txt

if ! bsk snapshot > "$SNAP" 2>/dev/null; then
  echo "FAIL[env]: bsk snapshot 失败，浏览器或 session 不可用"
  exit 2
fi

# dialog 块 = 以 4 空格开头的 dialog 行，到下一个 4 空格开头的非子元素为止。
# 子元素缩进 6+ 空格，所以 /^    [^ ]/ 只匹配回到 4 空格的控制行。
awk '/^    dialog "/{f=1; print; next} f && /^    [^ ]/{f=0} f{print}' "$SNAP" > "$DIALOG"

if [ ! -s "$DIALOG" ]; then
  echo "FAIL[env]: 页面上没有打开的 dialog，先跑 prepare.sh"
  exit 2
fi

if grep -qF "$TITLE" "$DIALOG"; then
  echo "PASS: dialog 内显示了公告标题"
  exit 0
fi

# 模态打开时 VOM 只返回 dialog，列表不在快照里 —— 不能拿"列表里有没有"来判断
echo "FAIL[impl]: dialog 内没有公告标题（快照 $(wc -l < "$SNAP") 行，仅 dialog）"
exit 1
