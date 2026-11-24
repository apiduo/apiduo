# jieapi.com 升级 0.0.196 → 0.0.226 执行记录

- 执行时间：2026-11-24 09:38–09:46（CST）
- 目标版本：`ghcr.io/apiduo/apiduo:0.0.226`（digest `sha256:fcc4d74e…c334b8`）
- 结果：**成功**。core-1（master）与 core-3（slave）均为 0.0.226 healthy，nginx 已恢复原状。
- 回滚锚点：镜像 `0.0.196` 仍在两节点本地；compose/env/nginx/容器定义全部备份在
  `/www/backup/jieapi-upgrade/pre-0226-20261124-093914/`（core-3 对应 `pre-0226-core3-*`）。

## 一、前置备份

| 项 | 结果 |
|---|---|
| MySQL 快照 | `mysqldump --single-transaction` → `mysql.sql.gz` 60 MB，4.99 s，gzip 校验通过 |
| 配置 | `docker-compose.yml`、`app.env`、`0.apiduo-canary.conf`、`docker inspect` 输出 |
| 基线读数 | 50 表 / users 4757 / tokens 5412 / channels 213 / tasks 7799 / options 119 / quota_data 426545 |
| 磁盘 | core-1 `/` 剩 1.2 TB（68% 已用）；core-3 剩 909 GB |

## 二、零停机的切流方式

站点只有 ~43 req/min、POST 占多数，nginx 默认不对 POST 重试，直接重建会掉几条请求。
所以按预演报告里的方案做两次 reload：

```
09:40:4x  给 upstream 的 127.0.0.1:3002 加 down → nginx -t → nginx -s reload
          新请求全部落到 61.111.253.243:3003（core-3，此时仍是 0.0.196）
          canary 日志的 upstream_addr 列确认切换生效
09:40:47  docker stop -t 45 jieapi-apiduo   → 45.4s 后被 SIGKILL（exit 137，有长流没退完）
09:41:33  docker-compose up -d apiduo       → 2s 后 /api/status 200
09:41:47  去掉 down → nginx -t → reload     → 流量回到 127.0.0.1:3002
09:42:5x  core-3 改 tag → docker-compose up -d → 37s healthy（跨机 MySQL，AutoMigrate 慢）
```

关键点：core-3 的三个连接串都指向 core-1（`104.238.221.50`），所以切流期间会话、限流、
渠道缓存全部共享，不存在「切换即掉登录」的问题。

## 三、迁移结果

| 项 | 升级前 | 升级后 |
|---|---|---|
| 表数量 | 50 | **53**（+`billing_statements` / `billing_statement_lines` / `billing_statement_exports`） |
| `billing_statement_lines.endpoint` | —（表不存在） | **varchar(191)**，正是 0.0.223 修的那一版 |
| users / tokens / channels / options | 4757 / 5412 / 213 / 119 | 完全一致 |
| tasks | 7799 | 7801（正常运行增长） |
| quota_data | 426545 | 426567（正常运行增长） |
| `billing_statements` | — | 0 行（今晚首次出账） |

启动日志里 `database migration started` 出现两次（业务库 + 日志库），无 error / panic。
`endpoint` 已是 191，说明**不需要**再跑 ALTER 分支 —— 新表直接按 0.0.223 的 tag 建出来。

## 四、验证

| 项 | 结果 |
|---|---|
| `X-New-Api-Version` | `0.0.226`（jieapi.com 与 api.jieapi.com 双入口） |
| `/api/status`、`/api/pricing` | 200，本机 p50 0.42 ms / 0.71 ms |
| `/v1/models`、`/v1/chat/completions`（伪造 key） | 401 JSON，本地拒绝未出网 |
| `/api/billing/statements`、`POST /api/billing/issue`（无鉴权） | 401 JSON（路由存在，不是 SPA 的 404） |
| 真实用户流量 | 切回后持续 200；应用日志可见 `record consume log` 与 `tiered_expr` 计费正常 |
| ClickHouse 日志写入 | 最近 3 分钟 55 行 / 6 用户，未中断 |
| core-3 角色 | `billing task skipped on non-master node`、`growth tasks skipped on non-master node` |
| 容器 | core-1 restarts=0 health=healthy mem 129.7 MB；core-3 mem 123.8 MB |
| autoheal | Up 8h，仍在监控新容器（连续 3 次健康检查失败才会重启） |

