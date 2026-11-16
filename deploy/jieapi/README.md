# jieapi.com 主站（apiduo 双节点）—— core-1 部署

2026-11-15 01:36 全面转正完成：rc.40 已下线，master 与 standby 都是 apiduo；
数据继续复用既有 MySQL（`jieapi-master` 库）与 ClickHouse（`new_api_logs` 库）。

## 当前状态（2026-11-15 01:50 实测）

| 项 | 值 |
|---|---|
| 对外版本 | `0.0.121`，`system_name=接JieAPI` |
| master | `jieapi-apiduo` @ core-1，`127.0.0.1:3002`，`NODE_TYPE=master`，**12C/12G**，healthy |
| 备节点 | `jieapi-apiduo-standby` @ **core-3**，`61.111.253.243:3003`，`NODE_TYPE=slave`，8C/8G，healthy |
| 同机 standby | `jieapi-apiduo-standby` @ core-1，**已停**（容器保留，`--profile local-standby` 可恢复） |
| nginx | `upstream jieapi_apiduo`：3002 主 + `61.111.253.243:3003 backup`，`keepalive 128` |
| 实际占用 | master CPU 0.9–3.1% / 130–141 MiB；core-3 备节点故障期外零流量 |
| MySQL | 40 / **2000** 连接（`/etc/my.cnf` 已同步） |
| 慢 SQL | **0**（转正前 `INSERT INTO logs` 4–7 秒/条） |
| 后台任务 | 只有 master 跑：`system task runner started: runner=jieapi-apiduo-...`；两个 slave 均为 0 |
| rc.40 | `jieapi-master`、`jieapi-master-rc40`（core-1）与 `jieapi-slave`（core-3）全部 `exited` + `restart=no`，容器保留作回滚点 |

### core-3 备节点的能力边界（必须先读）

core-3 到 core-1 的实测 RTT 是 **137 ms**（跨地域）：

| 依赖 | 单次往返 |
|---|---|
| MySQL 3306 | 146 ms |
| Redis 6379 | 151 ms |
| ClickHouse 9000 | 147 ms |

所以它**只能做灾难接管，不能分担流量**：按 Redis 池 64 / 150 ms 估算，吞吐上限约 210 req/s，
比 core-1 低一个数量级。nginx 里它是 `backup`，正常零流量。

**它防不了整机故障**：core-3 本地没有任何数据服务，MySQL/Redis/CH 全指向 core-1。
core-1 整机宕机时这个节点也起不来。它真正覆盖的是
「core-1 的 Docker 或容器全挂，但宿主机 MySQL/Redis 还活着」——
此时 ClickHouse 也挂了，但异步日志 writer 会落到本地 spool，不阻塞请求。

**Failover 演练实测**（2026-11-15 01:55，把 master 从 upstream 临时移除）：

| 路径 | core-1 本机 | core-3 接管 |
|---|---|---|
| `/api/status` | 0.0005 s | 0.30 s |
| `/api/pricing` | — | 0.38–0.69 s |
| `/v1/models`（401） | — | 0.43–1.13 s |
| **`/v1/chat/completions`（200）** | — | **3.95 s** |

结论：**能接管，真实 relay 请求可以正常完成**，代价是轻量端点慢 600 倍。
接管期间 7 req/s 的流量它能扛（210 req/s 上限），但不要指望它分担。

下面第三节是**执行时的完整步骤**，保留下来是为了下次重建或换机时能照着做。


## 一、参数调优：每一处都对着实测数据

调优前先确认了约束与真实负载，不是按经验调：

| 实测项 | 值 | 来源 |
|---|---|---|
| 峰值请求速率 | **7 req/s**（376 req/min，`/v1` 峰值 431） | `apiduo-canary.log` 按分钟聚合 |
| 实际 CPU | **0.61%** | `docker stats` |
| 实际内存 | **156 MiB** | `docker stats` / cgroup `memory.current` |
| 宿主机 | 32 核 / 249 Gi 内存 / **swap 4G 已用 3.4G** | `nproc` / `free` |
| 全机容器 cpu 限额合计 | **101.5**（超配 3.2 倍） | 所有运行中容器的 `NanoCpus` 求和 |
| MySQL | 9.0.1，`max_connections=500`，**历史峰值 501（已打满）** | `SHOW STATUS` |
| Redis | `maxclients=102400`，实际 34 客户端 | `INFO clients` |
| 容器 fd | 上限 1048576，实际用 147 | `ulimit -n` / `/proc/1/fd` |

