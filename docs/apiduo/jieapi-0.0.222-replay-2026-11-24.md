# jieapi.com 升级重放：0.0.196 → 0.0.222（生产规模）

> 2026-11-24 06:32–06:45 CST。这一轮把预演从「7 个用户的切片」升级成**整月 503 万行、
> 40 个用户的完整生产数据**，验证 0.214→0.222 之间那 8 个提交（账单聚合内存、并发闸门、
> Code 241 重试、归属探测）在真实体量下是否站得住。生产仍是 `0.0.196`，容器 21 小时无重启。

## 0. 结论：可以升级（GO），但有三个前提

| 项 | 判断 |
|---|---|
| 迁移 | ✅ 50 → 53 表，纯增；冷库 3.3 s 启动（含迁移） |
| 功能 | ✅ 生产规模下全站视图 0.07 s、单用户账单 1.5–1.8 s、导出 12.7 万行 3.6 s |
| 内存 | ✅ 全程 84–95 MiB / 4 GiB，5 M 行聚合没有出现尖峰 |
| 性能 | ✅ 与 0.0.196 同机 A/B 差异在 ±4% 噪声内 |
| 回滚 | ✅ 0.0.196 在 53 表的库上正常起、正常服务 |
| **前提 1** | **升级窗口避开 00:10–06:00**：升级后第一个凌晨会对 10 月自动补出账（47 份），这条路径**没法预演** |
| **前提 2** | B3 金额口径差仍在（12.7 万行时 6.7e-5 元），未改 |
| **前提 3** | 迁移只增表，但**首个 00:10 的出账要人盯一次**（`grep 'billing issued'`） |

## 1. 这一轮和上一轮的区别

上一轮预演的是 `0.0.214`，CH 里只有 7 个用户的 4.2 万 / 5.5 万行切片。
之后代码线又推了 8 个提交，其中 5 个是账单体量问题的修复：

| 提交 | 内容 |
|---|---|
| `f62cca847` | 汇总接口在生产规模下必撞内存上限；已删除用户的账单被判不存在 |
| `56cf1c892` | 归属探测用 `LIMIT 1` 代替 count |
| `9316ae59c` | 账单聚合加并发闸门与 30 s 超时 |
| `bf8051e58` | 聚合名额按 RSS 降到 2，单条上限 800 MB |
| `31b99e5e8` | 聚合遇到 Code 241 重试一次 |

这些改动**只在生产体量下才触发**，切片数据测不出来。所以这一轮把 CH 换成
**整个 11 月（5,027,308 行 / 40 用户）**，MySQL 换成当日生产快照（50 表 / 4,758 用户）。

## 2. 环境

| 依赖 | 值 |
|---|---|
| 镜像 | `ghcr.io/apiduo/apiduo:0.0.222`（本地已缓存） |
| 实例 | `apiduo-rehearsal` @ core-1 `127.0.0.1:3005`，`NODE_TYPE=master`，4C/4G |
| MySQL | `jieapi_apiduo_rehearsal`：当日快照，dump 5.0 s / 60 MB，恢复 18 s |
| ClickHouse | `new_api_logs_rehearsal`：`CREATE TABLE AS` + 全月复制，**9.3 s / 503 万行** |
| Redis | db9 |
| 访问 | `ssh -N -L 3005:127.0.0.1:13005 jump 'ssh -N -L 13005:127.0.0.1:3005 core-1'` |
| 账号 | `chatfire` / `Rehearsal#2026`（快照恢复后重新在副本库改的，生产没动） |

## 3. 迁移与启动

```
database migration started  06:37:33（主库）
database migration started  06:37:34（日志库）
healthy in 3.3 s，version 0.0.222，tables 50 → 53
启动后 RSS 86 MiB
```

告警只有已知三条（`SESSION_COOKIE_SECURE`、`TRUSTED_PROXIES` ×2 / 令牌限制来源 IP）。

## 4. 生产规模端到端（HTTP）

