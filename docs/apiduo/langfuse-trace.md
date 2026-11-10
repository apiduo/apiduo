# Langfuse Trace 接入日志页 · 方案

> 结论：可以做，但**主要工作量在 relay 侧的上报，不在日志页**。
> 最终形态：**全内容采集**——请求体 / 流式帧 / 非流式响应 / agent messages 全采，
> 默认 headtail 截断（头 32K + 尾 16K）。性能结论见 §3.5 实测：截断模式 +0.6 μs/请求。
> 决策过程（A 档 → B 档 → C 档翻案，中间因 SSE 成本误估走过弯路）见 §12。
> 日志页只加一行「在 Langfuse 打开」链接，且仅对管理员可见。
>
> 与 2026-11-03《NewAPI LLM 可观测性解决方案》v2.1 的关系见 **§12**——那份方案的
> 两条设计原则仍然成立，但具体锚点与四个技术前提已被代码演进推翻，不能照抄。

---

## 0. 第一原则：不影响原有系统性能

这条是**硬约束**，优先级高于功能完整性、成本、工期。任何触碰 relay 主链路字节流的设计
直接出局，不进入"优化一下也许行"的讨论。

### 0.1 "旁路"能挪走什么，挪不走什么

**能异步化的只有"处理"（序列化、脱敏、发送），"抓取"必须发生在数据经过挂接点的那一刻。**
不能异步地接一桶已经流过去的水。

但抓取成本取决于那份数据是不是**本来就已经在内存里**——这决定了三类采集点的成本差：

| 采集对象 | 数据来源 | 抓取成本 | 判定 |
|---|---|---|---|
| 元数据 | 结算函数里的现成字段 | 2.3 ns（关闭）/ 360 ns（开启） | 无条件做 |
| **请求体** | `KeyBodyStorage` 已缓存，`memoryStorage.Bytes()` **零拷贝返回引用** | 跨请求生命周期需一次 memcpy，中位 body 几 KB ≈ 0.4 μs | **低采样下可做** |
| **非流式响应体** | relay 已完整 `ReadAll` 过一次 | 同上 | **低采样下可做** |
| **流式响应内容** | **`StreamScannerHandler` 的 `dataHandler` 已经拿到解码后的帧明文** | append 到 buffer；500 KB ≈ **+11.6 μs**（实测） | **可做，见 §3.6** |

关键区别：**请求体和非流式响应体是"复用一次已经发生过的读取"，流式内容是"新增一次读取"。**
前者不改变主链路的 I/O 形态，后者会。

所以修正此前的判断：**"内容采集出局"是错的。** 三档都可行，差别只是成本量级（见 §3.6 实测）。

> **更正**：此前把 SSE tee 估成「每 chunk 1 μs、1000 chunk ≈ 1 ms」是错的。
> 那个数字把「write 系统调用」算进去了，而真实写本来就要发生，采集只多一次 memcpy。
> 实测 1000 chunk × 500 B 全量累积是 **+11.6 μs**，低两个数量级。

### 0.1.1 成本分水岭是截断，不是"流式 vs 非流式"

（曾设想 B 档做折中：元数据 + 请求体 + 非流式响应、流式出局。实测推翻——
流式采集只 +11.6 μs，而流式恰是 LLM 主场景，B 档没有存在价值，已取消。）

| 策略 | 额外 CPU | 折合 5859 QPS | buffer 内存（千并发） |
|---|---|---|---|
| 全量（平均 500 KB） | +11.6 μs | 6.8% 单核 | ~512 MB |
| **头 32 KB + 尾 16 KB** | **+0.6 μs** | **0.35% 单核** | **~48 MB** |
| 只计长度不存内容 | +0.05 μs | 0.03% 单核 | 0 |

32 KB 是拐点：开销降 20 倍，内存降 10 倍。代价是长回答会被截断（标记 `truncated`）。

如果确实需要全量：`LANGFUSE_BODY_MODE=full`，但要知道那是 6.8% 单核 + 512 MB 的账，
且必须配 `sync.Pool` 复用 buffer，否则 GC 会先崩。

### 0.1.2 真正零成本的唯一路径

让**客户端自己产生 trace**（OpenAI SDK + Langfuse SDK），网关只透传 trace id 并给链接。
网关侧零成本、零内存、零带宽。代价是要求调用方改造，且只对自有 Agent / 内网客户端有效。

### 0.2 量化验收标准（不达标即回退）

| 项 | 标准 | 验证方式 |
|---|---|---|
| 关闭态 | 主链路**零新增堆分配**，P99 与改动前无差异 | `go build -gcflags=-m` 确认不逃逸；`go test -bench` 对比空函数 |
| 关闭态指令数 | 只有一次 `atomic.Bool.Load` + 分支 | 已实测 **2.3 ns / 0 alloc**（§3.4） |
| 开启态（headtail 全开） | `/api/perf-metrics` 的 P99 相对基线波动 < **0.5%** | 直接复用项目已有指标做前后对照 |
| 峰值压测 | 5859 QPS（历史实测峰值）下成功率不变、GC pause 无增长 | 见 §9 |
| 队列满 | 直接丢，**绝不出现任何等待** | 代码审查 + 丢弃计数断言 |

> 为什么敢定 0.5% 而不是"3~5%"：§3.5 实测——采集路径 +0.6 μs，摊在 3–8s 的流式请求上是
> 10⁻⁷ 量级；真正可测的影响只剩 GC 抖动（§3.9 内存账已把它压到 74 MB 上界）。

---

## 1. 问题拆解

"把 langfuse trace 集成到日志页"这句话背后其实是两件事，必须分开算账：

| 子问题 | 现状 | 工作量 |
|---|---|---|
| A. 日志行 ↔ trace 的关联键 | **已存在**：`Log.RequestId`（`model/log.go:75`，已有 `idx_logs_request_id` 索引） | 0 |
| B. trace 在哪里产生 | **不存在**：全仓无 langfuse / otel / traceparent，apiduo 从未向任何 observability 后端发过数据 | 95% |
| C. 日志页展示入口 | 详情弹窗 `details-dialog.tsx:707` 已在展示 `request_id`，且有现成的 `props.isAdmin` | 5% |

所以这不是"加个链接"的需求，而是**给网关补一层 LLM observability 埋点**，然后把结果挂到已有日志上。

再往下一层：既然要发，就别写"Langfuse 集成"，要写 **OTLP 导出**。
Langfuse 自 v3.22 起就是标准 OTel 后端，同一份导出换 endpoint 就能对接 Phoenix / Tempo / Jaeger。
Langfuse 只是其中一个 consumer，专有属性（`langfuse.*`）在别的后端里被忽略，无副作用。

---

## 2. 数据契约

三条硬约束决定了设计：

1. OTEL 的 traceId 必须是 **32 位 hex**。
2. `common.NewRequestId()`（`common/utils.go:256`）产出 39 位（14 时间戳 + 9 纳秒 + 8 hex + 8 随机），**不能直接当 traceId**。
3. Langfuse 的 trace 页面 URL 需要 **projectId**，不是 API key。

因此：

```
traceId = hex( sha256(request_id)[:16] )   // 32 chars，确定性派生
```

- 派生逻辑放在 `pkg/apiduo/trace/id.go`，**单点定义**，上报侧和展示侧都从 `log.other` 读结果，不在前端复刻哈希。
- 异步任务（兜底路径）的 trace_id 用**提交时**的 request_id 派生（从 task 记录取），不是轮询结算时的——同一个任务提交与结算必须是同一条 trace。
- traceId 写入日志的 `other.admin_info`（走 `LogOther.SetAdmin`），**不落库到新列**：
  - 绕开 ClickHouse `logs` 表加列（现为 `ReplacingMergeTree`，加列要 ALTER，且 GORM 对 CH 的 AutoMigrate 有限制）
  - 走 admin 可见性，普通用户拿不到（见 §6）
  - 体积开销 32 字节/行，可忽略

日志页链接形如：

```
{LANGFUSE_HOST}/project/{LANGFUSE_PROJECT_ID}/traces/{traceId}
```

> 若自建版本路由不同，退化用 `{host}/trace/{traceId}`（会 302 到 project 路由）。上线前实测一次。

---

## 3. 架构

### 3.1 四层流水线：采集 → 快照 → 处理 → 导出

```
主链路（per-request 生命周期）              离线（与请求无关）
┌─────────────────────────────┐    ┌──────────────────────────────┐
│ ① collect  collector 状态机 │    │ ③ pipeline  worker 池        │
│   挂 gin.Context，随请求生灭 │    │   SSE 帧解析（按 relayFormat）│
│   流式帧 append（无锁）      │──► │   脱敏 / 组装 OTLP JSON      │
│   usage 填充（结算点）       │快照│   批量 200 / 5s              │
│   ② snapshot 在 relay defer │──► │ ④ sink  OTLP → Langfuse      │
│   一次快照，从此与主链路无关 │    │   失败重试 1 次，超时 3s      │
└─────────────────────────────┘    └──────────────────────────────┘
```

核心是②：**快照是唯一的隔离边界**。快照之前，collector 可以引用主链路的任何数据
（BodyStorage、SSE buffer、RelayInfo 字段）；快照之后，主链路对这份数据负零责任——
worker 想多慢就多慢，请求早已结束。两个内存世界在这一点完成交割。

### 3.2 collector 状态机（主链路上唯一的可变对象）

所有挂接点不再"各自上报"，而是向同一个 per-request collector 写状态。
collector 挂在 `gin.Context`（`c.Set`，与上游 `KeyBodyStorage` 同模式），随请求生灭：

```
        Relay() 开头                relay 过程                    relay defer
           │                        │                              │
读 traceparent ──► 创建 collector   │                              │
存入 ctx         │                  │                              │
                  ├─ stream_scanner: dataHandler 帧 append ──► 累积 buffer
                  ├─ compatible_handler: 非流式体 append ────► 累积 buffer
                  └─ text_quota 结算: usage / TTFT / 重试次数 ──► 字段填充
                                                 │
                                        finalize：一次性快照
                                        · body 此刻仍存活（见 3.4 ②），memcpy 入快照
                                        · SSE buffer 所有权转移入快照
                                        · 采样判定 + 入队（元数据队列 / 内容队列）
```

**为什么上报点从结算点挪到 relay defer**——三个理由，全是结构性的：

1. **defer 全覆盖**：成功、失败、panic（`recovered` 分支）都走 `controller/relay.go:140`
   的那个 defer，`perfmetrics.RecordRelayResult` 就在那。失败请求不再没有 trace——
   而失败恰恰是排障时最想看内容的场景。
2. **结算点语义降级为"字段填充"**：`text_quota.go` 不再决定"何时上报"，只写 usage。
   时序天然正确：结算先于 defer，defer 时 usage 已就位。
3. **body 生命周期安全**（见 3.4 ②）：defer 时 body storage 必然存活，
   快照可以在那一刻从容 memcpy，不需要提前拷贝占用峰值内存。

### 3.3 挂接点（后端 7 + 前端 3）

| # | 文件 | 动作（1–2 行，全在标记块内） | 语义 |
|---|---|---|---|
| 1 | `controller/relay.go` `Relay()` 开头 | 读 `traceparent` → 存 collector（与挂接点 5 同文件） | trace 接续 |
| 2 | `relay/helper/stream_scanner.go` | `dataHandler = wrap(handler)` | 流式帧采集 |
| 3 | `relay/compatible_handler.go` | 复用已读 body 字节 | 非流式采集 |
| 4 | `service/text_quota.go` | collector 填 usage 字段 | 字段填充 |
| 5 | `controller/relay.go` defer | `finalize + 入队` | **唯一上报点** |
| 6 | `model/log.go`（RecordConsumeLog） | 构元数据快照入队 | 兜底上报：任务结算 / MJ / 违规扣费等**无 relay defer 的路径** |
| 7 | `router/api-router.go` | +1 行配置路由 | AdminAuth |
| 8–10 | 前端 3 处（弹窗 / api / i18n） | 链接渲染 | 仅管理员 |

挂接点 6 的存在理由：异步任务结算是后台轮询线程，`task_billing.go` 那条路没有
relay defer 也没有流式响应，走 `model/log.go` 直接构快照（只有元数据 + 提交时的
request_id，从 task 记录取）。

### 3.4 三条生命周期不变量（架构公理，违反任何一条就是 bug）

**① 快照即边界。** 入队后主链路不再触碰快照数据；快照前的所有引用必须在 finalize
内完成拷贝或所有权转移。推论：worker 持有的 buffer 与主链路零共享，无锁、无竞态。

**② body 的安全窗口。** `CleanupBodyStorage` 在 `middleware/body_cleanup.go` 的 defer
里执行；Go 的 defer 是 LIFO，**中间件 defer 晚于 handler（Relay）defer**。所以 relay
defer 时 body storage 必然存活。防御式补充：finalize 若发现 body 已 Close
（`Bytes()` 返回 `ErrStorageClosed`），降级为元数据快照，不报错。

