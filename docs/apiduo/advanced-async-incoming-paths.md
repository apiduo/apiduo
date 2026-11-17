# 高级异步渠道（type=8001）· 入站路径

入站路径**不是网关的固定路由**，是每条渠道在 `advanced_async.incoming_paths` 里自己声明的。
除下面「内置端点」一节外，请求走 NoRoute，由分发器按「路径 + 模型」判定归属。

## 内置端点（Images 协议，免声明）

Images 协议有两条网关自有的路径，渠道**不需要**写进 `incoming_paths`：

```
POST /async/v1/images/generations        → 改道 → POST /v1/images/generations/async
GET  /async/v1/images/generations/{id}   → 改道 → GET  /v1/images/generations/{id}
POST /v1/images/generations/async        → 直接命中（静态路由，与上面那条等价）
```

- 定义在 `pkg/endpoint/async.go`：`AsyncImagePath` 是客户端前缀入口，`AsyncImageSubmitPath`
  是提交落地点。`AsyncEndpoint.Submit` 说明两者的改道关系；`Submit == Path` 表示它本身就是
  静态路由，分发器不得再改一次。
- 提交落在 **images** 路径，不是 `/v1/video/generations`：图像任务重入视频中继会让任务日志、
  presenter 和按动作的统计都把它记成视频任务，只有上游形状是图像的。
- 默认只有 8001 渠道能接管这几条路径。`channelMatchesFilter` 用 `endpoint.LookupAsync` 判定；
  同步渠道即使模型同名也进不了候选集，否则它的任务插件会把图像体当视频任务解析。
  打开本地执行（见下节）后才放开。
- `/v1/images/generations/async` 现在是静态路由：仍然可用，但不再需要渠道声明，
  在 `incoming_paths` 里再写它不会改变行为。
- 前端 Images 预设的入站默认值就是别名 `/async/v1/images/generations`
  （`ADVANCED_ASYNC_IMAGE_ENTRY_PATH`）。面板因此会在 `incoming_paths` 里写一条与内置
  入口同名的路径：冗余但无害（内置入口优先），作用是让「高级异步路由」面板有值可显示。

自定义业务分段（`/jimeng/...`）仍然走渠道声明制，规则不变。

## 本地执行：同步渠道也能服务异步端点

`LOCAL_IMAGE_ASYNC_ENABLED=true` 时，异步端点可以选中**同步**渠道：提交只落一行任务和一份
请求快照，后台 worker（复用 15 秒的 `async_task_poll` 调度）重放那次同步调用。客户端拿到的
task id、状态码、状态词与 8001 完全一致。

**上游地址在提交时冻结，worker 不自己拼。** 顺序是：

1. 渠道 `settings.primary_endpoint` 是图片路径（含 `/images/`）→ 用它。这就是「type 54 主端点」
   这类渠道的接入方式：运营声明一次，worker 与同步中继指向同一个地方。
2. 否则问渠道类型对应的 adaptor（`GetRequestURL`）——与同步中继同一次调用，所以 VolcEngine
   的 `/api/v3/images/generations`、OpenAI 的 `/v1/images/generations` 都自动正确。
3. 两者都拿不到绝对地址（没有 base url、没有 adaptor）→ **提交时拒绝**，不落库。

为什么不在 worker 里写死路径：那对 OpenAI 形状的渠道成立，对每一条自建端点的渠道都是错的，
而且错误发生在几分钟后——客户端已经被告知「已受理」。

其余边界：

| 项 | 规则 |
|---|---|
| 请求体 | 存客户端原文（≤256 KB），只把 `model` 改写成渠道映射后的上游名；内联 base64 请改用 URL 引用 |
| 定价 | 只支持按次（`per_call`）；tiered 表达式需要上游用量，同步渠道不报 |
| 重试 | 上游拒绝（4xx/5xx）可换渠道重试，8001 → 本地允许；上游已受理（2xx 后失败）不重试 |
| 归属 | 本地任务 `execution_mode=local`、`platform=local-image-async`，轮询半边按这一列认领 |
| 轮询 | 上游 poller 与超时清扫都排除 `execution_mode=local`；只有本地 worker 推进它 |

### 调度与并发

`RunLocalImageAsyncPass` **派发**而不是**执行**：认领后把任务扔进 goroutine，自己立刻返回。
不这么做的话，一次 8×20 秒的批量会把调度协程占住两分钟 —— 实测同窗口内
`async_task_poll` 只跑了 1 次，其它周期任务一起饿死。

