# T1 判据（不改写需求，逐字对应）

## 需求原文

> 宽表加 `primary_model` / `model_count` / `model_switched_at`，加一条「换模型」预警，模型级失败率与延迟（分母有了，错误数才有意义），权限墙单独成类预警

## 映射表

| # | 需求条目                                     | 判据                              | 覆盖 |
| - | ---------------------------------------- | ------------------------------- | -- |
| 1 | 宽表记录主用模型与模型数量                            | `TestScanUserModels`            | 是  |
| 2 | 换模型能被识别（不能把"首次建立画像"当切换）                  | `TestModelSwitch`               | 是  |
| 3 | 全局模型占比可查（消耗 + 用户数）                       | `TestScanModelBreakdown`        | 是  |
| 4 | 换模型要进预警队列                                | `TestModelSwitchAlert`          | 是  |
| 5 | 权限墙（model_not_found / access_denied）单独成类 | **uncovered** — 需先确认错误码语义，见交付说明 | 否  |

## 判据命令

```bash
go test ./model -run 'TestScanUserModels|TestScanModelBreakdown' -count=1
go test ./service -run 'TestModelSwitch|TestModelSwitchAlert' -count=1
```


---

# 第二批：页面重组

| # | 需求 | 判据 | 覆盖 |
|---|---|---|---|
| 6 | sk 分析下放给普通用户 | `section-visibility.test.ts` 4 条 + 权限矩阵 | 是 |
| 7 | /growth 压到 3 个页签 | bsk 快照：3 个 tab | 是 |
| 8 | 客户档案改抽屉（不占页签） | bsk：点分层表行 → dialog 打开 | 是 |
| 9 | 规则移出页签，走右上角 | bsk：点「规则」→ 抽屉打开 | 是 |
| 10 | retention 迁到 growth | — | **未做**，排入下一批 |

---

# 第三批：额度墙

| # | 需求 | 判据 | 覆盖 |
|---|---|---|---|
| 11 | 额度不足的错误要给出路 | `TestQuotaErrorCarriesTopUp` | 是 |
| 12 | 普通错误不能被污染 | `TestNormalErrorHasNoTopUp` | 是 |
| 13 | 未显式传 option 也要有兜底（9 处拒绝点靠人记必漏） | `TestQuotaErrorAutoCarriesTopUp` | 是 |
| 14 | 真实请求验证 | E2E：零余额 403 带 topup；成功/参数错误不带 | 是 |
| 15 | 给出「还能用几天」 | **uncovered** — 需查聚合表，请求路径上不值得 | 否 |

---

# 第二批补完：retention 迁移

| # | 需求 | 判据 | 覆盖 |
|---|---|---|---|
| 16 | 客户余额健康从 dashboard 迁到 growth | bsk：growth 客户页出现「客户余额健康度」 | 是 |
| 17 | dashboard 不再有该区块 | bsk：页签列表无「客户健康」 | 是 |
| 18 | 组件与测试一并迁移 | `vitest run dashboard + growth` 101 绿 | 是 |
| 19 | 充值流程仍可用 | **uncovered** — 需真实点击充值并走支付，本地无支付通道 | 否 |

---

# 第四批（一期）：服务质量台 · 错 + 慢

| # | 需求 | 判据 | 覆盖 |
|---|---|---|---|
| 20 | 时间线同时给量、失败、延迟 | `TestScanOpsSeries` | 是 |
| 21 | 失败按 request_id 去重 | 同上（同 id 两条重试只算一个） | 是 |
| 22 | 按模型/渠道下钻，未知维度报错 | `TestScanOpsBreakdown` | 是 |
| 23 | 页面双轴渲染（比例与毫秒不能共轴） | bsk 截图：两条线独立刻度 | 是 |
| 24 | 侧边栏入口 | bsk：管理员分组下有「服务质量」 | 是 |
| 25 | 迁移 dashboard 的 flow/models/capacity | **uncovered** — 排入下一轮 | 否 |
| 26 | 错误聚类（clusters） | **uncovered** — 一期未做 | 否 |

---

# 第五批：变更事件

| # | 需求 | 判据 | 覆盖 |
|---|---|---|---|
| 27 | 变更事件按时间窗取回并正序 | `TestChangeEventsWindow` | 是 |
| 28 | 条数上限生效 | 同上（limit=2 只返回 2 条） | 是 |
| 29 | 渠道启停自动记录 | 代码埋点 + 编译通过 | 部分（未真实触发禁用） |
| 30 | 手工登记接口 | E2E：POST 两条后 GET 返回 | 是 |
| 31 | 时间线上出现竖线 | bsk DOM：含 deploy/chg 标签 | 是 |
| 32 | 竖线落在正确的桶 | **uncovered** — 只在 DOM 层验证存在，未验位置 | 否 |

---

# 第六批：值班闭环

