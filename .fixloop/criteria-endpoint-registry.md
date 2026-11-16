# 判据映射（任务：端点注册表 · 本地端到端）

> 需求原话（逐字）：「本地 端到端测试了吗 结合bsk截图」「Fix loop」
> 前置需求：「V1/files」「V1/tools/search」这些当做新渠道如何引入虚拟model；
> 「为的就是重构…未来很多类似的这种接口 无需兼容旧版本」

> 留痕：`.fixloop/runs-endpoint-registry.ndjson`（与 `.fixloop/runs.ndjson` 是不同任务，不混写）

| # | 需求条目（逐字） | 判据 | 位置 |
|---|---|---|---|
| R1 | 「V1/tools/search … 引入虚拟model」 | 真栈 `POST /v1/tools/search` 返回上游 results，且 ClickHouse `logs` 有 `deepseek-search` 计费行 | 脚本 §3 |
| R2 | 「V1/files … 引入虚拟model」 | 以渠道模型名单里的 `@files/upload/file-extract` 选路：上传 200、content 原样回放、删除 200、删后 404 | 脚本 §3 |
| R3 | 「为的就是重构」 | 迁移把旧 `file_relay.purposes` 写进模型名单、删废字段、重建 abilities、其余设置不丢 | 脚本 §2 |
| R4 | 隐性：路由名不是模型 | `/v1/models` 不含 `@files/upload/` 与 `deepseek-search` | 脚本 §4 |
| R5 | 隐性：配置缺口可诊断 | 无渠道承担 purpose → 503 且 code=`no_file_channel` | 脚本 §3 |
| R6 | 「结合bsk截图」 | bsk 截控制台渠道抽屉：用途勾选写入模型名单 | **已覆盖**：`docs/apiduo/shots/file-relay-purposes.png`（文档解析已勾选 + 中文说明）、`docs/apiduo/shots/channel-model-list-routing-name.png`（模型名单里的 @files/upload/file-extract） |
| R7 | 「无需兼容旧版本」 | 旧 `purposes` 迁移后从 settings 移除，路由不读它 | R3 的字段断言 |
| R8 | 合并面 | `go run ./tools/markercheck -cost` 在预算内 | 本轮之前已验证 152/437/4188/242 |

## 判据命令

```bash
./.fixloop/e2e_endpoint_registry.sh --no-build   # 只跑断言（旧镜像，应见红）
./.fixloop/e2e_endpoint_registry.sh              # 重建镜像后跑（应转绿）
```

## 环境形态

`deploy/local/up.sh --mock`：MySQL 8.2 + ClickHouse 25.8 + 容器化应用 + 假上游。
不用 SQLite：迁移与 `abilities` 复合主键的行为在 SQLite 上不可信。

## 假上游扩展（本轮新增）

`deploy/local/mock_upstream.py` 补了 `/v1/tools/search|fetch` 与 `/v1/files` 四个动作
（上传 / 元数据 / content / 删除），否则 `files` 这条链路只能对着桩断言，测不到真实字节回放。

## 本轮由 E2E 抓出的真问题（都已修）

| 现象 | 根因 | 归属 |
|---|---|---|
| 迁移一条都没跑，日志 `Unknown column 'other_settings'` | 迁移 SQL 用了 Go 字段名，列名是 `settings` | IMPL |
| 内容回放 200 但 Content-Length: 0 | 假上游不解析 chunked 请求体，上传字节被读成 0 | ENV（测试基座） |
| 假上游改了代码不生效 | 单文件 bind mount 绑的是 inode（与 ClickHouse 同一类坑）→ 改挂目录 | ENV |
| 用量日志断言一直假红 | `clickhouse-client` 未指定库，`logs` 表解析不到 | TEST |
| 抽屉里说明文字是英文 | `t()` 的英文字符串改了，7 个语言文件没跟上 | IMPL（文案） |

## 第 6 轮（最终）

`./.fixloop/e2e_endpoint_registry.sh` → 16/16 通过，exit 0，见 `.fixloop/runs-endpoint-registry.ndjson`。
