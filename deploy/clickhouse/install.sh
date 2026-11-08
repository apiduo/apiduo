#!/usr/bin/env bash
# ClickHouse 系统日志表治理：一键安装 / 就地修复
#
# 解决两个真实故障：
#   1) system.*_log 表默认没有 TTL，且 text_log 默认级别是 trace，
#      某实例 10 天累积 22.6 亿行 trace_log（46.8 GiB），业务数据只有 976 KiB；
#   2) 只靠 ALTER 挂 TTL 会在重启时被 config 覆盖，留下 _0 空壳表。
#
# 本脚本做四件事（都可重复执行）：
#   渲染 config.d / users.d 覆盖 → 改成目录挂载（单文件挂载会被 inode 困住）
#   → 重建并等健康 → 把已存在的表 ALTER 对齐 → 装每日巡检 cron
#
# 用法：
#   ./install.sh --container apiduo-cn-clickhouse \
#                --config-root /www/data/apiduo-cn/clickhouse-config \
#                --compose-file /opt/apiduo-cn/docker-compose.apiduo-cn.yml \
#                --service apiduo-cn-clickhouse --patch-compose
#
# 常用参数：
#   --ttl-trace 90        各表 TTL 天数，见 --help
#   --no-cron             不装定时巡检
#   --drop-legacy         删除 _0 遗留表（默认只报告，不动数据）
#   --force               覆盖已存在的 logs.xml / profile.xml
#   --dry-run             只打印将要做的事
#
# 密码来源（优先级）：$CH_PASSWORD → 容器环境变量 CLICKHOUSE_PASSWORD → 空。
# 脚本本身不存任何密码。

set -uo pipefail
cd "$(dirname "$0")"

CONTAINER=apiduo-cn-clickhouse
CONFIG_ROOT=""
COMPOSE_FILE=""
SERVICE=""
PATCH_COMPOSE=0
DO_CRON=1
DROP_LEGACY=0
FORCE=0
DRY_RUN=0

# 默认值来自线上实测：稳态合计约 14 GiB，磁盘 929 GiB 用 11%。
TTL_TRACE=90
TTL_TEXT=180
TTL_QUERY=365
TTL_ERROR=365
TTL_PART=180
TTL_PROFILE=180
TTL_QUERY_METRIC=180
TTL_METRIC=30
TTL_ASYNC_METRIC=90
MAX_MEMORY_USAGE=4294967296   # 4 GiB，按实例内存 / 4 调整

usage() {
    sed -n '2,30p' "$0" | sed 's/^# \{0,1\}//'
    echo
    echo "TTL 参数（天数）：--ttl-trace --ttl-text --ttl-query --ttl-error --ttl-part"
    echo "                 --ttl-profile --ttl-query-metric --ttl-metric --ttl-async-metric"
    echo "其它：--max-memory-usage <bytes>"
    exit "${1:-0}"
}

while [ $# -gt 0 ]; do
    case "$1" in
        --container) CONTAINER=$2; shift 2 ;;
        --config-root) CONFIG_ROOT=$2; shift 2 ;;
        --compose-file) COMPOSE_FILE=$2; shift 2 ;;
        --service) SERVICE=$2; shift 2 ;;
        --patch-compose) PATCH_COMPOSE=1; shift ;;
        --no-cron) DO_CRON=0; shift ;;
        --drop-legacy) DROP_LEGACY=1; shift ;;
        --force) FORCE=1; shift ;;
        --dry-run) DRY_RUN=1; shift ;;
        --ttl-trace) TTL_TRACE=$2; shift 2 ;;
        --ttl-text) TTL_TEXT=$2; shift 2 ;;
        --ttl-query) TTL_QUERY=$2; shift 2 ;;
        --ttl-error) TTL_ERROR=$2; shift 2 ;;
        --ttl-part) TTL_PART=$2; shift 2 ;;
        --ttl-profile) TTL_PROFILE=$2; shift 2 ;;
        --ttl-query-metric) TTL_QUERY_METRIC=$2; shift 2 ;;
        --ttl-metric) TTL_METRIC=$2; shift 2 ;;
        --ttl-async-metric) TTL_ASYNC_METRIC=$2; shift 2 ;;
        --max-memory-usage) MAX_MEMORY_USAGE=$2; shift 2 ;;
        -h|--help) usage 0 ;;
        *) echo "未知参数：$1" >&2; usage 1 ;;
    esac
