# 卡密商城（外部卡密）设计方案

> 场景定案：用户在 apiduo 站内下单付款 → 拿到一串**外部凭证**（话费卡 / 游戏密钥 / 账号密码等）。
> 凭证的有效性由上游保证，**本系统不做任何核销**。

---

## 0. 一句话定位

apiduo 在这条链路里只承担**店铺 + 收银台 + 自动发货**三件事，
不承担**商品生产**，也不承担**凭证有效性**。

由此推出一条硬边界：

> **绝不复用 `model/redemption.go`。**
> `Redemption` 的语义是"本系统额度凭证"，`Redeem()` 会调 `creditTopUpQuota()` 给用户加余额。
> 把外部卡密塞进去，管理端会混、前端会混、将来一定有人误操作兑换，且 `quota` 字段对一张话费卡毫无意义。
> **外部卡密必须独立建表，独立状态机，独立管理页。**

复用清单（只复用这三样，其余全部新写）：

| 复用项 | 位置 | 用途 |
|---|---|---|
| `GetEpayClient()` / `client.Purchase()` | `controller/topup.go` | 发起支付，可自定义 notify_url |
| `service.GetCallbackAddress()` | `service/epay.go` | 拼回调地址 |
| `model.lockForUpdate(tx)` | `model/locking.go:20` | 跨三库的行锁（SQLite 自动降级） |

---

## 1. 状态机

```
订单 CardOrder
  待支付(0) ──支付回调──> 已支付(1) ──发卡──> 已发货(2) ──确认/超时──> 已完成(3)
      │                      │                    │
      │ 超时30min            │ 发卡失败(无库存)    │ 售后期内
      ▼                      ▼                    ▼
  已关闭(5)             已退款(6) <──── 退款 ──── 售后中(4)

卡密 CardItem
  未售(0) ──下单──> 锁定(1) ──回调成功──> 已售(2)
                        │                    │
                        │ 锁超时15min         │ 退款且未展示
                        ▼                    ▼
                     未售(0)              作废(3) 或 回收为未售(0)
```

两条不变式（写测试固定它们）：

1. 任一 `CardItem` 在任一时刻只可能属于一个 `OrderId`
2. 任一 `TradeNo` 最多触发一次发货

---

## 2. 数据模型

金额统一用 **`int64`（单位：分）**，不用 `float64`。上游 `TopUp.Money` 是 float，在边界处转换一次即可。
理由：浮点在累加和比对时会产生 `99.99 != 99.99000000001` 这类事故，而支付校验恰恰要比对金额。

### 2.1 `card_goods` 商品

```go
type CardGoods struct {
	Id            int    `json:"id"`
	Name          string `json:"name" gorm:"type:varchar(120);index"`
	Category      string `json:"category" gorm:"type:varchar(40);index"` // 话费/游戏/账号/其他
	Cover         string `json:"cover" gorm:"type:varchar(255)"`
	Description   string `json:"description" gorm:"type:text"`
	PriceCents    int64  `json:"price_cents"`      // 售价
	CostCents     int64  `json:"cost_cents"`       // 成本，仅后台可见
	DeliveryType  int    `json:"delivery_type"`     // 1自动发卡 2人工发货 3固定内容
	FixedContent  string `json:"fixed_content" gorm:"type:text"` // delivery_type=3
	WarrantyHours int    `json:"warranty_hours"`    // 质保时长，0=不保
	MinBuy        int    `json:"min_buy" gorm:"default:1"`
	MaxBuy        int    `json:"max_buy" gorm:"default:10"`
	StockCached   int    `json:"stock_cached"`      // 冗余计数，仅供列表展示
	Status        int    `json:"status" gorm:"default:0;index"` // 0下架 1上架
	Sort          int    `json:"sort"`
	SalesCount    int    `json:"sales_count"`
	CreatedTime   int64  `json:"created_time"`
}
```

`StockCached` 是冗余字段，**不可作为扣减依据**，只用于列表页显示"充足/紧张"。真实库存永远以 `card_item` 的 `COUNT(*) WHERE status=0` 为准。

### 2.2 `card_item` 卡密库存

