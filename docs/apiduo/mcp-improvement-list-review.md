# 对抗审查 · `apiduo/mcp 改进清单`

| 项 | 内容 |
|---|---|
| 审查对象 | `apiduo-改进清单.md`（12 条建议 + 2 条文档修正，评估基准 `main` @ 2026-11-14） |
| 审查方法 | 对抗审查：对每条断言做证伪尝试，只保留能在代码里走到终点的主张 |
| 代码基准 | 本仓库当前工作树（`mcp/` 28 个 `.go` + README） |
| 结论 | **12 条中 4 条证据不成立或前提被推翻，5 条严重度虚高，3 条设计会引偏；同时漏掉 1 个 P0 级产物缺失与 1 个 P1 级协议不一致。**按原顺序施工会做两件无效改动、漏掉一处每天都在发生的功能缺陷。 |

---

## 0. 裁决摘要

| ID | 原判 | 裁决 | 依据（一句话） |
|---|---|---|---|
| G1 | P0 | **撤销定性 + 改写为一致性修复（P2）** | `maxBatchN = 8` 已存在，relay 另有 `MaxImageN = 128`；`n=1000` 到不了上游。真实问题是三层上限三个数 |
| G2 | P0 | **降为 P1 + 换载体** | 幂等该做，但「查 `logs` 表按 `request_id` 前缀」不可行，且被 `RequestId` 中间件挡住 |
| G3 | P0 | **降为 P1 + 换绑定粒度** | 事实成立，但「钱拿不回来」不成立；应绑 `UserId` 而非 `TokenId` |
| G4 | P1 | **撤销** | 查询路径不选渠道、不打上游、不写 `logs` 表 —— 三条断言全部证伪 |
| G5 | P1 | 手段保留，理由降级 | `catalog_version` 可做，但「客户端缓存 tools 结果」不是 MCP 客户端的实际行为 |
| G6 | P1 | **撤销失效键**，保留缓存 | `version` 是行级乐观锁，`max(version)` 在新增/删除行时不变，缓存将永久陈旧 |
| G7 | P2 | 方向保留，落点修正 | 「只有两处接缝」对 mcp 特性成立，但仓库有 **426 个标记块 / 142 个文件**；注册点不能放 `relaykit` |
| G8 | P2 | 维持 | 但 `mcp_task_polls_total` 当前无数据来源（观察只覆盖 generate） |
| G9 | P2 | 维持，成本更低 | SDK 按 `Out` 类型**自动生成** `outputSchema`，不用手写 |
| G10 | P2 | 维持，须与 G5 联合设计 | `resources` 可被客户端缓存，版本号必须进 URI，否则放大 G5 |
| G11 | P2 | **削弱** | 任务型端点「不产生费用的探测」不成立；探测失败即禁止上线会制造假阴性 |
| G12 | P2 | 维持 + 扩展 | 还应把 `tools/markercheck -cost` 纳入 CI |
| E1 | — | **成立** | 实测 README 第 4 节表格确被段落截断 |
| E2 | — | **成立** | `max_ref_videos` 只进 `model_card`，工具层无对应入参 |

---

## 1. 四处硬伤

### 1.1 G4 完全证伪：任务查询不走渠道选择

文档断言：`task.get` 回环 `/v1` 会「重新做一次渠道选择」，多渠道路由下可能落到 B 渠道 → 404。**这条调用链只读到了起点**。

实际链路（方舟查询路径）：

```
GET /v1/contents/generations/tasks/{id}
  → router/apiduo_ark_task_alias.go:80   apiduoArkFetchPathRewrite()
        把 Path 改写为 /v1/video/generations/{id}
  → middleware/distributor.go:492-496    GET + /v1/video/generations → shouldSelectChannel = false
  → controller/apiduo_ark_task_fetch.go:31-40
        model.GetByTaskId(userId, taskID) → 读 task.Data 快照，直接回方舟形状
```

三处关键事实：

