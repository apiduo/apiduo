# 外部 MCP 服务接入方案（MCP 网关 + 计费）

> 2026-11-14 v2。设计稿，未落代码。
> 基线：`mcp/README.md`（内置 4 工具 + 模型能力注册）、`mcp/protocol_server.go`（Stateless + JSONResponse）、
> `service/billing_session.go`（统一计费会话）、`model/log.go`（logs 表）。
>
> v1 把计费判成"默认关闭的附加项"，那个判断是错的。既然定位是网关，计费就是主链路 ——
> 网关的商业模式只能来自转售，免费转发无法支撑运营成本。本文按「计费是一等公民」重写，
> 计费粒度也从服务下沉到工具。

## 0. 一句话

**同一个网关的第二种流量：LLM 走 `/v1` 按 token 计价，工具走 `/mcp` 按次计价，共用用户、令牌、分组、额度与日志。**

这句话决定了实现方式：MCP 不是旁挂的管理面，而是一条与 relay 平级的计费链路。任何"先跑通再加计费"的做法，都会在补计费时推翻数据结构（价格粒度、流水字段、退款语义），返工成本远高于一次做对。

## 1. 定位：它是什么网关

| 维度 | LLM 网关（现状） | MCP 网关（新增） |
|---|---|---|
| 入口 | `/v1/*` | `/mcp`（聚合）、`/mcp/s/{alias}`（直通） |
| 上游 | 模型供应商渠道 | 外部 MCP 服务 |
| 计量单位 | token | 次（P0），可扩展为上游声明的量 |
| 定价依据 | 模型倍率表 | 工具单价表 |
| 身份 | 用户令牌 | 用户令牌（同一套） |
| 额度 | 钱包 / 订阅 / 令牌额度 | 同一套 |
| 流水 | `logs` type=2 | 同一张表，`model_name` 用 `mcp/` 前缀 |

由此得出三条不可协商的约束：

1. **不新建身份体系**：令牌、分组、IP 白名单、模型限制全部沿用。第二套凭据等于第二套权限漏洞。
2. **不新建余额体系**：钱包、订阅、令牌额度三处必须与 LLM 一致。用户不能有两个余额。
3. **不新建账本**：计费流水进 `logs`，账单页与用量报表自动合并。独立账本会让"这个月花了多少"永远对不上。

## 2. 边界

### 做

| 项 | 说明 |
|---|---|
| 传输 | 远程 Streamable HTTP、远程 SSE（旧版） |
| 方法 | `initialize` / `tools/list` / `tools/call` |
| 端点 | 聚合 `/mcp`（工具名前缀）+ 直通 `/mcp/s/{alias}`（原样透传） |
| 凭证 | 平台代持（`bearer` / `header`），加密落库 |
| 治理 | 分组可见、工具级开关、限流、审计、用量统计 |
| 计费 | 按次计价，服务默认价 + 工具级覆盖 + 分组倍率；预扣 / 退款 / 流水 |

### 不做

| 项 | 原因 |
|---|---|
| 本地 stdio 服务（`npx` / `uvx` 起进程） | 多租户常驻进程，为每个用户起子进程等于把 RCE 面开到公网。需要 stdio 的用户自行 `mcp-remote` 桥成 HTTP |
| 聚合层透传 `resources` / `prompts` / `sampling` / `elicitation` | 一旦透传就要处理 URI 命名空间、内容大小、上游反向请求（sampling 是上游调客户端）。风险远超 `tools`，收益接近 0 |
| 服务端主动通知 | 对外是 Stateless + JSONResponse，没有长连接可推。上游变更由后台刷新吸收 |
| 公共 MCP 目录 / 一键安装 | 目录 = 平台为未知代码背书 |

**stdio 这条边界必须写在接入文档第一屏。** 用户对 MCP 的第一印象大多来自 `npx @modelcontextprotocol/server-xxx`，不提前说明会反复问"为什么不能填命令"。

## 3. 三个结构性判断

### 3.1 双轨端点：聚合 + 直通

只做聚合，等于承诺「我理解 MCP 的全部语义」；只做直通，等于没有产品价值（用户还是要配 N 个 URL）。

```
客户端 ──▶ /mcp              聚合：工具名加前缀，只透传 tools
       └─▶ /mcp/s/github     直通：body 原样转发，完整协议
```

直通端点的实现代价极低 —— 鉴权、SSRF 校验、限流之后，把 `Method` / `Body` / `Content-Type` / `Mcp-Session-Id` 原样转发，响应原样回写。**它不需要理解 MCP 的任何内容**，这正是它的价值：协议演进时它不会过时。

两者共用同一张表、同一套鉴权、同一套计费。区别只在于 `tools/list` 是否被改写、`tools/call` 是否被路由。

直通端点的计费：按 transport 计次（一次 POST = 一次调用），单价用服务的默认价。它无法按工具定价（因为不解析 body），这是设计取舍 —— 需要精细定价就配聚合端点。

