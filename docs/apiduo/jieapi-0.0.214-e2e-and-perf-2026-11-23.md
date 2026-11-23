# jieapi.com 0.0.214 预演：账单功能端到端 + 性能 A/B

> 2026-11-23 22:22–23:05 CST。预演实例在 core-1，连孤立副本
> （MySQL `jieapi_apiduo_rehearsal` / CH `new_api_logs_rehearsal` / Redis db9），
> **生产容器、nginx、生产库、生产 Redis 一个字节没动**；未向上游发出任何生成请求。

## 0. 结论

1. **账单功能可用**：页面、三个接口、xlsx 导出（18,491 行）全链路跑通，金额与 ClickHouse 原始行
   **逐项对得上**（quota 20,798,814 精确相等；input token = prompt − cache_read − cache_write，差 0）。
2. **发现 4 个缺陷，其中 2 个影响日常使用**：
   - **B1（中）当月明细分页在 ClickHouse 上原地打转**，只能看第一页；
   - **B2（中）导出明细的排序反了**（设计要求正序，实际倒序）；
   - B3/B4 是口径与健壮性问题（金额三处舍入差 3.2e-5；`?internal=1` 这个 URL 直接白屏）。
3. **性能无回归**：同机 A/B（0.196 与 0.214 各起一个实例打同一个副本库），
   三个端点的 p50/p95 差异都在 ±3% 噪声内，内存同为 86 MiB。

## 1. 预演服务怎么进

```bash
# 本地 3005 → jump → core-1:3005（两跳，jump 拒绝 -W，必须用嵌套 -L）
ssh -N -L 3005:127.0.0.1:13005 jump 'ssh -N -L 13005:127.0.0.1:3005 core-1'
# 浏览器打开
open http://127.0.0.1:3005/billing
```

- 账号 `chatfire`，口令 `Rehearsal#2026`（**只在副本库里改的**，生产口令没动）。
- 副本库的 `TurnstileCheckEnabled` 已置 `false`，生产仍是 `true`。
- 副本 CH 里灌了 7 个真实用户的日志切片：10 月 42,245 行、11 月 55,601 行
  （用户 37/55/62/77/304/657/3828），所以账单页有真实形状的数据。
- ⚠️ 这个实例能解开 213 个渠道密钥（复用生产 `CRYPTO_SECRET` 才能测），
  **正式升级后必须 `docker rm -f apiduo-rehearsal` 并 DROP 两个副本库**。

## 2. 通过的验证（端到端）

| 验证 | 结果 |
|---|---|
| 登录 → `/billing` 首屏 | 正常，`系统更新，当前版本：0.0.214` |
| 我的账单（admin=1 代客看 user 62） | 净费用 ¥303.6627、请求 19,614、Token 153.58M、quota 20,798,814 |
| 汇总表（模型 × 分组） | 11 行 + 合计，列含输入/缓存读/缓存写/输出/额度/金额 |
| 详情表 | 15 列，费用行 + 失败行（¥0.00）都在 |
| 模型筛选 | `model=deepseek-v4-flash` 只返回该模型 |
| 全部用户视图 | 6 行，按金额倒序，状态「进行中（预估）」 |
| 用户名筛选 | 输 `kaixin` 剩 1 行，数字与该用户账单页一致 |
| 导出按钮 | 全站视图下**禁用**（合理），我的账单下可用 |
| xlsx 导出 | 三张表 `账单信息 / 汇总 / 明细`，18,491 行明细，1.75 MB，0.85 s |
| 文件完整性 | 下载后 sha256 = 接口返回的 `3ef1b5da…` |
| 权限：普通用户传 `user_id=62` | 被忽略，返回**自己**的账单（user_id 4769，projection=customer） |
| 权限：普通用户传 `internal=1` | 被忽略，投影仍是 customer（看不到 quota/分组） |
| 权限：普通用户访问全站列表 | 403 |
| 权限：普通用户导出 `user_id=62` | 只生成自己的文件（`BILL-2026-11-u4769-…`），无 IDOR |
| 空账期 / 不存在的 `user_id` | 不 500，返回空账单（见 B5） |
| 非法账期 `2026-13` | `{"message":"Invalid period, want YYYY-MM."}` |

## 3. 数据对账（精确性）

