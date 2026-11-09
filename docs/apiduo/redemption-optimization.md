# 兑换码优化方案

> 状态：待评审 | 日期：2026-11-08 | 适用版本：0.0.16+
> 配套材料（同目录）：
> - 产品原型 `redemption-optimization-prototype.html` — 列表/生成/导出/用户端/审计，含编辑抽屉与空态
> - 架构图 `redemption-architecture.html` — 调用链、状态机、事务边界、检索分流、导出链路
> - Demo `redemption-demo.html` — 真实跑事务回滚、并发 CAS、编辑竞态，数字由代码算出

## 0 一句话

把兑换码从「能用的 CRUD」升级成「可对账的预付凭证」：**生成原子化、状态单向、检索可按码、全动作留痕、导出受控**。

不新增业务形态（定向码 / 订阅码 / 批次表），先补齐正确性与运营效率。理由见 §9。

---

## 1 现状（代码事实）

| 能力 | 现状 | 位置 |
|---|---|---|
| 批量创建 | 循环单条 `Insert()`，100 上限，中途失败留半批 | `controller/redemption.go:100-117` |
| 兑换并发 | 行锁 + CAS（`status = enabled` 才可翻），严密 | `model/redemption.go:150-178` |
| 检索 | `id` 等值 / `name` 前缀匹配 | `model/redemption.go:75-81` |
| 列表/搜索 | 只读却开事务包 Count + Find，固定 `id desc` | `model/redemption.go:28-60, 62-124` |
| 编辑 | `Update()` 的 `Select` 含 `status`，与兑换竞态 | `model/redemption.go:206-216` |
| 状态变更 | `status_only=true`，**任意方向**，含 used→enabled | `controller/redemption.go:176-178` |
| 清无效码 | 单条 `DELETE`，无上限 | `model/redemption.go:236-240` |
| 审计 | 仅 `create`（成功）与 `delete_batch` | `controller/redemption.go:118, 226` |
| 导出 | 只有生成后的一次性 TXT/MD，列表无导出 | `redemptions-export-dialog.tsx` |
| 用户端兑换 | 错误原因统一折叠（防枚举），per-user 锁 + 限流 | `controller/user.go:1291-1309` |
| access token | 8 条路由 scope 已声明 | `middleware/access_token_routes.go:235-243` |

已做过的加固不用重做：CAS 防重复兑换、批量删除（≤1000）、过期虚拟状态、生成后导出。

---

## 2 第一性原理：兑换码是预付负债

不看代码，先看这个东西在账上是什么。一张未兑换的码 = **一笔尚未兑付的负债**。三条不变量：

- **I1 生成即负债。** 库里每张 `enabled` 码，未来必然要兑出对应额度。所以「生成成功了多少张」必须是确定值，不能是「大概 100 张，失败了 7 张但不知道是哪 7 张」。
- **I2 兑换是一次性状态迁移。** `enabled → used` 严格一次、不可回放。反过来 `used → enabled` 不是「改个状态」，而是**把一笔已偿付的负债重新变成负债**——等于印钱。
- **I3 码即现金。** 32 位明文码泄露即损失。所以审计、日志、导出里它只能以掩码存在，导出本身是高危动作。

整份方案全部由这三条推出来。凡是不属于这三条的（排序、批次、卡密格式）一律降级。

---

## 3 问题与改法

### P0-1 批量生成不原子（违反 I1）

**现象**：`AddRedemption` 循环 100 次 `Insert()`。第 37 条失败时，前 36 条已经在库里，但接口返回的是错误 + 前 36 个 key，前端只把它当失败态展示。这 36 张码成为「没人知道、但可以兑」的负债。

**根因**：没有事务边界，一次请求被切成 N 个独立提交。

**改法**：单事务 + `CreateInBatches(&rows, 100)`。
- 为什么是 100/批而不是 1000 一条：MySQL 单语句参数上限、SQLite 变量上限、以及 PG 的 statement 长度都更安全；`100 × 7 字段 = 700` 参数，三库都稳。
- 为什么不是「循环 + 事务」：那只解决原子性，不解决 100 次往返。
- 冲突重试：uuid4 碰撞概率可忽略，但若整批因唯一键失败，重新生成该批 key 重试一次，仍失败则整体回滚。

**代价**：响应时间从 O(n) 次 RTT 降到 O(n/100) 次。

