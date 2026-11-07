# 协议矩阵：模型定义

> 2026-11-07。方法借自 `openai-image-adapter`：入口只是前门、中间只有一份、枚举值是格子不是开关。
> 代码：`web/src/features/protocol-matrix/`，37 个单测全过。

## 0. 三层，别混

| 层 | 是什么 | 落库 | 谁能改 |
|---|---|---|---|
| **配置层** `advanced_routes` | 唯一真相 | ✅ | 现有编辑器 + 矩阵 |
| **矩阵层** `Matrix` | 配置的另一种输入形态 | ❌ | 只在 dialog 的 state 里 |
| **覆盖层** `Coverage` | 从配置推导出的运行时后果 | ❌ | 只读，随时重算 |

三层的关系是单向的：`Coverage ← routes → Matrix`。矩阵改的是 routes，覆盖层只看不改。

**只要矩阵自己存一份（docx 原方案的 `endpoint_matrix` 字段），三层就变成两个真相**，这是整个设计里唯一不能让步的地方。

## 1. 端点只是前门

`openai-image-adapter` 的核心推论：**入口只决定两件事——怎么读请求、怎么回包。中间那段只有一份。**

矩阵里同样：端点只决定「命中哪条路由」，中间全是 `AdvancedCustomRoute` 的同一套语义。所以：

- 新增一个端点 ≠ 新增一套配置，矩阵只是多一行
- 真实渠道 3-5 个端点（你确认的），矩阵是小的，不需要滚动表格

## 2. canonical 为什么是 route，不是「矩阵字段」

skill 论证过一个结构：

| 候选 | 能否当规范 | 原因 |
|---|---|---|
| `edits`（multipart） | ❌ | 载体有歧义：分不清「没传」和「传了空值」 |
| `chat`/`responses` | ❌ | 对话是历史，图像是单轮，折叠必丢上下文 |
| `generations`（JSON） | ✅ | 载体无关、字段超集、能表达「未设置 vs 空值」 |

矩阵的选择题一模一样：

| 候选 | 能否当规范 | 原因 |
|---|---|---|
| 新增 `endpoint_matrix` 字段 | ❌ | 表达力更弱：装不下 `models` 规则、`auth`、模板路径 |
| `advanced_routes` | ✅ | 上游已有的最完备表达，后端校验和运行时都认它 |

所以 **`decodeMatrix` 遇到表达不了的配置就拒绝**——这不是功能缺陷，是诚实。对应 skill 那条：**上游没有的能力，静默丢弃是最坏处置**。

拒绝清单：自定义 auth、兜底重复、模型重叠。撞上就退回列表模式，配置一个字节都不动。

## 3. 格子，不是开关

skill §3.8：枚举值不是「支持 / 不支持」的一维开关，而是 `(模式 × 字段 × 值)` 上的格子。而且——

> **未测 ≠ 不支持。** 没测过的格子写「未测」——前者是待办，后者是结论。

矩阵的格子是 `(端点 × 模型)`：

```ts
type MatrixOutcome =
  | { kind: 'forwarded'; routeIndex: number; converter?: AdvancedCustomConverter }
  | { kind: 'rejected' }
```

⚠️ **当前只有两态，没有 `untested`。** 这是有意的取舍：

`untested` 是**证据层**的事实（谁在什么时候测过），要存。当前版本不存，改用 `rejected` + UI 提问代替：

> 这一格会 404 —— 是上游确实不支持，还是配漏了？

一句话就把 skill 的「未测 vs 不支持」变成了运维要回答的问题，而不是假装系统知道答案。真要做证据层，那是另一张表，另一个功能。

## 3.5 写侧：一格三态

读侧（覆盖）是两态，写侧（矩阵）必须多一档，因为「不生成路由」和「转成别的协议」是两件事：

```ts
type GridCellMode = 'off' | AdvancedCustomConverter   // 'off' 之外都是 AdvancedCustomConverter
```

| 值 | 含义 | 生成什么 |
|---|---|---|
| `'off'` | 不支持 | 不生成路由，后端 `MatchPathForModel` 匹配不到就报错 |
| `'none'` | 原生透传 | `{converter:'none'}` |
| `<converter>` | 转发前转换 | `{converter, auth, upstream_path}` 由转换器默认值填充 |

`'off'` 和 `'none'` 只差一个字母，语义相反，所以类型注释里写死了这一点——这是整个模块最容易写错的地方。

**同一端点可以有多条路由**，因为一格一个 converter，分组本来就是按 converter 做的：

```
/v1/chat/completions  flash  → ANT   ┐
/v1/chat/completions  260128 → GEM   ┘ 两条路由，各自带 models 列表
```

这条是相对上一版的实质变化：以前「同一端点下超过一条路由」直接判冲突、锁成只读，现在只有三种情况仍然锁：

1. 路由用了 `re:` 正则圈模型——展开不成具体的格子
2. 路由的 converter 服务不了这个端点——写回去上游会拒
3. 同一个模型被两条路由同时命中——格子的三态表达不了二选一

**收敛成兜底的条件**：只有一个 converter，且它覆盖了表格里的**每一个**模型。判断必须逐模型比对，不能用「分组里模型数 ≥ 表格模型数」——孤儿模型会把数量凑够，于是一个只勾了 flash 的端点会被写成兜底，把没勾的 pro 也放行。这条有测试固定。

**孤儿模型**：`routes` 里点名、但渠道模型清单里已经删掉的模型。表格不显示它们（表格按渠道清单展开），生成时原样带回同一 converter 的分组。否则改一次矩阵就静默删掉一条既有路由。

