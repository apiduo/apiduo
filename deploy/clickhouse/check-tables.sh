#!/usr/bin/env bash
# ClickHouse 系统日志表结构巡检
#
# 判据（关键设计）：查「有没有 TTL」「TTL 和 config 是否一致」，
# 而不是查「现在多大」。
#
#   大小是结果，TTL 是原因。一张 1 MB 但没有 TTL 的表和一张 60 GB 但没有
#   TTL 的表，性质完全一样，都是定时炸弹。只看大小会漏掉前者；
#   只看当下，会在空站和刚重建的实例上得到「一切正常」的假象。
#
# 为什么还要比对 config：ALTER TABLE ... MODIFY TTL 只改表元数据，
# 进程重启时 ClickHouse 按 config.d 重建表，ALTER 上去的 TTL 就丢了
# （表现为多出 xxx_0 表，且新表没有 TTL）。config 必须是唯一真相来源。
#
# 用法：
#   ./check-tables.sh [容器名]
#
# 环境变量：
#   CH_PASSWORD          ClickHouse 密码。留空则不带密码连接
#                        （本机 default 用户无密码的场景）。
#   CONFIG_DIR           存放 logs.xml 的宿主机目录。留空则从容器挂载自动推断。
#   LOG                  巡检日志路径。留空则只打印到 stdout。
#   SYSTEM_ALERT_BYTES   system 库告警阈值，默认 40 GiB（失控探测器，不是容量红线）。
#   APIDUO_ALERT_WEBHOOK 设置后 ALERT 会 POST 一条 JSON 到该地址。
#
# 退出码：0=OK  1=ALERT（缺 TTL / 与 config 不一致 / 超阈值 / 有遗留表）  2=ERROR（连不上）

set -uo pipefail

CH_CONTAINER=${1:-${CH_CONTAINER:-apiduo-cn-clickhouse}}
CH_PASSWORD=${CH_PASSWORD-}
CONFIG_DIR=${CONFIG_DIR-}
LOG=${LOG-}
SYSTEM_ALERT_BYTES=${SYSTEM_ALERT_BYTES:-42949672960}

# 不用 docker exec -i：-i 会接管 stdin，脚本若从 stdin 读入（bash -s）
# 会把后面的行一起吞掉，表现为「命令没输出、exit 0」。
q() {
    if [ -n "${CH_PASSWORD}" ]; then
        docker exec "$CH_CONTAINER" clickhouse-client --password="$CH_PASSWORD" --query "$1" 2>/dev/null
    else
        docker exec "$CH_CONTAINER" clickhouse-client --query "$1" 2>/dev/null
    fi
}

emit() {
    if [ -n "$LOG" ]; then
        mkdir -p "$(dirname "$LOG")" 2>/dev/null
        # 单文件超过 5000 行就截断到最近 2000 行，避免日志自己变成问题
        if [ -f "$LOG" ] && [ "$(wc -l < "$LOG" 2>/dev/null || echo 0)" -gt 5000 ]; then
            tail -n 2000 "$LOG" > "$LOG.tmp" && mv "$LOG.tmp" "$LOG"
        fi
        echo "$1" | tee -a "$LOG"
    else
        echo "$1"
    fi
}

ts=$(date '+%Y-%m-%d %H:%M:%S')

if ! q "SELECT 1" >/dev/null 2>&1; then
    emit "$ts ERROR 无法连接 $CH_CONTAINER（检查容器名 / CH_PASSWORD）"
    exit 2
fi

rc=0
problems=""

# 1) system 库里没有 TTL 的日志表 —— 根因检查，与流量无关
missing_ttl=$(q "SELECT name FROM system.tables WHERE database='system' AND name LIKE '%_log' AND engine_full NOT LIKE '%TTL%' ORDER BY name FORMAT TSV")
if [ -n "$missing_ttl" ]; then
    problems="${problems} 缺TTL=[$(echo "$missing_ttl" | tr '\n' ' ')]"
    rc=1
fi

# 2) 表 TTL 与 config 定义逐表比对
if [ -z "$CONFIG_DIR" ]; then
    # 从容器的 config.d 目录挂载反查宿主机路径
    CONFIG_DIR=$(docker inspect --format '{{range .Mounts}}{{if eq .Destination "/etc/clickhouse-server/config.d"}}{{.Source}}{{end}}{{end}}' "$CH_CONTAINER" 2>/dev/null)
fi

