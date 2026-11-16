# Langfuse ClickHouse 写入调优

一次真实故障的定位与处置，可直接复用的部署配置。

## 这份东西解决什么问题

`jieapi.com` 灰度从 10% 放到 100% 后，链路采样率从"十分之一流量"变成"全量"，
Langfuse 的 ClickHouse 立刻顶到内存上限：

| 项目 | 数值 |
|---|---|
| CH 容器内存上限 | 2 GiB |
| 实际占用 | 1.883 GiB / 2 GiB |
| worker `memory limit exceeded` | **84 次 / 5 分钟**（约 17 次/分） |
| CH CPU | 177% |
| `events_full` 体积 | 2.99 GiB / 11,134 行 ≈ **270 KB/行** |
| 单次 flush 申请内存 | 1.25 GiB |

根因不是并发，是**单批写入的字节量**：内容体是 MB 级，一批攒几百行就是 1 GB 以上，
而 `max_server_memory_usage = 0`（自动）按 0.9 × cgroup 算出来只有 1.8 GiB。

## 改动一：内存上限 2g → 8g

`docker update` 可以**在线**改 cgroup，不需要重启：

```bash
docker update --memory 8g --memory-swap 16g langfuse-langfuse-clickhouse-1
```

CH 26.3 的 memory worker 会读 cgroup，`CGroupMemoryTotal` 立刻变成 8 GiB，
内存实际用到 3.69 GiB（已超过旧的 1.80 GiB 硬限），OOM 归零。

注意两个坑：

- `SYSTEM SET max_server_memory_usage = ...` **不存在**，语法层面就没这个命令；
  自动值只能靠 cgroup 变化 + `SYSTEM RELOAD CONFIG` 重算。
- `docker update` 只改运行时。compose 里写死的 `mem_limit: 2g` 会在下次 recreate 时覆盖回来，
  必须同时改 compose（见下方 override）。

## 改动二：写入参数（users.d profile，在线生效）

`clickhouse` 用户用的是 `default` profile，所以直接扩展该 profile 即可覆盖到它：

| 参数 | 默认 | 调整后 | 作用 |
|---|---|---|---|
| `async_insert_max_data_size` | 10 MB | **2 MB** | 单次 async flush 的数据量上限 |
| `max_insert_block_size` | 1,048,576 | **65,536** | 限制单个 insert block 的解析内存 |
| `max_bytes_before_external_sort` | 0（禁用） | **256 MB** | 排序溢写磁盘，而不是 OOM |
| `max_bytes_before_external_group_by` | 0（禁用） | **256 MB** | 聚合溢写磁盘 |
| `max_threads` | auto(48) | **8** | 降低并行查询的内存峰值 |
| `max_memory_usage` | 0（等于服务端上限） | **5 GB** | 单查询硬顶，避免一条查询吃光全局 |

部署（不动容器）：

```bash
docker cp deploy/langfuse/clickhouse/20-langfuse-tuning.xml \
  langfuse-langfuse-clickhouse-1:/etc/clickhouse-server/users.d/
docker exec langfuse-langfuse-clickhouse-1 clickhouse-client -q "SYSTEM RELOAD USERS"
```

验证（必须是**新连接**，老会话保留旧值）：

```bash
docker exec langfuse-langfuse-clickhouse-1 clickhouse-client -q "
SELECT name, value FROM system.settings
WHERE name IN ('async_insert_max_data_size','max_insert_block_size',
               'max_bytes_before_external_sort','max_bytes_before_external_group_by',
               'max_threads','max_memory_usage')
ORDER BY name FORMAT TSV"
```

## 持久化

`docker cp` 进去的文件在容器 recreate 后消失，所以必须落到 compose：

```yaml
  langfuse-clickhouse:
    mem_limit: 8g
    memswap_limit: 16g
    volumes:
      - /www/docker/langfuse/clickhouse:/var/lib/clickhouse
      - /opt/langfuse/clickhouse-tuning/20-langfuse-tuning.xml:/etc/clickhouse-server/users.d/20-langfuse-tuning.xml:ro
```

`docker-compose.override.yml` 就是这两段，直接叠加到 `/opt/langfuse/docker-compose.yml`。

## 没有改的部分

- **链路采样保持 100%**（`LANGFUSE_SAMPLE_RATE=100`），每个请求都进 Langfuse。
- **内容采样保持 10%**（`LANGFUSE_CONTENT_SAMPLE_RATE=10`），未降采样，也未加 body 截断。