1. **不选渠道**。`getModelRequest` 对 `GET /v1/video/generations` 显式置 `shouldSelectChannel = false`（`middleware/distributor.go:492-496`）；`Distribute` 的 `if pinned || shouldSelectChannel` 整块被跳过，`channel` 为 `nil` 也照常 `SetupContextForSelectedChannel(c, nil, "")` 继续。
2. **不打上游**。`ApiduoArkTaskFetch` 只读本地 `tasks` 表里的 `task.Data`（后台轮询服务写入），不发任何上游请求 —— 比文档假设的「按 channel_id 取渠道」还要彻底。
3. **不写 `logs` 表**。`relay/relay_task.go` 全文没有 `RecordConsumeLog`；任务消费日志由提交时的 `service.LogTaskConsumption`（`controller/relay.go:895,921` → `service/task_billing.go:26`）写入。轮询只产生一行 **gin 访问日志**（`middleware/logger.go:23`），与 `logs` 表无关。

因此：

- 「查询可能落到 B 渠道 → 404」—— 不会发生；
- 「3 分钟视频 = 36 条 relay 日志 + 36 次渠道选择」—— 不会发生；
- 文档自己给的验证方法（**创建后禁用原渠道再查**）会直接证伪它自己：查询读的是 DB 快照，渠道禁用不影响结果。

**若按原建议施工**，会得到两个净负面结果：`task_id` payload 里多一个 `ChannelId`（可被任何拿到 task_id 的人读出渠道编号），以及一条永远走不到的「按渠道查询」分支。

> 唯一残留的真风险：`GetByTaskId` 是以 `(user_id, task_id)` 为条件的 DB 查询，`task.get` 的每次轮询 = 1 次 DB 读 + 1 次令牌校验。这是**成本**问题，不是**正确性**问题，与文档描述的不是同一件事。

---

### 1.2 G1 已被实现两次：MCP 有 `maxBatchN`，relay 有 `MaxImageN`

文档证据引到 `checkRange`（`mcp/protocol_tools.go:620`），结论是「`n` 未声明 max 时无全局上限」。但 `checkRange` 的调用点在 12 行之外的 `buildPayload` 里，那里已经有兜底：

```go
// mcp/protocol_tools.go:511-526
if in.N != nil {
    if *in.N < 1 { ... }
    if err := checkRange(m, "n", float64(*in.N)); err != nil { return nil, err }
    if _, declared := m.Params["n"]; !declared && *in.N > maxBatchN {
        return nil, userParamErrorf(CodeParamOutOfRange,
            "模型 %s 未声明 n 的上限，单次最多 %d；要更多请分多次调用", m.ID, maxBatchN)
    }
    ...
}
// mcp/protocol_tools.go:553-554
const maxBatchN = 8
```

连文档建议的错误文案（「要更多请分多次调用」）都已经在代码里。文档自己在条目末尾写了「采信前请用一次 mock 调用确认 `buildPayload` 没有另设上限——若已有，本条降级」；这个降级条件**已经成立**。

但**「一次 `n=1000` 打光额度」这个定性同样不成立**：relay 层还有一道独立硬上限。

```go
// relaykit/dto/openai_image.go:13-15
// MaxImageN caps the image generation count. Without this bound a huge or ...
const MaxImageN = 128
```

它在三处执行，都在**出站之前**：

- `relay/helper/valid_request.go:277` —— 入站请求校验，`n > 128` 直接 400；
- `relay/image_handler.go:173-178` → `relaykit/dto/openai_image.go:65` —— 对**出站 body**（含渠道改写与请求插件改过的 `n`）再算一次 `ImageCount`，越界 400；
- `service/image_billing.go:20` —— 预扣费前第三次校验 `count ∈ [1, 128]`（`relay/image_handler.go:192` 无条件调用）。

所以 `n=1000` 走不到上游，也产生不了账单。叠加预扣费按可用余额封顶（`service/billing.go:24-57`），G1 描述的「一次调用打光额度」没有可达路径。

