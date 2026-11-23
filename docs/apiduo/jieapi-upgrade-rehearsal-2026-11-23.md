# jieapi.com 升级预演：0.0.196 → 0.0.214

> 2026-11-23 22:02–22:08 CST。全程在 core-1 上做，**生产容器、nginx、生产库一个字节没动**：
> 排练实例连的是孤立库（MySQL `jieapi_apiduo_rehearsal` / CH `new_api_logs_rehearsal` / Redis `db9`），
> 端口 3005，未产生任何一次上游生成请求。

## 0. 结论

1. **迁移是纯增量的**：MySQL 50 → 53 表，只新增 `billing_statements` / `billing_statement_lines` /
   `billing_statement_exports`；既有 50 表的列级 diff **为空**（无类型、默认值、可空性变化）；ClickHouse 无结构变更。
2. **启动 0.6 s 完成**（含迁移），重建到可用 **0.8 s**，优雅停止 **0.2 s**。
3. **回滚成立**：0.0.196 在 0.0.214 迁移过的库上 1 s 起、`/api/status` 与 `/api/pricing` 200，
   53 张表保留、行数不变，旧版本不会删新表。
4. 正式升级的真实代价仍**只有重建窗口内的 POST 502**（与 11-22 那次同源），详见 §7。

## 1. 预演环境

| 依赖 | 生产 | 预演 |
|---|---|---|
| MySQL | `jieapi-master` | `jieapi_apiduo_rehearsal`（`mysqldump --single-transaction` 副本） |
| ClickHouse | `new_api_logs` | `new_api_logs_rehearsal`（`CREATE TABLE … AS` 仅结构） |
| Redis | `db8` | `db9`（预演前 dbsize=0） |
| Langfuse | `langfuse.apiduo.cn` | **不挂 env 文件**，`export_host` 为空 ⇒ 不导出 |
| 端口 | 3002 | 3005（3004 被 `stask-taskiq-admin-1` 占用） |
| 角色 | `NODE_TYPE=master` | master（**迁移只在 master 跑**，见 §6 坑 1） |
| 资源 | 12C/12G | 4C/4G，`SQL_MAX_OPEN_CONNS=16` |
| 数据卷 | `/www/docker/apiduo/{data,logs}` | `/www/docker/apiduo-rehearsal/{data,logs}` |

环境文件 `app-0214.env` 由生产 `app.env` 派生，只改三个连接串（库名 ×2、Redis db 8→9），
`CRYPTO_SECRET` / `SESSION_SECRET` 原样保留 —— 否则 213 个渠道密钥在副本里解不开。

## 2. 实测时间线

| 时刻 | 动作 | 结果 |
|---|---|---|
| 22:02:36 | `mysqldump` 生产库 | **5.0 s**，60 MB gz，`gzip -t` 通过 |
| 22:02:49 | 恢复到排练库 | **24.0 s**，50 表，行数与生产快照逐项相等 |
| 22:03:20 | 拉 `0.0.214` 镜像 | 4 s（层复用），249 MB |
| 22:03:48 | 首次启动（**slave**） | 2 s health，版本 0.0.214，**但没有迁移**（见 §6 坑 1） |
| 22:05:27 | 改 master 重启 | `database migration started` → 22:05:28 ready，**表 50→53** |
| 22:05:51 | 冒烟探针 | 全绿（§3） |
| 22:05:5x | 停 0.0.214、起 0.0.196 | 1 s health，回滚读路径全通（§4） |
| 22:07:27 | 0.0.214 二次启动 | 0.8 s 到可用；迁移幂等，无报错、表数不变 |
| 22:08 | 清理容器 | 生产 master 仍 `0.0.196 / Up 13 hours (healthy)` |

快照基线：users 4,756 / tokens 5,410 / channels 213 / tasks 7,749 / options 119 / quota_data 426,001。
迁移后逐项相等。

## 3. 冒烟结果（0.0.214，零成本）

| 探针 | 结果 |
|---|---|
| `/api/status` | 200，`version=0.0.214`，`system_name=接JieAPI` |
| `/api/pricing` | 200 |
| `/v1/models`（伪造 key） | 401 JSON，`Invalid token`（本地拒绝，未出网） |
| `/v1/chat/completions`（伪造 key） | 401 JSON（未出网） |
| `/api/billing/statements` | 401 JSON `AUTH_UNAUTHORIZED`（**路由存在**，非 404 HTML） |
| `/api/billing/statement?period=2026-10` | 401 JSON |
| `/` （`Host: jieapi.com`） | 200 HTML |
| 响应头 | `X-New-Api-Version: 0.0.214` |

