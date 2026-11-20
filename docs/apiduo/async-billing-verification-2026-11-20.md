# 高级自定义异步计费 · 线上验证报告（2026-11-20）

结论：按次、按秒、按 token 三种形态在**线上二进制**上都按表达式结算，全部判据通过
（36 条断言：历史回放 28、正向实测 8 + 1 条守卫 + 3 条审计/资金）。验证中发现
**两个问题**：一个已修（表达式缓存空哈希串味），一个是既有设计取舍（结算补扣无余额下限）。

## 一、结论摘要

| 判据 | 结果 | 覆盖方式 |
|---|---|---|
| 按次：`u("count") * 0.10`，时长不影响价格 | PASS | 正向实测 2 笔（5s / 30s 同价） |
| 按秒：`u("seconds") * u("factor") * 0.01` | PASS | 正向实测 3 笔 + 历史 20/20 |
| 按 token：`tokens > 0 ? token : call` 真实结算 | PASS | 正向实测 2 笔（12 万 / 4.8 万 token） |
| 上游不报用量 → 回退按次 + 审计行 | PASS | 正向实测 1 笔 |
| 多 factor 相乘、未命中回落 default | PASS | 正向实测 2 笔 |
| 按秒缺 duration 且无 default → 400 并点名 `[seconds]` | PASS | 正向实测 1 笔 |
| 任务失败全额退款 | PASS | 正向实测 1 笔 |
| 历史任务终态 quota = 表达式(facts) | PASS | MySQL 快照回放 8/8 |
| 历史提交预扣 = 表达式(facts) | PASS | ClickHouse 回放 20/20 |

**真实上游消耗：$0。** 全部正向实测走本地 mock 上游（容器 `apiduo-billmock`，
已在 `apiduo-cn_default` 网络内，测试后删除）。历史回放读的是已发生的真实账单。

## 二、线上现状（盘点的第一手事实）

6 条 8001 渠道，其中 1 条禁用（#19）：

| 渠道 | 状态 | 模型数 | 计费形态 |
|---|---|---|---|
| 18 hailuo-video | 启用 | 4 | 按秒 + factor（$0.0104/s） |
| 19 hailuo-image-legacy | **禁用** | 1 | per_call |
| 28 qwen2api-video | 启用 | 2 | 按秒（$0.05/s，无 factor） |
| 29 hailuo-image | 启用 | 18 | per_call（走 `ModelPrice`，非表达式） |
| 37 aivideomaker2api-video | 启用 | 10 | 按秒 + factor（$0.01/s，default ×2） |
| 38 hailuo-video-fallback-2.0 | 启用 | 1 | 按秒 + factor（同 #18，priority −1） |

`billing_setting.billing_mode` 27 条、`billing_expr` 17 条，**全部是按秒或 fixed**：

- 按秒 16 条：`hailuo-*`（0.0104 / 0.0175）、`happyhorse*`（0.05，无 factor）、
  `minimaxH3`/`seedance*`/`t2v*`/`i2v*`/`ltx23`/`wan30`（0.01 + factor）
- 固定价 1 条：`flux-3-image`（`fixed(0.2)`）
- **按 token：0 条。按次表达式：0 条。**

⇒ 用户问的三种形态里，按次在线上是「渠道 legacy per_call + 全局 ModelPrice」，
按秒是唯一的表达式形态，按 token **有能力、无配置、无样本**。这也是本次要造
受控上游的原因：线上真实供应商不报 `usage.total_tokens`，token 档永远走不到。

## 三、历史账单回放（真实数据，零成本）

用 `pkg/billingexpr` 的生产求值器，把冻结在日志/任务快照里的表达式重新算一遍，
与真实扣费逐笔比对。

### 3.1 ClickHouse 提交行

口径：`type=2 && is_task && billing_mode=tiered_expr`，共 20 行，表达式取行内
`expr_b64`（历史版本，不是当前配置），事实取 `usage_facts`，`quota` 由
`expr × 500000 × group_ratio` 换算。

```
ch18 hailuo-2.3        total=2 match=2
ch18 hailuo-3.0-max    total=1 match=1
ch18 hailuo-video      total=7 match=7
ch18 hailuo-video-sec  total=1 match=1
ch37 minimaxH3         total=9 match=9
TOTAL total=20 match=20 mismatch=0 errors=0
```

覆盖面：factor `0.57 / 1 / 2 / 3.2`，seconds `5 / 6 / 10`，四种不同表达式形状
（含历史版本的 `u("count") * 0.1`、`fixed()`、token 三元式）。

