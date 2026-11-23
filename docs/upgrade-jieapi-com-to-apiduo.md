# jieapi.com → apiduo 无感升级方案与 1% 灰度记录

> 2026-11-13 20:55 CST 实况版。结论来自：现网只读探查、生产库副本排练、
> apiduo 源码（HEAD `a38af9a4e`，镜像 `0.0.121`）、rc.40 对照源码（tag `v1.0.0-rc.40`）。
> 本文不含任何密钥明文。

---

## 0. 结论先行

**50% 灰度已上线（22:39 从 10% 放大到 50%），API 同步面可用；Web / 支付 / 异步任务尚未进入灰度，是 100% 切换前的独立闸门。**

截至 20:25，真实 canary 流量：

| 指标 | 实测 |
|---|---|
| 真实请求 | 9 条：8×200 + 1×401（真实客户端错 key，非 canary 问题） |
| 路径 | 4× `/v1/chat/completions`，4× `/v1beta/models/...:generateContent`，1× `/v1/models`（401） |
| CH 日志 | 8 行、8 个唯一 `request_id`、0 重复（401 不写日志） |
| 计费 | 8 条 200 合计 13,470 quota，模型 5 个，含 1 个流式请求 |
| 5xx / panic | 0 / 0 |
| p50 / p95 / max | 4.45s / 7.46s / 28.95s（长生成，非错误） |

### 0.1 20:47 放量到 10%

- `split_clients` 1% → **10%**，`nginx -t` + reload；合成验证 h1 33/300、h2 10/100。
- 10% 窗口（20:47–20:54）真实 `/v1`：36×200 + 2×502。502 发生在 20:53:38
  canary 容器重建的约 1 秒窗口内（`upstream_response_time 0.000`、无 `request_id`），
  不是代码错误；20:54 之后无 5xx。
- canary 仍是 slave，MySQL 连接 25、CH 写入正常；`LANGFUSE_MEDIA_BASE_URL` 已置空
  （BFF Langfuse 的 MinIO 报 `InvalidAccessKeyId`，媒体回退 base64，trace 本身正常）。
- **10% 下不要直接 `docker rm` canary**：重建前先把分流切回 legacy，或选低谷，
  否则每次重建都会有约 1 秒的 502。

### 0.2 22:39 放量到 50%

- `split_clients` 10% → **50%**，reload；合成验证 h1 148/300、h2 51/100、同一 keep-alive 连接 49/100。
- 稳态分钟（22:46–22:51，剔除本机探针）：canary 78 / main 164 ≈ **47.6%**，符合 50%。
- 50% 初检：canary `/v1` 72×200、6×503、2×403、1×401；**0×502、0 panic**；
  MySQL 25、CPU 0.18%、内存 127MB、restarts 0。
- 503/400/401 均为业务/上游（无可用渠道、图片格式 400、无效令牌），legacy 同期同样存在。
- 50% 下重建 canary 的 1 秒空窗会命中约 20~25 个请求，**必须先暂停分流**。

**三个必须记住的坑（本次实测）**
1. **CH 日志表迁移会超时，但不会阻断启动。**
   `logs` 有 21.2M 行 / 53.6GB；apiduo 的迁移是 `INSERT INTO staging SELECT * FROM logs`
   整表复制。本次复制耗时 302 秒，客户端在读阶段 `i/o timeout`，应用只记
   `log table engine upgrade failed` 然后继续启动。已手工补尾 + `RENAME` 完成，
   **服务无中断**，新表已是 `ReplacingMergeTree`，后续重启不会再复制。
2. **`$request_id` 在 HTTP/2 请求下为空。**
   用它做 `split_clients` 时，h1 正常 1%、**h2 实测 0/100**。本站真实流量几乎全是
   h2/h3，等于 1% 灰度完全没生效。已改为复合键
   `$msec$pid$connection$connection_requests$remote_addr$request_uri`，h1/h2 均命中 1%。
3. **按“主体”1% 在本站切不到量。**
   5,329 个 token 里活跃的很少，按 token/SID 分 1% 实测 5 分钟 0 请求。
   API 面改为**按请求 1%**；Web 面仍应按会话 SID 粘性，不能照抄。

---

## 1. 现网事实（2026-11-13 实测）

### 1.1 拓扑

| 层 | 实体 | 关键点 |
|---|---|---|
| 入口 | 宿主机 nginx 1.31.0 | `jieapi.com` / `api.jieapi.com` → `127.0.0.1:3001` |
| 应用 | 容器 `jieapi-master` | `calciumion/new-api:v1.0.0-rc.40`，digest `37c0f99a`，`NODE_TYPE=master` |
| 备节点 | core-3 `jieapi-slave` | 同库/同 Redis/同 CH，`NODE_TYPE=slave`，不在公网 DNS 主链路 |
| 主库 | 宿主机 MySQL 9.0.1 | 库 `jieapi-master`，36 表 / 0.33GB，`log_bin=OFF` |
| 日志库 | 容器 `clickhouse` 26.7.3 | `new_api_logs.logs`，21.2M 行 / 53.6GB，TTL 180d |
| 缓存 | 宿主机 Redis | **db8**（不是 db0） |
| 编排 | 宝塔 compose | `/www/server/panel/data/compose/jieapi-master/docker-compose.yaml` |
| 主机 | core-1 / 32C / 249G / 3.6T NVMe | 根分区 83%，可用 597G |

### 1.2 数据规模（升级前）

| 表/库 | 规模 |
|---|---|
| `users` | 4,751（`access_token` 非空 17 个） |
| `tokens` | 5,329 |
| `channels` | 202（其中 62 个是多 Key 渠道） |
| `tasks` | 5,794，全部已终态（无 PENDING/IN_PROGRESS） |
| `quota_data` | 409,866 行 / 144MB |
| `perf_metrics` | 30 万行 / 62MB |
| CH `logs` | 21,203,633 行 / 53.60GB；202608 单分区 52.19GB |
| CH `audit_logs` | 221,372 行 / 13MB |

### 1.3 流量（今日实测）

| 域名 | 今日请求 | 说明 |
|---|---|---|
| `jieapi.com` | 125,364 | 主体；18:00 峰值 10,333/h，19:00 6,670/h |
| `api.jieapi.com` | 6,450 | 02:00 峰值 2,528/h，其余时段很低 |

最近 3,000 条里 2,490 条（83%）落在本次 canary 的同步端点白名单内。
当前 20:00 前后约 100~150 req/min，1% ≈ 0.6~1.5 req/min，**24 小时才够做一次有效观察**。

### 1.4 外部依赖面

- 没有 BFF。`/api/user/token` 在 500MB 日志里只有 31 次，删掉不影响主链路。
- `api.jieapi.com` 与 `jieapi.com` 共用同一个 `jieapi-master`。
- 支付回调路径存在：`/api/user/topup`、`/api/stripe/webhook`、`/api/user/epay/notify`、
  `/api/user/pay` —— **本次 canary 全部排除**。
- 异步任务：`/v3/contents/generations/tasks`、`volcengine-audio`、`doubao` 等；
  62 个多 Key 渠道的任务 key 绑定是版本相关的 —— **本次 canary 全部排除**。

---

## 2. 必须原样保留的不变量

| 不变量 | 原因 |
|---|---|
| `SQL_DSN`（同库同账号） | apiduo 只加表/加列，回滚依赖同库 |
| `LOG_SQL_DSN`（同 CH 库） | 日志/审计连续 |
| `REDIS_CONN_STRING` → **db8** | 会话/额度/限流/缓存都在 db8 |
| `SESSION_SECRET` | 换掉 = 所有 refresh cookie 失效 = 全员重新登录 |
| `CRYPTO_SECRET` | 现网**仍是安装模板占位串**；换掉 = 202 个渠道密钥全部解不开 |
| `options.server_address` = `https://jieapi.com` | OAuth/支付回调 |
| `options.SystemName` = `接JieAPI`、Logo、Footer | 品牌不变 |
| `options.theme.frontend` = `default` | 前端主题 |
| 端口 `127.0.0.1:3001` / `3002` | nginx 写死 |
| `/www/docker/newapi/data` | 上传文件（当前为空） |
| `/www/docker/newapi/logs` | 旧实例日志，21GB，不动 |

本次新增/调整的环境变量（只加在 canary）：

```diff
  NODE_NAME=apiduo-canary
  NODE_TYPE=slave          # 迁移时临时 master，迁完立刻改回 slave
+ DEFAULT_LANGUAGE=zh-CN   # 后端错误文案保持中文
  SQL_MAX_OPEN_CONNS=256   # 现网 MySQL max_connections=500，不能给 1024
  SQL_MAX_IDLE_CONNS=64
```