## 3.6 预设：端点级最小改动

上游 `ADVANCED_CUSTOM_TEMPLATE_OPTIONS` 是**整份配置替换**（all protocols / openai only /
claude only / gemini only），而且只铺原生透传，不含转换器。

矩阵的预设只做两件它不做的事：

1. **只动点名的端点**，别的端点一个格子都不碰——所以两个预设可以叠加
2. **可以带转换器**——「客户端说 Claude、上游其实说 OpenAI」这类接线

```ts
type Preset = {
  id: string
  label: string          // i18n key
  hint: string
  entries: Array<{ endpoint: string; mode: GridCellMode; upstreamPath?: string }>
}
```

五条：图片双端点 / Claude→OpenAI / OpenAI→Claude / OpenAI→Gemini / OpenAI→Responses。

两个实现细节：

- **端点不在表里就补一行**。`/v1/messages` 不在 `MATRIX_DEFAULT_PATHS` 里，预设点它会
  追加一整行（全 `off`），然后按常规改格子。`cells` 是行优先摊平，追加整行不破坏索引。
- **`upstream_path` 在 `gridToRoutes` 之后打补丁**。格子只记模式，记不住上游路径；
  打完补丁它就是普通路由，后续编辑会把它当模板原样沿用，所以补丁只需一次。

冲突端点会被跳过——它的格子改不动路由，改了只会让界面和保存结果对不上。

## 3.7 自定义模板：把当前形状存下来

内置那五条写死在 `presets.ts`，改需求要改代码。自定义模板补的是这一半：
**把当前渠道的形状存成模板，下次套到新渠道**。

捕捉规则（`routesToPresetEntries`）：

| 保留 | 丢掉 |
|---|---|
| 端点、converter、上游路径、鉴权、per-route 透传开关 | `models`（套到新渠道时模型名不同，留着只会生成指向不存在模型的路由） |
| 端点顺序 | 管理路由（`/v1/models`、余额查询） |

同一端点只取第一条：一个端点两种转换器属于渠道个例，模板表达不了，取了也会误导。

存储复用 `options` 表：**一条模板一行**，不建表、不加接口。

| | |
|---|---|
| key | `apiduo.route_preset.<id>`（id 形如 `pmw5ltdhu3r6e`，时间戳打头） |
| value | `{"name":"…","entries":[…]}` |

```
GET  /api/option/        → 取全部行，按前缀过滤，逐条解析
PUT  /api/option/        → {key, value}            新增 / 改名 / 改内容
PUT  /api/option/        → {key, value: ""}        删除（空值即墓碑）
```

三个取舍，都是有意为之：

1. **为什么不塞进一行 JSON**：那样每次保存都是整份覆盖，两个人同时改，后写的把先写的吃掉。
   一条一行，增删各自原子——而这本来就是 `options` 表（key → value）的形状。
2. **为什么不建独立表**：模板是管理员写的几十条配置，不是业务记录。建表要迁移、要控制器、
   要路由、要权限，换来的只是 `created_at` 这类字段。`options` 已经是配置表，能复用就复用。
3. **删除为什么是墓碑**：没有通用的 option 删除接口，写空值代替，读的时候过滤掉。
   代价是库里留下空行；比起开一个「能删任意 option」的接口，这个代价更小。

校验全部在前端（后端不校验任意 option 的 value）：名字非空、条目 ≥1、端点以 `/` 开头、
`mode` 必须是 `off` 或已知 converter。坏行逐条丢弃，不连累整条 preset bar。

🔴 不要嵌 `Dialog`：`PresetBar` 挂在「高级自定义路由」的 Dialog 里面，再套一层 Dialog，
关闭时会把父级 `Tabs` 一起重挂载，用户存完模板就被弹回「路由转发」。用 `Popover`。

### 为什么要缓存

`GET /api/option/` 返回的是**整份**配置：本机 270 行 / 45.7 KB，其中 81% 的字节来自
`CompletionRatioMeta`(12.9 KB)、`ModelRatio`(6.1 KB) 这类模板一个字都用不上的行。
而矩阵是 Tabs 里的一页，**非激活面板会被卸载**，来回切三次就是三次整包——实测
「切 4 次」= 4 个请求 / 182.7 KB，只为读出 1 行模板。

所以 `preset-api.ts` 里有一层模块级缓存（不是组件内 `useRef`：组件卸载就没了，
正是要跨挂载）：

| 情况 | 行为 |
|---|---|
| 命中（60 s 内） | 直接返回解析结果，0 请求 |
| 并发挂载 | `inflight` 复用同一个 promise，只发一次 |
| 自己新增/删除 | **就地改缓存**（`sorted()` 保持 id 序），不重新拉 |
| 别人改了 | 最多等 60 s（TTL） |
| 读取失败 | `finally` 清 `inflight`，下次重试；界面弹「模板读取失败」而不是静默空列表 |

只缓存解析后的结果，不缓存原始响应——反正每次都要走 `parsePresets` 过滤一遍。

实测同一路径切 4 次标签：**4 → 1 个请求，182.7 KB → 45.7 KB**。

## 3.8 转置：行是模型，列是端点

矩阵原本是「行=端点、列=模型」，模型一多就得横向滚几十列。实测 10 个模型时表格
2104px 宽、可视区 942px，往右一滑端点列就没了——横向滚是所有滚动里最差的一种：
表头跟着动、看不出自己在哪一行。

**所以转置了。** 依据是两者数量级不同：

