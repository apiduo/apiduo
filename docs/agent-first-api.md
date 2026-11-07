# Agent 接入方案

> 面向 Codex、WorkBuddy 等 Agent 工具。两条路线：**存量功能用已有 API 拼接**，**新功能按 Agent-first 设计**。

## 1. 两条路线

| 路线 | 适用对象 | 做法 | 成本 |
|---|---|---|---|
| **存量拼接** | 今天之前已经上线的接口（136 个路径） | 用既有 REST 组合出场景，必要时多步调用 | 零开发，Agent 侧多几次往返 |
| **新功能设计** | 今天起新增的功能 | 设计阶段就把 Agent 可用性做进去（第 8 节清单） | 每个功能多写约 40 行 |

**当前默认凭据：个人访问令牌（PAT，`nap_` 前缀）。** 一把 Agent 一把，scope 最小化 +
`allow_ips` 钉住出口 IP；创建一次后长期直接 `Authorization: Bearer`，不需要任何握手。
账号级令牌（legacy）只在临时排障时用——它不受 scope 限制、所有非 session 路由通吃，改密码后仍然可用，
**但部署后 30 天自动退役（见 2.4），本来就不能当长期方案**。Agent Key（Ed25519 握手）保留为
「连可复制秘密都不能接受」时的备选，不是默认路径。
选型见 2.2。

判断标准只有一条：**一个只持有令牌（PAT 或账号级访问令牌）的程序，能不能在无人值守的情况下完成这件事？**
不能，就说明设计还没到位——补文档没用。

---

## 2. 认证与权限模型

```
Authorization: Bearer <access_token>
```

四个必须知道的事实：

1. **令牌以 scope 授权，不以角色授权。** 角色决定"最多能授什么"，scope 决定"这个令牌能做什么"。
2. **令牌只对声明的路由生效。** 未在 `middleware/access_token_routes.go` 声明的路由一律 403 `ACCESS_TOKEN_ROUTE_UNDECLARED`——刻意的失败关闭，避免新接口默认对 Agent 开放。
3. **创建令牌必须人在场。** `POST /api/user/access_tokens` 同时要求浏览器会话和二次验证（口令/2FA/Passkey），返回 `AUTH_SESSION_REQUIRED` 或 `SECURITY_PROOF_REQUIRED`。**Agent 不能自助申请凭据**，这是安全设计而非缺陷。
4. **存在一份 OpenAPI 3.0.1 规范**：`docs/openapi/api.json`，136 个路径，`securitySchemes` 已含 `AccessToken1`，可直接喂给代码生成器。

### 2.1 两种令牌，能力完全不同

控制台里有两处"访问令牌"，别混：

| | 账号级访问令牌（legacy） | 个人访问令牌（PAT） |
|---|---|---|
| 位置 | 个人设置 → 账号安全 → 访问令牌（单条，可"重新生成"） | 个人设置 → 访问令牌（多条列表） |
| 前缀 | 无固定前缀 | `nap_` |
| 数量 | 每人 1 条 | 每人多条 |
| 权限 | **账号全权** | scope 子集 |
| scope 校验 | **跳过** | 强制执行 |
| 未声明路由 | **放行** | 拒绝 `ACCESS_TOKEN_ROUTE_UNDECLARED` |
| 轮换 | 影响所有使用者 | 单条独立 |
| **改密码后** | **仍可用**，只能等 30 天过期或手动清空 | **立即失效**（绑定签发时的 `auth_version`） |
| 审计 | 只知道"某账号"，分不清哪个 Agent | `token_ref` 区分到每一把 |

判定逻辑在 `middleware/auth.go`：不以 `nap_` 开头即 `lookup.legacy = true`，而路由判定里 `case lookup.legacy: return true` 排在 scope 与声明检查**之前**——所以它连"没声明过的路由"都能调。

**唯一限制**：session 路由（如创建 PAT、改密码）仍返回 `AUTH_SESSION_REQUIRED`，legacy 也进不去。

