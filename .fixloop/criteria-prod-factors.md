# 判据映射表 — 线上部署 + factors 序列化端到端验证

## 需求（逐字抄）

> 部署 线上端到端验证测试 bsk

## 判据

| ID | 断言 | 期望 | 手段 |
|---|---|---|---|
| O1 | 线上版本与健康 | `/api/status` 的 `success=true`，且 `version=0.0.189` | curl |
| O2 | 面板打开不丢 factors（**只读**） | 异步配置「完整 JSON」的 `billing` 含 `factors` | bsk |
| O3 | 面板保存后库里保留 factors | #37 的 factors 与基线逐字相等 | SQL |
| O4 | 同一次保存不丢其他顶层键 | `settings` 顶层键集合与保存前一致 | SQL |

O2 是核心：编辑器的 JSON 就是保存时会落库的载荷，只读即可判定，不必先破坏生产数据。

## 基线（2026-11-19 23:27，线上 0.0.188）

带 factors 的 8001 渠道：#18 `hailuo-video`、#37 `aivideomideo2api-video`、#38
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