### 改动项

| 参数 | 旧值 | 新值（master / standby） | 依据 |
|---|---|---|---|
| `cpus` | 20 | **8 / 4** | 峰值 7 req/s、实测 0.6%。8 核是 13 倍余量。全机限额已 101.5 vs 32 核，20 会与其他容器互抢 |
| `cpu_shares` | 默认 1024 | **2048 / 1024** | 真争抢时主节点优先，空载无成本 |
| `mem_limit` | 20g | **8g / 4g** | 实测 156 MiB，8G 是 50 倍余量 |
| **`memswap_limit`** | 未设（docker 默认 2×） | **= mem_limit** | **最关键一项**。宿主机 swap 只剩 0.6G，容器一旦逼近上限就写 swap，延迟从 ms 变 s。禁 swap 后 OOM 会快速重启，不会拖慢全机 |
| `GOMAXPROCS` | 20 | **8 / 4** | 跟随 `cpus`，否则 Go 会按 20 个 P 起线程却抢不到核 |
| `GOMEMLIMIT` | 16GiB | **6GiB / 3GiB** | 留 2G / 1G 给非 Go 内存（mmap、文件描述符、runtime 元数据） |
| **`SQL_MAX_OPEN_CONNS`** | 1024 | **128 / 64** | MySQL 上限 500，旧值单节点理论上限就超总上限一倍。两节点 192 + 其他应用约 50，留 ~250 余量 |
| `SQL_MAX_IDLE_CONNS` | 128 | **32 / 16** | `wait_timeout=28800`（8 小时），空闲连接挂很久，给多了是纯占用 |
| `REDIS_POOL_SIZE` | 200 | **64 / 32** | 实测 34 客户端，Redis 上限 102400 |
| `ulimits.nofile` | 继承（未声明） | **1048576 显式** | 流式长连接多时 fd 是硬门槛，显式声明避免 dockerd 重启后继承值变化 |
| `logging` | 无轮转（`json-file map[]`） | **100m × 3** | 旧容器 stdout 无限增长，`apiduo-canary` 已 4.9 M |

### 最大的一处性能修复：`LOG_WRITER_ASYNC`

这一项比上面所有资源参数加起来更重要，且**默认是关闭的**。

apiduo 的 `model/log.go:createLog` 会先调 `enqueueLog`，但 `enqueueLog` 只在
`LOG_WRITER_ASYNC=true` 时才生效，否则直接落到 `LOG_DB.Create(log)` —— 也就是在
**请求路径上同步写 ClickHouse**。而 DSN 里带 `wait_for_async_insert=1` +
`async_insert_busy_timeout_ms=8000`，这条 INSERT 要等 CH 的 async-insert 缓冲 flush。

线上实测（升级前的 `apiduo-canary`）：

```
2026/11/15 01:30:05 /build/model/log.go:107 SLOW SQL >= 200ms
[7312.964ms] [rows:0] INSERT INTO `logs` ...
[6011.926ms] [rows:0] INSERT INTO `logs` ...
[5558.531ms] [rows:0] INSERT INTO `logs` ...
```

源码注释原话：`"a slow or busy log database directly stalls API requests"`。

开启后：

```
[SYS] log writer target: [clickhouse:9000] database=new_api_logs table=logs
[SYS] log writer enabled: dir=/data/log_spool interval=5s batch=2000 queue=100000 enqueue_wait=1s replayed=0
```

效果：**SLOW SQL 从 4–7 秒/条 降到 0**（两个节点 `grep -c 'SLOW SQL'` 均为 0）。
请求路径只做内存入队，后台每 5 秒用 native batch 批量写一次。

三个必须知道的副作用：