| # | 需求 | 判据 | 覆盖 |
|---|---|---|---|
| 33 | 并发认领只有一个成功 | `TestClaimIncidentIsExclusive`（8 goroutine） | 是 |
| 34 | 状态机不可回退 | `TestIncidentStateMachine` | 是 |
| 35 | 重复认领返回 409 | E2E：第二次 claim → 409 | 是 |
| 36 | 结单写备注 | E2E：resolution 落库且中文完好 | 是 |
| 37 | 前端认领/结单按钮态流转 | bsk：认领后按钮变「结单」 | 是 |
| 38 | 交接视图（谁值班、上一班做到哪） | **uncovered** — 现在只有认领人，没有班次概念 | 否 |

---

# 第七批：capacity 迁移 + 质量列

| # | 需求 | 判据 | 覆盖 |
|---|---|---|---|
| 39 | capacity 从 dashboard 迁到 quality | bsk：/service-health 有「渠道容量计划」，dashboard 无该 tab | 是 |
| 40 | 容量表加「当前质量」列 | bsk：表头含「失败率」「P95」 | 是 |
| 41 | 按 key 而非 label 对齐 | 代码：`qualityByKey[row.key]` | 是 |
| 42 | 质量列无数据时显示占位 | 代码返回 `—` | 部分（本地数据有匹配） |
| 43 | 失败率高/P95 高时标红 | 代码：>=20% / >=10s | 未实测（本地无高值样本） |

---

# 第八批：错误聚类

| # | 需求 | 判据 | 覆盖 |
|---|---|---|---|
| 44 | 同一根因归为一簇 | `TestNormalizeErrorMessage` 5 条规则 | 是 |
| 45 | 按 request_id 去重计数 | `TestScanErrorClusters`（同 id 重试只算 1） | 是 |
| 46 | 给出影响面 | 同上（users/channels 计数） | 是 |
| 47 | 真实数据验证 | E2E：3 条配额错误归 1 簇，30s/45s 归一 | 是 |
| 48 | 前端表格渲染 | bsk：指纹 + 样本 + 影响面 | 是 |
| 49 | 「数字+单位」保留单位 | **uncovered** — `30s` 归一成 `s`，语义对但可读性差 | 否 |

---

# 第九批：延迟精度 / 交接 / 满维度

| # | 需求 | 判据 | 覆盖 |
|---|---|---|---|
| 50 | 延迟记录到毫秒 | `TestOpsSeriesPrefersMilliseconds`（1250 vs 1890 可区分） | 是 |
| 51 | 旧数据按秒回落 | 同上（代码 latencyMs 逐条降级） | 是 |
| 52 | 未结事项可转交 | `TestReassignIncident` + E2E 200 | 是 |
| 53 | 已结事项不可转交 | 同上 + E2E 409 | 是 |
| 54 | 从上游头采集水位（两套方言） | `TestParseRateLimitHeaders` | 是 |
| 55 | 最紧张排最前 | `TestSnapshotRateLimitsOrder` | 是 |
| 56 | 不阻塞转发 | 纯内存，无 IO；实测请求 200 正常 | 是（无压测） |
| 57 | 多实例共享水位 | **uncovered** — 内存方案天然不支持 | 否 |

---

# 第十批：贵维度 + CH 迁移

| # | 需求 | 判据 | 覆盖 |
|---|---|---|---|
| 58 | 成本按单价与用量核算 | `TestComputeCost`（倒挂、无单价、除零） | 是 |
| 59 | 单价按渠道 upsert 不重复 | `TestUpsertChannelCost` | 是 |
| 60 | 未配价不假装零成本 | E2E：cost 列显示 — | 是 |
| 61 | 单价可从界面配置 | bsk：三个输入框 + 保存按钮 | 是 |
| 62 | 老 CH 库自动补列 | `ensureClickHouseLogColumns`（IF NOT EXISTS） | 部分（逻辑有，未在缺列库上实测） |
| 63 | 多币种 | **uncovered** — 只支持美元 | 否 |

---

# 第十一批：聚类可读性 + 交接班次

| # | 需求 | 判据 | 覆盖 |
|---|---|---|---|
| 64 | 数字带单位保留形状 | `TestNormalizeKeepsUnitShape`（Ns / Nms） | 是 |
| 65 | 30s 与 45.5s 同簇 | 同上 | 是 |
| 66 | 同一时刻只有一个在岗班次 | `TestStartShiftClosesPrevious` | 是 |
| 67 | 交班备注只属旧班 | `TestShiftNoteBelongsToPreviousOnly` + E2E | 是 |
| 68 | 前端显示值班人与上一班留言 | bsk：DOM 含三项 | 是 |
| 69 | 班次时长统计 / 交接覆盖率 | **uncovered** — 只记录，未做统计 | 否 |

---

# 第十二批：端到端 + 性能验收（20 万行数据）

用户需求原文：「对之前做的看板 + 分析日志全部端到端测试，特别是性能，千万别影响 API」

## 功能判据（端到端）

| # | 需求 | 判据命令 | 预期 |
|---|---|---|---|
| E1 | /api/ops/series 可用 | `curl` 返回 200 且 points 非空 | 200 |
| E2 | /api/ops/breakdown 可用 | 同上 | 200 |
| E3 | /api/ops/clusters 可用 | 同上 | 200 |
| E4 | /api/ops/changes 可用 | 同上 | 200 |
| E5 | /api/ops/ratelimits 可用 | 同上 | 200 |
| E6 | /api/ops/margins 可用 | 同上 | 200 |
| E7 | /api/ops/incidents 可用 | 同上 | 200 |
| E8 | /api/ops/shift 可用 | 同上 | 200 |
| E9 | 页面各区块渲染 | bsk snapshot | 全部出现 |