### P0-2 编辑与兑换竞态，会静默复活已兑换的码（违反 I2）

**现象**：`UpdateRedemption` 先 `GetRedemptionById` 拿到快照，再 `Update()`，而 `Update()` 的 `Select` 里**包含 `status`**。若两步之间这张码被兑换（1→3），管理员的「改个名字」会把 `status=1` 写回去——码复活，且 `redeemed_time` / `used_user_id` 仍是旧值，账面上完全看不出来。

**根因**：字段编辑和状态迁移共用了同一条 `UPDATE`，且状态来自前一次读的快照而不是条件更新。

**改法**：
1. 字段编辑只更新 `name / quota / expired_time`，`Select` 里去掉 `status`。
2. 状态变更走独立的 `UpdateStatus(id, from, to)`，带 `WHERE status = from` 条件更新，`RowsAffected == 0` 即冲突（与 `Redeem` 的 CAS 同构）。

**代价**：服务端多一个函数。**前端零改动**——编辑表单本来就不提交 `status`（`redemption-form.ts` 的 schema 只有 `name / quota_dollars / expired_time / count`），状态开关走的是另一个接口 `updateRedemptionStatus`，本来就是独立请求。这一条是纯服务端修复。

### P0-3 已用码可被改回未用（违反 I2）

**现象**：`status_only=true` 不做方向校验，used→enabled 后这张码可以再兑一次额度，`used_user_id` 被新用户覆盖，历史丢失。

**改法**：**禁止 used→enabled**，返回明确错误。
- 要「再发一张」就新建一张，账上干净。
- 要「撤销误兑换」属于独立高危动作（需回收额度或明确记账），列为 P2，不混进状态开关。
- enabled↔disabled 双向允许（这不改变负债总量，只改可用性）。
- 允许修改 `expired_time` 把过期码延期——负债本来就没兑付，不算复活。

**代价**：与现有管理员习惯冲突，UI 必须解释（原型里已体现）。

### P0-4 审计缺口（违反 I3 + 可追责）

**缺失**：`update`（含状态变更）、单条 `delete`、`delete invalid`、`create` 的失败路径。

**改法**：五个 action，全部只记掩码：

| action | params |
|---|---|
| `redemption.create` | `name, count, quota, expired_time` |
| `redemption.create_failed` | 上列 + `inserted, error` |
| `redemption.update` | `id, key_masked, before{name,quota,expired_time}, after{...}` |
| `redemption.status_update` | `id, key_masked, from, to, used_user_id` |
| `redemption.delete` | `id, key_masked, status, quota, used_user_id` |
| `redemption.delete_invalid` | `deleted, remaining` |
| `redemption.export` | `filters, rows` |

`key_masked` = 前 4 + `****` + 后 4。**审计里永不出现完整码**（I3）；完整码只在导出接口里出现，且导出本身留痕。

### P1-1 搜不到兑换码本身

**现象**：`keyword` 只匹配 `id` 和 `name` 前缀。运营最高频的动作是「用户贴一张码过来问为什么用不了」，现在无法完成。

**改法**：关键词归一化后按形状分流（详见 §4.3）。

### P1-2 上限 100

**改法**：100 → 1000，前后端同步。1000 张 × 约 700B/行 ≈ 0.7MB 单事务，MySQL 默认 `max_allowed_packet` 64MB 充裕。

### P1-3 清无效码无上限

**改法**：两步分批（`SELECT id ... LIMIT 1000` → `DELETE WHERE id IN (...)`），单次请求最多 10 批，返回 `{deleted, remaining}`；前端提示「还有 N 条，继续清理」。

为什么不是 `DELETE ... LIMIT 1000`：MySQL 支持，PG 不支持，SQLite 需要编译选项。两步是三库唯一通用写法（详见 §4.5）。

### P1-4 列表不能导出

**改法**：新增 `GET /api/redemption/export`，CSV 流式。约束继承 `ExportAllLogs` 的三条：导出并发锁、`Cache-Control: no-store`、`X-Accel-Buffering: no`（详见 §4.6）。

### P2（本次不做）

- 只读接口去掉无谓事务（纯代码质量，随手改）
- 排序白名单（`quota` / `redeemed_time` / `expired_time`）
- `batch_id` 批次维度（需加列，等 §9 的数据验证）

---

## 4 关键设计

