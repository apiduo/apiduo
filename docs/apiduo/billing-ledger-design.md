# 账单：月度对账与明细 · 解决方案

对应原型：`design/billing-ledger-prototype.html`
一句话：**账单自己的端点和快照表，不复用日志接口；展示层复用控制台已有的表格、统计卡、CSV 与格式化工具；当月的实时视图读 `logs`，已出账的账期读快照，金额一律由后端算好下发。**

---

## 0. 先说结论

1. **账单要与日志解耦，但不能与日志打架。** 当月账单（未出账）实时读 `logs`，因为快照还没生成；已出账的账期读 `billing_*` 快照，因为日志会被 TTL 清理、汇率会变。两者是同一页面的两个状态，由 `status` 切换数据源，不共存两份金额。
2. **你给的公式覆盖了 80% 的文本请求，但有四处漏项**（见 §2.2）：缓存写入、其他倍率、音频/工具附加费、固定价与分档模型。漏了这些，账单在 Claude 长上下文、gpt-image、异步视频三类请求上必然对不上。
3. **必须补四个落库字段**，否则"可解释账单"只能解释一半：`pricing`（汇率与 quota_per_unit 快照）、`endpoint`（请求类型）、`other_ratios`（文本路径缺失）、`uncached_input_tokens`（跨上游归一）。另加一个账务标记 `is_refund_of`，否则任务的多行只能靠 `task_id` 猜。
4. **聚合指标只能按分子分母算**。`avg(tps)`、`avg(单价)`、`avg(avg)` 全部错；固定价/任务行的 token 为 0，混进"综合单价"会把单价抬高，必须显式排除并标注口径。
5. **客户账单是单独投影，不是日志导出**（见 §9）。客户只该看到 14 列：请求、token、金额；`/api/log/self` 与现有 NDJSON 导出会把 `channel`、`quota`、`model_ratio` 等一起带出去，必须新端点 + 白名单。
6. **解耦只发生在数据层，展示层必须复用**（见 §11）。`StaticDataTable`、`DataTablePagination`、`toCsvLine`/`downloadCsv`、`formatTokens`、`EmptyState` 直接用；`usage-logs/*` 里带 `UsageLogsContext` 与日志路由参数的组件一个都不复用。唯一必须新写的是金额格式化——`formatLogQuota` 读运行期汇率，账单要显示冻结值。

---

## 1. 现状：数据在哪

| 事实 | 位置 |
|---|---|
| 明细行结构 | `model/log.go` `type Log`（22 个字段，含只读 ChannelName） |
| 文本计费入口 | `service/text_quota.go` `calculateTextQuotaSummary` |
| 倍率与 token 快照写入 | `service/log_info_generate.go` `GenerateTextOtherInfo` / `GenerateClaudeOtherInfo` |
| 任务差额结算 / 退款行 | `service/task_billing.go` `RecalculateTaskQuota` / `refundTaskQuota` |
| quota 单位 | `common/constants.go`：`QuotaPerUnit = 500 * 1000`（50 万 quota = 1 USD） |
| 展示汇率 | `setting/operation_setting/payment_setting_old.go`：`USDExchangeRate = 7.3` |
| 日志库 DDL | `model/main.go` `clickHouseLogCreateTableSQL` |
| 现有导出 | `controller/log.go` `ExportAllLogs`，NDJSON 流式 + 单用户并发锁 |

**关键约束**：ClickHouse 的 `logs` 表**没有** `cache_tokens` / `cache_creation_tokens` / 任何倍率列，全部在 `other String` 里。按缓存聚合就得 `JSONExtractInt(other, 'cache_tokens')`，这是全表 JSON 解析。

---

## 2. 计费公式（唯一真源）

### 2.1 标准文本

```
base   = max(0, prompt − cache_read − cache_write − image − audio)
tokens = base
       + cache_read    × cache_ratio
       + cache_write   × cache_write_ratio      // 5m/1h/12h 各按自己的 ratio
       + image         × image_ratio
       + completion    × completion_ratio

quota  = round( (tokens × model_ratio × group_ratio × Π other_ratios)
                + audio_input_price × audio / 1e6 × group_ratio × QPU
                + Σ(tool_price / 1000 × count × group_ratio × QPU) )
```

这段按 **OpenAI 语义**写：`prompt_tokens` 含缓存，所以要扣。Anthropic 语义下 `prompt_tokens` 本身已排除缓存，代码不会扣 `cache_read`/`cache_write`；`image`/`audio` 也只在有独立单价时才从 base 里扣除。跨上游判断依据是 `usage_semantic`，不要一律套上面的减法。

三个兜底，账单上会看到，必须能解释：

- `!hasBillableUsage()` → `quota = 0`（上游没返回 usage，不收费）
- `ratio ≠ 0 && quota ≤ 0` → `quota = 1`（防白嫖的最小计费）
- 超过 `MaxQuota = 2^31−1` → 饱和，`other.admin_info.quota_saturation` 留痕

### 2.2 你给的公式缺的四项

| 缺项 | 触发场景 | 影响 |
|---|---|---|
| `cache_write × cache_write_ratio` | Claude 缓存写入（1.25×/2×） | 少算 25%–100% 的输入成本 |
| `× Π other_ratios` | 多候选 `n`、任务 `seconds`/`size` | 少算 n 倍 |
| 音频与工具附加费 | Gemini 音频输入、web_search | 少算固定附加项 |
| 固定价 / 分档表达式 / 任务 | gpt-image、seedance、8001 表达式 | 公式根本不适用 |

### 2.3 另外两条计费路径

```
固定价:  quota = round(model_price × QPU × group_ratio × Π other_ratios)
分档:    quota 由 billingexpr 求值（other.billing_mode = "tiered_expr"，
         other.matched_tier / usage_facts 是快照，无法用倍率还原）
任务:    提交行 + 差额行（补扣 type=2 / 退款 type=6），
         other.pre_consumed_quota / actual_quota 记录差额上下文
```

**这意味着**：账单不能假设"每行都能用公式重算"。凡是 `billing_mode = tiered_expr` 或 `model_price > 0` 或带 `task_id` 的行，只能信 `quota`，不能反推。

### 2.4 金额

```
¥ = quota ÷ 500,000 × 7.30        // 1 quota = $0.000002 = ¥0.0000146
```

两个坑：

1. **`usd_exchange_rate` 是标价汇率，不是实付价。** 用户充值时按 `price_ratio`（例如 5 元买 1 USD 额度）实付，与 7.3 无关。账单上的"金额"如果叫"消费金额"，必须说明是标价折算。
2. **汇率可变，历史账单会变形。** 管理员把 7.3 改成 7.5，去年的账单金额跟着变。所以 `pricing` 快照要落库（见 §3.4）。

---

## 3. 明细表：字段字典

### 3.1 现有可直取

| 列 | 来源 | 说明 |
|---|---|---|
| 时间（本地） | `created_at` | Unix 秒，展示转 `Asia/Shanghai` |
| 请求 ID | `request_id` | 客户端可回传，排障主键 |
| 上游请求 ID | `upstream_request_id` | 与上游工单对接 |
| 类型 | `type` | 2 消费 / 6 退款 / 5 错误 / 1 充值 |
| 用户名 | `username` | 快照值，改名后仍显示当时名称 |
| 令牌 | `token_name` + `token_id` | 同名令牌靠 id 区分 |
| 模型 | `model_name` | **计费模型**；请求模型差异在 `admin_info` |
| 分组 | `group` | 实际使用分组（`UsingGroup`），不是用户默认分组 |
| Quota | `quota` | 最终扣费，退款行为正数 |
| Prompt | `prompt_tokens` | 上游原始值，语义随上游变化（见 §3.4） |
| Completion | `completion_tokens` | |
| 耗时 | `use_time` / `use_time_ms` | 秒 + 毫秒双列，报表用毫秒 |
| 流式 | `is_stream` | |
| 渠道 | `channel_id` | 成本归属；用户侧应隐藏 |
| IP | `ip` | 合规审计，默认不展示 |

