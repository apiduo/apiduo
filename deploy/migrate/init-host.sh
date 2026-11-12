#!/usr/bin/env bash
# 新机初始化：内核参数 / Docker 守护进程 / 目录 / 密钥。在**新机 root** 执行。
#
# 幂等：可重复跑。DRY_RUN=1 只打印不改。
#
# 为什么这些值不保守 —— 前提是新机是**独占**的。旧机 103 个容器抢一块盘，
# 写延迟 90ms，导致 COMMIT 卡 162 秒。如果新机也混跑，下面所有调优都会被
# IO 争抢抹平，那时该做的是换机器，不是继续调参数。
set -euo pipefail

DRY_RUN="${DRY_RUN:-0}"
APP_DIR="${APP_DIR:-/opt/apiduo}"
DATA_DIR="${DATA_DIR:-/www/data/apiduo}"

log() { printf '\033[36m==>\033[0m %s\n' "$*"; }
skip() { printf '\033[33mskip\033[0m %s\n' "$*"; }

[ "$(id -u)" = "0" ] || { echo "必须以 root 执行" >&2; exit 1; }

# ---------------- 0. 先看清这台机器的底子 ----------------
log "机器概况"
echo "  CPU 核数: $(nproc)"
echo "  内存:     $(free -g | awk '/^Mem:/{print $2"G 总 / "$7"G 可用"}')"
PARENT="$(dirname "$DATA_DIR")"
echo "  磁盘:     $(df -h "$PARENT" 2>/dev/null | awk 'NR==2{print $2" 总 / "$4" 可用 ("$5" 已用)"}')"
DEV="$(df -P "$PARENT" 2>/dev/null | awk 'NR==2{print $1}' | sed 's#/dev/##;s#[0-9]*$##')"
if [ -n "$DEV" ] && [ -r "/sys/block/$DEV/queue/rotational" ]; then
    if [ "$(cat "/sys/block/$DEV/queue/rotational")" = "1" ]; then
        echo "  ⚠️  $DEV 是机械盘，MySQL/CH 会慢一个数量级；innodb_io_capacity 要降到 200"
    fi
    echo "  磁盘调度: $(cat "/sys/block/$DEV/queue/scheduler" 2>/dev/null)"
fi

# ---------------- 1. 内核参数 ----------------
# 关键几项的理由：
#   somaxconn/backlog：SSE 与批量客户端同时握手，默认 128 在几百并发时就开始丢 SYN
#   ip_local_port_range：网关对上游是主动连接方，端口范围就是「每秒能新建多少上游连接」的上限
#   tcp_tw_reuse：短连接多，TIME_WAIT 复用能救回大量端口
#   overcommit_memory=1：ClickHouse 建议值，避免 fork 时 overcommit 估计失败
#   swappiness=1：数据库要内存不要 swap
log "内核参数 → /etc/sysctl.d/99-apiduo.conf"
if [ "$DRY_RUN" = "1" ]; then
    skip "会写入 somaxconn=65535 / port_range=10240-65000 / file-max=4194304 / swappiness=1"
else
    cat > /etc/sysctl.d/99-apiduo.conf <<'EOT'
# apiduo 网关：高并发长连接 + 大量对上游的主动连接
net.core.somaxconn = 65535
net.core.netdev_max_backlog = 65535
net.ipv4.tcp_max_syn_backlog = 65535
net.ipv4.tcp_max_tw_buckets = 262144
net.ipv4.tcp_tw_reuse = 1
net.ipv4.tcp_fin_timeout = 15
net.ipv4.tcp_keepalive_time = 300
net.ipv4.tcp_keepalive_intvl = 30
net.ipv4.tcp_keepalive_probes = 5
# 端口范围决定「每秒能对上游新建多少连接」，默认范围里有大量被保留段
net.ipv4.ip_local_port_range = 10240 65000
net.core.rmem_max = 134217728
net.core.wmem_max = 134217728
net.ipv4.tcp_rmem = 4096 262144 134217728
net.ipv4.tcp_wmem = 4096 262144 134217728
net.ipv4.tcp_slow_start_after_idle = 0
fs.file-max = 4194304
fs.nr_open = 4194304
vm.swappiness = 1
vm.overcommit_memory = 1
vm.max_map_count = 262144
EOT
    sysctl --system >/dev/null 2>&1 || true
fi

# ---------------- 2. Docker 守护进程 ----------------
# 默认 nofile 只有 1024（旧机 nginx worker 卡在 8192）。网关是「每个 SSE 连接 1 个 fd」
# 的模型，这个上限直接等于并发天花板。
log "Docker default-ulimits → /etc/docker/daemon.json"
if [ "$DRY_RUN" = "1" ]; then
    skip "会写 daemon.json：nofile 1048576 / nproc 65535 / live-restore"
else
    mkdir -p /etc/docker
    if [ -f /etc/docker/daemon.json ]; then
        cp -p /etc/docker/daemon.json "/etc/docker/daemon.json.bak-$(date +%Y%m%d-%H%M%S)"
        echo "  已备份原 daemon.json"
    fi
    cat > /etc/docker/daemon.json <<'EOT'
{
  "default-ulimits": {
    "nofile": { "Name": "nofile", "Soft": 1048576, "Hard": 1048576 },
    "nproc":  { "Name": "nproc",  "Soft": 65535,   "Hard": 65535 }
  },
  "log-driver": "json-file",
  "log-opts": { "max-size": "100m", "max-file": "3" },
  "live-restore": true
}
EOT
    if systemctl is-active --quiet docker; then
        log "重启 docker（会重启现有容器；新机上此时应为空）"
        systemctl restart docker
    fi
