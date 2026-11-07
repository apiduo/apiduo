# 模块隔离方案：哪些能攒，能攒多久

> 2026-11-07。承接 `merge-cost-forecast.md` 的实测数据。
> 回答：能不能先建一批新模块，等齐了再一起合并上游。

## 0. 先拆掉一个混淆

「合并上游」和「发布功能」是两个独立动作，可以解耦：

- **同步上游** = 把 `upstream/main` 的新提交 merge 进来。这是维护动作，不影响你本地未完成的功能。
- **发布功能** = 把已完成的东西推出去。这是交付动作。

把同步停下来等新模块做完，等于用「确定的成本上升」换取「心理上的整洁」。你完全可以每周 merge 一次上游，同时让新模块在本地开发三个月——这两件事互不干扰。

所以真正该问的不是「能不能攒着一起合并」，而是：**哪些代码攒着不涨价，哪些攒着会变成负债。**

## 1. 实测：延迟的代价不是线性增长，是形态转换

把当前 217 个文件的改动，3-way 回放到越来越旧的上游快照上：

| 回放跨度 | 干净应用 | 冲突块 | 结构性缺失 | 可用率 |
|---|---|---|---|---|
| 0 天（当前 upstream/main） | 217 | 0 | 0 | 100% |
| 30 天（2026-10-08） | 211 | 8 | 0 | 100% |
| 90 天（2026-08-09） | 199 | **0** | **18** | 92% |
| 180 天（2026-05-11） | 143 | **0** | **62** | 66% |

**冲突块在 90 天之后反而变成 0，这不是变好了。** 是「文件根本不存在」这种更坏的情况吞掉了冲突——没法进入三方比较的东西不会报冲突。

看 18 个缺失是什么：`controller/access_token.go`、`model/user_access_token.go`、`service/security_verification.go`、整个 `web/src/features/security/`。这些是上游 8-11 月新建的**访问令牌功能域**，我们的 IP 白名单改造长在它上面。

180 天时 62 个缺失里 45 个在前端——因为 `web/src/features/` 当时还是 `web/src/pages/`。

**代价的形态：**

| 攒的时长 | 你会遇到什么 | 单次成本 |
|---|---|---|
| 0-30 天 | 行级冲突，80% 可机械解决 | 40 分钟 |
| 30-90 天 | 少数文件找不到落点，要重新定位 | 2-3 小时 |
| 90-180 天 | 1/3 的改动要重做一遍 | 1-2 人天 |
| >180 天 | 上游结构变了，等于重写 | 不可预算 |

真正的安全线是 **90 天**，不是「感觉还好」。

## 2. 判定：按挂接点分三档

模块能不能攒，不看它有多少行，只看它在**上游文件里占了几行、在哪一行**。

| 档 | 定义 | 上游触碰概率 | 能攒多久 |
|---|---|---|---|
| **S 零挂接** | 上游文件里 0 行 | 0 | 无限期 |
| **A 顶层插入** | 新 import、数组末尾、路由注册、结构体加字段、UI 插槽 | 低（不在函数体内） | ≤1 个季度 |
| **B 函数体内** | 改 switch、改 if 条件、改返回值、在既有流程中间插逻辑 | **高**（上游重写函数就整块废） | **当天合** |

判定式：

```
风险 = 该挂接点所在函数的「上游 90 天改动次数」 × 本块行数
```

`upstream-sync.md` 已经用这个思路排优先级了，现在把它变成硬规则。

**当前状态**：145 个挂接点 / 60 个文件（后端 64 块 / 32 文件，前端 81 块 / 28 文件）。后端的 64 块里，B 档约 12-15 块——这就是那张成本曲线上「现在 12 块、40 分钟」的来历。

## 3. 降档：把 B 变 A，把 A 变 S

### 3.1 渠道配置下沉成一个顶层字段（B → S，半天工作量）

现在 `ChannelOtherSettings` 里我们有 4 个独立字段：

```go
AdvancedCustom   *AdvancedCustomConfig      // 上游的
// APIDUO:advanced-async:BEGIN
AdvancedAsync    *AdvancedAsyncConfig
// APIDUO:file-relay:BEGIN
FileRelay        *ChannelFileRelayConfig
// APIDUO:artifact-storage:BEGIN
ArtifactStorage  *ChannelArtifactStorageConfig
```

