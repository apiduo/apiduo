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
| 假上游 #2（多渠道选路用） | 39404 | `UP2` |
| 假 sink #2（占位） | 39405 | `SINK2` |
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
| `suites/channel_select.py` | 多渠道选路：优先级分层独占、同优先级权重 3:1 分布、权重全 0 均分、`RetryTimes>0` 时降级到下一优先级、`RetryTimes=0` 不降级 |
| `suites/channel_affinity.py` | 渠道亲和：同一会话键 12 次全落同一渠道、第二个键各自粘住、关掉开关后恢复分散 |
| `suites/channel_health.py` | 渠道健康：开关关闭时 500 不禁用、开启后自动禁用（status=3）、批量测试成功自动启用（status=1）、人工禁用不被翻回、死渠道被批量测试判死 |
| `suites/multi_key_sticky.py` | 多 key 渠道：同一令牌只落一把 key、禁用 key 只重映射它自己的种子、`min_active_keys` 上限与备用补位、polling 轮转对照、重试不重复踩同一把、坏 key 只摘它自己 |
| `suites/auto_group.py` | 跨分组重试：分组顺序、组内优先级先用完、`cross_group_retry` 开关对照、全组失败时有界收敛、令牌 auto_groups 的四类约束 |
| `suites/discount_pat.py` | 折扣首条命中且不相乘、按次定价对账、审计 before/after、PAT 声明式作用域（未声明路由 fail-closed） |
| `ui/trace_link.mjs` | 真实 Chrome：日志详情里出现「在 Langfuse 查看」，href 主机/项目/32 位 id/`rel=noopener` 全对 |

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
