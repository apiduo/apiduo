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
