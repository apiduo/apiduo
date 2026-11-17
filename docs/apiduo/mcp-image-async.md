# MCP 图片异步：把 `/async/v1/images/generations` 接进 `image.generate`

> 2026-11-17 **v4**。已上线并端到端验证（0.0.183）：`nano-banana-2` 异步生图
> 提交 → working，7 次轮询（约 70 秒）→ completed，产物 URL 正常；$0.01 一次，
> 任务表 1 条记录（`platform=8001`、`quota=5000`）。
> v2 的前提变更：网关已给成功终态补上 `status`（`be0c1cfc0`），v1 的核心机制
> `TerminalOnData` 因此作废，理由见 §1.4。
> 配套原型：`design/mcp-image-async.html`。

---

## 0. 结论先行

1. **工具面一个都不加。** 异步是交付机制，不是新能力。仍然是 4 个工具；
   `image.generate` 的返回里出现 `task_id` + `status:"working"` 就代表异步，与
   `video.generate` 完全同形。新增 `image.generate_async`、或让 agent 传 `async:true`，
   等于把「协议选择」交给模型 —— 这是本次最该拒绝的设计。
2. **状态机零改动。** 网关的成功视图现在回 `status:"succeeded"`，而 `normalizeStatus`
   早已把 `succeeded` 映射为 `completed`（`protocol_tools.go:1586`）。所以 `getTask` 的
   三态判定**一行都不用改**，剩下的是纯接线。
3. **只剩三个接线点加两处文案，约 26 行**，全在 `mcp/` 自有目录：`transport` 加 `AsyncGenerate` +
   `Query`、`generate` 按 `m.Async` 选提交端点、`fanoutCount` 加 `!m.Async`。
   合并面预算（files / blocks / added / deleted）**不变**。
4. **端点用对外的客户端入口 `/async/v1/images/generations`**，不是
   `/v1/images/generations/async`。两者收到的响应一致，但后者在 `pkg/endpoint/async.go`
   的注释里被称作 older entry —— MCP 没有理由依赖一条准备下架的路由。
   代价是它落在 `/v1` 之外，`upClient` 需要站点根：路径以 `^` 开头表示相对站点根
   （§2.2），这是 8 行的事，一次到位。
5. **`n>1` 在异步下不拆单。** 拆单会产出 N 个 task_id，而 `generateOutput` 只能装一个
   —— 静默丢掉 N−1 个**已计费**任务。fanout 存在的唯一理由是绕开同步超时，异步没有
   这个问题。

---

## 1. 事实核对

### 1.1 服务端已经完整（三态都有 status）

| 半边 | 路径 | 响应 |
|---|---|---|
| 提交 | `POST /async/v1/images/generations`（客户端入口，dispatcher 改道到静态路由 `/v1/images/generations/async`；**MCP 用前者**） | `202 {"task_id":"..."}` |
| 查询 · 进行中 | `GET /async/v1/images/generations/{task_id}`（同上，改道到 `/v1/images/generations/{task_id}`） | `202 {task_id, id, object, created, status:"queued"\|"in_progress", progress?}` |
| 查询 · 成功 | 同上 | `200 {task_id, id, object, created, status:"succeeded", data:[{url}]}` |
| 查询 · 失败 | 同上 | `200 {..., status:"failure", error:{message, code}}` |
| 查询 · 成功但无地址 | 同上 | `200 {..., status:"failure", error:{code:"empty_result"}}` |

`controller/apiduo_image_async.go:59-115`。状态词表：`queued` / `in_progress` / `succeeded` /
`failure`，三态都由 `apiduoImageAsyncStatus` 统一产出。

能服务这两个半边的渠道有两类：

- **8001 高级异步渠道**：提交即转发，任务表记 `platform=8001`。
- **同步渠道**：`LOCAL_IMAGE_ASYNC_ENABLED=true` 时，提交只落一行任务和一份请求快照，
  后台 worker 重放那次同步调用（`controller/apiduo_local_image_async.go`）。