### 3.2 `other` 里的计费快照

`model_ratio`、`group_ratio`、`user_group_ratio`、`completion_ratio`、`cache_ratio`、`cache_creation_ratio(_5m/_1h/_12h)`、`cache_tokens`、`cache_creation_tokens(_5m/_1h)`、`cache_write_tokens`（归一后的写入总量）、`input_tokens_total`（归一后的输入总量，仅非 Claude 格式且上游声明 `UsageSource` 时写入）、`model_price`、`frt`（首字毫秒）、`request_path`、`billing_mode`、`matched_tier`、`expr_b64`、`usage_facts`、`actual_quota`、`pre_consumed_quota`、`task_id`、`tool_surcharges`、`billing_source`、`stream_status`。

`cache_write_tokens` 与 `input_tokens_total` 是专门给展示层准备的归一字段，**不要再用 prompt−cache 自己推**：Anthropic 语义下 `prompt_tokens` 本来就不含缓存，扣了会重复扣。注意 `input_tokens_total` 只在「非 Claude 格式且上游声明了 `UsageSource`」时写入，其余情况只能按 `usage_semantic` 分支处理。

### 3.3 需要派生

| 列 | 公式 | 备注 |
|---|---|---|
| 未缓存输入 | `uncached_input_tokens`，缺失时 `max(0, prompt − cache_read − cache_write)` | 仅 OpenAI 语义可这样推 |
| 总 Token | `prompt + completion` | 与上游 `total_tokens` 同口径 |
| 金额 | `quota / QPU × rate` | 负数行按退款展示 |
| 输出 tps | `completion / ((use_time_ms − frt)/1000)` | 仅流式且分母 > 0 |
| 缓存抵扣 | `cache_read × (1 − cache_ratio) × model_ratio × group_ratio / QPU × rate` | 讲清"省了多少钱" |

### 3.4 建议新增（按优先级）

| # | 字段 | 落在哪 | 为什么必须有 |
|---|---|---|---|
| 1 | `pricing: {quota_per_unit, usd_rate, currency}` | `other.pricing` | 汇率一变，历史账单金额就变；没有快照就无法复现 |
| 2 | `endpoint`（路由名） | `logs` 新列或 `other.endpoint` | 现在只能从 `request_path` 正则猜请求类型；`pkg/endpoint` 已有权威路由名 |
| 3 | `other_ratios` 全量快照 | 文本路径补写 | 任务路径已写（`task_billing.go`），文本路径漏了；带 `n` 的请求无法重算 |
| 4 | `uncached_input_tokens` | `logs` 新列 | 跨上游统一口径，聚合不必区分 anthropic/openai 语义 |
| 5 | `is_refund_of` / `adjust_reason` | `other` | 任务差额行现在只有 `task_id` + `reason`，对账要靠 task_id 归并 |

> 1、4 一旦缺失，账单就只是"看起来对"；3 缺失会在多候选请求上直接差 n 倍。

---

## 4. 指标口径：先定死

账单的汇总维度只有 `模型 × 分组`（客户视图只剩模型）。指标分两类：**可加的**与**必须重算的**。

| 指标 | 定义 | 可加性 |
|---|---|---|
| 请求数 | `count(type IN (2,5))`，含失败 | sum |
| 成功 / 失败 | `count(type=2)` / `count(type=5)` | sum |
| 退款笔数 | `count(type=6)` | sum |
| 输入 / 缓存读 / 缓存写 / 输出 | 各自 sum | sum |
| 净费用 | `Σtype=2.quota − Σtype=6.quota` | sum |
| 综合单价 ¥/M | `token计费行费用 ÷ (prompt+completion) × 1e6` | **不可加**，必须分子分母分别 sum |
| 单均 ¥/次 | `净费用 ÷ 有效请求数` | 不可加 |
| 日均 ¥/天 | `净费用 ÷ 自然日数` | 不可加 |
| 首字延迟 | `Σfrt ÷ n`（建议再给 P50/P95） | 不可加 |
| 输出 tps | `Σcompletion ÷ Σ生成秒` | 不可加，且仅流式 |
| 缓存命中率 | `cache_read ÷ (uncached + cache_read + cache_write)` | 不可加 |

### 4.1 三个必须写进 tooltip 的口径

1. **综合单价的分母只含 token 计费行。** 固定价（图片）与任务（视频）行的 token 为 0，若混入分子，单价会被抬高几倍。原型里这些行的单价列显示 `—`，汇总行按"文本费用 ÷ 文本 token"计算。
2. **日均按自然日，含零消费日，含当天。** 14 天区间就是除以 14；当天不做小时折算（否则早上看账单会觉得"日均暴跌"）。若产品要"活跃日均"，另开一列，别偷换分母。
3. **单均的分母默认不含退款行与失败行**，但失败行仍计入"请求数"。这个差异要在列头写清楚，否则运营会用两个数互相打架。

### 4.2 时区

ClickHouse 的 `toDateTime(created_at)` 用服务端时区（默认 UTC），按天分组会整体偏移 8 小时。必须显式：

```sql
toDate(toDateTime(created_at, 'Asia/Shanghai')) AS day
```

跨月边界（每月 1 日 00:00–08:00）是这个问题最容易被发现的地方。

---

## 5. 数据层

### 5.1 账期聚合 SQL（ClickHouse）

出账任务与"当月实时视图"共用这段聚合，只是时间窗不同：账期边界是 `[period_start, next_period_start)`，按 `Asia/Shanghai` 切。

```sql
SELECT
  model_name,
  `group`,
  countIf(type IN (2, 5))                    AS requests,
  countIf(type = 2)                          AS succeeded,
  countIf(type = 5)                          AS errors,
  countIf(type = 6)                          AS refunds,
  sumIf(quota, type = 2) - sumIf(quota, type = 6) AS net_quota,
  sumIf(prompt_tokens,     type = 2)         AS prompt_tokens,
  sumIf(completion_tokens, type = 2)         AS completion_tokens,
  sumIf(JSONExtractInt(other, 'cache_tokens'),       type = 2) AS cache_read,
  sumIf(JSONExtractInt(other, 'cache_write_tokens'), type = 2) AS cache_write,
  countIf(type = 2 AND JSONExtractString(other, 'billing_mode') = 'tiered_expr') AS tiered_rows
FROM logs FINAL
WHERE created_at >= ? AND created_at < ?
  AND type IN (2, 6)
  AND model_name = ?           -- 可选
  AND `group` = ?              -- 可选
GROUP BY model_name, `group`
ORDER BY net_quota DESC
```

三个细节：

- `FINAL` 不能省。`ReplacingMergeTree` 的重放去重只在查询期生效（见 `model/log_final.go`），去掉会让 `sum(quota)` 翻倍。
- 退款行 `quota` 存正数，净额要**减**，不是加。
- `type = 5`（错误）单独统计，不进 token 与费用。
- 按天分组的趋势不在账单里做：那是看板的职责（§11）。账单只需要账期末尾的一个总数。

### 5.2 性能

`JSONExtractInt` 对每行做一次 JSON 解析，1,000 万行约数秒级；日粒度 + 模型过滤后通常可接受。超过这个量级有两条路：

| 方案 | 代价 | 适用 |
|---|---|---|
| 加 `MATERIALIZED` 列（`cache_tokens` / `cache_write_tokens`） | 改 DDL，旧 part 需 mutation 或依赖读取期计算 | 读多写少，推荐先做 |
| 建 `billing_daily` 汇总表（AggregatingMergeTree） | 多一条链路，需要回填与对账 | 跨月/跨年报表 |

**不建议**在应用层先把明细全拉回来再聚合：365 天窗口（`LogMaxRangeDays` 默认 365）会直接打爆内存。

### 5.3 分页

账单是"深翻页"场景，`OFFSET 100000` 在 ClickHouse 上代价很高。用 keyset：

```sql
WHERE (created_at, request_id) < (?, ?)
ORDER BY created_at DESC, request_id DESC
LIMIT 50
```

排序键正好是 `(created_at, request_id)`，可以直接走主键顺序。

