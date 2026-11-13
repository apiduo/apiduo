# 用户并发治理：产品方案与改造难度评估

> 版本：v1（设计评审稿，2026-11-11）
> 范围：只做产品原型与解决方案，**不包含任何业务代码实现**
> 结论依据：OpenRouter 官方 Limits 文档（2026-11-11 核实）+ 本仓库实际文件核查

---

## 0. 结论先行

**更简单的是「OpenRouter 式」路线，改造量约为「额度自适应并发闸门」的 1/3～1/2，且几乎不引入新的运行时状态。**

但这个"更简单"有一个不可忽略的代价，必须先说清楚：

| 判断 | 内容 |
|---|---|
| 事实 1 | OpenRouter **没有**「按额度映射并发数」这种机制。它的控制骨架是：分层档位 + 请求速率/日配额 + 在途金额预算（in-flight spending budget）。 |
| 事实 2 | 速率限制**不约束瞬时并发**。一个被限为 20 RPM 的用户，仍可在同一秒发出 200 个并行请求。若痛点是「单用户瞬时打满上游共享渠道」，OpenRouter 式不能等价替代。 |
| 事实 3 | 本仓库已有「速率限流」「额度预扣—退还」「按用户窗口计数」三套机制，但**完全没有在途并发计数与释放收口**。难度的全部差异来自这里，而不是来自那个数学函数。 |

**推荐路径：C（混合 / 分两期）。**
Phase 1 落地 OpenRouter 骨架（更低风险、可灰度、可回滚），同时把您要的额度曲线**以「档位表」形式落地**——曲线离线生成分档，运行时只查表，不逐请求算对数。
Phase 2 只有当监控证明「瞬时并发」仍是真实瓶颈时，才新增并发闸门。

若必须一次性满足「手动 > 自动 限制并发」，最小改动变体见 §4.4：单节点内存计数 + 仅覆盖 HTTP relay + 手动覆盖走 option JSON（不改用户表）。

---

## 1. 事实核查（对抗审查基线）

### 1.1 OpenRouter 实际做法（官方 `docs/api-reference/limits`）

OpenRouter 把限制分成三类，用不同 HTTP 语义区分：

| 类别 | 管什么 | 触发码 | 检查位置 |
|---|---|---|---|
| Credit limits | 能花多少钱：账户余额、per-key 额度上限、**在途消费预算** | `402` | `GET /api/v1/key` → `limit_remaining`；`error.metadata.limit_source` |
| Budget limits | 组织管理员配置的支出上限（guardrail / workspace / key） | `403` | `GET /api/v1/key` → `budgets`、`effective_budget` |
| Rate limits | 请求数量：`:free` 额度、key 管理、per-IP burst、DDoS、上游 provider | `429` | `X-RateLimit-*` 响应头 |

关键规则（原文要点）：

1. **分层档位，且依据是累计购买额，不是当前余额**
   - `:free` 模型：`Credits purchased (all time) < 10` → 20 RPM / 50 次每日；`≥ 10` → 20 RPM / 1000 次每日。
   - 为吸收手续费与取整，高档实际从 **9 credits** 起授予；档位「与 `is_free_tier` 无关」，只看历史累计购买。
   - 设计含义：**档位单调不回退**。余额花光不会把用户降级，避免体验抖动和策略自激。
2. **付费模型没有平台级请求上限**
   - 原文：*"switch to the paid variant of the model, which has no platform-level request cap"*。
   - 平台只保留 DDoS 保护、per-IP burst、上游 provider 429 + 自动 fallback 路由。
3. **在途消费预算（与"并发"最接近的机制，但仍是金额而非条数）**
   - 原文：*"a cap on the estimated token cost of the paid requests you have running or recently completed, relative to your balance"*。
   - 请求前按 `input tokens + max_tokens` 估算成本并 hold，完成/失败后换成实际成本、短暂结算窗口后释放。
   - 预算 = 当前余额的一个比例，**有固定上限**；余额高于阈值则完全不适用；仅作用于低余额预付费账户与新账户。
   - 拒绝发生在请求到达 provider **之前**，返回 `402` + `limit_source: openrouter_in_flight_budget` + `Retry-After`；单请求成本超过整个预算时是 `openrouter_credits` / `weight_exceeds_budget`（重试无意义）。
   - BYOK、免费模型、企业/订阅账户豁免。
