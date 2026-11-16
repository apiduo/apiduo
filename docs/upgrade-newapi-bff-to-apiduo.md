# newapi-bff.oneapis.cn → apiduo 无感升级方案与坑位审查

> 2026-11-13 实地核查版。所有结论都来自三处：现网只读探查、
> apiduo 仓库源码（HEAD `4c2f4314c`，镜像 `0.0.121`）、rc.40 对照源码（tag `v1.0.0-rc.40`）。
> 本文不含任何密钥明文。

---

## 0. 结论先行

**能做，但「完全无感」有一个硬前提：先改 BFF，再换 new-api。**

现网不是「一个 new-api 站点」，而是 **new-api 被一个外部 BFF 当成账号/计费后端**：

```
浏览器 → nginx → newapi-for-bff (rc.40, :3001)
                     ↑
        newapi-bff (FastAPI, :8000)  ←— 用 NEWAPI_BASE_URL 指回同一个域名
```

BFF 的每一次用户登录都会调 `GET /api/user/token` 换长期 PAT（实测日志 1,532 次），
并从 `GET /api/user/self` 的 `access_token` 字段做故障恢复。而 apiduo
**删掉了这个路由，也删掉了这个字段**（`router/api-router.go`、`controller/user.go:494`）。
直接切镜像 → **所有 BFF 用户登录返回 500/502**（上游 404 被 BFF 解析成异常），不是降级，是全断。

第二颗雷：apiduo 把旧的 `users.access_token` 定为**升级后 30 天硬退役**
（`model/user_access_token.go:21`）。现网 **195 个用户**有这种令牌，BFF 管理员
`uid=184` 用的也是它（环境变量 `NEWAPI_ADMIN_PAT`）。30 天后管理员建号/加额度/兑换码全废。

这两件事解决之后，剩下的才是常规蓝绿：apiduo 对主库只做**加表/加列/加索引**，
`tasks`（实测 92,661 行 / 1.9GB）结构未变，rc.40 在结构上兼容迁移后的库（已逐项核对，
但仍须在 Phase 1 用真实副本实测确认）。

**推荐节奏（expand → migrate → contract）：**

1. **先发 BFF**：同时支持新旧网关（探测到 404 自动走新端点）。在 rc.40 上先验证不回归。
2. **再切 new-api**：蓝绿 + 5 分钟观察 + 秒级回滚。
3. **最后收口**：30 天内把管理员 PAT 换成 `nap_`，用户侧发公告，删兼容分支。

这样 BFF 对两个版本都兼容，**回滚不需要回滚 BFF**，是真正的双向可逆。

> 本次实际执行走了方案 B 的变体：主站切 apiduo，同时把 `/api/user/token` 和
> BFF 的 `/api/verify` 精确转发到一台 rc.40 slave 旁路，BFF 零改动保持可用。
> 这是**临时态**，退场条件是 BFF 改完新 PAT 契约（详见 §10）。

---

## 1. 现网事实（只读实测）

### 1.1 拓扑

| 层 | 实体 | 关键配置 |
|---|---|---|
| 入口 | 宿主机 nginx | `newapi-bff.oneapis.cn.conf`，`location ^~ /` → `127.0.0.1:3001` |
| 应用 | 容器 `newapi-for-bff` | `calciumion/new-api:v1.0.0-rc.40`，`127.0.0.1:3001→3000` |
| BFF | 容器 `newapi-bff` | FastAPI，`127.0.0.1:8000`，品牌「Workbuddy积分」 |
| 主库 | 宿主机 MySQL | `9.0.1`，库 `newapi-for-bff`，`utf8mb4_general_ci` |
| 缓存 | 宿主机 Redis | `db0`（BFF 用），`db3`（tasks 实例用） |
| 日志库 | 容器 `newapi-for-bff-clickhouse` | `26.9.1`，库 `new_api_logs` |
| 编排 | 宝塔 compose | `/www/server/panel/data/compose/newapi-for-bff/docker-compose.yaml` |
| 主机 | 96C / 123G / 813G 可用 | 容器实际 RSS 仅 60MB，CPU 0.35% |

另有一个 `newapi-for-tasks`（`:latest`，`:3003`），**用的是独立库 `newapi-for-tasks` +
独立 Redis db3**，与本次升级无 schema 耦合，不要动它。

### 1.2 数据规模

| 表/库 | 规模 | 迁移影响 |
|---|---|---|
| 主库合计 | 2,000.9 MB / 36 表 | — |
| `tasks` | 92,661 行 / 1,985.4 MB（`information_schema` 估算 206k，实测见 §10） | **结构未变**（apiduo 只改 `PrivateData` JSON），无 DDL |
| `users` | 202 行 | 无新增列（`auth_version` 在 rc.40 已存在且为 bigint） |
| `tokens` | 353 行（352 启用），`key` 0 重复 | 唯一索引迁移仅 PostgreSQL 生效 |
| `channels` | 36 行 | 无新增列 |
| `options` | 45 行 | 只补默认值 + 修主键（当前健康，no-op） |
| `redemptions` | 4,656 行 | 无 |
| `quota_data` | 2,633 行 | 新增 4 个索引（小表，秒级） |
| CH `logs` | 93,743 行 / 6.6 MiB，TTL 180d | **引擎 MergeTree → ReplacingMergeTree**（整表复制+改名） |
| CH `audit_logs` | 19,340 行 / 1.18 MiB | 新建/迁移，小 |

### 1.3 流量（决定切换窗口）

nginx 日志按小时聚合（约 77 天）：