### 3.2 MySQL 任务终态

口径：`tasks.private_data.billing_context.tiered_snapshot` 存在的 20 个任务，
取 `status=SUCCESS` 的 8 个（失败任务 quota 已被退款抹成 0，不构成计费判据），
用快照里的 `expr_string + usage_facts` 重算，与 `tasks.quota` 比对。

```
ch18 hailuo-3.0-max  total=1 match=1
ch18 hailuo-video    total=5 match=5
ch37 minimaxH3       total=2 match=2
TOTAL total=8 match=8 mismatch=0 errors=0
```

附带核对：20 个快照全部带 `expr_hash`，**没有一条缺哈希**（见 §5.1 的缺陷触发条件）。

## 四、正向实测（线上网关 + 受控上游）

临时对象：2 条渠道（`billverify-async` / `billverify-nousage`）、1 条失败渠道
（`billverify-fail`）、4 个模型、2 个用户 2 个令牌，全部 `billverify-*` / `billv-*`
命名，**只新增不修改现有行**，测后按名删除。

### 4.1 按次

| 用例 | 请求 | 期望 | 实测 |
|---|---|---|---|
| 5 秒 | `duration=5` | 50000（$0.10） | 50000 PASS |
| 30 秒 | `duration=30` | 50000 | 50000 PASS |

时长 6 倍差，金额不变 ⇒ 确认 `count` 是唯一维度、`seconds` 不参与。

### 4.2 按秒 + factor

| 用例 | 事实 | 期望 | 实测 |
|---|---|---|---|
| 5s × 720p(2) | seconds=5, factor=2 | 50000 | 50000 PASS |
| 2s × 480p(1) | seconds=2, factor=1 | 10000 | 10000 PASS |
| 5s × 未命中分辨率 | factor 回落 1 | 25000 | 25000 PASS |
| 5s × 720p(2) × high(1.5) | 两个 factor | 75000 | 75000 PASS |
| 5s × 720p(2) × 缺 quality | 第二 factor 回落 1 | 50000 | 50000 PASS |

多 factor **相乘**（2 × 1.5 = 3）成立，任一 factor 读不到或未命中都回落自身
default 而不阻断。`billv-fail`（无任何 factor 配置）5s 的 25000 也证明
`u("factor")` 在无配置时恒为 1。

### 4.3 按 token

表达式：`u("tokens") > 0 ? tier("token", u("tokens") * 10.0 / 1000000) : tier("call", u("count") * 0.20)`

mock 的 token 数由请求时长算出（`duration × 24000`），只出现在完成回包里：

| 用例 | 回包 tokens | 预扣 | 结算 | 结果 |
|---|---|---|---|---|
| 5 秒 | 120000 | 100000（call 档） | 600000（$1.20） | PASS |
| 2 秒 | 48000 | 100000 | 240000（$0.48） | PASS |
| 上游不带 usage | 无 | 100000 | 100000（$0.20） | PASS |

- 两笔 token 结算额与 `tokens × $10/1M` 精确相等 ⇒ **完成回包里的用量被真正读入并
  参与结算**，不是按预扣原值放过。
- 预扣按 call 档 100000、结算补扣 500000 ⇒ 差额结算（补扣）路径成立。
- 无 usage 那笔：结算停在 call 档，`type=4` 系统日志写
  `usage_fallback=tokens_missing`、`matched_tier=call`、`pre=100000/actual=100000`，
  `matched_tier` 在 type=2 行里是 `call` ⇒ 回退分支与审计行都对。

### 4.4 边界与失败

| 用例 | 判据 | 结果 |
|---|---|---|
| 按秒 + 无 duration + 渠道无 `default_seconds` | `400 model billv-second: the price expression needs usage facts this request does not provide [seconds]` | PASS |
| 上游返回 failed | 预扣 25000 全额退回，`tasks.quota=0`，用户/令牌余额回到测试前 | PASS |

### 4.5 资金守恒

`billverify-user` 起始 5,000,000；19 笔实测任务累计结算 2,375,000；
余额 2,625,000，`used_quota=2,375,000`，**逐位对上**。失败那笔 0 消耗。

## 五、发现

### 5.1 缺陷（已修）：空 `ExprHash` 让表达式在缓存里串味

`compileEntryFromCacheByHash(exprStr, hash)` 直接用 `hash` 作缓存 key。调用方若传空串
（`BillingSnapshot.ExprHash` 缺失，例如早期版本写入、缺字段导入的快照），
**所有空哈希调用共用 `cache[""]`**：第二个表达式会拿到第一个的编译产物，静默按别人的价格计费。

