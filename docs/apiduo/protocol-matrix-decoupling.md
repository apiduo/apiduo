# 协议矩阵的解耦设计

> 2026-11-07。核心代码已落地：`web/src/features/protocol-matrix/`，51 个单测全过。
> 配套：`module-isolation-plan.md`（三档判定）、`merge-cost-forecast.md`（成本实测）。

## 0. 一句话

**矩阵是 `advanced_routes` 的视图，不是新数据源。** 保存时只写 `advanced_custom`，后端、数据库、运行时路由匹配都不需要知道矩阵存在。

文档原方案的做法相反：新增 `endpoint_matrix` 字段、后端编译后覆盖 `AdvancedCustom.Routes`。那是双源真相 + 8 个高危挂接点。视图化之后，**后端改动是 0**。

## 1. 可逆性是前提，而且已经证明

矩阵的三态对应 `AdvancedCustomRoute` 的三种形态，双向无损：

| 矩阵 | Route | 反向还原 |
|---|---|---|
| `passthrough(path)` | `{incoming_path: p, upstream_path: p, converter: 'none'}` | `converter==='none'` → passthrough |
| `passthrough(path → up)` | `{incoming_path: p, upstream_path: up, converter: 'none'}` | upstream ≠ incoming → 改路径透传 |
| `transform(converter)` | `{incoming_path: p, upstream_path: 默认, converter: c, auth: 默认}` | `converter!=='none'` → transform(c) |
| `reject` | **不生成** | 不存在 → reject |

最后一行是关键：后端 `Adaptor.resolve()` 对未声明的路径本来就返回错误（默认拒绝），所以「不写」和「显式拒绝」在运行时完全等价。`reject` 是纯 UI 概念，一行 Go 都不用加。

**已用测试固定**（`web/src/features/protocol-matrix/lib/__tests__/matrix.test.ts`）：

```
decodeMatrix / encodeMatrix / round trip / adversarial round trip
validateMatrix / converterOptionsForPath
Tests  51 passed
```

adversarial 那组专盯往返中的静默改写：手改过的 `upstream_path`、`pass_through_body_enabled`、
显式 `{type:'none'}`、空 converter 串、`{model}` 模板上游，全部要求 decode→encode 后一字不差。

## 1.5 多模型：一行 = 端点 + 一组模型

同一个渠道挂多个模型是常态，不是例外。协议矩阵的行不是「一个端点」，而是**「一个端点 + 一组模型」**：

```ts
type MatrixItem =
  | { path: string; models: string[]; mode: 'passthrough'; upstreamPath: string }
  | { path: string; models: string[]; mode: 'transform'; converter: AdvancedCustomConverter }
  | { path: string; models: string[]; mode: 'reject' }
```

`models` 为空表示**该端点的兜底行**（任何模型都落这里）。

火山方舟 seedream 的例子：

| 端点 | 模型 | 模式 |
|---|---|---|
| `/v1/images/generations` | seedream-3, seedream-4 | 透传 |
| `/v1/images/edits` | seedream-4 | 转换 → generations |
| `/v1/images/edits` | （空 = 兜底） | 拒绝 |

第三行不用写也行——seedream-3 请求 edits 时匹配不到任何路由，后端直接报错，效果一样。写出来只是让人看懂意图。

对应的 `advanced_routes`：

```json
[
  {"incoming_path":"/v1/images/generations","upstream_path":"/v1/images/generations",
   "converter":"none","models":["seedream-3","seedream-4"]},
  {"incoming_path":"/v1/images/edits","upstream_path":"/v1/images/generations",
   "converter":"openai_images_edits_to_generations","models":["seedream-4"]}
]
```

### 上游对模型规则的三条硬约束

`relaykit/dto/channel_settings.go` 的 `validateAdvancedCustomRouteModels` 规定：

1. **同一端点下最多一个兜底行，且必须排在最后**
2. 同一个模型不能在两条路由里重复出现
3. 模型支持正则前缀：`re:^seedream-.*$`

第 1 条决定了矩阵的编码行为：**`encodeMatrix` 会把具体模型行统一提到兜底行之前**，不是照抄 UI 顺序。所以：

