# jieapi.com 滚动升级 0.0.241 → 0.0.250

- 执行时间：2026-11-26 14:55:15–14:58:31（CST），3 分 16 秒
- 方式：`deploy/jieapi/rolling.sh` **首次实战**（双槽位滚动，替代 0.0.228→0.0.241 的 stop-then-up）
- 结果：主备双槽位均 `0.0.250` healthy，双入口版本头一致，迁移无 error
- 代价：窗口内 1441 请求 / **17 条 5xx（1.18%）**，全部是 `/v1/chat/completions` 长请求，集中在三个切换点
- 回滚锚点：镜像 `0.0.241` 仍在本地；备份 `/www/backup/jieapi-upgrade/rolling-20261126-145515/`
  （compose / nginx conf / container.json / mysqldump 212 MB）

## 一、时间线

| 时刻 | 动作 | 结果 |
|---|---|---|
| 14:55:15 | mysqldump `--single-transaction` | 212 MB，gzip 校验通过（25 s） |
| 14:55:40 | `docker pull 0.0.250` | 3 s（层已缓存） |
| 14:55:43 | compose tag → 0.0.250，起备槽位 :3003 | — |
| 14:55:47 | 备槽位就绪 `0.0.250`（4 s） | 流量未动 |
| 14:55:47 | 主槽位置 down，reload | 落点抽样 5/5 仍 3002（旧 worker） |
| 14:57:05 | 排空 75 s 后 `docker stop -t 150` | — |
| 14:58:24 | 以 0.0.250 重建主槽位 | — |
| 14:58:27 | 主就绪，取消 down，reload 回切 | — |
| 14:58:31 | 完成，对外入口 200 | — |

## 二、变更范围（0.0.242–0.250，13 个提交）

全部在图片链路，**无 model/ 改动、无新表、无新增必填环境变量**：

| 类别 | 内容 |
|---|---|
| size 契约 | `size` 三种写法（`1024x1024` / `1:1` / `4K`）在 canonical 层统一折成 WxH 再下发；`16x9` 这类短边写法按比例解析（`minPixelSide=64`） |
| 回包 | `data[].size` 从交付字节头测得（PNG/JPEG，两级窗口 3KB→96KB）；chat chunk 不再带 images 专用的 size |
| 严格化 | `size` 系字段非字符串 400；空字符串删除不透传；超 4K 档两倍 400 |
| 文档 | `gemini-image-protocol.md` §3.1/§4/§5.1、`ops-hosts.md` 渠道 49 转存解禁 |

图片协议只影响挂了 `image_protocol` 的渠道；本机的 zenmux/duck 类渠道未配置该链路时行为不变。

## 三、验证

| 项 | 结果 |
|---|---|
| 版本 | `X-New-Api-Version: 0.0.250`（jieapi.com + api.jieapi.com）；本机 3002/3003 均 0.0.250 |
| 迁移 | `database migration started` ×2（业务库 + 日志库），无 error/panic；表数 53（与基线一致） |
| 数据 | users 4757 / tokens 5419 与 09:51 基线相等；channels 223（+1，期间新增）；tasks 8288（正常增长） |
| ClickHouse | 升级后 10 分钟写入 10517 行，未中断 |
| 冒烟 | `/v1/chat/completions`、`/v1/images/generations` 伪造 key 均 401 JSON（本地拒绝） |
| 容器 | 主 188 MB / CPU 2.1%，备 224 MB / 0.7%，均 healthy，restarts=0 |
| 后台任务 | `batch update started` 每 5 s 一次（仅 master） |

## 四、17 条 5xx 的归因

窗口 14:55:40–14:58:40，1441 请求。

| 状态码 | 条数 | 落点 | 归因 |
|---|---|---|---|
| 502 | **5** | 3002 | **1% 灰度 map 写死 3002**，主容器重建那一秒（14:58:24）打进来 |
| 500 | 3 | 3002 | 长请求在主槽位 `docker stop` 时被中断 |
| 504 | 2 | 3002/3003 | 同上，客户端侧超时 |
| 503 | 7 | 3002/3003 | 与该站常态同源（上游 429 转 503；14:45–14:55 基线有 1536 条 503） |

全部是 `/v1/chat/completions`（LLM 长请求），无短请求失败。

**为什么排空 75 s 仍不够**：该站的 chat 请求可跑 2–5 分钟，`docker stop -t 150` 会等
150 s，但 nginx 在切流那一刻已建立的长连接无法迁移，只能等它自然结束。要彻底消除
这几条，需要把排空时间拉到长请求 P99（≥5 分钟），代价是升级窗口变成 8–10 分钟。

## 五、脚本缺陷与修复（本次唯一代码改动）

`rolling.sh` 的 `primary_down()` 只给 upstream 的 server 行加了 `down`，**漏了 map**：

```nginx
map $apiduo_split $apiduo_backend {
    "0"     127.0.0.1:3003;
    "1"     127.0.0.1:3002;   # ← 1% 灰度，直接写 IP，绕开 upstream 的 down
}
```

这 1% 在主槽位重建瞬间必然 502，正是本次 5 条 502 的来源。已修：

- `primary_down` / `primary_up` 同时改写该行（3002 ↔ 3003），并用 `grep -Eq` 断言改写生效；
- 改写用「行匹配 + 行内替换」，不引入捕获组，避免反复升级把缩进越加越长。

修复后的脚本已上传 `/www/docker/apiduo/rolling.sh`（备份 `rolling.sh.bak-145515`），下次升级生效。

## 六、遗留

1. `aapi.cn.conf:90` 与 `cn.jieapi.com.conf:37` 仍硬编码 `127.0.0.1:3002`。本次窗口这两个域名零流量，
   未暴露；一旦启用，切流期间会 502。启用前必须把两处一并纳入切换。
2. 同机 standby（`:3003`）现在是常驻 backup，比 core-3 的 137 ms 跨域链路快得多，**建议保留**；
   它的 spool 必须保持 offset == size，否则下次启动会重放旧日志（脚本前置检查已拦）。
3. 长请求排空时间可调（`DRAIN_SECONDS`），默认 30 s，本次用 75 s。