### 2.2 该给 Agent 哪一种

| 场景 | 选择 | 理由 |
|---|---|---|
| 一个 Agent、受控机器、固定出口 IP（**默认**） | PAT | 一次创建长期直用，scope + `allow_ips` 就能圈住 |
| 多个 Agent、或交给别人跑 | PAT | 一 Agent 一把钥匙，泄漏只影响一条 |
| 需要最小权限（只读巡检 / 只发密钥） | PAT | scope 能精确到 `api_key:write` |
| 需要单独吊销某个 Agent | PAT | legacy 只能整账号重生成 |
| Agent 跑在不可信机器、怕配置文件被抄走 | Agent Key | 私钥不出本机，服务端只存公钥 |
| 临时排障、只想尽快跑通 | 账号级令牌（legacy） | 零配置、所有接口通吃，**用完记得清空** |

一句话：**默认发 PAT；legacy 是临时手段；Agent Key 是「可复制秘密也不能接受」时的退路。**

PAT 的三个硬约束（配置前先算清）：

- 每人最多 **20 把**（`AccessTokenMaxPerUser`），多 Agent 要算着分
- 最短有效期 **1 小时**；`expires_at=0` 表示永不过期，建议 ≤90 天。**改密码会让全部 PAT 立即失效**
- `allow_ips` 只在「服务端口只给反代」时成立：compose 必须是 `127.0.0.1:39047:3000`，
  写成裸 `39047:3000` 后私网主机可伪造 `X-Forwarded-For` 绕过白名单

### 2.2.1 三档最小 scope（可直接照抄）

**档 1 · 只读巡检（默认就选这个）**

```
profile:read
usage:read
log:read
```

读自己的资料、分组、模型列表，以及自己的用量与日志。定时报表、健康巡检、容量观察都够用。
不能跨用户读、不能改配置、不能创建密钥。

**档 2 · 代发与轮换 API 密钥**

```
profile:read
api_key:read
api_key:write
```

列出、创建、删除 API 密钥（sk），适合「Agent 给自己或子租户发钥匙」。

🔴 **不要加 `api_key:reveal`**：它能让 Agent 读出完整 sk 明文，等于把"持有者"变成"可以传播密钥的人"。

**档 3 · 全站只读（运维巡检，谨慎）**

```
profile:read
usage:read
log:read
user:read
billing:read
model:read
deployment:read
group:read
```

以管理员视角读全站用户、账单、模型、部署与分组。泄漏面从「一个人」变成「整个站点」，
只在确有巡检需求时给，且把 `expires_at` 压到 30 天内。

**不要给 Agent 的（给了等于交出站点）**

```
option:read / option:write     系统设置：支付、SMTP、渠道定价
nav_app:write                  导航入口
plugin:write                   任务插件
ops:write                      运维操作
account_security:write         改密码 / 2FA / 绑定
```

依据是 2026-11-07 的线上实测：绑定 root 账号的 PAT 若含 `option:write`，
`PATCH /api/option/model_pricing` 能**通过 scope 检查**（返回 400 是「空变更」的业务校验，不是权限拒绝）。

### 2.2.2 三个最容易「以为配了其实没配」的字段

| 你以为 | 实际判据 |
|---|---|
| 配了 IP 白名单 | 列表里 `allow_ips` 有值。**空值会被省略**，看不到就是没配 |
| 设了过期时间 | `expires_at` 是具体时间戳；`0` 表示**永久**，而且到期前不会有任何告警 |
| 限制了权限 | 拿它打一个越权路由，确认返回 403 `ACCESS_TOKEN_SCOPE_DENIED`，而不是 200 |

默认有效期是 30 天（`AccessTokenDefaultExpiryDays`），最短 1 小时；填 `0` 就是永久。

### 2.3 把令牌钉在网络上（推荐的防泄漏手段）

