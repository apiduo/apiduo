# 判据映射表 — 线上部署 + factors 序列化端到端验证

## 需求（逐字抄）

> 部署 线上端到端验证测试 bsk

## 判据

| ID | 断言 | 期望 | 手段 |
|---|---|---|---|
| O1 | 线上版本与健康 | `/api/status` 的 `success=true`，且 `version=0.0.189` | curl |
| O2 | 面板打开不丢 factors（**只读**） | 异步配置「完整 JSON」的 `billing` 含 `factors` | bsk |
| O3 | 面板保存后库里保留 factors | #37 的 factors 与基线逐字相等 | SQL |
| O4 | 同一次保存不丢其他顶层键 | 保存前键集合 ⊆ 保存后键集合 | SQL |

O2 是核心：编辑器的 JSON 就是保存时会落库的载荷，只读即可判定，不必先破坏生产数据。

## 基线（2026-11-19 23:27，线上 0.0.188）

带 factors 的 8001 渠道：#18 `hailuo-video`、#37 `aivideomaker2api-video`、#38
`hailuo-video-fallback-2.0`；#19/#28/#29 的 factors 为 NONE。

#37 = `[{"key":"resolution","path":["resolution"],"values":{"480":1,"720":2,
"480p":1,"720p":2},"default":2}]`

验证只写 #37，不触碰 #18 / #38。

## 红点（0.0.188，r01）

bsk 打开 #37 → 配置异步路由 → 完整 JSON：`billing` 只有
`mode=per_second / duration_path=["duration"] / default_seconds=5`，**无 factors**。
与库里的 factors 直接矛盾 ⇒ 保存即抹掉加价表。

## 绿点条件

O1–O4 全绿，且 `.fixloop/runs.ndjson` 最后一条 `exit_code=0`。

## 结果（2026-11-19 23:42，线上 0.0.189）

| ID | 结果 | 证据 |
|---|---|---|
| O1 | PASS | `/api/status` → `success=true`，`version=0.0.189`；容器 `Up (healthy)` |
| O2 | PASS | 编辑器「完整 JSON」`billing.factors` = 基线，逐字相同 |
| O3 | PASS | 保存后 `factor_len=1`，JSON 与基线逐字相同，`mode=per_second` |
| O4 | PASS（判据已改） | 键集合 `["advanced_async"] → ["advanced_async","disable_task_polling_sleep"]`，只增未减 |

O4 的判据由「键集合相同」改为「不丢键（子集）」：保存时
`buildSettingsJSON` 无条件写入 `disable_task_polling_sleep: false`，这是表单既有
行为，值为零值、与字段缺失等价（`dto/channel_settings.go` 是 `bool` + `omitempty`）。
按原判据会把它记成回退，属于断言过严（TEST 层），不是实现问题。

未受影响：#18 / #38 的 `factor_len` 仍为 1；#19/#28/#29 仍为 NULL。
保存前完整配置备份在线上 `/root/apiduo-backup/ch37-settings.json`（613B）。
