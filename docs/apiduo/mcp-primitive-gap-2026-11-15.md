# MCP 原语对照 · apiduo 实测

| 项 | 内容 |
|---|---|
| 实测入口 | `http://127.0.0.1:8081/mcp`（本机 mitm 反向代理 → `apiduo.cn`），凭据取自 `~/.workbuddy/mcp.json` |
| 实测时间 | 2026-11-15 15:46–15:58 |
| 计费 | **未创建任何生成任务**。只做了握手、`tools/list`、`models.list`，以及两条必定在本地被拒的 `image.generate`（`n=1000`、不存在的模型）。零出站、零扣费 |
| 结论 | 工具层完整度高于同机另一个 MCP（cutduo）。**缺的不是「更多原语」，而是三样便宜的协议元数据**：`annotations`、`title`、`task.get` 的结构化错误。真正缺的原语只有 `resources` / `prompts`；而在这个服务端已经支持的 **2026-07-28** 里，最该补的是 `input_required`（「花钱前先问一句」的协议化通道） |

> **2026-11-15 决策（覆盖下文排序）**：`annotations` 与 `input_required` **不做** —— 「不差钱、不需要提示」。
> 因此 §4 的 **P0-1 与 P1-3 撤销**。必要原语集收敛为 `tools` + `inputSchema` + `outputSchema` / `structuredContent`，
> 三项均已具备 ⇒ **原语层面无缺口**。剩余可做项只有 P0-2（`task.get` 结构化错误）与 P1-1（两个假能力声明），
> 二者都与成本无关，属于「schema 自洽」与「排障成本」。

---
---

## 0. 一个前提修正：服务端说的是 2026-07-28，不是 2025-06-18

`initialize` 回 `2025-06-18`，只说明「你要的这个版本我也支持」，不是服务端上限。拿一个它不认识的版本去握手（`2024-10-07`），它回的是 **`2025-11-25`** —— 比客户端请求的新 5 个版本。真正的上限要用新协议的无握手入口探：

```
POST /mcp
MCP-Protocol-Version: 2026-07-28
Mcp-Method: server/discover
{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{
  "io.modelcontextprotocol/protocolVersion":"2026-07-28",
  "io.modelcontextprotocol/clientCapabilities":{}}}}
```

应答：

```json
{"result":{"resultType":"complete",
 "supportedVersions":["2026-07-28","2025-11-25","2025-06-18","2025-03-26","2024-11-05"],
 "capabilities":{"logging":{},"tools":{"listChanged":true}}}}
```

所以：

1. **服务端已经在新协议上**，`Mcp-Method` 头路由、每请求 `_meta` 携带版本与客户端能力 —— 这正是 2026-07-28 的 stateless 形态。
2. 文档与本讨论里「stateless 所以做不了 X」的判断需要按 2026-07-28 重算：新协议把「服务端向客户端要输入」改成了 **`resultType: input_required` + `inputRequests` + `requestState`**，客户端补齐后**重发同一个请求**。不需要 SSE，不需要会话。
3. `logging` / `roots` / `sampling` 在 2026-07-28 已被 SEP-2577 标记废弃 —— 所以「补 logging」是错误方向，应该删声明。

### 附：复查官方规范后的两处修正（同日）

对照 `modelcontextprotocol/modelcontextprotocol` 的 `schema/2026-07-28` 与
`docs/specification/2026-07-28/basic/patterns/subscriptions.mdx`：

1. **`tools.listChanged: true` 不是假声明**。2026-07-28 用 `subscriptions/listen`
   建立长连接通知流（取代 HTTP GET 端点与 `resources/subscribe`），`list_changed`
   就发在这条流上。协议通道存在，只是 apiduo 的 4 个工具是静态的、永远不会触发。
   实测：直连 `POST /mcp`（`Mcp-Method: subscriptions/listen`）返回 200
   `text/event-stream`，并在首帧给出 `notifications/subscriptions/acknowledged`。
2. **`logging` 仍然是死的**。规范明确 `notifications/message` 与 `notifications/progress`
   只走「该请求自己的响应流」，不走 listen 流；JSONResponse 模式下没有这条流。
   加上 2026-07-28 已弃用，删声明依然正确。