| | 数量 | 结论 |
|---|---|---|
| 端点 | 默认 5 个，带自定义一般 ≤10 | 放列，宽度够 |
| 模型 | 十个起步，几十个不稀奇 | 放行，纵向滚是常态 |

转置后 10 个模型 6 个端点：**表格 942px，溢出 0**，一个横向滚动都不需要。

| 做法 | 原因 |
|---|---|
| 表头 `sticky top-0` + 模型列 `sticky left-0` | 滚 20 行也知道自己在看哪个模型 |
| 表格容器 `max-h-[440px]` | 让表头真的能粘住；不设限就永远是「整个对话框在滚」，sticky 失效 |
| 端点路径按 `/` 折行（`<wbr>`） | `/v1/chat/completion` + `s` 这种从词中间劈开像坏了；**不要用零宽空格**，它会被复制进剪贴板 |
| 模型名单行 + `title` | 行内折行会让行高参差不齐，更难扫 |
| 页脚不 `sticky` | 页脚钉在底部会把「被切开的最后一行」盖住，那是唯一能提示「下面还有」的东西 |

**「整行」变成「整列」。** 原来每行末尾有个「全部/无」按钮批量开该端点的所有模型；
转置后端点成了列，这个按钮移到 `tfoot`，语义不变（`fillEndpoint`）。

🔴 全站 `index.css` 有 `::-webkit-scrollbar{display:none}`，所以纵向能不能滚只能靠
「底部被切开的行」+ 表头的模型总数（`模型 10`）来暗示。别指望能加个滚动条。

横向仍然会滚的情况只剩一种：端点数超过可视宽度（约 7 个以上）。那时模型列是钉住的，
不丢上下文。

## 3.9 模型行按模态分组（文本 / 图片 / 视频 / 其他）

模型行按模态分组，组之间插一条整宽的色带（`文本 5` / `图片 3` / `视频 2`）。
十来个模型混着文本、生图、生视频时，不分组就是一长条扫描。

**判据靠名字，不查元数据。** 矩阵手里只有渠道的 `models` 字符串。定价目录虽然带
`supported_endpoint_types`，但那是另一份数据、另一次请求，而且渠道里常有目录里查不到的
自定义模型名——查不到就成了「未分类」，比按名字猜更糟。

判定顺序：**视频 → 图片 → 其他 → 文本**，越具体的先判：

- `wan2.1-t2v` 是视频、`wanx` 是图片（都含 `wan`）
- `qwen-image` 是图片、`qwen-vl` 是文本（视觉理解不是生图）
- `gpt-4o-audio` 是语音，别因为 `gpt` 归到文本
- 剩下的默认文本——渠道里文本模型占绝大多数，让「没认出来」落到文本比落到「其他」更接近事实

🔴 短词会误伤：`gen-3` / `gen-4` 本想匹配 Runway，结果 `imagen-4.0-generate-001`
里的 "gen-4" 被命中，图片模型被判成视频。Runway 靠 `runway` 前缀就够。这条有测试。

**只影响展示顺序**：格子按模型名查（`cellAt(grid, endpoint, model)`），所以行怎么排都不影响
读写；生成路由用的仍是渠道原本的顺序。分错组也不会写错数据，最多是行的位置不对。

组标题**可点**，点了横向滚到该组第一列（减掉固定端点列的宽度）。10 个模型时后面两个组
在屏幕外，光有组标题没用，得能跳过去——否则用户只能盲拖。

空组不出现：只有文本模型的渠道只有一个组标题，不会多出三个空表头。

## 4. 覆盖层的推导规则（必须和后端一字不差）

`findRouteForModel` 复刻 `AdvancedCustomConfig.MatchPathForModel`：

```go
for _, route := range c.Routes {
    if 路径相同 && 模型规则命中 { return route }
}
```

四条判据，错一条结论就是假的：

| 规则 | 实现 |
|---|---|
| 路径精确匹配 | 矩阵只处理精确路径，模板路径在 decode 就拒了 |
| `models` 为空 = 兜底 | 接住剩下的所有模型 |
| `re:` 前缀 = 正则 | `new RegExp(rule.slice(3)).test(model)` |
| **顺序匹配、先命中先赢** | 具体模型行必须排在兜底行之前，靠数组顺序保证 |

最后一条最容易错：兜底不是「优先级最低」，是「排在最后的那条」。用户把兜底拖到前面，行为就变了——上游的 `fixCatchAllOrder` 和矩阵的 `encodeMatrix` 都在防这件事。

坏正则返回 `false` 而不是抛异常：后端 `Validate` 保存时已经拦了，这里是防御，不该让整张矩阵因为一条脏规则崩掉。

## 5. 端点 ≤5 带来的简化

| 决定 | 理由 |
|---|---|
| 不做矩阵编辑（A 形态） | 3-5 个端点，现有卡片够用，只读总览的收益接近零 |
| 默认端点清单 5 个 | `MATRIX_DEFAULT_PATHS`：chat / responses / embeddings / images-generations / images-edits |
| 实际端点从 routes 自动补 | 配了 `/v1/images/variations` 就必须看得见，默认清单只是兜底 |
| 覆盖度 + 预设是主力 | 覆盖度回答「哪格会 404」，预设回答「怎么一次配对」 |

## 6. 和 skill 的概念对照

| skill | 矩阵 |
|---|---|
| 四个入口（generations / edits / chat / responses） | 端点 |
| canonical body | `AdvancedCustomRoute` + `dto.ImageRequest` |
| 四个转换维度（载体 / 结构 / 语义 / 时序） | converter 的差异全落在这四类里 |
| 枚举矩阵 `(模式 × 字段 × 值)` | `(端点 × 模型)` 格子 |
| 未测 ≠ 不支持 | `rejected` 不等于「不支持」，UI 必须追问 |
| 静默丢弃是最坏处置 | 没路由 = 404，显式失败，不是兜底放行 |
| 一个上游一个脚本 | 一个渠道一套 routes |

