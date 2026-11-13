# AIHOT × apidup 集成方案

> 状态：待评审
> 版本：v0.1 · 2026-11-10
> 结论摘要：**不集成 AIHOT 的代码，吸收 AIHOT 的编辑部。**
> 三段式架构，5 张只读投影表，P0 + P1 合计约 10 人日。代码层复用率低于 10%，方法论与提示词复用率接近 100%。

**配套交付物**：`prototype.html` — 可点击的产品原型，覆盖热点榜、事件详情、出刊三页。

| 操作 | 说明 |
|---|---|
| 打开 `prototype.html` | 默认进入热点页 |
| 点击左侧「情报」 | 回到热点页 |
| 点击「热点 / 出刊」标签 | 在两个视图间切换 |
| 点击任意热点行 | 进入事件详情，可切换时间线排序 |
| 右上角「同步」按钮 | 演示同步中的按钮态与状态文案更新 |
| 右下角圆形按钮 | 切换深浅色 |
| URL 追加 `?degraded=1` | 演示源站不可达时的降级态 |
| URL 追加 `?theme=light` | 直接以浅色打开 |

原型数据为构造的示例数据，用于验证信息密度与层级，不代表真实资讯。

---

## 0. 结论先行

三条判断，按重要性排序：

1. **AIHOT 的网站壳对 apidup 没有价值，它的流水线有价值。** 网站、SSR、主题页、分享图这些是"给一个独立内容站用的"，apidup 已经有一套成熟控制台。真正稀缺的是那套"预筛 → 独立两次评分 → 结构化 → 写作 → 事件聚簇 → 事件级热度 → 自动出刊"的编排，以及配着它的全套中文提示词和门槛参数。这部分是作者在 AI 领域跑了百万月活调出来的，自研需要反复试错。

2. **两者技术栈冲突是结构性的，不是工程量问题。** Node 24 + Fastify + PG 17 + pg-boss + React Router SSR，对上 Go 1.26 + Gin + 三库兼容 + React 19/Rsbuild CSR。强行重写等于重做一个项目，且从此无法合并上游更新。正确做法是**进程隔离 + 单向数据投影**。

3. **集成方向应该是双向的，而双向里 apidup 是获利更多的一方。** AIHOT 需要模型，apidup 就是模型入口；apidup 需要内容，AIHOT 产出内容。这条回路一接上，AIHOT 的每一分 token 消耗都落在 apidup 自己的日志里，成为可归因、可优化、可 A/B 的成本数据。**这是纯部署 AIHOT 永远拿不到的东西。**

---

## 1. 先拆清楚：AIHOT 到底是什么

### 1.1 剥离网站壳之后的本质

```
f(信源集合, 行业 KnowHow 提示词, 模型池) → 每日经过筛选/聚簇/排序/撰写的中文情报流
```

它不是 CMS，不是 RSS 阅读器，不是通用采集器。它是一个**有明确取舍标准的编辑部**，把"什么值得看"编码成了可执行的提示词和门槛。

这个函数有三个输入。信源是数据，模型是算力，**提示词和门槛是 KnowHow**——而 KnowHow 恰恰是自研最难补齐的部分。

### 1.2 可复用资产的三层分级

| 层 | 内容 | 复用方式 | 价值 | 成本 |
|---|---|---|---|---|
| **方法论** | 双评分防偷懒、信源分级门槛（T1=60 / T1.5=65 / T2=76）、事件级热度（48h 内每独立来源计一次、24h 减半）、同题去重与"新信息"判定、日报三段式编排、付费请求回执、预算熔断 | 直接吸收，写进设计 | ★★★★★ | 0 |
| **提示词** | `prefilter` / `selection-score` / `structure` / `content-understanding` / `summarize-*` / `group-*` / `story-digest` / `report-period` / `mend-wording`，中文语境调优，含反幻觉约束（"防止模型把原文没提到的公司写进标题"） | 复制进配置，随行业改写 | ★★★★★ | 低（改词） |
| **代码** | Node 24 + Fastify + PG17 + pg-boss + React Router SSR | 不复用 | ★ | 高（重写） |