### 3.2 能力是快照，不是实时查询

`tools/list` 绝不能在上游实时拉取。客户端初始化通常只有 60s 预算，串行打 5 个上游（各 3s 超时）就废了；上游挂一个，整个 `/mcp` 就初始化失败 —— **一个外部服务的故障，不该让所有工具消失**。

```
后台刷新 ──▶ 写 mcp_server_tools（快照）
                       │
客户端 tools/list ─────┘ 只读本地表
```

三条硬规则：

1. **刷新失败不写快照。** 保留上一次成功的结果。否则上游抖一下，线上所有 agent 的工具列表瞬间清空。
2. **快照带 `schema_hash`。** 变了就写审计（工具增/删/改），这是"上游偷偷改了工具"的唯一可观测点。
3. **连续 3 次刷新失败 → `degraded`，不自动禁用。** 自动禁用会让用户毫无征兆地丢工具；`degraded` 只在面板标红，工具继续可用。

### 3.3 计费是主链路

计费不能"后补"，因为三个东西一旦定错就要迁移数据：

- **价格放在哪一层**（服务 or 工具）
- **流水的字段**（是否含定价快照）
- **退款的分界点**（什么算"没成功"）

所以 §4 先定死计费模型，再谈路由和转发。

## 4. 计费模型

### 4.1 粒度：按工具，不按服务

一个服务里 `list_repos` 和 `create_pr` 的成本完全不同，`search` 和 `get_by_id` 也不是一个价。按服务定价只有两种结局：

- 按贵的定 → `list` 类工具被过度收费，用户会绕开网关直连
- 按便宜的定 → 高成本工具被贱卖，平台贴钱

所以价格落在 `mcp_server_tools.price_per_call`，服务级只有 `default_price` 作为缺省值。

### 4.2 三层定价与三态

```
tool.price_per_call  (*int64)   ← 覆盖（nil = 继承服务默认）
server.default_price (*int64)   ← 缺省（nil = 免费）
group_ratio          (float64)  ← 倍率，复用 ratio_setting.GetGroupRatio

quota = round(price × group_ratio)
```

`*int64` 是项目既有的指针三态约定（同 `max_ref_images`）：

| 值 | 含义 |
|---|---|
| `nil` | 继承 —— 工具级用服务默认；服务级则为免费 |
| `0` | **显式免费** —— 管理员主动放开某个工具 |
| `N` | 每次 N quota |

把 `nil` 当 `0` 会让"继承"和"免费"无法区分：管理员给服务设了 100，新发现的工具本该继承，结果变成免费 —— 而且是静默的。

分组倍率复用 `ratio_setting.GetGroupRatio(group)`（不带模型参数），使 VIP 用户在 MCP 与 LLM 两侧享受同一套折扣逻辑。**不新造倍率表**：两套倍率必然漂移，且没人能解释为什么同一个用户在两个入口价格不同。

### 4.3 计费分界点：收到上游响应

不按"成功"计费，按"上游处理了请求"计费。

| 结果 | 计费 | 理由 |
|---|---|---|
| 正常 result | 计 | — |
| `result.isError = true` | **计** | 工具执行了，业务失败不等于没有成本 |
| JSON-RPC `error` | 退 | 协议层拒绝，工具未进入执行 |
| 401 / 403 / 404 | 退 | 未进入执行 |
| 5xx | 退 | 上游入口失败，通常未执行 |
| 超时 / 连接失败 | 退 | 不确定是否执行，见下 |
| 本地参数校验失败 | 不计（未预扣） | 没有发出请求 |
| 余额不足 | 不计（预扣失败即拒） | 请求根本不会发出 |

**为什么 `isError` 要计费**：网关无法可靠区分"工具执行到一半失败"和"工具在参数校验阶段就拒绝"。若一律退款，用户可以用必然失败的请求稳定白嫖上游计算（比如让搜索工具跑完再报错）。规则必须可解释且不能有免费通道 —— "上游给了响应就计费"是唯一自洽的划分。

**为什么超时要退**：不退的话，用户要为"网关连不上"付费，这是明确的坏体验。滥用空间很小 —— 用户无法稳定构造"上游执行成功但网关侧超时"的场景。退款记 `refund_reason=timeout` 供事后审计；如果某用户超时退款率异常，是运维信号而非计费漏洞。

### 4.4 与现有额度体系的衔接

复用 `service.BillingSession`（钱包 / 订阅 / 令牌额度三处自动一致），**不复制扣费逻辑**。订阅、`TokenUnlimited`、信任额度旁路这些分支复制一遍必漏，漏掉的表现是"某些用户的余额不减"，且很难被发现。