## 7. edits 到底该在哪层折叠（已查证）

skill 说 **edits 只是载体前门**，应该折叠成 `canonical + image`，而不是当成一个「上游能力」去转换。

查了代码，`dto.ImageRequest`（`relaykit/dto/openai_image.go:24`）**确实就是那个 canonical**：

```go
Images  json.RawMessage `json:"images,omitempty"`
Mask    json.RawMessage `json:"mask,omitempty"`
Watermark *bool         `json:"watermark,omitempty"`
```

但接着追 `openai.ConvertImageRequest`（`relay/channel/openai/adaptor.go:520`）发现一个关键分支：

```go
case relayconstant.RelayModeImagesEdits:
    if isJSONRequest(c) {
        return request, nil          // 客户端本来就发 JSON → 原样放过
    }
    // 否则重建 multipart，从 c.Request.MultipartForm 里读文件
```

**结论修正**：

| 场景 | 需要做什么 |
|---|---|
| 客户端发 JSON（`image` 是 URL / data URI） | **什么都不用做**，`isJSONRequest` 分支直接放行 |
| 客户端发 multipart，上游收 JSON | 需要**载体转换**：把 `MultipartForm.File["image"]` 读出来，编码成 data URI / base64，填进 `Images` |

所以 `image_converter.go` 该写，但它的实质是**载体转换（文件 → 字符串引用）**，不是「协议转换」。对应 skill 的转换点 ①，也对应那条公理：

> 载体 carrier —— 这份数据用什么物理形式传？错的样子是 200 但内容不对，或 400 说不认识。

成本比 docx 描述的小得多：不用重写整个 body，只要把二进制读出来塞进 `Images`，`prompt`/`size`/`n` 全部原样。

**顺带一个协议矩阵层面的判断**：这类「客户端载体 → 上游载体」的差异，本质是**渠道能力**，不是**端点能力**。所以它应该表现为「这个渠道的 edits 端点支持 JSON 客户端」这种覆盖格，而不是让用户在每条路由上选一次。

现在文档里那两张表（`advanced_routes` 的矩阵、`Coverage` 的格子）都还没有字段表达「客户端载体」，这是下一步要补的维度。先记着，不急着实现。

### 7.1 「edits 由 generations 承接」的正确配置（已实测）

上游把图生图合进生成接口时（方舟这类），路由是这么写的：

```json
{
  "incoming_path": "/v1/images/edits",
  "upstream_path": "/v1/images/generations",
  "converter": "none",
  "pass_through_body_enabled": true,
  "auth": { "type": "header", "name": "Authorization", "value": "Bearer {api_key}" }
}
```

`converter` 只能是 `none`——任何协议转换器都会在 `ConvertImageRequest` 里报
`converter "x" does not support image requests`。

实测（假上游记录 body，客户端都把 `image` 发成 data URI）：

| 客户端 | `pass_through_body_enabled` | 上游收到 | 结果 |
|---|---|---|---|
| JSON | `false`（默认） | `{"image","model","n":1,"prompt","response_format","size","watermark"}` | 200，但 `seed`、`sequential_image_generation` **被静默丢掉** |
| JSON | `true` | 客户端原始 163 字节，一个字节不改 | 200，私有字段全在 |
| multipart | 任意 | `multipart/form-data`，原样打给 `/v1/images/generations` | 上游若只收 JSON 会 400 |

两个结论：

1. **必须开透传。** 不开的话请求体会按 `dto.ImageRequest` 重新序列化，结构体里没有的
   字段（`seed`、`sequential_image_generation`、`optimize_prompt_options`…）全部丢失。
   这是「200 但图不对」那一类最难查的错——比报错可怕得多。
   所以 `image-dual` 预设默认就给 edits 这条带上 `passThroughBody: true`，有测试固定。
2. **multipart 客户端仍然不行。** `DoRequest` 里
   `RelayMode == ImagesEdits && !isJSONRequest(c)` 会把原始 form 直接转发。
   要支持「客户端传文件、上游只收 JSON」，得写一个载体转换器（文件 → data URI），
   现在没有，这也是 §7 记的那个缺口。

### 7.2 载体折叠：edits 的 multipart → generations 的 JSON（已实现）

§7 记的缺口补上了。规则来自 `openai-image-adapter` skill 的转换点 ①：
**edits 只是一扇门，语义和 generations 一样**，图片可以是文件，也可以是一段字符串引用。

实现位置：

| 文件 | 作用 |
|---|---|
| `relay/channel/advancedcustom/apiduo_image_edits_fold.go` | 折叠本体（纯函数 + 路由判据） |
| `relay/channel/advancedcustom/adaptor.go` | 一处 `APIDUO:image-edits-fold` 标记块调用 |

**什么时候折**：`RelayMode == ImagesEdits` 且是 multipart，且上游路径**不以 `/images/edits` 结尾**。
最后一条是逃生阀——把上游路径写成 `.../images/edits` 就退回原生转发（很多上游确实收 multipart）。

| 表单 | canonical JSON |
|---|---|
| `image=@a.png` | `image: "data:image/png;base64,…"` |
| `image[]=@a` + `image[]=@b` | `image: [refA, refB]` |
| `image=` 空 part / 零字节 | 键不出现（= 文生图） |
| `mask=@m.png` | `mask: "data:image/png;base64,…"` |
| `n=2` / `n=abc` / 没传 | `2` / `1` / 键不出现 |
| `image=https://…`（文本） | 原样当引用 |
| 其余文本字段 | 原样透传 |

