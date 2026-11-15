# apiduo/mcp · 待办优先级（2026-11-15）

> 这是把三份材料收敛成一张表的版本：`mcp-improvement-list-review.md`（对抗审查）、
> `mcp-primitive-gap-2026-11-15.md`（原语实测）、以及 2026-11-15 的决策（不做成本提示）。
> 只保留还成立的项，按「今天 / 本周 / 两周内 / 顺手」四档。

| 档 | ID | 做什么 | 规模 | 验收 |
|---|---|---|---|---|
| **已完成** | ~~T1~~ | ~~`task.get` 的 text content 补产物 URL~~ | 3 行 | 2026-11-15 完成，含失败原因与轮询提示 |
| **已完成** | ~~T2~~ | ~~`task.get` 失败走结构化错误~~ | ~20 行 | 2026-11-15 完成，`error` 由 string 改对象 |
| **今天** | T1 | `task.get` 的 text content 补产物 URL | 3 行 | 完成的视频，text 里能直接看到地址 |
| **今天** | T2 | `task.get` 失败走结构化错误：`taskOutput.Error` 由 `string` 改 `*toolError`，失败也回 `structuredContent` | ~15 行 | 伪造 `task_id` 返回 `structuredContent.error.code`，不再是纯中文 |
| **已完成** | ~~T3~~ | ~~`task_id` 签名密钥改系统密钥，绑 `UserId` + `ExpireAt`~~ | S | 2026-11-15 完成，含 `TASK_ID_EXPIRED` |
| **已完成** | ~~T4~~ | ~~生成类工具加 `client_request_id` + 幂等表~~ | M | 2026-11-15 完成，含在途/陈旧接管 |
| **已完成** | ~~T5~~ | ~~`models.list` 加 `catalog_version`~~ | S | 2026-11-15 完成，版本随 text 下发（未做 `_meta`） |
| **已完成** | ~~T6~~ | ~~`models.list` 快照缓存 + per-token 节流~~ | S | 2026-11-15 完成，写路径显式失效 + 1 s TTL |
| **两周内** | T7 | sync 自检进 CI：3 条 grep + `go test` + `markercheck -cost` | S | 合并 upstream 后流水线能拦住标记丢失 |
| **已完成** | ~~T8~~ | ~~README 三处：E1 表格截断、E3 标题措辞、E2 参考视频~~ | 分钟级 | 2026-11-15 完成；E2 改为「不再进 card」 |
| **已完成** | ~~T9~~ | ~~删 `logging` 能力声明~~ | 3 行 | 2026-11-15 完成，capabilities 只剩 `tools` |
| **已完成** | ~~T10~~ | ~~`n` 上限收到单一出口（含 `extra` 通道）~~ | S | 2026-11-15 完成，`checkBatchN` 统一判定 |
| 可选 | T11 | 观察 hook 化（注册点放 `relay/`，不放 `relaykit`） | M | `grep APIDUO: relay/` 不新增；直连 `/v1` 也有样本 |
| 可选 | T12 | MCP 维度指标（含 `getTask` 样本） | M | 有 `mcp_task_polls_total` 数据 |

---

## 为什么是这个顺序

1. **T1 是唯一「每天都在损失功能」的**：视频是唯一异步产物，完成的视频全部经过 `task.get`，而它的 text 只有「任务状态：completed」。只渲染 text 的客户端看不到地址 —— 这违反了 README 约定 9，也违反了改进清单 §0 自己列的优点。
2. **T2 与 T1 同一条链路**，一起收口最省事：同一个服务端有两套错误契约 —— `image.generate` 的 `outputSchema` 把 `error` 声明成对象（`code/message/retryable`，且失败时真的返回 `structuredContent`），`task.get` 把 `error` 声明成 **string**，而失败时连 `structuredContent` 都没有（`getTask` 直接 `return err`，SDK 包成 `isError:true` + 纯文本）。所以 T2 是**契约变更**：把 `taskOutput.Error` 从 `string` 改成 `*toolError`，并让失败也回 `structuredContent`。
3. **T3 / T4 是「账与句柄」**：T3 决定用户重置令牌后还能不能拿回在途任务（现在的答案是不能，且代码注释把这写成了「期望行为」）；T4 决定重复提交会不会双扣。两者互不依赖，可以并行。
4. **T5–T7 是可见性与防线**：都不改变行为，只让问题更早暴露。T5 是热更新的最便宜实现（20 行，任何客户端都吃）；T7 是合并 upstream 的唯一自动防线。
5. **T8 / T9 顺手做**，不要单独排期。

---

## 上线注意（T1–T6 之后新增）

### ~~T13 · `task_id` 换密钥的兼容读取~~（2026-11-15 决定不做）

用户明确「无需兼容旧任务旧令牌」。因此 T3 的破坏性是有意接受的：
上线瞬间的旧 `task_id` 全部失效，在途任务到控制台任务页取；滚动发布窗口里
跨副本的旧句柄同样会失败。**不需要**加按 `api_key` 回溯验签的分支。

### 上线后的验证清单

```bash
# 1. 完成的视频，text 里应有地址
# 2. 伪造 task_id，应回 structuredContent.error{code:TASK_ID_INVALID}
# 3. 同一 client_request_id 连打两次，上游只收到一次
# 4. models.list 应带 catalog_version（text 末尾「清单版本 ...」）
# 5. 同一令牌连打 61 次 models.list，第 61 次回 RATE_LIMITED
```

## 不做（已定，避免反复讨论）

| 项 | 理由 |
|---|---|
| `annotations`（readOnly / destructive / openWorld） | 2026-11-15 定：不差钱、不需要提示 |
| `input_required`（花钱前确认） | 同上 |
| G4「`ChannelId` 签入 `task_id`」 | 问题不存在：查询路径不选渠道、不打上游、只读 DB 快照 |
| 「`n` 无上限导致打光额度」 | 三层上限已存在（MCP 8 / 声明值 / relay 128），`n=1000` 到不了上游 |
| `resources` / `prompts` | 现在做是净负担：把动态清单变成可缓存对象就要管失效。仅当要做推送时再上 `resources` |
| `progress` / `sampling` / `roots` / `completion` / `tasks` | 前两个无推送通道，`completion` 不支持工具参数，`tasks` 这版 SDK 没实现 |

---

## 若要做推送（独立一条线，不在上面四档里）

前置条件按顺序：

1. `AddResource(apiduo://catalog)` + 空 `SubscribeHandler`（否则 `Resources.Subscribe` 不置 true，客户端的订阅会被静默剔除）
2. 写 profile 时 `Server.ResourceUpdated(ctx, uri)`
3. `/mcp` 响应加 `X-Accel-Buffering: no` + 30 s SSE 注释心跳（apiduo 目前两者都没有）
4. `~/.workbuddy/mcp.json` 的 `url` 从 `127.0.0.1:8081`（mitm 反代）改成直连 —— 实测经反代 12 s 零字节
5. 多副本：订阅表在进程内，写库那台不一定持流 → Redis pub/sub 扇出，否则只有单副本可用

**判断**：第 1 步之前先做 T5。因为典型节奏是每轮任务先调一次 `models.list`，热更新的收益窗口只有「拿旧清单连续调用」的那几分钟，而推送的运维成本要高一个量级。