4. **429 契约细节（值得抄）**
   - `X-RateLimit-Limit / Remaining / Reset` 只在平台自身 429 上返回；成功响应不带这些头。
   - 上游 429 带 `error.metadata.provider_code`，并先走 fallback。
   - 流式已开始后触发限流，**不是 HTTP 429**，而是 SSE `finish_reason: "error"` 事件。

**对抗结论**：把「参考 OpenRouter」理解为「照抄一个按余额映射并发数的公式」是错的。OpenRouter 的成熟度不在于公式，而在于：① 用累计购买额做单调分档；② 用金额 hold 而非条数计数规避流式生命周期问题；③ 用 402/403/429 三码分离把「钱不够」「预算超了」「请求太快」分开，让客户端能正确选择"加钱 / 等待 / 退避"。

### 1.2 本仓库现状（已核实的实际文件）

> **重要前提**：当前工作区是**裁剪快照**——根目录无 `go.mod` / `go.sum`，`model/user.go`、`controller/relay.go`、`common/`、`i18n/`、`web/src/i18n/locales/`、大部分 `@/components/ui/*` 均不存在，无法编译或跑测试。以下难度评估按「完整 upstream 工程」给，本快照只能作为「改造地图」，不能直接落地验证。

**已具备（可直接复用）：**

| 能力 | 位置 | 说明 |
|---|---|---|
| 请求速率限流 | `router/relay-router.go:84` `middleware.ModelRequestRateLimit()`，`/v1` group 级 | 一处挂载覆盖全部 HTTP relay 路由 |
| 用户维度速率限流 | `router/api-router.go:45-46,155-164` `UserCriticalRateLimit(scope)` | 已有按用户限流的挂载范式 |
| 额度预留 / 退还 / 扣减 | `controller/file.go:158-230`：`TryReserveTokenQuota` → 失败 `refundUploadQuota`/`IncreaseTokenQuota` → 成功 `DecreaseUserQuota` | **这就是 in-flight spending budget 的现成骨架** |
| 按用户窗口计数 | `service/filerelay/quota.go:26-58` `CheckAllowance(userId, ...)` + `model.GetFileUsage(userId, since)` | 日请求数 / 容量计数可仿此实现 |
| 配置项注册与持久化 | `setting/system_setting/file_relay_setting.go:18-74` | struct + 默认值 + `init()` 注册 + OptionMap 重放；通用 `/api/option/` 自动覆盖新 key |
| 不改 schema 的 per-user 覆盖范式 | `model/nav_app.go:13-17` | 注释明确推荐「用 option 表存 JSON，不改 schema、便于 rebase upstream」 |
| 共享 Redis | `main.go:357` `InitRedisClient()` | `README.zh_CN.md:229` 要求多节点共享限流额度 |

**未找到（必须新建）：**

- 任何**在途请求数**计数：无 `Semaphore`、无 Redis `Incr/Decr` 业务实现、无 `rate` 令牌桶实现（Grep 全仓无业务命中）。
- 统一的 `defer` 释放收口：`controller.Relay` 不在仓库；可见的 `FileCreate` 是多 return 分支 + 每个失败分支手工退还。
- SSE 结束时机 / 客户端断连时序的本仓库范例（仅 `relaykit/README.md:150-163` 文档描述）。
- 用户模型定义、用户编辑页、7 语言 i18n JSON、`@/components/ui/*` 表单组件。

**风险提示**：`README.zh_CN.md:245` 明确写着「独立 Redis 或内存限流器会按节点分别计数」。任何内存计数器在多节点部署下都只是**每节点**的上限，不是全局上限。

### 1.3 三个必须先钉死的定义