---

## 3. 已执行记录（2026-11-13）

| 时刻 | 动作 | 结果 |
|---|---|---|
| 19:49:44 | `mysqldump` 全量备份 | 58MB gz，4 秒，结尾完整 |
| 19:50 | 排练库 `jieapi_apiduo_rehearsal` 恢复 + apiduo 启动 | **2 秒** health；36→41 表；关键行数不变 |
| 19:51 | 用 rc.40 在同一排练库启动（slave） | 2 秒 health，`/api/status` 200、`/api/pricing` 200 —— **L1 回滚成立** |
| 19:53 | 预置 `LegacyAccessTokenRetireAt` | 2027-02-11（90 天，不是默认 30 天） |
| 19:53:23 | 生产 canary `apiduo-canary` 以 master 启动 | 开始 MySQL 迁移 + CH 全量复制 |
| 19:58:26 | canary health 200 | **启动耗时 302 秒**，几乎全部是 CH 复制 |
| 19:58:24 | CH 迁移客户端超时 | 应用记 `log table engine upgrade failed`，继续启动；`logs` 仍是 MergeTree |
| 20:00 | 手工 `RENAME` + 补尾 | `logs` → ReplacingMergeTree；旧表保留 `logs_mergetree_backup`（53.6GB） |
| 20:00:08 | nginx 首次 1% 上线（按主体） | 合成 300 请求命中 3；真实流量 5 分钟 0 |
| 20:04:02 | 改为按请求 1% | 合成命中正常 |
| 20:08 | 修复 `$request_id` 在 h2 为空 | 复合键；h1 2/100、h2 1/100 |
| 20:10:39 | **第一条真实 canary 请求** | 200，deepseek-v3-2，quota 23，CH 1 行 |

### 3.1 排练库验证结果（生产副本）

- 表：36 → **41**（新增 `agent_keys`、`file_objects`、`mcp_error_rules`、
  `mcp_model_profiles`、`user_access_tokens`）。
- 行数：users 4,751 / tokens 5,329 / channels 202 / tasks 5,794 —— 全部相等。
- `users.access_token` 列保留（char(32) + 唯一索引），回滚不丢数据。
- `options` 79 → 78：`CheckSensitiveEnabled` / `CheckSensitiveOnPromptEnabled` /
  `SensitiveWords` 三行消失，但日志明确写了
  **“已把敏感词移到全局请求过滤实例 敏感词”**，落到 `request_plugin_setting.instances`。
  三项当前都是关闭状态，**不是功能丢失**；回滚到 rc.40 会回到默认值（关闭/空）。
- CH 排练表引擎成功变为 `ReplacingMergeTree`。
- rc.40 在迁移后的库上启动 2 秒，读接口正常 —— **L1 回滚已实测**。

### 3.2 CH 迁移的手工收尾（重要）

应用侧失败后，`logs_replacing_staging` 实际已经写入了 **21,026,305 行**，
是快照时刻的完整数据（与 `uniqExact(created_at, request_id)` 只差复制期间新写入的 3,074 个 key）。
不要重新跑一遍 5 分钟复制，直接：

```sql
-- 1) 原子换名（毫秒级，旧表保留）
RENAME TABLE new_api_logs.logs TO new_api_logs.logs_mergetree_backup,
             new_api_logs.logs_replacing_staging TO new_api_logs.logs;

-- 2) 补快照之后的尾巴（按 staging 的 max(created_at) 取）
INSERT INTO new_api_logs.logs
SELECT * FROM new_api_logs.logs_mergetree_backup
WHERE created_at > 1794570803;
```

本次执行后：`logs` = ReplacingMergeTree / 21,027,839 行 / 52.13GB；
`logs_mergetree_backup` = MergeTree / 21,203,633 行 / 53.60GB；
rc.40 与 apiduo 都能继续写入和查询。**旧表至少保留 30 天。**

> 如果下次换机重跑，建议在 `LOG_SQL_DSN` 里显式加 `read_timeout=900s`，
> 或者直接按上面的两段 SQL 手工迁移，不要依赖应用内 300 秒的客户端超时。

---

## 4. 1% 灰度设计（当前生效）

### 4.1 路由文件

新增 http 级文件：`/www/server/panel/vhost/nginx/0.apiduo-canary.conf`（1928B）。
两个 vhost 各改一行 + 一行日志：

```diff
- proxy_pass http://jieapi_upstream;          # jieapi.com
+ proxy_pass http://$jieapi_backend;

- proxy_pass http://127.0.0.1:3001;           # api.jieapi.com
+ proxy_pass http://$jieapi_backend;

+ access_log /www/wwwlogs/apiduo-canary.log apiduo_canary if=$jieapi_canary_flag;
```

备份：`jieapi.com.conf.bak-canary-20261113-195957`、
`api.jieapi.com.conf.bak-canary-20261113-195957`。

### 4.2 分流键

```nginx
split_clients "$msec$pid$connection$connection_requests$remote_addr$request_uri" $jieapi_canary {
    50%     apiduo;
    *       legacy;
}
```

- **不要用 `$request_id`**：本机 nginx 1.31 的 h2 请求里它是空串，实测 h2 0/100。
- **不要用 `weight=1`**：按请求权重会让同一会话在 3001/3002 之间翻转，Web 面会 UI/API 错位。
- **API 面按请求**是安全的：每个 `/v1` 请求自包含，SSE 单请求粘在后端，不跨请求。
- **Web 面**以后要灰度，应改成会话 SID 粘性（`$cookie_new_api_refresh` 的第一段），不能按请求。

### 4.3 只放同步端点

```nginx
map $uri $jieapi_canary_path {
    default 0;
    ~^/v1/chat/completions$                    1;
    ~^/v1/messages$                            1;
    ~^/v1/completions$                         1;
    ~^/v1/embeddings$                          1;
    ~^/v1/rerank$                              1;
    ~^/v1/models$                              1;
    ~^/v1beta/models/[^:]+:generateContent$    1;
}
```

**排除**：`/v1/responses`（可能 background）、`/v1/videos`、`/v1/images/*`、
`/v1/audio/*`、`/v3/contents/generations/tasks`、`/mj/*`、`/suno/*`、
所有 `/api/*`（Web/支付/管理/登录）。这些留给 100% 切换前的专项 gate。

### 4.4 验证方法（可复现）

```bash
# 300 个不同 key 的 GET，期望 ~3 条落 3002
for i in $(seq 1 300); do
  curl -s -o /dev/null -m 3 -H 'Host: jieapi.com' \
    -H "Authorization: Bearer probe-$i" http://127.0.0.1/v1/models
done
wc -l /www/wwwlogs/apiduo-canary.log
```

必须**同时**跑 h1 与 h2：

```bash
for mode in --http1.1 --http2; do
  before=$(wc -l < /www/wwwlogs/apiduo-canary.log)
  for i in $(seq 1 100); do
    curl -s -o /dev/null -m 5 $mode --resolve api.jieapi.com:443:127.0.0.1 \
      -H "Authorization: Bearer mode-$mode-$i" https://api.jieapi.com/v1/models
  done
  echo "$mode $(( $(wc -l < /www/wwwlogs/apiduo-canary.log) - before ))"
done
```

本次结果：h1 2~3/100，h2 0/100（修复前）→ 1/100（修复后）。

### 4.5 观测

canary 专用日志（只记 canary，1 天约 1~2 千行）：

```
$time_iso8601  $remote_addr  $request_method  $uri  $status
$request_time  $upstream_response_time  $upstream_addr  $body_bytes_sent
$sent_http_x_oneapi_request_id  $host
```

用 `request_id` 回查 CH：

```sql
SELECT request_id, user_id, token_name, model_name, quota,
       prompt_tokens, completion_tokens, use_time, is_stream, channel_id
FROM new_api_logs.logs
WHERE request_id IN (...);
```

本次 8 条 200 全部有且只有 1 行，quota 合计 13,470，含 1 条 `is_stream=1`。

### 4.6 控制台预览开关（2026-11-13 20:35 加）

1% 只覆盖 `/v1`，dashboard 仍在 rc.40 是**设计如此**。为了先看新控制台，加了一个按浏览器的开关：

- `https://jieapi.com/__apiduo_on` → 设 `jieapi_console_canary=1`（24h），302 到
  `/dashboard/overview`；该浏览器后续所有 `jieapi.com` 请求（HTML、静态资源、`/api/*`、`/v1`）
  都走 3002。
- `https://jieapi.com/__apiduo_off` → 清 cookie，回到 rc.40。