| 场景 | 结果 | 耗时 | 内存 |
|---|---|---|---|
| 全站当月汇总（5.03 M 行 / 40 用户） | 200，34 个有消费的用户，榜首 ¥151,493.2438 | **0.07 s** | 78.9 MiB |
| 单用户账单 user 1（159 万请求，227 个模型×分组） | 200，quota 10,376,249,578，¥151,493.2438 | **1.49 s** | 78.9 MiB |
| 单用户账单 user 4164（209 万请求） | 200，¥36,160.3575 | **1.75 s** | 79.4 MiB |
| 明细分页（page_size=200） | 200 ×2 页，游标 `r.1793491248.202611010000241…` 正常前进 | 19 ms / 16 ms | — |
| 导出 user 34（20 万日志行） | 200，写出 **127,692 行 / 12.65 MB** | **3.6 s** | 84.5 MiB |
| 导出 user 4164（209 万行 > 200 万上限） | 200 + `This statement has too many rows to export…`（按设计拒绝） | 0.0 s | — |
| 并发 4×全站 + 2×重用户 | 全部 200，无超时无 OOM | 总 1.7 s | 84.5 MiB |
| 空账期导出 | `No billable usage in this period.` + `message_key` | — | — |
| 查无此人 `user_id=999999` | `User not found.` + `message_key` | — | — |

> 全站汇总 0.07 s 的原因是它只读 `user_id/tp/type/quota` 这类窄列，不做 `other` 的 JSON 抽取；
> 单用户账单要逐行取缓存 token，所以是 1.5 s 量级。两者都远低于 30 s 闸门。

## 5. 浏览器端到端（bsk，0.0.222 + 完整前端）

| 步骤 | 结果 |
|---|---|
| 登录 → `/billing` | 净费用 **¥151,493.2438**、请求 **1,592,606**（失败 423,741 · 退款 407）、Token 48,549.92M |
| 汇总页签 | 真实模型行（kimi-k3.1-pro ¥43,818.32、deepseek-v4.3-flash-2027 ¥38,126.44 …） |
| 详情页签 + 下一页 | 首行 `11-01 08:00:00` → `11-01 08:00:08`，请求带 `cursor=r.1793491208.…` |
| 全部用户 | 34 行，按金额倒序，第 1 行与第 2 行与 API 完全一致 |
| 代客查看 `user_id=34` | 净费用 ¥2,827.0538、请求 202,722、`user=withgpt` |
| 导出按钮 | `GET …/statement/export` 200，页面出现 `billing-statement-2026-11-draft.xlsx · 127,692 行数 · 12.06 MB · 草稿` |

## 6. 导出文件校验（生产规模）

| 项 | 值 |
|---|---|
| 工作表 | 账单信息 / 汇总 / 明细 |
| 明细行数 | 127,692（与接口一致，失败行按设计不写） |
| 排序 | 首 `2026-11-01 09:04:03`、末 `2026-11-24 06:33:00`，**正序**（B2 修复在生产规模下成立） |
| quota 合计 | 193,633,822 = 接口「内部 quota」193,633,822 ✅ |
| 金额合计 | 2827.053868 vs 头部 2827.053801，差 **6.7e-5** —— B3 的舍入差随行数线性放大，仍然可忽略 |

## 7. 性能 A/B（同机、同库、8 线程 × 250、交替 3 轮取中位）

| 端点 | 0.0.222 (3005) | 0.0.196 (3006) |
|---|---|---|
| `/api/status` | 6,442 rps / p50 1.17 / p95 1.93 / p99 2.28 ms | 6,581 / 1.13 / 1.94 / 2.43 |
| `/api/pricing` | 4,955 / 1.48 / 2.63 / 3.66 | 4,749 / 1.55 / 2.73 / 3.79 |
| `/v1/models`（有效令牌） | 4,049 / 1.90 / 2.62 / 3.06 | 4,115 / 1.87 / 2.57 / 2.94 |

差异全部在 ±4% 内，无回归。常驻内存 86–95 MiB（0.196 为 84.5 MiB）。

> 第一次跑的时候 0.222 那台没关限流（`GLOBAL_WEB_RATE_LIMIT` 默认 600/分），
> `/api/pricing` 全是 429，数字作废；关掉限流重跑才是上表。

## 8. 回滚