### 5.4 MySQL / PostgreSQL 兼容

```sql
CAST(JSON_UNQUOTE(JSON_EXTRACT(other, '$.cache_tokens')) AS SIGNED)   -- MySQL
(other::jsonb ->> 'cache_tokens')::int                                -- PG
```

SQLite 用 `json_extract(other, '$.cache_tokens')`。三套方言要在同一层封装，不要让 controller 里出现 `if clickhouse`。

---

## 6. API 契约

账单只有三个端点，只服务**一个账期**。没有跨账期的列表、没有任意区间的透视——那两件事在日志页与看板（§11）。

```
GET /api/billing/statement?period=2026-10
→ {
    period, status: draft | issued | void,
    period_start, period_end, issued_at,
    currency, amount, quota, requests, errors, refunds,
    items: [{ model, group, requests, errors,
              input_tokens, cache_read_tokens, cache_write_tokens, output_tokens,
              quota, amount, unit_price }],
    pricing: { quota_per_unit, settle_rate, currency },
    is_final
  }

GET /api/billing/statement/lines?period=2026-10&cursor=...&page_size=50
    &model=&group=&username=&type=
→ { items: [{ request_id, created_at, type, token_name, model, endpoint,
              input_tokens, cache_read_tokens, cache_write_tokens, output_tokens,
              amount, quota, task_id, use_time_ms }],
    next_cursor }

GET /api/billing/statement/export?period=2026-10
→ application/vnd.openxmlformats-officedocument.spreadsheetml.sheet
   已出账时是出账生成的文件；当月实时生成（带 -draft）
```

约定：

- `period` 必填；缺省由前端补当月。服务端校验格式与权限，不提供"全部账期"。
- **已出账返回快照**（`status=issued`，`is_final=true`）；当月实时聚合（`draft`，`is_final=false`），页面明确标注"金额随用量变化"。
- 金额一律是 decimal 字符串，已含结算价；前端不做除法、不查汇率（§11.4）。
- 客户视图走 §9 白名单：同一端点按角色返回不同字段，或独立 `GET /api/billing/statement/self`；两条路都行，但**不能**让前端传 `as=customer` 决定字段（原型里的 `?as=` 只是模拟）。
- `items` 是服务端按 `(model, group)` 对账期明细做的一次聚合，不落独立表；`lines` 是逐请求，与明细一一对应。
- 导出从同一份快照取数，`kind=summary` 与 `kind=detail` 的合计必须相等。
- 已出账账期的导出**返回文件**（出账时已生成，§11.6），不查库、不流式；当月 `draft` 才实时生成并受行数上限约束。
- 一个账期只返回一份金额，不存在"列表金额"与"详情金额"两个数。

---

## 7. 对账：让账单能被质疑

三级，从便宜到贵：

1. **出账时重算**（每月 1 日 00:10，出账任务内）：仅对 `billing_mode` 为空、`model_price = 0`、无 `task_id` 的行，用快照倍率重算 `quota`。允许 ±1 quota 的取整差；不可重算的行单独计数。差异超阈值就让出账任务失败并告警，不把差异表推给用户。
2. **与钱包累计比对**（T+1）：`Σlogs.quota` 的日增量 vs `users.used_quota` 的日增量。差异通常来自并发写与退款顺序，超过 0.05% 报警。
3. **与上游账单比对**（月结）：渠道维度的成本 vs 上游发票。这一层超出本页范围，但 `channel_id` 必须在明细里保留。

第 1 级从界面上撤掉了：对账是**出账任务的断言**，不是给用户看的报表。用户在账单页看到的每一行，都应该是已经通过这道校验的数据；差异要出现在运维告警里，而不是客户的页面上。原型里保留的失败态演示（页脚「模拟取数失败」）对应的是取数失败，不是对账差异。

---

## 8. 产品原型

`design/billing-ledger-prototype.html`，单文件、无依赖、可离线打开。

**视觉不另起一套。** 账单是控制台内页，复用 apiduo 控制台的壳层与组件尺度：48px 顶栏 + 15rem 侧栏 + 内容区；卡片 `rounded-xl`（本主题 `--radius: 1rem`，实际约 20px）、1px `--border`、`shadow-xs`；主色 `--primary`（oklch 0.692 0.141 243.7）；表格 12/13px、控件 h-8、页签 pill。第一版那套纸墨报表风只适合独立看板，嵌进控制台会像另一个产品，已废弃。

结构只有一层（不再有账期列表页）：

- **页头**：`2026-10 账单` + 三态标签（进行中 / 已出账 / 已结清）+ 周期与冻结说明 + 账期下拉 + 「账单信息」+ 导出按钮。三个账期各演示一态：11 月进行中、10 月已出账未付、9 月已结清。
- **账单信息抽屉**：账务字段（账期、状态、周期、出账时间、到期日、结清日、净费用、请求/失败/退款）+ 归档文件（文件名、行数、大小、sha256 前 8 位、下载）+ 通知记录（时间、类型、渠道、收件人、状态）。运营视图看全站账单，通知区写明「内部账不对外投递」；客户与代客视图看该客户的发送记录（出账通知 + T-3/T-7 催收）。对应 §12 的两张表。
- **统计卡 3 张**：净费用（副行日均）、请求（副行失败与单均）、综合单价（副行 token 与缓存命中）。缓存命中降为副行，不单独占卡。
- **页签 2 个**：汇总 / 明细，默认汇总。
- **汇总**：一张平表（请求 / 失败 / 输入 / 缓存读 / 缓存写 / 输出 / 总 token / 费用 / 占比 / 综合单价），按费用降序，合计行吸底。管理员可切换维度（**用户 / 模型 / 分组**，默认用户），客户固定模型维度。点行下钻到明细并带上该维度的筛选。汇总由明细 `GROUP BY` 得到，不落独立表。

  维度做成一个 seg 控件而不是三级树：管理员先看"谁花得多"，再看"花在哪个模型"，是两个动作，不是一次展开。树把两件事塞进一个视图，反而要点三次才知道结果。
- **明细**：筛选（模型/分组/用户/类型/搜索）+ 20 列逐行 + 分页；时间与请求 ID 冻结，表头 sticky；点行开右侧 sheet（620px），逐项拆解 `(base + cache×ratio + …) × model × group`，末行 `quota ÷ 500,000 × 7.30`。对应 `statement_lines`。
- **导出**：页头一个主按钮，一次下载整月账单 xlsx（账单信息 / 汇总 / 明细 三个工作表），不跟随页签、不需要下拉选择。已出账时按钮是「下载月账单」（文件已生成），当月是「导出月账单」（实时生成，文件名带 `-draft`）。
- **视角**：运营 / 客户由身份决定，页面上不提供开关（原型用 `?as=customer` 模拟服务端投影）。客户视图 14 列，隐藏 quota/分组/用户/计费模式/首字/tps，只显示结算金额。
- **URL**：`?period=2026-10&tab=detail&model=claude-sonnet-4.5&group=default`，刷新与分享都能复原。
- **状态**：空结果写"该账期没有消费记录"；取数失败保留上次结果并标注"可能过期"（页脚可切换演示）。

**快捷入口**：侧栏「账单」一级项是主入口；顶栏头像菜单里的「我的账单」显示当月金额（`本月 ¥549.67`），点击直达当月账单；`?period=now` 可作书签或命令面板跳转；站外入口是邮件与短信里的签名链接（§12）。概览页的「本月消费」卡片还缺一条「查看账单 →」——用户看完数字最想核对明细，这条留给前端实现时补。

**入口位置**：放侧栏「工作台」下，与令牌、日志并列，**不放进「数据看板」**。三条理由：

1. 账单是结算凭证，不是分析工具；混进看板会让人以为它是可选的洞察页，找不到或不敢用。
2. 客户找账单是目标明确的动作，不会去"数据看板"里翻。
3. 出账、欠费、发票后续都会挂这里，需要自己的路由与权限位（`billing:read`）；没有该权限的用户，侧栏不显示这一项。

