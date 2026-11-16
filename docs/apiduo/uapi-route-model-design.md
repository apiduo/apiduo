# UAPI 路由 v2：在 new-api 现状上加一个端点维度

> 2026-11-14。只出方案与原型，不改代码。
> **本文替代 v1 的「Route 对象」方案**：v1 引入了新的线路实体，合并面大、运营负担重；v2 按「合并优先」重做，P0 只碰一个上游文件的单个分支。
> 配套原型：`design/uapi-endpoint-routing.html`（现状诊断 / 端点能力 / 覆盖矩阵 / 决策日志 / 合并面）。
> 事实来源：仓库 HEAD 源码 + 生产实例 `newapi-0to1` 只读聚合（30 天窗口）。

---

## 0. 结论先行

1. **你的判断成立**：用户请求的身份是 `(model, endpoint)`；`group` 是我们自己的授权与计价判别，不该参与「这个渠道能不能服务这个请求」的判断。
2. 现状键是 `(group, model)`。它解决「谁能用、按什么价」，但不解决「这个端点由谁来服务」。同一个 `(group, model)` 下的所有渠道，对任意端点都是同一组候选——这就是「同模型三个端点各种错误」的根因。
3. **不需要引入 Route 实体，也不需要改 `abilities` 表**。只要在候选集上补一层**端点能力过滤**，键就变成 `(group, model, endpoint)`；group 仍只做授权与计价。
4. **P0 的合并面**：1 个上游文件（`model/channel_constraint.go`）里的 1 个标记块、约 4 行；其余全部是新增文件，永不参与上游合并。无数据库变更、无迁移、可整块回滚。
5. 端点过滤只能消除「协议族根本不可能服务这个端点」的错误。**同类型渠道里的坏渠道（404/403/账户冻结）属于健康问题，必须另做**，两者共用同一个挂接点。

---

## 1. 事实核对

### 1.1 请求的三个维度分别从哪来

| 维度 | 来源 | 现状用途 |
|---|---|---|
| `group` | 令牌 group，回落到用户 group（`middleware/auth.go:685`） | 授权 + 计价 + 选渠道 |
| `model` | body 的 `model` / Gemini 路径 / 端点默认值（`middleware/distributor.go:423`） | 选渠道 + 计价 |
| `endpoint` | 请求路径（`/v1/messages`、`/v1/responses`、`/v1/images/edits` …） | 只决定走哪个 handler；**不参与选渠道** |

### 1.2 候选集怎么来

`abilities` 主键是 `(group, model, channel_id)`（`model/ability.go:23`）。所以：

```
(group, model) ──► N 条渠道 ──► 取最高优先级层 ──► weight+10 加权随机
```

端点在这一步**完全不出现**。`model/channel_constraint.go:94` 的 `FilterRequestPath` 分支对普通渠道直接 `return true`，只有 advanced custom(58)、advanced async(8001)、图片协议、Responses WS 会真正校验路径。

### 1.3 三个端点为什么互相污染

以一个模型挂在三条不同协议族的渠道上为例：

| 端点 | 渠道 A（Anthropic） | 渠道 B（OpenAI 兼容） | 渠道 C（Gemini） |
|---|---|---|---|
| `POST /v1/messages` | 原生 | 转换 | 转换 |
| `POST /v1/chat/completions` | 转换 | 原生 | 转换 |
| `POST /v1/images/generations` | **不可能** | 取决于上游 | **不可能** |

今天三条渠道对三个端点都是合法候选。`images` 请求有 2/3 的概率落到根本不能出图的渠道上，表现为 400/404/空响应；如果命中 B，还可能因为上游未实现而 404。重试再随机一次，错误形态就变成「时好时坏、每次不一样」。

### 1.4 生产证据（与 v1 相同，未变）

30 天日志按 `model_name` 聚合 `channel_id`：

| 模型 | 命中渠道 | 请求数 | 各渠道错误 |
|---|---|---|---|
| `minimaxH3` | 35 / 44 | 808 | 0 / 0 |
| `flux-3` | 39 / 38 | 23 | 9/9（404）、4/14（403） |
| `doubao-seedream-5-0-260128` | 2 / 42 | 19 | 10/10（500）、9/9（账户冻结） |

- `minimaxH3` 是健康对照组：同模型两条渠道、同协议族、都健康 → 分流无害。
- `flux-3` 与 `seedream` 是两类不同的问题：前者含端点错配（404），后者是渠道本身坏了（500/冻结）。**端点过滤修不了后者**，这点必须说清楚。
- `doubao-seedance-1-5-pro-251215` 另有 3 条渠道：2 条 task_plugin（p10/p9）+ 1 条火山（p1，名为 `seedance-redirect-to-minimaxh3`）。30 天无流量，属已配置未触发的跨模型降级。

### 1.5 已有的端点声明与转换声明（能复用什么）

**每个模型确实有一份端点声明，但它是目录/定价用的，不是路由用的。**

| 层 | 位置 | 内容 | 是否参与选渠道 |
|---|---|---|---|
| 模型元数据 | `model/model_meta.go:42` | `endpoints` JSON，可手工编辑（前端「Custom endpoints」） | 否 |
| 类型推断 | `common/endpoint_type.go:6` | 渠道类型 → 端点类型（带模型名特判） | 否 |
| 汇总 | `model/pricing.go:116` | `GetModelSupportEndpointTypes(model)` | 否，只进定价缓存与模型详情 |
| 自定义路由 | `AdvancedCustomRoute.Models` | 按模型声明 `incoming_path` | 是，但只对 58/62/63 |
| 插件路由 | `plugin.Meta.RelayRoute(path, model)` | 按模型声明 | 是，只对 61 |

两个限制决定了它不能直接当过滤器：

