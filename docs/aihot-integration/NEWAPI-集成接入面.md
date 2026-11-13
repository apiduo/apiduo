# New API 挂接接入面设计

> **v0.2 · 2026-11-10**：按"AIHOT 独立部署 + New API 挂接调用"重写。
> v0.1 的投影表方案不删除，降级为**升级路径**（见第 7 节），首版不实现。
> 术语：产品本体是 **New API**；`APIDUO:*` 是本仓库的定制标记前缀。

---

## 0. 结论

**新增 2 个文件，修改 1 处共 4 行，不建任何表。** 代码量约为投影方案的 1/4。

| 类别 | v0.1 投影方案 | v0.2 挂接方案 |
|---|---|---|
| 新增文件 | 8 | **2** |
| 修改既有文件 | 3 | **1** |
| 新建数据表 | 5 | **0** |
| 后台常驻任务 | 1 | **0** |
| 数据新鲜度 | 滞后 ≤ 5 分钟 | **实时** |

**但运维面一点没减少。** 仍是两套系统、两个数据库、两份备份、两个升级节奏。"独立服务"省的是代码，不是运维。这一点在第 8 节展开。

---

## 1. 为什么这个判断是对的

先对上一版做对抗审查。投影表方案的问题不在技术，在于**它解决的问题首版并不存在**。

| 投影表提供的 | 情报模块首版是否需要 |
|---|---|
| 跨表 join（事件 × 报道 × 信源） | 否。AIHOT 的 `/api/v1` 已经返回组装好的结构 |
| 按用户/分组过滤 | 否。首版内容对所有登录用户相同 |
| 跨事件聚合统计 | 否。榜单与计数由 AIHOT 算好 |
| 离线可用 | 否。情报是辅助信息，不可用时可降级 |
| 高频查询性能 | 否。日均请求量是"几十次打开页面"级别 |

四条全是否，那 5 张表和 1 个同步器就是纯粹的负债：多一份要维护的 schema、多一处会静默出错的地方、多一个要跟上游对账的数据源。

上一版把它放进 P1 是"以防万一"的思维，不是"按需"的思维。

---

## 2. 挂接的三种形状

"挂接调用"有歧义，三种做法的成本和风险差一个数量级：

| 形状 | 数据路径 | 成本 | 判断 |
|---|---|---|---|
| **A. 浏览器直连** | 浏览器 → `http://aihot:3000/api/v1` | 0 代码 | **不可行**，见下 |
| **B. 后端代理** | 浏览器 → New API `/api/intel/*` → AIHOT | 2 个文件 | **推荐** |
| **C. 定时抓取存表** | 后台任务 → 表 → 前端 | 8 个文件 | 首版不做 |

**A 为什么不可行**，三条硬伤：

1. **CORS**。AIHOT 不给你开跨域头，浏览器直接拦。
2. **地址暴露**。要让用户浏览器能访问，AIHOT 就必须有公网地址；而它本来应该只在容器内网。
3. **鉴权断裂**。AIHOT 的 `/api/v1` 是**匿名公开**的。直连等于绕开 New API 的登录态，任何人拿到地址就能看——这与你我商定的"内容仅登录用户可见"的合规策略直接冲突。

所以"挂接"只能是 B：**New API 后端代理，把公开接口收进自己的鉴权后面。**

---

## 3. 最小形态

```
浏览器
  │  GET /api/intel/hot        （带 New API 会话）
  ▼
New API  controller/intel.go    ← UserAuth + 60s 共享缓存 + 3s 超时
  │  GET /api/v1/events?...
  ▼
AIHOT   api 进程（内网）
```

一条链路，中间加三样东西：鉴权、缓存、超时。不加第四样。

---

## 4. 后端

### 4.1 路由：`router/intel-router.go`（新增）

```go
func SetIntelRouter(apiRouter *gin.RouterGroup) {
	intelRoute := apiRouter.Group("/intel")
	intelRoute.Use(middleware.DisableCache(), middleware.UserAuth())
	{
		intelRoute.GET("/hot", controller.GetIntelHot)
		intelRoute.GET("/events/:id", controller.GetIntelEvent)
		intelRoute.GET("/editions", controller.ListIntelEditions)
		intelRoute.GET("/editions/:kind/:period", controller.GetIntelEdition)
		intelRoute.GET("/status", controller.GetIntelStatus)
	}
}
```

