# 判据映射表 — 8001 任务渠道的表达式计费（按次 / 按秒 / 按 token）

## 需求（逐字抄，不改写）

> 本地端到端测下 该服务更新的表达式计费是否生效 按秒按次按token

补充口径（用户 2026-11-19 决定，同日修订）：

> 第一点：回退到 按秒>按次
> → 22:26 复审后改为「直接回退到按次」

- 第一点 = 按 token 计价在上游不报用量时**直接退到按次**。理由：秒档要读
  `u("seconds")`，而该事实在请求没带 duration 时不存在；写成 `u("seconds") > 0`
  会直接 400 而不是退档，必须加 `!= nil` 才是安全的，那层保护是每个运营都要
  记得写的陷阱。少一档换取「表达式不可能因为缺字段而拒绝请求」。
  代价：不报用量时失去时长区分度（5s 与 30s 同价）。
- 第二点 = 轮询报失败不是终态；任务以终态为准，失败要二次确认才落终态并退款。

## 判据

单条命令：`python3 deploy/local/e2e/tiered_task_billing.py`
（退出码 0 = 全绿；失败退出 1；脚本逐条打印 PASS/FAIL）

| ID | 断言 | 期望 |
|---|---|---|
| C0 | 环境就绪 | `/api/status` success；mock 可达 |
| C1 | 保存表达式：按次 / 按秒 / 按 token（回退链）/ 无用量渠道 | PATCH `/api/option/model_pricing` 200 |
| C2 | 拒绝越界键：同模型写 `u("image_count")` | 4xx，且 message 提到 usage key |
| C3 | 按次：两次提交（duration 5 / 30） | quota 均 = 50000（$0.10 × 500000） |
| C4 | 按秒：5s×720p(factor 2) / 2s×480p(factor 1) | quota = 50000 / 10000 |
| C5 | 按 token：5s→120000 tokens / 2s→48000 tokens | quota = 600000 / 240000 |
| C6 | 老路径：无表达式 + 渠道 per_call + ModelPrice 0.01 | quota = 5000 |
| C7 | 资金：用户 quota 与令牌 remain_quota | 减少额 = 各任务 quota 之和（3770000 = 5000000−1230000） |
| C8 | 结算留痕：按 token 的任务记录预扣与实结 | pre_consumed=100000→actual=600000；100000→240000 |
| C9 | 面板展示（bsk 人工） | 模型广场搜 `tier-bill`：3 张卡显示「动态计费 / 特殊计费表达式」+ 原文；legacy 显示「按次计费 $0.01/请求」 |
| C10 | 老路径：渠道 `billing.models` 指定 per_second + ModelPrice 0.01 | 5s → quota = 25000 |
| C11 | 缺 duration 的**纯按秒**表达式 | 400 `model_price_error`，message 含 `seconds` 与 `usage facts` |
| C12 | 上游不报用量 → 按次退档 | 5s × $0.20 = quota 100000 |
| C13 | 退档后时长不参与计价 | 30s 同样 = quota 100000 |
| C14 | 退档留审计：系统日志行带 `usage_fallback` | 两条退档任务各一行 `tokens_missing`，预扣/实结均 100000；报用量的任务**没有**这类行 |

## 判据里写的标准表达式（按 token）

```
u("tokens") > 0
  ? tier("token", u("tokens") * 10.0 / 1000000)
  : tier("call", u("count") * 0.20)
```

不需要任何 `!= nil` 保护：`tokens` 与 `count` 在 8001 渠道上恒有值
（提交时 `tokens=0`），所以这条表达式不会因为请求缺字段而失败。
纯按秒表达式仍必须处理缺 duration —— 那是 C11 守的边界。

## 结果（2026-11-19）

- 16/16 全绿，退出码 0，日志 `.fixloop/logs/tier-bill-r10.log`（56 秒）。
  失败路径另验：`APIDUO_BASE=…:39999` → 退出 1。
- C14 的审计行实测（ClickHouse，type=4）：
  `{"usage_fallback":"tokens_missing","matched_tier":"call","pre_consumed_quota":100000,
  "actual_quota":100000,"usage_facts":{...,"tokens":0},"task_id":...}`，
  `quota=0`（它不是一笔扣费）。同时服务端 `[WARN] task … settled without usage tokens`。
- 首轮红（旧构建）：保存被拒 `400 ... expression references usage keys [count]
  but the model has no task plugin usage schema` —— 与改动要补的分支一致。
- C9 用 bsk 实测：三张卡显示表达式原文，`tier-bill-legacy` 显示「按次计费
  $0.01 / 请求」。