启动告警只有三条已知项，与生产 0.0.196 完全一致：`SESSION_COOKIE_SECURE` 未设、
`TRUSTED_PROXIES` 未设、1 个令牌限制来源 IP。

一条 `SLOW SQL`：增长看板的 `quota_data` 聚合 525 ms，只在 master 启动时跑一次，量级可接受。

## 4. 回滚实测（L1 代码回滚）

| 项 | 0.0.196 on 已迁移库 |
|---|---|
| 启动 | 1 s healthy |
| `/api/status` | 200，`version=0.0.196` |
| `/api/pricing` | 200 |
| `/v1/models` | 401 |
| `/api/billing/statements` | **404**（旧版本没这条路由，反证 §3 的 401 是新路由） |
| 表数与行数 | 53 表保留，users/tasks 行数不变 |

结论：**迁移不阻塞回滚**。旧代码只是不认识三张新表，不读也不删。

## 5. 迁移差异（0.0.196 → 0.0.214，31 个提交）

| 层 | 变化 |
|---|---|
| MySQL | +3 表：`billing_statements`（period/user_id/period_start/period_end/total_quota/total_amount/currency/requests/refunds/errors/settle_rate/quota_per_unit/status/issued_at/due_at/username/created_at/updated_at）<br>`billing_statement_lines`（按请求一行明细）<br>`billing_statement_exports`（object_key/projection/rows/bytes/sha256） |
| MySQL | 既有列**零变更** |
| ClickHouse | 无。`use_time_ms` 是 0.0.195 引入的，本站已有 |
| options | 无迁移。`MigrateRetiredFrontendOptions` / `MigrateSensitiveWords` 已在 0.0.196 跑过 |
| 前端 | 新增 `/billing` 路由与分页组件；浏览器需重新拉 `index.html` |
| 环境变量 | **无新增必填项**。导出目录默认 `data/exports`，已在 `/data` 卷内 |

对照 11-22 的 0.0.121→0.0.195（41→50 表 + CH `use_time_ms` + 文件用途迁移），本次轻一个数量级。

## 6. 两个必须记住的坑

1. **slave 节点不跑迁移**。`model/main.go` 的 `InitDB()` 在 `!common.IsMasterNode` 时直接 return，
   所以用 `NODE_TYPE=slave` 起的排练实例永远看不到 AutoMigrate。
   预演迁移必须 `NODE_TYPE=master`，且 `SQL_DSN` / `LOG_SQL_DSN` / `REDIS_CONN_STRING`
   三处**全部**指向副本，否则就是拿生产库练手。
   本次 master 预演期间实际会跑起来的东西已逐项核过：账单任务在 22:00 不在出账窗口内、
   `monitor_setting` 无 `auto_test_channel_enabled` 行（默认关）、`tasks` 全终态（无轮询）、
   无 codex / 插件渠道（无凭据刷新外呼）。
2. **`docker compose` 插件没装**。core-1 / core-3 上只有 standalone `docker-compose`（2.27.1）。
   照抄网上 `docker compose` 会报 `unknown shorthand flag: 'f'`。

## 7. 正式升级步骤单

### 7.1 窗口与前置

- 时间窗：**06:00–09:00** 或白天低峰。**不要选 00:10–06:00** —— 新版 master 启动即判断出账窗口，
  会立刻对 2026-10 做一次全站首次出账（`ListBillingCandidateUsers` 扫 CH `logs` 整月 + 逐用户生成
  xlsx），在升级窗口里叠加重 IO。若必须在窗口内升级，先接受这次回填。
- 前置：两节点 `docker ps` 均 healthy、MySQL 连接 < 300、`df -h /` 有 > 100 G 余量（现 1.2 T）。

### 7.2 core-1（master，先升，因为迁移在它身上）