这是上游 90 天改 15 次的结构体。每加一个字段，就多一个抢位置的锚点。

**改成 1 个字段：**

```go
// APIDUO:settings:BEGIN
Apiduo *ApiduoChannelSettings `json:"apiduo,omitempty"`
// APIDUO:settings:END
```

```go
// relaykit/dto/apiduo_channel_settings.go —— 上游不认识这个文件
type ApiduoChannelSettings struct {
    AdvancedAsync   *AdvancedAsyncConfig
    FileRelay       *ChannelFileRelayConfig
    ArtifactStorage *ChannelArtifactStorageConfig
    EndpointMatrix  *EndpointMatrixConfig      // 以后加什么都塞这里
}
```

4 个块变 1 个块，且**以后新增渠道级配置零挂接点**。兼容性：读的时候两个位置都看，写的时候只写新的（迁移期），存量数据不用动。

### 3.2 Hook 总线（B → A，2-3 天）

后端 64 个块里，大部分是「在某处插一句判断」。把它们收敛成一个入口：

```go
// service/apiduo/gate/gate.go —— 上游不认识
type Ctx struct {
    Gin    *gin.Context
    Info   *relaycommon.RelayInfo
    Path   string
    Body   []byte
}

type Gate interface {
    Name() string
    Match(*Ctx) bool
    Handle(*Ctx) (handled bool, err error)
}

var gates []Gate
func Register(g Gate)          { gates = append(gates, g) }
func Run(c *Ctx) (bool, error) { /* 按序匹配，命中即返回 */ }
```

上游文件里只留一处：

```go
// APIDUO:gate:BEGIN
if handled, err := apiduoGate.Run(gateCtx); handled || err != nil {
    return
}
// APIDUO:gate:END
```

之后所有请求级功能（文件中转、产物改写、IP 白名单、限流、矩阵拒绝）都注册进 `gate`，**不再新增上游挂接点**。

适用：请求入口/出口的横切逻辑。
不适用：必须拿到某个局部变量才能工作的地方（这类老老实实做 A 档，别再往下压）。

预期收益：后端挂接点 64 → **12 左右**（路由注册 4、模型初始化 4、渠道结构 1、relay 入口 1、审计 1、其他 1）。

### 3.3 路由层不要抢注册位（B → A）

`/files` 的教训：上游已经用 `RelayNotImplemented` 占住了 4 条路径，我们抢注册必须删它的行。

改法：在 gin 的 `NoRoute` 前置钩子里拦（`router/file-relay-handoff.go` 已经在做类似的事），或者用 `http.Handler` 包装层。**上游那 4 行占位保持原样**，将来上游实现它时，我们只是把 handoff 关掉。

### 3.4 UI 用注册表，不用散落的条件渲染（A → S）

前端 81 个块没法全收敛（UI 必须在具体位置渲染），但可以约束形态：

- 已经做对的：`section-registry.tsx`、`system-settings.config.ts`、`marks.ts`——注册表 + 统一渲染点。
- 该改的：`channel-mutate-drawer.tsx` 里 11 个块，很多是「根据类型显示某个面板」。改成渠道类型 → 面板组件的注册表，块数能砍到 2-3 个。

这个文件上游 90 天改 25 次，是全仓第二热，值得花半天。

## 4. 目录与依赖规则

```
service/apiduo/
  gate/          # 请求门禁总线
  settings/      # ApiduoChannelSettings 及各子结构
  feature/       # 各功能实现
    filerelay/
    artifact/
    navapp/
    matrix/
```

三条硬规则：

1. **不建独立 go module**。`relaykit` 独立是因为它要给外部用户 import；我们没有这个需求。目标只是文件隔离，别引入 `replace` + 版本管理的复杂度。
2. `apiduo/*` 可以 import `dto` / `model` / `setting` 的公共类型；**不能 import `controller` / `router`**（会形成反向依赖，也说明这个功能放错了层）。
3. 上游文件 import `apiduo` 只允许在标记块内，且**每个上游文件最多 1 个 apiduo 块**。

第 3 条是最硬的约束——它把「一个文件插 11 块」这种形态直接禁掉，逼着往注册表/总线走。

## 5. 协议矩阵按这个框架怎么落