**代码层复用率 < 10%。** 这不是保守估计，是把依赖逐项对照后的结论（见 1.3）。

### 1.3 与 apidup 的技术错配清单

| 维度 | AIHOT | apidup | 冲突 |
|---|---|---|---|
| 语言/运行时 | Node.js 24 + TypeScript | Go 1.26 | 不可调和 |
| Web 框架 | Fastify + React Router SSR | Gin + React 19 SPA（Rsbuild 2） | 不可调和 |
| 数据库 | PostgreSQL 17 专用 | SQLite / MySQL / PostgreSQL 三库必支持 | 高 |
| 任务队列 | pg-boss（寄生在 PG 的队列） | gopool + jsplugin 轮询 | 高 |
| 迁移体系 | 文件名排序的 SQL 迁移，一条语句一文件 | GORM AutoMigrate | 中 |
| 渲染模式 | 服务端渲染（首屏直出） | 客户端渲染 | 中 |
| 认证 | 单一管理员密码 | 会话 / API Token / PAT / JWT / WebAuthn / OAuth / Casbin | 高 |
| 计费 | 无（只有预算上限） | 完整额度、倍率、分组、日志 | 高 |

唯一低冲突项是 **PostgreSQL**——apidup 支持 PG，但这不构成合并理由，因为 AIHOT 用了 PG 的向量与全文能力，其余两库无法承接。

---

## 2. 为什么值得集成

### 2.1 对 apidup 的三个增益

**① 获得一条可自我验证的模型质量基准线。**
AIHOT 的"同一份标准独立打两次分"原本是为了防止单次评分偷懒。放进 apidup 后它变成一个副产品：**把两次打分调度到两个不同渠道**，两次分数的分歧度就成了这两个模型在该任务上的判别力差异。

- 分歧率低 → 两个模型判断一致，这一条可靠
- 分歧率高 → 模型拿不准，这一条值得人工看

累积下来，`SelectBench` 的评测报告就是一份**用真实业务语料做的渠道选型报告**。这比跑公开 benchmark 有说服力，因为语料是你自己的。

**② 把网关从"成本中心"变成"能自证价值的地方"。**
纯网关的困境是：用户只看到账单，看不到产出。加一个每天更新的情报流，用户每天有一个理由打开控制台——**这是留存钩子，不是功能堆砌**。而且它消耗的正是你自己渠道的额度，等于内部消化产能。

**③ 提示词缓存命中率天然极高。**
AIHOT 的评分提示词固定且长（通常 2–4k tokens），每条资料只变正文。这意味着在支持 prompt caching 的上游（DeepSeek、Claude、Gemini 皆有）上，**缓存命中率可达 80% 以上，成本降到约 1/5**。这是一个只有"把调用收进网关"才能拿到的优化——AIHOT 独立部署时，它自己配置的 base_url 拿不到这层缓存统计。

### 2.2 反向增益：apidup 给 AIHOT 的

| 能力 | AIHOT 自己做要付的代价 | 走 apidup 后 |
|---|---|---|
| 多渠道容灾 | 自己写重试与降级 | 渠道自动重试、优先级、权重 |
| 模型热切换 | 改 `.env` 重启进程 | 控制台改分组，不重启 |
| 成本归因 | 只有总预算上限 | 按 model / channel / 环节三维归因 |
| 倍率与配额 | 无 | 完整 |
| 审计 | 只有回执表 | 完整请求日志、可追溯到 request_id |

**注意**：AIHOT 是 OpenAI 兼容接口的消费者，接 apidup 只需改 `LLM_BASE_URL` + `LLM_API_KEY` 两个环境变量，**零代码改动**。这是整个方案里性价比最高的一步。

### 2.3 对抗审查：这套东西可能不成立的情形

主动列出三个反面，请在评审时逐条确认：

**① 内容合规是真实风险，不是形式风险。**
聚合外媒内容 + 模型改写 + 面向公众发布，涉及信息网络传播与生成式服务备案。AIHOT 自己用 `site_fulltext` 开关规避（默认只展示摘要、链接回原文）。**对策**：apidup 侧默认只存"标题 + 摘要 + 原文链接"，全文永不落库；模块默认仅登录用户可见，不提供匿名公开访问；不生成"news"栏目对外 SEO。若要做公开站，必须先完成备案与内容安全能力接入。

