# 耗尽类预警的诊断规格（交 analytics 并入看板文档）

> 本文从 `growth-automation-plays.md` 抽出。原因：它描述的是 `service/growth.go` 的 `BuildGrowthAlerts`，属看板侧实现，不是运营 play 的定义。运营侧只需要结论（未付费的耗尽不进队列、不发邮件），机制细节在这里。

## 一、一条更普遍的病

**预警不是事件，是状态。** 现有实现每次刷新重新判定，等于只写了进入条件、没写退出条件。所以同一条规则会以两种方式失败：

- **漏**：窗口滚出后静默消失
- **刷屏**：状态长期成立后天天重复

已经发现的两个实例都是前者：

| 判据 | 失败方式 |
|---|---|
| `quota > 0 && balance/daily < 3` | 余额归零那一刻预警消失 |
| `Quota7d > 0 && balance ≤ 0` | 烧完额度后第 7 天 `Quota7d` 滚出窗口归零，预警消失，直到第 30 天才以"沉默"回来——**23 天不可见，且队列里不留痕迹** |

共同点：**判据用滑动窗口内的存量，而隐患在窗口外的存量。**

## 二、判据

拆成两个 kind，不合并：

```
exhausted_blocked: balance ≤ 0 且 blocked_3d > 0
                   // 立即 P0，不等 3 天。他正在撞墙

exhausted_quiet:   balance ≤ 0
                   且 非沉默
                   且 blocked_3d = 0
                   且 last_active_at ≤ now − 3d
                   // 试过、被拦住、然后走了。流失前夜
```

共用：
- 价值闸门 `topup_total > 0 或 Qs ≥ $20 或 tier ∈ (K,A)`
- 去重 `last_touch_at = 0 或 last_touch_at < now − 7d`
- 排序 `预计损失 = max(0, prev7 − quota7) + (balance ≤ 0 ? 原速率 × 14d : 0)`，`severity` 退成兜底

**为什么不合并**：前者电话第一句是"我先给你开 50 块把这周的活干完"，后者是"上次卡在哪一步"。合并后只能写一句通用文案，而对两种人都是错的。

**"非沉默"的写法**：直接用 `last_active_at ≥ DayStart(-settings.SilentDays+1)`——这就是 `assignTiers` 判 S 层用的同一个表达式，取反即互补，结构性对齐、零新字段。

两个不要：
- **别改 `Quota30d` 的窗口参数**。它同时是展示指标和 `assignTiers` 的排序输入，把它绑到 `SilentDays` 上，等于让"月消耗"在运营调完沉默天数后悄悄变成 45 天消耗，所有报表跟着漂。
- **别用 `EffectiveTier() != Silent`**。人工覆盖层（`ManualTier`）会让一个手动设成 K 的僵尸账户绕过这个判据。

**窗口错位的成因**（说明为什么不能靠巧合）：

```
growth.go:62   d30 := DayStart(-29)                                    // 硬编码
growth.go:142  assignTiers(rows, DayStart(-settings.SilentDays+1), s)  // 走配置
```

- `silent_days = 45` → silent 窗到 −44，`Quota30d` 窗停在 −29：中间 15 天既不耗尽也不沉默
- `silent_days = 14` → 两集合重叠：同一账户既是沉默又是耗尽，两条规则同时派活

## 三、`blocked_3d` 是下界，不是计数

```
blocked_3d = count(logs where user_id = ?
                   and type = error
                   and created_at >= now − 3d
                   and error_code in ('insufficient_user_quota',
                                      'pre_consume_token_quota_failed'))
```

**一条路径根本不写日志**：`billing_session.go` 四个额度 403 出口都挂了 `types.ErrOptionWithNoRecordErrorLog()`，而 `RecordRelayErrorLog` 第一行就是 `if !constant.ErrorLogEnabled || !types.IsRecordErrorLog(err) { return }`。

全仓 6 个额度拒绝点，**4 个不写日志**：

| 位置 | 错误码 | 是否记日志 |
|---|---|---|
| `billing.go` | `InsufficientUserQuota` | 记 |
| `image_billing.go` | `InsufficientUserQuota` | 记 |
| `billing_session.go:223` | `PreConsumeTokenQuotaFailed` | **不记** |
| `billing_session.go:251` | `InsufficientUserQuota`（订阅） | **不记** |
| `billing_session.go:272` | `InsufficientUserQuota`（钱包预扣） | **不记** |
| `billing_session.go:323` | `PreConsumeTokenQuotaFailed` | **不记** |

如果 trial 用户走预扣/订阅路径，这批人对 `blocked_3d` 完全失明——而 24h 那 13 条 `insufficient_user_quota` 恰恰是 trial。

所以：
- 文案写「**≥N 次**被额度拦住」，不写「N 次」
- **上线前先验证**：拿 24h 那 13 条去反查 `logs` 能捞到几条。明显偏少就说明盲点在预扣/订阅路径，需在 403 出口补记录（去掉那个 option 即可），历史缺口接受、不回溯
- SQL 方言要分开：MySQL 用 `JSON_UNQUOTE(JSON_EXTRACT(other,'$.error_code'))`，ClickHouse 用 `JSONExtractString(other,'error_code')`。没有两边都通的 `->>`。等 `error_code` 落成列就统一了——这也是那个 DDL 值得优先做的理由之一

**同一个盲点也污染失败率**：`logs` 缺这些行，任何基于 `logs` 的"用户感知失败率"都会把被额度拦住的人算成健康——**他看起来比实际好**。补记录不只为 `blocked_3d`。

## 四、`switch` 顺序与防回归

现顺序 `incident → balance → decline → dormant`，`Exhausted*` 插在 `balance` 之前且早于 `decline`。

后果不是"分错类"那么轻：`decline` 分支没有 `balance` 约束，顺序写反时一个跑空两周的客户会先被判"渐退"进 P2，而 `switch` 互斥——他**永远不会**进 `exhausted`。结果是所有真流失客户在队列里都显示为"用量下滑"，运营看到的解释是被美化过的，**且数字看起来完全合理**。这比漏报更难查。

防回归只能靠表驱动测试：
- 每个 kind 一个 fixture
- 两个重叠 fixture：`exhausted + decline` 必须出 exhausted；`incident + exhausted` 必须出 incident

这两条是唯一能挡住下次重排的东西。单条规则的测试发现不了互斥失效。

## 五、附带产物：`wall_recovery_rate`

`blocked_3d` 顺手给出一个产品指标：

```
wall_recovery_rate = 撞墙后 48h 内有成功调用的人数 ÷ 撞墙人数
```

它是 `quota_wall`（错误响应体挂充值链接）唯一的效果度量。没有它，这个改动永远只能凭感觉说"提示余额有用"。建议进看板"质量与渠道"页，或作为批次表的对照组。