- 拖拽排序时，兜底行要固定在每个端点的末尾（或者在提交前自动下沉），不要让用户以为顺序有意义
- 往返测试要按 encode 产物的顺序写断言，否则会因为重排而红

第 2 条由 `decodeMatrix` 和 `validateMatrix` 双向拦截：存量配置里模型重叠会解不出来（返回失败、退回列表模式），编辑时新增重叠会立即提示。

第 3 条不用做特殊处理——`re:` 前缀的字符串原样透传，后端自己编译正则。矩阵只需要保证按「字符串相等」判重，正则之间是否语义重叠交给后端。

## 2. 可行转换矩阵不用自己写

文档 §4 提出新增 `FeasibilityMatrix` + `ValidateMatrix`。上游前端**已经有了同一份逻辑**：

```ts
// web/src/features/channels/lib/advanced-custom.ts
export function getAdvancedCustomIncomingPathOptions(converter): AdvancedCustomIncomingPathOption[]
function isConverterPathAllowed(incomingPath, converter): boolean
```

它是后端 `validateAdvancedCustomConverterPath` 的前端投影。矩阵的「这一行能选哪些转换器」直接反查它：

```ts
export function converterOptionsForPath(path) {
  return ADVANCED_CUSTOM_CONVERTER_OPTIONS.map(o => o.value).filter(
    converter => converter !== 'none' &&
      getAdvancedCustomIncomingPathOptions(converter).some(o => o.value === path)
  )
}
```

**上游加一个 converter，矩阵自动支持，我们一行不改。** 自己再写一张可行矩阵表，就是给自己埋一个「上游加了转换器但矩阵不知道」的漂移点。

`encodeMatrix` 也是复用 `getAdvancedCustomConverterDefaults(converter, path)`——它同时给出 `upstream_path` 和 `auth`，所以矩阵生成的路由和用户在上游 UI 里手配的路由**逐字段相同**。

## 3. 数据流

```
channels.other_settings.advanced_custom.advanced_routes     ← 唯一真相
                    │
                    ├── decodeMatrix ──► Matrix { items }   ← UI 状态
                    │                        │
                    │                        ├── 编辑表格
                    │                        │
                    └── encodeMatrix ◄───────┘              ← 写回 config
                                    │
                                    ▼
                        原有保存链路（validateAdvancedCustomConfig → PUT /api/channel/）
```

矩阵的 state 不独立存在，`useMemo(() => decode(config), [config])` 派生。任何时刻 `config` 是唯一真相，因此：

- 用户切到矩阵改两行，切回传统列表，改动还在
- 原有保存流程、错误定位（`routeIndex`）、i18n 全部不用改

## 4. 文件结构

已落地：

```
web/src/features/protocol-matrix/
  types.ts                     Matrix / MatrixItem / MatrixDecodeResult
  index.ts                     对外导出
  lib/matrix.ts                decodeMatrix / encodeMatrix / validateMatrix / converterOptionsForPath
  lib/__tests__/matrix.test.ts 32 个用例
  lib/__tests__/coverage.test.ts 19 个用例
```

待建（纯 UI，不再碰任何逻辑）：

```
  components/matrix-editor.tsx    表格：端点 / 模式 / 上游路径或转换器
  components/matrix-presets.tsx   供应商预设（火山 seedream、纯对话、全兼容）
  lib/presets.ts                  预设 = 若干 MatrixItem，纯数据
```

## 5. 接入点：6 处，全是插入或单行

全部在 `web/src/features/channels/components/dialogs/advanced-custom-editor-dialog.tsx`（1850 行，上游 5 次/90 天）：

| # | 位置 | 改动 | 档位 |
|---|---|---|---|
| 1 | 顶部 import | 1 个标记块 | A |
| 2 | `type AdvancedCustomEditorTab`（第 130 行） | 加 `'matrix'` | A |
| 3 | `advancedCustomTabs` Set（第 144 行） | 加 `'matrix'` | A |
| 4 | `<TabsList>` 的 `sm:grid-cols-4` | 改成 `sm:grid-cols-5` | 改 1 行（纯样式） |
| 5 | `<TabsTrigger value='matrix'>` | 1 个标记块 | A |
| 6 | `<TabsContent value='matrix'>` | 1 个标记块 | A |

