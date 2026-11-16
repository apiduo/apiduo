# 端点注册表：无 model 的端点也按同一张表路由

> 2026-11-16。已落地，与 `docs/apiduo/uapi-route-model-design.md`（端点维度）互补：
> 那篇解决「同一 model 的多个端点由谁服务」，本篇解决「端点本身没有 model 时怎么进入路由」。

---

## 0. 结论先行

1. **一个端点 = 一条 Spec。** 路径、路由名、端点类型、价格原来分散在四个上游文件里，
   现在从 `pkg/endpoint` 的一张表出。
2. **新增端点不再碰上游文件。** `/v1/files` 是第一个验证：它只增加了一条表项，
   `middleware/distributor.go`、`common/endpoint_type.go`、`common/endpoint_defaults.go`、
   `builtin_billing.go` 一行没动。
3. **`/v1/files` 的选路删掉了 110 行重复实现**，改为复用标准候选管道
   （`model.GetRandomSatisfiedChannel`），group / 优先级 / 权重 / 启用状态过滤从此只有一份。
   注意：这只保证「配置层面不可用」的渠道被排除（停用、自动封禁会让 abilities 行失效），
   不包含按请求失败反馈的健康度。
4. 合并面：**+1 个上游文件、+1 个标记块、+15 行**，`deleted` 未变。新增代码全部在
   `pkg/endpoint/`、`model/apiduo_endpoint_models.go` 等自有文件里，不参与合并。

## 1. 第一性原理：为什么必须造「虚拟 model」

渠道选择只能从 **model 字符串**起：`abilities` 主键是 `(group, model, channel_id)`，
而 `Distribute()` 在 `middleware/distributor.go:77` 对空 model 直接 400。

所以对 `/v1/tools/search`、`/v1/files` 这类请求体里没有 model 的端点，只有两条路：

| 路 | 代价 |
|---|---|
| 绕开 `Distribute()`，自己扫渠道表 | 重复实现优先级/权重/分组/启用过滤，且拿不到消费日志 |
| **由端点提供一个路由名**，走同一条管道 | 需要一个「名字」的出处 |

虚拟 model 不是权宜之计，它是把端点身份翻译成既有键的**唯一**方式。真正的问题从来不是
「要不要虚拟 model」，而是「这个名字由谁定义、在哪几处被读」。

重构前答案：四处硬编码。
重构后：一处表项。

## 2. 表

`pkg/endpoint/registry.go`：

| ID | Method + Path | 路由名 | 端点类型 | 计价 |
|---|---|---|---|---|
| `tools.search` | `POST /v1/tools/search` | `deepseek-search` | `deepseek-tools-search` | `fixed(0.00075)`/次 |
| `tools.fetch` | `POST /v1/tools/fetch` | `deepseek-fetch` | `deepseek-tools-fetch` | `fixed(0.00075)`/次 |
| `files.upload` | `POST /v1/files` | `@files/upload/<purpose>` | `file-upload` | 按 MB，handler 承担 |

### 命名规则

**新条目必须用 `@` 前缀。** 路由名同时是计费名：厂商若真的发布了同名模型，
这个名字会被静默改价、改路由，而且从模型列表上看不出来。

`deepseek-search` / `deepseek-fetch` 是例外：渠道已经在模型名单里列了它们，
改名会让 `/v1/tools/search` 在升级瞬间 503。表里明确标注为 legacy，不追溯。

## 3. 四个挂点，同一张表

| 上游文件 | 原形态 | 现形态 |
|---|---|---|
| `middleware/distributor.go:531` | `if strings.HasPrefix(path, "/v1/tools/search")` 链 | `endpoint.Match(method, path)` |
| `common/endpoint_type.go:9` | `switch modelName` 两个 case | `endpoint.EndpointTypes(model)` |
| `common/endpoint_defaults.go:34` | 端点类型 → 请求形状的静态 map | `endpoint.DefaultInfo(et)` |
| `setting/billing_setting/builtin_billing.go` | 两条价格字面量 | `init()` 合并 `spec.Price` |

四处都只是「查表 + 回退到原有逻辑」，各自一个标记块。表里查不到时行为与重构前完全一致，
所以未登记的模型类型不受影响。

## 4. `/v1/files`：从重复选路到复用

### 删掉的三段

| 位置 | 内容 |
|---|---|
| `model.GetFileChannelCandidates` | 每次上传全表扫 + `strings.Contains` 预筛 + JSON 解析 + `ServesFilePurpose` |
| `filerelay.SelectChannel` 内的 group 过滤 | 自己实现的 `channelServesGroup` |
| `weightedPick` / `effectiveWeight` | 自己实现的「最高优先级 + 权重随机」 |

### 保留的一件事

`MaxUploadBytes` 留在 `other_settings.file_relay`：它是**数字**，不是能力。
把上限塞进模型名会造出 `@files/upload/batch:200MB` 这种不可检索的名字。

### 前端

勾选框不再写 `file_relay.purposes`，改为在**模型名单**里增删 `@files/upload/<purpose>`。
`web/src/features/channels/lib/file-relay.ts` 里是纯字符串函数，组件只读 `form.watch('models')`。

理由：模型名单就是路由表。放在 settings 里的第二份能力声明，路由永远不读它，
坏掉也看不出来。

### 迁移

`model.MigrateFileUploadPurposes()`（`main.go` 启动时、建缓存之前调用）：

1. 扫 `other_settings LIKE '%file_relay%'` 的渠道；
2. 把 `file_relay.purposes` 逐个写成 `@files/upload/<purpose>` 追加进 `models`；
3. 删掉 `purposes` 键（其余键原样保留，走 `map[string]any` 重写，不经过类型化结构体）；
4. 有改动时调一次 `FixAbility()` 重建 `abilities`。