1. `EndpointType` 枚举只有 12 个值，没有 audio / moderations / rerank（除 jina）/ images.edits，视频只有 `openai-video`。它覆盖不了「同模型三端点」里的音频与图像编辑。
2. 函数注释写明「所有的渠道都支持 OpenAI 端点」（`common/endpoint_type.go:5`），默认值就是 OpenAI；`mcp/suggest.go:135` 也只采信「恰好返回一个端点」的类型。把它当能力表会误杀音频渠道。

**转换声明存在，但粒度只到「格式 → 格式」。**

- `relaykit/relayconvert` 注册了 12 条有向转换（OpenAI / Claude / Gemini / OpenAI Responses 两两互转），每条带 quality（good / fair / discouraged）。
- 公开查询只有 `LookupRequestConverter(id)`；按 `(from,to)` 查的 `lookupRequestRoute` 是私有的（`request_registry.go:370`）。
- 没有「渠道类型 → 原生格式」的声明表；那是各 adaptor 在 `ConvertOpenAIRequest` 里硬编码的目标格式（例如 Claude 固定转 `RelayFormatClaude`）。
- 只覆盖文本；图像/音频/视频走 image_protocol、advanced routes、task plugin，不在这张表里。
- 运行时探测不可靠：Claude 的 `ConvertRerankRequest` 直接 `return nil, nil`（`relay/channel/claude/adaptor.go:139`），不报错不等于支持。

**结论**：P0 不把 `EndpointType` 当能力表，只在展示层复用它（模型详情的 `supported_endpoints`）；路由判断用新包里的负向表。要判断「这个渠道能不能转换到某端点」，需要 P2 补一张 `channelType → 原生 RelayFormat` 的映射，再查转换注册表。

---

## 2. 方案：一个挂接点，一张负向表

### 2.1 管线

```
1. 资格   (group, model) → abilities          ← 现有逻辑，不动
2. 正确性 endpoint        → 负向能力表         ← 新增，P0
3. 偏好   priority → weight → affinity        ← 现有逻辑，不动
4. 健康   (channel, model, endpoint)          ← P1，共用第 2 步的挂接点
```

- `group` 只出现在第 1 步和第 3 步的计价比率里，**不参与第 2 步**。
- 端点在第 2 步被解析成稳定的端点族（`chat` / `messages` / `responses` / `images.generations` / `images.edits` / `audio.*` / `video` / `embeddings` / `rerank`），不直接拿路径字符串比较。

### 2.2 负向能力表：只写「确定不可能」

三态：

| 状态 | 含义 | 选择时的行为 |
|---|---|---|
| `native` | 适配器有原生路径 | 通过 |
| `converted` | 适配器能转换 | 通过 |
| `unsupported` | 适配器的 `GetRequestURL` 对该 relay mode 无分支，明确返回错误 | **过滤** |
| 未列出 | 没验证过 | **通过**（保守，保持现状） |

关键取舍：**默认放行，只写负向**。理由有三条：

1. 兼容性：未列出的组合行为与今天完全一致，不会因为一张表没写完就把正常流量打成 503。
2. 可审查：每条 `unsupported` 都能对应到适配器里的一个 `default: return error` 或一个明确的模态缺失，评审时能逐条核对。
3. 可回滚：删掉一条记录就恢复该组合，不需要数据迁移。

首版负向表的依据（逐条来自适配器源码）：

| 渠道类型 | 明确不支持 | 依据 |
|---|---|---|
| Anthropic(14) | images / audio / embeddings / rerank / video | `GetRequestURL` 固定返回 `/v1/messages`，无这些模态的转换路径 |
| Gemini(24) | audio / rerank / video | 音频与重排没有对应转换；视频走 Veo 任务插件 |
| VolcEngine(45) | audio.transcriptions / audio.translations | `GetRequestURL` 的 switch 无这两个分支，落到 `unsupported relay mode` |
| 任务插件(61) / 自定义(58) / 异步(8001) | 不进入本表 | 它们已有自己的路由表声明，重复判断会互相打架 |

> 这张表是**起点，不是终局**。每加一条都要有源码依据 + 一个回归测试；没有依据的组合一律不写。

### 2.3 唯一挂接点

只改 `model/channel_constraint.go` 的 `FilterRequestPath` 分支：

```go
case dto.FilterRequestPath:
    if filter.RequestPath == "" {
        return true
    }
    if !constant.IsAdvancedCustomChannel(ch.Type) && ch.Type != constant.ChannelTypeAdvancedAsync {
        // APIDUO:endpoint-capability:BEGIN
        return endpoint.ChannelServesPath(ch.Type, filter.RequestPath)
        // APIDUO:endpoint-capability:END
    }
    ...
```

- 新逻辑全部在 `pkg/endpoint/`：`FromPath`（路径 → 端点族）、`ChannelServesPath`（类型 + 端点 → 是否放行）、表与测试。
- 该文件已经有 6 个 APIDUO 标记块，再加 1 个块、替换 1 行，不新增「上游文件数」。
- 分发的两条路径（内存缓存 `filterCandidateIDs` 与数据库 `GetChannel`）都会走 `ChannelSatisfiesFilters`，所以只改这一处即可同时覆盖热路径与冷路径。

### 2.4 明确不做的三件事

| 不做 | 原因 |
|---|---|
| 不改 `abilities` 表结构 | 加 `endpoint` 列会让主键变成四元组，上游所有 ability 写入、缓存、后台任务都要跟着改，合并面指数级上升 |
| 不新增 Route / 线路实体 | v1 的方案；运营要维护一层新对象，且线路内部仍可能异构 |
| 不用 `group` 表达端点偏好 | group 已经承担授权/定价/展示三重语义，再加一层会重演「分组选不对」的老问题 |

---

## 3. 合并面核算