五条路由，五条**显式白名单**。不用 `intelRoute.Any("/*path", proxy)`。

区别在于：通配代理写起来省 4 行，但它会把 AIHOT 的**每一个**路径都暴露给 New API 的登录用户——包括 `/admin`、`/admin/api/*`、`/openapi-v1.json`、`/api/site/*`（站内自用接口）。

**`/admin` 是必须单独讲的**：AIHOT 的后台用单一管理员密码登录，能做改信源、改提示词与门槛、看付费回执、手动重投任务。这层能力不该落到 New API 的普通用户手里，哪怕他们已登录。

### 4.2 控制器：`controller/intel.go`（新增）

一个文件，五个 handler，共用一段转发逻辑。

职责边界要划清：**它只做四件事，不做第五件**。

| 做 | 说明 |
|---|---|
| 组装上游 URL | 从白名单常量拼，不接受用户传入路径 |
| 加内部密钥 | `X-Internal-Key`，值从环境变量读 |
| 查缓存 / 回填缓存 | 60 秒，key 为 `method + path + query` |
| 超时与降级 | 3 秒不返回就回缓存或降级响应 |

| 不做 | 理由 |
|---|---|
| 不做字段转换 | 转换层放前端 `types.ts`。Go 侧一改就要重新编译部署，前端一改只是发版 |
| 不写入任何表 | 一旦写了，就回到了投影方案的复杂度 |
| 不代理非白名单路径 | 见 4.1 |
| 不把响应里的 `Set-Cookie` 透传 | 那是 AIHOT 的会话，与 New API 无关，透传会造成两个会话域混淆 |
| 不记录到 `logs` 表 | 情报请求不消耗额度，进计费日志会污染用量统计 |

**缓存必须共享，不能按用户。** 内容是公共的，按用户缓存会让内存占用随在线人数线性增长。key 里不含 user id。

**降级的形状**：

```
上游超时 / 连不上 / 非 2xx
  → 有缓存：200 + 缓存数据 + degraded: true
  → 无缓存：200 + 空结构 + degraded: true
```

不返回 5xx。理由：情报是辅助信息，控制台其他部分必须照常可用。前端拿到 `degraded: true` 显示顶部细条，不弹窗、不阻断。

### 4.3 挂载：`router/api-router.go`（修改 4 行）

接在 `APIDUO:capacity-forecast` 块之后：

```go
		// APIDUO:capacity-forecast:END
		// APIDUO:intel:BEGIN
		// 情报模块：只代理白名单只读路径，源站不可达时降级。
		SetIntelRouter(apiRouter)
		// APIDUO:intel:END
```

这是**唯一需要修改的既有文件**，且只加 3 行有效代码。`model/main.go` 和 `main.go` 都不用动——没有表要迁移，没有后台任务要启动。

### 4.4 配置：两个环境变量

| 变量 | 用途 | 缺失时行为 |
|---|---|---|
| `INTEL_BASE_URL` | AIHOT 内网地址，如 `http://aihot:3001` | 不注册路由，菜单隐藏 |
| `INTEL_INTERNAL_KEY` | 与 AIHOT 约定的共享密钥 | 拒绝启动代理，日志告警 |

**不放进 `option` 表。** 上一版把地址和开关放 option 是为了让管理员能在界面上改；但代理目标是一个部署事实，不该由界面随时改——改错了会让整个模块挂掉，而且排查时容易忽略数据库里的配置。用环境变量，与 `SQL_DSN` 这类同级。

---

## 5. 前端

### 5.1 结构

