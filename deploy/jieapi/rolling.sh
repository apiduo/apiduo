#!/bin/bash
# core-1 双槽位滚动升级（jieapi.com / api.jieapi.com）
#
# 原理与 2026-11-25 那次手工操作一致，只是固化成脚本：
#   1. 先升不吃流量的备槽位（127.0.0.1:3003，compose profile local-standby）
#   2. 主槽位在 nginx 里置 down → 流量落到备槽位
#   3. 排空后优雅停主槽位、以新 tag 重建
#   4. 回切 → 备槽位继续当 backup
#
# 用法：
#   ./rolling.sh 0.0.242              # 正常滚动（自动 mysqldump）
#   ./rolling.sh 0.0.242 --no-backup  # 跳过库备份（改配置类的小版本）
#   ./rolling.sh --check              # 只做前置检查，不动线上
#
# 失败处理：
#   - 备槽位起不来/版本不对 → 直接退出，线上未动
#   - 主槽位重建后版本不对 → nginx 保持在备槽位，退出码 1，人工介入
#
# 前置：备用槽位必须存在且能起。它的 data 目录若残留未消费的 log_spool，
# 启动会重放旧日志 —— 脚本启动前会检查 offset，与文件等长才继续。
set -euo pipefail

COMPOSE_DIR=/www/docker/apiduo
CONF=/www/server/panel/vhost/nginx/0.apiduo-canary.conf
PRIMARY_PORT=3002
STANDBY_PORT=3003
DOMAIN=https://jieapi.com
DRAIN_SECONDS="${DRAIN_SECONDS:-30}"
STOP_TIMEOUT="${STOP_TIMEOUT:-150}"
BACKUP_ROOT=/www/backup/jieapi-upgrade

NGINX_BIN=/www/server/nginx/sbin/nginx
[ -x "$NGINX_BIN" ] || NGINX_BIN=$(command -v nginx)

log()  { printf '[rolling %s] %s\n' "$(date +%H:%M:%S)" "$*"; }
die()  { log "失败：$*"; exit 1; }

version_of() { # $1=port
  curl -fsS --max-time 3 "http://127.0.0.1:$1/api/status" 2>/dev/null \
    | sed -n 's/.*"version":"\([^"]*\)".*/\1/p' | head -1
}

wait_version() { # $1=port $2=tag $3=timeout
  local port=$1 tag=$2 deadline=$(( $(date +%s) + ${3:-180} ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    [ "$(version_of "$port")" = "$tag" ] && return 0
    sleep 2
  done
  return 1
}

nginx_reload() {
  "$NGINX_BIN" -t >/dev/null || die "nginx -t 不通过"
  "$NGINX_BIN" -s reload
}

primary_down() { # 主槽位置 down
  sed -i 's#^\(\s*\)server 127\.0\.0\.1:3002 max_fails#\1server 127.0.0.1:3002 down max_fails#' "$CONF"
  grep -q '127.0.0.1:3002 down ' "$CONF" || die "置 down 失败，检查 $CONF"
}

primary_up() { # 取消 down
  sed -i 's#^\(\s*\)server 127\.0\.0\.1:3002 down max_fails#\1server 127.0.0.1:3002 max_fails#' "$CONF"
  grep -q '127.0.0.1:3002 down ' "$CONF" && die "取消 down 失败，检查 $CONF"
  return 0
}

# ---------------------------------------------------------------- 前置检查
if [ "${1:-}" = "--check" ] || [ "${1:-}" = "-c" ]; then
  log "只做检查，不改任何东西"
  log "主槽位  $PRIMARY_PORT 版本 $(version_of $PRIMARY_PORT)"
  log "备槽位  $STANDBY_PORT 版本 $(version_of $STANDBY_PORT)"
  log "对外入口 $(curl -s -o /dev/null -w '%{http_code}' -m 8 "$DOMAIN/api/status") / 版本 $(curl -s -m 8 -D- -o /dev/null "$DOMAIN/api/status" | sed -n 's/.*[Xx]-[Nn]ew-[Aa]pi-[Vv]ersion: *//p' | tr -d '\r' | head -1)"
  log "nginx   $(grep -c '127.0.0.1:3003 backup' "$CONF") 条 3003 backup"
  log "compose $(grep -m1 'image: ghcr.io/apiduo/apiduo' "$COMPOSE_DIR/docker-compose.yml")"
  spool="$COMPOSE_DIR/../apiduo-standby/data/log_spool"
  if [ -d "$spool" ]; then
    for f in "$spool"/*.jsonl; do
      [ -e "$f" ] || continue
      size=$(stat -c %s "$f")
      off=$(sed -n 's/.*"offset":\([0-9]*\).*/\1/p' "$spool/logspool.offset" 2>/dev/null || echo 0)
      log "spool $(basename "$f") size=$size offset=$off $([ "$size" = "$off" ] && echo 已消费 || echo '未消费→启动会重放，先处理')"
    done
  fi
  exit 0
fi

TAG="${1:?用法: $0 <image-tag> [--no-backup] | $0 --check}"
MODE="${2:-}"
case "$TAG" in
  0.*|v*) ;;
  *) die "tag 看起来不对：$TAG" ;;
esac

cd "$COMPOSE_DIR"
CURRENT=$(version_of $PRIMARY_PORT)
[ -n "$CURRENT" ] || die "主槽位 $PRIMARY_PORT 无响应，先查线上再升级"
[ "$CURRENT" = "$TAG" ] && { log "线上已经是 $TAG，无需升级"; exit 0; }