| 部分 | 档位 | 说明 |
|---|---|---|
| 矩阵编辑器 | **S** | 新前端组件，只经由 `advanced-custom-editor-dialog.tsx` 的 1 个插槽进入 |
| 矩阵 → routes 编译 | **S** | 纯前端函数，后端零感知 |
| 可行转换校验 | **S** | 前端选项生成 + 现有 `Validate()` 兜底 |
| `reject` 语义 | **S** | 纯 UI（删行即拒绝） |
| 图像转换器 | **A** | `relaykit/relayconvert/image_converter.go` 新文件 + `IsAdvancedCustomConverterAllowed` 1 行 |

**结论：整个协议矩阵是 S 档，只有图像转换器是 1 处 A 档。可以攒，攒多久都不会涨价。**

反过来说，文档原方案里 7 个 B 档挂接点，全都来自「把矩阵语义放到后端」这个决定。语义放前端，B 档全部消失。

## 6. 顺序

| 步骤 | 工作量 | 收益 |
|---|---|---|
| 1. `ApiduoChannelSettings` 合并（§3.1） | 0.5 天 | 4 块 → 1 块，以后零成本 |
| 2. Hook 总线（§3.2） | 2-3 天 | 后端 64 块 → ~12 块 |
| 3. `channel-mutate-drawer` 注册表化（§3.4） | 0.5 天 | 11 块 → 2-3 块 |
| 4. 新功能按档位开发 | — | 攒着不涨价 |

**先做 1 和 2，再开协议矩阵。** 顺序反了的话，矩阵会先按 B 档写出来，再拆一遍。

## 7. 护栏

`markercheck` 加一张台账，每个挂接点登记档位和上游热度：

```
ID                        档  位置                                    上游90d  行数
token-ip-allowlist        A   middleware/auth.go:TokenAuth              8       42
file-relay-handoff        A   router/relay-router.go:SetRelayRouter     9        4
channel-other-settings    A   relaykit/dto/channel_settings.go:struct   15       1
```

CI 每次同步上游后自动重算「上游 90d」列，以下情况打警告（不失败）：

- 任一 B 档挂接点的上游热度 > 10
- 后端挂接点总数 > 20
- 单个上游文件里的 apiduo 块 > 1

这三条正好对应 §2 的退化阈值和 §4 的第 3 条规则。

## 附：回放测试怎么跑

```bash
# 生成当前全部改造的补丁（含新增文件）
git diff $(git merge-base upstream/main HEAD) HEAD > /tmp/our_full.patch

# 检出目标历史快照（必须真实 checkout，不能 --no-checkout）
git worktree add --detach /tmp/wt <历史提交>

# 逐文件 apply：整体 apply 是原子的，一个失败全部回滚
cd /tmp/wt && python3 - <<'EOF'
import subprocess, re
patch = open('/tmp/our_full.patch').read()
for p in re.split(r'(?m)^(?=diff --git )', patch):
    if not p.startswith('diff --git '): continue
    open('/tmp/one.patch','w').write(p)
    r = subprocess.run(['git','apply','--3way','/tmp/one.patch'], capture_output=True, text=True)
    err = r.stderr
    kind = 'MISSING' if 'does not exist in index' in err else 'DRIFT' if 'does not apply' in err else 'OK'
    print(kind, p.split('\n')[0][11:])
EOF
```

`git apply --3way` 整体是**原子操作**：18 个文件失败会导致全部回滚，看起来像「什么都没做」。所以要按文件拆开跑。

## 7. 2026-11-07：把成本做成可测量、只降不升

前面几节讲的是「怎么降」，这一节解决「怎么保证不涨回去」。靠自觉不行——多 key 那一批功能就顺手加了 20 个挂接点，其中 `controller/channel.go` 一个文件 10 块。

### 7.1 工具：`-cost` 与预算

```bash
go run ./tools/markercheck -cost           # 按文件列出：块数 / 新增行 / 删除上游行
go run ./tools/markercheck -cost -update   # 功能收尾时收紧预算
```

预算存在 `.apiduo/merge-budget.txt`，只记四个总量（上游文件数、标记块数、新增行、删除上游行）。**只要有一项上涨，默认检查就失败**，必须显式跑一次 `-update`。

选总量而不是逐文件，是因为逐文件基线必然被无意义的波动反复触发；而「一共插了多少块」这个数字，任何一次功能开发都会推动它，推不动就说明真的没加挂接点。

