# 定价维度正交化设计

> 目标：把「模型渠道 / 模型厂商 / 模型 / 限时折扣 / 用户 / 用户分组」从一张互相耦合的矩阵，
> 拆成四个各自独立维护的维度，让运营动作变成加法而不是乘法。
>
> 状态：设计稿。未写代码。
> 适用：apiduo（`apiduo/apiduo`），基于上游 `QuantumNous/new-api`。

---

## 0. 一句话结论

`group` 这个字符串同时承担了六种语义，其中「折扣」是唯一的乘法维度，且是 N×M 矩阵。
分组数量本身不是问题，**分组之间的关系数量**才是。把折扣从 `group` 里剥离成带时间窗的策略实体，
`group` 退回纯标签，配置复杂度从 O(模型 × 池 × 组 × 促销) 降到 O(模型) + O(池) + O(策略) + O(组)。

### 配套原型

| 文件 | 看什么 |
|---|---|
| [demo-tradeoff.html](./demo-tradeoff.html) | **先看这个。** 六个语义的定性判据 + 复杂度模拟器（拨到你的真实规模，看结论怎么变）+ 按风险排的动作顺序 |
| [demo-group-health.html](./demo-group-health.html) | P0 分组体检的实际交互：展开看五个位置的存留状态、删除影响面、模板新建、批量归档 |

### 三个必须先想清楚的问题

1. **六项里只有一项要拆。** 判据是「在请求路径上参与算术，还是只做查找」。
   折扣参与乘法，是唯一的 N×M；其余五项是布尔判断或字段读写，配置量都是 O(N)。不要因为图画得对称就以为是六个问题。
2. **按风险排顺序，不是按重要性。** 前三步（体检 / `expires_at` / UI 隐藏矩阵）零风险，做完观察一个月，
   很可能第四步就不必要了。
3. **`group` 的正确定义只有一句话**：一个名字 = 一个路由池 + 一个资格集。
   折扣、订阅、统计都只是"恰好引用了这个名字"。任何新功能想往上加语义，先问它是路由还是资格。

---

## 1. 现状证据

全部来自本仓库代码，可直接核对。

### 1.1 同一个字符串，六种语义

| 语义 | 载体 | 证据 |
|---|---|---|
| 路由池 | `channel.group` 单值 + `ability(group, model, channel_id)` 三元组主键 | `model/channel.go:41`、`model/ability.go:18-26` |
| 折扣 | `group_ratio[group]` + `group_group_ratio[userGroup][usingGroup]` | `setting/ratio_setting/group_ratio.go:18-26` |
| 准入 | `user_usable_groups` + `group_special_usable_group`（`+:` / `-:` 前缀语法） | `setting/user_usable_group.go:13`、`service/group.go:17-34` |
| 令牌范围 | `token.group`、`auto_groups`、`max_token_auto_groups` | `model/token.go:29`、`setting/auto_group.go` |
| 订阅目标 | `plan.upgrade_group` 直接改 `user.group` | `model/subscription.go:176-179`、`270-274` |
| 统计 | `log.group`、`quota_data.use_group` | `model/log.go:74`、`model/usedata.go:19` |

### 1.2 折扣是矩阵，不是标量

```go
// setting/ratio_setting/group_ratio.go:88
func GetGroupGroupRatio(userGroup, usingGroup string) (float64, bool)
```

`groupGroupRatioMap` 的类型是 `map[string]map[string]float64`。
运营维护的不是 N 个分组，而是 N × M 个格子。加一个用户档要填 M 个格子，加一个渠道池要填 N 个格子。

默认值里已经有 `"vip": {"edit_this": 0.9}` —— `edit_this` 这个占位 key 本身就说明了这张矩阵没人愿意填。

更直接的证据：上游自己在配置表单里放了这句提示

```
web/src/features/system-settings/models/group-ratio-form.tsx:556
"Common pitfall: the user group base ratio is NOT a personal discount.
 It only applies when the user group itself is the billing group."
```

需要专门写一句话告诉运营「这个倍率不是折扣」，就说明这个概念已经绕到无法自解释。
`group_ratio[g]`（分组基准价）和 `group_group_ratio[ug][g]`（用户组×使用组）是两套语义、
两处配置、用同一个 `group` 命名空间命名，UI 只能靠文案补救。这是设计问题，不是文档问题。

### 1.3 促销没有时间维度

`billingexpr` 支持 `hour(tz)` / `minute(tz)` / `weekday(tz)` / `month(tz)` / `day(tz)`
（`pkg/billingexpr/expr.md:175-179`），技术上能表达时间。

但把它写进 `ModelBillingExpr` 意味着：

- 改一次促销要动所有相关模型的表达式
- 促销结束要逐个回滚，漏一个就是长期错价
- 表达式成为价格和营销的混合体，`expr_smoke_test` 无法区分「价格错」和「活动到期」

所以现实中运营只能新建 `vip_promo_618` 这类分组，再让用户进组、配矩阵、活动后移回。
**促销会结束，分组不会消失**，数量单调递增。

### 1.4 渠道组是单值

