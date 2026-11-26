# jieapi.com 升级 0.0.228 → 0.0.241 执行记录

- 执行时间：2026-11-26 09:51–09:54（CST）
- 目标版本：`ghcr.io/apiduo/apiduo:0.0.241`（digest `sha256:3ecc986a…4b618`）
- 结果：**成功**。core-1（master）0.0.241 healthy，双入口版本头一致，数据零差异。
- 代价：4.6 s 停机窗口内 **79 条请求 502**（原因见第四节，非常规）。
- 回滚锚点：镜像 `0.0.228` 仍在本地；配置与库快照在
  `/www/backup/jieapi-upgrade/pre-0241-20261126-095134/`。

## 一、变更范围（0.0.228 → 0.0.241，17 个提交）

全部集中在图片链路，**没有 model/ 改动、没有新表新列、没有新增必填环境变量**：

| 类别 | 内容 |
|---|---|
| 计费 | 上游不返 usage 时按官方下界估算 token；估算按实际交付张数缩放；识别 nano-banana 别名 |
| 回包 | `images` 回包补结算 usage；Gemini 回包 usage 不落空对象、模态明细对齐总数 |
| 协议 | Gemini 入口收 `fileData`、多垫图保序、`imageConfig` 折 size；图片协议渠道不被响应转存污染 |
| 错误码 | 内容级拒绝统一 `prompt_blocked`，`RECITATION` 归内容策略 |
| 开关 | `LOCAL_IMAGE_ASYNC_ENABLED` **默认改为开启**（可用 false/0/off 关闭） |
| 其他 | artifact_storage 打开时转存上游链接；deploy 文档改双槽位滚动 |

日志侧唯一变化：`other` JSON 里多了 `usage_estimated`（公共字段），**ClickHouse 无新列**。

## 二、执行步骤

```
09:51:34  mysqldump jieapi-master → 212 MB gz（21s），gzip 校验通过
          备份 compose / app.env / nginx conf / docker inspect
          基线：53 表 / users 4757 / tokens 5419 / channels 222 / tasks 8261 /
                options 120 / billing_statements 41 / billing_lines 5,351,332
09:51:5x  docker pull 0.0.241（digest 3ecc986a）
09:52:17  sed compose image → 0.0.241，docker-compose config -q 通过
09:52:17  docker stop -t 3 jieapi-apiduo（3.6 s，超时后 SIGKILL）
09:52:21  docker-compose up -d apiduo → 起在 0.0.241
09:52:21  首个 200，累计停机 4.6 s
```

## 三、验证

| 项 | 结果 |
|---|---|
| 版本 | `X-New-Api-Version: 0.0.241`（jieapi.com + api.jieapi.com） |
| 迁移 | `database migration started` ×2（业务库 + 日志库），无 error/panic；表数仍 53 |
| 冒烟 | status/pricing/console/pricing 页 200；`/v1/models`、`/v1/chat/completions`、`/v1/images/generations` 伪造 key 均 401 JSON（本地拒绝，未出网） |
| 账单路由 | `/api/billing/statements`、`POST /api/billing/issue` 无鉴权 401（路由存在） |
| 数据 | users/tokens/channels/tasks/options/billing_statements/billing_lines **与基线逐项相等** |
| ClickHouse | 恢复后 2 分钟写入 285 行，未中断 |
| 恢复后成功率 | 09:53 起 123 请求 / 122 成功（1 条为上游 429） |
| 容器 | restarts=0、healthy、内存 123 MB |

## 四、79 条 502 的原因（重点）

09:52:17–09:52:21 这 4.6 s 里恰好撞上一次 **图片批量请求突发**（大量
`/v1/images/generations` + `/v1/chat/completions` 并发），102 个请求里 79 个拿到 502。
按当时的瞬时速率（约 8 req/s）估算，`stop -t 3` 的窗口被放大成了 79 条失败。

**为什么没有像 11-24 那样切到备节点**：core-3 的备用容器已经停了 ——

```
jieapi-apiduo-standby | ghcr.io/apiduo/apiduo:0.0.226 | Exited (0) 22 hours ago
```

主机本身在线（ping 45 ms、22 端口开），但：

- 3003 端口关闭（容器没跑），core-1 连它也连不上；
- 跳板机 `~/.ssh/config` 里的 `Host core-3` 已不存在，`j ls` 的清单里也没有 core-3；
- nginx upstream 的 `backup 61.111.253.243:3003` 仍指向这个死地址。

结论：**当前 jieapi.com 没有可用的备用节点**，core-1 一旦故障就是全站中断。
这次升级只是把「没有备节点」这件事的代价暴露出来（79 条 502 而不是 4 条）。

另外，本机备用槽位 `/www/docker/apiduo-standby/data/log_spool` 里还留着
`logspool-20261125T123527…jsonl`（3.8 MB，11-25 12:35 未 flush 的日志）。
**直接把它拉起来会把这批旧日志重放到 ClickHouse**，要用它得先清空 spool。

## 五、两个客户可见的行为变化

1. **图片计费口径变了**：上游不返 usage 时改为按官方下界估算 token。以前这类请求的
   token 计费是缺省的或按旧口径，现在会稳定给出 usage —— 对按 token 计价的图片模型，
   金额会比以前**更完整（可能更高）**。建议本月出账前抽 3–5 条图片请求核对金额。
2. **`LOCAL_IMAGE_ASYNC_ENABLED` 默认开启**：异步图片端点现在可以选中同步渠道
   （提交只存请求、由 worker 稍后重放）。代价是一个渠道槽位被占整段生成时间、请求体
   存进 task 行。要恢复旧行为就在 `app.env` 加 `LOCAL_IMAGE_ASYNC_ENABLED=false` 并重启。

## 六、回滚

```bash
cd /www/docker/apiduo
sed -i 's#apiduo:0.0.241#apiduo:0.0.228#' docker-compose.yml
docker-compose up -d apiduo     # 约 5 s
```

0.0.228 与 0.0.241 都用同一套 53 表，回滚不涉及数据库。
库整体回滚才需要 `mysql.sql.gz`（212 MB，restore 约 1–2 分钟）。

## 七、建议（未执行）

| # | 建议 | 理由 |
|---|---|---|
| 1 | 恢复一个备用节点：要么把 core-3 的容器拉起来（并补回 ssh 别名），要么用本机 `--profile local-standby` 起 127.0.0.1:3003，再把 nginx 的 backup 指过去 | 现在零冗余，core-1 故障 = 全站中断；今天 79 条 502 本可避免 |
| 2 | 若确定不要备节点，就把 nginx 的 `backup 61.111.253.243:3003` 删掉 | 死地址会让故障切换每次都先等一次超时 |
| 3 | 以后升级用「先切流再重建」，别再裸重建 | 本次裸重建撞上突发流量，代价 79 条 |

第 1 条的取舍：同机备节点只能扛容器/Docker 故障（0.1 ms 接管），异机 core-3 能扛整机故障
（137 ms）。两者覆盖场景不同，资源允许时都留着最稳。