令牌可以被限制为**只能从指定地址段使用**。这是对付"密钥被复制到别处"成本最低的手段：密钥本身还有效，但从错误的地方用不了。

**配置**（控制台：安全与访问 → 访问令牌 → 创建/编辑）

```
允许的地址（可选）
10.0.0.0/8
203.0.113.7
```

每行一个地址或 CIDR 网段，留空表示不限制（即现有行为，升级后不变）。

**API 字段**

| 位置 | 字段 | 说明 |
|---|---|---|
| 创建 | `allow_ips` | 字符串，换行或逗号分隔；缺省不限制 |
| 更新 | `allow_ips` | `null`/缺省 = 保持现状；`""` = 清除限制 |
| 列表 | `allow_ips` | 只读回显 |

**错误码**

| 状态 | code | 含义 |
|---|---|---|
| 400 | `ACCESS_TOKEN_IP_INVALID` | 条目不是合法地址或 CIDR（**保存时**就拒绝，不会存进去） |
| 400 | `ACCESS_TOKEN_IP_TOO_LARGE` | 超过 128 条或 4096 字节 |
| 400 | `ACCESS_TOKEN_IP_TOO_BROAD` | 用了 `0.0.0.0/0` 或 `::/0` |
| 403 | `ACCESS_TOKEN_IP_DENIED` | 令牌有效，但当前地址不在白名单 |

`403` 而不是 `401` 是刻意的：凭据没问题，是来源不对。这样 Agent 不会去重建一把本来有效的令牌。

**两个上限都来自同一个原因**：白名单在**每个使用该令牌的请求上**被解析一次，开销随条目数线性增长（实测 10 条 ≈ 2 µs、1000 条 ≈ 160 µs、20000 条 ≈ 2.9 ms）。这段成本落在认证路径上，连 404 也要付。128 条 / 4096 字节对真实部署绰绰有余。

**为什么拒绝 `0.0.0.0/0`**：它看起来像"允许所有"，实际只覆盖 IPv4——`0.0.0.0/0` 放行任意 IPv4 而拒绝一切 IPv6，`::/0` 正相反。双栈部署下写这一条会静默锁死另一个地址族。"不限制"只应有一种写法：**留空**。

> ⚠️ **部署须知：白名单的强度取决于服务端能不能拿到真实来源地址**
>
> 地址判定读 `c.ClientIP()`，而它的取值规则由 `TRUSTED_PROXIES` 决定。默认值信任回环和全部私网段（`127.0.0.0/8`、`10.0.0.0/8`、`172.16.0.0/12`、`192.168.0.0/16`、`fc00::/7`），这是为"同机 nginx / Compose 反代"设计的。
>
> 由此有两条边界，实测确认过：
>
> | 场景 | 结果 |
> |---|---|
> | 公网客户端直连，带伪造 `X-Forwarded-For` | **伪造被忽略**，按真实地址判定 ✅ |
> | 私网内主机直连服务端口，带伪造 XFF | **伪造生效**，白名单失效 ❌ |
> | 最后一跳是可信反代，客户端预置伪造值 | **伪造被穿过**，取到真实地址 ✅ |
>
> 所以真正要管的不是"必须改这个变量"，而是**服务端口对谁开放**：
>
> **① 最优：端口只给反代。** 容器端口绑回环，内网机器连不进来，默认值就够。
>
> ```yaml
> ports:
>   - "127.0.0.1:3000:3000"     # 只有本机 nginx 能连
> ```
>
> **② 端口必须对外：把可信代理钉死。** 这样转发头只从那一跳采信。
>
> ```bash
> TRUSTED_PROXIES=127.0.0.1        # nginx 与本体同机
> TRUSTED_PROXIES=10.0.0.5         # nginx 是独立内网机
> TRUSTED_PROXIES=none             # 没有反代、客户端直连：完全忽略转发头
> ```
>
> **③ nginx 侧必须追加，不能透传：**
>
> ```nginx
> proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;   # ✅ 追加真实地址
> # proxy_set_header X-Forwarded-For $http_x_forwarded_for;      # ❌ 直接透传客户端值
> ```
>
> 判定逻辑是标准的：从右往左取第一个不可信地址。只要最后一跳可信，客户端预置的伪造值会被穿过。
>
> `docker-compose.yml` 里这两处都写了对应注释，见 `APIDUO:token-ip-allowlist` 标记块。