```go
// model/channel.go:41
Group string `json:"group" gorm:"type:varchar(64);default:'default'"`
```

一条上游只能进一个池。要让 vip 用户和普通用户共用同一条渠道（同一份额度、同一份限流、同一份故障域），
只能建两条重复渠道。这是分组数量被动增长的另一条路径。

### 1.5 没有成本，算不出毛利

`model/channel.go` 全文没有任何 `cost` / `Cost` 字段。
`model/vendor_meta.go` 的 `Vendor` 只在 `model/pricing.go:updatePricing()` 里用于展示
（`Description` / `Icon`），不参与任何计费计算。

结果：系统知道收了多少，不知道赚了多少。定价只能靠感觉。

---

## 2. 根因

计费本质是一个多元函数：

```
charge = base(model, usage) × price_factor(user, group, campaign, t)
cost   = base(model, usage) × channel_cost(channel)
profit = charge - cost
```

`base` 已经有正确答案（`billing_expr`，模型级、唯一真值、版本化）。
问题出在 `price_factor`：它被塞进了 `group` 这一个字符串，并且用「分组 × 分组」的矩阵相乘表达。

于是：

1. **维度耦合**：增加一个营销概念（促销、大客户、内测白名单），必须新增一个路由池。
2. **关系爆炸**：矩阵是 N×M，配置成本随分组数平方增长。
3. **不可逆**：没有 `ends_at`，所有临时概念永久沉淀。
4. **不可算**：没有成本，定价决策缺少基本约束。
5. **不可审计**：一笔消费只能看到最终 `group_ratio`，看不到"为什么是这个倍率"。

---

## 3. 目标架构

三个乘数拆开，且**成本不参与对外定价**。

```
对外计费 = base(model, usage) × discount(policy, ctx)
渠道成本 = base(model, usage) × cost_expr(channel)
毛利     = 对外计费 − 渠道成本
```

`group` 的新职责只有两条：

- 走哪个渠道池（路由）
- 能不能用某个模型（准入）

它不再是任何乘数。

### 3.1 四个维度的归属

| 维度 | 谁维护 | 变化频率 | 数量级 |
|---|---|---|---|
| 模型定价 `base` | 产品/定价 | 低（周） | 模型数 |
| 渠道成本 `cost` | 采购/运维 | 中（月） | 渠道数 |
| 折扣策略 `discount` | 运营 | 高（天） | 活跃策略 < 20 |
| 分组 `group` | 运营 | 低 | 池数 + 用户档数 |

关键：**四个维度各自独立变更，不需要交叉对齐。**
现在「给 vip 用户在做 618 期间用 pro 池打 8 折」要动 1 个分组 + 2 个矩阵条目；
之后是 1 条策略，删掉即结束。

---

## 4. 数据模型

### 4.1 折扣策略

```go
// 新文件。建议放 model/discount_policy.go
type DiscountPolicy struct {
    Id       int
    Name     string `gorm:"size:128;not null"`   // "618 大促 · 全站 9 折"
    Status   int    `gorm:"default:1;index"`     // 1 启用 / 0 停用

    // 谁
    ScopeType  string `gorm:"size:16;not null;index:idx_policy_scope"` // all|user|user_group|user_tag
    ScopeValue string `gorm:"size:64;not null;default:'';index:idx_policy_scope"`

    // 对什么
    TargetType  string `gorm:"size:16;not null;index:idx_policy_target"` // all|vendor|model|model_prefix|pool
    TargetValue string `gorm:"size:128;not null;default:'';index:idx_policy_target"`

    // 多少
    ValueType string  `gorm:"size:16;not null"` // rate | amount_off
    Value     float64 `gorm:"not null"`         // rate: 0.8 = 八折；amount_off: USD/请求

    // 何时（限时）
    StartsAt int64 `gorm:"bigint;not null;index:idx_policy_window"`
    EndsAt   int64 `gorm:"bigint;not null;index:idx_policy_window"` // NOT NULL，无「永久」

    // 冲突
    Priority  int  `gorm:"default:0"`
    Stackable bool `gorm:"default:false"`
    MaxStack  int  `gorm:"default:1"` // 仅 Stackable 时生效，硬上限 3

    // 预算（防薅）
    BudgetCap  float64 `gorm:"default:0"` // 0 = 不限，但 UI 强制填写
    PerUserCap float64 `gorm:"default:0"`
    UsedAmount float64 `gorm:"default:0"`

    // 互斥
    ExcludeSubscription bool `gorm:"default:false"`

    CreatedBy int
    CreatedAt int64
    UpdatedAt int64
}
```

约束（应用层 + DB 双保险）：

- `EndsAt > StartsAt`，且 `EndsAt - StartsAt <= 366 天`（防止"变相永久"）
- `ValueType = rate` 时 `0 < Value <= 1`；`amount_off` 时 `Value >= 0`
- `Stackable = true` 时 `MaxStack` 强制落在 1..3
- 同 `(ScopeType, ScopeValue, TargetType, TargetValue)` 的活跃策略数量 ≤ 3，超出时创建接口拒绝并提示合并

### 4.2 用户标签（替代"多分组"）