**② "有人看"是假设，不是事实。**
情报流的价值取决于受众是否是这个行业的从业者。如果 apidup 的用户主要是"调 API 的开发者"，那 AI 行业情报对他们是有用的；如果用户是泛化的 API 转售客户，他们可能只关心价格和可用性。**对策**：P0 只做内部可见，用 4 周的真实打开率决定是否投 P1。

**③ 双系统运维成本被低估。**
多一套 Node 进程、一个 PG 实例、一套迁移与备份。**对策**：纳入同一份 docker compose，apidup 启动时探测 AIHOT 健康度，不可达时前端降级为"情报暂不可用"而不是报错；投影表可随时 `TRUNCATE` 重建，不做双向写。

---

## 3. 架构设计

### 3.1 三段式与边界

```
┌─────────────────────────────────────────────────────────────────┐
│  L1 生产层：AIHOT 引擎（Node 24，独立进程组，独立 PG）           │
│  api(3001) · worker · web(3000)                                 │
│  职责：采集 / 判重 / 预筛 / 双评分 / 结构化 / 写作 / 聚簇 / 出刊 │
│  不做：认证、计费、多租户                                        │
└───────────────────────────┬─────────────────────────────────────┘
                            │ 只读 GET /api/v1/*（内网，带共享密钥）
                            │ 单向拉取，游标增量，幂等 upsert
┌───────────────────────────▼─────────────────────────────────────┐
│  L2 投影层：apidup Go 侧（Gin + GORM，主库）                     │
│  5 张只读投影表 · 同步任务 · 5 个只读端点 · 权限 · 缓存          │
│  职责：把外部内容翻译成 apidup 的权限与计费语境                  │
│  不做：内容加工、模型调用、事件计算                              │
└───────────────────────────┬─────────────────────────────────────┘
                            │ REST（既有 UserAuth / AdminAuth）
┌───────────────────────────▼─────────────────────────────────────┐
│  L3 消费层：apidup 前端「情报」模块（React 19 + Tailwind 4）     │
│  热点榜 · 事件详情 · 日报 · 成本与健康面板                       │
└─────────────────────────────────────────────────────────────────┘

           ▲                                     │
           │ 模型调用（OpenAI 兼容，反向）        │
           └──────────────  apidup 网关  ◄───────┘
                     LLM_BASE_URL 指向 apidup
```

**边界铁律三条：**

1. **内容只朝一个方向流。** AIHOT 是唯一写方，apidup 只读。不允许在 apidup 侧编辑情报内容——需要改就去 AIHOT 后台改，等下一次同步。这条规则省掉整个双向冲突处理。
2. **投影表可丢弃。** 任何一张投影表都可以 `TRUNCATE` 后从 AIHOT 全量重建。这条规则保证 AIHOT 升级、迁移、甚至换实现时，apidup 侧不受污染。
3. **apidup 不承内容生产的算力。** 情报的模型调用发生在 AIHOT 进程里，apidup 只作为出口。这条规则保证 apidup 的请求链路不被长耗时批处理拖累。

### 3.2 为什么 AIHOT 不进主库

五个理由，从强到弱：

1. **AIHOT 的迁移体系会与 GORM AutoMigrate 打架。** 它按文件名排序执行单语句迁移，apidup 用 AutoMigrate 对结构做声明式同步。两套体系共存于一个 schema，任何一方都可能把另一方的对象当作"多余的表"处理。
2. **它的表名空间不设前缀。** `content`、`events`、`reports`、`sources` 这些名字在 apidup 的库里是危险的通用词。
3. **它的表数量与演化速度远超 apidup 需要。** 全部迁移到 `0055+`，包含向量、全文索引、MCV 统计对象。apidup 只需要其中 5 个视图的信息。
4. **跨库兼容性。** AIHOT 依赖 PG 专有类型与索引。apidup 必须同时支持 SQLite 与 MySQL。把它的表放进主库等于放弃三库兼容。
5. **可逆性。** 投影表方案下，放弃集成只需删 5 张表和 1 个前端模块。合并 schema 后，回退需要一次数据迁移。