实测：

- 无 cookie：`X-New-Api-Version: v1.0.0-rc.40`
- 有 cookie：`X-New-Api-Version: 0.0.121`，`/api/status` 的 `system_name=接JieAPI`
- `/__apiduo_on` 返回 302 +
  `Set-Cookie: jieapi_console_canary=1; Path=/; Max-Age=86400; Secure; SameSite=Lax`

注意：带 cookie 时 `/v1` 也会 100% 走 3002（不是 1%），预览完务必 `/__apiduo_off`。
浏览器如仍显示旧页面，硬刷新（Cmd+Shift+R）；旧 `index.html` 缓存会引用已不存在的 chunk。

---

## 5. 观察与放量闸门

### 5.1 50%（当前）→ 观察 24h

硬闸门（任一 FAIL 不放大）：

- [ ] canary 5xx = 0；`docker logs apiduo-canary` 无 `panic` / `ERROR` / `ACCESS_TOKEN_*`
- [ ] 注意：legacy 近 5 分钟 5xx 有 252 条（多为 503），**不能用全站 5xx 绝对值判断 canary**；
      只看 canary 日志里的 500/502/504，并与同路径 legacy 对比
- [ ] canary 401/403 比例不高于同期 legacy（排除故意用错 key 的探针）
- [ ] canary p95 ≤ legacy 同路径 p95 × 1.2
- [ ] CH 中 canary `request_id` 均有且仅有 1 行（ReplacingMergeTree 尚未合并时允许暂时重复，但 `uniqExact` 必须等于请求数）
- [ ] `is_stream=1` 至少 1 条，SSE 正常结束
- [ ] 无 `INSUFFICIENT_USER_QUOTA` 异常升高（apiduo 对任务重试改为必须可用余额）
- [ ] `logs` 最新 `created_at` 与当前时间差 < 60s

### 5.2 放量节奏

| 阶段 | 比例 | 覆盖 | 最短观察 |
|---|---|---|---|
| A | 1% | 同步 `/v1`，两个域名 | 24h（当前） |
| B | 5% | 同上 + `/v1/responses` | 24h |
| C | 20% | 同上 | 24h |
| D | 50% | 同上 | 24h |
| E | 100% | 全部 API；Web/支付在独立窗口切 | 7 天保留 3001 |

每次改百分比只改 `split_clients` 的一个数字，`nginx -t && nginx -s reload`。

### 5.3 100% 切换前必须过的专项

1. **异步任务**：`SELECT count(*) FROM tasks WHERE status NOT IN ('SUCCESS','FAILURE')` = 0。
   62 个多 Key 渠道的任务 key 绑定在 rc.40 / apiduo 之间不兼容；
   混跑期间新建的任务可能被另一版本用错 key 轮询 → 404。
2. **Web 控制台**：用内部账号直连 `:3002`（`Host: jieapi.com`）走一遍登录、
   令牌、日志、充值预览、管理页；确认前端资源不会因旧缓存 404。
3. **支付回调**：`/api/stripe/webhook`、`/api/user/epay/notify`、`/api/user/topup`
   在 100% 后各做一笔小额真实回调（或让支付方重放）。
4. **登录/2FA/Passkey**：apiduo 新增 `auth_flows`、`passkey_credentials`、
   `security proof`；管理员删除/编辑用户现在要密码/2FA 证明，
   BFF/PAT 直调会 `SECURITY_PROOF_INVALID`，人工控制台可用密码完成。
5. **core-3 备节点**：core-1 切完后，把 `jieapi-slave` 也换成 `0.0.121`（slave），
   否则故障切换会回到 rc.40（能读库，但行为旧）。
6. **版本头**：`x-new-api-version` 从 `v1.0.0-rc.40` 变 `0.0.121`，通知监控方。

---

## 6. 回滚

| 级别 | 触发 | 动作 | 耗时 | 数据影响 |
|---|---|---|---|---|
| **L1 代码** | canary 5xx/panic | 两个 vhost 把 `proxy_pass http://$jieapi_backend;` 改回原值，`nginx -t && nginx -s reload` | 1~2s | 无 |
| **L1b 停止 canary** | 怀疑 canary 资源问题 | `docker stop apiduo-canary` | 1s | 无 |
| **L2 配置** | env 改错 | 用 `/www/docker/apiduo-canary/base.env` 重建 | 1~2min | 无 |
| **L3 数据** | 迁移写坏 | 停 canary → 恢复 `mysql.sql.gz` → 起 rc.40 | 10~20min | 丢 T-0 之后写入 |

L1 一行回滚：

```bash
cd /www/server/panel/vhost/nginx
sed -i 's#proxy_pass http://\$jieapi_backend;#proxy_pass http://jieapi_upstream;#' jieapi.com.conf
sed -i 's#proxy_pass http://\$jieapi_backend;#proxy_pass http://127.0.0.1:3001;#' api.jieapi.com.conf
/www/server/nginx/sbin/nginx -t && /www/server/nginx/sbin/nginx -s reload
```

CH 已经是 ReplacingMergeTree，rc.40 照常写入/查询，**不需要回滚 CH**（已实测）。

---

## 7. 坑位审查（按严重度）

### P0 —— 不处理就出事

| # | 坑 | 证据 | 表现 | 处置 |
|---|---|---|---|---|
| 1 | CH 21.2M 行 / 53.6GB 全量复制超过客户端读超时 | 本次 302s 触发 `i/o timeout`，应用只记日志 | `logs` 仍是 MergeTree；重复日志不去重；下次重启又重试 | 手工补尾 + RENAME；或 DSN 加 `read_timeout=900s`；旧表保留 |
| 2 | `$request_id` 在 h2 为空 | h2 0/100，h1 3/100 | 真实流量全 h2 → 1% 灰度 0 流量，误判“很稳” | 用复合键；每次改分流必须 h1+h2 双测 |
| 3 | 按主体 1% 切不到活跃 key | 5 分钟 0 请求 | 灰度空转 | API 按请求 1%；Web 按 SID 粘性 |
| 4 | 异步任务多 Key 绑定跨版本不兼容 | 62 个多 Key 渠道；`GetTaskKey` 用 `MultiKeyIndex/Fingerprint`，旧任务这两项为空 | 100% 切换后旧任务轮询 404 / 新建任务被旧版本用错 key | canary 排除异步端点；切换前 `tasks` 必须全终态 |
| 5 | MySQL 无 binlog | `@@log_bin=0` | 数据回滚只能回 dump | T-0 全量 dump（58MB，4s）+ 行数快照 |
| 6 | `CRYPTO_SECRET` 仍是模板默认值 | 现网 env | 一旦“顺手换强密钥”，202 个渠道密钥全解不开 | 本次原样保留；单独排期做密钥轮换 |
| 7 | canary 以 master 起会重复后台任务 | `StartSystemTaskRunner` 只 master 跑 | 渠道检测/清理重复 | 迁移用 master，**迁完重建为 slave** |
| 8 | 旧 PAT 默认 30 天退役 | `EnsureLegacyAccessTokenRetireAt` | 17 个用户 + 脚本 30 天后 401 | 已预置 90 天（2027-02-11） |

### P1 —— 会造成局部可感/运维事故

| # | 坑 | 证据 | 处置 |
|---|---|---|---|
| 9 | `proxy_pass` 变量会让 **100% 流量**失去 upstream keepalive | 原配置专门修过 `$connection_upgrade` 空值问题 | 1% 期间可接受（~1.5 req/s）；100% 切回 `proxy_pass http://jieapi_upstream;` |
| 10 | 敏感词 3 个 options 被迁移删除 | 日志“已把敏感词移到全局请求过滤实例” | 当前关闭，无感；回滚后是默认值；要恢复需改 `request_plugin_setting.instances` |
| 11 | canary 崩溃时 1% 直接 502 | 变量 proxy_pass 无 backup | `restart=unless-stopped` 已设；观察 `docker ps`；必要时 L1 回滚 |
| 12 | 宝塔面板改站点会重写 vhost | 主配置在 `panel/vhost/nginx` | 面板改站点后用 `nginx -T` grep `jieapi_backend` 复核 |
| 13 | 管理员删/改用户要 security proof | `admin.user.delete/update` | 控制台可用密码完成；PAT/脚本直调会 403，脚本改用控制台 |
| 14 | `TRUSTED_PROXIES` 未设置 | 启动警告 | 默认信任 loopback/RFC1918，nginx 在 127.0.0.1 反代，realip 正常；**不要设 `none`** |
| 15 | 旧 CH 表 53.6GB 占盘 | `logs_mergetree_backup` | 保留 30 天；磁盘 597G 可用，不紧张 |
| 16 | `logs` 行数会比旧表少 | ReplacingMergeTree 按 `(created_at, request_id)` 去重 | 预期；查 `uniqExact` 而不是 `count()` |
| 17 | 版本号/品牌 | `x-new-api-version` 变 `0.0.121`；`SystemName` 保留 | 通知监控；品牌不变 |
| 18 | 支付/Web 不在 1% 内 | 路径白名单 | 100% 前专项 gate，见 §5.3 |
| 19 | `jieapi.com` 的 `sub_filter` 会改写 `glm-5p2` 等模型名 | vhost 里 `sub_filter` 对 `text/event-stream application/json` 生效 | canary 响应体若模型名不同，改写可能不命中、露出上游原名；24h 内看 `glm-*` 的 canary 响应，100% 前回归 |
| 20 | 重建 canary 会造成瞬时 502 | 20:53:38 两条 `/v1` 502，恰在容器重建的 ~1s 窗口 | 10% 下重建前先把分流切回 legacy（或选低谷）；不要直接 `docker rm` |

