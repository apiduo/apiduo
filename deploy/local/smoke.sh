#!/usr/bin/env bash
# 冒烟检查：健康态 + 可选一次真实转发（需要已建令牌的明文 key）
# 用法：
#   ./smoke.sh                只看健康态与初始化状态
#   ./smoke.sh <token-key> [model]
set -euo pipefail

BASE="${BASE:-http://127.0.0.1:39047}"
PROXY_ARGS=(--noproxy '*')

echo "== /api/status"
curl -s "${PROXY_ARGS[@]}" "$BASE/api/status" | head -c 240; echo

echo "== /api/setup"
curl -s "${PROXY_ARGS[@]}" "$BASE/api/setup"; echo

if [[ $# -ge 1 ]]; then
  model="${2:-mock-gpt}"
  echo "== /v1/chat/completions model=$model"
  curl -s "${PROXY_ARGS[@]}" -X POST "$BASE/v1/chat/completions" \
    -H "Authorization: Bearer sk-$1" \
    -H 'Content-Type: application/json' \
    -d "{\"model\":\"$model\",\"messages\":[{\"role\":\"user\",\"content\":\"ping\"}]}" | head -c 400
  echo
fi