**③ 内容不落盘。** trace 队列**绝不复用 log-writer 的 spool**——spool 会把数据写盘，
内容一旦落盘就把泄露面从内存扩大到磁盘（还有磁盘增长问题）。优雅停机只 drain
内存队列（上限 5s），超时直接丢。观测数据丢得起，这是与记账日志的本质区别。

### 3.5 主链路开销（实测，M1 Max / go1.26）

```
BenchmarkBaselineEmpty       0.33 ns/op     0 B/op    0 allocs   ← 空循环基线
BenchmarkDisabled            2.3  ns/op     0 B/op    0 allocs   ← 关闭态
开启态 · 仅元数据             144–364 ns     128 B/op  1 alloc
开启态 · headtail 截断        +0.6 μs        ≤48 KB buffer
开启态 · 全量 500 KB          +11.6 μs       ≤500 KB buffer
```

设计规则（写死，不随实现漂移）：

- `Record` 入口必须可内联，关闭态只是一次 `atomic.Bool.Load`；
  重活全部压在 `//go:noinline` 的慢路径里
- 主链路**零序列化、零正则、零 json.Unmarshal**；帧解析、脱敏全部在 worker
- 采样判定用 `atomic.AddUint64(&n, 1) % 100 < rate`，不用 `math/rand`（全局源有锁）
- 采样在 `Begin` 判定一次并存进 `collector.sampled`，日志的 `trace_id` 只在采样命中时才写；
  失败请求允许被 `LANGFUSE_ERROR_SAMPLE_RATE` 追加放行。**不要在 defer 里才判定**——
  那样 10% 采样率下九成日志会带一个在 Langfuse 里不存在的 id，链接点开就是 404
- 错误日志（type=5）走 `ForceSample`：**先把 `sampled` 钉成 true，再落 id**，不靠
  `LANGFUSE_ERROR_SAMPLE_RATE` 抽中。那个值随时可能被调低，而链接的可信度不能跟着变；
  失败请求永远到不了结算，consume 日志那条路根本不会执行
- 队列满 `select/default` 直接丢 + 丢弃计数，**绝不等待**——log-writer 那套
  「队列满回退同步」是记账正确性策略，trace 不是账，两个策略永不统一

### 3.6 内容采集与截断

三个采集点，全部是"复用一次已经发生的读取"：

| 数据 | 位置 | 方式 |
|---|---|---|
| 请求体 | finalize 时读 `GetBodyStorage(c)` | `Bytes()` 零拷贝引用 → 快照时一次 memcpy |
| 流式响应 | `StreamScannerHandler` 包 `dataHandler` | 帧明文 append 到 per-request buffer |
| 非流式响应 | `compatible_handler` 读 body 处 | 复用已 `ReadAll` 的字节 |

SSE 不需要 tee：`dataHandler` 拿到的 `data` 已是去掉 `data: ` 前缀的帧明文
（`relay/helper/stream_scanner.go:120`，12 个渠道文件共用这个入口）。
包装 ResponseWriter / io.MultiWriter 的方案已被否定。

截断策略 `headtail`（默认）：头 32 KB + 尾 16 KB，置 `truncated=true`、`dropped_bytes=N`。
SSE 流的头部有 role / model / 首 delta，尾部有 `finish_reason` 和 usage，信息密度最高；
中间是重复文本 delta。三种模式：`off`（纯元数据）/ `headtail`（默认）/ `full`（上限 2 MB，
接受 6.8% 单核 + 512 MB/千并发的账，须配 `sync.Pool`）。

### 3.7 agent 与 trace 树

**采样必须尊重 traceparent 的 sampled flag**（W3C 语义）：

```
traceparent: 00-{trace-id 32hex}-{parent-span-id 16hex}-{flags 2hex}
                                                          ^^^^
                                        flags bit0 = sampled；父未采样，子默认不采
```

| 情况 | 行为 |
|---|---|
| 带 `traceparent` 且 sampled=1 | 用客户端 trace-id，**parentSpanId 设为客户端 span-id**，正常采样 |
| 带 `traceparent` 且 sampled=0 | 沿用客户端 trace-id（保树完整），本 span 的采样决策独立做 |
| 不带 | `sha256(request_id)[:16]` 派生 trace-id，parentSpanId 空 |
| 转发上游 | 透传 `traceparent` / `tracestate`（当前网关把它们弄丢了，上游 OTEL 链是断的） |

不设 parentSpanId，span 会挂成新 trace 的根——agent 树就断了。这是树正确性的两个必要条件之一。

能采到的 agent 数据：`messages[]`（role / content / tool_calls / tool_results）、
`tools[]` 定义、采样参数、text delta、`delta.tool_calls[]`（含 name / arguments 分片）、
finish_reason、usage。**采不到**的：agent 内部 planning / 记忆（在客户端进程里），
只能靠 traceparent 接续。

### 3.8 失败请求与重试语义

**失败请求默认全采**（挂接点 5 在 defer，天然覆盖）。理由：失败是少数（正常 <5%），
全采不构成量级压力；而排障场景里"失败请求到底发了什么、上游回了什么"价值最高。
`LANGFUSE_ERROR_SAMPLE_RATE` 独立于常规采样率，默认 100。

**重试（auto 跨渠道）语义**：一个请求 N 个 attempt，collector 的 SSE buffer
**只保留最后一个 attempt**——每次重试前 reset（与 `InitChannelMeta` 重置 converter
的既有惯例同构）。RetryIndex / 尝试次数进元数据。要全 attempt 内容属于 `full` 模式的
诊断特性，v1 不做。

半截流（上游中断、客户端断连）：buffer 里有什么发什么，元数据标 `status=interrupted`。
这恰是断流诊断的核心样本。

### 3.9 内存账（最坏场景上界）

之前的账只算了 per-request buffer，漏了**队列自身的驻留内存**。补全：

| 项 | 规模 | 上界 |
|---|---|---|
| 元数据队列 | 2048 × ~128 B | 256 KB |
| 内容快照队列 | 512 × ≤48 KB | **25 MB** |
| per-request buffer（千并发） | ≤48 KB × 1000 | 48 MB |
| 全量模式（可选） | ≤500 KB × 1000 + 队列 256 MB | 768 MB（需显式开启） |

合计 headtail 默认配置最坏 **~74 MB**，可控。两条派生规则：

- 内容队列与元数据队列**分离**：元数据队列绝不能被大快照挤占（否则低流量站点
  连元数据 trace 都丢）
- `sync.Pool` 池化 per-request buffer 列为**条件触发**：默认不池化
  （中位请求 SSE 总量仅几 KB，分配速率 ~13 MB/s @1667 QPS，可接受）；
  线上 GC profile 显示压力再启用，避免一开始就背生命周期管理的复杂度

---

## 4. 配置项

初始值全部来自环境变量（`common.GetEnvOrDefault` 风格）。§19 起后台多了配置页、
§24 起常用参数在页面上热更新——**env 只在「从未保存过设置」时生效**：

```bash
LANGFUSE_ENABLED=false                              # 总开关
LANGFUSE_HOST=https://langfuse.example.com          # 自建地址，不带尾斜杠（上报用）
LANGFUSE_CONSOLE_URL=https://langfuse.example.com   # 日志页跳转链接的基地址；留空 = 同 HOST。
                                                    # 上报走内网容器名时必须单列，否则链接不可解析
LANGFUSE_PUBLIC_KEY=pk-lf-...                       # 只用于 Basic 认证
LANGFUSE_SECRET_KEY=sk-lf-...                       # 只用于 Basic 认证，永不外发
LANGFUSE_PROJECT_ID=...                             # 从 Langfuse UI 地址栏取
LANGFUSE_SAMPLE_RATE=10                             # 0-100，常规元数据采样
LANGFUSE_CONTENT_SAMPLE_RATE=10                     # 0-100，内容（prompt/回答）采样，见 §3.6
LANGFUSE_ERROR_SAMPLE_RATE=100                      # 失败请求采样（独立，默认全采，见 §3.8）
LANGFUSE_REDACT=false                               # 仅认字面 false；任何其它值（含未设）= 开
LANGFUSE_ENVIRONMENT=production                     # langfuse.environment 标签，空 = 不打标签
LANGFUSE_RELEASE=                                   # 显式 pin 版本号；默认取构建版本（§21.1）
LANGFUSE_BODY_MODE=headtail                         # off / headtail / full（§3.6）
LANGFUSE_BODY_HEAD_BYTES=32768                      # headtail 头部上限
LANGFUSE_BODY_TAIL_BYTES=16384                      # headtail 尾部上限
LANGFUSE_TIMEOUT_SECONDS=3
LANGFUSE_QUEUE_META=2048                            # 元数据队列深度
LANGFUSE_QUEUE_CONTENT=512                          # 内容队列深度（×48KB 上限 = 25MB 上界，§3.9）
```

运行时改 env 需重启；所有开关读取后缓存进 atomic 变量（§3.5 内联早退依赖它）。
2026-11-10 起，采样、内容窗口、失败采样、脱敏、环境标签这五组改为**后台热更新**
（§24）：保存即生效，无需重启；env 值只作为「从未保存过」时的初始值。

认证头：`Authorization: Basic base64(pk:sk)`，同时带 `x-langfuse-ingestion-version: 4`（不带这个头，v4 数据可能延迟 10 分钟才可见）。

端点：`POST {LANGFUSE_HOST}/api/public/otel/v1/traces`，`Content-Type: application/json`（OTLP/HTTP JSON，不用引 protobuf 依赖，也不必引完整 otel SDK）。

> 直接用 JSON 手写 OTLP payload，比引 `go.opentelemetry.io/otel` + `otlptracehttp` 更轻，且完全可控。代价是 payload 结构要自己维护——每请求 1 个 span、字段固定（§3.2 span 结构），结构漂移风险低。

---

## 5. 代码落点

新增文件的完整清单见 §13.1（trace 包 10 个文件 + controller 1 个 + 前端 2 个）。本节只列上游文件的修改：

| 文件 | 动作 | 说明 |
|---|---|---|
| `controller/relay.go`（两处，同文件） | 修改（标记块） | +2 行：`Relay()` 开头读 `traceparent`；defer 内 finalize |
| `relay/helper/stream_scanner.go` | 修改（标记块） | 1 行：包 `dataHandler`，流式帧 append |
| `relay/compatible_handler.go` | 修改（标记块） | 1 行：非流式体 append |
| `service/text_quota.go` | 修改（标记块） | 1–2 行：usage 填充 + trace_id 写 `other`（见下） |
| `controller/relay.go` | 修改（标记块） | 1 行：defer 内 finalize（**唯一上报点**） |
| `model/log.go` | 修改（标记块） | 1 行：兜底快照（无 relay defer 的路径） |
| `router/api-router.go` | 修改（标记块） | 1 行：配置路由（AdminAuth） |
| `web/.../dialogs/details-dialog.tsx` | 修改（`//#region apiduo`） | 3 行：条件渲染链接 |
| `web/src/features/usage-logs/api.ts` | 修改（`//#region apiduo`） | 6 行：`getTraceConfig()` |

**依赖方向（对齐 `pkg/apiduo/discount` 先例）**：trace 包**零内部 import**
（不引 `model`/`service`/`common`/`relay`），配置用 `os.Getenv` 直读。因此：

- `LogOther.SetAdmin("trace_id", ...)` 这种碰 `model` 类型的动作**由挂接点调用方完成**
  （`text_quota.go` 本来就 import model）：结算点写日志前把 trace_id 塞进 `other`
- collector 本体只存 string / int，不持有任何上游类型；finalize 的入参是裸字段
- 后果：失败请求的 trace 在 Langfuse 有，但日志页无入口——失败请求不写消费日志，
  本来就不出现在那个列表里，一致

标记块命名：`// APIDUO:trace:BEGIN` / `END`，需登记进 `.apiduo/merge-budget.txt`。

---

## 6. 权限与安全（对抗审查）

这一节是我最在意的部分，四条都不能省：

1. **链接只给管理员。**
   Langfuse 没有按用户的行级隔离——同一个实例里所有人的 trace 都在。如果普通用户拿到链接和 projectId，改一下 URL 就能看别人的请求元数据。`details-dialog.tsx` 已有 `props.isAdmin`，直接用它门控。

2. **配置接口必须鉴权，不能塞 `/api/status`。**
   `/api/status` 是**未登录可访问**的公开接口。把 `LANGFUSE_HOST` 放进去等于公开内网地址和 projectId。改为独立的 `GET /api/apiduo/trace/config` + `middleware.AdminAuth()`，且**永远不返回 secret key**。

3. **traceId 走 `SetAdmin`，不走 `SetPublic`。**
   `formatLogOtherJSON` 在 user 视角会剥掉 `admin_info`（`model/log_other.go:246`），这正好是我们要的可见性。

4. **不采集内容。**
   元数据模式不含 prompt / completion / 请求头 / IP。`userId` 会外发，但 Langfuse 在内网自建，且这是追踪的必要维度；若认为 `userId` 也算 PII，可退化为哈希。

另外两个容易被忽略的：

