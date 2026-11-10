# e2e/live — 跑在真二进制上的端到端回归

`e2e/*.go` 是进程内单测（`httptest`）。这里不同：**拉起真网关进程**，前面挂假上游、
旁边挂假 Langfuse，断言的是「上游实际收到什么字节」「sink 实际收到几个 span」
「浏览器里真的出现了这个链接」——网关自己的日志只能证明它以为自己在干什么。

```bash
e2e/live/run_all.sh          # 约 2-3 分钟，跑完自动回收进程与临时库
KEEP=1 e2e/live/run_all.sh   # 保留临时目录（日志、SQLite、sink 落盘）排查用
```

前置：`go`、`bun`、`python3`（图片套件需要 `httpx`）、本机 Chrome。
`web/dist` 缺失时脚本会先 `bun run build`（`//go:embed` 要求它存在）。

## 端口

| 用途 | 默认端口 | 覆盖变量 |
|---|---|---|
| 假上游（chat，含 500 模式） | 39401 | `TRACE_UP` |
| 假 Langfuse OTLP sink | 39402 | `SINK` |
| 假上游（图片，记录式） | 39403 | `IMG_UP` |
| 网关 · trace 开启 | 39410 | `GW_TRACE` |
| 假上游 #2（多渠道选路用） | 39404 | `UP2` |
| 假 sink #2（占位） | 39405 | `SINK2` |
| 假上游（MCP：图片同步 + 视频异步） | 39430 | `MCP_UP` |
| 网关 · 图片/主端点 | 39420 | `GW_IMAGE` |
| 网关 · trace 关闭 | 39440 | `GW_OFF` |

端口被占用时脚本直接退出并打印占位进程 —— 上一次没收干净时先处理掉，别硬跑。

## 套件