| 项 | 账单接口 | ClickHouse 原始行 | 结论 |
|---|---|---|---|
| quota（user 62，2026-11） | 20,798,814 | `sum(quota) where type=2` = 20,798,814 | ✅ 精确相等 |
| requests | 19,614 | 18,491（消耗）+ 1,123（失败） | ✅ |
| errors | 1,123 | type=5 行数 1,123 | ✅ |
| input tokens | 37,386,680 | 146,203,080 − 108,725,200 − 91,200 | ✅ 差 0 |
| 汇总 items 之和 | = quota / requests / tokens | — | ✅ 逐项相等 |

口径确认：`input_tokens` = `prompt_tokens − cache_read − cache_write`（非缓存输入），
页面与导出都是这个口径。

## 4. 缺陷清单

### B1（中）当月明细翻页在 ClickHouse 上原地打转 【影响：运营看不了第二页】

复现（生产同形态，CH 日志库）：

```bash
# 第 1 页返回 next_cursor=1793680170_0
GET /api/billing/statement/lines?period=2026-11&user_id=62&internal=1&page_size=50
# 带上它请求第 2 页 —— 返回的 50 行与第 1 页逐行相同
GET /api/billing/statement/lines?period=2026-11&user_id=62&internal=1&page_size=50&cursor=1793680170_0
```

浏览器实测：点「下一页」，`/api/.../lines?...&cursor=1793680170_0` 发出去了，表格首行仍是
`2026-11-01 13:55:01 / 202611010553185537899618268d9d6yzJAUWTn`。

根因：ClickHouse 的 `logs.id` **恒为 0**（实测 33,556/33,556 行都是 0）。
- `LoadBillingLinesPage` 用最后一行的 `(created_at, id)` 造游标 → `"1793680170_0"`；
- `ParseBillingLineCursor` 有 `id <= 0` 的兜底校验 → 判为非法 → **回落到第一页**。

反证：手工把游标改成 `1793680170_1`，后端就正确前进了（首行不重叠）。所以 SQL 没问题，
坏在游标的取值与校验互斥。已出账账期走 MySQL `billing_statement_lines.id`（真主键），不受影响。

修法（供参考）：CH 上用 `(created_at, request_id)` 做 tie-breaker，或去掉 `id<=0` 的拒绝条件并
在 CH 分支改用 request_id；两者择一，不能只改校验（同秒多行会漏）。

### B2（中）导出明细排序反了 【影响：账单文件不好对账，且与文档约定相反】

- 设计文档 `billing-ledger-design.md:578/589`：明细**按时间正序**（月初到月末），页面倒序。
- 实测 xlsx 明细：首行 `2026-11-23 03:36:37`，末行 `2026-11-01 13:55:01` → **倒序**。
- 实测页面详情：首行 `2026-11-01 13:55:01` → **正序**。
- 即：**页面与导出正好互换**。

根因：`model/log.go` 的 `streamLogs` 在 ClickHouse 下无条件把 order 改写成
`created_at desc, request_id desc`，覆盖了 `StreamUserLogsForBilling` 传入的
`logs.created_at ASC, logs.id ASC`（该函数的注释还专门说明了为什么要正序）。
MySQL 日志库不受影响 —— 这也是本地上测不出来、只有本站（CH）才发作的原因。

### B3（低）金额三处口径相差 3.2e-5

| 出处 | user 62 的金额 |
|---|---|
| 账单头（净费用） | 303.662684 |
| 汇总逐行四舍五入后相加 | 303.662685 |
| 明细逐行四舍五入后相加 | 303.662716 |

差异来自「先舍入再求和」与「先求和再舍入」。金额量级可忽略（¥0.000032），
但一份账单里有三个总和不一致，客户对账时会问。建议：明细/汇总行内保留全精度，
只在展示与合计各舍入一次，并在表头注明净费用为权威值。

### B4（低）`?internal=1` 让整页白屏

`/billing?period=2026-11&user_id=62&internal=1` → 整页错误边界「糟糕！出错了」。
控制台：`{expected:"boolean", code:"invalid_type", path:["internal"]}`。