只有第 4 处动了上游既有行，而且是 className 字符串。冲突时一眼能解。

如果连这一处都想避免：不改列数，让第 5 个 tab 在 `grid-cols-4` 下换行——能跑，但难看。不建议。

## 6. 降级：矩阵不吃存量配置

`decodeMatrix` 遇到以下任一情况**返回失败**，UI 禁用矩阵模式并说明原因，用户继续用传统列表：

| 情况 | 为什么 |
|---|---|
| `upstream_path` 含 `{model}` | 模板路径不是一个具体端点 |
| `auth` 不是该转换器的默认值 | 用户手工定制过鉴权，矩阵回写会覆盖 |
| 同一端点下模型重复 | 上游校验会拒；矩阵按模型判重，解不出来 |
| 同一端点有两个兜底行 | 上游校验会拒 |
| 端点为空 | 数据损坏 |

这条比功能本身重要：**矩阵上线后，任何存量渠道点一下都不会被改坏**。

注意它没有拦「多模型」——模型维度是支持的（见 §1.5）。被拦的只是那些**矩阵无法无损表达**的形态，比如自定义鉴权。如果将来真要支持，得先扩 `MatrixItem` 的表达力，而不是让矩阵去猜。

## 7. 什么情况下必须动后端

只有一种：**新增协议转换能力本身**。矩阵只是编辑器，它不能凭空造出上游没有的转换器。

典型例子就是文档里的 `images/edits → images/generations`：需要把 multipart 请求改写成 JSON，这是真能力，前端做不了。

成本压到最低的写法：

```go
// relaykit/relayconvert/image_converter.go —— 上游不认识这个文件
package relayconvert

func init() {
    // 同包内可直接调用未导出函数，不用改 request_registry.go
    registerBuiltinRequestConverter(RequestConverterSpec{
        ID:      "openai_images_edits_to_generations",
        From:    types.RelayFormatOpenAIImage,
        To:      types.RelayFormatOpenAIImage,
        Quality: RequestConverterQualityDiscouraged,
        Convert: convertImagesEditsToGenerations,
    })
}
```

加上 `relaykit/dto/channel_settings.go` 的 `IsAdvancedCustomConverterAllowed` switch 里一行常量：

```go
case advancedCustomConverterNone,
+   ConverterOpenAIImagesEditsToGenerations,
    ...
```

**1 个新文件 + 1 行白名单**。这行是常量列表，不是逻辑分支，上游加自己的 case 时冲突是纯文本。

即使这 1 行也不想碰：先只上 `passthrough + reject` 的矩阵（纯前端、零后端），验证有人用之后再决定要不要加转换器。

## 8. 分阶段

| 阶段 | 内容 | 工作量 | 状态 |
|---|---|---|---|
| 1 | decode / encode / 校验 / 单测 | ~2 小时 | ✅ 已完成 |
| 2 | 矩阵表格 + 预设 | 1.5 天 | 待做 |
| 3 | 接入 dialog（6 处） | 0.5 天 | 待做 |
| 4 | 图像转换器（可选） | 1.5 天 | 待需 |

阶段 1 是全部风险所在——它决定了「矩阵能不能无损表示现有配置」。这一步已经用测试证明，剩下的都是画界面。

## 附：为什么不用后端 compiler

再明确一次理由，避免以后又绕回来：

1. **双源真相**。`endpoint_matrix` 和 `advanced_routes` 并存时，谁是准的？任何绕过前端直接改库的操作都会让两者不一致。
2. **注入时机脆弱**。文档方案在 `Adaptor.Init()` 里把编译结果写回 `info.ChannelOtherSettings.AdvancedCustom`——这是请求生命周期里的一次隐式写覆盖。上游调整 `Init` 或 `RelayInfo` 的构建顺序，它就会静默失效，而且测试很难覆盖。
3. **每次请求都编译**。矩阵 → routes 的转换本该在保存时做一次。
4. **错误定位丢失**。前端编译能用 `routeIndex` 直接高亮出问题的行；后端编译只能返回一句错误文案。

一句话：**能放在保存前的，不要放到请求里。**