```go
type UserTag struct {
    UserId int    `gorm:"primaryKey;autoIncrement:false;index"`
    Tag    string `gorm:"primaryKey;type:varchar(64);size:64"`
}
```

用途：一个用户既是 `vip` 又在 `beta_tester` 名单里，这是**多归属**，不是多分组。
现在只能二选一，或者造 `vip_beta` 这种组合分组 —— 组合数随 tag 数指数增长。
tag 只作用于 `ScopeType = user_tag` 的策略，不影响路由。

### 4.2.1 个人特殊折扣：必须存在 users 表，不能是独立表

```go
// users 表新增列（标记块，见第 9 节）
Discount string `json:"discount,omitempty" gorm:"type:text"`

// ToBaseUser() 里 Unmarshal 成结构体，随用户缓存一起下发
type UserDiscount struct {
    Rate      float64 `json:"rate"`       // 0.7 = 七折
    AmountOff float64 `json:"amount_off"` // USD / 请求
    Models    string  `json:"models"`     // "" = 全部，支持 "gpt-*" 前缀
    Pools     string  `json:"pools"`
    EndsAt    int64   `json:"ends_at"`    // 0 = 不过期
    Reason    string  `json:"reason"`     // "合同价 #1234"，审计用
    GrantedBy int     `json:"granted_by"`
}
```

**为什么不是独立表 `user_discount_grants`：**

`PublishUserAuthCache`（`model/user_auth_cache.go:238`）的实现是
`GetUserById` → `updateUserCache` → 再 `GetUserById` 校验，循环 3 次。
它**只认 users 表的字段**。折扣放独立表，这个刷新通道带不上它，
必须改上游函数（违反解耦规范），且刷新后缓存里的折扣仍是旧值。

放 users 表则：

- `ToBaseUser()` 加一行映射，刷新机制天然复用
- 与用户改名/改密/改分组的更新在同一事务，不会出现"分组更新了折扣没更新"
- 用户删除/迁移天然跟随

**读路径为什么零成本：**

`common.RedisHGetObj`（`common/redis.go:167`）是 `HGetAll` + 反射反序列化整个结构体。
往 `UserBase` 加字段**不增加任何一次 Redis 往返**，只是反序列化时多填一个字段。

对比现有代价：

| 环节 | 现在 | 新设计 |
|---|---|---|
| 用户缓存 | `HGETALL` × 1 | 同一次 |
| 折扣查找 | `GetGroupRatio` 1 次 map + `GetGroupGroupRatio` 2 次嵌套 map | 3 次扁平 map / 字段读 |
| 数据库 | 0 | 0 |

新设计去掉了嵌套 map 的第二层查找，**数量级相同、常数更小**。
这是"不影响性能"唯一站得住的论证方式：不是承诺，是可比对的成本结构。

### 4.3 渠道池（多对多）

```go
type ChannelPool struct {
    Name string `gorm:"primaryKey;type:varchar(64)"`
    Kind int    `gorm:"default:0"` // 0 = 路由池，1 = 仅用于策略定位
}

type ChannelPoolMember struct {
    Pool      string `gorm:"primaryKey;type:varchar(64)"`
    ChannelId int    `gorm:"primaryKey;autoIncrement:false;index"`
    Priority  int64  `gorm:"default:0"`
    Weight    uint   `gorm:"default:0"`
    Enabled   bool   `gorm:"default:true"`
}
```

迁移期双向：读 `ability.group` 优先，`channel_pool_members` 存在时以它为准。
`Channel.Group` 字段保留不删（上游文件不改内容，符合解耦规范 v2 第 1 条）。

### 4.4 渠道成本

```go
// Channel 结构体新增字段（属于标记块内的增补，见第 9 节）
CostExpr      string  `json:"cost_expr,omitempty" gorm:"type:text"`
CostRatio     float64 `json:"cost_ratio" gorm:"default:0"` // 与 CostExpr 二选一
CostCurrency  string  `json:"cost_currency" gorm:"type:varchar(8);default:'USD'"`
CostUpdatedAt int64   `json:"cost_updated_at" gorm:"bigint;default:0"`
```

- `CostExpr` 复用 `pkg/billingexpr`，与 `ModelBillingExpr` 同语法、同编译缓存，不造第二套引擎。
- 解析顺序：`CostExpr` 非空则用它，否则 `base × CostRatio`。
- **安全边界**：`CostExpr` / `CostRatio` / `CostCurrency` 绝不出现在
  `GET /api/pricing`（`controller/pricing.go:GetPricing`）与任何非 admin 响应中。
  这是硬约束，需要在 handler 层显式剥离，不能依赖前端不展示。

### 4.5 定价排期（P3，可选）

```go
type PricingSchedule struct {
    Id          int
    ModelName   string `gorm:"size:128;not null;index"`
    Expr        string `gorm:"type:text;not null"`
    Mode        string `gorm:"size:16;default:'tiered_expr'"`
    EffectiveAt int64  `gorm:"bigint;not null;index"`
    Status      int    `gorm:"default:0"` // 0 待生效 / 1 已应用 / 2 已取消
    CreatedBy   int
}
```