- **`RecordConsumeLog` 在 `LogConsumeEnabled=false` 时直接 return**（`model/log.go:345`）。意味着兜底路径（异步任务结算）关掉消费日志就没有 trace；但同步请求的 relay defer 路径**不受影响**——这是 finalize 放 defer 而非结算点的额外红利。
- **内容不落盘是硬约束**（§3.4 ③）：trace 队列不复用 log-writer 的 spool；停机只 drain 5s 超时即丢。谁想给 trace 加磁盘缓冲，先读这一条。
- **采集内容不出内网**：Langfuse 自建于 apiduo.cn 内网，内容经 https 直发，不经过任何第三方；脱敏（§3.6）是纵深防御层而非唯一防线。
- **失败请求不写 consume log**，所以 4xx/5xx 不会有 trace。v1 接受这个缺口，v2 再挂到 error log 上。

---

## 7. Langfuse 自身的部署账

这是最容易被低估的部分。自建 Langfuse v3/v4 **不是单容器**：

| 组件 | 能否复用现有 | 说明 |
|---|---|---|
| ClickHouse | ✅ 可复用实例 | 独立 database（如 `langfuse`）。你现有的是 25.8，满足 ≥24.3 要求 |
| PostgreSQL | ❌ 需新建 | 不能拿 MySQL 顶，Langfuse 用了 PG 特性 |
| Redis | ❌ 需新建（或复用现有缓存实例的独立 db） | |
| S3 / MinIO | ❌ 需新建 | v3 起强制 S3 兼容存储（事件上传） |
| langfuse-web + langfuse-worker | 新增 2 容器 | web 是 Next.js，内存胃口不小 |

净增量：约 **4–5 个容器，常驻 2–4 GB 内存**。上 ai-prod 前必须先看这台机器的余量，否则会和 apiduo 抢内存。

如果觉得这账不划算，有两个更轻的替代（两者都仍是 OTLP，换 endpoint 就行）：

- **Arize Phoenix**：单容器，可用 SQLite/PG，功能弱一些但够看 trace。
- **Langfuse Cloud**：零运维，但数据出境，与"自建"的初衷冲突。

---

## 8. 分阶段

| 阶段 | 内容 | 验收 |
|---|---|---|
| P0 | 确认 Langfuse 部署形态与机器余量；跑通 `curl` 手发一条 OTLP 到 `/api/public/otel/v1/traces` | Langfuse UI 能看见这条 trace |
| P1 | trace 包 + collector + 7 个挂接点 + 配置接口；默认关闭 | 单测：payload 结构、traceId 稳定性、队列满丢弃、**失败请求也产 trace**（defer 路径）、body 已 Close 时降级 |
| P2 | 内容采集：三采集点 + headtail 截断 + SSE 三格式解析 + 脱敏 | 截断正确性（头尾保留、dropped_bytes 准确）；半截流标 interrupted |
| P3 | 前端链接 + admin 门控 + agent 树（traceparent） | e2e：发真实请求 → 日志详情点链接 → 命中同一 traceId；带 traceparent 的请求在 Langfuse 成树 |
| P4 | 采样调优、丢弃告警、压测、部署实录 | §0.2 三项 + **最坏内存场景**（§3.9：全大请求 + 队列满载 ≤74 MB）+ Langfuse ingestion 吞吐压测 |

按解耦规范 v2，P1 需要额外提供**开启态最小 e2e**，不能只测关闭态的等价行为。

---

## 9. 验证方法

```bash
# 1) 手工验 OTLP 通路（先于写代码，确认 endpoint 和认证没问题）
AUTH=$(printf '%s:%s' "$LANGFUSE_PUBLIC_KEY" "$LANGFUSE_SECRET_KEY" | base64)
curl -sS "$LANGFUSE_HOST/api/public/otel/v1/traces" \
  -H "Authorization: Basic $AUTH" \
  -H 'Content-Type: application/json' \
  -H 'x-langfuse-ingestion-version: 4' \
  --data @payload.json

# 2) 派生一致性（前后端不能漂移）
go test ./pkg/apiduo/trace/ -run TestTraceIDStable
```

### 9.1 性能验证（第一原则的落地证据，不能省）

```go
// pkg/apiduo/trace/bench_test.go
func BenchmarkRecordDisabled(b *testing.B) {
	enabled.Store(false)
	ctx, info, biz, other := benchFixtures()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		Record(ctx, info, biz, other)
	}
}
func BenchmarkRecordEnabled(b *testing.B) {
	enabled.Store(true)
	// drain goroutine 必须在跑，否则 queue 很快填满、测出来的是丢弃路径
	go drainQueue()
	...
}
```

期望结果（不达标就说明实现跑偏了）：

| 场景 | 目标 | 含义 |
|---|---|---|
| Disabled | **0 allocs/op、< 2 ns/op** | 关闭态确实只是一次 atomic load |
| Enabled（元数据） | ≤ 1 alloc/op、< 150 ns/op | 只有一次逃逸 |
| Enabled（headtail 采集 1000 帧） | ≤ 3 allocs/op、< 5 μs | buffer 预分配后 append 零再分配 |
| 队列满 | 0 alloc、纳秒级 | 丢弃路径不能有额外分配 |
| finalize（body 48KB memcpy） | 1 alloc、< 15 μs | 快照是一次拷贝 |
| **失败路径**（defer + 无 usage） | 与 Enabled 同量级 | panic recover 分支不额外分配 |

### 9.2 线上对照（比 benchmark 更有说服力）

项目已有 `/api/perf-metrics`，直接用同一套口径做前后对照，不用自建压测：

```bash
# 改动前，先抓 24h 基线并留档
curl -sS 'http://127.0.0.1:3000/api/perf-metrics?hours=24' > /tmp/perf-baseline.json

# 灰度：先 1% 跑 1h，再 10% 跑 24h，最后再决定要不要更高
# 每一步对比 avg_ttft_ms / avg_latency_ms / success_rate 三项
```

判据：三项指标的波动必须落在这段时间的正常抖动范围内（建议用改动前 7 天的日间波动区间做参照），
而不是跟单个数字比。CPU profile 里 `apiduoTrace` 的占比应 < 0.1%。

### 9.3 Langfuse 端吞吐（别只压自己，把后端也压一遍）

网关 10% 采样 @ 1667 QPS ≈ 167 traces/s × ~40 KB ≈ 6.7 MB/s 入库。
自建 Langfuse（web + worker 单副本）的 ingestion 瓶颈在 worker → ClickHouse/S3 批量写。
灰度前用合成 payload 直打 `/api/public/otel/v1/traces` 压 30 分钟：
目标 ≥ 500 traces/s 且 Langfuse UI 检索延迟 < 3s。不达标就降采样率或加 worker 副本，
**不要**在网关侧堆积（队列满丢弃是设计行为，不是故障）。

---

## 10. 明确不做

- 不做 langfuse 侧的内容回写（只单向推送）
- 不做 Langfuse iframe 内嵌（跨域 + 鉴权 + 性能三重问题，Langfuse UI 也不支持嵌入）
- 不做反向拉取（Langfuse API → 日志页渲染 span 树），成本和收益不成比例
- 不做失败请求 trace（v1）。若将来要补，唯一正确位置是 `controller/relay.go` 的 defer
  （`perfmetrics.RecordRelayResult` 就在那里，能拿到 `newAPIError`），不要塞进消费日志链路
- 不在主链路上做任何序列化、正则、加锁统计（见 §3.3）
- 不为"更精确的 input token"去读 body——那会直接违反第一原则
- 不给普通用户开链接（见 §6.1）

---

## 11. 已定与待拍板

**已随方案确定**（无需再议）：

- 采集范围：全内容（C 档），headtail 默认截断
- 采样率：常规 10%、失败 100%（§3.8；实际值灰度时按 Langfuse 吞吐压测调）
- 内容不落盘、队列满即丢、主链路零序列化（§3.4/§3.5 架构公理）

**仍待拍板**（不影响开工，P4 前定即可）：

1. **Langfuse 部署形态** —— 复用现有 ClickHouse + 新增 PG/Redis/MinIO（4–5 容器、2–4 GB 常驻），
   还是先用 Phoenix 单容器把链路跑通？与 apiduo 自身性能无关（独立进程），只看机器内存余量。
2. **`langfuse.user.id`** —— 明文 userId（可直接下钻）还是哈希（更保守）。内网自建前提下建议明文。

## 12. 与 2026-11-03《NewAPI LLM 可观测性解决方案》v2.1 的对照

那份方案（下称 v2.1）写于仓库重置前一天，基于旧仓 `jushenzhidao/apiduo`。
它的**两条设计原则完全正确**，应当继承；但**具体实现锚点和技术前提已被代码演进推翻**，
逐条核实如下（全部对照当前工作区代码）。

### 12.1 依然成立、必须继承的部分

| v2.1 主张 | 现状印证 |
|---|---|
| 观测不得影响主链路，旁路永不反压 | 正确。但 trace 的丢弃策略要比 log-writer 更激进（见 §3.3） |
| 挂接点选"统一入口 + 请求边界唯一"，不选局部 handler | 正确，且代码已是这个形态：`perfmetrics.RecordRelayResult` 的注释写着 "samples one finished relay exactly once, at the request boundary, regardless of how many channel attempts it took" |
| 可插拔 sink，便于退役 | 正确，`pkg/perf_metrics` 的 `Store` 接口就是范例 |
| 默认关闭 + 行为等价 | 已是项目规范 |
| 内容采集要两处挂接（body 入口 + 响应出口） | 正确。这也是"元数据模式"换不来的能力 |

### 12.2 已被当前代码推翻的部分

| v2.1 的说法 | 当前事实 | 影响 |
|---|---|---|
| 锚点 `relay/controller/relay.go` 的 `Relay()` | **该路径不存在**。现为 `controller/relay.go:72` 的 `Relay(c, relayFormat types.RelayFormat)`，多了一个参数 | 锚点必须重选；但"选 Relay 统一入口"的原则未变 |
| 挂接点 1 需自己 `io.ReadAll` + `io.NopCloser` 重建 body，且"不能用 TeeReader" | **上游已内建**：`common.BodyStorage`（内存/磁盘双模式、`NewReader` 独立游标、可并发回放）+ `common.GetRequestBody(c)` 带 `KeyRequestBody` 缓存 | 那套 buffer 池化、大 body 落盘、sync.Pool 的工程量**不用再写** |
| "TTFT 采不到，需在挂接点 2 自己扫描 SSE 首帧" | **已在采**：`pkg/perf_metrics` 从 `FirstResponseTime - StartTime` 直接得出，含 `HasTtft` 判定 | 省掉 SSE 帧解析这一整块风险最高的实现 |
| "NewAPI 自带 Prometheus `/metrics`" | `prometheus/client_golang` 在 `go.mod` 里是 **indirect**，`router/` 内**没有** `/metrics` 注册。指标走 `/api/perf-metrics/{,summary}` + 前端 `features/performance-metrics` 页面 | v2.1 的 P1「抓 /metrics + Grafana 大盘」不成立，要么自己加端点，要么改用现有 API |
| "Langfuse 复用 ClickHouse，**存储栈零新增**" | **错**。ClickHouse 可复用（独立 database），但 Langfuse v3/v4 强制要求 **PostgreSQL + Redis + S3/MinIO**，一个都省不掉 | 资源账要按 §7 重算，不能按"零新增"立项 |
| `// OBS:BEGIN/END` 标记 + CI diff 白名单 | 已被 `tools/markercheck` + `.apiduo/merge-budget.txt` 取代，判据也从"路径白名单"升级为"**有没有改上游既有文件的内容**" | 标记名、校验工具、预算登记方式全部要换 |

### 12.3 v2.1 没有覆盖的部分（这也是你最初的问题）

- **全篇没有"日志页面"三个字。** 它解决"数据怎么进 Langfuse"，不解决"从日志页怎么跳到那条 trace"。
  §4.2 只写了"两边用 trace_id / 请求 ID 关联"，没有说关联键怎么生成、放在哪、谁能看见。
- **没有权限设计。** Langfuse 无行级隔离，同一实例里全站 trace 都在。v2.1 没考虑
  "普通用户拿到链接就能看别人"这个问题。本方案 §6 的 `props.isAdmin` + `SetAdmin` 就是补这个洞。
- **没有具体协议细节。** OTLP endpoint、Basic 认证、`x-langfuse-ingestion-version: 4`
  这些落地必需的东西，本方案 §4 补齐。

### 12.4 v2.1 比本方案强的地方（要承认）

**内容采集。** 如果目标包含"在 Langfuse 里复盘每次对话的 prompt/completion"，那 v2.1 的两处挂接
（body 入口 + 响应出口 ring buffer）是唯一路径，本方案的元数据模式做不到。代价是：

- 要处理 SSE 流式 tee（`io.MultiWriter` + 有界 ring buffer，前 N KB + 尾 M KB）
- 要在 worker 侧做脱敏 / 截断 / 采样分级
- 引入 WAL 的运维复杂度（多实例隔离、滚动重启 drain、磁盘增长监控）
- 工作量约为元数据模式的 3–5 倍，且是唯一会碰到 relay 主链路字节流的地方