根因：路由 `validateSearch` 是 `internal: z.boolean().optional()`，而 TanStack Router 默认
`parseSearch` 用 `JSON.parse` —— `"1"` 解析成数字 1，不是布尔。UI 自己写的是 `internal=true`
（解析成 true，正常），所以只有手写/外部链接会崩。
建议 `internal: z.coerce.boolean().optional()` 或 `z.enum(['true','false']).transform(...)`。

### B5（低）空账单与不存在的用户都"成功"

- 0 行的账期照样生成 4.5 KB 的空 xlsx（只有表头）并返回 `success:true`。
- `user_id=999999` 返回 `username=""`、金额 0 的草稿账单，不报「用户不存在」。

运营代客查看时输错 ID 会被误读成「该用户当月没消费」。

### B6（低）403 响应体为空

`schema=all` 对普通用户返回 **403 且 body 为空**（`Content-Type` 也没有），
前端只能落到通用错误块。用 `common.ApiError(c, ...)` 给一段 JSON 更好排查。

### 另记：`page_size` 静默封顶

传 `page_size=500` 实际返回 50 行（`limit > 200 → 50`），响应里没有任何提示。
前端如果按"请求 500 就该拿到 500"写逻辑会死循环（本次就是这么发现 B1 的）。

## 5. 性能：0.196 vs 0.214 同机 A/B

方法：同机两个实例（3005=0.214 / 3006=0.196）连**同一个**副本库；8 线程 × 300 请求，
每请求新建连接，交替跑 3 轮取中位数；限流已放宽（否则单 IP 会 429）。

| 端点 | 0.214 rps / p50 / p95 / p99 | 0.196 rps / p50 / p95 / p99 | 判断 |
|---|---|---|---|
| `/api/status` | 6,473 / 1.18 / 1.87 / 2.25 ms | 6,375 / 1.18 / 1.90 / 2.24 ms | 无差异 |
| `/api/pricing` | 4,749 / 1.53 / 2.83 / 3.90 ms | 4,633 / 1.58 / 2.78 / 3.62 ms | p50 略好，p99 噪声内 |
| `/v1/models`（有效令牌，非空模型表） | 4,326 / 1.78 / 2.49 / 2.86 ms | 4,175 / 1.82 / 2.62 / 3.01 ms | 略好 |

- 内存：0.214 = 86.07 MiB，0.196 = 86.51 MiB（同一副本库、同样请求量后）。
- 代码层：`relay/` 的改动只落在 `advancedcustom`（图片/主端点协议）与 `helper/price.go`
  的「未配置价格」错误分支，**没有动 OpenAI/Anthropic/DeepSeek 的转发主循环**
  —— 本站主力渠道不经过这些新代码。

### 账单接口自身耗时（服务器本机，非隧道）

| 接口 | 中位 | 说明 |
|---|---|---|
| 全站当月汇总（6 用户） | 5 ms | 一条 CH `GROUP BY user_id`，无 N+1 |
| 单用户当月账单（19.6k 行） | 30 ms | CH 聚合下推 |
| 明细 50 行 | 5 ms | 键集分页，首屏不随行数变慢 |
| 单用户 10 月账单（42k 行） | 20 ms | — |
| 导出 18,491 行 xlsx | 850 ms | 同步生成，期间占用一个连接 |

**容量提示**：全站视图每次刷新都对当月 CH 做一次整月扫描（生产 5.5 M 行/月），
且路由带 `DisableCache()`。按 47 个用户、5.5 M 行估算，生产应该是 100–300 ms 量级；
管理员多点几次就是几次全月扫描。建议照 `service/growth.go` 的 `cachedOps` 加 30 s 缓存，
或在 `LoadBillingUserSummaries` 上包一层。转发不受影响（不同表、不同连接池）。

## 6. 预演没覆盖的

1. **出账任务本身**（`RunBillingIssueForPeriod`）：只在 00:10–06:00 窗口内由 master 触发，
   没有任何手动入口；改容器时钟会改宿主机时钟（容器共享内核时钟），已在方案阶段排除。
   ⇒ 三张新表和归档文件的写入路径、已出账页签的「已冻结」状态**未做端到端**。
   升级后第一个 00:10 窗口用 `docker logs | grep 'billing issued'` 复核。
2. **真实 relay 200 流量**：按零成本纪律未发任何上游请求，`/v1` 只到 401/模型列表。
3. **登录后的其他新页面**：只走了 `/billing`，增长与服务健康页未回归。