解决"涨价要半夜改配置"。后台按 `EffectiveAt` 轮转应用，每次应用写审计日志。

---

## 5. 解析算法

### 5.1 伪代码

```
resolve(user, model, vendor, pool, now) -> DiscountResult

  // 1. 取候选：把所有过滤条件下推到 SQL
  rows = SELECT * FROM discount_policies
         WHERE status = 1
           AND starts_at <= now AND ends_at > now
           AND scope matches (user.id | user.group | user.tags | all)
           AND target matches (model | model_prefix | vendor | pool | all)
         ORDER BY target_specificity DESC, priority DESC, id ASC

  // 2. 硬规则：具体优先于宽泛，priority 只在同 specificity 内比较
  //    specificity: model=4, model_prefix=3, vendor=2, pool=1, all=0

  // 3. 第一条非 Stackable 定案
  primary = first row where !stackable
  if primary == nil { primary = rows[0] }

  // 4. 叠加层：仅显式声明 stackable 的，最多 MaxStack-1 层
  applied = [primary]
  extra   = 1.0
  for row in rows after primary:
      if len(applied) >= primary.MaxStack { break }
      if !row.stackable { continue }
      if exceeded(row) { continue }          // 预算检查
      extra *= row.Value; applied.append(row)

  // 5. rate 先乘，amount_off 后减（顺序固定，不可配置）
  rate      = primary.ValueType == "rate" ? primary.Value : 1.0
  amountOff = primary.ValueType == "amount_off" ? primary.Value : 0.0
  rate *= extra

  return { rate, amountOff, applied }
```

### 5.2 为什么是这三条

**具体优先于宽泛是硬规则，不是 priority 的子集。**
反过来（priority 优先）会导致"全场 9 折"（priority=100）盖掉"GPT-4o 8 折"（priority=10），
运营每加一次全站活动就要去把历史策略的 priority 全部上调。这是现在矩阵最难维护的同一个病。

**同层级只取一条定案，不相乘。**
相乘是现在 `group_ratio × group_group_ratio` 的模式，再加订阅就是三层。
三层乘法之后运营无法心算，也无法向用户解释。一条主策略 + 显式声明的最多 2 层叠加，
是"能人肉验算"的上限。

**rate 先、amount_off 后，顺序写死。**
乘法可交换，打折和满减不可交换。`100 × 0.8 − 5 = 75`，`(100 − 5) × 0.8 = 76`。
顺序必须写进代码注释、运营文档、财务对账口径三处，且不可配置。

### 5.3 预算扣减

预算在**结算**时扣，不在预扣时扣。

- 预扣按 `rate` 全额算（宁可多扣，不能少扣）
- 结算时 `UsedAmount += 实际让利`，若超 `BudgetCap` 则本次仍按策略价结算（已经承诺给用户），
  但策略置 `Status = 2`（预算耗尽），下一请求不再命中
- 原子性：`UPDATE discount_policies SET used_amount = used_amount + ?
  WHERE id = ? AND (budget_cap = 0 OR used_amount + ? <= budget_cap)`，
  影响行数为 0 说明已耗尽

不要用"先查后写"，并发下会把预算打穿。

### 5.4 时间基准

**必须用请求开始时刻的快照，不能用结算时的 `time.Now()`。**

长流式请求、异步任务（`model/task.go` 有 `Group` 字段用于修正计费）会跨越促销边界。
`pkg/billingexpr/types.go` 的 `BillingSnapshot` 已有冻结机制，
新增 `DiscountSnapshot{ PolicyIds, Rate, AmountOff, ResolvedAt }` 挂在同一处。

这条不是优化，是正确性要求。实现时如果有人图省事用 `time.Now()`，会出现：
同一笔请求预扣 8 折、结算 9 折，账对不上，且只在跨零点时偶发。

### 5.5 缓存

热路径必须缓存，键为 `(userId, modelName, poolName, 时间片)`。

- 时间片：按策略的最近 `EndsAt` / `StartsAt` 切分，不用固定 TTL
- 失效：策略表 `version` 单调递增（类似现有 `ModelPricingVersion` 的 sha256 思路），
  缓存键带上 version，写操作 bump version 即全量失效
- 容量：LRU，上限按在线用户数 × 活跃模型数估算，别用 map 无界增长

**这是本方案唯一的高风险点**：缓存写错就是全站错价，且错得很隐蔽（部分用户贵、部分便宜）。
必须有对拍兜底，见 7.2。

### 5.6 公开价格展示

`controller/pricing.go` 的 `GetPricing` 目前返回静态价格（`ModelRatio` / `ModelPrice` / `BillingExpr`）。
加了限时折扣后，模型广场需要展示「原价 + 现价 + 截止时间」，否则促销用户感知不到。

但这里有个必须守住的边界：

**只能展示 `ScopeType = "all"` 的策略。**

理由：`ScopeType = user | user_group | user_tag` 的策略是因人而异的。
如果在公共页展示，等于向所有人泄露「vip 组有 8 折」「某人被单独打了 7 折」——
既泄露用户分层，也让非目标用户产生"为什么我没有"的客诉。

实现上：