### 4.1 新逻辑放新文件，上游文件只留一行

这是本方案最重要的工程约束。两个兑换码文件都是上游原文件、零标记块，按红线每改一行都要占合并面预算（只许降）。

所以：

| 内容 | 落点 | 预算 |
|---|---|---|
| 批量生成、按码检索、状态 CAS、分批删除 | **新文件** `model/apiduo_redemption.go` | 0 |
| 导出 handler | **新文件** `controller/apiduo_redemption_export.go` | 0 |
| 导出锁 | **新文件** `service/apiduo_redemption_export.go` | 0 |
| scope 声明 | **新文件** `middleware/apiduo_redemption_routes.go`（`init()` 注册，先例：`apiduo_discount_routes.go`） | 0 |
| 上游 `model/redemption.go` | 只把 4 个函数体改成 1 行委托 | 4 块 |
| 上游 `controller/redemption.go` | 只把 4 个 handler 改成 1 行委托 + 1 行审计 | 5 块 |
| 上游 `router/api-router.go` | +1 行导出路由 | 1 块 |

### 4.2 状态机收紧

```
        ┌──────────── disabled ◄──── enabled ────► used
        └──────► enabled          （禁止）  used ─┘
```

- `enabled → used`：只能由 `Redeem` 触发，CAS 条件更新
- `enabled ↔ disabled`：管理员自由切换
- `used → *`：全部拒绝（除 P2 的「撤销兑换」高危动作）
- 过期不是状态，是 `expired_time` 与 `now` 的比较，保持虚拟状态（现有实现正确）

### 4.3 检索归一化

输入 → 归一化（`TrimSpace`、去 `-`/空格/零宽字符、转小写）→ 分流：

| 归一化后 | 判定 | SQL |
|---|---|---|
| 纯数字且 ≤ 10 位 | ID | `id = ?` |
| 长度 = 32 且全 hex | 完整码 | `key = ?`（三库都能用唯一索引） |
| 长度 4..31 且全 hex | 码前缀 | `key LIKE 'xxx%'` |
| 其他 | 名称 | `name LIKE '%kw%'` |

**为什么前缀只从 4 位起**：4 位 hex = 65536 种组合，在万级表上最多命中几十条，配合分页可用；再短就退化成扫表。

**名称从前缀改成包含**：`name` 有普通索引，`%kw%` 用不上它。取舍是明确的——兑换码表是运营表，量级万级以内，全表扫可接受；而「记得名字里有个词但记不住开头」是真实场景。

**边界：名称恰为纯十六进制**（如把批次命名为 `abc123`）会被判成码前缀，搜不到。这种命名在 20 字上限下很罕见，且判据条会显示当前识别路径，用户看一眼就知道为什么。**不引入 `name:` 转义语法**——多一个 DSL 就要多一份文档和解释成本，不值。

**三库索引差异（必须写进注释）**：
- MySQL：`char(32)` + 默认 `*_ci` collation，`LIKE 'x%'` 走索引，且大小写天然不敏感。
- SQLite：`LIKE` 对 ASCII 默认不敏感，走索引。
- PostgreSQL：`LIKE 'x%'` **在默认 collation 下用不了 btree 索引**（需要 `text_pattern_ops`），会顺序扫。同时 PG 的 `=` 是大小写敏感的。

结论：完整码（32 位）用 `key = ?` 走唯一索引，三库都最优；前缀匹配在 PG 上是扫表，接受，并注释说明「若兑换码表超过 10 万行再考虑加 `lower(key) text_pattern_ops` 索引」。**不为了 PG 的前缀查询去改上游迁移**。

### 4.4 导出的四条硬约束

| 约束 | 理由 |
|---|---|
| `text/csv; charset=utf-8` + BOM | 运营要喂 Excel，无 BOM 中文乱码 |
| 字段以 `= + - @ \t \r` 开头时前置 `'` | CSV 公式注入（Excel 会执行 `=cmd|...`） |
| 单次 ≤ 50000 行 | 内存与响应时长可控；超出提示收窄筛选 |

导出范围里的「未兑换」= `status = 1 AND (expired_time = 0 OR expired_time >= now)`，与列表的「未使用」筛选口径**逐字对齐**。不能直接用 `status = 1`——过期码状态仍是 1，会把已经作废的码导进发放清单。
| 每管理员同时 1 个导出 + 审计 | 防止把导出当批量拖库通道 |