| 歧义 | 事实 | 默认假设（写进配置） |
|---|---|---|
| **额度单位** | 项目用 `QuotaPerUnit`（默认 500000 quota = $1）。若 10 / 1000 按 quota 解释，则 **$1 = 500000 quota 已远超 1000**，规则会退化成「几乎所有用户都不限」。 | 阈值按 **USD / credit**：$10 = 5,000,000 quota、$1000 = 500,000,000 quota。配置项以 quota 存储，UI 以 USD 展示，用 `QuotaPerUnit` 换算。 |
| **额度取值** | 当前余额随消耗下降，且请求开始时 `TryReserveTokenQuota` 会**预扣**，实时余额被在途请求压低。用实时余额算并发上限会形成正反馈自激：并发越多 → 余额越低 → 上限越低。 | 用**结算后稳定余额**或**历史累计充值额**作为输入；禁止使用含预扣的实时余额。 |
| **「并发」是什么** | 需明确是否计入 SSE 长连接、WebSocket（`/v1/responses`、`/v1/realtime`）、MJ 路由、插件任务；以及客户端提前断连、进程崩溃后的计数回收。 | Phase 1 只覆盖 `/v1` 下的 HTTP relay；WebSocket/MJ/插件路由显式标注「未纳管」，二期决策。 |

---

## 2. 第一性原理拆解：难度差在哪里

把「限制用户并发」拆成三层，成本差异只出现在其中一层：

```
决策层  f(用户状态) → 上限值          纯函数 / 查表，两条路线都简单
计量层  统计并原子地维护「当前用量」   ← 全部难度差异在这里
拒绝层  429 + 响应头 + 可观测 + i18n   中等，可复用既有范式
```

| 计量对象 | 需要的操作 | 生命周期挂钩 | 本仓库基础 |
|---|---|---|---|
| 请求速率 / 日配额 | `+1`、固定窗口过期 | **不需要**知道请求何时结束 | ✅ 已有 `ModelRequestRateLimit`、`GetFileUsage(userId, since)` |
| 在途金额 | `hold(预估成本)` → `release` | 挂钩**计费/预扣链路**（已有收口） | ✅ 已有 `TryReserveTokenQuota` / `refundUploadQuota` |
| 在途请求**条数** | `+1` → `-1`，且必须严格配对 | 挂钩**每个网络请求的开始与结束**（无收口） | ❌ 零基础 |

第三行的难度不是"写个原子计数器"，而是要覆盖五种结束形态：正常返回、流式 SSE 最后一个 chunk、客户端断连、上游超时/重试/fallback、进程崩溃或滚动重启。任何一处漏 `-1` 都会造成计数泄漏，用户被永久限死；因此还必须加 TTL 兜底与对账。这也是 OpenRouter 选择「金额 hold」而非「条数计数」的工程原因。

---

## 3. 三条路线与难度对比

| 路线 | 定义 |
|---|---|
| **A 原需求** | 手动 > 自动；自动按额度算在途并发上限：<10 文本 3 / 其他 1；10～1000 用函数；≥1000 不限。 |
| **B OpenRouter 式** | 分层档位（累计充值额）+ RPM/日请求配额 + 在途金额预算；付费高额度档不做平台级请求上限，靠上游 429 + fallback + DDoS 兜底。手动覆盖作用于档位。 |
| **C 混合（推荐）** | Phase 1 = B；把 A 的额度曲线**离线生成档位表**作为档位判定输入；Phase 2 可选新增并发闸门，复用同一张档位表。 |

### 难度矩阵（1 = 最轻，5 = 最重）