### 3.3 数据流与进程归属

| 环节 | 进程 | 存储 | 触发方式 | 失败处理 |
|---|---|---|---|---|
| 采集信源 | AIHOT worker | PG（AIHOT） | 定时（频率按产出自动调整） | 重试 + 回执去重 |
| 预筛 / 双评分 / 结构化 / 写作 | AIHOT worker | PG（AIHOT） | 队列 | 停在"全部动态"，10 分钟告警，后台可重投 |
| 事件归组 / 热度 | AIHOT worker | PG（AIHOT） | 队列 + 定时重算 | 归组需等确认，不自动放行 |
| 出刊（日/周/月） | AIHOT worker | PG（AIHOT） | 定时，每半小时检查补刊 | 缺期告警 |
| **投影同步** | **apidup 后台 goroutine** | **apidup 主库** | **每 5 分钟 + 手动触发** | **指数退避，连续失败降级为只读缓存** |
| 读取与渲染 | apidup api | apidup 主库 | HTTP | 缓存 60s |

**新增到 apidup 的只有一行**——投影同步任务。这也是为什么这个方案轻。

### 3.4 幂等与失败恢复

同步任务的设计要点：

```
for each resource in [events, items, editions]:
    cursor = sync_state[resource].cursor          # 上次成功的 max(updated_at)
    loop:
        batch = GET /api/v1/{resource}?since=cursor&limit=200
        if batch.empty: break
        upsert(batch)                              # 以远端 id 为唯一键
        cursor = max(batch.updated_at)
        commit(sync_state[resource].cursor)        # 与 upsert 同一事务
```

四个关键决定：

- **游标用 `updated_at` 而不是 `published_at`。** 热度会随时间衰减重算，事件行会被更新。只拉新发布的内容会漏掉热度变化。
- **游标与数据同事务提交。** 崩溃时要么全成功要么全回退，不会出现"游标前进了但数据没写"。
- **upsert 用 `clause.OnConflict`，不用 `ON CONFLICT` 原生 SQL。** GORM 会为三种数据库生成各自的语法（SQLite/MySQL 用 `ON CONFLICT`/`ON DUPLICATE KEY`，PG 用 `ON CONFLICT`）。这是三库兼容的关键。
- **不做物理删除。** AIHOT 有"撤回"语义（报道失去公开资格），同步时按远端返回的 `visible` 字段软标记。投影侧永不 `DELETE`，避免"远端临时不可读"被误判为删除。

### 3.5 安全边界

| 面 | 措施 |
|---|---|
| 内网通信 | AIHOT 只监听容器内网；apidup 访问它用服务名，不经公网 |
| 接口鉴权 | 共享密钥放 `X-Internal-Key` 头，值由 apidup 启动时生成并注入；AIHOT 侧不允许匿名读 `/api/v1` 之外的路径 |
| 出站调用 | AIHOT 的 `LLM_BASE_URL` 指向 apidup 内网地址，走内部用户，不出公网 |
| 内容渲染 | 前端渲染摘要一律走现有 `dompurify`；不允许注入 HTML |
| 外链 | 原文链接统一加 `rel="noopener noreferrer"` + `target="_blank"`，经既有外链中间页（若存在） |
| 图片 | 封面图走 AIHOT 的图片代理，apidup 侧不直连第三方图床，避免 IP 泄露与防盗链 |

---

## 4. 数据模型：5 张投影表

### 4.1 表设计

> 以下为设计示意，字段类型按三库兼容取值（不用 `jsonb`、不用数组、不用 `enum`）。

**`aihot_sources`** —— 信源镜像，用于"我们在看哪些源"

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | int PK | 远端 id |
| `name` | varchar(128) | 信源名 |
| `kind` | varchar(16) | rss / web / json / x / wechat / push |
| `tier` | varchar(8) | T1 / T1_5 / T2 |
| `category` | varchar(64) | 类别（可空，影响门槛） |
| `enabled` | bool | 是否启用 |
| `synced_at` | bigint | 同步时间戳 |