| 文件 | 断言的核心事实 |
|---|---|
| `suites/trace.py` | span 形状（流式帧重组、tool_calls、usage、失败态 ERROR+statusMessage+保留请求体）、`trace_id` 与 span 一一对应、traceparent 被采纳成同一条 trace、redaction 生效 |
| `suites/off.py` | 默认关闭时**行为等价**：不导出、日志不写 `trace_id`、`traceparent` 不转发给上游 |
| `suites/privacy.py` | 普通用户 `/api/log/self` 拿不到 `admin_info`、打 config 接口 403 |
| `suites/image_primary.py` | 主端点落库去空白、空 `upstream_path` 跟随、显式覆盖保留、三入口落点、载体闸门 400 且**零出站**、chat 不抢图片主端点 |
| `suites/channel_select.py` | 多渠道选路：优先级分层独占、同优先级权重 3:1 分布、权重全 0 均分、`RetryTimes>0` 时降级到下一优先级、`RetryTimes=0` 不降级 |
| `suites/channel_affinity.py` | 渠道亲和：同一会话键 12 次全落同一渠道、第二个键各自粘住、关掉开关后恢复分散 |
| `suites/channel_health.py` | 渠道健康：开关关闭时 500 不禁用、开启后自动禁用（status=3）、批量测试成功自动启用（status=1）、人工禁用不被翻回、死渠道被批量测试判死 |
| `suites/multi_key_sticky.py` | 多 key 渠道：同一令牌只落一把 key、禁用 key 只重映射它自己的种子、`min_active_keys` 上限与备用补位、polling 轮转对照、重试不重复踩同一把、坏 key 只摘它自己 |
| `suites/auto_group.py` | 跨分组重试：分组顺序、组内优先级先用完、`cross_group_retry` 开关对照、全组失败时有界收敛、令牌 auto_groups 的四类约束 |
| `suites/affinity_override.py` | 亲和参数覆盖模板：模板只补空缺（渠道值优先）、`operations` 在渠道静态值之后执行、规则未命中/亲和关闭时完全不参与、日志 `override_template` 标记 |
| `suites/combo_group_multikey.py` | auto 分组 + 多 key 组合：组内换 key 不切组、组内 key 全坏才切组、跨渠道尝试与日志 `use_channel` 一致 |
| `suites/channel_select.py` | 多渠道选路：优先级分层独占、同优先级权重 3:1 分布、权重全 0 均分、`RetryTimes>0` 时降级到下一优先级、`RetryTimes=0` 不降级 |
| `suites/channel_affinity.py` | 渠道亲和：同一会话键 12 次全落同一渠道、第二个键各自粘住、关掉开关后恢复分散 |
| `suites/channel_health.py` | 渠道健康：开关关闭时 500 不禁用、开启后自动禁用（status=3）、批量测试成功自动启用（status=1）、人工禁用不被翻回、死渠道被批量测试判死 |
| `suites/multi_key_sticky.py` | 多 key 渠道：同一令牌只落一把 key、禁用 key 只重映射它自己的种子、`min_active_keys` 上限与备用补位、polling 轮转对照、重试不重复踩同一把、坏 key 只摘它自己 |
| `suites/auto_group.py` | 跨分组重试：分组顺序、组内优先级先用完、`cross_group_retry` 开关对照、全组失败时有界收敛、令牌 auto_groups 的四类约束 |
| `suites/affinity_override.py` | 亲和参数覆盖模板：模板只补空缺（渠道值优先）、`operations` 在渠道静态值之后执行、规则未命中/亲和关闭时完全不参与、日志 `override_template` 标记 |
| `suites/combo_group_multikey.py` | auto 分组 + 多 key 组合：组内换 key 不切组、组内 key 全坏才切组、跨渠道尝试与日志 `use_channel` 一致 |
| `suites/discount_pat.py` | 折扣首条命中且不相乘、按次定价对账、审计 before/after、PAT 声明式作用域（未声明路由 fail-closed） |
| `suites/mcp_protocol.py` | MCP 协议：入口鉴权三层（无/错/空令牌）、tools/list、models.list 三态、四个工具全链路、本地校验**零出站**、上游 5xx 的 key 脱敏、task_id 换 key 失效、观察落库不含提示词 |
| `suites/mcp_admin.py` | MCP 控制面：/api/mcp/* 全接口、普通用户 403、`max_ref_images` 三态、更新不改 `model_id`、规则播种与 match 不可改、面板页不内联凭证 |
| `ui/mcp_panel.mjs` | 真实 Chrome：登录态复用（refresh cookie 换 token）、列表/详情/保存/三态/启停/规则/观察/巡检、新建→删除闭环、三档视口 + 深色 + 未登录自解释 |
| `ui/trace_link.mjs` | 真实 Chrome：日志详情里出现「在 Langfuse 查看」，href 主机/项目/32 位 id/`rel=noopener` 全对 |
| `ui/observability_settings.mjs` | **手动运行**（未接入 `run_all.sh`）：设置页归位到「扩展」、状态条回显（开关/地址/正文模式/生效速率/时区偏移）、预设与边界值、脏状态深比较、规则增删与持久化、半截时间窗被拦、白名单权限、改采样率后 sink 的 span 数与日志页链接同步变化、三档视口/深色/无 JS 报错。依赖见文件头 |

## 手动套件：可观测性配置页

`ui/observability_settings.mjs` 需要 `run_all.sh` 没有的三样东西，所以单独跑：

```bash
# 1) 两个实例：A 开 trace（SAMPLE_RATE=10、TZ=Asia/Shanghai），B 关 trace。
#    两者都要起假上游与假 sink（fakes），A 的 FLUSH_INTERVAL_MS 调小到 300。
# 2) 两个账号：管理员 mgr(id=2) + 普通用户 alice(id=3)
E2E_BASE=http://127.0.0.1:39510 E2E_DB=/tmp/apiduo-observe/gw/one-api.db \
  python3 e2e/live/lib/observability_accounts.py
# 3) 跑套件（RELAY_KEY 用 bootstrap 打印的中继 key）
RELAY_KEY=... node e2e/live/ui/observability_settings.mjs
```

断言里有一条**必须用干净库**：初始元数据/内容采样要等于 `LANGFUSE_SAMPLE_RATE`
（证明默认值来自环境而不是写死的 100）。跑过一次保存后这条会红，重跑前清库重启。

## 环境前提（不满足会一片红，先看这条）

- **本地盘水位**：脚本已把 `performance_setting.monitor_disk_threshold` 设成 99。本机盘常年 97%+，
  不放开就是全站 503 `system_disk_overloaded`。
- **模型定价**：全新库没有定价。图片实例开 `SelfUseModeEnabled=true`；折扣套件自己关掉它并
  PATCH 一条 `ModelPrice`，所以它排在图片套件**之后**跑。
- **日志可见性**：写日志到 `/api/log/` 可查有 100–300ms 延迟，断言一律按 `id` / `request_id` 轮询收敛。
- **中继令牌**：用扁平 JSON 建（`{"name":...}`），套 `{"token":{...}}` 会建成 `status=4`（禁用），
  表现为 401 Invalid token。
- **渠道生效**：`SYNC_FREQUENCY=1`，脚本已设；判就绪不能用 `/v1/models`（无价模型不列出）。
- **内存缓存必须开**：`MEMORY_CACHE_ENABLED=true`（生产就是开的）。优先级与权重只在
  `GetRandomSatisfiedChannel` 的缓存选路里生效；不开缓存跑的是「直查库」另一份代码，测了等于没测。
- **套件顺序不能随便调**：`discount_pat` 会关掉自用模式且只给 `disc-model` 配价，
  之后的套件都会撞 `price not configured`。所以它排在最后。
- **每个渠道类套件自己重设磁盘水位并回读**：不要依赖上一个套件留下的实例状态。
- **亲和规则要写全字段**：配置是按字段合并进结构体的，`rules` 里省略 `path_regex` 会继承
  默认 codex 规则的 `/v1/responses`，规则就永远匹配不上。`channel_affinity.py` 里给的是完整对象。
- **自动启用只在批量/定时测试里发生**：单渠道测试 `/api/channel/test/:id` 只测不启，
  所以那条用例必须走 `/api/channel/test`（入队系统任务）。
- **`auto` 分组要显式放进 `UserUsableGroups`**：令牌分组校验读的就是这张表，
  只加 `vip` 不加 `auto` 时，请求会在选路之前就被 403 `No permission to access group auto` 挡掉。
- **第二台假上游用 `-fail-prefix=` 关掉了内容失败**：它代表「总是成功」的那台，
  否则跨分组用例里两组同时 500，看起来像重试没生效。
- **多 key 渠道用 `mode: "multi_to_single"` 建**，`key` 按行分隔；断言「这次用的哪把 key」
  只能看假上游记下的 `auth`（网关自报的 `multi_key_index` 只能用来交叉核对）。
- **重试预算是按组算的**：`RetryTimes=N` 时每个分组都有 N+1 次机会，两个组全失败就是
  2×(N+1) 次尝试，然后选路返回 nil。这是设计，不是泄漏。
- 🔴 **`GLOBAL_API_RATE_LIMIT` 必须放开**（默认 360 次/180s/IP）：所有套件都从 127.0.0.1 发，
  跑到第九套就会 429，症状是"登录失败 / 写入失败 / 令牌建不出来"，极容易被误判成产品缺陷。
  `run_all.sh` 已设成 200000。
- 🔴 **令牌按名字查要翻页**：`/api/token/` 单页最多 100 条，前面的套件建了几十个令牌后，
  新令牌会落到第二页；`find_token()` 逐页找，别只看第一页。
- **MCP 的视频渠道必须是 type=8001**（`ChannelTypeAdvancedAsync`），不是 64。写成 64 会被
  `invalid api platform: 64` 直接拒掉；`settings.advanced_async.incoming_paths` 复用 host 已有的
  `/v1/video/generations`，自定义路径会落到 SPA 首页。
- **MCP 的 `task.get` 打的是 `/v1/video/generations/:id`**，该端点回 `{code,message,data}` 包装
  （生成端点是扁平结构）。MCP 层必须先解包再读 `status`/`result_url`，否则任务永远读成 working。
  这条只有真网关能暴露 —— 进程内单测的 mock 一直是扁平形状。
- **异步任务靠系统任务轮询推进**（`UPDATE_TASK=true`，默认开，`async_task_poll` 每 15s）。
  提交后立刻查是 `NOT_START`，套件按 5s 间隔最多等 75s；别把「首次 working」当成 bug。
- **MCP 套件需要足够余额**：视频任务每次提交都预扣一次 per_call 费用（约 $18.75），
  反复回归几十次就会把初始额度吃光，表现为 `403 insufficient_user_quota` ——
  看起来像功能坏了，其实是测试环境耗尽。`mcp_protocol.py` 自己调 `/api/user/manage`
  充到 $500 以上，不必手工处理。
- **MCP 套件自己建渠道与声明**，跑完不删（下次跑先删同名再建）。UI 套件会改 `mcp-e2e-image`
  的 display_name 与 max_ref_images，跑完还原成 2；顺序是 mcp → mcpadmin → ui/mcp_panel。

- **参数覆盖模板的合并语义**：模板只补渠道没设过的键；要覆盖渠道已有的键，必须写成
  `operations`（渠道静态值先应用，模板 operations 后应用）。
- **内存缓存必须开**：`MEMORY_CACHE_ENABLED=true`（生产就是开的）。优先级与权重只在
  `GetRandomSatisfiedChannel` 的缓存选路里生效；不开缓存跑的是「直查库」另一份代码，测了等于没测。
- **套件顺序不能随便调**：`discount_pat` 会关掉自用模式且只给 `disc-model` 配价，
  之后的套件都会撞 `price not configured`。所以它排在最后。
- **每个渠道类套件自己重设磁盘水位并回读**：不要依赖上一个套件留下的实例状态。
- **亲和规则要写全字段**：配置是按字段合并进结构体的，`rules` 里省略 `path_regex` 会继承
  默认 codex 规则的 `/v1/responses`，规则就永远匹配不上。`channel_affinity.py` 里给的是完整对象。
- **自动启用只在批量/定时测试里发生**：单渠道测试 `/api/channel/test/:id` 只测不启，
  所以那条用例必须走 `/api/channel/test`（入队系统任务）。
- **`auto` 分组要显式放进 `UserUsableGroups`**：令牌分组校验读的就是这张表，
  只加 `vip` 不加 `auto` 时，请求会在选路之前就被 403 `No permission to access group auto` 挡掉。
- **第二台假上游用 `-fail-prefix=` 关掉了内容失败**：它代表「总是成功」的那台，
  否则跨分组用例里两组同时 500，看起来像重试没生效。
- **多 key 渠道用 `mode: "multi_to_single"` 建**，`key` 按行分隔；断言「这次用的哪把 key」
  只能看假上游记下的 `auth`（网关自报的 `multi_key_index` 只能用来交叉核对）。
- **重试预算是按组算的**：`RetryTimes=N` 时每个分组都有 N+1 次机会，两个组全失败就是
  2×(N+1) 次尝试，然后选路返回 nil。这是设计，不是泄漏。
- 🔴 **`GLOBAL_API_RATE_LIMIT` 必须放开**（默认 360 次/180s/IP）：所有套件都从 127.0.0.1 发，
  跑到第九套就会 429，症状是"登录失败 / 写入失败 / 令牌建不出来"，极容易被误判成产品缺陷。
  `run_all.sh` 已设成 200000。
- 🔴 **令牌按名字查要翻页**：`/api/token/` 单页最多 100 条，前面的套件建了几十个令牌后，
  新令牌会落到第二页；`find_token()` 逐页找，别只看第一页。
- **参数覆盖模板的合并语义**：模板只补渠道没设过的键；要覆盖渠道已有的键，必须写成
  `operations`（渠道静态值先应用，模板 operations 后应用）。

## 常驻开发实例（`bin/dev-e2e.sh`）

`run_all.sh` 是**一次性回归**：三个实例、临时库、跑完即焚。要「起一个能点的实例，
顺手跑几套」，用 `bin/dev-e2e.sh`：

```bash
bin/dev-e2e.sh                      # 起服务并前台守着，Ctrl-C 退出并清理
bin/dev-e2e.sh --all                # 起服务 + 跑全部单实例套件（10 套）
bin/dev-e2e.sh --suites multikey    # 只跑指定套件，逗号分隔
bin/dev-e2e.sh --all --keep         # 跑完不清理，留实例手点
```

数据落在 `DATA`（默认 `/tmp/apiduo-dev-e2e`）：SQLite、二进制、假上游抓到的报文、
各进程日志。**重启不丢账号和渠道**——bootstrap 是幂等的（同名渠道删了重建、令牌复用）。
默认入口 `http://127.0.0.1:39300`，`root / E2ePassw0rd!2026`。

| | `run_all.sh` | `bin/dev-e2e.sh` |
|---|---|---|
| 实例数 | 3（trace / image / off） | 1 |
| 数据库 | 临时，跑完删 | 持久 |
| 覆盖 | 13 套（含 `off` / `image`） | 10 套 |

只有 `run_all.sh` 能跑的两套：`off.py`（要求特性全关的专用实例）、
`image_primary.py`（要求带图片渠道 settings 的实例）。

持久数据目录会带来两个只有它才有的假红，脚本已处理：跑 `trace` 前清空 sink 目录
（否则旧 span 混进交叉核对，报「日志行指向错的 trace」）；`E2E_HOST_HINT` /
`E2E_SECRET_KEY` 必须跟着端口和密钥覆盖（套件里的默认值写死成 39402 / `sk-lf-e2e`）。

### 不在 CI 里的手动验证

`e2e/live/manual/auto_enable_tick.py` —— 验「自动禁用 → 自动启用」的两条路径
（手动批量测试 + 定时任务）。定时那条要等 scheduler 15s 一轮 **加上** Interval
（默认 1 分钟），实测 140s 才收敛；而且它依赖一个活着的假上游，假上游掉了会先把
所有渠道判死，后面每步都假红。所以只手动跑：

```bash
E2E_BASE=http://127.0.0.1:39300 python3 e2e/live/manual/auto_enable_tick.py
```

判据别看日志，看 `system_tasks.result`：

```json
{"tested":2,"succeeded":2,"failed":0,"disabled":0,"enabled":1}
```

`enabled` 就是 `ShouldEnableChannel` 真的返回 true 的次数。手动那条 1 秒收敛，
定时那条 140 秒。

## 只跑单个套件

实例在跑就能单跑：

```bash
E2E_BASE=http://127.0.0.1:39410 python3 e2e/live/suites/trace.py
E2E_BASE=http://127.0.0.1:39420 E2E_RELAY_KEY=sk-... python3 e2e/live/suites/image_primary.py
```

## 不覆盖

- **真实上游**：所有渠道都指向假上游。图片/视频生成走真上游就会计费，不在回归范围内。
- **生产环境**：需要管理员凭据；生产上的验证是另一套（见 `.workbuddy/memory/topics/observability.md`）。
- 并发/压测与 WebSocket realtime。