**所以真正的分叉不是"新旧方案二选一"，而是：你到底要不要内容。**

而在"第一原则是不影响性能"（§0.1）之下，这个问题被切成了三档：

| 档 | 内容范围 | 主链路成本（实测） | 结论 |
|---|---|---|---|
| A | 仅元数据 | 2.3 ns（关）/ 360 ns（开） | 可做 |
| B | + 请求体 + 非流式响应 | +0.4 μs | 被 C 覆盖，取消 |
| C | + 流式响应内容 | **+11.6 μs（全量）/ +0.6 μs（截断）** | **可做，推荐** |

v2.1 的结论是对的：内容可以采。**是我此前把 SSE tee 的成本估高了两到三个数量级**——
错在把「本来就要发生的 write」算成了新增成本。实测之后 C 档成为推荐方案。

| 目标 | 路线 |
|---|---|
| 只想从日志页点进 Langfuse 看延迟/成本/渠道归因 | 本方案 §3（元数据），保留 v2.1 的原则，丢掉它的重工程量 |
| 要复盘 prompt/completion | v2.1 的两处挂接必须做，本方案退化为它的"展示层" |
| 两者都要 | 分两期：先元数据打通链路和展示，再补内容采集 sink |

### 12.5 融合后的最终形态（2026-11-08 优化版）

```
middleware(RequestId 旁) ── 读 traceparent ─┐
                                             ▼
controller/relay.go ── Relay() ──► collector（挂 gin.Context，随请求生灭）
                                        │  stream_scanner 包 dataHandler：流式帧 append
                                        │  compatible_handler：非流式体 append
                                        │  text_quota 结算：usage 字段填充
                                        ▼
                      relay defer（controller/relay.go:140，perfmetrics 旁）
                                        │  finalize：一次快照（body memcpy + buffer 转移）
                                        │  采样判定 + 入队（元数据队列 / 内容队列分离）
                                        ▼
                      pkg/apiduo/trace worker：解析 / 脱敏 / 组装 OTLP → Langfuse
                                        ▲
                      model/log.go 兜底快照（任务结算 / MJ / 违规：无 relay defer 的路径）
```

与旧版的差别：上报点从"结算点 + 兜底点各自入队"统一为 **collector 状态机 + relay defer
唯一 finalize**；失败请求因此天然有 trace；body 生命周期由 defer 顺序保证（§3.4 ②）。

已废弃不再采用：`io.ReadAll` 重建 body、SSE 首帧扫描采 TTFT、自建 WAL、Prometheus 抓取、
`relay/controller/` 路径、`// OBS:` 标记前缀。

---

## 13. 改造清单（供决定是否开工）

### 13.1 新增文件（不动上游）

| 文件 | 行数（估） | 职责 |
|---|---|---|
| `pkg/apiduo/trace/config.go` | ~70 | env 读取、`enabled` / `bodyMode` 原子量 |
| `pkg/apiduo/trace/id.go` | ~35 | `TraceID(reqId)` / `SpanID` / `parseTraceparent` |
| `pkg/apiduo/trace/snapshot.go` | ~55 | 入队结构体，固定字段无 map |
| `pkg/apiduo/trace/collect.go` | ~110 | 请求体引用、SSE 累积、headtail 截断 |
| `pkg/apiduo/trace/payload.go` | ~180 | OTLP/HTTP JSON，含 input/output |
| `pkg/apiduo/trace/exporter.go` | ~200 | 有界队列、批量 flush、丢弃计数、优雅停机 |
| `pkg/apiduo/trace/redact.go` | ~90 | worker 侧脱敏，规则热加载 |
| `pkg/apiduo/trace/parse_sse.go` | ~140 | 按 relayFormat 把帧还原成 text / tool_calls |
| `pkg/apiduo/trace/trace.go` | ~90 | 对外 API：`Record` / `WrapStreamHandler` / `RecordLog` |
| `pkg/apiduo/trace/*_test.go` | ~320 | payload、id、截断、脱敏、三格式解析、队列丢弃 |
| `controller/apiduo_trace.go` | ~35 | `GET /api/apiduo/trace/config`（AdminAuth） |
| `web/src/features/usage-logs/components/trace-link.tsx` | ~50 | 链接组件，`data-apiduo="text"` |
| `web/src/features/usage-logs/components/__tests__/trace-link.test.tsx` | ~40 | admin 门控、无配置时隐藏 |

**合计约 1,300 行**（含测试），全部新文件，不参与上游 merge。

### 13.2 修改上游文件（全部在标记块内，每处 1–2 行）

| 文件 | 改动 | 说明 |
|---|---|---|
| `controller/relay.go`（两处，同文件） | +2 行 | `Relay()` 开头读 `traceparent`；defer 内 finalize——后端挂接文件 7→6 |
| `relay/helper/stream_scanner.go` | +1 行 | 包 `dataHandler`，流式帧 append 进 collector |
| `relay/compatible_handler.go` | +1 行 | 非流式体 append 进 collector |
| `service/text_quota.go` | +1 行 | usage 字段填充（不再是上报点） |
| `controller/relay.go`（两处，同文件） | +2 行 | `Relay()` 开头读 `traceparent` + defer 内 finalize（**唯一上报点**，成功/失败/panic 全覆盖） |
| `model/log.go` | +1 行 | 兜底快照（任务结算 / MJ / 违规，无 relay defer 的路径） |
| `router/api-router.go` | +1 行 | 配置路由（AdminAuth） |
| `web/.../dialogs/details-dialog.tsx` | +3 行 | 条件渲染链接 |
| `web/src/features/usage-logs/api.ts` | +6 行 | 取配置 |
| `web/src/i18n/locales/*.json` | 7 × +2 键 | |

上游净增约 **20 行**。预算登记约 `files +13 / blocks +8 / added +1300`（以 `markercheck -cost` 实测为准）。

### 13.3 不做的事

- 不包装 `ResponseWriter`（不需要 tee，`dataHandler` 已给明文）
- 不给 trace 加磁盘缓冲 / spool（§3.4 ③，内容不落盘）
- 不复用 `pkg/perf_metrics` 的 Store 接口（它是纯内存聚合，语义不同；只对齐代码形态）
- 不在主链路 `json.Unmarshal` / 正则脱敏 / 序列化
- 不改 `pkg/perf_metrics` 的数据结构
- 不给 `common.BodyStorage` 加 refcount
- 不动 ClickHouse `logs` 表结构

### 13.4 回滚

| 层级 | 动作 | 耗时 |
|---|---|---|
| L1 | `LANGFUSE_ENABLED=false` + 重启 | 秒级 |
| L2 | `LANGFUSE_BODY_MODE=off`，退回纯元数据 | 秒级 |
| L3 | 删掉 8 个标记块里的调用（后端 7 + 前端 1） | 分钟级 |
| L4 | 删 `pkg/apiduo/trace/` 与路由，回到纯上游 | 分钟级 |

L4 之后 diff 为零。

### 13.5 工期

| 阶段 | 内容 | 工作量 |
|---|---|---|
| P1 | collector + 7 挂接点 + 元数据链路 + 配置接口 + 单测（含失败路径） | 2 天 |
| P2 | 内容采集（三采集点）+ headtail 截断 + 三格式解析 + 脱敏 | 2 天 |
| P3 | traceparent + agent 树 + 前端链接 + e2e | 1 天 |
| P4 | benchmark + 最坏内存压测 + Langfuse 吞吐压测 + 灰度 1% → 10% → 100% | 1 天 |

Langfuse 部署另算 0.5–1 天。


---

## 14. 合并负担评估（上游同步成本，2026-11-08 实测）

> 判据沿用解耦规范 v2：**侵入度 = 改了多少上游既有文件的内容**。新增文件零合并负担
> （上游不可能碰自己没有的路径），真正的合并面只有标记块里的插入行。

### 14.1 侵入面总账

| 类别 | 规模 | 合并负担 |
|---|---|---|
| 新增 `pkg/apiduo/trace/` 等 13 个文件 | ~1,300 行 | **零**（上游无此路径，永不冲突） |
| 后端标记块 | 6 个文件、7 个块、~16 行 | 低（见 14.2 实测频率） |
| 前端标记块 | 2 个文件、~9 行 | 低 |
| i18n 7 个 locale | 各 +2 键 | **中**（频率最高，但解法机械） |
| markercheck 预算 | blocks 226→~234（+3.5%）；added（仅上游文件插入行）+~25（+1%） | — |

### 14.2 挂接点文件的实测改动频率（upstream/main 最近 500 次提交）

| 文件 | 命中次数 | 冲突风险 | 说明 |
|---|---|---|---|
| `i18n/locales/zh.json` / `en.json` | **90 / 89** | **最高** | 每 5–6 次上游提交改一次。缓解：键追加文件尾部，冲突时两边保留，机械解 |
| `router/api-router.go` | 23 | 低 | 路由组尾部加 1 行，三方合并基本自动过 |
| `controller/relay.go` | 22 | 低 | 上游改的是函数中段逻辑；插行点在 `Relay()` 开头与 defer，是稳定区 |
| `details-dialog.tsx` | 16 | 低 | request_id 行下方插入，上下文稳定 |
| `service/text_quota.go` | 11 | 低 | 结算函数尾部 1 行 |
| `model/log.go` | 10 | 低 | `RecordConsumeLog` 内 1 行 |
| `relay/helper/stream_scanner.go` | 6 | 低 | **函数签名区全历史仅改 5 次**，非常稳定 |
| `relay/compatible_handler.go` | 6 | 低 | |
| `usage-logs/api.ts` | 2 | 极低 | |

反直觉结论：**最大的合并摩擦源是 i18n，不是后端挂接点。** 后端 6 个文件里频率最高的
`controller/relay.go` 也只有 4.4%（22/500），且我们插行的位置（函数头/defer）恰是该文件
最稳定的区域——上游的 22 次改动集中在请求处理中段。

### 14.3 最坏情况剧本

上游重构 `StreamScannerHandler` 签名（如 `dataHandler` 加参数）：

1. rebase 时标记块附近冲突 → **这是设计意图，不是事故**：强制人工 review 挂接点
2. 解法：标记块整体平移到新签名下方，包装行适配新参数，通常 5 分钟内完成
3. `go run ./tools/markercheck` 闸门兜底：任何越出标记块的改动 exit 1，
   不存在"冲突悄悄解错"的可能

对比参照：本仓现有 226 个标记块、两轮上游同步的实践，最坏情况就是标记块内冲突
（手动挪一行），从未出现挂接函数被删的情况。

### 14.4 如果上游将来原生支持 OTLP

退出路径（v2.1 §7.6 的思想仍然成立）：删 `pkg/apiduo/trace/` 与 8 个标记块 = diff 归零。
采集点都在我们自己的 collector 里，不污染任何上游结构，上游若出自己的方案可直接切换。

---

## 15. 落地状态（2026-11-08 P1+P2 后端已实现，未提交）

### 15.1 已落地

**新增 `pkg/apiduo/trace/`（7 文件）**：config / id / snapshot / collector / payload / exporter / trace。
- 关闭态实测 **2.2 ns / 0 alloc**（Begin），FillUsage/Finalize/Wrap ~14 ns / 0 alloc
- collector 挂 gin.Context；tailRing 环形尾窗 O(n)；headtail 截断；binaryPlaceholder 保护非文本体
- 双队列（meta 2048 / content 512）分离；worker 按 tick/批量/深度三种触发 flush；满即丢并计数
- OTLP/HTTP JSON 手写 payload，langfuse.* + gen_ai.* 双属性族

**桥接新文件 4 个**（上游标记块因此每处只有 1 行）：
- `controller/apiduo_trace_relay.go`（RelayInfo→RelayMeta 翻译 + nil info 分支）
- `service/apiduo_trace_usage.go`（textQuotaSummary→Usage）
- `model/apiduo_trace_bridge.go`（RecordConsumeLogParams→兜底快照 + HasCollector 去重）
- `controller/apiduo_trace.go`（GET /api/apiduo/trace/config，AdminAuth + log:read scope）

**上游标记块 11 个**：relay.go×3（import/Begin/defer finalize）、stream_scanner×2、http.go×2、
text_quota×1、log.go×1、api-router×1、access_token_routes×1。
预算 95 files / 254 blocks / 2395 added / 91 deleted 已登记，markercheck GATE_PASS。

### 15.2 测试与验证

- 单测 + 开启态 e2e（httptest 假 Langfuse sink）全绿；`go test -race` 干净
- traceId 派生字节级钉死；traceparent 采样位/parentSpanId 语义有测试
- 队列满丢弃不阻塞（worker 卡死在 HTTP 时 50 次入队 <1s）
- 受影响包回归：relay/helper / model / service / controller 全过
- router 包有**预存在**失败（discounts 路由未登记 access-token 规则，0.0.13 欠账，与本改动无关）

### 15.3 落地过程中修掉的实现 bug（写代码才暴露的）

1. **worker 小批量永不 flush**：ticker 分支原只置标志不触发，单条 span 会无限滞留。
   改为 ticked 标志参与 flush 判定。
