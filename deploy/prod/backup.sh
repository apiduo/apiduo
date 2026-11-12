#!/usr/bin/env bash
# apiduo-cn 生产备份（MySQL 主库 + /data 上传文件 + ClickHouse 表结构）。
#
# 为什么是这个顺序 —— 按「丢了能不能恢复业务」排：
#   1. MySQL  apiduo_cn：渠道、令牌、用户、额度、价格。丢了 = 站点回到空库，
#      重建要重新录几十个渠道的密钥，基本等于停服。**唯一不可替代的数据**。
#   2. /data 上传文件：用户历史图片/附件。丢了业务能跑，但历史内容 404。
#   3. ClickHouse 日志：审计与计费明细。丢了不阻断业务，只影响对账与追溯。
#      141 万行/天量级，每天全量导出代价远大于收益 ⇒ 这里只备份表结构
#      （DDL + TTL 配置），真正需要长留的日志走 CH 自身的 TTL 与磁盘冗余。
#
# 一致性要点：
#   - mysqldump --single-transaction 让 InnoDB 在 RR 隔离级别下取一致快照，
#     全程不锁表，生产可直接跑（不要用 --lock-all-tables）。
#   - 管道到 gzip 时 mysqldump 失败也会留下一个「看起来正常」的坏包，
#     所以 pipefail + 结尾 "Dump completed" 标记双重校验，缺一即判失败删包。
#   - 密码走 MYSQL_PWD 环境变量传进容器，不出现在进程 argv 里（ps 可见）。
#
# 诚实边界：备份落在同机 /www/backup，只防「误删/误操作/逻辑损坏」，
# 不防磁盘或主机整体故障。异地副本需要对象存储，见 deploy/prod/README.md。
set -euo pipefail

BACKUP_ROOT="${BACKUP_ROOT:-/www/backup/apiduo-cn}"
KEEP_DAYS="${KEEP_DAYS:-14}"
MYSQL_CT="${MYSQL_CT:-apiduo-cn-mysql}"
CH_CT="${CH_CT:-apiduo-cn-clickhouse}"
DATA_DIR="${DATA_DIR:-/www/data/apiduo-cn/data}"
CH_DB="${CH_DB:-apiduo_cn_logs}"

STAMP="$(date +%Y%m%d-%H%M%S)"
DEST="$BACKUP_ROOT/$STAMP"
umask 077
mkdir -p "$DEST"

log() { printf '%s %s\n' "$(date '+%F %T')" "$*"; }

fail() {
    log "FAILED: $*"
    # 半成品不留在备份目录里，否则下一次 find -mtime 会把它当有效备份数
    rm -rf "$DEST"
    exit 1
}

# ---------- 1. MySQL ----------
MYSQL_PWD_VALUE="$(docker exec "$MYSQL_CT" printenv MYSQL_ROOT_PASSWORD)"
[ -n "$MYSQL_PWD_VALUE" ] || fail "无法从 $MYSQL_CT 读取 MYSQL_ROOT_PASSWORD"

set -o pipefail
if ! docker exec -e MYSQL_PWD="$MYSQL_PWD_VALUE" "$MYSQL_CT" \
        mysqldump -uroot \
        --single-transaction --quick --skip-lock-tables \
        --routines --triggers --events --set-gtid-purged=OFF \
        --default-character-set=utf8mb4 \
        apiduo_cn \
    | gzip -6 > "$DEST/mysql.sql.gz"; then
    fail "mysqldump 失败（管道任一段非 0）"
fi

# 结尾标记是 mysqldump 唯一的「完整导出」信号
if ! zcat "$DEST/mysql.sql.gz" | tail -n 3 | grep -q 'Dump completed'; then
    fail "mysql.sql.gz 缺少 'Dump completed' 结尾，判定为截断包"
fi

# ---------- 2. /data（只保业务文件，不保日志） ----------
# 实测 /data 592M 里 99.9% 是 data/logs/ 的容器 stdout 旧日志（--log-dir 落的盘），
# 与 docker json-file 重复且无恢复价值；log_spool 是可重放缓存。两者都排除，
# 备份从 45M 降到百 KB 级，cron 里跑得起、留得住。
if [ -d "$DATA_DIR" ]; then
    if ! tar -C "$(dirname "$DATA_DIR")" -czf "$DEST/data.tar.gz" \
            --exclude='logs' --exclude='log_spool' \
            "$(basename "$DATA_DIR")" 2>"$DEST/data.tar.err"; then
        fail "打包 $DATA_DIR 失败：$(cat "$DEST/data.tar.err")"
    fi
    rm -f "$DEST/data.tar.err"
fi

# ---------- 3. ClickHouse 表结构 ----------
CH_PWD_VALUE="$(docker exec "$CH_CT" printenv CLICKHOUSE_PASSWORD)"
if [ -n "$CH_PWD_VALUE" ]; then
    docker exec -e CLICKHOUSE_PASSWORD="$CH_PWD_VALUE" "$CH_CT" \
        clickhouse-client --password="$CH_PWD_VALUE" -q "SHOW CREATE TABLE $CH_DB.logs" \
        > "$DEST/clickhouse-schema.sql" 2>/dev/null \
        || log "WARN: 导出 CH 表结构失败（不影响业务恢复）"
fi

# ---------- 4. 保留策略 ----------
find "$BACKUP_ROOT" -mindepth 1 -maxdepth 1 -type d -mtime "+$KEEP_DAYS" -exec rm -rf {} + 2>/dev/null || true

# ---------- 5. 摘要（cron 日志只留一行，便于一眼看是否真跑过） ----------
BYTES="$(du -sb "$DEST" | cut -f1)"
ROWS="$(zcat "$DEST/mysql.sql.gz" | grep -c '^INSERT INTO' || true)"
log "backup ok: ${STAMP} $(numfmt --to=iec "$BYTES" 2>/dev/null || echo "${BYTES}B") mysql_insert_stmts=${ROWS}"