## 性能判据（核心）

| # | 需求 | 判据 | 阈值 |
|---|---|---|---|
| P1 | 分析接口本身不能慢 | 20 万行下单次 `/api/ops/series` 墙钟 | < 2000ms |
| P2 | **不能影响 relay** | 干扰下 relay P95 ÷ 基线 P95 | ≤ 2.0 且绝对增量 ≤ 200ms |
| P3 | 干扰下 relay 不报错 | 非 200 响应数 | 0 |

P2 的阈值依据：P95 翻倍已经是很严重的劣化，正常情况干扰应控制在 20% 以内。
给到 2 倍是因为本地单机 Docker 资源紧张，真实多核部署时分母更大。

## 实测结果（第十二批）

| # | 判据 | 实测 | 结论 |
|---|---|---|---|
| E1-E8 | 8 个接口 200 | 全部 200 | PASS |
| E9 | 页面区块 | 同一构建此前已验证；本轮 bsk 会话失效未复验 | 部分 |
| P1 | 单次查询 < 2000ms | 最慢 69ms | PASS（远超） |
| P2 | 干扰下 P95 ≤ 282ms | 183ms | PASS |
| P3 | 干扰下 0 失败 | 0 | PASS |

### 修复前后对比

| 接口 | 修复前 | 修复后 | 倍数 |
|---|---|---|---|
| series | 257ms | 56ms | 4.6× |
| breakdown | 322ms | 52ms | 6.2× |
| margins | 303ms | 69ms | 4.4× |
| 干扰下 relay P95 | 547ms | 183ms | 3.0× |

### 关键发现

1. **有全局限流**（默认 360 次/180 秒 = 2 req/s），这是「不影响 API」的第一道保险。
2. 限流本身也有成本：54329 次 429 让 relay P95 再多 40ms。
3. **极端压力（361 req/s，无限制）下仍会饱和到 450ms**——这是物理极限（超载 3 倍），不是代码缺陷。

## 聚类下推（补充）

| # | 判据 | 实测 | 结论 |
|---|---|---|---|
| P4 | 聚类接口 < 2000ms | 66ms（冷）/ 7ms（热） | PASS |
| P5 | CH 归一化与 Go 一致 | 4 个样本手工对比一致 | 部分（未穷尽边界） |
| P6 | 最终 P2 复验 | P95 184ms，0 失败 | PASS |

**残留风险**：两套归一化实现的长期一致性。改动一侧必须同步另一侧。

---

# 第十三批：按第一性原理优化（三条约束）

用户约束原文：① 能用 clickhouse 加速尽量用 ② 能异步就异步或准实时，有些指标按调度也行 ③ mysql 尽量不占连接也尽量不加表

| # | 约束 | 落地 | 判据 |
|---|---|---|---|
| C1 | CH 加速 | series/breakdown/margins/clusters 全部聚合下推 | 冷查询 47-63ms |
| C2 | 异步/准实时 | 时间窗对齐 30 分钟 + RecordChange 异步 | `TestAlignWindow*` / `TestRecordChangeIsNonBlocking` |
| C3 | MySQL 不占连接 | 单价配置改内存（启动加载 + 写时刷新） | `TestChannelCostCacheRefreshesOnWrite` |
| C4 | 不加表 | 未新增表 | — |
| P7 | 热路径响应 | 全部 8 接口第二轮 < 10ms | 4-6ms |
| P8 | 干扰下 relay | P95 ≤ 282ms | 202ms |

## 本轮最大发现

**缓存 key 用精确 from/to 时，缓存命中率接近 0。**
前端每次 `Date.now()` 都不同，两次相隔一秒的请求参数完全不同。
之前测出的「5ms 热查询」是因为我手工用了完全相同的 URL——真实场景不成立。
对齐到 30 分钟边界后，命中率 100%（66ms → 4-6ms）。

---

# 第十四批：前后端端到端交互验收

用户需求原文：「看下还有没有需要优化的，前后端端到端测试，本地启网页」

## 交互判据（不只是接口 200）

| # | 交互 | 判据 | 通过标准 |
|---|---|---|---|
| U1 | 页面加载 | `bsk snapshot` 含 8 个区块标题 | 8/8 |
| U2 | 时间范围切换 | 点 1h → 图表 x 轴标签数量变化 | 点数减少 |
| U3 | 维度下钻切换 | 点「按渠道」→ 表格首列变成 #数字 | 出现 # |
| U4 | 一键开单 | 点表格「开单」→ 值班看板出现该条 | 条数 +1 |
| U5 | 认领 | 点「认领」→ 出现「结单」按钮 | 出现 |
| U6 | 结单 | 点「结单」→ 出现「已解决」 | 出现 |
| U7 | 接班 | 填备注 + 点「接班」→ 当前值班人变化 | 变化 |
| U8 | 单价配置 | 填渠道+价格 → 保存 → 毛利表出现 | 出现 |
| U9 | 无控制台错误 | bsk console 无 Error | 0 |

