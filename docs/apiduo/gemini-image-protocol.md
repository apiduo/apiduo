# 图片协议 5 × 3：五入口、三上游、主端点不变、不暴露上游

> 读者：改这个仓库的人。第 10 节是对这套方案的反驳。

## 0. 一句话

**五个入口折叠成一份 canonical，三份上游脚本各自编码；主端点仍然是
`/v1/images/generations`，不出现模板变量；客户端从任何入口、打任何上游，
都只看到自己协议的字段。**

## 1. 不变量

| 编号 | 必须成立 | 违反后的症状 |
|---|---|---|
| **I1** | 主端点不变：仍是 `/v1/images/generations`，末段必须是 `generations` | 用户要为每个上游改一次地址，配置面漂移 |
| **I2** | 五个入口只决定「怎么读请求、怎么回包」 | 入口被当成路由，每条入口各配一个上游 |
| **I3** | 五个入口折成同一份 canonical，中间产物不配置 | 五套请求体、五套计费 |
| **I4** | 上游三个脚本；Gemini 的路径由 `base_url + 版本 + 模型名` 构造，不读主端点 | 主端点写模板，违反 I1 |
| **I5** | 出口净化：响应里不出现上游的任何标识 | 客户端能从字段、错误、响应头或 URL 判断上游是谁 |

I1 与 I4 的组合是本方案最容易误读的地方，先解释：

- 主端点是**渠道的图片落点声明**，不是最终 URL。
- 对 `ark`：它就是最终 URL。
- 对 `openai`：它是 generations，图生图时按末段替换推导 edits（契约 §3 的现有规则）。
- 对 `gemini`：它是「这条渠道服务图片」的声明；最终 URL 由
  `base_url + /{version}/models/{model}:generateContent` 构造，主端点的路径不参与。

这不是给 I4 开例外，而是把 I4 本来的含义写清楚：canonical 之后发往**主端点所代表的上游**，
第二遍编码可以改路径（OpenAI 的 edits 也是改路径）。配置面仍然只有一个地址。

## 2. 配置面

### 2.1 渠道级字段

| 字段 | 值 | 说明 |
|---|---|---|
| `primary_endpoint` | `/v1/images/generations` | 不变；末段必须是 `generations` |
| `image_protocol.enabled` | `true` | 不开则整条协议层不介入 |
| `image_protocol.upstream` | `ark` / `openai` / `gemini` | 三选一，描述上游报文形状 |
| `image_protocol.edit_path` | 空 | 只对 `openai` 生效，默认按末段替换推导 |

没有新增字段：没有 `version`、没有 `primary_path`、没有 `allow_url_fetch`、没有协议开关。

### 2.2 Gemini 的路径来源

```text
{base_url}/{version}/models/{upstream_model}:generateContent
```

| 段 | 来源 | 缺省 |
|---|---|---|
| `base_url` | 渠道配置 | 必填，形状校验已有 |
| `version` | `model_setting.GetGeminiVersionSetting(upstream_model)` | `VersionSettings["default"]`（`v1beta`） |
| `upstream_model` | `info.UpstreamModelName` | 模型重定向后的名字 |

不读主端点的原因：主端点的前缀是 `/v1`，Gemini 的版本前缀是 `/v1beta`，两者不是同一个东西。
从主端点推导只能靠字符串猜测，猜错的表现是每次请求 404。

代价：改了主端点不会改变 Gemini 的实际路径。渠道页给一行只读说明：

> Gemini 上游按 `base_url` + 版本设置 + 模型名构造路径；主端点只声明这条渠道的图片落点。

### 2.3 保存期校验

| 规则 | 违反后的症状 |
|---|---|
| `upstream` 必须是三个协议名之一 | 请求期 400，运维看不出是配置问题 |
| 主端点末段必须是 `generations` | 同契约 §3，现有校验不变 |
| `edit_path` 非空时必须是绝对路径 | 请求期才失败 |

Gemini 不需要额外的主端点校验——它不读主端点。

## 3. 入口层：五个前门

| # | 入口 | 折叠函数 | 回包形状 | 启用条件 |
|---|---|---|---|---|
| 1 | `POST /v1/images/generations` | `canonicalFromJSON` | OpenAI Images `data[]` | 协议自身放行（`ServesPath`） |
| 2 | `POST /v1/images/edits` | `canonicalFromForm` | 同上 | 协议自身放行 |
| 3 | `POST /v1/chat/completions` | `canonicalFromChat` | `chat.completion` + markdown | 协议自身放行 |
| 4 | `POST /v1/responses` | `canonicalFromResponses` | `response` + `output_text` | 协议自身放行 |
| 5 | `POST /v1beta/models/{model}:generateContent` | `canonicalFromGemini` | `candidates[].inlineData` | 协议自身放行 |

**五个入口全部默认接管，不需要在 `advanced_routes` 里声明。** 判据是渠道定位而不是请求内容：
开了图片协议的渠道就是图片渠道，它接管的五个入口一律按出图处理。

- 渠道选择器：`channelMatchesFilter` 里 `ImageProtocol.ServesPath(path)` 返回 true 就直接放行，
  不再要求 `advanced_routes` 命中；
- 路由预检：`adaptor.resolveRoute` 用同一份判据合成一条「跟随主端点」的路由，真实地址仍由
  `prepareImageProtocol` 的 plan 决定；
- 配置校验：图片渠道的 `advanced_routes` 可以是空数组（`AdvancedCustomConfig.ValidateForImageChannel`），
  保存时不再被「至少一条路由」拦住；非图片渠道仍然至少一条；
- 代价：**同一条渠道不能既做文本又做图片**，混挂要拆渠道。

### 3.1 images / edits

- `generations`：JSON，`image` 可缺省（文生图）或为 str/list（图生图）。
- `edits`：只接受 multipart 文件流，文本引用一律 400（现有 `requireFileStreamEdits`）。
- `mask` 在五个上游都不转发。
- `size` 与上游的 `(aspectRatio, imageSize)` 是同构的，三种写法都收：
  `1024x1024`（像素）、`1:1`（比例）、`4K`（档位）。像素按官方表取最近比例，
  档位按面积取最近、降质超三分之一升一档；超 4K 档两倍报 400。
  `aspect_ratio` 可单独覆盖比例（new-api 既有字段）。响应里 `data[].size` 回填
  **从交付字节头测得的真实像素**，闭合这条映射（§5.1）。

### 3.2 chat / responses

- chat：取最后一条 user 轮；更早的 user 轮与 assistant 轮丢弃；system/developer 前拼。
- responses：`canonicalFromResponses` 直接读 `input` / `instructions`，取最后一条 user 输入。
  不经过 chat——绕一层会把 `input_image` 的 data URI 全量序列化一遍，也会把工具调用、
  `previous_response_id` 这些与图片无关的语义带进来。
  回包由 `wrapResponsesResponse` 直接构造 `output_text`，同样不依赖 chat 中间态。
- 两个入口回包都是 markdown；data URI 会很大，这是已知代价（契约 §2.1）。
- **responses 的 `stream=true` 走伪流式**：上游仍是非流式，`writeResponsesImageStream`
  把完整 markdown 造一个 `chat.completion.chunk`，交给
  `relayconvert.NewResponseStreamState(OpenAI → OpenAIResponses)` 生成事件，再用
  `helper.ResponseChunkData` 写出。不手写事件序列，手写必漂移。转换器实际输出六个：
  `response.created` → `response.output_item.added` → `response.output_text.delta` →
  `response.output_text.done` → `response.output_item.done` → `response.completed`。
  不含 `response.in_progress` 与 `response.content_part.*`——这是仓库
  `responses_via_chat` 的既有最小序列，不是图片路径独有的省略。
  `completed` 的 usage 与账本同口径（`{1,0,1}`）。

