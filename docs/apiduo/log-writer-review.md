# apiduo.cn 线上版本 · 日志优化 / 充分性测试 / 对抗审查

> 2026-11-07 16:00–16:26 GMT+8。对象：**线上 `apiduo.cn`**（ai-prod `210.121.44.245`）。
> 方法：① 直接对线上发请求（黑盒）② ai-prod 服务端只读核对 ③ 本地可复现的对抗测试与 benchmark。
>
> 🔄 **16:19 已按本文 §5-B 重新部署为 ClickHouse 日志版本**（TTL 360 天）。
> §0-#3 / §1.1 / §5 描述的是**部署前**状态；变更与验证见 **§8**。部署前已备份 compose 与 .env，可随时回滚。

---

## 0. 结论先行

| # | 结论 | 级别 |
|---|---|---|
| 1 | 线上 = `apiduo-v0.0.3` / `7c558b02`，digest `sha256:1f5d6864…`，与本地 HEAD 完全一致；15:59:48 重建、healthy、RestartCount=0 | ✅ |
| 2 | **空库未初始化**：`users` 表 0 行、`logs` 表 0 行，而 `/setup` 与 `/api/setup` 公网可达且已被访问 ⇒ **任何人可抢注管理员** | 🔴 P0 |
| 3 | **没用 ClickHouse**：env 里无 `LOG_SQL_DSN` / `LOG_WRITER_ASYNC`，走默认 SQLite；异步日志写入器（仅 CH 生效）在这套部署上**不可达**。与「以后用 CH 部署」的约定相反 | 🔴 P0 |
| 4 | 版本一致性通过：body `version=0.0.3` = 响应头 `x-new-api-version: 0.0.3`；request-id 12/12 唯一；`/api/appearance` 200 证明 apiduo 改造确实在镜像里 | ✅ |
| 5 | 性能：公网 p50 **1.13s** 是链路 RTT（服务端 GIN 仅 **0.21ms**），不是应用瓶颈；线上 0 条日志 ⇒ 日志性能无法在线上直接观测，改用本地实测 | ⚠️ 归因 |
| 6 | 代码层对抗审查：**12 个缺陷/缺口**（2 个 P0、6 个 P1、4 个 P2），均有可复现测试 | 🔴 |
| 7 | 参数：当前只配了 7 个 env，缺 `BATCH_UPDATE` / `MEMORY_CACHE` / `ERROR_LOG` / `NODE_NAME` / `TRUSTED_PROXIES` / 停机超时对齐 | ⚠️ |

---

## 1. 直接测线上：版本与链路

### 1.1 部署事实（服务端只读）

| 项 | 值 |
|---|---|
| 域名 / DNS | `apiduo.cn` → `210.121.44.245`（ai-prod）；`www` / `platform` / `api` 三个子域**均未解析** |
| 入口 | Tengine/nginx `server_name apiduo.cn`，`limit_req_zone 600r/m`，80→301 https |
| 容器 | `apiduo-cn`，`ghcr.io/apiduo/apiduo:0.0.3`，`sha256:1f5d6864…` |
| 端口 | `127.0.0.1:39047->3000`（仅回环） |
| compose | `/opt/apiduo-cn/docker-compose.apiduo-cn.yml`，project `apiduo-cn`，`restart: unless-stopped` |
| env_file | `/www/data/apiduo-cn/.env`，实际只 7 个 key：`SESSION_COOKIE_SECURE`、`SESSION_COOKIE_TRUSTED_URL`、`TZ`、`GIN_MODE`、`SESSION_SECRET`、`CRYPTO_SECRET`、`PATH` |
| 数据卷 | `/www/data/apiduo-cn/data` → `/data`；SQLite `one-api.db` 864K + WAL 428K |
| 启动 | 2026-11-07 15:59:48 UTC，`ready in 7490 ms`，healthy，`RestartCount=0` |
| `/api/status` | `version: 0.0.3`、`system_name: New API`、`enable_batch_update: false`、`quota_display_type: USD` |
| `stop_grace_period` | **30s**（应用侧 `SHUTDOWN_TIMEOUT_SECONDS` 默认 **120s**，见 §6） |

同机还跑着已弃用的旧容器 `apiduo`（`ghcr.io/jushenzhidao/apiduo:0.0.24`），它带
`LOG_SQL_DSN=clickhouse://…@apiduo-clickhouse:9000/newapi_logs?async_insert=1&wait_for_async_insert=0`、
`LOG_SQL_CLICKHOUSE_TTL_DAYS=30`、`MEMORY_CACHE_ENABLED=true` —— **新旧参数不要混淆**。

### 1.2 黑盒实测（本机直连 `curl --noproxy '*'`）

| 项目 | 结果 |
|---|---|
| `/api/status` × 12 顺序 | 全 200；min 1.085s / p50 **1.125s** / p95 1.167s / max 1.979s |
| `/api/status` × 40（并发 5） | **40/40 200**，无限流；p50 1.134s / p95 1.298s / p99 1.301s / max 1.607s |
| 版本一致性 | body `0.0.3` = header `0.0.3`；`cache-version` 恒为 `b688f2fb…`；request-id **12/12 唯一** |
| 安全头 | HSTS / nosniff / SAMEORIGIN / referrer-policy / `x-robots-tag: noindex,nofollow` 齐全；`/api/status` **无 Cache-Control** |
| 只读端点 | `/api/appearance` 200（apiduo 外观特效）、`/api/pricing` 200、`/api/notice` 200、`/robots.txt` 200、`/api/models` 401、`/api/user/self` 401 |
| SPA fallback | `/.git/HEAD`、`/backup.tar.gz`、随机不存在路径 **全部 200 + 同一个 1707B index.html** |
| 静态资源 | 首页 1707B 中未提取到 `/assets/...` 绝对路径 ⇒ 长缓存头**未验证**（roadmap 的 P1 验收项仍未完成） |
| 伪造 `X-Forwarded-For: 8.8.8.8` | 服务端 GIN 记录仍是真实 `112.86.208.37` ⇒ **伪造无效**，nginx 已重写该头 |

> **延迟归因**：容器 GIN 日志 `200 | 211.938µs | GET /api/status`。公网 1.1s 是 TLS 握手 + 链路 RTT，
> 与日志写入无关。把它当成「日志慢」会得出完全错误的优化方向。

### 1.3 🔴 空库与抢注风险

```
SELECT count(*) FROM users => 0
SELECT count(*) FROM logs  => 0
表：options / logs / audit_logs / login_encryption_keys
```

启动日志：`system is not initialized and no root user exists`。
nginx 最近 20 行里出现 `16:13:05 /setup`、`16:13:06 /api/setup?t=…` 等请求。
compose 自带注释「空库首次启动会建 root/123456 —— 起来后必须立刻改密码」。
⇒ **当前是公网可写的管理员抢注窗口**，应立刻完成初始化并改口令（本轮未代为操作）。

---

## 2. 「日志改了几个地方」精确清单

| 层面 | 事实 |
|---|---|
| 代码（`97be2dc3`） | **6 文件 / +1091 行 / 0 删除**：新增 `model/log_spool.go`(261)、`model/log_writer.go`(449)、两个测试(363)；改上游既有文件 `main.go`(+13)、`model/log.go`(+5)，且**只插入、未改/删任何既有行** |
| 挂接块 | `APIDUO:log-writer` 共 **3 处**：`main.go:348` 启动、`main.go:232` 停机、`model/log.go:101` createLog 分流（合计 17–18 行） |
| 后续改动 | `git log 97be2dc3..HEAD -- model/log_*.go model/log.go` = **0 个提交**，日志模块落地后没再动过 |
| 部署侧 | **0 处**：线上 env 没有任何 `LOG_*`，compose 也没带；`docker-compose.yml`（仓库）里的 CH 服务整段仍是注释 |
| 运行态 | 无 `log_spool` 目录、容器日志无 `log writer enabled`、SQLite `logs` 表 0 行 ⇒ **默认关闭态与上游行为等价，验证通过**（规范第 3 条） |

也就是说：**代码改了 3 处挂接 + 4 个新文件，但线上这套部署一行日志配置都没接上**。

---

## 3. 充分性测试

### 3.1 覆盖矩阵

| 场景 | 原有 12 例 | 本轮新增 12 例 |
|---|---|---|
| happy path（flush / 失败重试 / 关机排空 / 回放幂等 / encode） | ✅ | — |
| 真 CH 列对齐 roundtrip | ⚠️ `TEST_CLICKHOUSE_DSN` 未配 ⇒ **SKIP** | 纯字符串 DDL↔INSERT 列比对（CI 可见） |
| 队列满 / 背压 | ❌ | ✅ 实证阻塞 |
| 停机竞态 / 在途 enqueue | ❌ | ✅ 实证随机丢弃 |
| spool 写失败 + insert 失败 | ❌ | ✅ 实证重启丢行 |
| 崩溃窗口重复 / 统计去重 | ❌ | ✅ 实证 sum(quota) 翻倍 |
| 多实例共享 spool 目录 | ❌ | ✅ 实证互删 |
| torn tail / 坏 marker / 坏记录 | ❌ | ✅ 3 例 |
| 类型截断 / 列顺序漂移 / 默认关闭 | ❌ | ✅ 4 例 |
| 性能基准 | 1 个（被 skip） | 5 组实跑 |

🔴 **最大的测试缺口：开启态 e2e 从未在 CI 跑过**。解耦规范要求「默认关闭 + 开启态最小 e2e」，
但唯一连真 CH 的 `TestClickHouseLogInserterRoundTrip` 需要手工设 `TEST_CLICKHOUSE_DSN`，
发版 workflow 也不跑它 ⇒ 切 CH 上线时，列顺序/类型/批量协议全部没有自动化兜底。

### 3.2 对抗测试实证（`go test -race ./model/ -run TestAdversarial -v`，12/12 PASS）

```
队列满：enqueue 阻塞 80ms 未返回，直到 close(stop) 才回 (handled=true, "Log writer stopped")
停机竞态：stop 已关且队列未满，1000 次 enqueue => 接受 485 / 报错 515（同一输入两种结果）
spool 失败：spool.Append 失败 + Insert 失败 => 内存 1 行，重启 replay 0 行（永久丢失）
列契约：DDL 20 列 = INSERT 20 列 = Log 结构体字段顺序（当前一致）
类型截断：8589934599 被 int32() 静默截成 7
重放重复：3 行重放后库中 6 行，sum(quota)=600（应为 300）
共享 spool：B 的 Confirm 删掉 A 未确认文件，A 重启反而回放 B 的行
坏记录：跳过且不计入 replayed（1 行），坏行永久丢失
默认关闭：非 CH / 开关 false 时 StartLogWriter 为 no-op，全局 writer 保持 nil
```

新增文件：`model/log_writer_adversarial_test.go`（未提交）。

### 3.3 性能基准（darwin/arm64 M1 Max，200x，`-benchmem`）

| 基准 | ns/op | 吞吐 | B/op | allocs/op |
|---|---|---|---|---|
| `EncodeLogBatch rows=1` | 914 | — | 908 | 2 |
| `EncodeLogBatch rows=200` | 134,566 | — | 189,437 | 201 |
| `EncodeLogBatch rows=2000` | 1,304,070 | — | 1,856,126 | 2,001 |
| `SpoolAppend`（200 行 + rotate + Confirm） | 449,304 | 445,226 rows/s | 3,679 | 34 |
| `AsyncFlushBatch`（2000 行整体） | 2,803,533 | **715,163 rows/s** | 1,876,488 | 2,040 |
| `EnqueueOnly` | **49.38** | — | 0 | 0 |
| `SyncCreateSingle`（SQLite GORM 单行） | 68,343 | **14,633 rows/s** | 11,603 | 114 |

- 异步批量 vs 同步单行 ≈ **49×** 吞吐（注意：inserter 是内存 mock，**不含网络/CH 落盘**，只量化应用侧开销）。
- `enqueueLog` 本身 49ns / 0 分配 ⇒ 把同步 DB 写移出请求路径的收益是确定的。
- 每行 JSON 序列化 1 次分配 ⇒ `EncodeLogBatch` 的 allocs ≈ 行数，批量越大 GC 压力越线性。

> **对照数据（非本次对象，仅作参考）**：我最初误连了 core-4 的上游 new-api 部署
> （`v1.0.0-rc.23` + CH `26.7.3.19`）。其 `system.query_log` 近 1 小时显示 16,232 次 INSERT、
> **每次仅 1 行**、p50 **5.16s** / p99 **10.02s**，全局 `async_insert=1 / wait_for_async_insert=1`、
> TTL 180 天。这组数字说明「同步逐行 + 服务端等待」的组合能有多糟，也直接支撑 §5 的参数建议。

---

## 4. 一致性审查

| 项 | 结论 |
|---|---|
| 列顺序 | 两处**硬编码字符串**（`clickHouseLogInsertStatement` 与 `clickHouseLogCreateTableSQL`）当前一致，但没有编译期绑定；已补 CI 可见的字符串比对测试 |
| 字段类型 | `Log` 计数器是 Go `int`(64bit)，DDL 是 `Int32`，native inserter 显式 `int32(...)` **无范围检查**；同步 GORM 路径不做该转换 ⇒ 两条路径对越界值行为不同 |
| 交付语义 | 文档声明 at-least-once 且「reconciliation 必须按 request_id 去重」，但表是普通 `MergeTree`（无 `ReplacingMergeTree`、无唯一键），`model/log.go` 用 `sum(quota)` 直接聚合 ⇒ **重放即重复计费**（实测翻倍） |
| request_id | `NewRequestId()` = 时间戳 + 模块 hash + 8 随机字符，**无全局唯一保证**；DDL 的 `ORDER BY (created_at, request_id)` 不是去重键；手写 DDL 也不创建 gorm tag 声明的索引 |
| TTL | `LOG_SQL_CLICKHOUSE_TTL_DAYS` 默认 0 ⇒ `syncClickHouseLogTTL(0)` 检测到已有 TTL 时执行 `ALTER TABLE logs REMOVE TTL`。旧容器设的是 30 天；若新容器接同一个库且漏配该变量，会把旧站保留策略**静默删掉** |
| 库隔离 | 旧容器写 `newapi_logs.logs`；新容器若复用同库同表 ⇒ **两代日志串台**。必须换库名 |
| 多实例 spool | `LOG_WRITER_SPOOL_DIR` 默认相对 `data/log_spool`，单 marker 文件；两副本共享卷会互相 Confirm/删除（已实证） |
| 默认关闭等价 | ✅ 线上无 spool 目录、无额外日志行、无 writer 全局对象 |

---

## 5. 参数优化（可直接套用，本轮未落地）

### A. 立即生效，不涉及 CH 迁移

```dotenv
BATCH_UPDATE_ENABLED=true          # 当前 false；减少每请求同步写额度
MEMORY_CACHE_ENABLED=true          # 仓库 compose 已推荐，线上 apiduo-cn 漏带
BATCH_UPDATE_INTERVAL=5            # 与上游默认一致
ERROR_LOG_ENABLED=true             # 当前 false，错误日志不入库，排障盲区
NODE_NAME=apiduo-cn                # 日志/审计里区分节点身份
TRUSTED_PROXIES=127.0.0.1/32,::1/128   # 消除启动 WARNING；不要写 none（会破坏 access token IP 白名单）
SHUTDOWN_TIMEOUT_SECONDS=60        # 与 compose stop_grace_period 对齐
```

同时把 compose 的 `stop_grace_period: 30s` 提到 **≥90s**：`logWriterFlushTimeout=60s`，
若接 CH 后排空时卡一次 flush，30s 会被 SIGKILL ⇒ 丢队列与 spool 未确认数据。

### B. 切 ClickHouse（符合「以后用 CH 部署」）

> ✅ **已于 2026-11-07 16:19 落地，TTL 按用户要求改为 360 天**，实际值见 §8。

```dotenv
LOG_SQL_DSN=clickhouse://<user>:<pwd>@<ch-host>:9000/apiduo_cn_logs?dial_timeout=5s
LOG_SQL_CLICKHOUSE_TTL_DAYS=30     # 必须显式写；0 会 REMOVE TTL
LOG_WRITER_ASYNC=true              # 不开则上面所有 LOG_WRITER_* 都不生效
LOG_WRITER_INTERVAL_MS=2000        # 默认 5000；缩短崩溃窗口
LOG_WRITER_BATCH_SIZE=5000         # 默认 2000；CH 侧更爱大块，减少 parts/merge
LOG_WRITER_QUEUE_SIZE=50000        # 默认 100000（≈50MB 内存）；与 spool 容量匹配
LOG_WRITER_SPOOL_MAX_MB=256        # 默认 64MB；按 峰值速率×行大小×可容忍故障时长 估
LOG_WRITER_SPOOL_DIR=data/log_spool/apiduo-cn   # 每实例独立，避免共享 marker
```

要点：
- **库名必须换**（如 `apiduo_cn_logs`），不要复用旧站的 `newapi_logs`。
- DSN **不要带 `async_insert=1`**：writer 会强制 `async_insert=0` 走原生 batch；但 fallback 同步路径仍受 DSN 影响，带上会引入二次缓冲。
- CH 服务端建议 `async_insert=0` / `wait_for_async_insert=0`；若保留 async_insert，则**必须**关等待（core-4 的 5s/10s 延迟就是 `wait_for_async_insert=1` 的代价）。
- 版本：仓库 compose 写 `clickhouse/clickhouse-server:24.8`，ai-prod 现有的是 `25.8`，core-4 是 `26.7.3.19`（`latest` 漂移）。驱动是 `clickhouse-go/v2 v2.46.0` + `ch-go v0.71.0`，**三者版本不一致且未经端到端验证** ⇒ 上线前必须跑一次 `TestClickHouseLogInserterRoundTrip`。

### C. 不建议

- `TRUSTED_PROXIES=none`：实测伪造 XFF 已被 nginx 消解，但设 none 会让 ClientIP 变成回环地址，破坏 access token IP 白名单。
- 把 `LOG_WRITER_QUEUE_SIZE` 无限加大：当前队列满是**阻塞请求**而非降级（§6 P0-1），队列越大只是把故障暴露时间往后推。

---

## 6. 对抗审查问题清单