当前预算（`.apiduo/merge-budget.txt`）：`files 142 / blocks 426 / added 4054 / deleted 221`。

### 3.1 P0（端点过滤）

| 文件 | 类型 | 改动 | 预算影响 |
|---|---|---|---|
| `pkg/endpoint/endpoint.go` | 新文件 | 端点族与路径解析 | 0 |
| `pkg/endpoint/capability.go` | 新文件 | 负向表 | 0 |
| `pkg/endpoint/*_test.go` | 新文件 | 表驱动测试 | 0 |
| `model/channel_constraint.go` | 上游（已有标记） | 1 个标记块，替换 1 行 | blocks +1，added +3，deleted +1，files 不变 |

预期：**files 仍是 142**；`blocks 427 / added 4057 / deleted 222`。实施后用 `go run ./tools/markercheck -cost` 复核，再决定是否 `-update` 收紧。

### 3.2 可选增强（各自独立，可后置）

| 期 | 文件 | 改动 | 预算影响 |
|---|---|---|---|
| P1 | `service/request_policy.go` | `PolicyEvent` 加 `Endpoint` 字段 | files +1、blocks +1、added +4 |
| P2 | `relaykit/dto/channel_settings.go` | `ChannelOtherSettings` 加 `Endpoints []string`（渠道级覆盖） | blocks +1、added +3 |

P1 只是让日志能回答「这次为什么被过滤」；P2 是给「同类型但上游只实现了部分端点」的渠道一个逃生口（例如 bfl 只实现了一个图片路径）。

### 3.3 回滚

删掉 `pkg/endpoint/`，把标记块恢复成 `return true`。没有表结构变更、没有数据写入、没有配置迁移。回滚成本约为一次提交。

### 3.4 配置项选型：在哪配

四个候选面，按「是否值得为它增加合并面」筛：

| 面 | 存储 | 编辑入口 | 热加载 | 多实例 | 合并面 | 结论 |
|---|---|---|---|---|---|---|
| 代码基线 | 常量 / switch | 发版 | — | 随镜像 | 新文件 0 | **P0 选它** |
| 全局配置 | `options` 表，注册到 `config.GlobalConfig` | 系统设置 →「路由」 | 是 | 是（`BumpSyncTopic(SyncTopicOptions)`） | Go 0；前端 section +1 块 | P1 |
| 渠道级 | `channel.other_settings.endpoints` | 渠道编辑 → 高级设置 | 是 | 是 | `relaykit/dto` +1 块；前端 +1 块 | P2 |
| 模型级 | `models.endpoints`（已有） | 模型元数据 | 是 | 是 | 0 | **不选** |

**P0：代码基线 + 两个环境变量，零配置项。**

| 变量 | 取值 | 默认 | 作用 |
|---|---|---|---|
| `ENDPOINT_CAPABILITY_FILTER` | `on` / `off` | `on` | 总开关；出事不用回滚发版 |
| `ENDPOINT_CAPABILITY_MODE` | `shadow` / `enforce` | `shadow` | 影子模式只记录不拦截 |

P0 先跑 `shadow`：每条请求在决策日志里写「如果 enforce，会过滤哪些渠道」，但不改变结果。跑一两天确认没有误杀，再切 `enforce`。两个变量都在新包里读，不碰任何上游文件。

**P1：结构化全局配置，只追加 deny。**

```json
// options.key = endpoint_capability_setting.rules
[
  {
    "channel_type": 14,
    "deny": ["images", "audio", "embeddings", "rerank", "video"],
    "note": "Anthropic adaptor has no path for these modalities",
    "enabled": true
  }
]
```

- 注册方式：`config.GlobalConfig.Register("endpoint_capability_setting", &Setting{})`；`handleConfigUpdate` 已经支持 `name.field` 前缀热更新，**不需要改 `model/option.go`**。
- 校验放在新文件的 `Validate()` 里，读取时执行：枚举闭集、渠道类型白名单、`(type, endpoint)` 不重复、规则数上限；校验失败回退代码基线并告警，不阻断启动。
- 语义：全局规则**只能追加 deny**，不能删除代码基线；需要例外时走 P2 的渠道级 `allow`，并留下审计记录。
- 多实例：`UpdateOption` 已经调用 `BumpSyncTopic(SyncTopicOptions)`，不需要另做同步。
- 前端：系统设置新增「路由」section（与运营分开，这是正确性配置不是营销配置）。这是 P1 唯一的合并面增量：前端 section registry +1 块。

**P2：渠道级覆盖，解决「同类型不同实现」。**

```json
// channel.other_settings.endpoints
{ "deny": ["images"], "allow": ["audio.speech"] }
```

- `deny` 追加到全局；`allow` 只能豁免 P1 的软规则，不能豁免代码基线。
- 入口：渠道编辑 → 高级设置 → 端点能力（多选）。
- 用途：bfl 与 fal 都是 type 1，但只有 fal 实现 `/v1/images/generations`；逐个渠道声明比维护一张类型表更准。

**为什么不选 `models.endpoints`。**

1. 它已经承担目录展示与定价（`model/pricing.go:295` 只追加、不裁剪），再加路由语义就是两套真相。
2. 模型级表达不了渠道差异：同一个 `flux-3` 在不同渠道上的端点实现不同，问题不在模型。
3. 组合爆炸：338 个模型 × 8 个端点，运营不可能逐个维护。

### 3.5 优先级与灰度

```
env off  >  option enabled=false  >  channel deny/allow  >  global rules  >  code baseline
```

- 任何一层关闭，下一层仍然生效；env 是最后的安全阀。
- `shadow` 模式下所有层都只记录不拦截；切 `enforce` 前先看「would_filter」计数。
- 规则变更后不需要重启：options 热更新 + 多实例同步；代码基线变更才需要发版。

