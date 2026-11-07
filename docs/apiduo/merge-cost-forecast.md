# 上游合并成本评估与规避

> 2026-11-07。基于实测数据，不是估算。
> 配套文档：`upstream-sync.md`（规范与操作步骤，本文不重复）。

## 0. 结论

**现在的形态是良性的：合并成本约 0.5 人天/月，且不会随时间恶化。**

理由是实测出来的，不是设计愿望：32308 行新增、72 行删除，**删除比 0.2%**。git 的冲突只发生在「同一行被双方修改」或「同一位置双方插入」，99.8% 纯插入意味着冲突面几乎为零——三次与上游合并（含刚刚的实时校验）全部 0 冲突。

**真正会把成本推上一个数量级的，是「协议矩阵」按文档现有方案实现。**

文档要在 `relay/channel/advancedcustom` 区的 7 个函数内部插逻辑，而这个区域是上游 2026 年最活跃的开发区：90 天 13 个提交，平均 5-7 天动一次。开发 8.5 人天是一次性的，维护是长期的，而且大概率会被上游原生功能覆盖。

**规避方案：矩阵只做前端视图，零后端改动；真能力（图像转换器）压成「1 个新文件 + 2 行白名单」。改动面从 9 处降到 3 处，高危点从 7 个降到 0 个。**

---

## 1. 实测基线

### 1.1 上游节奏

| 指标 | 数值 |
|---|---|
| 2026 年至今总提交 | 1763 |
| 月均 | ~180 |
| 近 90 天 | 472 |
| 近 30 天 | 189 |
| 峰值周 | 74（2026-W38） |
| 发版 | rc.43 → rc.48 + relaykit/v0.3.0 → v0.3.7，90 天内 relaykit 独立打了 8 个 tag |

结论：日均 6 个提交，**按周同步是合理节奏，按月是上限**。

### 1.2 我们的改造面

| 类别 | 数量 | 说明 |
|---|---|---|
| 新增文件（上游永远不会碰） | 139 | 28679 行 |
| 修改的上游既有文件 | 78 | churn 3701 行 |
| ├─ 其中 i18n locales | 7 | ~2013 行（占 54%） |
| └─ 其余真实代码 | 71 | ~1688 行 |
| 删除的上游文件 | 0 | — |

**改动形状（决定性指标）**

| 指标 | 数值 |
|---|---|
| 总新增 | 32308 行 |
| 总删除 | 72 行 |
| 删除/新增 | **0.2%** |
| 改动块总数（78 个上游文件） | 261 |
| 与上游近 90 天改动区域重叠的块 | 109（41.8%） |

41.8% 是**上界**：它只说明"区域被反复触碰"，不代表冲突。i18n 的 7 个文件 90 天内被上游改了 1175 次块，重叠率 100%，三次合并却全部自动通过——因为双方改的是不同行的 key。

### 1.3 三次合并实测

| 时点 | 目标 | 冲突 |
|---|---|---|
| 2026-11-05 | upstream @ 72f3dc7d | 0 |
| 2026-11-07 | upstream @ 8c8940c9（4 个新提交） | 0 |
| **回放：把当前全部改造 3-way 应用到 2026-10-08 的上游快照** | 45094bdf | **6 文件 / 8 块** |

第三条是关键的预测依据：**跨越 30 天的上游演进，只产生 8 个冲突块**。逐块看性质：

| 文件 | 块 | 性质 | 解决成本 |
|---|---|---|---|
| `web/bun.lock` | 3 | 锁文件 | `bun install` 重生成，1 分钟 |
| `web/src/hooks/use-sidebar-view.ts` | 1 | import 列表双方都加 | 机械合并，2 分钟 |
| `web/src/components/layout/components/authenticated-layout.tsx` | 1 | 同上 | 2 分钟 |
| `router/plugin-router.go` | 1 | 双方在同一位置插入变量 | 人工确认，5 分钟 |
| `router/plugin_router_test.go` | 1 | 同上 | 5 分钟 |
| **`router/relay-router.go`** | **1** | **功能撞车** | **需决策，见 §4.1** |