新增的落地要求（规范原文）：流式响应 SHOULD 带 `X-Accel-Buffering: no`，
并周期性发 SSE 注释行做心跳。apiduo 目前两者都没有 —— 实测本机 8081 那层反代
12 秒零字节（流被缓冲吃掉），直连 `https://apiduo.cn/mcp` 正常。

---

## 1. 实测记录

| 探测 | 结果 | 解读 |
|---|---|---|
| `initialize` @2025-06-18 | `protocolVersion: 2025-06-18`，`capabilities: {logging:{}, tools:{listChanged:true}}` | 老客户端走握手，能力集里有两个可疑项 |
| `initialize` @2024-11-05 | 回 `2024-11-05` | 向下兼容到 2024-11 |
| `initialize` @2026-07-28 | 回 `2025-11-25` | 握手属于旧生命周期，新协议走 `server/discover` |
| `server/discover` | 成功，`supportedVersions` 含 2026-07-28 | 新协议可用 |
| `tools/list` | 4 个工具：`models.list` / `image.generate` / `video.generate` / `task.get`；**全部带 `outputSchema`**；**全部没有 `annotations`、没有 `title`** | 结构化输出这块做到了，元数据缺 |
| `models.list` | 5 个模型（4 图 + 1 视频 `happyhorse1.1`），卡片含 `parameters` / `cost` / `input.max_reference_images`；**没有 `catalog_version`** | 清单可读，但版本不可见 |
| `image.generate` `n=1000` | `isError:true` + `PARAM_OUT_OF_RANGE`，**带 `structuredContent.error`** | 本地拒绝生效，无出站 |
| `image.generate` 不存在模型 | `MODEL_NOT_FOUND` + `retry_action:call_models_list`，带 `structuredContent` | 同上 |
| `task.get` 伪造 id | `isError:true`，**无 `structuredContent`**，只有一句「task_id 无效或已过期」 | 两条工具的错误契约不一致 |
| `resources/list` | `{"resources":[]}` | 没有资源 |
| `resources/read` | `-32602 Resource not found` | 诚实 |
| `prompts/list` | `{"prompts":[]}` | 没有模板 |
| `completion/complete` | `-32601 method not found` | 未实现 |
| `logging/setLevel` | `{}` 成功 | **收下了，但永远不会有 `notifications/message`** |
| `ping` | `{}` | 正常 |
| `GET /mcp` | 405 | 符合 stateless（不得开 SSE 流） |
| `DELETE /mcp` | 405 | 无会话，正确 |
| `notifications/initialized` | 202 | 符合规范 |
| POST 不带 `Accept: text/event-stream` | **400** | 规范要求 **406**，小偏差 |
| 任一请求带 `_meta.progressToken` | 无任何 `notifications/progress` | 结构性做不到 |

---

## 2. 原语矩阵（对 2026-07-28）

| 原语 / 机制 | 规范状态 | apiduo 实测 | 判断 |
|---|---|---|---|
| `tools` | 核心 | ✅ 4 个，`inputSchema` + `outputSchema` 齐全 | 合格 |
| `tools.icons` / `title` | 核心元数据 | ⚠️ `title` 已补（2026-11-15）；`icons` 没有 | 体验项 |
| `tools.annotations` | 核心元数据 | ❌ 4 个工具全空 | **最贵的一处缺失**（见 §4 P0-1） |
| `outputSchema` / `structuredContent` | 核心 | ✅ 自动生成且形状合法；但 `generate` 的 `error` 是对象、`task.get` 的 `error` 是 string，两套契约 | 领先同机 cutduo，内部不统一 |
| `resources` | 核心 | ❌ 空 | 缺 |
| `resources/templates` | 核心 | ❌ 空 | 缺 |
| `prompts` | 核心 | ❌ 空 | 缺 |
| `completion` | 核心 | ❌ -32601 | **做了也没用**：`CompleteReference` 只支持 `ref/prompt` / `ref/resource`，不支持工具参数 |
| `logging` | 2026-07-28 已废弃 | ⚠️ 声明了、`setLevel` 返回成功、发不出消息 | 假能力，应删 |
| `notifications/tools/list_changed` | 核心 | ⚠️ 声明 `true`，stateless+JSON 无法推送 | 假能力，应改 `false` |
| `progress` | 核心 | ❌ 无法推送 | 结构性不可用，`poll_after_ms` 已替代 |
| `input_required`（MRTR / elicitation） | 2026-07-28 起 | ❌ 未使用 | **可用且高价值**（见 §4 P1-3） |
| `sampling` | 已废弃 | — | 不需要 |
| `roots` | 已废弃 | — | 不需要 |
| `tasks`（SEP-1686） | 新协议实验性 | ❌ | 当前 SDK 无相关类型，`task.get` 是唯一可行替代 |
| 缓存字段 `ttlMs` / `cacheScope` | 2025-11-25+ | ✅ 已返回 | ~~恒为 0~~ → 2026-11-15 起 `tools/list` 等静态结果给 5 分钟 |
| `ping` | 核心 | ✅ | — |
| 工具结果内容类型：`image` / `audio` / `resource_link` | 核心 | ❌ 只用 `text` | 产物卡片可升级（P2） |