| 维度 | A 并发闸门 | B OpenRouter 式 | C 混合 |
|---|:---:|:---:|:---:|
| 新增运行时状态 | 5（分布式在途计数 + 崩溃兜底） | 1（复用已有计数器/额度 hold） | 2 |
| 可复用既有件 | 1（仅配置注册、OptionMap） | 4（速率限流、窗口计数、额度预扣退还、通用 option API） | 4 |
| 改动文件数量级（完整工程） | 约 15～22 个 | 约 6～9 个 | 一期 6～9，二期 +8～12 |
| 数据库迁移 | 3（用户级覆盖字段，或 option JSON 规避） | 1（无需改 schema） | 1 |
| 多节点一致性 | 5（必须 Redis 原子 + 租约 + 释放幂等） | 2（沿用既有共享 Redis 限流） | 2 |
| 流式 / 断连 / WebSocket 风险 | 5（全部是硬骨头） | 1（无生命周期挂钩） | 2 |
| 前端 + 7 语言 i18n | 3（设置页 + 用户覆盖 UI + 新文案 key） | 3（同） | 3 |
| 灰度与回滚 | 4（计数泄漏会影响线上用户） | 1（配置开关即可关） | 1 |
| 与「瞬时并发」语义等价 | ✅ 是 | ❌ 否 | 二期才是 |
| **综合难度** | **4.0 / 5** | **1.5 / 5** | **一期 1.5，整体 2.5 / 5** |

**结论**：B 最简单，且简单在「不引入新的运行时状态」这一根本点上。A 的成本 80% 不在您要设计的那个函数，而在计数器的生命周期。

### 分场景建议

| 如果真实目标是…… | 选 | 理由 |
|---|---|---|
| 防止低额度用户滥用、超支、长期占用共享容量 | **B** | 分层 + 日配额 + 在途金额预算已经覆盖，且可灰度可回滚 |
| 硬性约束「单用户同时最多 N 个请求」 | **A 或 C 二期** | 只有条数计数能保证这个不变量，B 不等价 |
| 既要低风险上线、又要保留并发能力 | **C** | 一期先拿数据，用监控证明是否真的需要二期 |

---

## 4. 方案设计

### 4.1 自适应额度曲线（三条路线通用）

记 `B` = 有效额度（USD，由 §1.3 口径换算），`L` = 全局软上限（默认 64，可配）。

| 区间 | 文本并发 `C_text` | 其他并发 `C_other` |
|---|---|---|
| `B < 10` | 3 | 1 |
| `10 ≤ B < 1000` | 见下式 | `max(1, floor(C_text / 3))` |
| `B ≥ 1000` | 解除**用户级**限制 | 解除**用户级**限制 |

**推荐：对数插值**（跨 2 个数量级时，每 10 倍额度给相同增量，比幂律更平滑）

```
C_text(B) = 3 + floor( (L - 3) * log10(B / 10) / 2 )
```

**备选 1：幂律（更保守）** `C_text(B) = floor(3 * (B/10)^k)`，`k = ln(L/3)/ln(100) ≈ 0.6645`。低段明显更严（B=100 只给 13），高段更陡。

**备选 2：阶梯档位（最简、最可解释、OpenRouter 味最浓）**
`[10,50)→4 / [50,200)→8 / [200,1000)→16 / ≥1000→不限`。无浮点、无抖动、后台一眼能讲清。

数值对照（L = 64）：

| B (USD) | 对数 text | 对数 other | 幂律 text | 幂律 other | 阶梯 text |
|---:|---:|---:|---:|---:|---:|
| < 10 | 3 | 1 | 3 | 1 | 3 |
| 15 | 8 | 2 | 3 | 1 | 4 |
| 20 | 12 | 4 | 4 | 1 | 4 |
| 31.6 | 18 | 6 | 6 | 2 | 4 |
| 50 | 24 | 8 | 8 | 2 | 8 |
| 100 | 33 | 11 | 13 | 4 | 8 |
| 200 | 42 | 14 | 21 | 7 | 16 |
| 316 | 48 | 16 | 29 | 9 | 16 |
| 500 | 54 | 18 | 40 | 13 | 16 |
| 800 | 61 | 20 | 55 | 18 | 16 |
| ≥ 1000 | 不限（软顶 64） | 同左 | 不限（软顶 64） | 同左 | 不限 |

**三个必须加的工程修正（对抗审查）：**

