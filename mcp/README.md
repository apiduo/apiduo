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
| `protocol.go` | 协议族表：`openai_image` / `openai_video`（生成端点 + 查询端点绑定） |
| `protocol_taskid.go` | HMAC 自包含任务 ID（无状态，跨副本可解） |
| `protocol_upstream.go` | apiduo `/v1` 客户端 + 错误脱敏 |
| `protocol_model.go` | `ModelProfile` → 工具视图（替代旧版的 catalog 多级合并） |
| `protocol_tools.go` | 4 个 MCP 工具：models.list / image.generate / video.generate / task.get |
| `protocol_server.go` | Streamable HTTP 装配、令牌注入 |
| `protocol_observe.go` | 观察埋点（异步队列，只记结构性参数） |
| `panel/index.html` | 面板页面，`go:embed` 打进二进制 |

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
| GET | `/mcp/admin` | 面板页面（公开，不含密钥） |
| GET | `/api/mcp/overview` | 顶部摘要 |
| GET | `/api/mcp/candidates` | 上游有、尚未声明的模型 |
| GET/POST | `/api/mcp/profiles` | 列表 / 新建 |
| GET/PUT/DELETE | `/api/mcp/profiles/:id` | 详情 / **部分更新（只写请求里出现的键）** / 删除 |
| POST | `/api/mcp/profiles/:id/toggle` | 启停（不重传整份声明） |
| GET | `/api/mcp/profiles/:id/observations?days=7` | 参数取值分布 |
| GET | `/api/mcp/profiles/:id/audit?days=7` | 声明与观察的差异 |
| GET | `/api/mcp/error-rules?model_id=` | 全局规则 + 模型覆盖 |
| PUT | `/api/mcp/error-rules/:id` | 更新规则 |

管理 API 全部挂 `middleware.AdminAuth()`。

## 5. 几条不能破的约定

1. **三态**：`max_ref_images` 是 `*int`。`nil` = 未声明（不拦截）、`0` = 明确不支持、`N` = 上限。
   `model_card` 在 `nil` 时不输出该键 —— 输出 0 会让 agent 以为不支持垫图。
2. **更新不改 `model_id`**：它是与渠道对齐的业务键，改名等于换模型，应新建一条。
3. **未匹配的错误默认不可重试**：重试的代价是计费。
4. **只暴露已注册且启用的模型**：未注册的模型 agent 不知道参数约束，放出去只会靠猜。
5. **MCP 层不认识厂商形状**：协议族只有 `openai_image` / `openai_video` 两套。
   新增厂商差异应该落在 apiduo 的渠道或 JS 插件里，不要往协议表里加第三套。
6. **观察只记结构性参数**：`size` / `duration` / `n` / 参考图张数。
   提示词内容与图片 URL 一个字都不落盘。
7. **观察写入不阻塞调用**：队列满就丢样本。丢一条观察数据可以接受，卡住生成请求不行。

## 6. 已修的坑（别再踩）

- **任务查询响应是包装形状**：`GET /v1/video/generations/:id` 回 `{code,message,data}`，
  而生成端点回扁平结构。`unwrapTaskBody` 只解一层 —— `data.data` 是上游原始响应，
  再解会让上游 `queued` 覆盖网关已落定的 `SUCCESS`。产物取 `data.result_url`，失败原因取 `fail_reason`。
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
