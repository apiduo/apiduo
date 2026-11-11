# 同步转异步 · 队列与 worker 方案（**已否决，不实施**）

> 🔴 **2026-11-11 决定：8001 不做同步任务。** 8001 只承载「上游本身就是异步协议」的中继，
> 不在网关侧把同步上游任务化。因此本文的 `protocol: "sync"`、队列、worker 池、
> `execution_mode` 列、两处轮询排除**全部不做**；`flux-3-image` 这类同步模型保持同步，
> 不提供异步入口。文档保留，仅供将来若另立独立通道类型（如 8002）时参考。
>
> 目标（原）：让只有同步上游的模型（如 `flux-3-image`）支持 `?async=true`，
> 不阻塞客户端、不拖垮网关、任务在现有页面里可见可管。

## 0. 先回答三个问题

| 问题 | 答案 |
|---|---|
| 需要其他中间件吗 | **不需要**。队列建在现有的 `tasks` 表（生产 MySQL 8.2）上，不引 Redis / Kafka / RabbitMQ |
| 任务在哪管理 | 存储就是 `tasks` 表；用户看 `GET /api/task/self`，管理员看 `GET /api/task`，都是现成接口 |
| 任务还在任务日志里吗 | **在**。任务日志是 `logs` 表（生产 ClickHouse）里 `type=2` 的记录，sync 任务复用同一条计费链路，会自动写入 |

一句话：**队列是 tasks 表的一批特殊状态行，日志是既有的消耗日志，两者都不新增存储。**

---

## 1. 中间件：零新增

### 1.1 生产现状

三容器：`apiduo-cn` + `apiduo-cn-mysql`（8.2）+ `apiduo-cn-clickhouse`（25.8）。
`.env` 里没有 `REDIS_CONN_STRING` ⇒ `common.RedisEnabled = false`（`common/redis.go:25`）。
即使以后配上 Redis，它也只在缓存/限流路径上生效，不参与任务持久化。

### 1.2 为什么不用消息队列

| 方案 | 问题 |
|---|---|
| Redis List / Stream | 任务已预扣配额，消息丢失就是资金差额；要补 AOF、主从、监控，等于多养一套有状态服务 |
| Kafka | 队列深度只有几百，为它引一个 broker + zookeeper/kraft，运维成本远超收益 |
| RabbitMQ | 同上，且 ack 语义与"不自动重跑"的要求冲突（重投递 = 重复生成、重复计费） |
| **DB 队列（选定）** | 任务行本身就是队列项，状态、结果、租约都在同一事务里；出问题能直接 SQL 对账 |

关键论据：**任务在入队前已经扣了钱**。队列项的持久性要求等同于账本，只有数据库能满足。

### 1.3 为什么不用 `SELECT ... FOR UPDATE SKIP LOCKED`

MySQL 8.2 支持，但：

- SQLite 不支持（本地开发、单机部署都会挂）
- 事务里持有行锁直到提交，批量抢占时容易和计费/日志写入互相等待
- 现有 `model/locking.go:10` 的 `lockForUpdate` 已经为 SQLite 做了降级，再叠一层 `SKIP LOCKED` 会更碎

改用**条件 UPDATE 乐观抢占**，一条语句判定归属：

```sql
UPDATE tasks
   SET status = 'IN_PROGRESS',
       start_time = :now,
       queue_lease_owner = :worker_id,
       queue_lease_until = :now + :timeout
 WHERE id = :id
   AND status = 'QUEUED'
   AND platform = '8001';
-- RowsAffected == 1 才算抢到
```

`RowsAffected` 是唯一裁判，不依赖数据库特性，MySQL/SQLite 行为一致。

### 1.4 现成的 `system_task` 为什么不能直接当队列

`model/system_task.go:270` 的 `ClaimSystemTask` 看起来正好，但它的锁表 `SystemTaskLock`
**以 `type` 为主键**（`model/system_task.go:44`）：

```go
type SystemTaskLock struct {
    Type        string `gorm:"type:varchar(64);primaryKey"`
    ...
}
```

同一 type 全局只允许一个 runner。这是为「定时任务互斥」设计的（`async_task_poll` 不能两个实例同时跑），
拿来当队列会让 4 个 worker 串行成 1 个。

可以借鉴它的 `locked_until` + `locked_by` 思路，但锁粒度必须是**每行一个租约**，不是每 type 一个。

---

## 2. 表结构增量