1. **「不限」不等于无限**。`B ≥ 1000` 应定义为「解除按额度计算的用户级上限」，仍受全局软上限 `L`、上游 provider 429、DDoS 保护约束。否则单个高额度用户可无限打满共享渠道——OpenRouter 同样不做真无限，它靠上游与 DDoS 兜底。
2. **滞回（hysteresis）**：升级即时生效；降级需连续 N 分钟（默认 10 分钟）低于阈值、或到下次结算/充值才生效。这是对 OpenRouter「累计购买额单调不回退」的借鉴，避免用户「越用越慢」的抖动。
3. **稳定余额输入**：禁止用含预扣的实时余额；否则并发 ↑ → 余额虚低 → 上限 ↓ 形成自激。建议用累计充值额做档位（B 路线），或结算后余额做曲线输入（A/C 路线）。

`B = 1000` 处存在从 64 到软顶的语义跳变，不是数值跳变（软顶默认同为 64），因此用户实际无感；若把软顶调大，需在 UI 上明确提示「解除额度上限后受全局上限 G 约束」。

### 4.2 手动 > 自动 的优先级与契约

| 顺序 | 来源 | 说明 |
|---|---|---|
| 1 | 用户级手动覆盖 | 最高优先级；含「跟随自动 / 不限 / 固定整数」 |
| 2 | 令牌级手动覆盖 | 可选，粒度更细 |
| 3 | 分组级手动覆盖 | 可选 |
| 4 | 自动策略 | 按 §4.1 曲线或档位计算 |
| 5 | 全局默认 | 兜底值，配置关闭时生效 |

值域约定：`-1 = 跟随自动`、`0 = 不限`、`n ≥ 1 = 固定并发数`（文本与其他各一组）。

对外查询契约（字段示意，非代码）：

```json
{
  "user_id": 1024,
  "effective": { "text": 12, "other": 4 },
  "source": "auto",
  "auto_value": { "text": 12, "other": 4 },
  "manual_value": { "text": -1, "other": -1 },
  "basis": { "balance_usd": 20, "tier": "10-50", "hysteresis": "stable" }
}
```

- **生效值必须可解释**：管理后台要能显示「手动覆盖中（自动应为 X）」，避免运营误判。
- **存储建议**：per-user 覆盖用一条 option JSON（仿 `model/nav_app.go:13-17`），**不改 users 表、无迁移、便于 rebase upstream**。
- **审计**：记录谁在何时把某用户从自动改为手动、原自动值是多少。

### 4.3 拒绝契约（借鉴 OpenRouter 的码分离）

| 场景 | 状态 | 关键字段 |
|---|---|---|
| 在途并发/速率超限，可重试 | `429` | `error_type: concurrency_limit_exceeded`；`X-RateLimit-Limit / Remaining / Reset`；`Retry-After` |
| 在途金额预算耗尽，可重试 | `429`（或沿用项目既有 402 语义） | `limit_source: in_flight_budget`；`Retry-After` |
| 单请求预估成本 > 整个预算，重试无意义 | `402` | `reason: weight_exceeds_budget`；提示降低 `max_tokens` 或充值 |
| 额度不足以支付 | `402` | 沿用项目既有余额不足语义 |

- 所有面向 AI 客户端的错误文案用**英文源串**作 key，按 `AGENTS.md:33-42` 补齐 7 个前端 locale 与后端 3 语言。
- 流式已开始后再触发限流，用 SSE `finish_reason: "error"` 事件而非 HTTP 429（OpenRouter 同款做法）。

### 4.4 若必须一期就做「条数并发」：最小改动变体

1. 只在 `router/relay-router.go:84` 之后、`Distribute()` 之前挂**一个** group 级 middleware（一处覆盖所有 HTTP relay 路由）。
2. 计数放 middleware 的 `c.Next()` + `defer` 释放，覆盖所有 return 分支；WebSocket / MJ / 插件路由显式不纳管并写进文档。
3. 存储优先共享 Redis + 带 TTL 的租约（防进程崩溃泄漏）；单节点可降级为内存计数，但 UI 必须标注「每节点上限」。
4. 手动覆盖走 option JSON，不碰用户表。
5. 灰度：先只对 `B < 10` 档开启（这档本来就是 3/1），观察 24h 计数对账无泄漏后再逐档放量；保留一键关闭开关。