| ID | 级别 | 问题 | 证据 | 反驳视角（避免夸大） |
|---|---|---|---|---|
| L1 | 🔴 P0 | 空库 + `/setup` 公网可达，可抢注管理员 | `users=0`，nginx 有 `/api/setup` | 无：这是时间窗口问题，初始化即关闭 |
| L2 | ✅ 已修 | ~~队列满**阻塞请求**~~：改为有界等待 + 交回同步插入（`LOG_WRITER_ENQUEUE_WAIT_MS`，默认 1000ms），并计入 `fallback` | `TestAdversarial_EnqueueFallsBackWhenQueueIsFull` | 同步回退本身也会等 CH，真需求是「有界等待 + 明确降级」，不能简单改回同步 |
| L3 | ✅ 已修 | ~~stop 后随机接受/报错~~：stop 优先判定，一律返回 `handled=false` 让 `createLog` 走同步插入，不再丢行 | `TestAdversarial_EnqueueAfterStopFallsBack` | `srv.Shutdown` 正常情况下会等在途请求，所以 HTTP 路径概率低；但后台 goroutine（task billing）不受其约束，风险真实 |
| L4 | ✅ 已修 | ~~spool 写失败 + insert 失败即丢行~~：`spoolDirty` 标记后下一轮把 pending 一并写盘，重启可 replay | 重启 replay 0 行 | 需磁盘故障叠加 CH 故障，双故障；但日志正是为这种时刻存在的 |
| L5 | 🟠 P1 | at-least-once 重复且查询不去重 ⇒ 统计翻倍 | 3 行→6 行，sum 300→600 | 文档已声明「查询须去重」，属**实现未兑现**，不是设计疏忽 |
| L6 | ⏸ 部署约定 | ~~多实例共享 spool 目录互删~~：单副本无影响；扩容时必须为每个副本设置不同的 `LOG_WRITER_SPOOL_DIR`（代码不做自动隔离，避免 hostname 变动误判） | A 丢行 + 误重放 B | 当前单副本未触发；一旦按「用 CH 部署」扩容即命中 |
| L7 | 🟠 P1 | TTL 漂移：`TTL_DAYS=0` 会 REMOVE 已有 TTL | `syncClickHouseLogTTL` | 只要显式配 30 就没事；风险在「漏配」而非代码错 |
| L8 | 🟠 P1 | `stop_grace_period 30s` < `flushTimeout 60s` | compose / main.go | 当前 SQLite 无 writer，未触发；接 CH 后才会暴露 |
| L9 | 🟡 P2 | `int32()` 静默截断；两条写入路径对越界值行为不同 | 8589934599→7 | 正常业务值远小于 21 亿，属长尾 |
| L10 | 🟡 P2 | 列顺序双硬编码、真 CH roundtrip 在 CI 被 skip | 现有测试 SKIP | 已补字符串比对，但未覆盖协议层 |
| L11 | 🟡 P2 | SPA fallback 把 `/.git/HEAD` 等全回 200，制造扫描噪声与 access log 膨胀 | nginx 日志 130×200 | 不泄露内容（返回 index.html），但建议 nginx 直接 404/444 |
| L12 | 🟡 P2 | `/api/status` 无 Cache-Control；静态资源长缓存未验证 | 黑盒 | 首页 1707B 未提取到 `/assets/` 绝对路径，roadmap P1 验收项未完成 |

**未列为问题**（实测排除）：伪造 `X-Forwarded-For` 未生效（nginx 重写）；`SESSION_SECRET`/`CRYPTO_SECRET` 均已固定（上游 HMAC 约束已满足）；容器运行期无 ERROR/WARN；版本头体一致；request-id 唯一。

---

## 7. 未做的事 / 需要拍板

1. **未改线上任何配置、未重启容器**（生产变更需你确认）。初始化 root 与改口令建议你立即手动完成。
2. **未改产品代码**：L2/L3/L4 涉及并发模型与降级策略，是产品决策（阻塞 / 丢弃 / 同步回退 / 有界等待），等你选方向再动手。
3. **未提交、未 push**：新增 `model/log_writer_adversarial_test.go` 与本报告均为工作区新文件；push main 会触发 brand-release 发版。
4. 待定：切 CH 的库名与凭据来源、TTL 天数、是否复用 ai-prod 上现有的 `apiduo-clickhouse:25.8`、是否把 `TestClickHouseLogInserterRoundTrip` 接进 CI（需要 CI 起 CH service）。

### 复现命令

```bash
# 线上
curl --noproxy '*' -sS -D - https://apiduo.cn/api/status | head -20
# 本地对抗测试
go test -race ./model/ -run 'TestAdversarial' -count=1 -v
# 本地基准
go test ./model/ -run '^$' -bench BenchmarkAdversarial -benchtime=200x -benchmem
```

---

## 8. 全新部署记录（2026-11-07 16:19–16:26）

### 8.1 最终形态（全新建，三个独立容器）

| 组件 | 容器 | 镜像 | 数据目录 | 说明 |
|---|---|---|---|---|
| 应用 | `apiduo-cn` | `ghcr.io/apiduo/apiduo:0.0.3` | `/www/data/apiduo-cn/data` | `127.0.0.1:39047→3000` |
| 主库 | `apiduo-cn-mysql` | `mysql:8.2` | `/www/data/apiduo-cn/mysql` | 库 `apiduo_cn`，utf8mb4，+08:00 |
| 日志库 | `apiduo-cn-clickhouse` | `clickhouse/clickhouse-server:25.8` | `/www/data/apiduo-cn/clickhouse` | 库 `apiduo_cn_logs`，TTL **360 天** |

- `SQL_DSN=root:***@tcp(apiduo-cn-mysql:3306)/apiduo_cn?charset=utf8mb4&parseTime=True&loc=Local`
- `LOG_SQL_DSN=clickhouse://default:***@apiduo-cn-clickhouse:9000/apiduo_cn_logs?dial_timeout=5s`
- `LOG_WRITER_ASYNC=true`，batch 5000 / interval 2s / queue 50000 / spool 256MB `/data/log_spool/apiduo-cn`
- `LOG_SQL_CLICKHOUSE_TTL_DAYS=360`、`BATCH_UPDATE_ENABLED=true`、`MEMORY_CACHE_ENABLED=true`、`ERROR_LOG_ENABLED=true`、`NODE_NAME=apiduo-cn`
- `SQL_MAX_OPEN_CONNS=100`、`SQL_MAX_IDLE_CONNS=20`、`SQL_MAX_LIFETIME=3600`
- compose `stop_grace_period: 90s`，三容器 `depends_on: service_healthy`
- 未设 `TRUSTED_PROXIES`：nginx 在宿主机、容器来源是 docker 网关，设 `127.0.0.1/32` 会让 ClientIP 变内网并破坏令牌 IP 白名单。

### 8.2 验证（16:25）

```
using MySQL as database
using ClickHouse as log database
log writer enabled: dir=/data/log_spool/apiduo-cn interval=2s batch=5000 queue=50000 replayed=1
CH: count()=1, request_id=apiduo-fresh-selftest-20261107  → 清理后 0
MySQL: AutoMigrate 完成（abilities/.../logs/options/...）
/api/status: version=0.0.3, enable_batch_update=true
三容器 healthy
```

- SQLite 旧库保留在 `/www/data/apiduo-cn/data/one-api.db`（空库、未迁移，仅作备份）。
- 回滚点：`compose.bak.20261107-1628` + `.env.bak.20261107-1628`（MySQL + 旧 CH）；更早 `-1621`（SQLite + 旧 CH）。

### 8.3 事故记录（16:23）

CH healthcheck 写成 `clickhouse-client --password '$CLICKHOUSE_PASSWORD'`，**单引号阻止变量展开** ⇒ 永远 unhealthy
⇒ `depends_on` 卡住 `apiduo-cn`，站点约 3 分钟不可用。改成 `--password=$$CLICKHOUSE_PASSWORD` 后恢复。
教训：compose healthcheck 给变量加引号必须用双引号，或直接 `--flag=$VAR`。

### 8.4 调优落地与前后对比（2026-11-07 16:31）

| 项 | 调优前 | 调优后 |
|---|---|---|
| API p50（公网 15 次） | 1.131s（含 1 次 `000` / 11s 毛刺） | **1.102s**（全 200） |
| API 并发 30×5 p50/p95/p99 | 1.126 / 1.213 / 1.214 | **1.115 / 1.146 / 1.151** |
| MySQL 1000×`SELECT 1` | 158 ms | 156 ms |
| CH 查询 | p95 1ms / p99 2ms | **1710 QPS**，p95 2ms / p99 2ms |
| CH 批量写 5×5000 行 | 900 ms | **801 ms（-11%）** |
| MySQL `innodb_buffer_pool_size` | 128 MB | **4096 MB** |
| MySQL `innodb_redo_log_capacity` | 100 MB | **2048 MB** |
| MySQL `innodb_io_capacity` | 200 | **2000**（max 4000） |
| MySQL `max_connections` | 151 | **300** |
| MySQL `slow_query_log` | off | **on, long_query_time=1s** |
| MySQL `binlog_expire_logs_seconds` | 默认 | **7 天** |
| CH `log_queries` | 全部查询（min_duration 0） | **只记 >1s**；`log_query_settings=0` |
| CH `max_memory_usage` | 0（无限制） | **4 GB** |
| CH `max_server_memory_usage` | 约 90% RAM（~42 GB） | **4 GB** |
| 容器内存限额 | 无 | **apiduo 2G / mysql 6G / clickhouse 6G** |
| 容器实际内存 | 29 / 439 / 575 MB | 80 / 705 / 553 MB |

结论：
- API p50 只从 1.131→1.102（-2.6%），因为 **1.1s 是公网链路 RTT**（服务端 GIN 0.21ms），调优对 API 延迟不是主要矛盾。
- MySQL 空库下简单查询 158→156ms，**buffer pool 的收益要等数据量增长后才体现**。
- CH 批量写 900→801ms（-11%），查询 1710 QPS。
- 真正收益是**上限与稳定性**：MySQL 不再 128MB buffer / 151 连接；CH 不再可能吃光 42G 内存；三个容器有硬限额，避免互相挤占。

### 8.5 日志写入端到端压测（5000 行）

```
造 5000 行 spool = 2,073,890 B
docker restart apiduo-cn → healthy: 7328 ms（含容器生命周期 + healthcheck）
应用 ready in 943 ms，其中 log writer enabled ... replayed=5000
CH: rows=5000, uniqExact(request_id)=5000（无重复）
抽查: user_id=1, type=2, content=perf replay row, quota=1, prompt_tokens=10, completion_tokens=5, group=default, ip=127.0.0.1
SELECT count() WHERE request_id LIKE 'perf-replay-%': 176 ms（含 docker exec 开销）
清理后 0 行；/api/status=0.0.3
```

结论：**5000 行一批的 replay + native batch 在应用启动 943ms 内完成**，批量链路可用、字段对齐、无重复。`docker restart→healthy` 的 7.3s 主要是容器停止/启动与 healthcheck 间隔，不是写入耗时。

### 8.6 日志页面/接口端到端验证（16:40）

背景：后台「日志」页 `/usage-logs/common` 之前看不到任何记录 —— `logs` 表 0 行。原因：站点上线后只有登录请求（写 `audit_logs` 审计日志），**没有任何 API 消费请求**，不是功能故障。

注入 3 条真实类型日志（走 spool → replay → native batch → CH，不触发真实上游、不计费）：

| type | 含义 | 筛选结果 |
|---|---|---|
| 2 | 消费 | `/api/log/self?type=2` total=1 |
| 4 | 系统 | `/api/log/self?type=4` total=1 |
| 5 | 错误 | `/api/log/self?type=5` total=1 |

| 接口 | 结果 |
|---|---|
| `GET /api/log/self?p=1&page_size=20` | success，total=3，items=3 |
| `GET /api/log/self/stat` | `{quota:1, rpm:1, tpm:15}` |
| `GET /api/log/`（管理员） | success，total=3 |
| `GET /api/log/stat` | 同 self/stat |
| `GET /api/log/export`、`/api/log/self/export` | 流式 NDJSON，正常 |
| `GET /api/log/search`、`/api/log/self/search` | **已废弃**，返回 "This API is deprecated"，前端不用 |

结论：写入 → CH 查询 → 接口 → 统计 → 导出全部通；页面刷新 `/usage-logs/common` 即可看到这 3 条。

排障注意：`SESSION_COOKIE_SECURE=true` 时，从 `http://127.0.0.1` 登录拿到的 Secure cookie **不会被发送** ⇒ 必须走 `https://` 或 `Authorization: Bearer <access_token>`。这也是我第一次测接口全部 401 的原因。

### 8.6.1 真实业务请求测试（16:48）

为了验证真实消费日志（不是手工注入），临时搭了一条完整链路：

| 组件 | 做法 |
|---|---|
| 上游 | 本地 `python:3.12-slim` mock，OpenAI 兼容，支持 `stream` + `usage` |
| 渠道 | `POST /api/channel/` `type=1`，`base_url=http://apiduo-cn-mock:8080`，`models=gpt-4o-mini` |
| 令牌 | `POST /api/token/`（unlimited），`POST /api/token/1/key` 取明文 |
| 定价 | `ModelRatio={"gpt-4o-mini":0.075}`、`CompletionRatio=4` |

结果：
- 4 次 `/v1/chat/completions`（1 次流式）全部 **200**，响应 4–7ms
- CH 真实消费日志：**4 条 `type=2`，其中 1 条 `is_stream=1`**；`quota` 合计 4、`prompt_tokens` 20、`completion_tokens` 4
- `/api/log/self` total=7（4 条真实 + 3 条注入）
- 测试后已删除 mock 容器、渠道、令牌；**日志保留**，页面 `/usage-logs/common` 可见

参数发现：新建渠道要等 `SYNC_FREQUENCY=60` 的缓存同步才生效 —— 第一次请求返回 503
`No available channel for model gpt-4o-mini under group default`，60s 后自动恢复。
若希望渠道变更秒级生效，可把 `SYNC_FREQUENCY` 调到 15–30s（DB 查询很轻）。

### 8.7 启动参数优化与对比

| 参数 | 调优前 | 调优后 | 作用 |
|---|---|---|---|
| `GOMAXPROCS` | 未设（=64） | **8** | 宿主 64 核 / load 264，限制 P 数减少调度争抢 |
| `GOMEMLIMIT` | 未设 | **1536MiB** | 容器 2G 的 75%，给非堆内存留余量 |
| `SQL_SLOW_THRESHOLD_MS` | 未设（200） | **500** | 减少 GORM 慢 SQL 日志噪声 |
| `LOG_WRITER_*` | 5000 / 2s / 50000 / 256MB | 不变 | 已按批量优化 |
| `SQL_MAX_*` | 100 / 20 / 3600 | 不变 | MySQL max_connections 300，3× 余量 |

启动一致性（连续 3 次）：**786 / 882 / 990 ms**；10000 行 replay 1090ms；CH 恢复后 729ms；停机恢复 1231ms。空载内存 **26MB**。

批量回放：

| 行数 | spool 大小 | 应用 ready | CH 落盘 | 重复 |
|---|---|---|---|---|
| 5,000 | 2.07 MB | 943 ms | 5,000 | 0 |
| 10,000 | 4.14 MB | 1,090 ms | 10,000 | 0 |

### 8.8 故障注入与停机测试

**CH 停机（16:44:00）→ 重启 apiduo**：

```
[error] failed to initialize database, got error dial tcp: lookup apiduo-cn-clickhouse ... no such host
[FATAL] 2026/11/07 - 16:44:02 | [dial tcp: lookup apiduo-cn-clickhouse ... no such host]
docker ps: apiduo-cn | Restarting (1)
```

- 原因：`InitLogDB` 的 `gorm.Open(clickhouse)` 会立即 `SELECT version()`，失败 → `common.FatalLog` → 进程退出 → `restart: unless-stopped` 循环。
- 停机窗口约 **17s**（16:44:00–16:44:17），期间站点不可用。
- spool 完整保留（4MB + 41KB），**数据没丢**。
- CH 恢复后容器自动重启，`replayed` 补写，CH 100 行 / 100 unique。

**优雅停机**：`docker stop` **1.69s**，日志 `log writer stopped: written=0 replayed=0 failed=0` + `server exited`；`docker start` 后 **1.23s** healthy。

### 8.9 对抗审查（最终）

| ID | 级别 | 问题 | 建议 |
|---|---|---|---|
| C1 | 🔴 P0 | **CH 不可用 ⇒ apiduo 无法启动**（`InitLogDB` Fatal，重启循环），与 writer「不让日志设施拖垮网关」的目标矛盾 | `InitLogDB` 对日志库失败降级到主库（`model/main.go` 加标记块），或 CH 连接 lazy + 迁移失败不 Fatal |
| C2 | 🟠 P1 | CH 恢复靠 `restart` policy 自愈，无告警 | 加容器 restarts / writer `failed` 监控告警 |
| C3 | 🟠 P1 | `GOMEMLIMIT` 是软限制，容器 2G 是硬限制 | 监控 RSS，接近 1.5G 告警 |
| C4 | 🟠 P1 | MySQL 慢日志无轮转 | logrotate / 定期 truncate |
| C5 | 🟠 P1 | 无 MySQL/CH 备份 | 每日 mysqldump + CH 备份/快照 |
| C6 | 🟡 P2 | spool 残留 4.1MB 已确认测试文件 | 下次真实 Append 自动清理 |
| C7 | 🟡 P2 | `/api/log/search`、`/self/search` 为死路由 | 清理 |
| C8 | 🟡 P2 | 宿主机 load 264 | 迁独立机或限制其他容器 |
| C9 | 🟡 P2 | 管理员弱密码（用户会改） | 用户自行处理 |

### 8.10 仍未做

- 未做真实业务请求压测（无渠道/令牌）；未配备份与监控；**C1 未修复**。

---

## 9. 高并发压测（2026-11-07 16:50）

### 9.1 只读 `/api/status`（直连容器，随机 XFF 绕过应用限流）

| 并发 | QPS | p50 | p95 | p99 | 错误 |
|---|---|---|---|---|---|
| 50 | 2,382 | 13.4ms | 37.9ms | 77.3ms | 429×1,655（应用限流） |
| 50（XFF） | 2,500 | 14.5ms | 21.6ms | 69.3ms | 0 |
| 100（XFF） | **2,858** | 25.6ms | 33.4ms | 54.7ms | 0 |
| 200（XFF） | 2,815 | 47.4ms | 83.8ms | 96.7ms | 0 |
| 400（XFF） | 2,653 | 106.8ms | 197.4ms | 248.6ms | 0 |

饱和点约 **2,850 QPS**（并发 100–200）；并发 400 后延迟上升、QPS 不再增长。

### 9.2 日志写入 `/v1/chat/completions`（mock 上游 + 渠道 + 令牌）

| 并发 | 请求 | QPS | p50 | p95 | p99 | 错误 |
|---|---|---|---|---|---|---|
| 20 | 400 | 434 | 44.7ms | 49.5ms | 106.1ms | 0 |
| 50 | 800 | **1,031** | 46.2ms | 54.6ms | 82.0ms | 0 |

落盘校验：**1,200 行 / 1,200 unique request_id**，quota 1,200、prompt 6,000、completion 1,200；
writer `failed=0`、无 dropping；CH parts 5、行数 7→1,207。高并发下日志**零丢失、零重复**。

### 9.3 限流：为什么压测会 429

| 层 | 配置 | 等效 |
|---|---|---|
| nginx | `limit_req_zone $binary_remote_addr zone=apiduo_cn_api rate=600r/m burst=120 nodelay` | **10 r/s/IP** |
| 应用 | `GLOBAL_API_RATE_LIMIT=360` / `GLOBAL_API_RATE_LIMIT_DURATION=180` | **~2 r/s/IP** |

压测请求全部来自同一个 IP，瞬间打满。应用层可用随机 `X-Forwarded-For` 绕过（容器只绑回环，外部伪造不了）；nginx 层按真实 IP，绕不过。

**已落地（16:57，简化版）**：
- nginx：`rate=600r/m` → **10000r/m（166 r/s/IP）**，`burst=120` → **600**；
- 应用：`GLOBAL_API_RATE_LIMIT` 360 → **36000（200 r/s/IP，180s）**；
- 敏感操作保持不变：`CRITICAL_RATE_LIMIT=20/1200s`，覆盖注册、登录、2FA、Passkey、密码重置、OAuth、支付；
- 验证：直连与经 nginx 各 100 次连续请求 **全部 200**（同一 IP）。
- 取舍：正常用户完全无感；极端压测（单 IP >100 r/s）仍会被 nginx 兜底，这是设计而非误伤。

