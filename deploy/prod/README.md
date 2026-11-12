# apiduo.cn 生产部署手册

> 2026-11-12 整理。目标：明天上线前，任何一个人拿到这份文档 + 一台机器，能把站点重建起来；
> 出故障时能在 5 分钟内退回去。

## 一、现状

| 项 | 值 |
|---|---|
| 主机 | `ai-prod` = 210.121.44.245（IDC 托管，47G 内存 / 64 核 / 929G 盘） |
| 入口 | `https://apiduo.cn`（裸域，控制台与 `/api` `/v1` 同源），nginx 在宿主机 |
| 应用 | 容器 `apiduo-cn`，`127.0.0.1:39047 → 3000`，当前 `0.0.119` |
| 主库 | 容器 `apiduo-cn-mysql`（mysql 8.2，库 `apiduo_cn`），2.2G |
| 日志库 | 容器 `apiduo-cn-clickhouse`（25.8，库 `apiduo_cn_logs`），TTL 360 天 |
| 编排 | `/opt/apiduo-cn/docker-compose.apiduo-cn.yml`（本目录同名文件是入库版） |
| 应用密钥 | `/www/data/apiduo-cn/.env`（600） |
| 数据目录 | `/www/data/apiduo-cn/{mysql,clickhouse,data}` |
| 备份 | `/www/backup/apiduo-cn/<时间戳>/`，每日 04:20，保留 14 天 |
| 配置副本 | `/opt/apiduo-cn/nginx/*.conf`（安装到 `/www/server/panel/vhost/nginx/`） |

资源占用：容器限额 6+6+2=14G，实测整机用 16/47G；磁盘用 17%。

## 二、上线前自检

```bash
ssh ai-prod 'bash /opt/apiduo-cn/preflight.sh'
```

10 项检查，`FAIL` 必须清零。重点看三项：

1. **`/api/status` 的 `success` 字段**，不是 HTTP 200。`/api/status` 恒 200，
   只有 `success=true` 才说明 DB 连上、迁移跑完。
2. **`/api/setup` 的 `root_init` 必须是 `false`**。若为 `true`，任何人都能走初始化
   向导创建 root 账号接管站点 —— 空库重建后最容易漏这一步。
3. **备份新鲜度 ≤ 26h**。cron 静默失败是常态，别靠"应该跑了"判断。

## 三、发版（push main 自动发版）

```bash
git push origin main          # 触发 brand-release：patch 自增 → 构建 → 冒烟 → 推 GHCR + tag
gh release view -R apiduo/apiduo   # 确认新版本号，别凭记忆
ssh ai-prod 'docker pull ghcr.io/apiduo/apiduo:latest && cd /opt/apiduo-cn && docker compose up -d apiduo-cn'
ssh ai-prod 'curl -s --noproxy "*" http://127.0.0.1:39047/api/status | grep -o "\"version\": *\"[^\"]*\""'
```

坑：

- **必须先 `docker pull` 再 `up -d`**。compose 只看配置不看 digest，直接 up 不会拉新镜像，
  表现是"发了版但版本号没变"。线上曾因此白跑一轮。
- **`docker restart` 不重读 `env_file`**。改了 `.env` 必须
  `docker compose up -d --force-recreate apiduo-cn`。
- 部署机偶尔卡在 `up -d` 的隐式 pull 十几分钟。手动 pull 会解开。
- commit message 里只要出现 `[skip release]`，整次发版被跳过。

## 四、回滚（三级，从快到慢）

**第 1 级：回滚镜像（30 秒，首选）**

```bash
ssh ai-prod 'cd /opt/apiduo-cn && APIDUO_IMAGE_TAG=0.0.118 docker compose up -d apiduo-cn'
```

`APIDUO_IMAGE_TAG` 只在入库版 compose 里生效。线上现版写死 `:latest`，
回滚要临时改 `image:` 那一行为具体 tag，或把入库版 compose 换上去（推荐后者，一次性）。

回滚前确认 GHCR 上有该 tag：`gh release list -R apiduo/apiduo`。
注意**服务器本地只有 0.0.14 及更早的镜像**，0.0.115+ 的 tag 都得重新 pull。

**第 2 级：回滚配置**

`/opt/apiduo-cn/` 下有一串 `docker-compose.apiduo-cn.yml.bak-*`。
改坏了就 copy 回最新那个，再 `up -d`。

**第 3 级：回滚数据（有损，最后手段）**

见下一节。会丢最后一次备份之后的数据。

## 五、备份与恢复

```bash
ssh ai-prod '/opt/apiduo-cn/backup-cn.sh'              # 手动跑一次
ssh ai-prod 'ls -lh /www/backup/apiduo-cn/$(ls /www/backup/apiduo-cn | tail -1)/'
```

每个快照三个文件：

| 文件 | 内容 | 丢了会怎样 |
|---|---|---|
| `mysql.sql.gz` | 主库全量（渠道/令牌/用户/额度/价格） | 站点回空库，要重录几十个渠道密钥 |
| `data.tar.gz` | `/data` 里的业务文件（不含 logs） | 历史上传文件 404，业务能跑 |
| `clickhouse-schema.sql` | 日志表 DDL + TTL | 不影响业务，只影响追溯 |