- `Pricing` 结构体新增 `PublicDiscount *PublicDiscountView`，只在 `ScopeType=all` 且有活跃策略时填充
- 该字段**只包含** `rate` / `endAt` / `name`，不含 `policyId`、`targetValue`、预算信息
- 需要登录态才能看到的个人折扣，走独立的 `GET /api/pricing/my-discounts`（复用 resolver，
  返回当前用户命中的策略摘要），不混进公共接口

注意缓存：模型广场的 `pricingMap` 是全局缓存（`model/pricing.go:GetPricing`，1 分钟 TTL）。
`PublicDiscount` 只能来自全局策略，不能按用户解析 —— 否则缓存会串用户。
个人折扣必须走独立接口、独立缓存键。

### 5.7 促销形态分类与 v1 边界

"限时折扣"不是一种机制，是六种。一张表硬塞六种，三个月后就是第二个 `group`。

| # | 形态 | 例子 | 可归约为 | v1 |
|---|---|---|---|---|
| 1 | 模型促销 | 新模型上线 8 折 | `target=model` + 单次窗口 | ✅ |
| 2 | 分组促销 | vip 用户 9 折 | `scope=user_group` + 单次窗口 | ✅ |
| 3 | 组合促销 | 618 期间 vip 用 pro 池 8 折 | `scope=user_group` + `target=pool` | ✅ |
| 4 | 名额促销 | 前 1000 名 5 折 | 需要计数器 | ❌ |
| 5 | 新客促销 | 首次调用 5 折 | 需要用户状态机 | ❌ |
| 6 | 周期促销 | 每晚 20–22 点特价 | 需要 cron 窗口 | ❌ |

**第 4–6 用生成器绕开，不扩存储模型：**

- 名额促销：活动上线时批量生成 1000 条 `users.discount`，先到先得由发放入口控制
- 新客促销：注册时写入一条带 `ends_at` 的个人折扣
- 周期促销：后台任务按 cron 预生成未来 N 个窗口的单次策略

这样"限时"始终只有一种存储形态（`starts_at` / `ends_at`），
而名额、状态机、周期这些复杂语义留在生成器里，不进计费引擎。
代价是生成器本身要幂等（重复跑不能重复发放），这是它唯一的正确性要求。

### 5.8 时间窗的三个坑

**① 时区。** 存 UTC 时间戳，UI 按运营时区显示。
`billingexpr` 的 `hour(tz)` 已经吃时区参数，这里必须用同一套解释，否则
"618 零点开始"会变成北京时间早上 8 点，活动第一天直接漏掉 8 小时。

**② 跨窗口用请求开始快照。** 见 5.4。长流式请求跨过 22:00 不能被劈成两个价。

**③ 自动分组重试必须重算折扣。**
`pkg/billingexpr/types.go:62` 注释明确：`group-dependent fields are refreshed
before an auto-group retry and settlement`。
折扣如果挂在 `GroupRatioInfo` 之外、不被刷新，会出现
**用 A 池的价格预扣、用 B 池的价格结算**。
这是全方案里最容易漏的一处，因为它只在重试路径触发，常规测试覆盖不到。
实现要求：把折扣并入 `GroupRatioInfo` 或在同一处刷新，不能另开字段。

### 5.9 全局促销必须有一键熔断

配错系数（写 `0.08` 而不是 `0.8`）就是全站一折，几分钟能亏掉一个月利润。

需要一个 `atomic.Bool` 全局开关，在解析入口第一行读取：

```go
if !apiduo.PromoEnabled.Load() { return defaultResult }
```

成本约 1ns。要求：

- 管理端一个按钮，秒级生效，不需要逐条停用策略
- 触发时写审计日志（谁、何时、影响了多少在途请求）
- 熔断后**在途请求按快照价结算**，不追溯改价

---

## 6. 现有矩阵的无损迁移

`group_group_ratio[userGroup][usingGroup] = ratio` 可以直接映射为一条策略：

| 原字段 | 新字段 |
|---|---|
| key `userGroup` | `ScopeType = "user_group"`, `ScopeValue = userGroup` |
| key `usingGroup` | `TargetType = "pool"`, `TargetValue = usingGroup` |
| `ratio` | `ValueType = "rate"`, `Value = ratio` |

补默认值：

- `StartsAt = 0`（历史无起点，视为一直有效）
- `EndsAt = 迁移时刻 + 365 天`（**不是 0，不能造出永久策略**；迁移后由运营逐个确认真实期限）
- `Priority = 0`，`Stackable = false`
- `Name = "migrated: <userGroup> × <usingGroup>"`

`group_ratio[group]` 同样映射为 `ScopeType = "all"`, `TargetType = "pool"`, `Value = ratio`，
并使用最低 specificity 保证任何显式策略都能盖过它。

迁移脚本必须满足：

1. 幂等（按 `Name` 前缀 `migrated:` 去重）
2. 只读旧表，不删不改
3. 迁移前后对同一个 `(userGroup, usingGroup)` 组合，`GetUserGroupRatio` 返回值一致
   —— 这是唯一的验收判据

---

## 7. 分阶段落地