三条容易错的：

1. **mime 按字节嗅探**（`http.DetectContentType`），客户端声明的 `Content-Type` 只在嗅探不出时兜底；
   两者都不像图片就 **400**，不把 `application/octet-stream` 丢给上游。
2. **转 data URI 而不是裸 base64**：裸串会被上游当 URL 解析（`invalid url specified`）。
3. **改完 Content-Type 要还原**：`SetupApiRequestHeader` 从 `c.Request.Header` 抄 Content-Type，
   折叠后临时改成 `application/json`，发完立刻还原——重试会用同一个 gin.Context 再走一遍
   `DoRequest`，留着 JSON 头会让重试把原始 multipart body 当 JSON 发出去。

实测（假上游记录请求）：

```
客户端 curl -F image=@ref.png  → 上游 POST /api/v3/images/generations
                                 application/json
                                 {"image":"data:image/png;base64,iVBORw0…","model":…,"n":1,"prompt":"换成蓝底","size":"2048x2048"}
客户端 JSON + image=URL        → 上游收到原样 URL（走的是原本的 JSON 分支）
回归：上游路径改成 /api/v3/images/edits → 上游收到的仍是 multipart（不折）
```

单测 9 例：`apiduo_image_edits_fold_test.go`（单图/多图/空 part/mask/n 归一/文本引用/非图片/声明 mime 兜底/路径判据）。

## 8. 现在的代码状态

```
web/src/features/protocol-matrix/
  types.ts                    MatrixItem / MatrixOutcome / Coverage
  index.ts                    导出
  lib/matrix.ts               decode / encode / validate / converterOptionsForPath
  lib/coverage.ts             findRouteForModel / collectEndpoints / buildCoverage / rejectedCells
  lib/grid.ts                 写侧：buildGrid / setCellMode / gridToRoutes（一格三态）
  components/coverage-summary.tsx   只读覆盖弹层（挂在「路由转发」页顶部）
  components/matrix-grid.tsx        勾选表（第 5 个标签页）
  lib/__tests__/matrix.test.ts    32 个用例
  lib/__tests__/coverage.test.ts  19 个用例
  lib/__tests__/grid.test.ts      32 个用例
```

仍然：**0 个上游文件改动**（改动全在 `advanced-custom-editor-dialog.tsx` 的 `APIDUO:*` 块内），
`bun run typecheck` 0 错误，`markercheck` 通过。

UI 已落地：矩阵标签页（勾选 + 每格选转换器）、覆盖弹层（只读，看哪格会 404）。
两者都是 `advanced_routes` 的投影，不新增字段。


## 9. 图像多端点协议：以 generations JSON 为 canonical（2026-11-07）

### 9.1 为什么重做

§7.2 那版「edits multipart → generations JSON」只覆盖了一个格子，而且用
`upstreamPath` 后缀猜要不要折叠。真实需求是**三个下游入口 × 两种上游端点布局**，
按 skill `openai-image-adapter` 的分层，中间必须有唯一一份 canonical：

```
/images/generations ─┐
/images/edits       ─┼─ 折叠 ①② ─→ canonical ─→ 上游脚本 ③ ─→ upstream
/chat/completions   ─┘   {model,prompt,image?,…}      ↑
                              ← ⑤ 响应 ← ⑥ 按入口回包 ─┘
```

**配置面只有主端点**，从端点由脚本按 canonical 有没有 image 推导（skill §3.4.1）。
路由结构 `AdvancedCustomRoute` 表达不了「同一入口按请求体内容分流」，
所以端点选择放在脚本层，绕开 `MatchPathForModel` 的 first-match 限制。

### 9.2 配置

渠道 `settings` 里的 `image_protocol`（默认不配置 = 完全不介入）：

```json
{
  "image_protocol": {
    "enabled": true,
    "upstream": "seedream",
    "primary_path": "/v1/images/generations",
    "edit_path": "",
    "allow_url_fetch": false
  },
  "advanced_custom": { "advanced_routes": [ "... 三个入口各一条 converter:none 路由 ..." ] }
}
```

| upstream | 端点布局 | 有 image 时 |
|---|---|---|
| `seedream` | 单端点，image 为 `str \| list<str>` | 仍走主端点，JSON，删 mask |
| `gpt-image-2` | generations 不收 image；edits 收 multipart 文件 | 推导 `/v1/images/edits`，编码成 multipart |

`edit_path` 是覆盖开关（推不出来才填）。推导规则：末段 `generations` → `edits`，
**scheme / host / query 全部保留**；末段不是 `generations` 直接报配置错，不猜路径。

### 9.3 实测（假上游，2026-11-07）

两场景各 8 个格子，全部 200。`G`=generations 入口，`E`=edits 入口，`C`=chat 入口；
`t2i` 文生图、`i2i` 图生图、`multi` 两张垫图。

| 场景 | 无图 | 有图 | 多图 | chat 回包 |
|---|---|---|---|---|
| seedream | `generations` + JSON | `generations` + JSON，image str | `generations` + JSON，image 数组 | `chat.completion` |
| gpt-image-2 | `generations` + JSON | `edits` + multipart 1 文件 | `edits` + multipart 2 个 `image[]` | `chat.completion` |

三个 400 已验证：mask 无 image / 无图无 prompt / URL 未开下载。
`chat` 回包里 `choices[0].message.content` 是 `image_url` parts 数组，`b64_json` 转 data URI。

