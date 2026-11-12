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

### 1. 前置

```bash
# 目录（和应用里的 bind mount 一一对应）
mkdir -p /www/data/apiduo/data /www/data/apiduo/mysql /www/data/apiduo/clickhouse
mkdir -p /www/data/apiduo/clickhouse-config/{users.d,config.d}
mkdir -p /opt/apiduo

# 网络：compose 里引用了两个 external 网络（langfuse / obs）。
# 新机上没有就先把 compose 里这两行删掉，否则 up 会报 network not found。
```

### 2. 密钥（**必须一开始就固定，别让它走随机 uuid**）

```bash
cd /opt/apiduo
umask 077
cat > .env.db <<'EOT'
MYSQL_ROOT_PASSWORD=$(openssl rand -base64 32 | tr -d '/+=' | head -c 32)
CLICKHOUSE_PASSWORD=$(openssl rand -base64 32 | tr -d '/+=' | head -c 32)
EOT
# 注意：上面这种写法不会展开 $( )。要么手动填，要么用 shell 赋值后写文件。
```

`/www/data/apiduo/.env`（应用环境，600）至少要有：

```ini
SQL_DSN=root:<密码>@tcp(apiduo-mysql:3306)/apiduo?parseTime=true
LOG_SQL_DSN=clickhouse://default:<密码>@apiduo-clickhouse:9000/apiduo_logs
LOG_WRITER_ASYNC=true
SESSION_SECRET=<openssl rand -hex 32>
CRYPTO_SECRET=<openssl rand -hex 32>
SESSION_COOKIE_SECURE=true
SESSION_COOKIE_TRUSTED_URL=https://<新域名>
TZ=Asia/Shanghai
GIN_MODE=release
NODE_NAME=apiduo-node-1
BATCH_UPDATE_ENABLED=true
MEMORY_CACHE_ENABLED=true
ERROR_LOG_ENABLED=true
GLOBAL_API_RATE_LIMIT=600000
GLOBAL_API_RATE_LIMIT_DURATION=180
CRITICAL_RATE_LIMIT=30
CRITICAL_RATE_LIMIT_DURATION=1800
```

🔴 `SESSION_SECRET` / `CRYPTO_SECRET` 一旦定下就不能改，改了所有会话失效、已加密字段读不出来。

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