当前基线：78 文件 / 199 块 / 1956 行 / 删除 80 行。

### 7.2 原则：优先「追加」而不是「改写」

同样一处逻辑，写法决定了冲突概率：

| 写法 | 上游改动后的结果 |
|---|---|
| 替换上游那行 | 必然冲突，哪怕语义没变 |
| 在上游行之后追加一行覆盖结果 | 只有上游删掉上下文才冲突 |
| 在 switch 里加 `case` | 上游重排分支就冲突 |
| 在 switch 之前加守卫 `if` | 上游怎么改分支都不冲突 |

这一批改动的具体应用：

- `model/channel.go` 轮询游标：从「改写赋值行」改成「保留上游行 + 后一行镜像到缓存对象」，删除行 1 → 0
- `model/channel.go` 未知模式：从「改写 `default:` 分支」改成「switch 之前加前置守卫」，删除行 2 → 0
- `controller/channel.go` 状态统计：删除 `switch` 里的 `case`，改为响应组装时一次 `channel.MultiKeyStatusCount(...)` 调用，删除行 1 → 0，顺带把 4 块压成 2 块
- `controller/channel.go` 请求结构体：新字段放到结构体**末尾并空一行**——gofmt 按空行分对齐组，插在中间会让上游那几行的空格宽度全变，凭空多出 4 行「删除」
- `channel-mutate-drawer.tsx` 模式提示：从「替换整段三元表达式」改成「把上游那段包进 `!== 'sticky' &&`，另起一个 `=== 'sticky' &&`」，删除行 11 → 0

净结果：这批改动的上游删除行 38 → 23，`model/channel.go`、`controller/channel.go`、`channel-mutate-drawer.tsx` 三个热点文件**删除行全部为 0**。

### 7.3 还没做的

- `channel-mutate-drawer.tsx` 19 块、`channel-form.ts` 15 块、`types.ts` 10 块——都是历史累积，不是一批功能造成的。要降只能按 §3.4 做注册表化，属于独立任务。
- `relay/relay_task.go` 的 5 行删除是语义必需（把 `channelModel.Key` 换成绑定 key），无法用追加表达，留在账上。

### 7.4 「新开一个模式」到底要几块

常被问：能不能不改上游的既有逻辑，另开一条路？可以，而且比想象中便宜——前提是**常量也放进新文件**。

Go 允许在同一个包里新建文件声明变量和常量。所以给 `constant.MultiKeyMode` 加一个值，不需要去改 `multi_key_mode.go`：

```go
// constant/apiduo_multi_key_mode.go —— 上游不认识这个文件
package constant

const MultiKeyModeSticky MultiKeyMode = "sticky"
```

同理 `common.MultiKeyStatusStandby` 放进 `common/apiduo_multikey_status.go`。这两个文件改完之后，`common/constants.go` 和 `constant/multi_key_mode.go` 的 diff **整个归零**。

代价只有一处：如果上游哪天也定义了同名常量，编译会直接报重复声明。这是好事——它会在编译期炸出来，而不是安静地变成两套语义。

于是「新增一个多 key 策略」的总成本收敛到：

| 层 | 挂接点 |
|---|---|
| 枚举常量 | **0**（新文件） |
| 选路分发 | 1 块（`GetNextEnabledKey` 的 switch 之前一个守卫） |
| 请求期覆盖 | 1 块（`distributor` 里一行调用） |
| 前端下拉 + 提示 | 2-3 块（选项数组、渲染、类型枚举） |

**但要分清两种情况：**

- **「多一种选择」**：新开模式几乎免费，就是上面这张表。
- **「改已有选择的行为」**：模式帮不上忙。例如轮询游标丢写、任务 key 绑定——这些是**所有模式共用**的路径，只能改共享代码。用模式绕过等于给每个模式复制一份修复，比冲突更糟。

判据：**这个需求能不能表达成「并列的一条路」？** 能，就新开模式；不能，就老实找最小切入点。

反过来的风险也要说清：模式不是越多越好。每多一个模式，绕开共享路径的测试矩阵就翻一倍，而共享路径里的上游修复也未必覆盖到它。现在四个模式（random / polling / sticky / 未配置）已经是合理上限，再往里塞就该考虑「策略是不是该做成配置项而不是模式」。