7 个机械块 + 1 个真语义冲突。**30 天 8 块 ≈ 每月 0.5 人天**（含验证）。

---

## 2. 成本模型

合并成本不是「冲突数 × 时间」，它由四项构成，量级差很多：

```
C_total = C_fixed × N_sync          # 固定开销
        + Σ(C_mech)                  # 机械冲突
        + Σ(C_sem)                   # 语义冲突 ← 大头
        + C_drift                    # 静默漂移 ← 最贵且不可测
```

| 项 | 单位成本 | 说明 |
|---|---|---|
| `C_fixed` | 20-30 分钟 | build + go test + typecheck + bun build + 起服务验证。同步多勤都得付 |
| `C_mech` | 2-5 分钟/块 | import、锁文件、测试文件。7 块 = 25 分钟 |
| `C_sem` | 30-60 分钟/块 | 双方改了同一段逻辑，要读上游意图 + 判断我方是否仍成立 + 跑回归 |
| `C_drift` | **1-5 人天/次** | 合并成功、编译通过、行为变了。上游改了 `resolve` 的语义，我们插在里面的判断就静默失效 |

`C_drift` 是唯一无法用 git 度量的，只有**开启态 e2e** 能在 CI 里拦住它。这也是为什么 `upstream-sync.md` 的规范第 3 条（默认关闭 + 开启态最小 e2e）不能省。

### 2.1 关键洞察：同步频率不影响总成本

冲突是**区域级**的，不是时间级的。一个区域只要双方都改，早晚撞一次，拆成 12 次同步撞 12 次小的，和 1 次同步撞 1 次大的，总块数相近。

频率真正影响的是 `C_sem`：**跨 7 天解冲突，你还记得自己上周为什么这么写；跨 90 天就只剩考古。**

所以「1-2 周同步一次」的理由是认知负担，不是冲突数量。

### 2.2 成本函数的形式

决定成本的不是改动行数，是**改了上游多少个函数体内部**：

| 改动类型 | 冲突概率 | 说明 |
|---|---|---|
| 新增文件 | ≈ 0 | 上游不知道它存在 |
| 文件顶层插入（新 import、数组末尾、路由注册） | 低 | 只要位置不撞 |
| 结构体加字段 | 中 | 同一结构体多人加，位置竞争（`ChannelOtherSettings` 上游 15 次/90 天） |
| **函数体内插入** | **高** | 上游重写函数体就会全块冲突，且语义可能已变 |
| **修改既有 switch / 分支** | **高** | 加 case 是竞争最激烈的位置 |

「高危块」指后两类。当前约 10-15 个，散在 relay 中间件链、router 注册、渠道表单三处。

**退化阈值：高危块超过 20-25 个时，合并会从「分钟级」跳到「小时级」**，因为每次都要读上游 diff 来判断我方逻辑是否还成立。

---

## 3. 未来成本预测

### 3.1 维持现状

| 粒度 | 固定开销 | 冲突 | 合计 |
|---|---|---|---|
| 每次（按周） | 25 分钟 | 2-3 块 ≈ 10 分钟 | **~40 分钟** |
| 每月 | 2 小时 | 8 块 ≈ 1 小时 | **~3 小时** |
| 每年 | — | — | **~4 人天** |

且**不会随时间恶化**：新增文件占比 65%，纯插入占比 99.8%，这两个比例只会随新功能增加而更有利（新功能都写在新文件里）。

### 3.2 按文档方案上协议矩阵

新增 7 个高危点：