真实的缺口换了形态：**上限有三层、三个数字，没有单一真值。**

| 层 | 上限 | 生效条件 |
|---|---|---|
| MCP `maxBatchN` | 8 | 仅当 profile **未声明** `n` |
| profile 声明的 `max` | 声明值 | 仅当声明里写了 `max` |
| relay `MaxImageN` | 128 | 所有图片请求 |

由此产生两个真实缺口，但都不是资损级：

| 缺口 | 路径 | 后果 |
|---|---|---|
| `extra: {"n": 1000}` | Extra 循环（`:527-535`）→ `checkExtra`（`:636`）在未声明时返回 `nil`，`standardParams`（`:558`）放行 `n` | 绕开 `≥1` 与 `≤8`，被 relay 截在 128 —— 得到 128 张，而不是一个明确的拒绝 |
| 声明了 `n` 但只写 `min` 或只写 `note` | `checkRange` 仅在 `spec.Max != nil` 时拦 | 同上 |

**改写后的做法**：把 `n` 的判定收到 `buildPayload` 的唯一出口 —— `p["n"]` 定稿前做一次终局校验（`1 ≤ n ≤ min(声明上限 ?? maxBatchN, MaxImageN)`）。目的不是堵额度，而是让「MCP 告诉 agent 的边界」和「relay 实际执行的边界」是同一个数：现在 MCP 层写着 8、relay 写着 128，两个都会生效。

---

### 1.3 G6 的失效键不成立：`version` 是行级乐观锁，不是目录版本

文档建议「进程内缓存，按 `max(version)` 失效（表里已有 `version`，天然是失效依据）」。

`version` 的定义在 `mcp/models.go:67-71`：

```go
// Version 是乐观锁版本号，每次写库 +1。
Version int64 `json:"version" gorm:"not null;default:0"`
```

它是**单行**的并发控制字段。以下三种变更都不会让 `max(version)` 变化：

- 新增一个模型（新行 `version=1`，若已有行 `version=7`，`max` 仍是 7）；
- 删除一个模型（删掉的不一定是 `max` 那行）；
- 启停一个非 `max` 行的模型（`toggle` 只让该行 +1）。

结果就是文档最想避免的那类故障：缓存**永久**陈旧，而运维以为「改了就生效」。正确做法二选一：

- **1 秒 TTL**。这张表是几十行的全表扫描，缓存只为挡高频重复读，不需要强一致；
- **全局 `catalog_epoch`**，在 `SaveProfile` / `SetProfileEnabled` / `DeleteProfile` 的写入路径上 +1。

**另一处降级理由**：`mcp_model_profiles` 是几十行的配置表，`ListProfiles` 是一条无 join 的 `ORDER BY` 查询（`mcp/models.go:207-211`）。称它为「热点表」不成立；G6 的真实收益是挡住 `models.list` 的滥用流量，而不是省 DB。

---

### 1.4 G2 的载体不成立：`logs` 表承担不了幂等窗口

方向（幂等）对，实现路径有三处硬阻塞：

1. **`request_id` 由中间件强制生成，客户端无法注入**

```go
// middleware/request-id.go:11-16
id := common.NewRequestId()
c.Set(common.RequestIdKey, id)
```

无论 MCP 回环时带什么 `X-Oneapi-Request-Id`，都会被覆盖。文档第 3 步「把该值编码进本次 `request_id`」必须改这层中间件 —— 这是新增接缝，与文档 G7 自己主张的「接缝越少越好」冲突。

2. **命中窗口对不上：日志是异步批量写的，重试那一刻还查不到**

`enqueueLog` 把行塞进 channel，由后台批量落库（`model/log_writer.go:251-272`），`createLog` 只是「已入队」。MCP 返回给 agent 之后的重试通常发生在数百毫秒到数秒内 —— 恰好是这行还没写进 `logs` 的时间窗。用它判重，等于在网络抖动最密集的那段时间里查一个还没出现的记录。