**对 MCP 的意义**：异步入口是**客户端形态契约**，不是渠道形态。MCP 声明 `async=true`
之后不需要知道背后是 8001 还是本地执行 —— 这正是它可以只改 `mcp/` 的原因。

### 1.2 MCP 侧还缺什么（v1 五条，现在四条）

| # | 缺口 | 位置 | 症状 |
|---|---|---|---|
| 1 | `transports["image"]` 没有 `Query` | `mcp/protocol.go:26-31` | `task.get` 直接报「协议 image 不支持任务查询」 |
| 2 | 提交端点写死同步 | `transport.Generate` | 异步模型打到 `/images/generations`：4xx，或同步等 8 分钟 |
| 3 | `fanoutCount` 不看 async | `generate` | `n>1` 拆成 N 个请求 → N 个任务被计费，只返回 1 个句柄 |
| 4 | 只有 `m.Async` 为真才签 `task_id`（P1） | `generate` | 声明 sync、上游却回 task_id 时，钱花了、任务在跑，用户看到「上游未返回图片数据」 |

v1 的第 1 条「完成态没有 status → 判成 working」已由网关修复（`be0c1cfc0`），从缺口表删除。

### 1.3 「图片只有一个入口」还成立吗

成立，但要把「入口」说成**请求形状**，而不是**响应形状**。

`mcp/README.md:150` 现在写的是：

> 图片只有一个入口：`/v1/images/generations`，文生图与图生图共用（图生图靠 `image` 字段），
> 不存在第二套图片协议。

这句话在 `pkg/endpoint/async.go` 落地之前是对的。现在网关多了一条兄弟路径，但两者接收
**同一个 body**（`prompt` / `image` / `n` / `size`），差异只在响应：

```
同步  200 {data:[{url}]}
异步  202 {task_id}  → 轮询 →  200 {status:"succeeded", data:[{url}]}
```

所以「一套协议」指的是 body，不是 URL，也不是状态码。**要改的是这句话，不是这条原则。**
建议改成：

> 图片只有一个**请求**形状（`prompt` + 可选 `image`），两种**响应**形状由能力声明的
> `execution` 决定：`sync` 直接回 `data`，`async` 回 `task_id`。客户端不得用参数在两者
> 之间切换。

这条措辞同时挡住了两种退化：加 `async` 入参、加 `image.generate_async` 工具。

### 1.4 v1 的 `TerminalOnData` 为什么作废

v1 的判断是「成功终态没有 status，所以 MCP 必须靠 data 是否存在判完成」，并为此设计了
一个 transport 级开关。这个判断在当时（`52472bf47`）是准确的，但现在不成立：

- `be0c1cfc0` 在成功分支加了 `status: apiduoImageAsyncStatus(task.Status)` 与
  `object:"image.generation"`，`data` 保留给旧客户端。
- 网关文档（`docs/apiduo/advanced-async-incoming-paths.md:185-190`）同步写死了这条契约：
  「客户端应以 `status` 判断终态，不要靠 `data` 是否存在 —— 后者只是兼容旧客户端的冗余信号」。
- `normalizeStatus` 本来就认 `succeeded`，所以 MCP 侧不需要任何新判定。

**这次的教训值得记下来**：v1 把一个**消费端补偿**当成了核心机制，而它补偿的那个行为
其实是网关的缺陷，修缺陷只要 4 行。正确的顺序是先问「这个形状是不是缺陷」——如果是，
修生产方永远比在消费方加兜底便宜：`TerminalOnData` 要 15 行、一个测试、以及长期向后来者
解释「为什么只有图片要特殊判定」。

现在的形态反而最干净：**图片异步与视频异步共用同一条状态机**，`getTask` 里没有任何按 kind
分支的判定。

### 1.5 同名模型会不会混淆：分四层看，只有一层是真缺口