# 备槽位 spool 必须已消费完，否则启动会重放旧日志
SPOOL="$COMPOSE_DIR/../apiduo-standby/data/log_spool"
if [ -d "$SPOOL" ]; then
  for f in "$SPOOL"/*.jsonl; do
    [ -e "$f" ] || continue
    size=$(stat -c %s "$f")
    off=$(sed -n 's/.*"offset":\([0-9]*\).*/\1/p' "$SPOOL/logspool.offset" 2>/dev/null || echo 0)
    [ "$size" = "$off" ] || die "备槽位 spool 未消费（$(basename "$f") size=$size offset=$off），启动会重放旧日志，先人工处理"
  done
fi

TS=$(date +%Y%m%d-%H%M%S)
BK="$BACKUP_ROOT/rolling-$TS"
mkdir -p "$BK"
log "备份目录 $BK"
cp "$COMPOSE_DIR/docker-compose.yml" "$BK/docker-compose.yml"
cp "$CONF" "$BK/0.apiduo-canary.conf"
docker inspect jieapi-apiduo > "$BK/container.json" 2>/dev/null || true

if [ "$MODE" != "--no-backup" ]; then
  log "mysqldump（--single-transaction）"
  DSN=$(grep -m1 '^SQL_DSN=' app.env | cut -d= -f2-)
  DB_USER=$(python3 -c "import sys;v=sys.argv[1];print(v.split('@tcp(',1)[0].split(':',1)[0])" "$DSN")
  DB_PASS=$(python3 -c "import sys;v=sys.argv[1];print(v.split('@tcp(',1)[0].split(':',1)[1])" "$DSN")
  DB_NAME=$(python3 -c "import sys;v=sys.argv[1];print(v.split(')/',1)[1].split('?',1)[0])" "$DSN")
  MYSQL_PWD="$DB_PASS" mysqldump --single-transaction --quick --routines --triggers \
    --skip-add-locks --set-gtid-purged=OFF -h127.0.0.1 -u"$DB_USER" "$DB_NAME" \
    2>"$BK/dump.err" | gzip > "$BK/mysql.sql.gz"
  gzip -t "$BK/mysql.sql.gz" || die "备份 gzip 校验失败"
  log "备份完成 $(du -h "$BK/mysql.sql.gz" | cut -f1)"
else
  log "跳过库备份（--no-backup）"
fi

log "拉取镜像 ghcr.io/apiduo/apiduo:$TAG"
docker pull "ghcr.io/apiduo/apiduo:$TAG" >/dev/null || die "拉取镜像失败"

sed -i "s#image: ghcr.io/apiduo/apiduo:.*#image: ghcr.io/apiduo/apiduo:${TAG}#" docker-compose.yml
docker-compose -f docker-compose.yml config -q || die "compose 校验失败"
log "compose tag → $TAG"

# ---------------------------------------------------------- 1) 先升备槽位
log "启动/升级备槽位 :$STANDBY_PORT（不吃流量）"
docker-compose --profile local-standby up -d apiduo-standby >/dev/null
if ! wait_version $STANDBY_PORT "$TAG" 180; then
  log "备槽位未就绪，线上未动；回滚 compose tag 到 $CURRENT"
  sed -i "s#image: ghcr.io/apiduo/apiduo:.*#image: ghcr.io/apiduo/apiduo:${CURRENT}#" docker-compose.yml
  die "备槽位版本不是 $TAG"
fi
log "备槽位就绪 $(version_of $STANDBY_PORT)"

# ---------------------------------------------------------------- 2) 切流
log "主槽位置 down，流量切到 :$STANDBY_PORT"
primary_down
nginx_reload
sleep 3
log "落点抽样：$(tail -5 /www/wwwlogs/apiduo-canary.log | awk -F'\t' '{print $8}' | sort | uniq -c | tr '\n' ' ')"

# ---------------------------------------------------------------- 3) 升主
log "排空 ${DRAIN_SECONDS}s（让长连接自然结束）"
sleep "$DRAIN_SECONDS"
log "优雅停止主槽位（-t $STOP_TIMEOUT）"
docker stop -t "$STOP_TIMEOUT" jieapi-apiduo >/dev/null || true
log "以 $TAG 重建主槽位"
docker-compose up -d apiduo >/dev/null
if ! wait_version $PRIMARY_PORT "$TAG" 240; then
  log "主槽位未就绪：nginx 仍指向备槽位 :$STANDBY_PORT，线上可用；请人工排查"
  docker logs --tail 30 jieapi-apiduo 2>&1 | tail -10
  exit 1
fi
log "主槽位就绪 $(version_of $PRIMARY_PORT)"

# ---------------------------------------------------------------- 4) 回切
log "取消 down，回切 :$PRIMARY_PORT"
primary_up
nginx_reload
sleep 3
log "落点抽样：$(tail -5 /www/wwwlogs/apiduo-canary.log | awk -F'\t' '{print $8}' | sort | uniq -c | tr '\n' ' ')"

log "完成：$CURRENT → $TAG；备份 $BK"
log "注意：reload 前的旧 worker 还会把少量请求发到 :$STANDBY_PORT，3 分钟内不要动备槽位"
curl -s -o /dev/null -w '对外入口 %{http_code}\n' -m 8 "$DOMAIN/api/status"