### P2 —— 记录即可

- `perf_metrics` 唯一索引、`quota_data` 7 个索引在现网已存在，AutoMigrate 是 no-op。
- `users.access_token` 列保留，回滚不丢；`tokens.key` 唯一索引迁移仅 PostgreSQL 生效。
- `tasks` 5,794 行 / 68MB，结构未变；apiduo 只改 `PrivateData` JSON。
- `audit_logs` 已在 CH，`MigrateAuditLogs` 自动建/补，不影响主库。
- `server_address` / `theme.frontend` / `SystemName` 均保留。
- `SQL_MAX_OPEN_CONNS` 现网 1024，MySQL `max_connections=500`；canary 已压到 256，
  100% 切换时只留一个实例，问题不大。

---

## 8. 待办

1. **观察 24h**（到 2026-11-14 20:00），按 §5.1 填表；无异常再放 5%。
2. 5% 时把 `/v1/responses` 纳入白名单；20% 时纳入 Gemini 之外的异步？
   **不**——异步必须等 §5.3 第 1 条。
3. 100% 窗口建议 05:00–06:00（今日 05:00 3,239 请求/小时，是全站低谷）。
4. 100% 前：`tasks` 全终态、Web/支付专项、core-3 换镜像。
5. 单独排期：`CRYPTO_SECRET` 轮换（要重加密 202 个渠道 key，不能和升级同窗口）。
6. 30 天后：删 `logs_mergetree_backup`、停 rc.40 容器、把宝塔 compose 镜像改成
   `ghcr.io/apiduo/apiduo:0.0.121`（否则面板“重新部署”会把 rc.40 拉起来）。

---

## 9. 可观测（Langfuse）接入记录

rc.40 不带 Langfuse；canary 初版直接复用 rc.40 的 env，所以**一开始没加**。
20:28 已补，参考 `newapi-bff.oneapis.cn` 的接法。

| 项 | 值 |
|---|---|
| 上报地址 | `https://newapi-bff.oneapis.cn:8443`（BFF 站点自建 Langfuse 4.80.0） |
| 控制台 | 同上 |
| 项目 | `newapi-bff`（env 文件里的 pk/sk 属于该项目的 `oneapis` 组织） |
| 环境标签 | `jieapi-com` |
| 媒体基址 | `https://jieapi.com` |
| 采样 | 元数据 **100%**（canary 只有 1% 流量，刻意全采）/ 内容 10% / 失败 100% |
| 内容模式 | `headtail`，`LANGFUSE_REDACT=true` |
| env 文件 | `/www/docker/apiduo-langfuse.env`（从 apps 复制，600） |

为什么不用 `https://langfuse.1task.cn`：core-1 上 `GET /api/public/health` 是 200，
但 `/api/public/projects` 与 `/api/public/otel/v1/traces` 都 30s 超时；
BFF 的 `:8443` 入口实测 200、OTLP 摄入 200，所以走 BFF。

验证（20:31）：

- 容器内 `GET https://newapi-bff.oneapis.cn:8443/api/public/health` =
  `{"status":"OK","version":"4.80.0"}`
- 带 pk/sk 的 `/api/public/projects` = 200，项目 `newapi-bff`
- 手工 OTLP 探针 200，`events_core` 出现 `probe` / `jieapi-com`
- 真实 canary 请求后，`events_core` 出现
  `chat glm-5.3-flash` / `GENERATION` / `jieapi-com`

注意：

- Langfuse v4 的 `traces` / `observations` 表是异步物化的，刚接入时可能还是 0；
  看 `events_core.environment='jieapi-com'` 才是摄入成功的判据。
- `LANGFUSE_PROJECT_ID=newapi-bff` 是 BFF 项目，用 environment 区分站点。
  要彻底隔离，需在 Langfuse 新建项目并换 pk/sk。
- canary 现在是元数据 100% 采样；10% 流量下实测约 570 event / 2h，量级可接受。
  放到 20% 前建议降回 10%。
- 日志页“在 Langfuse 打开”只对 root 或
  `observability_setting.trace_viewer_user_ids` 可见。

---

## 10. 升级后观察（20:47–22:36，10%）

### 10.1 结论

**正常。** 未发现 apiduo 引入的计费、功能或性能回归。3 条 502 全部可解释：
2 条是 canary 容器重建窗口（nginx error log 明确 `connect() failed (111: Connection refused)` 到 3002），
1 条是上游渠道 #248 `Upstream service temporarily unavailable`，不是网关问题。

### 10.2 与 legacy 对比（同一窗口，`/v1` 白名单路径）

| 指标 | canary（10%） | legacy | 判断 |
|---|---|---|---|
| 请求数 | 697 | 6,639 | canary 占 9.5%，接近目标 10% |
| 200 | 582（83.5%） | 5,282（79.6%） | canary +3.9pp |
| 401 | 45（6.5%） | 778（11.7%） | canary 更低 |
| 503 | 49（7.0%） | 428（6.4%） | 基本一致，业务侧“无可用渠道” |
| 403 | 10（1.4%） | 100（1.5%） | 一致，订阅额度不足 |
| 499 | 6（0.9%） | 34（0.5%） | 客户端断开，长流正常 |
| 502 | 3（0.4%） | 0 | 2 条重建 + 1 条上游 #248 |
| 非 200 | 16.5% | 20.4% | canary 不差于 legacy |

### 10.3 同模型延迟（CH `use_time`，秒）

| 模型 | canary p50/p95 | legacy p50/p95 | 样本 c/l |
|---|---|---|---|
| gemini-3-flash-preview | 5 / 21 | 5 / 46.3 | 259 / 2396 |
| glm-5.3-flash | 31 / 119.6 | 38.5 / 240.2 | 19 / 220 |
| deepseek-v4.1-flash-1017 | 19.5 / 167.2 | 16 / 183.3 | 38 / 436 |
| doubao-seed-2-0-pro | 14 / 25.6 | 11 / 27 | 9 / 96 |
| deepseek-v3.2 | 3 / 6.5 | 3 / 7 | 46 / 396 |
| mimo-v2.6-pro | 50.5 / 84.3 | 22 / 38.2 | 2 / 18（样本太小） |

长尾主要来自生成时长，不是网关开销；gemini/glm 的 canary p95 反而更低。

### 10.4 计费与资源

- canary 582 条 200 对应 582 条 `type=2` 消费日志，quota 全部为正；抽样 200 条中 6 条 `quota=0`
  是 `type=5` 的上游错误日志（如 Gemini 403），不是重复计费。
- legacy 同窗口 18,589 行 / 16,632 唯一 `request_id`，同样存在大量 `type=5` 错误日志 +
  一条 `type=2` 消费日志 —— 这是存量行为，不是 apiduo 新增。
- canary 容器：20:53:39 启动，`RestartCount=0`，CPU 0.25%，内存 150MB。
- MySQL 25 连接；Redis db8 18 clients / 6MB；CH 5 分钟 227 行、`max(created_at)=now()`。
- Langfuse `jieapi-com` 573 条 event，trace 正常；媒体外置已关闭（BFF MinIO `InvalidAccessKeyId`）。

### 10.5 仍需盯的点

- **503 基线高**：48 条“无可用渠道”集中在 user 7 的 `doubao-seed-*` / `glm-5*`，
  legacy 同期一样有；属于模型/渠道可用性，不是升级回归，但会干扰错误率判断。
- **502**：后续不要再直接重建 canary；确需重建先把分流切回 legacy。
- **异步/Web/支付**仍未灰度；100% 前的 gates 不变（tasks 全终态、多 Key 任务绑定、core-3 换镜像）。