### 3.3 Gemini 原生入口

`/v1beta/models/{model}:generateContent` 已在 `/v1beta` 路由组，`RelayModeGemini`，
`info.Request` 是 `*dto.GeminiChatRequest`。不新增 router 条目。

判据只有一层：`image_protocol.enabled`。

| 层 | 判据 | 谁执行 | 不满足 |
|---|---|---|---|
| L1 | `image_protocol.enabled` | 渠道选择器 `ServesPath` + `prepareImageProtocol` 首行 | 渠道不认这条路径，503 |

**渠道定位约定：开了图片协议的渠道就是图片渠道。** `:generateContent` 是它默认接管的五个入口
之一，不需要额外声明路由；请求进来一律按出图折叠，不看 body，也不查全局模型名单。

不查 `IsGeminiModelSupportImagine` 的原因：那份名单是 chat 转换用来决定是否加
`responseModalities` 的，不是渠道能力判据。放进图片入口会制造新的静默失败——渠道配好了，
全局名单漏加，请求就不出图，还没有任何报错。

因此也不需要 `responseModalities` 作为判据：它是客户端可选项，网关不替它推断；渠道声明
已经表达了意图。

代价完全落在运营侧：**同一条渠道不能既做文本又做图片**，混挂的文本模型也会被按出图处理。
渠道页的「入口去向」会列出五个默认入口（即使路由表里没有它们），提醒这条渠道的请求
全部按出图处理。这是展示，不是运行时开关。

折叠：

| Gemini 请求 | canonical |
|---|---|
| `contents` 最后一条 `role=user` 的 `parts[]` | 只取这一轮 |
| `parts[].text`（多个） | `prompt`，按序拼接 |
| `parts[].inlineData`（可多张） | `image` = `data:<mime>;base64,<data>`，**保序**；多张保持数组 |
| `parts[].fileData`（`fileUri` 公网 http(s)） | `image` 里的一项 URL 引用，**与 inlineData 按 parts 顺序合并**；落不落成字节由脚本层按上游决定（§3.3.1） |
| URL 形态的 `inlineData.data` | **400**：不是 Gemini 的语法，公网引用属于 `fileData.fileUri` |
| `generationConfig.imageConfig.aspectRatio` | `aspect_ratio`（原值，只给 `planGemini`） |
| `generationConfig.imageConfig.imageSize` | `image_size`（大写，只给 `planGemini`） |
| `aspectRatio` / `imageSize` 任一 | **同时**折出 `size`（官方像素 WxH，见 §3.3.1） |

#### 3.3.1 `imageConfig` 折成 `size`，载体只有 b64

`aspect_ratio` / `image_size` 是 Gemini 方言，只有 `upstream=gemini` 的 `planGemini` 读它们。
上游是 ark / openai 形状时（duck 就是 ark 形状），请求体只认 `size` —— 不折这一步，比例和
档位会一起丢（2026-11-25 实测：`16:9 2K` 与 `1:1 4K` 都恒出 1254×1254）。

折算口径（`geminiCanonicalSize`）：

| 输入 | canonical `size` | 理由 |
|---|---|---|
| `aspectRatio` + `imageSize` | 官方像素表的精确 WxH（`16:9` + `2K` → `2752x1536`） | duck 的档位只能表方形、比例只能控比例；同时兑现两者只有 WxH 一种形态 |
| 只有 `aspectRatio` | 该比例的 1K 档（`16:9` → `1376x768`） | `imageSize` 的官方默认值就是 1K |
| 只有 `imageSize` | 该档位的 1:1（`2K` → `2048x2048`） | `aspectRatio` 的官方默认值就是 1:1 |
| 比例不在官方表 | 按档位面积算，宽高取 8 的倍数 | 不因为网关枚举落后就 400 |
| `imageSize` 不是 1K/2K/4K | **400** | 这个值上游也认不出，丢掉等于让客户端以为档位生效了 |
| `aspectRatio` 不是 `a:b` | **400** | 同上 |

官方像素表（1K/2K/4K × 14 个比例）来源：Google Gemini API image generation 文档的
resolution 表。`16:9` 的 1K 是 1376×768，不是按 1024² 面积现算的 1368×768 ——
与官方一致，同一份 `imageConfig` 在这条链路和 Google 直连上拿到同一组像素。

`aspect_ratio` / `image_size` 原值仍保留在 canonical 里给 `planGemini` 用（从像素反推比例
是有损的，表外比例会被算回去）。发给 ark / openai 形状的上游之前，`dropEntryDialect()`
会把这两个键删掉：它们已经被 `size` 兑现，再下传就是「收下再丢」。

**载体有两种：`inlineData` 的 base64 与 `fileData` 的公网 URL。** 两者按 `parts` 出现顺序
合并进同一个 `image` 数组 —— 顺序就是语义，混合载体也不许先收完 b64 再补 URL。URL 要不要
落成字节由脚本层按上游能力决定：

| 上游 | URL 引用怎么处理 | 代价 |
|---|---|---|
| `ark`（duck） | `image` 字段原样带 URL | 零下载，上游自己抓 |
| `openai` / `gemini` | 下载后转 data URI / inlineData | 单张 ≤20 MiB、整批 90s、首字节 10s；SSRF 三道闸（解析后校验、逐跳重定向重验、私网段拒绝）在下载层 |

三级降级，不拒绝合法语法：

| 情况 | 行为 |
|---|---|
| 上游收 URL | 直接折进 `image`，零下载 |
| 上游只收 base64 | 网关下载后内联 |
| 下载失败 / 超大 / 非图片 | 400 且 message 指明原因；网关到图源之间的问题（5xx / 超时）回 502 并保留重试 |

**收下 URL 不等于收下任何字符串。** `gs://` / `file://` 一律 400（网关和上游都取不到），
字面量内网地址（`127.0.0.1`、`10.x`、`localhost`…）也在折叠层就拒：ark 形状的上游会原样
转发链接并自己抓取，不挡等于让客户端借上游网段做 SSRF。域名不在折叠层解析（纯函数不碰
网络），解析后的校验在 `validateImageRefURL`。

| 客户端给的 | 行为 |
|---|---|
| `parts[].inlineData` + `mimeType: image/*` + base64 | 收下，多张保序成数组 |
| `parts[].fileData.fileUri` 是公网 http(s) | 收下，与 inlineData 按序合并 |
| `fileData.fileUri` 是 `gs://` / `file://` / 空 / 无 host | **400**，message 指明只收公网 http(s) |
| `fileData.fileUri` 是字面量内网地址 / `localhost` | **400**（SSRF） |
| 一个 part 同时给 `inlineData` 与 `fileData` | **400**：一个 part 只有一个载体 |
| `inlineData.data` 是 `http(s)://` / `gs://` / `data:` | **400**，message 指向 `fileData.fileUri` |
| `inlineData.mimeType` 缺失 | **400** |
| `inlineData.data` 为空 | **400** |
| 非 `image/*` 的 `inlineData` / `fileData`（音频 / PDF） | 跳过：不参与图像语义，与 text part 里的无关内容同等对待 |