### 2.4 三种凭据的取舍

| | 管理员令牌（legacy） | PAT + IP 白名单 | PAT + 公钥签名 |
|---|---|---|---|
| 配置成本 | 零 | 低（一个字段） | 高（密钥对、交换流程） |
| 防"复制到别处" | ❌ | ✅ | ✅ 除非私钥被导出 |
| 防"本机被入侵" | ❌ | 部分（限来源网段） | 部分（仍需本机可签） |
| 改动上游文件 | — | 1 个字段 + 2 处挂接 | 1 张表 + 8 处挂接 |
| 适合 | 过渡期 | **主力** | 高价值、少变更的自动化 |

**建议默认走 PAT + IP 白名单**：它用最小改动拿到"泄漏后不能用"的核心收益。公钥签名只在"需要第二因子"或"来源 IP 不固定"时才值得那份复杂度。

> ⚠️ **硬期限：管理员令牌在部署后 30 天自动失效。**
> 首次启动会把截止时间写进 `LegacyAccessTokenRetireAt`（`now + 30 天`），到期后 `ValidateAccessToken`
> 直接返回 `legacy access token has been retired`，所有用它接入的 Agent 立刻断掉，且**不可续期**（该 option 由服务端管理，不允许手改）。
>
> 所以"先用管理员令牌、以后再上 PAT"实际只有 30 天窗口。到期前必须把 Agent 换成 `nap_` 开头的 PAT，
> 并把 `X-Agent-Name` 一起带过去（审计字段不变）。
>
> 反过来说，PAT 是唯一能被"改密码"一键止损的凭据：每个令牌记录签发时的 `auth_version`，
> 账号版本一变（改密码、强制下线、安全操作），所有旧令牌同时返回 `ACCESS_TOKEN_REVOKED`。

---

## 3. 存量接口清单（拼接用）

### 3.1 读取

| 方法 | 路径 | scope | 用途 |
|---|---|---|---|
| GET | `/api/status` | 公开 | 版本、注册开关 |
| GET | `/api/user/self` | `profile:read` | 当前身份、角色、余额 |
| GET | `/api/models` | `profile:read` | 可用模型目录 |
| GET | `/api/token/` | `api_key:read` | API 密钥列表（掩码） |
| GET | `/api/token/:id` | `api_key:read` | 单个密钥详情 |
| GET | `/api/nav_apps/all` | `nav_app:read` | 导航入口全量 + revision |
| GET | `/api/option/` | `option:read` | 全部系统设置 |
| GET | `/api/option/model_pricing` | `option:read` | 模型定价 |
| GET | `/api/channel/` | `channel:read` | 渠道列表 |
| GET | `/api/log/stat` | `log:read` | 全站用量统计 |
| GET | `/api/log/self/stat` | `usage:read` | 本人用量 |

### 3.2 更新

| 方法 | 路径 | scope | 用途 |
|---|---|---|---|
| POST | `/api/token/` | `api_key:write` | 创建 API 密钥 |
| PUT | `/api/token/` | `api_key:write` | 修改密钥 |
| PUT | `/api/user/self` | `profile:write` | 修改本人资料 |
| PUT | `/api/nav_apps` | `nav_app:write` | 全量替换导航入口（带 revision） |
| PUT | `/api/option/` | `option:write` | 修改单个配置项 |
| PATCH | `/api/option/model_pricing` | `option:write` | 批量改定价 |

### 3.3 执行