如果产品上一定要收拢入口，可以放进「数据看板」组的最后一项，但路由、权限与数据源必须独立，不能与概览共用查询。

从上一版砍掉的，都不是丢弃，而是挪走：

| 砍掉 | 去向 |
|---|---|
| 账期列表页 | 账期下拉。月份只有个位数，列表多一层点击却没多信息 |
| 对账页签 | 出账任务的断言（§7）。差异应进运维告警，不该出现在客户页面 |
| 列设置 | 默认列已按角色定好。让用户自己配 20 列，是把设计问题推给用户 |
| 视角切换器 | 视角是权限，不是开关；同一 URL 不因个人偏好返回不同字段 |
| 自定义区间 | 要查任意区间的实时流水，去「用量日志」页，那里本来就有 |
| 每日柱状图 / 日期树 | 账单的最小单位是账期，不是日。看趋势是看板的事（§11） |

---

## 9. 下游客户对账单（自助导出）

原则：一行只回答三个问题——**哪次请求、用了多少 token、扣了多少钱**。上游、渠道、倍率、quota 一律不下发。

### 9.1 字段白名单

| 列 | 来源 | 备注 |
|---|---|---|
| 时间（本地） | `created_at` | 账户时区，精确到秒 |
| 请求 ID | `request_id` | 客户投诉/对账的唯一主键，完整展示，不截断 |
| 类型 | `type` → 消费/退款 | 退款为负 |
| 令牌 | `token_name` | 客户自己的令牌名 |
| 模型 | `model_name` | 计费模型 = 请求模型或其定价规范名 |
| 输入 token（未缓存） | 归一字段 | `max(0, prompt − cache_read − cache_write)` |
| 缓存读 / 缓存写 | `other.cache_tokens` / `other.cache_write_tokens` | 缓存折扣要能被解释 |
| 输出 token | `completion_tokens` | |
| 总 token | 上四项之和 | 与用量页一致 |
| 金额（¥） | 服务端计算 | 只给结算金额，不给 quota 与汇率 |
| 请求类型 | `endpoint` | 公开路由名，可选 |
| 任务 ID | `other.task_id` | 异步客户需要，可空 |
| 耗时（ms） | `use_time_ms` | 可选，排障用 |

### 9.2 禁止下发

- **归属**：`channel_id`、`channel_name`、`upstream_request_id`、`ip`、`user_id`、`token_id`。
- **计价结构**：`quota`、`model_ratio`、`group_ratio`、`completion_ratio`、`cache_ratio`、`cache_creation_ratio*`、`model_price`、`billing_mode`、`matched_tier`、`expr_b64`、`usage_facts`。
- **原始元数据**：`other` 整体、`admin_info`、`trace_id`、渠道名、上游模型名。

### 9.3 现有实现的三个泄漏点

1. `GET /api/log/self` 返回整行 `Log`，JSON 里有 `channel`（channel_id）、`quota`、`ip`；`other` 只删了 `admin_info` 与几个历史敏感键，`model_ratio` / `group_ratio` / `cache_ratio` / `model_price` / `billing_mode` 仍然可见。
2. `ExportUserLogs` 直接 `common.Marshal(log)` 写 NDJSON。`StreamUserLogs` 虽然调了 `formatLogOtherJSON(..., user)`，但那只是**角色裁剪**，不是**字段白名单**。
3. `model_name` 是计费模型（`GetBillingModelName` = 定价别名或请求模型），不是上游模型，可以保留；但 `admin_info` 里的 `upstream_model_name` / `response_model` 绝不能随导出出去。

结论：**不要复用 `/api/log/self` 与现有导出做客户对账单**，要新加投影端点，否则每次加字段都会重新泄漏一次。

### 9.4 金额口径

- quota 是内部单位（500,000/USD），暴露它等于暴露美元成本结构，客户账单只给金额。
- 结算金额按**客户合同价**（`price_ratio` 与折扣后单价）折算；运营侧的标价（`usd_exchange_rate`）是另一个数。两者只下发一个，且要在页脚写明是哪一个。
- 退款为负，保留 4 位小数；币种随账户配置，不写死 ¥。

### 9.5 端点与权限

```
GET /api/billing/statement?...          # 只读自己
GET /api/billing/statement/export       # CSV 流式
```

- `user_id` 从会话取，不接受客户端传 username；管理员代客导出走 `?user_id=`，写审计（操作者、目标用户、区间、行数、文件哈希）。
- 时间窗复用 `log_setting.max_range_days`（默认 365）；导出复用单用户并发锁 `AcquireLogExportLock`。
- 文件：xlsx（与 §10.6 同一份生成逻辑，客户投影），中文列头，文件名 `statement-2026-10.xlsx`；不导出 `other`。若客户系统需要对账导入，再单独提供 CSV。
- 错误行金额为 0，默认不导出；需要排障时由客户在页面按类型筛。

投影要显式声明，不要 `SELECT *`：

```go
type statementRow struct {
    CreatedAt  int64  `json:"created_at"`
    RequestID  string `json:"request_id"`
    Type       int    `json:"type"`          // 2 消费 / 6 退款
    TokenName  string `json:"token_name"`
    Model      string `json:"model_name"`    // 计费模型，非上游模型
    Endpoint   string `json:"endpoint"`
    Input      int    `json:"input_tokens"`  // 未命中缓存
    CacheRead  int    `json:"cache_read_tokens"`
    CacheWrite int    `json:"cache_write_tokens"`
    Output     int    `json:"output_tokens"`
    Amount     string `json:"amount"`        // decimal 字符串，结算币种
    TaskID     string `json:"task_id,omitempty"`
    UseTimeMs  int    `json:"use_time_ms"`
}
```

```sql
SELECT created_at, request_id, type, token_name, model_name,
       JSONExtractString(other, 'request_path')                               AS endpoint,
       greatest(prompt_tokens
                - JSONExtractInt(other, 'cache_tokens')
                - JSONExtractInt(other, 'cache_write_tokens'), 0)             AS input_tokens,
       JSONExtractInt(other, 'cache_tokens')                                   AS cache_read_tokens,
       JSONExtractInt(other, 'cache_write_tokens')                             AS cache_write_tokens,
       completion_tokens                                                       AS output_tokens,
       JSONExtractString(other, 'task_id')                                     AS task_id,
       use_time_ms
FROM logs FINAL
WHERE user_id = ? AND created_at >= ? AND created_at < ? AND type IN (2, 6)
ORDER BY created_at DESC, request_id DESC
LIMIT ?
```

金额在 Go 里用 `decimal` 算（`quota / 500000 × 结算价`），不要用 SQL 浮点，也不要让前端二次换算。

### 9.6 管理员与普通用户的差异

字段级对照（同一份账期数据，两个投影）：

| 维度 | 管理员 | 普通用户 |
|---|---|---|
| 可见范围 | 全站；可切换用户，也可代客查看 | 仅本人 |
| 汇总维度 | 用户 / 模型 / 分组（可切换，默认用户） | 固定模型 |
| 明细列 | 20 列 | 14 列（无 quota / 分组 / 用户 / 计费模式 / 首字 / tps / 流式，多一列总 token） |
| 金额 | 标价：`quota ÷ 500,000 × 7.30` | 结算金额：后端下发的 decimal 字符串 |
| 拆解抽屉 | 倍率、公式、quota、汇率快照 | 只有 token 用量与结算金额 |
| 对账动作 | 出账时自动跑（§7），差异进告警 | 不可见 |
| 导出 | 24 列，含 `model_ratio` / `group_ratio` / `billing_mode` | 14 列白名单（§9.1） |
| 跨账期 | 可切换任意账期 | 只能看自己开户后的账期 |
| 权限 | `billing:read` + `admin` | `billing:read`（隐含本人） |

### 9.7 管理员其实有三个人格

不要用一个 `isAdmin` 布尔把三种场景混在一起，否则字段迟早错配：