- 并发上限 `apiduoLocalImageAsyncBatch = 8`（进程级槽位），先占槽再认领，避免「认领了没人跑」。
- 提交成功后直接 `KickLocalImageAsyncWorker()`，不等 15 秒调度周期；槽位释放时再踢一次。
- 任务用 `context.WithoutCancel` 脱离调度协程的生命周期，自己带 5 分钟超时。
- 进程被杀会留下 `IN_PROGRESS` 的孤儿行：`ClaimStaleLocalImageAsyncTasks` 回收
  `start_time` 超过 6 分钟的记录，判失败并退款。

### 故障与自愈

| 故障 | 现象 | 自愈方式 |
|---|---|---|
| worker panic | 单任务失败 | 该 goroutine 自身 recover + 当次判失败退款，进程不受影响 |
| 进程被杀（OOM / 发版） | 行停在 `IN_PROGRESS` | 重启后 reaper 在 6 分钟窗口内判失败并退款 |
| 上游挂住 | 占用一个槽位 | 5 分钟 HTTP 超时 → 判失败退款 |
| 上游限流（429） | 本次请求被拒 | 不重试：判失败退款，客户端重提会重新选渠道 |
| 数据库不可用 | 认领不到任务 | 每 15 秒重试一次，任务不丢 |
| 派发循环 panic | 即时派发失效 | 循环自身 recover；15 秒周期调度兜底 |

并发度 `LOCAL_IMAGE_ASYNC_CONCURRENCY`（默认 8，上限 64）。容量 = 并发 ÷ 单任务耗时：
20 秒的任务约 24 张/分钟/进程，60 秒的约 8 张。实测 24 个 20 秒任务（3 倍槽位）62 秒
全部完成，无丢任务、无重复执行。

**渠道隔离**：一个渠道最多占 `并发 - 1` 个槽位，最后一个留给后来者。只做轮转排序不够 ——
派发发生在提交时，第一个渠道在第二个请求存在之前就把池子占满了（实测饿死 39 秒）；
硬性对半分又把单渠道吞吐砍半（24 个任务 122 秒 vs 62 秒）。留一个空位是同时成立的最小改动：
新来的立刻开工，独占时仍拿到全部减一（24 个任务 82 秒）。

**准入控制**：队列上限 `LOCAL_IMAGE_ASYNC_MAX_QUEUE`（默认 200）、每人
`LOCAL_IMAGE_ASYNC_MAX_QUEUE_PER_USER`（默认 20），超了回 429 `queue_full`。
拒绝发生在计价之前，不预扣额度；而 429 的「上游容量不足」文案只对上游成立，
本地拒绝保留自己那句话。

**时钟同源**：认领写入的 `start_time` 与回收的截止点都取数据库时钟（`model.DatabaseNow`）。
两边读不同的钟，一台机器的偏移就能把活任务判死。

### 渠道轮换：429 换一条渠道重提

429 的第一选择是**换一条渠道**，因为它立刻生效。任务被限流时，worker 不判失败，而是：

1. 把这条渠道记进 `private_data.execution.tried_channels`；
2. 用 `GetRandomSatisfiedChannel(group, originModel, 0, [排除已试渠道, 请求路径])`
   另选一条能服务该模型的同步渠道；
3. 改 `channel_id`、清掉旧的密钥绑定、状态退回 `NOT_START`，由派发器重新接手；
4. 最多换 3 条渠道，全试过才判失败退款。

**换哪条渠道，只看客户端模型名。** 渠道的模型名单是运营对「客户端可以拿它要什么」的声明；
一条只声明 `vendor-x` 的渠道从未被授权服务 `public-model`，它的成本基线和分组策略属于另一个名字，
用它等于花了没人批准的钱。候选集就是 `abilities` 里登记了客户端模型名的那些渠道。

**请求体按「执行时那条渠道」重新映射**，不在提交时冻结。快照里只存客户端的原始请求
（`execution.origin_request`），换到哪条渠道就按那条渠道的 `model_mapping` 重新算出上游模型名。
带模型映射的渠道因此**正常参与轮换**——运营配映射就是为了让这条渠道能服务这个模型。

`tried_channels` 也是这条链路的审计记录：客户端看到的始终是同一个 task id，
但真正出图的是最后那条渠道。

### 渠道级并发

渠道可以在 `other_settings` 里声明自己的并发上限，因为**每个账号的配额不一样，只有渠道知道**：

```json
{"local_worker": {"concurrency": 5}}
```

worker 派发时读这个值并夹到进程上限内；没声明的渠道沿用「进程份额 − 1」。
实测：声明 2 的渠道，上游观测到的峰值并发恰好是 2。

### 限流时的两种恢复，按代价排序