旧版本（0.0.220 ~ 本改动前）把 `fileData` 一律 400 是方向错的：官方文档把「用网址传图」
与 inlineData、File API 并列为三种输入方式，Vertex 还给了 15 MiB 的单文件上限与专用错误码
`URL_RETRIEVAL_STATUS_UNSAFE`。拒绝合法语法会把 Gemini SDK / Vertex 封装客户端整类挡在门外。

**流式载体按 `alt` 分派。** `:streamGenerateContent` 带 `?alt=sse` 回 SSE（Google SDK
默认如此），不带则回官方默认的 JSON 数组流 `[{…}]`。一刀切回 SSE 会让 curl 与自研客户端
按 JSON 数组解析失败。两种载体都只有一帧：上游始终走 `:generateContent`，不伪造渐进输出。

**渠道配置：一条路由，`converter` 留空。**

```json
{
  "incoming_path": "/v1beta/models/{model}:generateContent",
  "upstream_path": "/v1beta/models/{model}:generateContent",
  "models": ["xx"]
}
```

- `incoming_path` 只写 `:generateContent`。`matchAdvancedCustomIncomingPath` 对含
  `:generateContent` 的配置会自动替换成 `:streamGenerateContent` 再匹配一次
  （`channel_settings.go:485`）。**反向不成立**：只写流式那条，非流式请求会 503。
- `upstream_path` 必填（`Validate` 拒绝空值），但图片协议下会被 `planGemini` 覆盖，
  填同样的模板只为可读。
- `converter` **必须留空**。填 `openai_chat_to_gemini_content` 后，`buildRouteURL` 的
  `shouldUseGeminiStreamURL` 在 `info.IsStream` 为真时会把上游改写成
  `:streamGenerateContent?alt=sse`（`adaptor.go:564`），图片解码器读到 SSE 直接 502。
  写死模型名同理：`/v1beta/models/xx:generateContent` 也自动覆盖流式。

### 3.4 模型名：模板、别名、字面量

`{model}` 是入站路径占位符，按前缀/后缀匹配，且模型名不能含 `/`（`matchAdvancedCustomIncomingPathTemplate`）。
三种配置形态：

| 形态 | `incoming_path` | 渠道 models | `model_mapping` | 客户端可请求 | 上游收到 |
|---|---|---|---|---|---|
| 模板 | `/v1beta/models/{model}:generateContent` | `gemini-2.5-flash-image` | 空 | 仅这一个名字 | 同名 |
| 别名 | 同上 | `banana` | `{"banana":"gemini-2.5-flash-image"}` | 仅 `banana` | 固定 |
| 字面量 | `/v1beta/models/gemini-2.5-flash-image:generateContent` | `gemini-2.5-flash-image` | 空 | 必须请求这个字面路径 | 同名 |

**字面量路径不是重定向。** 配置路径不含占位符时，匹配器要求字符串完全相等；请求别的模型名
直接 503（`model_not_found`）。它只是把入口收窄到一个名字，没有「任意名都转发到固定模型」的语义。

**「只服务一个模型」的落点是渠道 models 只填一个，不是写死路径。** abilities 由
`channel.Models` 精确生成（`model/ability.go:226`，不支持通配），是渠道选择的唯一依据；
写死路径不改变它，只会多出一处必须与 models 保持一致的白名单——两处任一写错都是 503。

**`model_mapping` 是精确键映射**（`pkg/modelalias/alias.go:24`），支持链式与 base model 回退，
没有通配。要让多个客户端名落到同一个上游模型，必须为每个名字各建一条 ability 与一个映射键；
本仓库不支持「任意名 → 固定上游」。

**Gemini 上游不读路由的 `upstream_path`。** 图片协议下它的地址由 `planGemini` 构造并覆盖
`route.UpstreamPath`（`apiduo_image_adaptor.go:291`），所以写死上游模型只能靠 `model_mapping`，
在 `upstream_path` 里改模型名无效。

**模型名带命名空间时必须用字面量路径。** 聚合商的 Gemini 模型 ID 是
`google/gemini-nano-banana-2.1`，`{model}` 只表示一个路径段，不能匹配含 `/` 的值：

```json
{
  "incoming_path": "/v1beta/models/google/gemini-nano-banana-2.1:generateContent",
  "upstream_path": "/v1beta/models/{model}:generateContent",
  "models": ["google/gemini-nano-banana-2.1"]
}
```

不要为了它放宽共享匹配器：异步提交路径（`/v1beta/models/{model}:generateContent`）用的是
同一个函数，放宽会把 `/a/b` 误当成模型名 `a/b`。字面量路径走精确相等分支，天然支持多段。

上游构造侧由 `escapeGeminiModelPath` 按段转义：整串 `url.PathEscape` 会把 `/` 变成 `%2F`，
上游按单段解析后 404；逐段转义才能既保留路径分隔、又转义段内字符。

**字面量路径 + `model_mapping` 可以实现「一个对外名，固定上游模型」。** 链路是：

```text
POST /v1beta/models/xx:generateContent
  → 路径字面量相等，命中路由
  → extractModelNameFromGeminiPath 取出 xx
  → abilities 里有 xx，渠道选中（OriginModelName = xx）
  → model_mapping {xx: gemini-2.5-flash-image}，UpstreamModelName = gemini-2.5-flash-image
  → planGemini 拼 /{version}/models/gemini-2.5-flash-image:generateContent
```

三处必须同时配：渠道 models 含 `xx`、`model_mapping` 含 `xx` 键、**模型定价里有 `xx`**。
计费按 `OriginModelName` 查价（`resolveBillingModelName` 只解析模型名修饰符，不读
`model_mapping`），`xx` 没配价且用户未开「接受未定价模型」时返回 `model_price_not_configured`。

边界：客户端必须请求 `xx` 这个字面路径，请求别的模型名是 503。这是「入口只接受一个名字」，
不是「任意名字都转发到 xx」——后者需要 abilities 枚举全部名字，见上文。

### 3.5 实测：nano-banana-2.1 一次调用回两张图

2026-11-23 经代理实测 `zenmux.ai`（`google/gemini-nano-banana-2.1`）：

```text
POST /api/vertex-ai/v1beta/models/google/gemini-nano-banana-2.1:generateContent
→ 200, 9.98 MB
parts[0] text+thought  "refining the concept…"
parts[1] inlineData    image/png  1376×768  （草稿）
parts[2] text+thought  "completed the current illustration…"
parts[3] inlineData    image/png  1376×768  （成品，字节不同）
```

四组实测（同一模型，同一 key，经代理）：

| `responseModalities` | `thinkingBudget` | 图片数 | 响应体 | 耗时 |
|---|---|---|---|---|
| `["TEXT","IMAGE"]` | 不传 | **2** | 9.98 MB | 18.5 s |
| `["TEXT","IMAGE"]` | `0` | **2** | 5.84 MB | 21.2 s |
| `["IMAGE"]` | `0` | **1** | 3.71 MB | 14.7 s |
| `["IMAGE"]` | 不传 | **1** | 5.38 MB | 29.3 s |

结论：

- **`responseModalities` 是唯一有效的手段。** 只写 `IMAGE` 就只回一张成品，
  响应体从 9.98 MB 降到 3.71–5.38 MB。
- **`candidateCount: 1` 无效**，仍然两张——这是迭代出图（草稿→成品），不是候选采样。
- **`thinkingBudget: 0` 无效**，只去掉 `thought` part，图片数不变。但它会把
  `totalTokenCount` 从 1947 降到 1123，属于省 token 的优化，不影响张数。