| 时段 | 请求数 | 说明 |
|---|---|---|
| 10:00 | 8,986 | 峰值 |
| 05:00 | 374 | 低谷 |
| 06:00 | 690 | 低谷 |
| 07:00 | 188 | **最低** |

**推荐窗口：05:00–06:00 CST**。整小时约 400 请求 ≈ 每分钟 6 个，即使退化为
30 秒停机也只影响 3 个请求；蓝绿方案下理论 0 中断。

### 1.4 外部依赖面（这是本次升级的真正风险面）

BFF 对 new-api 的调用（来自 nginx 日志 + BFF 源码）：

| 接口 | 77 天调用 | apiduo 是否保留 | 结论 |
|---|---|---|---|
| `GET /api/user/token` | **1,532** | ❌ 已删除 | **登录链路断裂（P0）** |
| `GET /api/user/self`（取 `access_token`） | 7,189 | ⚠️ 保留但**不再返回该字段** | **故障恢复断裂（P0）** |
| `GET /api/user/token/status` | 10 | ❌ 已删除 | 旧页面/脚本报错 |
| `GET /api/task_plugin_options` | 241 | ❌ 已删除 | 仅旧前端标签页 |
| `GET /api/audit` | 65 | ✅ 保留（改为权限路由注册） | 无影响 |
| `POST /api/user/login` | 2,603 | ✅ 保留，响应结构不变 | 无影响 |
| `GET /api/token/`、`POST /api/token/:id/key` | 10,284 / 高频 | ✅ 保留 | 需要新 PAT 作用域 |
| `GET /api/log/self`、`/self/stat` | 2,550 / 2,475 | ✅ 保留 | 需要 `usage:read` |

---

## 2. 目标态与不变量

**只换一个东西**：`newapi-for-bff` 的镜像从 `calciumion/new-api:v1.0.0-rc.40`
换成 `ghcr.io/apiduo/apiduo:0.0.121`（linux/amd64 已确认存在）。

以下**必须原样保留**，任何一项改动都会造成用户可感或不可逆事故：

| 不变量 | 原因 |
|---|---|
| `SQL_DSN`（同库同账号） | 迁移只加表/加列，回滚依赖同库 |
| `REDIS_CONN_STRING` → **db0** | 会话/额度/限流/缓存全在 db0；换 db = 全员掉线 + 额度漂移 |
| `SESSION_SECRET` | 换掉 = 所有 refresh cookie 失效 = 全员重新登录 |
| `CRYPTO_SECRET` | 现网仍是安装模板的占位串；**换掉 = 36 个渠道密钥全部解不开** |
| `LOG_SQL_DSN`（CH 同库） | 日志与审计连续 |
| `server_address` / `passkey_rp_id` 选项 | 改域名 = OAuth 回调与 Passkey 全失效 |
| 端口 `127.0.0.1:3001` | nginx 写死 |
| `/data`、`/app/logs` 挂载 | 上传文件与运行日志 |

新增/调整的环境变量：

```diff
  NODE_TYPE=master          # 保持不变：迁移与后台任务只在 master 跑，见 §4
+ DEFAULT_LANGUAGE=zh-CN    # 可选：让后端错误文案保持中文，减少 BFF 文案匹配回归
```

`GENERATE_DEFAULT_TOKEN` 仍然支持（`common/init.go:206`），保持原值即可。

---

## 3. 阻断项（Go / No-Go）

### B-1（P0）BFF 登录链路必须改造

**证据链**：

- BFF `newapi_client.py:543 login()`：`login → _mint_access_token() → GET /api/user/token`
- BFF `newapi_client.py:333 _recover_admin_pat_by_readback()`：`GET /api/user/self` 读 `data.access_token`
- apiduo `router/api-router.go`：`selfRoute.GET/POST/DELETE("/token")` 全部删除，
  替换为 `/api/user/access_tokens`（会话级 + 安全验证）
- apiduo `controller/user.go:494 buildSelfUserData()`：注释明确 "intentionally excludes
  password, management PAT" → 不再返回 `access_token`
- 实测失败点有两处：`POST /api/verify` 不带 `context` 直接 **400**
  （"The action details are invalid."）；`GET /api/user/token` 被 `/api/user/:id`
  吞掉，返回 HTTP 200 + `success:false`（"strconv.Atoi: parsing \"token\""）。
  BFF 两处都会抛错，登录失败。

**方案 A（推荐）：BFF 前向兼容改造**

在 BFF 中把「换 PAT」抽象成两路探测：

```python
async def _mint_access_token(session_token, uid, password, client_ip=None) -> str:
    payload = {"name": "bff",
               "scopes": ["profile:read", "profile:write",
                          "api_key:read", "api_key:write", "api_key:reveal",
                          "usage:read", "wallet:read", "wallet:write"],
               "expires_at": 0}
    try:
        body = await request("POST", "/api/user/access_tokens",
                             headers=user_headers(session_token, uid), json=payload)
        return body["data"]["token"]          # nap_...（新网关）
    except NewApiError as e:
        if e.status_code == 404:              # 老网关没有这个路由
            proof = await _proof(session_token, uid, password, "access_token.generate")
            return await _legacy_mint(session_token, uid, proof)  # 老网关不校验 context
        if e.status_code not in (401, 403):   # 403 = 新网关要求安全验证
            raise
    proof = await _proof(session_token, uid, password, "access_token.generate",
                         context={"scopes": payload["scopes"], "expires_at": 0})
    body = await request("POST", "/api/user/access_tokens",
                         headers={**user_headers(session_token, uid),
                                  "X-Security-Proof": proof}, json=payload)
    return body["data"]["token"]
```

要点：