fi

# ---------------- 3. 目录 ----------------
log "目录"
if [ "$DRY_RUN" = "1" ]; then
    skip "会创建 $APP_DIR 与 $DATA_DIR/{data,mysql,clickhouse,clickhouse-config}"
else
    mkdir -p "$APP_DIR" \
             "$DATA_DIR/data" "$DATA_DIR/mysql" "$DATA_DIR/clickhouse" \
             "$DATA_DIR/clickhouse-config/config.d" "$DATA_DIR/clickhouse-config/users.d" \
             /www/backup/apiduo
    chmod 700 "$APP_DIR"
fi

# ---------------- 4. 密钥 ----------------
# 一次生成就固定：SESSION_SECRET/CRYPTO_SECRET 变了会让所有会话失效、
# 已加密字段读不出来。所以只在文件不存在时生成。
log "密钥（已存在则跳过）"
if [ -f "$APP_DIR/.env.db" ]; then
    skip "$APP_DIR/.env.db 已存在，保留原值"
elif [ "$DRY_RUN" = "1" ]; then
    skip "会生成 $APP_DIR/.env.db 与 $DATA_DIR/.env（含随机密码与 SESSION_SECRET）"
else
    umask 077
    MYSQL_PW="$(openssl rand -base64 48 | tr -dc 'A-Za-z0-9' | head -c 32)"
    CH_PW="$(openssl rand -base64 48 | tr -dc 'A-Za-z0-9' | head -c 32)"

    cat > "$APP_DIR/.env.db" <<EOT
MYSQL_ROOT_PASSWORD=$MYSQL_PW
CLICKHOUSE_PASSWORD=$CH_PW
APIDUO_IMAGE_TAG=latest
EOT
    chmod 600 "$APP_DIR/.env.db"
    echo "  已生成 $APP_DIR/.env.db"

    if [ -f "$DATA_DIR/.env" ]; then
        skip "$DATA_DIR/.env 已存在，保留原值"
    else
        cat > "$DATA_DIR/.env" <<EOT
TZ=Asia/Shanghai
GIN_MODE=release
NODE_NAME=apiduo-node-1
SQL_DSN=root:$MYSQL_PW@tcp(apiduo-mysql:3306)/apiduo?parseTime=true
LOG_SQL_DSN=clickhouse://default:$CH_PW@apiduo-clickhouse:9000/apiduo_logs
LOG_SQL_CLICKHOUSE_TTL_DAYS=360
LOG_WRITER_ASYNC=true
LOG_WRITER_INTERVAL_MS=1000
LOG_WRITER_BATCH_SIZE=2000
LOG_WRITER_QUEUE_SIZE=100000
LOG_WRITER_SPOOL_MAX_MB=512
LOG_WRITER_SPOOL_DIR=/data/log_spool
SESSION_SECRET=$(openssl rand -hex 32)
CRYPTO_SECRET=$(openssl rand -hex 32)
SESSION_COOKIE_SECURE=true
SESSION_COOKIE_TRUSTED_URL=https://CHANGE-ME.example.com
BATCH_UPDATE_ENABLED=true
BATCH_UPDATE_INTERVAL=5
MEMORY_CACHE_ENABLED=true
ERROR_LOG_ENABLED=true
GOMAXPROCS=16
GOMEMLIMIT=3GiB
SQL_MAX_OPEN_CONNS=200
SQL_MAX_IDLE_CONNS=50
SQL_MAX_LIFETIME=3600
SQL_SLOW_THRESHOLD_MS=200
GLOBAL_API_RATE_LIMIT=600000
GLOBAL_API_RATE_LIMIT_DURATION=180
CRITICAL_RATE_LIMIT=60
CRITICAL_RATE_LIMIT_DURATION=1800
STREAMING_TIMEOUT=3600
SHUTDOWN_TIMEOUT_SECONDS=90
EOT
        chmod 600 "$DATA_DIR/.env"
        echo "  已生成 $DATA_DIR/.env（记得改 SESSION_COOKIE_TRUSTED_URL）"
    fi
fi

# ---------------- 5. 自检 ----------------
log "自检"
echo "  nofile 上限: $(ulimit -n)"
echo "  somaxconn:   $(sysctl -n net.core.somaxconn 2>/dev/null)"
echo "  端口范围:    $(sysctl -n net.ipv4.ip_local_port_range 2>/dev/null)"
echo "  swappiness:  $(sysctl -n vm.swappiness 2>/dev/null)"
echo
echo "下一步："
echo "  1. cp deploy/migrate/docker-compose.yml $APP_DIR/"
echo "  2. cp deploy/migrate/clickhouse-config/{config.d,users.d}/*.xml 到 $DATA_DIR/clickhouse-config/"
echo "  3. 改 $DATA_DIR/.env 里的 SESSION_COOKIE_TRUSTED_URL 为真实域名"
echo "  4. docker compose -f $APP_DIR/docker-compose.yml up -d apiduo-mysql apiduo-clickhouse"
echo "  5. 导入数据（见 README 第二节）"
