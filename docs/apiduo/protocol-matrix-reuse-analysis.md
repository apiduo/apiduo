# 协议矩阵：复用分析

> 2026-11-07。基于截图 1（现有路由转发编辑器）与截图 2（渠道编辑页）。
> 结论先说：**核心能力上游全有，但「矩阵」和「路由列表」不是同一种交互**。
> 需要先定清楚它到底是「看」还是「填表生成」，再谈复用。

## 1. 你截图里那套 UI，能力已经到哪一步

逐项对照 `advanced-custom-editor-dialog.tsx`（1850 行）：

| 能力 | 上游实现 | 我写的 | 判定 |
|---|---|---|---|
| 多模型分流 | `AdvancedCustomRoute.models` + 分组卡片的「+ 添加分流」 | `MatrixItem.models` | 数据模型**同构**，我这边只是另一种输入界面 |
| 按入口路径分组 | `buildRouteGroups()`（236 行） | 矩阵按端点拍平 | 可复用分组结果，不必重写 |
| 兜底必须排最后 | `isCatchAllRoute()`（173）+ `fixCatchAllOrder()`（546）+ `onMoveCatchAllToEnd` | `encodeMatrix` 的兜底下沉 | **语义重复**，但形态不同（见 §2） |
| 单条路由的字段编辑 | `RouteEditor()`（1302）：客户端 model 规则 / 上游路径 / 转换器 / 认证 / 透传 / 上下移 | 无 | 矩阵不必重写字段编辑，但要换一种排列 |
| 转换器合法性 | `getAdvancedCustomIncomingPathOptions()`（advanced-custom.ts:502） | `converterOptionsForPath()` | 我在它上面包了一层，**没有另建表** |
| 整套模板 | `ADVANCED_CUSTOM_TEMPLATE_OPTIONS`（advanced-custom.ts:284）：all_protocols / openai_only / claude_only / gemini_only | 无 | 按**协议族**分，没有按模型分的 |
| 保存校验 | `validateAdvancedCustomConfig()`（607） | `validateMatrix()` | 我这边只查矩阵结构，语义仍交给它 |

**所以「协议矩阵要支持多个模型」这件事，上游已经做完了。** 你要的 seedream-3 + seedream-4，在现有 UI 里就是「OpenAI Image Generations 卡片 → 添加分流 → 客户端 model 填两个」。

## 2. 我写的东西里，哪些是真的重复

诚实盘点：

**重复的（可以删）**
- `decodeMatrix` 里「读 models、按端点聚合」——上游 `buildRouteGroups` 干的是同一件事，只是输出结构不同。
- `validateMatrix` 的模型重复检查——`validateAdvancedCustomConfig` 已经查了，而且更全（含正则）。
- `encodeMatrix` 里「兜底下沉」——`fixCatchAllOrder` 就是它。

**不重复的（要留）**
- `encodeMatrix` 本身：`fixCatchAllOrder` 是 **React 事件处理器**，读写的是组件 state（`routeRows` + `routeKey`），不是纯函数，没法在别处调用。矩阵要「一次生成整套 routes」，还是得有自己的编译函数。
- `decodeMatrix` 的**拒绝逻辑**：判断「这套存量配置能不能用矩阵视图表达」（自定义 auth、兜底重复、模型重叠）。上游没有这个判据，因为它不需要——它的 UI 什么都能编。
- `converterOptionsForPath`：一行封装，让矩阵的转换器下拉跟上游永远一致。

比例大概是：**逻辑 60% 重复，但那 40% 才是矩阵能不能成立的关键**。

## 3. 关键分歧：矩阵是「看」还是「填表生成」

这才是决定做不做的地方。两种形态的价值完全不同：

### A. 只读视图（把 N 张卡片拍平成一张表）

- 成本：1-2 天（纯 UI）
- 解决：3 个端点时卡片够用，11 个端点要滚很久
- **不解决**：配错。你还是得在卡片里一条条填
- 判断：如果渠道平均只配 3-5 个端点，**价值接近零**（你的截图就是 3 个）

### B. 声明式生成（填「模型 × 端点」表 → 编译成 routes）

- 成本：3-4 天（我已完成 core，剩 UI）
- 解决：用户不需要理解 route / converter / auth / 兜底顺序，填表就行
- 解决：配错。表里没勾的组合，编译后就是没有路由 = 拒绝，不会出现「以为拒绝其实兜底放行」
- 判断：如果配 seedream 这类渠道要手工填 3-5 条路由，**每次都要想「这条要不要开兜底」**，B 就有价值

你 docx 里写的是 B——「协议矩阵是语义层唯一数据源，保存时编译为 AdvancedCustomRoute 列表」。我那 51 个测试测的也是 B 的往返性。

**但 B 有个前提**：routes 仍然是唯一真相，矩阵只是它的另一种输入方式。一旦矩阵自己存一份（docx 原方案的 `endpoint_matrix` 字段），就变成双源，那是错的。

## 4. 如果做 B，复用边界在哪

| 层 | 复用 | 自建 |
|---|---|---|
| 数据 | `advanced_routes` 数组，同 path 多条 = 分流 | 不新增字段 |
| 分组 | 可以复用 `buildRouteGroups`（需导出） | — |
| 字段合法性 | `getAdvancedCustomIncomingPathOptions` / `getAdvancedCustomConverterDefaults` | — |
| 保存校验 | `validateAdvancedCustomConfig` | — |
| **行编辑 UI** | **不复用 `RouteEditor`** | 矩阵行只有三列：端点 / 模型 / 模式。硬套 8 列的 RouteEditor 反而更难用 |
| **编译** | 不复用（`fixCatchAllOrder` 不是纯函数） | `encodeMatrix` |
| **预设** | 复用 `AdvancedCustomConfig` 结构 | 按模型/供应商的预设（seedream、纯对话、仅图像） |

**要导出两个私有函数**（改 2 行加 `export`）：`buildRouteGroups`、`isCatchAllRoute`。这是唯一需要动上游既有行的元凶，而且只是加一个关键字。

## 5. 我的建议

**先做 B 的「表驱动生成」，但砍掉一半工作量：**

1. **保留** `decodeMatrix` / `encodeMatrix`（往返性已有 51 个测试固定）
2. **删掉** `validateMatrix` 里与上游重复的模型校验，只留「兜底重复」「模型重叠」这两条上游没有的
3. **不做**独立的只读总览表（A 形态在这个端点数下没价值）
4. **加**预设：`presets.ts`，火山 seedream / 纯对话 / 仅图像，每个预设就是一组 `MatrixItem`
5. **加**覆盖度提示：在矩阵表里标出「该模型在该端点会被拒绝」，这是 B 相对 A 唯一真正的增量信息

第 5 条是重点。它不是「看配置」，而是**「看配置的后果」**：

> seedream-3 调 `/v1/images/edits` → 没有路由 → 404

这句话现有 UI 说不出来。它要遍历 `models × endpoints` 的笛卡尔积，逐个跑 `MatchPathForModel` 的等价逻辑。这才是矩阵存在的理由。

## 6. 反过来：如果只是「配 seedream 太麻烦」

那答案不是矩阵，是**预设**。点一下生成 3 条路由，2 小时的工作量。

矩阵是给「需要反复调整、端点组合复杂」的场景用的。如果你的渠道就固定的那几种模式，预设覆盖 90% 的需求，矩阵的 UI 成本收不回来。

---

## 附：一个可以立刻验证的问题

你的渠道里，平均配几个端点？

- **≤5 个**：只做预设 + 覆盖度提示，矩阵表格先不做
- **≥8 个** 或 **模型 ≥5 个**：矩阵表格值得做