`0.0.196`（3006）在**已被 0.222 迁移过**的 53 表库上正常启动、`/api/status` `/api/pricing`
`/v1/models` 全部 200，读到的用户与账单数据不变。镜像两台机器本地都在。

## 9. 升级步骤（0.0.222 版）

与上一份步骤单一致，只换版本号：

```bash
# core-1 master（先升，迁移在它身上）
cd /www/docker/apiduo
TS=$(date +%Y%m%d-%H%M%S); mkdir -p /www/backup/jieapi-upgrade/pre-222-$TS
cp docker-compose.yml /www/backup/jieapi-upgrade/pre-222-$TS/
cp /www/server/panel/vhost/nginx/{jieapi.com.conf,api.jieapi.com.conf,0.apiduo-canary.conf} /www/backup/jieapi-upgrade/pre-222-$TS/
# mysqldump 同前（5 s / 60 MB）
sed -i 's#ghcr.io/apiduo/apiduo:0.0.196#ghcr.io/apiduo/apiduo:0.0.222#' docker-compose.yml
docker-compose config -q && docker-compose up -d apiduo
curl -s http://127.0.0.1:3002/api/status | python3 -c "import sys,json;print(json.load(sys.stdin)['data']['version'])"

# core-3 standby（后升，不跑迁移）
cd /www/docker/apiduo
sed -i 's#ghcr.io/apiduo/apiduo:0.0.196#ghcr.io/apiduo/apiduo:0.0.222#' docker-compose.yml
docker-compose up -d apiduo-standby
curl -s http://127.0.0.1:3003/api/status | head -c 80
```

- 重建窗口：停 0.2 s + 起 0.8–3.3 s；POST 仍会有个位数 502（nginx 不对非幂等请求重试）。
- 回滚：`sed` 改回 `0.0.196` → `docker-compose up -d apiduo`，秒级。

## 10. 仍未验证的一条

**出账任务（`RunBillingIssueForPeriod`）**：只在 00:10–06:00 由 master 触发，没有任何手动入口。
升级后第一个凌晨会对 **2026-10** 跑一次全量首次出账（47 个用户、CH 约 550 万行）。
按本轮单用户 1.5 s + 导出 3.6 s 的实测外推，预计 2–5 分钟；聚合有 2 并发闸门与 800 MB 单条上限，
理论上不会挤爆，但**没有端到端证据**。建议：

```bash
# 升级后当晚 00:15 看一次
docker logs jieapi-apiduo 2>&1 | grep -E 'billing issued|billing build failed'
mysql -e "SELECT COUNT(*), SUM(requests) FROM billing_statements WHERE period='2026-10'"
ls /www/docker/apiduo/data/exports/billing/2026-10/ | head
```

若出问题：作废（`status='void'`）后下一个 30 分钟 tick 会重跑，或直接回滚到 0.0.196。

---

# 补测：手动触发出账（07:00）

出账任务没有 HTTP 入口，只在 00:10–06:00 由 master 调 `RunBillingIssueForPeriod`。
为了把它测掉，编了一个**只在预演用的**一次性二进制：在 `main()` 里 `StartBillingTaskLoop()`
之前加一个 `BILLING_ISSUE_NOW=<period>` 钩子，同步跑完一次出账再 `os.Exit(0)`
（补丁当场还原，没进仓库；进仓库的只有下面的列宽修复）。

## 第一次跑：发现一个会阻断上线的缺陷

```
billing issue (manual) done: period=2026-11 issued=32 skipped=0 failed=2 exports=64 elapsed=4m16s
billing build failed: user=4762 error=Error 1406 (22001): Data too long for column 'endpoint' at row 150
billing build failed: user=4479 error=Error 1406 (22001): Data too long for column 'endpoint' at row 1
```

**B12（高，阻断）`billing_statement_lines.endpoint` 只有 varchar(64)，放不下 Gemini 的流式路径。**

| 端点 | 长度 |
|---|---|
| `/v1beta/models/gemini-3.1-pro-preview-customtools:streamGenerateContent` | 71 |
| `/happyhorse/api/v1/aigc/video-generation/video-synthesis` | 65 |