**`aihot_events`** —— 事件（核心表，热点榜按它排）

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | int PK | 远端事件 id |
| `title` | varchar(512) | 事件标题 |
| `digest` | text | 事件综述（模型生成） |
| `heat` | int | 热度分 |
| `source_count` | int | 48h 内独立来源数 |
| `item_count` | int | 关联报道数 |
| `categories` | text | JSON 数组字符串 |
| `trend` | varchar(16) | new / rising / steady |
| `cover_url` | varchar(1024) | 封面 |
| `first_seen_at` | bigint | 事件起点 |
| `last_seen_at` | bigint | 最近进展 |
| `visible` | bool | 是否可公开阅读 |
| `synced_at` | bigint | |

索引：`(heat DESC)`、`(last_seen_at DESC)`、`(trend)`、`(visible)`

**`aihot_items`** —— 报道（单篇文章）

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | int PK | 远端 id |
| `event_id` | int index | 所属事件（可空） |
| `title_zh` | varchar(512) | 中文标题 |
| `summary` | text | 答案先行的摘要 |
| `reason` | text | 推荐理由 |
| `url` | varchar(1024) | 原文链接 |
| `source_id` | int | 信源 id |
| `source_name` | varchar(128) | 冗余，省 join |
| `source_tier` | varchar(8) | T1 / T1_5 / T2 |
| `score_1` / `score_2` | int | 两次独立评分 |
| `score_avg` | int | 平均分（向下取整，页面显示值） |
| `category` | varchar(64) | 分类 |
| `tags` | text | JSON 数组字符串 |
| `lang` | varchar(8) | 原文语言 |
| `is_representative` | bool | 是否代表报道（机器出口每条新闻只出一条） |
| `published_at` | bigint | 原文发布时间 |
| `selected_at` | bigint | 入选确认时间 |
| `visible` | bool | |
| `synced_at` | bigint | |

索引：`(event_id)`、`(selected_at DESC)`、`(score_avg DESC)`、`(visible, selected_at)`

**`aihot_editions`** —— 日报 / 周报 / 月报

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | int PK | |
| `kind` | varchar(16) | daily / weekly / monthly |
| `period_key` | varchar(16) | `2026-11-10` / `2026-W45` / `2026-11` |
| `headline_item_id` | int | 头条 |
| `body` | text | JSON：`{headline, highlights[], briefs[]}` |
| `summary` | text | 总述（周报月报有，日报为头条导语） |
| `published_at` | bigint | 出刊时间 |

唯一索引：`(kind, period_key)`

**`aihot_sync_state`** —— 同步游标与健康度

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | int PK | |
| `resource` | varchar(32) | events / items / editions / sources |
| `cursor` | varchar(64) | 上次成功游标 |
| `last_success_at` | bigint | |
| `last_error` | text | |
| `consecutive_failures` | int | 连续失败次数，达阈值熔断 |

### 4.2 同步协议

| 远端 | 用途 |
|---|---|
| `GET /api/v1/events?since=&limit=` | 事件增量 |
| `GET /api/v1/items?since=&limit=` | 报道增量 |
| `GET /api/v1/daily/{date}` · `/weekly/` · `/monthly/` | 刊物 |
| `GET /api/v1/sources` | 全量（量小，每次覆盖） |

**说明**：以上路径以 AIHOT `/openapi-v1.json` 为准。落地前需拉取该文件逐条核对参数名与分页语义——这是 P0 的验收项之一，不在此处假定。

### 4.3 三个容易踩的坑

1. **`score_avg` 是"两次平均向下取整"，不是四舍五入。** 页面显示值必须与 AIHOT 一致，否则同一篇文章在两处显示 78 和 79。
2. **`is_representative` 决定"一条新闻只出一条"。** 事件下可能挂 10 篇报道，机器出口只出代表报道。热点榜若按 items 聚合会重复计数，必须走 events。
3. **热度是事件属性，不是文章属性之和。** 不能由 items 的分数求和推导，必须直接取事件表的 `heat`。

---

## 5. 接口契约

