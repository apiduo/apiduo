#!/usr/bin/env bash
# apiduo 日志管道巡检
#
# 为什么需要它：异步日志写入器（LOG_WRITER_ASYNC）的故障是静默的。
# 进程 healthy、API 正常、面板还能开，只是日志页慢慢变空 —— 没有任何告警。
#
# 2026-11-12 线上实际发生：
#   native batch 的 Send 卡在一条坏连接上，flush 循环停摆；
#   /data/log_spool 从 16:10 起不再增长，之后所有日志退化为同步写，
#   MCP 那条 playground 记录至今查不到。
#
# 三条判据，互不依赖，任一命中就告警：
#   1. 心跳  —— 最近 BEAT_WINDOW 秒内容器日志里必须有 "log writer alive"。
#                writer 每 5 分钟打一行，它是"还活着"的唯一外部证据。
#   2. 积压  —— spool 目录超过 SPOOL_ALERT_BYTES，说明批次一直没被确认。
#   3. 退化  —— 心跳里的 fallback 计数在涨，说明队列长期满、writer 跟不上。
#                单独看 fallback 绝对值没意义（重启后归零），必须和上次比。
#
# 用法：
#   ./check-log-pipeline.sh                 # 人读输出
#   APIDUO_ALERT_WEBHOOK=https://... ./check-log-pipeline.sh
#
# 退出码：0=OK  1=ALERT  2=ERROR（查不到）
#
# cron：每 10 分钟一次。心跳窗口 900s 足够容下一次漏采。

set -uo pipefail

CONTAINER=${CONTAINER:-apiduo-cn}
SPOOL_DIR=${SPOOL_DIR:-/data/log_spool}
BEAT_WINDOW=${BEAT_WINDOW:-900}
SPOOL_ALERT_BYTES=${SPOOL_ALERT_BYTES:-8388608}   # 8 MiB；正常应接近 0
STATE=${STATE:-/tmp/apiduo-log-pipeline.state}

ts() { date '+%F %T'; }
say() { printf '%s %s\n' "$(ts)" "$*"; }

alert() {
    say "ALERT $*"
    if [ -n "${APIDUO_ALERT_WEBHOOK:-}" ]; then
        curl -sS --noproxy '*' -m 10 -X POST "$APIDUO_ALERT_WEBHOOK" \
            -H 'Content-Type: application/json' \
            -d "{\"text\":\"[apiduo log-pipeline] $*\"}" >/dev/null 2>&1 || say "WARN webhook failed"
    fi
    exit 1
}

docker inspect "$CONTAINER" >/dev/null 2>&1 || { say "ERROR container $CONTAINER not found"; exit 2; }

# ---------- 1. 心跳 ----------
BEAT_LINE=$(docker logs "$CONTAINER" --since "${BEAT_WINDOW}s" 2>&1 | grep 'log writer alive' | tail -1)
# 刚重启的进程还没来得及打第一行心跳：用启动时间豁免一个心跳周期。
if [ -z "$BEAT_LINE" ]; then
    STARTED=$(docker inspect "$CONTAINER" --format '{{.State.StartedAt}}' 2>/dev/null)
    STARTED_EPOCH=$(date -d "$STARTED" +%s 2>/dev/null || echo 0)
    NOW_EPOCH=$(date +%s)
    if [ "$STARTED_EPOCH" -gt 0 ] && [ $((NOW_EPOCH - STARTED_EPOCH)) -lt 600 ]; then
        say "OK writer started $((NOW_EPOCH - STARTED_EPOCH))s ago, first heartbeat pending"
        exit 0
    fi
    alert "no 'log writer alive' heartbeat in ${BEAT_WINDOW}s — async log writer is stopped or wedged"
fi

# ---------- 2. spool 积压 ----------
SPOOL_BYTES=$(docker exec "$CONTAINER" sh -c "du -sb ${SPOOL_DIR} 2>/dev/null | cut -f1" 2>/dev/null)
if [ -z "$SPOOL_BYTES" ]; then
    say "WARN cannot measure ${SPOOL_DIR} (writer may be disabled)"
else
    [ "$SPOOL_BYTES" -gt "$SPOOL_ALERT_BYTES" ] && \
        alert "log spool backlog ${SPOOL_BYTES} bytes exceeds ${SPOOL_ALERT_BYTES} — batches are not being acknowledged"
fi

# ---------- 3. fallback 增速 ----------
FALLBACK=$(printf '%s' "$BEAT_LINE" | sed -n 's/.*fallback=\([0-9]*\).*/\1/p')
PENDING=$(printf '%s' "$BEAT_LINE" | sed -n 's/.*pending=\([0-9]*\).*/\1/p')
QUEUED=$(printf '%s' "$BEAT_LINE" | sed -n 's/.*queued=\([0-9]*\).*/\1/p')

if [ -n "$FALLBACK" ] && [ -f "$STATE" ]; then
    PREV=$(cat "$STATE" 2>/dev/null || echo "")
    if [ -n "$PREV" ] && [ "$FALLBACK" -gt "$PREV" ]; then
        alert "log writer queue is overflowing: fallback ${PREV} -> ${FALLBACK} since last check"
    fi
fi
[ -n "$FALLBACK" ] && printf '%s' "$FALLBACK" > "$STATE"

say "OK heartbeat ok queued=${QUEUED:-?} pending=${PENDING:-?} fallback=${FALLBACK:-?} spool=${SPOOL_BYTES:-?}B"
exit 0