---

## 4. 分期

| 期 | 内容 | 验收 |
|---|---|---|
| P0 | 端点族解析 + 负向表 + 单挂接点 | 构造用例：Anthropic 渠道不再接到 images 请求；未列出组合行为不变 |
| P1 | 决策日志记录 endpoint + 被过滤原因 | 给定一条日志能回答「哪个端点、被谁过滤」 |
| P2 | 渠道级端点覆盖（`other_settings.endpoints`） | bfl 这类「同类型但只实现部分端点」的渠道可被正确排除 |
| P3 | 健康闸门：`(channel, model, endpoint)` 粒度熔断 | 100% 失败的 cell 在 10 分钟内停止接流量；配置类错误需人工恢复 |

P0 与 P1 可以同批发布；P2/P3 各自独立。顺序不能颠倒：没有 P0 的端点维度，P3 的健康度只能做到渠道粒度，仍会把「这个端点坏了」误判成「整条渠道坏了」。

---

## 5. 验收

**单元测试（新包内，表驱动）**

- `FromPath` 覆盖：`/v1/messages`、`/v1/responses`、`/v1/responses/compact`、`/v1/images/generations`、`/v1/images/edits`、`/v1/audio/*`、`/v1/video/generations`、`/v1/videos`、`/v1beta/models/*`、`/mj/*`、`/pg/*`。
- 负向表：每条 `unsupported` 一个用例；未列出组合必须返回 `true`（防止「表没写完 = 全站 503」）。
- 回归：`ChannelSatisfiesFilters` 对 58/8001/61 的行为与改动前一致。

**集成验证**

- 一条 `claude-sonnet-4-5` + `/v1/images/generations` 的请求，在只有 Anthropic 渠道时返回明确的「无可用渠道」，而不是打到上游拿 400。
- 同样的请求在有 OpenAI 图片渠道时，候选集只包含后者。
- `minimaxH3` 的两条插件渠道行为不变（808 次调用的分流比例不变）。

**合并面验证**

```bash
go run ./tools/markercheck          # 无新增违规
go run ./tools/markercheck -cost    # files 142 不变，blocks/added 与预期一致
```

---

## 6. 这套方案解决不了什么

1. **同类型渠道的端点差异**：bfl 与 fal 都是 type 1，但只有 fal 实现了 `/v1/images/generations`。类型级负向表无法区分，需要 P2 的渠道级覆盖或 P3 的健康反馈。
2. **渠道整体坏掉**：seedream 的 500 与账户冻结，与端点无关，只能靠 P3。
3. **跨模型降级**：seedance → MiniMax H3 是 fallback 策略问题，不是端点问题；在 P3 之前，建议先核实并下线该类渠道。
4. **上游模型本身的质量波动**：不在路由层解决。

把「端点错配」「渠道坏掉」「跨模型降级」分成三类处理，是这份方案与 v1 最大的区别：**一个挂接点只负责一件事**，每类问题都有独立的开关和回滚路径。

---

## 附录 A：渠道类型 → 文本格式映射（评估结论：P0 不做）

按适配器族分组，不按 60 多个类型逐个列：

| 家族 | 代表类型 | 适配器接受的文本格式 | P0 负向 |
|---|---|---|---|
| OpenAI 兼容 | 1/3/6/8/9/10/12/13/19/20/21/22/26/27/40/42/43/44/47/48/53/59/60 | OpenAI；Claude / Gemini / Responses 由转换注册表先转 | 不设 |
| Anthropic 原生 | 14/25/33 | Claude；OpenAI / Gemini / Responses 由注册表先转 | images / audio / embeddings / rerank / video |
| Gemini 原生 | 24/41 | Gemini；其它由注册表先转（41 按模型可为 Claude） | audio / rerank / video |
| 厂商原生 | 4/11/15/16/17/18/23/34/35/36/38/39/49/50/51/52/54/55/56/57 | OpenAI（各自 `ConvertOpenAIRequest`）；Responses 多数未实现 | 默认放行，逐厂商补 |
| 路由表 / 插件 | 58/61/62/63/8001 | 由配置声明 | 已有路由表，不重复判断 |

三条判据：

1. 文本四格式两两互通（注册表 12 条有向边），所以「渠道能不能收某种文本格式」几乎总是能成立；真正的缺口在**入口方法未实现**，例如 Cohere 与 Jina 的 `ConvertOpenAIResponsesRequest` 直接返回 `not implemented`（`relay/channel/cohere/adaptor.go:62`、`relay/channel/jina/adaptor.go:65`）。
2. 这类缺口可以用 `grep "not implemented"` 机械枚举，不需要人工维护一张正矩阵。
3. 它们集中在文本端点（Responses / compact），不是这次误判的主因；主因是图片、音频、视频的模态错配。

**结论**：P0 不做这张映射。若 P2 要做，产物应是「适配器 × 端点 → 未实现」的**负向清单**，而不是「类型 → 格式」的正向表；生成方式是从适配器源码提取 `not implemented` 分支，再补回归测试。这样既不会漏，也不会因为一张没写完的白名单把正常流量挡掉。

---

## 7. 互转实测与「很兼容」的差距（2026-11-14 探针）

**方法**：在 `relaykit/relayconvert` 内加一个临时探针测试，直接调用 `ConvertRequest` / `ConvertResponse` / 流式转换，使用包内既有 golden 夹具（含图片、工具定义、工具调用与工具结果）。跑完即删，工作区无 Go 变更；`go test ./relayconvert` 全绿。

### 7.1 转换层：6×6 全通

| 层 | 方向数 | 结果 | 工具保留 |
|---|---|---|---|
| 请求 | 6 | 全通，0 诊断 | 是 |
| 响应 | 6 | 全通 | 是 |
| 流式（文本 chunk） | 6 | 全通 | 夹具未含工具增量，未验 |