（`request_id` 本身有普通索引 `idx_logs_request_id`，`model/log.go:77`；MySQL 下 `LIKE '前缀%'` 能用上。真正的问题不是索引，是可见性。ClickHouse 侧排序键是 `(created_at, request_id)`，按 `request_id` 检索不走主键 —— 这一半仍然成立。）

3. **语义错位**：`logs` 是账本，不是幂等表。账本受 `LOG_DB` 保留期与清理策略约束，一旦过期幂等窗口就消失 —— 而这个窗口本该由调用方定义。

**仓库里已经有正确范式**，直接复用即可：

```go
// model/subscription.go:1255-1268
type SubscriptionPreConsumeRecord struct {
    RequestId string `gorm:"type:varchar(64);uniqueIndex"`
    Status    string `gorm:"type:varchar(32);index"` // consumed/refunded
    ...
}
// model/subscription.go:1488  7 天后清理
```

对应做法：新建 `mcp_idempotency(token_id, tool, client_request_id, status, task_id, result, created_at)`，`(token_id, tool, client_request_id)` 唯一索引；**先插入占位再发上游**（唯一索引冲突即命中），返回后回填 `task_id` / 结果摘要，按 TTL 清理。这样幂等窗口由 MCP 自己定义，不依赖账本保留期。

**严重度也要降**：`classifyToolError` 对 `timeout` / 网络错误统一落到 `CodeInternal, Retryable:false, RetryAction:"none"`（`mcp/protocol_error.go:100-105`），守规矩的 agent 不会重发。真正会双倍计费的只有两种：上游 5xx 命中了运营配置的 `retryable=true` 规则；或 agent 不守约。这是 P1，不是 P0。

---

## 2. 两处严重度虚高

### 2.1 G3 是「句柄丢失」，不是「钱打水漂」

事实部分成立：签名密钥确实由 `api_key` 派生，代码注释还把这写成了期望行为（`mcp/protocol_taskid.go:17`）：

> 密钥由 api_key 派生：换 key 即作废旧任务 ID —— 这正是期望行为

但「钱已花、产物拿不到」不成立。任务记录是用户级的，产物有三条独立的取回路径：

- `GET /api/task/self`（`router/api-router.go:473`）—— `TaskDto.ResultURL` 直接带结果地址（`dto/task.go:47`）；
- 前端任务页 / `GET /api/task/:task_id/artifacts`（`router/api-router.go:475` → `controller/task.go:68`）；
- 直连 `GET /v1/video/generations/{id}`（本地任务号，不经 MCP）。

丢失的只是 **MCP 会话里那个自包含句柄**，不是资产。因此正确严重度是 P1（agent 侧体验断裂 + 需要人工介入），不是 P0（资损）。

两处设计要改：

1. **绑定 `UserId`，不是 `TokenId`**。网关自己的授权模型就是用户级（`GetByTaskId(userId, taskID)`，`/api/task/self` 也是按 `user_id` 过滤）。绑 `TokenId` 会让「删旧令牌、建新令牌」这种常规操作继续丢任务 —— 与要解决的问题同类。绑 `UserId` 后，越权仍被 DB 的 `user_id` 条件挡住，`TokenId` 那层是重复防御。
2. **`ExpireAt` 是安全项，不只是可用性项**。现在 `IssuedAt` 被签进去了但**从不校验**（`mcp/protocol_taskid.go:20-25` 定义了 `IssuedAt`，`parseTaskID` 全文从不读它），即 `task_id` 永不过期。补 `ExpireAt` 应该单独说明为「限定句柄有效期」，否则评审时会被当成需求噪声。

### 2.2 G5 的机制搞错了对象

MCP 规范里 `tools/call` 的返回**没有缓存语义**（它不是可缓存资源，只是一次调用的结果）；会被客户端缓存的只有 `tools/list`，而工具定义是静态的 4 个（`mcp/protocol_tools.go:25-62`），与模型能力无关。