| 层面 | 会不会混 | 依据 |
|---|---|---|
| 能力声明 | **不会**。一个模型名只能有一条声明，也就只有一种执行形态 | `mcp_model_profiles` 的 `uniqueIndex:uk_mcp_profile_model`（`models.go:34`） |
| 返回形状（同一时刻） | **不会**。形状是 `m.Async` 的确定函数，不随机 | §2.8 的唯一分支 |
| 返回形状（跨时间） | **会**。运维改声明后，同一个模型名下次可能换形状 | 无版本约束，只有 `catalog_version` 变 |
| 渠道选择 | 会，但被抹平。同名模型可能既有 8001 又有同步渠道（`LOCAL_IMAGE_ASYNC_ENABLED`），`Distribute` 按权重选 —— 但异步入口把两者都变成 `202 + task_id` | `channelMatchesFilter` + 本地执行 |
| **`models.list` 的 text 摘要** | **会，而且现在就混** | 见下 |

**唯一现在就会混的地方**：`modelsSummary` 的 tag 只有 `kind` 和「默认」，没有执行形态
（`protocol_tools.go:185-190`）。而函数上方的注释自己写着「多数客户端只显示 text」——
结果是只渲染 text 的客户端看到的清单长这样：

```
- hailuo-image-01（image，默认）：…
- flux-dev（image）：…
```

它无法知道第一个要轮询、第二个直接出图。structuredContent 里的 `execution.task` 它看不到。
agent 不指定 model 时拿到 `working` + task_id，很可能以为出错了，或者反过来拿同步模型去等
一个永远不会来的 task_id。

第二个小缺口同源：异步提交的 text 是「已提交（模型 X），任务 Y 进行中」，**没有**
`defaultedModelNote` 的那句「未指定 model，本次用了 X」—— 而同步完成分支有。同一个
`ModelDefaulted` 字段，两条出口不一致。

**真风险不是混淆，是配置缺失**：声明 `async` 但没有渠道能服务异步入口时，返回的是 503
而不是另一种形状。这种情况看起来像「混淆」，实际是运维没接线（§6.2）。

**由此得到一条必须守住的不变量**：

> `model_id` → `(kind, execution)` 是单射且持久。唯一允许的变化是运维显式改声明，
> 且每次改都会让 `catalog_version` 变；在途任务不受影响，因为句柄自带形态（§2.3）。

任何「让同名模型同时支持 sync 和 async」的方案都必须拒绝（见 §8.9）：
`models.list` 无法表达同名两形态，agent 只能靠猜；`task_id` 归属会变歧义；计费口径分裂。

### 1.6 同一个模型走同步还是异步：三层判定

「同模型怎么判定」的答案不在模型名上，在**路径**上。三层各管一件事：

| 层 | 判定依据 | 作用 |
|---|---|---|
| MCP 声明 | `mcp_model_profiles.async` | 决定 `image.generate` 打哪条路径（一个 model_id 只有一条声明） |
| 网关 | 请求路径 | 路径即形状：`/v1/images/generations` 回 `data`，`/async/v1/images/generations` 回 `task_id` |
| 渠道 | `channelMatchesFilter` | 决定这个模型名能不能服务这条路径 |

渠道那层是唯一会「拒绝」的一层，规则只有两条：

- **8001 渠道只能服务异步路径**。`endpoint.LookupAsync(path)` 命中才放行
  （`channel_constraint.go`）。同步路径上它会一路走到 `config.SupportsPathForModel`，
  而 8001 渠道没有 `advanced_custom` 配置 → `false`。
- **同步渠道默认只能服务同步路径**。`LOCAL_IMAGE_ASYNC_ENABLED=true` 时才允许它服务
  异步路径（网关落任务、worker 重放），但它**不会**因此获得同步路径之外的任何能力。

具体到线上 `nano-banana-2`（只有渠道 #29，type 8001）：

| 请求路径 | 候选渠道 | 结果 |
|---|---|---|
| `POST /v1/images/generations` | #29 被过滤，无其他渠道 | **503 无可用渠道** |
| `POST /async/v1/images/generations` | #29 ✓ | `202 {task_id}` |

所以这个模型**没有同步形态**，MCP 声明 `async=0` 只会让它 503。这就是 §1.5 里
「声明必须与渠道能力一致」的具体含义，也是上线顺序不能反的原因。