## 实测结果（第十四批）

| # | 判据 | 结果 |
|---|---|---|
| U1 | 8 区块 | PASS（8/8） |
| U2 | 范围切换 | PASS（62→47） |
| U3 | 维度切换 | PASS，并修了格式不一致 |
| U4 | 一键开单 | PASS |
| U5 | 认领 | PASS |
| U6 | 结单 | PASS |
| U7 | 接班 | 首次 FAIL（留言错）→ 修复后 PASS |
| U8 | 单价配置 UI | API PASS / UI 受阻（bsk ref 失效） |
| U9 | 无控制台错误 | PASS |

## 补充（第十四批收尾）

| # | 判据 | 结果 |
|---|---|---|
| U8 | 单价配置 UI | **PASS**（改用 CSS selector，绕过 bsk ref 失效） |
| C5 | incidents/shift 不占 MySQL 连接 | PASS：18.3ms→5.1ms，写后立即失效 |
| C6 | 单价变更后毛利重算 | PASS：InvalidateOpsCache("margins:") |

**9/9 全通过。**

---

# 第十五批：高爆发数据 + 充分性验收

## 数据（保留，后续复用）
- 100 万行，7 天窗口，5 模型 / 6 渠道 / 120 用户
- 延迟对数正态 `exp(randNormal(6.5, 1.2))`：均值 1367ms，P95 4987ms
- 错误率 5%，5 种错误类型，分散在全部 6 个渠道

## 性能（100 万行）

| 接口 | 冷查询 | 热查询 |
|---|---|---|
| series | 106ms | 14ms |
| breakdown | 69ms | — |
| clusters | 103ms | — |
| margins | 113ms | — |

| 场景 | relay P95 | 失败 |
|---|---|---|
| 基线 | 147ms | 0 |
| 真实限流下干扰 | 188ms | 0 |
| **878 req/s 极端干扰** | **201ms** | **0** |

## 交互（4/4）

| # | 判据 | 结果 |
|---|---|---|
| I1 | 按渠道 → 首列 `#1` `#2` | PASS |
| I2 | 开单 → 值班看板 +1（标题含维度/失败率/P95） | PASS |
| I3 | 接班 → 时间更新 + 上一班留言显示 | PASS |
| I4 | 单价配置 → 该渠道出现成本与毛利 | PASS |

## 本批修复
1. 造数相关性 bug（同模导致错误全落渠道 1）→ 换互质基数
2. 负毛利格式 `$-788.2051` → `-$788.21`
3. 三列精度不一致（4/4/2 位）→ 统一 2 位
4. 收入列两个 text node 导致快照有空格 → 合成模板字符串

## 缺口
- `web/src/features/ops` **没有前端单测**，全靠 bsk 端到端覆盖。

---

# 第十六批：复核（用户要求「你测下」）

## 接口层 19/19

| 判据 | 实测 |
|---|---|
| series 桶数 > 300 | 336 |
| 总记录 ≈ 100 万 | 998,487 |
| 失败率 ≈ 5% | 5.4% |
| P95 中位 3-8s | 4759ms |
| breakdown 渠道数 = 6 且降序 | ✓ |
| clusters 簇数 = 5、每簇 6 渠道、指纹无重复 | ✓ |
| margins 6 渠道、已配价有成本、未配价成本 0 | ✓ |
| 冷查询 < 500ms | 82ms |
| 热查询 < 30ms | 5ms |
| 四个轻量接口 < 20ms | 5-7ms |

## 交互 4/4
按渠道 `#1/#2`、开单 2→3、接班留言、单价渠道 5（毛利 +27%）。

## 关键归因（推翻此前结论）

| 场景 | relay P95 |
|---|---|
| 基线 | 146ms |
| 分析查询干扰（324 次查询 + 42770 个 429） | 195ms |
| **纯 429 噪音（0 次查询 + 40917 个 429）** | **234ms** |

**结论：看板查询对 relay 的影响在测量噪声以下。此前归因于「查询拖累转发」的劣化，实际来自限流拒绝处理。**

## 新发现的产品问题
1. 重复开单产生完全相同的标题，无法区分是两条还是一条
2. 反复登录会触发 `AUTH_SESSION_LIMIT`（53 条 user_sessions），且清 `sessions` 表无效——表名是 `user_sessions`

---

# 第十七批：线上部署 + 端到端

## 部署结果

| 项 | 值 |
|---|---|
| 版本 | 0.0.119(旧声明) → **0.0.161** |
| 主机 | ai-prod (210.121.44.245)，经 jump 堡垒机 |
| 备份 | `/www/backup/apiduo-cn/20261116-174710/`（43 表 / 含真实数据） |
| Preflight | OK=9 WARN=1 FAIL=0 |

## 线上实测发现（按严重度）

| # | 问题 | 状态 |
|---|---|---|
| 1 | `status_code=502` 被归一成 `status_code=`，502/404 混成一簇 | ✅ 0.0.160 修复 |
| 2 | 维度下钻按渠道时表头仍写「密钥」 | ✅ 0.0.161 修复 |
| 3 | 缺数据时区块完全不渲染（变更/水位） | ⚠️ 未修 |
| 4 | 「请求数」列实为成功数（失败 100% 时显示 0 请求） | ⚠️ 未修 |
| 5 | 1h 视图深夜无数据 | 正常 |

