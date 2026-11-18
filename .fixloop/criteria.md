# 判据映射表 — MCP 能力注册面板可编辑 priority / cost

## 需求（逐字抄，不改写）

1. 「模型优先级在mcp配置页面可以设置？」
2. 「B」→ 我给出的 B 方案：「A + 前端加输入框 + 7 个语言文件」
   / 「面板能直接设，你要的形态」

## 判据

| ID | 断言 | 命令 | 说明 |
|---|---|---|---|
| G1 | 面板出现「默认优先级」 | gates/mcp-profile-fields.sh | label 文案，中文环境 |
| G2 | 面板出现「价格说明（可选）」 | 同上 | label 文案 |
| G3 | priority 输入框显示库中值（textin-demoire=24） | 同上 | 读值一致性 |
| G4 | cost 输入框显示库中值（免费额度池（13 出口）） | 同上 | 读值一致性 |
| G5 | 新建临时模型 → priority=7 → 保存 → 读回 → 删除 | 手动 bsk 流程（`.fixloop/logs/r03.log`） | **已通过**：UI 读回 7、DB id=34 priority=7，删除后 probe_rows=0 |
| G6 | `mcp.` 前缀的键在 7 个语言包中都存在 | gates/i18n-mcp-keys.sh | 约定：useMcpCopy 加前缀 |
| G7 | 白名单每个 key 都有 case | `go test ./mcp/ -run TestEveryPatchKeyHasEffect` | 防静默丢弃 |

## uncovered

- 「7 个语言文件」的**翻译质量**（法/俄/日/越）无法自动判定，只能人工看 —— 键存在已由 G6 覆盖，语义是否自然没有判据。
- 保存按钮在**修改已有模型**时能否解禁（G5 只覆盖了新建路径）。
- 删除模型、改 kind 等破坏性操作不在本循环范围。