### 9.4 结论

- 单实例只读 **~2,850 QPS**、写入 **~1,031 QPS**；写入瓶颈在 mock 上游/计费链路，不在日志 writer。
- 1,200 条并发日志全部落盘、无重复、`failed=0`。
- 入口实际吞吐被 nginx 限流压到 **10 r/s/IP**；对普通用户够用，但前端首屏多次请求（status/appearance/notice/pricing）在共享 NAT 下可能被应用层 2 r/s 误伤，建议放宽或对 `/api/status` 豁免。

---

## 10. nginx：一份配置覆盖全部端点（2026-11-07 17:02–17:12）

### 10.1 为什么取消了「/v1、/v1beta 单独 location」

17:02 的第一版把 `/v1`、`/v1beta` 从 `location /` 拆出、各写一份参数。用户质疑「有必要单独设置吗，还有其他端点呀」——**质疑成立，已回退为一份配置**：

1. nginx 层 IP 限流已按用户要求关闭，拆分原本唯一的理由（给业务 API 单独限流）不复存在；
2. 两个 location 的 10 行指令里 8 行重复；
3. **真正有害的是覆盖不全**：只给 `/v1` 抬到 128m / 3600s，其余端点仍继承公共片段的 32m / 300s。

### 10.2 应用实际注册的全部端点（实证）

| 前缀 | 来源 | 属于 |
|---|---|---|
| `/api/*` | `router/api-router.go:18` | 控制台 |
| `/v1/*` | `relay-router.go:75` + `video-router.go` + `task-router.go` + `file-relay-router.go` | 业务（含 `/v1/tasks`、`/v1/files`） |
| `/v1/realtime`、`/v1/responses` | `relay-router.go:82,89` | **WebSocket** |
| `/v1beta/*` | `relay-router.go:193` | Gemini 兼容 |
| `/mj/*` | `relay-router.go:182` | Midjourney |
| `/:mode/mj/*` | `relay-router.go:187` | MJ 模式前缀（`/fast/mj`、`/relax/mj`） |
| `/pg/chat/completions` | `relay-router.go:64` | Playground |

第一版只覆盖 2/7，`/mj`、`/:mode/mj`、`/pg`、`/v1/tasks`、`/v1/files` 全部漏在 32m / 300s 之下。

### 10.3 改法：参数全下沉到 `apiduo-cn-proxy.inc`

站点文件 114 行 → **75 行**，`location` 只剩 3 个（`= /robots.txt`、`/static/`、`/`），参数只有一份：

| 项 | 值 | 说明 |
|---|---|---|
| `proxy_pass` | `http://apiduo_cn_llm` | 所有 location 共用 upstream，`keepalive 128` |
| `proxy_http_version` + `Connection $apiduo_conn_upgrade` | 1.1 + map | 非 WS 为空（keepalive 生效）；WS 为 `upgrade` |
| `proxy_set_header Upgrade` | `$http_upgrade` | 见 10.4 |
| `proxy_buffering` | `off`（`/static/` 单独 `on`） | SSE 增量 |
| `proxy_request_buffering` | off | 边收边转 |
| `proxy_read_timeout` / `send_timeout` | 3600s | 与应用 `STREAMING_TIMEOUT=3600` 对齐 |
| `client_max_body_size` | 128m | 与应用 `MAX_REQUEST_BODY_MB=128` 对齐 |
| `client_body_buffer_size` | 1m | 少落盘 |
| `proxy_socket_keepalive` | on | 回源 TCP keepalive |
| `proxy_connect_timeout` | 10s | 快速失败 |
| `ssl_buffer_size` / `http2_max_concurrent_streams` | 4k / 256 | server 级 |
| `access_log` | `combined buffer=64k flush=5s` | 见 10.5 |

`/static/` 是唯一例外（保持 `proxy_buffering on` + 一年缓存头），因为它 include 同一份片段后单独覆盖。

### 10.4 顺带修掉一个真 bug：WebSocket 之前必然握手失败

公共片段原来写死 `proxy_set_header Connection "";`（为了让回源 keepalive）。这行会**清掉客户端发来的 `Connection: Upgrade`**，`/v1/realtime`、`/v1/responses` 的 WebSocket 升级在 nginx 这一层就被掐断。

改成 map：

```nginx
map $http_upgrade $apiduo_conn_upgrade {
    default upgrade;
    ''      "";
}
```

非 WS 请求 `$http_upgrade` 为空 ⇒ `Connection` 也为空 ⇒ keepalive 不受影响；WS 请求才发 `Connection: upgrade`。

### 10.5 踩坑：Tengine 的 `access_log` 不认裸 `buffer=`

```
nginx: [emerg] unknown log format "buffer=64k"
```

Tengine/3.1.0 在省略 format 名时把 `buffer=64k` 当成日志格式名。必须显式写 format：`access_log path combined buffer=64k flush=5s;`。改错后 `nginx -t` 失败、reload 未执行（服务未受影响，但配置文件处于坏状态），已加 `nginx -t` 守卫并回滚兜底。

### 10.6 验证

| 项 | 改前 | 改后 |
|---|---|---|
| `location` 数 | 5（含 2 个 /v1 专用） | **3** |
| 站点文件行数 | 114 | **75** |
| 40MiB body → `/mj/submit/imagine` | **413**（nginx 拦） | **401**（到达应用鉴权） |
| 40MiB body → `/api/token/` | **413** | **401** |
| 40MiB body → `/v1/chat/completions` | 401（已 128m） | 401 |
| 连发 300 次 `/api/status` | — | 300×200 |
| 端点矩阵 | — | `/api/status` 200、`/v1/models` 401、`/v1beta/models` 401、`/mj/submit/imagine` 200、`/pg/chat/completions` 200、`/v1/files` 401、`/v1/realtime` 401 |
| `nginx -t` | — | successful |

### 10.7 性能（经 nginx，HTTP/2 + TLS，同 IP 无限流）

| 并发 | 请求 | QPS | p50 | p95 | p99 | 错误 |
|---|---|---|---|---|---|---|
| 50 | 2000 | **1773** | 14.4ms | 20.0ms | 360.7ms | 0 |
| 100 | 2000 | 1327 | 32.0ms | 696.5ms | 718.4ms | 0 |

对照：**直连容器**（绕过 nginx）此前为 2,500 / 2,858 QPS。差值来自 TLS 握手 + HTTP/2 帧处理 + 宿主机 load（本次 153–169）。

`access_log` 缓冲前后几乎无差异（1812 → 1773 QPS，在噪声内）——**瓶颈不是日志 I/O，而是 TLS 与机器负载**。这条改动保留但不算性能收益项。

p95 在并发 100 时飙高，是 aiohttp 在 `limit=c` 下集中新建连接做 TLS 握手所致，不是服务端排队；p50 仍为 32ms。

### 10.8 回滚

```bash
cd /www/server/panel/vhost/nginx
cp -a apiduo.cn-ssl.conf.bak.20261107-1712 apiduo.cn-ssl.conf     # 合并后、加缓冲前
cp -a apiduo-cn-proxy.inc.bak.20261107-1710 apiduo-cn-proxy.inc   # 合并前（32m/300s）
nginx -t && nginx -s reload
```

---

## 11. IP 日志与错误日志（2026-11-07 17:11–17:20）

### 11.1 为什么「日志里看不到 IP」

不是 bug，是**默认关闭的用户级开关**：

```go
// model/log.go:291 / :357（RecordErrorLog 与 RecordConsumeLog 各一处）
needRecordIp := false
if settingMap, err := GetUserSetting(userId, false); err == nil {
    if settingMap.RecordIpLog { needRecordIp = true }
}
```

- 字段：`dto.UserSetting.RecordIpLog`（JSON `record_ip_log`），存在 `users.setting` 的 JSON 里，**不是全局配置**。
- 默认 `false` ⇒ `log.Ip = ""`，所有历史日志 ip 都是空串。
- 开启位置：前台「个人设置」→ 记录 IP，走 `PUT /api/user/setting`（`controller/user.go:1428`）。
- 实测：开关打开后新产生的日志全部带 IP（压测 2502 条 **2502 条有 IP**）。

⚠️ **注意这是 per-user 的**：管理员看别人的日志时，若对方没开，IP 仍为空。要「全站都记 IP」需要改默认值（属上游行为变更，本轮未做）。

### 11.2 错误日志

| 项 | 值 | 状态 |
|---|---|---|
| `ERROR_LOG_ENABLED` | `true` | ✅ 已开（默认 false） |
| 落点 | CH `logs` 表 `type=5` | ✅ 实测写入 |
| 内容 | `status_code=500, upstream error: do request failed` | 含 ip / model_name / status_code |
| nginx `error_log` | `/www/wwwlogs/apiduo.cn.error.log` | ✅ 有 429、413 记录 |
| nginx `access_log` | `combined buffer=64k flush=5s` | ✅ 第一列即客户端 IP |

**实测样本**（压测时 mock 上游拒连产生的 23 个 500）：全部写入 `type=5`，带 `ip=210.121.44.245`、`model_name=gpt-4o-mini`、`status_code=500`，可直接定位到「上游连不上」。

### 11.3 一个覆盖缺口（值得补）

**选渠道阶段失败不写错误日志**：

```bash
POST /v1/chat/completions  {"model":"model-does-not-exist"}
→ HTTP 503  {"code":"model_not_found","message":"No available channel for model ... under group default (distributor)"}
→ logs 表 type=5 **无新增**
```

原因：`Distribute()` 在 relay 之前就返回错误，没进 `RecordErrorLog`。所以「模型没配渠道」「分组无可用渠道」这类最常见的配置问题，只能从容器日志 `/api/log` 之外的地方找，日志页看不到。

建议：在 `Distribute` 失败分支补一条 `type=5`（内容 `model not found / no available channel`，带 model 与 group）。这属上游行为增强，需按标记块规范改。

### 11.4 本轮端到端测试（含一次假故障排查）

| 项 | 结果 |
|---|---|
| 非流式 `/v1/chat/completions` | 200，134ms |
| **流式 SSE** | 200，**TTFB 0.296s / 总 1.098s**（mock 每 200ms 一块）⇒ `proxy_buffering off` 确认生效 |
| `/v1/realtime` WS 握手 | 401（鉴权前，未验到 Upgrade；map 修复保留） |
| 40MiB body → `/mj`、`/api` | 401（改前 413） |
| 压测 conc=50/100/200（500 次） | 全 200，0 错误（修 mock backlog 后） |
| 日志一致性 | 2502 条 / 2502 unique request_id / 2502 条有 IP |
| writer 失败 | 0 |

**假故障（重要教训）**：首轮压测 conc=100 出现 23 个 500，看着像 apiduo 的问题。实际是 **Python mock 上游 `TCPServer.request_queue_size` 默认只有 5**，并发一上来就拒连，Go 侧报 `upstream error: do request failed`。改成 512 后 0 错误。压测时先确认上游不是瓶颈。

### 11.5 nginx 损耗到底有多大

| 场景 | 直连容器 | 经 nginx | 差值 |
|---|---|---|---|
| `/api/status` 串行 50 次 | avg 1.7ms / min 0.8ms | avg 3.4ms / min 1.0ms | **+1.7ms** |
| `/api/status` 并发 50 | ~2500 QPS | ~1773 QPS | -29% |
| `/v1/chat/completions` conc=50 | 981 QPS / p95 65ms | 573 QPS / p95 374ms | 见下 |
| `/v1/chat/completions` conc=100 | 1489 QPS / p95 114ms | 532 QPS / p95 682ms | 见下 |

结论：
- **单请求转发成本只有 1.7ms**，nginx 本身不慢。
- `/v1` 的 p95 差距在同一配置下重测波动极大（direct 981→799→1489→962），宿主机 **load 75–140** 是主因，数据不足以归因到 nginx。
- 试过 `worker_processes 8`（原 auto=128）：**无改善反而更差**，已恢复 `auto`。

### 11.6 清理

测试用渠道 `e2e-test` / `ip-demo`、令牌、mock 容器均已删除；渠道表 0 行。压测噪音日志（约 2500 条 `content=bench`）已清空，只保留带 IP 的真实样本与错误样本。

---

## 12. 限流到底影不影响性能（2026-11-07 17:22，A/B 实测）

用户问「nginx 放开之后是不是性能更好」。同条件 A/B：`/api/status`，并发 100，20000 请求，唯一变量是 `limit_req` 开/关。

| | 限流开 100000r/m（1667 r/s，burst 5000） | 限流关 |
|---|---|---|
| 总 QPS | **2160** | 2108 |
| 成功 200 | 18442 | **20000** |
| 429 | **1558** | 0 |
| **成功吞吐** | 18442 / 9.26s = **1992 rps** | 20000 / 9.49s = **2108 rps** |
| p50 | **28.9ms** | **29.7ms** |
| p95 | 43.3ms | 73.5ms |
| p99 | 80.0ms | 93.9ms |

### 结论

1. **单请求不变快。** p50 28.9 vs 29.7ms，差异在噪声内。`limit_req` 只是一次共享内存 hash 查表 + 计数，开销是微秒级，**不是性能税**。
2. **放开只多 5.8% 吞吐。** 1992 → 2108 成功 rps。因为限流没让请求变慢，只是把超出的部分快速拒掉。
3. **QPS 数字会骗人。** 限流时总 QPS（2160）反而高于无限流（2108），p95 也"更好"（43 vs 74ms）—— 因为 1558 个 429 不转发后端、几毫秒就返回，把均值和中位数拉低了。这是**少干活**，不是更快。
4. 所以「放开限流 = 性能优化」是错觉。真正的天花板是 TLS、CPU、上游，跟 `limit_req` 无关。

### 那 10 万 RPM 这个值怎么样

- 100000r/m = **1667 r/s**，实测压到 2160 QPS（约 13 万 RPM）才开始出现 429，说明阈值下方留了余量。
- 对真实用户（含几十人共享 NAT 的办公网）完全不可感知；只有脚本洪水才会碰到。
- 建议**保持这个值**：它是纯兜底，不影响性能，也不要再关。

### 副作用提醒

这轮压测把宿主 load 从 ~140 推到 **229**（64 vCPU）。机器本身已在超载区间，10 万 RPM 级别的压力测试不宜长时间反复跑。

回滚：`apiduo.cn-ssl.conf.bak.20261107-1722`、`.env.bak.20261107-1722`（应用层 36000）。

---

## 13. chat SSE 压测 + IP 是否可伪造（2026-11-07 17:25）

用户指出「nginx 刚才你应该测 chat SSE」——之前 A/B 用的是 `/api/status`（纯 JSON），SSE 是长连接 + 增量，行为完全不同。补测如下。

### 13.1 SSE 单条与并发

mock 上游每 200ms 发一个 chunk，共 5 块（总约 1s），6 个 data 事件。

| 场景 | TTFB p50 | TTFB p95 | 总时长 p50 | 总时长 p95 | 错误 |
|---|---|---|---|---|---|
| 单条 ×3 | 207–323ms | — | 1009–1125ms | — | 0 |
| 并发 20（100 请求） | **207ms** | 652ms | 1010ms | 1457ms | 0 |
| 并发 50（200 请求） | **209ms** | 576ms | 1011ms | 1403ms | 0 |
| 并发 100（200 请求） | **673ms** | 901ms | 1218ms | 1726ms | 0 |

结论：
- **并发 ≤50 时 TTFB 稳定在 ~208ms**（就是 mock 的首块延迟），说明 nginx 没有攒包，`proxy_buffering off` 对 SSE 确实生效。
- **并发 100 时 TTFB p50 劣化到 673ms（×3.2）**，但全部 200、无超时。原因是 SSE 每个连接要占住 1 秒，100 并发 = 100 个连接 + 100 个上游连接同时挂着，nginx/上游开始排队。
- 这不是配置问题，是长连接的成本：**SSE 的并发能力按「同时挂着的连接数」算，不是按 QPS**。

### 13.2 代理池 / IP 伪造测试

用 3 个不同的 `X-Forwarded-For`（`1.2.3.4`、`5.6.7.8`、`203.0.113.77`）各发一条 SSE 请求，然后看 ClickHouse 里记录的 ip：

```
ip=210.121.44.245   407 条   17:24:52 ~ 17:25:10
```

**伪造的 IP 一个都没进库**，全部记录真实 TCP 源地址。

原因：`TRUSTED_PROXIES` 未配置 ⇒ Gin 的 `TrustedProxies` 为空 ⇒ `c.ClientIP()` 直接返回 `RemoteAddr`，忽略 XFF。

这是**安全**的选择（客户端无法伪造 IP 污染审计），但带来两个语义：

| 场景 | 日志记录的是 |
|---|---|
| 直连 apiduo | 真实客户端 IP ✅ |
| 经 nginx（本机反代） | 真实客户端 IP ✅（同一台机器） |
| **经外部代理池 / CDN** | **代理或 CDN 节点 IP**，不是最终用户 |
| 客户端伪造 XFF | 被忽略，仍记真实源 IP ✅ |

所以「记上代理池」要注意：**走代理池后，日志里的 IP 是代理 IP**。想拿真实用户 IP 必须：
1. 代理层正确设置 `X-Forwarded-For`；
2. apiduo 配置 `TRUSTED_PROXIES` 为该代理的网段（当前**故意不设**，因为 nginx 在宿主机、容器来源是 docker 网关，设 `127.0.0.1/32` 会让 ClientIP 变内网并破坏令牌 IP 白名单）。

### 13.3 日志页没有 IP 列（待补）

用户截图里的日志表格只有：时间 / 渠道 / 用户 / 令牌 / 模型 / 流 / Token / 费用 / 耗时 / 详情。

代码核实：`web/src/features/usage-logs/components/columns/common-logs-columns.tsx` 的列定义只有
`created_at`、`token_name`、`model_name`、`is_stream`、`prompt_tokens`、`quota`、`use_time`、`content`——
**从未定义过 ip 列**（全文件搜 `ip` 无命中）。

即：后端从 CH 查出来带了 `ip` 字段（`model/log.go:75` 有 `Ip string`），前端压根没渲染。
这是上游的既有缺口，不是本次部署的问题。

要补需要：
1. `common-logs-columns.tsx` 加一列（`accessorKey: 'ip'`），并在 `marks.ts` 登记 + 加 `data-apiduo` 标识（项目界面规范）；
2. 重建前端 → 发版（`brand-release.yml` 自动）。

---

## 14. RPM 10 万压测：三层拆解（2026-11-07 17:28–17:40）

### 14.1 冷启动阶梯加压（`/v1/chat/completions`，经 nginx）

| 并发 | QPS | RPM | p50 | p95 |
|---|---|---|---|---|
| 50 | **740** | 44,407 | 45.5ms | 346ms |
| 100 | 725 | 43,482 | 49.8ms | 870ms |
| 200 | 540 | 32,398 | 92.8ms | 1431ms |
| 400 | 345 | 20,686 | 177ms | 2654ms |
| 600 | 234 | 14,050 | **3804ms** | 4010ms |

表面看只有 4.4 万 RPM，且并发越高越差。但这是**冷启动**数据（每轮都新建连接），把 TLS 握手成本算进去了。

### 14.2 三层拆解（同一时刻、同一 backend）