`tasks` 表在 `model/main.go:358` 的 AutoMigrate 列表里，加列自动生效。新增 3 列（包 APIDUO 标记块）：

| 列 | 类型 | 用途 |
|---|---|---|
| `execution_mode` | `varchar(16)`，index | `upstream`（默认，上游产任务号）/ `local`（网关本地执行） |
| `queue_lease_owner` | `varchar(64)`，index | worker 实例标识，用于判定租约归属 |
| `queue_lease_until` | `bigint`，index | 租约到期时间戳，过期即视为崩溃 |

`execution_mode` 不只是队列用，它还是**排除现有轮询的判据**（见 §6.1），必须落列而不是塞 JSON —— 现有轮询查询要能直接 `WHERE` 掉。

> 备选：把三个字段塞进 `PrivateData`（`type:json`，不用迁移）。否决原因：
> 队列的核心查询是「找 `QUEUED`」「找租约过期的 `IN_PROGRESS`」，JSON 字段没法走索引，
> 而且 `HasUnfinishedSyncTasks` 这种廉价存在性检查会退化成全表扫。

---

## 3. worker 池

### 3.1 组成

```
submit（HTTP）                       worker 池（进程内）
─────────────                        ─────────────────
建 task(QUEUED)  ────────────────▶   取任务 → 抢渠道许可 → 调上游
预扣配额                               → 回写结果 → 释放 → 取下一个
写消耗日志
立即返回 {id, queue_position}
      │
      └── queueSignal <- struct{}{}   （非阻塞，容量 1）
```

**信号只做唤醒，不承载任务。** 提交成功往 channel 里塞一个空结构体（满了就丢），
worker 收到立刻抢一轮；抢不到或没信号时，15s 的系统轮询兜底。

这样既没有"每次提交起一个 goroutine"的并发失控，也不用等满一个轮询周期。

### 3.2 四层并发控制

| 层 | 机制 | 默认值 | 作用 |
|---|---|---|---|
| 1 | `max_queue_depth` | 100 | 队列满直接 429（不建任务、不预扣），带 `Retry-After` |
| 2 | worker 数 | 4 | 同时最多 4 个在途上游请求 |
| 3 | 每渠道 `max_concurrency` | 2 | 抢不到渠道许可就放回队列，不占 worker |
| 4 | `execute_timeout_seconds` | 300 | 上游卡死时主动断开、标失败、释放许可 |

第 3 层是必须的：4 个 worker 全打到同一条 flux 渠道，上游可能只允许 2 并发，
另外 2 个会占着 worker 空转。渠道信号量用进程内 `chan struct{}` 即可（单实例），
多实例下每实例各持一份，等于"实例数 × 每渠道并发"，可接受。

### 3.3 容量预期（写进文档，避免上线后被问"为什么慢"）

| worker | 上游 p50 | 吞吐 | 第 50 个任务等待 |
|---|---|---|---|
| 4 | 40s | ~360 张/时 | ~8 分钟 |
| 8 | 40s | ~720 张/时 | ~4 分钟 |
| 8 | 20s | ~1440 张/时 | ~2 分钟 |

提交响应带 `queue_position` 与 `estimated_wait_seconds`（`ceil(position / workers) × p50`，
p50 用最近 100 个成功任务的滑动中位数）。**排队本身不可避免，把等待时间提前告知才是体验**。

### 3.4 优雅停机

`SIGTERM` → 停止接受新任务 → 等待在途任务（最多 30s）→ 仍在途的保持 `IN_PROGRESS` 与租约，
租约到期后由其他实例/下次启动回收为 `FAILURE(lease_expired)` 并退款。

---

## 4. 任务管理

### 4.1 用户视角：零改动

`GET /api/task/self`（`controller/task.go:427`）直接查 `tasks` 表，按 `user_id` 过滤。
sync 任务插进去就可见，状态流转走现有的 `QUEUED → IN_PROGRESS → SUCCESS/FAILURE`。

前端任务列表页不用改一行代码。

### 4.2 管理员视角：零改动

`GET /api/task`（`controller/task.go:416`）支持 `platform` / `status` / `action` / `channel_id` 过滤，
sync 任务的 `platform` 是 `"8001"`（`relay/relay_adaptor.go:139` 用 `strconv.Itoa(channelType)`），
直接按平台筛就行。

### 4.3 运维视角：新增一个快照接口

现有接口回答不了"为什么慢"。新增：