1. `POST /api/user/access_tokens` 走**浏览器会话**（`requireBrowserSession`），
   只能用登录返回的 15 分钟 JWT，不能用 PAT 调。
2. `expires_at: 0` = 不过期；非 0 时必须 ≥ now+1h。
   **安全验证 proof 是一次性的**：新老两条分支必须各自取 proof，不能复用同一个。
3. 新版 PAT 明文只在创建时返回一次，**BFF 必须自己持久化**（见 B-3）。
4. `profile:write` 是 `PUT /api/user/self`（绑定账密）需要的；`api_key:reveal` 是
   `POST /api/token/:id/key` 需要的。少一个就 403 `ACCESS_TOKEN_SCOPE_DENIED`。
5. 先发 BFF、后切服务端：BFF 在 rc.40 上走老分支，apiduo 上走新分支，**两边都不回归**。

**方案 B（应急）：apiduo 侧兼容层**

在 apiduo 加一个环境变量开关（如 `LEGACY_ACCESS_TOKEN_COMPAT=true`），恢复
`GET/POST/DELETE /api/user/token` 并在 `/api/user/self` 回填 `access_token`。
优点：BFF 零改动。缺点：把 apiduo 刻意移除的「PAT 暴露给浏览器」安全缺陷又打开了；
需要改代码 + 发版 + 测试。**只建议在 BFF 排不上期时作为 30 天临时桥**，且必须
与「旧 PAT 退役时间」对齐。

**方案 C：硬切** —— 不可接受。登录 100% 失败不是"无感"。

### B-2（P0）旧 PAT 30 天退役必须做决策

- 现网 195 个用户有 `users.access_token`，BFF 管理员 `uid=184` 也在其中。
- apiduo 首次以 master 启动时写入 `options.LegacyAccessTokenRetireAt = now + 30d`
  （`model/user_access_token.go:362`，`OnConflict DoNothing`，先到先得）。
- 到期后 `ValidateAccessToken` 直接返回 `ACCESS_TOKEN_LEGACY_RETIRED`（401），
  不再查库（`model/user.go:1236`）。

三个选项：

| 选项 | 做法 | 代价 |
|---|---|---|
| **A 预置 90 天**（推荐） | 升级前手动插入 `LegacyAccessTokenRetireAt = now+90d`，给用户争取迁移期 | 需在切换前执行一条 SQL；安全暴露期变长 |
| B 30 天 + 公告 | 保持默认，邮件/站内信通知 | 30 天后仍有用户脚本挂掉，客服成本 |
| C 立即迁移 | 强制所有用户换新 PAT | 202 个用户，不可行 |

> 预置 SQL 必须**在 apiduo 第一次启动前**执行，否则 `DoNothing` 不会覆盖（7776000 秒 = 90 天）：

```sql
INSERT INTO options (`key`, `value`)
VALUES ('LegacyAccessTokenRetireAt', UNIX_TIMESTAMP() + 7776000)
ON DUPLICATE KEY UPDATE `value` = VALUES(`value`);
```

### B-3（P0）BFF 新 PAT 配额只有 20/用户

`service/access_token_scope.go:18 AccessTokenMaxPerUser = 20`。BFF 现在每次登录都
mint 一个新 PAT（旧的留在 `users.access_token` 单列里被轮换）。切到新模型后，
同一用户第 21 次登录（换设备、清 cookie、重装）会收到
`ACCESS_TOKEN_LIMIT: You can create at most 20 access tokens` → 登录失败。

**必须同时改 BFF**：按 uid 持久化一个名为 `bff` 的 PAT，登录时优先复用；
只有不存在时才创建。BFF 目前是**无状态**的（`store.py` 只在 mock 模式用），
需要新增一个小存储（文件/SQLite 均可，`/data` 已在容器里）或把 PAT 放进
BFF 自己的会话存储。

---

## 4. 升级方案

### Phase 0（D-7 ~ D-2）：BFF 前向兼容 + 演练环境

1. 按 §3 B-1 改造 BFF，**先在现网 rc.40 上灰度发布**，确认：
   - 老分支（`/api/user/token`）仍然成功；
   - 登录、建 Key、查日志、兑换码、充值预览全链路正常；
   - 无新增 5xx。
2. 准备影子环境（本机即可，不碰生产）：
   - `mysqldump` 恢复到 `newapi_bff_rehearsal`；
   - ClickHouse 建 `new_api_logs_rehearsal`，`CREATE TABLE ... AS` + `INSERT SELECT`；
   - 起一个 apiduo 容器（`:3009`，`NODE_TYPE=master`，指向 rehearsal 库）。
3. 在影子环境跑**回滚演练**：apiduo 迁移 → 写一条日志/建一个 token → 停掉 →
   用 rc.40 起同一库 → 验证能登录、能读 token、能写日志。**这一步证明 L1 回滚可用**。

### Phase 1（D-1）：生产副本全量彩排（Go/No-Go 依据）

用**当天最新**的生产 dump 再走一遍，记录：

- [ ] apiduo 启动到 `success=true` 的耗时（迁移窗口）
- [ ] `SHOW TABLES` 增量（预期新增 `user_access_tokens`、`agent_keys`、`file_objects`、
      `external_identity_claims`、`auth_flows`、`passkey_credentials`、`two_fa*`、
      `subscription_*` 等）
- [ ] `tasks`/`users`/`tokens`/`channels` 行数逐项相等
- [ ] CH `SHOW CREATE TABLE logs` 含 `ReplacingMergeTree`，行数 ≥ 93,743
- [ ] `/api/setup` 的 `root_init=false`
- [ ] BFF 影子实例（`:8002`，`NEWAPI_BASE_URL=http://127.0.0.1:3009`）全流程通过
- [ ] rc.40 回滚演练再次通过
- [ ] 记录迁移期间 MySQL `SHOW PROCESSLIST` 是否有长事务/锁等待

