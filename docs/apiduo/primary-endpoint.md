# 主端点 + 派生端点（路由配置页重构）

> 2026-11-08。实现范围：**前后端**。
> 前端：`web/src/features/channels/lib/primary-endpoint.ts` + `components/primary-endpoint-editor.tsx` +
> 路由对话框新增「主端点」页签；后端：渠道 `settings.primary_endpoint` 字段、校验与运行时回退。

## 1. 问题：把「结果」当成了「配置」

现有 `advanced_routes` 是「每条入口一行」：入站路径、上游路径、转换器、是否透传。
它描述的是**结果**，不是**意图**。由此产生三个必然问题：

1. **改一处要改多处**：换上游端点时，每条路由都要跟着改，漏一条就是静默漂移。
2. **页面没法表达「一个渠道只讲一种母语」**：矩阵按模型 × 端点勾选，勾完还得去
   「路由转发」里手改上游路径；预设「图片双端点」其实是 `edits → generations`（Ark 风格
   合并端点），名字和行为对不上。
3. **后果不可见**：`pass_through_body_enabled` 决定发出去的是原文还是重建的 JSON，但它
   藏在二级展开里。实测结果是：页面配出来的渠道把 edits 的文本引用原样转给 bfl，回 **422**，
   而不是本地该给的 400。

## 2. 第一性原理：一个渠道一个主端点

对齐 `openai-image-adapter` 的三段式（折叠 → 上游脚本 → 按入口回包）：

| skill 里的概念 | apiduo 的落点 |
|---|---|
| canonical 请求 | 主端点收到的形状（上游母语） |
| 上游脚本 | 从主端点派生的转发（是否重建、是否透传） |
| 入口回包 | 入站端点的响应形状（由入口决定，不由路由决定） |

于是「渠道」应该被描述成：

```
主端点（上游母语） + N 条派生（入站端点 → 主端点，带各自的载体策略）
```

三条不变量：

1. **上游路径默认等于主端点**，只有显式覆盖才偏离；
2. **换主端点只改跟随主端点的行**，手写覆盖一律保留；
3. **载体是入口属性**：`/v1/images/edits` 透传才是 multipart，否则重建 JSON；
   预览必须能说出这一点，而不是让用户去生产环境验证。

## 3. 实现

### 3.1 纯函数层 `features/channels/lib/primary-endpoint.ts`

| 函数 | 职责 |
|---|---|
| `resolvePrimaryEndpoint` | 取上游路径的**众数**作为主端点；同票取先出现的（避免顺序抖动） |
| `buildDerivations` | 按入站路径归并成派生行，标出 `followsPrimary` / 转换器 / 透传 / 模型范围 |
| `applyPrimaryEndpoint` | 换主端点：只改跟随主端点的行，显式覆盖保留（最关键的约束） |
| `patchDerivation` | 按入口整组改转换器 / 透传 / 上游 |
| `addDerivation` / `removeDerivation` | 增删派生；新增默认跟随主端点、原生转发、不预设透传 |
| `previewRoutePlan` | 试算：命中哪条、发到哪、载体是什么、有哪些风险 |

管理面路由（`/api/advanced-custom/models`、`/api/advanced-custom/balance`）全程排除，
它们不属于「入口 → 上游」的投影。

### 3.2 页面

渠道 → 高级自定义路由 对话框，新增第一个页签**主端点**（原「路由转发 / 矩阵 / 模型列表 /
余额查询 / 完整 JSON」全部保留）：

- 顶部：主端点下拉（全量端点 + 已出现的上游路径），旁边显示派生条数；
- 中部：派生表，一行一个入站端点，可改转换器、开关透传、删除；新增派生走右上角下拉；
- 底部：**试算**——选入站端点（和可选模型），直接显示
  `入站 → 上游`、载体（multipart / JSON）、是否重建、以及风险提示：
  - `edits` 透传 + 主端点是 `generations` ⇒ **红色**：原文会发给只收 JSON 的端点；
  - `edits` 不透传 ⇒ 表单会被重建成 JSON；
  - `chat` 命中图片端点 ⇒ 提示需要图片协议才会转成出图任务；
  - 命中兜底路由 / 完全没路由 ⇒ 明说。