| 项 | 说明 |
|---|---|
| 日志延迟 | 约 5 秒（`LOG_WRITER_INTERVAL_MS`）。看实时日志会觉得"慢半拍"，但计费与对账不受影响 |
| 语义 | **at-least-once**。崩溃后 spool 回放会重复行，由 `ReplacingMergeTree(ORDER BY created_at, request_id)` 去重 |
| spool 目录 | **无实例锁**，多实例必须各自独立。本编排两节点挂不同 `/data`，安全；若将来共享卷会互相 rotate/confirm 对方的文件 |

### 其它对外域名（易被忽略）

下线任何容器前，**必须反查所有 nginx upstream 引用**，不要只看 `jieapi.com` 的 vhost。
2026-11-15 就踩过：停掉 core-3 的 rc.40 后，`cn.jieapi.com` 和 `aapi.cn` 直接 502，
它们由 core-3 的 `cn-entry.conf` 服务，`upstream newapi_slave` 指向那台容器的 3001。

| 域名 | 解析 | 服务方 | 状态 |
|---|---|---|---|
| `jieapi.com` / `api.jieapi.com` | core-1 | nginx → apiduo master 3002 | 200 / 0.0.121 |
| `cn.jieapi.com` | core-3 | core-3 nginx → apiduo 备节点 3003 | 200 / 0.0.121（跨 137 ms 回库，约 +300 ms） |
| `aapi.cn` | core-3 | 同上（与 cn 共用 upstream） | 200 / 0.0.121 |
| `apicn.jieapi.cn` | **110.42.51.129**（非本集群） | 该机只配了 `all.jieapi.cn` | **HTTPS 证书不匹配 + HTTP 404** |

`apicn.jieapi.cn` 需要单独确认归属：那台机器的证书是 `CN=all.jieapi.cn`，
没有 `apicn.jieapi.cn` 的 server 块。要么在那台机器加 vhost + 证书，要么把 DNS 指到 core-1。

`cn.jieapi.com` / `aapi.cn` 的流量现在叠加在 core-3 的备节点上。
如果 core-1 master 也挂了，core-3 要同时承接 jieapi.com + cn + aapi，负载会叠加——
虽然当前总量只有约 7 req/s，但扩容时要记得算进去。

### 保持不动的项（附理由，避免下次"顺手优化"踩回去）

| 参数 | 值 | 为什么不动 |
|---|---|---|
| `STREAMING_TIMEOUT` | 300 | 与实测 p99（69s）、max（353s 为重试累计）匹配；调长只会让僵尸连接占资源更久 |
| `RELAY_MAX_IDLE_CONNS` | 4096 | 这是 Go Transport 的 `MaxIdleConns`，只占 fd 不占 CPU，fd 余量 7000 倍 |
| `RELAY_MAX_IDLE_CONNS_PER_HOST` | 1024 | 同上，是上游并发能力，不是本地消耗 |
| `GLOBAL_API_RATE_LIMIT` | 10240 | 峰值 7 req/s，远未触达，留作洪水保护 |
| `LANGFUSE_SAMPLE_RATE` | 100 | 全量 trace 有真实 CPU 开销（每请求序列化 body），但这是明确要求保留的观测能力。**这是唯一一处"为可观测性牺牲性能"** |
| nginx `proxy_buffering` | 保持默认 on | 实测过：apiduo 在 `relay/helper/common.go:210` 发 `X-Accel-Buffering: no`，nginx 对流式响应自动不缓冲。19,667 个 `/v1` 请求的 `request_time - upstream_response_time` **平均差 0.038s**，>0.5s 仅 1.3%。缓冲层无需改动 |

**结论：这套站点的性能瓶颈不在网关资源**，而在（a）swap 抖动、（b）MySQL 连接池超上限。两处都已修正。
把 CPU 从 20 降到 8 不会降低吞吐——上限不是预留，实测用量差了 13 倍。

## 二、容量模型：并发升高时先扩什么

2026-11-15 01:49 实测（100 并发 × 2000 请求打 `/api/status`）：

```
耗时 1s  QPS ≈ 2000  全部 200  CPU 0.92%  内存 130 MiB
```

