# 用户折扣 + 模型广场：对抗性测试报告

2026-11-08 · 真实二进制 + 假上游 + BrowserSkill（真实 Chrome）

## 一句话结论

计费链路是准的（5 组对照全部精确匹配），但**展示层有 3 个会误导用户的缺陷、审计有 1 个无法定责的缺口**。5 个全部修复并实测通过，其中「全部」视图选择了「折扣算进跨组最低价」。

## 测试方法

不用 mock，直接跑真实链路：

- 本地起真实二进制（SQLite），建 `root` / `vipuser`（vip 组，带折扣）/ `normaluser`（default 组，无折扣）
- 假 OpenAI 上游固定返回 `prompt_tokens=1000, completion_tokens=0`，`gpt-4o` 倍率设 1、vip 组倍率 0.9
- 每次请求前后读 `users.quota` 差值，反推实际扣费
- 前端用 BrowserSkill（真实 Chrome + 真实鼠标 + VOM），不用 puppeteer

这样每个数字都能手算核对：`quota = tokens × model_ratio × group_ratio(或折扣率)`。

## 通过项

| 用例 | 预期 | 实测 |
|---|---|---|
| 无折扣（default 组） | 1000 | **1000** |
| 折扣 0.7 | 700 | **700** |
| 折扣 0.8（`*` 兜底行） | 800 | **800** |
| 首条过期，回落第二条 | 800 | **800** |
| 全部过期，回落组倍率 0.9 | 900 | **900** |
| 改折扣 0.5 后立即生效 | 500 | **500** |

跨用户隔离（重点）：`/api/pricing` 的 `user_discount_ratios` 只有 vipuser 拿到，`normaluser` / `root` / 匿名都是 `null`。换账号登录后前端也没有串缓存——normaluser 看到 `$2`（default 组）和 `$1.8`（跨组最低），不是 vipuser 的 `$1`。

前端展示：卡片视图与表格视图数值完全一致（`$1.4 / 1.8 / -22%`，输出 `5.6 / 7.2`，缓存 `0.7 / 0.9`），划线是 `line-through`，折扣标是 10% 主色底。

抽屉：`2 / 20` 计数、`vip / gpt-4o / 7 折 / 永久` 回填、`default` 组显示「1.00 组价」作为对照、试算器实时、无「年/月/日」残影。改 5 折 → 保存 → 两条 PUT（`/api/user/` + `discounts`）都成功，其它 setting 保留。

## 缺陷（4 个）

### 1. 折扣率高于组倍率 → 静默涨价（已修）

vip 组倍率 0.9，管理员把「折扣」填成 0.95：

- 实际扣费 **950**，比无折扣的 900 贵 5.6%
- 前端 `getGroupDiscountInfo` 在 `effectiveRatio >= baseRatio` 时返回 `null`，**不显示任何标记**
- 但 `getDisplayGroupRatio` 优先返回 0.95，页面单价确实涨了

用户看到价格变贵、没有任何解释，账单来了才知道。这是四个里影响最直接的。

**修复**：不再返回 `null`，改为标记 `isIncrease`，渲染红色 `+6%` 并保留划线价（原价 1.8 → 现价 1.9）。

### 2. 默认「全部」视图不显示折扣（未修，需决策）

默认进入 `/pricing`（不带 group 参数）时显示 **$1.8**，而该用户实际扣费是 **$1.4**。

原因是「全部」展示跨组最低价，折扣无法归因到某一个组，代码里显式返回 `null`。设计上能自洽，但**用户默认就在这个视图**，看到的标价比真实账单贵 28%。

两种修法各有代价，都已实现并实测（对比页 `.workbuddy/artifacts/discount-test/compare.html`）：

| | 方案 A：叠加折扣 | 方案 B：价格不动 + 提示 |
|---|---|---|
| 标价 | $1.4（= 实付） | $1.8（贵 28%） |
| 改动 | `getDisplayGroupRatio` + `getGroupDiscountInfo` | 新增一个提示组件，价格逻辑不动 |
| 代价 | 「全部 = 跨组最低标准价」旧语义被改变 | 标价与账单不一致，只是被解释了 |
| 边界 | 最便宜的组本身无折扣时不画划线价 | 选中分组时提示自动消失 |

**最终采用方案 A。** 理由：默认视图就是「全部」，B 等于让所有人在最常用入口看到贵 22% 的价；而「点进去才发现更便宜」的流失不会出现在任何日志里。方案 B 的实现已回退（`pricing/index.tsx` 回到上游原样）。