只有一个障碍：`NewBillingSession(c *gin.Context, ...)` 依赖 `gin.Context`，而 MCP 工具层拿不到它 —— `engine.Any("/mcp", ..., gin.WrapH(proto))` 之后 gin.Context 已经不可见，现有的 `captureCaller()` 正是为此把身份搬进 `request.Context()`。

`gin.Context` 在 `preConsume` 里只用于两件事，都有替代：

| 用法 | 替代 |
|---|---|
| `i18n.T(c, key, args)` | `i18n.Translate(lang, key, args)`（`T` 本身就是 `Translate(StatedLang(c),…)` 的包装） |
| `logger.LogInfo(c, msg)` | `logger.LogInfo(ctx, msg)`（签名是 `context.Context`） |

所以改造是**新增一个入口**，不动原有路径：

```go
// service/billing.go
// PreConsumeForTool 供非 HTTP-relay 路径（MCP 工具网关）使用。
// 只依赖已解析的身份与语言，不持有 gin.Context。
func PreConsumeForTool(relayInfo *relaycommon.RelayInfo, lang string, quota int) (relaycommon.BillingSettler, *types.NewAPIError)
```

内部把 `NewBillingSession` 的两处 `c` 依赖换成 `lang` / `ctx`，约 20 行。`relayInfo` 由 MCP 层构造：

```go
info := &relaycommon.RelayInfo{
	UserId:           caller.UserId,
	TokenId:          caller.TokenId,
	TokenGroup:       caller.Group,
	UsingGroup:       caller.Group,
	OriginModelName:  "mcp/" + srv.Alias + "/" + tool.Name,
	BillingModelName: "mcp/" + srv.Alias + "/" + tool.Name,
	UserSetting:      caller.UserSetting, // 决定钱包 or 订阅
	RequestId:        common.NewRequestId(),
	ForcePreConsume:  true,               // 不允许信任额度旁路
}
```

`ForcePreConsume = true` 是必需的：`shouldTrust` 的信任旁路是为 LLM 流式设计的（后续一定能扣到），而 MCP 调用是**一次性、不可撤回**的 —— 放行后再扣，用户余额不足就变成坏账。

`caller` 需要从 `mcpCaller` 扩展，加载 `Group` / `UserSetting` / `Lang`。这些都是 `TokenAuthReadOnly` 已经解析过、或一次缓存查询能拿到的，不额外开库。

### 4.5 流水与对账

写 `logs` 表，与其他消费完全同构：

| 字段 | 值 |
|---|---|
| `Type` | `2`（消费） |
| `ModelName` | `mcp/{alias}/{tool}` |
| `Quota` | 实扣额度（`0` 的行不写） |
| `ChannelId` | `0` |
| `TokenId` / `TokenName` / `Group` / `Ip` | 来自令牌与请求 |
| `UseTime` | 上游耗时（毫秒） |
| `Other` | `mcp_server` / `mcp_tool` / `mcp_price` / `mcp_group_ratio` / `mcp_status` / `mcp_refund_reason` |

三条硬规则：

1. **必须存定价快照 `mcp_price`。** 单价从 100 改成 200 之后，历史行只有 `quota`，无法回答"当时多少钱一次"。`quota` 是结果，`mcp_price` 是依据，对账需要依据。
2. **不存 `tool_id`。** 快照刷新会删工具行，存 id 会让历史账单失去归属。存 `alias + name` 字符串，让流水自包含。
3. **`model_name` 用 `mcp/` 前缀。** 使现有"按模型消费"报表自动包含 MCP，前端加一个筛选即可区分；不加前缀会与真实模型名冲突（模型名里也可能有斜杠）。

**报表侧**：现有消费报表自动包含 MCP 行（因为读 `logs`）。前端需要在模型筛选里把 `mcp/` 前缀的行归到"MCP 工具"一栏，否则模型下拉框会被几十个 `mcp/xxx` 污染。

### 4.6 免费调用与用量统计

免费不等于不记录。但**免费调用不写 `logs`** —— `quota=0` 的消费行会稀释报表、影响客单价、污染"消费笔数"。

用量走独立统计：

```sql
UPDATE mcp_server_tools
SET call_count = call_count + 1, last_called_at = ?
WHERE id = ?
```

异步执行，失败忽略。这样：

- 计费：`logs` 只承载真实扣费
- 运营：`call_count` / `last_called_at` 回答"这个工具到底有没有人用"
- 排障：需要调用明细时打开服务级 `log_calls`，写审计（默认关，量可能很大）

三件事分开，才能出现"这个工具天天被调但一分钱不收"的可发现状态 —— 那通常是定价漏配。

### 4.7 计费的五个坑