如果将来内存再次吃紧，第一优先级是把 `LANGFUSE_CONTENT_SAMPLE_RATE` 往下调——
单行体积是线性的，比调 CH 参数更有效。

## 排障备忘

- `langfuse.traces` / `langfuse.observations` 是 **v4 之前**的表，v4 已移除对应 API
  （返回 `This endpoint is not available on deployments running in Langfuse v4 events_only`），
  两张表为 0 行是**正常**的，数据全在 `events_*`。
- `start_time` 存 **UTC**，而 `now()` 返回容器本地时间（`TZ=Asia/Shanghai`）。
  用 `start_time > now() - INTERVAL 5 MINUTE` 会**静默返回 0 行**，必须写显式 UTC 时间。
- worker 日志里的 `memory limit exceeded` 才是真正的健康指标，`docker stats` 的内存数字会骗人。

---

# 第二套实例：langfuse.apiduo.cn（core-1）

独立全栈（web / worker / ClickHouse 26.8 / Postgres 16 / Redis 7 / MinIO），
compose 在 `/www/docker/langfuse/docker-compose.yml`，入口经 nginx `langfuse.apiduo.cn` → `127.0.0.1:3100`。

## nginx 四项优化（已上线，reload 生效）

原配置有两个实质问题：`proxy_set_header Connection "upgrade"` 对**每个**请求都发 upgrade，
上游连接无法复用；静态资源既没长缓存也没 gzip。

| 改动 | 原因 | 实测 |
|---|---|---|
| `upstream langfuse_upstream` + `keepalive 32`，普通请求 `Connection ""` | 复用上游连接 | 50 次请求后到 3100 的连接数仍是 **2**（改前每个请求一条） |
| `location ^~ /_next/static/` 单独处理 | Next.js 产物是内容哈希命名 | `Cache-Control: public, max-age=31536000, immutable` + `content-encoding: gzip` |
| `proxy_hide_header Cache-Control` 后再 `add_header` | `expires` 和 `add_header` 会产出两个 Cache-Control | 现在只有一个头 |
| `add_header Strict-Transport-Security` | 没有 HSTS | 首页响应带 `max-age=31536000` |

`/api/public/otel/` 单独给 location（不缓冲、60s 超时、100m body），其余保持不缓冲。

配置见 `nginx/langfuse.apiduo.cn.conf`。

## ClickHouse 调优的差异

这套的 CH 用户叫 `langfuse`，`access_management=1`，但用户定义在 `users.d/*.xml` 里，
`ALTER USER` 会报 `Cannot update user ... in users_xml because this storage is readonly`，
所以同样走 **扩展 `default` profile + `SYSTEM RELOAD USERS`**。

参数与第一套一致，唯一差别是 `max_threads: 4`（该容器 `cpus: 4.0`，设 8 没有意义）。
文件：`clickhouse/20-langfuse-tuning-core1.xml`，线上已挂载
`./clickhouse/users.d/20-langfuse-tuning.xml`，recreate 不会丢。

## 版本与 v4 write mode（已确认是正式版）

镜像固定 tag，不是 latest / rc / beta：

| 项 | 值 |
|---|---|
| web | `langfuse/langfuse:4.80.0` |
| worker | `langfuse/langfuse-worker:4.80.0` |
| 运行版本 | `GET /api/public/health` → `"version": "4.80.0"` |
| 发布状态 | v4.80.0 是 GitHub 正式 release（2026-11-13 10:32 UTC），非 pre-release；本机镜像构建于同日 10:39 UTC |

A 实例用的是滚动 tag `langfuse/langfuse:4`，会在无人察觉时跳到 4.81+；B 用固定 patch tag，可复现。

### 关于三个 `LANGFUSE_MIGRATION_V4_*`：都是 v4 默认值，不是"预览模式"

变量名里的 `PREVIEW` 是 v3→v4 迁移期的历史遗留（当时用于控制 UI 里那个 "v4 preview" 开关），
**不表示实例跑在预览版上**。官方对三者的定义：

| 变量 | 现值 | 是否为 v4 默认 |
|---|---|---|
| `LANGFUSE_MIGRATION_V4_WRITE_MODE` | `events_only` | 是。含义 "Fully on v4. All users see the v4 experience." |
| `LANGFUSE_MIGRATION_V4_NATIVE_OTEL_BEHAVIOUR` | `direct` | 是 |
| `LANGFUSE_MIGRATION_V4_ALLOW_PREVIEW_OPT_IN` | `true` | 是，且**在 `events_only` 下必须为 true** |

