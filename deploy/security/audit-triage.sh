#!/usr/bin/env bash
#
# 审计分诊 —— 把「权限不足：拒绝访问」的噪音，聚合成一眼能决策的主体清单。
#
# 为什么需要它：access.denied 按「用户 x 方法 x 路由」每分钟落一条，一个脚本
# 扫 5 个管理端点就是 5 行。直接看原始列表只会看到一片 403，看不出「谁在同一
# 分钟内横扫管理面」。分诊按主体聚合，把行为序列还原出来。
#
# 用法（在 ai-prod 上，或 ssh ai-prod 'bash -s' < 本文件）：
#   ./audit-triage.sh          # 最近 24 小时
#   ./audit-triage.sh 72       # 最近 72 小时
#
# 只读：仅 SELECT，不写任何表、不改任何账号。
set -euo pipefail

HOURS=${1:-24}
SINCE=$(( $(date +%s) - HOURS * 3600 ))
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

run_ch() {
  docker cp "$1" apiduo-cn-clickhouse:/tmp/triage.sql >/dev/null
  docker exec apiduo-cn-clickhouse sh -c \
    'clickhouse-client --password="$CLICKHOUSE_PASSWORD" --queries-file=/tmp/triage.sql'
}

# ---- 1. 主体维度：谁在被拒、拒了几个端点、之后有没有拿到令牌 ----
cat > "$WORK/subject.sql" <<SQL
SELECT
    username,
    ip,
    countIf(action = 'access.denied')                AS denied,
    uniqExactIf(route, action = 'access.denied')     AS routes,
    countIf(action = 'token.create')                 AS tokens,
    countIf(action = 'login')                        AS logins,
    any(user_agent)                                  AS ua,
    min(created_at)                                  AS first_seen,
    max(created_at)                                  AS last_seen
FROM apiduo_cn_logs.audit_logs
WHERE created_at >= ${SINCE} AND username != ''
GROUP BY username, ip
HAVING denied > 0 OR tokens > 0
ORDER BY denied DESC, tokens DESC
FORMAT TSV
SQL

# ---- 2. IP 维度：同一出口养了多少个账号（批量注册信号）----
cat > "$WORK/ip.sql" <<SQL
SELECT
    ip,
    uniqExact(username)                              AS accounts,
    countIf(action = 'access.denied')                AS denied,
    uniqExactIf(user_agent, action = 'access.denied') AS ua_kinds,
    min(created_at)                                  AS first_seen,
    max(created_at)                                  AS last_seen
FROM apiduo_cn_logs.audit_logs
WHERE created_at >= ${SINCE} AND username != ''
GROUP BY ip
HAVING denied > 0 OR accounts >= 3
ORDER BY accounts DESC, denied DESC
FORMAT TSV
SQL

echo "================ 主体分诊（近 ${HOURS}h） ================"
run_ch "$WORK/subject.sql" | while IFS=$'\t' read -r user ip denied routes tokens logins ua first last; do
  # 判定不是看单条日志，是看「跨端点」这个动作：单点 403 多半是误点或权限不够，
  # 短时间内横扫多个管理端点才是有人在试探权限边界。
  if   [ "$routes" -ge 3 ] && [ "$tokens" -gt 0 ]; then verdict="扫描+取令牌"
  elif [ "$routes" -ge 3 ];                        then verdict="扫描"
  elif [ "$denied" -gt 0 ] && [ "$tokens" -gt 0 ];  then verdict="踩点+取令牌"
  elif [ "$denied" -gt 0 ];                        then verdict="单点拒绝"
  else                                                  verdict="正常建令牌"
  fi
  printf '%-18s %-16s 拒绝%-4s 端点%-4s 令牌%-4s [%s]\n' \
    "$user" "$ip" "$denied" "$routes" "$tokens" "$verdict"
  printf '    %-18s %s\n' "" "UA ${ua:0:64}  ── $(date -d "@$first" '+%m-%d %H:%M')..$(date -d "@$last" '+%m-%d %H:%M')"
done

echo
echo "================ 出口分诊（近 ${HOURS}h） ================"
run_ch "$WORK/ip.sql" | while IFS=$'\t' read -r ip accounts denied uakinds first last; do
  note=""
  if [ "$accounts" -ge 3 ]; then note="批量注册"; fi
  if [ "$denied" -gt 0 ] && [ "$accounts" -ge 3 ]; then note="批量注册+扫描"; fi
  printf '%-16s 账号%-4s 拒绝%-4s UA种类%-3s %s\n' "$ip" "$accounts" "$denied" "$uakinds" "$note"
done

echo
echo "提示：封 IP 前先确认它是不是运营商 NAT 出口（家宽/移动 4G 段）。"
echo "      NAT 后面可能有无辜用户，那种情况只封账号，不封 IP。"