### 7.1 P0 · 收敛（2–3 天，纯管理面）

可交互原型：**[demo-group-health.html](./demo-group-health.html)**（含展开详情、删除影响面、模板抽屉、批量归档、移动端）

| 动作 | 收益 |
|---|---|
| `GroupGroupRatio` 从 UI 隐藏，仅保留 API 兼容 | 运营不再看见矩阵 |
| 分组定义加 `expires_at` + 到期自动归档 | 促销组不再永久沉淀 |
| 分组体检：90 天无流量 / 无用户 → 标灰 | 能看见僵尸分组 |
| 分组模板：`{默认 ratio, 可用池, 可用模型}` | 新建分组不用手填 |

P0 解决 80% 的痛，且零计费风险。**先做这个，观察两周再决定要不要 P1。**

### 7.2 P1 · 策略引擎（1–1.5 周）

1. 新表 + `model/discount_policy.go` + resolver，功能开关 `DISCOUNT_ENGINE_ENABLED` 默认关
2. `group_ratio` / `group_group_ratio` 降级为内置策略（priority=0，最低 specificity）
3. **Shadow 对拍**：开关关闭时也走新解析器，只比对不生效。
   `abs(old - new) > 1e-9` 时写一条 `common.SysError`，带完整上下文
   （userId、model、pool、policy 链路、新旧金额）。对拍运行至少 7 天且零差异才允许开启
4. 消费日志新增 `discount_policy_ids` / `discount_rate` / `discount_amount`

### 7.3 P2 · 池与成本（1 周）

1. `channel_pool_members` 表，双读（members 优先，`channel.Group` 兜底）
2. `CostExpr` / `CostRatio` 字段，**先只做报表**，不进计费路径
3. 管理端毛利看板：按 `(model, channel)` 聚合 `收入 − 成本`
4. 添加 admin API 显式剥离非 admin 响应的集成测试

### 7.4 P3 · 定价排期（3–5 天，可选）

`pricing_schedules` + 后台应用 + 审计日志。
只有当你确实需要"提前排期涨价"时才做，否则用现有 `PATCH /api/option/model_pricing` 手工改即可。

---

## 8. 对抗审查

完全按"这个方案会在哪里坏"来写，不列优点。

### 8.1 它可能是过度设计

如果你的真实痛点只是「分组列表太长、找不到、不敢删」，
P0 的三条就够了，P1 是净负担 —— 多一张表、多一层缓存、多一个可能错价的地方。

**判据**：你是否出现过「同一用户同时命中两个优惠，没人能说清最终该收多少」？
- 没有 → 停在 P0
- 有，但每月不到一次 → 用人工特批，别建系统
- 有，且每周都有 → 做 P1

### 8.2 缓存是唯一会伤人的地方

策略解析比矩阵解析贵一个量级（要查表、要比 specificity、要比 priority）。
缓存失效没做对的两个典型故障：

- **version 没 bump**：改了策略不生效，运营以为改错了，反复改，最后数据库里三条冲突策略
- **缓存键漏了 pool**：同一用户在池 A 拿到池 B 的折扣，金额错但看起来"合理"，能瞒很久

必须有的保护：
- 缓存键强制包含 `(userId, model, pool, policyVersion)`，缺一不可
- 上线前跑一个断言：随机 1000 组 `(user, model, pool)`，缓存命中值与直接解析值必须完全一致

### 8.3 预算与并发的交互

`UsedAmount` 是热点行。全站促销时，所有请求都在 `UPDATE` 同一行。

- SQLite：会锁库。促销期间必须接受这个代价，或干脆在 SQLite 部署下禁用 `BudgetCap`（置 0）
- MySQL / PostgreSQL：行锁可接受，但要设 `innodb_lock_wait_timeout` 上限，
  避免慢请求堆积
- **不要**把预算检查放在事务外做"先查后写"

### 8.4 成本字段的泄露面

`Channel` 结构体是 GORM 模型，很多地方直接序列化返回。
加字段后必须逐处确认：

- `GET /api/channel/*`（admin only，OK）
- `GET /api/pricing`（**public，必须剥离**）
- 日志、导出、`/api/status`、错误响应体

最稳的做法：成本字段放在独立的 `channel_costs` 表（`channel_id` 为 PK），
`Channel` 结构体完全不动。这样序列化路径一行都不用审。

**这一条建议采纳**：比在 `Channel` 上加字段安全得多，也符合解耦规范（新增文件不限位置）。

### 8.5 订阅与折扣的双重让利

`SubscriptionPlan` 已经有 `UpgradeGroup`（购买后改用户分组），
而分组又参与 `group_ratio`。叠加后可能出现"订阅打了折，促销再打折"。

`ExcludeSubscription` 就是这个开关，但它的默认值该是 `true` 还是 `false`？
建议 **`true`**：默认不允许订阅用户再吃促销。
理由是订阅已经是长期让利，促销是拉新工具，两者目标冲突。要叠加必须显式开启。

### 8.6 给 UserBase 加字段 = 全量缓存击穿