**任何一项 FAIL → 不切。**

### Phase 2（D-0，05:00–06:00）：生产蓝绿

时间轴（T 为开始）：

| 时刻 | 动作 | 判据/回滚 |
|---|---|---|
| T-30m | 确认无大促/公告；`docker pull ghcr.io/apiduo/apiduo:0.0.121` | 镜像 digest 记录在案 |
| T-20m | 全量备份：MySQL dump + 行数快照 + CH schema | dump 可解压、行数一致 |
| T-10m | 记录基线：登录数、5xx、p95 | 有对照 |
| T-5m | 预置 `LegacyAccessTokenRetireAt`（若选 90 天） | 选项存在 |
| T-0 | 从运行中容器导出 env 到 `/www/docker/apiduo-canary.env`（600），改 `NODE_NAME`；起 canary `:3002`，`NODE_TYPE=master` | 容器 healthy，`/api/status` 带 `success:true` 与 `version:0.0.121` |
| T+2m | 直连 `:3002` 冒烟（§5） | 全绿 |
| T+5m | nginx 加 upstream，`:3002` 为主、`:3001` 为 backup，`proxy_next_upstream`，reload | `nginx -t` 通过，reload 无报错 |
| T+6m | 观察 5xx/登录/延迟 | 5xx < 0.1%，登录成功 |
| T+15m | 确认无异常 | — |
| T+15m | 先 `docker update --restart=no newapi-for-bff`（防重启后野容器复活），等 `:3001` 在途连接归零（SSE 可能长），再 `docker stop -t 90 newapi-for-bff` | 见 §6 坑 12 |
| T+30m | BFF 全链路回归（登录/建 Key/兑换/查单） | 全绿 |
| T+2h | 旧容器保留但停止（不删除），镜像保留 | 可秒回滚 |

> **canary 全程保持 `NODE_TYPE=master`**。原因：apiduo 只在 master 上跑迁移
> （`model/main.go:215`）。如果先起 slave，新表不存在，接口会 500。
> 双 master 的窗口只有 T-0 到 T+15m，任务终态结算是 CAS「恰好一次」
> （`service/task_polling.go:695`），用量聚合是每节点各自累加（`model/usedata.go:58`），
> 不会重复计费；只有渠道定时检测可能多跑一轮（36 个渠道，成本可忽略）。

### Phase 3（D+1 ~ D+30）：收口

1. **D+1**：用管理员账号在 apiduo 控制台创建一个 **scoped 管理 PAT**
   （`user:read/write`、`redemption:read/write`、`billing:read/write`、`log:read`），
   替换 BFF 的 `NEWAPI_ADMIN_PAT` + `NEWAPI_ADMIN_UID=184`，重启 BFF。
   （旧 legacy PAT 在过渡期内仍可用，可随时回退。）
2. **D+1**：验证用户侧新 PAT 复用逻辑（同一 uid 多次登录不增长 token 数）。
3. **D+7**：发公告：旧「系统访问令牌」将于 `LegacyAccessTokenRetireAt` 失效，
   引导到「个人设置 → 访问令牌」重建。
4. **D+30**：确认无 `ACCESS_TOKEN_LEGACY_RETIRED` 异常峰值后，移除 BFF 老分支。
5. **D+30**：旧容器/镜像退役；`logs_mergetree_backup` 表按需清理。
6. **D+30**：把宝塔 compose 项目 `newapi-for-bff` 的镜像改成
   `ghcr.io/apiduo/apiduo:0.0.121`（否则下次有人点「编排 → 重新部署」会把
   rc.40 重新拉起来，和 canary 抢同一份数据/端口）。

---

## 5. 冒烟与验收清单

### 5.1 服务端（直连 `127.0.0.1:3002`，不带 nginx）

```bash
curl -s --noproxy '*' http://127.0.0.1:3002/api/status | jq '.success, .data.version'
curl -s --noproxy '*' http://127.0.0.1:3002/api/setup  | jq '.data.root_init'   # 必须 false
curl -s --noproxy '*' -o /dev/null -w '%{http_code}\n' http://127.0.0.1:3002/v1/models  # 401
curl -s --noproxy '*' http://127.0.0.1:3002/api/status | jq '.data.system_name'
```

### 5.2 数据一致性

```sql
SELECT 'users', COUNT(*) FROM users UNION ALL
SELECT 'tokens', COUNT(*) FROM tokens UNION ALL
SELECT 'channels', COUNT(*) FROM channels UNION ALL
SELECT 'tasks', COUNT(*) FROM tasks;
SELECT COUNT(*) FROM user_access_tokens;       -- 0（升级瞬间）
SELECT `key`,`value` FROM options WHERE `key`='LegacyAccessTokenRetireAt';
SHOW TABLES;                                    -- 36 + 新增
```

### 5.3 BFF 全链路（切流后 30 分钟内）

| 流程 | 判据 |
|---|---|
| 密码登录 | 200，cookie 写入，无 502 |
| 兑换码登录 | 200，到账积分正确 |
| 绑定账密 | 200，uid 不变 |
| Key 列表 / 新建 / 揭示明文 | 200，`sk-` 正常 |
| 用量日志 / 统计 | 200，`content` 仍为字符串 |
| 充值预览 / 下单 | 200，金额不变 |
| 管理员建号 / 加额度 | 200，`search` 可查到 |

### 5.4 观测