流式实现：照 `ExportAllLogs` 的模式，`c.Writer.Write` 逐行，不攒全量。

### 4.5 分批删除的写法

```go
for i := 0; i < 10; i++ {
    ids := SELECT id FROM redemptions WHERE <cond> LIMIT 1000   // 只取主键
    if len(ids) == 0 { break }
    DELETE FROM redemptions WHERE id IN (ids)                    // 软删
    total += len(ids)
    if len(ids) < 1000 { break }
}
```
关键点：只 `SELECT id`（不回表取 `key`），且每批独立提交，避免长事务持锁。

### 4.6 用户端兑换：只改体验，不改语义

`controller/user.go:1306` 的错误折叠是**故意的**（防枚举码状态），不动。可改的只有：

- 粘贴自动清洗：去空格、横线、零宽字符，转小写
- 兑换中禁用按钮 + 防重复提交（已有 per-user 锁，前端仍要防抖）
- 失败后保留输入并全选，不清空
- 成功后余额卡片即时刷新（已有 `getSelf`）

明确不做：区分「码不存在 / 已用 / 过期」的提示。那等于给攻击者一个状态查询接口。

---

## 5 API 变更

| 方法 | 路径 | 变更 | scope | 审计 |
|---|---|---|---|---|
| GET | `/api/redemption/` | keyword 归一化 + key 检索；可选 `sort` | `redemption:read` | — |
| GET | `/api/redemption/search` | 同上（两个上游接口共用底层查询构造，语义不变） | `redemption:read` | — |
| POST | `/api/redemption/` | 原子批量；`count` 上限 1000 | `redemption:write` | `redemption.create` / `_failed` |
| PUT | `/api/redemption/` | 字段编辑不写 `status`；状态独立 CAS | `redemption:write` | `redemption.update` / `redemption.status_update` |
| DELETE | `/api/redemption/:id` | 补审计 | `redemption:write` | `redemption.delete` |
| DELETE | `/api/redemption/invalid` | 分批，返回 `{deleted, remaining}` | `redemption:write` | `redemption.delete_invalid` |
| GET | `/api/redemption/export` | **新增**，CSV 流式 | `redemption:read` | `redemption.export` |

`/api/redemption/export` 的 scope 用 `init()` 注册在新文件里，不碰上游规则表——理由与 `apiduo_discount_routes.go` 相同，且 `router/access_token_scope_test.go` 的覆盖测试会自动校验。

---

## 6 数据模型

**本次不加列。** 理由：

- 唯一想加的 `batch_id` 服务于「按批次对账」，而这个需求尚未验证（见 §9）。
- `AutoMigrate` 已包含 `&Redemption{}`（`model/main.go:352`），加列技术上无痛，但加了就要维护、要进导出、要在 UI 表达。
- 现有 `name + created_time` 足以做近似分组，先看运营是否真的需要。

---

## 7 前端交互（对应原型）

原型已实现四个主视图 + 一页审计，另补了编辑抽屉（点行内「编辑」）与搜索空态：

1. **列表 + 检索**：搜索框占位符改成「码 / 名称 / ID」，输入 32 位 hex 时自动切到「精确查码」并高亮命中行；已用码的状态开关置灰，hover 解释「已兑换的码不能改回未用」。
2. **批量生成**：数量快捷档 10 / 100 / 500 / 1000，超过 100 时提示「将生成 N 张，单事务提交」；失败态展示「全部回滚，未产生任何码」。
3. **导出**：范围（当前筛选 / 仅未兑换）、格式（CSV / TXT）、含已用开关，明确写「导出包含完整兑换码，将记录审计」。
4. **用户端兑换**：粘贴即清洗的可见反馈（显示归一化后的码）、失败保留输入、成功余额动画。

抽屉里的状态行是这套设计的门面：已兑换的码显示「已兑换的码不能改回未使用；要再发一张请新建」，
保存时提示「状态未参与本次写入」。**界面把不可逆讲清楚，比事后审计更省事。**

---

## 8 合并面预算

| 项 | 现值 | 预计 | 增量 |
|---|---|---|---|
| files | 97 | 104 | +7 |
| blocks | 286 | 298 | +12 |
| added | 2698 | ~+2878 | +180 |
| deleted | 94 | 94 | 0 |

