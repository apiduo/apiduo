# ClickHouse 系统日志表治理

一套可直接部署到新机器的配置 + 脚本，用来防止 `system.*_log` 表无限增长。

## 这份东西解决什么问题

一次真实故障的数字：

| 项目 | 数值 |
|---|---|
| 某实例 `system` 库占用 | **60.31 GiB** |
| 同实例业务数据（`logs` + `audit_logs`） | **976 KiB** |
| `trace_log` | 46.83 GiB / **22.6 亿行** |
| `text_log` | 12.22 GiB / 1.5 亿行 |
| 磁盘 | 180 G → 106 G（清理后释放 74 G） |
| load average（64 核） | 110 → 17.9 |

也就是说：**磁盘和 merge CPU 有 99.8% 花在没人看的数据上。** 而且它静默增长了 10 天才被发现——磁盘只用了 20%、CPU 还有 82% idle、`docker stats` 看着也正常。

## 根因（三层，缺一不可）

**第一层：默认定义里没有 TTL。**

ClickHouse 25.x 的 `config.xml` 中，这些表长这样：

```xml
<trace_log>
    <database>system</database>
    <table>trace_log</table>
    <partition_by>toYYYYMM(event_date)</partition_by>
    <flush_interval_milliseconds>7500</flush_interval_milliseconds>
    <max_size_rows>1048576</max_size_rows>   <!-- 极具迷惑性 -->
</trace_log>
```

`max_size_rows` 是**内存缓冲上限**——缓冲攒满 100 万行就落盘，落盘后继续无限增长。它不限制磁盘占用。名字暗示有上限，实际只决定什么时候刷盘。

**第二层：数据量级被低估。** 真正的大户不是 query profiler，而是**内存采样**：

```
trace_type 分布：Memory 161350 / MemoryPeak 161339 / Real 8 / CPU 11
query_id 样本：ad97d399-...::202611_1_9714_207   ← 后缀是 merge 任务的 part range
```

默认 `total_memory_profiler_step = 4194304`，即每分配 4 MiB 记一次调用栈。后台 merge 大 part 时会触发**上百万次**。所以 merge 越猛、trace 涨得越快，与查询量无关。

**第三层：`text_log` 默认级别是 `trace`。** 会把 merge 内部细节（`Selected 1 merge ranges` 之类）全部记录下来——实测 1.5 小时 239 万行，其中 Debug + Trace 占 94%。

## 部署（新机器三步）

```bash
# 1. 把 kit 放到固定路径再安装 —— cron 会写入脚本的绝对路径
sudo mkdir -p /opt/clickhouse-kit && sudo cp -r deploy/clickhouse/* /opt/clickhouse-kit/

# 2. 安装
sudo /opt/clickhouse-kit/install.sh \
    --container apiduo-cn-clickhouse \
    --config-root /www/data/apiduo-cn/clickhouse-config \
    --compose-file /opt/apiduo-cn/docker-compose.apiduo-cn.yml \
    --service apiduo-cn-clickhouse \
    --patch-compose --drop-legacy

# 3. 看自检输出（脚本最后会自动跑一次）
#    期望：OK container=... TTL齐全且与config一致

# 密码从哪来：优先 $CH_PASSWORD，其次读容器的 CLICKHOUSE_PASSWORD 环境变量。
# 本机 default 用户无密码的场景留空即可。脚本不存密码。
```

脚本可重复执行：已存在的文件跳过（`--force` 覆盖），已是目录挂载则跳过重建，TTL 已是目标值则不 ALTER。

`install.sh` 的退出码：

| 码 | 含义 | 怎么办 |
|---|---|---|
| 0 | 完成 | — |
| 3 | 还没改成目录挂载 | 按提示改 compose，或加 `--patch-compose` |
| 4 | 已有别的 XML 管同一批表 | 见下节，先做选择 |

## 和已有配置共存（重要）

如果机器上已有 `zz-apiduo-cn.xml` 之类的文件也在定义这些表的 TTL，脚本会**拒绝安装并退出 4**：

> ClickHouse 按文件名字典序合并 `config.d`，同名元素**后者覆盖前者**。两个文件同时定义同一张表，会造成「改了却没生效」的静默故障——这类问题在现场极难定位。

两条路：

- **沿用现有文件**（已经治理过的机器）：不装 `logs.xml`，直接用 `check-tables.sh` 校验；要改数值就改现有文件再 `ALTER` 对齐。
- **换成这套模板**：先删掉现有文件里的对应块（保留 `max_server_memory_usage` 等实例级调优），再重跑。

`check-tables.sh` 也会检测这种重复定义并报 ALERT。

## TTL 取值

| 表 | TTL | 依据 |
|---|---|---|
| `trace_log` | 90 天 | profiler 关掉后日常零增长，只在人工抓调用栈时产生 |
| `text_log` | 180 天 | 级别降到 `information`，日增不到 1 MB |
| `query_log` | 365 天 | 只记 >1s 慢查询，日增 1~2k 行，排障最值钱 |
| `error_log` | 365 天 | 极少但价值最高 |
| `part_log` | 180 天 | 查「磁盘为什么涨」「part 为什么多」 |
| `processors_profile_log` | 180 天 | 算子级性能 |
| `query_metric_log` | 180 天 | 单查询内存 / IO |
| `metric_log` | 30 天 | **唯一有真实体量的表**，日增约 300 MB，30 天约 9 GB |
| `asynchronous_metric_log` | 90 天 | 压缩率极高，51 万行只占 400 KB |