---

## 3. 与同机 cutduo 的对比

| 维度 | apiduo | cutduo | 结论 |
|---|---|---|---|
| 协议版本 | 最高 2026-07-28 | 2025-03-26 | apiduo 领先 |
| `outputSchema` | ✅ 全部工具 | ❌ 无 | apiduo 领先 |
| `structuredContent` | ✅ `generate` | ❌ JSON 塞进 text | apiduo 领先 |
| `annotations` | ❌ | ❌ | 两家都缺 |
| 能力声明诚实度 | ⚠️ `logging` + `listChanged:true` 都发不出 | ✅ 只声明 `tools.listChanged:false` | **cutduo 更诚实** |
| 不支持的方法的应答 | `[]` / `-32602` | `-32601 不支持的方法` | cutduo 更明确 |
| 计费前预检 | ❌ 只能靠第一次真调用报错 | ✅ `cutduo_validate`（只校验不出片）、`cutduo_probe`（出单帧） | **apiduo 缺这一层** |
| `inputSchema` 严格度 | 宽松（`additionalProperties:true`，无 `required`） | 严格（`enum` + `required`） | 取舍不同：apiduo 要接受平铺参数，代价是客户端无法前置校验 |
| 工具数 | 4 | 7（含 capabilities / describe / validate / probe / gate） | cutduo 把「先查、再校验、再出片」拆成了工具 |

一句话：**apiduo 的协议实现更现代，cutduo 的工具语义更防错。** 前者缺元数据，后者缺结构化输出。

---

## 4. 缺什么（按性价比排序）

### ~~P0-1 · `annotations` 全缺~~（已撤销：不做成本提示）

规范里这四个 hint 的默认值是：`readOnlyHint: false`、`destructiveHint: true`、`openWorldHint: true`、`idempotentHint: false`。**不写注解不是「中性」，是「危险」**：

| 工具 | 当前隐含语义 | 应声明 |
|---|---|---|
| `models.list` | 可能破坏环境、面向开放世界、非只读 | `readOnlyHint: true`、`openWorldHint: false` |
| `task.get` | 同上 | `readOnlyHint: true`、`openWorldHint: false` |
| `image.generate` | 同上（与只读工具完全没有区分） | `readOnlyHint: false`、`openWorldHint: false`（域是自家渠道）、`destructiveHint: true`、`idempotentHint: false` |
| `video.generate` | 同上 | 同上 |

后果很具体：客户端若按 hint 决定「要不要人工确认」，会对 `models.list` 弹确认，而对 `image.generate` 没有任何额外提示 —— 正好反过来。这是**唯一一个补上就能被现有客户端立刻用起来**的缺口。

一处需要说清的语义妥协：**MCP 没有「会花钱」的 hint**。`destructiveHint` 的定义是「可能对环境做破坏性更新」，而生成图片严格说是 additive。把它当作「计费提示」是拉伸语义 —— 但在没有更好字段的前提下，`true` 是安全方向的错误（宁可多问一句），而「只读工具也被标成 destructive」是危险方向的错误。真要表达成本，只能靠 `description` 与 `instructions` 里的文字，或者 P1-3 的 `input_required`。