#### `image` 判定规则（需求口径，实测 2026-11-07）

| 客户端给的 `image` | 判定 | 上游看到 |
|---|---|---|
| 不传 / `null` / `""` / `[]` / `["", " "]` | **文生图** | 键被删除 |
| `"ref"` | 图生图 | 字符串 |
| `["ref"]` | **图生图**（长度 1 也算） | **仍是数组** `["ref"]` |
| `["a","b"]` | 图生图 | 数组 |

`hasImage()` 只看归一后有没有引用，不看键在不在。形状**原样保留**：
长度 1 的数组不压成字符串（skill 技巧 6「无损优先」），`planSeedream` 和
`foldEditsMultipart` 都不再改写形状；表单里用 `image[]` 这个 key 上传单个文件
也保持数组。场景2 下 `["ref"]` 会正确分流到 `/v1/images/edits`。

⚠️ **与 skill §2.1 的口径差异**：skill 把 `image: ""` 判 400（当作客户端主动写的
空值），这里按需求口径归一化为文生图（删键）。这是有意的取舍，不是漏实现。

**默认关闭等价**：去掉 `image_protocol` 后，generations 仍按 `dto.ImageRequest` 重建
（丢 `seed`、注入 `n:1`），chat 仍原生转发 `messages` 并原样回包 —— 与改造前一字不差。

### 9.4 对抗审查留下的结论

1. **需求里场景2 第 2、3 条写的 `=> /v1/images/generations` 是「canonical 中心」的简写**，
   不是字面打 generations。上游 generations 不收 image，按字面实现必然失败；
   已与需求方确认按 canonical 分流。
2. **edits 入口的「文生图」做不到，也不需要**：上游 `openai.ConvertImageRequest`
   在 edits 模式下强制要求 multipart 里有图片文件（`image is required`）。
   需求写「前期可以先不支持」与此一致。当前返回的是上游的 500，尚未改成 4xx。
3. **「透传就行」不成立**：`converter:none` 会重建 body。协议层直接从原始 body 折叠，
   vendor extras（`seed`、`sequential_image_generation`…）原样保留。
4. **`image` 三态载体**：URL / data URI / 裸 base64。seedream 只把裸 b64 补成 data URI；
   gpt-image-2 的 edits 需要文件，data URI 与裸 b64 本地解码，http(s) 必须显式开
   `allow_url_fetch`，且拒绝回环/私网 IP、10s 超时、20 MiB 上限。默认零出站连接。
5. **seedream 的 mask 不转发**（skill §3.7）；之前版本把 mask 转 data URI 下传是错的，已修。
6. **未做的三件事**（都是取舍，不是遗漏）：
   - `size` 档位换算（`1024x1024` → `2K`）属上游方言，未经实测不擅自改写；
   - `response_format` 归一化的上游行为未按格子取证（skill §3.8：未测 ≠ 不支持）；
   - 错误信封重铸（401/403 → 502、429 保留 Retry-After）是破坏性变更，skill §3.9 标为待定。
8. **`image` 判定以需求口径为准**：长度 1 的数组是图生图，长度 0 / 为空是文生图；
   空串不再 400 而是归一化（见 §9.3 的表）。

7. **「1-n 张垫图」目前只验证到 n=2 的编码与保序**，真实上游的多图尺寸/张数上限
   没有取证卡，不能当成已支持。

### 9.5 代码落点

| 文件 | 作用 |
|---|---|
| `relaykit/dto/apiduo_image_protocol.go` | 配置结构 + 校验 |
| `relay/channel/advancedcustom/apiduo_image_canonical.go` | 三个折叠 ①② + canonical 校验归一化 |
| `relay/channel/advancedcustom/apiduo_image_script.go` | 上游脚本 ③：选端点、载体、编码 |
| `relay/channel/advancedcustom/apiduo_image_response.go` | ⑤⑥ + chat 按张计费 |
| `relay/channel/advancedcustom/apiduo_image_adaptor.go` | 挂接块的全部实现 |
| `apiduo_image_protocol_test.go` | 12 组判别式单测 |

上游文件只留 3 个不可替代挂接点（字段、URL、请求、响应），块内各一次调用；
`.apiduo/merge-budget.txt` 已登记 +5 块 / +26 行。

**chat 入口不用改 TextHelper**：调研确认响应形状由 `adaptor.DoResponse` 掌控，
`TextHelper` 只强转 `usage.(*dto.Usage)`。按张计费靠
`info.PriceData.AddOtherRatio("n", count)` + `info.UpdateImageCount()`，
`text_quota.go:362/374` 会 `ApplyOtherRatiosToDecimal` 吃到这个乘子。

**legacy fallback**：未启用协议层时，§7.2 的 edits 路径后缀折叠仍生效，
保证已提交的渠道配置不被破坏；新代码一律走协议层。


### 9.6 本地端到端复验与计费修复（2026-11-07 21:00）

完整记录见 `docs/apiduo/image-e2e-2026-11-07.md`：三渠道同时在线（seedream /
gpt-image-2 / 默认关闭），24 格功能矩阵全 200，四个 400、chat 回包内容、
默认关闭等价全部实测通过。

**本轮查出并修掉两个计费 bug**（此前只做了代码推断，没落库验证）：

| | images 入口 | chat 入口 |
|---|---|---|
| 修复前 | `quota=38` | `quota=75` |
| 修复后 | `quota=38` | `quota=38` |