所以 `planGemini` 固定发 `["IMAGE"]`。`deliverableImages` 保留为兜底：模型若无视模态
仍回多张（例如 future 版本改回交错生成），裁剪保证 `data[]` 长度仍等于 `n`。

上游元数据（`/api/vertex-ai/v1beta/models`）也印证了这个行为：该模型标注
`supports interleaved text and image generation`、`outputModalities: ["image","text"]`，
按 token 计价（image $30/1M tokens，completion $7.5/1M tokens）。

因此交付规则按入口分：

| 入口 | 交付 | 理由 |
|---|---|---|
| `images/generations`、`images/edits`、`chat`、`responses` | **只取最后一个 inlineData** | 单次调用只产出一张成品；`n` 只决定计费张数，不决定 `data[]` 长度 |
| `:generateContent`（Gemini 原生） | **全部 part** | 客户端说 Gemini，就按 Gemini 语义回；两个 part 都是上游的合法输出 |

实现是 `deliverableImages`（`apiduo_image_response.go`）：只对 `upstream=gemini` 且多个图时
裁到最后一个。2026-11-23 起三条上游都不按 n 扇出（`n` 不上行，见
`image-endpoint-contract.md` §2.4）；裁剪仍只对 gemini 生效——只有它会在一次回包里
给草稿 + 成品两张。

裁掉草稿也省一次转存：`alignImageCarriers` 在裁剪之后执行，草稿不会被上传对象存储。

同一份回包里还有三处需要处理：

| 上游字段 | 处理 |
|---|---|
| `usageMetadata.promptTokenCount/candidatesTokenCount/totalTokenCount/thoughtsTokenCount` | 透出，作为客户端看到的用量（Gemini 入口） |
| `usageMetadata.trafficType`（`ON_DEMAND`） | **丢弃**：这是上游的调度信息 |
| `createTime` / `responseId` | **丢弃**：上游请求标识 |
| `candidates[].index` | 上游不返回；网关重建时补 `0` |
| `parts[].thought` | 丢弃（Gemini 入口重建时只保留 inlineData） |
| `parts[].thoughtSignature` | 丢弃。实测成品图那一 part 带约 3.6 MB 的签名——响应体接近 10 MB 主要来自它，不是图本身。签名用于多轮续写，网关每次请求都是无状态单发，留着只增加体积 |
| `mask`（图生图蒙版） | **拒绝 400**。Gemini 没有独立蒙版通道，静默忽略会让客户端以为在做局部重绘，实际拿到整图重绘且有计费 |

`usageMetadata` 只影响展示，账本仍是 `Usage{Prompt:1, Completion:0, Total:1}` —— 
入口与上游都不改变价钱。

## 4. 上游层：三个脚本

| upstream | 端点 | 载体 | 生图 / 编辑 | 回包解析 |
|---|---|---|---|---|
| `ark` | 主端点 | JSON | 同端点，`image` 为 str/list | `data[]` |
| `openai` | 主端点 + 末段替换的 edits | JSON / multipart | 文生图主端点，图生图 edits | `data[]` |
| `gemini` | `base + /{version}/models/{model}:generateContent` | JSON `contents/parts` | 同端点，多传 `inlineData` | `candidates[]` |

三者的脚本签名相同（`canonical → plan{path, contentType, body}`），入口不感知上游。

**脚本只描述报文形状，不描述某家实现的行为。** 判据里不出现厂商名、域名、`DUCK_*` 这类
常量，也不出现「4~9 张会拼 contact sheet」「最多几张」「放大系数」这类能力假设 ——
那些是上游自己的事，它拒绝就按错误信封回，网关不预判、不截断、不替它降档。
判断标准：把这条分支的注释换到任意一家同类上游身上，如果它变得没意义，那它就是过度适配。

**multipart 的多图字段名是 `image[]`，不是重复的 `image`。** 这是 OpenAI 官方文档与官方
SDK 的编码方式（数组参数在 multipart 里写成同名 `key[]` 重复）。只认裸 `image` 的上游
（duck 的 openai 模式实测 400 `image is required`）属于实现不符合约定，网关侧不猜：
改成重复 `image` 会让真正的 OpenAI edits 多图失效，两者只能二选一。接这类上游用它自己的
ark 模式（单端点 JSON，`image` 直接是数组），或让上游接受 `image[]`。

**回包解码必须按 upstream 分派。** `doImageProtocolResponse` 不能无条件用 OpenAI 解码器：
gemini 上游回的是 `candidates[]`，用 `decodeImageResponse` 解会得到空 `data[]`，表现为
「编码对、解码错」——日志里上游 200，客户端收到 502。唯一的按上游分叉点是
`decodeImageForUpstream(raw, upstream, createdAt)`，images / chat / responses 三个入口共用。
判定用 `dto.NormalizeImageUpstream`，历史值（`seedream` / `gpt-image-2`）自动收敛。

`gemini` 脚本的固定写入：

```json
{
  "contents": [{ "role": "user", "parts": [{ "text": "…" }, { "inlineData": { "mimeType": "image/png", "data": "…" } }] }],
  "generationConfig": {
    "responseModalities": ["IMAGE"],
    "imageConfig": { "aspectRatio": "16:9", "imageSize": "2K" }
  }
}
```

`responseModalities` 只写 `IMAGE`（不带 `TEXT`）：这是唯一能压住「草稿图 + 成品图」
两张的开关，见 §3.5 的四组实测。

- `n` 不转发：上游恒出 1 张，计费张数按客户端请求的 n（`image-endpoint-contract.md` §2.4）。
- `size` 按 §3.1 折算成 `(aspectRatio, imageSize)`；`aspect_ratio` 覆盖比例维；
  `image_size` 只作为档位别名兼容老客户端，与 `size` 冲突时 `size` 赢。
- `seed` / `style` / `quality` / `negative_prompt` / `mask` 丢弃并记 `other.dropped_params`。

## 5. 出口净化：不暴露上游

五条规则，按「客户端能看到什么」排列。原则：**响应按入口白名单重建，任何情况下不透传上游报文。**

### 5.1 响应体

| 入口 | 允许出现的字段 | 必须丢弃 |
|---|---|---|
| images | `created` `data[].url` `data[].b64_json` `data[].revised_prompt` `data[].size` `usage?` | `candidates` `modelVersion` `promptFeedback`、上游的 `size` / `model` / `size_applied` 以及一切上游附加字段 |
| chat | `id` `object` `created` `model` `choices` `usage` | 同上 |
| responses | `id` `object` `created_at` `status` `model` `output` `usage` | 同上 |
| gemini | `candidates` `usageMetadata` `modelVersion` `promptFeedback` | `data` `created` `object` 以及 OpenAI 特有字段 |

**上游的 `size` 不回显，网关测出来的回。** 上游回的 size 是它自己重采样的声明
（duck 把 1254² 放大到 2048² 再回 `size=2048x2048`），透传会被当成模型原生像素。
images 入口的 `data[].size` 改为网关从**交付字节的 PNG/JPEG 头**量出的宽高 ——
这是 `size -> (aspectRatio, imageSize)` 的回程，客户端传 2048x2048、上游实际给
2752x1536 时必须能看到 2752x1536。认不出格式就不填，不猜。
同理，`model` 是上游身份，客户端的模型名由 `OriginModelName` 提供（§5.2），
两者都不从上游报文里取。