### 3.3 测试

`features/channels/lib/__tests__/primary-endpoint.test.ts`，15 例，覆盖：

- 主端点取众数、忽略管理面路由；
- 同入口多路由归并、模型范围合并、`followsPrimary` 判定；
- 换主端点保留显式覆盖（最关键的不变量）；
- 增删改派生；
- 试算的四种载体/风险组合 + 兜底 + 无路由。

`bun run typecheck` 与 `bun run lint`（新增文件 0 error）通过。

## 4. 已知边界

1. ~~后端仍是 `advanced_routes`~~：已加 `primary_endpoint`（见 §5），路由可由它派生；
   但 `advanced_routes` 仍是存储形态，历史渠道不会自动迁移成「空 upstream」。
2. ~~`image_protocol` 还在页面之外~~：已并入主端点页签（§6），主端点留空跟随主端点。
3. **对象存储的 `public_base_url` 仍无入口**：转存返回的是内网 host，客户端不可达。
4. 派生表暂不支持「同一入口按模型分叉」（多行同入口会合并显示），细粒度仍去「矩阵 / 路由转发」。

## 5. 后端 primary_endpoint（2026-11-08 落地）

前端只是收敛层；真正让「主端点 + 派生」成立的是渠道字段 `settings.primary_endpoint`。

**语义**

- `primary_endpoint` 是渠道级字段（`dto.ChannelOtherSettings.PrimaryEndpoint`），表示上游母语端点；
- 路由的 `upstream_path` **留空 = 跟随主端点**，写了就是显式覆盖；
- 未声明 `primary_endpoint` 时行为与旧版完全一致（空 `upstream_path` 仍报
  `advanced_routes[i].upstream_path is required`）。

**校验**（`relaykit/dto/channel_primary_endpoint.go`，创建与更新都走）

| 输入 | 结果 |
|---|---|
| 空 / 纯空白 | 允许（等于没声明） |
| `/v1/images/generations` | 允许 |
| `https://…`（完整 URL） | 拒绝：必须是单斜杠开头的路径 |
| `//v1/…`、`v1/…` | 拒绝 |
| 带空白 / `?query` / `#fragment` | 拒绝 |
| `/api/advanced-custom/*`（管理面） | 拒绝 |

**运行时**

`advancedcustom` 的 `resolve()` 在校验与匹配之前先 `WithPrimaryFallback(primary)`：
把空的 `upstream_path` 在**内存副本**里补成主端点，再跑 `Validate` 与 `MatchPathForModel`。
存储里的空值保持不变——这就是「配置是意图、不是结果」：换主端点只改一个字段。

**实测（本地 3299 + 记录式假上游 4020）**

- 渠道 `primary_endpoint=" /v1/images/generations "` + 路由 `/v1/images/edits`（无 upstream）
  ⇒ 创建成功，**落库仍是空 upstream**；请求 `/v1/images/edits` → 上游收到
  `/v1/images/generations`（JSON，走表单折叠）
- 显式覆盖 `/v1/chat/completions → /v1/chat/completions` 保持不变（单测固定）
- 非法 primary（完整 URL）⇒ 创建被拒，错误直接引用入参

**图片协议也跟随主端点**

`image_protocol.primary_path` 显式配了就用它；**没配**时跟随 `primary_endpoint`——
但只接受「像图片端点」的主端点（路径含 `/images/`）。主端点是 `/v1/chat/completions` 时
保持默认 `/v1/images/generations`，不拿聊天端点当图片主端点。

这样「主端点选到图片端点」时，路由、chat 传图、双端点三处指向同一个地方；
协议层不再需要单独维护一份主端点。