- `docker logs apiduo-canary 2>&1 | grep -E "ERROR|panic|ACCESS_TOKEN_ROUTE_UNDECLARED"`
- CH：`SELECT count() FROM logs WHERE created_at > now()-600`（新日志在写）
- MySQL：`SHOW PROCESSLIST` 无长事务；`SHOW ENGINE INNODB STATUS` 无锁等待
- 5xx 比例、p95 延迟、登录成功率（对比 T-10m 基线）

---

## 6. 坑位审查（按严重度）

### P0 — 不处理就出事

| # | 坑 | 证据 | 表现 | 处置 |
|---|---|---|---|---|
| 1 | BFF 登录依赖 `GET /api/user/token`，apiduo 已删 | `router/api-router.go`；BFF `newapi_client.py:543`；日志 1,532 次 | 所有 BFF 用户登录失败（400/业务错误） | §3 B-1，先发 BFF |
| 2 | `/api/user/self` 不再返回 `access_token` | `controller/user.go:494` | BFF 管理员 PAT 无法读回自愈 | 同上；管理员改用 `nap_` PAT |
| 3 | 旧 PAT 30 天硬退役，195 个用户 + BFF 管理员 | `model/user_access_token.go:21,362` | 30 天后 401 `ACCESS_TOKEN_LEGACY_RETIRED` | 预置 90 天 + 公告 + 迁移 |
| 4 | 新 PAT 上限 20/用户，BFF 每次登录都 mint | `service/access_token_scope.go:18` | 第 21 次登录失败 | BFF 持久化复用；或临时调高上限 |
| 5 | **slave 节点不跑迁移** | `model/main.go:215` | 新节点先起 slave → 新表不存在 → 500 | canary 必须 master 起 |
| 6 | MySQL **无 binlog** | `SHOW VARIABLES log_bin=OFF` | 数据回滚只能回到 dump 时刻 | T-0 全量 dump + 行数快照；先彩排再切 |
| 7 | `CRYPTO_SECRET` 是占位串 | 现网 env 未替换模板值 | 一旦"顺手改成强密钥"，36 个渠道密钥全解不开 | 原样保留，本次不动；后续单独排期做密钥轮换 |

### P1 — 会造成局部可感/运维事故

| # | 坑 | 证据 | 表现 | 处置 |
|---|---|---|---|---|
| 8 | CH 日志引擎升级是「整表复制 + RENAME」 | `model/log_final.go:190-218` | 复制窗口内老实例写入的日志落在旧表，切换后查不到 | 选择低谷；接受个位数丢失；旧表保留为 `logs_mergetree_backup` |
| 9 | 旧前端标签页会调已删路由 | `/api/user/token`、`/api/task_plugin_options` | 未刷新的页面操作报错 | BFF 改造 + 页面提示刷新；旧路由 404 属预期 |
| 10 | `MEMORY_CACHE_ENABLED=true` + 60s 同步 | `SYNC_FREQUENCY=60`，`model.SyncOptions` | 双实例期间后台改配置最多 60s 不一致 | 切换窗口内不改配置；切换后只对 canary 操作 |
| 11 | 会话缓存 MAC 校验 | `model/user_session.go:271` | rc.40 写的 Redis 缓存被 apiduo 判为无效 → 回源 DB | 预期行为，DB 兜底；观察 DB QPS |
| 12 | SSE 长连接 + `proxy_read_timeout 6000s` | nginx 配置 | 立刻 `docker stop` 老容器会掐断在途流 | 先切流，等在途连接归零再 stop；`-t 90` |
| 13 | 双 master 重复后台任务 | 20+ 处 `IsMasterNode` 判断 | 渠道检测多跑、任务轮询重复 | 任务结算 CAS 恰好一次，不重复计费；窗口压到 15 分钟内 |
| 14 | 错误文案 i18n 化，BFF 有中文串匹配 | `service/billing_session.go`；BFF `main.py:279,443,911` | 「验证码/已存在/Redemption failed」匹配可能失效 | 设 `DEFAULT_LANGUAGE=zh-CN`；回归登录/注册/兑换三条错误路径 |
| 15 | 版本号从 `v1.0.0-rc.40` 变 `0.0.121` | `/api/status` | 依赖版本比较的监控/客户端可能误判"降级" | 通知监控方；必要时在展示层标注 |
| 16 | 品牌已随 apiduo 默认显示 `Apiduo` | `options.SystemName` 为空，apiduo 用内置默认 | 与旧「New API」文案不同 | 已按「切 APIDUO」接受；若要改回，显式设置 `SystemName`/Logo/Footer |
| 17 | 顶栏永久提示「有新版本 rc.5x」 | apiduo 拿 fork 版本比上游 release | 管理员观感差 | 已知问题，非阻塞；后续单独修 |
| 18 | `TRUSTED_PROXIES=0.0.0.0/0` | 现网 env | 任何来源可伪造 `X-Forwarded-For`；PAT 的 IP 白名单形同虚设 | 既有风险；本次不改，单独排期收紧 |
| 19 | BFF 管理员删/改用户被安全验证拦截 | `controller/user.go:945`（delete）、`:691`（update）；rc.40 无此校验 | BFF 的 `delete_user` / `update_user` 返回 403 `SECURITY_PROOF_INVALID`；加额度不受影响 | 用 apiduo 控制台操作；或 BFF 补 `admin.user.*` 的 proof（需 2FA/Passkey） |
| 20 | 容器少挂 `shared-net`，内部渠道解析失败 | 实测 `channel_id=2 bifrost-chat`：`lookup bifrost ... server misbehaving` | 该渠道模型检查/转发全失败 | `docker network connect shared-net apiduo-canary`；网络连接会随容器重启保留，重建容器必须重挂 |