1. `finishImageChatResponse` 返回的 usage 是 `1/1`，而 ImageHelper 是 `1/0`；
   TextHelper 按 token 计价，多一个 completion token 正好翻倍。改成 `1/0`。
2. `n` 乘子原用「响应 data 长度」，ImageHelper 用「请求 n」；且非 UsePrice 时
   `PrepareImageBillingForRequest` 把 quantity 固定为 1。改为缓存 canonical 的请求 n，
   只调 `UpdateImageCount()`，不再无条件 `AddOtherRatio`。

修复后 images n=1/n=2 与 chat n=1/n=2/图生图共 5 个请求全部 `quota=38`。


### 9.7 空 `data[]` 的口径修正（2026-11-07 21:43）

§9.6 曾把「上游 200 + 空 `data[]`」统一升格为 502，**这是过度处理，已撤销**。

需求方口径：**入口只负责回包形状完整，不替上游判定业务成败**。chat 入口只要
`choices[0].message.content` 存在即可，`content: []` 也是合法回包；images 入口
原样透传 `{"data":[]}`。skill §5.5 自己也把「200 但没图必须变 4xx/5xx」标为
「产品取舍，不是定律」，这里采用需求方口径。

实现上同时带来一个好处：`doImageProtocolResponse` 恢复为「只在 chat 时接管」，
不再读 `resp.Body` 再还回去，正常路径与上游零差异（此前那段前置检查会改变
`resp.Body` 类型并引入额外内存拷贝）。

实测（v7）：chat → 200 `chat.completion` + `content=[]`；images → 200 `{"data":[]}`；
24 格矩阵仍全 200，计费仍 `38/38`。单测 `TestEmptyImageDataKeepsChatContentShape` 锁住形状。


### 9.8 chat 回包：Markdown 图片 + 流式 SSE（2026-11-07 21:47）

需求口径：chat 入口返回图片用 **Markdown 包装**，`content` 是字符串而不是
`image_url` parts；`stream:true` 也要「拼装为 chat」，不能拒绝。

- 非流式：`content = "![image](url)"`，多图一行一个，`b64_json` 转 `data:` URI 后嵌入。
- 流式：`chat.completion.chunk` 三帧（md delta → `finish_reason=stop` → `[DONE]`），
  自己写 `Content-Type: text/event-stream`（`IOCopyBytesGracefully` 会把上游的
  `application/json` 复制回来覆盖掉，不能用）。
- 空 `data[]`：`content: ""`，字段仍在（沿用 §9.7 口径）。
- `TextHelper` 在 stream 下没有特殊分支，无需改上游分发。

⚠️ 与 skill §2.2「chat 回 `image_url` parts」有意不同，采用需求方口径
（多数 chat 客户端只认 string content）。SSE 只有「完整结果一帧」，不涉及
partial images（skill §6 范围外）。

实测：单图 / 双图 / 双图流式均正确；计费非流式·流式·images 三入口 `38/38/38`；
24 格矩阵全 200。详见 `docs/apiduo/image-e2e-2026-11-07.md` 末节。


### 9.9 chat 最终口径（2026-11-07 21:55，取代 §9.8 的非流式部分）

| 项 | 口径 |
|---|---|
| chat 非流式 | **不用 md**，`content` 是 `image_url` parts 数组（skill §2.2 形状） |
| chat 流式 | **用 md**，`delta.content` 是 Markdown 字符串 |
| 请求字段 | 只认 OpenAI 标准字段（`messages[].content` 的 text / image）；`n`、`size`、`temperature`、`tools`、`stream` 等一律不往下传 |
| 文生/图生判定 | `prompt` 非空且无 `image` → 文生图；有 `image` → 图生图；皆空 → 400 |
| 空 `data[]` | 非流式 `content: []`、流式 `delta.content: ""`，均不升格 5xx |

`canonicalFromChat` 只产出 `model` / `prompt` / `image?`。响应侧附加字段仍按入口
carry over（不参与判定）。计费因 `n` 不再下传而恒用请求数 1，实测非流式 / 流式
均 `quota=38`，与 images 入口一致。

实测与复现见 `docs/apiduo/image-e2e-2026-11-07.md` 末节。


### 9.10 chat 严格识别 + 对象存储转存验证（2026-11-07 22:00）

**chat 图片只认标准形态**（需求指定）：

```json
{"type":"image_url","image_url":{"url":"...","detail":"high"}}
```

`readChatContent` 只取 `image_url.url`，`detail` 等忽略；`input_image` / `image` /
裸字符串 / 顶层 `url` 一律不当图片（避免误判成垫图走错端点）。text 只认 `type:"text"`。
实测标准形态认图、非标准不当图、只有标准图片无 text 仍能图生图（prompt 允许为空）。
单测 `TestChatOnlyRecognisesStandardImageURLPart`。

**对象存储转存**由 `service.RewriteResponseArtifactsInPlace` 在 `DoResponse` 之前介入
（`relay/compatible_handler.go:174` / `image_handler.go:160` / `claude_handler.go:151`），
与本协议层是串行的两段：上游响应先被改写成存储链接，再由 ⑥ 按入口包装。
实测 data URI / 内联 base64 转存、公开链接与 presign、失败降级、幂等 key、
chat 非流式 parts 与流式 md 拿到转存后链接，全部通过。完整矩阵与 7 条风险见
`docs/apiduo/image-e2e-2026-11-07.md` 末节。

⚠️ 两个值得记住的口径：`b64_json` 默认不转存（需 `rewrite_paths`，且内联 base64
要 ≥1024 字符）；`=> url clear` 是把源字段置 `null` 而非删键。