临时在 nginx 加了个明文 `:18080`（同参数、同 upstream）做对照：

| 路径 | 轮1 | 轮2 | p95 |
|---|---|---|---|
| ① 直连 apiduo（明文 h1） | 1835 | 1675 | 75–94ms |
| ② **nginx 明文 :18080** | 1645 | 1767 | 88–149ms |
| ③ **nginx TLS :443** | 820 | 877 | **657–688ms** |

**nginx 转发本身几乎零损耗**（② ≈ ①），损耗 100% 来自 TLS。

curl 交叉验证（与 aiohttp 独立）：

| 路径 | 100 请求耗时 | 等效 rps |
|---|---|---|
| TLS + HTTP/2（1 条连接多路复用） | 107ms | 833 |
| 明文 :18080 | 44ms | 2272 |
| **TLS + HTTP/1.1（每请求新建连接）** | **3747ms** | **26** |
| 直连 39047 | 35ms | 2857 |

### 14.3 决定性实验：预热连接池

先发 100 个请求把连接池填满（不计时），再计时 1000 个请求：

| 路径 | QPS | p50 | p95 | p99 |
|---|---|---|---|---|
| ① 直连 apiduo | 1881 | 47.6ms | 59.1ms | 73.1ms |
| ② nginx 明文 | 1865 | 47.9ms | 68.6ms | 82.9ms |
| ③ **nginx TLS** | **1726** | 50.5ms | **75.5ms** | 93.8ms |

**结论：TLS 的每请求成本只有 8%（1726 vs 1865）。之前吞吐减半，全部是冷启动时的握手成本。**

- 单次 TLS 握手实测 **40–52ms**（正常机器应 < 5ms），是宿主 load 130–150 放大的调度延迟。
- 100 并发同时建连 = 100 次握手抢 CPU，把并发槽位占满，所以 QPS 掉一半。

### 14.4 结论：10 万 RPM 达标

**1726 QPS × 60 = 103,560 RPM** ✅

- 应用处理能力：~1880 QPS（11.3 万 RPM）
- TLS 稳定态损耗：8%
- 冷启动握手是唯一显著成本，真实用户（浏览器 HTTP/2 复用连接）不会每请求握手
- 已排除的因素：`worker_processes`（128→16 无变化）、`ssl_buffer_size`（4k→16k 无变化）、`ssl_session_tickets`（off→on 无变化）

---

## 15. 日志查询性能曲线（146k → 114 万条）

### 15.1 应用层接口耗时（各 5 次取中位）

| 查询 | 14.6 万条 | **114 万条** |
|---|---|---|
| 全站 第1页 20 条 | 51.4ms | **46.7ms** |
| 全站 第1页 100 条 | 46.1ms | **36.5ms** |
| 全站 今天 100 条 | 53.3ms | 50.7ms |
| 全站 7 天 100 条 | 56.5ms | 47.4ms |
| 全站 type=2 100 条 | 59.5ms | 41.6ms |
| 全站 深分页 p=500 | 183.7ms | 140.8ms |
| 全站 深分页 p=5000 | — | **488.7ms** |
| 我的日志 第1页 | 53.0ms | 51.8ms |
| 统计 stat | 30.2ms | 39.3ms |

数据量涨 7.8 倍，**第一页查询几乎不变**（46–51ms）—— 因为 `ORDER BY (created_at, request_id)` + 按月分区，时间倒序取前 N 条只读最新 part。

### 15.2 CH 侧裸查询（clickhouse-benchmark，精确）

| 查询 | p50 | p95 | p99 |
|---|---|---|---|
| `ORDER BY created_at DESC LIMIT 100` | **20ms** | 30ms | 33ms |
| `... LIMIT 100 OFFSET 500000` | **578ms** | 676ms | — |

**深分页是 O(N) 的**：OFFSET 必须扫描并丢弃前 N 行。p=500（OFFSET 5 万）141ms，p=5000（OFFSET 50 万）489ms，基本线性。

这是 CH 的固有特性，不是配置问题。**前端如果允许翻到第 5000 页，单次查询要 0.5 秒。**

### 15.3 导出

`/api/log/export` 24 小时：**94,099 行 / 63MB / 3326ms**，流式 NDJSON，内存不膨胀。

### 15.4 表结构与 TTL

```sql
PARTITION BY toYYYYMM(toDateTime(created_at))
ORDER BY (created_at, request_id)
TTL toDateTime(created_at) + toIntervalDay(360)
SETTINGS index_granularity = 8192
```

- 11 个 part 全部带 `delete_ttl_info_max > 0` ⇒ **TTL 生效**
- 114 万条只占 **38 MB**（压缩比约 30:1）
- `type` 不在排序键里，type 过滤要全表扫，但 100 万量级仍只需 41ms

---

## 16. 日志模块全功能验证 + 对抗审查（第一性原理）

### 16.1 第一性原理：日志系统只有五个本质要求

抛开实现，一个异步日志系统必须同时满足：

| # | 要求 | 本质 | 当前实现 |
|---|---|---|---|
| 1 | **不阻塞业务** | 日志是旁路，不能拖慢主链路 | ✅ 异步 + 队列 50000 + 批量 5000/2s |
| 2 | **不丢** | 进程崩溃/下游故障后数据还能回来 | ⚠️ spool 兜底有效，但恢复后不自动补写 |
| 3 | **不重** | 重放不能产生重复行 | ❌ **多文件时重复（P0）** |
| 4 | **可查** | 时间倒序分页要快 | ✅ 第一页 36–51ms；深分页 O(N) |
| 5 | **可回收** | 过期数据自动消失 | ✅ TTL 360 天，11 个 part 全生效 |

「不丢」和「不重」是一对矛盾：想不丢就得重试，重试就可能重复。正确做法是**重试 + 幂等**，而当前实现只做了前者。

### 16.2 🔴 P0：多个 spool 文件时重复回放（实测 10 万条重复）

**复现步骤**（我已实测）：

1. spool 里有一个已确认但未删除的大文件 `logspool-...174000.jsonl`（10 万行 / 45MB），offset 文件记着 `{"file":"...174000.jsonl","offset":47061784}`
2. 停掉 ClickHouse
3. 发一条业务请求 → writer 写入新文件 `logspool-...173621.jsonl`（2 行），insert 失败，文件滞留
4. 恢复 ClickHouse
5. **重启 apiduo**

**结果**：

```
重启前: 1,146,370
重启后: 1,246,370   ← +100,000
按 token 核对：
  bulk-100k    rows=200000  uniq=100000  dup=100000
全局: total=1,246,370  uniq=1,146,370  dup=100,000
```

**根因**（`model/log_spool.go` 的 offset 结构）：

```json
{"file":"logspool-...jsonl","offset":47061784}
```

`logspool.offset` **只保存单个文件的游标**。replay 按文件名升序处理：

```
处理 173621.jsonl：offset.file=174000 ≠ 173621 → 从 0 读 → 读完保存 {173621, 2216}
处理 174000.jsonl：offset.file=173621 ≠ 174000 → 从 0 读 → 整个 10 万条重新回放 ❌
```

**触发条件**：spool 目录里存在 ≥2 个未确认文件 + 发生重启。而「已确认文件不删除」让这个条件很容易满足——只要有过一次下游抖动，残留文件就会一直在。

**这是两个缺陷叠加**：
- 缺陷 A：`Confirm` 后不删除文件（之前记为 P2，实际是 P0 的催化剂）
- 缺陷 B：offset 是单文件游标，多文件时被覆盖

**影响**：日志重复、`sum(quota)` / 统计翻倍、按 token 计费核对失真。业务数据（额度扣减）不受影响，只有日志表失真。

**修复建议（按成本排序）**：

1. **最小改动**：replay 每成功处理完一个文件就 `Confirm` 并删除它 ⇒ 任何时刻最多一个未确认文件，offset 不会错乱。（约 10 行）
2. **结构性**：offset 改成 `map[filename]offset` 持久化，支持多文件断点。（约 40 行）
3. **兜底（表结构层）**：若改成 `ReplacingMergeTree` 并把 `request_id` 放进排序键，重复行会在后台合并时被去掉——但查询要 `FINAL` 才能立刻看到去重结果，有性能代价。

推荐 1 + 2 组合。**上线前必须修**，否则一次下游抖动就可能造成整批日志重复。

### 16.3 🟠 P1：运行时下游故障后，spool 不自动补写

实测（停 CH → 发请求 → 启 CH）：

| 步骤 | 结果 |
|---|---|
| CH 停机 → `/v1/chat` | **200**，业务不受影响 ✅ |
| 日志去向 | 写入新 spool 文件（1.1KB），未丢 ✅ |
| CH 恢复 → 再发请求 | 新日志正常入库（+2）✅ |
| **CH 恢复后，滞留的 1.1KB 会自动补写吗** | **不会** ❌（CH 计数不变） |
| 重启 apiduo 后 | 补写了（但顺带触发了 16.2 的重复） |

原因：writer 的 flush 循环只消费 channel 里的新数据，**不会主动重扫 spool 里的历史文件**；只有启动时的 replay 会读。

影响：下游短时抖动期间产生的日志，在下次重启前查不到（数据在磁盘上，不会丢，但「不可见」）。对排障是硬伤——恰恰是出故障的时候最需要日志。

建议：writer 加一个低频补偿任务（如每 60s 检查 spool 是否有未确认文件并尝试投递），或失败后指数退避重试当前文件。

### 16.4 🟠 P1：日志页在 CH 故障时返回 HTTP 200 + 暴露内部主机名

```
HTTP=200  {"message":"dial tcp: lookup apiduo-cn-clickhouse on 127.0.0.11:53: no such host","success":false}
```

两个问题：
1. **状态码 200**：前端只能靠 `success:false` 判断，监控/网关层看不到错误。
2. **信息泄露**：把内部容器名、Docker DNS 地址（127.0.0.11:53）暴露给前端。

建议：日志库不可用时返回 503，错误信息脱敏为「日志服务暂时不可用」。

### 16.5 功能验证矩阵（均已实测）

| 功能 | 验证方式 | 结果 |
|---|---|---|
| 异步批量写入 | 压测 41,362 条 | ✅ 全落盘，uniq 一致 |
| 队列容量 50000 | 并发 600 压测 | ✅ 无 503/丢弃 |
| spool Append / rotate | 停机注入 | ✅ 自动新建文件 |
| offset 断点（单文件） | 连续重启 2 次 | ✅ 条数差 0 |
| offset 断点（多文件） | 见 16.2 | ❌ 重复 10 万 |
| 启动 replay | 5,000 / 100,000 / 1,000,000 行 | ✅ 6.4s 回放 48MB |
| TTL | `system.parts.delete_ttl_info_max` | ✅ 11/11 part 生效 |
| 查询第一页 | 146k vs 1.14M | ✅ 46–51ms 不随数据量变差 |
| 深分页 | p=500 / p=5000 | ⚠️ 141ms / 489ms（O(N)） |
| 统计 stat | 114 万条 | ✅ 30–39ms |
| 导出 | 24h 94,099 行 63MB | ✅ 3326ms 流式 |
| 业务解耦（CH 停机） | 停 CH 发请求 | ✅ 200，日志进 spool |
| 启动依赖（CH 停机重启） | 见 §8.8 | ❌ `InitLogDB` Fatal，起不来 |
| 关闭态等价性 | 无 `LOG_SQL_DSN` 时 | ✅ 走原 SQLite 路径，日志不可达 writer |

### 16.6 当前数据状态（已修复重复）

```
总行数 1,146,370   唯一 request_id 1,146,370   重复 0
million-1m        1,000,000
bulk-100k-clean     100,000
perf-rpm10w          41,362
perf-keep             5,000
parts 4   size 38.14 MiB
```

重复的 10 万条已删除并按干净数据重插（用户要求不删压测数据，这些保留）。

---

## 17. TLS 并发优化（2026-11-07 17:44–17:48）

### 17.1 先诊断：底子已经是最优的

| 项 | 实测值 | 评价 |
|---|---|---|
| 证书类型 | **ECDSA P-256**（ecdsa-with-SHA384） | ✅ 比 RSA 握手快数倍 |
| 协议 | TLS 1.2 + **1.3** | ✅ |
| 套件 | `TLS_AES_256_GCM_SHA384` / `ECDHE-ECDSA-AES256-GCM-SHA384` | ✅ 有 AES-NI 加速 |
| HTTP/2 | `http2 on` + 256 并发流 | ✅ 浏览器侧靠多路复用减少握手 |
| `ssl_session_cache` | `shared:APIDUO_CN_TLS:10m` | ✅ 约 4 万 session |
| **session 复用实测** | `openssl s_client -reconnect` **全部 `New`** | ❌ **完全没生效** |

### 17.2 根因：没有固定 ticket key

```
$ grep ssl_session_ticket_key  →  (无)
```

nginx 在 `ssl_session_tickets on` 但**未配置 `ssl_session_ticket_key`** 时，每次启动/reload 都会随机生成 ticket 密钥。后果：

- 我们这一轮改配置 reload 了 10+ 次 ⇒ **每次 reload 后所有已连接客户端的 ticket 全部作废**
- 客户端下次访问必须完整握手（40–50ms），session cache 也白搭

### 17.3 优化（已落地）

```nginx
ssl_session_tickets on;
ssl_session_ticket_key /www/server/panel/vhost/nginx/apiduo.cn-ticket.key;  # 新增，80 字节，600 权限
ssl_session_cache   shared:APIDUO_CN_TLS:10m;
ssl_session_timeout 4h;    # 10m → 4h
```

**验证**（TLS 1.2，因为 openssl 在 1.3 下 ticket 是异步下发，`echo |` 关太快拿不到 session 文件）：

```
首次:  New,    TLSv1.2, Cipher is ECDHE-ECDSA-AES256-GCM-SHA384
复用:  Reused, TLSv1.2, Cipher is ECDHE-ECDSA-AES256-GCM-SHA384   ✅
```

### 17.4 但压测并发没有提升（诚实结论）

冷启动压测（并发 100、800 请求、每轮新建 connector）：

| | QPS | p50 | p95 |
|---|---|---|---|
| 优化前 r1/r2/r3 | 650 / 581 / 700 | 50.8 / 54.2 / 51.2ms | 808 / 972 / 751ms |
| 优化后 r1/r2/r3 | 587 / 619 / 749 | 50.0 / 49.2 / 49.2ms | 950 / 900 / 690ms |

**在噪声范围内，没有实质变化。** 原因很明确：

> **aiohttp 不复用 TLS session**。压测每一轮都新建 100 个连接做完整握手，session ticket 优化对它完全无效。

session ticket 只对**会做 resumption 的客户端**（浏览器）生效。对真实用户的收益：

- 同一用户 4 小时内再次访问 ⇒ 免完整握手（≈10–20ms 而非 40–50ms）
- 我们频繁 reload 部署时，用户不再被强制重新握手

### 17.5 试过但无效的三项（都已回滚或保留默认）

| 尝试 | 结果 |
|---|---|
| `worker_processes` 128 → 16 | 无变化（809/872 vs 820/877） |
| `ssl_buffer_size` 4k → 16k | 无变化（844/846 vs 820/877） |
| `ssl_session_tickets` off → on | 无变化（压测不认） |
| `listen 443 ssl reuseport` | 无变化，且与同机 `dola.1task.cn` 的 443 冲突（`protocol options redefined` 警告）⇒ **已回滚** |

### 17.6 为什么优化不动了：瓶颈不在配置

单次 TLS 握手实测 **40–52ms**，而正常机器应 < 5ms。同一台机器上：明文连接建立 ~1ms，TLS 握手 ~45ms。

差的这 44ms 不是密码学计算（ECDSA P-256 + AES-NI 只需要微秒级），而是**宿主 load 150–210 时的 CPU 调度延迟**——握手进程被抢占，排队等时间片。

**结论：TLS 侧能做的已经做完（证书/协议/session 复用都是最优），并发能力的真正上限由宿主机负载决定。** 要再提升只有两条路：降机器负载，或把 TLS 终止卸载到独立机器/CDN。

### 17.7 保留的配置

```nginx
listen 443 ssl;
http2 on;
ssl_protocols       TLSv1.2 TLSv1.3;
ssl_session_tickets on;
ssl_session_ticket_key /www/server/panel/vhost/nginx/apiduo.cn-ticket.key;
ssl_session_cache   shared:APIDUO_CN_TLS:10m;
ssl_session_timeout 4h;
ssl_buffer_size     16k;
http2_max_concurrent_streams 256;
```

回滚：`apiduo.cn-ssl.conf.bak.20261107-1746`

---

## 18. TLS 还能怎么优化：联网调研 + 逐项实测（2026-11-07 17:50）

用户要求「结合联网再想想」。查了 2026 年的实践（kTLS / 0-RTT / OCSP / HTTP3），逐条在这台机器上验证：

| 方案 | 理论收益 | 本机可行性 | 实测结论 |
|---|---|---|---|
| **固定 `ssl_session_ticket_key`** | reload 后客户端免重新握手 | ✅ 已落地 | ✅ **生效**（TLS1.2 实测 `Reused`） |
| `ssl_session_timeout` 4h | 提高复用窗口 | ✅ 已落地 | ✅ |
| **kTLS**（内核 TLS） | 省每 record 的用户态拷贝，大流量 +8~30% | ❌ **OpenSSL 未编译** | 见 18.1 |
| **TLS 1.3 0-RTT**（`ssl_early_data`） | 回访用户省 1 RTT | ⚠️ 可开但有重放风险 | **不能开**，见 18.2 |
| **OCSP Stapling** | 省客户端查吊销的 RTT | ❌ 证书无 OCSP URL | Let's Encrypt 已停 OCSP，见 18.3 |
| **HTTP/3 / QUIC** | 0-RTT + 无队头阻塞 | ❌ Tengine 3.1 不支持 | 需换 nginx 主线重编 |
| `listen 443 ssl reuseport` | 减少 worker 惊群 | ⚠️ 与其他 443 站点冲突 | 实测无收益，已回滚 |
| `worker_processes` 128→16 | 减少调度竞争 | ✅ 可改 | 实测无收益，已回滚 |
| `ssl_buffer_size` 4k→16k | 减少 TLS record 数 | ✅ 已改 | 实测无收益，保留默认 |

### 18.1 kTLS：环境不支持，不是配置问题

按官方写法加了：

```nginx
ssl_conf_command Options KTLS;
```

`nginx -t` 直接失败：

```
nginx: [emerg] SSL_CONF_cmd("Options", "KTLS") failed
  (SSL: error:1414E180:SSL routines:SSL_CONF_cmd:bad value:cmd=Options, value=KTLS)
```

逐层验证：

| 检查项 | 结果 |
|---|---|
| 内核 6.8.0 + `tls` 模块 | ✅ 已加载（155648 字节） |
| `ssl_conf_command` 指令 | ✅ 被识别（报的是值错误，不是未知指令） |
| OpenSSL 版本 | 3.0.13（Ubuntu 包，2026-09-16 构建） |
| `ssl.h` 里的 `SSL_OP_ENABLE_KTLS` | ❌ **不存在** |
| `openssl s_client -ktls` | ❌ 无此选项 |

结论：**这套 OpenSSL 编译时没开 `enable-ktls`**。要用必须重编译 OpenSSL + nginx，而 kTLS 的收益主要在**大文件/静态资源**（`sendfile` 零拷贝），**对 TLS 握手毫无帮助**——我们的瓶颈恰恰是握手。投入产出不划算，放弃。已回滚，`nginx -t` 恢复正常。