```go
type CardItem struct {
	Id          int    `json:"id"`
	GoodsId     int    `json:"goods_id" gorm:"index:idx_goods_status,priority:1"`
	SecretEnc   string `json:"-" gorm:"type:text"`        // AES-GCM 密文，永不返回
	SecretMask  string `json:"secret_mask" gorm:"type:varchar(32)"` // abcd****wxyz
	Status      int    `json:"status" gorm:"index:idx_goods_status,priority:2"` // 0未售 1锁定 2已售 3作废
	BatchId     int64  `json:"batch_id" gorm:"index"`
	Supplier    string `json:"supplier" gorm:"type:varchar(80);index"` // 上游供应商
	OrderId     int    `json:"order_id" gorm:"index"`
	LockedUntil int64  `json:"locked_until"`
	SoldTime    int64  `json:"sold_time"`
	CreatedTime int64  `json:"created_time"`
}
```

关键约束（三库通用，用 GORM tag + 迁移时建）：

- `UNIQUE(goods_id, secret_hash)` —— 防重复导入。存 `secret_hash`（SHA256 前 16 字节 hex）而不是明文比对。
- `INDEX(goods_id, status, id)` —— 取货查询的主路径。

### 2.3 `card_order` 订单

```go
type CardOrder struct {
	Id           int    `json:"id"`
	OrderNo      string `json:"order_no" gorm:"unique;type:varchar(64)"` // CARD{uid}NO{ts}{rand}
	UserId       int    `json:"user_id" gorm:"index"`
	GoodsId      int    `json:"goods_id" gorm:"index"`
	GoodsName    string `json:"goods_name" gorm:"type:varchar(120)"` // 快照
	Quantity     int    `json:"quantity"`
	UnitPrice    int64  `json:"unit_price"`
	TotalCents   int64  `json:"total_cents"`   // 应付金额，回调只信这个
	Status       int    `json:"status" gorm:"index"`
	PayMethod    string `json:"pay_method" gorm:"type:varchar(30)"`
	TradeNo      string `json:"trade_no" gorm:"unique;type:varchar(128)"` // 第三方流水号，幂等键
	ChannelId    int    `json:"channel_id" gorm:"index"` // 分销渠道，预留
	ClientIp     string `json:"client_ip" gorm:"type:varchar(64)"`
	CreateTime   int64  `json:"create_time"`
	PayTime      int64  `json:"pay_time"`
	DeliverTime  int64  `json:"deliver_time"`
	WarrantyEnd  int64  `json:"warranty_end"`
	RefundReason string `json:"refund_reason" gorm:"type:varchar(255)"`
}
```

`TradeNo` 的 `unique` 索引是**幂等的第一道闸**，比任何业务判断都可靠。

### 2.4 `card_order_item` 交付明细

```go
type CardOrderItem struct {
	Id        int    `json:"id"`
	OrderId   int    `json:"order_id" gorm:"index"`
	ItemId    int    `json:"item_id" gorm:"unique"`  // 一卡只属一单
	SecretEnc string `json:"-" gorm:"type:text"`     // 密文快照，防商品删除后丢失
	CreatedTime int64 `json:"created_time"`
}
```

为什么要有明细表而不是在订单里塞 JSON：退款要按单张卡回收，客服要查"这张卡卖给了谁"，对账要按张统计废卡率。JSON 会让这三件事都变成全表扫描。

---

## 3. 三个核心事务

### 3.1 下单（锁卡）

```go
// CreateCardOrder：下单即锁卡，避免并发超卖
// 1) 校验商品上架、限购、库存
// 2) 事务内 SELECT ... FOR UPDATE 取 N 张 status=0 的卡
// 3) CAS 更新为 status=1, locked_until=now+900, order_id=order.Id
// 4) RowsAffected != N 则回滚重试（最多 3 次）
```

必须注意：**先建订单拿到 Id，再锁卡**。顺序反了会导致锁住的卡没有归属，超时释放逻辑无法定位。

锁定期 15 分钟；用户在支付页停留超过 15 分钟，支付回调到达时库存已被释放——
此时**不要拒付**，而是进入"人工处理"状态（订单置为 `已支付` 但 `已发货` 失败），后台告警补货。
拒付会让用户钱货两空，这是最差的结果。

### 3.2 支付回调（幂等发卡）