碰到的既有上游文件：`model/redemption.go`、`controller/redemption.go`、`router/api-router.go`、
`web/src/features/redemption-codes/{api.ts, components/redemptions-columns.tsx,
components/redemptions-mutate-drawer.tsx, components/redemptions-primary-buttons.tsx}`。

若先只做 P0（不动前端），增量约为 files +3 / blocks +5 / added +80，可分批登记。

按红线，预算只许降，涨需要显式改一次并在提交里说明。这次就是那次「显式改」。

若不想涨：把 P0-2 的字段编辑拆分也做成「新函数 + 上游 1 行委托」，可再压 ~40 行，但 blocks 不会少（委托点本身要标记）。**建议接受这次上涨**，因为换回来的是原子性和防复活。

---

## 9 先量数据，再决定要不要做 P2

方案里所有「不做」都基于一个假设：兑换码是**低频运营工具**，不是高频交易链路。验证方式（部署前跑，只读）：

```sql
-- MySQL 写法；PG 需要 sum((status = 1)::int)
SELECT count(*) AS total,
       sum(status = 1) AS enabled,
       sum(status = 3) AS used,
       max(created_time) AS last_created,
       count(DISTINCT name) AS names
FROM redemptions WHERE deleted_at IS NULL;
```

再加上审计表里 `redemption.*` 的近 30 天频次。判据：

- 总行数 < 10 万 且 日增 < 500 → 全部 P0 + P1 都值得做，P2 不做
- 总行数 > 100 万 或 日增 > 5000 → 检索必须重新设计（P1-1 的前缀扫表不可接受），批次维度也必须做
- 几乎没有数据 → 只做 P0（正确性），P1 的导出与上限都可以缓

---

## 10 验证计划

**单测（新增）**
- 批量生成：1000 张一次成功；注入第 500 条冲突 → 全表 0 行新增
- 状态机：used→enabled 拒绝；enabled→disabled→enabled 允许；CAS 冲突返回明确错误
- 编辑竞态：编辑请求与兑换交错（先读后写），断言 status 保持 3
- 检索分流：32 位 hex / 前缀 / 纯数字 / 中文名 各一条；大小写混合输入
- 清除：12000 条无效码，单次请求删 10000，`remaining=2000`
- 导出：字段含 `=SUM(A1)` 时输出 `'=SUM(A1)`；BOM 存在；超过 50000 行报错

**三库**：SQLite + MySQL + PG 各跑一遍检索与分批删除（PG 校验 `key = ?` 走索引、前缀走扫表但结果正确）

**并发**：50 并发兑换同一张码 → 1 成功 49 失败，额度只加一次（现有 CAS 已保证，回归）

**审计**：七个 action 各触发一次，断言 `params` 里无完整码（正则 `[0-9a-f]{32}` 不得命中）

**性能**：生成 1000 张，对比改造前后 P95（预期 100×→10× 的往返差）

---

## 11 待拍板

1. **used→enabled 直接禁止**（推荐）还是保留为「撤销兑换」高危动作？前者干净，后者兼容现有习惯但要处理额度回收。
2. **名称改包含匹配**（`%kw%` 扫表）是否接受？不接受就保持前缀。
3. **导出上限 50000 行**是否合适？
4. 是否接受预算上涨 +120 行 / +10 块？
5. §9 的 SQL 谁来跑（生产只读），结果决定 P2 去留。

---

## 附录 A 代码事实索引

| 事实 | 位置 |
|---|---|
| 循环单条插入 | `controller/redemption.go:100-117` |
| 失败返回部分 keys | `controller/redemption.go:113` |
| `Update()` 的 Select 含 status | `model/redemption.go:214` |
| `SelectUpdate()` 死代码（无调用） | `model/redemption.go:200` |
| status_only 无方向校验 | `controller/redemption.go:176-178` |
| 只读却开事务 | `model/redemption.go:30, 63` |
| 清无效码无上限 | `model/redemption.go:236-240` |
| 兑换 CAS | `model/redemption.go:164-176` |
| 错误折叠（故意） | `controller/user.go:1306-1308` |
| 导出模式可复用 | `controller/log.go:98-168` |
| 导出锁实现 | `service/log_export.go:33-60` |
| scope 已声明 | `middleware/access_token_routes.go:235-243` |
| 自动迁移已含 | `model/main.go:352` |
