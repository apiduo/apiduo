# 最近一周更新的网关性能影响评审（2026-11-13 → 2026-11-20）

判据日期 2026-11-20。基线 `42577be46`（2026-11-12），当前 `5f1e1a174`。
本地形态：MySQL 8.2 + ClickHouse 25.8 + Langfuse（`deploy/local/up.sh --mock` 同形态）。

## 一、结论

**没有发现性能回归。** 三组判据指向同一结论：

| 判据 | 基线 42577be46 | 当前 5f1e1a174 | 变化 |
|---|---|---|---|
| 分发器取 model（320B 体） | 1783 ns/op，17 allocs | 1352 ns/op，10 allocs | **−24%** |
| 分发器取 model（120KB 体） | 214.5 µs/op，17 allocs | 161.2 µs/op，10 allocs | **−25%** |
| 端到端 p50（50 rps 定速，小体） | 8.36 ms | 8.61 ms | +3%（在轮次噪声内） |
| 端到端 p50（20 rps 定速，120KB 体） | 13.11 ms | 11.22 ms | **−14%** |
| 每请求容器 CPU（小体） | 5722 µs | 5868 µs | +2.5%（单轮极差 ±7%，未见稳定差） |
| 每请求容器 CPU（120KB 体） | 17341 µs | 16448 µs | **−5%** |
| 每请求 SQL 条数 | 19.75 | 19.68 | 无新增 |
| 每请求路由判定（新端点注册表） | 1.69 ns | 8.59 ns | +6.9 ns，0 allocs |

唯一确定变慢的是路由判定：`endpoint.Match` + `LookupAsync` 两次短切片扫描 8.6 ns，
取代两条 `strings.HasPrefix` 的 1.7 ns。相对 8 ms 的请求耗时可忽略（0.0001%），且零分配。

## 二、方法

- 同一容器、同一 DB、同一假上游，**交替跑**变体：base → head → base → head，各两轮；
  60 s 测量期、5 s 预热、定速 50 rps（并发 8）。
- 大请求体场景：120 KB 消息体、20 rps（并发 4）、45 s。
- 假上游是 Go 写的零延迟 OpenAI 兼容端（`/v1/chat/completions`），排除 Python mock 的
  accept backlog 干扰（该 mock 在 8 并发下会自己排队到 2 s，足以掩盖被测差异）。
- CPU 取容器 cgroup `cpu.stat` 的 `usage_usec` 增量 ÷ 成功请求数；SQL 取 MySQL
  `Questions` 增量 ÷ 成功请求数。
- 二进制约 140 MB，`CGO_ENABLED=0 GOOS=linux GOARCH=arm64`，与 `up.sh` 同参数。

## 三、本周改动按热路径分类

### 3.1 在每请求热路径上（均已测）

| 改动 | 提交 | 判据 |
|---|---|---|
| 请求体解析改随机访问：`ValidReader` + `io.TeeReader` 数 `model` 键、`ReadBodyJSONFields` | 上游合并（11-13） | 唯一有量级意义的改动，**变快**：少 7 次分配、少 57% 分配字节、少 25% 时间 |
| 端点注册表 `endpoint.Match` / `LookupAsync` 取代路径 if 链 | 97bbad959 | +6.9 ns / 请求，零分配 |
| 上游额度水位观测 `service.ObserveRateLimit` | 97bbad959 | 无响应头即返回；命中时一次 map 写，无 IO |
| 日志新增 `use_time_ms`、`request_plugins`、`ip` 列 | 97bbad959 / 27f3787e8 | 复用既有插入，未新增语句；SQL/请求数不变 |
| `ApplyHeaderOverride` 透传请求插件头 | 上游合并 | 无插件效果时走原路径，仅多一次 `len()` 判断 |
| `ApplyUpstreamBodyMetadata` 为 BodyStorage 建独立游标 | 上游合并 | 每请求多一次 `NewReplayableBodyReader` 分配，量级同 io.NopCloser |

### 3.2 不在热路径上（无配置/无请求形态即空转）

- `relay/requestplugin/*`（16k 行，上游请求插件）：`NewPlan()` 读一份共享切片，
  无实例时循环体为空。