1. **平台运营**：全站月度收入。汇总按用户，看谁贡献最多；点进去看该用户的模型分布。金额是标价，关注的是营收与成本差。
2. **代客对账**：管理员帮客户解释账单。此时应看到**客户视角的字段**（14 列、结算金额），再额外叠加一个"内部倍率"折叠区，供解释用。导出必须走客户白名单——否则客服把 24 列的文件发给客户，等于泄漏。
3. **审计**：查某一行为什么是这个数。走日志详情或 `statement_lines` 的原始行，不在账单页做。

对应实现：`?as=customer` 只用于第 2 种场景，且必须由服务端根据"操作者是否有 admin + 目标是否他人"决定，不能让前端传参决定字段（原型里的 `?as=` 只是模拟）。

代客对账的**渲染投影 = 客户投影**，这是原型已实现的规则：

- 汇总按模型、明细 14 列、金额只显示结算值，与导出完全一致，避免"看到 20 列、导出 14 列"。
- 唯一保留的内部信息在抽屉里：一个默认折叠的「内部计费拆解（不对客）」，展开后才显示倍率、公式、quota。
- 判定条件：`角色 = admin` 且 `筛选到具体用户`。选回"全部用户"即退出代客态。
- 导出**强制**客户白名单，不受页面开关影响。管理员要导内部版，必须回到"全部用户"或走审计入口——多一步，但换掉一次误发。

---

## 10. 月度账单（出账、下钻与导出）

账单按自然月，次月 1 日 00:10 自动出账。原型只有一个页面，用账期下拉切换月份：

- **汇总页签**（默认）：`?period=2026-10`，模型 × 分组，合计吸底，点行下钻。
- **明细页签**：`?period=2026-10&tab=detail&model=claude-sonnet-4.5&group=default`，逐行 + 拆解抽屉 + 导出。

### 10.1 账期与出账时点

- 账期 = `[当月 01 00:00, 次月 01 00:00)`，时区 `Asia/Shanghai`。`created_at` 是 UTC 秒，边界必须按本地时区切，否则每月 1 日凌晨会错 8 小时。
- 出账任务：每月 1 日 **00:10** 跑，留 10 分钟给迟到日志与异步任务结算；失败重试，T+1 仍可重跑。
- 当月账单随时可看，标「进行中 · 预估计」；出账后金额冻结，页面不再随汇率/倍率变化。

### 10.2 出账是快照，不是每次查询现算

两样东西必须冻结：

1. **金额口径**：`pricing {quota_per_unit, settle_rate, currency}`。只存 `quota` 而不存结算汇率，管理员一改汇率，历史账单金额就变。
2. **明细**：账期内逐行留存到独立表，每行的 `amount` 在这一步定死。**不能只引用 `logs`**——日志 TTL 建议 180–365 天，账单通常要留 3–5 年；日志删了，账单就无法复现。

```sql
billing_statements(
  id, period, user_id, status,               -- draft | issued | paid | void
  period_start, period_end, issued_at,
  currency, total_amount, total_quota, requests, refunds,
  pricing json,
  UNIQUE(period, user_id)
)

billing_statement_lines(                     -- 明细：逐请求，ClickHouse 月分区
  statement_id, request_id, created_at, type, token_name, model, endpoint,
  input_tokens, cache_read_tokens, cache_write_tokens, output_tokens,
  quota, amount, task_id
)

billing_statement_exports(                   -- 出账时生成好的文件（§11.6）
  statement_id,
  projection,                                -- admin | customer
  object_key, bytes, rows, sha256, created_at,
  UNIQUE(statement_id, projection)
)
```

**为什么不复用现有的表。** 三个候选都核过：

| 候选 | 看起来像 | 实际缺什么 |
|---|---|---|
| `quota_data`（`model/usedata.go`） | 按小时聚合，维度齐全（user / model / group / token / channel / node），已有 `cache_read_tokens` 与 `cache_creation_tokens` | 只有 `token_used = prompt + completion` 的**合并值**，拆不出输入与输出；退款不冲减（任务路径只在 `LogTypeConsume` 时写）；没有金额字段；且只在 `DataExportEnabled` 打开时才有数据 |
| `subscription_orders` / `user_subscriptions` | 有金额、有周期 | 是买套餐与额度，不是用量账单；同一笔钱不等于同一个月用了多少 |
| `topups` / `redemptions` | 有金额流水 | 余额充值，与消费构成无关 |

`logs` 也不是账单表：TTL 会删、整行含渠道与内容、金额要按当时汇率现算、退款是独立正数行。它是账单的**输入**，不是凭证。

一个与账单无关的观察，不阻塞本方案：`quota_data` 按小时聚合、没有清理任务，且没有唯一索引（多实例下同一组合可能插出重复行，总量仍正确、行数偏多）。它服务的是看板默认的「最近 24 小时」视图，粒度不能改；量级起来再说，本方案不依赖它。

**汇总不单独建表。** `statement_lines` 每行的 `amount` 出账时已冻结，按 `(model, group)` 加总就是汇总，结果不会变；单用户单月 2 千到 200 万行，ClickHouse 上 `GROUP BY` 在百毫秒级。省掉 `statement_items` 同时省掉"汇总与明细对不上"这类问题——它们本来就是同一批数。首屏若有更严的延迟要求，再加物化视图或出账时写一份缓存，那是优化，不是语义必需。

再加一样：**导出文件**。大客户的月度明细是几百万行，不可能每次点导出都实时流一遍；出账任务已经把全月数据读了一遍，顺手写文件几乎不增加成本，之后每次下载都是静态文件。

### 10.3 生成流程（幂等）

1. 按本地时区算账期边界，确认该 `(period, user_id)` 不存在或仍是 `draft`。
2. 从 `logs FINAL` 聚合，写 `statement_lines`（逐行，金额在这一步冻结）。
3. 分批把明细写入 `statement_lines`，以 `request_id` 去重。
4. 校验：`Σ lines.amount == statements.total_amount`（汇总由明细加总，不存在第三个来源）。
5. 写 `statements` 头（含定价快照），`status = issued`。
6. **生成账单文件**：一份 xlsx 三个工作表（账单信息 / 汇总 / 明细），管理端与客户端两个投影各一份。写入 `data/exports/<period>/`（或对象存储），登记 `billing_statement_exports`（含行数与 `sha256`）。文件可校验、可归档、可 CDN，也是邮件附件的来源。
7. 可选：生成 PDF、发通知。

重跑只允许 `draft`；已 `issued` 的账单只能作废重开（`void` + 新单号），不能原地改数。

### 10.4 跨月退款与调整

- 退款/补扣记在**发生月**，冲减当月；已出账月份不回溯。
- 必须追溯的争议退款，开 `adjustment`（关联原账期、金额为负），在下一期账单体现，不修改已出账快照。
- 任务月末提交、次月结算：按结算行的时间归属；提交行 `quota=0` 不产生金额。

### 10.5 路由与查询参数

```
/billing                              当月（进行中）
/billing?period=2026-10               已出账账期
/billing?period=2026-10&tab=detail    直接进明细
/billing?period=2026-10&model=...&group=...   下钻后的明细
```

- 不设 `/billing/statements` 列表页：账期用下拉切换，`period` 缺省取当月。
- `period` 与 `tab` 必须可分享；服务端按 `period` 校验权限、状态与是否存在。
- 「下钻」= 汇总行点模型/分组 → 明细页签 + 对应筛选，URL 同步，刷新后仍在同一视图。
- 任意区间的实时流水不在本页：那是「用量日志」页的职责（§11）。

### 10.6 导出：一个 xlsx，三个工作表

一份账单文件，不做「先看汇总再切页签导明细」那套：

| 工作表 | 内容 | 客户版差异 |
|---|---|---|
| 账单信息 | 账期、周期、状态、出账时间、范围、币种、净费用、请求/失败/退款数、四类 token、综合单价、生成时间 | 少「内部 quota」行 |
| 汇总 | 模型 × 分组，逐组一行 + 合计 | 去掉分组与 quota 两列 |
| 明细 | 逐请求，按时间**正序**（月初到月末） | 14 列，无 quota / 分组 / 用户 / 倍率 |

为什么是 xlsx 而不是两个 CSV：