```
web/src/features/intel/
├── api.ts            // 5 个端点的 fetch 封装
├── types.ts          // AIHOT 响应的类型定义 —— 唯一的适配层
├── hooks.ts          // TanStack Query 封装
└── components/
    ├── rank-row.tsx
    ├── heat-bar.tsx
    └── source-badge.tsx

web/src/routes/_authenticated/intel/
├── index.tsx         → /intel
├── events.$id.tsx    → /intel/events/:id
└── editions.tsx      → /intel/editions
```

比投影方案少一个 `cost-panel.tsx`，原因见 6.3。

### 5.2 适配层放在前端的理由

AIHOT 的 `/api/v1` 字段名会随它自己迭代变（作者明确说"不保证每次同步"）。适配放前端，改一次 `types.ts` 加一个 `mapEvent()` 就能发版；放 Go 侧则要走编译、镜像、重启。

代价：前端要写一层映射函数（约 40 行）。可接受。

### 5.3 空态与降级

| 状态 | 界面 |
|---|---|
| `INTEL_BASE_URL` 未配置 | 菜单项不渲染（与 `nav_app` 的模块开关同一思路） |
| 已配置但无数据 | 空态："尚无情报数据，源站可能在首次采集" |
| `degraded: true` | 顶部琥珀色细条 + 数据正常展示 |
| 请求失败且无缓存 | 空态 + 重试按钮，**不显示红色报错** |

---

## 6. 这个方案的代价（三项，都要认）

### 6.1 每次请求穿透

首版 QPS 极低（用户打开页面才触发），60 秒缓存后真实穿透约每用户每页 1 次。可接受。

但如果将来把情报做成首页卡片、或开放给 Agent 高频拉取，穿透量会上去。那时的信号是 `INTEL_BASE_URL` 上游的 QPS 持续超过 10。

### 6.2 结构耦合

AIHOT 升级改了 `/api/v1` 的字段，前端会白屏或显示空。没有投影表做缓冲。

缓解：把 `types.ts` 的解析写成"宽松模式"——缺字段给默认值，不抛异常；再加一个构建期的接口快照测试（把 `/openapi-v1.json` 存进仓库，CI 里对比字段集）。

### 6.3 **"双评分分歧"做不了**

这是最实质的能力损失，必须明说。

AIHOT 的公开 API 只输出**平均分**（页面显示值），不暴露 `score_1` / `score_2`。而"分歧度"这个我在上一版重点推荐的功能，正依赖这两个原始分数。

三条路，都不理想：

| 方案 | 代价 |
|---|---|
| 改 AIHOT 源码，在 `/api/v1` 加两个字段 | 要维护一个 fork，从此不能直接合并上游更新 |
| 从 AIHOT 的 PG 直读 `content` 表 | 破坏"不碰它数据库"的边界，且表结构无契约 |
| 放弃这个功能 | 原型里的分歧徽标和复核队列一并删掉 |

**首版建议选第三条**。它是锦上添花，不是核心价值。等挂接跑稳、确认有人看之后，再决定要不要为它付 fork 的代价。

同时受影响的还有**生产开销面板**——但这一项能救：

模型调用走 New API，日志本来就有。给 AIHOT 用一个专属 token（`token_name = "intel"`），就能直接对既有 `logs` 表做聚合：

```sql
SELECT model_name, SUM(prompt_tokens), SUM(completion_tokens), SUM(quota)
FROM logs WHERE token_name = 'intel' AND created_at > ?
GROUP BY model_name
```

**零新表，零新任务**，只需要一个只读 handler（`GetIntelUsage`，`AdminAuth`）。这是挂接方案下唯一还成立的原型面板。

---

## 7. 什么时候该回头上投影表

四条触发条件，**任意一条成立**就升级。写下来是为了避免"当初为什么不上表"变成半年后的争论。

| # | 触发信号 | 说明 |
|---|---|---|
| 1 | 需要按用户/分组差异化内容 | 技术组看模型发布、商务组看融资。需要用户维度 join，代理做不到 |
| 2 | 需要跨事件聚合统计 | 比如"本周最活跃的信源""分类分布趋势"。AIHOT 没有这类聚合端点 |
| 3 | 需要历史与离线 | 要看 30 天前的榜单，或源站长期不可用仍要可读 |
| 4 | 上游 QPS 持续 > 10 | 穿透成为瓶颈 |