```go
// CardEpayNotify
// 1) isEpayWebhookEnabled() 闸门
// 2) client.Verify(params) 验签，失败直接 fail
// 3) verifyInfo.TradeStatus == success 才继续
// 4) 事务：
//    a. SELECT order WHERE trade_no=? FOR UPDATE
//    b. 若 order.Status >= 已支付 → 直接 return 200 success（幂等）
//    c. 比对 verifyInfo.Money 与 order.TotalCents，不一致 → 告警 + 不发卡
//    d. order.Status=已支付, PayTime=now
//    e. UPDATE card_item SET status=2, sold_time=now WHERE order_id=? AND status=1
//    f. RowsAffected != quantity → 库存异常，转人工
//    g. INSERT card_order_item
//    h. order.Status=已发货, DeliverTime=now, WarrantyEnd=now+WarrantyHours*3600
// 5) 返回 "success"（易支付要求返回 success 字符串，否则会重试）
```

返回体必须是 `success` 而不是 JSON —— 这是易支付协议要求，写错会导致无限重试。

### 3.3 退款（回收）

```go
// 人工退款，不做自动退款
// 1) 校验订单在售后期内、卡密未被标记为"已使用"
// 2) 事务：order.Status=已退款；card_item 回收为 status=0（下一单可再卖）
// 3) 退款走原路返回，由管理员在支付通道后台操作，本系统只记账
```

**不做自动退款的原因**：外部卡密一旦交付，技术上无法确认对方是否已使用。
自动退款等于给白嫖开门。人工审核 + 质保期是唯一可行的平衡。

---

## 4. 接口清单

### 前台（需登录）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/card/goods` | 商品列表（分页、分类筛选） |
| GET | `/api/card/goods/:id` | 商品详情（库存只返回"充足/紧张"） |
| POST | `/api/card/order` | 下单并锁卡，返回 order_no |
| POST | `/api/card/pay` | 发起支付，返回支付链接 |
| GET | `/api/card/order/:orderNo` | 查单（校验 user_id 归属） |
| GET | `/api/card/orders` | 我的订单 |
| POST | `/api/card/order/:orderNo/after-sale` | 申请售后 |

### 后台（admin）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET/POST/PUT/DELETE | `/api/card/admin/goods` | 商品 CRUD |
| POST | `/api/card/admin/items/import` | 批量导入卡密（CSV / 多行文本） |
| GET | `/api/card/admin/items` | 库存列表（永久打码） |
| POST | `/api/card/admin/items/void` | 批量作废 |
| GET | `/api/card/admin/orders` | 订单列表 |
| POST | `/api/card/admin/orders/:id/refund` | 人工退款 |
| GET | `/api/card/admin/stats` | 供应商废卡率、毛利统计 |

### 回调（匿名）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET/POST | `/api/card/epay/notify` | 易支付异步回调 |

---

## 5. 文件清单与挂接点

### 新增（不碰上游逻辑）

```
model/card_goods.go            商品
model/card_item.go             库存（含加密写入、掩码计算）
model/card_order.go            订单 / 明细 / 状态常量
service/card_crypto.go         AES-GCM 加解密，密钥来自环境变量
service/card_delivery.go       发卡事务（系统心脏）
controller/card_shop.go        前台接口
controller/card_admin.go       后台接口
controller/card_epay.go        支付发起 + 回调
router/card_router.go          路由注册
web/src/routes/_authenticated/cards/index.tsx        商品列表
web/src/routes/_authenticated/cards/$goodsId.tsx     商品详情 + 下单
web/src/routes/_authenticated/cards/orders.tsx       我的订单
web/src/routes/_authenticated/admin/cards.tsx        后台（商品+库存+订单）
```

### 挂接点（必须打标记块）

```
router/api-router.go        // APIDUO:card:BEGIN  一行 cardrouter.Register(apiRouter)  // APIDUO:card:END
web/src/routes/__root.tsx   导航入口（若需要）
```

按 `AGENTS.md` 的解耦规范，**挂接 = 单次调用 + 标记块，块内业务逻辑 > 3 行即判泄漏**。
本方案只有 1~2 处挂接，符合 v2 规范。

---

## 6. 安全设计

