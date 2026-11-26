# 站点清单与运维细节

`MEMORY.md` 只留硬约束，主机 IP / 路径 / 回滚命令这类手册信息放这里。新增站点也写这里。

## jieapi.com（core-1）

- 入口 nginx → `upstream jieapi_apiduo`：`127.0.0.1:3002` 为主，core-3 `61.111.253.243:3003 backup`。
- 容器 `jieapi-apiduo` 跑后台任务（billing 出账、增长派生、保留期清理），`app.env` 在
  `/www/docker/apiduo/`。**`CRYPTO_SECRET` 与 `SESSION_SECRET` 不可换** —— 换了等于所有会话与密文作废。
- core-3 是 slave（回库延迟 137 ms，只做灾难接管），另有 `cn.jieapi.com` / `aapi.cn` 两个入口；
  **停它之前必须反查所有 nginx upstream**，别只看 jieapi.com。

## jieapi.cn（core-4）

- `110.42.51.129`，容器 `jieapi-cn-apiduo`，目录 `/www/docker/apiduo-cn/`。
- 数据：MySQL `jieapi-master-cn`、Redis db8、ClickHouse `new_api_logs`（同机 `clickhouse` 容器）。
- nginx 单上游 `127.0.0.1:3000`，**没有备节点**：任何重建窗口都是必然 502（2026-11-24 升 0.0.226
  实测 6 s / 22 条）。
- **拉镜像**：该机直连 GHCR 只有 ~200KB/s，走 `ghcr.nju.edu.cn` 拉到本地再 `docker tag` 成官方名，
  **必须比对 digest** 才算同一镜像。
- **升级容器**：2026-11-24 之前是 `docker run` 创建（无 compose 标签），`docker-compose up -d` 会报
  name conflict，需先 `stop -t 5` + `rm`；现已归 compose 管理。
- **自助注册是关的**（`RegisterEnabled=false`、`PasswordRegisterEnabled=false`，Turnstile 开着）：
  要验客户视角只能后台建号；root 的「我的账单」就是客户投影（导出名带 `-u1-draft-customer`）。
- 备份与回滚：`/www/backup/jieapi-cn-upgrade/`，报告
  `docs/apiduo/jieapi-cn-upgrade-0.0.226-2026-11-24.md`，部署档 `deploy/jieapi-cn/`。

## newapi-bff.oneapis.cn（apps）

- `154.40.45.22`，容器 `apiduo-canary` 绑 `127.0.0.1:3002`。**非 compose**：`docker run` +
  `/www/docker/apiduo-canary/deploy.sh <tag>`，升级前必须 `stop` + `rm`。
- 数据：MySQL `newapi-for-bff`（宿主，`tasks` 单表 2GB）、CH 容器 `newapi-for-bff-clickhouse`、
  Redis db0。
- 域名：客户 API 是 `workbuddy.oneapis.cn`（`location /v1` → 3002），控制台是 `newapi-bff.oneapis.cn`，
  **切流量必须两个域名一起切**。
- 端口：**3003 被 `newapi-for-tasks` 占用**（calciumion/new-api，2026-10-20 起）。预热/预演用 3005+，
  先 `ss -lntp` 判空。就绪断言必须比对 `/api/status` 的**版本号** —— 那台别的实例同样回 200
  （版本头 `v1.0.0-rc.40`），只看 200 会被骗过去。
- 日志不在 `docker logs`：`--log-dir /app/logs` 写文件，启动告警/迁移/慢 SQL 读
  `/www/docker/apiduo-canary/logs/oneapi-*.log`。
- 备份与回滚：`/www/backup/apiduo-upgrade-20261124/`（含 `rollback-run.sh`），报告
  `docs/apiduo/newapi-bff-upgrade-0.0.226-2026-11-24.md`，部署档 `deploy/newapi-bff/`。

## apiduo.cn 的 ClickHouse

- 配置在宿主机挂载目录 `/www/data/apiduo-cn/clickhouse-config/config.d/zz-apiduo-cn.xml`。
- 改 `max_server_memory_usage` 用 `SYSTEM RELOAD CONFIG` 热生效，**不要重启容器**；改前 `cp -a` 备份。
- 现为 5G（容器 6G）。原理与判据见 `MEMORY.md` 第十六节。

## apiduo.cn 的常驻渠道：zenmux gemini 图片

- 渠道 **49** `zenmux-gemini-banana-2.1`（type 58，`default` 组，公开名 `zenmux-banana-2.1` →
  `google/gemini-nano-banana-2.1`），上游 `https://zenmux.ai/api/vertex-ai`，按次 $0.05。
- `image_protocol.upstream=gemini` + `primary_endpoint=/v1/images/generations`，五条派生路由全空 upstream。
- **`artifact_storage` 2026-11-26 起已开**（`{"enabled": true}`）。0.0.230 修掉了「转存改写器跑在
  解码之前」的 chat 502；0.0.247 四入口复验：images 回 `images/<sha32>.png` + `size` 字段，
  chat/responses 回 84 字节 markdown 链接，gemini 入口仍回 inlineData 真 b64（`alignImageCarriers`
  在 gemini 回包路径传 `wantURL=false`）。产物落 `oss.s3ai.cn/cdn/apiduo/images/`。
  要关回去就 `JSON_REMOVE(settings, '$.artifact_storage')` 再插一条 `sync_channels` 任务。
- 计费走 zenmux 付费 key（存 `channels.key`）；**在 `default` 组意味着任何 default 令牌都能调用并消耗余额**，
  要限制就换到独立分组。
- 2026-11-25 建，同日清理后按用户要求重建；配置模板见 `.workbuddy/memory/2026-11-25.md`。