**前端**

页面读写 `settings.primary_endpoint`（`readPrimaryEndpoint` / `writePrimaryEndpoint`，保留 settings 里
其它字段）；未声明时仍从路由众数推导，保证旧渠道打开就能看到主端点。换主端点时会同时把
「跟随主端点」的路由补齐成新值，显式覆盖不动。

## 6. 页面上的图片协议（2026-11-08 补）

主端点页签里新增「图片协议」区块（`components/image-protocol-section.tsx`）：

| 控件 | 写入 | 说明 |
|---|---|---|
| 启用开关 | `image_protocol.enabled` | 关闭时**删键**，不写 `{"enabled": false}` |
| 上游布局 | `upstream` | `seedream` 单端点 / `gpt-image-2` 双端点 |
| 主端点路径 | `primary_path` | 留空跟随主端点；placeholder 显示实际会用的地址 |
| 从端点覆盖 | `edit_path` | 留空按主端点末段推导 |

读写收敛在 `lib/image-protocol-settings.ts`：保留 settings 里其它键、剪掉空字段。
页面此前完全没有协议入口，只能走 API 改 `settings`；现在和主端点在同一个页签，
选到图片端点时用户能一眼看到「协议会跟到哪」。

## 7. 历史渠道收敛（2026-11-08 补）

存量渠道的路由上游路径大多是「写死的绝对路径」，和主端点重复。保存时收敛一次：

`Channel.NormalizePrimaryDerivedRoutes()`（`model/channel_primary_endpoint.go`）在 `validateChannel`
最前面调用：**上游路径正好等于 primary 的路由改写成空**，其它路由不动。

三条实现约束：

1. 用 `map[string]any` 往返，不用结构体——settings 里本版本不认识的字段会被结构体重编码丢掉；
2. 只有真的改了才重新序列化（幂等，二次保存不再变化）；
3. 坏 JSON 不在这里报错，交给 `ValidateSettings`，错误信息保持原来那一条。

前端同时配合：换主端点时派生路由直接写空值（`applyPrimaryEndpoint(..., { deriveToEmpty: true })`），
新配置不会再产生「重复写一遍 primary」的形态。

实测（本地）：提交 `primary=/v1/images/generations` + 两条路由（edits→primary、chat→chat），
落库变成 `edits.upstream_path=""`、`chat.upstream_path="/v1/chat/completions"`，primary 原样保留。

## 8. 术语对照（为什么叫 primary，不叫 hub / canonical）

命名直接借用 `openai-image-adapter` skill 的用词，避免「同一概念三个名字」：

| skill 里的词 | apiduo 落点 | 指什么 | **不是**什么 |
|---|---|---|---|
| 主端点（§3.4.1「配置面只有主端点」） | `settings.primary_endpoint` | 上游真正讲的那个端点 | 不是「路由中枢」，也不是 canonical |
| 从端点 / 覆盖开关 | `advanced_routes[].upstream_path`、`image_protocol.edit_path` | 由主端点推导、可被显式覆盖 | 不是第二个主端点 |
| canonical 请求体 | `imageCanonical`（`canonicalFrom*`） | 折叠后的唯一一份 generations JSON | **不是端点**——它是报文形状 |
| 前门（载体前门 / 对话前门） | 四个入口 | 只决定怎么读请求、怎么回包 | 不含适配逻辑 |
| 上游脚本 | `planSeedream` / `planGPTImage2` | 把 canonical 变成上游请求 | 不做入口判断 |

两个曾用名与弃用理由：

- **hub**：描述的是拓扑（一进多出），不是语义；在网关仓库里还容易被读成「网关自己的路由中枢」，
  方向正好相反。
- **canonical endpoint**：`canonical` 在本仓库（`imageCanonical`，116 处）和 skill §1 里都已专指
  **请求体**。用它命名端点，「`canonicalFromChat()` 的产物该发给它吗」就没有一致答案——
  图生图会发到 `/v1/images/edits`。