2. **`info.ChannelId` 是嵌入指针提升字段**：`RelayInfo` 内嵌 `*ChannelMeta`，渠道选择前
   `cm` 为 nil，直接读 `info.ChannelId` = 解引用 nil（controller 测试抓到）。改用
   `info.GetChannelID()`。**以后碰 RelayInfo 的 ChannelType/UpstreamModelName 等提升字段都要走
   nil-safe getter。**
3. **flushInterval 测试竞态**：包级 var 被泄漏 worker 并发读；挪进 Config（FlushIntervalMS）。
4. 新 AdminAuth 路由必须同步登记 `middleware/access_token_routes.go` 的 scope 规则表，
   否则 router 的覆盖测试红。

### 15.4 未落地（下一期）

- P3 前端：日志详情链接组件 + `GET /api/apiduo/trace/config` 消费 + i18n
- worker 侧 SSE 帧解析（openai/claude/gemini delta 还原）与脱敏正则
- 优雅停机 drain（main.go 钩子，v1 接受停机丢弃）
- Langfuse 真实实例验证 payload 映射（P0 curl 冒烟）

---

## 16. 第二轮落地（P3 前端 + 内容管线 + 充分性测试）

### 16.1 本轮新增

**前端（P3）**
- `web/src/features/usage-logs/lib/trace-link.ts`：`buildTraceUrl` 纯函数（**拓展点**：
  换 Phoenix/Tempo 只改这里；路由模板单点定义）
- `components/trace-link.tsx`：`ApiduoTraceLink`，`data-apiduo="text"`，颜色用 `--apidup-ink`
  （`--primary` 对比度不足，红线）
- `api.ts` `getTraceConfig()`、`types.ts` `admin_info.trace_id`、详情弹窗挂载、i18n ×7
- **权限模型**：后端 `formatLogOtherJSON(user)` 已剥 `admin_info` ⇒ 普通用户拿不到 trace_id，
  前端自然不渲染；`isAdmin` 是第二道锁，不是第一道

**内容管线**
- `parser.go` 注册表 + `parser_openai/claude/gemini.go`：帧 → `{text, tool_calls, finish_reason}`
  （**拓展点**：新上游格式 = 1 个文件 + `RegisterParser`，不动别处）
- `redact.go`：API key / Bearer / 身份证 / 手机 / 银行卡 / 邮箱，默认开
  （`LANGFUSE_REDACT=false` 可关）；规则表即配置
- `formatOutput`：先解析再脱敏；认不出的格式保留原始帧（文档承诺的 fallback）
- traceparent **上游透传**：`trace.Propagate` + `SetupApiRequestHeader` 挂接（enabled 才透传，
  默认态出站请求与上游逐字节一致）

### 16.2 对抗审查抓到的 3 个真缺陷（写测试才暴露）

1. **截断劈帧**：headtail 原来按字节切，SSE 帧被拦腰斩断，解析器拿到半截 JSON 直接失明。
   改为**帧级窗口**：head 整帧进入，放不下就 seal，后续整帧进 tail；tail 按整帧淘汰，
   单帧超预算保完整帧（不切 JSON）。
2. **head 乱序**：首帧超 head 预算时 head 空着，后续小帧却进 head ⇒ 输出顺序颠倒。
   用 `headSealed` 锁：第一次放不下即封口，之后全走 tail。
3. **trace_id 双写覆盖**：同步请求同时走 text_quota（写 collector id，含 traceparent）和
   model 兜底（写派生 id），**后者覆盖前者** ⇒ 带 traceparent 的请求日志链接指向不存在的 trace。
   新增 `TraceIDForLog`（collector 优先），兜底路径只在无 collector 时派生。
4. **空 request_id 产出垃圾 span**：`TraceID("")=""` ⇒ traceId 为空的 span 无法关联任何日志。
   `Begin` 现在直接跳过（生产必有 request_id，middleware.RequestId 保证）。

### 16.3 测试矩阵（本轮）

| 层 | 内容 |
|---|---|
| 单元 | parser ×3（含 tool_call 分片合并、未知格式拒绝）、redact（key/手机/身份证/邮箱/银行 + 不误伤正文）、帧级截断、headSealed、
tail 淘汰、帧尾窗口保整帧 |
| 集成 | 开启态 e2e（假 sink 收 OTLP）、**日志 trace_id == span traceId**（含/不含 traceparent）、兜底路径、
队列满不阻塞、采样率、批量单请求 |
| 防御 | Propagate 关闭态零改动、空 request_id 不建 collector、Redact=false 透传 |
| 前端 | buildTraceUrl 全分支、组件 admin 门控、无 id 隐藏、配置失败 fail closed、disabled 隐藏、`rel="noopener noreferrer"` |
| 闸门 | `go test -race` 干净、`go build ./...`、`bun run typecheck`(1518 文件)、`bun run build`(67MB)、markercheck |

### 16.4 可拓展点（写给别人看的）

| 想改什么 | 改哪里 | 不用碰 |
|---|---|---|
| 换 OTLP 后端（Phoenix/Tempo） | `LANGFUSE_HOST` + `buildTraceUrl` 模板 | 采集、队列、parser |
| 支持新上游流格式 | 新 `parser_xxx.go` + `RegisterParser` | exporter、payload、config |
| 加脱敏规则 | `redact.go` 的 `redactRules` | 其余全部 |
| 换队列/批量策略 | `exporter.go` 常量 + `Config.FlushIntervalMS` | 采集点 |
| 加新挂接点（如某渠道特例） | 新桥接文件 + 1 行标记块 | 已有挂接点 |

### 16.5 未落地

- 优雅停机 drain（main.go 钩子；v1 停机即丢，文档已声明）
- 真机 Langfuse 冒烟（P0 curl 验证属性映射，`x-langfuse-ingestion-version: 4`）
- 前端全量 vitest 未跑通：工作区有**并行会话的在途改动**（`route-hub*`，16:11 后新增 4 文件），
  其半成品测试会让全量跑挂起；本轮只跑 `usage-logs` 范围。markercheck 的 1 处残留违规
  （`advanced-custom-editor-dialog.tsx` 的 3 行 route-hub import）属该会话，不属本功能。

---

## 17. 本地端到端实测（2026-11-08 18:4x–19:0x）

真进程、真 HTTP、真浏览器，零外部依赖：假上游 + 假 Langfuse OTLP sink。

### 17.1 测试环境

| 组件 | 端口 | 作用 |
|---|---|---|
| 网关（真二进制） | 39110 | SQLite 空库 + `/api/setup` 建 root |
| 假上游 | 39101 | OpenAI 兼容：SSE 5 帧含 tool_calls 分片、非流式、按关键字回 500；记录收到的 `traceparent` |
| 假 Langfuse | 39102 | 收 `/api/public/otel/v1/traces`，落盘 body 与请求头 |
| 浏览器 | — | 真 Chrome（puppeteer-core，`--no-proxy-server`） |

驱动脚本当前放在 `/tmp/trace-e2e/`（`fakes/`、`drive.py`、`privacy.py`、`off.py`、`fe.mjs`）；
要长期保留就挪进 `e2e/`，脚本里的绝对路径一并改成相对项目根。

关键 env：`LANGFUSE_ENABLED=true`、`SAMPLE_RATE=100`、`ERROR_SAMPLE_RATE=100`、
`FLUSH_INTERVAL_MS=300`、`BODY_MODE=headtail`、两个限流 `*_ENABLE=false`。

### 17.2 结果

| 套件 | 断言 | 结果 |
|---|---|---|
| 后端 E2E（`drive.py`） | 36 | **36/36** |
| 隐私（`privacy.py`） | 10 | **10/10** |
| 关闭态等价（`off.py`） | 7 | **7/7** |
| 真实浏览器（`fe.mjs`） | 5 | **5/5** |

覆盖的事实（不再只是单测）：

- 非流式 / 流式 / 带 `traceparent` / 上游 500 四条请求 → **四条 span**，一对一
- 流式输出被还原成 `{"text","tool_calls","finish_reason"}`，tool_call 的
  `name` 与分片 `arguments` 拼回 `{"city":"北京"}`，不再是原始 SSE 信封
- 注入的 `sk-live-…` 在导出内容里变成 `[REDACTED:api_key]`
- 客户端 `traceparent` 的 trace-id **被采纳**，`parentSpanId` 等于客户端 span-id
- 日志 `admin_info.trace_id` 与 span 的 `traceId` **逐一相等**（驱动从 span 侧反查，避免历史行干扰）
- 非管理员 `/api/log/self` 的 `other` 里**没有 `admin_info`**，也没有顶层 `trace_id`；
  `/api/apiduo/trace/config` 对非管理员 403（不泄露内网 host）
- 关闭态：0 导出、0 trace_id、**上游收到的 `traceparent` 为空**（出站与上游逐字节一致）
- 浏览器：管理员登录 → 日志页 → 详情弹窗出现「在 Langfuse 中查看 Trace」，
  href = `{host}/project/{project_id}/traces/{32 hex}`，`rel="noopener noreferrer"`、`target=_blank`

### 17.3 本地 E2E 抓到的 4 个缺陷（单测与 code review 都没抓到）

1. **`Enabled()` 永远为假（最严重）**：热路径是一个裸 `atomic.Bool`，而它只在
   `current()` 的 `sync.Once` 里被赋值。`Begin` 先查 `Enabled()` 再读配置 ⇒ 新进程里
   谁都没触发过 `current()`，开关永远是关的；**访问一次 `/api/apiduo/trace/config`
   反而会让它从下个请求开始生效**（因为那次调用顺路读了配置）。修：`init()` 里
   `armFromEnv()`，进程启动即武装（`TestArmFromEnvOpensGate` 钉住）。
2. **失败请求丢 input**：`HasContent` 被定义成 `hasContent || usageSet`，而失败请求既没有
   帧也没有结算 ⇒ 恒假 ⇒ payload 里 input/output 一起被跳过。**失败恰恰是最想看
   「我们发了什么」的场景**。修：队列路由按「真的有没有字节」判定，input/output 各自
   按长度写（`TestFailedRequestKeepsInputAndLevel`、`TestHasContentFollowsBytes`）。
3. **失败 span 在 Langfuse 里看着像成功**：只设了 OTLP `status.code=2`，没有
   `langfuse.observation.level` 与 `status_message`。修：失败时补 `level=ERROR` +
   脱敏后的原因（`MaskSensitiveErrorWithStatusCode`，按 400 字符截断且不劈 UTF-8），
   OTLP `status.message` 同步带上。
4. **并行提交吞掉共享注册表里的标记块**：另一会话的提交重写了
   `middleware/access_token_routes.go`，把我加的 `GET /api/apiduo/trace/config` 规则删了，
   路由覆盖测试立刻红。→ 每次 rebase/合并后必须 `grep APIDUO:trace:BEGIN` 数块，
   数量应为 **18**（后端 13 + 前端 5）。

### 17.4 本机环境坑（下次复现直接用）

- **端口冲突是跨协议族的**：Go 的 `:39100` 绑在 `*:39100`（IPv6 双栈），另一个进程绑
  `127.0.0.1:39100`（IPv4）时**两个都"成功"**，curl 走 IPv4 命中别人（返回 uvicorn 的
  `{"detail":"Not Found"}`）。`lsof -nP -iTCP:PORT` 会同时列出两行。
- 磁盘 >95% 会全站 503 `system_disk_overloaded`：`PUT /api/option/`
  `performance_setting.monitor_disk_threshold=99` 临时放宽。
- `/api/token/{id}/key` 返回的是**不带 `sk-` 前缀**的裸 key，客户端要自己加。
- 新建用户的 `quota` 字段不生效，要 `POST /api/user/manage` `{"action":"add_quota"}`。
- 断言必须按「本次请求的 request_id」收敛，否则库里的历史行会制造假阳性。

---

## 18. 真实 Langfuse 实测（ai-prod，2026-11-08 19:30–20:20）

假 sink 只能证明「我们发了自认为正确的 JSON」。真实后端才能回答「Langfuse 认不认这些属性」——
认不出的属性会被**静默丢弃**，而 span 本身看起来毫无问题。

### 18.1 环境

| 组件 | 位置 | 说明 |
|---|---|---|
| Langfuse | `langfuse.1task.cn`（`127.0.0.1:39196`） | 自建 v3.225.15，project `apiduo`，此前零流量 |
| 测试网关 | ai-prod 容器 `trace-e2e-gw` | `127.0.0.1:39197`，挂在 `apiduo-langfuse_default` 网络 |
| 假上游 | ai-prod 容器 `trace-e2e-fakes` | `http://trace-e2e-fakes:39101`（容器内解析） |
| 数据 | `/www/data/apiduo/trace-e2e/` | 二进制 + SQLite，`bin/apiduo` 换文件 + restart 即升级 |

网关用 **静态编译产物**跑在 `alpine` 里（`CGO_ENABLED=0`，sqlite 驱动是纯 Go），
`LANGFUSE_HOST=http://apiduo-langfuse-web:3000` 走容器网络直连，不经过公网与 nginx。

### 18.2 结果