- **退档改变了预扣**：按 token 的模型现在在提交时按次档预扣（100000），结算时
  补差到 600000 / 240000。之前裸 token 表达式预扣为 0，所以结算日志的 `quota`
  字段等于实结；现在 `quota` 是差额，实结看 `actual_quota`。
- 三档版本（token > 秒 > 次）已实测通过过（r08），22:26 按「更简单」的复审
  意见改为两档；判据与用例同步改，未留死代码。

## 设计要点（为什么这样判）

- **两个数据点**：每种模式跑两次且输入的 duration/分辨率不同，才能区分
  「真的按该维度算」与「恰好等于某个常量」。
- **token 数由 mock 按 duration 换算**（24000 token/秒），只有真的读完成回包
  才会得到 120000 / 48000；不报用量的渠道回包干脆没有 `usage`。
- **C6 / C10 是回归哨兵**：新增 `ExtractUsageFacts` 不能让没有表达式、走
  legacy `billing.mode`（per_call / per_second）的模型改变价格。
- **C7 是钱**：`tasks.quota` 只是账本字段，余额才是扣费事实。
- **C11 是安全属性**：缺 duration 若按 0 秒计价，客户端只要不传 duration
  就能免费；纯按秒表达式必须拒绝，且要能指向缺的那个事实。
- **C12 / C13 是退档策略**：按 token 计价的模型在上游不报用量时不能变成免费；
  退到按次后时长不参与，两个不同 duration 同价即为证据。

## uncovered

- 面板**写**路径（模型定价弹窗里改表达式再保存）—— 判据走的是同一个
  `PATCH /api/option/model_pricing`，前端是否把表达式写进请求体不由本循环判定。
- **裸 token 表达式没有保存期拦截**：回退顺序目前是书写约定，不是强制。
  没写回退链的模型在不报用量的回包下仍会结算 0。
- 失败/取消任务的退款（表达式结算只在成功任务上生效，属既有行为）。
- 并发下同一用户多任务结算的余额一致性（任务并发提交，但额度扣减是串行的）。
- 除 `count/seconds/tokens/factor` 之外的未来事实键。

## 仓库闸门（CI 会跑，不在判据脚本内）

| 闸门 | 命令 | 结果 |
|---|---|---|
| 标记覆盖 | `go run ./tools/markercheck` | 通过；本轮新增 8 个上游文件的标记块 |
| 合并面预算 | `go run ./tools/markercheck -cost` | 与 `.apiduo/merge-budget.txt` 一致（183/555/5917/432 → 后来控制器测试再加 1 文件，见下） |
| 全仓单测 | `go test ./...` | 见下方记录 |

首跑时这两个闸门都是红的：8 个上游文件的新增行没有包进
`APIDUO:<feature>:BEGIN/END`，CI 的 `Marker coverage` 步骤会直接失败。
补块后预算上涨，按 `.apiduo/merge-budget.txt` 的规矩逐条登记了理由
（2026-11-19 那段，共 6 条）。

## 代码改动（本轮）

- `service/task_billing.go`：新增 `recordedUsageFallback`（表达式读 `tokens` 且
  合并后为 0 / 缺失）与 `recordUsageFallbackAudit`（系统日志行 +
  `usage_fallback=tokens_missing`）。`service/task_polling.go` 在结算分支调用，
  位置在 `RecalculateTaskQuota` 之前，所以行里能同时写到预扣与实结。
- `relay/relay_task.go`：表达式求值失败且存在「声明了但本次没有的事实」时，
  客户端错误点名缺失事实，原始引擎错误写服务端日志（C11）。
- `relay/relay_task_test.go`：`TestRealtimeFetchRefundsTaskItFindsFailed` 按
  「以任务终态为准」更新 —— 第一次轮询报失败只登记待确认、不动钱；第二次
  仍报失败才落终态并退款。
- `relay/channel/advancedcustomasync/adaptor_test.go`：回退用例改为两档
  （token / 次），并新增「缺 duration 也不失败」一条，改名
  `TestTokenFallbackToPerCallExpression`。
- 七份语言包补 `Task settled on the fallback tier: the vendor reported no usage tokens`
  （审计行的 `content_parts` 用它，面板按查看者语言渲染）。
- `controller/billing_option_test.go`：上游断言「无插件模型写 `u("seconds")`
  必须拒绝」，与新分支相反。改为放行内置事实键、仍拒绝越界键 —— 越界键保留
  `no task plugin usage schema`，聊天量不能混进任务计价。
- 未改计费金额语义。