| 方法 | 路径 | scope | 用途 |
|---|---|---|---|
| POST | `/api/token/:id/key` | `api_key:reveal` | 取回明文密钥 |
| GET | `/api/channel/test/:id?model=` | `channel:operate` | 测试渠道连通性 |
| POST | `/api/channel/:id/key` | `channel:secret_view` + 二次验证 | 读渠道密钥（**Agent 不可用**，见 4.3） |
| POST | `/api/option/rest_model_ratio` | `option:write` | 重置模型倍率 |
| DELETE | `/api/option/channel_affinity_cache` | `option:write` | 清空渠道亲和缓存 |
| POST | `/api/plugin/task/:key/activate` | `plugin:write` | 激活任务插件 |

> 完整目录：浏览器会话下 `GET /api/user/access_tokens/scopes`。漏授权时错误会直接告诉你缺哪个 scope。

---

## 4. 权限说明

### 4.1 三组 scope

| 组 | 资源 | 最低角色 |
|---|---|---|
| 个人 | `profile` `api_key` `usage` `wallet` `account_security` | 普通用户（1） |
| 管理 | `user` `billing` `model` `deployment` `log` `redemption` `group` `channel` | 管理员（10） |
| 系统 | `nav_app` `option` `plugin` `ops` | 超级管理员（100） |

动作：`read` / `write`，外加专用动作 `api_key:reveal`、`channel:operate`、`channel:secret_view`、`channel:sensitive_write`。

授权不能越过自身权限：申请超出自己持有范围的 scope 会被拒（`ACCESS_TOKEN_SCOPE_FORBIDDEN`）。

### 4.2 最小授权建议

| Agent 职责 | 建议 scope |
|---|---|
| 只读巡检 | `channel:read` `log:read` |
| 自助建号 | `api_key:read` `api_key:write` |
| 密钥交付 | 追加 `api_key:reveal` |
| 改站点配置 | `option:read` `option:write` |
| 改导航入口 | `nav_app:read` `nav_app:write` |

能拿明文凭据的 scope（`api_key:reveal`、`channel:secret_view`）单独授予并设短有效期。

### 4.3 二次验证拦截的接口

`POST /api/channel/:id/key` 除了 `channel:secret_view`，还挂了安全验证：必须先由用户完成口令/2FA/Passkey，服务端签发一次性 proof 后才放行。

**Agent 无法单独完成**。设计自动化流程时把它排除，或由人在会话内手动取值。

```json
{ "success": false, "code": "SECURITY_PROOF_REQUIRED", "message": "Additional verification required" }
```

---

## 5. 错误码

| HTTP | code | 含义 / 处理 |
|---|---|---|
| 401 | `AUTH_UNAUTHORIZED` | 缺少或无效令牌 → 重新注入 |
| 401 | `AUTH_TOKEN_EXPIRED` | 令牌过期 → 让人重建 |
| 401 | `ACCESS_TOKEN_REVOKED` | 签发后账号凭据变更过（改密码 / 强制下线）→ 重新签发 |
| 403 | `ACCESS_TOKEN_SCOPE_DENIED` | 缺 scope，`message_params.scope` 给出名字 → 换令牌，别重试 |
| 403 | `ACCESS_TOKEN_ROUTE_UNDECLARED` | 该路由未对令牌开放 |
| 403 | `AUTH_SESSION_REQUIRED` | 只接受浏览器会话 |
| 403 | `AUTH_INSUFFICIENT_PRIVILEGE` | 账号角色不够 |
| 403 | `SECURITY_PROOF_REQUIRED` | 需用户在场完成二次验证 |
| 400 | `ACCESS_TOKEN_SCOPE_INVALID` / `_FORBIDDEN` | 建令牌时的 scope 非法 / 越权 |
| 400 | `ACCESS_TOKEN_NAME_INVALID` / `_EXPIRY_INVALID` | 名称 1–64 字符 / 有效期 ≥1 小时 |
| 404 | `ACCESS_TOKEN_NOT_FOUND` | 令牌不存在 |
| 409 | `ACCESS_TOKEN_LIMIT` | 令牌数量超上限 |
| 429 | —（空响应体） | 限流，`Retry-After` 给出等待秒数 |

