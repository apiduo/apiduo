# newapi-bff.oneapis.cn（apps，apiduo canary）

2026-11-24 08:57 升级到 `0.0.226`（此前 `0.0.187`）。复盘见
`docs/apiduo/newapi-bff-upgrade-0.0.226-2026-11-24.md`。

## 现状

| 项 | 值 |
|---|---|
| 主机 | `apps` = 154.40.45.22（96C/126G/902G，宝塔 nginx 1.30.4） |
| 入场 | 本机只能经 jump 跳：`ssh jump` 后 `ssh apps`，脚本用 `ssh jump 'ssh apps bash -s' < script.sh` |
| 域名 | `newapi-bff.oneapis.cn`（BFF/控制台）、`workbuddy.oneapis.cn`（客户 API，`location /v1`） |
| 容器 | `apiduo-canary`，`127.0.0.1:3002:3000`，`restart=always`，**非 compose** |
| 目录 | `/www/docker/apiduo-canary/`（`apiduo.env`、`deploy.sh`、`data/`、`logs/`） |
| 数据 | MySQL `newapi-for-bff`（宿主 `host.docker.internal:3306`）、CH 容器 `newapi-for-bff-clickhouse`、Redis db0、Langfuse `https://langfuse.apiduo.cn` |
| 旁路 | `/api/user/token` → 3001（`newapi-legacy-bridge`，rc.40）；`/api/verify` 为「源 IP + UA」映射，默认走 3002 |

## 升级步骤（无感滚动，2026-11-25 起）

`deploy.sh` 默认就是双槽位滚动：新版本先起在 3006 接流，旧容器优雅退出、主槽位重建完再切回 3002。

```bash
# 1) 备份（dump 约 126 s，1.4 GB gz；tasks 单表 2 GB）
TS=$(date +%Y%m%d-%H%M%S); B=/www/backup/apiduo-upgrade-$TS; mkdir -p $B
mysqldump -uroot -p<pass> --single-transaction --quick --default-character-set=utf8mb4 \
  --skip-lock-tables newapi-for-bff | gzip -1 > $B/mysql.sql.gz
cp -a /www/docker/apiduo-canary/{apiduo.env,deploy.sh} $B/
cp -a /www/server/panel/vhost/nginx/{newapi-bff,workbuddy}.oneapis.cn.conf $B/

# 2) 镜像：apps 直连 GHCR 正常，digest 必须与官方一致
docker pull ghcr.io/apiduo/apiduo:<tag>
docker images --digests | grep apiduo        # 与 core-1 上同 tag 的 digest 比对

# 3) 滚动更新（脚本内部：起 3006 → 切 nginx → 排空 75s → 优雅停旧 → 重建 3002 → 切回 → 停 3006）
cd /www/docker/apiduo-canary && ./deploy.sh <tag>
curl -s http://127.0.0.1:3002/api/status | grep -o '"version":"[^"]*"'

# 4) 断言：看切换窗口的 access log 是否零 5xx，不要只看探针
grep -h "$(date +%d/%b/%Y)" /www/wwwlogs/newapi-bff.oneapis.cn.log | awk '$9 >= 500' | tail
```

应急/回滚：`./deploy.sh <旧 tag> --cold`（停旧再起，有秒级中断，旧镜像仍在本地）。
库是增量迁移，老版本可直接跑在迁移后的库上（已在 0.0.187 → 0.0.226 上实测）。

2026-11-25 实测 0.0.226 → 0.0.228：探针 653 轮双域名检查零失败；旧容器优雅停止约 100 s
（`-t 150`，容器内置 `SHUTDOWN_TIMEOUT_SECONDS=120`）。

## 五个坑

1. **迁移只在 master 上跑**（`model/main.go`：`if !common.IsMasterNode { … return nil }`）。
   预演容器设 `NODE_TYPE=slave` 会"正常启动但不迁移"，看起来通过、其实什么都没验。
   预演用 master + `UPDATE_TASK=false`（关异步任务轮询，避免真去轮询上游）。
2. **3003 被 `newapi-for-tasks` 占用**（2026-10-20 起）。滚动槽位固定用 3006，
   起之前先 `ss -lntp` 判空；就绪断言要比对 `/api/status` 的版本号，不能只看 200
   —— 别的 new-api 实例也返回 200 和 `v1.0.0-rc.40` 版本头。
3. **两个域名都要切**：`workbuddy.oneapis.cn` 的 `location ^~ /v1` 和 BFF 的
   `newapi_apiduo_pool`(`upstream`) 都指 3002，只切一个等于只搬了一半流量。
   `/api/verify` 通过 `map` 默认指向 `newapi_apiduo_pool`，会跟着一起走。
4. **nginx -s reload 是异步的**：切完等 2 s 再断言；deploy.sh 固定排空 75 s 再停旧容器，
   让 SSE / 图片生成这类长请求自然结束。
5. **容器日志不在 `docker logs` 里**：`--log-dir /app/logs` 把日志写文件，
   启动告警/迁移/慢 SQL 都要读 `/www/docker/apiduo-canary/logs/oneapi-*.log`。