## 五、影响面（如实记录）

只有 **4 条 502** 是升级造成的，占当日 ~3,400 条请求的 0.12%：

| 时间 | upstream | 原因 |
|---|---|---|
| 09:41:23 ×2 | 127.0.0.1:3002 | 旧容器已经关闭监听、正在排空连接，客户端 keepalive 连接上的新请求被拒 |
| 09:42:44 ×2 | 61.111.253.243:3003 | nginx reload 前的旧 worker 仍按 `down` 的配置把请求转到 core-3，而 core-3 正在重建 |

当天其余 159 条 502 都发生在升级前（0.0.196 时期，按分钟零散分布），属上游供应商抖动，
与本次升级无关。另外 `docker stop` 45 s 后被 SIGKILL，说明有 1 个长流式请求被截断 ——
这类长连接在重建时无法保住，只能靠客户端重试。

> 下次改进：恢复 nginx 后**等 3 分钟**再动 core-3。这次的 09:42:44 两条就是 reload 后
> 57 秒就重建 core-3 撞上的（旧 worker 默认会一直服务已建立的连接）。
> 或者给 vhost 加 `worker_shutdown_timeout 30s`，让旧 worker 到点退出。

## 六、回滚方式（秒级）

```bash
# core-1
cd /www/docker/apiduo
sed -i 's#apiduo:0.0.226#apiduo:0.0.196#' docker-compose.yml
docker-compose up -d apiduo
# core-3
cd /www/docker/apiduo
sed -i 's#apiduo:0.0.226#apiduo:0.0.196#' docker-compose.yml
docker-compose up -d
```

0.0.196 在 53 表的库上实测可正常启动（预演结论），新增的 3 张 billing 表不影响旧版。
数据库整体回滚才需要 `mysql.sql.gz`（restore 约 20 s，会丢掉升级后的增量写入）。

## 七、今晚必须看一眼的事

升级后第一个 00:10–06:00 窗口，master 会对 **2026-10** 全量出账：
ClickHouse 里 2026-10 有 **41 个用户 / 4,225,396 行**（type=2/6）。
参照 11 月预演（34 用户 / 503 万行 → 4m16s、归档 549 MB），预计 **4–6 分钟、归档约 450 MB**。

```bash
# 出账是否跑过（成功/失败）
docker logs jieapi-apiduo 2>&1 | grep -E 'billing issued|billing build failed'
# 账单数量与状态
mysql -uroot -p -e "SELECT status, COUNT(*), SUM(requests) FROM \`jieapi-master\`.billing_statements WHERE period='2026-10' GROUP BY status"
# 归档文件
du -sh /www/docker/apiduo/data/data/exports/billing/2026-10
```

预期 `status='issued'` 41 条、`failed=0`。若有用户失败，不用等第二天：

```bash
TOK=$(curl -s -H 'Content-Type: application/json' -d '{"username":"<admin>","password":"<pwd>"}' \
  https://jieapi.com/api/user/login | jq -r .data.access_token)
curl -X POST -H "Authorization: Bearer $TOK" 'https://jieapi.com/api/billing/issue?period=2026-10'
curl -H "Authorization: Bearer $TOK" https://jieapi.com/api/billing/issue   # running/issued/skipped/failed
```

## 八、遗留

1. 出账失败**没有主动告警**，只在日志里留一行（`billing build failed`）。要接通知通道需先定通道。
2. 预演环境仍在：core-1 上 `apiduo-rehearsal`（127.0.0.1:3005）+ MySQL
   `jieapi_apiduo_rehearsal` + ClickHouse `new_api_logs_rehearsal`（5.03 M 行）。
   已确认不影响生产（独立库、独立端口、`--restart=no`），但占约 2.5 GB 磁盘和 4 核配额。
3. `billing_statement_lines` 的金额口径差（B3，3.2e-5 量级）保持原状，属产品决策。