方案 A 顺带解决了一件事：折扣配错（rate 高于组倍率）时页面会显示红色 `+X%`；方案 B 下这个错误完全静默。

### 3. 免费模型显示「$0 ~~0~~ -44%」（已修）

`model_ratio=0` 的模型，基准价为 0，但倍率比较仍然成立（0.5 < 0.9），于是三列都显示：

```
$0 / 1M    $0 / 1M    $0 / 1M
0 -44%     0 -44%     0 -44%
```

划掉的价和现价都是 0，却标着降价 44%，纯噪音。

**修复**：基准价为 0 时返回 `null`，不渲染任何折扣元素（实测 `strikethrough: 0, pct: 0`）。

### 4. 审计无法追溯「改成了什么」（已修）

修改前，一条折扣审计长这样：

```json
{"action": "generic", "params": {"method": "PUT", "route": "/api/user/:id/discounts"}}
```

对比同一张表里 `option.update` 带 `key`、`token.create` 带 `name`：折扣这条只有路由模板，**没有用户 ID、没有改动内容**。审计页也只会显示 `PUT /api/user/:id/discounts`，而旁边都是「更新用户 vipuser（ID: 2）」这种自然语言。

对「谁能给谁降价」这种资金敏感操作，等于只记了「有人点了保存」。

**修复**：接口内手动埋点（跳过通用兜底），记录完整快照：

```json
{
  "action": "user.discount_update",
  "params": {
    "id": 2, "username": "vipuser",
    "before": [{"groups":"vip","models":"gpt-4o","rate":0.5},
               {"groups":"vip","models":"*","rate":0.8}],
    "after":  [{"groups":"vip","models":"gpt-4o","rate":0.6}]
  }
}
```

审计页显示「更新用户 vipuser（ID: 2）的模型折扣」。这条记录还顺带证明了 PUT 是**全量覆盖**语义：一次调用把两条规则替换成一条。

## 5. 越权尝试不留痕（已修）

`normaluser` 对 `PUT /api/user/2/discounts` 发请求返回 403，审计表**一条记录都没有**（21 → 21）。

原因是 `beginAdminAudit` 挂在 AdminAuth 通过之后，鉴权失败时中间件根本没进入。这是通用机制问题，不只折扣接口——但折扣是资金相关，越权探测无痕值得单独关注。

**修复**：在 `authHelper` 的角色校验失败分支补埋点，记 `action=access.denied`、`category=security`：

```json
{"action":"access.denied","category":"security","username":"normaluser",
 "params":{"method":"PUT","route":"/api/user/:id/discounts","role":1,"required":10}}
```

**必须节流**，否则一个账号循环打管理接口就能把审计表灌满。按「用户 × 方法 × 路由」每分钟一条，重复次数累加到 `repeated`。实测：首次 403 落 1 条，立刻重复不再写。

401（未登录 / 令牌无效）不记：没有可信身份，记下来只是无法归因的 IP，且极易被刷。

## 顺带发现（与折扣无关）

- `performance_setting` 的监控开关改了只落库、不生效，`UpdateAndSync` 在配置加载后没有被调用，必须重启才生效
- 本地数据卷 97%，磁盘过载保护会拦截所有 `/v1` 请求；测试时用 RAM 盘做 `$TMPDIR` 绕过（探测路径是 `os.TempDir()`）
- 登录接口 429：`CriticalRateLimit` 把登录和 `auth/refresh` 记在同一个 `"CT"+ClientIP` 计数器上，反复登录会被自己打满

## 修复验证

| 项 | 结果 |
|---|---|
| 涨价场景 | `$1.9` + 划线 `1.8` + 红色 `+6%`，computed color `rgb(228,0,20)` |
| 免费模型 | 无 `<s>`、无百分比标签 |
| 正常折扣回归 | `$1.4 / 1.8 / -22%` 不变 |
| 前端测试 | 15 文件 / **343 个**全过 |
| 后端 | `controller` + `discount` 包测试通过，`go vet` 干净 |
| 越权留痕 | 403 落 1 条 `access.denied`，重复请求被节流（42 → 42） |
| 合并面 | 97 文件 / 273 块 / +2584 行，已在 `merge-budget.txt` 登记 |

## 未覆盖

- 并发写：PUT 没有 `expected_version`，两个管理员同时改同一用户是「后写覆盖」，未做并发压测
- 动态定价（`billing_expr`）模型：前端不显示折扣，后端是否应用未单独验证
- 多实例部署下的定价缓存一致性（`pricing_version` 是硬编码常量，不含用户维度）