---

## 2. 方案

### 2.1 工具面：不变

4 个工具，名字、schema、数量都不变。只有三处文案要跟着改：

| 位置 | 现在 | 改成 |
|---|---|---|
| `serverInstructions` 第 3 条 | "Video is usually asynchronous" | "Some models are asynchronous — mostly video, but some image models too. The call returns task_id; poll task.get with poll_after_ms." |
| `image.generate` description | 未提 task_id | 加一句「声明为异步的模型会返回 task_id，用 task.get 查询；此时响应里没有产物地址」 |
| `task.get` description 与 `taskInput` 的 jsonschema | "由 video.generate 返回" | "由 image.generate 或 video.generate 返回" |

`task.get` 的描述不改会有一个具体后果：agent 拿到图片的 task_id 后会认为「这个工具只服务
视频」，从而回头重新生成一次 —— 那是第二次计费。

### 2.2 transport：加「异步半边」（两个字段，不加开关）

```go
type transport struct {
    Name          string
    Generate      string // 同步提交
    AsyncGenerate string // 异步提交；空 = 该协议没有异步半边
    Query         string // 异步查询；空 = 没有任务可查
}
```

| kind | Generate | AsyncGenerate | Query |
|---|---|---|---|
| image | `/images/generations` | `^/async/v1/images/generations` | `^/async/v1/images/generations/{id}` |
| video | `/contents/generations/tasks` | 同 `Generate` | `/contents/generations/tasks/{id}` |

`video.AsyncGenerate` 与 `Generate` 同值，是为了让「按 `m.Async` 选端点」成为唯一一条
分支，而不是给 video 加例外。video 的表行为与今天完全一致。

`upClient` 加一个 `rootURL`（去掉 base 末尾的 `/v1`）与 `url(path)`：以 `^` 开头的路径走
站点根，其余仍相对 `/v1`。这是「异步入口在 `/v1` 之外」这一件事的全部代价。

#### 请求体同构，所以这条分支只影响两件事

同步入口与异步入口接收**完全相同**的 body（`model` / `prompt` / `image` / `n` / `size` /
`quality` / `response_format`）—— 本地执行那条链路就是原文透传，只改写 `model`
（`docs/apiduo/advanced-async-incoming-paths.md`「请求体：存客户端原文」）。
所以 MCP 侧除了「打哪个路径」，所有请求构造环节都不用分支：

| 环节 | 要不要按 async 分支 |
|---|---|
| `buildPayload`（prompt / image / size / n / quality） | 否 |
| `parseRefs` + `imageRef`（URL 与 base64 垫图） | 否 |
| `checkEnum` / `checkRange` / `checkBatchN` / `checkExtra`（按声明校验） | 否 |
| `requestFingerprint`（幂等指纹） | 否 |
| `snapshotParams`（观察埋点） | 否 |
| **提交路径** | **是 —— 全流程唯一一处** |
| 查询路径 + 状态词表 | 是，但由 `transport` 表驱动，不是 if |

换句话说：**`async` 在 MCP 里只有两个作用 —— 选提交路径、选查询端点。它不进入 payload，
不进入参数校验，不进入幂等指纹，也不改变工具面。** 它是一个路由开关，不是协议开关。

这个区分决定了产品的观感：如果是「协议开关」，客户端会看到两套图片协议；如果是
「路由开关」，客户端看到的永远是一套工具、一套参数、一套状态机 —— 只是返回里有或没有
`task_id`。

#### 那能不能干脆不要 `async` 声明，让 MCP 自适应

不能，两个方向都会更差：

- **总是打同步端点**，拿到 `task_id` 再转任务。死因有两层：异步渠道根本不在同步路径的
  候选集里（`channelMatchesFilter` 只让 8001 进异步端点），请求直接 503；即使能服务，
  也会同步等满 8 分钟 —— 那正是本项目要消除的东西。
- **总是打异步端点**。死因是同步模型被迫落任务表、小图多一次往返，且 `n` 不再拆单
  （§2.7），30 秒能出图的场景反而更慢。