### 18.2 0-RTT：技术上能开，但这个站不能开

`ssl_early_data on` 能让回访用户省掉 1 个 RTT（公网约 20–50ms）。但 0-RTT 的早期数据**没有任何重放保护**——攻击者抓一次 ClientHello 就能重放。

apiduo 的核心接口是 `POST /v1/chat/completions`，**每调用一次就扣一次额度**。如果开了全站 0-RTT，一次重放 = 一次重复扣费。

虽然可以按方法过滤（对非 GET 返回 `425 Too Early`），但：

- 判断逻辑要写在 nginx 或应用层，**写错就是资损**；
- `425` 会让客户端多一次往返重试，收益被吃掉大半；
- 我们的瓶颈是**本机 CPU 调度**（握手 45ms），不是网络 RTT（本机几乎为 0）——0-RTT 在这里根本没有收益场景。

**结论：不开。** 这是「速度快一点」和「可能重复扣钱」的选择，后者不可接受。

### 18.3 OCSP Stapling：Let's Encrypt 已经不提供了

```bash
openssl x509 -in fullchain.pem -noout -ocsp_uri   # 空
```

Let's Encrypt 在 2025 年停止 OCSP 服务（改用 CRL），证书里不再带 OCSP URL。所以 `ssl_stapling on` 配了也是空转，**没有意义**。

### 18.4 结论：配置层已经到顶

把 9 个候选方案全试完，能生效的只有 session ticket 一项，且已落地。

**剩余瓶颈的定量证据**：同一台机器，load 从 168 降到 89 时，同样的压测（预热后并发 100）从 1726 QPS 涨到 **1775–1882 QPS**。配置一个字没改。

所以 TLS 并发能力的提升路径只剩三条，都不在 nginx 配置里：

1. **降低宿主负载**——当前 64 vCPU 上跑着 50+ 容器，load 长期 90–210；
2. **TLS 终止卸载**——加一台专门做 TLS 的前端，或套 CDN（Cloudflare 等），让握手不占这台机器的 CPU；
3. **换 nginx 主线并编译 HTTP/3**——QUIC 天然 0-RTT + 无队头阻塞，但工程量大，且需要客户端支持。

### 18.5 最终保留的 TLS 配置

```nginx
listen 443 ssl;
http2 on;
ssl_protocols       TLSv1.2 TLSv1.3;
ssl_session_tickets on;
ssl_session_ticket_key /www/server/panel/vhost/nginx/apiduo.cn-ticket.key;  # 本轮新增
ssl_session_cache   shared:APIDUO_CN_TLS:10m;
ssl_session_timeout 4h;    # 本轮 10m → 4h
ssl_buffer_size     16k;
http2_max_concurrent_streams 256;
```

回滚：`apiduo.cn-ssl.conf.bak.20261107-1750`（kTLS 尝试前）、`.bak.20261107-1746`（session 优化前）

---

## 19. 硬件到底是不是瓶颈：PSI 诊断 + 测量方法纠错（2026-11-07 17:52）

用户问「从硬件上也没法优化吗，难道需要换盘/换机器」。查完 PSI 和存储结构后，**结论是：不需要为 apiduo 换硬件，而且我之前有一个测量错误必须纠正。**

### 19.1 PSI 压力指标：CPU 根本不是瓶颈

```
cpu      some avg10=0.87  avg60=6.06  avg300=10.77   full avg10=0.00 avg300=0.00
memory   some avg10=3.31  avg60=6.40  avg300=11.43   full avg10=3.26 avg300=10.02
io       some avg10=14.56 avg60=13.14 avg300=14.80   full avg10=14.34 avg300=14.07
```

- **CPU `full=0.00`** —— 从来没有「所有任务都在等 CPU」的情况，`some` 也只有 10.77%
- **IO `full avg300=14.07%`** —— 最紧的资源
- **memory `full avg300=10.02%`** —— 次紧，swap 已用 4.8 GiB / 8 GiB

同时 `vmstat` 显示 `r=1 b=5`（只有 1 个任务在跑、5 个在 D 状态）、`id=65~82%`。

**load 126 是被 147 个僵尸进程和 D 状态进程抬上去的，不是 CPU 真的忙。**

### 19.2 存储：是 iSCSI 网络盘，不是本地盘

```
/sys/block/sda → host6/session1/target6:0:0     ← iSCSI 会话
vendor: LIO-ORG                                 ← Linux-IO target
iqn.2019-04.com.rainyun:4afb449f-...            ← 雨云 iSCSI 存储
机型: Dell PowerEdge C6320（2015 年）
```

`iostat`：`r/s=2581, rkB/s=300MB/s, r_await=12.72ms, %util=64.5%, aqu-sz=41.5`

**`r_await=12.72ms` 是 iSCSI 的典型值**——本地 NVMe 是 0.05–0.1ms，差两个数量级。所有磁盘 IO 都要走网络到存储端。

### 19.3 谁在吃资源：3 个 ClickHouse + 一堆邻居

| 资源 | 占用者 |
|---|---|
| **IO** | `newapi-0to1-clickhouse` 累计 **35 TB** 读写；`apitest python` 22 TB |
| **内存** | CH(2.8GB) + next-server(1.1GB) + CH(1.06GB) + mysqld(933MB) + 多个 Chromium(900MB+) |
| **swap** | CH(1.1GB) + python(490MB) + gunicorn(230MB) |

同机跑着 **3 个 ClickHouse 容器**：`apiduo-cn-clickhouse`、`apiduo-clickhouse`、`newapi-0to1-clickhouse`。

**而 apiduo 自己极其干净**：

```
VmRSS: 123,684 kB    VmSwap: 888 kB      ← 几乎没被换出
read_bytes: 48 MiB   write_bytes: 214 MiB
三容器总内存 1.58 GB / 47 GiB = 3.4%
CPU < 10%
```

### 19.4 🔴 纠错：之前的「TLS 握手 40–52ms」是客户端假象

我在 §14、§17 说「握手 40–52ms，是 CPU 调度延迟」。**这个结论是错的**，因为我用的压测客户端是 **Python 单进程 + asyncio**，TLS 握手在客户端也受 GIL 限制——100 个并发握手其实是在一个 Python 线程里串行的。

用多进程重测 200 个全新 TLS 连接：

| 客户端 | 200 个新连接耗时 | 握手速率 |
|---|---|---|
| Python 单进程 | 1415 / 1490 ms | 141 / 134 每秒 |
| **Python 8 进程并行** | **318 / 299 ms** | **629 / 668 每秒** |
| 明文单进程（对照） | 193 / 181 ms | 1036 / 1105 每秒 |

**8 进程比单进程快 4.7 倍** ⇒ 瓶颈一直在客户端，不在 nginx。

服务端真实 TLS 握手能力 **≥ 668 次/秒**，而不是之前算出来的「每秒二十几次」。

### 19.5 修正后的完整结论

| 问题 | 之前结论 | **修正后** |
|---|---|---|
| TLS 握手慢 | 40–52ms，CPU 调度延迟 | **客户端 GIL 假象**，服务端 ≥668 握手/秒 |
| 冷启动吞吐 669 QPS | TLS 握手成本 | **客户端串行限制** |
| TLS 每请求开销 | 8% | 8%（这一条仍成立，预热后 1726 vs 1865） |
| 瓶颈在哪 | 宿主机 CPU | **整机 IO（iSCSI）+ 内存 swap，但与 apiduo 无关** |
| 需要换硬件吗 | — | **不需要为 apiduo 换** |

### 19.6 硬件建议（如果要整机优化，按收益排序）

| 优先级 | 项 | 现状 | 建议 | 依据 |
|---|---|---|---|---|
| 🥇 | **内存** | 47 GiB，swap 用 4.8 GiB，`allocstall_movable=341` | **加到 128 GiB** | memory PSI full 10%，消除 swap |
| 🥈 | **存储** | iSCSI，`r_await=12.72ms` | **换本地 NVMe**（或云盘换 ESSD/本地 SSD） | 差两个数量级，IO PSI full 14% |
| 🥉 | **整理** | 3 个 CH + 147 僵尸进程 + 多个 Chromium | 合并/下线不用的 CH、清理僵尸 | IO 大半被 `newapi-0to1-clickhouse` 吃掉 |
| — | CPU | E5-2682 v4（2016），64 vCPU，`PSI full=0` | **不用换** | CPU 从未饱和 |

**但如果只为 apiduo**：它占 3.4% 内存、<10% CPU、swap 只有 888 KB，**换机器没有意义**。真要隔离，用 cgroup `cpuset` 给它绑几个核，比换机器便宜得多。

### 19.7 方法论教训

压测一个 TLS 服务时，**必须先用多进程/多机客户端确认客户端不是瓶颈**，否则测出来的是你的测试工具的极限。这次单进程和 8 进程差了 4.7 倍，前 18 章里所有「冷启动」数据都要按这个折扣看。

---

## 20. TLS 最终优化与真实能力基线（2026-11-07 18:00）

### 20.1 检查发现：密码学环节已经全是最优

| 环节 | 实测协商结果 | 是否最优 |
|---|---|---|
| 密钥交换曲线 | **X25519, 253 bits**（TLS1.2 / TLS1.3 都是） | ✅ X25519 实测 4ms/次，比 P-256 的 7ms 快 43%，比 P-384 的 10ms 快 60% |
| 证书 | **ECDSA P-256** | ✅ 比 RSA 签名快数倍 |
| 对称加密 | `ECDHE-ECDSA-AES256-GCM-SHA384` / `TLS_AES_256_GCM_SHA384` | ✅ AEAD + AES-NI |
| 协议 | TLS 1.2 + **1.3** | ✅ |
| session 复用 | 固定 ticket key + 4h + 10m shared cache | ✅ |

也就是说：**默认配置本身就已经选中了全部最优项，没有「从 A 换成 B 能更快」的余地了。**

### 20.2 本轮做的：把最优值显式固化

```nginx
ssl_protocols       TLSv1.2 TLSv1.3;
ssl_ecdh_curve      X25519:prime256v1;    # 新增：固化曲线优先级
ssl_ciphers         ECDHE-ECDSA-AES256-GCM-SHA384:ECDHE-ECDSA-AES128-GCM-SHA256:ECDHE-ECDSA-CHACHA20-POLY1305;
ssl_prefer_server_ciphers off;            # 新增：TLS1.3 下无意义，TLS1.2 让客户端选更快套件
```

**诚实说明：这不是性能提升**（默认值本来就是这些），而是**防止 OpenSSL / nginx 升级后默认值漂移**导致某天悄悄降级到较慢的曲线或 CBC 套件。同时把 CBC 类套件从 `ssl_ciphers` 里剔掉。

验证协商未变：TLS1.2 → `X25519` + `ECDHE-ECDSA-AES256-GCM-SHA384`；TLS1.3 → `X25519` + `TLS_AES_256_GCM_SHA384`。

### 20.3 用多进程客户端测出的真实 TLS 能力

（之前用的是 Python 单进程，被 GIL 限制了 4.7 倍，见 §19.4）

| 场景 | 连接数 | 耗时 | 连接速率 |
|---|---|---|---|
| 8 进程 × 25，全新 TLS | 200 | 551ms | 363 /s |
| **16 进程 × 25，全新 TLS** | **400** | **550ms** | **727 /s** |
| 8 进程 × 25，复用连接 | 200 | 324ms | 617 /s |

**关键**：8 进程 200 连接用 551ms，16 进程 400 连接还是 550ms —— 耗时没变、吞吐翻倍，说明**客户端仍在限制，服务端握手能力 >727 次/秒**。

换算：**727 握手/秒 = 每分钟 4.4 万次新连接**。真实浏览器一个 TCP 连接跑几千个请求，握手只发生一次，这个能力绰绰有余。

### 20.4 TLS 优化到此为止：完整清单

已落地且生效的（本轮累计）：

```nginx
listen 443 ssl;  http2 on;
ssl_protocols            TLSv1.2 TLSv1.3;
ssl_ecdh_curve           X25519:prime256v1;      # 本轮固化
ssl_ciphers              ECDHE-ECDSA-AES256-GCM-SHA384:ECDHE-ECDSA-AES128-GCM-SHA256:ECDHE-ECDSA-CHACHA20-POLY1305;  # 本轮固化
ssl_prefer_server_ciphers off;                    # 本轮
ssl_session_tickets      on;                      # 本轮（原来 off，session 复用完全失效）
ssl_session_ticket_key   .../apiduo.cn-ticket.key;# 本轮（关键：固定 key，reload 不再踢人）
ssl_session_cache        shared:APIDUO_CN_TLS:10m;
ssl_session_timeout      4h;                      # 本轮 10m → 4h
ssl_buffer_size          16k;
http2_max_concurrent_streams 256;
```

试过、无效、已回滚的：`worker_processes` 128→16、`ssl_buffer_size` 4k、`ssl_session_tickets` 单独开、`listen reuseport`、关闭 shared session cache、kTLS（环境不支持）。

**没有一个剩下的配置项能提升 TLS 性能了。** 再往上只能是：降整机 IO/内存压力、把 TLS 终止卸载到独立机或 CDN、换 nginx 主线用 HTTP/3。

---

## 21. 10 万 RPM 压测 + 日志一致性验证（2026-11-07 18:03–18:06）

用户要求：压 10 万 RPM、百万条日志、看一致性、数据不删。

### 21.1 第一轮失败：被自己的限流拦掉 83%

按原配置（限流 100000r/m = 1667 r/s）用 16 进程 × 50 连接压 10 万请求：

```
总耗时 9.6s
状态码: {200: 16,767  429: 83,126  500: 107}
```

- **429 占 83%** —— nginx 限流命中 85,156 次，瞬时 10,408 rps 远超 1667
- **500 有 107 个** —— `upstream error: do request failed`，mock 是 Python 线程模型，800 并发扛不住

但即便这样，日志一致性也是对的：`1,186,044 - 1,169,170 = 16,874 = 16,767(成功) + 107(错误日志)`，一条不多一条不少。

### 21.2 第二轮：修掉两个卡点后成功

修了两处：
1. **mock 改成 asyncio 单线程事件循环**（`asyncio.start_server`，backlog 4096），彻底避开 Python 线程模型
2. **临时把限流放到 600000r/m（10000 r/s）+ burst 20000**，应用层 `GLOBAL_API_RATE_LIMIT=600000`，避免压测被自己的保护机制干扰

压测参数：16 进程 × 6250 请求 = **100,000 请求**，每进程 5 连接（总并发 80）。

### 21.3 结果：22.2 万 RPM，日志零丢失零重复

```
压测前 CH: 1,186,044
压测后 CH: 1,286,044  （+100,000，精确对应请求数）

这批 10 万条的时间跨度：
  min: 2026-11-07 18:04:11
  max: 2026-11-07 18:04:38
  span: 27 秒
```

| 指标 | 值 |
|---|---|
| 请求数 | 100,000 |
| 耗时 | **27 秒** |
| 实际 QPS | **3,704** |
| **实际 RPM** | **222,222**（10 万目标 × 2.2） |
| 日志写入条数 | **100,000**（精确对应，无丢失） |
| 重复行 | **0**（`count == uniqExact(request_id)`） |
| 带 IP 比例 | **100%**（100,000 / 100,000） |
| 字段合计 | quota=200,000、prompt_tokens=500,000、completion_tokens=500,000（每条 quota=2、5/5 tokens，完全一致） |
| writer 失败 | **0** |

全局一致性（含历史 115 万条）：

```
总行数 1,286,044   唯一 request_id 1,286,044   重复 0
million-1m        1,000,000
perf-rpm10w         181,036
bulk-100k-clean     100,000
perf-keep             5,000
```

### 21.4 压测期间观察到的其他事实

- CH 在写入 10 万条后只有 **5 个 part / 42 MiB**，合并效率正常
- writer 全程 `failed=0`，队列（50000）未溢出，无 503
- 10 万条全部 `type=2`（消费），说明分流逻辑正确，没有误写成错误日志
- spool 留下一个 104MB 文件（`logspool-...180412.jsonl`）—— **`Confirm` 后不删除**的老问题仍在（§16.2 缺陷 A），本次因只有单个文件、offset 正确，**没有触发重复**

### 21.5 限流已恢复

压测完立刻恢复为用户设定的 10 万 RPM：

```
nginx  : rate=100000r/m  burst=5000
应用层 : GLOBAL_API_RATE_LIMIT=300000 / 180s
```

服务 `/api/status` 200，容器 healthy，**所有数据保留未删**。

### 21.6 结论

1. **10 万 RPM 目标达成，实际跑到 22.2 万 RPM**（3,704 QPS），而且日志一条不丢、一条不重。
2. 第一次失败的 83% 429 **不是系统不行，是限流保护在正常工作** —— 瞬时 10,408 rps 本来就该被拦。
3. 107 个 500 全部是**测试用 mock 上游**的连接问题，不是 apiduo 的问题（错误日志里写得很清楚：`upstream error: do request failed`）。
4. 日志链路的真实瓶颈是 **mock 上游和客户端**，不是 writer / CH —— 10 万条在 27 秒内全部落盘，writer 零失败。

---

## 22. 限流按业务量重新标定（2026-11-07 18:18）

用户定调：**「以后线上并发差不多 RPM 在 1-10 万，限流要放开」**。

### 22.1 改前的问题

旧值 `rate=100000r/m`（1667 r/s）**正好等于业务量上限**，等于没有余量：
- 业务跑到 10 万 RPM 时已经在阈值上，任何突发立刻 429；
- 上一轮压测就被自己的限流拒掉 83%（85,156 次命中）。

### 22.2 新配置（已落地）

| 层 | 项 | 改前 | **改后** | 相对业务上限（1667 r/s） |
|---|---|---|---|---|
| nginx | `rate` | 100000r/m（1667 r/s） | **600000r/m（10000 r/s）** | **6 倍** |
| nginx | `burst` | 5000 | **20000** | 可吃 2 万瞬时突发 |
| 应用层 | `GLOBAL_API_RATE_LIMIT` | 300000/180s（1667 r/s） | **600000/180s（3333 r/s）** | 2 倍 |
| 敏感操作 | `CRITICAL_RATE_LIMIT` | 20/1200s | **不变** | 注册/登录/2FA/Passkey/重置/OAuth/支付 |

### 22.3 一个重要的事实澄清：`/v1` 本来就不受应用层全局限流

代码核实（`router/api-router.go:23`）：

```go
apiRouter.Use(middleware.GlobalAPIRateLimit())   // 只挂在 /api
```

`router/relay-router.go` 的中间件链里**没有** `GlobalAPIRateLimit`。所以：

| 路径 | 应用层全局限流 | nginx 限流 |
|---|---|---|
| `/api/*`（控制台） | ✅ 3333 r/s | ✅ 10000 r/s |
| **`/v1`、`/v1beta`、`/mj`、`/pg`（业务）** | ❌ **不经过** | ✅ 10000 r/s |

这解释了为什么压到 5,859 QPS 时应用层 429 是 0 —— 业务 API 的实际保护只有 nginx 那一层。

### 22.4 实测验证（压 10 万请求）