| 套件 | 断言 | 结果 |
|---|---|---|
| 远程驱动（`remote.py`，跑在服务器上） | 16 | 16/16 |
| 真实 Langfuse API 反查（`verify_langfuse.py`） | 36 | **36/36** |
| 真实 UI（headless Chrome 打开 trace 页） | 6 | 5/6（见 18.4） |

真实后端确认的事实：

- `admin_info.trace_id` == Langfuse `trace.id`，**日志里的跳转链接点开就是那条 trace**
- `langfuse.observation.model.name` / `gen_ai.usage.*` 都映射成功（21/13/34、27/19/46）
- 流式输出还原成 `{text, tool_calls, finish_reason}`，tool_call 分片拼回完整 JSON
- 非流式回答同样出现在 Output（表现为 `finish_reason + text`）
- 密钥脱敏在 UI 里可见：`[REDACTED:api_key]`
- 失败请求：`level=ERROR`（树里红点）+ `statusMessage="status_code=500, upstream exploded on purpose"`
  + **`?level=ERROR` 可查询**（41 条 error observation，是过滤器不是靠人眼扫）
- 客户端 `traceparent`：span 的 `parentObservationId` == 客户端 span-id；
  同一 trace 下累积了 **4 条 generation**（多次网关调用一棵树），UI 树里能看到 7 个 observation

### 18.3 这轮抓到的 3 个缺陷（前两个假 sink 抓不到）

1. **非流式响应完全丢失**。`outputBytes()` 在 headtail 模式只读 SSE 帧窗口，
   而非流式响应写在 `respBody` —— 帧窗口为空时直接返回 nil，整个补全被丢掉。
   本地假 sink 那一轮其实已经有迹象（那条 span 只有 input 没有 output），但我当时只断言了流式。
   ⇒ 修：空帧窗口回落 `respBody`；`TestNonStreamResponseBodyIsCaptured` + 驱动补非流式断言。
2. **完成态被当成空流解析**。解析器只读 `choices[].delta`，完成响应是 `choices[].message`，
   于是输出成了 `{"finish_reason":"stop","text":""}` —— **比不解析更糟**（真实答案在原始体里被丢弃）。
   ⇒ 修：解析器同时支持 `delta` 与 `message`；`parseOutput` 增加认领门槛：
   **解析不出文本也没有 tool_calls 就回退原始体**（只拿到 finish_reason 不算解析成功）。
3. **自定义属性不可筛选**。Langfuse 只把 `langfuse.trace.metadata.*` / `langfuse.observation.metadata.*`
   放进顶层 metadata；裸 `apiduo.*` 全部沉到 `metadata.attributes`，**UI 里无法过滤**。
   「哪个渠道慢」正是核心用法，等于白采。
   ⇒ 修：五个运维要筛的维度另发一份 `langfuse.trace.metadata.{request_id,channel_id,group,token_name,format}`，
   `apiduo.*` 保留给原始报文与直读 OTLP 的场景。

### 18.4 已知取舍（真实 UI 观察）

- **`statusMessage` 在 v3.225 的 Preview 面板不渲染**（DOM 里没有该文本），API 里有、v4 UI 有。
  失败可见性靠 `ERROR` 级别（红点）+ `?level=ERROR` 过滤，够用；要看到文案需升 v4。
- **带 traceparent 时 trace 级 input/output 为空**：Langfuse 的 trace 级字段取自 **root observation**，
  而我们的 span 是客户端 span 的子节点。客户端自己的 root span 到达后自然补上；
  在那之前列表页这两列为空，点进 observation 才有内容。
  想立刻有列表预览可以加 `langfuse.trace.input/output`（已废弃，v4 移除），代价是内容翻倍入库——
  **当前不做**，等真实需求。
- Langfuse worker 日志里有 `otel-ingestion-queue` 等 socket timeout（环境侧 Redis/CH 压力），
  但 trace 全部落库；网关侧 4 条请求零重试、零 5xx。

### 18.5 复现

```bash
# 上传（本机 → 服务器）
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o apiduo-linux .
gzip -9 -c apiduo-linux > apiduo-linux.gz && scp apiduo-linux.gz ai-prod:/www/data/apiduo/trace-e2e/
# 服务器上
gunzip -c apiduo-linux.gz > bin/apiduo && docker restart trace-e2e-gw
cd /www/data/apiduo/trace-e2e && E2E_UPSTREAM=http://trace-e2e-fakes:39101 python3 remote.py
LANGFUSE_HOST=http://127.0.0.1:39196 python3 verify_langfuse.py <request_id:trace_id>...
```

⚠️ 上传 41 MB 走家用上行约 10 分钟；容器内二进制建议用**目录挂载**（`bin/`）而不是单文件挂载，
换版本只需替换文件 + `docker restart`。Linux 下假服务的监听地址必须显式 `0.0.0.0`，
绑 `127.0.0.1` 时端口发布与容器间访问都不通。

---

## 19. 后台可观测性配置页（2026-11-10）

采样从「环境变量」升级为「后台策略」：不再需要改 env + 重启，保存即生效。

### 19.1 采样模型

| 维度 | 作用 | 默认 |
|---|---|---|
| 元数据采样 | 该请求**是否产生 trace**（也是日志页跳转链接是否存在的前提） | 跟随 `LANGFUSE_SAMPLE_RATE`，未设为 100% |
| 内容采样 | 已产生的 trace **是否携带提示词与回复**（只影响内容，不影响链接） | 跟随 `LANGFUSE_CONTENT_SAMPLE_RATE`，未设为 100% |
| 规则 | 按模型 / 星期 / 时间段细化上面两项，或整条排除 | — |
| 父采样 | 有 `traceparent` 时遵循客户端 sampled 位 | 开 |

两条独立的判定用 **trace id 的哈希**（不同 salt），而不是计数器：同一 trace 永远同一结论、
多副本无需协调、重启不漂移。代价是改采样率会让一部分既有 trace 的判定翻转——可接受。

**规则语义**（写进页面说明，避免误配）：

- 一条规则内条件取交集；列表**自上而下**，第一条命中生效。
- 只写模型 = 该模型专用速率；只写时间窗 = 该时段专用速率。
- **窗口外回落默认值**，所以「只在 09:00–18:00 采集」= 默认 0 + 窗口 100。
- 起止时间 `start > end` 表示跨午夜（22:00–06:00）。
- 排除规则的优先级靠**位置**：想永久排除某模型，把它放在最上面。
- 模型名大小写不敏感；空模型表示不限制。
- 时区可配 IANA 名；容器默认 UTC 是个坑，页面上直接显示「规则按 X 时区计算，当前 HH:MM」。

### 19.2 存储与生效

选项系统按 `模块.字段` 逐项存储（`observability_setting.rules` 等），保存走现有
`PUT /api/option/`，`model.updateOptionMap` 命中 `observability_setting` 后调用
`UpdateAndSync()` → `trace.ApplySettings()` 原子换掉运行时策略。**没有重启。**

连接信息（host / 密钥 / 总开关）仍留在环境变量：一条写坏的设置行不应该能把导出指向别处。

### 19.3 查看权限

| 角色 | 能否看到 trace 入口 |
|---|---|
| root | 始终可以 |
| 其他管理员 | 仅当 id 出现在 `trace_viewer_user_ids` |
| 普通用户 | 不可以（其日志响应本来就不含 `admin_info`） |

三处同时生效：配置接口 403、日志响应里 `admin_info.trace_id` 被剥离、前端拿不到 id 不渲染。
只改前端不算限制——字段还在 payload 里。

### 19.4 对抗审查抓到的 4 个缺陷（全部已修 + 回归测试）

1. **非流式响应提前决策**：`CaptureResponseBody` 在响应写出时就调用了 `decide()`，那时模型还
   没绑定（`BindModel` 在结算点），判定被冻结在默认速率上——**所有按模型规则静默失效**。
   修：那里只缓冲，不决策；`decide` 由 `BindModel`/结算/收尾触发。
2. **`HasWindow` 零值陷阱**：不写时间窗的规则，`Start/End` 是 `0/0`，被当成
   「00:00–00:00 的窗口」→ 永远不命中。修：显式 `HasWindow`，零值表示「不限时间」。
3. **布尔标记省略 false**：`attrBool` 只写 true，导致「内容被裁掉」和「旧版本没有这个字段」
   在 Langfuse 里长得一样。修：`content_sampled` 恒写。
4. **整块 JSON 存设置**：选项系统按字段存储，把整块 JSON 存到 `observability_setting`
   只会进 OptionMap，永远到不了注册的配置结构——页面显示保存成功、实际没生效。
   修：前端逐字段 PUT。

### 19.5 已验证（本地真进程 + 假 Langfuse）

`settings.py` 19 条断言全过：默认 100/100、内容 0 仍出 span 且无 input、
按模型排除后零导出、模型规则恢复且 content 生效、时间窗内导出/窗外不导出、
非 root 管理员 403 且日志无 trace_id、加入白名单后 200 且有 trace_id。

### 19.6 仍然没做

- 采样决策不回写日志：命中率变化后，历史日志里的链接仍指向当时确实导出的 trace（正确）。
- 没有「tail sampling」（按错误/延迟事后决定）：需要网关侧缓冲整条 trace，代价与收益另算。
- 规则没有 UI 拖拽排序：顺序即优先级，目前靠新增顺序，删除后重排需要手工调整。

## 20. 面板归位与「开箱可用的默认」（2026-11-10）

### 20.1 为什么搬到「扩展」

后台设置侧边栏由上游分组 + 一个 apiduo 自己的 `Extensions` 分组组成；该分组只收
`marks.ts` 里登记过的 section。配置页原先挂在 `operations` 下，导航里和「日志维护」
「性能」混在一起——运营者找的是「apiduo 加的那个东西」，不是某个站点设置，所以按
现有约定迁到 `site/observability`，与自定义入口 / 外观特效 / 对象存储 / 登录页风格同组，
并补了 `Activity` 图标（子项图标本身就是 apiduo 才有的结构）。

旧路径 `/system-settings/operations/observability` 未发版，不做重定向。

### 20.2 默认值必须等于「实际生效值」

原来的 `observability_setting` 在 `init()` 里把结构体默认的 100/100 推给 trace 运行时，
而 trace 的 env 默认来自 `LANGFUSE_SAMPLE_RATE`。两者一冲突，后果是：**升级即事故**——
一个按 10% 采样的生产实例，装上这个版本后会在没有任何人操作的情况下变成 100%，
Langfuse 摄入量直接 ×10。

修法（`setting/observability_setting/config.go`）：

- 结构体初始值改为 `trace.EnvDefaultRates()`，即「没配过就显示 env 里的真值」；
- `init()` 里**不再**调用 `UpdateAndSync()`。数据库没有该模块的行时，运行时保持 env 值；
  一旦有人保存（`handleConfigUpdate` 命中），才由设置行接管。

`EnvDefaultRates()` 同时被 `loadFromEnv()` 复用，避免「表单显示值」与「导出器实际值」
两处各算一遍再漂移。

### 20.3 页面上补的三件事

| 位置 | 内容 | 解决什么 |
|---|---|---|
| 顶部状态条 | 采集开关 / 上报地址 / 正文抓取模式 / 当前生效速率 / 规则时钟 | 之前 `LANGFUSE_ENABLED=false` 时页面照样能编辑，调半天不知为何没数据 |
| 快速预设 | 全量采集 100/100、全量链接+10% 内容、仅元数据 100/0、暂停 0/0 | 4 个真实答案，比让人手填两个百分数再猜含义快 |
| 危险值提示 | 0% 无链接、内容 0% = 合规档 | 采样率是能静默改变「日志页有没有链接」的开关 |

时区一行增加了 `服务器本地 / Asia/Shanghai / UTC` 快捷键；`ServerZone()` 的返回值补上
UTC 偏移（`Asia/Shanghai (UTC+08:00)`），因为未设 `TZ` 时 `time.Local.String()` 只是
`Local`，看不出规则是按 UTC 还是东八区读的。

### 20.4 验证（真进程 + 真浏览器，72 条断言）

套件：`e2e/live/ui/observability_settings.mjs`（手动运行，依赖见文件头与 README）。
环境：空库 + `LANGFUSE_SAMPLE_RATE=10` + `TZ=Asia/Shanghai` 起网关 A（trace 开）与
网关 B（`LANGFUSE_ENABLED=false`）+ 假上游 + 假 Langfuse；账号 root / mgr(role=10) / alice(role=1)。