一条可复用的判据：**名字要能通过「反过来问」**。「主端点发到哪？」→ 发到上游那个；
「canonical 发到哪？」→ 不知道，得看脚本。

## 9. 2026-11-10 撤销 888，回到「58 + 默认关闭的主端点开关」

### 为什么撤

888（`ChannelTypePrimaryEndpoint`）把「主端点」做成了独立渠道类型。代价是上游有 11 处按
`channel.Type == 58` 硬判的地方都要多认一个等价类型（adaptor 选择、定价端点、配置缓存、
余额查询、模型拉取、代理候选、流式能力、工具端点），前端另有 5 个文件按 58 分派表单
序列化——`channel-form.ts` 那处一旦漏掉，888 的 `advanced_custom` 就保存即丢。

撤销后回到一个类型，用一个**默认关闭**的开关表达同一件事：

| 开关 | 存储 | 行为 |
|---|---|---|
| 关（默认） | 不写 `primary_endpoint`、不写 `image_protocol` | 四个入口各按 `advanced_routes` 转发，报文原样，与 new-api 一字不差 |
| 开 | `primary_endpoint` + `image_protocol.enabled` | 空 upstream 跟随主端点；图片入口折叠成一份 canonical 请求 |

关着时后端不新增任何分支，上游同步不会冲突。

### 页面上变成什么

- 「主端点」页签（始终存在）：一个开关 + 一个地址输入框；关着时只有那句「与 new-api 一致」的说明。
- 「转发路由」页签保留：开关关着时它就是唯一的配置面。
- 开关打开后才出现「流向」：四个入口 → ① 折叠 → canonical → ② 发出 → 主端点。
  折叠只覆盖文生图 / 图生图 / 对话三个入口；`/v1/responses` 不参与折叠，仍按路由表转发。
- 「高级」里收着图片协议开关、上游布局（seedream / gpt-image-2）、`edit_path` 覆盖与派生端点表。
  布局默认从现有路由推断：`/v1/images/edits` 已指向 edits 就按双端点，否则按单端点。

### 校验口径

`validateAdvancedCustomConfig` 只认「每条路由都有上游」。开着主端点时，空 upstream 是
「跟随主端点」而不是缺配置，所以前端在校验前先跑 `withPrimaryFallbackRoutes` 补齐副本
（抽屉统计用 `fillPrimaryForStats`，口径一致）。后端本来就是 `WithPrimaryFallback` 先补齐再校验。

### 回归

- 前端：`bun check -b` 通过；`primary-endpoint.test.ts` 24 项（含新增的补齐函数）。
- 后端：`go build ./...`、`model` 与 `advancedcustom` 包测试通过。
- e2e：`ONLY=image bash e2e/live/run_all.sh` 29 项全过（该套件本来就用 type 58）。
- 本地实例实测：抽屉徽标显示「主端点：/v1/images/generations」，编辑器开关、流向、高级区块渲染正常。

## 10. 2026-11-10 对抗审查：4 个真问题 + 3 条已知取舍

### 修掉的（都在 e2e `primary_adversarial` 里有断言）

| # | 问题 | 之前的实际行为 | 现在 |
|---|---|---|---|
| 1 | `image_protocol` 只在请求时校验 | `upstream: "bogus"` 能存进库，之后每次出图都 400；运维看不出是配置错 | `ValidateSettings` 里校验，保存即拒 |
| 2 | 图片协议的路径校验比主端点松 | `primary_path: "//evil.example/x"` 能保存；请求时 URL 构建失败，**静默退回路由表地址**，配置与行为不一致 | 路径形状与 `primary_endpoint` 同一套规则；URL 构建失败直接报错，不再改道 |
| 3 | 主端点不归一尾斜杠 | `/v1/images/generations/` 原样落库、原样发给上游；与不带斜杠的路由永远收敛不到一起 | `NormalizePrimaryEndpoint` 去尾斜杠（保留 `/` 与非法 `//`） |
| 4 | 空路由表打开开关 | 开关能开，保存被「至少一条路由」拦住，而错误指向的路由列表是空的 | 打开时先种一条跟随主端点的派生（有管理面路由时保留） |