| 坑 | 后果 | 处理 |
|---|---|---|
| 快照刷新覆盖价格与启停 | 管理员设的价被上游刷新清零，账单静默变 0 | upsert 只更新 `Name` / `Description` / `InputSchema` / `Annotations` / `LastSeenAt`，**不碰** `Enabled` / `PricePerCall` |
| 先调上游再扣费 | 余额不足的用户刷爆额度，无法追回 | 预扣失败即拒，请求不发出 |
| 退款不幂等 | 同一次调用退两次，平台漏财 | 退款绑定 `request_id`，一次调用最多一次；进程崩溃不重放 |
| 价格变更影响历史 | 对账时无法解释旧账单 | 流水存 `mcp_price` 快照 |
| 免费工具不统计 | 永远不知道哪些工具该定价 | `call_count` 无条件累加 |

第一条是项目里已经踩过的同一类坑：`SaveProfile` 更新时强制沿用库里的 `enabled`，因为 bool 零值是 false，漏传会把模型静默停用。快照刷新比那更危险 —— 它每 10 分钟跑一次。

## 5. 数据模型

两张新表，与 `mcp_model_profiles` 并列。**不扩 `channel` 表**：channel 的语义是 LLM 渠道，带 relay 的渠道选择、权重、自动禁用逻辑；把工具代理塞进去，等于让 relay 的调度器去理解"工具"。

```go
// McpServer 是一个外部 MCP 服务。
type McpServer struct {
	Id          int64  `json:"id" gorm:"primaryKey"`
	Alias       string `json:"alias" gorm:"size:16;not null;uniqueIndex:uk_mcp_server_alias"`
	DisplayName string `json:"display_name" gorm:"size:191"`
	Transport   string `json:"transport" gorm:"size:16;not null"` // http | sse
	Endpoint    string `json:"endpoint" gorm:"size:512;not null"`

	AuthType     string `json:"auth_type" gorm:"size:16;not null"` // none | bearer | header
	AuthHeader   string `json:"auth_header" gorm:"size:64"`        // auth_type=header 时的头名
	SecretCipher string `json:"-" gorm:"type:text"`                // v1:<base64(nonce||ct)>，永不出库
	SecretHint   string `json:"secret_hint" gorm:"size:8"`         // 末 4 位，仅供人辨认
	ExtraHeaders string `json:"extra_headers" gorm:"type:text"`    // JSON，非敏感头

	TimeoutMs      int  `json:"timeout_ms" gorm:"not null;default:30000"`
	MaxConcurrency int  `json:"max_concurrency" gorm:"not null;default:8"`
	AllowPrivateIP bool `json:"allow_private_ip" gorm:"not null"` // 默认 false
	Enabled        bool `json:"enabled" gorm:"not null;index"`

	OwnerUserId int64  `json:"owner_user_id" gorm:"index"` // 0 = 平台级
	Visibility  string `json:"visibility" gorm:"size:16"`  // public | private
	Groups      string `json:"groups" gorm:"size:255"`     // CSV，空 = 全部分组

	// 计费
	BillingMode    string `json:"billing_mode" gorm:"size:16"` // free | per_call
	DefaultPrice   *int64 `json:"default_price"`               // nil = 免费
	RatePerMin     int    `json:"rate_per_min"`
	DefaultToolset string `json:"default_toolset" gorm:"size:64"` // 空 = 不裁剪
	LogCalls       bool   `json:"log_calls" gorm:"not null"`      // 调用明细进审计，默认 false

	Status        string `json:"status" gorm:"size:16"` // ok | degraded | never
	LastError     string `json:"last_error" gorm:"size:512"`
	LastRefreshAt int64  `json:"last_refresh_at"`
	CreatedAt     int64  `json:"created_at"`
	UpdatedAt     int64  `json:"updated_at"`
	Version       int64  `json:"version" gorm:"not null;default:0"`
}

// McpServerTool 是上游 tools/list 的一条快照 + 本地定价与开关。
//
// 快照字段（Name/Description/InputSchema/Annotations）由刷新覆盖；
// 本地字段（Enabled/PricePerCall）刷新时一律不碰 —— 见 §4.7。
type McpServerTool struct {
	Id          int64  `json:"id" gorm:"primaryKey"`
	ServerId    int64  `json:"server_id" gorm:"not null;uniqueIndex:uk_mcp_tool_origin"`
	Name        string `json:"name" gorm:"size:191;not null;uniqueIndex:uk_mcp_tool_origin"` // 上游原名
	ExposedName string `json:"exposed_name" gorm:"size:64;not null;uniqueIndex:uk_mcp_tool_exposed"`
	Description string `json:"description" gorm:"type:text"`
	InputSchema string `json:"input_schema" gorm:"type:text"` // JSON
	Annotations string `json:"annotations" gorm:"type:text"`  // readOnlyHint 等

	Enabled      bool   `json:"enabled" gorm:"not null"`
	PricePerCall *int64 `json:"price_per_call"`               // nil = 继承服务默认
	CallCount    int64  `json:"call_count" gorm:"not null"`  // 无条件累加，含免费调用
	LastCalledAt int64  `json:"last_called_at"`
	LastSeenAt   int64  `json:"last_seen_at"`
}
```