实测：`tier("a", count*0.1)` 与 `tier("b", count*0.9)` 都传 `hash=""`，
后者返回 0.1（应为 0.9）。

现状：产品两个构造点（`relay/helper/price.go:442`、`relay/relay_task.go:344`）都填了
哈希，线上 20 个快照也都有 ⇒ **未实际发生**。但整类风险只值一次 SHA-256。

修法（`pkg/billingexpr/compile.go`）：`hash == ""` 时自算 `ExprHashString(exprStr)`。
回归测试 `TestRunExprByEmptyHashDoesNotReuseAnotherExpression`，敏感性已验证：
去掉修复即红（0.1 vs 0.9）。

### 5.2 设计取舍（待定）：结算补扣没有余额下限

`WalletFunding.Settle(delta>0)` → `DecreaseUserQuota` → `quota = quota - delta`，
无下限判断；预扣走 `TryReserveUserQuota`（原子、余额不足即拒）。

实测：用户余额 120,000，提交按 token 任务（预扣 100,000），结算 600,000
⇒ 余额 **−480,000**，任务 SUCCESS，令牌 `status=4` 被自动禁用（后续请求 401）。

这意味着：
- 单笔可透支（上限 = 结算额 − 余额），不能连续透支（令牌随即失效）；
- 对按 token 尤其明显：预扣按 call 档 $0.20，结算可到 $12（600 秒 × 高 token 率），
  差额 60 倍；
- 若要收口，方向是提交时按「最大可能成本」预扣，或给补扣加下限并把差额记欠费 ——
  两者都要产品决定，本次未改。

## 六、清理与还原

删除对象：2 条验证渠道 + 1 条失败渠道、4 个 abilities 行、2 个用户、2 个令牌、
`billing_setting.billing_mode`/`billing_expr` 里新增的 5 个键、21 条测试任务及其日志、
mock 容器与 `/root/billmock`。

还原核对（与测试前第一手快照逐字比对）：

| 项 | 测试前 | 测试后 |
|---|---|---|
| `billing_mode` 键数 | 27 | 27 |
| `billing_expr` 键数 | 17 | 17 |
| `billing_expr` 值 | 17 条 | **逐字一致** |
| 8001 渠道 | 6 条（#19 禁用） | 6 条（#19 仍禁用） |
| `billv-*` 残留 | — | 0 |

全程未重启 `apiduo-cn`（`SYNC_FREQUENCY=15` 自同步），无中断。

## 七、未覆盖项（明确记录）

1. **线上真实供应商报 token 的路径**：`#18/#28/#38` 都配了 `total_tokens_path`，
   但历史上从未收到过用量（`usage_facts.tokens` 恒 0）。本次 token 结算验证只能靠
   受控上游。若要把按 token 用于真实视频渠道，需要先确认供应商回包确实带用量。
2. **`token_fallback_price`（渠道 legacy `per_token` + 回退价）**：线上无渠道启用，
   本次也未验；表达式计费不读该字段。
3. **并发重复结算**：同一 task 被两路轮询同时结算的竞态，需要并发工具，线上不便制造；
   仅有单测覆盖。
4. **渠道 #37 真实上游**：上游号池 429 严重（11-19 记录 6 次提交 1 次成功），
   未再触发真实任务，其按秒行为由历史回放覆盖（9/9 匹配）。

## 八、复现方法

历史回放（零成本，任何时间可跑）：

```sql
-- ClickHouse：提交行
SELECT model_name, channel_id, quota,
       JSONExtractString(other,'expr_b64') AS expr_b64,
       JSONExtractRaw(other,'usage_facts') AS usage_facts
FROM logs
WHERE JSONExtractString(other,'billing_mode')='tiered_expr'
  AND JSONExtractBool(other,'is_task') AND type=2;
```

把 `expr_b64` 解码、`usage_facts` 反序列化，塞进
`billingexpr.BillingSnapshot{ExprString, ExprHash, TaskUsageBilling: true, QuotaPerUnit: 500000}`
调 `ComputeTieredQuotaWithRequest`，与 `quota` 比。**`ExprHash` 必须填**（见 §5.1）。

正向实测：起一个返回 `{task_id}` / `{status,data[0].url,usage.total_tokens}` 的
最小 HTTP 服务，按 §4 的渠道 settings 建 8001 渠道，`billing_expr` 写入
`billing_setting.billing_expr`，等 15 秒缓存同步后提交。