范围：`chat` / `responses` / `messages` 三种格式，双向。

### 7.2 渠道层：responses 是短板

按适配器扫描三种入站方法的实现，`not implemented` / `not supported` 的：

| 入站 | 未实现的适配器 | 数量 |
|---|---|---|
| chat | codex、mokaai、replicate | 3 |
| messages | codex、coze、jimeng、replicate、submodel | 5 |
| responses | aws、baidu、baidu_v2、cohere、coze、dify、jimeng、jina、minimax、mistral、mokaai、palm、replicate、siliconflow、submodel、tencent、vertex、xunfei、zhipu | 19 |

其中 codex 是 responses-only（chat/messages 拒绝是设计），jimeng/jina/replicate 不是常规文本渠道；但 **AWS、Vertex、MiniMax、Mistral、SiliconFlow、智谱、腾讯、百度、Cohere、Dify、讯飞、PaLM、MokaAI** 是文本渠道，属真实缺口。

### 7.3 「很兼容」的实现路径：中央兜底，不是逐个补 19 个适配器

现状已有单向兜底：chat 客户端 → responses-only 渠道，由 `ShouldChatCompletionsUseResponsesPolicy` + `textRequestViaResponses` 完成。缺的是反向：

```
responses 客户端 → 渠道未实现 responses → 转成 chat → 走 ConvertOpenAIRequest → 响应再转回 responses
messages 客户端  → 渠道未实现 messages  → 转成 chat → 走 ConvertOpenAIRequest → 响应再转回 messages
```

三点设计约束：

1. **只在「未实现」时兜底**：用哨兵错误区分 `not implemented` 与真实转换失败；后者不能兜底，否则会把参数错误吞成降级。
2. **状态化字段显式拒绝**：`previous_response_id`、`store`、reasoning item 等无法无损转成 chat；命中这些字段时返回明确的 400，而不是静默降级。
3. **兜底必须可见**：日志写 `conversion_path=native|fallback`，响应头回传实际路径；质量按 `fair` 标注。

合并面：2 个挂接点（`relay/responses_request.go` 与 `relay/claude_handler.go`）+ 1 个新错误判定文件 + 兼容矩阵测试；不改 19 个适配器。高价值渠道（AWS、Vertex、智谱）可以后续单独补原生实现，兜底作为保底。

### 7.4 兼容矩阵应成为 CI 守卫

按「适配器 × 3 端点」生成测试：每个格子必须满足 `native` 或 `fallback` 至少一条路径可用；只有明确声明不支持的端点（如 codex 的 messages）才允许 `reject`。这样新增适配器、改转换器、改兜底策略都会被这张表拦住，而不是等到用户报 400。

---

## 8. 端点转换策略：高级路由的泛化（设计草案）

### 8.1 现状：高级路由已经能配转换，但只覆盖一小块

`AdvancedCustomRoute` 的形态是 `incoming_path + models[] + converter + upstream_path`（`relaykit/dto/channel_settings.go:217`），已经支持「按模型声明某个入站端点走哪个转换器」。限制有三条：

1. **只对 advanced custom 渠道生效**（58/62/63），普通渠道（1/14/24/45…）没有这层配置，转换写死在适配器里。
2. **单跳、无兜底**：一条路由一个 converter，没有「直连没有就走两跳」的策略。
3. **转换器枚举是注册表的子集**：UI 只暴露 9 条文本转换，`responses → claude`、`gemini ↔ claude` 在注册表里有，但配置里选不到。

### 8.2 建议的配置模型：少一个维度

不需要让人选「从哪个端点转换」。转换的源永远是客户端请求的端点，目标永远是渠道的原生端点；中间走哪条路由可以由注册表算出来。需要人决定的只有三件事：

| 决策 | 取值 | 默认 |
|---|---|---|
| 原生端点 | `chat` / `responses` / `messages` / … | 由渠道类型推导 |
| 非原生端点怎么办 | `convert` / `reject` | `convert`（有路径且质量达标时） |
| 质量下限 | `good` / `fair` / `discouraged` | `fair` |

配置形态（渠道级，按模型覆盖）：

```json
// channel.other_settings.endpoint_policy
{
  "native": "chat",
  "allow_convert": ["responses", "messages"],
  "quality_floor": "fair",
  "models": {
    "deepseek-flash": { "native": "chat" },
    "gpt-5": { "native": "responses", "allow_convert": ["chat"] }
  }
}
```

优先级：`models[精确名]` > `models["*"]` > 渠道默认 > 代码默认。UI 上不需要下拉选 `via`，只显示自动计算出的路径与质量，例如 `responses → chat → native（fair）`；需要覆盖时才展开。

### 8.2.1 配置面：管理员 / 渠道，不是终端用户

这条策略**不对终端用户暴露**。用户只发 `model` 与端点，不需要知道中间走了哪条转换路径。

| 层 | 谁配 | 存哪 | 作用 |
|---|---|---|---|
| 渠道级（主） | 管理员 | `channel.other_settings.endpoint_policy` | 这个渠道的原生端点、允许转换的端点、质量下限、按模型覆盖 |
| 全局默认（可选） | 管理员 | `endpoint_policy_setting.*`（系统设置 → 路由） | 按渠道类型给默认值，避免逐渠道重复配 |
| 适配器推导（兜底） | 代码 | 新包内的类型 → 原生端点表 | 没有配置时的默认值 |

优先级：**渠道级 > 全局默认 > 适配器推导**。用户侧最多在模型详情里看到只读的「支持端点 / 转换质量」标签，没有任何可编辑项。

为什么主配置放渠道级：