## 前两次发版失败原因（都在 CI，不在代码）

1. **Marker coverage**：11 个上游文件 102 行未标记 → 补 5 个标记块
2. **deleted 预算**：CI 无 upstream 远程，退回旧口径 287（本地 35）→ 取最大值

## 线上写入链路（生产环境实测，已清理）

| 交互 | 结果 | 证据 |
|---|---|---|
| 一键开单 | ✅ | 0→1 条，标题「模型 qwen-image-3.0 · 失败率 15.5% · P95 345000ms」 |
| 认领 | ✅ | status→claimed，owner=apiduo，显示「18:12 · apiduo」 |
| 结单 | ✅ | status→resolved，列表不再显示 |
| 接班 | ✅ | 创建班次，第二次接班时上一班 note 写入 24 字节 |
| 单价配置 | ⚠️ 未完成 | bsk 被用户浏览器 tab 抢占，未能点保存 |

### 发现的真 bug
**首次接班留言丢失**：基线无班次时，第一条留言没有"被结束的班"可写，静默丢弃。
用户在空系统上第一次接班记的话会消失，且没有任何提示。

### 清理
`ops_incidents=0 / ops_shifts=0 / channel_costs=0`，与测试前基线完全一致。

---

# T2 判据：公告详情弹窗的标题（2026-11-17）

## 需求原文

> 修（语境：我上一条指出的「详情弹窗不显示公告标题」）

## 映射

| 需求 | 判据 | 状态 |
|---|---|---|
| 详情弹窗显示公告标题 | `.fixloop/gate-dialog-title.sh` | covered |
| 「公告窗口大小合适吗」 | — | **uncovered**（评估性提问，无客观判据） |
| 「Bsk 端到端测试」 | 同上，断言用 bsk 的 VOM | covered |

## 判据说明

断言 bsk 的 VOM 快照里 `dialog` 块内包含该公告的标题。

**准备步骤**（环境前提，不属于判据）：

```bash
bsk open http://localhost:3000/dashboard/overview --wait-for 公告
# 点第一条公告；bsk 动作带 intent-WAL，被拒时先 bsk sync 对账
bsk sync
```

**为什么判据只做断言、不做交互**：bsk 的 click 会因 `uncertain_prior_effect` 被拒，
每步都要 sync。把准备与断mix在一起，失败时分不清「没打开弹窗」和「弹窗里没标题」，
违反规则 3（一轮一个根因）。

**红/绿证据**：

| 轮 | exit | 输出 |
|---|---|---|
| R1 | 1 | `FAIL[impl]: dialog 内没有公告标题（快照 48 行，仅 dialog）` |
| R2 | 0 | `PASS: dialog 内显示了公告标题` |

模态打开时 VOM 只返回 dialog，列表不在快照里 —— 不能用「列表里有没有」区分失败原因，
诊断分支据此改写过一次（R1 首版误报「页面上找不到该公告」）。

## 根因

`Dialog` 的 title 写死 `t('Announcement Details')`，公告自身的 `title` 从未传给弹窗。
列表有 `item.title?.trim() || getPreviewText(content)` 的回落，详情侧没有对应逻辑。

## 修法

`title={announcement?.title?.trim() || t('Announcement Details')}` + `titleClassName='line-clamp-2'`。

用 title 而非在 body 加 `<h3>`：弹窗的可访问名随之变成公告标题，
屏幕阅读器播报的是「哪一条公告」。

## 尺寸结论（uncovered 那条）

实测视口 1200×741：

| 项 | 值 | 判断 |
|---|---|---|
| 弹窗宽 | 512px（`max-w-lg`），占视口 42% | 合适，中文每行约 30 字 |
| 弹窗高 | 内容区 `min(58vh,520px)`，实测 430px | 合适，内容 + 4 阶段时间线**一屏放下，无滚动** |
| 列表面板 | 固定 `h-72`（288px），每条约 65px | **唯一瑕疵**：5 条时第 5 条被切掉约 0.6 条 |

列表那条不是「px 调错」，是「固定高度 × 动态条数」的固有矛盾：
调到刚好 5 条，6 条时照样切半条。解法是让裁切可感知（底部渐隐 / 「共 N 条」），不是继续调 px。

---

# 异步图像端点矩阵（2026-11-17 04:20）

## 需求原文

> 疯狂的测试 fix-loop
> 1. 异步透传
> 2. 网关侧同步转异步
> 3. 1失败回落到2
> 4. Worker性能如何
> 5. 积分管够随便测
> 6. 异步任务结果url这个怎么约束

## 映射表