影响：只要一个用户当月调过一次长路径模型，他的**整份**账单在插入明细时回滚
（`BuildBillingStatement` 的 defer 会清掉半份账单），出账任务每 30 分钟重试、每次必失败，
一直到窗口关闭 —— 该用户当月没有账单，且只有日志里一行 `billing build failed`。
11 月样本里 4762（98 行）、4479（25 行）两个用户命中。

全站历史最大长度实测（`new_api_logs.logs` 全部行）：

| 字段 | 实际最大 | 列宽 | 结论 |
|---|---|---|---|
| request_id | 39 | 64 | OK |
| model_name | 36 | 128 | OK |
| token_name | 38 | 128 | OK |
| group | 13 | 64 | OK |
| **endpoint** | **71** | **64** | **溢出** |
| task_id | 37 | 64 | OK |

## 修复

1. `BillingStatementLine.Endpoint` 列宽 **64 → 191**。
2. 光改 tag 不够：GORM 的 AutoMigrate 不会因为「同是 varchar、长度变了」去 ALTER 已有列
   （实测重启后列仍是 64）。所以加了 `MigrateBillingStatementEndpointWidth()`：
   查 `information_schema` 拿当前长度，只有 < 191 才 `ALTER`，放在 `migrateDB()` 的
   APIDUO 标记块里。空表即时完成；4.6 M 行的存量表实测 **1 分 45 秒**（apiduo.cn 升级时要注意这个窗口）。

## 修复后重跑

| 指标 | 第一次 | 修复后 |
|---|---|---|
| issued | 32 | **34**（两个失败用户补上） |
| failed | 2 | **0** |
| exports | 64 | 68（549 MB，含 user 1 的 1.59 M 行账单） |
| 耗时 | 4m16s（34 用户） | 20s（只补 2 个） |

## 顺带把「已出账」这条路径测掉了

之前一直没证据的那条路径，现在有实物：

| 验证 | 结果 |
|---|---|
| `/api/billing/statement?...&user_id=34` | `status=issued`、`is_final=true`、金额冻结 ¥2,827.053801 |
| `/api/billing/statements?scope=all` | 34 条，`is_draft=false`，全部 `is_issued=true` |
| `/api/billing/statement/export`（已出账） | 直接返回归档文件，`is_draft=false`、127,692 行、12.64 MB，下载 200 |
| 归档目录 | `data222/data/exports/billing/2026-11/`，68 个文件共 **549 MB** |
| 浏览器 | 卡片显示「已出账 · 2026-11-24 06:50:47」，不再有「预估值」字样，汇总仍可查 |

## 结论修正

- ✅ 出账任务本身（`RunBillingIssueForPeriod`）在 503 万行 / 34 用户上跑通，4m16s，
  幂等（第二次只补失败的两个）。
- ❌ **0.0.222 不能直接上线**：它的 `endpoint` 是 64，`billing_statements` 会在升级时被建成
  varchar(64)，凌晨第一次出账就会重演上面那两个失败。**必须先把列宽修复发版**。

---

# 发版：0.0.223

| 项 | 结果 |
|---|---|
| 提交 | `5a3d4cd7c` 列宽修复 + `6a5b77746` 把迁移挪到出账入口 |
| 标签 | `apiduo-v0.0.223` |
| 镜像 | `ghcr.io/apiduo/apiduo:0.0.223`（manifest 200） |
| Release | `apiduo-v0.0.223` 已创建 |

第一次发版被 CI 拦下：`Marker coverage` 报 `blocks 673→674 / added 6996→7001`。
原因是把迁移调用挂在 `model/migrateDB()`（上游文件，需要新标记块）。
改为挂在 `service/billing_job.go` 的 `StartBillingTaskLoop()`（apiduo 自有文件，不占合并预算），
`go run ./tools/markercheck` 本地通过（遗留欠账 48 文件 / 428 行不变）。

另有一处流水线噪声：推 `main` 的 workflow 会在 **Tag and push** 步骤自动打 tag，
而本地已手工推过同名 `0.0.223`，那次 main 运行的 tag 步骤失败（镜像由 tag 那次运行产出）。
下次推 main 会自动打 `0.0.224`。