约定：HTTP 状态码表达类别，`code` 是稳定标识，`message_key` 可多语言渲染。**按 `code` 编程，不要解析 `message`。**

---

## 6. 接入步骤

**① 人取凭据**（浏览器，一次性）：个人设置 → 账号安全 → 访问令牌 → 重新生成，复制完整值交给 Agent。
（将来要做最小权限时，改走「个人设置 → 访问令牌」创建 `nap_` 开头的 PAT 并勾选 scope。）

**② 注入 Agent**

```bash
export NEWAPI_BASE_URL="https://api.example.com"
export NEWAPI_ACCESS_TOKEN="nap_xxxxxxxxxxxxxxxx"
```

**③ 连通性探测**（顺便带上 Agent 名，从第一条请求就开始留痕）

```bash
curl -s "$NEWAPI_BASE_URL/api/user/self" \
  -H "Authorization: Bearer $NEWAPI_ACCESS_TOKEN" \
  -H "X-Agent-Name: codex-refactor"
```

`success: true` 且 `data.role` 符合预期即就绪。

**④ 按需调用**：先读后写；写操作前先读一次拿到 revision。

**⑤ 轮换**：有效期建议 ≤90 天；删除用 `DELETE /api/user/access_tokens/:id`（需浏览器会话）。

---

## 7. Agent 审计日志

两个记录点，带令牌的请求都会经过：

| 类别 | 什么时候写 | 关键字段 |
|---|---|---|
| `access_token` | **每次**令牌调用（成功或失败） | `token_ref`、`status`、`ip`、`user_agent` |
| `operation` | 发生了管理类改动（如改导航入口） | `action`、`params`、`admin_info` |

要回答“这是哪个 Agent 干的”，按两级识别：

**第一级 · 显式声明**（准确，推荐）

```
X-Agent-Name: workbuddy-deploy
```

**第二级 · User-Agent 识别**（零配置，会有误判可能）

没有上面的头时，才看 UA 是否含已知标记：

| UA 标记 | 记为 |
|---|---|
| `workbuddy` / `codebuddy` | `workbuddy` |
| `codex` | `codex` |
| `claude-code` / `claude-cli` | `claude-code` |
| `cursor` / `cline` / `aider` | 同名 |

匹配顺序保证“外层产品优先”：`WorkBuddy/2.0 codex_cli_rs/0.9` 记为 `workbuddy`，不是 `codex`。

**两者都没有 → 留空。**

这一点是刻意的：不设默认值。否则浏览器会话、curl 脚本、巡检程序会被统一记成“最常见的那个工具”，审计就从“如实记录”退化成“猜一个看起来合理的”。宁可空着，也不写假信息。

| 项目 | 说明 |
|---|---|
| 落库位置 | `audit_logs.other.admin_info.agent` / `.agent_source` |
| `agent_source` | `header`（声明）或 `user_agent`（推断）；空表示未识别 |
| 长度 | 最长 64 字符，超长截断 |
| 清洗 | 剔除控制字符（防止伪造换行、破坏 JSON 消费方） |
| 缺省 | 都没识别到时字段不出现，老客户端行为不变 |
| 前端 | 审计详情多一行 `Agent`；界面不显示来源，需要精确判断时查 `agent_source` |

### 它是自报，不是认证

任何人都能伪造这个头，所以它**不参与鉴权、不能作为归属证据**。真正的归属靠 `token_ref`（令牌指纹），
`X-Agent-Name` 只解决“同一个账号下，几个 Agent 分别是哪一条”。

### 实测落库

六种客户端各发一次请求的落库结果：