改法（`mcp/protocol_tools.go` `registerTools`）：

```go
mcpsdk.AddTool(s, &mcpsdk.Tool{
    Name:  "models.list",
    Title: "列出可用模型",
    Annotations: &mcpsdk.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &falseV},
    ...
}, d.listModels)
```

`idempotentHint` 这一项和幂等键是绑定的：等 G2 的 `client_request_id` 落地，`image.generate` 才能诚实地写 `true`；现在写 `false`。

### ~~P0-2 · `task.get` 的失败不走结构化通道~~（2026-11-15 已修）

实测：

```json
{"result":{"content":[{"type":"text","text":"task_id 无效或已过期（换过 api_key？）"}],"isError":true}}
```

同一个服务端的 `task.get`，`outputSchema` 把 `error` 声明成 **string**，而失败时连 `structuredContent` 都没有 —— 那个声明出来的 `error` 字段永远无法被返回。对比 `image.generate`（`error` 是对象，失败时真的带 `structuredContent`）：

```json
{"structuredContent":{"error":{"code":"MODEL_NOT_FOUND","retryable":false,
 "retry_action":"call_models_list"},"model":"...","status":"failed"}}
```

同一个服务端、同一份 schema 风格，两条工具的错误契约不一致。agent 在 `task.get` 上只能读中文猜「要不要重试」—— 而它恰好是唯一会持续轮询的工具。

改法：`getTask` 的三个 `return nil, taskOutput{}, err` 改成 `return d.fail(...)`，并把 `taskOutput.Error` 一起返回。

### P1-1 · 两个假能力声明

```json
"capabilities": {"logging": {}, "tools": {"listChanged": true}}
```

- `logging`：`setLevel` 收下返回 `{}`，但服务端是 JSONResponse + stateless，没有任何通道发 `notifications/message`；而且该能力在 2026-07-28 已废弃。
- `tools.listChanged: true`：工具是静态的 4 个，永远不会有变更通知；`GET /mcp` 返回 405 也证明没有推送流。

两者都来自 SDK 默认值（`server.go:661-676`：`opts.Capabilities == nil` 时默认给 `logging:{}`，有工具就加 `Tools:{ListChanged:true}`）。改法：

```go
&mcpsdk.ServerOptions{
    Instructions: serverInstructions,
    Capabilities: &mcpsdk.ServerCapabilities{
        Tools: &mcpsdk.ToolCapabilities{ListChanged: false},
    },
}
```

收益不是功能，是**排障成本**：客户端问「你为什么说能推日志却从不推」，现在没有答案。

### P1-2 · `resources`：能力清单目前只能靠工具调用获取

`apiduo://catalog` 的收益在本机就能验证：`models.list` 每次返回 5 个模型的全文摘要（约 1.5 KB），而 agent 每轮对话只需要知道「有哪些、贵不贵」。做成资源后：

- 可被客户端按 URI 缓存（配 `ttlMs`），不必每轮重拉；
- 可被 `@` 引用，用户能直接点开看；
- 与 `catalog_version` 是同一个问题的两端（版本要进 URI：`apiduo://catalog?v=<epoch>`，否则缓存会放大陈旧）。

### ~~P1-3 · `input_required`~~（已撤销：不做花钱前确认）

现在这条规则写在 `instructions` 里：

> If the request is vague, ask one clarifying question before spending money.

它依赖模型自觉。2026-07-28 提供了正式通道：工具返回 `resultType: "input_required"` + `inputRequests`（`ElicitParams`）+ `requestState`，客户端把用户答复带回来**重发同一个 tools/call**。SDK 已经支持（`CallToolResult.InputRequests` / `RequestState`），服务端一行都不用改传输层。

对 apiduo 的具体用法：`image.generate` 在「模型未指定 + 价格超过阈值」时返回一个 elicitation（选模型 / 确认花费），而不是直接扣费。这是**唯一一条能在 stateless 下实现「人工确认」的路径**，也是 cutduo 的 `gate` 想做而没做到的事。

