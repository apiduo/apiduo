#!/usr/bin/env bash
# 判据：公告面板只能列 announcement，changelog 必须只出现在顶部横幅。
#
# 观察方式与 CDP 判据不同：这里用 bsk 的 VOM（可访问性树），
# 面板里的条目渲染成 `button "标题 日期 级别"`，而横幅里的是 listitem。
# 所以「snapshot 里存在 button "标题」= 它被当成了公告。
#
# 前置：seed_announcement_ui.py 已造好「E2E 功能更新」，浏览器已登录且停在仪表盘。
set -uo pipefail
export PATH="$HOME/.local/bin:$PATH"

TITLE='E2E 功能更新'
SNAP="${FIXLOOP_SNAP:-/tmp/bsk-announcement-panel.txt}"

# 先确认服务端确实下发了这条 changelog，否则「没找到」是假绿
if ! curl -s --max-time 5 http://127.0.0.1:39047/api/status \
  | grep -q "$TITLE"; then
  echo "FAIL[env]: 服务端没有「${TITLE}」，先跑 seed_announcement_ui.py"
  exit 2
fi

if ! bsk snapshot > "$SNAP" 2>/dev/null; then
  echo "FAIL[env]: bsk snapshot 失败"
  exit 2
fi

if grep -qE "button \"${TITLE}" "$SNAP"; then
  echo "FAIL[impl]: changelog 出现在公告面板的按钮列表里"
  grep -nE "button \"${TITLE}" "$SNAP" | head -3
  exit 1
fi

# 不要求「页面别处也能看到它」：横幅是否显示取决于 localStorage 的已读状态，
# 而 bsk 无法清除它。把它当判据会让同一条断言在已读与否两种状态下自相矛盾。
# 「服务端确实下发了」已在上面用 curl 确认，数据没到前端的情况由 UI 判据覆盖。
echo "PASS: changelog 不在公告面板的按钮列表里"
exit 0