1. 原生能力是渠道实现与上游的属性，不是模型目录的属性；同一模型在 bfl 与 fal 上可以不同。
2. 与现有渠道配置同构（`primary_endpoint`、`image_protocol`、`advanced_routes` 都在 `other_settings`），保存时能一起做校验。
3. 放模型级会与 `models.endpoints` 的目录/定价语义冲突；放用户级则把正确性问题推给了调用方。

### 8.3 兜底放在两处，不要放在「换格式重试」

1. **选择期兜底**：候选渠道分两级——`native` 优先，`convertible` 其次；同级内仍按 priority / weight。这样「上游支持就透传」是排序结果，不需要每次请求试错。
2. **失败期兜底**：转换类失败标记为可重试，重试时优先换 `native` 渠道；同一条转换路径不重复尝试。
3. **明确拒绝**：`previous_response_id`、`store`、reasoning item 这类状态化字段无法无损转成 chat，命中即 400，不做运行时降级。否则会出现「同一个会话第二次请求突然失忆」这种最难排查的故障。

### 8.4 与 P0 的关系

P0 的负向表是这张策略表的退化形态：只有 `native / unsupported` 两态。加上 `convertible` 之后：

- 端点过滤从「能不能」变成「怎么服务」；
- 候选排序从「同质随机」变成「原生优先、转换兜底」；
- `AdvancedCustomRoute` 的转换器枚举可以逐步对齐注册表，不需要再维护第二套。

不建议的做法：把 `via` 做成用户必填；把策略写进 `models.endpoints`（那是目录/定价）；在运行时反复换格式重试（工具调用与计费会重复）。

---

## 9. 火山 DeepSeek 端到端验证（2026-11-14）

**对象**：`apiduo.cn`（生产，容器 `apiduo-cn` 0.0.137）。两条渠道恰好是 §8 所述「原生 + 转换」的现成实例：

| 渠道 | 模型 | `primary_endpoint` | `/v1/chat/completions` | `/v1/responses` | `/v1/messages` |
|---|---|---|---|---|---|
| 15 `ark-deepseek-v4-1-flash` | `deepseek-v4-1-flash-economy-261017` | `/api/v3/chat/completions` | 透传 | 转 chat | 转 chat |
| 16 `ark-deepseek-v4-1-flash-responses` | `deepseek-v4-1-flash-261017` | `/api/v3/responses` | 转 responses | 透传 | 转 responses |

验证方式：临时令牌（`e2e-endpoint-probe`，user 1，`default`），从服务器本机请求 `https://apiduo.cn`，全部跑完即软删除令牌。共 24 个请求。

### 9.1 主链路：全部通过

| 用例 | 渠道 15 | 渠道 16 |
|---|---|---|
| chat 非流式 | 200 / 2.93s | 200 / 1.41s |
| responses 非流式 | 200 / 2.92s | 200 / 2.78s |
| messages 非流式 | 200 / 2.89s | 200 / 1.78s |
| chat 流式 | 200，19 帧 + `[DONE]` | 200，21 帧 + `[DONE]` |
| responses 流式 | 200，22 帧，`response.created`…`response.incomplete` | 200，26 帧，同族 |
| messages 流式 | 200，21 帧，`message_start`/`message_stop` 完整 | 200，18 帧，同族 |
| 工具调用 chat | 200，返回 `tool_calls` | 200，返回 `tool_calls` |
| 工具调用 responses | 200，`output` 含 `function_call` | 200，`output` 含 `function_call` |
| 工具调用 messages | 200，`tool_use` + `stop_reason=tool_use` | 200，`tool_use` + `stop_reason=tool_use` |

结论：**高级路由的转换在三种常用端点上可用**，工具调用与流式在转换路径上均未丢失。response id 会暴露路径：原生回 `resp_…`，转换回 `chatcmpl-…`。

### 9.2 四条真实缺陷

| # | 现象 | 路径 | 严重度 |
|---|---|---|---|
| 1 | `store=true` 被**静默改为 false**，回包 `store: false`、`status: incomplete`，无任何告警 | responses → chat（渠道 15） | 高 |
| 2 | 参数不支持时返回 **500** 而非 4xx（`n>1 is not supported in responses compatibility mode`、`does not support stateful fields: previous_response_id`） | 转换路径 | 中 |
| 3 | `n=2` 在**透传**路径只回 1 个 choice，不报错（静默降级） | chat 透传（渠道 15） | 中 |
| 4 | 透传返回两个 `tool_calls`，第二个 `arguments` 为空字符串，客户端 `JSON.parse("")` 会抛异常 | chat 透传（渠道 15） | 低（上游行为） |

缺陷 1 与 2 是同一个根因的两面：**状态化字段在转换路径上要么被静默吞掉，要么以 500 形式拒绝**。§8.3 已经写明约束「命中即 400，不做运行时降级」，实测确认当前实现违反该约束，需要在转换入口统一拦截：

```
store / previous_response_id / include[] / reasoning item
  → 请求携带且当前路径无法无损表达 → 400 invalid_request_error（不是 500）
```

缺陷 2 的错误码 `convert_request_failed` 语义是「客户端参数不被目标格式支持」，属 `invalid_request_error`，必须映射为 400，否则 SDK 会按 5xx 自动重试，把参数错误放大成流量。

### 9.3 日志：端点与转换链已经存在（修正 P1 范围）

`apiduo_cn_logs.logs` 没有 `endpoint` 列，但 `other` JSON 里已有两个关键字段：

```json
{ "request_path": "/v1/responses",
  "request_conversion": ["OpenAI Responses", "OpenAI Compatible"],
  "admin_info": { "request_policy": [...], "use_channel": ["15"] } }
```

近 3 天 310 条成功请求的覆盖与分布：

| 指标 | 数值 |
|---|---|
| 有 `request_path` | 258 / 310（83%） |
| 有 `request_conversion` | 226 / 310（73%） |
| 有 `request_policy` | 226 / 310（73%） |
| 有 `store` 标记 | 0 |