| 改动点 | 上游 90 天热度 | 类型 |
|---|---|---|
| `relaykit/dto/channel_settings.go` — 加 `EndpointMatrix` 字段 | 15 | 结构体字段（中危） |
| `IsAdvancedCustomConverterAllowed` — 加 case | 15 | **修改既有 switch** |
| `validateAdvancedCustomConverterPath` — 加 case | 15 | **修改既有 switch** |
| `adaptor.go` — `Init` 插编译注入 | 5 | **修改既有函数** |
| `adaptor.go` — `resolve` 插拒绝逻辑 | 5 | **修改既有函数** |
| `adaptor.go` — `ConvertImageRequest` 加 case | 5 | **修改既有 switch** |
| `controller/channel-test.go` — `testChannel` 加分支 | 12 | **修改既有函数** |
| `request_registry.go` — 注册转换器 | 8 | 修改既有表 |
| `channel-mutate-drawer.tsx` — 挂载 UI | **25** | **修改既有组件** |

其中 8 个落在 `advanced_custom` 区域。看这个区域的上游演进节奏：

```
2026-08-18  refactor: advanced custom channel route editor (#6865)
2026-09-15  feat: per-route pass-through for advanced custom channels (#7386)
2026-09-18  feat: bind multiple task plugins to New API channels
2026-10-07  feat(relay): web search encoding per channel
2026-10-10  feat(channel): per-channel switch to skip upstream TLS verification
2026-10-16  feat(aws): add Mantle Messages API support
2026-11-03  feat(vertex): add per-channel v1beta1 endpoint switch (#8179)
```

**每 2-4 周一次结构性增强。** 这不是「已经稳定的老代码」，是「正在被反复重写的热区」。

预测：

| 项 | 增量 |
|---|---|
| 开发 | 文档说 8.5 人天，**实际 12-15 人天**（见 §4） |
| 语义冲突 | 每月 +3~5 块 ≈ 0.4 人天/月 |
| 漂移风险 | 每次上游重构 `resolve`/`Init` 都要重新验证矩阵语义 |
| 年化维护 | **+4~6 人天/年** |

第一年总计 17-20 人天，之后每年 5-7 人天。**而且这些成本买到的东西，有相当概率被上游原生实现覆盖。**

---

## 4. 协议矩阵方案：对抗审查

文档写得结构清晰，但有 6 处与真实代码不符，直接决定了工作量。

### 4.1 「可行转换矩阵」上游已经有了

文档 §4 提出新增 `FeasibilityMatrix` + `ValidateMatrix`，放在 `relay/channel/advancedcustom/matrix.go`。

实际上 `relaykit/dto/channel_settings.go` 已有：

```go
func IsAdvancedCustomConverterAllowed(converter string) bool   // 白名单，switch case 列表
func validateAdvancedCustomConverterPath(index int, incomingPath, converter string) error  // 端点 ↔ 转换器合法性
```

后者逐条校验「converter X 只能用在 incoming_path Y 上」，正是文档想做的「可行转换矩阵」。

**后果**：新写一遍 = 与上游逻辑并存，两条校验规则会随时间漂移（上游加转换器我们不知道）。而且新转换器**必须**进 `IsAdvancedCustomConverterAllowed` 的 switch，绕不过去。

**正确做法**：不新增校验层，把矩阵的合法性约束**内联成前端选项生成规则**，由现有 `Validate()` 在保存时兜底。后端零新增校验代码。

### 4.2 `reject` 是多余的模式

文档 §2 设计三种模式，`reject` 行「不生成 Route」，靠「匹配不到即拒绝」实现。

但 `Adaptor.resolve()` 现在的行为就是：

```go
route, ok := config.MatchPathForModel(incomingPath, info.OriginModelName)
if ok { ... }
return fmt.Errorf("advanced custom channel does not support request path %s ...")
```

**未声明 = 已拒绝**。所以 `reject` 行在数据层等价于「不写这一行」，编译后无差异——它是一个纯 UI 概念。