所以 `async` 必须是一个显式声明，但它只需要表达「该模型的交付形态」，不需要（也不应该）
出现在请求里。

### 2.3 句柄形态（P1，可延后）

`taskPayload` 加形态字段：

```go
Mode string `json:"x,omitempty"` // "async"；空 = 按 kind 的历史默认
```

- 签发：异步提交时 `Mode = "async"`。
- 解析：`Mode == ""` 时按 `kindDefaultMode(kind)` 推断（video → async，image → sync），
  保证在途视频句柄继续可查，不需要版本号。
- 查询：`Mode == "async"` 才走 `Query`；否则明确回 `TASK_ID_INVALID`
  「该任务不是异步任务」。

为什么这是 P1 而不是 P0：只要不修缺口 4（「拿到 task_id 就签」），同步 image 模型就永远
不会有句柄，image 的 `Query` 也就只会被 async 句柄命中。**先上线 P0 是安全的。**
修缺口 4 时再加 `Mode`，两件事必须同一个提交里做。

不一起做的后果：一个 image 句柄若来自同步模型，打到异步查询端点会被
`apiduoImageAsyncOwnsTask` 判成 404 `task_not_found` —— 与「句柄伪造」是同一句话，
排查会走偏到签名密钥上去。

### 2.4 终态判定：零改动

`normalizeStatus` 已经覆盖全部四态：

| 上游 status | MCP status |
|---|---|
| `queued` / `in_progress` | `working` |
| `succeeded` | `completed` |
| `failure` | `failed` |

`parseOutputs` 对 `data[].url` 已支持，`upstreamFailReason` 对 `error.message` 已支持。
**`getTask` 不需要任何按 kind 的分支。** 这是 v2 相比 v1 最大的简化：图片异步和视频异步
走完全相同的代码路径。

### 2.5 进度（可选增强，不参与决策）

进行中视图可选带 `progress:"30%"`（`task.Progress` 非空时才输出）。本地执行的任务
**永远不报**（`71463e582`：「网关只知道自己发了一个请求、最后拿到结果」）。

如果要做，`taskOutput` 加：

```go
Progress int `json:"progress_percent,omitempty"`
```

从 `progress` 解析（容错 `"30"`、`"30 %"`），缺失时不出该字段、text 里也不提百分比
—— 「卡在 30%」比「不报进度」更像故障，这是控制台已经踩过一次的结论。

**进度不参与决策。** 不给「进度卡住就重试」的暗示 —— 重试等于二次计费。`poll_after_ms`
是唯一的行为指令。

建议：这一项单独提交，且默认不做。收益是给人看的安抚，成本是一段解析 + 一条测试。

### 2.6 幂等：零改动

`generate` 在异步提交后已经写 `completeIdempotency(..., status:"working", TaskID)`；
重放时 `out.Status == "working"` 会调 `getTask` 刷新（`protocol_tools.go:437-450`）。
这条链路对 image 与 video 是同一条，接上 `getTask` 后自动可用。

已知边界：`requestFingerprint(kind, payload)` 不含 async 标记。把某个模型的 `execution`
从 sync 改成 async 之后，旧的 `client_request_id` 重放会拿到旧形态的结果。语义上仍然正确
（同参数同结果），不处理。

### 2.7 fanout：异步不拆单

`fanoutCount` 增加一个前提：`!m.Async`。

- 上游的 `n` 是串行的（qwen n=4 实测约 349 秒）—— 异步任务不怕串行，它本来就不占连接，
  也不受 MCP 的 8 分钟超时约束。
- 拆单的理由（同步超时 300s 丢产物、计费 20000）在异步下不存在。
- 反向代价：单个任务失败 = 整批失败；同步拆单能拿到部分产物。这里选「不丢句柄」而不是
  「部分成功」—— 丢句柄意味着已计费的任务无人认领。
- 替代方案（并发提交 N 个任务、返回 N 个 task_id）被否决：`task.get` 要变成复数契约，
  agent 要跟踪 N 个句柄，而 `generateOutput.TaskID` 是单值。收益是「部分成功」，代价是
  契约复杂度翻倍。

