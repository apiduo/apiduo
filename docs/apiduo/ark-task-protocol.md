# 方舟（Ark）任务协议 · 下游与上游形状

> 参考实现：`plugins/tasks/doubao/plugin.js`（Doubao 插件，v1.4.0）。
> 8001 的 ark 协议与它保持**同一套下游形状**：状态词汇、错误码、`id` 覆盖规则全部一致，
> 差别只在 8001 是「配置驱动的中继」，不内置方舟 schema 校验。

## 三条入口

客户端把 base URL 写成域名、带 `/v1`、或按方舟原生 `/api/v3`，三条都要能用：

| 入口 | 归属 | 何时走任务中继 |
|---|---|---|
| `POST/GET /contents/generations/tasks[/{task_id}]` | 网关静态路由（`router/apiduo_ark_task_alias.go`） | 恒走。base URL 只填域名的客户端打这条 |
| `POST/GET /v1/contents/generations/tasks[/{task_id}]` | 网关静态路由（同上） | 恒走，推荐写法 |
| `POST/GET /api/v3/contents/generations/tasks[/{task_id}]` | 插件原生路径 | 模型被 8001 渠道认领 → 抢在插件前；否则交给插件 |

🔴 裸 `/contents/...` 必须显式注册：不注册会落到 SPA 兜底，返回 **200 的 HTML**。
客户端把 200 当成功再去解 JSON，报错信息与真实原因完全对不上（实测过一次）。

查询侧按任务归属分派：`GET /api/v3/.../{task_id}` 只有在该任务 `platform=8001` 时才转任务中继，
插件任务仍由插件回答。

8001 渠道的上游路径（Ark 预设，`advanced-async.ts`）：

```
submit: /api/v3/contents/generations/tasks
query:  /api/v3/contents/generations/tasks/{task_id}
```

Ark 预设的 `incoming_paths` 就是上面三条（面板只展示，运维不用手填）。额外的自定义入口
仍可声明，也可以按业务分段：`/doubao/api/v3/contents/generations/tasks`。后者正是 Doubao
插件注释里「网关也在 `/doubao` 下服务同一套路径」的写法，匹配是逐字相等，biz 段不是通配符。

## 响应形状

### 提交

```json
{"id": "task_xxxx"}
```

方舟 SDK 只读 `id`。网关的通用提交响应还会带 `task_id / status / model / created_at`，
多字段对方舟 SDK 无害（未知字段被忽略）。

### 查询

以**上游最近一次查询结果**（`task.Data`）为基础，覆盖三处：

```json
{
  "id": "task_xxxx",              // 本地任务号，上游 id 不透出
  "status": "succeeded",          // 方舟词汇
  "content": {"video_url": "..."}, // 终态补齐本机结果地址
  "error": {"code": "InternalServiceError", "message": "..."} // 仅网关判定失败时
}
```

`model / usage / seed / resolution` 等上游字段原样保留。

### 状态优先级

```
本地 SUCCESS            → succeeded（即使上游 data 还停在 running）
本地 FAILURE + 上游终态  → 保留上游终态（cancelled / expired 不被改写成 failed）
本地 FAILURE + 上游在飞  → failed + error
其它                    → 上游 status 原样；缺失时按本地状态映射
```

上游 `cancelled` / `expired` 是本地状态枚举里没有的终态。若一律按本地映射成 `queued`，
客户端会永远轮询一个不再变化的任务 —— 这是与 Doubao 插件对齐的核心一条。

### 错误码

按 `docs.volcengine.com/docs/ark/error-codes`，与 Doubao 插件同一张表：

| HTTP | code |
|---|---|
| 400 | `InvalidParameter` |
| 401 | `AuthenticationError` |
| 403 | `AccessDenied` |
| 404 | `NotFound` |
| 408 | `RequestTimeout` |
| 429 | `AccountRateLimitExceeded` |
| 其它 | `InternalServiceError` |

错误信封：`{"error": {"code": "...", "message": "..."}}`，不是网关的 `{code, data}`。

## `callback_url` 被拒绝

方舟的 `callback_url` 会带**上游任务号**回调客户端，而网关只发放自己的 `task_xxxx`。
客户端拿回调里的号查不到任何任务，只会以为回调丢了。与 Doubao 插件一致，ark 协议收到
非 null 的 `callback_url` 直接 400：

```
callback_url is not supported; poll the task instead
```

`callback_url: null` 放行（方舟 SDK 可能显式置空）。images 协议不受影响。

## 不做什么

| 不做 | 原因 |
|---|---|
| 内置方舟请求体 schema 校验 | 8001 是中继：`model` 由路径认领隐式要求，`content` 形状交给上游判定，避免跟着方舟版本改校验 |
| 同步图片 `/api/v3/images/generations` | Doubao 插件有（Seedream 同步返回），8001 只接上游异步任务 |
| `draft_task` 改写 | 方舟新特性，等有真实调用再补 |