cfg_pairs=""
if [ -n "$CONFIG_DIR" ] && [ -d "$CONFIG_DIR" ]; then
    # 扫描目录下所有 XML，而不是写死 logs.xml：
    # 不同机器的文件名可能不同（logs.xml / zz-logs.xml / 合并进别的覆盖文件）。
    # 只认 <xxx_log> 紧跟的 <ttl>，容忍行首空格数量变化。
    for f in "$CONFIG_DIR"/*.xml; do
        [ -r "$f" ] || continue
        parsed=$(awk -v file="$(basename "$f")" '
            /<[a-z_]+>/ {
                s = $0; sub(/^.*</, "", s); sub(/>.*$/, "", s)
                if (s ~ /_log$/) cur = s
            }
            /<ttl>/ {
                n = $0; sub(/^.*INTERVAL /, "", n); sub(/ DAY.*$/, "", n)
                if (cur != "" && n ~ /^[0-9]+$/) { print cur "\t" n "\t" file; cur = "" }
            }
        ' "$f")
        [ -n "$parsed" ] && cfg_pairs="${cfg_pairs}${parsed}"$'\n'
    done
fi

# 同一张表在多个文件里被定义 → ClickHouse 按文件名字典序覆盖，先定义的静默失效。
# 这种覆盖最难查，单独报出来。
cfg_conflicts=$(printf '%s' "$cfg_pairs" | awk -F'\t' '
    NF >= 3 && $1 != "" {
        if (v[$1] != "" && v[$1] != $2) c = c " " $1 "(config " v[$1] "d/" $2 "d)"
        v[$1] = $2
    }
    END { printf "%s", c }
')
if [ -n "$cfg_conflicts" ]; then
    problems="${problems} 重复定义:${cfg_conflicts}"
    rc=1
fi

# 去重：同名表取最后出现的值（与 ClickHouse 的覆盖方向一致）
cfg_pairs=$(printf '%s' "$cfg_pairs" | awk -F'\t' 'NF >= 2 && $1 != "" { v[$1] = $2 } END { for (k in v) print k "\t" v[k] }')

if [ -n "$cfg_pairs" ]; then

    db_pairs=$(q "SELECT name, if(engine_full LIKE '%TTL%', extract(engine_full, 'toIntervalDay\\\\(([0-9]+)\\\\)'), '0') FROM system.tables WHERE database='system' AND name LIKE '%\\\\_log' AND name NOT LIKE '%\\\\_0' FORMAT TSV")

    mismatch=""
    while IFS=$'\t' read -r t d; do
        [ -z "$t" ] && continue
        actual=$(echo "$db_pairs" | awk -F'\t' -v k="$t" '$1==k{print $2}')
        if [ "$actual" != "$d" ]; then
            mismatch="${mismatch} ${t}(config=${d}d,表=${actual:-无}d)"
        fi
    done <<< "$cfg_pairs"

    if [ -n "$mismatch" ]; then
        problems="${problems} TTL与config不一致:${mismatch}"
        rc=1
    fi
else
    problems="${problems} ${CONFIG_DIR:-config.d} 下没有带 <ttl> 定义的 XML，无法校验一致性"
    rc=1
fi

# 3) system 库总大小（失控探测器）
sys_bytes=$(q "SELECT sum(bytes_on_disk) FROM system.parts WHERE active AND database='system'")
sys_bytes=${sys_bytes:-0}
if [ "$sys_bytes" -gt "$SYSTEM_ALERT_BYTES" ] 2>/dev/null; then
    problems="${problems} system库=$(numfmt --to=iec "$sys_bytes" 2>/dev/null || echo "${sys_bytes}B") 超阈值"
    rc=1
fi

# 4) 升级 / 重建遗留的 _0 表（不再写入，纯占空间，也是 config 不一致的物证）
legacy=$(q "SELECT name FROM system.tables WHERE database='system' AND name LIKE '%\\\\_0' ORDER BY name FORMAT TSV")
if [ -n "$legacy" ]; then
    problems="${problems} 遗留表=[$(echo "$legacy" | tr '\n' ' ')]"
    rc=1
fi

if [ "$rc" -eq 0 ]; then
    emit "$ts OK container=$CH_CONTAINER system=$(numfmt --to=iec "$sys_bytes" 2>/dev/null || echo "${sys_bytes}B") TTL齐全且与config一致"
else
    msg="ALERT container=$CH_CONTAINER$problems"
    emit "$ts $msg"
    # 与其它巡检脚本共用同一个告警出口
    if [ -n "${APIDUO_ALERT_WEBHOOK-}" ]; then
        curl -sS -m 10 -X POST "$APIDUO_ALERT_WEBHOOK" \
            -H 'Content-Type: application/json' \
            -d "{\"text\":\"[apiduo] $msg\"}" >/dev/null 2>&1 \
            || emit "$ts WARN webhook 推送失败"
    fi
fi

exit "$rc"