| 风险 | 对策 |
|---|---|
| 卡密明文落库被拖库 | AES-GCM 加密，密钥 `CARD_SECRET_KEY`（32 字节 hex）。**未配置密钥时拒绝启动**，fail-closed，绝不明文兜底 |
| 后台列表泄露 | 永久只展示 `SecretMask`（`abcd****wxyz`），列表页不解密 |
| 日志泄露 | 交付路径严禁把明文写进 `RecordLog` / 系统日志 |
| 超卖 | `FOR UPDATE` + CAS + `order_item.item_id unique` |
| 回调重放 | `trade_no` unique + 事务内状态判断 + 返回 success |
| 金额篡改 | 只信 `order.TotalCents`；回调金额不一致 → 告警不发卡 |
| 枚举/暴力下单 | 下单接口 IP 限速 + 用户维度 `CriticalRateLimit` |
| 黄牛扫货 | `MaxBuy` 限购 + 单 IP 单日上限 + 异常订单人工复核 |
| 库存探测 | 前台不返回精确库存，只返回 `充足 / 紧张 / 售罄` |
| 越权查单 | 查单必须 `WHERE order_no=? AND user_id=?`，不能只按单号 |

---

## 7. 合规风险（必须先看这一节再决定做不做）

### 7.1 Stripe / Creem 禁售清单

Stripe 的 Acceptable Use Policy 对以下品类有明确限制：

- **账号交易**（含 ChatGPT / 流媒体 / 游戏成品号）：属于"未授权转售账户"，**基本禁止**
- **游戏密钥 / 点卡**：需品牌授权，否则属"未授权数字商品"，风控命中即冻结
- 后果不是拒单，是**账户封禁 + 最长 180 天资金冻结 + 历史交易拒付**

结论：**用 Stripe 卖账号类商品，一次风控命中就game over。**

### 7.2 国内通道（易支付 / 码支付）

- 无支付牌照，商户资金在第四方手里，跑路时未结算款全损
- 收款方多为个人账户，**无法开票**；销售数字商品属经营行为，需要营业执照
- 一旦通道被查，商户资金冻结，且存在被牵连的风险

### 7.3 账号类商品的刑事边界

如果号源来自盗号 / 撞库：
- 《刑法》285 条：非法获取计算机信息系统数据
- 《刑法》287 条之二：帮助信息网络犯罪活动罪

司法实践中，**"明显低于市场价的批量成品号"是推定明知的常见依据**。
这条不是"注意一下"，是**建议不做**：你承担全部质保责任，却对号源毫无控制力，风险收益比在结构上就是负的。

---

## 8. 分期与验收

| 阶段 | 内容 | 验收标准 |
|---|---|---|
| **P0**（2 天） | 4 张表 + 加密模块 + 后台商品/库存管理 | 能导入 100 张卡、能看库存打码、商品能上下架 |
| **P1**（3 天） | 下单锁卡 + 支付 + 回调发卡 + 查单 | 并发 50 单不超卖；回调重放 3 次只发 1 次货 |
| **P2**（2 天） | 锁超时释放 + 金额校验 + 售后 + 限购 | 超时释放后库存可再售；金额不符不发卡 |
| **P3**（2 天） | 前台商城 UI + 我的订单 | 完整走通：选品 → 付款 → 看到卡密 → 售后 |
| **P4**（后续） | 分销（channel_id 已预留） | 先只留字段，不做佣金计算 |

到 P3 约 **9 个工作日**可上线收单。

### P1 必须写的三个测试

1. **超卖测试**：`t.Parallel()` 起 50 个 goroutine 抢 10 张卡，断言成功数 == 10
2. **重放测试**：同一 `trade_no` 回调连续调用 3 次，断言只产生 1 条 `card_order_item`
3. **金额篡改测试**：回调金额改成 0.01，断言订单不发货且告警

---

## 9. 待确认（会改变设计）

1. **商品品类**：只做自营 AI 额度？还是真要卖话费卡/游戏key/成品号？
   - 只做额度 → 不用建库存池，发码即生成，砍掉 `lock` 状态和超时逻辑，工期从 9 天降到 3 天
   - 卖外部卡 → 本文档的完整方案
2. **支付通道**：国内走易支付（有跑路风险），还是只面向海外走 Stripe（但账号类会被禁售）
3. **是否允许未登录下单**：建议不允许。登录才有售后凭证、才有分销归属、才能防风控
4. **实体卡 vs 电子凭证**：若上游给的是实体卡，需要额外发快递，`delivery_type=2` 走人工