## 0.0.223 在预演实例上的最终冒烟

```
version 0.0.223        endpoint column = varchar(191)        RSS 82 MiB
单用户已出账账单   0.37 s   ¥2,827.053801  requests=202,722
全站 34 条        0.01 s   is_draft=false（读 MySQL 快照，不再扫 CH）
归档文件          127,692 行 / 12.64 MB / is_draft=false
空账期导出        No billable usage in this period.
```

## 升级口径（最终）

- 目标版本 **0.0.223**（不是 0.0.222：后者 endpoint 仍是 64，凌晨首次出账会对
  调用过长 Gemini 路径的用户整份失败）。
- 窗口避开 00:10–06:00；core-1 先升（迁移），core-3 后升。
- 升级后第一个凌晨会自动对 **2026-10** 出账（约 550 万行 / 47 用户）。
  依据本轮 11 月实测（34 用户 / 503 万行 / 4m16s + 549 MB 归档），预计 4–6 分钟、归档约 500 MB 量级。
  建议当晚 `docker logs | grep 'billing issued'` 复核一次。

---

# 补充：导出文件的位置、压缩与自愈（07:30–07:45）

## 文件在哪

| 场景 | 生成时机 | 路径（容器内 / jieapi 宿主） |
|---|---|---|
| 已出账（归档） | **出账那一刻生成一次**，之后下载只是签名 + 静态文件 | `/data/data/exports/billing/<账期>/BILL-<账期>-u<id>-<admin\|customer>.xlsx`<br>`/www/docker/apiduo/data/data/exports/billing/<账期>/…` |
| 当月（草稿） | **每点一次导出就现算一次**，覆盖同名文件 | 同目录，文件名带 `-draft-` 前缀 |

多出来的那层 `data` 是 `EXPORT_FILE_DIR` 默认值（相对 `data/exports`）叠加 WORKDIR `/data` 的结果，
要扁平化就显式设 `EXPORT_FILE_DIR=/data/exports`。写入是「临时文件 + fsync + rename」，不会留半截文件。

实测体量（11 月 / 34 用户 / 503 万行）：归档 68 个文件 **549 MB**，最大 user 1 113 MB、user 4164 106 MB。
`billing_statement_lines` 一个月 **2.1 GB**（1.16 GB 数据 + 0.93 GB 索引）。合计约 2.6 GB/月。

## 压缩能省多少：省不了多少

xlsx 本身就是 zip，写入口用的是 `zip.Deflate`：

| 度量 | 数值 |
|---|---|
| 117.6 MB 的 admin 账单，内部 XML 解压后 | **1,203 MB** |
| 压缩比 | **10.2×** |
| 同样数据用 zlib level 9 重压 | 107 MB（−8%） |
| 把整个 xlsx 再 gzip -9 | 98.5 MB（−16%，但下载下来不是能直接打开的文件） |

结论：**已经压到 10 倍，再优化只有 8%（换 level 9）或 16%（外套 gzip），代价是出账时间翻倍或
下载要用户自己解压，不划算。** 真正该做的是保留策略（明细表 + 导出按月归档/清理），
这条仍未做。

## 修复 #2：归档文件丢失后自动重建

原来 `ExportBillingStatement` 只要 DB 里有导出记录就直接签名返回，不校验磁盘；
文件被清理、换挂载卷、从旧机器搬过来，用户点下载就是一个 404，而且永远不会重建。

现在先 `ExportFileStat` 探一下：文件在就签名直出；不在就落下去重新生成
（已出账的数据来自 MySQL 快照，不碰 ClickHouse，2 秒级），并记一行
`billing export file missing, regenerating`。

**实测**：在一个**没有任何归档文件**的卷（data223，0 个文件）上请求 user 34 的已出账导出：

```
billing export file missing, regenerating: statement=9 projection=admin key=billing/2026-11/BILL-2026-11-u34-admin.xlsx
自愈导出: 2.2s rows=127692 bytes=12.64MB is_draft=False
download=200 size=12644993
```

日志与下载都对上，文件重新出现在 `data223/data/exports/billing/2026-11/`。