主要流量分布：

| `request_path` | `request_conversion` | 次数 |
|---|---|---|
| `/v1/images/generations` | `openai_image` | 138 |
| （空） | （空） | 52 |
| `/v1/images/edits` | `openai_image` | 23 |
| `/v1/chat/completions` | `OpenAI Compatible` | 19 |
| `/pg/chat/completions` | `OpenAI Compatible > OpenAI Responses` | 13 |
| `/v1/responses` | `OpenAI Responses` | 5 |
| `/v1/responses` | `OpenAI Responses > OpenAI Compatible` | 4 |

**对 P1 的三条修正**：

1. **不需要新增 `endpoint` 字段**。`request_path` 已记录端点，`request_conversion` 已记录转换链，方向可读（左＝客户端，右＝上游）。
2. **需要解决的是可查性**：两者埋在 `other` JSON 里，只能 `JSONExtractString`，无法建索引、无法直接 `GROUP BY`。P1 应把它们提升为顶层列（或物化列），而不是重新设计字段。
3. **真正缺的是「被排除的候选」**。`request_policy` 只记录 `channel_selected`，没有记录「这个端点还命中了哪些渠道、哪些被过滤、为什么」。P0 的负向表上线后，必须把 `would_filter` 写进同一条决策链，否则过滤无法验证也没法回滚判断。

### 9.4 修正：`channel_id = 0` 不是「绕过渠道」，是 MCP 观察日志

初判「存在绕过渠道的成功计费路径」是**错的**。核实后：图片/视频模型都有渠道，`channel_id = 0` 的 52 条是 MCP 协议层的**观察日志**，与 relay 的消费日志成对写入。

真实渠道绑定与 3 天数据：

| 模型 | 渠道 | 真实 relay（`type=2`, `channel_id>0`） | 额度 | MCP 观察（`channel_id=0`） |
|---|---|---|---|---|
| `flux-3-image` | 7 `bfl-flux-3-image` | 58 次 | 5,800,000 | 41 次 |
| `happyhorse1.1` | 28 `qwen2api-video` | 10 次 | 1,250,000 | 8 次 |
| `qwen-image-3.0` | 27 `qwen2api-image` | 2 次 | 10,000 | 2 次 |
| `doubao-seedream-5-0-flash-260915` | 14 `ark-seedream-5-flash` | 6 次 | 49,200 | 1 次 |

同秒对照（`flux-3-image`，2026-11-14 13:25:37）：

```
type=2  channel_id=7  quota=100000  content="Quality standard, Image count 1"   ← relay 真实消费
type=2  channel_id=0  quota=0       content="mcp image.generate"                ← MCP 观察
```

写入点：`mcp/protocol_observe.go` 的 `writeObservation`。代码注释明确说明意图：

```go
// quota 留 0：MCP 层的这条日志只用于观察，计费由 relay 完成。
// 记成 0 而不是真实额度，避免运营看日志页时把两笔账加在一起。
Quota: 0,
```

**但这只解决了金额不叠加，没解决笔数与错误率叠加。** 三个口径缺陷仍然存在：

| # | 缺陷 | 影响 |
|---|---|---|
| 1 | 观察日志复用 `logTypeConsume = 2`（`mcp/service.go:309`） | 按 `type=2` 统计请求笔数会多算一份；看板与账单笔数对不上 |
| 2 | `Log.ChannelId` 未设置，默认 0 | 按渠道聚合时出现 `channel_id=0` 幽灵桶；`flux-3-image` 有 41 条落在这里 |
| 3 | MCP 层失败复用 `logTypeError = 5` 且 `channel_id=0`（`happyhorse1.1` 有 16 条） | 按渠道算错误率会被污染；P3 的健康闸门若直接吃这张表，会把「参数传错」误判成「渠道坏了」 |

第 3 条直接危及 P3。这些失败多数没有到达 relay（参数校验、工具层失败），按 `(channel, model, endpoint)` 统计健康度时必须显式排除 `admin_info.mcp = true` 且 `channel_id = 0` 的记录，否则健康闸门会摘除正常渠道。

**附带的可观测性缺口**：观察日志走 `model.EnqueueLog`，会**新生成** `request_id`，与 relay 那条不同（实测 `…6OXlg6OmE` vs `…6PGjmgUXC`），无法 JOIN。也就是说「一次 MCP 调用最终落到哪个渠道、花了多少额度」目前只能靠 `created_at` 同秒 + `model_name` 间接关联，没有确定性键。

### 9.5 已实施的口径修正（`mcp/` 内，0 上游改动）

| 改动 | 位置 | 做法 |
|---|---|---|
| 渠道维度可区分 | `mcp/protocol_observe.go` | 观察日志写 `ChannelId = -1`（原为默认 0）。`channel_id > 0` 的统计天然排除，管理端显示为 `#-1` |
| 两笔账可关联 | `mcp/protocol_upstream.go` + `protocol_observe.go` | `do()` 读响应头 `X-Oneapi-Request-Id`，逐层返回到 `callObservation`，写入 `admin_info.relay_request_id`。失败路径同样返回 |
| 回归守卫 | `mcp/e2e_test.go` | mock 上游回该响应头；断言 `channel_id = -1`、`quota = 0`、`other` 含 `relay_request_id` |

**为什么没有改 type（原第 1 条建议已撤回）**：MCP 自己的观察统计依赖 `type = 2 / 5` 做成功-失败分类（`mcp/service.go:113` 与 `:136` 的 `countIf(type = ?)`，外加 `:184` 的分类分支）。新增 `type=7` 会连带改 ClickHouse / MySQL / SQLite 三套方言查询、结果分类与巡检，收益（笔数不再重复）小于风险（MCP 观测面板失效）。改用 `channel_id = -1` 后，凡按 `channel_id > 0` 聚合的统计已自动排除这批记录，重复计数问题在统计侧即可消除。