### 2.8 提交端点选择

```go
t := getTransport(m.Kind)
path := t.Generate
if m.Async { path = t.AsyncGenerate }
```

`video.AsyncGenerate == Generate`，所以视频行为不变。这是本次唯一的路径分支。

---

## 3. 契约变更明细

### P0（已实施，26 行逻辑 + 注释与文案）

| 文件 | 位置 | 改动 | 行量 |
|---|---|---|---|
| `mcp/protocol.go` | `transport` | +2 字段 | 3 |
| `mcp/protocol.go` | `transports` | image +2 行、video +1 行、注释修订 | 6 |
| `mcp/protocol.go` | 新增 `submitPath(async bool)` | 选端点 | 8 |
| `mcp/protocol_tools.go` | `generate` | 用 `t.submitPath(m.Async)` | 3 |
| `mcp/protocol_tools.go` | `fanoutCount` | `if m.Async { return 1 }` | 2 |
| `mcp/protocol_tools.go` | 三处文案 | 见 2.1 | 3 |
| `mcp/protocol_tools.go` | `modelsSummary` 的 tag | 执行形态为 async 时加「异步」 | 2 |
| `mcp/protocol_tools.go` | 异步提交的 text | 带上 `defaultedModelNote`，与同步分支一致 | 2 |
| `mcp/README.md` | 147-151 行 | 一个请求形状、两种响应形状 | 4 |

### P1（可选，约 15 行）

`taskPayload.Mode` + 签发/解析 + `getTask` 形态校验 + 缺口 4 的「拿到 task_id 就签」。
必须与缺口 4 同一个提交。

### P2（可选，约 10 行）

进度透出（§2.5）。

**合并面**：P0 全部落在 `mcp/`。实测 `go run ./tools/markercheck -cost` 为
172 / 530 / 5633 / 415，与 `.apiduo/merge-budget.txt` **逐项一致**。

### 已落地（2026-11-17，0.0.183）

| 文件 | 改动 |
|---|---|
| `mcp/protocol.go` | `transport.AsyncGenerate`、image/video 表项、`withExecution` |
| `mcp/protocol_upstream.go` | `rootURL` + `url(path)`，让 `^` 前缀能表达站点根路径 |
| `mcp/protocol_tools.go` | `generate` 选路、`fanoutCount`、清单「异步」标签、异步提交的默认模型说明、三处文案 |
| `mcp/protocol_server.go` | instructions 第 3 条改为「部分模型异步」（图片也算） |
| `mcp/README.md` | 「一个请求形状、两种响应形状」 |

线上声明 `nano-banana-2`：`kind=image`、`async=1`、`enabled=1`、`priority=50`、
`cost=$0.01/次`（`mcp_model_profiles.id=31`）。

新增 `mcp/image_async_test.go`，五个用例：形态表、异步端点、同步不被带偏、
异步不拆单、清单标记。`go test ./mcp/` 全绿，`gofmt` / `go vet` / `go build ./...` 无输出。

**可整块回滚**：删掉 image 的 `AsyncGenerate` / `Query` 两行，`submitPath` 回落
`Generate`，行为立刻回到「异步图片不可用」，而不是错乱。

### 测试（新增文件，不计预算）

| 用例 | 锁的是 |
|---|---|
| async 模型提交打 `/async/v1/images/generations`；sync 模型打 `/images/generations` | 缺口 2 |
| `202 {task_id}` → `working` + 句柄可查 | 主链路 |
| `200 status:"succeeded" + data` → `completed` + outputs | 新契约（v1 的这条用例要改写） |
| `200 status:"failure"` → `failed` + code/retryable | 失败路径 |
| async + `n=4` → 上游只收到一次提交 | 缺口 3 |
| `models.list` 的 text 里 async 模型带「异步」标记 | §1.5 的混淆源 |
| P1：同步 image 句柄 → `TASK_ID_INVALID`；旧 video 句柄仍可查 | 2.3 |

---

## 4. 管理面