真实机制是另一件事：**同一会话里，agent 的上下文留着上一次 `models.list` 的文本**。它不会主动重拉，直到调用失败。所以 `catalog_version` 的价值是「让失败可解释、让提示可自动化」，而不是「打破客户端缓存」。

结论：手段保留（成本确实极低），但把「这是本设计最尖锐的一处」降为「一处一致性瑕疵」。**并且必须与 G10 联动**：`resources` 是可被客户端按 URI 缓存的，`apiduo://catalog` 如果不带版本，会把 G5 的问题放大 —— 应该设计成 `apiduo://catalog?v=<epoch>`。

---

## 3. 三处设计会引偏

### 3.1 G7：接缝论证用的是错的数字

文档说「与 upstream 只有两处接缝……每多一处，未来合并成本就上升一档」。

对 `mcp` 这个特性成立，对仓库不成立。实测：

```
$ grep -rn "APIDUO:" --include=*.go relay/ | wc -l
62
$ cat .apiduo/merge-budget.txt
files 142 / blocks 426 / added 4062 / deleted 221
```

合并面是 426 个标记块跨 142 个文件，由 `tools/markercheck -cost` 记账，且**只允许下调**。在 relay 里新增一处观察调用，是「+1 块，按约定显式上调一次预算」，不是「打破唯一约束」。

真正该说的成本是另外两条，文档没写：

- **耦合方向**：让 relay 的热路径知道「有人在观察参数」，等于把计费链路和 MCP 可观测性绑在一次函数调用里；
- **冲突面**：`relay/image_handler.go`、`relay/relay_adaptor.go` 是上游改动最频繁的文件，内联调用会持续产生合并冲突。

**落点也要改**：`relaykit` 是独立 Go module，定位是「协议 DTO 与转换，不含传输、鉴权、DB 访问」。把带可变全局状态的 `RegisterObserver` 注册表放进去，违反它自己的边界。正确位置是宿主 `relay/` 包导出接口 + 注册函数，`relaykit` 最多只放接口定义。

### 3.2 G11：「不产生费用的探测」对任务端点不成立

图片/视频的任务端点，**提交即受理、受理即可能计费**。用「`max_tokens` 级别的空请求」探测形状，在 chat 端点上成立，在任务端点上要么被参数校验拒掉（探不到形状），要么真的建一个任务（产生费用）。

`GET /v1/models`（若渠道支持）只能证明「这个 key 活着」，证明不了「这个渠道认得任务端点形状」。

更严重的是**假阴性**：以「探测失败」阻止 `enabled=true`，会把「探测方法本身不适配」误判成「渠道不可用」，直接挡住正确接入。

**替代方案**（用真实流量做证据，不额外发请求）：把每个 profile 的「首次调用结果」写回 —— `unverified / ok / shape_mismatch`，来源是 `observeCall` 已经采集的 `code` 与 `upstream` 摘要。`models.list` 里标出状态。这比探测便宜、准确，且不会因为探测手段本身出错而阻塞上线。

### 3.3 G9 的成本被高估

SDK 文档明确：`ToolHandlerFor` 的 `Out` 类型（非 `any`）**自动生成 `outputSchema`**，并在 `Content` 未设置时自动填充（`go-sdk@v1.8.0/mcp/tool.go:28-51`）。`generate` / `task.get` 已经在用 `generateOutput` / `taskOutput` 作为 `Out`，所以这不是「纯声明工作」，而是**什么都不用做**。

真正要做的是反向工作：`generateOutput.Upstream map[string]any` 会让自动生成的 schema 变成开放对象，需要把它排除或标 `additionalProperties`，否则客户端拿到的 schema 等于没约束。

---

## 4. 清单漏掉的

### ~~M1（P0）· `task.get` 的 text 里没有产物 URL~~（2026-11-15 已修）

```go
// mcp/protocol_tools.go:705
return textResult("任务状态：" + status), out, nil
```

