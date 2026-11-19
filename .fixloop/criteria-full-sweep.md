# 判据映射表 — 全面回归（前后端端到端）

## 需求（逐字抄）

> 继续测试 全面测 / Bsk 前后端 端到端 fix-loop

## 范围

线上 `0.0.189`（已部署 factors 修复 + C15）为被测对象；本地栈用于后端全量 E2E 回归。
**不发起任何真实生成请求**（视频/图片按条计费，需单独确认后再跑）。

## 判据

| ID | 断言 | 期望 | 手段 |
|---|---|---|---|
| B1 | 本地后端表达式计费全量 | `tiered_task_billing.py` 全部 PASS，退出 0 | 脚本 |
| B2 | 全仓 Go 测试 | `go test ./...` 0 FAIL | go test |
| F1 | 线上渠道列表 | 19 行，卡片含 8001 渠道 | bsk + 截图 |
| F2 | 线上模型管理 | 有数据行，表达式模型显示「特殊计费表达式」 | bsk + 截图 |
| F3 | 线上任务日志 | 页面可加载；空态与库内当日计数一致 | bsk + ClickHouse |
| F4 | 线上通用日志 | 同上 | bsk + ClickHouse |
| F5 | factors 往返 | 面板保存后 `billing.factors` 逐字保留 | 前轮 r02/r03，本轮复核 |
| W1 | 前端全量单测 | `vitest run` 0 failed | vitest |

## 结果（2026-11-20 00:25）

| ID | 结果 | 证据 |
|---|---|---|
| B1 | PASS | 19 条 PASS，`== PASS`，退出 0（setup 5 / guard 1 / billing 13） |
| B2 | PASS（1 次偶发） | 首跑 1 FAIL（`model.TestRecordChangeIsNonBlocking`，`no such table: change_events`）；单跑 0.05s PASS；`go test ./model` 连跑 2 次均 ok（14.6s / 11.6s） |
| F1 | PASS | 截图：卡片视图 19 行，含 #29/#37 等 8001 渠道 |
| F2 | PASS | 截图：61 行；HappyHorse 定价列显示「特殊计费表达式」 |
| F3 | PASS | 空态「未找到日志」；ClickHouse `toDate(created_at)=today()` 为 0，与页面一致 |
| F4 | PASS | 同 F3；近 6 小时 42 条，最新 2026-11-19 23:10，故当日 0 条为真 |
| F5 | PASS | 见 r02/r03 |
| W1 | **未全绿** | 293 文件：291 通过、2 失败 → 修掉 1 条后剩 1 条（见下） |

## W1 的两条失败

**① `i18n/__tests__/apiduo-locales.test.ts` — 已修（TEST 层）**

`en.json` 里 5 条 `mcp.*` 的英文源串被断言判成「翻译」。实际是断言写窄了：
`useMcpCopy()` 查 `mcp.<源串>`、兜底 `FALLBACK` 存的是中文，所以英文覆盖层必须
写去掉前缀的源串，否则英文控制台会显示中文。已改为
`value !== key.replace(/^mcp\./, '')`，并把一条值改成中文验证仍会变红（敏感性已测）。

**② `components/__tests__/command-menu.test.tsx` — 未修（预存在，未定位）**

管理员在命令面板搜不到 Channels / Models / Users / Redemption Codes。
- 在上一版本 `0f35a1442` 上**同样复现** ⇒ 与本次 factors 修复、0.0.189 部署无关。
- 已排除：i18n 语言（测试环境 `LANG=en`，`t('Channels')='Channels'`）、
  `useSidebarData` 的导航源（诊断输出里 admin 组四项齐全）。
- 未定位：丢失发生在 `useSidebarView` 之后（该 hook 需要路由上下文，临时诊断无法构造）。
- 结论：需单独排查，不在本次部署验证范围内，**未做猜测性修复**。

## 未覆盖（显式交底）

1. **线上真实计费**：factors 是否乘进结算未在线上发任务（本地 C4 覆盖 5s×720p×2→50000）。
2. **C15 线上复验**：需要改真实模型定价，风险高于收益；本地 API 层已覆盖。
3. **B2 的偶发失败**：3 次运行中 1 次失败，未复现；未做修复（与本次改动无关的既有测试隔离问题）。