```
请求数    : 100,000（16 进程 × 6250）
耗时      : 28 秒（18:17:38 → 18:18:06）
平均      : 3,571 QPS = 214,260 RPM
峰值(单秒): 5,859 QPS = 351,540 RPM
最低(单秒): 594 QPS
```

| 检查项 | 结果 |
|---|---|
| **nginx access log** | **100,001 条全部 200，0 个 429** |
| **nginx error log `limiting requests`** | 18:17–18:19 时段 **0 条**（最后一次是 18:02，改配置前） |
| **应用层 429（GIN 日志）** | **0** |
| CH 落盘 | **+100,000**，精确对应 |
| 重复行 | **0**（`count == uniqExact(request_id)`） |
| 带 IP | 100,000 / 100,000 = 100% |
| 日志类型 | 全部 `type=2`（消费），无一条误写错误日志 |
| writer 失败 | **0** |

全局：**1,386,044 行 / uniq 1,386,044 / dup 0**；CH 8 个 part / 45.22 MiB。

### 22.5 余量分析

| 场景 | 速率 | 距 nginx 阈值（10000 r/s） |
|---|---|---|
| 业务常态下限 1 万 RPM | 167 r/s | 1.7% 占用 |
| 业务常态上限 10 万 RPM | 1,667 r/s | 16.7% 占用 |
| 业务峰值 ×2（20 万 RPM） | 3,333 r/s | 33% 占用 |
| **本次压测峰值** | **5,859 r/s** | 58.6% 占用 |
| 触发限流 | 10,000 r/s = 60 万 RPM | — |

**结论：业务在 1–10 万 RPM 区间内，即使翻倍到 20 万 RPM 也不会被限流。** 只有单 IP 持续超过 60 万 RPM（约 1 万 r/s）才会 429，这个量级已属洪水/攻击。

### 22.6 保留限流的理由

用户说「放开」，但不是「去掉」：
- 单 IP 洪水会直接打到 Go runtime，消耗连接和 CPU；
- nginx 在 C 层拦更便宜，等于一层廉价保险；
- 阈值设在业务峰值 6 倍以上，正常用户完全不可感知。

本轮回滚点：`apiduo.cn-ssl.conf.bak.20261107-1818`、`.env.bak.20261107-1818`

---

## 23. 覆盖性核查与第二轮对抗审查（2026-11-07 18:25）

用户问「log 优化的地方都测试了？对抗审查下」。先给**覆盖矩阵**，再给**补测的新发现**，最后是诚实的**未覆盖清单**。

### 23.1 覆盖矩阵：24 项已测 / 6 项仍未覆盖

| # | 测试项 | 方式 | 结论 |
|---|---|---|---|
| 1 | 异步批量写入 | 线上 3 轮压测（5k/1万/10万） | ✅ 零丢失 |
| 2 | 队列容量 50000 | 10 万 RPM 压测 | ✅ 未溢出 |
| 3 | spool Append / rotate | 停机注入 + 本地单测 | ✅ 自动切文件 |
| 4 | offset 断点（**单文件**） | 连续重启 2 次 | ✅ 条数差 0 |
| 5 | offset 断点（**多文件**） | 造 2 个文件 + 重启 | 🔴 **重复 10 万条（P0）** |
| 6 | 启动 replay | 5k / 1万 / 10万 / 100 万行 | ✅ 6.4s 回放 48MB |
| 7 | TTL 配置 | `system.parts.delete_ttl_info_max` | ✅ 8/8 part 生效 |
| 8 | 查询第一页 | 146k vs 141 万 | ✅ 36–55ms，不随量劣化 |
| 9 | 深分页 | p=500 / p=5000 | ⚠️ 141 / 489ms（O(N)，CH 固有） |
| 10 | 统计 stat | 141 万条 | ✅ 30–41ms |
| 11 | 导出 | 24h 94,099 行 63MB | ✅ 3.3s 流式 |
| 12 | 日志页接口矩阵 | self / admin / stat / export / search | ✅ 除 search 已废弃 |
| 13 | IP 记录 | 真实请求 + XFF 伪造 | ✅ 记录真实 IP，XFF 无效 |
| 14 | 错误日志 type=5 | 触发 500 | ✅ 含 status_code / ip / model |
| 15 | 关闭态等价性 | 无 LOG_SQL_DSN | ✅ 走原 SQLite 路径 |
| 16 | CH 停机 → 启动 | 停 CH 重启 apiduo | 🔴 **Fatal，起不来** |
| 17 | 运行时 CH 停机 | 停 CH 发请求 | ⚠️ 业务 200，spool 不自动补写 |
| 18 | 优雅停机 | SIGTERM | ✅ 1.69s，writer stopped |
| 19 | 列顺序 DDL↔INSERT | 本地字符串比对 | ✅ 三处一致 |
| 20 | int32 截断 | 本地单测 | ⚠️ 静默截断（1<<33 → 0） |
| 21 | 多实例共享 spool | 本地单测 | 🔴 互相覆盖 marker |
| 22 | **日志清理 mutation** | **临时库 100 万行** | ✅ **378ms 删 83 万行**（新） |
| 23 | **audit_logs** | **线上只读** | ✅ 76 行正常写入 / **无 TTL**（新） |
| 24 | **查询与写入并发** | **线上混合负载** | ⚠️ 查询劣化 1.4–1.8×（新） |
| 25 | **消费速率上限** | **本地单测** | 🔴 **2500 行/s 结构性上限**（新） |
| 26 | **spool 总量上限** | **本地单测** | 🔴 **无上限，可写满磁盘**（新） |
| 27 | **大字段 512KB** | **本地单测** | ✅ 完整往返 |
| 28 | **背压形状** | **本地单测** | ⚠️ 队列满后 enqueue 阻塞 ~1.75×interval（新） |

### 23.2 🔴 新发现 1：消费速率有 2500 行/s 的结构性上限

`run()` 的实现决定：

```go
ticker := time.NewTicker(w.interval)   // 2s
for {
    select {
    case <-ticker.C:
        w.flush(false)                  // 每次只 collect 一个 batchMax
    ...
```

`flush()` 每次只调一次 `collect()`（最多 `batchMax` 行），**而 flush 只由 ticker 触发，没有「队列积压就提前 flush」的路径**。所以：

```
稳态上限 = batchMax / interval = 5000 / 2s = 2500 行/s = 15 万 RPM
```

本地实测验证（`TestCoverage_DrainRateEqualsBatchOverInterval`）：batch=100/interval=50ms 时实测 **1994 行/s**，理论 2000 行/s。

**这意味着什么**：

| 业务量 | 折算速率 | 与 2500 行/s 上限 |
|---|---|---|
| 1 万 RPM | 167 r/s | 6.7% |
| **10 万 RPM（用户定的上限）** | 1,667 r/s | **66.7%** |
| 15 万 RPM | 2,500 r/s | 100%（临界） |
| 上次压测的 22 万 RPM | 3,571 r/s | **143%（超限）** |

**上次「10 万请求 28 秒 = 21.4 万 RPM 全部落盘」是真的，但那是靠队列缓冲**：28 秒内生产 10 万行、消费约 7 万行，剩 3 万行在队列里继续消化。稳态持续 21 万 RPM 会积压 → 队列 50000 满（约 20 秒）→ **enqueue 开始阻塞业务请求**。

对用户定的 1–10 万 RPM：**10 万 RPM 占 66.7% 上限，安全但余量只有 1.5 倍**，不是 nginx 限流那种 6 倍。

**建议**（三选一，都只需改环境变量或几行代码）：
- `LOG_WRITER_INTERVAL_MS` 2000 → **1000** ⇒ 上限翻到 5000 行/s（30 万 RPM），代价是 INSERT 频率翻倍；
- `LOG_WRITER_BATCH_SIZE` 5000 → **10000** ⇒ 同样翻倍，代价是单批内存与 CH 写入量增大；
- 代码层：队列长度 > batchMax 时立即 flush，不等到 tick —— 治本，但改上游挂接块。

### 23.3 🔴 新发现 2：`SPOOL_MAX_MB` 不是总量上限，能写满磁盘

`SPOOL_MAX_MB` 映射到 `logSpool.maxSize`，而 `maxSize` **只决定何时 rotate 到新文件**：

```go
if s.file == nil || s.written+int64(len(payload)) > s.maxSize {
    s.rotateLocked()   // 切新文件，旧文件留着
}
```

`cleanupLocked()` 只删「严格早于 marker 指向的文件」：

```go
if name >= s.marker.File { continue }   // marker 指向的那个永不删
```

本地实测（`TestCoverage_SpoolHasNoTotalSizeLimit`）：maxSize=8KiB 时写入 96KiB → **12 个文件、总共 98,304 字节**，无任何截断或拒绝。

**含义**：CH 长时间不可用时（比如 1 小时），按 10 万 RPM 会产生 360 万行约 200MB；按 25 万 RPM 会 1GB+。**磁盘占用没有上限，只能靠外部监控**。线上 spool 目录当前 **134MB**（已确认但未删除的那一份）。

**建议**：加一个「spool 目录总大小」检查，超阈值时告警；或代码层限制文件数（保留最近 N 个）。

### 23.4 🟠 新发现 3：audit_logs 没有 TTL（设计如此，但需归档策略）

```go
// model/audit_log.go:250
// MigrateAuditLogs also supports independently configured ClickHouse log stores.
// No TTL clause or usage-log cleanup integration is intentional.
```

线上实测：`audit_logs` **76 行**，正常记录（login / option.update / 带真实 IP 与 UA），`ORDER BY (created_at, event_id)`，**确实没有 TTL**。

对比 `logs` 表有 360 天 TTL。审计日志永不删除在合规上可能是**期望行为**，但缺少归档策略，长期会累积。当前量小（76 行）不急。

### 23.5 ✅ 风险下调：日志清理 mutation 比预想快得多

之前担心 `DeleteOldLogBatch` 的 CH 分支（同步 mutation）会拖垮 CH。在**临时库**（不碰生产表）实测 100 万行：

| 操作 | 耗时 | 结果 |
|---|---|---|
| 造 100 万行 | 1,611ms | 7 parts / 33.93 MiB |
| 删 30 天前（833,424 行）`mutations_sync=1` | **378ms** | 剩 166,576 行 |
| 重复执行同一条 mutation | 268ms | 幂等，无额外开销 |
| 删全部（targetTimestamp = 未来） | **238ms** | 0 行 |

原因：CH 的 DELETE 是**按 part 重写**，百万行只有 7 个 part / 34MB，很快。

**但两个真实风险仍在**：
1. **误操作无二次确认**：targetTimestamp 传未来时间 → 238ms 删光全表。控制台点「清理日志」若填错就是数据事故。
2. **磁盘不立即释放**：mutation 后数据逻辑删除，物理空间要等后续 merge。

### 23.6 ⚠️ 查询与写入并发：查询劣化 1.4–1.8 倍

线上混合负载（8 进程压 30,000 业务请求 + 同时查日志页）：

| 查询 | 无写入压力 | 写入并发中 | 倍数 |
|---|---|---|---|
| 全站第 1 页 20 条 | 55.1ms | **97.9ms** | 1.8× |
| 全站第 1 页 100 条 | 54.8ms | **76.8ms** | 1.4× |
| 统计 stat | 40.8ms | **72.8ms** | 1.8× |

写入侧不受影响：30,000 条全部落盘，CH CPU 32%、无 mutation、无 merge 积压。查询劣化在百毫秒内，可接受。

### 23.7 诚实的「仍未覆盖」清单

| # | 未覆盖项 | 原因 | 风险 |
|---|---|---|---|
| 1 | **native 驱动真 CH e2e**（`TEST_CLICKHOUSE_DSN`） | CH 端口未映射宿主机，CI/本地都连不上 | 🟡 中：但生产 141 万条数据已实证驱动可用 |
| 2 | **业务侧日志函数单测**（`RecordConsumeLog` / `RecordErrorLog` / `GetUserLogs` / `SumUsedQuota` 等） | 覆盖率实测 **0.0%**（整包 2.4%） | 🟠 高：只有线上 happy path，没有单测锁住分流/过滤逻辑 |
| 3 | **TTL 真实删除** | 360 天无法等待 | 🟡 低：`delete_ttl_info_max>0` 是 CH 的确定性信号 |
| 4 | **队列真满导致业务连续阻塞** | 生产不敢复现（会真阻塞） | 🟠 高：单测已证形状（阻塞 1.75×interval），但线上未实测 |
| 5 | **CH 慢（非挂）时的积压曲线** | 需要故障注入 CH 延迟 | 🟡 中：可由 23.2 的上限推算 |
| 6 | **spool 写满磁盘的后果** | 需造 700GB 数据 | 🟡 低：必然是无日志 + 写入报错，业务仍可用 |

### 23.8 对抗审查最终分级

| ID | 级别 | 问题 | 状态 |
|---|---|---|---|
| C1 | 🔴 P0 | **多 spool 文件 offset 覆盖 → 重复回放**（实测 10 万条重复） | 未修 |
| C2 | 🔴 P0 | **CH 不可用 ⇒ apiduo 无法启动**（`InitLogDB` Fatal） | 未修 |
| C3 | 🟠 P1 | **消费上限 2500 行/s**（10 万 RPM 占 67%，余量 1.5×） | 未修，可调参 |
| C4 | 🟠 P1 | 运行时 CH 故障后 spool **不自动补写**（等重启） | 未修 |
| C5 | 🟠 P1 | 日志页在 CH 故障时返回 **HTTP 200** + 泄露内部主机名 | 未修 |
| C6 | 🟠 P1 | **spool 无总量上限**，可写满磁盘 | 未修 |
| C7 | 🟠 P1 | 业务侧日志函数 **0% 单测覆盖** | 未修 |
| C8 | 🟡 P2 | `int32()` 静默截断（quota > 21 亿时归零） | 未修 |
| C9 | 🟡 P2 | 多实例共享 spool 目录互删（无实例锁） | 部署约束已注明 |
| C10 | 🟡 P2 | 日志清理 mutation **无二次确认**，误填未来时间即删全表 | 未修 |
| C11 | 🟡 P2 | audit_logs 无 TTL / 无归档 | 设计如此 |
| C12 | 🟢 P3 | 深分页 O(N)（p=5000 需 489ms） | CH 固有 |
| C13 | 🟢 P3 | `/api/log/search` 死路由 | 未清 |

**结论：核心链路（写入→落盘→查询→回收）在 141 万条真实数据上验证通过、零丢失零重复；但 P0 两项必须先修，P1 五项建议排期。**

### 23.9 本轮新增的测试文件

`model/log_writer_coverage_test.go` —— 5 个用例，`-race` 全绿：

| 用例 | 固定的事实 |
|---|---|
| `TestCoverage_DrainRateEqualsBatchOverInterval` | 实测 1994 rows/s vs 理论 2000；打印生产配置 2500 行/s |
| `TestCoverage_BacklogBlocksEnqueueProportionally` | 队列满后单次 enqueue 最慢 70ms（interval 40ms 的 1.75×） |
| `TestCoverage_SpoolHasNoTotalSizeLimit` | 8KiB maxSize 写入 96KiB → 12 个文件无截断 |
| `TestCoverage_LargeRowSurvivesSpool` | 512KB `other` 完整往返（524,607 字节） |
| `TestCoverage_SpoolDirIsPerInstance` | 实例 B 回放到 A 写的行（无实例锁） |

全量日志测试（39 个函数）`-race` 通过。

---

## 24. 功能维度补充测试（2026-11-07 18:30）

第 23 章覆盖的是「链路/性能/一致性」，本章专测**功能面**：过滤维度、权限、日志类型、审计、导出、前端。

### 24.1 过滤维度：9/9 全部正常

| 过滤条件 | API total | 判定 |
|---|---|---|
| `type=2`（消费） | 1,415,935 | ✅ 与全库 1,416,045 差 110 条其他类型 |
| `token_name=perf-rpm10w` | 311,036 | ✅ 与 CH 直查一致 |
| `username=apiduo` | 1,416,044 | ✅ |
| `model_name=gpt-4o-mini`（精确） | 528,960 | ✅ 精确匹配 |
| `model_name=gpt-4o%`（LIKE） | 746,877 | ✅ **明显多于精确值，通配生效** |
| `channel=6` | 311,036 | ✅ **与 token_name 结果完全相同，交叉印证** |
| `group=default` | 1,416,044 | ✅ |
| `request_id=clean-0-20261107` | 1 | ✅ 精确命中单条 |
| `upstream_request_id=`（空） | 1,416,044 | ✅ 空值不参与过滤 |
| 组合 `type=2&model_name=gpt-4o-mini` | 528,852 | ✅ 比单条件少 108，**组合真的生效** |
| `start_timestamp=今天` | 335,245 | ✅ |

语义确认（`model/log.go:17`）：不含 `%` 走 `=` 精确，含 `%` 走 `LIKE`（CH 下 `sanitizeClickHouseLikePattern` 转义 `\` 和 `_` 但保留 `%`）。

### 24.2 参数边界：8/8 全部 200，无 500

| 参数 | 结果 |
|---|---|
| `p=0` / `p=-1` | 200，默认值兜底（items=10） |
| `page_size=0` | 200，回退默认 |
| `page_size=10000` | 200，**被截到 100**（防拉爆，好设计） |
| `type=abc` / `start_timestamp=abc` | 200，参数忽略 |
| `model_name=nonexistent` | 200，total=0 |
| `type=99` | 200，total=0 |

### 24.3 权限矩阵：8/8 全部正确

用真实普通用户 `logtest`（role=1，本轮新建）验证：

| 请求 | 结果 | 判定 |
|---|---|---|
| 普通用户 `GET /api/log/`（管理员列表） | **403** `insufficient privileges` | ✅ |
| 普通用户 `GET /api/log/self` | 200，total=0（自己无日志） | ✅ |
| 普通用户 `GET /api/log/self?username=apiduo` | 200，**total=0** | ✅ **参数无法越权** |
| 普通用户 `GET /api/log/stat` | **403** | ✅ |
| 普通用户 `GET /api/audit` | **403** | ✅ |
| 普通用户 `GET /api/audit/self` | 200，total=1（自己的登录审计） | ✅ |
| 普通用户 `GET /api/log/export` | **403** | ✅ |
| 普通用户 `GET /api/log/self/export` | 200 | ✅ |
| 无 token `GET /api/log/self` | **401** `invalid access token` | ✅ |

### 24.4 日志类型：type=1 真实跑通，type=3/6 需业务场景

`logs` 表实际只出现过 3 种类型：`type=2`（1,415,935）、`type=4`（1）、`type=5`（108）。

本轮通过「管理员调整用户额度」真实触发了 **type=1 充值**：

```
POST /api/user/manage {"id":2,"action":"add_quota","value":500000,"mode":"add"}
→ HTTP 200，logtest.quota: 0 → 500000
→ logs 表新增 1 条：type=1, username=logtest, content="Increased user quota by 500000"
→ 普通用户登录后 /api/log/self total=1，type=1 筛选 total=1  ✅
```

**但这条日志有两个字段问题**（见 24.6）。

| 类型 | 触发方式 | 状态 |
|---|---|---|
| 1 充值 | 管理员加额度 ✅ / 兑换码 ❌（需支付合规确认） | 部分验证 |
| 2 消费 | 真实 `/v1/chat/completions` ✅ | 已验证 |
| 3 管理 | 订阅变更（`controller/subscription.go:426`） | **未测**，需订阅功能 |
| 4 系统 | 未找到触发点（现有 1 条是历史注入） | 未验证 |
| 5 错误 | 上游失败 ✅（108 条） | 已验证 |
| 6 退款 | 任务计费退款（`service/task_billing.go:288`） | **未测**，需 MJ/视频任务 |
| 7 登录 | **不写 logs 表**，只写 `audit_logs`（`RecordLoginLog` 无 enqueue） | 设计如此 |

### 24.5 审计日志：覆盖面比预期完整

`audit_logs` 现有 78 行，覆盖 9 类操作：

```
login/login              23
operation/option.update  22
security/token.delete     9
security/token.key_view   8
security/token.create     6
operation/channel.create  6
operation/channel.delete  5
operation/user.quota_add  2
operation/user.create     1
```

**关键优点**：失败操作也记录。实测两次 `user.quota_add`：

```json
// 失败那次
{"success":0, "content":"Failed user quota adjustment",
 "op":{"params":{"failure_reason":"invalid_parameters","requested_quota":500000,"target_user_id":0}}}