现有 `Execution` 下拉（`Sync response` / `Async task · task.get`，
`web/src/features/mcp/profiles-panel.tsx:836`）已经对 image 开放，不需要新控件。补一处：

**保存时软校验**：勾 async 且 `kind=image` 时，检查是否有渠道能服务异步入口 ——
8001 渠道覆盖该模型，或 `LOCAL_IMAGE_ASYNC_ENABLED=true`。都不满足时在表单下方提示：
「该模型当前没有可服务异步入口的渠道，勾选后调用会返回 503」。

不阻断的理由：渠道可能正在配置中，硬拦会让运维无法先声明后接线（与 `knownKinds`
允许「先声明、端点后接」同一逻辑）。

---

## 5. 错误矩阵

| 上游 / 网关返回 | MCP 出口 | agent 下一步 |
|---|---|---|
| `202 {task_id}` | `working` + task_id + `poll_after_ms` | 等 10 秒查 |
| `202 status:"queued"` | `working` | 同上 |
| `202 status:"in_progress"`（+ 可选 progress） | `working`（+ `progress_percent`） | 同上 |
| `200 status:"succeeded" + data` | `completed` + outputs + text 里的 URL | 交给用户 |
| `200 status:"failure"` + `task_failed` | `failed` + code / `retryable` | 按 `retryable` |
| `200 status:"failure"` + `empty_result` | `failed`，**必须不可重试** | 去控制台确认；不要重新生成 |
| `404 task_not_found` | `TASK_ID_INVALID` | 不要重试；重新提交 |
| `413 request_too_large_for_async` | `UPSTREAM_ERROR` | 改用 URL 参考图；不要重发同一 body |
| `503` 无可用渠道 | `UPSTREAM_ERROR` | `call_models_list` 或改用同步模型 |

`413` 与 `empty_result` 都走 `mcp_error_rules`（数据驱动），**不改代码**。

`empty_result` 必须显式配规则的理由：它的 message 是
`upstream reported success without a result url`，`transientTaskFailure` 的关键词表命中不了，
于是落到兜底 hint「不要用同样参数重试；先确认参数或换模型」。而这条恰恰发生在**已经计费**
之后，「换模型」等于让 agent 再花一次钱。建议：

```
match        = without a result url
retryable    = false
retry_action = check_console
hint         = 上游已判定成功但没给地址，产物可能仍在控制台任务页；不要重新生成
```

`ruleMatches` 是大小写折叠的子串匹配（`protocol_error.go:189-202`），不必匹配整句；
`check_console` 已是 `serverInstructions` 里定义过的动作词。

---

## 6. 反向意见（这套方案在什么情况下是错的）

1. **如果异步图片模型长期只有 1–2 个**，机制仍然值得做（约 26 行），但收益只是
   「这两个模型不再超时」，不要包装成「图片全面异步化」。判断依据：
   `SELECT count(*) FROM mcp_model_profiles WHERE kind='image' AND async=1`。
2. **如果运维不打算开 8001、也不开 `LOCAL_IMAGE_ASYNC_ENABLED`**，异步入口没有渠道服务，
   MCP 改动全是死代码。上线顺序必须反过来：
   **确认渠道 → 改 MCP → 再把 `async` 勾上**。反过来做会得到一批 503。
3. **如果图片主要 ≤30 秒完成**，异步带来的是「多一次往返」，体验更差。异步值得做的判据
   是「同步路径已经在超时」：`mcp_observations` 里 `image.generate` 的 p95 延迟，与
   `UPSTREAM_TIMEOUT` 码的出现次数，可以量化这条。若 p95 < 60s 且没有超时样本，先别做。
4. **如果打算把 `n` 保留给上游**（§2.7），要接受「一张失败就整批失败」。若不可接受，
   正确做法是让 agent 自己调 N 次 `image.generate`，而不是在 MCP 内部拆单后丢掉
   N−1 个句柄。