### P2 — 不影响业务，但要记录

| # | 坑 | 说明 |
|---|---|---|
| 21 | `options` 主键修复、token 唯一索引迁移 | 均为 PostgreSQL 专属，MySQL 上是 no-op（已核对 `migrateTokenKeyUniqueness` / `migratePrefillGroupUniqueness`） |
| 22 | `tokens.model_limits` / `subscription_plans.price_amount` 类型迁移 | 现网已经是 `text` / `decimal(10,6)`，不会触发 ALTER |
| 23 | `tasks` 9.3 万行 / 1.9GB | `PrivateData` 是 JSON 列，apiduo 未加 DB 列；GORM 只做表结构比对，不重写数据 |
| 24 | 新表 `audit_logs` 在 CH | 小表，`MigrateAuditLogs` 自动建；不影响主库 |
| 25 | `/data` 里目前只有日志 | 无上传文件迁移风险；新容器给独立 logs 目录，避免两实例写同一文件 |
| 26 | 健康检查只看 `success=true` | `/api/status` 恒 200；不要用状态码判活 |

---

## 7. 回滚预案

| 级别 | 触发条件 | 动作 | 耗时 | 数据影响 |
|---|---|---|---|---|
| **L1 代码回滚** | 5xx/登录/计费异常，apiduo 仍可读库 | nginx upstream 主备对调 + reload；`docker start newapi-for-bff` | 10–30s | 无（apiduo 只加表/列，rc.40 兼容） |
| **L2 配置回滚** | 环境变量改错 | 用 `/www/docker/apiduo-canary.env.bak` 重建 canary | 1–2min | 无 |
| **L3 数据回滚** | 数据损坏/迁移写坏 | 停 canary → 恢复 T-0 dump → 起 rc.40 | 20–40min | **丢失 T-0 之后所有写入** |

**L1 的前提已在 Phase 1 验证**：rc.40 能在 apiduo 迁移过的库上正常启动、
登录、读写 token。若验证失败，L1 不成立，只能走 L3 —— 这也是为什么彩排不能省。

---

## 8. 工作量与分工建议

| 工作 | 人天 | 前置 |
|---|---|---|
| BFF 前向兼容改造（含 scoped PAT + 持久化复用） | 1.5–2 | 无 |
| BFF 回归测试（登录/建 Key/兑换/充值/管理） | 0.5 | BFF 改造 |
| 影子环境搭建 + 全量彩排 + 回滚演练 | 0.5–1 | 生产 dump 权限 |
| 生产切换（含值守 2h） | 0.5 | 以上全部 |
| 用户公告 + 旧 PAT 迁移支持 | 0.5 | 产品确认 |

**总工期：3–4 个工作日**（不含 30 天观察期）。

---

## 9. 需要拍板的 5 件事

1. **BFF 方案**：A 前向兼容改造（推荐）/ B apiduo 兼容层 / C 硬切（不可接受）
2. **旧 PAT 期限**：预置 90 天 / 保持 30 天 + 公告
3. **品牌**：保持 `New API` / 切 `APIDuo`（改 `SystemName`、Logo、Footer）
4. **切换窗口**：05:00–06:00 CST（推荐）/ 其他
5. **是否启用 canary 观察期**：15 分钟（推荐）/ 直接全切

---

## 10. 本次实际执行记录（2026-11-13 18:27–18:40 CST）

已按「直接切」执行，生产当前状态：

| 项 | 结果 |
|---|---|
| 备份 | `/www/backup/newapi-for-bff/20261113-182731/mysql.sql.gz`（927MB）；恢复到临时库逐表比对一致（202/353/36/92,661/45） |
| 旧 PAT 期限 | 预置 `LegacyAccessTokenRetireAt=1802341785`（2027-02-11） |
| 迁移 | apiduo 0.0.121 在 `:3002` 启动，迁移约 2 秒；表 36→41；关键行数不变 |
| 主站切流 | nginx `proxy_pass` 3001→3002，reload；公网 `/api/status` = `0.0.121 / Apiduo` |
| 容器网络 | apiduo 补挂 `shared-net`（与旧容器一致），修复 `bifrost:8080` 解析失败；未挂时渠道 2 模型检查失败 |
| BFF 兼容桥 | `/api/user/token` 转发到 `:3001` 的 rc.40 **slave** 旁路（`newapi-legacy-bridge`）；`/api/verify` 按源 IP + UA 双条件分流（`154.40.45.22` + `python-httpx` → rc.40，其余 → apiduo）；临时用户 BFF 登录实测 200 |
| 旧容器 | `newapi-for-bff` 已停且 `restart=no`，容器与镜像保留用于 L1 回滚 |
| CH 日志 | 93,743 → 82,349；减少的是 5,689 个重复 `(created_at, request_id)` 键，备份表 `logs_mergetree_backup` 仍有 93,743 |
| 清理 | 测试用户、测试 PAT、登录会话均已删除；`users` 回到 202 |
| 已知回归 | BFF 管理员「删除用户/编辑用户」被 `admin.user.delete/update` 安全验证拦截；加额度、建号、查单正常 |
| 20:34 补切 | 发现 `workbuddy.oneapis.cn`（真正的客户 API 域名）`location ^~ /v1` 仍指向 `127.0.0.1:3001`；已改为 `3002`，实测 `/v1/models` 200（91 个模型）。**这是此前只切了 `newapi-bff` 域名的遗漏** |

**L1 回滚（约 30 秒）**：

```bash
docker stop newapi-legacy-bridge
docker start newapi-for-bff
# nginx: 把 proxy_pass 改回 127.0.0.1:3001，nginx -t && nginx -s reload
```