### 10.6 22:39 放量到 50% 初检

| 项 | 结果 |
|---|---|
| 分流 | 合成 h1 148/300、h2 51/100、同 keep-alive 连接 49/100 |
| 真实稳态 | 22:46–22:51 剔除本机探针，canary 78 / main 164 ≈ 47.6% |
| canary 状态 | 72×200、6×503、2×403、1×401；0×502、0 panic |
| 503 原因 | `无可用渠道`（doubao/glm 等），legacy 同期同样有 |
| 400 原因 | 上游图片格式 `Image not recognized`，不是网关 |
| 资源 | MySQL 25、CPU 0.18%、内存 127MB、restarts 0 |

结论：50% 初检正常，继续观察；不要在 50% 下直接重建 canary。

### 10.7 50% 两小时复核（22:39–00:37）

| 指标 | canary | legacy | 差 |
|---|---|---|---|
| 请求数 | 4,223 | 4,344 | canary 占 **49.3%** |
| 200 | 3,686（87.3%） | 3,815（87.8%） | -0.5pp |
| 503 | 242（5.7%） | 236（5.4%） | +0.3pp |
| 401 | 221（5.2%） | 217（5.0%） | +0.2pp |
| 403 | 35（0.8%） | 29（0.7%） | +0.1pp |
| 499 | 21（0.5%） | 23（0.5%） | 0 |
| 400 | 18（0.4%） | 20（0.5%） | -0.1pp |
| **502** | **0** | **0** | 0 |

- 401 里 618 条来自 `156.238.230.218` 的扫描（canary 208 / legacy 410），不构成偏差；
  上一版本条写成"约 410 条、各半"有误，实际是 618 条，特此更正。
- 503 全是 `无可用渠道`（doubao/glm 等），legacy 同期一样，属于模型/渠道可用性；
  逐模型归因见 §10.8。
- 0 条 502、0 panic；canary p50 4.79s / p95 44.8s / p99 121.5s，与 10% 窗口持平。
- 抽样 369 条 200：347 条 `type=2` 消费日志（1.52M quota，0 负），16 条 `type=5` 错误日志
  （quota=0）；15 个 `request_id` 有重复行，均为“错误日志 + 一条消费日志”，legacy 同模式，
  不是重复计费。
- 资源：MySQL 44 连接、CPU 0.57%、内存 169MB、`RestartCount=0`；CH 296 行/5min；
  Langfuse `jieapi-com` 4,164 event。

**结论：50% 两小时无 apiduo 回归。** 状态码分布与 legacy 的差异全部在 ±0.5pp 内，属采样噪声；
502 为 0；计费无负额度、无重复消费日志。可以进入 100% 评估，但建议至少再覆盖一个白天高峰。

### 10.8 错误按模型归因（22:39–00:37）

`/v1` 白名单、剔除本机合成探针；legacy = 主站日志减去同一批 canary `request_id`。

| 状态 | canary（4,220） | 占比 | legacy（4,347） | 占比 |
|---|---|---|---|---|
| 200 | 3,683 | 87.3% | 3,818 | 87.8% |
| 503 | 242 | **5.73%** | 236 | **5.43%** |
| 401 | 221 | 5.24% | 217 | 4.99% |
| 403 | 35 | 0.83% | 29 | 0.67% |
| 499 | 21 | 0.50% | 23 | 0.53% |
| 400 | 18 | 0.43% | 20 | 0.46% |
| 429 / 404 | 0 | 0 | 2 / 2 | 0.09% |

#### 503 = `distributor.no_available_channel`，全部是分组内无启用渠道

文案来自 `i18n/locales/*.yaml` 的 `distributor.no_available_channel`
（`middleware/distributor.go` 渠道选择阶段返回，1ms 内响应，**不发起上游请求、不写 CH 日志**）。
rc.40 是同一套逻辑，只是英文文案，所以两边同时出现同一批模型。

去重请求数（按 `request_id`）：canary 250 / legacy 340。legacy 口径里混有未过本机 nginx
的入口流量，绝对数仅供模型分布参考。

| 模型 | 分组 | canary | legacy | `abilities` 启用渠道 |
|---|---|---|---|---|
| doubao-seed-1-8-251228 | cn | 77 | 68 | **0**（唯一 #53 已被自动禁用 status=3） |
| doubao-seed-2-0-code-preview-260215 | volcengine | 39 | 69 | **0**（`channels.models` 里没有任何渠道含此模型） |
| glm-5 | cn | 33 | 40 | **0**（5 条 abilities 全 `enabled=0`；#215/266/520/522 只有 `glm-5.3*`） |
| doubao-seed-1-6-250615 | volcengine | 10 | 15 | **0**（无 ability 行） |
| glm-5.1 | cn | 10 | 12 | **0**（11 条 abilities 全 disabled） |
| qwen3.7-max | cn | 9 | 6 | **0**（7 条 abilities 全 disabled） |
| doubao-seed-2-0-mini-260215 | volcengine | 0 | 20 | **0**（无 ability 行） |
| doubao-seed-2-0-mini-260428 | volcengine | 4 | 10 | **0**（无 ability 行） |
| gemini-2.5-flash | gemini-cli | 2 | 0 | **0**（#49 status=2 手动禁用） |
| 长尾 20+ 个 volcengine 模型 | volcengine | 66 | 80 | 0 |
| **合计** | | **250** | **340** | |

前 6 个模型占 canary 503 的 **71%**、legacy 的 **62%**。`abilities` 与 `channels` 无漂移
（`status=1` 但无启用 ability 的渠道数 = 0），即"确实没有渠道"，不是路由表损坏。

#### 上游错误（CH `type=5`，请求已发出、上游返回 4xx/5xx）

| 模型 | 上游状态 | 渠道 | canary | legacy | 文案 |
|---|---|---|---|---|---|
| gpt-4o-2024-08-06 | 429 | #537 / #539 | 95 | 107 | 当前分组上游负载已饱和（上游限流） |
| minimax-m3 | 404 | #522 | 21 | 16 | 上游没有模型 MiniMax-M3 |
| deepseek-v4-flash | 400 | #520 / #266 | 13 | 17 | prompt is too long: 1.42M tokens > 1M |
| gemini-3-flash-preview | 500 | #36 | 5 | 14 | upstream error: do request failed |
| gemini-4-argon | 503 | #277 | 6 | 9 | 分组 Gemini-Anti 下无可用渠道（上游透传） |
| gpt-5.6-luna | 503 | #183 | 3 | 11 | Service temporarily unavailable |
| gpt-4o | 429 | #515 | 0 | 10 | 上游负载已饱和 |

`gpt-4o-2024-08-06` 的 429 是单一最大错误源（202/370 条 type=5），消息是上游分发器限流，
与网关无关。

#### 401 / 403

- **401**：扫描器 `156.238.230.218` 共 618 条（canary 208 / legacy 410）；
  user 7 的无效令牌 canary 228 / legacy 231。前者是公网扫描，后者是客户手里是错 key。
- **403**：订阅额度不足，canary 35 / legacy 29，用户级账单判定，与模型无关；
  集中在 `159.75.91.221`（16）与 `107.173.31.62`（14）。

**结论**：503 的 71% 集中在 8 个"分组内确实没有启用渠道"的模型上，属渠道配置问题；
同一时刻 legacy 打同样的模型也报同样错误。上游 4xx/5xx 按模型一一对应，
没有出现"只在 canary 上出错"的模型。**无升级引入的模型级回归。**

### 10.9 用 remap 渠道补齐无渠道模型（2026-11-14 01:05）

`remap.oneapis.cn`（apps 上的 `ghcr.io/aichatfire/remap-service`）本身是**通配符映射网关**：
替换规则由渠道的 `header_override["X-Model-Map"]` 决定，默认 `UPSTREAM_BASE` 是火山 Ark。
`*:<模型>` 表示该渠道下所有请求都换成目标模型。

实测（用 #139 的 key 直连 remap，带 `X-Model-Map` 头）：

| 请求模型 | 映射目标 | remap 结果 |
|---|---|---|
| doubao-seed-1-8-251228 | doubao-seed-2-0-mini-260428 | 200 |
| doubao-seed-1-6-250615 | doubao-seed-2-0-mini-260428 | 200 |
| doubao-seed-2-0-code-preview-260215 | doubao-seed-2-0-mini-260428 | 200 |
| glm-5 / glm-5.1 | glm-5-2-260617 | 200 |
| qwen3.7-max | — | 仅有 `qwen3-32b/14b` 且 404，唯一 200 是跨厂商换成 doubao mini，**不采用** |