- 财务要的是"这一个月的账单"，一个文件才能转发、归档、作为邮件附件。
- 两个 CSV 要么让用户切页签分别下，要么打包 zip 再解压，都是把复杂度推给使用方。
- 多工作表是 Excel 的原生能力，汇总和明细在同一份文件里还能互相印证。

细节：

- 时间列必须是 `2026-10-31 22:59:57`，不能是界面上的 `10-31 22:59`——离开年份的文件没法归档。
- 明细**不跟随页面排序**：页面按时间倒序（最近在前），账单按正序（从头读）。
- 导出**不应用页面筛选**：模型/分组/搜索是查看辅助，账单范围由账期决定。按钮 tooltip 写明。
- 表头加粗、列宽按内容估算（中文按 2 字符），打开即读，不用再拖列宽。
- 当月允许导出，文件名带 `-draft`（`billing-statement-2026-11-draft.xlsx`）；出账后的正式文件不带后缀，两份可并存对比。
- 已出账账期**直接下载**出账时生成的文件（§11.6）；当月实时生成，受 `LOG_EXPORT_MAX_ROWS` 约束，超限提示"出账后可下载完整文件"。
- 页头显示 `归档文件 2026-11-01 00:10 生成 · 2,292 行明细 · 2.0 MB`，客户能核对"我拿到的就是账单"。
- 客户导出走 §9 白名单；代客对账（§9.7）强制 `customer` 投影。
- 前端原型为了离线可用，手写了最小 xlsx 生成器（zip + inlineStr + 一个加粗样式）；**真实实现用 Go 的 `excelize`**，共享字符串 + deflate 压缩，同规模文件约为原型的 1/5。

### 10.7 验收

- 同一账期重复出账不产生第二份（唯一键 + 状态机）。
- 出账后改汇率/倍率/分组倍率，账单金额不变。
- `Σ lines.amount == statements.total_amount`；页面汇总 = 同一批明细的 `GROUP BY` 结果。
- 账期边界（月初 00:00、月末 23:59:59）不漏不重。
- 已出账月不因新退款变化；调整单出现在下一期。

---

## 11. 与日志、看板的关系：数据解耦，展示复用

账单、用量日志、数据看板解决三个不同的问题，混用会同时毁掉三者：

| | 用量日志 | 数据看板 | 账单 |
|---|---|---|---|
| 回答 | 这次请求发生了什么 | 最近趋势与健康度 | 这个月该付多少 |
| 数据源 | `logs` 实时 | `logs` 实时聚合 | 当月读 `logs`，已出账读快照 |
| 时间 | 任意区间（默认 24h，上限 365 天） | 最近 N 天，滑动窗口 | 自然月，出账后冻结 |
| 金额 | `quota` 按**当前**汇率折算 | 累计用量，不冲减退款 | 出账时冻结的 `amount` |
| 留存 | 受 TTL 限制（建议 180–365 天） | 不落库，每次重算 | 3–5 年，独立留存 |
| 权限 | `log:read` / 本人 | 本人 | `billing:read` / 本人 |

### 11.1 解耦：账单不碰日志端点

不复用，也不得调用：

| 现有实现 | 为什么不能用 |
|---|---|
| `GET /api/log`、`GET /api/log/self` | 返回整行 `Log`，含 `channel`(id)、`ip`、`quota`、`other` 全部倍率 |
| `model.StreamUserLogs` / `ExportUserLogs` | `formatLogOtherJSON(user)` 只做**角色裁剪**，不是字段白名单，`model_ratio` / `billing_mode` 仍在 |
| `features/usage-logs/lib/export.ts` 的 `getLogCsvHeader/Rows` | 列里含 IP、Channel、Details，正是客户账单要剔的 |
| `usage-logs-table.tsx`、`common-logs-stats.tsx` | 通过 `UsageLogsContext` 与 `/_authenticated/usage-logs/$section` 取数，账单复用它们等于继承日志的路由参数与状态机 |
| `formatLogQuota` | 见 §11.4 |
| `SumUsedQuota` / `getUserQuotaDates` | 只统计 `type = 2`，退款不冲减；看板的"用量"是毛额，账单是净额 |

账单自己的端点（§9.5、§10.5），自己的表（§10.2），自己的金额字段。

### 11.2 复用：只复用无状态的东西

| 复用项 | 位置 | 用途 |
|---|---|---|
| `StaticDataTable` / `StaticDataTableColumn` | `components/data-table/static` | 汇总表与明细表骨架（`columns + data`，数据已在手） |
| `DataTablePagination` | `components/data-table/core` | 明细分页 |
| `DataTableColumnHeader` | 同上 | 排序表头 |
| `DataTableViewOptions` | `components/data-table/toolbar` | 运营侧的列显隐（如保留） |
| `toCsvLine` / `downloadCsv` | `features/usage-logs/lib/export.ts` | CSV 编码（含 `=+-@` 公式注入防护）与下载 |
| `formatTokens` / `formatUseTime` | `lib/format.ts` | token 紧凑显示、耗时 |
| `getCurrencyLabel` | `lib/currency.ts` | **只取币种符号**，不取汇率 |
| `EmptyState` / `ErrorState` / `Skeleton` | `components/` | 空态、错误态、载入态 |
| `Button` `Input` `Select` `Tabs` `Badge` `Card` `Tooltip` | `components/ui` | 控件 |
| `StatCard` | `features/dashboard/components/ui/stat-card.tsx` | 统计卡容器（可选；账单 3 张卡也可用 `Card` 直排） |

**允许的唯一跨 feature import 是 `features/usage-logs/lib/export.ts`。** 其余一律走 `@/components` 或 `@/lib`；`features/usage-logs/*` 的其他文件不进账单的依赖图。这条用 ESLint `no-restricted-imports` 钉住，避免以后有人"顺手复用一下"又把日志的上下文引进来。

### 11.3 判断标准

一句话：**纯函数与无状态组件复用，带上下文的组件与带语义的接口不复用。**

- `toCsvLine` 是纯函数 → 复用。
- `formatLogQuota` 读全局汇率 → 不复用。
- `StaticDataTable` 只吃 `columns + data` → 复用。
- `UsageLogsTable` 吃 `UsageLogsContext` → 不复用。
- `/api/billing/*` 是新语义 → 新写。
- `DataTablePagination` 只吃页码与回调 → 复用。

### 11.4 唯一必须新写：金额格式化

`formatLogQuota(quota)` → `formatQuotaWithCurrency` → `getCurrencyDisplay()`，读的是**运行期**的 `quotaPerUnit` 与汇率。用它渲染历史账单，管理员改一次汇率，三年前的账单金额就变了——这正好是账单最不能接受的事。

```ts
// features/billing/lib/format.ts
/** 金额已经是结算值，只做符号、千分位与小数位，不查任何全局配置。 */
export function formatStatementAmount(amount: string, currency: string): string {
  const value = Number(amount)
  if (!Number.isFinite(value)) return '-'
  return new Intl.NumberFormat(undefined, {
    style: 'currency',
    currency,
    minimumFractionDigits: 2,
    maximumFractionDigits: 4,
  }).format(value)
}
```

后端下发 `amount`（decimal 字符串，已含 `price_ratio`）与 `currency`；前端不做除法，也不做汇率换算。quota 只在运营视图作为内部字段出现，且同样来自快照。

### 11.5 后端边界

- 新增 `controller/billing.go` + `service/billing/`，与 `controller/log.go` 并列，不共享请求结构体。
- 出账任务查 `logs` 时写新方法（如 `SumStatementQuota`），**不要复用** `SumUsedQuota`：账单要 `type IN (2, 6)` 带符号累加，`SumUsedQuota` 只有 `type = 2`。
- 可复用的后端工具：`common.QuotaFromDecimalChecked`、`common.MaxQuota`、时区与 `setting.QuotaPerUnit`（只在出账那一刻读一次，落进 `pricing` 快照）。
- 出账任务里的重算校验（§7 第 1 级）与 `service/task_billing.go` 的 `RecalculateTaskQuota` 是两回事：前者校验文本行的倍率算术，后者重算任务额度，不要合并。