实现上不是「解析后原样编码」，而是「按入口的结构体重建」。现有
`finishImageNativeResponse` 在解析失败时 `IOCopyBytesGracefully` 原样透传——这条兜底必须删除：
它会把 `candidates[]` 直接给 OpenAI 客户端，也会把上游错误页原样吐出。

解析失败一律 502 `upstream_unavailable`，日志里保留原始 body（管理端可见）。

这条改动作用于**所有图片渠道**，不只是 Gemini。上线前先统计存量 `ark` / `openai` 的解析
成功率：边缘形状要先补解码器，再删兜底。只对 Gemini 严格、对其余透传是不可取的——
客户端能通过「解析失败时是否透传」区分上游，反而制造了新的泄漏面。

### 5.2 模型名

三种名字必须分开：

| 名字 | 用途 |
|---|---|
| `OriginModelName` | 回包里的 `model` / `modelVersion`，客户端只认这个 |
| `UpstreamModelName` | 拼 URL、发上游 |
| `BillingModelName` | 计费查价 |

回包写 `UpstreamModelName` 是最常见的泄漏：模型重定向后，客户端会发现自己的模型名被换成了别人的。

### 5.3 错误信封

| 入口 | 形状 |
|---|---|
| images / chat / responses | `{"error":{"message","type","param","code"}}` |
| gemini | `{"error":{"code","message","status"}}` |

- 复用 `relaykit/types/error_catalog.go` 的码表（`clientErrorByCode` + `applyClientFacing`），
  上游 message 一律替换成固定英文，真实原因写 `SetAdmin` 日志。
- **接入点在 `controller/relay.go` 的 defer**：`switch relayFormat` 必须有
  `case types.RelayFormatGemini` → `c.JSON(status, ToGeminiError())`；流式错误走
  `writeRelayStreamError` 的 Gemini 分支。少这两个 case，`ToGeminiError` 就是死代码，
  Gemini 入口会回落 OpenAI 信封，把上游原文（含 `models/…`）带给客户端。
- **`SetClientFacing` 的条件**：`RemapHardeningEnabled` 或「Gemini 入口 + 图片协议开启」。
  后者无条件：Gemini 图片渠道的错误必须固定文案，不能依赖硬化开关；文本渠道保持原条件，
  避免改变存量诊断。
- 新增 `ToGeminiError`：`status` 按 HTTP 码映射（400 `INVALID_ARGUMENT`、403 `PERMISSION_DENIED`、
  404 `NOT_FOUND`、429 `RESOURCE_EXHAUSTED`、5xx `INTERNAL`）。
- 安全过滤（`finishReason=SAFETY` / `IMAGE_SAFETY` / `PROHIBITED_CONTENT` /
  `IMAGE_PROHIBITED_CONTENT` / `BLOCKLIST` / `RECITATION` / `IMAGE_RECITATION` / `SPII` /
  `blockReason`）统一成 400 + `invalid_request`，不把上游的原始 reason 写进客户端 message
  （reason 只进管理端日志）。码没有用 `content_filter`：目录里 `prompt_blocked → content_filter`
  只有走 `SetClientFacing` 才生效，而那条路会把 message 换成固定文案，客户端就看不出是提示词被拒。
- 零图且不是上述理由时，错误文案带上 `finishReason`（`upstream returned no image
  (finishReason=…)`）：管理端日志必须能区分「上游抽风」和「被挡了」。

### 5.4 响应头

复用 `service.ShouldCopyUpstreamHeader` 的白名单，图片协议**无条件**执行（不等渠道开硬化）：

保留：`Content-Type`、`Cache-Control`、`Retry-After`、`X-Accel-Buffering`。
删除：`Server`、`X-Goog-*`、`X-Request-Id`（上游的）、`Set-Cookie`、`X-RateLimit-*`。
`Content-Length` 由网关重算。

### 5.5 图片 URL

上游给链接时，链接的 host 会直接暴露上游（`*.volces.com`、`storage.googleapis.com`）。
规则：

| 上游给 | 渠道有存储 | 渠道无存储 |
|---|---|---|
| base64 | 落存储换链接（客户端要 url 时） | 回 `b64_json` |
| URL（非本域） | 下载 → 落存储 → 回本域链接 | **改写为 b64**，不透传原链接 |

这是对契约 §2.2「上游给 url 就透传」的收紧，登记在案：不暴露上游优先于少一次下载。
失败时回 b64，不把「转存失败」变成 502。

### 5.6 无法消除的侧信道

以下三项登记为已知边界，不承诺消除：

- 首字节延迟（Gemini 4K 比 ark 慢一个量级）；
- 图片的编码细节（PNG 压缩参数、EXIF）；
- 客户端能看到的 `created` 时间精度。

## 6. 计费

五个入口、三个上游共用两个计费口径，由模型**怎么配价**决定，不由入口决定：

| 模型配置 | `UsePrice` | 口径 | 公式 |
|---|---|---|---|
| `ModelPrice` 有值 | true | 按次 | `模型价 × n × 分组倍率` |
| 只有 `ModelRatio` | false | 按 token | `(prompt + completion × CompletionRatio) × ModelRatio × 分组倍率` |

**按 token 计费不需要改计费引擎**——`calculateTextQuotaSummary` 的非 `UsePrice` 分支
本来就是按 token 算的。缺的是「图片入口上报真实用量」：之前四个入口一律返回写死的
`{1,0,1}`，所以按 token 计价的图片模型每个请求只收 1 个 token，等于免费。

现在 `imageResponse.billedUsage()` 从上游取真实用量：

| 上游形状 | 来源 | 折算 |
|---|---|---|
| Gemini | `usageMetadata` | `prompt = promptTokenCount`，`completion = totalTokenCount - promptTokenCount` |
| ark / OpenAI 形状 | envelope 的 `usage` | `prompt = prompt_tokens/input_tokens`，`completion = completion_tokens/output_tokens` |
| 都没有 | `pkg/imageusage` | 本地下界估算，标 `usage_estimated`；模型无公开 token 表才退回 `{1,0,1}`（按次口径的下限，也是 `hasBillableUsage` 的下限） |

**`completion` 取 `total - prompt`，不取 `candidatesTokenCount`。** Gemini 的
`totalTokenCount` 含 `thoughtsTokenCount`（思考按输出计费），用 candidates 会漏掉思考
token。实测 `nano-banana-2.1`：prompt 3 / candidates 1120 / thoughts 526 / total 1649，
按 token 计费要收 3 + 1646。

配置示例（zenmux 官方价：prompt $1.5/1M、completion $7.5/1M、image $30/1M）：

```json
{
  "ModelRatio": {"google/gemini-nano-banana-2.1": 0.75},
  "CompletionRatio": {"google/gemini-nano-banana-2.1": 20}
}
```

折算规则：`每 token 配额 = 美元价 × QuotaPerUnit / 1e6`，`QuotaPerUnit=500000` 时
$1.5/1M → 0.75、$30/1M → 15。`CompletionRatio` 是相对 `ModelRatio` 的倍数，
所以 `15 / 0.75 = 20`。**不是** `30 / 7.5`：分子分母取错了维度。