视频是唯一异步产物，**所有完成的视频都从这条路径返回**；而 `Outputs` 只进了 `structuredContent`。

这直接违反两处文档自己立的规矩：

- 本文 §0 第 6 条：「产物 URL 同时进 text 与 structuredContent」；
- `mcp/README.md` 约定 9：「产物地址必须进 text content —— 多数 MCP 客户端只渲染 text」。

`generate` 的同步分支是守约的（`appendOutputLinks`），异步分支漏了。结果就是最常见的一条链路（`video.generate` → `task.get` 直到 `completed`）在只渲染 text 的客户端上表现为「agent 说视频好了，用户看不到视频」。

**顺带一个方法论事实**：§0 是整份清单里唯一**不给行号**的章节，而它恰恰有一条是错的。凡是不给证据位置的断言，都应该按「未验证」处理，包括「已经做对的」。

### ~~M2（P1）· `task.get` 的错误不走可分类通道~~（2026-11-15 已修）

`getTask` 的三条失败路径全部 `return nil, taskOutput{}, err`（`mcp/protocol_tools.go:680,684,688`），没有经过 `d.fail` / `classifyToolError`。SDK 会把它们包成 `IsError: true` + 纯文本（`go-sdk@v1.8.0/mcp/server.go:422-432` 的 `errRes.SetError(err)`），**没有 `code`、没有 `retryable`、没有 `retry_action`**。

于是一个「任务号过期」和一个「上游 503」在 agent 眼里长得一样，都只能靠读中文。文档 §0 第 4 条把「错误可分类」列为不可动的优点 —— 这条优点只覆盖了 `generate`，没覆盖 `task.get`。

### M3（P2）· 观察盲区让 G8 的一条指标没有数据源

`observeCall` 只在 `generate` 调用（成功与本地拒绝各一处），`getTask` 完全不产生样本。所以：

- `mcp_task_polls_total`（G8 表格里的第 4 项）没有埋点来源；
- 「轮询放大系数」这个指标即使建了也是恒 0。

要么给 `getTask` 补一条轻量样本（只记 `status` 与耗时，不记 task_id），要么把这条指标从 G8 删掉 —— 留着会制造「已监控」的错觉。

---

## 5. 方法论：这些错误为什么会出现

三处判断错误（G1 / G4 / G6）是同一类问题：**断言停在调用链的起点**。

| 文档写法 | 停在哪 | 应该跟到哪 |
|---|---|---|
| G1 | `checkRange` 只拦 `Max != nil` | 调用方 `buildPayload` 有 `maxBatchN`，relay 有 `MaxImageN` |
| G4 | `getTask → c.do(GET, ...)` | `distributor` 的 `shouldSelectChannel`、`ApiduoArkTaskFetch` 的实现 |
| G6 | 表里有 `version` 字段 | `version` 的语义与写入路径 |

三条都能用「再往下读 30 行」证伪。而文档整体用的是「证据 + 行号」的写法，读者会默认它已经读完 —— 这比不给行号更危险。

**建议加一条写作规则**：凡涉及 relay / distributor 行为的断言，必须指到**那个分支所在的行**，不能只指到调用表达式；指不到就标 `待验证`，不能进 P0/P1 表。

### 回到第一性原理：这个包真正的承重结构只有三条

1. **一次工具调用 = 一次真实计费调用。** MCP 不持账，所以它唯一该拥有的持久状态是「幂等映射」，不是能力缓存、不是任务状态。凡是把状态放在 MCP 层的设计都要先问：这份状态和 relay 的账本谁是真值？
2. **句柄只能由稳定量派生。** `user_id` + 全局密钥 + 上游任务号是稳定的；`api_key` 不是。把用户可变的量放进签名密钥，等于让「改密码」变成「丢数据」。
3. **声明与渠道的一致性只能靠观测收敛，不能靠一次性探测。** 声明是人写的，渠道是运维改的，两者持续漂移。巡检 + 采纳建议（已实现）是正确方向；纳管时探测只是把漂移检测提前到最没有信息的那一刻。