新建两个渠道（克隆 #139，priority 100、weight 0、tag 沿用、auto_ban=1）：

| id | 名称 | 分组 | 模型 | X-Model-Map |
|---|---|---|---|---|
| 542 | remap-doubao-补齐 | cn,volcengine | 上述 3 个 doubao | `*:doubao-seed-2-0-mini-260428` |
| 543 | remap-glm-补齐 | cn | glm-5, glm-5.1 | `*:glm-5-2-260617` |

`abilities` 直接写入（6 + 2 行，enabled=1、priority=100、weight=0），
两个进程的 `SyncChannelCache`（`SYNC_FREQUENCY=60`）在 60s 内自动重载，无需重启。

**glm 的坑**：首轮全部 400，`max_tokens` 与 `max_completion_tokens` 同时下发被上游拒。
给 #543 加条件参数覆盖后解决：

```json
{"operations":[
  {"mode":"delete","path":"temperature"},
  {"mode":"delete","path":"max_completion_tokens",
   "conditions":[{"path":"max_tokens","mode":"gt","value":0}]}
]}
```

效果（01:20 后）：

| 模型 | 成功请求 | quota | 残留 503 |
|---|---|---|---|
| doubao-seed-1-8-251228 | 2 | 232 | 0 |
| doubao-seed-2-0-code-preview-260215 | 8 | 1,142 | 0 |
| doubao-seed-1-6-250615 | 3 | 29 | 0 |
| glm-5 | 2 | 105 | 0 |

**必须知道的三件事**：

1. **这是静默降级**：1-8 / 1-6 / code-preview 实际由 `doubao-seed-2-0-mini-260428` 作答，
   glm-5 / glm-5.1 由 `glm-5-2-260617` 作答；但计费仍按请求的模型名算。
   若不调整价格或下架，等于按旗舰价卖 mini。
2. 只补了 `cn` / `volcengine` 两个分组。`kimi` / `default` 分组打这些模型仍会 503
   （用户 35 在遍历分组探测）。
3. `qwen3.7-max` 没补：库里的百炼 key 全失效（#8 dashscope 401、#233 remap 401、#4 空），
   remap 也没有可用的 qwen 目标。需要新的百炼 key 才能补。

### 10.10 100% 全量运行（00:53–02:13，81 分钟）

00:52:48 把 `split_clients` 放到 100%，只覆盖 `/v1` 同步白名单；Web/异步任务/支付仍在 rc.40。

**路由确认**：取 02:10 整分钟对账，主站日志白名单路径 83 条 = canary 日志 83 条，
`upstream_addr` 全部为 `127.0.0.1:3002`，差值为 0。
注意：主站日志本身**也记录 canary 的请求**（两个 `access_log` 并存），
直接用主站计数当 legacy 会重复计算——判 100% 是否生效必须做差值。

| 指标 | 数值 | 占比 |
|---|---|---|
| 白名单请求 | 6,576 | — |
| 200 | 5,966 | **90.7%** |
| 401 | 292 | 4.44% |
| 503 | 152 | 2.31% |
| 403 | 111 | 1.69% |
| 499 | 35 | 0.53% |
| 400 | 19 | 0.29% |
| 429 | 1 | 0.02% |
| **502 / 504** | **0** | 0 |

延迟：p50 4.81s / p90 33.57s / p95 41.04s / p99 69.21s / max 353.19s（长生成）。

与 50% 窗口对比：200 从 87.3% 升到 90.7%，503 从 5.73% 降到 2.31%（remap 补渠道的直接效果）；
403 从 0.83% 升到 1.69%，全部来自单个客户 `203.195.206.154`（75/111）的订阅额度耗尽，与升级无关。

**计费**（CH `type=2`）：5,923 行、36,569,427 quota、**0 条负额度**、**0 个重复计费请求**；
`type=5` 76 行、额度全为 0；`type=6` 1 行。新渠道 542/543 累计 40 次成功
（1-8 2 次 / 1-6 8 次 / code-preview 9 次 / glm-5 20 次 / glm-5.1 1 次）。

**资源**：CPU 0.85%、内存 172.6MiB/20GiB、`RestartCount=0`；MySQL 56 连接；
Redis 17 clients；CH 日志写入延迟 0s；无 panic。

**Langfuse**：`jieapi-com` 环境 5,699 条 `GENERATION`、5,699 个唯一 trace，
覆盖 6,576 条请求的 **86.7%**（401/403/503 不产生 generation span，缺口合理）。
模型分布与实际流量一致：gemini-3-flash 1,787 / deepseek-v3.2 1,721 /
doubao-seed-2-0-lite-260215 859 / minimax-m3.1-flash 202 / glm-5.3-flash 143 / gpt-5.6-luna 139。

⚠️ **Langfuse 的 ClickHouse 曾经过载（已在线扩容修复）**：放量到 100% 后事件量放大近 10 倍，
worker 出现 `memory limit exceeded`（改前 5 分钟 84 次，约 17 次/分），
CH 容器 2GB 上限被顶到 1.883GB、CPU 177%。

**动态扩容可行，无需重启**：`docker update --memory 8g --memory-swap 16g <container>` 后，
cgroup 立即变 8GB，CH 的 `CGroupMemoryTotal` 立刻反映新值，内存实际用到 3.69GB
（已超过原 1.80GB 硬限），OOM 归零。`SYSTEM SET max_server_memory_usage` 语法层不支持，
只能走 cgroup + `SYSTEM RELOAD CONFIG`。compose 原本写死 `mem_limit: 2g`，
已改为 `8g` / `memswap_limit: 16g`（备份 `docker-compose.yml.bak-20261114-0227`），否则 recreate 会退回。

**更正**：`traces` / `observations` 为 0 行 **不是故障**。Langfuse v4 已移除这两个接口
（返回 `This endpoint is not available on deployments running in Langfuse v4 events_only`），
数据都在 `events_*` 表。摄入是连续的：每分钟 34–84 条，最新一条到 18:24:34 UTC。

**插入调优（已落地，2026-11-14 02:35）**：

写死 `content 采样`不做改动（链路采样保持 100%），只收紧 CH 侧参数。
`clickhouse` 用户用的是 `default` profile，通过 `users.d/20-langfuse-tuning.xml`
扩展该 profile，`SYSTEM RELOAD USERS` 在线生效，未重启容器：

| 参数 | 默认 | 现值 | 作用 |
|---|---|---|---|
| `async_insert_max_data_size` | 10 MB | **2 MB** | 单次 flush 数据量 |
| `max_insert_block_size` | 1,048,576 | **65,536** | 单块解析内存 |
| `max_bytes_before_external_sort` | 0（禁用） | **256 MB** | 排序溢写磁盘 |
| `max_bytes_before_external_group_by` | 0（禁用） | **256 MB** | 聚合溢写磁盘 |
| `max_threads` | auto(48) | **8** | 并行内存峰值 |
| `max_memory_usage` | 0（=服务端上限） | **5 GB** | 单查询硬顶 |

配置已入库：`deploy/langfuse/`（`README.md`、`clickhouse/20-langfuse-tuning.xml`、
`docker-compose.override.yml`），线上 `/opt/langfuse/clickhouse-tuning/` 与仓库文件逐字节一致，
compose 已加只读挂载，recreate 不会丢。

**未覆盖项**：`volcengine` 组下 `doubao-seed-2-0-mini-260428` 仍无渠道（本窗口 46 次 503），
不在原清单内；`qwen3.7-max` 9 次 503；`kimi` / `default` 组打这些模型仍 503。

### 10.11 Langfuse 主实例切到 langfuse.apiduo.cn（03:07）

原来 apiduo 的 `LANGFUSE_HOST` 指向 apps 上的 `newapi-bff.oneapis.cn:8443`（A）。
core-1 的 `langfuse.apiduo.cn`（B）是独立全栈，从此作为主实例。

B 先做了三项修复（都要重建 web/worker）：

| 改动 | 原因 |
|---|---|
| `WRITE_MODE: dual → events_only` | dual 是 v3 迁移态，写入/存储翻倍；B 是全新库，无历史数据 |
| `HOSTNAME: "0.0.0.0"` | Next.js standalone 用 `HOSTNAME` 决定监听地址，Docker 默认给容器 ID，导致只绑容器 IP、健康检查永久 `starting` |
| `NODE_OPTIONS: --max-old-space-size=3072` | 4G 容器给 Node 合理堆上限 |

切换过程（**零失败请求**）：

1. `split_clients` 不接受 `0%`（nginx 报 `invalid percent value "0%"`），改用把
   `map` 里的 `"apiduo1"` 目标临时改成 `127.0.0.1:3001`，reload 后流量全部回 legacy；