升级成本可控：投影表是**增量**，不推翻代理层。届时把 `controller/intel.go` 的转发逻辑换成查表，前端一行不改（因为 DTO 在前端）。

这也是为什么第 4.2 节强调"不做字段转换"——保留这个边界，将来换数据源才不用动前端。

---

## 8. 运维面没有减少

"做成独立服务"容易让人以为运维也解耦了。实际不会：

| 项目 | 投影方案 | 挂接方案 |
|---|---|---|
| 进程 | New API + AIHOT（api/worker/web） | **相同** |
| 数据库 | 主库 + AIHOT 的 PG | **相同** |
| 备份 | 两份，两个恢复演练 | **相同** |
| 升级 | 两个项目的节奏 | **相同** |
| 监控 | 两套健康检查 | **相同** |
| 代码 | 8 个新文件 | **少 6 个** |

省下来的是代码量和心智负担，不是运维工作量。

**能省的只有一处**：用同一份 `docker compose` 纳管，让 AIHOT 与 New API 共享网络与重启策略。这样至少"起服务"是一个动作。

---

## 9. 改动清单

### 新增（后端 2 个）

```
router/intel-router.go
controller/intel.go
```

### 新增（前端 8 个）

```
web/src/features/intel/api.ts
web/src/features/intel/types.ts
web/src/features/intel/hooks.ts
web/src/features/intel/components/rank-row.tsx
web/src/features/intel/components/heat-bar.tsx
web/src/features/intel/components/source-badge.tsx
web/src/routes/_authenticated/intel/index.tsx
web/src/routes/_authenticated/intel/events.$id.tsx
web/src/routes/_authenticated/intel/editions.tsx
```

### 修改（4 处）

```
router/api-router.go          +4 行（APIDUO:intel 标记块）
web/src/i18n/locales/*.json   ×7（约 25 个 key，比投影方案少 5 个）
web/src/components/layout/    1 处导航配置
```

**`model/main.go` 与 `main.go` 都不用改。**

### 配置

```
INTEL_BASE_URL=http://aihot:3001
INTEL_INTERNAL_KEY=<random>
```

---

## 10. 安全清单

| 项 | 措施 |
|---|---|
| 代理路径 | 五条显式白名单，**绝不代理 `/admin`、`/admin/api/*`、`/api/site/*`** |
| 用户输入进 URL | 只接受 `id`（数字校验）、`kind`（枚举校验）、`period`（正则校验）；其余 query 只透传白名单键 |
| 内部密钥 | 环境变量，不进数据库、不进日志 |
| 响应头 | 只透传 `Content-Type`、`Cache-Control`、`ETag`；剥离 `Set-Cookie`、`Server`、`X-Powered-By` |
| 内容渲染 | 摘要走 `dompurify`；外链 `rel="noopener noreferrer"` |
| 图片 | 封面走 AIHOT 的图片代理，不由浏览器直连第三方图床 |
| 超时 | 连接 1s，整体 3s。防止上游卡死拖垮 New API 的工作协程 |
| 限流 | 沿用 `apiRouter` 上的 `GlobalAPIRateLimit`，不额外加（情报不消耗额度，无需 CriticalRateLimit） |

---

## 附：两版方案的分工

| 文档 | 回答什么 |
|---|---|
| 《AIHOT-集成方案》第 1–3、7–9 节 | 要不要做、成本、风险、分期。**仍然有效** |
| 《AIHOT-集成方案》第 4 节（5 张投影表） | 升级路径的设计稿。**首版不实现** |
| 本文 | 首版落到哪些文件、怎么代理、代价是什么 |

两处事实边界不变：

1. 本仓库是裁剪快照，中间件函数名依据 `api-router.go` 的调用点推定，落地前对照完整仓库核对。
2. AIHOT 的 `/api/v1` 精确参数未核对（仓库无 `openapi-v1.json`）。代理层不依赖具体参数名，只做透传，所以这块风险低——这恰好是挂接方案相对投影方案的一个隐性优势：**参数变了不用改后端**。