```
GET /api/apiduo/sync-image/queue
{
  "queued": 37, "running": 4, "succeeded_24h": 1240, "failed_24h": 12,
  "oldest_queued_seconds": 310,
  "workers": {"total": 4, "busy": 4},
  "by_channel": [
    {"channel_id": 12, "name": "flux", "queued": 30, "running": 2,
     "limit": 2, "p50_seconds": 38, "saturated": true}
  ]
}
```

三个关键数字：

- `oldest_queued_seconds` 持续上涨 ⇒ worker 不够
- `saturated=true` 的渠道 ⇒ 该渠道是瓶颈，调 `max_concurrency` 或加渠道
- `failed_24h` 突增 ⇒ 上游出问题

### 4.4 取消与重试

现有路由只有两个 GET（`router/api-router.go:456-460`），没有取消。新增：

| 操作 | 语义 |
|---|---|
| `POST /api/task/:task_id/cancel` | `QUEUED` 直接 `FAILURE(cancelled)` + 全额退款；`IN_PROGRESS` 打标记，worker 完成时丢弃结果并退款（不中断上游请求 —— 中断也计费） |
| `POST /api/task/:task_id/retry` | **新建**一条任务，不复用旧记录，避免状态机出现 `FAILURE → QUEUED` 的回退 |

---

## 5. 任务日志

### 5.1 两条链路，两张表

| 页面 | 数据源 | 过滤条件 |
|---|---|---|
| 任务管理（用户/管理员） | MySQL `tasks` | `user_id` / `platform` / `status` |
| 任务日志（使用日志的 task tab） | ClickHouse `logs`（生产） | `type=2`（`model.LogTypeConsume`）+ `other.is_task=true` |

两张表不是同一份数据：`tasks` 是任务状态快照，`logs` 是**计费流水**。
所以"任务在任务日志里"这件事，取决于有没有走计费日志，而不是有没有建任务行。

### 5.2 sync 任务复用同一条计费链路

| 时机 | 调用 | 写入 |
|---|---|---|
| 提交（HTTP 内） | `service.LogTaskConsumption`（`service/task_billing.go:26`） | 一条 `type=2`，`other.task_id`、`other.is_task=true`，`quota` = 预扣额 |
| 完成（worker 内） | `service.RecalculateTaskQuota`（`service/task_billing.go:310`） | 差额 > 0 记 `type=2`，< 0 记 `type=5`（refund） |
| 失败 | `service.RefundTaskQuota`（`service/task_billing.go:263`） | 一条 refund，`other.reason` 说明原因 |

调用点在 `controller/relay.go:763` 与 `:784`，都在提交请求内同步完成。

**worker 只需要调 `RecalculateTaskQuota` / `RefundTaskQuota`** —— 它们接收 `context.Context`，
不依赖 `*gin.Context`，可以在后台协程里安全执行。

### 5.3 前端零改动

任务日志页调 `/api/log/self`（见 `web/src/features/usage-logs/components/__tests__/billing-source-visibility.test.tsx:87`），
按 `type=2` 展示。sync 任务写进去后自然出现，`task_id` 列、模型名、额度、详情弹窗全部复用。

### 5.4 一个细节：不要被误标成"同步任务"

`service/task_billing.go:203` 的 `taskDeliveredInline` 会把"提交即返回结果"的任务在日志里标 `task_sync=true`。
sync 模式虽然上游是同步的，但网关对它做了任务化，客户端拿到的是 task_id 而不是图片 —— **不该标 `task_sync`**。
判据用 `execution_mode`：`local` 一律不标。

---

## 6. 必须处理的坑

### 6.1 🔴 现有轮询会在 15 秒内把 sync 任务批量标失败（且不退款）

`service/task_polling.go:146` 调 `model.GetAllUnFinishSyncTasks`（`model/task.go:401`），
查询条件是"进度不是 100% 且状态不是 SUCCESS/FAILURE"——**sync 任务全部命中**。

然后 `service/task_polling.go:171-189`：

```go
upstreamID := task.GetUpstreamTaskID()
if upstreamID == "" {
    nullTaskIds = append(nullTaskIds, task.ID)
    continue
}
...
err := model.TaskBulkUpdateByID(nullTaskIds, map[string]any{
    "status":   "FAILURE",
    "progress": "100%",
})
```

sync 任务的上游 task_id 是空的（id 由网关产生）⇒ 每 15 秒被扫一次、直接改 `FAILURE`，
而且 `TaskBulkUpdateByID` **不走 `RefundTaskQuota`**，用户的钱不会退。