---

### 11.6 导出：出账时生成，下载不查库

**定案：账单的明细与汇总在出账时生成文件，导出就是下载文件；不做"全异步任务"。**

三个理由：

1. **账单有终点。** 账期是固定的，出账后一个字节都不再变，生成一次可以复用整个留存期（3–5 年）。
2. **全异步要一整套基础设施**：任务表、worker、轮询、失败重试、到期清理。这些成本是为"生成一次只用一次"的日志导出付的，账单不该跟着付。
3. **异步会让小导出变慢**：导 200 行本来 200ms 拿到，异步后要先建任务、等 worker、再轮询，体验倒退。

| 场景 | 生成时机 | 导出路径 | 行数上限 |
|---|---|---|---|
| 账单（已出账） | 出账任务最后一步 | 下载归档文件 | 无，文件已落盘 |
| 账单（当月 draft） | 点导出时实时生成 | 同步流式 | `LOG_EXPORT_MAX_ROWS`（默认 50 万） |
| 日志导出 | 点导出时实时生成 | 同步流式 | 同上 |

```sql
billing_statement_exports(
  statement_id,
  projection,                    -- admin | customer
  object_key, bytes, rows, sha256, created_at,
  UNIQUE(statement_id, projection)
)
```

`projection` 是必须的：同一账期、同一 kind 要存两份文件。代客导出取 `customer`，管理员导出取 `admin`，**绝不能让客服拿到 admin 文件**——那是 §9.7 说的"一次误发"。判定走服务端，不看前端参数。

**存储不必上对象存储。** 起步用本地磁盘：

```
data/exports/<period>/<statement_id>-<projection>.xlsx
```

nginx `internal` 直出即可，天然支持 `Range`、`ETag`、`gzip`。需要 CDN 或多实例共享时，表里只存 `object_key`，换 S3/MinIO 不用改业务逻辑。

**出账任务多这一步贵不贵**：不贵。出账本来就要遍历全月数据写 `statement_lines`，在同一个循环里顺手写工作表，增量成本就是一次磁盘顺序写。

**当月的边界**：当月没有终点，只能实时生成。撞上限时不说"失败"，说"出账后可下载完整文件"——这不是敷衍，是真实存在的兜底：月末最后几天的大客户确实会撞线，而下个月 1 号就有全量文件。

### 11.7 文件存储与下载链路（已落地）

账单文件的下载链路已经实现，可以独立验证，不依赖出账任务：

| 组件 | 位置 | 职责 |
|---|---|---|
| 存储 | `service/export_file.go` | 路径白名单、原子写、sha256、ETag |
| 凭据 | `service/export_file_access.go` | `<过期时间戳>.<签名>`，HMAC(CryptoSecret) |
| 端点 | `controller/export_file.go` | `GET /api/export/files/*key?access=...` |
| 注册 | `router/apiduo_routes.go` 的 `export-file` | 走自有注册表，不动上游路由文件 |

几个刻意的选择：

- **key 逐段白名单**，不是"拼完再查前缀"。每段只允许 `[A-Za-z0-9][A-Za-z0-9._-]*`、最多 3 段、拒绝 `.` 与 `..`；拼完再用 `filepath.Abs` 确认仍在根目录内。两道检查，防的是符号链接与将来有人改写拼接逻辑。
- **原子写**：先写同目录临时文件再 `rename`。中途失败不会留下半个文件——下载端拿到截断的账单比拿不到更糟。
- **签名绑定 key 与过期时间**，篡改 `exp` 而不重签等于签名失效。这一条有测试守着（`TestExportFileAccessBindsKeyAndExpiry`）。
- **普通用户只能凭签名下载**，即使他已登录：否则他用自己的会话就能猜别人的 key。管理员可以凭会话直接取，方便后台。
- **Range / ETag 交给 `http.ServeFile`**，几 MB 的 xlsx 支持断点续传，条件请求返回 304。
- 根目录由 `EXPORT_FILE_DIR` 指定，默认 `data/exports`；测试用 `t.TempDir()` 隔离，不碰仓库。

测试：`service/export_file_test.go` 覆盖穿越、原子性、凭据绑定与过期；`controller/export_file_test.go` 覆盖 403/400/200/206；`router/export_file_router_test.go` 确认它出现在注册表里。

路由进了 `accessTokenExemptRoutes`（带 `APIDUO:export-file` 标记）。理由写在标记块里：这个端点的凭据是 capability，不是 PAT scope；key 里没有 `user_id` 可对账，硬加一条 scope 等于让任何持有该 scope 的 token 去猜别人的文件名。

**目前没有调用方**：出账任务还没实现，所以还没有人往里写文件。这也是先做这条链路的原因——邮件链接、催收短信、后台下载都指向它，先把地基验证完，接什么都不会返工。

### 11.8 日志导出的现状与上限

日志没有终点：时间窗任意、每秒在变，没有"生成一次到处复用"的机会，所以只能同步生成 + 上限。

已落地三条（`controller/log.go`、`model/log.go`）：

| 改动 | 行为 |
|---|---|
| 列投影 | `streamLogs` 永久 `Omit("content")`。日志与账单都不含请求/响应体，这一列通常比其余所有列大两个数量级 |
| 行数上限 | `LOG_EXPORT_MAX_ROWS`（默认 50 万）。先 `CountLogsForExport` 再决定，超限直接拒绝并带上实际匹配数 |
| 并发闸门 | `logExportSlots` 容量 2（进程内；多实例时上限 = 2 × 实例数） |

拒绝时的文案是可操作的，不是一句"失败"：

```
log export matches 1437289 rows, above the 500000 row limit;
add a model or group filter, narrow the time range, or raise LOG_EXPORT_MAX_ROWS
```

**为什么不做"流到一半截断"**：半截文件看起来完整，对账时少一半数据也发现不了。宁可明确失败。

**为什么上限是行数而不是字节**：行数可数、可解释；字节要采样估算，反而不准。50 万行按每行 600 字节约 300MB，gzip 后 30MB 左右，是同步下载的上限。

**gzip 不在应用层做**：导出协议最后一行是 `c.JSON` 写的结果对象，套 `gzip.Writer` 会让它绕开压缩流、破坏尾部。交给 nginx：`gzip on; gzip_types application/x-ndjson application/json;`。

### 11.9 什么时候才真的需要异步任务

只有一种情况：**日志导出成为高频操作，且经常逼近上限**。这时才值得上任务表 + worker + 对象存储 + 签名链接。

判断信号（三个月内满足两条再动手）：

- 因超限被拒绝的导出每周超过 50 次；
- 平均导出耗时超过 30 秒；
- 用户开始用脚本分批调用。

在那之前，三条更便宜的手段够用：

1. **加筛选**（零成本）：`model_name` / `group` / `token_name` 都能砍一个数量级，拒绝文案已经这么提示。
2. **提高上限**（一个环境变量）：字段少、行短的部署可以把 50 万调到 200 万。
3. **ClickHouse 直出**（改查询与写出）：`SELECT ... FORMAT JSONEachRow` + `io.Copy`，省掉 GORM 的逐行 `ScanRows` 与 `Marshal`，CPU 降一个数量级。要绕过 GORM 拿底层 `sql.Rows`，先在压测环境验证。

**账单不走这条路**：预生成已经让下载与行数无关，再叠加异步只是把简单问题做复杂。

### 11.10 明细能不能直接跳 logs 页

**已出账不能跳；当月的直觉是对的，但也不建议跳。**

不能跳的五个理由：

1. **数据不在**：`logs` 有 TTL（建议 180–365 天），账单要留 3–5 年。三年后点"查看明细"会看到空表——而且是静默的空。
2. **字段会泄**：logs 页返回整行，带 `channel`、`ip`、倍率。要跳就得给 logs 加"客户模式"，那正是 §11.1 要避免的耦合：账单的字段白名单被日志页接管。
3. **口径不同**：账单金额是冻结值，logs 金额按当前汇率算。同一行在两个页面显示两个数字，客户会投诉，而且你没法解释哪个对。
4. **范围可改**：跳过去要传 `start/end`，用户能改；改了就不是这张账单了。
5. **总数对不上**：账单要"本账期共 2,292 行"，logs 页是"当前筛选的计数"，退款、失败、时区边界都会让两个数不等。