### 已知取舍（不是 bug，但要知道）

1. **图片协议优先于逐条覆盖**。开着协议时，`/v1/images/generations` 的落点由协议脚本决定，
   路由表里给它写的显式 upstream 不生效。这是「载体是入口属性」的必然结果：折叠之后
   请求体已经不是原来那份，按原路由发出去没有意义。要逐条控制就别开协议。
2. **对话入口一律被转成出图任务**。开了协议的渠道上，纯文本 chat 也会被折叠成
   `{model, prompt}` 发到图片主端点。所以这种渠道必须图片专用；页面流向图下已明说。
3. **`/v1/responses` 不折叠**。协议只接管 generations / edits / chat 三个入口；
   responses 仍按自己的路由转发（e2e A8 断言）。
4. **显式覆盖与主端点同值时会被收敛**。存储只有「空 = 跟随」一种形态，所以「故意把某条
   路由钉在与主端点相同的地址」在保存时会变成跟随；下次换主端点，这条也跟着走。

### 浏览器端（`e2e/live/ui/primary_endpoint.mjs`，42 项）

API 测试证明不了「人能不能在页面上做对这件事」，所以另开一套真 Chrome 用例，
断言尽量落在保存后的真实存储上，而不是只看页面自己说了什么：

- 抽屉徽标 / 默认页签：已声明 → 主端点；未声明 → 路由转发
- 开启态：开关、路径回显、四个入口、脚注、派生计数；关闭态：说明块、开关收起
- 高级区：协议开关、布局回显与修改（改 gpt-image-2 → 保存 → 落库 → 重开仍是新值）
- 关闭主端点：路由写实、`primary_endpoint` 与 `image_protocol` 两个键一并消失
- 空路由表打开开关：自动种一条 generations 派生、管理面路由（`/v1/models`）保留
- 非法路径：保存被拦（提示「主端点必须以 / 开头」）、值不落库
- 非 generations 路径：红字提示出现
- 派生增删：行数 1→2→1
- 换渠道不串状态：先开有主端点的、再开没有的，页签仍各自正确
- 390 窄屏：页签全部渲染、无横向溢出；深色模式；Escape 关闭；全程零 JS 报错

### 由此修掉的两个显示问题

1. **非法主端点的报错指错地方**：补齐逻辑把非法值灌进空路由，用户看到的是
   「上游路径必须是完整 URL 或以 / 开头」，对着路由表改不到真正填错的字段。
   现在表单与对话框都先校验主端点自身形状，提示「Primary endpoint must be a path starting with /」，
   输入框下也有对应的红字。
2. **布局下拉显示存储值**：触发器显示 `seedream` 而不是「Seedream 单端点」——
   base-ui 的 `SelectValue` 在选项延迟挂载时拿不到 label。改成显式渲染标签。

### 验证

- 浏览器套件 42 项全过（1440 / 390 / 深色三档，零 JS 报错）。
- `primary_adversarial.py` 23 项：保存期拒绝、尾斜杠归一、更新后跟随新值、
  覆盖保留/协议优先、ServesPath 放行、对话折叠、responses 不折叠、未声明入口 503 零出站。
- 全量 `bash e2e/live/run_all.sh`：15 套通过（含 trace / 渠道选择 / 多 key / 折扣 / 两个浏览器套件）。
- `relaykit/dto` 新增 3 个单测（尾斜杠归一、`//` 不被裁成空、协议路径形状）；
  前端 `primary-endpoint.test.ts` 29 项（含 `seedPrimaryRoute` 与 `looksLikeImagePath`）。
