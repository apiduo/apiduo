# 能力端点方案：搜索、文件解析与三类调用形态

> 2026-11-20。承接 `endpoint-registry-design.md`（端点 = 一条 Spec）与
> `uapi-route-model-design.md`（端点维度选路）。本篇解决的是下一层问题：
> 新增一类「模型能力」时，**契约、供给、授权、计价**分别归谁。

---

## 0. 结论

1. 「放渠道还是放全局」是个假二分。四个维度的变更频率差一个数量级，混在一处必然
   有一类变更要付解释成本。
2. **渠道内的第二张能力表已经被证否过一次**（`file_relay.purposes` 退役，
   `model/apiduo_endpoint_models.go` 是它的迁移）。不要造第二次。
3. **搜索必须 family 化**：`@tools/search/<engine>`。现有 `deepseek-search` 是
   legacy 单例，加第二家供应商会撞名，而路由名同时是计费名。
4. **family 端点不能用 `Spec.Price` 计价。** `builtin_billing.go:45` 用 `spec.Model`
   作 map 键，family 的键是前缀，请求时的 model 是完整名，永远查不到。价格必须由
   渠道模型定价承担，且要在 Spec 上加校验，否则静默失效。
5. 「两者都要」的真相：**上游内嵌工具已实现，网关代理执行不存在**。而且不建议把它
   做成 agent loop —— 给不支持 hosted tool 的国产模型加搜索，正解是预取注入。
6. **红线：同一能力不能有两套价格。** 上游 hosted tool 走加价表；网关代理搜索
   不再加价（成本已在端点价里）。判据是「本次请求是否由网关发起过内部搜索调用」。

---

## 1. 现状盘点：四条路，一条已死

| 路径 | 机制 | 供给 | 计价 | 状态 |
|---|---|---|---|---|
| 独立端点 | `pkg/endpoint.Spec` → 路由名 → `abilities` | 渠道模型名单 | `Spec.Price` | 已落地 |
| 上游 hosted tool | chat 请求 `tools[]` 透传给上游 | 渠道类型是否支持 | `tool_price_setting` 加价 | 已落地 |
| 文件解析 | `/v1/files` 的 `<purpose>` family | 渠道模型名单 | 按 MB 预留 + 退款 | 已落地 |
| 渠道内能力表 | `other_settings.file_relay.purposes` | — | — | **已退役** |

还有一个不能动的旁支：`POST /v1/alpha/search`（`relay/alpha_search_handler.go`）
是有 model 的，服务于 codex 类客户端，属于 OpenAI 协议的一部分。本方案不碰它。

四条路的共同结论：**只有「供给」需要渠道，其余三个维度已经在全局。**
这不是设计偏好，是 `abilities` 的主键结构决定的 —— 它是 `(group, model, channel_id)`。

---

## 2. 判据：一个能力该不该做成渠道

只看一件事：**是否存在多个供给方，且供给方之间有选路价值。**

| 情况 | 归属 | 理由 |
|---|---|---|
| 多供应商、key 不同、单价差 3 倍以上 | 渠道 | 分组分流、权重、成本控制都需要选路 |
| 多供应商但完全同质 | 渠道（单条） | 保留换供应商的能力，成本零 |
| 本地确定性计算（文本抽取、格式转换） | 站内执行器 | 无 key、无上游、无供给方差 |

**假渠道是负资产。** 一个没有 base_url、没有 key、不发上游请求的渠道行，会让
「渠道」这个词失去可解释性，还会在 `abilities` 里留下永远健康度良好的幽灵行，
掩盖真实的供给不足。

搜索属于第一类：DeepSeek `$0.75/1K`、智谱 `0.01–0.05 CNY/次`、Bing `$14/1K`。
价差 6 倍以上，必须能按用户组分流。
文本抽取属于第三类：现有 `service/filerelay` 的 `file-extract` 已经是它的形态，
不要再包装成端点。

---

## 3. 端点形状：统一，不按 provider 分裂

网关的职责是屏蔽上游方言。chat completions 屏蔽了 40 家上游，搜索没有理由例外。
已有两条并行搜索路径（`/v1/tools/search` 与 `/v1/alpha/search`）是历史包袱，
不要再加第三条。

统一形状（`/v1/tools/search` 这一族）：

```json
POST /v1/tools/search
{
  "query": "…",
  "max_results": 5,
  "engine": "zhipu-std"
}
```

`engine` 的语义是**选择器，不是协议**。它不进响应体，不改变字段名，不同 provider
的返回差异由 adaptor 归一化（URL / title / snippet）。

### 3.1 engine 缺省怎么办