| 组 | 断言 | 关键事实 |
|---|---|---|
| A 导航 | 7 | 「扩展」可展开且含该页、点击进入、深链与刷新、`data-apiduo=self` 标题标记、运维分组不含它 |
| B 状态条 | 6 | 已启用 / 上报地址 / 首尾截取 / `10% 有链接 · 100% 含内容` / `Asia/Shanghai (UTC+08:00)` / 输入框预填 **10**（默认值来自 env） |
| C 关闭态 | 4 | 「已禁用」+ 指认 `LANGFUSE_ENABLED` + 仍可编辑 + config 接口仍 200 |
| D 表单 | 14 | 150→100、-5→0、清空→0、0% 警告、4 个预设、脏状态深比较（改回原值即消失）、放弃修改 |
| E 规则 | 13 | 增删、字段完整落库、刷新还原、排除规则、**半截时间窗被前端拦下并指名第几条**、直连 API 仍容错 |
| F 权限 | 8 | mgr/alice 403、设置页 403、白名单后 mgr 200 而 alice 仍 403、白名单不放宽设置页 |
| G 联动 | 7 | 100% 采样 3 次 → sink **3 个 span**；详情出现「在 Langfuse 中查看」且 href 带 project+trace id；0% 时 sink 不增、链接消失 |
| H 健壮 | 6 | 1440/1024/390 无横向溢出、深色模式生效且正文亮度 0.96、保存按钮可聚焦、0 条 JS 报错 |

另外 `/api/option/` 首启返回 `metadata_sample_rate=10`、`content_sample_rate=100`。

一条只有真浏览器才抓得到的缺陷：**半截时间窗**（只填 `09:00` 不填结束）后端会丢弃两端
（运行时正确），但 `OptionMap` 保留原值，于是界面显示「已保存 22:00」而实际按全天算。
修法是提交前拦下并指出第几条规则；后端容错保留给直连 API 的场景。

另：`go test ./router/` 的「每条面板路由都要有 scope 规则」用例此前因并行开发的
`/pg/video|images/generations` 未登记而红，本轮补了三条 `accessTokenSessionRule`
（playground 面板走浏览器会话，中继 key 不应能驱动）。

### 20.5 没做

- 预设不会自动保存：填表后仍需点「保存」，因为这一页改的是生产。
- 「恢复出厂」只回到表单默认，不能删除数据库里已有的行（选项系统没有删除语义）。

### 20.6 第一性修复：让「存下来的」「跑起来的」「显示出来的」是同一个东西

测试报告里的问题看起来是三个 bug，根因只有一个：**同一份配置在数据库、运行时、表单、
状态条四处各说一套**。所以修的都不是症状：

| 原来 | 现在 | 为什么 |
|---|---|---|
| 半窗口在**读取时**修补（normalize 丢掉两端），库里留着 `09:00–` | 在 `validateOptionValue` 里**写入即拒绝**（`ValidateRuleWindows`） | 读取时修补保证运行时不犯错，但库和界面继续撒谎。normalize 保留给老数据/手工导入 |
| 表单 6 个字段逐个 PUT，第 5 个失败时前 4 个已落库 | 前端 1 个 `PUT /api/option/batch`，后端 `UpdateOptionsBulk`：全量校验 → 单事务 → 一次审计 | 「新速率 + 旧规则」的半套策略比保存失败更难排查 |
| 规则顺序只能靠「删了重加」 | 卡片带编号 + 上移/下移（首尾禁用） | 顺序就是优先级，优先级不能编排等于规则只有一半可用 |
| `rules`/`trace_viewer_user_ids` 空值序列化成 `null` | 初始值与 `Get()` 都返回 `[]` | 每个消费方都要特判 null，迟早有一个漏掉 |
| 视觉 Label 是兄弟节点，输入框没有可访问名 | 全部补 `aria-label`（含开关） | 屏幕阅读器读不出「元数据采样」还是「内容采样」 |
| 状态条写「当前生效」，实际只反映未命中规则的默认值 | 改成「未命中规则时的默认值」 | 配了规则之后这个数字对多数模型是错的 |

验证：浏览器套件从 72 条增到 **80 条**（新增：直连 API 被拒绝且库未变、批量失败整批不落地、
上移/下移真的改了后端顺序、`main` 内所有输入与开关都有可访问名），全绿。
另单独验证「一次保存只发一个 `PUT /api/option/batch`」（原来 6 个）。

### 20.7 收口：标记闸门、合并面与一个慢网竞态

**1) 数据没到就别让人编辑（真 bug，单测抓到的）**

`optionsQuery` 还没 resolve 时，表单以 `EMPTY_DRAFT`（100/100）为基底渲染。此时改一个字段
再保存，提交的是「改过的字段 + 100/100」，而不是库里的 10/100 —— 慢网下一次误操作就能把
采样率从 10% 拉到 100%。修法：`optionsQuery.data === undefined` 时只渲染「正在读取设置…」，
读失败时给重试提示，**不渲染可编辑表单**。测试里 `timezone` 从 `Asia/Shanghai` 变成空串，
就是这条的指纹。

**2) 批量助手移出上游文件**

`updateSystemOptionsBatch` 原来加在 `web/src/features/system-settings/api.ts`（上游文件）里，
21 行未标记。真正的修法不是补标记块，而是**挪出去**：新建
`web/src/features/system-settings/apiduo-option-batch.ts`，`api.ts` 与 HEAD 字节一致，
合并面归零。

**3) go.mod 不该按行标记**

它是 `require` 列表，`go mod tidy` 会按模块名重排；包块注释或加行尾注释都会被下一次 tidy
打散。依赖冲突是版本号的语义冲突，靠 tidy + 编译解决。已加入 `markercheck` 的 `skipName`
（与 `routeTree.gen.ts` 同类：生成物，手写标记会被覆盖）。

**4) 预算**

`markercheck` 从红转绿：遗留欠账 36 文件/444 行 → **35 文件/433 行**；预算
108/320/3048/109 → **107/324/3065/104**（文件数与删除行下降，标记块 +4、插入行 +17，
增量来自两处不可挪的挂接点：`access_token_routes.go` 的 playground 路由规则、
`model/option.go` 的写入校验钩子）。

---

## 21. 对齐 Langfuse v4（2026-11-10）

上一轮（§18）对着 v3.225.15 做的验证，所有属性映射都在 v3 上确认过。v4 是
**observations-first 数据模型**（`events_full` / `events_core` 两张新表，trace 变成
root observation），属性命名空间没变，但有几个字段的语义从「可选」变成了「唯一入口」。
本节记录两侧对齐的结果。

### 21.1 代码侧：三个属性从「采集了但没人看」变成「官方字段」

| 属性 | 原来 | 现在 | 为什么必须发 |
|---|---|---|---|
| `langfuse.observation.completion_start_time` | 只有 `apiduo.ttft_ms`（自定义键 ⇒ 沉到 `metadata.attributes`，UI 不读） | ISO 8601，`start + ttft` | v4 UI 的 "Time to first token" 只认这个字段。只发自定义键 = 数采了但永远不显示 |
| `langfuse.release` | 无 | 构建版本（`common.Version`，桥接 `init()` 注入） | v4 把 trace 级属性复制到每一行，于是「哪个网关版本产生的这条 span」成了可筛列。发版后不用再靠时间戳猜 |
| `langfuse.environment` | 无 | `LANGFUSE_ENVIRONMENT`，默认 `production` | 临时 e2e 网关和生产网关连同一个 Langfuse，没有它两类 trace 混在一个池子里；e2e 设 `e2e` 即可分开 |

`langfuse.release` 的取值优先级：`LANGFUSE_RELEASE`（显式 pin） > `SetBuildRelease()`
（构建注入） > 空（不写）。运维在 `.env` 里钉的值必须赢过二进制里的版本戳，
否则一次「临时指向 canary」的验证会被构建号覆盖。

trace 包不 import `common`（零内部依赖红线，§5），所以版本号由
`controller/apiduo_trace_relay.go` 的 `init()` 注入 —— 那是已有的桥接文件，不新增挂接点。

### 21.2 v4 里确认仍然有效的东西（不要误删）

| 属性 | 状态 |
|---|---|
| `langfuse.observation.type=generation` | v4 扩充了可选值（`agent` / `tool` / `guardrail` 等），`generation` 仍是合法值 |
| `langfuse.trace.name` / `langfuse.trace.metadata.*` | 有效，且仍是**唯一**能进顶层可筛 metadata 的写法 |
| `langfuse.user.id` | 有效；v4 建议「每个 span 都带」，我们一请求一 span，天然满足 |
| `gen_ai.prompt` / `gen_ai.completion` | 仍是 `langfuse.observation.input/output` 的 fallback，保留 |
| `x-langfuse-ingestion-version: 4` | v4 下不带这个头会**延迟最多 15 分钟**才可见；已从 §4 起就有 |

### 21.3 服务端：v3.225.15 → v4.77.0 的实际操作

数据量极小（`langfuse` 库 634 KB / 823 行），backfill 秒级，所以没有走「保留
retention 窗口等数据自然滚出」那条路。

| 步骤 | 动作 | 结果 |
|---|---|---|
| 0 | 冷备到 `/www/data/apiduo/obs/backup/20261110-v4/` | PG `pg_dumpall`(102K) + ClickHouse **整目录**(405M) + MinIO 桶(953K) + 两份 compose + `.env` |
| 1 | `clickhouse/clickhouse-server:25.8` → `25.12` | v4 硬性下限；25.8 → 25.12 同年度小版本，数据目录原地升级，`langfuse` 库 823 行无损 |
| 2 | `langfuse/langfuse:3` → `:4`（web + worker） | 启动即跑 PG + CH 迁移，`All migrations have been successfully applied` |
| 3 | `LANGFUSE_MIGRATION_V4_WRITE_MODE=dual` | v3 表与 v4 `events_*` 同时写；老 API 继续 200，回退窗口保留 |
| 4 | `LANGFUSE_BACKGROUND_MIGRATION_V4_ENABLE_HISTORIC_BACKFILL=true` | 历史 203 traces → `events_full` 201 行、`events_core` 201 行，`observations_batch_staging` 清空 |
| 5 | **未执行**：`events_only` | 见 21.5 |

🔴 **ClickHouse 备份必须整目录**。25.x 把 part 放在 `store/<前缀>/<uuid>/`，`data/<db>/`
里只有符号链接；`tar czf ... data/langfuse` 只会抓到 12 个链接（727 字节），看起来
「备份成功」而实际内容是空的。第一次就踩了这个坑。

🔴 **热 tar 也会坏**：CH 在跑 merge 时 part 会被删除/改名，`tar` 直接报
`File removed before we read it`。必须先 `docker stop apiduo-clickhouse` 再打包。

### 21.4 端到端验证（真 Langfuse、真 OTLP）

```
POST http://127.0.0.1:39196/api/public/otel/v1/traces   → 200 {"name":"otel-ingestion-job",...}
```

| 断言 | 结果 |
|---|---|
| 带 `x-langfuse-ingestion-version: 4` 的写入 | 12 秒内出现在 `events_full`，无需等 15 分钟 |
| `langfuse.observation.type` → `type` | `GENERATION` |
| `langfuse.environment` / `langfuse.release` | `probe` / `probe-release`，作为**列**存在（可筛） |
| `langfuse.observation.model.name` | `provided_model_name` = `probe-model` |
| `langfuse.observation.input` / `output` | `ping` / `pong` 原文 |
| `langfuse.observation.completion_start_time` | `2026-11-10 07:25:01.500`（毫秒精度完整保留） |
| `gen_ai.usage.*` | 进入 `usage_details` |
| v4 Observations API v2 | `GET /api/public/v2/observations` 返回该行，`isRootObservation=true` |
| v3 老 API（dual 模式） | `GET /api/public/traces/{id}` 仍 200 |
| 健康检查 | `{"status":"OK","version":"4.77.0"}` |

### 21.5 为什么停在 dual，不切 events_only

`events_only` 之后：v3 的 ingestion / read API 全部 404、镜像回退无效（PG/CH schema
已前进），只能靠 21.3 的冷备恢复。而 dual 模式的代价只有「多写一份 v3 表」——
本库 634 KB，可以忽略。

切换的前置条件（都满足后再动手，一条命令即可）：

1. 观察满 24h，`events_full` 行数持续跟上 `traces`（dual 下两者应同量增长）
2. 确认除了 apiduo 没有第二个客户端在走 v3 ingestion（`POST /api/public/traces|spans|generations|events`）
3. `curl /api/health?failIfEventPropagationStuck=true` 不返回 503

切完可选 `LANGFUSE_BACKGROUND_MIGRATION_V4_DROP_PID_TID_SORTING_TABLES=true` 回收旧排序表。

### 21.6 悬而未决

- ~~`langfuse.trace.tags` 未发~~ → **2026-11-10 已发**。原判断「metadata 里可筛，
  标签属于重复信息」把「信息内容」和「通道能力」混成了一件东西：`metadata.*` 只能**过滤**，
  而 tags 是 v2 metrics API 里唯一可**分组**的 trace 维度。不发 tags，「按渠道 / 分组看 p95」
  在 Langfuse 里根本无法表达。现行标签为 `channel:<id>` / `group:<name>` / `stream|non-stream`，
  全部低基数（塞 request_id 之类会让维度爆掉）。
- **`langfuse.observation.model.parameters` 未发**（temperature / max_tokens 等）：
  需要把它们带进 snapshot，属于采集面改动，等有「按 temperature 对比效果」的真实需求再说。
- **apex 指标差异**：v4 的 `timeToFirstToken` 要求
  `completion_start_time ∈ [start_time, end_time]`。探针里我写死了时间导致越界、该字段为
  `null`，真实请求不会越界（`ttftMs` 由 `FirstByteNano - StartUnixNano` 得出，恒在区间内）。