唯一索引两条：`ExposedName` 全局唯一（命名冲突必须在写入时暴露）；`(ServerId, Name)` 唯一（上游重名工具属于异常，保留两行只会让路由二义）。

### 工具集（toolset）：防止工具数量爆炸

这一条容易被忽略，但它是规模化之后的主要故障源。

MCP 的 `tools/list` 结果会**进 prompt**。一个服务 50 个工具、接 8 个服务 = 400 条工具描述，agent 的选择质量会显著下降，token 成本翻几倍。这不是理论问题，是接第 5 个服务时必然发生的事。

聚合端点支持按工具集裁剪：

```
GET /mcp?toolset=dev          # 只返回该集合内的工具
```

一条 toolset = 名字 + 一组 `(server_alias, tool_name)`，可含通配 `github:*`。管理面提供"从某服务里挑几个工具"的勾选 UI。

不传 `toolset` 时返回该令牌可见的、`enabled=true` 的全部工具。是否暴露写工具由注册默认值与管理员开关决定（§10.2），**不在读取路径上二次判断** —— 否则管理员明明启用了写工具，agent 却看不到。

## 6. 请求路径

### 6.1 tools/list

```
1. 内置 4 工具（models.list / image.generate / video.generate / task.get）
2. 取该令牌可见的 server：
     owner_user_id = 0 AND enabled AND (groups 为空 OR 命中令牌分组)
     OR owner_user_id = 当前用户
3. 读快照表里 enabled 的工具，按 toolset 过滤
4. 合并，逐条校验 exposed_name 合法性
```

第 4 步不是多余的：上游返回 `name: "文件/读取"` 完全合法（MCP 规范允许），但会让多数客户端拒绝整个工具列表。这种情况要**跳过该工具并写一次 warn 审计**，不能因为一条脏数据废掉整个端点。

### 6.2 tools/call

```
1. 用 exposed_name 查表 → (server, tool)        ← 查表，不解析字符串
2. 可见性 + enabled + toolset 校验
3. 限流（每用户每 server 每分钟 N 次）
4. 算价：price = tool.price ?? server.default_price
5. price > 0 → PreConsumeForTool(price × group_ratio)
   │            失败 → 拒绝（403），请求不发出
6. 转发上游
7. 收到响应 → 计费生效（含 isError）
   连接失败 / 超时 / JSON-RPC error / 5xx → 退款
8. 写 logs（仅 price > 0）+ call_count++（无条件）
9. 回写 result（content 原样，isError 原样）
```

**第 1 步必须是查表。** 常见错误是 `strings.SplitN(name, "__", 2)` —— 上游工具名本身可能含 `__`（`aws__s3__get`），split 会路由到错误的服务。前缀只用于人类阅读，路由靠唯一索引。

**第 9 步的错误语义分两层**：

| 层 | 载体 | 处理 |
|---|---|---|
| 协议错误（方法不存在、参数不合 schema、上游 5xx） | JSON-RPC `error` | 映射成 apiduo 错误码，退款 |
| 工具执行错误（工具跑了但失败） | `result.isError = true` | **原样透传，照常计费** |

把 `isError` 提升成协议错误，客户端会当成传输故障重试 —— 而工具可能已经产生了副作用，且重试会再扣一次费。

## 7. 命名空间

MCP 客户端对工具名的限制不统一。按最严的来（OpenAI / Anthropic 的 function name 约束）：

```
^[a-zA-Z0-9_-]{1,64}$
```

| 项 | 规则 |
|---|---|
| `alias` | `^[a-z][a-z0-9-]{0,15}$`，1–16 字符，注册时人工填 |
| 暴露名 | `{alias}__{sanitized_name}` |
| `sanitized_name` | 非 `[A-Za-z0-9_-]` → `_`，连续折叠为一个 |
| 超长 | 总长 > 64 时截断到 59 + `_` + crc32(原名) 4 位十六进制 |
| 冲突 | 写入时撞唯一索引 → 报错，要求改 alias（**不静默改名**） |

```
github  + create_issue       → github__create_issue
notion  + search             → notion__search
aws     + s3.get_object      → aws__s3_get_object
```

**不静默改名**是硬要求：agent 的调用参数里带着工具名，改名会让既有 prompt / 工作流静默失效，而且没有人知道为什么。宁可让注册失败。

## 8. 转发实现

用官方 `github.com/modelcontextprotocol/go-sdk` 的 `Client` + `StreamableClientTransport`，不手写 JSON-RPC。

```go
cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{
	Endpoint:             srv.Endpoint,
	HTTPClient:           protectedClient(srv),   // SSRF 保护 + 超时
	DisableStandaloneSSE: true,                   // 只要请求-响应，不要长连接
	MaxRetries:           -1,                     // ★ SDK 默认 5，必须关掉
	MaxEventSize:         8 << 20,
}, nil)
defer cs.Close()
res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: tool.Name, Arguments: args})
```