`UserBase.cacheMAC()`（`model/user_cache.go:35`）对**整个结构体**做 HMAC 签名
（只把 `Quota` 置零）。新增 `Discount` 字段会改变签名内容，
存量缓存的 MAC 全部校验失败，`cacheGetUserBase` 一律返回
`user cache integrity check failed` → **回源数据库**。

升级瞬间所有活跃用户同时打 DB。这不是"加个字段"，是一次全量缓存重建。

必须的准备：

1. `userCacheSchemaVersion` 从 2 升到 3（不升会读到结构不完整的旧缓存）
2. 低峰发布，提前压测 DB 承载
3. 写预热脚本：按 `last_active` 倒序逐个 `populateUserCache`，观察 QPS 爬升曲线
4. 准备回滚：回滚也要重建缓存（字段消失，同样是 MAC 不匹配），
   **回滚的成本和发布一样高**，必须一次做对

顺带的风险：上游下次改 `UserBase`（加字段、改 schema）会与这里冲突。
缓解办法是 `UserBase` 里只放一个纯数据字段，所有解析/合并逻辑放在 apiduo 自己的包里，
让合并冲突面最小化。

### 8.7 批量改折扣会撞上 PublishUserAuthCache 的 4 次查询

`PublishUserAuthCache` 内部是 `GetUserById` → `updateUserCache` → 再 `GetUserById`
比对，循环最多 3 次校验。单用户约 2–4 次 DB 查询。

改 1000 个用户 = 2000–4000 次查询。一次活动批量发折扣会把这个接口打爆。

批量场景必须走另一条路：一个事务 `UPDATE users SET discount=...` 批量写，
然后 `DEL user:<id>` 批量失效（pipeline），让下次读自然回源重建。

代价：绕过 fence 保护，可能和并发的改名/改密/封禁产生竞争。
取舍是——**批量发折扣和改用户凭证同时发生的概率极低**，可以接受；
但单用户改折扣必须走 `PublishUserAuthCache`，不能图省事统一走批量路径。

### 8.8 users.discount 是 JSON，报表查不了

"列出所有折扣低于 0.8 的用户"需要全表扫。

- 特殊客户几十个：接受，运营手工看
- 上百个：加 MySQL 生成列 + 索引
  （`ALTER TABLE users ADD COLUMN discount_rate DECIMAL(6,4)
   AS (JSON_EXTRACT(discount,'$.rate')) STORED, ADD INDEX(...)`）
- **绝对不要**再建一张同步表做报表 —— 双写必然不一致，
  而且不一致的时候你无法判断哪边是对的

### 8.9 个人折扣必须覆盖 group_ratio，不能相乘

这是设计约束，不是实现细节。

不加这条约束，一个用户可以同时叠加：订阅带来的 `group_ratio` × 个人折扣 × 全场促销。
三层乘法又回来了，运营算不清、客服解释不了、财务对不上账。

规则：**`users.discount` 存在且命中时，直接覆盖 `group_ratio` 的结果，不相乘。**

副作用要说清楚：拿 7 折合同价的客户，享受不到 vip 的 9 折。
这通常符合直觉（合同价就是最终价），但必须在合同和产品说明里写明，
否则客户会拿两个折扣来算给你看。

### 8.10 折扣变更的生效延迟

用户缓存 TTL 来自 `RedisKeyCacheSeconds()`（`model/user_cache.go:68`），默认 60 秒。

- 改了折扣**不 publish**：最多 60 秒不生效
- **publish 了**：下一次请求生效
- **在途请求**：快照已冻结，按旧价结算

运营必须理解最后一条，否则会出现"我 10:00 改了价格，为什么 10:00:03 那笔还是旧价"的工单。
UI 上应该直接写一句：**改完对新请求生效，进行中的请求按原价完成**。

### 8.11 这个方案解决不了的事

- **上游成本波动**：`CostExpr` 只能反映你录入的采购价，上游调价你不更新，毛利就是假的。
  必须有"成本超过 X 天未更新则告警"的机制，否则这个字段三个月后变成装饰。
- **多币种**：`CostCurrency` 只是记录，不做汇率换算。真要算跨币种毛利，
  需要汇率表和汇率日期，那是另一个工程。
- **退款与冲正**：`UsedAmount` 是单调递增的，退款不会回滚。预算口径会偏高。
  这是可接受的（宁可保守），但要写进文档，否则财务会问。

---

## 9. apiduo 解耦规范落地约束

按 `MEMORY.md` 的「解耦规范 v2」：判据是「有没有改上游既有文件的内容」。
个人折扣这一项**必须**改三个上游文件，所以要把改动面压到最小并显式登记。

### 9.1 改动清单（4 处，每处 ≤3 行）

| 文件 | 改动 | 行数 |
|---|---|---|
| `model/user.go` | `User` 加 `Discount string` 列 | 1 |
| `model/user_cache.go` | `UserBase` 加 `Discount UserDiscount` 字段 | 1 |
| `model/user_cache.go` | `userCacheSchemaVersion` 2 → 3 | 1 |
| `relay/helper/price.go` | `HandleGroupRatio` 后加标记块调用 | 3 |

每处都用标记块包裹：