---

## 5. 改造清单（按完整工程估算）

### Phase 1（B / C 一期，约 6～9 个文件）

| 层 | 动作 |
|---|---|
| 配置 | 新增 `setting/system_setting/concurrency_setting.go`（仿 `file_relay_setting.go`：struct + 默认值 + `init()` 注册 + OptionMap 重放）；通用 `/api/option/` 无需改路由 |
| 决策 | 新增纯函数模块：档位/曲线判定 + 手动覆盖优先级 + 滞回；输入为稳定余额/累计充值额 |
| 计量 | 复用 `ModelRequestRateLimit()` 与 `GetFileUsage(userId, since)` 做 RPM/日配额；复用 `TryReserveTokenQuota` / `IncreaseTokenQuota` 做在途金额预算 |
| 拒绝 | 复用既有 429 出口，补 `X-RateLimit-*` / `Retry-After` / `limit_source` 字段 |
| 前端 | 系统设置新增一个 section + 表单组件（仿 `web/src/features/channels/components/file-relay-setting.tsx`）；试算器 |
| i18n | 7 个前端 locale + 后端 3 语言补 key；跑 `bun run i18n:sync` |
| 测试 | Go 表驱动测试（档位边界 10 / 1000、滞回、手动优先级、429 契约）；前端组件测试 |

### Phase 2（C 二期，可选，约 +8～12 个文件）

新增 `middleware/concurrency.go` + `service/concurrency.go`（Redis 原子计数与租约释放）、路由挂载、WebSocket/MJ 决策、用户覆盖 UI、计数对账与监控、断连/崩溃回归测试。

### 验收要点

- 边界：B = 9.99 / 10 / 999.99 / 1000 四处的生效值；文本与其他的 3:1 关系。
- 手动 > 自动：手动值不随额度变化；清除覆盖后回到自动值。
- 多节点：两个实例同时压测，验证是全局上限而非每节点上限。
- 生命周期：流式正常结束、客户端提前断连、上游超时、进程 kill 后计数无泄漏。
- 可回滚：关闭开关后行为与改造前完全一致。

---

## 6. 待确认（不阻塞原型，但阻塞实现）

1. **阈值单位与口径**：10 / 1000 是 USD/credit 还是内部 quota？默认按 USD（见 §1.3）。
2. **曲线输入**：用「历史累计充值额」（OpenRouter 式、单调不回退）还是「结算后余额」（更贴合"自适应"但会降级）？
3. **是否必须硬限制瞬时并发**：这一项直接决定走 B 还是必须上 A/C 二期。
4. **纳管范围**：WebSocket `/v1/responses`、`/v1/realtime`、`/mj`、插件任务路由是否纳入。
5. **曲线形态**：对数（推荐）/ 幂律 / 阶梯；软上限 L 与滞回窗口的默认值。

---

## 7. 参考资料

- OpenRouter, *Limits*（Credit / Budget / Rate limits、in-flight spending budget、402/403/429 契约）：https://openrouter.ai/docs/api-reference/limits
- OpenRouter, *Pricing — Usage and Rate Limits*（付费模型无平台级请求上限）：https://openrouter.ai/pricing
- OpenRouter Support, *Rate Limits – What You Need to Know*（free 档 20 RPM / 50→1000 daily、$10 累计购买）：https://openrouter.zendesk.com/hc/en-us/articles/39501163636379-OpenRouter-Rate-Limits-What-You-Need-to-Know
- 本仓库：`AGENTS.md`（i18n / 三库迁移 / 测试约定）、`README.zh_CN.md:229,245`（共享 Redis 与按节点计数）、`router/relay-router.go:75-111`、`controller/file.go:158-230`、`service/filerelay/quota.go:26-58`、`setting/system_setting/file_relay_setting.go:18-74`、`model/nav_app.go:13-17`