自己手写 JSON-RPC 看着简单，实际要处理：SSE 流式响应解析、`Mcp-Session-Id` 生命周期、协议版本协商、`notifications/initialized`、分块事件、`progress` 通知跳过。官方 client 把这些都做了，且跟随规范演进。

三个必须显式设置的参数：

1. **`MaxRetries: -1`** —— SDK 默认对 POST 重试 5 次。外部工具可能有副作用（转账、发邮件、删文件），重试等于重复执行，**并且会重复计费**。这与 `mcp/README.md` §5「未匹配的错误默认不可重试」是同一条原则。
2. **`DisableStandaloneSSE: true`** —— 否则 `Connect` 会额外建立一条 GET 长连接，在网关里等于每个并发调用多占一条上游连接。
3. **`HTTPClient`** —— 必须是受保护客户端（§10.1），不能是 `http.DefaultClient`。

### 每次调用的 2 RTT

`Connect` 会走 `initialize`，所以一次工具调用 = initialize + tools/call。局域网 5ms，公网 100–200ms。对生成类工具（秒级）可忽略；对高频查询是明显开销。

**P0 接受这个开销，不做连接池。** 连接池要处理会话过期、上游回收、并发共享，出错时的表现是"偶发 404 session not found"，排查成本远高于收益。

### 探测（probe）

注册时必须能一键验证。这是接入体验的核心 —— 外部服务配置错误率极高（URL 少一段、token 过期、路径多了斜杠）。

```
initialize → notifications/initialized → tools/list
```

失败原因要能区分，且给人话：

| 现象 | 结论 |
|---|---|
| 401 / 403 | 凭证无效 |
| 404 | 端点路径不对 |
| 超时 / DNS 失败 | 网络不通或域名拼错 |
| `unexpected content type` | 多半是只支持 SSE 的旧服务 |
| JSON 解析失败 | 不是 MCP 端点（可能填了文档地址） |

探测**必须同步返回**（管理员在等），超时 8s 封顶。

## 9. 鉴权与凭证

### 9.1 两段鉴权

```
客户端 ──Bearer sk-xxx──▶ apiduo ──Bearer <上游凭证>──▶ 外部 MCP 服务
        （用户令牌，已有）          （平台代持，新增）
```

第一段完全复用 `TokenAuthReadOnly`：额度、分组、IP 白名单、模型限制全部生效，不新增凭据体系。

第二段是新增的暴露面：**apiduo 持有第三方账号的凭证**。这是本方案最大的风险点。

### 9.2 落库加密

现有 `CRYPTO_SECRET` 只用于 HMAC（`common/crypto.go`），channel key 是明文存的。上游 MCP 凭证不能沿用 —— 它能操作对方的 GitHub / Notion / 数据库。

```
CRYPTO_SECRET --HKDF-SHA256(label="mcp-server-secret-v1")--> 32B key
secret       --AES-256-GCM-------------------------------▶ v1:base64(nonce||ct)
```

- 用标准库 `crypto/aes` + `crypto/cipher`，不新增依赖。
- API 永不回明文：只回 `has_secret: true` + `secret_hint`（末 4 位）。要改只能覆盖，不支持读回。
- 审计 / 日志只记 `secret_hint`，header 类字段按 **key 名**脱敏（值根本不进日志）。
- **解密失败必须硬失败**，不能回落成空凭证。否则轮换 `CRYPTO_SECRET` 后，所有服务会在无凭证状态下请求上游 —— 运气好是 401，运气不好是打到了某个允许匿名的内部服务。

### 9.3 不做 OAuth（P0）

部分服务（GitHub 官方 MCP 等）走 OAuth 2.1 动态客户端注册。SDK 有 `OAuthHandler` 钩子，但接入需要授权码回跳、token 刷新、每用户独立授权。

P0 只支持静态凭证（PAT / API key）。**接入文档要写明**，否则用户会拿 OAuth-only 的服务来试，然后卡在"为什么没有授权按钮"。

## 10. 安全

### 10.1 SSRF

管理员填的 URL 由服务端发起请求，是教科书级 SSRF。

- 强制走 `service.newProtectedFetchHTTPClient()`（复用 `common.SSRFProtection`）。
- `allow_private_ip` 默认 `false`。打开需要二次确认 + 写审计 —— 这是有意留的口子（自建内网 MCP 是真实需求），但要留痕。
- **不跟随重定向**，或最多 1 跳且对目标重新做私网校验。跟随重定向等于把防护绕过去。
- 响应体上限：`tools/list` 1 MiB，`tools/call` 8 MiB。超限截断并标 `isError`。

### 10.2 工具级开关

`annotations.readOnlyHint` 是**上游自述，不可信**，但它是唯一信号，不能不用。