done

say()  { printf '%s\n' "$*"; }
step() { printf '\n==> %s\n' "$*"; }
die()  { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
run()  { if [ "$DRY_RUN" -eq 1 ]; then printf '  [dry-run] %s\n' "$*"; else eval "$@"; fi; }

# ---------- 前置检查 ----------
command -v docker >/dev/null 2>&1 || die "未找到 docker"
docker inspect "$CONTAINER" >/dev/null 2>&1 || die "容器不存在：$CONTAINER"

[ -n "$CONFIG_ROOT" ] || CONFIG_ROOT=$(docker inspect --format \
    '{{range .Mounts}}{{if eq .Destination "/etc/clickhouse-server/config.d"}}{{.Source}}{{end}}{{end}}' \
    "$CONTAINER" 2>/dev/null)
[ -n "$CONFIG_ROOT" ] || die "无法推断配置目录，请用 --config-root 指定"
# 挂载可能指向单文件，取它的父目录
CONFIG_ROOT=${CONFIG_ROOT%/config.d/*}
CONFIG_ROOT=${CONFIG_ROOT%/config.d}
CONFIG_ROOT=${CONFIG_ROOT%/users.d/*}
CONFIG_ROOT=${CONFIG_ROOT%/users.d}

CH_PASSWORD=${CH_PASSWORD-}
if [ -z "$CH_PASSWORD" ]; then
    CH_PASSWORD=$(docker exec "$CONTAINER" printenv CLICKHOUSE_PASSWORD 2>/dev/null || true)
fi

chq() {
    if [ -n "$CH_PASSWORD" ]; then
        docker exec "$CONTAINER" clickhouse-client --password="$CH_PASSWORD" --query "$1"
    else
        docker exec "$CONTAINER" clickhouse-client --query "$1"
    fi
}

chq "SELECT 1" >/dev/null 2>&1 || die "ClickHouse 连不上（检查容器状态或密码）"

say "容器        : $CONTAINER"
say "配置目录    : $CONFIG_ROOT"
say "TTL(天)     : trace=$TTL_TRACE text=$TTL_TEXT query=$TTL_QUERY error=$TTL_ERROR"
say "              part=$TTL_PART profile=$TTL_PROFILE query_metric=$TTL_QUERY_METRIC"
say "              metric=$TTL_METRIC async_metric=$TTL_ASYNC_METRIC"

# ---------- 1. 目录与默认配置 ----------
step "准备宿主机配置目录"
run "mkdir -p '$CONFIG_ROOT/config.d' '$CONFIG_ROOT/users.d' '$CONFIG_ROOT/backup'"

# 目录挂载会盖掉镜像里的 config.d，必须先把原有文件搬出来，
# 否则 listen_host 丢失 → ClickHouse 只监听容器内 127.0.0.1，应用连不上。
if [ ! -f "$CONFIG_ROOT/config.d/docker_related_config.xml" ]; then
    step "复制镜像自带的 docker_related_config.xml（含 listen_host）"
    run "docker cp '$CONTAINER:/etc/clickhouse-server/config.d/docker_related_config.xml' '$CONFIG_ROOT/config.d/docker_related_config.xml'"
fi

render() {
    local src=$1 dst=$2
    if [ -f "$dst" ] && [ "$FORCE" -ne 1 ]; then
        say "  已存在，跳过（--force 可覆盖）：$dst"
        return
    fi
    [ -f "$dst" ] && run "cp '$dst' '$CONFIG_ROOT/backup/$(basename "$dst").bak-$(date +%Y%m%d%H%M%S)'"
    run "sed -e 's/__TTL_TRACE__/$TTL_TRACE/' \
             -e 's/__TTL_TEXT__/$TTL_TEXT/' \
             -e 's/__TTL_QUERY_METRIC__/$TTL_QUERY_METRIC/' \
             -e 's/__TTL_QUERY__/$TTL_QUERY/' \
             -e 's/__TTL_ERROR__/$TTL_ERROR/' \
             -e 's/__TTL_PART__/$TTL_PART/' \
             -e 's/__TTL_PROFILE__/$TTL_PROFILE/' \
             -e 's/__TTL_METRIC__/$TTL_METRIC/' \
             -e 's/__TTL_ASYNC_METRIC__/$TTL_ASYNC_METRIC/' \
             -e 's/__MAX_MEMORY_USAGE__/$MAX_MEMORY_USAGE/' \
             '$src' > '$dst'"
    say "  渲染：$dst"
}

# ClickHouse 按文件名字典序合并 config.d / users.d，同名元素后者覆盖前者。
# 两个文件同时定义同一张表的 TTL，会变成「改了却没生效」的静默故障 ——
# 这类问题在故障现场极难定位，所以这里直接拒绝，让人先做选择。
step "检查是否已有其它覆盖文件管理同一批配置"
conflicts=""
for f in "$CONFIG_ROOT/config.d"/*.xml; do
    [ -f "$f" ] || continue
    [ "$(basename "$f")" = "logs.xml" ] && continue
    grep -qE '<[a-z_]*_log>|<ttl>' "$f" 2>/dev/null && conflicts="$conflicts $f"
done
for f in "$CONFIG_ROOT/users.d"/*.xml; do
    [ -f "$f" ] || continue
    [ "$(basename "$f")" = "profile.xml" ] && continue
    grep -q '<profiles>' "$f" 2>/dev/null && conflicts="$conflicts $f"
done

if [ -n "$conflicts" ] && [ ! -f "$CONFIG_ROOT/config.d/logs.xml" ]; then
    say "  这些文件已经在管理同样的配置："
    for f in $conflicts; do say "    - $f"; done
    cat <<EOF

  同一批表被两个文件定义时，ClickHouse 只认文件名排序靠后的那个，
  另一个静默失效。请二选一：

    a) 沿用现有文件（这台机器已经治理过）：不要安装 logs.xml，
       直接用 check-tables.sh 校验；若数值要改，改现有文件再 ALTER 对齐。
    b) 换成这套模板：先删掉上面文件里的对应块（保留 max_server_memory_usage
       等实例级调优），再重跑本脚本。
EOF
    exit 4
fi

step "渲染 logs.xml / profile.xml"
render logs.xml "$CONFIG_ROOT/config.d/logs.xml"
render profile.xml "$CONFIG_ROOT/users.d/profile.xml"

# ---------- 2. 权限 ----------
# ClickHouse 进程以 clickhouse 用户运行：目录必须 755（700 会 Permission denied
# 直接起不来）；users.d 里的默认用户文件保持 644（镜像原本就是）。
step "修正权限"
run "chmod 755 '$CONFIG_ROOT' '$CONFIG_ROOT/config.d' '$CONFIG_ROOT/users.d'"
run "chmod 644 '$CONFIG_ROOT/config.d/'*.xml '$CONFIG_ROOT/users.d/'*.xml"
run "[ -f '$CONFIG_ROOT/users.d/default-user.xml' ] && chmod 644 '$CONFIG_ROOT/users.d/default-user.xml' || true"

# XML 必须能被解析，否则 ClickHouse 起不来。占位符没替换也算失败。
if command -v python3 >/dev/null 2>&1 && [ "$DRY_RUN" -eq 0 ]; then
    for f in "$CONFIG_ROOT/config.d/logs.xml" "$CONFIG_ROOT/users.d/profile.xml"; do
        python3 -c "import sys,xml.etree.ElementTree as ET; ET.parse(sys.argv[1])" "$f" \
            || die "XML 非法：$f"
        grep -q '__[A-Z_]*__' "$f" && die "占位符未替换：$f"
    done
    say "  XML 校验通过"
fi

# ---------- 3. 挂载形态 ----------
step "检查挂载形态（必须是目录挂载）"
mount_ok() {
    docker inspect --format '{{range .Mounts}}{{.Type}}|{{.Source}}|{{.Destination}}{{"\n"}}{{end}}' "$CONTAINER" 2>/dev/null \
        | grep -qx "bind|$CONFIG_ROOT/config.d|/etc/clickhouse-server/config.d"
}
if mount_ok; then
    say "  已是目录挂载，无需改动"
else
    say "  当前不是目录挂载。原因：单文件 bind mount 绑定 inode，"
    say "  宿主机用 sed -i / cp / git checkout 换文件后，容器里仍是旧内容且不报错。"
    if [ "$PATCH_COMPOSE" -ne 1 ]; then
        cat <<EOF

请手工在 compose 的 volumes 里改成：

      - $CONFIG_ROOT/users.d:/etc/clickhouse-server/users.d
      - $CONFIG_ROOT/config.d:/etc/clickhouse-server/config.d:ro

注意 users.d 不能加 :ro —— 镜像 entrypoint 要写 default-user.xml，
只读会让容器无限重启。改完执行：

  docker compose -f <file> up -d --force-recreate $CONTAINER
  $0 --container $CONTAINER --config-root $CONFIG_ROOT

或者交给脚本：加 --compose-file <file> --service <服务名> --patch-compose
EOF
        exit 3
    fi

    [ -n "$COMPOSE_FILE" ] || die "--patch-compose 需要 --compose-file"
    [ -n "$SERVICE" ] || die "--patch-compose 需要 --service（compose 里的服务名，不是容器名）"
    [ -f "$COMPOSE_FILE" ] || die "compose 文件不存在：$COMPOSE_FILE"

    step "改写 compose 挂载"
    run "cp '$COMPOSE_FILE' '$COMPOSE_FILE.bak-$(date +%Y%m%d%H%M%S)'"
    if [ "$DRY_RUN" -eq 0 ]; then
        python3 - "$COMPOSE_FILE" "$SERVICE" "$CONFIG_ROOT" <<'PY' || die "compose 改写失败（未改动文件）"
import io, re, sys

path, service, cfg_root = sys.argv[1], sys.argv[2], sys.argv[3]
text = io.open(path, encoding='utf-8').read()
lines = text.split('\n')

start = None
for i, line in enumerate(lines):
    if re.match(r'^\s{2}' + re.escape(service) + r':\s*$', line):
        start = i
        break
if start is None:
    sys.exit('找不到服务 %s（缩进需为 2 空格）' % service)

end = len(lines)
for i in range(start + 1, len(lines)):
    if re.match(r'^\s{2}[A-Za-z0-9_.-]+:\s*$', lines[i]):
        end = i
        break

block = lines[start:end]
vi = None
for i, line in enumerate(block):
    if re.match(r'^\s+volumes:\s*$', line):
        vi = i
        break
if vi is None:
    sys.exit('服务 %s 没有 block 形式的 volumes:' % service)

item_indent = None
last_item = vi
j = vi + 1
while j < len(block):
    line = block[j]
    if not line.strip() or re.match(r'^\s*#', line):
        j += 1
        continue
    m = re.match(r'^(\s+)-\s+\S', line)
    if m:
        item_indent = m.group(1)
        last_item = j
        j += 1
        continue
    break

item_indent = item_indent or '      '
items = []
for k in range(vi + 1, last_item + 1):
    line = block[k]
    if re.search(r'/clickhouse-config/(config\.d|users\.d)', line) or \
       re.search(r':/etc/clickhouse-server/(config\.d|users\.d)', line):
        continue          # 删掉旧的单文件 / 旧目录挂载
    items.append(line)

items.append('%s- %s/users.d:/etc/clickhouse-server/users.d' % (item_indent, cfg_root))
items.append('%s- %s/config.d:/etc/clickhouse-server/config.d:ro' % (item_indent, cfg_root))
block[vi + 1:last_item + 1] = items
lines[start:end] = block
io.open(path, 'w', encoding='utf-8').write('\n'.join(lines))
print('  compose 已更新')
PY
    fi

    step "重建容器让挂载生效"
    run "docker compose -f '$COMPOSE_FILE' up -d --force-recreate '$SERVICE'"
    if [ "$DRY_RUN" -eq 0 ]; then
        for _ in $(seq 1 60); do
            st=$(docker inspect --format '{{.State.Health.Status}}' "$CONTAINER" 2>/dev/null)
            [ "$st" = "healthy" ] && { say "  容器 healthy"; break; }
            sleep 2
        done
        [ "${st:-}" = "healthy" ] || die "容器未在 120s 内 healthy，检查 docker logs $CONTAINER"
        mount_ok || die "重建后挂载仍不是目录形态"
    fi
fi

# ---------- 4. 重载配置 ----------
# 注意：total_memory_profiler_step 只在启动时读入，RELOAD 对它无效。
# 若刚重建过，它已生效；否则要提示需要重启。
step "重载配置"
run "chq 'SYSTEM RELOAD CONFIG' >/dev/null"
run "chq 'SYSTEM RELOAD USERS' >/dev/null"

# ---------- 5. 已有表对齐 ----------
# config 只管「新建的表」。已存在的表必须 ALTER，否则重启时
# ClickHouse 认为定义不一致，会 rename 成 _0 再按 config 重建。
step "把已存在的表 ALTER 对齐到 config"
pairs="trace_log:$TTL_TRACE
text_log:$TTL_TEXT
query_log:$TTL_QUERY
error_log:$TTL_ERROR
part_log:$TTL_PART
processors_profile_log:$TTL_PROFILE
query_metric_log:$TTL_QUERY_METRIC
metric_log:$TTL_METRIC
asynchronous_metric_log:$TTL_ASYNC_METRIC"

while IFS=: read -r tbl days; do
    [ -z "$tbl" ] && continue
    cur=$(chq "SELECT if(engine_full LIKE '%TTL%', extract(engine_full, 'toIntervalDay\\\\(([0-9]+)\\\\)'), '0') FROM system.tables WHERE database='system' AND name='$tbl'" 2>/dev/null)
    cur=${cur:-0}
    if [ "$cur" = "$days" ]; then
        say "  $tbl 已是 ${days}d"
        continue
    fi
    if [ "$DRY_RUN" -eq 1 ]; then
        say "  [dry-run] $tbl: ${cur}d -> ${days}d"
        continue
    fi
    # 表大时 mutation 会吃内存：先看规模，超过 5000 万行提示先 TRUNCATE。
    rows=$(chq "SELECT sum(rows) FROM system.parts WHERE active AND database='system' AND table='$tbl'" 2>/dev/null)
    rows=${rows:-0}
    if [ "$rows" -gt 50000000 ] 2>/dev/null; then
        say "  WARN $tbl 有 $rows 行，MODIFY TTL 会触发全表 mutation，可能 OOM"
        say "       建议先 TRUNCATE（历史数据无业务价值）再跑本脚本"
    fi
    if chq "ALTER TABLE system.$tbl MODIFY TTL event_date + INTERVAL $days DAY" >/dev/null 2>&1; then
        say "  $tbl: ${cur}d -> ${days}d"
    else
        say "  WARN $tbl ALTER 失败（可能内存不足），巡检会继续报"
    fi
done <<< "$pairs"

# ---------- 6. 遗留表 ----------
legacy=$(chq "SELECT name FROM system.tables WHERE database='system' AND name LIKE '%\\_0' ORDER BY name FORMAT TSV" 2>/dev/null)
if [ -n "$legacy" ]; then
    step "发现 _0 遗留表（config 与实际定义不一致的物证）"
    echo "$legacy" | sed 's/^/  /'
    if [ "$DROP_LEGACY" -eq 1 ]; then
        for t in $legacy; do
            run "chq 'DROP TABLE IF EXISTS system.$t' >/dev/null"
            say "  已删除：$t"
        done
    else
        say "  确认无用后加 --drop-legacy 删除"
    fi
fi

# ---------- 7. 巡检 cron ----------
if [ "$DO_CRON" -eq 1 ]; then
    step "安装每日巡检（root crontab，09:00）"
    cron_line="0 9 * * * CONFIG_DIR=$CONFIG_ROOT/config.d LOG=/var/log/clickhouse-tables.log $(pwd)/check-tables.sh $CONTAINER >/dev/null 2>&1"
    if crontab -l 2>/dev/null | grep -qF "check-tables.sh $CONTAINER"; then
        say "  已存在，跳过"
    elif [ "$DRY_RUN" -eq 1 ]; then
        say "  [dry-run] $cron_line"
    else
        crontab -l 2>/dev/null > /tmp/ct.$$ || true
        printf '%s\n' "$cron_line" >> /tmp/ct.$$
        crontab /tmp/ct.$$ && rm -f /tmp/ct.$$ && say "  已写入 crontab"
    fi
fi

# ---------- 8. 自检 ----------
if [ "$DRY_RUN" -eq 0 ]; then
    step "自检"
    CONFIG_DIR=$CONFIG_ROOT/config.d CH_PASSWORD=$CH_PASSWORD ./check-tables.sh "$CONTAINER"
    say "自检退出码：$?"
fi

say ""
say "完成。关键提醒："
say "  · total_memory_profiler_step 只在启动时读入 —— 若本次没有重建容器，"
say "    trace_log 仍会增长，重启后才归零（TTL 是兜底）。"
say "  · 改 config 后必须再跑一次本脚本，把已存在的表 ALTER 对齐。"
