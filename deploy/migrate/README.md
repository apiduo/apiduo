# new-api → apiduo 迁移手册（新机器 / 新域名）

> 源：`newapi-0to1`（`calciumion/new-api` rc.49，库 `newapi_0to1`，50MB）
> 目标：新机器 + 新域名 + `ghcr.io/apiduo/apiduo:latest`
> 2026-11-12 已在旧机上做过一次**完整演练**，结论见第一节。

## 一、已实测的结论（不是推测）

| 检查项 | 结果 |
|---|---|
| 双方 schema 同名列的类型/可空差异 | **0 处**（纯增量，无破坏性变更） |
| 只在 apiduo 侧的表 | `agent_keys`、`file_objects`、`mcp_error_rules`、`mcp_model_profiles`（新功能，空表即可） |
| 只在 apiduo 侧的列 | 58 个，全属上述新功能 |
| 只在源侧的表 | `e2e_tmp_tok`（e2e 临时表，**不迁**） |
| 实测：apiduo 0.0.119 挂 rc.49 数据启动 | **7 秒就绪**，AutoMigrate 只建 `mcp_error_rules`，无 error/duplicate |
| 数据可读 | 32 渠道（32 个都有 key，14 启用）/ 131 令牌 / 79 用户 |
| `/api/setup` | `root_init=false` —— 迁移后不会被初始化向导接管 |

所以迁移的**唯一真实风险不在 schema，在 DNS 切换和证书**。

## 二、新机器上从零起

### 1. 系统初始化（一条命令）

```bash
sudo bash deploy/migrate/init-host.sh            # 先 DRY_RUN=1 看一眼改什么
```

它做的事：内核参数（somaxconn / 端口范围 / file-max / swappiness）、Docker 的
`default-ulimits`（nofile 提到 1048576 —— 旧机卡在 8192 是隐形天花板）、目录、
以及**一次性生成** `.env.db` 与 `.env`（含 SESSION_SECRET/CRYPTO_SECRET）。

跑完记得改 `.env` 里的 `SESSION_COOKIE_TRUSTED_URL` 为新域名。

```bash
cp deploy/migrate/docker-compose.yml /opt/apiduo/
mkdir -p /www/data/apiduo/clickhouse-config/{config.d,users.d}
cp deploy/migrate/clickhouse-config/config.d/99-apiduo.xml  /www/data/apiduo/clickhouse-config/config.d/
cp deploy/migrate/clickhouse-config/users.d/99-apiduo.xml   /www/data/apiduo/clickhouse-config/users.d/
```

### 2. 密钥

`init-host.sh` 已经生成好了，不用手工写。要点只有一条：**生成一次之后不要再改**，
`SESSION_SECRET`/`CRYPTO_SECRET` 变了会让所有会话失效、已加密字段读不出来。

### 3. 导数据（在**源机**上执行）

```bash
bash deploy/migrate/dump-source.sh          # 产出 /tmp/mig/newapi-0to1-<时间戳>.sql.gz
scp <新机>:/tmp/xxx                         # 或走内网传输
```

### 4. 新机上起库并导入

```bash
cd /opt/apiduo
docker compose -f docker-compose.yml up -d apiduo-mysql apiduo-clickhouse
# 等两个都 healthy（mysql 首次初始化 30-60s）
docker compose -f docker-compose.yml ps

docker exec apiduo-mysql mysql -uroot -p"$MYSQL_ROOT_PASSWORD" -e \
  'CREATE DATABASE IF NOT EXISTS apiduo CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci'
zcat newapi-0to1-*.sql.gz | docker exec -i apiduo-mysql \
  mysql -uroot -p"$MYSQL_ROOT_PASSWORD" apiduo
```

### 5. 起应用

```bash
docker compose -f docker-compose.yml up -d apiduo
# 反复探到 success=true，别只看容器 Running
until curl -s --noproxy '*' http://127.0.0.1:<端口>/api/status | grep -q '"success":true'; do sleep 2; done
```

### 6. 域名与证书

新域名必须重新签，别拷旧证书。nginx 四份配置里所有 `/www/server/panel/vhost/nginx/`
路径要改成新机的实际路径，`server_name`、证书路径、`limit_req_zone` 的 `_cn` 后缀
（共享内存区名是全局的，和别站重名 `nginx -t` 直接失败）都要改。

### 7. 切换后验收

```bash
bash /opt/apiduo/preflight.sh     # 需按新环境改 APP/PORT/BACKUP_ROOT
```

手工再确认四项：

1. 后台能登录，用户列表 79 人
2. 渠道列表 32 个，随机点一个「测试」能通
3. 令牌复制一个，`curl /v1/models` 能列出模型
4. `/api/setup` 返回 `root_init:false`

## 三、不迁的东西（想清楚再决定）

| 项 | 建议 | 理由 |
|---|---|---|
| ClickHouse 日志 | **不迁** | 审计数据，不影响业务；如需对账再单独导 |
| `e2e_tmp_tok` 表 | 不迁 | e2e 临时表 |
| 源机 `/data` 里的 `logs/` | 不迁 | 容器 stdout 旧日志，无恢复价值 |
| 131 个令牌 | 全迁，但**顺手清理** | 迁移是清理废弃令牌的最好时机 |
| 渠道 1 `mock-upstream` | 核对后决定 | 若指向假上游，正式对外前禁用 |

## 四、回滚

**最大的安全网：旧机不动。** 迁移期间 `newapi-0to1` 照常跑，DNS 切过去之前新环境
随时可以放弃。出问题就把 DNS 指回去，分钟级。

数据回滚只在新环境已经写入新数据时才需要，届时从旧机重新 dump 一次即可（差量）。
所以**切换后的第一件事是确认旧机仍然可写、可回退**，别急着停它。