| # | 需求原文 | 判据（命令） | 覆盖 |
|---|---|---|---|
| 1 | 异步透传 | `python3 deploy/local/e2e/async_matrix.py s1`：8001 渠道 → 202 受理 → 轮询到 200 + `data[0].url`；任务行 `execution_mode != local` | 是 |
| 2 | 网关侧同步转异步 | `async_matrix.py s2`：同步渠道 → 202 → 轮询到 200 + `data[0].url`；任务行 `execution_mode=local`、`upstream_url` 为渠道地址、落库模型是映射后的值 | 是 |
| 3 | 1 失败回落到 2 | `async_matrix.py s3`：8001 先被选中且返回 500 → 重试落到同步渠道 → 仍返回 202 与结果；mock 侧确实收到失败端点的请求 | 是 |
| 4 | Worker 性能如何 | `async_matrix.py s4`：8 个 20 秒任务全部完成 ≤ 60 秒；期间 `async_task_poll` 至少 3 条（调度器未被占住） | 是 |
| 5 | 积分管够随便测 | — | 否（是许可，不是判据） |
| 6 | 异步任务结果 url 怎么约束 | `async_matrix.py s5`：上游返回 `javascript:` 地址时客户端拿不到它 | 是 |

## 判据命令

```bash
python3 deploy/local/e2e/async_matrix.py s1
python3 deploy/local/e2e/async_matrix.py s2
python3 deploy/local/e2e/async_matrix.py s3
python3 deploy/local/e2e/async_matrix.py s4
python3 deploy/local/e2e/async_matrix.py s5
```

退出码 0 = 通过。s1/s2/s3 各用 1 张真实图（hailuo，4 积分/张），s4/s5 全 mock。

## 追加：Worker 会不会死 / 能否自愈（2026-11-17 05:05）

| # | 需求原文 | 判据 | 覆盖 |
|---|---|---|---|
| 7 | Worker 性能足够吗 | `async_matrix.py s6`：24 个 20 秒任务（3 倍槽位）≤110 秒全部成功，无丢失无重复 | 是 |
| 8 | 会不会死掉 | `service` 单测 `TestApiduoRunLocalTaskGuardedContainsPanic`：worker 内 panic 被捕获 | 是 |
| 9 | 能自动修复 | `async_crash_recovery.py`：SIGKILL → 重启 → 6 分钟内判失败且额度回到提交前 | 是 |

实测（05:02）：24 任务 62 秒完成；SIGKILL 后 6 分 21 秒判 `FAILURE`，额度 96834618 → 96834618。

## 追加：三个已知缺口（2026-11-17 05:40）

| # | 需求 | 判据 | 覆盖 |
|---|---|---|---|
| 10 | 按渠道隔离 | `async_matrix.py s7`：A 渠道占满时，B 的首个任务 ≤30 秒完成 | 是 |
| 11 | 准入控制 | `async_matrix.py s8`：超出队列额度时回 429 且 code=`queue_full` | 是 |
| 12 | 时钟偏移 | `async_matrix.py s9` + `TestApiduoClaimLocalTaskUsesTheSharedClock` | 是 |
| 附 | 隔离不能牺牲吞吐 | `async_matrix.py s6`：24 个 20 秒任务 ≤110 秒 | 是 |

过程里的两次回归（都是判据抓到的）：

- R1 第一版用「每渠道一半」：s7 通过，s6 由 62 秒退化到 122 秒 → 改回。
- R1 第二版用「轮转排序」：s6 恢复 62 秒，s7 仍饿死 39 秒（派发早于第二个渠道出现）→ 改成「每渠道留一个空位」。
- 终态：s6 82 秒、s7 20 秒，两者同时成立。

## 追加：429 不重试（2026-11-17 06:30，第二轮结论）

第一轮按「429 零计费风险」加了重试，用户否决「同步侧不要开重试」，已撤销。
第二轮用户再问「建议加不」——结论仍是**不加**，但理由换成：重试解决不了 429 的成因。

| # | 判据 | 覆盖 |
|---|---|---|
| 13 | `async_matrix.py s10`：上游前两次 429、第三次放行 → 任务**失败**且 mock 只收到 **1** 次请求 | 是 |
| 14 | ~~`TestApiduoLocalImageAsyncRetryDelay`~~ 已随重试一并删除 | — |

边界：`apiduoReplayLocalTask` 单次调用，语义上不再有「尝试次数」这个概念。

## 追加：同步入口不得变成异步（2026-11-17 06:30）

用户原话：「同步侧不要开异步 对的」。

| # | 判据 | 覆盖 |
|---|---|---|
| 15 | `async_matrix.py s11`：`POST /v1/images/generations` 回 200 + 图片、耗时 <5 秒、不建任务行、上游命中 images 路径且 worker 未参与 | 是 |

实测：200 / 0.0 秒 / 任务表 674→674 / 上游收到 `POST /v1/images/generations`。

### 顺带发现的不对称（未修，待决策）

同一条渠道（`primary_endpoint=/mock/images/slow`）：

- 同步入口 → 上游 `/v1/images/generations`（**忽略** primary_endpoint）
- 异步入口 → 本地执行 → 上游 `/mock/images/slow`（**遵循** primary_endpoint）

即「type 54 + 主端点」的渠道，同步图片会打到旧地址、异步反而正确。
修它要动同步中继的 URL 解析，影响所有图像请求，需独立判据。

## 追加：弹性（渠道并发 + 429 换渠道）（2026-11-17 06:50）

用户原话：「好多个上游同步渠道 如何轮换」「做1与2增加弹性」。