## 7. 建议的处置顺序

| 优先级 | 事项 |
|---|---|
| 升级前 | B1、B2 修掉（都在 CH 分支，改动小）；B4 一行 zod 改动 |
| 升级前 | B5/B6 视时间，属体验问题 |
| 升级后 | 首个 00:10 窗口复核出账；给全站视图加缓存 |
| 清理 | `docker rm -f apiduo-rehearsal`、DROP `jieapi_apiduo_rehearsal` / `new_api_logs_rehearsal`、FLUSHDB db9 |

---

# 修复记录（22:40–23:15）

上面四个缺陷已改并**在真实的 ClickHouse 副本上验证**：交叉编译 linux/amd64 二进制挂进
`0.0.214` 镜像（`-v /tmp/new-api-fix2:/new-api:ro`），起在 core-1:3005，版本号 `0.0.214-fix1`。

## 改了什么

| 编号 | 修法 | 文件 |
|---|---|---|
| B1 | 游标分两种模式：ClickHouse 用 `r.<ts>.<request_id>`，其他库仍是 `<ts>_<id>`；查询条件与 `ORDER BY` 跟着游标里的模式走。空 request_id 不再产出无法解析的游标（宁可在该页停住，也不回第一页死循环） | `model/billing_statement.go`、`model/billing_draft_query.go`、`service/billing_query.go` |
| B2 | 把 `streamLogs` 拆成「按调用方 order 读」与「强制最新在前」两个入口；账单导出走前者，`ORDER BY created_at ASC, request_id ASC` | `model/log.go`、`model/billing_draft_query.go` |
| B7 | `NormalizeBillingLinePageSize` 改成夹取（0→50、>200→200），并在响应里回 `page_size`；模型层上限改成 201，不再把「200+多取的一行」打回 50 | `service/billing_query.go`、`model/billing_draft_query.go`、`controller/billing_statement.go` |
| B5 | 空账期导出直接拒绝（`No billable usage in this period.`）；代客查看时 `user_id` 查不到人报 `User not found.`，不再给空账单 | `controller/billing_statement.go` |
| B6 | 全站视图的 403 改成 JSON（`ApiErrorStatus`），带可读原因 | `controller/billing_statement.go` |
| B4 | `internal` 参数先折叠 `1/0/true/false` 再交给 `z.boolean()`；schema 抽到 `features/billing/search.ts` 并补单测 | `web/src/features/billing/search.ts`、`web/src/routes/_authenticated/billing.tsx`、`__tests__/search.test.ts` |
| B3 | **未改**。三处金额差 3.2e-5 属「先舍入 vs 先求和」，改它要动账单口径，需产品拍板；已在页面上以净费用为权威值 | — |

## 实测（core-1:3005，同一份 ClickHouse 副本）

| 验证 | 0.0.214 | 0.0.214-fix1 |
|---|---|---|
| `next_cursor` | `1793680170_0`（解析非法） | `r.1793680170.20261103042929…` |
| 第 2 页首行 | 与第 1 页相同（原地打转） | `created_at 1793680276`，前进 |
| 全量翻页（99 页 × 200） | — | **19,614 行 / 19,614 个唯一 (created_at, request_id) / 0 重复**，与 CH 行数一致 |
| `page_size=200` | 只回 50 行且 `next_cursor` 为空 | 回 200 行，`page_size: 200` |
| `page_size=5000` | 50 行 | 夹到 200 行 |
| 导出明细排序 | 首 11-23、末 11-01（倒序） | 首 11-01 13:55、末 11-23 03:36（**正序**），18,491 行，quota 合计 20,798,814 |
| 空账期导出 | 200 + 4.5 KB 空文件 | `No billable usage in this period.` |
| `user_id=999999` | 200 + 空账单 | `User not found.` |
| 普通用户全站视图 | 403 空 body | 403 `{"message":"Administrator role required…"}` |
| `?internal=1` | 整页错误边界 | 正常渲染，¥303.6627 / 19,614 / 153.58M |
| 浏览器翻页 | 首行不变 | 首行变 `2026-11-03 12:31:16`，请求带 `cursor=r.1793680170.…` |

测试与静态检查：

