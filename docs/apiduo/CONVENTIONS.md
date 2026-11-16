# apiduo 项目约定

本文件记录**上游代码形态不支持自动校验**、只能靠人遵守的约定。能写成测试的规则不放这里。

---

## 新增后台管理页：三处联动

后台管理没有统一收口。权限判定分散在导航配置与每个管理路由各自的 `beforeLoad` 里，
两处都不具备「漏写即报错」的能力——页面照常渲染，只有接口 403 会暴露。

新增一个管理页必须同时改三处：

| # | 位置 | 内容 |
|---|---|---|
| 1 | `web/src/hooks/use-sidebar-data.ts` | 条目放进 `id: 'admin'` 分组的 `items`，填 `requiredRole`（`ROLE.ADMIN` 或 `ROLE.SUPER_ADMIN`） |
| 2 | `web/src/routes/_authenticated/<segment>/index.tsx`（或 `route.tsx` 作布局级守卫） | `beforeLoad` 里判角色，不满足则 `throw redirect({ to: '/403' })` |
| 3 | `web/src/i18n/locales/*.json` | 七个语言文件补导航标题 key |

### 校验覆盖到哪一步

`web/src/routes/__tests__/admin-guard.test.ts` 以**导航配置为唯一事实源**，反查每个
admin 条目对应的路由根是否仍带角色判断。

- 漏第 2 步 → 测试失败，CI 拦截。
- 漏第 3 步 → `bun run i18n:sync` 会提示缺失 key。
- **漏第 1 步 → 无法被发现。** 页面没进 admin 分组，导航里没有入口，测试也不会检查它。
  这是当前形态的固有盲区，只能靠这条约定。

### 判据写法

新页面统一用范围判断 `role < ROLE.X`，不要用严格判等 `role !== ROLE.X`。
后者在新增角色层级时会把新角色挡在页面外（过度拒绝），届时需要逐页修改；
前者只需要新角色的枚举值大于阈值即可自动通过。

现存 4 个页面用的是 `!== ROLE.SUPER_ADMIN`（`system-info` / `task-plugins` /
`request-processing` / `system-settings`），且这四处的守卫是上游原有代码。
**不做批量改写**——为写法一致性去动 9 个上游文件，冲突成本高于收益。
等到确实需要新增角色层级、或上游同步触发这些文件冲突时，再顺手统一。

---

## 新接口只落四个前缀

上游路径不得移动、不得改语义。我们自己注册的路径必须落在其中之一：

| 前缀 | 语义 |
|---|---|
| `/api/agent/*` | Agent 自注册与凭证 |
| `/api/apiduo/*` | apiduo 管理面 |
| `/mcp` | MCP 协议端点 |
| `/v1/apiduo/*` | Agent 面转发入口（目前为空） |

**存量不迁移。** `/api/data/sk`、`/api/growth/*`、`/api/appearance` 已在用，迁移动前端和外部脚本，
换来的只是整齐。规则从下一个新接口开始生效。

判据：`grep -c 'apiduo\|agent' router/` 能数出我们加了什么，上游重构 `apiRouter` 时也不会撞路径。

## 无 model 的端点只能走 `pkg/endpoint` 注册表

请求体里没有 model 的端点（`/v1/tools/search`、`/v1/files`、将来同类）不能自己扫渠道表。
渠道选择的唯一键是 model，绕开它就要重新实现 group / 优先级 / 权重 / 启用过滤，而且拿不到消费日志。
注册表只做一件事：把端点身份翻译成一个路由名，让这类端点回到同一条管道。

| 要加的东西 | 加在哪 |
|---|---|
| 路径、路由名、端点类型、价格 | `pkg/endpoint` 的 `specs` 一条 |
| 新的端点类型常量 | `relaykit/types/endpoint_type.go` + `constant` 别名 |
| 渠道能不能服务它 | 渠道的**模型名单**（如 `@files/upload/<purpose>`） |

**不要**在 `middleware/distributor.go`、`common/endpoint_type.go`、`common/endpoint_defaults.go`、
`setting/billing_setting/builtin_billing.go` 里为某个端点加分支或价格字面量 —— 这四处只在注册表
查不到时回退到原有逻辑。

两条不可越过的边界：

1. **新路由名必须 `@` 前缀。** 路由名同时是计费名，与真实模型同名会被静默改价改路由。
2. **状态型端点的读路径不配 model。** 创建后按 id 读的接口（`GET /v1/files/:id`）路由键是记录里的
   `channel_id`；按 model 选路会选到不持有这份数据的渠道，那是正确性 bug 不是风格问题。

细节与取舍见 `docs/apiduo/endpoint-registry-design.md`。

## 新增条目不能插在上游两组之间

gofmt 把「空行分隔的连续行」当作一个对齐组。把一个新条目插进上游某组中间，会让该组的
最长 key 变化，从而**整组重新对齐** —— diff 里表现为「删 N 行 + 增 N 行」，那 N 行全部
被算成「删除了上游行」。

实测：`access_token_routes.go` 的 3 条 `/api/agent/keys` 插在 `/api/user/sessions` 与
`/api/user/access_tokens` 之间，制造 8 行假删除；移到文件末尾的 apiduo 分组后归零。

三条要求：

1. 新条目放在**自己那一组的末尾**，前后留空行（或紧接其他 apiduo 块）。
2. 不要插在两个上游条目之间，哪怕逻辑上「这里更相关」。
3. 改完自查：`git diff -U0 $(cat .apiduo/sync-base) -- <file> | grep '^-' | grep -v '^---'` 应为空。

同样的道理适用于结构体字段（Go 的字段对齐、TS 的对象字面量）。

## 组合三选一

| 能力形态 | 实现方式 | 合并面增量 |
|---|---|---|
| 会产生上游请求（花钱） | **HTTP 回环**打 `/v1/*` | 0 |
| 只读派生、聚合 | 进程内调 L0 导出函数 | 0 |
| 只加常量/类型/方法 | 同包新文件（`apiduo_*.go`） | 0 |
| 以上都不是 | 改旧行为 → 进预算 | 必须认账 |

**「会产生上游请求的一律回环」是硬规则。** 计费、额度、选路、重试、审计只能在 relay 里有一份实现；
两份逻辑漂移的那天，账对不上且找不到哪份错的。

回环三条纪律：身份透传不提权（`injectBearer` + `captureCaller`）、错误必须翻译成
`code / retryable / retry_action`、不得复制旧接口的业务规则。

## 单个上游文件的 apiduo 块不超过 3 个

超过就说明形态错了 —— 应该是注册表 + 统一渲染点，或者总线。现有超限的文件
（`channel-mutate-drawer.tsx` 23 块、`advanced-custom-editor-dialog.tsx` 17 块）是历史累积，
不追溯，但**不允许再往上加**；新增必须先注册表化。

---

## 相关文档

- `docs/apiduo/repo-blueprint.md` — **总纲**：第一性原理、分层归属、接口契约、成本指标、路线图
- `docs/apiduo/module-isolation-plan.md` — 上游同步的成本模型、挂接点分档与预算规则
- `docs/apiduo/endpoint-registry-design.md` — 无 model 端点的路由表、files 的 purpose 归属、新增端点手册
- `.apiduo/merge-budget.txt` — 合并面预算基线，由 `go run ./tools/markercheck -cost` 校验