**后果**：为了表达「显式拒绝」，文档要在 `resolve()` 里加一段扫描 `matrix.Items` 的循环，只为了让错误信息更漂亮。而 `resolve` 是上游 5 次/90 天在动的函数。**为一个提示文案付出一个高危点，不划算。**

**正确做法**：`reject` 只存在于前端 UI（删除该行 = 拒绝），不落库、不进运行时。

### 4.3 矩阵可以无损折叠进现有 `routes`

文档 §3 认为必须新增 `endpoint_matrix` 字段来保存矩阵语义，再「保存时编译」成 routes。

但看 `AdvancedCustomRoute` 的能力：

| 矩阵模式 | 等价 Route | 可逆？ |
|---|---|---|
| `passthrough` | `{IncomingPath: path, UpstreamPath: path, Converter: "none"}` | ✅ |
| `transform` | `{IncomingPath: path, UpstreamPath: from, Converter: "xxx"}` | ✅ |
| `reject` | 不生成 | ✅（不存在 = 拒绝） |

**三行模式全部可逆**。也就是说 `endpoint_matrix` 与 `routes` 是同一份数据的两种表示，不需要并存。

并存反而制造了**双源真相**：文档 §7 的 `Init()` 里「如果启用矩阵模式，用编译结果覆盖 `AdvancedCustom.Routes`」，等于在请求生命周期里做一次隐式写覆盖。上游任何一次对 `Init` 或 `ChannelOtherSettings` 读写时序的调整，都会让这个覆盖失效——而且失效是静默的。

**正确做法**：`endpoint_matrix` 不落库。前端把矩阵状态编译成 `routes` 提交，打开编辑器时从 `routes` 反推矩阵视图。数据库零变更，运行时零感知。

### 4.4 前端代码跑不起来

| 文档写的 | 实际 |
|---|---|
| `import { Table, Select, Button, Tag, Tooltip, message } from 'antd'` | 依赖是 `@base-ui/react` + Tailwind 4，**项目里没有 antd** |
| `web/src/pages/Channel/Edit.tsx` | 该路径不存在，实际是 `web/src/features/channels/` |
| `web/src/pages/Channel/EndpointMatrix/Editor.tsx` | 同上 |
| `<Form.Item label="配置模式"><Radio.Group>` | 是 Base UI 的 Form/Radio，API 完全不同 |

文档里的 `EndpointMatrixEditor` 组件**一行都编译不过**。

**正确做法**：挂载点是 `web/src/features/channels/components/dialogs/advanced-custom-editor-dialog.tsx`（1850 行，上游 8 月刚重构过）与 `channel-mutate-drawer.tsx`（我们已插 138 行，上游 25 次/90 天）。这俩是前端最高风险的位置。

### 4.5 后端签名与 API 不符

| 文档写的 | 实际 |
|---|---|
| `testChannel(channel *model.Channel) (*TestResult, error)` | `testChannel(ctx, channel, testUserID, testModel, endpointType, isStream) testResult`（6 参数，值返回） |
| `info.AddDiagnostic(types.ConversionDiagnostic{...})` | `convmeta.Meta` 接口**没有 `AddDiagnostic`**；`ConversionDiagnostic` 只用于响应侧 |
| `relayconvert.RegisterRequestConverter(...)` | 实际是未导出的 `registerBuiltinRequestConverter` |
| `request_registry.go` 里集中注册 | 可以改为新文件自带 `func init()`（同包内可调用未导出函数），**不用改 `request_registry.go`** |

`ConvertImageRequest` / `resolve` / `resolveForConversion` / `Init` 确实存在，这部分是对的。

**后果**：测试框架要重写（文档 §9.2 的 `testEndpointMatrix` 依赖错误的签名）；转换器诊断要另找落点。

### 4.6 已经有功能撞车的先例

`router/relay-router.go`：我们在 `registerFileRelayRoutes(router)` 里实现了 `/files`，为了不让 gin 重复注册，**删掉了上游的 4 行**：