```
go build ./...                                     OK
go test ./service/ -run 'Billing|Log'              ok
go test ./model/ -run 'Billing|Log'                ok
go test ./controller/ -run 'Billing'               ok
bun check -b                                       No type errors (1,737 files)
oxlint（改动文件）                                  0 warning 0 error
vitest src/features/billing/__tests__/search.test.ts   4 passed
```

新增回归测试：`TestNormalizeBillingLinePageSize`、`TestParseBillingLineCursorRoundTripsRequestId`、
`TestBillingLinesCursorByRequestIdTieBreak`、`TestBillingLinesPageHonoursLargePageSize`
（最后一条在旧代码上会失败：只能拿到 50 行）。

## 怎么验证的（可复现）

```bash
# 本机交叉编译
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
  -ldflags "-s -w -X 'github.com/QuantumNous/new-api/common.Version=0.0.214-fix1'" -o /tmp/new-api-fix2 .
# 传到 core-1 后挂进官方镜像跑（不改镜像、不动现网）
docker run -d --name apiduo-rehearsal --network jieapi-master_new-api-network \
  --env-file /www/docker/apiduo-rehearsal/app-0214.env -e NODE_TYPE=master \
  -v /tmp/new-api-fix2:/new-api:ro -p 127.0.0.1:3005:3000 ghcr.io/apiduo/apiduo:0.0.214
```

## 仍未覆盖

1. **出账任务**（`RunBillingIssueForPeriod`）没有入口，只能在 00:10–06:00 由 master 触发 ——
   已出账页签、归档文件与「金额冻结」仍未经端到端验证。
2. 已出账账期的明细分页走 MySQL 主键，本次改动没碰它，但也没有真实已出账数据可测。
3. B3 的金额口径差异保留原样。

---

# 第二轮自查补丁（23:28–23:40）

上一轮修完后按「还有哪些同类问题」再审了一遍，又抓到三处**该修但漏了**的：

| 编号 | 问题 | 修法 |
|---|---|---|
| G1 | **已出账账期的明细分页也有同一个 200 行上限 bug**：`ListBillingStatementLinesAfter` 里 `limit > 200 → 50`，而服务层传的是 `limit+1=201`，`page_size=200` 在已出账账期上同样只回 50 行且 `next_cursor` 为空 | 上界改成 `BillingLinePageSizeMax+1`，与当月路径共用同一个常量 |
| G2 | 新增的三条错误文案是裸字符串，前端拿不到 `message_key`，切语言时不会翻译 | 改走 `common.NewMessage(...)`，并把三条键补进 `web/src/i18n/apiduo/{en,zh,zh-TW,fr,ru,ja,vi}.json` |
| G3 | 上一轮把「`user_id` 查不到人」改成 404 后，**误伤已删除用户**：用户被删后 `users` 查不到名字，但他的已出账账单还在，代客查看会被 404 | 名字为空时再查一次 `BillingStatementExistsForUser`；有账单就按 `user#<id>` 继续，没有才报 404 |

顺带把只在测试里用到的 offset 版 `ListBillingStatementLines` 的上界也对齐（原来是 200→50）。

## 第二轮实测（挂在 3005 的修复版）

| 验证 | 结果 |
|---|---|
| 空账期导出 | `{"message":"No billable usage in this period.","message_key":"…"}` |
| 查无此人 | `{"message":"User not found.","message_key":"…"}` |
| 造一条 `user_id=999888`（users 表无此人）的已出账账单后代客查看 | **200**，`username=user#999888`，金额 12.340000（不再 404） |
| 普通用户全站视图 | 403 + `message_key` |
| `page_size=200`（当月） | 200 行，第 2 页前进 |

新增测试：`TestIssuedBillingLinesHonoursLargePageSize`、`TestIssuedBillingLinesSameSecondCursor`、
`TestBillingStatementExistsForUser`；i18n 断言 `apiduo-locales.test.ts` 通过。

## 仍然没做的

1. **B3（金额三处舍入差 3.2e-5）** —— 不动，属账单口径决策。
2. **出账任务 / 已出账页签** —— 无手动入口，只能在 00:10–06:00 由 master 触发；
   三张新表的写入、归档文件生成、「金额冻结」状态仍未端到端验证。
3. 修复只在工作区，**未提交**。上线仍要先发版（CI 出镜像）再按升级步骤单切 jieapi.com。