**为什么 ClickHouse 不备数据**：日志是审计数据，不是恢复业务的最小集合；141 万行/天全量导出
的代价远大于收益，长期保留靠 CH 自己的 TTL（360 天）与磁盘冗余。真要异地长留，走对象存储。

**恢复 MySQL（已演练过，2026-11-12）**：

```bash
PASS=$(docker exec apiduo-cn-mysql printenv MYSQL_ROOT_PASSWORD)
docker exec -e MYSQL_PWD="$PASS" apiduo-cn-mysql mysql -uroot -e \
  'CREATE DATABASE IF NOT EXISTS apiduo_cn CHARACTER SET utf8mb4'
zcat /www/backup/apiduo-cn/<时间戳>/mysql.sql.gz \
  | docker exec -i -e MYSQL_PWD="$PASS" apiduo-cn-mysql mysql -uroot apiduo_cn
# 恢复后必须复查
curl -s --noproxy '*' http://127.0.0.1:39047/api/setup    # root_init 必须为 false
```

演练记录：2026-11-12 19:24 的备份导入临时库，41 表 / 14 渠道 / 54 令牌 / 42 用户，
与生产逐项相等。

**诚实边界**：备份在同机 `/www/backup`，只防误删/逻辑损坏，**不防磁盘或主机整体故障**。
异地副本是待办（见第八节）。

## 六、已知坑

1. **`apiduo-cn-mock` 是野容器**。7 天前手工 `docker run` 的 python mock（e2e 用的假上游），
   挂在 `apiduo-cn_default` 网络上。`docker compose down` 会因
   `network has active endpoints` 失败 —— 先 `docker stop apiduo-cn-mock`。
   正式上线前建议直接停掉；要留就先 `docker network connect` 到别的网络。
2. **`/opt/apiduo-cn/.env` 是 0 字节**。那是 compose 变量插值文件，不是应用 env；
   应用的 env 在 `/www/data/apiduo-cn/.env`。两份别混。
3. **不设 `TRUSTED_PROXIES` 是有意的**。nginx 在宿主机，设成回环会让 ClientIP 变内网地址，
   令牌 `allow_ips` 白名单直接失效。
4. **端口只能绑 `127.0.0.1`**。裸绑 `0.0.0.0` 后同私网主机可伪造 `X-Forwarded-For` 绕过白名单。
5. **healthcheck 里 CH 密码必须 `$$VAR`**。单引号会让变量不展开，CH 永远 unhealthy，
   进而卡死应用的 `depends_on`。
6. **CH 的 config.d 必须挂目录不能挂单文件**。单文件绑 inode，`sed -i` 换文件后容器里还是旧内容，
   且无任何报错。
7. **`LOG_SQL_DSN` 同一条串被两个解析器读**：native 认 `?database=`，GORM 只认路径段。
   写错会"日志页空白但库里有数据"。0.0.117 起启动会校验并直接报错。
8. **nginx 配置在 `http` 块内 include**，`limit_req_zone` / `ssl_session_cache` 的名字是全局的，
   必须带 `_cn` 后缀，否则与同机其他站点重名导致 `nginx -t` 失败。

## 七、安全面（已核查，2026-11-12）

- `/.env`、`/.git/config`、`/one-api.db` 之类路径返回的都是前端 `index.html`（SPA fallback），
  **不是文件泄漏**。
- 管理 API（`/api/channel/`、`/api/option/`、`/api/user/`）无鉴权访问全部 401，鉴权正常。
- `/api/setup` 无鉴权且暴露 `root_init`，属必需接口；**每次重建库后要复查它为 false**。
- 背景扫描噪音很大（`.git/config` 79 次、`.env` 37 次），均为扫描器，可忽略。

## 八、待办

- [ ] **异地备份**：同机备份不防盘坏。有 MinIO（`apiduo-langfuse-minio`），可每日上传一份
      `mysql.sql.gz`（62K，成本可忽略）。需要单独的桶与凭据。
- [ ] **告警**：目前只有 `check-dashboard-freshness.sh`（每 5 分钟）和
      `check-log-pipeline.sh`（未装 cron）。缺"站点 5xx 率 / 磁盘 / 备份失败"的主动告警。
- [ ] **限流口径统一**：nginx 边缘 `600r/m/IP burst=120`，应用层 `600000/180s`，
      两者差三个数量级。上线前需按真实流量形态定一个口径（见第九节）。
- [ ] **Chrome/agent 握手 429**：`CRITICAL_RATE_LIMIT` 的 key 是 `"CT"+ClientIP`，
      登录 / refresh / 2FA / OAuth / agent 握手共用同一个计数器，会互相打满。

## 九、明天上线前需要拍板的两个数

1. **nginx 边缘限流**：现在 `600r/m/IP`（= 10 rps）。
   - 面向浏览器用户：够用（一屏请求约 30-50 个，`burst=120` 能接住）。
   - 面向 API/agent 客户端：单 IP 10 rps 极易打满，且 ClientIP 若被 NAT 聚合，
     整栋楼共享 600r/m。实测 20 万请求里已有 325 个 429。
2. **`CRITICAL_RATE_LIMIT=30/1800s`**：登录、refresh、2FA、agent 握手共用，
   平均 1 次/分钟。正常用户刷新几次控制台就可能被锁 30 分钟。