幂等：第二次启动时 `purposes` 已不存在，直接跳过。

## 5. 用户可见面

| 面 | 处理 | 理由 |
|---|---|---|
| `GET /v1/models` | **过滤** `@` 前缀 | 客户端只会拿模型名去 `/v1/chat/completions`，列出来就是给 SDK 制造一个调不通的名字 |
| `GET /api/channel/models_enabled` | **不过滤** | 运营要靠它把路由名配进渠道模型名单，藏了就没法配 |
| 定价页 | 暂不处理 | 见 §7 |

## 6. 对抗审查

1. **状态型读路径不可能注册表化。** `GET/DELETE /v1/files/:id`、`/:id/content` 的渠道路由键
   是 `FileObject.ChannelId`。给它们配 model 不是设计取舍，是正确性 bug：
   model 会选到不持有这份字节的渠道。表项 `Family: true` 的注释写明了这一点。
2. **`abilities` 里出现 `@` 开头的行。** `/v1/models` 已过滤，但定价页、渠道编辑页的模型下拉、
   `model_pricing_config` 相关后台仍会看到。要彻底干净需要给模型加 `virtual` 标记并贯通前端，
   本次没做。
3. **计费仍有两套形状。** tools 是 `fixed`/次，能走 `billingexpr`；files 是按 MB 预留 + 失败退款
   （`service/filerelay/quota.go`）。注册表统一了**路由**，没有统一**计费**。
   把 «按 MB» 表达成计费表达式需要请求字节数进入计价上下文，本次不动。
4. **令牌模型白名单口径不一致，但改动前后一致。** tools 走 `Distribute()`，受
   `TokenModelLimitAllows` 约束；files 不经过 `Distribute()`，不受约束。
   把 `@` 名字做成白名单豁免是另一个决定（等于允许「只用某模型的令牌」发起上传），本次不做。
5. **`@files/upload/<purpose>` 让 `abilities` 行数按 (渠道 × purpose) 增长。**
   purpose 上限 7 个，可接受，但运营看到的「模型总数」会变多，看板上会出现非模型条目。
6. **迁移会写 `channels` 表。** 语义是纯增量 + 删一个废字段，但仍建议先在本地库跑一遍
   （`deploy/local/up.sh` 起的 MySQL），确认没有渠道被改坏。
7. **`endpoint.Match` 是精确匹配，原来的 `HasPrefix` 不是。**
   `/v1/tools/search/xxx` 这种带尾巴的路径以前能命中，现在不会。没有客户端这么用。

## 7. 已知偏离与后续

1. **`middleware/distributor.go` 现在 4 个标记块，超过「单文件不超过 3 块」的约定。**
   该块替掉的是一条随端点数量增长的 if-else 链，仓库块总数只 +1。要回到 3 块，
   把两处 `playground-path-alias` 调用点收敛成单点即可（两处都在 `getModelRequest` 附近，
   各 2~3 行），建议与下一次上游同步一起做。
2. **定价页未过滤 `@` 条目。** 需要先决定它们是展示（带 virtual 徽标）还是隐藏。
3. **`/v1/files` 仍然不写消费日志。** 它在 `Distribute()` 之外。要修就得把上传并入管道，
   代价是 multipart body 在跨渠道重试时需要显式回放（`ParseMultipartForm` 已经把 body 吃掉了）。
4. **只读接口仍按 `FileObject.ChannelId` 回放**，这是终态，不要"优化"。

## 8. 怎么新增一个端点（操作手册）

1. 在 `pkg/endpoint/specs` 加一条：`Method`、`Path`、`Model`（`@` 前缀）、`Endpoints`、`Price`。
2. 若端点类型是新的，在 `relaykit/types/endpoint_type.go` 加常量，并在
   `constant/endpoint_type.go` 加别名。
3. 渠道侧把路由名加进模型名单（厂商 preset 则改它的 `ModelList`）。
4. **不要碰** `distributor.go` / `common/endpoint_type.go` / `common/endpoint_defaults.go` /
   `builtin_billing.go`。跑了这三步还要改它们，说明表项不够表达，先改 Spec。
5. 若端点是状态型的（创建后再按 id 读），**不要**给读路径配 model，按记录回放。

## 9. 验证

单元/路由测试：

```bash
go test ./pkg/endpoint/ ./router/ ./service/filerelay/ ./model/ ./controller/ ./common/ ./middleware/
cd web && bun run vitest run src/features/channels/components/__tests__/file-relay-setting.test.tsx
```

本地端到端（生产形态：MySQL 8.2 + ClickHouse 25.8 + 容器化应用 + 假上游）：

```bash
./.fixloop/e2e_endpoint_registry.sh            # 重建镜像后跑，16 条判据
./.fixloop/e2e_endpoint_registry.sh --no-build # 只跑断言
```

它验的是这条路走通而不是函数返回值：上传字节经假上游往返、`/v1/tools/search` 在
ClickHouse 里留下 `deepseek-search` 的计费行、旧 `purposes` 迁移成模型名、`/v1/models` 不暴露路由名。
判据与留痕见 `.fixloop/criteria-endpoint-registry.md` 与 `runs-endpoint-registry.ndjson`。

截图（bsk，控制台真实交互）见 `docs/apiduo/shots/`：
`file-relay-purposes.png`（用途勾选 + 中文说明）、`channel-model-list-routing-name.png`（模型名单里的 `@files/upload/file-extract`）。

`router/file_relay_*_test.go` 里的渠道改用 `channel.Insert()`（会写 `abilities`），
否则新选路读不到候选 —— 这本身就是新选路生效的证据：旧实现直接扫渠道表，不依赖能力行。