2. 用 `/www/docker/apiduo-langfuse-b.env`（B 的 pk/sk、`LANGFUSE_HOST=https://langfuse.apiduo.cn`、
   环境 `jieapi-com`、链路采样 100%、内容采样 10%、媒体不外部化）重建 `apiduo-canary`，2s 健康；
3. `map` 改回 `3002`，reload。

验证：B 的 `events_core` 出现 `environment='jieapi-com'` 事件 68 条（最新时间与切换时刻吻合），
模型分布与实时流量一致（gemini-3-flash 53 / kimi-k2.5 5 / glm-5.3-flash 3 …）；
web / worker / CH 三个容器全部 `healthy`；canary 侧 0 条上报错误。

A 停止接收新数据，历史 1.1 万条留在 A 上，建议保留 7 天后下线。
注意 B 是 `events_only`，`/api/public/traces` 已不可用，读历史只能回 A。

### 10.12 升级 24 小时体检（11-14 00:53 → 23:13）

**结论：apiduo 本体无异常；当天成功率只有 69.6%，全部由两个上游故障和一个配置缺口造成。**

apiduo 自身指标：

| 项 | 值 |
|---|---|
| 路由 | 90,928 条 `/v1` 请求，`upstream=127.0.0.1:3002` 占 99.99%（7 条是空 upstream 的探测） |
| 容器 | `RestartCount=0`、`OOMKilled=false`、panic 0、CPU 2.38%、内存 177 MiB / 20 GiB |
| 计费 | `type=2` 70,164 行、462,735,598 quota、**0 条负额度**、**0 个重复计费请求** |
| 依赖 | MySQL 35 连接、Redis 35 clients / 6 MB、CH 写入延迟 0s、根分区 77%（可用 823 G） |

分段成功率（`/v1` 白名单）：

| 时段 | 请求 | 200 | 成功率 | p50 / p95 |
|---|---|---|---|---|
| 00:53–16:00 | 59,859 | 51,117 | 85.4% | 5.3s / 22.5s |
| 16:00–21:00 | 25,679 | 8,334 | **32.5%** | 2.0s / 32.8s |
| 21:00–23:13 | 5,397 | 3,883 | 71.9% | 2.8s / 28.0s |

非 200 里 401（4,809，扫描器）和 403（2,674，订阅额度）占大头，5xx 才是真实故障：

| 故障 | 时间 | 证据 | 归因 |
|---|---|---|---|
| `all accounts are temporarily unavailable` | 08:00 起、16:00–21:00 高峰 | 59,615 条，模型 `deepseek-v3.2` / `deepseek-chat`，渠道 **#269**（remap-deepseek） | 上游账号池被打空，21:00 后自愈 |
| `The upstream stream ended before completion` | 18:00 起持续到现在 | 40,838 条，模型 `gemini-2.5-flash`，渠道 **#36 / #48**（amutes） | 上游流式中断；近 10 分钟仍有 1,557 条 |
| `models/gemini-2.5-flash is no longer available to new users` | 全天 | 5,198 条 | Google 侧弃用该模型 |

关键判断依据：`stream ended before completion` 这句在 apiduo 源码和 i18n 里**都不存在**，
是上游透传；且在 11-13（升级前）全天出现 **0 次**，说明不是 apiduo 引入。
apiduo 只是重试把大部分失败吸收成了 200，代价是延迟上升（22:29–22:40 有 43 条 502，
耗时 141–166s，是重试耗尽后的最终失败）。

配置缺口：`mimo-v2.6-flash` 无启用渠道，全天 1,185 条 `no available channel`。

**Langfuse B**：`jieapi-com` 64,078 条事件，`events_full` 22 小时涨到 **12.55 GiB**（约 13.7 GB/天），
worker 无 OOM，CH 936 MiB / 8 GiB。按此增速，30 天约 410 GB——需要设保留期，否则会和业务 CH 抢磁盘。

**待办**：

1. `gemini-2.5-flash` 在 #36/#48 上已经不可用，建议直接下架或 remap 到 `gemini-3-flash-preview`；
   当前每分钟制造 1,500+ 条无效重试，只增加延迟。
2. 找 #269 的提供方确认账号池容量，避免再次整点打空。
3. 补 `mimo-v2.6-flash` 渠道，或从定价页下架。
4. 给 Langfuse `events_full` 设保留期（14–30 天），并评估 `/www/wwwlogs`（58 G，仍未切割）。

### 10.13 gemini-2.5-flash 已 remap 到 gemini-3-flash-preview（23:42）

先定位到真实失效面：**非流式正常，流式才失败**。

| 测试 | 结果 |
|---|---|
| #36 `gemini-2.5-flash` 非流式 | 200 |
| #36 `gemini-2.5-flash` 流式 | `The upstream stream ended before completion.`（amutes 自己返回） |
| #36 `gemini-3-flash-preview` 流式 | 200，5 条 SSE + `[DONE]` |
| #48 `gemini-3-flash-preview` 流式 | 200 |

两个渠道的失败量：`#36` 32,313 条 502 + 3,801 条 404；`#48` 10,576 条 502 + 1,407 条 404
（成功量 #36 7,078 / #48 243）。

处置：给两个渠道加模型重定向，不动模型列表、不需要重建 abilities：

```sql
UPDATE channels SET model_mapping='{"gemini-2.5-flash":"gemini-3-flash-preview"}'
WHERE id IN (36,48);
```

渠道缓存 60s 内自动生效。效果（改后 3 分钟）：

| 指标 | 改前 | 改后 |
|---|---|---|
| `gemini-2.5-flash` 错误（10 分钟） | 1,500+ | **0** |
| 全局 type=5 错误（3 分钟） | 上万 | **5** |
| nginx 5 分钟 5xx | 数百 | 3（503） |

**计费注意**：`model_mapping` 只改上游模型名，计费仍按请求的 `gemini-2.5-flash` 结算。
如果 `gemini-3-flash-preview` 的上游成本更高，毛利会被压缩——要么把 2.5-flash 的售价调到与 3-flash 一致，
要么在定价页标注"已升级至 3-flash"。这套用表达式计费（`billing_mode=tiered_expr`），
`ModelRatio` 里查不到，需要到计费配置里单独核。

`vertex` 组的 #519 也列了 `gemini-2.5-flash`，暂未动（该组量小），后续同样处理或下架。

---

## 11. 全面转正（2026-11-15 01:03）

`jieapi.com` / `api.jieapi.com` 的**所有路径**（Web 控制台、`/api/*`、支付回调、异步任务、`/v1`）
全部由 apiduo `0.0.121` 承载。rc.40 保留为热备，不再承接流量。

### 11.1 转正前核查

| 项 | 结论 |
|---|---|
| 在途异步任务 | 无（4,899 SUCCESS / 1,269 FAILURE 全终态） |
| 主节点 | 只有 `jieapi-master`（rc.40），`apiduo-canary` 是 slave |
| 支付路由 | `/api/user/epay/notify`、`/api/subscription/epay/notify` 在 apiduo 中均存在 |
| 备份 | MySQL 58 MB（`/www/backup/jieapi-upgrade/cutover-20261115-010230/`）+ 三份 nginx 配置 |

### 11.2 切换顺序（关键）

后台任务只能有一个 master，`common.IsMasterNode` 是静态标记（`NODE_TYPE`），
两个 master 会**重复执行**轮询、渠道检测、订阅重置等定时任务。所以顺序必须是：

1. **先切流量**：`map $uri $jieapi_canary_path` 的 `default 0;` 改成 `default 1;`，
   全站瞬间走 3002（web/API/支付同时切）；
2. **再降级 legacy**：停止、改名 `jieapi-master` → `jieapi-master-rc40`，
   用同一份 env + `NODE_TYPE=slave`、`NODE_NAME=new-api-standby` 重建为 3001 热备；
3. **最后升 apiduo**：把 `"apiduo1"` 临时指向 3001 排空 → 重建 canary 为 `NODE_TYPE=master` → 指回 3002。

顺序反了就会出现双 master；先降级再切流量，则会有一段所有节点都是 slave 的空档。

### 11.3 结果验证

| 项 | 实测 |
|---|---|
| 版本 | `/api/status` → `version=0.0.121`，`system_name=接JieAPI` |
| 真实登录 | 15 分钟 31 次 `/api/user/login`，**全部 200** |
| 管理台接口 | 42×200、4×204、1×401（过期会话） |
| 前端 | 首页 200、静态 JS 200（332 KB） |
| 支付路由 | `/api/user/epay/notify` 返回 200（路由可达，非 502） |
| 异步任务 | `系统任务执行器已启动 runner=apiduo-canary-...`；任务表持续有新的终态记录 |
| 资源 | CPU 0.7–2.1%、内存 130–166 MiB / 20 GiB、MySQL 36 连接 |