| # | 判据 | 覆盖 |
|---|---|---|
| 16 | `async_matrix.py s13`：渠道声明 `local_worker.concurrency=2`，上游观测峰值并发 == 2 | 是 |
| 17 | `async_matrix.py s12`：高优先级渠道 429 → 任务换到低优先级渠道完成，task_id 不变，`tried_channels` 记录被弃渠道 | 是 |
| 18 | `TestChannelSatisfiesFiltersExcludeChannels`：排除过滤器只挡指定渠道，空列表不挡任何渠道 | 是 |
| 19 | `TestApiduoLocalImageMapsModel`：带模型映射的渠道不参与轮换 | 是 |

### 过程

s12 第一次跑失败：`tried_channels=[133,133]` —— 排除过滤器写进了 switch，但没写进
`filterEvalOrder`，`ChannelSatisfiesFilters` 按 order 遍历 ⇒ 新过滤器被静默跳过。
补进 order（放第一位）后通过。这是「加了代码没加注册」的典型，判据抓到了。

## 追加：限流弹性（第三版，2026-11-17 07:00）

用户第三次调整：「由429重试 弹性分配不是也可以吗…或者当前渠道重试」。

**我上一轮的分析错了一半**：我论证的是「几秒的重试没用」，结论推成了「重试没用」。
正确结论是「**退避必须与任务时长同量级**」——8/16 秒的退避在 20–40 秒的生成面前，
等价于一个本地队列。

| # | 判据 | 覆盖 |
|---|---|---|
| 20 | `async_matrix.py s10`：单渠道模型，上游 429 两次后放行 → 任务成功、3 次请求、**耗时 ≥20 秒** | 是 |
| 21 | `TestApiduoLocalImageAsyncRateLimitBackoff`：8/16 秒退避、封顶 25 秒、Retry-After 优先 | 是 |
| 22 | `async_matrix.py s12` 不变：有别的渠道时**先换渠道**（0 等待），不进入退避 | 是 |

恢复顺序固定：换渠道（立即）→ 原地等待（8/16 秒）→ 失败退款。上限 3 次尝试、3 条渠道。

## 追加：映射渠道参与轮换（2026-11-17 07:15）

用户原话：「重定向的不重试，这个规则也不太好吧，很多重定向是运营配的，为的就是参与重试」。

我那条规则是为了绕开实现问题定的（冻结体里是第一条渠道的上游名），站不住脚。
正确做法是**执行期按渠道重新映射**。

| # | 判据 | 覆盖 |
|---|---|---|
| 23 | `async_matrix.py s12`（改）：fallback 渠道带 `model_mapping` → 轮换成功且上游收到 `model=slow-test` | 是 |
| 24 | `TestApiduoLocalImageBodyPreparesPerChannel`：按目标渠道映射，且大整数不被舍入 | 是 |
| 25 | `TestApplyModelMapping`：空映射不是错误（这条差点让所有无映射渠道挂掉） | 是 |
| 26 | `TestApiduoLocalImageBodyFallsBackToStoredRequest`：无 origin 的老行原样发送 | 是 |

过程：第一次跑 s12 全红 —— `ApplyModelMapping("")` 把空字符串当 JSON 解析失败，
所有无映射渠道直接「无法为这条渠道准备请求」。单测补上空映射分支后通过。

## 追加：重试选路补齐 + 并发槽（2026-11-17 07:40）

用户三条：①重试复用渠道选路，按优先级与权重；②重试支持重定向后的模型；③提高并发槽。

**先纠正我上一轮的错误判断**：我说「回退只从最高优先级档挑一次，比首次提交更窄」——错了。
`filterCandidateIDs` 在分档**之前**就应用了排除过滤器，所以 `retry=0` 天然是「剩余渠道里的最高档」。
我加的分层循环是死代码，已删。回退真正窄的地方只有一处：**只按客户端模型名找**。

| # | 判据 | 覆盖 |
|---|---|---|
| 27 | `async_matrix.py s15`：三档优先级 20/10/0，前两档限流 → 逐档降到第三档成功，`tried_channels` 记录前两档 | 是 |
| 28 | `async_matrix.py s14`：客户端名找不到时用上游名再找 → 命中只暴露 `vendor-x` 的渠道，body 带 `model=vendor-x` | 是 |
| 29 | 默认并发 8 → 16（`LOCAL_IMAGE_ASYNC_CONCURRENCY`，上限 64） | 是 |

## 追加：选渠道只认客户端模型名（2026-11-17 07:58）

用户否决「用上游名兜底找渠道」：「分析错误，就应该按客户端模型名找渠道」。

理由比「方向」严重：渠道模型名单是**契约**。只声明 `vendor-x` 的渠道从未被授权服务
`public-model`，它的成本基线与分组策略属于另一个名字——用它是花了没人批准的钱。

| # | 判据 | 覆盖 |
|---|---|---|
| 28' | `async_matrix.py s14`（改为反向）：客户端名只有一条渠道且它 429 → 任务**失败**，只暴露上游名的那条渠道**一次未被调用** | 是 |
| 30 | `async_matrix.py s10`：单渠道模型 429 → **原地重试** 3 次（8+16 秒），24.2 秒后成功 | 是 |

