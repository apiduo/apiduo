#!/usr/bin/env bash
# apiduo.cn 上线前自检 —— 在 ai-prod 上执行，只读不写。
#
# 每条判据都对应一次真实踩过的坑，不是泛泛的「检查一下服务」。
# 用法：bash preflight.sh        （期望输出全 OK；WARN 需人工判断，FAIL 必须处理）
set -uo pipefail

APP=apiduo-cn
MYSQL=apiduo-cn-mysql
CH=apiduo-cn-clickhouse
PORT=39047
BACKUP_ROOT=/www/backup/apiduo-cn
ok=0; warn=0; fail=0
pass() { printf '  \033[32mOK\033[0m   %s\n' "$*"; ok=$((ok+1)); }
note() { printf '  \033[33mWARN\033[0m %s\n' "$*"; warn=$((warn+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*"; fail=$((fail+1)); }

echo "== 1. 容器 =="
for c in $APP $MYSQL $CH; do
    st="$(docker inspect -f '{{.State.Status}}|{{if .State.Health}}{{.State.Health.Status}}{{end}}' "$c" 2>/dev/null)"
    [ -z "$st" ] && { bad "$c 不存在"; continue; }
    case "$st" in
        running\|healthy) pass "$c running/healthy" ;;
        running\|)        note "$c 运行中但无 healthcheck" ;;
        *)                bad "$c 状态=$st" ;;
    esac
done

echo "== 2. 应用可用性与版本 =="
body="$(curl -s --noproxy '*' --max-time 5 "http://127.0.0.1:$PORT/api/status" || true)"
if printf '%s' "$body" | grep -q '"success":true'; then
    pass "/api/status success=true"
else
    bad "/api/status 未返回 success=true（进程活着≠DB 就绪）"
fi
ver="$(printf '%s' "$body" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("data",{}).get("version",""))' 2>/dev/null)"
[ -n "$ver" ] && pass "运行版本 $ver" || note "未能解析版本号"
echo "       容器镜像: $(docker inspect -f '{{.Config.Image}}' $APP 2>/dev/null)"
echo "       容器启动: $(docker inspect -f '{{.State.StartedAt}}' $APP 2>/dev/null)"

echo "== 3. 备份新鲜度（每日 04:20）=="
latest="$(ls -d "$BACKUP_ROOT"/*/ 2>/dev/null | sort | tail -1)"
if [ -z "$latest" ]; then
    bad "从未备份过"
else
    age_h=$(( ( $(date +%s) - $(stat -c %Y "$latest") ) / 3600 ))
    if [ "$age_h" -le 26 ]; then pass "最近备份 ${age_h}h 前: $latest"
    else bad "最近备份 ${age_h}h 前，超过 26h —— 检查 cron 与 /var/log/apiduo-cn-backup.log"; fi
    [ -s "$latest/mysql.sql.gz" ] || bad "mysql.sql.gz 缺失或为空"
fi

echo "== 4. 磁盘 =="
used="$(df -P /www | awk 'NR==2{gsub("%","",$5); print $5}')"
if [ "$used" -lt 80 ]; then pass "/www 使用 ${used}%"
else bad "/www 使用 ${used}%（≥80% 会拖慢 MySQL/CH 合并）"; fi

echo "== 5. nginx =="
if nginx -t >/dev/null 2>&1; then pass "nginx -t 通过"; else bad "nginx -t 失败"; fi
cert=/www/server/panel/vhost/cert/apiduo.cn/fullchain.pem
if [ -f "$cert" ]; then
    days=$(( ( $(date -d "$(openssl x509 -in "$cert" -noout -enddate | cut -d= -f2)" +%s) - $(date +%s) ) / 86400 ))
    if [ "$days" -gt 21 ]; then pass "证书剩余 ${days} 天"
    else bad "证书剩余 ${days} 天 —— acme 续期可能已失效"; fi
else
    note "未找到证书 $cert"
fi

echo "== 6. 限流参数（人工判断，不是对错）=="
zone="$(grep -ho 'rate=[0-9]*r/m' /opt/apiduo-cn/nginx/*.conf 2>/dev/null | head -1)"
burst="$(grep -ho 'burst=[0-9]*' /opt/apiduo-cn/nginx/*.conf 2>/dev/null | head -1)"
echo "       nginx: ${zone:-未设置} ${burst:-未设置}"
echo "       应用:  GLOBAL=$(grep -h '^GLOBAL_API_RATE_LIMIT' /www/data/apiduo-cn/.env 2>/dev/null | cut -d= -f2) / CRITICAL=$(grep -h '^CRITICAL_RATE_LIMIT' /www/data/apiduo-cn/.env 2>/dev/null | cut -d= -f2)"

echo "== 7. 点击日志链路 =="
chp="$(docker exec $CH printenv CLICKHOUSE_PASSWORD 2>/dev/null)"
recent="$(docker exec -e P="$chp" $CH clickhouse-client --password="$chp" -q \
    "SELECT count() FROM apiduo_cn_logs.logs WHERE created_at > toUnixTimestamp(now()) - 3600" 2>/dev/null)"
if [ -n "$recent" ] && [ "$recent" -gt 0 ]; then pass "近 1 小时写入 ${recent} 条日志"
elif [ -n "$recent" ]; then note "近 1 小时 0 条日志（低峰正常，但先确认 spool 无积压）"
else bad "无法查询 apiduo_cn_logs.logs"; fi

echo "== 8. 配置漂移提醒 =="
if docker ps --format '{{.Names}}' | grep -qx "${APP}-mock"; then
    note "${APP}-mock 仍在运行（手工 docker run，不在 compose 里）"
    echo "       ⚠️ compose down 时会报 network has active endpoints，先 stop 它"
fi
extra="$(docker ps --filter "label=com.docker.compose.project=apiduo-cn" --format '{{.Names}}' | wc -l)"
echo "       compose 项目内容器数: $extra（期望 3）"

echo
printf '结果: OK=%d WARN=%d FAIL=%d\n' "$ok" "$warn" "$fail"
[ "$fail" -eq 0 ] || exit 1