**CompletionRatio 要按 image 价折，不能按 completion 价。** `billedUsage` 把
`candidates + thoughts` 合成一个 `completion`，而图片模型的输出 token 绝大多数是
IMAGE 模态，上游按 `image` 计价（这里是 `completion` 的 4 倍）。同一次调用
（prompt 11 / 输出图片 1120 / 输出文字含思考 522）三种口径：按 0.25 收 $0.0126、
按模态精确算 $0.0375、整段输出按 image 价一刀切 $0.0493。倍率口径下宁可高收
不可低收（多收的文本/思考只有几百 token，$0.0004 量级）。

要精确到模态只能用表达式计费（`billing_setting.billing_mode[model]=tiered_expr`）。
图片入口与文本共用 `PostTextConsumeQuota`，所以表达式对 `/v1/images/generations`
同样生效，且能拿到 `image_count`、`img_o`。`img_o` 来自
`candidatesTokensDetails[modality=IMAGE]`（Gemini）或
`completion_tokens_details.image_tokens`（OpenAI 形状），表达式引用它之后 `c`
会自动扣掉这部分。zenmux 这个模型写成：

```text
v1: p * 1.5 + c * 7.5 + img_o * 30
```

`p * 1.5` 是 $1.5/1M 输入，`c * 7.5` 是剩下的文字/思考输出，`img_o * 30` 是图片
输出。输入图片（图生图）还没拆出来，仍在 `p` 里按输入价收。

`cachedContentTokenCount` 也不参与计费：Gemini 的缓存命中量没有被拆出来，
prompt 一律按全价收（对图片模型可忽略）。

两条口径都遵守的约束：

- `n` 不上行；计费张数按客户端请求的 n（`image-endpoint-contract.md` §2.4）。
- **每个图片入口都要标记 `ImageRequestCount`**（`markImageRequest`）。它影响
  `BillingSession.reserve` 的 `requireAvailableQuota`：不设时 `relayInfo.Request` 不是
  `*dto.ImageRequest`，授信额度用户可以跳过可用额度检查，把余额打成负数。
- 展示与账本同源：回包的 `usage` / `usageMetadata` 与账本用同一组数字，避免
  「响应说 1 个 token、账单按 1100 收」。
- 4K 与 2K 同价是已知偏差（Pro 差 1.79 倍），v2 用 `BillingRatios["image_size"]` 解决。
  按 token 计费的模型天然没这个问题（4K 的输出 token 更多）。

实测（本地，`xr` 走 ModelRatio=1 / CompletionRatio=1，`xx` 走 ModelPrice=0.02）：

```text
model  prompt  completion  quota
xr     7       1290        1297      ← 7 + 1290，按 token
xx     1       0           10000     ← 0.02 × 500000，按次，未受 token 影响
```

### 6.0 上游不返 usage 时的本地下界估算

`pkg/imageusage` 按模型族查公开表，一律取官方口径的下界：尺寸未知按 1024×1024，
quality 未知按 low，输入图按最小画布计，思考 token 记 0，结果向下取整。

| 模型族 | 单张输出 token |
|---|---|
| `gpt-image-1` / `-1-mini` / `-1.5` | 1024²：low 272 / medium 1056 / high 4160；其余尺寸按面积外推 |
| `gpt-image-2` | 1024²：low 196 / medium 1763（按官方价格比推算）/ high 7024 |
| `gemini-2.5-flash-image` | 1290，与宽高比、分辨率无关 |
| `gemini-3-pro-image*` | 1K/2K 1120，4K 2000 |

估算只对表内模型生效，表外模型保留 `{1,0,1}`，不凭空造数。估算结果同源写入
回包 `usage` 与账本；`CompletionTokenDetails.ImageTokens` 必须一起填，否则表达式
的 `img_o` 为 0、整段输出按文本价收。日志 `other.usage_estimated=true` 标记本次
结算走了估算，便于和真实上游对账后调表。

### 6.1 生产实测（2026-11-23，apiduo.cn 0.0.201，zenmux 真实上游）

临时渠道：group `zenmux-test`、`image_protocol.upstream=gemini`、base_url
`https://zenmux.ai/api/vertex-ai`，两个公开模型名分别落到按 token 与按次。
均为 `POST /v1/images/generations` 的真实请求（单次 21–46s，返回 1.2–1.4 MB PNG）：

| 公开模型 | 配价 | 入口 | prompt | completion | 实收 quota | 期望 | 结论 |
|---|---|---|---|---|---|---|---|
| `zenmux-gemini-token` | ratio 15 / completion 0.25 | images | 11 | 1642 | 6323 | 11×15 + 1642×3.75 = 6322.5 | 一致 |
| `zenmux-gemini-call` | price $0.05 | images | 11 | 1896 | 25000 | 0.05 × 500000 | 一致 |
| `zenmux-gemini-token` | ratio 15 / completion 0.25 | chat | 10 | 1966 | 7523 | 10×15 + 1966×3.75 = 7522.5 | 一致 |

三条日志 `other.usage_billing_path` 都是 `upstream`；按次那条的 `completion_tokens`
是 1896，`quota` 仍是 25000 —— 按次口径确实不吃 token。小数按四舍五入
（6322.5 → 6323）。

**已知缺口：images 入口的回包没有 `usage`。** Gemini 的 usageMetadata 只进了账本
（`imageResponse.Usage` 没有写进 `encodeImageResponse` 的信封），chat 入口有。
ark/openai 上游反而会把 envelope 的 `usage` 原样带出，于是同一个入口按上游不同
给不给 usage。按 token 计价的客户端目前只能从账单侧核对。

## 7. 测试方案：两类 + 一条不可区分性

测试按矩阵的两个方向拆，不按「入口」或「上游」单维铺开。

### 7.1 T1：Gemini 上游 × 五个入口

同一语义的请求（文生图、单图编辑）分别从五个入口进入，断言去程与回程。

| 用例 | 入口 | 断言 |
|---|---|---|
| T1-1 | images/generations | 上游收到 `:generateContent`；`parts` 只有 text；回包 `data[0].b64_json` |
| T1-2 | images/edits（multipart） | 文件变成 `inlineData`，不是 multipart 转发 |
| T1-3 | chat/completions | 回包 `choices[0].message.content` 是 markdown 字符串 |
| T1-4 | responses（`stream=false`） | 回包 `output[0].content[0].type = output_text`，text 是 markdown |
| T1-5 | responses（`stream=true`） | SSE 依次出现 `response.created` → `output_item.added` → `output_text.delta` → `output_text.done` → `output_item.done` → `response.completed`；`delta` 含完整 markdown；`completed.usage.total_tokens = 1` |
| T1-4 | responses | 回包 `output[0].content[0].text` 是 markdown |
| T1-5 | Gemini 原生 | 回包 `candidates[0].content.parts[0].inlineData`，`finishReason=STOP` |

每个用例再加三个变体：`imageConfig` 五档比例、`n=2`（断言 400 且**未发上游**）、
`SAFETY`（断言 400 `invalid_request`、未重试）。

### 7.2 T2：非 Gemini 上游 × Gemini 入口

Gemini 入口发同一请求，分别打 `ark` 与 `openai`。

| 用例 | 上游 | 断言 |
|---|---|---|
| T2-1 | ark | 上游收到主端点 JSON，`prompt`/`image` 形状正确；`size` 来自 imageConfig 折算（`16:9`+`2K` → `2752x1536`），且**不带** `aspect_ratio`/`image_size`；回包是 Gemini 形状 |
| T2-2 | openai（文生图） | 上游收到主端点 JSON，无 `image` |
| T2-3 | openai（图生图） | 上游收到 edits multipart，文件 part 正确；回包是 Gemini 形状 |