**待办（按优先级）**：BFF 改新 PAT 契约 → 删掉两个兼容 location 与 rc.40 旁路 →
把宝塔 compose 的镜像改成 apiduo → 处理管理员删/改用户的 proof。

## 11. 可观测：独立 Langfuse（已部署）

2026-11-13 20:00 起，本机跑一套**独立** Langfuse 4.80.0，不再复用 ai-prod 的
`langfuse.1task.cn`。上报不出宿主机，只有浏览器看控制台走公网 8443。

### 11.1 访问与凭据

| 项 | 值 |
|---|---|
| 控制台 | `https://newapi-bff.oneapis.cn:8443`（复用现有证书，未改 DNS） |
| 登录邮箱 | `admin@oneapis.cn` |
| 登录密码 | `/opt/langfuse/.env` 的 `LANGFUSE_INIT_USER_PASSWORD`（600，不落文档） |
| 组织 / 项目 | `oneapis` / `newapi-bff` |
| API keys | `/opt/langfuse/.env` 的 `LANGFUSE_INIT_PROJECT_PUBLIC_KEY` / `..._SECRET_KEY` |
| 编排 | `/opt/langfuse/docker-compose.yml`，项目名 `langfuse` |
| 数据卷 | `/www/docker/langfuse/{postgres,redis,clickhouse,minio}` |

### 11.2 组件与网络

| 容器 | 作用 | 端口 |
|---|---|---|
| `langfuse-langfuse-web-1` | UI / API | `127.0.0.1:3100→3000`，nginx 8443 反代 |
| `langfuse-langfuse-worker-1` | 异步摄入 | 无 |
| `langfuse-langfuse-clickhouse-1` | 事件存储 | 仅容器网络 |
| `langfuse-postgres-1` | 元数据 | 仅容器网络 |
| `langfuse-redis-1` | 队列 | 仅容器网络 |
| `langfuse-minio-1` | S3（events/media） | 仅容器网络 |

apiduo 容器额外接入 `langfuse_default` 网络，`LANGFUSE_HOST=http://langfuse-web:3000`；
浏览器侧 `LANGFUSE_CONSOLE_URL=https://newapi-bff.oneapis.cn:8443`。
**上报链路：apiduo → langfuse-web（同宿主机 Docker 网络），不经过公网、不跨机器。**

### 11.3 验证

- 宿主机与外网 `GET /api/public/health` 均 200（4.80.0）；
- `GET /api/public/projects` 用项目 pk/sk 返回 `newapi-bff` / `oneapis`；
- 用同一组 keys 向 `/api/public/otel/v1/traces` POST 合成 span，返回 200 并进入摄入队列；
- apiduo 容器内 `wget http://langfuse-web:3000/api/public/health` 返回 200；
- 公网主站仍为 `0.0.121 / Apiduo`，BFF 兼容桥复测通过。

**两域名差异（2026-11-13 20:34 修正）**：`newapi-bff.oneapis.cn` 与 `workbuddy.oneapis.cn`
是两套入口。前者整站走 apiduo；后者的 `/` 指向 BFF(8000)，而 **`/v1` 之前仍指向旧网关(3001)**。
只切前者时，客户推理流量仍由 rc.40 处理，Langfuse 里自然只剩 root 控制台的 trace。
两个域名的 `/v1` 都指向 3002 之后，采集才覆盖全部调用。

### 11.4 切换后「页面崩溃」的真正原因（2026-11-13 20:45）

切流前打开的标签页持有旧前端 bundle。客户端路由跳到懒加载页时，按旧 hash 请求
`/static/js/async/58666.ec32322695.js`；apiduo 的 NoRoute 只对 `/v1`、`/api`、`/assets`
返回 404（`router/web-router.go:44`），`/static/*` 会**回落到 index.html 且 200**，
浏览器把 HTML 当 JS 执行 → 白屏。当前构建的同一 chunk 是 `58666.a0d74fe6a5.js`。

处置：nginx 增加 `location ^~ /static/`，用 `header_filter_by_lua_block` 把
`Content-Type: text/html` 的响应改成 404（实测旧 chunk 404、新 chunk 200）。
用户侧强制刷新即可恢复。代码层建议把 `/static` 加进 NoRoute 的前缀判断。

### 11.4 部署踩过的坑（都已修）

1. **`HOSTNAME` 必须设 `0.0.0.0`**：否则 Next.js 只绑容器主机名，`127.0.0.1:3000` 不通。
2. **web 内存不能按 1g 限**：1g 下 Node 堆直接 OOM（exit 134）；已调到 **3g**，worker 2g。
3. **`clickhouse` 服务名与 new-api 的 CH 冲突**：apiduo 接入 `langfuse_default` 后，
   `clickhouse` 解析到了 Langfuse 的 CH，日志库报 516 认证失败并崩溃。已把 Langfuse 的
   CH 改名为 `langfuse-clickhouse`，`CLICKHOUSE_URL/CLICKHOUSE_MIGRATION_URL` 同步改。
4. **MinIO 目录权限**：chainguard 镜像以 uid 65532 运行，`/www/docker/langfuse/minio`
   必须 `chown 65532:65532`，否则 `mkdir /data/langfuse` 失败、健康检查永远不过。
5. **8443 需要 UFW 放行**：`ufw allow 8443/tcp`；否则外网超时。
6. 旧的 `langfuse-clickhouse-1` 容器改名前会占着同一个数据目录，必须 stop/rm 后再起新容器。

### 11.5 媒体与指标