`events_only` 时 v4 是唯一读路径，没有 v3 可切，所以这个开关实际不产生分支；
写成 `false` 会被启动校验拒绝（`events_only` + opt-in=false 是非法组合，容器起不来）。
三行显式声明等价于 v4 默认，删掉行为也一样——但显式写可以防止将来默认值漂移时静默改变模式。

`dual` 才是**从 v3 迁移**的中间态：每个事件同时写旧表 + staging，由 worker 做属性传播，
官方明确说写入量和存储翻倍、旧 SDK 数据约 15 分钟延迟。这套是全新部署、无历史数据要迁，
2026-11-14 已从 `dual` 切到 `events_only`（重建 web + worker），`traces`/`observations`
两张表只剩 dual 期间留下的 7 / 5 条测试数据，`events_core` 与 `events_full` 行数一致，
证明当前是单写路径。

代价是 v3 的 `/api/public/traces`、`/api/public/observations` 按设计返回 404
（`This endpoint is not available on deployments running in Langfuse v4 events_only`），
读历史需走 v2 events API 或回 A 实例。

## 两套实例：2026-11-14 03:07 起 B 为主

| 实例 | 地址 | 主机 | 现状 |
|---|---|---|---|
| A | `newapi-bff.oneapis.cn:8443` | apps | 历史数据 1.1 万条，**已停用**（apiduo 不再写入），等保留期结束下线 |
| B | `langfuse.apiduo.cn` | core-1 | **主实例**，apiduo 指向它 |

切换做法（零失败请求）：

1. 把 canary 的 map 目标临时改成 `127.0.0.1:3001`（注意 `split_clients` 不接受 `0%`，会报
   `invalid percent value "0%"`），reload，让流量全部回到 legacy；
2. 用新 env 文件重建 `apiduo-canary`，等 `/api/status` 返回 200；
3. map 改回 `127.0.0.1:3002`，reload。

切换后 B 的 `events_core` 立即出现 `environment='jieapi-com'` 的事件，模型分布与实时流量一致。

apiduo 侧的变量模板见 `apiduo-langfuse-b.env.example`，线上文件是
core-1 的 `/www/docker/apiduo-langfuse-b.env`（600，含真实 pk/sk，不入库）。

## ⚠️ 待决策：两套实例并存（已决策，保留供回溯）

## 旧决策记录（已执行，保留供回溯）

切换前的状态：A 上有 1.1 万条事件（`jieapi-com` / `newapi-bff`），B 全新。
历史 trace 不迁移，留在 A 上；A 建议保留 7 天后下线。
`/api/public/traces` 在 B（events_only）已不可用，需要读历史只能去 A 或走 v2 events API。

## 一个隐蔽的坑：web/worker 永远 `health: starting`

镜像自带健康检查：

```
wget --no-verbose --tries=1 --spider http://localhost:3000/api/public/health
```

但 Next.js standalone 的 `server.js` 会用 **`HOSTNAME` 环境变量**作为监听地址，
而 Docker 把 `HOSTNAME` 设成容器 ID。结果进程只绑定容器 IP：

```
/proc/net/tcp:  060015AC:0BB8  →  172.21.0.6:3000
```

`127.0.0.1:3000` 和 `localhost:3000` 都没有监听，健康检查 `rc=4`（network failure），
容器就一直显示 `health: starting`。后果：

- `depends_on: {condition: service_healthy}` 会永远卡住；
- 监控/编排把实例当成未就绪。

修法（一条环境变量，标准 k8s 部署也这么写）：

```yaml
    environment:
      HOSTNAME: "0.0.0.0"
```

## 需要重建容器的三项（约 20–30s 不可用）

| 改动 | 收益 | 风险 |
|---|---|---|
| `LANGFUSE_MIGRATION_V4_WRITE_MODE: dual → events_only` | 写入量和存储减半，去掉 staging 与属性传播任务 | 移除 `/api/public/traces` 等废弃接口 |
| `HOSTNAME: "0.0.0.0"`（web + worker） | 健康检查真正变绿 | 无 |
| `NODE_OPTIONS: --max-old-space-size=3072` | 4G 容器内给 Node 合理堆上限 | 无 |

三项都是改 compose 后 `docker-compose up -d langfuse-web langfuse-worker`，一次性完成。