### 9.11 部署态验证（2026-11-07 22:57）

`apiduo-v0.0.9` 已发布并部署到 ai-prod 的**独立测试实例** `apiduo-cn-e2e`
（`127.0.0.1:39048`、独立 MySQL 库 `apiduo_cn_e2e`、独立 ClickHouse 日志库
`apiduo_cn_e2e_logs`），生产 `apiduo-cn` 仍是 0.0.8 未动。部署态 8 个用例全 200，
端点分流/载体/回包形状与本地一致。完整记录（含发版失败与 4 个踩坑）见
`docs/apiduo/image-e2e-2026-11-07.md` 末节。

🔴 纪律：新增标记块要**同步提交** `.apiduo/merge-budget.txt`，否则 CI markercheck 判超支，发版会失败。


### 9.12 真实上游（火山方舟 / seedream）渠道配置（2026-11-07 23:45）

部署态独立实例 `apiduo-cn-e2e` 已配真实上游渠道 `volcengine-ark-seedream`
（type 58，`https://ark.cn-beijing.volces.com`，`image_protocol.upstream=seedream`）。

要点：方舟基址是 `/api/v3`，所以 `upstream_path` 与 `primary_path` 都要写成
`/api/v3/images/generations`，`base_url` 不带路径；三入口 `incoming_path` 仍是
`/v1/...`。渠道匹配与协议层接管已用两个本地 400 验证（零出站）。

⚠️ **`LIVETEST_ARK_API_KEY` 已失效**：`GET /api/v3/models` 在工作站/ai-prod 宿主/
容器内三视角均 401 `The API key doesn't exist`（上次实测 2026-09-13）。
配置可用但凭据需更新，未发任何真实生成请求。契约要点（`size` 至少 3686400 像素、
`response_format=url` 返回 TOS 链接）见 `docs/apiduo/image-e2e-2026-11-07.md` 末节。


### 9.13 真实上游 `flux-3-image` 端到端（2026-11-08 01:25）

用户指定 `bfl.1task.cn` 的 `flux-3-image` 做真实生成验证（火山方舟 Key 失效后的替代路径）。
渠道 `bfl-flux-3-image`（type 58，`upstream=seedream`，`primary_path=/v1/images/generations`）。

bfl2openai 的 `/v1/images/generations` **支持 `image: string | list<string>`**（首张当画布、
其余当参考图），正好是 seedream 单端点布局 —— 三入口全部折叠到该端点的 JSON。

真实结果：文生图 200/25.8s、图生图（单元素 image 数组 + 1.6MB data URI 垫图）200/27.3s、
chat 流式 200（SSE 3 帧、md delta）、chat 非流式 200（1 个 `image_url` part）。
其中一次 chat 流式撞上游 503 `image service is busy`，重试即通，失败请求 quota=0。
ClickHouse 实证：4 张成功各 `quota=38`、`usage 1/0`，三入口计费完全一致。

⚠️ chat 入口按 §9.9 口径不下传 `size`，上游走默认更大尺寸 ⇒
流式单帧 md 达 **4.2 MB**，且真实 MPS 成本更高。这是口径代价，不是 bug。

`git diff 0.0.9..HEAD` 对图像协议文件为空 ⇒ 结论对生产 0.0.12 成立。
另记：e2e 日志表是 `MergeTree`、生产是 `ReplacingMergeTree`，升级 e2e 必须同步换引擎，
否则 `logs FINAL` 报 `ILLEGAL_FINAL`。完整记录见 `docs/apiduo/image-e2e-2026-11-07.md` 末节。


### 9.14 部署收尾（2026-11-08 01:45）

- 真实上游 `bfl-flux-3-image` 已同步到生产 `apiduo_cn`（渠道 7、`ModelPrice 0.04`），
  生产 app 以 0.0.12 重建使配置生效；mysql/CH 容器与数据未动。
- 独立测试实例 `apiduo-cn-e2e` 升级到 0.0.12 完成 7/7 零成本矩阵复测后**已拆除**。
- 渠道缓存延迟按需求**只降配置不改代码**：`SYNC_FREQUENCY 60 → 15`（部署机 env）。
  根因是 `AddChannel` 不刷缓存 + 定时全量重载；实测 DB 直写 8.2s 可见。
  ⚠️ 该值不在仓库里，重新部署需带上。详见 `docs/apiduo/image-e2e-2026-11-07.md` 末节。


### 9.15 flux 多端点 / 多形态 / 多垫图全面验证（2026-11-08 02:05）

分层测试：mock 层 41 个零计费形态用例 + 真实 `bfl.1task.cn` 10 个用例 + 24 并发。
覆盖三入口、两上游端点布局、`image` 数组 0/1/2/3/5/10/11 张、裸 base64、data URI、
edits multipart、chat 非流式/流式、`b64_json`/`url` 响应、vendor extras、错误路径。

真实结果：垫 1/5/10 张均 200（10 张 102s），11 张被上游 400 `at most 10 images`
（零计费）；双端点布局图生图走 `/v1/images/edits` multipart 200；chat 两形态 200；
9 条成功日志各 `quota=38 / usage 1/0`；2/5/10 张保序 True；24 并发串扰 0。

🔴 **口径修正**：`allow_url_fetch` 只在 **gpt-image-2 布局**（需把 URL 下载成 multipart
文件）起作用；**seedream 布局下 URL 原样透传**，开关与 SSRF 校验都不触发，SSRF 边界
转到上游服务。此前「URL 一律 400」的结论只在双端点布局成立。
详见 `docs/apiduo/image-e2e-2026-11-07.md` 末节。