- `relay/channel/providerplugin/*`（800 行）：仅插件型渠道命中。
- `service/apiduo_local_image_async.go`（784 行）：仅 `/async/v1/images/generations`。
- `service/growth.go`（774 行）：30 分钟一次的运营重算，非请求路径。
- `service/node_sync.go`（412 行）：**3 秒**心跳，每跳一次 `system_task` 查询 + 实例行写入，
  多副本按副本数叠加，量级为个位数 QPS。
- `service/ops_cache.go`、`service/ratelimit.go`（快照）、公告/审计/PAT scope 声明、
  `task_id_paths` 候选链（仅异步任务轮询）、`PostWssConsumeQuota` 毫秒化（仅 realtime）。

## 四、环境层面的两个既有事实（非本周引入，但影响读数）

1. **本地没开 `BATCH_UPDATE_ENABLED`**，配额按请求逐条落库 ⇒ 每请求 19.7 条 SQL、
   5.7 ms 容器 CPU。根 `docker-compose.yml` 与 `docker-compose.dev.yml` 都显式开 true，
   生产值在 `/www/data/apiduo-cn/.env`（不入库）。本地读数因此偏悲观；A/B 两变体同等条件下
   对比，结论不受影响。
2. **消费日志是同步插入**（`model/log.go` 的 `createLog` 在请求路径上）。16 并发下
   ClickHouse 打满 98% CPU，吞吐停在约 83 rps，p99 冲到 2 s。这是既有形态，不是本周改动；
   但它是这套部署的吞吐上限所在 —— 日志量增长时先看 CH 写入能力。

## 五、复现

```bash
# 基线
git worktree add /tmp/apiduo-base 42577be46
cp -R web/dist /tmp/apiduo-base/web/          # 主树构建产物，embed 需要
(cd /tmp/apiduo-base && GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o /tmp/perfbin/base.bin .)
# 当前
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o /tmp/perfbin/head.bin .
# 交替压测（脚本与压测客户端在 /tmp/perfbin/，不入库）
```

## 六、优化（同日实测，按 profile 排序）

CPU profile（`ENABLE_PPROF`，cgroup 采样 30s）给出请求路径的成本分布：

| 占比 | 位置 |
|---|---|
| 22.0% | `service.PostTextConsumeQuota` |
| 17.4% | ↳ `model.RecordConsumeLog` |
| 8.9% | ↳↳ `createLog` → `gorm.Create`（同步插 ClickHouse） |
| 4.3% | ↳↳ `model.GetUserSetting`（每请求一条 SQL，只为判断日志要不要写 IP） |
| 3.2% | ↳ `service.SettleBilling` |
| 2.0% | ↳↳ `logger.LogInfo`（每请求打印整条 params JSON） |

### 6.1 已落地：`BATCH_UPDATE_ENABLED=true`（配置，对齐线上）

| 判据 | 关闭 | 开启 |
|---|---|---|
| 每请求 SQL | 19.75 | 5.88 |
| 每请求容器 CPU | 5722 µs | 3525 µs |
| p50（50 rps 定速） | 8.36 ms | 8.33 ms |

已写入 `deploy/local/.env.app` 与 `up.sh` 的自举模板。注意这是**降低写放大**，不是降延迟：
本地原来每请求直接 UPDATE 用户行与令牌行，与线上的形态不同。

### 6.2 已落地：日志的 IP 判定不再回查用户行（`model/user_cache.go` + `model/log.go`）

认证中间件已经把同一行用户数据的设置写进上下文（`UserBase.WriteContext`），
两处 `RecordXxxLog` 却各自 `GetUserSetting` 一次。没有 Redis 的部署（线上就没有）里，
`GetUserSetting` 没有内存缓存，每次都是一条 SQL。

新增 `shouldRecordIP(c, userId)`：先读上下文，缺失时回落原路径。实测：

| 判据 | 改前 | 改后 |
|---|---|---|
| 每请求 SQL | 5.88 | 4.71（−20%） |
| 每请求容器 CPU | 3525 µs | 3312 µs（−6%） |
| p50 | 8.33 ms | 8.15 ms |

判据 `model/log_ip_setting_test.go`：上下文说「不记」不能被库里的默认值覆盖，
上下文说「记」也不能被库里的关闭覆盖。

### 6.3 已对齐：`LOG_WRITER_ASYNC`（线上本来就开着）