```go
// APIDUO:discount:BEGIN
Discount UserDiscount `json:"apiduo_discount"`
// APIDUO:discount:END
```

并在 `docs/apiduo/hooks.md`（待建）登记：文件名、标记名、行号、用途。

### 9.2 不改的

- `model/channel.go` 不加字段 → 成本走新表 `channel_costs`
- `setting/ratio_setting/group_ratio.go` 不改逻辑 →
  在其外层新增 `service/discount_resolver.go`，把它作为 fallback 调用
- `model/user_auth_cache.go` 不改 → 靠 `ToBaseUser()` 带上新字段，复用现有 publish 通道

### 9.3 挂接点

```go
// relay/helper/price.go, HandleGroupRatio
// APIDUO:discount:BEGIN
groupRatioInfo = apiduo.ResolveDiscount(c, relayInfo, groupRatioInfo)
// APIDUO:discount:END
```

块内只有一行调用，业务逻辑全部在 `pkg/apiduo/discount/` 下。
**折扣必须并入 `GroupRatioInfo` 或在同一处刷新**，不能另开字段挂在 `RelayInfo` 上
（原因见 5.8 ③：auto-group 重试不会刷新旁挂字段）。

### 9.4 默认关闭 + 行为等价

- `DISCOUNT_ENGINE_ENABLED=false` 时 `ResolveDiscount` 直接返回入参，零副作用
- 测试固定：关闭态下 100 组 `(user, model, pool)` 的 `QuotaToPreConsume` 与改动前逐位相同
- **开启态的 e2e**：开启 → 建一条 rate=0.8 策略 → 发一次请求 →
  断言日志 `discount_policy_ids` 与金额

### 9.5 标记与界面

- 标记前缀统一 `APIDUO:`，前端用 `//#region apiduo`
- 新增的策略管理页、毛利看板必须带 `data-apiduo`，
  登记进 `web/src/features/apiduo-marks/marks.ts`
- 促销熔断按钮必须有二次确认 + 审计日志

---

## 10. 验收标准

按优先级排列，每一条都可以写成测试。

| # | 判据 | 方式 |
|---|---|---|
| 1 | 关闭态行为等价 | 100 组 `(user, model, pool)` 的预扣 quota 与改动前逐位相同 |
| 2 | 迁移无损 | 对全部 `(userGroup, usingGroup)` 组合，新旧 `GetUserGroupRatio` 返回值一致 |
| 3 | 具体优先 | 同时存在 `all` 与 `model` 策略时，`model` 的系数生效 |
| 4 | 不相乘 | 两条同层级非 stackable 策略命中时，只应用 priority 高的一条 |
| 5 | 时间窗 | `EndsAt` 之后请求不再命中；`EndsAt` 之前的请求在结算时仍按快照价 |
| 6 | 预算 | 并发 100 请求打同一策略，`UsedAmount` 不超 `BudgetCap` |
| 7 | 成本不泄露 | 非 admin 调 `GET /api/pricing`，响应体不含 `cost` 任何形态 |
| 8 | 缓存一致 | 随机 1000 组，缓存值与直接解析值完全一致 |
| 9 | 订阅互斥 | `ExcludeSubscription=true` 时，有活跃订阅的用户不命中该策略 |
| 10 | 个人折扣零额外 IO | 开启前后 `TokenAuth` 的 Redis 命令数不变（同一 `HGETALL`） |
| 11 | 个人折扣覆盖而非相乘 | 用户同时有 group_ratio=0.9 与 discount=0.7 时，最终系数 = 0.7 |
| 12 | 缓存击穿可控 | 升级后 5 分钟内 `users` 表 QPS 峰值不超过预热脚本预估的 1.5 倍 |
| 13 | 重试重算折扣 | auto-group 从有折扣池重试到无折扣池，结算价按最终池计算，无跨池混价 |
| 14 | 熔断生效 | 触发 kill switch 后 1 秒内的新请求不再命中任何促销，在途请求按快照结算 |
| 15 | 批量发放不超时 | 1000 用户批量发折扣 < 3 秒，且不调用 `PublishUserAuthCache` |

---

## 11. 如果只做一件事

**给分组加 `expires_at`。**

一天工作量，直接切断"促销沉淀成分组"这条唯一的分组增长路径。
做完观察一个月，如果分组数量停止增长且运营不再抱怨，P1 整个可以不做。

## 12. 如果只回答一个问题

设计取舍的判据不是"能力多少"，是"当前规模下的最小代价"。

| 场景 | 建议 | 工作量 |
|---|---|---|
| 只有 3–5 个特价客户，无促销 | 改 `user.group` + `group_ratio`，**什么都不用做** | 0 |
| 分组列表太长、找不到、不敢删 | 只做 P0（`expires_at` + 体检 + 模板） | 2–3 天 |
| 每月一两次促销，能人工盯 | P0 + 促销熔断开关 | 3–4 天 |
| 每周都有促销，且出现优惠叠加纠纷 | P0 + P1 策略引擎（含 shadow 对拍） | 1.5 周 |
| 需要看清哪个模型赚钱 | P0 + P2 成本层 | 2 周 |
