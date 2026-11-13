#!/usr/bin/env bash
# 本地测试环境一键启动
#
#   ./up.sh            启动 MySQL + ClickHouse + 应用
#   ./up.sh --mock     额外启动假上游（不花钱的端到端回归）
#   ./down.sh          停止（数据保留在 data/）
#
# 首次运行会自动生成 .env / .env.app（数据库密码随机）与 ClickHouse config.d，
# 之后重复运行不会覆盖已有配置。
set -euo pipefail

cd "$(dirname "$0")"
export PATH="$HOME/.local/bin:$PATH"

COMPOSE=(docker compose -f docker-compose.local.yml)
ROOT="$(cd ../.. && pwd)"

# ---------------------------------------------------------------- 配置自举
mkdir -p data/{mysql,clickhouse,app} data/clickhouse-config/{config.d,users.d}

if [[ ! -f .env ]]; then
  MYSQL_PW="$(openssl rand -base64 24 | tr -d '/+=' | head -c 24)"
  CH_PW="$(openssl rand -base64 24 | tr -d '/+=' | head -c 24)"
  printf 'MYSQL_ROOT_PASSWORD=%s\nCLICKHOUSE_PASSWORD=%s\n' "$MYSQL_PW" "$CH_PW" > .env
  chmod 600 .env
  echo "已生成 .env（数据库密码）"
else
  MYSQL_PW="$(grep '^MYSQL_ROOT_PASSWORD=' .env | cut -d= -f2-)"
  CH_PW="$(grep '^CLICKHOUSE_PASSWORD=' .env | cut -d= -f2-)"
fi

if [[ ! -f .env.app ]]; then
  cat > .env.app <<EOF
PORT=3000
TZ=Asia/Shanghai
SQL_DSN=root:${MYSQL_PW}@tcp(mysql:3306)/apiduo_local?charset=utf8mb4&parseTime=true&loc=Local
LOG_SQL_DSN=clickhouse://default:${CH_PW}@clickhouse:9000/apiduo_local_logs?database=apiduo_local_logs&dial_timeout=10s
SESSION_SECRET=$(openssl rand -hex 32)
DEBUG=false
EOF
  chmod 600 .env.app
  echo "已生成 .env.app（应用配置）"
fi

# ClickHouse 官方镜像默认只监听回环，跨容器连不上；仓库里另有系统日志 TTL 配置
if [[ ! -f data/clickhouse-config/config.d/listen.xml ]]; then
  cat > data/clickhouse-config/config.d/listen.xml <<'EOF'
<clickhouse>
    <listen_host>0.0.0.0</listen_host>
</clickhouse>
EOF
fi
cp -f "$ROOT/deploy/clickhouse/logs.xml" data/clickhouse-config/config.d/logs.xml

# ---------------------------------------------------------------- 构建
BUILD_ID="local-$(date +%m%d%H%M)"
echo "==> 编译 linux/arm64 二进制（${BUILD_ID}）"
(cd "$ROOT" && GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath \
  -ldflags "-s -w -X github.com/QuantumNous/new-api/common.Version=$BUILD_ID" \
  -o bin/apiduo-server-linux-arm64 .)

echo "==> 构建并启动容器"
if [[ "${1:-}" == "--mock" ]]; then
  "${COMPOSE[@]}" --profile mock up -d --build
else
  "${COMPOSE[@]}" up -d --build
fi
"${COMPOSE[@]}" ps

# ---------------------------------------------------------------- 就绪判定
echo "==> 等待应用就绪（判据 /api/status 的 success 字段，不看 HTTP 200）"
for _ in $(seq 1 60); do
  body="$(curl -s --noproxy '*' --max-time 3 http://127.0.0.1:39047/api/status || true)"
  if [[ "$body" == *'"success":true'* ]]; then
    echo "OK  ${body:0:160}"
    break
  fi
  sleep 2
done

echo "==> 初始化状态"
curl -s --noproxy '*' http://127.0.0.1:39047/api/setup || true
echo
echo "控制台：http://127.0.0.1:39047/"
echo "首次使用：/api/setup 的 status 为 false 时，先完成初始化向导"