---

## 22. 日志页「轨迹」列（2026-11-10 落地）

§18 把入口放在详情弹窗里，理由是「列表页不该为可观测性让出空间」。这个判断对了一半：
弹窗一次只看一行，而运维真正要回答的是 **「这 200 条里哪几条慢在首字节」** —— 那需要一列。

### 22.1 形态

表格最后一列（`详情` 之后），一请求一格，点开右侧抽屉。**不跳 Langfuse** ——
数据全部走网关自建的代理接口，浏览器拿不到 Langfuse 的 secret。

| 单元格状态 | 依据 | 说明 |
|---|---|---|
| 图标 + 绿点 | `admin_info.trace_id` 存在且日志类型不是错误 | 有 trace |
| 图标 + 红点 | `log.type === LOG_TYPE_ENUM.ERROR` | 有 trace，请求失败 |
| `—`（极淡） | 没有 `trace_id` | 元数据采样没命中，或该行对当前用户剥离了 `admin_info` |

🔴 **列表里不区分「有内容 / 仅元数据」**。这个状态只有 trace 后端知道，逐行去查
等于把日志列表变成 N 次 HTTP。它在抽屉里说清楚 —— 见 22.3。原型阶段画过三态灯，
落地时砍掉第三态就是这个原因。

### 22.2 后端：`GET /api/apiduo/trace/detail/:trace_id`

- 权限与 `/apiduo/trace/config` 完全一致（root 恒可、其余看 `trace_viewer_user_ids`）；
  普通用户拿不到 trace id，所以这一列对他们整列不渲染，而不是渲染成空
- 返回白名单字段（`pkg/apiduo/trace/fetch.go` 的 `Observation`），**不透传 Langfuse 原始 JSON**
- 三种「没有数据」不是 HTTP 错误：`reason=disabled` / `reason=not_found` 走 200 + 说明文案；
  只有后端不可达才是 502

🔴 **v4 删掉了单条 observation 端点**。`GET /api/public/v2/observations/{id}` 在
`events_only` 模式直接 404（HTML）。唯一入口是列表 + `traceId` 过滤，而且**必须带 `fields`**：

```
GET /api/public/v2/observations?traceId=<32hex>&fields=basic,io,usage,metadata,model,time,trace_context
```

不带 `fields` 只回摘要行（和表格里那几列一样），没有 input/output/usage/metadata。
这是本次实测踩到的第一个坑。

取哪一行：带 `traceparent` 的请求是一棵树，只有 `isRootObservation=true` 的根节点才带
网关自己的提示词与回复（子节点属于调用方 agent）。没有该标记的旧数据取最早一条。

🔴 **`/apiduo/trace/detail/` 里的 `detail` 是静态段不是装饰**。Gin 在同一层不允许静态段和
通配段共存，写成 `/apiduo/trace/:trace_id` 会和已有的 `/apiduo/trace/config` 冲突并在启动时 panic。

### 22.3 抽屉里有什么

时间轴（等待上游 / 生成两段，比例来自 `ttft_s / latency_s`）、6 格指标、失败原因、
请求/响应原文、元数据（渠道/分组/令牌/格式/环境/release）。

**「仅元数据」必须有专门文案**：

> 该请求只做了元数据采样，提示词与回复从未离开网关内存，因此这里为空是策略所致，
> 不是采集故障。可在「系统设置 → 可观测性」调整内容采样率。

没有这句，空白 prompt 会被读成采集故障 —— 内容采样率默认 10%，这类行占多数。

### 22.4 前端结构（为什么是三个文件）

| 文件 | 职责 |
|---|---|
| `lib/trace-detail-api.ts` | 类型 + `getTraceDetail`。**独立于 `api.ts`**，让上游文件零改动 |
| `lib/use-apiduo-trace-column.tsx` | 列定义。非管理员或追踪关闭时返回 `null`（不是禁用的空列） |
| `components/apiduo-trace-cell.tsx` | 单元格 + 抽屉 |

拆开的硬原因：一个文件同时 export hook 和组件会触发 `react(only-export-components)`
（Fast Refresh 失效）。`.ts` 里写 JSX 也不行 —— 含 `cell: () => <X/>` 的列定义必须是 `.tsx`，
否则报 `TS1005: ';' expected`，这个错看起来像语法问题，实际是扩展名。

### 22.5 上游合并面

`lib/columns.ts`（+2 标记块 / +12 行）与 `router/api-router.go`（+3 行）是仅有的两处上游改动，
其余全是新文件。`api.ts` 一度加过 45 行，后来整段挪进 `lib/trace-detail-api.ts` ——
能放进新文件的就不要留在上游文件里。

---

## 23. Langfuse 侧指标能力（2026-11-10 实测契约）

远端实例 4.77.0 的 `/api/openapi.yaml` 给出的硬边界，直接决定「要不要再上 Prometheus」：

| 想要 | 通道 | 备注 |
|---|---|---|
| QPS / 请求数 | `GET /api/public/v2/metrics` + `view=observations` + `measure=count` | v2 **没有** traces view |
| P50/P95/P99 延迟 | `aggregation=p50/p75/p90/p95/p99`（另有 `histogram`） | 由 span 的 start/end 推导，时间戳错则分位数全废 |
| TTFT | `measure=timeToFirstToken` | 前提 `completion_start_time ∈ [start, end]` |
| 错误率 | filter `level=ERROR` 的 count ÷ 总 count | 两侧采样率不一致时会系统性失真 |
| 按模型分组 | `dimensions=[providedModelName]` | |
| 按渠道 / 分组**分组** | 只能靠 `langfuse.trace.tags` | `metadata.*` 可筛不可分组 |
| 原生告警 | Alerts（v4+ 自托管无条数上限） | Slack / PagerDuty / Webhook / GitHub Actions；窗口最短 1h |

采样配置有两条硬约束，弄错会让上面每一项都变成假数：

1. `LANGFUSE_SAMPLE_RATE` 同时是 **Metadata 与 Content 的 fallback**（`config.go:142-143`）。
   要「全量指标 + 少量内容」必须显式设 `LANGFUSE_CONTENT_SAMPLE_RATE`；只改前者会把内容体
   （每请求 ≤48 KB）一起拉满。
2. 控制台设置走 `ApplySettings` 覆盖环境变量（`setting/observability_setting`）。库里已有设置行时
   改 `.env` 不生效。

`ParentBased` 硬编码为 `true`（`config.go:157`，无 env 开关）：请求带 `traceparent` 且
sampled bit = 0 时整条 trace 不发，采样率设 100 也救不回来。

### 22.6 对话视图（2026-11-10 追加）

原文 JSON 对写网关的人够用，对"这次到底给客户发了什么提示词"不够用 —— 要在
`messages[].content` 里逐层找。所以请求/响应默认渲染成对话轮次，原文留一个切换按钮。

| 元素 | 来源 |
|---|---|
| 角色分组（system / user / assistant / tool） | `messages[].role`；Anthropic 顶层 `system` 提到最前 |
| 气泡内容 | `content` 字符串，或 content parts 数组（`text` 拼接、`image_url` → `[image]`） |
| 工具调用 | `tool_calls[].function.name/arguments`；Anthropic 的 `tool_use` block 也认 |
| 参数行（model / stream / temperature…） | 与消息分开，避免把"配置"当成"说过的话" |
| `（空）` 占位 | 有 role 但没内容（assistant 调工具前的那一轮），保留以维持轮次顺序 |

**解析失败一律回退原文**，不做"看起来完整"的重构。三种典型失败：headtail 截断
（JSON 不完整）、SSE 帧堆、上游错误字符串。

`web/src/features/usage-logs/lib/conversation.ts`，12 个单测覆盖上面每一种分支
（`lib/__tests__/conversation.test.ts`）。纯函数，不碰 DOM，所以能在 vitest 里跑。

🔴 **前端构建必须用 `bun run build`**。直调 `./node_modules/.bin/rsbuild build` 会
静默丢掉 5MB 级的大 chunk（rsbuild 照样打印 `Total: 68096 kB`），页面表现为全空白 +
`SyntaxError: Unexpected token '<'` —— 缺的 chunk 被 SPA fallback 成 index.html 了。

### 22.7 取数延迟的实测基线

| 场景 | 耗时 | 说明 |
|---|---|---|
| ClickHouse 单条查询 | **4 ms** | `system.query_log` 实测，read_rows=1 |
| Langfuse API：查不存在的 trace | **1.2 s** | 空结果，纯应用层开销 |
| Langfuse API：107 字符的 trace | 1.4 s | |
| Langfuse API：51 KB 的 trace | 1.7–3.0 s | body 已外置到 MinIO |

结论：**慢在 Langfuse 应用层与主机负载，不在查询、不在前端**。
`time -p ssh … docker exec … clickhouse-client` 会报出 47 秒的假数字，
那是 ssh + docker 启动开销；真实耗时只看 `system.query_log`。

主机侧：`load average 36/94/86`（64 核），两个非 Langfuse 的 ClickHouse 各占 56–70% CPU，
Langfuse 自己的 CH 只占 5.5%。所以要提速得挪机器或降负载，不是改 SQL。

v4 会把**约 1 KB 以上**的 body 外置到 MinIO（`blob_storage_file_path` 非空），
235 字符以下留在 CH。前端因此做了会话级缓存：同一条 trace 只读一次。

---

## 24. 配置面板热更新（2026-11-10）

§4 的 env 列表是「从未保存过设置」时的初始值来源。此后可观测性页
（系统设置 → 扩展 → 可观测性）接管以下参数，**保存即生效，无需重启**：

| 参数 | option key | 默认（= env 未设时） | 热更新 |
|---|---|---|---|
| 常规元数据采样 | `observability_setting.metadata_sample_rate` | `LANGFUSE_SAMPLE_RATE`（100） | 采集时读实时值 |
| 内容采样 | `observability_setting.content_sample_rate` | `LANGFUSE_CONTENT_SAMPLE_RATE`（10） | 同上 |
| 失败请求采样 | `observability_setting.error_sample_rate` | `LANGFUSE_ERROR_SAMPLE_RATE`（100） | 同上 |
| 内容模式 | `observability_setting.body_mode` | `LANGFUSE_BODY_MODE`（headtail） | 同上 |
| 头/尾窗口 | `observability_setting.body_head_bytes` / `body_tail_bytes` | 32768 / 16384 | 同上 |
| 脱敏 | `observability_setting.redact` | 开（`LANGFUSE_REDACT=false` 才关） | **导出时**读实时值 |
| 环境标签 | `observability_setting.environment` | `LANGFUSE_ENVIRONMENT`（production） | 同上 |
| 时区 / 规则 / 查看者 | 同 §19 | — | 同上 |

窗口有合并上限 **128 KB**（head+tail，`trace.MaxBodyWindowBytes`）：写接口的跨键
校验在合并视图上判（只改一条也要按现存另一条合并），运行时 `ClampBodyWindow`
按比例压回，normalize 保留操作员选择的比例而不是清零一端。

### 24.1 热更新的实现边界：两种配置、两种生命周期

导出 worker 是按启动时的配置构建的，`flush` 分开读两种配置：

- **路由配置**（host、密钥、project id）用 worker 启动时捕获的那份。这些只随
  重启变化，且旧 worker 绝不能中途改发别的端点 —— 否则一个正在退役的 sink
  会收到为旧端点排队的批次（测试里表现为跨用例污染，曾真实发生过：
  `TestUnsampledLogHasNoTraceID` 稳定挂，直到拆分路由与实时配置）。
- **逐批决策**（脱敏、环境标签、release）走 `liveExportConfig` 每批读实时值。
  原因是合规开关：关掉脱敏的「下一批生效」必须是真的，一个要重启才生效的
  合规开关在重启前一直是错的。

回归钉子：`TestConsoleEditsApplyWithoutRestart`（e2e_test.go）用同一个 worker
先发一批脱敏开启的 span、再 `ApplySettings` 关掉脱敏换环境标签、再发一批，
断言第二批原样带 key 且标签已翻转 —— 同进程、不重启、不重建 exporter。

### 24.2 仍然只认 env 的参数（面板上明示「需重启」）

| env | 为什么不能热更 |
|---|---|
| `LANGFUSE_ENABLED` | 进程启动时 tracing 关 = 从未构建 exporter，热开缺整套管道 |
| `LANGFUSE_HOST` / `KEYS` / `PROJECT_ID` | 路由与凭据，见 §24.1；且密钥不该能从管理表单读出 |
| `LANGFUSE_QUEUE_META` / `QUEUE_CONTENT` | 队列是信道容量，worker 启动时固定 |
| `LANGFUSE_FLUSH_INTERVAL_MS` | 是 ticker，worker 启动时固定 |
| `LANGFUSE_TIMEOUT_SECONDS` | 是 HTTP client 创建参数 |

面板把这些列成只读清单（含各自原因），并注明 `docker restart` 不重读 env ——
要 `docker compose up -d --force-recreate`。