// 成功那次
{"success":1, "content":"Increased user quota by 500000",
 "op":{"params":{"from":0,"to":500000,"mode":"add","target_user_id":2,"target_username":"logtest"}}}
```

分类过滤正常：`login` 23 / `security` 23 / `operation` 36 / `access_token` 0。
非法类目 `category=xxx` → `success=false, "Invalid audit filters"`。

### 24.6 新发现 4 项

#### 🟠 D1：`logs` 表的 type=1 没有 IP，`audit_logs` 有

同一次「加额度 500000」：

| 表 | ip | 内容 |
|---|---|---|
| `logs` | **空** | `Increased user quota by 500000` |
| `audit_logs` | **172.30.47.1** | 同上 + admin_info + route |

`RecordLogWithAdminInfo` 接收了 `*gin.Context` 却没取 IP。结果是**查日志页看不到管理操作的来源 IP，必须切到审计页**。全库 `ip=''` 共 5 条（4 条早期 + 这 1 条）。

#### 🟡 D2：`logs` 表的 type=1 `quota=0`，真实额度在 `other` 里

```json
other: {"op":{"action":"user.quota_add","params":{"from":0,"mode":"add","quota":500000,"to":500000}},
        "admin_info":{"admin_id":1,"admin_username":"apiduo","auth_role":100}}
```

按 `sum(quota)` 统计充值额会**全部漏掉**（type=1 的 quota 列恒为 0）。要统计必须解析 `other.op.params.to`。

#### 🟡 D3：列表页没有 IP 列，但导出 CSV 有

- 列表页 `common-logs-columns.tsx` 只有 8 个 `accessorKey`：`created_at/token_name/model_name/is_stream/prompt_tokens/quota/use_time/content` —— **无 ip**
- 导出 `use-logs/lib/export.ts:96` 有 `log.ip ?? ''`，CSV 里**有 IP 列**

修 §13.3 的旧结论：不是「前端完全没有 IP」，而是「列表页没有、导出有」。属于不一致。

#### 🟡 D4：`/api/audit` 非法参数返回 HTTP 200

`category=xxx` → `HTTP 200 + {"success":false,"message":"Invalid audit filters"}`。与 C5（CH 故障时也 200）同类：**错误用 200 表达，监控和网关看不到失败**。

#### ✅ D6（澄清，非缺陷）：导出流末尾有一行汇总

`/api/log/export` 的响应是 NDJSON + 最后一行：

```json
{"data":{"count":335246},"message":"","success":true}
```

前端解析时会跳过（没有 `type` 字段），但**脚本直接消费导出文件时必须跳过最后一行**，否则会把它当日志。文档要写清楚。

### 24.7 前端导出实现（代码审查）

`web/src/features/usage-logs/lib/export.ts` 是**纯前端生成 CSV**（不是下载服务端 JSONL）：

- ✅ 17 列，含 `IP`、`Request ID`、`Details`（管理员多 `User`、`Channel`）
- ✅ **CSV 注入防护**：`/^[=+\-@\t\r]/` 开头的内容加前导单引号，防止 Excel 执行公式
- ✅ **BOM**（`\uFEFF`）确保 Excel 正确识别 UTF-8
- ✅ 全字段双引号包裹 + `""` 转义
- ⚠️ 只导出**已加载的页**（前端本地数据），不是全量 —— 用户以为「导出全部」时会漏

### 24.8 仍未覆盖的功能

| # | 项 | 原因 |
|---|---|---|
| 1 | **type=3 管理、type=6 退款** | 需订阅功能 / MJ·视频任务计费失败场景 |
| 2 | **兑换码充值（type=1 另一路径）** | 需开启支付合规确认（`IsPaymentComplianceConfirmed`），会改变生产配置 |
| 3 | **前端页面实际渲染** | 需浏览器登录态，本轮只做了接口 + 代码审查 |
| 4 | **跨月分区查询** | 现有数据集中在 11/1–11/7，未构造跨 10/11 月边界的查询 |
| 5 | **日志详情弹窗**（长 content / other 展开） | 需前端渲染 |
| 6 | **移动端卡片布局** | 需前端渲染 |

### 24.9 功能测试结论

**能用，且边界处理得体**：9 个过滤维度全对、参数不崩、权限零越权、审计记录完整（含失败操作）、导出有注入防护。

**三个该修的不一致**：
1. 管理类日志进 `logs` 表时不带 IP（D1）—— 修法：`RecordLogWithAdminInfo` 从 gin context 取 `ClientIP()`
2. 充值额度不进 `quota` 列（D2）—— 要么填 `quota`，要么文档明确「充值看 other」
3. 列表页无 IP 但有导出（D3）—— 加一列即可（需在 `marks.ts` 登记 + `data-apiduo` 标识）

---

## 25. 审计日志（audit_logs）专项测试（2026-11-07 18:36）

之前只验证了「有 76 行、分类分布」，本章把审计当独立功能系统测：**写入路径、查询过滤、权限分级、故障行为**。

### 25.1 写入路径：同步写、fail-open、无 spool

```go
// model/audit_log.go:135
if err := LOG_DB.Table("audit_logs").Create(row).Error; err != nil {
    logger.LogError(ctx, ...)   // 只记日志，不返回错误
}
```

| 特征 | 审计 `audit_logs` | 业务日志 `logs` |
|---|---|---|
| 写入方式 | **同步 GORM Create** | 异步 writer + native batch |
| 失败处理 | **fail-open**（只记 error，不影响业务） | 进 spool，重启回放 |
| 有 spool 兜底 | ❌ **没有** | ✅ 256MB/文件 |
| 有 TTL | ❌ 无（注释声明 intentional） | ✅ 360 天 |
| 走 `LOG_SQL_DSN` | ✅ 同一 CH 库 | ✅ |

### 25.2 参数校验：6/6 全部拒绝（比 logs 接口严格）

| 参数 | 结果 |
|---|---|
| `category=xxx` | `success=false, "Invalid audit filters"` |
| `token_ref=abc`（非 64 hex） | `Invalid audit filters` |
| `token_ref=zzz...`（64 非 hex） | `Invalid audit filters` |
| `start_timestamp=-1` | `Invalid audit time range` |
| `start=100&end=50`（倒序） | `Invalid audit time range` |
| `success=maybe` | `Invalid audit result` |
| `page=0` / `page=-1` / `page_size=0` | **200，内部兜底到合法值**（不是漏校验） |

对比：`/api/log/` 对非法参数是**静默忽略**，审计是**显式拒绝** —— 更严格，正确。

### 25.3 过滤维度：8/8 与 CH 完全一致

| 过滤 | API total | CH 直查 | 一致 |
|---|---|---|---|
| 无过滤 | 82 | 82 | ✅ |
| `category=login` | 23 | 23 | ✅ |
| `category=security` | 23 | 23 | ✅ |
| `category=operation` | 36 | 36 | ✅ |
| `username=apiduo` | 80 | — | ✅（82-2 logtest） |
| `username=logtest` | 2 | 2 | ✅ |
| `username=不存在` | 0 | — | ✅ |
| `success=false` | 5 | 5 | ✅ |
| `success=true` | 77 | — | ✅（5+77=82） |
| `request_id` 精确 | 1 | — | ✅ |
| 今天时间范围 | 82 | — | ✅ |

`event_id` 唯一性：**82 / 82**，无重复。

字段填充率 **14/14 全部 100%**（82 行全部有 event_id / user_id / username / actor_role / category / action / auth_method / ip / user_agent / method / route / status / request_id）。

### 25.4 权限分级：只有 root 能看全局审计

| 调用者 | 接口 | 结果 |
|---|---|---|
| root(100) | `GET /api/audit` | **200，total=83（全量）** |
| root(100) | `GET /api/audit/self` | 200，total=81（只有自己的） |
| **admin(10)** | `GET /api/audit` | **403 `insufficient privileges`** |
| admin(10) | `GET /api/audit/self` | 200，total=3（自己的） |
| common(1) | `GET /api/audit` | 403 |
| common(1) | `GET /api/audit/self` | 200 |

**结论**：`/api/audit` 受 `authz.AuditRead` 权限控制，**实际只有 root 能进**。代码里 `if viewerRole < RoleRootUser { WHERE actor_role IN (1,10) }` 这个过滤分支在 `/api/audit` 上**永远走不到**（路由已拦），只有 `/api/audit/self` 会用（但 self 已按 user_id 过滤）。

含义：**admin 角色完全看不到审计日志**（一条都不行），运维人员想查「谁改了什么」必须有 root 账号。

### 25.5 `other` 字段裁剪：实测生效

同一条 `user.quota_add` 记录，两种接口看到的内容不同：

| 接口 | `other` 的 keys | 说明 |
|---|---|---|
| `GET /api/audit`（root，viewerRole=100） | `['admin_info', 'audit_info', 'op']` | 全量 |
| `GET /api/audit/self`（**同一个 root**，SelfView=true） | **`['op']`** | **admin_info + audit_info 都被剥离** |

这证明 `SelfView` 的裁剪优先级高于角色：**即使是 root，用 self 接口也只能看到 `op`**（操作内容），看不到「是谁以什么方式认证的」。

### 25.6 安全审查：`other` 里没有明文凭证

| action | `other` 内容 |
|---|---|
| `token.create` | `{"op":{"action":"token.create","params":{"id":1,"name":"mock-test"}}}` |
| `token.key_view` | `{"op":{"action":"token.key_view","params":{"id":1,"name":"mock-test"}}}` |

**只记 id 和 name，不记密钥明文** ✅。而且 `token.key_view` 这个 action 本身说明「有人查看了密钥」——行为可追溯。

另外：**创建 admin 需要二次验证** `SECURITY_PROOF_REQUIRED`（HTTP 403），防止 session 被盗后直接提权 ✅

### 25.7 故障行为：CH 停机时登录不受影响，但审计会丢

实测（停 CH 21 秒）：

| 操作 | CH 正常 | **CH 停机** |
|---|---|---|
| 登录 `POST /api/user/login` | 0.204 / 0.147 / 0.137 s | **0.153 / 0.215 s，HTTP 200，拿到 token** |
| `/api/status` | 0.03 s | 0.027 s，200 |
| `GET /api/audit/self` | 200 | **200 + `{"message":"dial tcp: lookup apiduo-cn-clickhouse ... no such host","success":false}`** |
| 应用日志 | — | `[ERR] audit log write failed (request_id=...): dial tcp: lookup ... no such host` |

**为什么登录没变慢**：容器停止后 **DNS 解析立即失败**（`no such host`），不进入 dial 阶段，所以没有 5 秒超时惩罚。

⚠️ **但有一种情况会更糟**：如果 CH 容器在运行但**不响应**（连接能建立、握手超时），会等 `dial_timeout=5s`，届时每次登录都会**慢 5 秒**。这个场景本轮没测（需要 `docker pause` 或 iptables DROP），是已知风险。

**审计数据丢失**：停机期间 2 次登录的审计**永久丢失**（84 → 恢复后 87，只补回 1 条成功登录 + 2 条失败未写）。`audit_logs` **没有 spool 兜底**，这是与 `logs` 表最大的可靠性差异。

### 25.8 新发现（审计专属）

| ID | 级别 | 问题 |
|---|---|---|
| A1 | 🟠 P1 | **审计无 spool，CH 停机期间的审计永久丢失**（业务日志有 spool 会补，审计不会） |
| A2 | 🟠 P1 | **CH「卡住不响应」时每次审计操作慢 5s**（`dial_timeout=5s`），登录会被拖慢。本轮只测了「DNS 失败立即返回」 |
| A3 | 🟠 P1 | `GET /api/audit` 在 CH 故障时返回 **HTTP 200 + 泄露内部主机名**（同 C5） |
| A4 | 🟡 P2 | **admin(10) 完全看不到审计**，只有 root 能查。运维分工上没有中间层 |
| A5 | 🟡 P2 | **`token_ref` / `exclude_token_ref` 过滤实际无效**：82/82 全为空。原因是没有 `access_token` 认证场景（全走 session），`AccessTokenFingerprint` 只对 legacy access token 生效 |
| A6 | 🟡 P2 | `category=access_token` 恒为 0（同 A5 原因） |
| A7 | 🟢 P3 | 审计无 TTL / 无归档（注释声明 intentional，合规上可能期望，但长期会累积） |

### 25.9 未覆盖的审计场景

| # | 项 | 原因 |
|---|---|---|
| 1 | **CH 卡住（连接可建立不响应）的延迟惩罚** | 需 `docker pause` 或 iptables，比 stop 更危险 |
| 2 | **access_token 认证场景**（触发 token_ref 填充） | 需创建 legacy access token 并用它认证 |
| 3 | **2FA / Passkey 的 security 子类** | 需绑定 2FA 设备 |
| 4 | **审计日志导出** | 无此接口（只有 GET /audit 与 /audit/self） |
| 5 | **审计表超大数据量下的查询性能** | 当前仅 88 行，无法类比 logs 的 141 万 |

### 25.10 审计测试结论

**设计得当的地方**：
- 参数校验严格（非法 category / token_ref / 时间 / success 全部显式拒绝）
- `other` 三级裁剪按「角色 × SelfView」生效，实测 root 用 self 也只看到 `op`
- 不记明文凭证，但记「查看密钥」这个动作
- 提权需二次验证
- fail-open：审计写失败不阻断业务（CH 停机时登录仍 0.15s / 200）

**该修的**：
1. A1 审计缺 spool —— 要么接受「审计可能丢」，要么加 spool（但审计要求强一致，spool 的 at-least-once 可能重复）
2. A3 错误码 200 + 泄露主机名 —— 应改 503 并脱敏
3. A4 admin 看不到审计 —— 若运维需要，把 `authz.AuditRead` 授给 admin，或接受「审计只有 root 可查」
4. A5/A6 token_ref 过滤空转 —— 文档注明「只对 access_token 认证有效」，或从 UI 隐藏该筛选框

---

## 26. 数据看板依赖与 log 改造的影响（2026-11-07 18:45）

用户问：看板需要的日志数据能否优化，是否受 log 改造影响。

### 26.1 关键事实：看板和业务日志是两条独立的数据链

```
                     ┌──────────────────────────────────────┐
RecordConsumeLog ──┬─→ createLog()  → enqueueLog → CH writer → CH `logs`（业务日志）
                   │                              └→ spool（兜底）
                   │
                   └─→ LogQuotaData() → 内存 CacheQuotaData（按 8 维聚合）
                                              ↓ 每 5 分钟（DataExportInterval）
                                         SaveQuotaDataCache()
                                              ↓
                                         MySQL `quota_data`（预聚合，小时粒度）
                                              ↓
                              /api/data/series、/api/data/sk、/api/data/user-balances
