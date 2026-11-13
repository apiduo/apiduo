#!/usr/bin/env bash
# 停止本地测试环境。数据保留在 data/，下次 up.sh 直接复用。
#   ./down.sh           停止并移除容器（保留数据卷目录）
#   ./down.sh --purge   连同 data/ 一起清空（不可恢复）
set -euo pipefail

cd "$(dirname "$0")"
COMPOSE=(docker compose -f docker-compose.local.yml)

"${COMPOSE[@]}" --profile mock down --remove-orphans

if [[ "${1:-}" == "--purge" ]]; then
  echo "==> 清空 data/（数据库与上传文件全部丢失）"
  find data -mindepth 1 -maxdepth 1 -exec rm -rf {} +
  mkdir -p data/{mysql,clickhouse,app} data/clickhouse-config/{config.d,users.d}
fi
