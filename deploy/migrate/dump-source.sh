#!/usr/bin/env bash
# 从运行中的 new-api 实例导出一份可迁移的 dump（在源机上执行）。
#
# 设计要点：
#   - --single-transaction 让 InnoDB 取一致快照，全程不锁表，源站照常服务
#   - 排除 e2e 临时表：生产库里有 e2e 跑出来的垃圾表（e2e_tmp_tok），
#     迁过去只会让新环境多一张无主表
#   - 结尾校验 "Dump completed"：管道到 gzip 时 mysqldump 失败会留下一个
#     大小正常、内容截断的坏包，这是最典型的"备份看着有其实没用"
#   - 不导出 mysql 系统库：账号权限在新环境重新建
set -euo pipefail

CT="${MYSQL_CT:-newapi-0to1-mysql}"
DB="${DB:-newapi_0to1}"
OUT_DIR="${OUT_DIR:-/tmp/mig}"
IGNORE_TABLES="${IGNORE_TABLES:-e2e_tmp_tok}"

STAMP="$(date +%Y%m%d-%H%M%S)"
OUT="$OUT_DIR/${DB}-${STAMP}.sql.gz"
umask 077
mkdir -p "$OUT_DIR"

PASS="$(docker exec "$CT" printenv MYSQL_ROOT_PASSWORD)"
[ -n "$PASS" ] || { echo "无法从 $CT 读取 MYSQL_ROOT_PASSWORD" >&2; exit 1; }

IGNORE_ARGS=()
for t in ${IGNORE_TABLES//,/ }; do
    IGNORE_ARGS+=(--ignore-table="$DB.$t")
done

set -o pipefail
docker exec -e MYSQL_PWD="$PASS" "$CT" mysqldump -uroot \
    --single-transaction --quick --skip-lock-tables \
    --routines --triggers --events --set-gtid-purged=OFF \
    --default-character-set=utf8mb4 \
    "${IGNORE_ARGS[@]}" \
    "$DB" | gzip -6 > "$OUT" \
    || { echo "mysqldump 失败" >&2; rm -f "$OUT"; exit 1; }

zcat "$OUT" | tail -n 3 | grep -q 'Dump completed' \
    || { echo "dump 缺少结尾标记，判定截断" >&2; rm -f "$OUT"; exit 1; }

# 行数快照，供新环境导入后逐项比对
COUNTS="$(docker exec -e MYSQL_PWD="$PASS" "$CT" mysql -uroot -N -B -e "
SELECT CONCAT('channels=', COUNT(*)) FROM $DB.channels;
SELECT CONCAT('tokens=',   COUNT(*)) FROM $DB.tokens;
SELECT CONCAT('users=',    COUNT(*)) FROM $DB.users;
SELECT CONCAT('options=',  COUNT(*)) FROM $DB.options;")"

echo "$COUNTS" > "${OUT%.sql.gz}.counts"
echo "$(date '+%F %T') dump ok: $OUT $(du -h "$OUT" | cut -f1)"
echo "$COUNTS" | sed 's/^/  /'
echo "行数快照: ${OUT%.sql.gz}.counts"