```go
-httpRouter.GET("/files", controller.RelayNotImplemented)
-httpRouter.POST("/files", controller.RelayNotImplemented)
-httpRouter.DELETE("/files/:id", controller.RelayNotImplemented)
-httpRouter.GET("/files/:id/content", controller.RelayNotImplemented)
```

上游 9 月就预留了这四条 `/files` 占位路由，说明它在路线图上。我们抢先用真实现占了位置，代价是**永久维护这 4 行删除**，且上游实现 `/files` 那天必然冲突。

这是 30 天回放里唯一的真语义冲突，也是最该现在处理的（见 §5.3）。

---

## 5. 规避方案

### 5.1 结构性：把「函数体内插入」当成红线

现有规范管的是「新增行是否在标记块内」，但没管「标记块插在什么位置」。位置才是成本函数的主变量。

建议补一条判据，写进 `markercheck`：

> **在函数体内插入的标记块，块内代码 >3 行时，必须有自己的错误路径与日志，不得修改函数的返回值语义。**

无法机器校验的部分，退一步用计数告警：高危块 >20 时 CI 打警告（不是失败），提醒该重构了。

具体手法（按优先级）：

1. **改成「新增 + 条件包裹」**：不要改 `if x { A }` 成 `if x || y { A }`，而是加上下文处理器/中间件，在主流程外拦截。
2. **利用已有扩展点**：`RegisterXxx` / `init()` / 接口方法，优先于直接改调用方。
3. **结构体字段单独拎出来**：`ChannelOtherSettings` 已经是 4 家（上游 + 我们 3 个块）共用的追加点，再加字段前先问「能不能挂到自己新增的顶层结构里」。

### 5.2 协议矩阵：改写方案

**目标：把 9 处改动压到 3 处，高危点从 7 个降到 0 个。**

| 部分 | 文档方案 | 改写后 | 上游改动 |
|---|---|---|---|
| 矩阵数据模型 | 新增 `EndpointMatrixConfig` + 落库 | **不落库**，前端编译成 `routes` 提交 | 0 |
| 矩阵校验 | 新增 `FeasibilityMatrix` + `ValidateMatrix` | 前端选项生成规则 + 现有 `Validate()` 兜底 | 0 |
| `reject` 模式 | `resolve()` 里加扫描 | 纯 UI 语义（删行 = 拒绝） | 0 |
| 矩阵 → Route 编译 | 后端 `compiler.go` 在 `Init` 里注入 | **前端编译**，后端不感知 | 0 |
| 对抗测试 | 改 `controller/channel-test.go` 加分支 | 前端「模拟测试」调现有渠道测试 API，逐端点发请求 | 0 |
| **图像转换器** | `image_converter.go` + 改 3 处 | `image_converter.go` + **1 行白名单** | **1 行** |
| 前端编辑器 | 新建对抗测试面板 + 改 Edit.tsx | 新组件 + 在 `advanced-custom-editor-dialog.tsx` 挂 1 个标记块 | 1 处插入 |

**关键：图像转换器是唯一必须动后端的部分，也是唯一的真能力。**

实现要点（对应 §4.5 的修正）：

```go
// relaykit/relayconvert/image_converter.go —— 上游不认识这个文件
package relayconvert

func init() {
    // 同包内可直接调用未导出函数，不需要改 request_registry.go
    registerBuiltinRequestConverter(RequestConverterSpec{
        ID: "openai_images_edits_to_generations",
        From: types.RelayFormatOpenAIImage, To: types.RelayFormatOpenAIImage,
        Quality: RequestConverterQualityDiscouraged,
        Convert: convertImagesEditsToGenerations,
    })
}
```

然后 `IsAdvancedCustomConverterAllowed` 的 switch 里加一行（`relaykit/dto/channel_settings.go`，唯一必须改的上游行）：

```go
case advancedCustomConverterNone,
+   ConverterOpenAIImagesEditsToGenerations,
    ...
```