5. **如果将来 MCP SDK 支持 Tasks 扩展**（`resultType:"task"` + `tasks/get`），今天的
   `task.get` 要迁过去。v2 不会让迁移面变大 —— 它没有引入任何新的判定语义，只是把已有的
   三态接到了新端点上。

---

## 7. 验收

1. 把一个真实异步图片模型设为 `async`，`image.generate` 返回 `working` + task_id，
   `task.get` 在 2–5 次轮询内返回 `completed` 与可打开的 URL。
2. 完成那一刻 `task.get` 的 text 里有 URL，structuredContent 里 `outputs[0].url` 同值。
3. 把同一模型改回 `sync`：`image.generate` 恢复同步返回，不再产生 task_id。
4. `n=4` 的异步调用，任务页只有 1 条任务、1 次计费。
5. **负向**：断掉异步渠道后调用，返回 `UPSTREAM_ERROR` 且 hint 指向「改用同步模型」，
   而不是一句「上游未返回图片数据」。

---

## 8. 不做清单

1. 不加 `image.generate_async` 工具。
2. 不加 `async: true` 入参（协议选择不能交给客户端）。
3. 不做 `/v1/images/edits` 第二入口（图生图仍靠 `image` 字段）。
4. 不做 `task.cancel`（图片任务秒级完成，取消的价值低于实现成本）。
5. 不把 `progress` 变成重试触发条件。
6. 不在 MCP 复制 `LOCAL_IMAGE_ASYNC_ENABLED` / 8001 的判定（渠道细节对客户端不可见）。
7. 不加 `TerminalOnData` 之类的完成态兜底 —— 契约已经承诺 `status`，消费端不该替生产端兜底。
8. 不改 `abilities` 表、不加路由实体、不动端点注册表（异步图片端点自带 model，
   不属于 `pkg/endpoint/specs` 那类无 model 端点）。
9. 不允许同一个 `model_id` 同时声明 sync 与 async（加 `async_variants`、按 group 区分、
   加 `mode` 入参都不行）。`model_id → (kind, execution)` 必须单射，否则 `models.list`
   无法表达、`task_id` 归属歧义、计费口径分裂。

---

## 附：同步模型的渠道配置（线上渠道 33）

异步配好之后补同步，走的是另一条渠道，不是改声明：

| 项 | 值 |
|---|---|
| 上游端点 | `POST https://hailuo.1task.cn/v1/images/generations`（OpenAI 形状） |
| 渠道类型 | **1（OpenAI）**，不是 8001 |
| models | `gpt-image-2` |
| group / priority / weight | `default` / 1 / 1（与 #29 一致） |
| 价格 | `options.ModelPrice` 里 `gpt-image-2: 0.02` —— **不配会走默认倍率，按 token 算图片** |
| MCP 声明 | `async=0`、`priority=50`、`$0.02/次` |

实测：直调 15.85s、经 MCP 14.8s，均返回 `{data:[{url}]}`，`usage.forecast_credits=2`。

### 两个必须知道的坑

1. **直接 INSERT 渠道要同时写 `channel_info`。** 多 key 渠道（`key` 用换行分隔多个 JWT）如果不写
   `{"is_multi_key":true,"multi_key_mode":"polling","multi_key_size":N,...}`，整串 key 会带着换行被
   塞进 Authorization，Go 的 `http.NewRequest` 直接拒绝 → `500 do_request_failed`，0.1 秒返回。
   面板建渠道不会踩，因为前端会写这个字段。
2. **读多 key 用 `mysql --raw`。** 不加 `--raw` 时换行被转义成字面 `\n`，524 字节的 key 看起来
   像「一个含 4 个点的怪 token」，会把排查引向「key 格式不对」而不是「多 key 未标记」。

### 同一个模型名在网关层可以有两条渠道

`nano-banana-2` 只在 8001（异步），`gpt-image-2` 只在 type 1（同步）。如果将来把同一个模型名
同时挂到两类渠道上，网关会在两条路径上分别返回对应形状 —— 但 **MCP 层仍然只能声明一种**
（`uk_mcp_profile_model`），这是刻意的：客户端不该根据「这次调用是不是同步」来决定怎么解析。
