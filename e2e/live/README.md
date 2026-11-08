# e2e/live — 跑在真二进制上的端到端回归

`e2e/*.go` 是进程内单测（`httptest`）。这里不同：**拉起真网关进程**，前面挂假上游、
旁边挂假 Langfuse，断言的是「上游实际收到什么字节」「sink 实际收到几个 span」
「浏览器里真的出现了这个链接」——网关自己的日志只能证明它以为自己在干什么。

```bash
e2e/live/run_all.sh          # 约 2-3 分钟，跑完自动回收进程与临时库
KEEP=1 e2e/live/run_all.sh   # 保留临时目录（日志、SQLite、sink 落盘）排查用
```

前置：`go`、`bun`、`python3`（图片套件需要 `httpx`）、本机 Chrome。
`web/dist` 缺失时脚本会先 `bun run build`（`//go:embed` 要求它存在）。

## 端口

| 用途 | 默认端口 | 覆盖变量 |
|---|---|---|
| 假上游（chat，含 500 模式） | 39401 | `TRACE_UP` |
| 假 Langfuse OTLP sink | 39402 | `SINK` |
| 假上游（图片，记录式） | 39403 | `IMG_UP` |
| 网关 · trace 开启 | 39410 | `GW_TRACE` |
| 网关 · 图片/主端点 | 39420 | `GW_IMAGE` |
| 网关 · trace 关闭 | 39440 | `GW_OFF` |

端口被占用时脚本直接退出并打印占位进程 —— 上一次没收干净时先处理掉，别硬跑。

## 套件

| 文件 | 断言的核心事实 |
|---|---|
| `suites/trace.py` | span 形状（流式帧重组、tool_calls、usage、失败态 ERROR+statusMessage+保留请求体）、`trace_id` 与 span 一一对应、traceparent 被采纳成同一条 trace、redaction 生效 |
| `suites/off.py` | 默认关闭时**行为等价**：不导出、日志不写 `trace_id`、`traceparent` 不转发给上游 |
| `suites/privacy.py` | 普通用户 `/api/log/self` 拿不到 `admin_info`、打 config 接口 403 |
| `suites/image_primary.py` | 主端点落库去空白、空 `upstream_path` 跟随、显式覆盖保留、三入口落点、载体闸门 400 且**零出站**、chat 不抢图片主端点 |
| `suites/discount_pat.py` | 折扣首条命中且不相乘、按次定价对账、审计 before/after、PAT 声明式作用域（未声明路由 fail-closed） |
| `ui/trace_link.mjs` | 真实 Chrome：日志详情里出现「在 Langfuse 查看」，href 主机/项目/32 位 id/`rel=noopener` 全对 |

## 环境前提（不满足会一片红，先看这条）

- **本地盘水位**：脚本已把 `performance_setting.monitor_disk_threshold` 设成 99。本机盘常年 97%+，
  不放开就是全站 503 `system_disk_overloaded`。
- **模型定价**：全新库没有定价。图片实例开 `SelfUseModeEnabled=true`；折扣套件自己关掉它并
  PATCH 一条 `ModelPrice`，所以它排在图片套件**之后**跑。
- **日志可见性**：写日志到 `/api/log/` 可查有 100–300ms 延迟，断言一律按 `id` / `request_id` 轮询收敛。
- **中继令牌**：用扁平 JSON 建（`{"name":...}`），套 `{"token":{...}}` 会建成 `status=4`（禁用），
  表现为 401 Invalid token。
- **渠道生效**：`SYNC_FREQUENCY=1`，脚本已设；判就绪不能用 `/v1/models`（无价模型不列出）。

## 只跑单个套件

实例在跑就能单跑：

```bash
E2E_BASE=http://127.0.0.1:39410 python3 e2e/live/suites/trace.py
E2E_BASE=http://127.0.0.1:39420 E2E_RELAY_KEY=sk-... python3 e2e/live/suites/image_primary.py
```

## 不覆盖

- **真实上游**：所有渠道都指向假上游。图片/视频生成走真上游就会计费，不在回归范围内。
- **生产环境**：需要管理员凭据；生产上的验证是另一套（见 `.workbuddy/memory/topics/observability.md`）。
- 并发/压测与 WebSocket realtime。