```
UA = WorkBuddy/2.1.0        -> agent='workbuddy'        source='user_agent'
UA = CodeBuddy/1.0          -> agent='workbuddy'        source='user_agent'
UA = codex_cli_rs/0.20.0    -> agent='codex'            source='user_agent'
UA = Chrome/131.0           -> agent=None               source=None
header=workbuddy-deploy + UA=codex_cli_rs -> agent='workbuddy-deploy'  source='header'
UA = python-urllib/3.13     -> agent=None               source=None
```

浏览器与裸脚本保持空，声明永远压过推断。

### 命名建议

`<工具>-<用途>`，例如 `codex-nav`、`workbuddy-audit`、`claude-deploy`。
一个 Agent 一个固定名字，翻日志时才能按名字聚合。

## 8. 新功能设计规范

新增功能时逐条对照。**这七条不是文档要求，是接口设计的一部分。**

| # | 要求 | 反例 | 为什么 |
|---|---|---|---|
| 1 | **声明式 scope**：路由必须进 `access_token_routes.go`，粒度到 read/write | 只挂 `RootAuth()` | 没声明 = `ACCESS_TOKEN_ROUTE_UNDECLARED`，Agent 直接调不通 |
| 2 | **稳定错误码**：响应带 `code` | 只有 `message` 文本 | Agent 按 `code` 分支重试/改参/换凭据，不解析自然语言 |
| 3 | **可定位的拒绝**：失败时给出 `field` | "Invalid parameters" | 否则 Agent 要靠猜来修正 |
| 4 | **幂等写**：PUT 全量替换，或接受 `Idempotency-Key` | POST 每调一次建一条 | 网络重试会重复创建 |
| 5 | **乐观并发**：`revision` / `If-Match`，冲突回 409 | 直接覆盖 | 两个 Agent 同时写会静默丢数据 |
| 6 | **整份读写**：一次拿到完整状态 | 列表页 + 逐条详情 N+1 | 减少往返，避免读到半更新状态 |
| 7 | **无需交互**：不要求二次验证；若必须，明确标 human-only | 静默挂 step-up | Agent 会卡在 403 且无法自救 |

---

## 9. 范例：导航入口配置

这个功能是今天新增的。改造前**完全不满足**第 1、2、3 条——它是"新功能写成存量形态"的典型。

### 8.1 改造前后

| 维度 | 改造前 | 改造后 |
|---|---|---|
| 能否被 PAT 调用 | ❌ 三条路由都没声明 → `ACCESS_TOKEN_ROUTE_UNDECLARED` | ✅ `nav_app:read` / `nav_app:write` |
| 错误可分支吗 | ❌ 只有 `message` 文本 | ✅ `NAV_APP_INVALID` / `NAV_APP_CONFLICT` / `NAV_APP_LIMIT` / `NAV_APP_STORE_*` |
| 能定位错在哪个字段 | ❌ 只有一句话 | ✅ `message_params.field` |
| 重复提交安全吗 | ✅ PUT 全量替换，天然幂等 | 同左 |
| 并发写会丢数据吗 | ✅ `base_revision` 乐观锁 | 同左 |
| 一次拿到完整状态 | ✅ `GET /api/nav_apps/all` 返回全量 + revision | 同左 |

**幂等和并发控制一开始就做对了，缺的是"能被调用"和"错误可编程"**——而这恰恰是 Agent-first 与普通 API 的分水岭。

### 8.2 读

```bash
curl -s "$BASE/api/nav_apps/all" -H "Authorization: Bearer $TOKEN"
```

```json
{
  "success": true,
  "message": "",
  "revision": "da99fd4b869b2f97",
  "data": [
    {
      "slug": "langfuse",
      "name": "Langfuse",
      "name_i18n": "{\"zhCN\":\"监控面板\"}",
      "icon": "activity",
      "href": "https://langfuse.1task.cn",
      "display_type": "iframe",
      "placement": "both",
      "min_role": 1,
      "sort": 0,
      "enabled": true
    }
  ]
}
```