任务被 429 拒绝时，**不会立刻退款**。这不是「网络重试」——同步出图不是幂等的，
5xx 可能是「已经出图、只是回包丢了」，重试就是第二张图加第二次收费。只有 429 例外，
它证明上游在做任何事之前就拒绝了请求。

恢复顺序是固定的，代价从小到大：

1. **换一条渠道**（见上一节）。立即生效，不产生等待。这是首选，所以先试。
2. **在本渠道等一会再试**。只在上一步无处可去时发生——也就是这个模型只有一条渠道。
   退避 8 秒、16 秒（或按上游的 `Retry-After`，封顶 25 秒），最多 3 次尝试。

为什么退避是「十几秒」而不是「两秒」：429 的成因是上游并发占满，而并发要到前面那批
生成结束才会释放。两秒后重试只会被再拒一次，等于把上游当轮询器打。

**等待期间槽位不释放**，这也是设计的一部分：它顺带阻止本进程在供应商仍然繁忙时继续加压。

代价说清楚：等待把「配额超了」从失败变成延迟，但每次 429 都是一次真实的拒绝，
会记在供应商的限流器上。如果运营知道某条渠道的配额，用
`other_settings.local_worker.concurrency` 直接不超过它，比事后恢复更省。

### 结果地址的约束### 结果地址的约束

上游给的结果地址只校验**形状**：绝对地址、`http`/`https`、有 host、不带凭据、≤8 KB。
`data:` / `javascript:` 一律拒绝 —— 前者会把图片字节塞进每次轮询响应，后者让客户端把
网关当成投递渠道。校验点在两处：`advancedcustomasync.ParseTaskResult`（8001 链路，
终态但地址非法 → 直接判失败，不重试）与本地 worker。

**不做** DNS 解析与私网拦截：那属于「网关自己去抓」的场景，`artifactFetchURLValidator`
已经做了，且内网 MinIO 的地址对客户端是合法的。

## 两种形状

| 形状 | 例子 | 用途 |
|---|---|---|
| 默认 | `/async/v1/images/generations` | 内置别名，Images 协议免配置 |
| 按业务分段 | `/jimeng/v1/images/generations`、`/hailuo/v1/images/generations` | 业务/供应商各占一段，客户端按业务决定打哪条 |

两种都合法。`{biz}` 段**由运营在渠道里写死**，不是通配符：即梦的渠道只接 `/jimeng/...`，
不会顺带接走海螺的流量。

**默认值取网关别名而不是静态路由**（`ADVANCED_ASYNC_IMAGE_ENTRY_PATH`）：静态路由
`/v1/images/generations/async` 命中后不经过分发器，`X-Apiduo-Task-Action` 没人设置，
任务会被记成 `text_to_video`，任务详情里的图片制品也因此取不到。别名走 NoRoute，
分发器会把动作补齐再改道。两条路径都能用，区别只在日志与制品是否记为图片。

## 客户端怎么调

```
POST <incoming>              → 提交，立刻拿任务号
GET  <incoming>/{task_id}    → 轮询
```

Images 协议的轮询回标准 Images 响应（任务成功时），Ark 协议回任务对象。
内部落地点是 `/v1/images/generations/{task_id}`，只注册了 GET，同名路径上的同步 POST 不动。

## 校验与规范化（保存时）

- 必须以 `/` 开头，不能带 query。
- **尾斜杠会被去掉**：入站路径是逐字相等匹配，`/x/y/` 与 `/x/y` 是两条路径，
  留着就是「保存成功、永远收不到请求」。
- 不能是同步入口：`/v1/images/generations`、`/v1/images/edits` 一律拒绝，任何协议都一样。
  8001 没有同步适配器，接管同步入口只会让客户端拿到任务号而不是图片。
- `{model}` 是唯一的占位符（分段内匹配，值里不能有 `/`）。`{biz}` 不是占位符，写了就是字面量。

## 禁止清单

| 不要做 | 为什么 |
|---|---|
| 把 `/v1/images/generations` 填进入站路径 | 同步入口会被异步渠道接管，同步请求全变成任务号 |
| 指望 `/{biz}/...` 当通配前缀 | 匹配只认 `{model}`；biz 必须逐条渠道写具体值 |
| 同一路径让两条同协议渠道都声明 | 归属按模型判定，但路径冲突时先命中的渠道先接，难以排查 |
| 用一级段与已有静态路由重名（`/api`、`/v1`、`/pg`） | 静态路由优先，NoRoute 收不到，渠道永远不生效 |
| 用 `/async` 当业务分段 | 该前缀是内置异步端点的命名空间，与端点表冲突 |
