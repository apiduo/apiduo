# 本地 Langfuse

> 状态：**已完成。** 六件套全部健康，apiduo 的 trace 能正确落到 Langfuse。
> 目的：与生产同形态，本地验证 trace 导出。
> 最后验证：2026-11-16 09:27，Langfuse v4.80.0

---

## 怎么用

```bash
cd deploy/langfuse/local
docker compose up -d
open http://127.0.0.1:3110        # admin@apiduo.local / ApdiduoE2e2026!
```

端口是 **3110**：3100 被 `taskview-taskview-mcp-1` 占用。

---

## 编排来源

用**官方 docker-compose.yml**，只改一处：`langfuse-web` 的端口 `3000 -> 3110`。
其余镜像、依赖、健康检查全部保持原样。

这一点是刻意的：之前我改过网络、改过 ClickHouse 用户、加过变量，
每次失败都多一个变量，最后分不清是哪个导致的。回到官方原版后，
问题才被逐个隔离出来。

---

## 起不来的三个叠加原因

排查花了很久，因为三个问题会互相掩盖症状。按发现顺序：

### 1. `CLICKHOUSE_USER=default` 让 entrypoint 自相矛盾

日志里唯一有价值的一行：

```
/entrypoint.sh: create new user 'default' instead 'default'
```

官方镜像的 entrypoint 只在 `CLICKHOUSE_USER` **不是** `default` 时才创建用户。
设成 `default` 会让它试图「用名为 default 的新用户替代 default」，
创建失败，随后所有客户端认证一律被拒（`code 516`）。

**必须用 `clickhouse` 或任何非 default 的名字。**

### 2. 空密码被 `:-` 语法回落成默认值

compose 写的是 `${CLICKHOUSE_PASSWORD:-clickhouse}`。
在 `.env` 里留空（`CLICKHOUSE_PASSWORD=`）不等于空密码，而是**回落到 `clickhouse`**。
我一度以为设了空值，实际两边都是 `clickhouse`，排查方向被带偏。

### 3. MinIO 凭证不匹配，trace 写不进去

web 不再重启后，trace 仍查不到，日志报：

```
Failed to upload JSON to S3
```

原因：`.env` 里 `MINIO_ROOT_PASSWORD` 是随机值，但 Langfuse 端读到的
`LANGFUSE_S3_*_SECRET_ACCESS_KEY` 回落成默认的 `miniosecret`，两把钥匙不一样。

现在 `.env` 里显式写 `MINIO_ROOT_PASSWORD=miniosecret` 与官方默认对齐（本地环境可接受）。

---

## apiduo 怎么接

### 网络

两个 compose 项目在 colima 里默认不通。`host.docker.internal`、桥接网关、
跨项目服务名、网络别名四种方式都试过，只有一种有效：

**把 apiduo 的应用容器接入 Langfuse 的默认网络**，同网络下按服务名访问。

`deploy/local/docker-compose.local.yml` 里：

```yaml
  app:
    networks:
      - default
      - langfuse

networks:
  langfuse:
    external: true
    name: local_default
```

注意方向：是 app 接入 `local_default`（Langfuse 的网络），不是反过来。
反方向试过，DNS 解析失败。

### 环境变量

写在 `deploy/local/.env.app`：

```
LANGFUSE_ENABLED=true
LANGFUSE_HOST=http://langfuse-web:3000
LANGFUSE_CONSOLE_URL=http://127.0.0.1:3110
LANGFUSE_PUBLIC_KEY=pk-lf-apiduo-local
LANGFUSE_SECRET_KEY=sk-lf-apiduo-local
```

`HOST` 用容器内服务名，`CONSOLE_URL` 用浏览器能访问的地址——两者必须分开，
否则要么容器发不出 trace，要么界面里的跳转链接点不开。

### 协议

apiduo 用的是 **OTLP**（`POST /api/public/otel/v1/traces`），已经适配 v4。

Langfuse v4 运行在 `events_only` 模式，旧端点 `/api/public/traces` 返回 404：

```
This endpoint is not available on deployments running in Langfuse v4 events_only mode.
```

查数据要用 `/api/public/v2/observations`。**排查时先用错端点，会误以为 trace 没发出去。**

---

## 验证证据

发一次真实转发请求：

```bash
curl -X POST http://127.0.0.1:39047/v1/chat/completions \
  -H "Authorization: Bearer <token>" \
  -d '{"model":"mock-gpt","messages":[{"role":"user","content":"trace check"}]}'
```

查到观测：

```json
{"type":"GENERATION","name":"chat mock-gpt","traceId":"06df230b...",
 "latency":0.048,"userId":"1","isRootObservation":true}
```

界面 `Tracing` 页能看到对应记录，含完整 input。

---

## 排错清单

按这个顺序查，能省掉我踩的坑：

1. `docker compose ps` —— 有没有在 restarting
2. `docker logs local-langfuse-web-1 | head -30` —— 看 migration 是否通过
3. ClickHouse 里 `CLICKHOUSE_USER` 不能是 `default`
4. `.env` 里 `CLICKHOUSE_PASSWORD` / `MINIO_ROOT_PASSWORD` 不能留空
5. 查 trace 用 `/api/public/v2/observations`，不是 `/api/public/traces`
6. 容器间不通时，先把 app 接进 `local_default`，再用服务名