| 阶段 | 缺省行为 | 理由 |
|---|---|---|
| P0 | 走 `deepseek-search`（现状） | 零破坏，现有客户端行为不变 |
| P1 | 读全局 option `tools.search.default_engine` | 这是**策略**不是能力表，一个字符串，允许运营改 |

「默认引擎」放全局是合理的，因为它不描述「谁能服务」，只描述「同等条件下优先选谁」。
把它写进渠道设置反而会让默认值随渠道数量变得不可预测。

### 3.2 engine 名的开放度 —— 建议半开放（未决）

| 方案 | 优点 | 缺点 |
|---|---|---|
| 封闭 enum（像 files 的 7 个 purpose） | 拼错在客户端就暴露 | 搜索供应商长尾长，每加一家要发版 |
| 半开放：自由字符串 + 推荐清单 | 运营可自建 provider | 拼错是 503 不是 400 |
| 完全开放 | 零约束 | 无发现性，等于没有目录 |

建议半开放，理由是 files 的 purpose 封闭是因为**它是 OpenAI 协议的一部分**，
engine 不是。两者不一致是合理的，不是疏漏。

缓解拼错：503 的响应里带上该用户组可用的 engine 列表（一次
`SELECT DISTINCT model FROM abilities WHERE group_name=? AND model LIKE '@tools/search/%'`）。
这条查询要进 P1 的缓存，否则每次错误都打一次库。

---

## 4. 计价：family 不能用 `Spec.Price`

代码事实（`setting/billing_setting/builtin_billing.go`）：

```go
for _, spec := range endpoint.All() {
    if spec.Price == "" { continue }
    builtinBillingExpr[spec.Model] = spec.Price
}
```

这是精确键查找。非 family 的 `spec.Model` 就是请求时的 model，能命中；
family 的 `spec.Model` 是前缀 `@tools/search/`，而请求时的 model 是
`@tools/search/zhipu-std`，**永远查不到，且没有任何报错**。

规则：

| Spec 形态 | `Price` 字段 | 价格来源 |
|---|---|---|
| 非 family（`deepseek-search`） | 可用 | `Spec.Price` 内置表达式 |
| family（`@tools/search/<engine>`） | **必须为空** | 渠道模型定价（`model_pricing_config`） |

并加一条断言：`Family && Price != ""` 视为配置错误，启动即报。这条断言的价值
在于 —— 这个错误在 UI 上完全静默，配了价、不生效、没人发现，正是
`file_relay.purposes` 那类故障的同一形态。

---

## 5. 两条调用路径的价格单点

| 执行方 | 谁付上游 | 网关怎么收 |
|---|---|---|
| 上游 hosted tool（OpenAI `web_search`、Gemini grounding） | 上游，含在 tool 调用里 | `tool_price_setting` 加价，按次 |
| 网关代理（预取注入） | 网关，走端点选路 | 端点价，按次；模型只收 token |

**红线**：代理执行那次搜索不再写 `tool_price_setting` 的加价行。
否则同一个搜索被收两次（一次端点价、一次加价）。

判据：本次请求是否由网关发起过对 `/v1/tools/search` 的内部调用。
实现上不要用「有没有写加价行」来判断，要显式记账 —— 加价行是结果，不是状态。

---

## 6. 三期

### P0 · 搜索 family 化

这是唯一需要动现有代码的一期，因为 `deepseek-search` 的 handler 是按 DeepSeek
形状写死的（`relaykit/dto/deepseek_tools_request.go`）。family 化等于把 handler
抽成 `searcher` 接口，DeepSeek 成为第一个实现。

改动点（与新增端点同构，5 处）：

| # | 位置 | 改动 | 进合并面 |
|---|---|---|---|
| 1 | `relaykit/types/endpoint_type.go` | 新增中性能力类型 `tools-search` | 是 |
| 2 | `constant/endpoint_type.go` | 加别名 | 是 |
| 3 | `pkg/endpoint/registry.go` | 加 family Spec，`Price` 留空 | 否 |
| 4 | `relay/channel/<provider>` | 实现 searcher；渠道 preset 认领路由名 | 是（仅 deepseek 一家必改） |
| 5 | 前端渠道模型名单 | 推荐项 + 定价页展示 | 否（`web/` 自有） |

### 6.1 能力类型：复用 `deepseek-tools-search`（已定，2026-11-20）

反转了初稿的「新增 `tools-search`」。初稿理由是「能力名带 vendor，渠道声明时会误解」
—— 该理由不成立：**渠道不声明能力**，能力是 `FixAbility` 从模型名推导的
（`common/endpoint_type.go:13` → `endpoint.EndpointTypes`）。

更关键的事实：**能力类型不参与任何选路决策。**