**可见性**：`relay_request_id` 放在 `admin_info` 内，`logOtherVisibilityUser` 会把整个 `admin_info` 从用户视角剥离（`model/log_other.go:246`），因此不会泄露给终端用户，仅管理员可见。

**生产验证（2026-11-14 22:52，0.0.141）**：用 `qwen-image-3.0` 真实出站一次（成本 `quota=5000` ≈ $0.01），两条日志同秒写入：

```
type  model           channel_id  quota  request_id                            relay_request_id
2     qwen-image-3.0  27          5000   202611141451028799385588268d9d6URR82dhs  （空）
2     qwen-image-3.0  -1          0      202611141452160358313178268d9d69CxIXdys  202611141451028799385588268d9d6URR82dhs
```

上一行是 relay 的真实消费，下一行是 MCP 的观察记录：`channel_id = -1`、`quota = 0`，且 `relay_request_id` **精确等于** relay 那条的 `request_id`。两条记录的 `request_id` 本身不同（`…URR82dhs` vs `…9CxIXdys`），这正是改前无法关联的原因。

JOIN 可用（ClickHouse）：

```sql
SELECT m.created_at, m.channel_id AS mcp_ch, m.quota AS mcp_q,
       r.channel_id AS relay_ch, r.quota AS relay_q,
       substring(r.content, 1, 40) AS relay_content
FROM logs AS m
INNER JOIN logs AS r
  ON r.request_id = JSONExtractString(m.other, 'admin_info', 'relay_request_id')
WHERE m.channel_id = -1 AND m.created_at >= toUnixTimestamp(now() - INTERVAL 1 DAY)
```

注意 `relay_request_id` 在 `other` JSON 内，不是顶层列——要 JOIN 必须用 `JSONExtractString` 现算，无法走索引。若 P1 把它提升为顶层列，这个 JOIN 才具备可用性能。

**合并面**：改动全部位于 `mcp/`（apiduo 自有目录），`markercheck -cost` 复核为 `files 142 / blocks 426 / added 4054 / deleted 221`，与基线一致。

### 9.6 转换错误归因：500 → 400（已实施）

§9.2 的缺陷 1 与 2 已修。定位结果：错误不是转换器直接返回给客户端的，而是由 `relay/convert_request_error.go` 的 `newConvertRequestFailedError` 统一包装，最后落到 `types.NewError` —— **该方法默认状态码是 500**（`relaykit/types/error.go:265`）。

该函数本来就有分类逻辑，顺序是：

```
ConversionLossError → 400
reasoning.IsClientError → 400
"tools[" 前缀 → 400      ← 已有的 APIDUO:tool-type-400 标记块
其余 → 500
```

`n>1` 与 `previous_response_id` 两类错误满足不了前三条，于是落到 500。

**改法**：在已有的 `APIDUO:tool-type-400` 标记块内补两条文本判断，不新增标记块、不新增上游文件：

```go
if strings.HasPrefix(err.Error(), "tools[") ||
    strings.Contains(err.Error(), "is not supported in responses compatibility mode") ||
    strings.Contains(err.Error(), "does not support stateful fields:") {
    return types.NewErrorWithStatusCode(err, types.ErrorCodeConvertRequestFailed,
        http.StatusBadRequest, types.ErrOptionWithSkipRetry())
}
```

**为什么用文本匹配而不是哨兵错误**：`relaykit/relayconvert` 的两处拒绝返回的是普通 `error`，没有类型标记。引入哨兵要改 relaykit 内部 3 个上游文件，且要给未来所有「目标格式不支持」的错误逐一加包装。文本匹配沿用 `tools[` 的先例，代价是措辞变更时同步一处，已在注释里写明来源。

**为什么是 400 而不是 409/422**：客户端必须**改请求**（去掉 `n` 或 `previous_response_id`）才能成功，重试无意义。400 + `skip_retry` 让 SDK 立刻停止，这是 OpenAI 兼容网关的通行表达。

**回归守卫**：`relay/convert_request_error_apiduo_test.go`（新建文件，不参与合并面检查）用真实转换器触发两类错误，断言 `400`、`convert_request_failed`、`IsSkipRetryError`。测试放在新文件而不是上游的 `convert_request_error_test.go`，避免在别人的测试文件里插标记块。

**合并面**：`files 142` 不变、`blocks 426` 不变、`added 4054 → 4062`（+8）、`deleted 221` 不变。已在 `.apiduo/merge-budget.txt` 显式上调并写明原因。

### 9.7 未修的两条：判定为上游行为，不改

| 缺陷 | 判定 | 理由 |
|---|---|---|
| `store=true` 被静默改为 false | **保留** | OpenAI 官方 SDK 在 `/v1/responses` 上**默认 `store=true`**。拒绝会让所有用官方 SDK 的客户端在 chat 转换渠道上直接 400，代价远大于收益。回 `store: false` 是语义诚实的降级；真正的失败点是第二次带 `previous_response_id`，现已明确 400 + 不重试 |
| `n=2` 透传只回 1 个 choice | **不改** | 上游火山自身对 `n` 的支持有限。网关透传时改内容会破坏「原生路径不改写」的契约，属于渠道能力问题，应由 P2 渠道级能力声明表达 |
| 透传返回空 `arguments` 的 `tool_calls` | **不改** | 上游返回什么就透传什么。网关介入改写工具调用会掩盖上游缺陷，且可能破坏合法分片 |

**结论**：本次只修「网关自己造成的错误」，即错误归因错误（5xx vs 4xx）。上游行为不代偿。