单请求基线延迟 0.4–0.9 ms。真实 relay 面 7 req/s 时 CPU 0.5%（≈0.06 核）。
说明**网关进程不是瓶颈**，先撞墙的按顺序是：

| 顺序 | 约束 | 当前值 | 撑到多少 | 怎么扩 |
|---|---|---|---|---|
| 1 | **日志 writer 吞吐** | 8000 行 / 3s ≈ 2667 行/秒 | ≈2400 req/s（每请求 1.09 行） | 加 `LOG_WRITER_BATCH_SIZE` |
| 2 | **MySQL 连接** | `max_connections=2000` | 两节点 384 + 其他 ~50，撑 ~1500 并发 DB 操作 | 再提 `max_connections` 与 `SQL_MAX_OPEN_CONNS` |
| 3 | **上游供应商限流** | — | 未知，历史 429 的主要来源 | 加渠道 / 提配额，**不是扩本机** |
| 4 | 文件描述符 | 1048576 | 约 50 万并发连接 | 无需动 |
| 5 | nginx | 32 worker × 51200 conn | 远未触达 | 无需动 |
| 6 | CPU | 12 + 8 核 | 按 0.06 核/7req/s 外推 ≈ 2000 req/s | 最后才扩 |

**结论：这套配置覆盖到当前流量的约 280 倍（≈2000 req/s）才会碰 CPU 天花板。**
在那之前，真正的瓶颈是上游渠道配额，而不是本机。

### 高并发前的三个动作（按性价比排序）

1. **把 standby 迁到 core-3**（机器级隔离）。现在是同机两个容器抢同一批核，
   而 core-3 已经腾空。跨机只需把 `SQL_DSN` / `REDIS_CONN_STRING` / `LOG_SQL_DSN`
   里的 `host.docker.internal` 换成 `104.238.221.50`，nginx upstream 指向它。
   注意跨机 Redis 会有约 0.3–0.5 ms 额外延迟（同机房内可接受）。
2. **给 Langfuse 降采样**。`LANGFUSE_SAMPLE_RATE=100` + 内容 10% 是唯一
   "为观测牺牲性能"的地方：每个请求都要序列化 request/response body。
   高并发下这是主要的额外 CPU 来源。建议高并发期降到 20–30%。
3. **确认 CH 能跟上**。日志表的 `ReplacingMergeTree` 合并有开销，
   写入从 4.5 行/秒涨到 2400 行/秒时，`events_full` 那种 13 GB/天 的量级要重新评估磁盘。

### 扩容触发条件（监控这三个就够）

```bash
# 1. 日志队列是否积压（>0 说明 writer 跟不上）
docker logs jieapi-apiduo 2>&1 | grep -iE 'queue full|fell back|synchronous' | tail -3

# 2. MySQL 连接使用率（>70% 就要动）
mysql -e "select count(*)/@@max_connections from information_schema.processlist;"

# 3. 单实例 CPU 是否接近 GOMAXPROCS（>80% 才需要加核）
docker stats --no-stream --format '{{.Name}} {{.CPUPerc}}' jieapi-apiduo
```

> 注意 `CPUPerc` 是相对**单核**的百分比。`1200%` 才是吃满 12 核，
> 也就是说 `>800%` 时才接近上限。

## 三、目录布局

```
/www/docker/apiduo/
├── docker-compose.yml        # 主本在仓库 deploy/jieapi/，这里是运行时副本
├── app.env                   # 业务配置，master / standby 共用（600）
├── data/                     # master /data（新目录，原本就是空的，无数据迁移）
└── logs/                     # master /app/logs

/www/docker/apiduo-standby/{data,logs}   # standby 独立目录
/www/docker/apiduo-langfuse-b.env        # Langfuse B 密钥（600）
```

两个节点共用同一份 `app.env`；差异只在 `docker-compose.yml` 的 `environment` 段
（`NODE_*`、`GOMAXPROCS`、连接池大小）。这是为了避免旧 `base.env` 那个坑：
文件里写 `NODE_TYPE=master`，再靠命令行 `-e` 覆盖，`docker inspect` 看到的第一值是错的。

## 四、转正执行（零失败，约 2 分钟）

### 0. 备份