| 注册时的情况 | 默认暴露 |
|---|---|
| 全部工具 `readOnlyHint=true` | 全部启用 |
| 存在写工具 / 无 annotations | 只启用 readOnly 的，写工具需管理员逐个勾 |

面板对写工具显示"上游声明该工具可能产生副作用"，`destructiveHint=true` 加重提示。**不做名字推断**（`get_` / `list_` 开头就当只读）—— 猜错的代价是数据。

### 10.3 审计

| action | 时机 | params |
|---|---|---|
| `mcp.server_create` | 新建 | alias、endpoint、transport、has_secret |
| `mcp.server_update` | 更新 | alias、changed（只含真正变的键） |
| `mcp.server_probe` | 探测 | alias、ok、duration_ms、error_kind |
| `mcp.server_refresh` | 刷新 | alias、tool_count、added/removed/changed |
| `mcp.server_delete` | 删除 | alias |
| `mcp.tool_toggle` | 工具开关 | alias、tool、enabled |
| `mcp.tool_price` | 改价 | alias、tool、from、to |
| `mcp.external_call` | 调用明细（`log_calls` 开时） | alias、tool、status、duration_ms、caller |

`mcp.tool_price` 必须独立成一条：定价变更是最需要事后追溯的操作，混在 `server_update` 的 `changed` 里会被淹没。

`mcp.external_call` 量与 LLM 日志不同级（可能更高频），**默认关闭**。开启时只记参数**键名**，不记值 —— 工具参数里可能有文件内容、密钥、用户数据。

## 11. 失败模式

| 场景 | 对外表现 | 计费 | 内部处理 |
|---|---|---|---|
| 上游超时 | `isError` + 可读原因 | 退 | 不重试，不降级到别的服务 |
| 上游 401 | 探测/刷新失败 | 退 | 标 `degraded` + `LastError`，工具仍可见 |
| 上游返回非法 JSON | 协议错误（脱敏） | 退 | 原文进系统日志，不回给客户端 |
| 上游 `isError=true` | 原样透传 | **计** | — |
| 快照从未成功 | 该服务工具不出现 | — | 面板显示"从未成功刷新" |
| 刷新连续失败 3 次 | 工具仍在（旧快照） | — | `degraded`，面板标红 |
| 上游删了某工具 | 该工具消失 | — | 审计记录 removed |
| 工具被管理员禁用 | 列表不可见 | — | 直调返回"工具不存在"，不泄露存在性 |
| 解密失败 | 探测失败 | — | 硬失败，不回落空凭证 |
| 余额不足 | 403 + 剩余额度 | 不计 | 预扣失败即拒，不发出请求 |

「工具不存在」与「工具被禁用」对外必须**同一条错误**。区分会让攻击者能枚举出平台上有哪些服务。

## 12. API

