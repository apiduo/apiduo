# apiduo/mcp · 能力注册控制面 + MCP 协议端点

把「模型能力声明」从 YAML / 插件代码搬到 apiduo 的管理面板，MCP 协议层按声明工作。
本目录同时是**与 new-api 上游解耦的边界**：合并 upstream 时只按第 2 节的清单核对。

## 1. 目录

| 文件 | 作用 |
|---|---|
| `models.go` | 两张表（`mcp_model_profiles` / `mcp_error_rules`）、迁移、默认规则种子 |
| `service.go` | 观察查询（读日志库）、巡检、ModelCard |
| `api.go` | 管理 API handler |
| `router.go` | `Register(engine)`：唯一的对外入口 |
| `protocol.go` | 端点表：按产物类型 `image` / `video` 绑定生成与查询端点（上游统一方舟形状） |
| `protocol_taskid.go` | HMAC 自包含任务 ID（无状态，跨副本可解） |
| `protocol_upstream.go` | apiduo `/v1` 客户端 + 错误脱敏 |
| `protocol_model.go` | `ModelProfile` → 工具视图（替代旧版的 catalog 多级合并） |
| `protocol_tools.go` | 4 个 MCP 工具：models.list / image.generate / video.generate / task.get |
| `protocol_server.go` | Streamable HTTP 装配、令牌注入 |
| `protocol_observe.go` | 观察埋点（异步队列，只记结构性参数） |

## 2. 与 upstream 的接缝（sync 时只查这两处）

| 位置 | 改动 | 标记 |
|---|---|---|
| `router/main.go` | import 一行 + `mcpserver.Register(router)` 一行 | `// APIDUO:mcp:BEGIN/END` |
| `model/log.go` | 新增导出函数 `EnqueueLog`（5 行），复用既有批量写入器 | `// APIDUO:mcp:BEGIN/END` |
| `go.mod` | 新增 `github.com/modelcontextprotocol/go-sdk` | 无标记（require 块由 go 管理） |
| 其他 | **无** | — |

建表不写进 `model/main.go` 的 `AutoMigrate` 列表，由本包 `Migrate()` 自己完成。

`model/log.go` 那 5 行是**唯一的例外**，且不可避免：观察埋点要写 `logs` 表，而写 `logs` 的正确入口
（`createLog`：补 `request_id` → 异步批量 → 同步回落）是包内私有的。绕过它直接 `LOG_DB.Create`
在 SQLite 上完全正常，到 ClickHouse 上有两条隐蔽故障：

| 绕过 createLog 的后果 | 为什么本地看不出来 |
|---|---|
| 每条观察单行 INSERT，CH 每次生成一个 data part，量上来 `Too many parts` | SQLite/MySQL 单行插入本来就正常 |
| `request_id` 为空，而线上 `logs` 是 `ReplacingMergeTree(ORDER BY created_at, request_id)`，同秒的行会被后台合并折叠 | 只有 CH 的 merge 会去重，SQLite 不会 |

所以宁可多一处标记，也不自己再造一套批量写入。

sync 后自检：

```bash
grep -n "APIDUO:mcp" router/main.go     # 两处标记都应存在
grep -n "APIDUO:mcp" model/log.go       # EnqueueLog 仍在（观察写入依赖它）
grep -n "modelcontextprotocol" go.mod   # 依赖仍在
go test ./mcp/... ./router/... -count=1
```

## 3. 数据流

```
MCP 客户端 ──POST /mcp──▶ tools ──▶ /v1（回环自调）──▶ 渠道 / 插件 / 上游
                            │
                            ├──写──▶ 观察队列 ──▶ model.EnqueueLog ──▶ logs.Other.admin_info.param_snapshot
                            │                      （补 request_id + 批量写，不单行直插）
                            │
面板 /api/mcp/* ──写──▶ mcp_model_profiles ──读──▶ tools（能力来源）
                            │
                       只读 ──▶ logs（观察 / 巡检）
```

自调走回环 `127.0.0.1:{PORT}/v1`，不经过 nginx。
**不改 relay**：渠道选择、计费、插件分发继续由既有代码负责，MCP 只做协议翻译。

## 4. API

| 方法 | 路径 | 说明 |
|---|---|---|
| ANY | `/mcp` | MCP 协议端点（`TokenAuthReadOnly`，空令牌 401） |

后台界面只有前端「系统设置 → 扩展 → MCP」一处；本包不再自带静态面板。
| GET | `/api/mcp/overview` | 顶部摘要 |
| GET | `/api/mcp/candidates` | 上游有、尚未声明的模型 |
| GET/POST | `/api/mcp/profiles` | 列表 / 新建 |
| GET/PUT/DELETE | `/api/mcp/profiles/:id` | 详情 / **部分更新（只写请求里出现的键）** / 删除 |
| GET | `/api/mcp/profiles/by-model?model_id=` | 按业务键取一条（读不到 404 + `MCP_PROFILE_NOT_FOUND`） |
| PUT | `/api/mcp/profiles/by-model?model_id=&expected_version=` | **按业务键幂等写入（agent 纳管）**，不存在则建、存在则 patch |
| POST | `/api/mcp/profiles/:id/toggle` | 启停（不重传整份声明） |
| GET | `/api/mcp/profiles/:id/observations?days=7` | 参数取值分布 |
| GET | `/api/mcp/profiles/:id/audit?days=7` | 声明与观察的差异；可落地的条目带 `suggestion`（数值给 `max`、字符串给 `enum`） |
| POST | `/api/mcp/profiles/:id/adopt` | **采纳一条巡检建议**：只传 `param`，`max`/`enum` 由服务端按当前样本重算 |
| GET | `/api/mcp/error-rules?model_id=` | 全局规则 + 模型覆盖 |
| PUT | `/api/mcp/error-rules/:id` | 更新规则 |