| 位置 | 行为 |
|---|---|
| `model/channel_constraint.go:149` | 非 advanced-custom 渠道的 `FilterRequestPath` 直接 `return true`，不读能力类型 |
| `model/channel_constraint.go:157` | advanced-custom 走 `SupportsIncomingPath(RequestPath)`，按**路径字符串**匹配 |
| `controller/model.go:181` | 能力类型只喂给 `supported_endpoint_types`，而 `IsSynthetic` 已把 `@` 名与 legacy 名挡在 `/v1/models` 之外 |
| `web/src/features/pricing/constants.ts:78` | 定价页的 `ENDPOINT_TYPES` 没有这个值，前端直接忽略 |

所以复用与新增功能等价，差别只在改动量与命名：

| | 复用 | 新增 |
|---|---|---|
| `abilities.endpoint` 存量行 | 不动 | 需迁移 |
| `constant/endpoint_type.go` 别名 | 无 | +1 |
| `endpoint_defaults.go` 的 map | 无 | +1 |
| `advancedCustomEndpointTypeFromIncomingPath` 的 switch | 无 | **必须** +1，否则新类型永远不会被 advanced-custom 渠道产生，成为死代码 |
| 命名 | 带 vendor | 中性 |

代价只剩命名，用**中性别名**消化：在 `constant/endpoint_type.go` 增一个别名常量
`EndpointTypeToolsSearch`，值与 `EndpointTypeDeepSeekToolsSearch` 相同，新代码一律用
别名，`abilities` 列不动。等 legacy 名退役时再改值 —— 那是一次
`UPDATE abilities SET endpoint=...` 加一次常量改值，不是永久成本。

**放弃的东西**：能力维度不再能区分引擎。这本来就不该由它承担 ——
「哪个组能用 bing 搜索」是 model 名（`@tools/search/bing`）的授权问题。

**迁移判据**：`engine` 缺省时行为与改造前逐字节一致（同一批渠道、同一个价格）；
`engine=zhipu-std` 时命中新渠道；DeepSeek 渠道既服务 legacy 名也服务
`@tools/search/deepseek`，两者价格可以不同（legacy 保留旧价，新名按渠道定价）。

### P1 · 能力目录

`GET /v1/capabilities`，**按当前 token/组过滤**。

```json
{
  "capabilities": [
    {
      "name": "search",
      "endpoint": "POST /v1/tools/search",
      "options": [{ "engine": "zhipu-std" }, { "engine": "deepseek" }]
    }
  ]
}
```

三个设计约束：

1. **不复用 `/v1/models`。** `endpoint.IsSynthetic` 必须继续过滤路由名
   （否则给 SDK 一个调不通的 model 名），但「被过滤」和「不存在」是两件事，
   能力需要自己的发现入口。
2. **不给价格数字。** 价格只有定价页一处来源。目录里放价格等于开第二套价格口径，
   而定价页已经有一套 `billing_usage_schema` 的展示契约。
3. **必须缓存。** 复用 `mcp/catalog.go` 的范式（1s TTL + 写路径显式失效）。
   否则每个客户端启动都打一次 `abilities`，而这张表在正常部署里是几十万行。

### P2 · 预取注入（建议立项，未决）

这是「给所有模型加搜索」的唯一低成本路径。上游 hosted tool 只有 OpenAI / Gemini /
xAI 支持，DeepSeek、Qwen、GLM 大多没有 —— 而这批模型才是国内客户的主力。

流程：

1. 请求带 `tools: [{ "type": "web_search" }]`
2. 网关识别 → 取得 query → 内部调 `/v1/tools/search`（同一条选路）
3. 结果注入为一条 `system` 消息（带来源 URL）
4. 从 `tools[]` 删除该项 → 转发上游（上游看不到 hosted tool）
5. 计费：搜索一次端点价 + 模型 token（按 §5 红线，不加价）

query 的两种来源：

| 模式 | 可靠性 | 说明 |
|---|---|---|
| `explicit`：客户端给 `query` 字段 | 高 | 推荐，语义明确 |
| `auto`：取最后一条 user 消息 | 低 | 长对话、多语言、指代会偏 |

**必须承认的边界**：这是「每次都搜」，不是模型自主决定何时搜。要自主决定就必须
有 agent loop，而 agent loop 不在本方案范围内（见 §7）。

### P3 · （不做）网关侧 agent loop

流式响应下的 `tool_call` 累积、并行 tool call、多轮、超时、跨轮计费，每一项都是
独立难题。更关键的是 OpenAI / Anthropic 官方 SDK 已内建 tool loop，网关再做一层
会双跑。现有 `relay/common/tool_usage.go` 的加价模型也明确假设「上游执行」，
改成网关执行等于重写计费语义。

