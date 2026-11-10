/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { useTranslation } from 'react-i18next'

/**
 * 文案。
 *
 * 全局语言包里还没有这批键：那 7 个文件正被另一条会话改写（追踪相关），
 * 一次性塞 60 个键进去会和它的改动缠在一起。这里先自带一份中文兜底，
 * `t(key, { defaultValue })` 在语言包补齐后会自动改用翻译，不需要再动组件。
 */
const FALLBACK: Record<string, string> = {
  Models: '模型',
  'Error rules': '错误规则',
  Observations: '观察记录',
  Inspection: '巡检',
  Refresh: '刷新',
  'New model': '新建模型',
  Save: '保存',
  Saved: '已存',
  Delete: '删除',
  Enable: '启用',
  Disable: '停用',
  'Model ID': '模型 ID',
  'Output kind': '产物类型',
  Protocol: '协议',
  Execution: '执行方式',
  'Display name (optional)': '显示名（可选）',
  'Ref image limit': '参考图上限',
  'Ref video limit': '参考视频上限',
  'Prompt limit': '提示词上限',
  'Parameter constraints': '参数约束',
  Summary: '一句话',
  'Capability description': '能力描述',
  'Tags (comma separated)': '标签（逗号分隔）',
  Image: '图片',
  Video: '视频',
  Search: '搜索',
  'Sync response': '同步直返',
  'Async task · task.get': '异步任务 · task.get',
  Undeclared: '未声明',
  Unsupported: '不支持',
  Limit: '上限',
  'Search models': '搜索模型',
  'Basic · three required, the rest empty means no enforcement':
    '基础 · 3 项必填，其余留空即不拦截',
  'Undeclared is not 0 — treating it as 0 locks image-to-image models':
    '未声明 ≠ 0，别把图生图模型锁死',
  'JSON, e.g. {"size":{"enum":["1K","2K"],"default":"2K"}}':
    'JSON：{"size":{"enum":["1K","2K"],"default":"2K"}}',
  'Shown in tools/list and models.list': '进 tools/list 与 models.list',
  'Select a model on the left, or click New model':
    '从左侧选一个模型，或点「新建模型」',
  'No models match': '没有匹配的模型',
  'Failed to load': '读取失败',
  'Model ID is required': '模型 ID 不能为空',
  'Parameter constraints is not valid JSON': '参数约束不是合法 JSON',
  'Delete this declaration?': '删除这条能力声明？',
  'The agent loses its parameter constraints for this model.':
    '删除后该模型的能力声明会消失，agent 将失去参数约束。',
  'Global defaults': '全局默认规则',
  'Unmatched errors are treated as non-retryable; retrying costs money':
    '未匹配到的错误一律按不可重试处理；重试的代价是计费',
  'Model overrides': '模型级覆盖',
  'This model has no overrides — the global defaults apply':
    '该模型没有覆盖规则，使用全局默认',
  'No rules yet': '暂无规则',
  Match: '匹配',
  Retryable: '可重试',
  Action: '动作',
  'Message to the agent': '给 agent 的话',
  Yes: '是',
  No: '否',
  'No samples yet — the distribution appears once parameter snapshots reach the log store.':
    '还没有样本。参数快照写入日志后这里会出现分布。',
  'Failed samples': '被拒',
  'No inconsistency found': '没有发现需要处理的不一致',
  'Running inspection…': '巡检中…',
  Conflict: '冲突',
  'To confirm': '待确认',
  'To verify': '待验证',
  Consistent: '一致',
  'Pick a model first': '先选一个模型',
  'Input limits': '输入限制',
  Loading: '加载中…',
  'enabled {{count}}': '已启用 {{count}}',
  'Log store': '日志库',
  Form: '表单',
  Default: '默认',
  'Set as default': '设为默认',
  'Remove value': '删除取值',
  'Add value': '添加取值',
  Enum: '枚举',
  Range: '范围',
  Min: '最小值',
  Max: '最大值',
  'Add parameter': '添加参数',
  'Remove parameter': '删除参数',
  'Lowercase letters, digits and _, starting with a letter':
    '小写字母开头，只能含小写字母、数字、下划线',
  'Not valid JSON — switch back to Form after fixing it':
    '不是合法 JSON —— 改好后可切回表单',
  'No constraints yet — add one, or switch to JSON.':
    '还没有约束，可以加一条，或切到 JSON。',
  'Enum or range, per parameter. Both views write the same JSON.':
    '按参数声明枚举或范围；表单与 JSON 写的是同一份内容。',
  'No declarations yet — click New model to start':
    '还没有能力声明，点右上角「新建模型」开始',

  '{{count}} models': '{{count}} 个模型',
  '{{count}} samples': '{{count}} 次样本',

  samples: '次样本',
}

export function useMcpCopy() {
  const { t } = useTranslation()
  // 统一加 `mcp.` 前缀：`Limit`、`Disable`、`Action` 这类通用词在语言包里
  // 已经有各自的条目，直接复用会显示成别的分区的措辞（实测出现半截英文）。
  // 前缀之后这些键永远走下面的兜底，接入翻译时按 `mcp.*` 补即可。
  return (key: string, options?: Record<string, unknown>) =>
    t(`mcp.${key}`, { defaultValue: FALLBACK[key] ?? key, ...options })
}
