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
| G5 | 改 priority 后保存按钮由 disabled 变可用 | 同上 | 脏检测 |
| G6 | `mcp.` 前缀的键在 7 个语言包中都存在 | gates/i18n-mcp-keys.sh | 约定：useMcpCopy 加前缀 |
| G7 | 白名单每个 key 都有 case | `go test ./mcp/ -run TestEveryPatchKeyHasEffect` | 防静默丢弃 |

## uncovered

- 「7 个语言文件」的**翻译质量**（法/俄/日/越）无法自动判定，只能人工看。
- 删除模型、改 kind 等破坏性操作不在本循环范围。