按这三条重排，重心会变：G3（句柄稳定性）与 G2（幂等）是承重的；G1 只是一个边界对齐补丁；G4 不存在；G5/G6/G7/G10/G11 是可选优化。

---

## 6. 修正后的施工顺序

| 顺序 | 项 | 规模 | 理由 |
|---|---|---|---|
| 1 | **M1** `task.get` text 补产物 URL | S | 每天都在发生的功能缺失，改动 3 行 |
| 2 | **M2** `task.get` 错误走 `classifyToolError` | S | 与 §0 第 4 条对齐，避免 agent 误判 |
| 3 | **G3′** 系统密钥 + `UserId` + `ExpireAt` | S | 句柄稳定性；顺带补上「永不过期」的安全缺口 |
| 4 | **G2′** 独立幂等表（复用 `SubscriptionPreConsumeRecord` 范式） | M | 真正防双计费 |
| 5 | **G1′** `n` 上限收到单一出口（含 `extra` 通道） | S | 三层上限对齐成一个数；不是额度问题，是一致性问题 |
| 6 | **G5 + G10** `catalog_version` + `apiduo://catalog?v=` | S/M | 一起设计，避免资源缓存放大陈旧 |
| 7 | **G6′** 1s TTL 或全局 epoch | S | 撤销 `max(version)`，换成能真正失效的键 |
| 8 | **G12** CI：3 条 grep + `go test` + `markercheck -cost` | S | 合并上游的唯一自动防线 |
| 9 | **G8 + M3** 指标（含 `getTask` 样本） | M | 没有数据源的指标不要建 |
| 10 | **G7′** 观察 hook 化（注册点放 `relay/`） | M | 结构优化，可延后 |
| 11 | **G11′** 首调结果回写 `verified` 状态 | M | 替代「免费探测」 |
| — | ~~G4~~ | — | 撤销：问题不存在 |
| — | ~~G1 原定性~~ | — | 撤销：「一次 `n=1000` 打光额度」不可达，relay 上限 128 |
| — | **G9** | — | 实际是「约束自动生成的 schema」，不是「手写 outputSchema」 |

---

## 7. 文档自身的修正（E1 / E2）

两条都成立，实测确认：

- **E1**：`mcp/README.md` 第 4 节，`| ANY | /mcp | ... |` 与 `| GET | /api/mcp/overview | ... |` 之间夹了一段正文，表格被截成两半，Markdown 渲染时后半段不会成为表格。
- **E2**：`mcp/README.md` §7 自认 `max_ref_videos` 只进 `model_card`；工具层 `generateInput` 没有对应字段，`buildPayload` 也从不校验 `MaxRefVideos`。agent 看到声明会尝试传参，然后收到 `PARAM_NOT_DECLARED`。

**补充 E3**（文档未提）：`mcp/README.md` 第 2 节标题写「sync 时只查这两处」，但同一节表格里还有一行 `go.mod`（无标记）。核对清单实际是「两个文件 + 一个依赖」，标题与内容不一致，建议改成「这两处标记 + 一个依赖」。

---

## 8. 一句话总结

这份清单的**问题识别方向是对的，证据纪律不合格**：12 条里 3 条（G1 / G4 / G6）在把调用链读完之前就下了结论，另有 1 条（G2）给了一个走不通的实现路径。最重的两条是 G4（问题不存在）与 G1（上限已经存在三层，`n=1000` 到不了上游）。与此同时，它漏掉了整份清单里唯一一条「每天都在损失功能」的缺陷：异步任务的产物 URL 从不进入 text content。

**一句话取舍**：按本文 §6 重排（1–4 项都是半天内可完成的 S 级改动），先把「视频生成完用户拿不到地址」和「任务查询错误无法分类」这两条修掉，再谈幂等与句柄稳定；`maxBatchN` 与 `MaxImageN` 的对齐顺手做，不要当成 P0 止血。