T2 的关键不是转发，而是**回包**：ark/openai 返回 `data[]`，必须重建成
`candidates[].inlineData`，且 `modelVersion` 是请求名。

### 7.3 T3：不可区分性（不暴露上游的验收）

同一个 `/v1/images/generations` 请求，分别打三个上游，收集响应后逐字段比较：

- 字段名集合必须完全相同；
- 响应头集合必须完全相同（除日期/长度）；
- 错误场景（429、500、SAFETY）的 code/type/status 必须完全相同；
- 差异只允许出现在 `b64_json` 内容与耗时。

这条用例是 I5 的唯一硬判据。字段集合用反射比较，不做字符串包含（避免漏掉新增字段）。

补充扫描：对三个响应体递归搜 `gemini|candidates|inlineData|google|volces|ark|openai`，
命中即失败。

### 7.4 本地夹具

`deploy/local/e2e/gemini_image_matrix.py` 是判据脚本（36 项，T1/T2/T3）。它依赖：

| 渠道 | models | `image_protocol.upstream` | `model_mapping` | 主端点 |
|---|---|---|---|---|
| `gemini-image-local` | `xx` | `gemini` | `xx → mock-gemini` | 默认 |
| `ark-image-local` | `yy` | `ark` | `yy → mock-ark` | `/mock-b64/v1/images/generations` |
| `openai-image-local` | `zz` | `openai` | `zz → mock-openai` | `/mock-b64/v1/images/generations` |

mock（`deploy/local/mock/mock_upstream.py`）提供 `:generateContent`、
`:streamGenerateContent`、`/mock/last-gemini-image`（断言去程）、`/mock/out/*`（图片字节）
与 `/mock-b64/v1/images/generations`（回 b64）。后两条是为了绕开本地限制：mock 在内网，
Gemini 入口把上游 URL 转 inlineData 要下载公网地址，SSRF 防护会拒绝私有地址。

**本地部署必须先构建前端**：`web/dist` 被 `go:embed` 进二进制，而
`deploy/local/Dockerfile.app` 不构建它。只改后端时 dist 可以不动；改过 `web/` 之后
先 `cd web && bun run build`，否则镜像里跑旧界面。`up.sh` 会在检测到
`web/src` 比 `web/dist` 新时打印警告。

### 7.5 夹具（原始）

- 三个 mock 上游，共用一套「固定 PNG + 固定错误」响应，便于逐字节比较。
- mock Gemini 支持 `:generateContent` 与 `:streamGenerateContent`，验证后者不被误接。
- e2e 脚本放 `deploy/local/e2e/`，命名 `image_protocol_matrix.py`；单测在
  `relay/channel/advancedcustom/` 与 `relaykit/dto/`。

## 8. 代码改动清单

| 文件 | 改动 |
|---|---|
| `relaykit/dto/apiduo_image_protocol.go` | 加 `ImageUpstreamGemini`；`Validate` 三分支；不加模板校验、不改 `WithChannelPrimary` |
| `relay/channel/advancedcustom/apiduo_image_script.go` | `planGemini`：路径用 `base_url + GetGeminiVersionSetting + model`；`size` 映射；`n>1` 不在这里拒 |
| `relay/channel/advancedcustom/apiduo_image_canonical.go` | `canonicalFromGemini`（contents/parts）、`canonicalFromResponses`（input/instructions）、`readResponsesContent` |
| `relay/channel/advancedcustom/apiduo_image_adaptor.go` | `prepareImageProtocol` 加 `RelayModeResponses` 与 `RelayModeGemini` 分支；`resolveRoute` 复用 `ServesPath` 合成预检路由 |
| `relay/channel/advancedcustom/apiduo_image_response.go` | `decodeGeminiImageResponse`；`wrapGeminiResponse`；按入口重建、删除透传兜底；64 MiB 上限 |
| `relay/image_handler.go` | 计费前调 `ImageProtocol.RejectsImageCount` |
| `relaykit/types/error.go` | 新增 `ToGeminiError` |
| `service/http.go` | 图片协议路径无条件走 `ShouldCopyUpstreamHeader` 白名单 |
| `web/.../image-protocol-settings.ts` | 枚举加 `gemini`；往返测试 |
| `web/.../primary-endpoint-editor.tsx` | 第三张卡（只读显示 Gemini 路径来源）；不出现 `{model}` 输入；加出口净化提示 |
| `docs/apiduo/image-endpoint-contract.md` | §3 不变；§4 增加「主端点不参与 Gemini 路径」与「URL 不透传」两条 |

## 9. 验收判据

功能：

- 五个入口 × 两个上游（gemini、ark）都能出图，回包形状符合各自协议；
- `n=2` 在五个入口都返回 400，且未产生上游请求、未产生预消费；
- `SAFETY` / `IMAGE_RECITATION` 在五个入口都返回 400 `invalid_request`，不重试。

不暴露上游：

- T3 的字段集合比较通过；
- 三个响应体扫描 0 命中；
- 响应头扫描 0 命中；
- 图生图在无存储渠道返回 b64，不返回上游链接。

回归：

- 未开图片协议的渠道，五个入口的行为与改动前逐字节相同；
- `ark` / `openai` 原有图片用例全绿；
- Gemini 原生入口在「未声明路由」时仍是 503（不是 400，也不是协议接管）。

## 10. 设计与反驳

### D1 为什么主端点不写模板？

模板把「模型在路径里」显式化，但代价是整个配置面变成上游的镜像：换一家上游就要改主端点，
I1 失效。用 `base_url + 版本 + 模型` 构造，主端点保持稳定，代价是「改主端点对 Gemini 无效」——
这条通过页面只读说明消化。

### D2 为什么 Gemini 路径不从前缀推导？

`/v1/images/generations` 的前缀是 `/v1`，Gemini 需要 `/v1beta`。两者不是同一维度：
一个是 API 版本，一个是厂商版本。从字符串推只能靠猜，且猜错表现为 404。

### D3 为什么 n > 1 不扇出？

N 次里部分成功怎么计费、怎么退、怎么拼 `data[]`、超时怎么算，同步入口没有承载者。
所以 `n` 只做计费口径，不进上游：上游收到的是 1（或者干脆没有这个字段）。早期版本在
入口拒绝 `n>1`，2026-11-23 起统一为「上游 1 张、按 n 计费」，拒绝分支已删除。

### D4 为什么 `imageConfig` 要折成 `size`？

`aspectRatio` 是比例、`imageSize` 是档位，两者合起来才等价于一个像素目标。上游是 gemini 时
原样转发最好；但 ark / openai 形状的上游只认 `size`，而且档位只能表方形、比例只能控比例 ——
要同时兑现两者，唯一形态是 WxH（§3.3.1）。

表用 Google 的官方像素表，不是面积现算：客户端从 Gemini 迁过来，期望的就是表里的宽高
（16:9 的 1K 是 1376×768）。表外比例才按面积算，不为枚举落后而 400。

不做客户端可见的 `size` 下拉：那个字段在 Gemini 协议里不存在，暴露它等于让客户端依赖
本网关的方言。

### D5 为什么删除透传兜底？

「不暴露上游」与「解析失败原样透传」互斥。透传会把 `candidates[]`、上游错误页、
上游响应头一起给客户端。删除后，解析失败统一 502，真实原因只在管理端日志。

