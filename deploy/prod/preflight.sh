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

# --proxy-peer：采出「容器视角下，连到 3000 的对端地址」。
# 这是 TRUSTED_PROXIES 该不该钉、钉成什么的唯一可靠依据：iptables DNAT 下
# 对端是 127.0.0.1，docker-proxy 用户态转发下是网桥网关。钉错的那一个会让
# ClientIP 恒定成网关 —— 日志和令牌 allow_ips 一起失效，比不钉更糟。
if [ "${1:-}" = "--proxy-peer" ]; then
    pid="$(docker inspect -f '{{.State.Pid}}' "$APP" 2>/dev/null)"
    if [ -z "$pid" ] || [ "$pid" = "0" ]; then
        echo "容器 $APP 未运行，无法采样" >&2
        exit 1
    fi
    echo "采样 10s（每 0.2s 读一次 /proc/$pid/net/tcp{,,6}），只读……"
    python3 - "$pid" <<'PY'
import collections, ipaddress, sys, time

pid = sys.argv[1]
peers = collections.Counter()
loopback = False


def decode(value):
    raw, port = value.split(":")
    port = int(port, 16)
    if len(raw) == 8:
        ip = ipaddress.IPv4Address(bytes.fromhex(raw)[::-1])
    else:
        words = (raw[i : i + 8] for i in range(0, 32, 8))
        ip = ipaddress.IPv6Address(b"".join(bytes.fromhex(w)[::-1] for w in words))
    shown = f"[{ip}]:{port}" if ip.version == 6 else f"{ip}:{port}"
    return shown, ip.is_loopback


deadline = time.time() + 10
while time.time() < deadline:
    for table in ("tcp", "tcp6"):
        try:
            with open(f"/proc/{pid}/net/{table}") as handle:
                rows = handle.read().splitlines()[1:]
        except OSError:
            continue
        for row in rows:
            fields = row.split()
            if len(fields) < 4 or fields[3] != "01":
                continue
            if int(fields[1].split(":")[1], 16) != 3000:
                continue
            peer, is_loop = decode(fields[2])
            peers[peer] += 1
            loopback = loopback or is_loop
    time.sleep(0.2)

if not peers:
    print("没采到活动连接：站点空闲，或 nginx 到上游的连接已经关掉。")
    print("在有流量的时段重跑一次。")
    sys.exit(0)

for peer, count in peers.most_common():
    print(f"  {peer:<44} {count} 次")
print()
if loopback:
    print("对端是回环：可以钉 TRUSTED_PROXIES=127.0.0.1/32，比默认更窄且不影响 ClientIP。")
    print("写进 /www/data/apiduo-cn/.env 后需要 --force-recreate 才生效。")
else:
    print("对端不是回环，是上面那个网关地址。此时钉 127.0.0.1/32 会让 ClientIP")
    print("恒定成网关，日志与 allow_ips 一起失效。保持未设置即可 ——")
    print("真正挡住 X-Forwarded-For 伪造的是端口只绑回环（第 9 节会查）。")
PY
    exit 0
fi

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

echo "== 9. 安全基线（IP 追溯链路）=="

# 9.1 端口只绑回环。绑到 0.0.0.0 时，同网段任何主机都能直连并伪造
#     X-Forwarded-For，日志里的 IP 与 PAT 的 allow_ips 同时失效。
binds="$(docker port $APP 2>/dev/null | awk -F' -> ' '{print $2}' | paste -sd, -)"
if [ -z "$binds" ]; then
    note "读不到端口映射（容器未运行？）"
else
    nonloop="$(printf '%s\n' "$binds" | tr ',' '\n' | grep -vE '^(127\.0\.0\.1|\[::1\]):' || true)"
    if [ -n "$nonloop" ]; then
        bad "端口映射含非回环地址 $nonloop —— 必须 127.0.0.1:$PORT:3000"
    else
        pass "端口只绑回环（$binds）"
    fi
fi

# 9.2 nginx 必须把真实客户端地址传进来。少了这一行，所有请求的 ClientIP
#     都会是代理地址，日志里查不到人。
ngdir=/www/server/panel/vhost/nginx
[ -d "$ngdir" ] || ngdir=/opt/apiduo-cn/nginx
if grep -qs 'X-Forwarded-For' "$ngdir"/apiduo* 2>/dev/null; then
    pass "nginx 转发 X-Forwarded-For"
else
    bad "nginx 未转发 X-Forwarded-For（$ngdir）—— 所有请求都会记成代理地址"
