# 高级异步渠道（type=8001）· 计费配置说明

一条渠道可以给**每个模型**配不同的计费方式：按次、按秒、按 token。三者可以共存。

## 一、配置形状

渠道 `settings.advanced_async.billing`：

```json
{
  "billing": {
    "mode": "per_call",
    "models": {
      "seedance-1-5-pro-250528": "per_token",
      "seedance-2-0-fast-260128": "per_second"
    },
    "duration_path": ["duration"],
    "default_seconds": 5,
    "token_fallback_price": 0.5
  }
}
```

| 字段 | 作用 |
|---|---|
| `mode` | 默认模式，未在 `models` 里列出的模型都用它 |
| `models` | 按模型覆盖（模型名大小写不敏感，取客户端请求的名字） |
| `duration_path` | 从请求体里取时长的路径，默认 `["duration"]`，只服务 `per_second` |
| `default_seconds` | 取不到时长时的兜底秒数 |
| `token_fallback_price` | 按 token 的任务没拿到用量时，按这个**每次价**结算 |

同一个模型只能是一种模式：`models` 是**覆盖**，不是叠加。

## 二、三种模式

| 模式 | 计费依据 | 价格配置 | 预扣 | 完成时 |
|---|---|---|---|---|
| `per_call` | 与用量无关 | `ModelPrice`（每次价） | 价 × 1 | 不重算 |
| `per_second` | 请求里的时长 | `ModelPrice`（**每秒价**） | 价 × 秒数 | 不重算 |
| `per_token` | 上游返回的 token 数 | `ModelRatio`（**不能有 ModelPrice**） | ratio/2 × QuotaPerUnit | 有 tokens 就按 token 重算；没有则按 `token_fallback_price` 结算 |

`QuotaPerUnit` 默认 500000。

## 三、价格怎么配

```
按次：   ModelPrice[model] = 0.50          # 每次 0.5 美元
按秒：   ModelPrice[model] = 0.02          # 每秒 0.02 美元（不是每次！）
按 token：ModelRatio[model] = 0.1          # 且不要给该模型配 ModelPrice
```

⚠️ **按秒最容易配错**：`ModelPrice` 会被读成"一秒的价格"，写 0.02 表示每秒两分钱，5 秒就是 0.10。

⚠️ **按 token 的模型必须只配 `ModelRatio`**。只要该模型存在 `ModelPrice`，任务就会被标记为按次计费，完成时的 token 结算会被直接跳过。

## 四、计费公式

```
按次    quota = ModelPrice × QuotaPerUnit × groupRatio
按秒    quota = ModelPrice × duration × QuotaPerUnit × groupRatio
按 token quota = totalTokens × ModelRatio × groupRatio
```

按 token 的金额在任务**成功**时结算，失败全额退款；按次/按秒在提交时就定死，失败全额退款、成功不重算。

## 五、按 token 的上游要求

上游查询接口必须返回 token 数，并在渠道里配好路径：

```json
"fields": {
  "completion_tokens_path": ["usage", "completion_tokens"],
  "total_tokens_path": ["usage", "total_tokens"]
}
```

不配就完全不提取，任务停在预扣额度。

火山官方 Ark 的 Seedance 会返回 `usage.completion_tokens`；`aivideomaker` 兼容层**不返回**（任务视图恒为 `usage: null`），所以接 avm 只能用按次/按秒。

## 六、怎么验证生效

查任务记录与消费日志：

- 按次 → 日志 `Action generate`，无计费参数
- 按秒 → 日志 `Action generate, Billing parameters: seconds: 5.00`
- 按 token → 完成后再来一条 `Recalculated from tokens: tokens 100000, model ratio 0.10, ...`，
  且任务 `quota` 是重算后的值，不是预扣值

## 七、常见坑

1. **按秒价格写成每次价** —— 实际扣费会被放大到"秒数倍"。
2. **按 token 的模型残留了 `ModelPrice`** —— token 结算被静默跳过，按次收费。
3. **在 UI 里保存渠道会重置计费** —— 前端只保留它认识的字段；`types.ts` 里没有的 billing 字段会被丢掉（已修，新增字段记得两处同步）。
4. **`billing` 是渠道级配置** —— 一个渠道共用一套 `duration_path` / `default_seconds`，按模型只能切模式，不能切"取时长的路径"。
5. **按 token 的预扣是 ratio/2** —— 余额不足会在**提交阶段**就失败，不是等到结算。

## 八、按 token 拿不到用量时

有些上游不返回 `usage`（例如 `aivideomaker` 兼容层的任务视图恒为 `usage: null`）。
这时按 token 的任务会走回退：

```
配了 token_fallback_price  →  按该价结算（等价于按次）
没配                        →  保持预扣额度（ratio/2 × QuotaPerUnit）
```

回退金额 = `token_fallback_price × QuotaPerUnit × groupRatio`，groupRatio 用**提交时冻结**的那份
（含个人折扣），与预扣口径一致。

所以按 token 的渠道建议始终配一个 `token_fallback_price`，否则上游偶尔漏返回用量时，
用户会按 `ratio/2` 这种既不直观也不公平的额度被扣。
