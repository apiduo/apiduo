#!/bin/bash
# apiduo-canary 发布脚本（newapi-bff.oneapis.cn / workbuddy.oneapis.cn，主机 apps）
#
# 默认 **无感滚动**：新版本先起在 3006 接流，旧容器优雅退出、主槽位重建完再切回 3002。
#   ./deploy.sh 0.0.228
# 应急/回滚用 --cold：直接停旧容器再起，有停机窗口。
#   ./deploy.sh 0.0.227 --cold
#
# 环境变量来自同目录 apiduo.env（docker inspect 导出，勿手改）。
# 非 compose 创建：同名容器存在时 docker run 会失败，脚本内部先 rm。
#
# 注：--add-host langfuse-minio 是历史遗留 —— Langfuse 媒体 endpoint 已是
# https://oss.s3ai.cn（core-1 共享 MinIO 的 HTTPS 入口），apps 不再需要解析
# MinIO 内部服务名。确认不回退到 langfuse-minio endpoint 时可删这一行。
set -euo pipefail

TAG="${1:?usage: $0 <image-tag> [--cold]}"
MODE="${2:-rolling}"
NAME="${NAME:-apiduo-canary}"
NEXT="${NAME}-next"
PORT=3002
NEXT_PORT=3006
DIR="$(cd "$(dirname "$0")" && pwd)"
IMAGE="ghcr.io/apiduo/apiduo:${TAG}"
NGINX_DIR="${NGINX_DIR:-/www/server/panel/vhost/nginx}"
CONFS=(workbuddy.oneapis.cn.conf newapi-bff.oneapis.cn.conf)
BACKUP_DIR="$DIR/nginx-backup-$(date +%Y%m%d-%H%M%S)"

log() { printf '[deploy %s] %s\n' "$(date +%H:%M:%S)" "$*"; }

# 用 /api/status 的版本号判就绪：这台机器上有别的实例同样回 200，只看 200 会被骗过去。
wait_version() { # $1=port $2=timeout_seconds
  local port=$1 deadline=$(( $(date +%s) + ${2:-120} ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    if curl -fsS --max-time 3 "http://127.0.0.1:${port}/api/status" 2>/dev/null | grep -q "\"version\":\"${TAG}\""; then
      return 0
    fi
    sleep 2
  done
  return 1
}

run_app() { # $1=name $2=host_port $3=update_task $4=data_dir $5=logs_dir $6=restart
  docker rm -f "$1" >/dev/null 2>&1 || true
  docker run -d --name "$1" --restart "$6" \
    --network newapi-for-bff-net --network shared-net \
    --add-host host.docker.internal:host-gateway \
    --add-host langfuse-minio:210.121.44.245 \
    -p "127.0.0.1:$2:3000" \
    -v "$4:/data" -v "$5:/app/logs" \
    --memory 8g --memory-swap 16g --cpus 8 --shm-size 64m \
    --log-opt max-size=50m --log-opt max-file=3 \
    --env-file "$DIR/apiduo.env" \
    -e "UPDATE_TASK=$3" \
    "$IMAGE" --log-dir /app/logs
}

switch_nginx() { # $1=from_port $2=to_port
  local from=$1 to=$2 f
  for f in "${CONFS[@]}"; do
    if [ ! -f "$NGINX_DIR/$f" ]; then
      log "找不到 $NGINX_DIR/$f"
      return 1
    fi
    sed -i "s/127\.0\.0\.1:${from}/127.0.0.1:${to}/g" "$NGINX_DIR/$f"
  done
  nginx -t >/dev/null || return 1
  nginx -s reload
}

restore_nginx() {
  local f
  for f in "${CONFS[@]}"; do
    [ -f "$BACKUP_DIR/$f" ] && cp -a "$BACKUP_DIR/$f" "$NGINX_DIR/$f"
  done
  nginx -t >/dev/null 2>&1 && nginx -s reload || true
}

docker pull "$IMAGE"

if [ "$MODE" = "--cold" ]; then
  log "冷更新 ${IMAGE}（有停机窗口）"
  docker stop -t 150 "$NAME" >/dev/null 2>&1 || true
  run_app "$NAME" "$PORT" true "$DIR/data" "$DIR/logs" always
  wait_version "$PORT" 180 || { log "未在 180s 内就绪"; exit 1; }
  log "完成"
  exit 0
fi

mkdir -p "$BACKUP_DIR" "$DIR/next-data" "$DIR/next-logs"
for f in "${CONFS[@]}"; do
  cp -a "$NGINX_DIR/$f" "$BACKUP_DIR/$f"
done

log "起第二槽位 ${NEXT} :${NEXT_PORT}"
# UPDATE_TASK=false：两个实例同时轮询会重复跑异步任务；扣费走 InitBatchUpdater，
# 不受 NODE_TYPE 限制，且 SQL 是原子增量，所以第二槽位接流不丢计费。
run_app "$NEXT" "$NEXT_PORT" false "$DIR/next-data" "$DIR/next-logs" no
if ! wait_version "$NEXT_PORT" 180; then
  log "第二槽位未就绪，线上未动，退出"
  docker rm -f "$NEXT" >/dev/null 2>&1 || true
  exit 1
fi

log "nginx 切到 :${NEXT_PORT}"
if ! switch_nginx "$PORT" "$NEXT_PORT"; then
  log "nginx 切换失败，恢复备份"
  restore_nginx
  docker rm -f "$NEXT" >/dev/null 2>&1 || true
  exit 1
fi

# 排空：让旧容器上的长连接（SSE / 图片生成）自然结束，再进优雅停止。
log "排空 75s"
sleep 75

log "优雅停止旧容器 ${NAME}（-t 150）"
docker stop -t 150 "$NAME"
docker rm -f "$NAME" >/dev/null 2>&1 || true

log "以 ${TAG} 重建主槽位"
run_app "$NAME" "$PORT" true "$DIR/data" "$DIR/logs" always
if ! wait_version "$PORT" 180; then
  log "主槽位未就绪；nginx 仍指向 ${NEXT_PORT}，请人工检查后切回"
  exit 1
fi

log "nginx 切回 :${PORT}"
switch_nginx "$NEXT_PORT" "$PORT"

log "停第二槽位"
docker stop -t 60 "$NEXT" >/dev/null 2>&1 || true
docker rm -f "$NEXT" >/dev/null 2>&1 || true

log "完成：${IMAGE}；nginx 已回到 ${PORT}；配置备份 ${BACKUP_DIR}"