fi

envs="$(docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' $APP 2>/dev/null)"

# 9.3 TRUSTED_PROXIES 不能写成全放行；未设置时默认只信回环 + 私网。
#     注意私网信任是有代价的：容器接在多个网络时，同网络的其他容器能绕过
#     nginx 直连 3000 并伪造 X-Forwarded-For（绕过 allow_ips 与按 IP 限流）。
tp="$(printf '%s\n' "$envs" | sed -n 's/^TRUSTED_PROXIES=//p' | head -1)"
nets="$(docker inspect -f '{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}' "$APP" 2>/dev/null | wc -w)"
if printf '%s' "$tp" | grep -qE '(^|,)(0\.0\.0\.0/0|::/0|\*)(,|$)'; then
    bad "TRUSTED_PROXIES=$tp 等于信任所有来源，X-Forwarded-For 可被任意伪造"
elif [ -z "$tp" ]; then
    if [ "$nets" -gt 1 ]; then
        note "TRUSTED_PROXIES 未设置，而容器接了 $nets 个网络：同网络容器可绕过 nginx 伪造 XFF —— 用 --proxy-peer 测出 nginx 源地址后钉死"
    else
        pass "TRUSTED_PROXIES 未设置（默认只信回环 + 私网，配合 9.1 已足够）"
    fi
else
    note "TRUSTED_PROXIES=$tp —— 只认 --proxy-peer 采到的对端，别照抄"
fi

# 9.4 会话密钥必须固定。为空时每次启动随机生成：重启掉所有会话，
#     依赖它的 HMAC（产物直链、缓存完整性）也会一起失效。
ss="$(printf '%s\n' "$envs" | sed -n 's/^SESSION_SECRET=//p' | head -1)"
if [ -z "$ss" ]; then
    note "SESSION_SECRET 未注入：每次启动随机生成，重启掉所有会话"
elif [ "$ss" = "random_string" ]; then
    bad "SESSION_SECRET=random_string：程序会拒绝启动"
else
    pass "SESSION_SECRET 已固定（${#ss} 字符）"
fi

# 9.5 空库重建后最容易漏的一步：status=false 表示没有 setup 记录且没有 root，
#     /api/setup 对所有人开放，可被直接创建 root 接管站点。
#     注意不能只看 root_init：空库和已初始化都是 false。
setup_body="$(curl -s --noproxy '*' --max-time 5 "http://127.0.0.1:$PORT/api/setup" || true)"
initialized="$(printf '%s' "$setup_body" | python3 -c 'import sys,json;print(str(json.load(sys.stdin).get("data",{}).get("status")).lower())' 2>/dev/null)"
case "$initialized" in
    true)  pass "已初始化（/api/setup 不再开放创建 root）" ;;
    false) bad "/api/setup status=false —— 未初始化，任何人都能创建 root 接管站点" ;;
    *)     note "读不到 /api/setup 的 status（$setup_body）" ;;
esac

# 9.6 内存缓存是选路代码的开关，不是性能选项。
mc="$(printf '%s\n' "$envs" | sed -n 's/^MEMORY_CACHE_ENABLED=//p' | head -1)"
if [ "$mc" = "true" ]; then
    pass "MEMORY_CACHE_ENABLED=true"
else
    bad "MEMORY_CACHE_ENABLED=${mc:-未设置} —— 会退到每请求查库的另一套选路"
fi

# 9.7 追溯窗口由日志 TTL 决定。只记 7 天等于「一周前的盗刷查不到」。
ttl="$(docker exec "$CH" clickhouse-client --password="$chp" -q \
    "SELECT ttl_expression FROM system.tables WHERE database='apiduo_cn_logs' AND name='logs'" 2>/dev/null)"
ttl_days="$(printf '%s' "$ttl" | grep -oE '[0-9]+' | sort -n | tail -1)"
if [ -n "$ttl_days" ] && [ "$ttl_days" -ge 90 ]; then
    pass "日志保留 ${ttl_days} 天"
elif [ -n "$ttl_days" ]; then
    note "日志保留仅 ${ttl_days} 天（$ttl）—— 事后追溯窗口过短"
else
    note "读不到 logs 的 TTL（${ttl:-查询失败}）"
fi

echo
printf '结果: OK=%d WARN=%d FAIL=%d\n' "$ok" "$warn" "$fail"
[ "$fail" -eq 0 ] || exit 1