### P2-1 · `prompts`

4 个模板（电商主图 / 改风格 / 16:9 概念视频 / 批量变体）。收益边际：模板本质是静态文本，客户端本地也能存；但它是「能力资产」的协议级表达，且 `completion/complete` 只对 prompt 参数有效 —— 想用补全，必须先有 prompts。

### P2-2 · 工具结果只用 `text`，没用 `resource_link` / `image`

`content` 是联合类型，可以用 `resource_link`（客户端渲染成卡片、可 `resources/read`）或 `image`（base64）。现在产物一律是 text 里的裸 URL。对只渲染 text 的客户端没差别，但对支持 `resource_link` 的客户端，产物会从「一段链接」变成「一张可点的卡片」。

### P2-3 · `title` 缺失 + `ttlMs` 恒为 0

- 工具的人类可读名：UI 里显示 `image.generate` 不如「生成图片」。
- `tools/list` 是静态的，却很诚实地标了 `ttlMs: 0`（不缓存）。给 300000 秒 + `cacheScope:"public"` 能省掉每个会话一次往返。需要设 `ServerOptions.SetCacheable`。

### 不建议做（附理由）

| 项 | 理由 |
|---|---|
| `progress` | JSONResponse 下无推送通道；`poll_after_ms` 已经比 progress 更适合「分钟级轮询」的语义 |
| `completion` | 规范只支持 prompt / resource-template 的参数，**不支持工具参数**；没有 prompts 之前做了也没用 |
| `sampling` / `roots` | 2026-07-28 已废弃；且网关没有「让客户端调模型」的场景 |
| `tasks` | 新协议有，但这版 SDK（v1.8.0）没有相关类型；等 SDK 跟进再评估，现在 `task.get` + 签名句柄是正确替代 |
| 补 `logging` | 已废弃。删声明，不要实现 |

---

## 5. 最小改动清单

| 文件 | 改动 | 规模 |
|---|---|---|
| `mcp/protocol_tools.go` `registerTools` | 4 个 `Tool` 加 `Annotations`（含 `ReadOnlyHint`）与 `Title` | ~20 行 |
| `mcp/protocol_tools.go` `getTask` | 3 处 `return ..., err` 改走 `d.fail` | ~6 行 |
| `mcp/protocol_server.go` | `ServerOptions` 显式给 `Capabilities{Tools:{ListChanged:false}}` | 3 行 |
| `mcp/protocol_tools.go` `listModels` | 响应加 `catalog_version`（全局 epoch，在写入路径 +1；不要用 `max(version)` 或 `max(updated_at)`，删行时不变） | ~10 行 |
| `mcp/protocol_server.go` | `AddResource(apiduo://catalog)` + `AddPrompt` ×4（可与 P1-2/P2-1 一起做） | ~80 行 |
| `mcp/protocol_tools.go` `generate` | 价格阈值 + `input_required`（P1-3） | ~40 行 |

前三条是半小时的量，且都能用本文 §1 的探测方法回归验证。

---

## 6. 复现方式

```bash
K="<取 ~/.workbuddy/mcp.json 里 apiduo 的 Bearer>"
U="http://127.0.0.1:8081/mcp"

# 能力声明
curl -sS --noproxy '*' -X POST "$U" -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' -H "Authorization: Bearer $K" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"p","version":"1"}}}'

# 新协议发现（需要三个头 + 两个 _meta 字段）
curl -sS --noproxy '*' -X POST "$U" -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' -H "Authorization: Bearer $K" \
  -H 'MCP-Protocol-Version: 2026-07-28' -H 'Mcp-Method: server/discover' \
  -d '{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}'

# 工具元数据（看 annotations / title 是否为空）
curl -sS --noproxy '*' -X POST "$U" -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' -H "Authorization: Bearer $K" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' | python3 -m json.tool | head -40
```

计费边界：以上全部只读。唯一会花钱的是 `tools/call` 到 `image.generate` / `video.generate` 且参数能通过本地校验 —— 本文所有生成类调用都用了必然被本地拒绝的参数（`n=1000`、不存在的模型）。