### 8.3 写（全量 + 乐观锁）

```bash
curl -s -X PUT "$BASE/api/nav_apps" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "base_revision": "da99fd4b869b2f97",
    "apps": [ { "slug": "langfuse", "name": "Langfuse", "icon": "activity",
                "href": "https://langfuse.1task.cn", "display_type": "iframe",
                "placement": "both", "min_role": 1, "sort": 0, "enabled": true } ]
  }'
```

成功返回新 `revision`，Agent 保存下来供下次写使用。

`href` 有两种合法取值。别的机器上的绝对 `http(s)` 地址可以内嵌，也可以新标签打开；
`cherrystudio://providers/api-keys?v=1&data=...` 这样的客户端深链会交给操作系统调起已安装的客户端，
所以只能 `display_type: "external"`，配 `iframe` 会被 `NAV_APP_INVALID` 拒绝。
能在本站执行脚本的 scheme（`javascript://`、`data://`、`file://` 等）一律拒绝。

`href` 里可以写占位符，点击时才在浏览器端替换：`{key}` 与 `{cherryConfig}`
`{aionuiConfig}` `{deepchatConfig}` `{aqbotConfig}` 用当前登录用户自己的 API 密钥，
`{address}` `{base_url}` 用本站地址。所以配置里不要写字面凭据——`GET /api/nav_apps`
会把它返回给每个能看到该条目的用户。占位符不能出现在 host 里（那会绕过同源检查），
也不能用于 `iframe`（内嵌页在点击前就已加载，key 会从目标站日志和 Referer 泄漏）。

### 8.4 三种拒绝

```json
// 字段非法
{ "success": false, "code": "NAV_APP_INVALID",
  "message_key": "Target address must be an absolute http(s) URL or a client link such as cherrystudio://",
  "message_params": { "field": "href" } }

// 并发冲突（revision 过期）
{ "success": false, "code": "NAV_APP_CONFLICT",
  "message_key": "Navigation entries were changed by another session. Reload the page and try again." }

// 条目超限
{ "success": false, "code": "NAV_APP_LIMIT",
  "message_params": { "max": 50 } }
```

Agent 的处理策略因此变得确定：`NAV_APP_INVALID` 改 `field` 指向的参数；`NAV_APP_CONFLICT` 重新 GET 再 PUT；`NAV_APP_STORE_FAILED` 可原样重试。

### 8.5 为什么不做单条端点

考虑过 `POST /api/nav_apps`、`PATCH /api/nav_apps/:slug`、`DELETE /api/nav_apps/:slug`，最终只保留全量 PUT：

- 顺序由数组位置决定，单条新增必须处理"插到第几位"，反而更复杂
- 全量 PUT 幂等：同一份内容重复提交结果一致，重试零风险
- 条目上限 50、常见个位数，读全量成本可忽略
- 两套写语义并存最容易产生不一致

结论：**"少而幂等"优于"多而精巧"**。

---

## 10. 仍存在的差距

| 缺口 | 影响 | 建议 |
|---|---|---|
| 无聚合端点 | 一个场景要拼 5–6 次调用 | 场景化 `/api/agent/tasks/*`，一次完成多步 |
| 凭据仍需人工发放 | 每个新 Agent 都要人手工复制一次令牌（账号级令牌还是共享的） | 受 root 审批的签发流程，或设备码 |
| 无幂等键 | 非 PUT 的写操作重试有风险 | 接受 `Idempotency-Key`，24h 去重 |
| 无机器可读错误目录 | Agent 只能从错误里逐个学习 | OpenAPI 补 `x-error-codes`，或 `/api/agent/capabilities` |

在这四项落地前，建议 Agent 侧加一层薄封装：固定"读 revision → 改 → 带 revision 写"序列、按 `code` 分流、对写操作做本地去重。