管理 API 全部挂 `middleware.AdminAuth()`（浏览器会话与 PAT 都收，PAT 需 `model:read` / `model:write`）。

### 按业务键 upsert

`PUT /api/mcp/profiles/by-model?model_id=openai/gpt-image-1`，body 与 `PUT /profiles/:id`
一样是 patch。给 agent 用，一次调用完成纳管；面板仍走 `:id`，不需要知道版本。

- **业务键放 query 不放路径**：模型 ID 常带斜杠（`openai/gpt-image-1`），
  路径段会被 net/http 解码成两段，`PathEscape` 也救不回来。
- **新建即启用**（`enabled=true`）：纳管通道的语义是「声明即上线」。
  更新路径不碰 `enabled` —— 被人手动停用的模型，agent 重跑 upsert **不会**自动恢复，
  要恢复得显式调 toggle。patch 里带 `enabled` 直接拒绝，不静默忽略：
  agent 传 false 却拿到 true，它会以为自己停用了。
- **乐观锁 `expected_version`**：省略 = 用读到的版本做栅栏；`0` = 要求不存在；
  `N` = 要求 `version == N`。不匹配返回 **409** + `code=MCP_PROFILE_VERSION_CONFLICT`
  + `data.current`（当前整条），调用方直接在 `current` 上重放 patch 即可。
- **不自动重试**：乐观锁的价值是让冲突可见。静默重试会把「你的写入被覆盖了」
  变成一次 200，agent 再也查不出来。
- `version` 每次写库 +1（含 toggle），所以它比秒级 `updated_at` 可靠 ——
  同一秒内的两次写入不会拿到同一个版本号。

**审计**（`audit_logs`，category=operation）：

| action | 何时 | params |
|---|---|---|
| `mcp.profile_create` | upsert 新建 | `model_id`、`after`、`via=upsert`、`version` |
| `mcp.profile_update` | upsert 更新 | `model_id`、`changed`（只含真正变的键）、`via=upsert`、`version` |
| `mcp.profile_conflict` | 409 拒绝 | `model_id`、`expected_version`、`current_version`、`exists`、`via=upsert` |

冲突那条是 `status=409 / success=false`，所以审计页按「结果=失败」能直接筛出来；
鉴权链路的 generic 兜底会被 `ContextKeyAuditLogged` 抑制，不会出现一条看不懂的重复记录。

每条都带 `token_ref`（PAT 指纹，`audit_logs` 的一级列）。审计页的「Token scope」
筛选因此能把某一把钥匙的全部写入捞出来；session 请求该列为空，表示没有令牌可归属。
`agent` 字段只是 `X-Agent-Name` / User-Agent 的声明值，**不可信**，归属一律看 `token_ref`。

指纹是 64 位十六进制，人读不了。`GET /api/audit` 返回时会把整页的 ref 批量还原成
`token_label`（令牌名）与 `token_agent_key`（agent key 名），详情弹窗展示这两项；
令牌已被删除时两者为空，只剩指纹 —— 「钥匙没了但记录还在」是正常形态。

```bash
# 首次纳管
curl -sS --noproxy '*' -X PUT \
  'https://apiduo.cn/api/mcp/profiles/by-model?model_id=openai/gpt-image-1&expected_version=0' \
  -H "Authorization: Bearer pat-xxxx" -H 'Content-Type: application/json' \
  -d '{"kind":"image","display_name":"GPT Image","max_ref_images":4}'
# → {"success":true,"data":{"created":true,"profile":{...,"version":1}}}
```

## 5. 几条不能破的约定

1. **三态**：`max_ref_images` 是 `*int`。`nil` = 未声明（不拦截）、`0` = 明确不支持、`N` = 上限。
   `model_card` 在 `nil` 时不输出该键 —— 输出 0 会让 agent 以为不支持垫图。