```bash
TS=$(date +%Y%m%d-%H%M%S)
mkdir -p /www/backup/jieapi-cutover/$TS
cd /www/server/panel/vhost/nginx
cp -a jieapi.com.conf api.jieapi.com.conf 0.apiduo-canary.conf /www/backup/jieapi-cutover/$TS/
DSN=$(docker inspect jieapi-master --format '{{range .Config.Env}}{{println .}}{{end}}' | sed -n 's/^SQL_DSN=//p')
U=$(echo "$DSN" | sed -E 's#^([^:]+):.*#\1#'); P=$(echo "$DSN" | sed -E 's#^[^:]+:([^@]+)@.*#\1#'); DB=$(echo "$DSN" | sed -E 's#.*\)/([^?]+).*#\1#')
MYSQL_PWD="$P" mysqldump --single-transaction --quick --routines --triggers \
  -h127.0.0.1 -P3306 -u"$U" "$DB" | gzip > /www/backup/jieapi-cutover/$TS/mysql.sql.gz
```

### 1. 建目录与 app.env

```bash
mkdir -p /www/docker/apiduo/{data,logs} /www/docker/apiduo-standby/{data,logs}
cd /www/docker/apiduo
cp <repo>/deploy/jieapi/docker-compose.yml .
cp <repo>/deploy/jieapi/app.env.example app.env    # 再把占位符换成真值
chmod 600 app.env
# 或者直接从旧 base.env 派生（连接串/密钥/限流都现成）：
#   grep -vE '^(NODE_|GOMAXPROCS|GOMEMLIMIT|SQL_MAX_|REDIS_POOL_SIZE)' \
#     /www/docker/apiduo-canary/base.env > app.env
docker-compose config >/dev/null && echo "schema OK"
```

### 2. 先起 standby（不影响现网流量）

```bash
cd /www/docker/apiduo
docker-compose up -d apiduo-standby
sleep 8
curl -s -m 5 -o /dev/null -w 'standby=%{http_code}\n' http://127.0.0.1:3003/api/status
docker exec jieapi-apiduo-standby sh -c "tr '\0' '\n' < /proc/1/environ | grep -E '^NODE_(TYPE|NAME)='"
# 必须是 NODE_TYPE=slave，否则会和主节点抢后台任务
```

### 3. 排空 master 到 rc.40，再重建

`split_clients` 不接受 `0%`，只能改 map 目标：

```bash
F=/www/server/panel/vhost/nginx/0.apiduo-canary.conf
sed -i 's#    "apiduo1"   127.0.0.1:3002;#    "apiduo1"   127.0.0.1:3001;#' "$F"
nginx -t && nginx -s reload

B=$(wc -l < /www/wwwlogs/apiduo-canary.log); sleep 10
A=$(wc -l < /www/wwwlogs/apiduo-canary.log); echo "增量=$((A-B))（应为 0）"
```

排空后**等 10–30 秒**再删容器 —— 2026-11-15 转正时急着删，打断了 5 条在途长流。

```bash
docker rm -f apiduo-canary
cd /www/docker/apiduo && docker-compose up -d apiduo
sleep 8
curl -s -m 5 -o /dev/null -w 'master=%{http_code}\n' http://127.0.0.1:3002/api/status
```

### 4. nginx 换成 upstream 双后端

当前是**变量 `proxy_pass`**（`http://$jieapi_backend_final`），变量无法指向 upstream 名，
所以做不到自动 failover。改成固定 upstream：

`0.apiduo-canary.conf` 里追加：

```nginx
upstream jieapi_apiduo {
    server 127.0.0.1:3002 max_fails=3 fail_timeout=10s;
    server 127.0.0.1:3003 backup;     # 主节点被摘除才接管
    keepalive 32;
    keepalive_timeout 30s;
    keepalive_requests 1000;
}
```

`jieapi.com.conf` 与 `api.jieapi.com.conf` 各改一处：

```nginx
# 改前
proxy_pass http://$jieapi_backend_final;
# 改后
proxy_pass http://jieapi_apiduo;
```