上一版这里写「线上是否开启未知」，是只看了仓库里的 `deploy/prod` compose 就下的结论 ——
不对。实测 `ai-prod:/www/data/apiduo-cn/.env` 与容器启动行：异步写入一直开着，而且参数
调过（不是默认的 5s/2000）。

| 变量 | 线上 | 本地（已对齐） |
|---|---|---|
| `LOG_WRITER_ASYNC` | true | true |
| `LOG_WRITER_INTERVAL_MS` | 2000 | 2000 |
| `LOG_WRITER_BATCH_SIZE` | 5000 | 5000 |
| `LOG_WRITER_QUEUE_SIZE` | 50000 | 50000 |
| `LOG_WRITER_SPOOL_MAX_MB` | 256 | 256 |
| `LOG_WRITER_SPOOL_DIR` | `/data/log_spool/apiduo-cn` | `/data/log_spool/apiduo-local` |

线上 14:16:56 启动行：`log writer enabled: dir=/data/log_spool/apiduo-cn interval=2s
batch=5000 queue=50000 enqueue_wait=1s`。本地对齐后同一行一致，3 个请求后 ClickHouse
多出 3 行、spool 生成对应文件与 marker。

同一组参数下的高并发实测（c=32 闭环、20s、每轮重建容器、轮间 20s 让 CH 合并落定）：

| 轮次 | 同步 rps / p50 / p99 | 异步 rps / p50 / p99 |
|---|---|---|
| 1 | 161.4 / 194.3 / 341.1 ms | 359.5 / 82.3 / 219.9 ms |
| 2 | 141.1 / 203.6 / 752.4 ms | 336.9 / 86.8 / 249.9 ms |
| 3 | 157.1 / 197.2 / 368.1 ms | 326.2 / 88.6 / 253.7 ms |
| 中位 | **157 / 197 / 368** | **337 / 87 / 254** |

吞吐 +115%，p50 −56%，p99 −31%。原因是 ClickHouse 的插入（5–15 ms）原本在请求路径上，
并发一上来就串行化。

#### 代价（全部来自实现，不是推测）

1. **崩溃窗口丢日志（≤ interval）**：行先入内存队列，flush 时才写 spool。`kill -9` / OOM
   时队列里的行消失。正常 `docker stop` 会 drain（收到 stop 先刷完队列再 final flush），
   滚动升级不丢。
2. **至少一次 ⇒ 可能重复**：插入成功但 marker 未落时崩溃，重启重放整批（marker 损坏会
   从头重放）。`logs` 是 `ReplacingMergeTree(ORDER BY created_at, request_id)`，靠后台
   合并折叠；合并前查询可能看到两条，对账必须按 `request_id` 去重。
3. **可见性延迟 ≤ interval**：刚失败的请求可能立刻查不到日志，排障时容易误判「没打日志」。
4. **背压回到请求路径**：队列满 → 最多等 1s → 仍满则回落同步插入。日志库慢时延迟双峰，
   实测见过 p99 2.4 s。
5. **长时间故障不自动回填**：spool 重放只在启动时做一次（`StartLogWriter` 的
   `writer.replay()`）。日志库宕机期间的行落在 spool，要等**下次进程重启**才补插；
   内存里的 `pending` 超过 batch×10 会被 `trimPending` 丢弃（行仍在 spool）。
6. **spool 没有总量上限**：`LOG_WRITER_SPOOL_MAX_MB` 是单文件上限，写满轮转，清理只删
   已确认的文件。日志库长期不可用时未确认文件持续累积，只能靠磁盘兜底（线上 `/` 尚有 672G）。
7. **可观测性弱**：只有启动行 + 5 分钟一次心跳，没有指标端点，面板不展示。出问题时靠
   `docker logs | grep "log writer"` 区分「没流量」和「写入器卡住」。

### 6.4 其余可做但未做

- `logger.LogInfo` 每请求打印整条 params JSON（2% CPU + stdout IO）。可按日志级别收敛到
  DEBUG，代价是运维不能再 grep 这些行。
- `SettleBilling` 3.2%：钱包结算路径，改动面大，需要单独的判据。
- ClickHouse 仍是这套部署的吞吐天花板（16 并发即 98% CPU）。消费日志量增长前先看 CH 的
  写入能力与 part 数。