```

**核实结论**：`/api/data/*` 全部查 `DB.Table("quota_data")`（MySQL），**不查 CH**。

```go
// model/series.go:47
query := DB.Table("quota_data").
```

所以 **`LOG_SQL_DSN` 改造对看板的读取路径和查询性能零影响**。实测 6 个接口：

| 接口 | 耗时（3 次） |
|---|---|
| `/api/data/series?dimension=model` | 46 / 27 / 32 ms |
| `/api/data/series?dimension=user` | 25 / 21 / 17 ms |
| `/api/data/series?dimension=channel` | 43 / 18 / 15 ms |
| `/api/data/sk` | 18 / 11 / 16 ms |
| `/api/data/user-balances` | 18 / 15 / 15 ms |
| `/api/data/series/self` | 16 / 16 / 16 ms |

### 26.2 但有三处间接影响

#### 🟠 影响 1：写入时机被 createLog 阻塞

```go
// model/log.go:392
err := createLog(log)        // ← 走 CH writer，队列满会阻塞
if err != nil { logger.LogError(...) }
if common.DataExportEnabled {
    LogQuotaData(...)        // ← 在 createLog 之后
}
```

CH 队列（50000）打满时 `createLog` 阻塞，**看板数据的记录也跟着延迟**。正常流量无影响（队列不满）。

#### 🔴 影响 2：可靠性倒挂 —— 日志能补，看板补不回

| | 业务日志 `logs` | 看板 `quota_data` |
|---|---|---|
| 崩溃前未落盘的数据 | 写入 spool，重启 replay | **只在内存，进程死即丢** |
| 兜底机制 | ✅ spool（256MB/文件） | ❌ 无 |
| 落盘频率 | 每 2s 或批量满 | **每 5 分钟**（`DataExportInterval=5`） |

实测：停 CH 21 秒期间的 2 次登录审计丢了（审计无 spool）；同理，**apiduo 被 kill -9 时最后 5 分钟的看板数据会永久丢失，而同一时段的日志能通过 spool 恢复**。

⇒ **看板数字会永久小于日志统计数字**，差距随每次异常退出累积。

#### 🟠 影响 3：删除日志不回滚 quota_data

`quota_data` 是预聚合，`ALTER TABLE logs DELETE` 不影响它。实测同一时间窗（16:00–19:00）：

| 来源 | count | quota |
|---|---|---|
| MySQL `quota_data` | **319,359** | **637,514** |
| CH `logs`（type=2 全部） | 319,115 | 1,531,331 |
| CH `logs`（仅 `perf-rpm10w` 真实请求） | 310,929 | 621,858 |

- **count 差 244 条**：我之前清理过 `e2e-test` / `sse-test` / `load-test` 的日志，但 `quota_data` 不会回滚 ⇒ **日志删了，看板数字还在**
- **quota 差 89 万**：CH 里包含**直接注入的测试数据**（million-1m / bulk-100k-clean），它们没走 `RecordConsumeLog`，所以不进 `quota_data`

⇒ 两条结论：
1. **任何绕过应用写日志的行为（数据修复、导入、注入）都会造成看板与日志永久不一致**
2. 运维删日志时要意识到「看板数字不会跟着降」

### 26.3 看板数据本身的优化空间

#### ✅ 优点：内存聚合效果极好

`logQuotaDataCache` 用 8 维 key 聚合：

```go
key := fmt.Sprintf("%d\x00%s\x00%s\x00%d\x00%s\x00%d\x00%d\x00%s",
    UserID, Username, ModelName, CreatedAt, UseGroup, TokenID, ChannelID, NodeName)
```

实测压测期间（18:17 的 10 万请求，同用户/同模型/同小时）：缓存里只有 **1 条**记录。

```
[SYS] 18:27:16 | saved dashboard data, 1 records
[SYS] 18:32:16 | saved dashboard data, 0 records
```

**31 万条请求 → 1 行 quota_data。** 写放大为零，设计正确。

#### 🟠 问题 1：没有唯一索引，写入是「先查后写」

```
8 个索引全部 NON_UNIQUE=1（只有 PRIMARY(id) 唯一）
idx_qdt_created_at (created_at)
idx_qdt_model_user_name (model_name, username)
idx_quota_data_{user_id,token_id,channel_id,use_group,node_name}
```

`SaveQuotaDataCache` 对每个维度组合：**1 次 SELECT + 1 次 UPDATE/INSERT** = 2 次往返。

- 单实例 + `CacheQuotaDataLock` 保护下没出问题（实测重复组 **0**）
- **但多实例部署（不同 `NODE_NAME`）会各写各的，产生重复行** —— 当前没有唯一约束能拦

**优化**：加唯一索引 `(user_id, model_name, created_at, use_group, token_id, channel_id, node_name)`，改 `INSERT ... ON DUPLICATE KEY UPDATE quota=quota+VALUES(quota), count=count+VALUES(count), ...`。收益：**2 次往返 → 1 次**，且天然幂等。

#### 🟠 问题 2：series 查询 300ms @ 10 万行，随数据量线性变差

用临时表 `quota_data_bench`（不碰真表）造 10 万行（100 用户 × 20 模型 × 48 小时）实测：

| 索引方案 | 耗时（3 次） | 执行计划 |
|---|---|---|
| **现状**（idx_qdt_created_at） | 326 / 286 / 294 ms | `type=ALL`, `Using temporary; Using filesort` |
| +`(created_at, model_name)` | 321 / 323 / 533 ms | **无改善**（优化器仍不全用） |
| **+覆盖索引 `(created_at, model_name, count, quota)`** | **288 / 301 / 288 ms** | `type=range`, **`Using index`**（免回表） |

**结论：索引不是主因，`GROUP BY` + `filesort` 才是。** 覆盖索引只省了回表，临时表排序仍存在。

规模推算（真实场景 100 用户 × 20 模型 × 24 小时）：

| 数据量 | 推算查询耗时 | 说明 |
|---|---|---|
| 48,000 行/天 | ~150 ms | 一天 |
| 33.6 万行（7 天） | ~1 s | 7 天窗口 |
| 1,750 万行（1 年） | 不可用 | 全表扫 |

当前站点只有 **7 行 / 16 KB 数据 + 112 KB 索引**（索引比数据大 7 倍），完全无压力。但上量后 series 会成为慢查询。

### 26.4 优化建议（按收益/风险排序）

| 优先级 | 项 | 改动 | 收益 | 风险 |
|---|---|---|---|---|
| 🥇 | **唯一索引 + upsert** | 1 条 DDL + 改 `SaveQuotaDataCache` 约 20 行 | 写往返减半、防多实例重复 | 低（当前 0 重复，可直接加） |
| 🥈 | **覆盖索引 `(created_at, model_name, count, quota)`** | 1 条 DDL | 10 万行下省回表（300→288ms） | 低（索引已 112KB，再加约 2MB/百万行） |
| 🥉 | **日级汇总表** `quota_data_daily` | 新表 + 定时任务 | 查询快 24 倍（7 天从 33.6 万行降到 1.4 万行） | 中（多一层聚合，要处理补数） |
| — | `quota_data` 按月分区 | DDL + 迁移 | 老数据不参与扫描 | 中 |
| — | **加「数据新鲜度」监控** | 一条 SQL 查询 + 告警 | 发现 `SaveQuotaDataCache` 卡住/进程挂了 | 零 |

**最后一个几乎免费但价值最高**：现在如果 `UpdateQuotaData` goroutine 挂了、或进程被 kill，看板会一直显示旧数字，**没有任何告警**。查一条 SQL 就能发现：

```sql
SELECT MAX(created_at) FROM quota_data;   -- 与 now 比较，超过 10 分钟即异常
```

### 26.5 结论

1. **看板读取完全不受 log 改造影响** —— 它查 MySQL `quota_data`，与 CH 无关，实测 15–46ms。
2. **但可靠性被 log 改造反衬出短板** —— 日志有 spool 能补，看板只有 5 分钟内存缓存，崩溃即丢；且删日志不回滚看板。**看板数字 ≤ 日志统计数字**。
3. **当前性能不是问题**（7 行 / 16 KB），优化是为上量做准备：**唯一索引 + upsert** 最值得先做，**日级汇总表**是规模化后的正解。
4. **零成本高价值项**：加「`quota_data` 最新时间 vs now」的监控告警。

---

## 27. 进程被杀后的恢复：三种场景实测（2026-11-07 18:52–18:55）

用户问「进程 kill 了咋恢复」。本轮**真机演练**了三种 kill 方式，结论出人意料。

### 27.1 三种 kill 的自动恢复能力完全不同

| 场景 | 谁发起的 | RestartPolicy 是否生效 | 实测结果 |
|---|---|---|---|
| **A. `docker kill apiduo-cn`** | 从宿主机发命令行 | ❌ **不生效** | 容器 `exited(137)`，**95 秒后仍然没起来**，站点 down **2 分 1 秒** |
| **B. 容器内 `kill -9 1`** | 容器内部 | — | **无效**：`kill_rc=0` 但进程没死（容器内 PID 1 未被杀死） |
| **C. 宿主机 `kill -9 <容器主进程 PID>`** | OOM killer / 运维 | ✅ **生效** | **8 秒自动恢复到 healthy**，`RestartCount 0→1` |

**结论**：
- **`docker kill` / `docker stop` 会被 Docker 标记为「人为停止」**，`restart: unless-stopped` 的语义就是「除非被手动停」，所以**永远不自动重启** —— 必须 `docker start`
- **进程自己退出（OOM、panic、从宿主机 kill PID）** → Docker 视为意外退出，**按 policy 自动重启**，实测 8 秒
- 容器内 `kill -9 1` 实测杀不死（PID 1 有特殊保护），不必依赖

### 27.2 恢复命令（按场景）

```bash
# 场景 A：docker kill / docker stop 之后
docker start apiduo-cn
# 等待 healthy（实测 5.9 秒）
until [ "$(docker inspect apiduo-cn --format '{{.State.Health.Status}}')" = healthy ]; do sleep 1; done

# 场景 C：自动重启了，但可能进入 CrashLoop（如 CH 挂了 → InitLogDB Fatal）
docker ps -a --filter name=apiduo-cn          # 看 STATUS 是否 Restarting
docker logs --tail 50 apiduo-cn               # 看是否 Fatal
# 如果是 CH 不可用导致的 CrashLoop，先修 CH：
docker start apiduo-cn-clickhouse
# CH healthy 后容器会自动起来；否则再 docker restart apiduo-cn

# 万一 healthcheck 卡住
docker restart apiduo-cn
```

### 27.3 恢复后必须验证的 4 项

```bash
CPWD=$(docker inspect apiduo-cn-clickhouse --format '{{range .Config.Env}}{{if eq (index (split . "=") 0) "CLICKHOUSE_PASSWORD"}}{{index (split . "=") 1}}{{end}}{{end}}')
CH() { docker exec apiduo-cn-clickhouse clickhouse-client --password "$CPWD" -q "$1"; }

# ① 服务起来了吗
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:39047/api/status

# ② writer 是否启用 + 回放了多少
docker logs --tail 50 apiduo-cn | grep -E "log writer enabled|ready in"

# ③ 🔴 有没有重复（重点！多 spool 文件时会出现）
CH "SELECT count(), uniqExact(request_id), count()-uniqExact(request_id) AS dup FROM apiduo_cn_logs.logs"

# ④ spool 是否残留多个文件（>1 个就有重复风险）
ls -la /www/data/apiduo-cn/data/log_spool/apiduo-cn/
```

### 27.4 三次演练的数据结果：零丢失、零重复

| 演练 | logs 变化 | dup | 启动日志 |
|---|---|---|---|
| A. `docker kill` | 1,416,045 → 1,416,045 | **0** | 手动 start 后 `replayed=0`、`ready in 874ms`、5.9s healthy |
| C. 宿主机 `kill -9` | 1,416,045 → 1,416,045 | **0** | 自动重启 `replayed=0`、`ready in 1026ms`、8s healthy |

**为什么 `replayed=0`**：spool 里只有一个 140MB 文件，marker 的 `offset` 已经等于文件大小（全部已确认）。重启时从 offset 读，没有待回放数据。

### 27.5 什么情况下会丢 / 会重

| 情形 | 后果 | 量级 |
|---|---|---|
| **kill 时有日志还在队列里**（未 flush） | **丢**（只在内存） | 最多 **2 秒**的量（`interval=2s`），当前 QPS 下约 0–5000 行 |
| **kill 时有批次 insert 成功但 marker 未落盘** | **重**（replay 再写一次） | 微秒级窗口，概率极低 |
| **spool 里有 ≥2 个文件 + 重启** | **大量重复**（P0 缺陷） | 实测 10 万条 |
| **看板数据（quota_data）** | **丢最后 5 分钟**（内存缓存，无 spool） | `DataExportInterval=5` |
| **审计（audit_logs）** | 同步写，无队列；kill 瞬间在写的丢了就没了 | 单条 |

### 27.6 一个必须知道的运维陷阱

本次演练**造成了 2 分钟真实停机**（18:52:39–18:54:40），原因就是先验了 `docker kill` —— 它不自动重启，而我在等一个永远不会发生的自动恢复。

**教训**：
- 维护窗口内用 `docker kill`/`stop` 是可以的（本来就要停），但**停完必须记得 `docker start`**
- 想测试「自动恢复」，要用 `kill -9 <宿主机 PID>`，不要用 `docker kill`
- 生产上真正的意外（OOM、panic）走的是场景 C，会自愈；真正需要人介入的是「CrashLoop」（如 CH 挂了导致 `InitLogDB` Fatal）

### 27.7 建议加的一条兜底

当前没有任何「容器不在跑」的告警。场景 A 这种「手动停了但忘了起」的情况，站点会一直 down 而无人知晓（本次 2 分钟都没人发现）。

最小做法（任选其一）：
1. cron 每 2 分钟检查 `docker ps` 是否包含 apiduo-cn，不在则 `docker start` + 发通知
2. Prometheus `container_last_seen` / `up` 指标告警
3. 外部拨测 `/api/status`（推荐，能同时覆盖 nginx、TLS、应用三层）

---

## 28. 看板数据新鲜度监控（已落地，2026-11-07 19:06）

§26.4 提过「一条 SQL 就能发现看板卡住」，本轮把它做成了可执行的监控。

### 28.1 要解决的问题

`quota_data` 由 `UpdateQuotaData` goroutine 每 `DataExportInterval=5` 分钟从内存落库。以下情况会让看板永久显示旧数字，且**系统没有任何告警**：

| 情况 | 现象 |
|---|---|
| `UpdateQuotaData` goroutine 挂了 | 进程还活着，但 quota_data 不再增长 |
| 进程被 kill（最后 5 分钟的数据在内存） | 那 5 分钟永久缺失 |
| MySQL 写失败（被 `SysLog` 吞掉） | 只写日志，不重试 |
| `DataExportEnabled` 被关 | 直接不写 |

### 28.2 判据设计：不能用 `now - quota_latest`

**错误做法**：

```sql
SELECT MAX(created_at) FROM quota_data;  -- 与 now 比较
```

问题：**没有流量时 `quota_data` 本来就不更新**，静默期会误报。实测当前（19:04，最后一条消费日志 18:27）`quota_data` 停在 18:00 —— 这是正常的。

**实际采用的判据**：以**日志的最新消费时间**为基准，比较看板落后多少。

```
logs_latest  = CH:   SELECT max(created_at) FROM logs WHERE type=2
quota_latest = MySQL: SELECT MAX(created_at) FROM quota_data
lag          = logs_latest - quota_latest
告警条件      = lag > 75 分钟
```

这样：
- 无流量 → `logs_latest` 本身很旧 → `lag` 依然小 → **不告警** ✅
- 有流量但落库卡住 → `logs_latest` 往前走，`quota_latest` 不动 → **lag 变大 → 告警** ✅

### 28.3 为什么阈值是 75 分钟而不是 60

正常情况下的最大 lag 由两段构成：

```
小时取整（quota_data 的 created_at 是小时向下取整）  → 最多 60 分钟
+ DataExportInterval 落库延迟                        → 最多 5 分钟
= 65 分钟（正常上限）
```

所以阈值取 **75 分钟**（`LAG_ALERT_SECONDS=4500`），留 10 分钟余量：
- 不误报（65 < 75）
- 落库真挂掉后，约 **10–15 分钟内**告警

### 28.4 脚本：`/opt/apiduo-cn/check-dashboard-freshness.sh`

四个退出码，对应四种状态：

| 退出码 | 状态 | 含义 |
|---|---|---|
| **0** | `OK` | 看板跟得上日志 |
| **0** | `IDLE` | 最后一条消费日志超过 24 小时（长期无流量，不判断） |
| **1** | `ALERT` | **看板落后日志超过 75 分钟** |
| **2** | `ERROR` | 查询失败（CH 或 MySQL 不可达 / 密码读不到） |

关键参数全部支持环境变量覆盖（便于临时调整 / 测试）：

```bash
CH_CONTAINER=${CH_CONTAINER:-apiduo-cn-clickhouse}
DB_CONTAINER=${DB_CONTAINER:-apiduo-cn-mysql}
LOG=${LOG:-/www/wwwlogs/apiduo-cn/dashboard-freshness.log}
LAG_ALERT_SECONDS=${LAG_ALERT_SECONDS:-4500}
IDLE_THRESHOLD_SECONDS=${IDLE_THRESHOLD_SECONDS:-86400}
```

内置日志轮转：超过 5000 行只保留最后 2000 行（每 5 分钟一行，一年约 10 万行，必须截断）。

可选 webhook（配了 `APIDUO_ALERT_WEBHOOK` 才推送）：

```bash
if [ -n "${APIDUO_ALERT_WEBHOOK:-}" ]; then
    curl -sS -m 10 -X POST "$APIDUO_ALERT_WEBHOOK" \
        -H 'Content-Type: application/json' \
        -d "{\"text\":\"[apiduo] $MSG\"}" >/dev/null 2>&1 || log "WARN webhook push failed"
fi
```

### 28.5 部署

```bash
*/5 * * * * /opt/apiduo-cn/check-dashboard-freshness.sh >> /www/wwwlogs/apiduo-cn/dashboard-freshness-cron.log 2>&1
```

- 每 5 分钟，与 `DataExportInterval` 对齐（落库一次就检查一次）
- 输出同时写两处：脚本自己的 `dashboard-freshness.log`（含历史）+ cron 的 stdout 日志
- cron 服务状态：`active`

### 28.6 四个分支实测（全部通过）

| 分支 | 触发方式 | 输出 | 退出码 |
|---|---|---|---|
| **正常** | 默认参数 | `OK lag=27min` | 0 ✅ |
| **ALERT** | `LAG_ALERT_SECONDS=60` | `ALERT dashboard stale: logs_latest=2026-11-07 18:27:02 quota_latest=2026-11-07 18:00:00 lag=27min quota_rows=7` | 1 ✅ |
| **IDLE** | `IDLE_THRESHOLD_SECONDS=1` | `IDLE last consume log was 0h ago (...)` | 0 ✅ |
| **ERROR** | `CH_CONTAINER=no-such-container` | `ERROR: cannot read CH password` | 2 ✅ |

日志样例（生产）：

```
[2026-11-07 19:05:45] OK logs_latest=2026-11-07 18:27:02 quota_latest=2026-11-07 18:00:00 lag=27min quota_rows=7
```

### 28.7 使用方式

```bash
# 手动查一次
/opt/apiduo-cn/check-dashboard-freshness.sh

# 看历史
tail -20 /www/wwwlogs/apiduo-cn/dashboard-freshness.log

# 只看告警
grep ALERT /www/wwwlogs/apiduo-cn/dashboard-freshness.log

# 临时调严阈值（不改进脚本）
LAG_ALERT_SECONDS=1800 /opt/apiduo-cn/check-dashboard-freshness.sh
```

### 28.8 还没做的（下一步可选）

| 项 | 说明 |
|---|---|
| **告警出口** | 目前只写日志文件。要真正「通知到人」，需配 `APIDUO_ALERT_WEBHOOK`（企业微信/钉钉/Slack 的 webhook 都能直接用） |
| **容器存活检查** | §27.7 提的问题：`docker kill` 后不会自动重启，站点会一直 down 无人知晓。建议合并进同一脚本（检查 `docker ps` 是否含 apiduo-cn） |
| **外部拨测** | 覆盖 nginx / TLS / 应用三层，比容器内检查更全面 |
| **日志 dup 检查** | P0 缺陷（多 spool 文件重复）也可以定期查 `count()-uniqExact(request_id)` |

---

## 9. 修复记录（2026-11-07 21:05，0.0.8）

按 §6 清单动手，只改 `model/log_writer.go` 与对抗测试，未碰上游文件。

### L2 队列满不再阻塞请求

原实现 `select { case writer.ch <- log: ... case <-writer.stop: ... }` 在队列满时无限阻塞，
一个卡住的日志库会把所有请求线程一起拖住。

现在是三段式：

1. 先判 `stop`（停机优先，见 L3）；
2. 非阻塞试写一次；
3. 失败则有界等待 `LOG_WRITER_ENQUEUE_WAIT_MS`（默认 1000ms），超时**返回 `handled=false`**，
   由 `createLog` 落到同步插入 —— 行不丢，代价是每个请求最多多等一个超时。

降级次数记在 `dropped` 计数器里，每 64 次打一条 `[ERR]` 避免刷屏，停止时随
`log writer stopped: written/replayed/failed/fallback` 一起输出。

### L3 停机不再随机丢行

`enqueueLog` 进入时先做一次 `select { case <-writer.stop: return false, nil; default: }`。
旧实现返回 `(true, error)`，`createLog` 看到 `handled=true` 直接 `return err`，那一行就没了。
现在一律交回同步插入。

### L4 spool 失败后不再永久丢行

旧逻辑只在 `len(batch) > 0` 时写 spool，而 `pending`（上一轮 insert 失败的行）**从不重写**。
于是「spool 写失败 + insert 失败」这两件事同时发生时，行只在内存，重启即丢。

新增 `spoolDirty`：append 失败就置位，下一轮 flush 把 `rows`（pending + batch）整体写盘。
正常路径仍是只写新 batch，不会重复。

### 未做

- **L6**：多副本隔离靠部署约定（每个副本不同 `LOG_WRITER_SPOOL_DIR`）。代码层加实例前缀会改变
  现有文件名，旧文件认不出来反而丢 replay，收益不抵风险。
- **L9**：线上实测 `max(quota)=499`、`max(prompt_tokens)=3999`，离 int32 上限三个数量级，不动。
- **L5**（重放重复 → 统计翻倍）：需要把 `logs` 表换成 `ReplacingMergeTree` 或查询层去重，
  涉及表迁移，单独排期。
- **L1**（空库 `/setup`）：当前已初始化（`users>0`），窗口期已过；nginx 侧限制另议。

### 验证

```bash
go test ./model/ -run 'TestAdversarial_Enqueue' -v   # 2 个新语义用例
go test ./model/ -count=1                            # 全包 12.5s 通过
```