2. **更新不改 `model_id`**：它是与渠道对齐的业务键，改名等于换模型，应新建一条。
3. **未匹配的错误默认不可重试**：重试的代价是计费。
4. **只暴露已注册且启用的模型**：未注册的模型 agent 不知道参数约束，放出去只会靠猜。
5. **协议族只描述「入口形状」**：图片 `openai_image` 打 `/v1/images/generations`
   （方舟的图片接口本身就兼容这个形状），视频 `openai_video` 打
   `/v1/contents/generations/tasks`（方舟任务形状）。两者上游都是方舟，厂商差异
   由渠道与 JS 插件吸收 —— **不要往协议表里加第三套**。
   图片只有一个入口：`/v1/images/generations`，**文生图与图生图共用**（图生图靠 `image`
   字段），不存在第二套图片协议。上游方言（方舟档位 / OpenAI edits）是**渠道**的属性，
   由 `image_protocol` 在内部改写，对客户端不可见。
   `model_profiles.protocol` 因此只允许 `ark`：它不参与路由，也不该用来表达上游方言 ——
   把它标成 `openai` 会让「图片有两种协议」的假象成立。
6. **观察只记结构性参数**：`size` / `duration` / `n` / 参考图张数。
   提示词内容与图片 URL 一个字都不落盘。
7. **观察写入不阻塞调用**：队列满就丢样本。丢一条观察数据可以接受，卡住生成请求不行。
8. **错误必须可分类**：本地校验给稳定码（`PARAM_OUT_OF_RANGE` / `MODEL_NOT_FOUND` …），
   上游错误查 `mcp_error_rules` 翻译成 `retryable` / `retry_action` / `hint`。
   agent 靠这三个字段决定「重发、改参数还是充值」；纯文本它只能猜，猜错要计费。
9. **产物地址必须进 text content**：多数 MCP 客户端只渲染 text，`structuredContent`
   里的 URL 它们看不见。只给结构化输出会出现「agent 说生成了、用户拿不到图」。
10. **能力边界写进 `note`**：`1080P 存在但当前号池不可用` 这类事实表达不进 enum；
    note 随 models.list 下发，并在参数被拒时附在错误里。`content` 同理 ——
    agent 按方舟形状传 `content` 时直接拆成 prompt + reference_images，不报「未声明参数」。
11. **`model` 可省略，但必须回报默认**：不点名时回落第一个已启用模型，返回里写明
    「本次用了 X，可选 a/b/c；下次带上 model 就能指定」，`structuredContent` 带
    `model_defaulted`。硬性必填会把「画张图」变成「先查清单再画」——MCP 的用户是
    让 agent 帮忙做事的人，引导要放在结果里，不放在门槛上。
12. **`models.list` 的 text 必须是可读清单**：逐模型给出 id / kind / 一句简介 / 价格 /
    参数枚举，并标出哪一个是默认模型。只回「共 N 个模型」时，只渲染 text 的客户端拿不到
    任何选择依据。
13. **默认模型由 `priority` 决定**：排序固定为 kind → priority desc → id。
    不能靠 id 字典序 —— 新加一个名字靠前的模型会静默变成默认，而它可能贵 20 倍。
    `priority` 是产品决策，必须显式声明。

## 6. 已修的坑（别再踩）

- **任务查询响应有两种形状**：方舟形状 `GET /v1/contents/generations/tasks/:id`
  直接回任务对象 `{id,status,content:{video_url}}`；OpenAI 别名
  `GET /v1/video/generations/:id` 回 `{code,message,data}`。`unwrapTaskBody` 只解带
  `code:"success"` 的那层 —— `data.data` 是上游原始响应，再解会让上游 `queued`
  覆盖网关已落定的 `SUCCESS`。产物依次取 `content.video_url` / `data.result_url` /
  顶层 `video_url`，失败原因取 `fail_reason`。
- **保存声明不改启停**：`SaveProfile` 更新时强制沿用库里的 `enabled`，启停只走 `SetProfileEnabled`。
  bool 零值是 false，漏传会把模型静默停用，而界面显示「保存成功」。
- **模型 ID 不可改**：请求里带新 `model_id` 会被忽略，响应体回的也是库里那份。
- **提示词上限按 rune 校验**：声明了 `max_prompt_chars` 就必须拦，否则它只是一句注释。
- **更新是「只写出现的键」**：`UpdateProfile` / `UpdateErrorRule` 以库里的行做底稿，
  只覆盖请求真正给出的键，显式 `null` 才清成未声明。
  之前是全量 PUT：bool 零值是 false，漏传 `async` 会把异步模型改成同步、漏传 `retryable`
  会把可重试规则改成不可重试，两次界面都显示「保存成功」。

## 7. 已知限制

- **观察样本只覆盖经过 MCP 的调用**。绕过 MCP 直接打 `/v1` 的请求不会产生 `param_snapshot`。
  要全覆盖，需要把埋点下沉到 relay 层（下一版）。
- 观察日志的 `quota` 固定写 0：MCP 层的这条只用于统计，真实计费由 relay 完成。
  两条账并存，用 `other.admin_info.mcp=true` 标记区分。
- **`max_ref_videos` 只进 model_card，工具层还没有参考视频入参**。声明了也不会被校验，
  因为 `image.generate` / `video.generate` 目前只收 `reference_images`；要真支持视频生视频，
  得先给工具加 `reference_videos`（届时它必须像参考图一样按上限拦截）。
- 没有 `search.query` 工具：apiduo 的搜索端点尚未在本部署启用。
- 参数约束目前是 JSON 文本框，不是可视化行编辑器。
- AI 回填草稿、边界探测尚未实现。