### 5.1 后端端点（5 个，全部只读）

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/api/aihot/hot` | `UserAuth` | 热点事件榜，参数 `limit`、`window`（24h/7d）、`category` |
| GET | `/api/aihot/events/:id` | `UserAuth` | 事件详情 + 报道时间线，参数 `order`（latest/earliest） |
| GET | `/api/aihot/editions` | `UserAuth` | 刊物列表，参数 `kind`、`limit` |
| GET | `/api/aihot/editions/:kind/:period` | `UserAuth` | 单期刊物 |
| POST | `/api/aihot/sync` | `RootAuth` | 手动触发同步，返回本次拉取条数 |

**权限设计说明**：读接口用 `UserAuth` 而非 `AdminAuth`，因为这是给全体用户的产品功能，不是运营后台。写接口（同步）用 `RootAuth`，与 `nav_apps` 的编辑权限同级——都是全局配置类操作。

**错误语义**：AIHOT 不可达时，读接口返回 **200 + 缓存数据 + `degraded: true`**，而不是 5xx。理由：情报是辅助信息，不该阻断控制台主流程。

### 5.2 缓存

| 端点 | 缓存 | 失效 |
|---|---|---|
| `/hot` | 60s 内存缓存 | 同步任务完成后主动清 |
| `/events/:id` | 300s | 同上 |
| `/editions` | 600s | 同上 |

用 apidup 既有的内存缓存层，不引入新依赖。

---

## 6. 前端：情报模块

### 6.1 为什么不复用 `nav_app` 的 iframe

`nav_app` 已经支持 `iframe` 挂外部应用，看起来可以直接把 AIHOT 站点嵌进来。不建议，三个原因：

1. **视觉割裂。** AIHOT 是浅色内容站风格，apidup 是深色控制台。iframe 里外两套主题，切换深浅色不联动。
2. **权限不透明。** iframe 内的内容无法按 apidup 的用户分组做差异化。
3. **价值点丢失。** 混排后的"成本面板"需要 apidup 自己的数据，iframe 里拿不到。

`nav_app` 的 iframe 能力保留给"完全无关的第三方工具"，情报模块走原生页面。

### 6.2 信息架构

```
情报（一级菜单，位于「日志」之后）
├── 热点        /intel            事件级热度榜 + 今日要闻 + 成本面板
├── 事件详情    /intel/events/:id  综述 + 时间线 + 来源分布 + 报道列表
└── 日报        /intel/editions    日/周/月刊物，按日期翻页
```

**三个页面，不多做。** 主题页、搜索、收藏这些 AIHOT 有，但首版不做——先把"每天来看一眼"这条路径跑通。

### 6.3 关键交互

| 交互 | 行为 |
|---|---|
| 点击热点行 | 进事件详情，不新开窗口（内部导航） |
| 时间线排序 | 右上角切换"最新在前 / 最早在前"，URL 带 `?order=`，可分享 |
| 日报翻页 | 左右箭头 + 日期选择器，到边界自动禁用 |
| 同步状态 | 页头常驻"最近同步 X 分钟前"，超过 30 分钟变琥珀色，超过 2 小时变红 |
| 降级提示 | `degraded: true` 时顶部细条提示"数据可能延迟"，不弹窗 |

### 6.4 差异化设计：「分歧待复核」

这是本方案唯一一个 AIHOT 没有、只有集成后才有意义的功能。

两次评分的分歧度 = `|score_1 - score_2|`。

- `≥ 25` 分：两个模型判断严重不一致，这条大概率是边界案例
- `10–24` 分：轻微分歧，正常
- `< 10` 分：一致，可信

在热点榜和事件页给高分歧条目一个琥珀色标记，点击筛选出全部高分歧条目。**用途有两个**：给编辑做人工复核队列；给运营看"哪个环节的模型判断不稳"。

如果双评分走的是两个不同渠道，这个指标还顺带回答了"哪个模型更接近人的判断"。

---

## 7. 成本模型

### 7.1 每篇文章的调用序列

| 环节 | 次数 | 触发条件 | 输入规模 |
|---|---|---|---|
| 预筛 `prefilter` | 1 | 全部新资料 | 中（含行业定义） |
| 评分 `selection-score` | **2** | 通过预筛 | 大（标准全文，2–4k） |
| 结构化 `structure` | 1 | 与评分并行 | 中 |
| 写作 `content-understanding` | 1 | 入选或接近入选（约 40%） | 大 |
| 归组 `group-*` | 1 | 分数过线 | 中 |
| 归组复核 `group-review` | 0.3 | 拿不准时 | 小 |
| 翻译 | 0.5 | 外文占比 | 大 |
| 综述 `story-digest` | 0.1 | 按事件摊 | 中 |
| **合计** | **≈ 5.5 次/篇** | | |

### 7.2 日成本估算

以每日 200 篇新资料、平均 1500 input + 400 output tokens 计：

```
调用次数  ≈ 200 × 5.5           = 1,100 次
input     ≈ 1,100 × 1,500       = 1.65M tokens
output    ≈ 1,100 × 400         = 0.44M tokens
```

折算金额 = `input × 渠道input倍率 + output × 渠道output倍率`，直接读 apidup 自己的倍率表即可，无需外部价格。

**量级判断**：这是一个**廉价应用**。即使按高价模型全量跑，日成本也在两位数人民币量级。它的成本约束不是钱，是**调用延迟与并发**——1100 次调用集中在出刊前的窗口里。

### 7.3 三个降本杠杆

| 杠杆 | 做法 | 预期效果 |
|---|---|---|
| **提示词缓存** | 评分提示词固定且长，走支持 cache 的上游，命中率 80%+ | 成本降至约 1/5 |
| **预筛降级** | 预筛只判断"是不是本行业"，不需要推理能力，路由到最便宜的模型 | 单环节省 60–80% |
| **双评分分渠道** | 第一次用便宜模型，第二次用贵模型；或反之，用 SelectBench 数据定 | 可测、可调 |

**注意**：`LLM_REASONING_TOKENS` 需要为推理模型预留输出额度，否则会截断。这是 AIHOT README 明确点出的坑，配置时不要漏。

---

## 8. 分期计划

### P0 · 打通回路（3–5 人日）

| 事项 | 产出 |
|---|---|
| AIHOT 单机部署（docker compose，内置 18 个示范源） | 能跑出日报的独立站 |
| apidup 建内部用户 + 专属 token + 独立分组 | 内部调用不计入用户额度 |
| AIHOT `LLM_BASE_URL` 指向 apidup，`LLM_API_KEY` 用专属 token | 模型调用走 apidup |
| 验证：apidup 日志能按 model / channel 看到 AIHOT 的调用 | 成本可见 |

**验收标准**：连续 3 天出刊，apidup 日志里能看到完整调用链，日成本可计算。

### P1 · 投影与界面（5–8 人日）

| 事项 | 产出 |
|---|---|
| 拉取 `/openapi-v1.json`，核对参数与分页语义 | 接口对照表 |
| 5 张投影表 + GORM 模型 | 迁移可跑（三库） |
| 同步任务（游标 + 幂等 upsert + 退避） | 每 5 分钟增量 |
| 5 个只读端点 + 缓存 + 降级 | Postman 可验 |
| 情报模块三个页面 | 可用 |
| 成本与健康面板 | 可用 |

**验收标准**：AIHOT 停止后，apidup 情报页仍能展示缓存数据并正确降级；投影表 `TRUNCATE` 后能自动全量重建。

### P2 · 价值验证后再投入（可选）

- 事件订阅推送（热点进 IM / 邮件）
- 按用户分组差异化内容（技术组看模型发布，商务组看融资）
- 高分歧条目的复核队列
- 公开只读出口（**需先解决合规**）

### 明确不做

| 事项 | 理由 |
|---|---|
| Go 全量重写流水线 | 失去上游更新能力，工作量为 P1 的 5 倍以上 |
| 与 AIHOT 共用主库 | 迁移体系与三库兼容都会破 |
| apidup 侧编辑情报内容 | 破坏单向流，引入双向冲突 |
| 主题页 / 搜索 / 收藏 | 首版不做，先把主路径跑通 |
| 用 AIHOT 的名字与 Logo | MIT 不覆盖品牌，必须换站名 |

---

## 9. 风险矩阵

| 风险 | 概率 | 影响 | 对策 | 责任期 |
|---|---|---|---|---|
| 内容合规（聚合 + 改写 + 公开） | 高 | 高 | 只存标题摘要链接；默认仅登录可见；全文开关关闭；不做公开 SEO | 全程 |
| AIHOT 上游 API 漂移 | 中 | 中 | 锁定版本 tag；只依赖 `/api/v1`（作者声明的稳定面）；契约测试 | P1 |
| 投影数据与源不一致 | 中 | 低 | 每日一次全量对账（按 id 集合求差） | P1 |
| 双系统运维负担 | 高 | 中 | 同一 compose 纳管；健康探测 + 前端降级；投影表可丢弃 | P1 |
| 品牌与许可误用 | 低 | 中 | 换站名与 Logo；NOTICE 中的字体许可单独核对 | P0 |
| 价值不达预期（无人看） | 中 | 高 | P0 后先内部试跑 4 周，用打开率决定是否投 P1 | P0 后 |
| 模型输出不合格式导致积压 | 中 | 中 | 沿用 AIHOT 的 10 分钟告警与后台重投；评分模型优先选结构化输出稳定的 | 全程 |

---

## 10. 待你决策的 5 件事

1. **可见范围**：情报模块给全员、指定分组、还是仅管理员？这直接决定合规风险等级。
2. **首发行业**：先用内置的 18 个 AI 信源跑通，还是直接换成你的目标行业？前者零配置，后者要重写提示词与门槛。
3. **AIHOT 部署位置**：同机容器（省事，共享资源），还是独立机器（隔离，好扩容）？
4. **能否接受"多一个 PG 实例"**：AIHOT 必须 PG，apidup 主库可能是 SQLite/MySQL。这是唯一新增的基础设施。
5. **P0 之后是否用真实数据决策 P1**：如果你倾向"先看到效果再投"，P1 的 5–8 人日应该在 4 周试跑之后再启动。

---

## 附录 A：提示词资产清单（需从 AIHOT 复制）

| 文件 | 作用 | 改写工作量 |
|---|---|---|
| `industry/prompts/prefilter.md` | 是不是本行业的事 | 高（换行业定义） |
| `industry/prompts/selection-score.md` | 评分标准 0–100 | **最高**（KnowHow 所在） |
| `industry/prompts/structure.md` | 分类、标签、主体、事实抽取 | 中 |
| `industry/prompts/content-understanding.md` | 中文标题、答案先行摘要、推荐理由 | 中 |
| `industry/prompts/summarize-*.md` | 未入选条目的简短摘要 | 低 |
| `industry/prompts/group-*.md` | 事件关系判断 | 低 |
| `industry/prompts/story-digest.md` | 事件综述 | 中 |
| `industry/prompts/report-period*.md` | 周报月报总述与导读 | 低 |
| `industry/prompts/mend-wording.md` | 用词修正 | 低 |
| `industry/selection.ts` | 门槛参数 | 需用样本重新校准 |

## 附录 B：P0 配置对照

| AIHOT 环境变量 | 应设为 | 说明 |
|---|---|---|
| `LLM_BASE_URL` | `http://apidup:3000/v1` | 内网地址 |
| `LLM_API_KEY` | 内部用户专属 token | 不要用管理员 token |
| `LLM_MODEL` | 渠道里已配的模型名 | |
| `LLM_REASONING_TOKENS` | 推理模型必填 | 否则输出被截断 |
| `ADMIN_PASSWORD` | 强密码 | 后台入口 |
| `COLLECT_ENABLED` | `true` | 采集总闸 |
| `MODEL_CALLS_ENABLED` | `true` | 模型总闸 |
| `GROUP_REVIEW_MODEL` | 可与主模型不同 | 归组复核单独换模型 |

## 附录 C：参考

- AIHOT 仓库：`https://github.com/KKKKhazix/AIHOT`（MIT，品牌不在许可范围）
- 架构：`docs/architecture.md` · 精选：`docs/selection.md` · 定制：`docs/customize.md`
- 公开契约：站点 `/openapi-v1.json`（P0 需核对）
- apidup 扩展位：`router/api-router.go` 中的 `APIDUO:*:BEGIN/END` 标记块，情报端点应新增独立标记块 `APIDUO:intel`