必须改两处，都在已有查询上加条件：

- `model/task.go:401` `GetAllUnFinishSyncTasks`：`WHERE execution_mode <> 'local'`
- `model/task.go:416` `HasUnfinishedSyncTasks`：同上（否则系统任务每 15s 白跑一轮）

这两处是**上线前必须绿**的前置条件。漏了不是"任务可能失败"，是"每个 sync 任务必失败"。

### 6.2 不能把 `gin.Context` 带进 worker

gin 的 context 从对象池取，handler 返回后会被复用。worker 只能读 `task` 行
（`Data` 存原始 body、`ChannelId` 取渠道、`PrivateData` 取 token/计费快照），
这也是把它挂在轮询链上而不是 goroutine 里的原因 —— 那条链本身没有 context。

### 6.3 不自动重跑

同步图像生成不幂等：重跑一次多做一张图、多扣一次费。租约过期、进程重启、上游超时
一律 `FAILURE` + 退款 + 让用户显式重试。除非上游支持 `Idempotency-Key`，
那时开一个 per-channel 开关，默认关闭。

### 6.4 只接受 URL，不接受 b64

上游返回 `b64_json` 时不能直接塞进 task 结果，否则每次轮询返回几百 KB。
`sync` 模式明确只认 `fields.result_url_path` 指向的 URL；识别到 `b64_json` 直接判配置错误，
在渠道保存时就拦住，而不是等到第一笔任务失败。

### 6.5 队列满时的顺序

先查深度 → 满了 429 → **不建任务、不预扣、不写日志**。
反过来做（先扣再拒）会产生一批需要退款的空任务，对账很难看。

---

## 7. 工程量

| 模块 | 文件 | 规模 | 预算 |
|---|---|---|---|
| 表结构 3 列 | `model/task.go` | ~6 行 | 标记块 |
| 队列抢占 / 租约 / 统计 | `model/apiduo_sync_image_queue.go` | ~150 行 | 专有文件，不占 |
| worker 池 + 渠道信号量 | `service/apiduo_sync_image_worker.go` | ~230 行 | 专有文件，不占 |
| 提交入口（跳过上游） | `controller/apiduo_sync_image_submit.go` | ~120 行 | 专有文件，不占 |
| 队列快照 / 取消 / 重试 | `controller/apiduo_sync_image_admin.go` | ~150 行 | 专有文件，不占 |
| 配置字段 | `relaykit/dto/advanced_async.go` | ~14 行 | 标记块 |
| 排除现有轮询 | `model/task.go`、`service/task_polling.go` | 各 ~3 行 | 标记块 |
| 路由注册 | `router/*.go` | ~10 行 | 标记块 |
| 前端队列面板 | `web/src/features/system-settings/...` | ~160 行 | 前端专有 |
| 路由层（`?async=true` 分流） | `middleware/apiduo_image_mode_split.go` | ~90 行 | 专有文件，不占 |

服务端约 720 行，其中 **650 行在 apiduo 专有文件里，不占上游合并预算**；
只有表结构、DTO、轮询排除、路由挂载需要走标记块（合计约 30 行）。

---

## 8. 落地顺序

| 阶段 | 内容 | 可独立上线 |
|---|---|---|
| P0 | 路由层：`?async=true` / `background:true` / `/async/` 前缀分流 | 是（无此步，后面全白做） |
| P0 | `execution_mode` 列 + 两处轮询排除 | **必须与 P1 同批**，否则任务全灭 |
| P1 | 队列 + worker + 提交入口 | 是 |
| P2 | 队列快照接口 + 前端面板 | 是 |
| P3 | 取消 / 重试 | 是 |
| P4 | `reuse_channel_id`（复用同步渠道的 key/base_url） | 是 |

P0 的两项要一起发：先加列、再加排除、最后才允许建 `local` 任务。
顺序反了会有一批任务在 15 秒内被误杀。

---

## 9. 不做的事

- **异步转同步**（`?wait=true` 阻塞到出图）：反方向，另开议题
- **多副本共享 worker 池**：当前单实例，租约机制已为扩容留好；真要扩容时把渠道信号量换成 DB 计数即可
- **回调推送 `callback_url`**：属于 webhook，和队列解耦
- **任务进度百分比**：同步上游不回报进度，硬造一个假进度不如直接给排队位置