全部挂 `middleware.AdminAuth()`，PAT 需要 `mcp:read` / `mcp:write` scope。

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/mcp/servers` | 列表（含状态、工具数、最近刷新、当日消耗） |
| POST | `/api/mcp/servers` | 新建（`enabled=false` 起步） |
| GET | `/api/mcp/servers/:id` | 详情（不含凭证） |
| PUT | `/api/mcp/servers/:id` | 部分更新（secret 省略 = 不改） |
| DELETE | `/api/mcp/servers/:id` | 删除，连带快照 |
| POST | `/api/mcp/servers/:id/toggle` | 启停 |
| POST | `/api/mcp/servers/:id/probe` | 同步探测（8s 上限） |
| POST | `/api/mcp/servers/:id/refresh` | 同步刷新快照 |
| GET | `/api/mcp/servers/:id/tools` | 工具快照（含 enabled、价格、调用次数） |
| PUT | `/api/mcp/servers/:id/tools/:toolId` | 工具开关 / 改价 / 重置为继承 |
| PUT | `/api/mcp/servers/:id/pricing` | 批量定价（服务默认价 + 按工具覆盖） |
| GET | `/api/mcp/servers/:id/usage?days=7` | 调用次数、失败率、P95 耗时、消耗额度 |
| GET/POST | `/api/mcp/toolsets` | 工具集 |

`POST` 新建后**不自动启用**，`DefaultPrice` 默认 `nil`（免费）。两条都是"先接进来看清楚，再决定收费"：与 `profiles` 的 `by-model` upsert（"声明即上线"）相反 —— 模型声明是 apiduo 自己的资产，外部服务是第三方代码。

## 13. 分期

| 期 | 内容 | 验收 |
|---|---|---|
| P0 | 两张表 + 管理 API + probe + 聚合 `tools/list` / `tools/call` + 直通端点 + **计费（按次 + 预扣 + 退款 + 流水）** | 接一个真实服务完成调用；账单页出现 `mcp/` 行且金额正确；余额不足时请求被拒且未发出 |
| P1 | 分组可见、工具级开关与定价、限流、审计、toolset | 不同分组看到不同工具集；改价后历史账单不变；禁用工具直调返回不存在 |
| P2 | 按上游声明用量计费、BYO（用户级服务）、OAuth、客户端池 | 复杂计价可对账；用户能在自己空间接入私有服务 |

P0 工作量估算：转发内核 ~400 行（SDK 承担大部分）、快照与刷新 ~250 行、凭证加密 ~120 行、计费 ~180 行、管理 API ~400 行、测试 ~800 行。

**侵入现有代码的两处**：

1. `mcp/protocol_server.go` 的 `protoOnce` 单例。现在 `getServer` 返回同一个 server 实例，要按令牌过滤工具就必须改成"按可见集合指纹缓存实例"：

   ```go
   key := visibleSetFingerprint(groups, userId)   // 例如 "g:default,vip|u:0"
   srv := cache.GetOrBuild(key, func() *mcp.Server { ... })  // TTL 5min
   ```

   不能每请求 `mcp.NewServer` + 注册 N 个工具 —— 构造虽轻，但工具数上百时反射注册开销可观。缓存实例的代价是"改配置后 5 分钟内不生效"，通过配置变更时主动失效解决。

2. `service/billing.go` 新增 `PreConsumeForTool`（§4.4），约 20 行，不改原有路径。

其余全部是新文件，`router/main.go` 无需再动（`Register` 内部挂新路由）。

## 14. 反对意见（这个方案可能是错的）

**1. "网关的价值可能不成立"**
只做转发，用户完全可以让 Cursor 直连外部 MCP，为什么付你钱？统一鉴权、统一账单、统一审计，对企业有价值；对个人开发者，价值接近 0 —— 他会直连。
→ 真正的差异化在于"MCP 后面接的是 apiduo 的模型能力"，即支持 sampling（上游反向调模型）。但那条路要处理双向 JSON-RPC、客户端能力协商、模型计费归属，工作量是本文的两倍。**如果目标客户是个人，这个方案打不赢；是企业，才成立。**

**2. "按次计费会扭曲上游真实成本"**
一个 `search` 可能内部跑 5 次 LLM，一次 `list` 不花钱，都收 1 次的钱，长期看定价与成本脱节。
→ 缓解：工具级定价允许人工纠偏；P2 支持按上游 `_meta` 声明用量。但根本问题是 MCP 没有标准 usage 字段，这件事无法在协议层解决。

**3. "聚合层会长期落后于 MCP 规范"**
结构性问题。规范每半年加一组能力（elicitation、MRTR、tasks），聚合层永远只实现 `tools`。
→ 缓解：直通端点是逃生舱。这是双轨设计的全部理由。

**4. "凭证代持是单点"**
DB 泄露 = 所有第三方账号沦陷。加密只防"拖库直接读"，防不了"拿到进程内存 + CRYPTO_SECRET"。
→ 缓解：接入文档要求最小权限 token（只读 scope、单仓库 scope）；`secret_hint` 让人能识别是哪把钥匙；提供"一键清空所有凭证"（P1）。

**5. "工具数量会毁掉 agent 的选择质量"**
比安全风险更早发生，接第 5 个服务就撞上。
→ 缓解：toolset + 默认不暴露写工具。但 toolset 靠人工维护会腐烂（新工具默认不进任何 toolset，于是没人发现它存在）。长期可能需要 meta-tool（`tools.search` 按语义检索），那是另一个产品决策。

**6. "2 RTT 会让高频工具变慢"**
`get_current_time` 这类工具，200ms × 2 是荒谬的。
→ 如果用户真的高频调用轻量工具，说明它不该走网关，应该在客户端本地实现。这不是缺陷，是边界。

**7. 反方向：为什么不直接用现成的 MCP gateway？**
`mcp-proxy` / `mcpo` / bifrost 之类解决的是"协议转换 / 单机代理"，没有一个能同时提供 apiduo 令牌体系、分组倍率、订阅额度、统一账单。复用它们的成本低于自建，但会多出一套并行的身份与账务体系 —— 这与 `mcp/README.md` 开篇"不复制 relay 的渠道选择与计费"是同一判断。

## 15. 落地第一步

不是写代码，是**接一个真实服务把计费闭环跑通**。顺序：

1. 找一个支持 Streamable HTTP + 静态 Bearer 的官方服务。
2. 手工 `curl` 走完 `initialize` → `tools/list` → `tools/call`，确认形状。
3. 把这三步固化成 `probe` 的测试用例，再写转发内核。
4. **先跑"免费"再跑"收费"**：同一工具先用 `price=nil` 调通，再设价调一次，比对 `logs` 行的 `quota = price × group_ratio`、`mcp_price` 快照、余额变动三者一致。

跳过第 2 步直接写代码，会在 SSE 解析和 session 语义上返工两次；跳过第 4 步，会在上线后第一次对账时发现金额对不上 —— 那时已经有真实用户账单了。