`DispatchedModel` 字段随之上游名回退一起删除。

---

# E2E 判据：公告全链路（2026-11-17）

## 需求原文

> 公告 本地端到端测试 fix-loop

## 一条命令

```bash
./.fixloop/e2e-announcement.sh
```

内部顺序：API 层 24 项 → 造 UI 数据 → 起 headless chrome → UI 层 14 项 → 自动清理。
任一层失败立即停，退出码非 0。`trap EXIT` 保证中断也会清数据，可重复执行。

## 映射

| 需求 | 判据 | 状态 |
|---|---|---|
| API 的增删改查 | `e2e_announcement_api.py` 前 4 项 | covered |
| 四类校验（category/kind/title/content/时间窗/阶段） | 同文件 7 项 | covered |
| 读者侧 `[publishDate, expiresAt)` 过滤 | 同文件 3 项 + 管理侧 2 项 | covered |
| 功能更新由前端分流 | 同文件 1 项 | covered |
| PATCH 局部更新 / 404 | 同文件 4 项 | covered |
| 列表渲染（级别、日期） | `e2e_announcement_ui.mjs` 3 项 | covered |
| 详情弹窗标题 | 同文件 1 项 | covered |
| 时间线三态 | 同文件 3 项 | covered |
| 列表溢出提示 | 同文件 1 项 | covered |
| 功能更新横幅（数量/标签/NEW/关闭） | 同文件 4 项 | covered |
| 移动端布局 | — | **uncovered** |
| 多语言切换后的文案 | — | **uncovered** |

## 轮次记录

| 轮 | 层 | exit | 说明 |
|---|---|---|---|
| R1 | API | 1 | **判据自身 bug**：`call("GET", path, session)` 把 session 当 body 传，第 16 项崩溃 |
| R2 | API | 0 | 24/24 |
| R3 | UI | 0 | 13/13，但时间线断言过宽（`\|\| length === 3` 让任何三阶段都通过） |
| R4 | 全部 | 0 | 收紧为「当前必须落在第二个阶段」+「之后不带当前」，14/14 |

## 归因

R1 与 R4 都是 **TEST 层**：判据写错了，不是产品缺陷。
- R1：GET 调用的参数位传错，属于脚本 bug
- R4：`A \|\| B` 形式的断言里 B 恒真，等于没测

R4 这条值得记：**`some(...) || 长度相等` 是假断言**。它看起来在检查位置，
实际只要条数对就通过。收紧后仍然绿，说明产品本来是对的，但原来的判据无法证明这一点。

## 结论

产品侧未发现缺陷。38 项断言全部通过，本地数据已自动清理（读者侧回到 5 条、0 条 E2E）。

---

# bsk 判据：公告分流与可访问性（2026-11-17）

## 为什么还要用 bsk 测一遍

CDP 判据读的是 `innerText` 和 DOM 结构，bsk 读的是**可访问性树**。
两者的盲区不同 —— 这一轮就抓到了一个 CDP 完全没覆盖的缺陷。

## 判据 1：公告面板不得含 changelog

```bash
./.fixloop/gate-panel-excludes-changelog.sh
```

在 VOM 里，公告面板的条目渲染成 `button "标题 日期 级别"`，
而横幅里的是 `listitem`。所以「snapshot 里存在 button "某 changelog 标题」= 它被当成了公告。

**R1 见红**：`@e48 button "E2E 功能更新 11-17 08:07"` 出现在公告列表区域。

## 判据 2：弹窗的可访问性结构

```bash
./.fixloop/gate-dialog-a11y.sh
```

断言四件事：

| 断言 | 为什么 |
|---|---|
| `dialog` 的可访问名 = 公告标题 | 读屏播报「公告详情」时，读者不知道是哪一条 |
| 时间线是 `list` + 3 个 `listitem` | 用 div 堆的时间线在 innerText 上完全一样，但读不出层级 |
| 「当前」在第二个 `listitem` 上 | 位置错了语义就反了 |
| 之后的 `listitem` 不带「当前」 | 防止标签泄漏 |

## 根因

`useAnnouncements()` 直接返回 `status.announcements` 全量，没有按 `category` 过滤。
我在 `use-notifications.ts` 里做了分流，但公告面板用的是**另一个 hook**，两条路径各走各的。

修在 hook 里而不是面板里：这个函数的语义就是「公告列表」，
将来任何新调用点都不该再自己判断一次 category。

## CDP 为什么没抓到

UI 判据只断言了「列表出现 E2E 界面公告」，没有断言「changelog 不在列表里」。
**只验证「应该有」，不验证「不应该有」，就会漏掉串台类缺陷。**

已在 `e2e_announcement_ui.mjs` 补上这条，现在 15/15。

## 一个工具经验

bsk 的浏览器在 HMR 之后可能停在 ErrorBoundary（页面显示「糟糕！出错了」），
但同一 URL 用 CDP 打开完全正常 —— 说明是浏览器侧的旧状态，不是代码问题。
`bsk close` 后重新 `open` 即恢复。

判断方法：CDP 新开 profile 登录后访问同一页，0 错误 ⇒ 问题在 bsk 的 session，不在代码。