## 五、现有生产机的参考配置

同目录上一级的 `deploy/prod/` 是旧机 `apiduo-cn` 的完整配置（compose / nginx /
备份 / 自检），可直接当模板，改这四处即可：项目名、容器名、端口、路径。

那条经验同样成立：**空库首次启动会建 root/123456，起来后必须立刻改密码**；
迁移过来的库不存在这个问题（`root_init=false`）。


## 六、性能参数：为什么这么定

先说结论：**这台应用从来不慢，慢的是它脚下的磁盘。**

旧机实测（apiduo-cn，0.0.119，同一份镜像）：

| 指标 | 数值 |
|---|---|
| 吞吐 | **2471 QPS**，p50 23ms / p99 134ms，0 错误 |
| 压测时 CPU | **1.04 核**（机器 64 核） |
| 应用内存 | 29MB 空转 / 50MB 压测中（限额 2G） |
| MySQL 慢查询 | 212 条，最慢的一条是 `COMMIT`，**162 秒** |
| 同机最慢 `SELECT` | 70 秒，只扫了 149 行（每秒 2 行） |
| 磁盘 `sda` 写延迟 | **86–90ms**（正常 SSD < 5ms），util 28–50% |
| 同机容器数 | **103 个** |

162 秒的 COMMIT 不是 MySQL 配置问题 —— 是同一块盘上 103 个容器在抢 IO。
所以新机的第一优先级是**独占磁盘**，其次才是下面这张表。

### 逐项依据

| 参数 | 旧值 | 新值 | 理由 |
|---|---|---|---|
| `innodb_flush_log_at_trx_commit` | 1 | **2** | 每笔 commit 两次 fsync → 每秒一次。应用侧 `BATCH_UPDATE_INTERVAL=5` 本来就是 5 秒批量落额度，实时性没有变化 |
| `sync_binlog` | 1 | **0** | binlog 交 OS 刷盘。前提是每日全量备份在跑 + binlog 保留 3 天（`binlog_expire_logs_seconds=259200`） |
| `innodb_buffer_pool_size` | 4G | **8G** | 现在数据 50MB，但按两年增长留余量；8G 可覆盖千万级行 |
| `innodb_log_buffer_size` | 默认 16M | **64M** | 批量更新一次提交上千行，大 buffer 少刷盘 |
| `max_connections` | 300 | **1000** | 旧机峰值用到 131，多实例/备份/监控要留头 |
| `innodb_io_capacity` | 2000 | **4000** | 按 NVMe 给；**如果是 SATA/机械盘要降回 200–800** |
| `GOMAXPROCS` | 8 | **16** | 压测 1 核跑 2471 QPS，16 核有足够余量，同时避免 64 核全开的 GC 抖动 |
| `GOMEMLIMIT` | 1536MiB | **3GiB** | 容器限额 4G，留 1G 给 runtime 与 off-heap |
| `SQL_MAX_OPEN_CONNS` | 100 | **200** | MySQL 端 1000，200 不会打满 |
| `LOG_WRITER_BATCH_SIZE` | 5000 | **2000** | 批越大失败重放代价越高；配合 1 秒间隔，延迟更低 |
| `LOG_WRITER_QUEUE_SIZE` | 50000 | **100000** | 削峰，扛住 CH merge 时的短暂停顿 |
| CH `max_server_memory_usage` | 4G | **8G** | 容器 10G，留 2G 给页缓存与合并峰值 |
| CH `background_pool_size` | 16 | **32** | 64 核机器 |
| CH 系统表 TTL | 部分靠 ALTER | **config.d 定死** | ALTER 的 TTL 会在 CH 重启重建表时丢失，旧机因此累积 60G `trace_log` |
| nginx `worker_connections` | 51200（fd 8192） | **65535 + rlimit 1M** | 旧机配置写 51200 但 worker fd 只有 8192，实际永远达不到，容量估算被误导 |
| `CRITICAL_RATE_LIMIT` | 30/1800s | **60/1800s** | 登录 / refresh / 2FA / agent 握手共用一个计数器，30 次/30 分钟对正常用户偏紧 |

### 三条不能越的边界

1. **`innodb_flush_log_at_trx_commit` 不给 0**。`=2` 最多丢 1 秒内提交的事务；`=0` 连
   「每秒刷」都不保证，MySQL 进程崩溃就会丢，计费系统不拿这个换 IOPS。
2. **`sync_binlog=0` 有前提**：每日全量备份必须在跑，且 `binlog_expire_logs_seconds`
   不能设成 0（否则 binlog 无限涨，会把盘吃满）。PITR 就靠这两样。
3. **端口必须绑 `127.0.0.1`**。裸绑 `0.0.0.0` 后同私网主机可伪造 `X-Forwarded-For`，
   直接绕过令牌的 `allow_ips` 白名单。

### 上线后盯这几个数

```bash
# MySQL：fsync 频率（=2 之后应该远低于 QPS）
docker exec apiduo-mysql mysql -uroot -p"$MYSQL_ROOT_PASSWORD" -N -B   -e "SHOW GLOBAL STATUS WHERE Variable_name IN ('Innodb_data_fsyncs','Innodb_os_log_written','Threads_connected')"
# 磁盘：写延迟应稳定在个位数 ms
iostat -x 2 5 | grep -E "^sd|^nvme"
# CH：合并是否堆积
docker exec apiduo-clickhouse clickhouse-client --password="$CLICKHOUSE_PASSWORD"   -q "SELECT count() FROM system.merges; SELECT count() FROM system.parts WHERE active"
```

判断标准：`iostat` 的 `w_await` 若长期 > 20ms，说明新机也在被别的负载抢盘 ——
那时该做的是换盘或迁走邻居，继续调 MySQL 参数没有意义。