`$conn_upgrade_jieapi` 的 map 已经是 `default upgrade; '' '';` 形式，
与 upstream 的 `keepalive` 兼容（空串时复用连接，WebSocket 时正常升级），不需要改。

全量转正后，`split_clients` / `$jieapi_canary` / `$jieapi_console_forced` 这套分流与
预览 cookie 机制都不再需要，可以一并删除。**建议保留文件不删，只改 `proxy_pass`**，
回滚时少一步。

### 5. rc.40 下线

```bash
docker stop jieapi-master jieapi-master-rc40 2>/dev/null
docker update --restart=no jieapi-master jieapi-master-rc40
# 保留容器与镜像作为最终回滚点，不要 rm
```

**不要删** `/www/server/panel/data/compose/jieapi-master/docker-compose.yaml`：
它现在仍管着 `clickhouse` 容器（`project=jieapi-master`，running）。
删掉文件会让下次有人在宝塔面板操作时把日志库编排搞乱。
也**不要**对该工程执行 `docker-compose up` —— 它会按 `container_name: jieapi-master`
和 `NODE_TYPE=master` 重建，与现状冲突。

### 6. 验证

```bash
curl -s -H 'Host: jieapi.com' http://127.0.0.1/api/status \
  | python3 -c "import sys,json;d=json.load(sys.stdin)['data'];print(d['version'],d['system_name'])"
# 期望：0.0.121 接JieAPI

# 两个节点的角色
for c in jieapi-apiduo jieapi-apiduo-standby; do
  docker exec $c sh -c "tr '\0' '\n' < /proc/1/environ | grep -E '^NODE_(TYPE|NAME)='"
done

# 后台任务只有一个 runner
docker logs --since 5m jieapi-apiduo 2>&1 | grep -c '系统任务执行器已启动'   # 1
docker logs --since 5m jieapi-apiduo-standby 2>&1 | grep -c '系统任务执行器已启动'  # 0

# MySQL 连接不超限
mysql -e "select count(*) from information_schema.processlist;"   # 应 < 300
```

## 五、回滚

**回到 rc.40（约 10 秒）**。当前 `proxy_pass` 已是固定 upstream，回滚要改回变量：

```bash
F=/www/server/panel/vhost/nginx
docker start jieapi-master          # 它是 NODE_TYPE=slave，不会抢后台任务
sed -i 's#proxy_pass http://jieapi_apiduo;#proxy_pass http://$jieapi_backend_final;#' \
  $F/jieapi.com.conf $F/api.jieapi.com.conf
nginx -t && nginx -s reload
```

`$jieapi_backend` 的两个分支当前都指向 `127.0.0.1:3001`（转正时排空留下的），
所以改回变量后流量直接落到 rc.40，不需要再动 map。

**只回滚单节点**：master 挂了但 standby 正常时，`upstream` 的 `backup` 会自动接管，
nginx 不需要动。确认接管情况：

```bash
tail -n 200 /www/wwwlogs/jieapi.com.error.log | grep -E 'upstream|no live'
```

⚠️ 注意 autoheal 配的是 `AUTOHEAL_CONTAINER_LABEL=all`（每 5 秒扫一次），
所以 healthcheck 连续失败约 90 秒后容器会被**自动重启**。
要临时禁用这套兜底，`docker stop autoheal` 比改 compose 快。

## 六、排障备忘

- **容器起不来先看 `/proc/1/environ`**，不要信 `docker inspect` 的 Env 数组顺序。
- **`LOG_SQL_DSN` 里的 `clickhouse` 是容器名**，必须与 clickhouse 在同一个 docker 网络
  （`jieapi-master_new-api-network`）才解析得到。
- **`CRYPTO_SECRET` 不要改**：加密 `channels.key`，变更会让全部渠道密钥解不开。
- **禁 swap 后 OOM 会直接杀容器**，`restart: unless-stopped` 会自动拉起（约 2–5 秒）。
  这是有意选择的：快速失败优于全机 swap 抖动。
- **两个 `NODE_TYPE` 不能都是 master**：`IsMasterNode` 只看这个值，没有选举没有锁，
  双 master 会重复跑任务轮询、渠道定时检测、订阅重置。