**这 1 行是白名单常量，不是逻辑分支**——上游改这个 switch 时只是加自己的 case，冲突是纯文本、一眼可解。这是可接受的最低成本形式。

如果连这 1 行都想避免：把矩阵的 `transform` 能力限制在**已有转换器**（`openai_chat_to_claude_messages` 等 11 个）之间，先上线 UI，图像转换器等上游支持。**先验证用户是否真的需要矩阵，再决定要不要为它改上游。**

### 5.3 现在就该修的三件事

**① `/files` 路由撞车（30 分钟）**

不要删上游的 4 行占位。改成在 gin 之外拦截：

```go
// router/file-relay-router.go 里，注册在 v1 组之前
// 用 http.Handler 层前缀匹配，或 gin 的 NoRoute 前置钩子
```

或者更简单：把我们的 `/files` 实现**挪到上游占位路由的位置**——即让上游那 4 行指向 `controller.FileRelay`，但这是改上游行（4 行替换），比删除好一些：冲突时一眼能看出「上游想留占位，我们指向了真实现」。

**② `bun.lock` 与 i18n 不要进冲突统计（低成本，长期收益）**

`web/bun.lock` 在 30 天回放里贡献了 3/8 的冲突块。同步时一律 `--theirs` 后 `bun install` 重生成，不要手工解。写进 `upstream-sync.md` 的步骤里。

**③ 现在就开始按「新增文件」写协议矩阵**

不改任何 Go 逻辑，先做前端矩阵编辑器 + 编译成 routes。**这是零成本的探针**：如果上线后没人用，「可行转换矩阵」的复杂设计也就不必做了。

### 5.4 监控指标

放进 `brand-release.yml`，不阻断发版，只打表：

| 指标 | 现状 | 阈值 |
|---|---|---|
| 高危块（函数体内 >3 行的标记块） | ~10-15 | 20 时警告 |
| 上游文件删除行数 | 72 | 200 时警告 |
| 上次同步距今提交数 | 4 | 200 时警告 |

---

## 6. 行动清单

| 优先级 | 动作 | 成本 | 收益 |
|---|---|---|---|
| **P0** | 修 `/files` 占位路由的删除 | 0.5h | 消除唯一的真语义冲突 |
| **P0** | `upstream-sync.md` 补 `bun.lock` 处理约定 | 10min | 每轮省 10 分钟且不出错 |
| **P1** | 协议矩阵改成纯前端（零后端） | 3-4 人天 | 省 8-11 人天开发 + 4-6 人天/年 |
| **P1** | `markercheck` 加「高危块」计数 | 2h | 让 §2.2 的退化阈值可见 |
| **P2** | 图像转换器（等矩阵 UI 验证需求后再做） | 1.5 人天 | 真能力，代价 1 行白名单 |
| **P2** | 上游活跃度周报 workflow | 1h | 提前知道哪个区域要动 |

---

## 附：这篇文章的数字怎么来的

```bash
# 上游节奏
git rev-list --count upstream/main --since="2026-08-09"     # 472
git rev-list --count upstream/main --since="2026-10-08"     # 189

# 改动形状
git diff --numstat $(git merge-base upstream/main HEAD) HEAD | awk '{a+=$1;d+=$2} END{print a,d}'

# 实测冲突（0 天）
git merge --no-commit --no-ff upstream/main && git diff --name-only --diff-filter=U

# 实测冲突（30 天回放）
git worktree add --detach /tmp/wt-test <30天前的上游提交>
cd /tmp/wt-test && git apply --3way <全部改造的 patch>
git ls-files -u | awk '{print $4}' | sort -u
```

注意：`git apply --cached --3way` 在 `--no-checkout` 的 worktree 里会**静默返回 0 但什么都不做**（我踩过一次，index tree 对比才发现）。必须用真实 checkout 的 worktree。