**代价**：排空只挡住了新请求，没有等在途请求结束，`docker rm -f` 时打断了 5 条在途请求，
表现为 01:03:16 的 5 条 502（耗时 31–148 s）。下次排空后应等 10–30 s 再删容器。

### 11.4 回滚

秒级：把 `0.apiduo-canary.conf` 里 `default 1;` 改回 `default 0;`，reload。
3001 热备是 `slave`，可直接承接流量；若要它跑后台任务，需再把 `NODE_TYPE` 改回 `master`。

### 11.5 历史数据

| 数据 | 处置 |
|---|---|
| MySQL | 未迁移，两个版本共用同一个库（升级时已由 apiduo 完成结构迁移） |
| ClickHouse 业务日志 | 已迁移到 `ReplacingMergeTree` 表，21.25M 行；旧表 `logs_mergetree_backup`（53.6 GiB）**保留**，磁盘 77%/823 G 可用，不急删 |
| Langfuse A 历史 | 1.1 万条留在 `newapi-bff.oneapis.cn:8443`，保留 7 天后下线 |
| Langfuse B | `langfuse.apiduo.cn`，`events_full` 约 13.7 GB/天，需设保留期 |

## 附录 A：当前文件与容器

| 项 | 值 |
|---|---|
| canary 容器 | `apiduo-canary`，`ghcr.io/apiduo/apiduo:0.0.121`，`127.0.0.1:3002`，**`NODE_TYPE=master`（2026-11-15 转正后）**，`restart=unless-stopped` |
| canary env | `/www/docker/apiduo-canary/base.env` + `/www/docker/apiduo-langfuse-b.env`（600，指向 langfuse.apiduo.cn） |
| canary 数据/日志 | `/www/docker/apiduo-canary/{data,logs}` |
| 热备容器 | `jieapi-master`（rc.40，`NODE_TYPE=slave`，`NODE_NAME=new-api-standby`，3001，`restart=unless-stopped`）；原容器改名 `jieapi-master-rc40` 已停止，保留回滚 |
| 热备 env | `/www/docker/newapi/base.env`（600，从原容器导出） |
| 扩容 | `docker network connect shared-net apiduo-canary|jieapi-master`（均已挂） |
| nginx 路由 | `/www/server/panel/vhost/nginx/0.apiduo-canary.conf`（`default 1` = 全量，改 0 回滚） |
| canary 日志 | `/www/wwwlogs/apiduo-canary.log`（转正后记录全站流量） |
| 备份 | `/www/backup/jieapi-upgrade/20261113-194944/mysql.sql.gz`（升级前）<br>`/www/backup/jieapi-upgrade/cutover-20261115-010230/`（转正前，含 nginx 配置） |
| 排练库 | `jieapi_apiduo_rehearsal`（可随时 DROP） |
| 排练 CH | `new_api_logs_rehearsal`（可随时 DROP） |

## 附录 B：常用命令

```bash
# 健康
curl -s http://127.0.0.1:3001/api/status | head -c 120
curl -s http://127.0.0.1:3002/api/status | head -c 120

# canary 真实请求（排除本机探针）
awk -F'\t' '$2!="127.0.0.1"' /www/wwwlogs/apiduo-canary.log | tail -20

# canary 错误
docker logs --since 30m apiduo-canary 2>&1 | grep -iE 'ERROR|panic|failed'

# CH 引擎与行数
docker exec clickhouse clickhouse-client --query \
  "SELECT table,engine,sum(rows),formatReadableSize(sum(bytes_on_disk)) FROM system.parts \
   WHERE database='new_api_logs' AND active AND table LIKE 'logs%' GROUP BY table,engine"

# 回滚（1~2 秒）
cd /www/server/panel/vhost/nginx
sed -i 's#proxy_pass http://\$jieapi_backend;#proxy_pass http://jieapi_upstream;#' jieapi.com.conf
sed -i 's#proxy_pass http://\$jieapi_backend;#proxy_pass http://127.0.0.1:3001;#' api.jieapi.com.conf
/www/server/nginx/sbin/nginx -t && /www/server/nginx/sbin/nginx -s reload
```

---

## 12. 版本升级 0.0.121 → 0.0.195（2026-11-22 21:48）

跨度 176 个提交 / 74 个版本。结论：**升级成功，无功能回归；代价是 master 重建窗口内 8 条 POST 502。**

### 12.1 执行顺序与依据

1. **备份**：`mysqldump` 60 MB gz（`/www/backup/jieapi-upgrade/pre-0195-20261122-214812/`），
   同目录存 compose、app.env、行数快照（users 4756 / tokens 5392 / channels 212 /
   tasks 7589 / quota_data 424758 / options 119）与旧镜像 sha。
2. **先升 master（core-1）**：迁移在本地 MySQL 上做；nginx 把连接错误交给 core-3 备节点
   （仍是 0.0.121，已知可用），比先升备节点、让主站 failover 到未验证的新版本安全。
3. **再升 core-3 备节点**：`docker stop -t 5` 压短重建窗口。

### 12.2 迁移实际发生的事

| 项 | 结果 |
|---|---|
| MySQL 表 | 41 → **50**（新增 `change_events`、`channel_costs`、`ops_incidents`、`ops_shifts`、`user_growth_stats` 等） |
| ClickHouse | `logs` 新增 `use_time_ms Int32 DEFAULT 0`（元数据级，秒级完成） |
| `MigrateFileUploadPurposes` | 未触发（本站无 `file_relay.purposes`，无 `FixAbility` 全表重写） |
| 启动耗时 | master 3 s 可用；core-3 27 s（跨 137 ms 回库） |

### 12.3 重建窗口的真实代价：8 条 502

21:48:47–21:48:56（9 秒）内 `jieapi.com` 8 条、`api.jieapi.com` 1 条 502，
error log 均为 `connection reset by peer` / `upstream prematurely closed connection`。

**根因不是配置错误**：nginx 默认 `proxy_next_upstream error timeout` 只重试**幂等**请求，
POST 要重试必须显式加 `non_idempotent`；所以容器重建期间的 POST 不会切到 backup。
以后要零 502，只有两条路：① 先在 upstream 摘掉目标节点再重建；② 加 `non_idempotent`
（等于允许 POST 重放到另一个后端，对计费/任务端点有重复提交风险，不建议）。

### 12.4 升级后行为变化（需运营知晓）

- **公开异步图片入口 `/async/v1/images/generations` 在 0.0.121 上不存在**（返回 SPA HTML），
  0.0.195 起真实路由（无凭证 401 JSON）。本站 618/619 两个 8001 渠道早就按这个别名配置，
  升级前形同未接线，升级后**开始真的能接单计费**。历史 7 天这两个渠道合计 5 行日志。
- 新增增长运营离线聚合（master 启动即算一次，之后每 30 分钟；只读 `quota_data`/`logs`，
  写派生表 `user_growth_stats`）。可用 `GROWTH_TASKS_DISABLED=1` 关闭。
- 任务轮询报失败改为**二次确认**才落终态并退款；进度 100% 但非终态仍继续轮询。
- 计费表达式引入快照缓存（空 `ExprHash` 串味的 bug 已在 0.0.195 内修好）。

### 12.5 升级暴露的两个配置缺口（未改，待决策）

| 警告 | 影响 | 建议 |
|---|---|---|
| `SESSION_COOKIE_SECURE` 未设 | HTTPS 站点仍下发非 Secure 的 refresh cookie，且 refresh/logout 的 Origin 校验关闭 | 评估后设 `SESSION_COOKIE_SECURE=true`（站点全程 HTTPS，nginx 已设 `X-Forwarded-Proto`） |
| `TRUSTED_PROXIES` 未设 + 1 个令牌限制来源 IP | 默认信任所有私网网段 ⇒ 同 docker 网络内可伪造 `X-Forwarded-For` 绕过该令牌的 IP 限制 | 若要收口，需按 docker 网关实际源地址配 `TRUSTED_PROXIES`，不能直接写 `127.0.0.1`（会丢真实 IP） |

### 12.6 回滚

镜像 `ghcr.io/apiduo/apiduo:0.0.121` 仍在两台机器本地；把 compose 的 tag 改回并
`docker-compose up -d` 即可。库结构只增不减，旧版本可直接读；CH 多出的 `use_time_ms` 无影响。