稳态合计约 **14 GiB**。机器小的话按比例下调，用 `--ttl-metric` 等参数覆盖。

`metric_log` 只给 30 天不是保守——它每秒采一次，是唯一不适合「尽量大」的表。历史趋势交给 Prometheus 更合适。

## 验收

```bash
# 1. TTL 是否都在（应输出 9 行）
docker exec <容器> clickhouse-client --query \
  "SELECT name, extract(engine_full,'toIntervalDay\\(([0-9]+)\\)') FROM system.tables
   WHERE database='system' AND name LIKE '%_log' AND name NOT LIKE '%_0' ORDER BY name"

# 2. trace_log 是否停止增长（两次读数应相同）
docker exec <容器> clickhouse-client --query \
  "SELECT sum(rows) FROM system.parts WHERE active AND table='trace_log'"

# 3. text_log 是否只剩 information 及以上
docker exec <容器> clickhouse-client --query \
  "SELECT level, count() FROM system.text_log WHERE event_time > now()-300 GROUP BY level"

# 4. 巡检
./deploy/clickhouse/check-tables.sh <容器>
```

线上实测（配置生效后 3 分钟）：

| 表 | 变化 |
|---|---|
| `trace_log` | 1750 → 1750（**零增长**，此前 12 万行/分钟） |
| `text_log` | 2,401,239 → 2,401,256（**+17 行**，此前每分钟 12 万行） |

## 六个坑（都是实际踩过的）

**1. 单文件 bind mount 绑的是 inode。**
用 `sed -i` / `cp` / `git checkout` 换掉宿主机文件后，容器里仍然是**旧内容且没有任何报错**。表现是宿主机 XML 明明合法，`SYSTEM RELOAD CONFIG` 却报 `SAXParseException`。所以 `config.d` / `users.d` 必须**目录挂载**。

**2. `users.d` 不能挂只读。**
镜像 entrypoint 要按 `CLICKHOUSE_USER` / `CLICKHOUSE_PASSWORD` 生成 `users.d/default-user.xml`，`:ro` 会让容器进入 `Restarting` 循环，日志是 `Read-only file system`。`config.d` 可以 `:ro`。

**3. 目录权限必须 755。**
ClickHouse 进程以 `clickhouse` 用户运行，目录设成 700 会直接 `Permission denied ["/etc/clickhouse-server/config.d"]` 起不来。`users.d/default-user.xml` 用 644（镜像原本就是）。

**4. 改 config 不会重算已存在的表。**
ClickHouse 只在**建表时**读 config。所以必须「config 管未来 + ALTER 管现在」，两边数值一致。否则重启时它认为定义不符，把旧表 rename 成 `xxx_0` 再按 config 重建——`ALTER` 上去的 TTL 就是这么丢的。

**5. `total_memory_profiler_step` 只在启动时读入。**
`SYSTEM RELOAD CONFIG` 对它无效。`system.settings` 显示 profiler 为 0、但 `trace_log` 还在涨，就是这个原因——必须重启进程。

**6. `ALTER ... MODIFY TTL` 会触发全表 mutation。**
9351 万行的表直接 `MEMORY_LIMIT_EXCEEDED`，mutation 卡死还要 `KILL MUTATION`。正确顺序是**先 `TRUNCATE`（秒级、几乎不占内存）再对空表加 TTL**。`install.sh` 会在表超过 5000 万行时提示。

> 另外一个操作细节：`docker exec -i` 会接管 stdin。脚本用 `bash -s` 从 stdin 读入时，`-i` 会把后面的行一起吞掉，表现为「命令没输出、exit 0」。查 CH 一律用 `docker exec`（不带 `-i`）。

## 日常运维

**巡检**：`install.sh` 会装一条 root cron（每天 09:00）。判据是**「有没有 TTL」+「和 config 是否一致」**，不查「现在多大」——大小是结果，TTL 是原因；不看流量，所以空站和刚重建的实例也能正确判断。

```bash
# 手工跑一次（CONFIG_DIR 留空会自动从容器挂载推断）
./check-tables.sh <容器>

# 接告警
APIDUO_ALERT_WEBHOOK=https://... ./check-tables.sh <容器>
```

它会扫 `config.d` 下**所有** `*.xml`（文件名各家不同），并报四类问题：缺 TTL、与 config 数值不一致、同一张表被多个文件重复定义、`_0` 遗留表。

退出码：`0` OK / `1` ALERT / `2` 连不上。

**扩容**：换机器时按磁盘和内存调整 `--ttl-metric`、`--max-memory-usage`，其余保持。

**回滚**：`install.sh` 会把原 compose 备份成 `*.bak-<时间戳>`，`--force` 覆盖文件前也会备份到 `<config-root>/backup/`。回滚 = 换回备份 + `--force-recreate`。

## 文件说明

| 文件 | 作用 |
|---|---|
| `logs.xml` | `config.d` 覆盖：9 张表的 TTL、`text_log` 级别、关闭内存采样 |
| `profile.xml` | `users.d` 覆盖：关 query profiler、只记慢查询、单查询内存上限 |
| `install.sh` | 一键安装 / 就地修复 / 改挂载 / 对齐 TTL / 装巡检 |
| `check-tables.sh` | 巡检：缺 TTL、与 config 不一致、重复定义、超阈值、`_0` 遗留表 |

模板里的 `__TTL_*__` / `__MAX_MEMORY_USAGE__` 由 `install.sh` 渲染，已渲染的文件会被跳过（`--force` 覆盖）。