---

## 7. 明确不做

1. **渠道 `settings` 里加能力字段。** 这是 `file_relay.purposes`，已退役一次。
2. **全局「能力 → 渠道 ID」映射。** 绕过 `abilities`，则分组、优先级、权重、
   健康过滤、重试、熔断、消费日志全部要么失效要么重写，还要与渠道配置双写。
3. **按 provider 分裂端点路径。** 那是转发器，不是网关。
4. **网关侧 agent loop。**
5. **能力目录里给价格数字。**
6. **为文本抽取新增端点。** `file-extract` 已覆盖；OCR / 图像理解是视觉模型调用，
   走 chat，不是新端点。

---

## 8. 对抗审查

1. **family + `Price` 静默失效** —— 本节方案的核心风险。断言必须真的在启动路径上，
   只写在单测里等于没有（升级不跑测试）。
2. **`engine` 拼错返回 503 而不是 400** —— 语义上这是客户端错误，但网关无法区分
   「拼错」与「运营还没配这个引擎」。缓解是响应里列可用 engine，不是改成 400。
3. **`abilities` 行数按 (渠道 × engine) 增长。** 搜索 engine 是长尾，比 files 的 7 个
   purpose 更容易膨胀。运营看到的「模型总数」会含非模型条目，已有偏离（见
   `endpoint-registry-design.md` §6.5），本方案会放大它。
4. **能力目录的查询成本。** 必须缓存，且缓存失效要挂在渠道写路径上（`FixAbility`
   之后），不能靠 TTL 自然过期 —— 运营加完渠道立刻刷新看不到，会重复加。
5. **预取注入的 query 提取不可靠。** `auto` 模式在长对话下会搜错东西，而且客户
   很难归因（看起来是模型答得差）。所以 `explicit` 应该是默认推荐路径。
6. **legacy 名需要退役计划。** `deepseek-search` 一旦有渠道在用就不能删。判断方法
   是查渠道模型名单的引用计数，不是查请求量 —— 引用还在就还有升级风险。
7. **定价页仍不展示 `@` 条目**（`endpoint-registry-design.md` §7.2 的悬空项）。
   本方案 P1 让能力「可被发现」，但「可被定价查询」仍缺。两者不是同一件事，
   不要以为加了目录就解决了。
8. **`/v1/alpha/search` 与本族并存。** 客户端看到两个搜索入口会困惑。
   短期靠文档区分（alpha 是 codex 专用），长期取决于 codex 流量的存续。
9. **「能力类型不参与选路」是反直觉的，不要据此推论。** 普通渠道的
   `FilterRequestPath` 直接放行（`channel_constraint.go:149`），排除只来自
   `abilities` 有无该 model 行。所以「渠道没有声明 search 能力」不会让它被过滤，
   「渠道声明了别的能力」也不会。任何需要按能力排除的场景，都得先新增判据，
   而现有代码里没有这个判据。

---

## 9. 落地顺序与判据

| 期 | 交付 | 判据 |
|---|---|---|
| P0 | 搜索 family 化 + 断言 | ① `engine` 缺省行为不变；② 第二家 engine 可路由；③ family Spec 带 Price 启动即失败 |
| P1 | `/v1/capabilities` | ① 按组过滤（A 组看不到 B 组引擎）；② 命中缓存不再打库；③ 渠道新增后立即可见 |
| P2 | 预取注入 | ① 注入后上游收不到 hosted tool；② 只收一次搜索费；③ `explicit` 模式 query 与请求一致 |

每期结束跑：

```bash
go test ./pkg/endpoint/ ./router/ ./service/filerelay/ ./model/ ./controller/ ./middleware/
go run ./tools/markercheck -cost
```

P0 的合并面增量预期：`relaykit/types/endpoint_type.go` 与
`constant/endpoint_type.go` 各一两个标记块，`relay/channel/deepseek/` 内的改动
落在自有文件则不参与合并。若 `-cost` 显示新增行越过预算，说明 searcher 抽象被
放进了上游文件，应移出。

---

## 10. 待决策清单

| # | 问题 | 建议 | 影响 |
|---|---|---|---|
| 1 | ~~能力类型复用还是新增~~ | **已定：复用 + 中性别名**，见 §6.1 | 改动量最小，且不产生死代码 |
| 2 | `engine` 封闭 enum 还是半开放 | 半开放 | 决定新增供应商是否要发版 |
| 3 | P2 预取注入是否立项 | 立项，但排 P0/P1 之后 | 决定国内模型能否获得搜索能力 |
| 4 | 默认引擎放全局 option 还是留在 legacy 名 | 全局 option | 决定运营能否不重启切默认 |