### D6 为什么 URL 不允许透传？

链接的 host 就是上游身份。`*.volces.com`、`storage.googleapis.com` 一眼可辨。
无存储时改写成 b64 是唯一不新增依赖的做法；转存失败也回 b64，不把成功变失败。

### D7 这套方案最可能在哪出问题？

1. Gemini 路径的版本段取错（`v1` vs `v1beta`）→ 全量 404，且页面显示正常。
2. 某个入口忘了按白名单重建、走了透传 → 只有该入口泄漏，T3 只测 images 会漏。
3. 无存储渠道的上游 URL 没改写 → 泄漏发生在「上游给 url」的分支，mock 要覆盖。
4. `n` 的改写顺序放错（先改 canonical 再读计费）→ 计费退化成按 1 张。
5. Gemini 回包缺 `index` / `finishReason` → 只有官方 SDK 报错，自研客户端测不出来。
6. 解码器没按 upstream 分派 → images / chat / responses 入口 × gemini 上游全部 502，
   而 T1 如果只测 `b64_json` 是否非空、不看状态码，会误判为通过。

前四条与第六条都能靠 §7 的矩阵覆盖，第 5 条要额外用官方 SDK 跑一次。

### D8 什么情况下这套方案要改？

- 上游出现「一次请求多张图」的同步接口（`n` 扇出重新变成真需求）；
- 渠道必须同时服务文本与图片同一路径（按约定应拆渠道，不能靠网关猜）；
- 4K 调用占比超过 5%（要引入 `image_size` 倍率）。

### D9 备选：Gemini 单独做（不走图片协议）

| 维度 | 单独渠道 | 协议层 5 × 3 |
|---|---|---|
| 入口覆盖 | `images/generations`、`images/edits` | 5 |
| 上游组合 | Gemini 渠道只服务 Gemini | 任意入口 × 任意上游 |
| 代码量 | 小：`ConvertImageRequest` 加香蕉分支 + `GeminiImageHandler` 解码 `candidates` | 大：5 折叠 + 3 编码 + 5 回包 + 出口净化 |
| 存量回归 | 零（只加分支） | 中（删透传兜底影响所有图片渠道） |
| `n>1` 处理 | 单独渠道各写一套 | 统一为「上游 1 张 + 按 n 计费」 |
| 一致性 | 两套 size / 错误 / 净化，必然漂移 | 一套 |
| 扩展成本 | 每接一家写一次 | 加一个脚本 |

单独做的边界：`chat/completions` 已有但回 markdown（data URI 很大），`responses` 的转换不读
`inlineData`（图会丢）。这两条入口拿不到图片语义，要它们出图只能走协议层。

**推荐分阶段：**

1. 先单独做，覆盖 `images/generations` + `images/edits`。这是最快让香蕉可用的路径，且零存量回归。
2. 协议层作为终态，覆盖 5 入口。
3. 两条路径共用同一对纯函数（`canonical ↔ gemini` 的编码与解码），否则一年后两套 `size`
   映射与净化规则必然不一致。
4. 同一个模型只挂一种渠道。abilities 选到哪条渠道决定行为，两条都挂会让「同一次请求为什么
   行为不同」变成不可解释的问题。

## 11. 升级兼容性

这次改动对存量渠道有三类影响，按严重度排列。前两类必须在升级前审计。

### 11.1 破坏性：新入口会被接管

`prepareImageProtocol` 对 `RelayModeResponses` 与 `RelayModeGemini` 的接管条件是
「渠道声明了该路由 + `image_protocol.enabled`」，不看请求内容。存量渠道里如果有一条
**同时服务文本与图片**（旧版本完全合法：图片协议只接管两个 `/v1/images/*`），升级后
它的 responses / Gemini 文本请求会被折叠成出图，上游收到 `:generateContent` 或主端点。

| 存量配置 | 升级前行为 | 升级后行为 |
|---|---|---|
| `image_protocol.enabled=true` 的渠道收到 `/v1/responses` | 按 converter 转发文本 | 折叠成出图 |
| `image_protocol.enabled=true` 的渠道收到 `:generateContent` | 原生透传 | 折叠成出图 |

判据**不要求**路由表里写过这两条路径——只要渠道开着图片协议就接管。所以审计的关键是
「这条渠道是不是纯图片渠道」，而不是「它有没有声明这两条路由」。

审计（MySQL）：

```sql
SELECT id, name
FROM channels
WHERE type = 58
  AND settings LIKE '%"image_protocol"%'
  AND settings LIKE '%"enabled":true%'
  AND (settings LIKE '%/v1/responses%' OR settings LIKE '%:generateContent%');
```

结果必须为空。非空时把图片与文本拆成两条渠道：文本走不开图片协议的渠道，图片走这条。

### 11.2 行为变更：解析失败不再透传

删掉 `finishImageNativeResponse` 与 chat 分支的透传兜底后，**所有 advancedcustom 图片渠道**
（含存量 `ark` / `openai`）的非标准回包从「原样透传、客户端能拿到图」变成 502。

受影响的形状：`data` 不是数组、回包不是 JSON 对象、字段名不是 `url` / `b64_json`。
升级前逐条验证：对每条 `image_protocol.enabled=true` 的渠道发一次最小请求，确认 200。
失败时先临时关闭该渠道的 `image_protocol.enabled`，补解码器后再开。

### 11.3 响应字段与响应头收紧

| 位置 | 之前 | 现在 |
|---|---|---|
| `data[].size` / `quality` / 其它 item 字段 | 原样带回 | 只留 `revised_prompt` |
| envelope 的 `model` / `system_fingerprint` / `service_tier` | 原样带回 | 只留 `usage` |
| 上游响应头 | `IOCopyBytesGracefully` 全量复制 | 不复制，只写网关自己的头 |

客户端若依赖这些字段，需要在升级说明里点名。`usage` 是计费依据，没有收紧。

### 11.4 容量：单响应上限从 16 MiB 提到 64 MiB

64 MiB 是为 4K 图准备的（base64 通常 20–30 MB）。代价是并发下的内存上限提高 4 倍：
100 个并发 4K 响应约 6 GB 峰值。存量渠道若只服务 1K/2K，可以按渠道收窄上限；
当前是全局常量，没有按渠道配置。

### 11.5 已知功能缺口（非兼容性，但需要在发布说明里写）

- **`systemInstruction` 被丢弃。** `canonicalFromGemini` 只取最后一条 user 轮，Gemini 的
  `systemInstruction` 不参与折叠。用它写「你是插画师」的客户端会看到效果变化。
- **`candidateCount` 被忽略。** `planGemini` 不写它。客户端传 4 只会拿到 1 张，且不报错。
  Gemini 的候选不是副本，不应映射成 `n`。
  （反方向也成立：`nano-banana-2.1` 会自己回两张——草稿 + 成品，加 `candidateCount=1`
  也拦不住。四个 OpenAI 形状的入口按 §3.5 只交付最后一张。）

### 11.6 回滚

| 粒度 | 操作 | 注意 |
|---|---|---|
| 单渠道 | `PUT /api/channel/` 把 `image_protocol.enabled` 置 false | 必须走接口；`PUT` 是整体更新，漏字段写零值 |
| 单入口 | 从 `advanced_routes` 删掉 `/v1/responses` 或 `:generateContent` | 删后该入口回落到普通转发 |
| 全局 | 回滚镜像 | core-1 与 core-3 是主备，没有流量分配，只能整体切 |