```bash
# 1) 备份（约 5 s，60 MB）
TS=$(date +%Y%m%d-%H%M%S); D=/www/backup/jieapi-upgrade/pre-0214-$TS; mkdir -p $D
# 连接串从 app.env 取，命令同 2026-11-22 那次：mysqldump --single-transaction --quick --routines --triggers | gzip
cp /www/docker/apiduo/docker-compose.yml $D/
cp /www/server/panel/vhost/nginx/{jieapi.com.conf,api.jieapi.com.conf,0.apiduo-canary.conf} $D/

# 2) 改 tag（镜像已在本地：docker images | grep 0.0.214）
cd /www/docker/apiduo
sed -i 's#ghcr.io/apiduo/apiduo:0.0.196#ghcr.io/apiduo/apiduo:0.0.214#' docker-compose.yml
docker-compose config -q && echo schema_OK

# 3) 重建（预期 0.2 s 停 + 0.8 s 起到可用）
docker-compose up -d apiduo
curl -s -m 5 http://127.0.0.1:3002/api/status | python3 -c "import sys,json;print(json.load(sys.stdin)['data']['version'])"
docker logs --since 2m jieapi-apiduo 2>&1 | grep -iE 'migrat|error|panic'
```

验证：版本 0.0.214；`billing_statements` 等 3 表存在；`grep -c 'database migration started'` = 2（主库 + 日志库）；
无 panic；三条已知配置告警仍在。

### 7.3 core-3（standby，后升，**不跑迁移**）

```bash
cd /www/docker/apiduo
sed -i 's#ghcr.io/apiduo/apiduo:0.0.196#ghcr.io/apiduo/apiduo:0.0.214#' docker-compose.yml
docker-compose up -d apiduo-standby
curl -s -m 8 http://127.0.0.1:3003/api/status | head -c 80   # 跨 137 ms 回库，约 +300 ms
```

顺序不能反：先升备节点会让主站 failover 到未验证版本。备用节点是 slave，先升也不会抢后台任务。

### 7.4 502 窗口

11-22 的实测是 **9 秒窗口内 8 条 POST 502**，根因是 nginx 默认只对幂等请求重试
（`proxy_next_upstream error timeout` 不含 `non_idempotent`），重建期间 POST 不会切到 core-3 备节点。
本次重建实测只有 0.2 s + 0.8 s，但**性质不变**。三个选项：

| 方案 | 代价 | 建议 |
|---|---|---|
| 直接重建，接受少量 502 | 低峰约 3~8 条 POST 502 | **默认选它** |
| 先在 upstream 给 3002 加 `down` → reload → 重建 → 去掉 `down` → reload | 两次 reload，期间全站走 core-3（+600 ms、上限 210 req/s） | 要求零 502 时用 |
| 加 `non_idempotent` | POST 会重放到另一个后端，计费/任务端点有重复提交风险 | 不要用 |

### 7.5 回滚

| 级别 | 动作 | 实测耗时 |
|---|---|---|
| L1 代码 | `sed` 把 tag 改回 `0.0.196` → `docker-compose up -d apiduo` | 0.2 s 停 + 1 s 起（已在排练库验证） |
| 只回滚单节点 | 停掉该节点，nginx 的 `backup` 自动接管 | 秒级 |
| 数据 | 不需要。三张新表留在库里，旧版本不读不删 | — |

镜像 `0.0.196` 两台机器本地都在，不需要重新拉。

## 8. 预演未覆盖的部分

1. **真实 relay 200 流量与计费写入**：按零成本纪律没有发任何上游生成请求，`/v1` 只验证了 401 路径。
2. **账单 SQL 的完整执行**：排练 CH 是空表，`JSONExtractInt(other,…)` 之类只在解析层被校验，
   未验证 21 M 行上的实际耗时。首次出账的实际时长要等正式升级（或单独跑 `RunBillingIssueForPeriod`）。
3. **Web 控制台的交互**：只验证了首页 200 与路由存在，没做登录后的页面走查（需要密码/2FA）。
   建议升级后在浏览器里走一遍 `/billing`，重点看横向滚动与分页。

## 9. 残留

| 项 | 位置 | 处置 |
|---|---|---|
| 生产库快照 | `/www/backup/jieapi-upgrade/rehearsal-20261123-220236/mysql.sql.gz`（60 MB） | 保留到正式升级后 |
| 排练库 | MySQL `jieapi_apiduo_rehearsal`（383 MB，53 表） | 正式升级后可 DROP |
| 排练 CH | `new_api_logs_rehearsal`（空表） | 可 DROP |
| 镜像 | `ghcr.io/apiduo/apiduo:0.0.214`（249 MB） | **保留**，正式升级免拉取 |
| 环境文件 | `/www/docker/apiduo-rehearsal/app-0214.env`（600） | 保留，再预演直接复用 |
| Redis db9 | 2 个键（排练会话/缓存） | 可 FLUSHDB |