当月实时明细有两条路，选一条：

- **A（推荐）**：明细页签始终存在，**数据源随状态切换**——`draft` 读 `logs` 的投影查询，`issued` 读 `statement_lines`。界面完全一致，用户不需要知道数据来自哪。
- **B**：当月给一个"在用量日志中打开"的次级链接（新窗口 + 预填账期范围 + 只读），作为排障入口；账单页仍显示自己的汇总与明细。

不要做成"账单明细 = logs 页"：那等于把字段白名单、金额口径、账期边界全部外包给日志页维护，改一次日志页就要重新验一次账单。

---

## 12. 出账后的通知与催收

出账只是把数算完，还得让人知道。这里有两个不同的动作，不要合成一个：

| | 出账通知 | 催收提醒 |
|---|---|---|
| 时机 | 每月 1 日出账完成后 | 到期未付，按 T+3 / T+7 / T+15 递进 |
| 触发 | 出账任务状态变 `issued` | 定时任务扫 `due_at < now AND status = issued` |
| 语气 | 账单已生成，可下载 | 尚未结清，附截止日 |
| 渠道 | 邮件（主）、站内信 | 邮件 + 短信 |

### 12.1 投递表：先记录，再发送

```sql
billing_statement_deliveries(
  statement_id, channel,           -- email | sms
  target,                          -- 邮箱 / 手机号（脱敏后存储）
  target_hash,                     -- 用于唯一键与退订匹配
  status,                          -- pending | sent | failed | skipped
  attempts, last_error,
  sent_at, created_at,
  UNIQUE(statement_id, channel, target_hash)
)
```

- 出账任务**只写 pending 记录**，不直接发。发送是独立 worker 的事，出账任务不该因为 SMTP 超时而失败。
- 幂等靠唯一键：同一账单 × 渠道 × 收件人只发一次；重发是显式动作，写新记录。
- 失败指数退避（1min / 5min / 30min），三次后置 `failed` 并告警，不无限重试。

### 12.2 内容与附件

- **邮件**：主题 `2026-10 账单 · ¥2,480.62`，正文放四个数（净费用、请求、账期、截止日），**给签名下载链接**，不直接附文件。原因：几百万行的 xlsx 有几 MB，邮件网关普遍限制 10–25MB；链接可追踪打开、可设置有效期。
- **短信**：≤70 字，`【API多】您 10 月账单 ¥2,480.62 已出，10 日内结清：<短链>`。金额是否入短信是产品选择——放了催收更有效，不放更保守，建议放。
- 附件本身复用 `billing_statement_exports` 里的文件，**不重新生成**；链接只是指向它的一个签名 URL。

### 12.3 能复用什么

- 邮件：`common.SendEmail(subject, receiver, content)` 已存在，支持 SMTP 与 Resend。正文是纯文本，HTML 模板需要扩展（或拼好再传）。
- 短信：仓库里**没有**短信通道，需要接供应商（阿里云/腾讯云），要新增配置项、签名报备与限流。
- 短链：不必引服务，用 HMAC 签名 URL 即可：`/billing/statement/2026-10?t=<hmac>&e=<exp>`，7 天有效，服务端校验签名与过期。

### 12.4 合规与审计

- 短信必须可退订：单独的 `message_optouts(target_hash, channel, created_at)`，命中即 `skipped`。
- 邮件带 `List-Unsubscribe` 头。
- 每次发送记审计：操作者（自动/人工）、目标（脱敏）、内容哈希、供应商返回码。
- 手机号与邮箱在列表页脱敏展示（`138****1234`、`a***@b.com`），只有发送那一刻解密。

### 12.5 逾期与状态机

```
draft ──出账──▶ issued ──付款──▶ paid
                  │
                  ├─ 逾期（due_at < now）──▶ 催收提醒（不改 status）
                  └─ 作废 ──▶ void（必须开新单，不原地改数）
```

- `due_at` 在出账时按合同账期写入（月结 30 天 / 预付等），不要每次查询时算。
- **不自动停服**：停服是商业决策，不是技术动作。系统只负责提醒与记账，谁该停由运营决定。
- 催收不能改金额：调整走 §10.4 的调整单，进下一期。

### 12.6 分期

| 期 | 范围 |
|---|---|
| P2 | 邮件出账通知（含签名链接、退订、审计） |
| P2 | 逾期扫描 + 邮件提醒（T+3 / T+7 / T+15） |
| P3 | 短信通道（含报备、限流、退订） |
| P3 | 打开率/点击率统计、催收效果看板 |

---

## 13. 分期

| 期 | 范围 | 判据 |
|---|---|---|
| P0 | 账期页（汇总 + 明细 + 拆解抽屉）+ 整月 xlsx 导出；补 `pricing` 快照与 `endpoint` | 任取一行，能手算出金额，误差 ≤ 1 quota；xlsx 汇总合计 = 明细净额 = 页面金额 |
| P1 | 出账任务：幂等 + 重算断言 + `statement_lines` 独立留存 + 预生成导出文件（admin/customer 两份） | 同一账期重跑不产生第二份；差异 0 行；日志 TTL 到期后账单仍可复现；下载不查库；代客下载拿到的是 customer 文件 |
| P2 | 账期邮件 / PDF、跨月调整单、`billing_daily` 汇总表 | 调整单只进下一期，不修改已出账快照 |

---

## 14. 风险清单

1. 汇率变动 → 历史金额变形（必须有 `pricing` 快照）。
2. Anthropic 与 OpenAI 的 `prompt_tokens` 语义不同 → 展示层自行扣减会重复扣。
3. 固定价/任务行 token 为 0 → 混算综合单价会虚高。
4. 退款行 `quota` 为正 → 净额算成加法就翻倍。
5. 任务一次产生多行（提交 / 补扣 / 退款）→ 按行计数会把请求数算多。
6. ClickHouse 少写 `FINAL` → 重放批次让金额翻倍。
7. 按天分组不指定时区 → 每月 1 日凌晨错 8 小时。
8. `avg(avg)` 与 `avg(tps)` → 长请求被短请求稀释。
9. `quota` 饱和到 2^31−1 → 大额任务金额看起来偏小，需展示饱和标记。
10. 日志被关闭（`LogConsumeEnabled=false`）→ 账单空白但没有提示，需要页面级降级说明。
11. 客户导出复用原始日志 → 渠道、倍率、上游字段随 `other` 一起出去，而且每新增一个字段就重新泄漏一次；只能白名单投影。
12. 出账任务重跑或并发 → 同一账期出现两份账单（唯一键 `(period, user_id)` + 状态机，只有 `draft` 可重跑）。
13. 日志 TTL 短于账单留存 → 历史账单无法复现；`statement_lines` 必须独立落库，不能只存 `request_id` 回查。
14. 复用 `features/usage-logs/*` 的组件 → 账单被动继承日志的路由参数与 `UsageLogsContext`，一次"顺手复用"就把解耦抵消掉；用 lint 规则钉住。

---

## 15. 待确认

1. "综合单价"的分母用 `prompt+completion`（含缓存），还是"未缓存输入+输出"？原型按前者，与上游 `total_tokens` 对齐。
2. "日均"是否需要折算当天未过完的小时？原型不折算。
3. 失败请求是否进"单均"分母？原型不进，但计入请求数。
4. 普通用户是否可见渠道与成本价？原型默认隐藏，仅管理员可见。
5. 账单金额是"标价折算"还是"实付"？运营视图按 `quota ÷ 500,000 × 7.30` 展示标价；客户视图只显示结算金额（应含 `price_ratio`）。两者不能同时下发，需要确认客户合同价从哪个字段取。
6. 账期下拉的默认值：进页面看当月（进行中）还是最近一个已出账月？原型默认已出账月，因为对账发生在出账之后。