- 媒体桶暂用内部 MinIO endpoint，文本 trace 不受影响；若要浏览器直接看 Langfuse 里的
  图片，需要再给 MinIO 一个公网 endpoint（当前未做）。
- **指标仍无 Prometheus**：apiduo 架构本来就没有 `/metrics`，见 §11.6。

### 11.6 指标：apiduo 架构上本来就没有 Prometheus

这不是本次升级的缺失，仓库自己的设计文档已经钉死：

- `prometheus/client_golang` 在 `go.mod` 里是 **indirect**，依赖链是
  `model → samber/hot → prometheus`，不是业务代码在用；
- 全仓没有 `promhttp` / `MustRegister`，`router/` 里没有 `/metrics` 注册；
- 指标走 `pkg/perf_metrics` → MySQL `perf_metrics` 表 →
  `GET /api/perf-metrics` 与 `/summary` → 前端「性能指标」页；
- `docs/apiduo/langfuse-trace.md:563` 原话：
  「v2.1 的 P1『抓 /metrics + Grafana 大盘』不成立，要么自己加端点，要么改用现有 API」。

`obs.1task.cn` 上的 Prometheus/Grafana 是**另一个 fork** 的产物：
`ghcr.io/jushenzhidao/apiduo:0.0.24`（带 `OBS_ENABLED` / `OBS_METRICS_ADDR=0.0.0.0:39101` /
`OBS_CLICKHOUSE_DSN` / `OBS_SPOOL_DIR` 补丁）在 39101 暴露指标并写 `obs_content` 表。
它不是 `apiduo/apiduo`，也不是 `apiduo-cn`；`apiduo-cn` 同样没有 `OBS_*` 环境变量。

要在这台实例上要 Grafana 指标，等于给 apiduo **新增一个功能**（加 `/metrics` 或搬 OBS 补丁），
不是补一个配置项。当前实例的可观测边界就是：

| 维度 | 载体 |
|---|---|
| 请求级 trace | 本机 Langfuse（OTLP，已接入） |
| 性能指标 | `/api/perf-metrics` + 前端页面（MySQL `perf_metrics`） |
| 持续 profiling | 可选 `PYROSCOPE_URL`；当前未配 |

## 附录 A：关键命令

### A.1 备份 + 校验

```bash
TS=$(date +%Y%m%d-%H%M%S); DIR=/www/backup/newapi-for-bff/$TS; mkdir -p "$DIR"
DSN=$(docker inspect newapi-for-bff --format '{{range .Config.Env}}{{println .}}{{end}}' | sed -n 's/^SQL_DSN=//p')
CRED=${DSN#*://}; CRED=${CRED%%@*}; DBUSER=${CRED%%:*}; export MYSQL_PWD="${CRED#*:}"
mysqldump --single-transaction --quick --routines --triggers \
  --default-character-set=utf8mb4 -h127.0.0.1 -u"$DBUSER" newapi-for-bff | gzip > "$DIR/mysql.sql.gz"
mysql -h127.0.0.1 -u"$DBUSER" -D newapi-for-bff -N -e "
  SELECT 'users',COUNT(*) FROM users UNION ALL
  SELECT 'tokens',COUNT(*) FROM tokens UNION ALL
  SELECT 'channels',COUNT(*) FROM channels UNION ALL
  SELECT 'tasks',COUNT(*) FROM tasks;" | tee "$DIR/rowcounts.txt"
ls -lh "$DIR"; zcat "$DIR/mysql.sql.gz" | tail -c 200   # 确认结尾完整
```

### A.2 起 canary（:3002）

```bash
docker inspect newapi-for-bff --format '{{range .Config.Env}}{{println .}}{{end}}' \
  | grep -vE '^(PATH|HOSTNAME)=' \
  | sed -E 's#^NODE_NAME=.*#NODE_NAME=new-api-apiduo#' \
  > /www/docker/apiduo-canary.env
chmod 600 /www/docker/apiduo-canary.env

docker rm -f apiduo-canary 2>/dev/null || true
docker run -d --name apiduo-canary --restart=always \
  --network newapi-for-bff-net \
  --add-host host.docker.internal:host-gateway \
  -p 127.0.0.1:3002:3000 \
  --env-file /www/docker/apiduo-canary.env \
  --env-file /www/docker/apiduo-langfuse.env \
  -e NODE_TYPE=master \
  -v /www/docker/apiduo-canary/data:/data \
  -v /www/docker/apiduo-canary/logs:/app/logs \
  --memory 8g --cpus 8 \
  ghcr.io/apiduo/apiduo:0.0.121 --log-dir /app/logs
docker network connect shared-net apiduo-canary          # 内部渠道 bifrost:8080 依赖
docker network connect langfuse_default apiduo-canary   # 本地 Langfuse 上报（langfuse-web:3000）

docker logs -f apiduo-canary   # 看到 success=true 通过健康检查
```

### A.3 nginx 蓝绿

```nginx
upstream newapi_bff_upstream {
    server 127.0.0.1:3002 max_fails=3 fail_timeout=10s;   # apiduo
    server 127.0.0.1:3001 backup;                          # rc.40
}
# location ^~ / 内：
proxy_pass http://newapi_bff_upstream;
proxy_next_upstream error timeout http_502 http_503;
```

回滚：把 `3002` 改成 `backup`、`3001` 改成主，`nginx -t && nginx -s reload`。

### A.4 在途连接排空

```bash
watch -n2 "ss -tn state established '( sport = :3001 )' | wc -l"
# 归零后（先取消重启策略，避免宿主机重启后 rc.40 野容器复活）：
docker update --restart=no newapi-for-bff
docker stop -t 90 newapi-for-bff
```
