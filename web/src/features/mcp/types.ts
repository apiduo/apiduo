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

export type McpKind = 'image' | 'video' | 'files' | 'search' | 'audio'
/**
 * 协议按上游厂商命名。图片和视频都走方舟 —— 网关只对这两种产物开门，
 * 厂商差异由渠道和 JS 插件吸收；deepseek / openai 是留给检索与文件的，
 * 那两个端点还没接。
 */
export type McpProtocol = 'ark' | 'deepseek' | 'openai'

/** 每种产物类型能选哪些协议。 */
export const PROTOCOLS_BY_KIND: Record<McpKind, McpProtocol[]> = {
  // 协议不再由人选择：端点由产物类型决定，上游方言归渠道。这里保留映射
  // 只是为了让保存 payload 有一个稳定值（后端同样只接受 ark）。
  image: ['ark'],
  video: ['ark'],
  files: ['ark'],
  search: ['ark'],
  audio: ['ark'],
}

/**
 * 服务端（mcp/suggest.go）对候选模型的协议建议。
 *
 * confidence 决定前端怎么用：
 *   high / medium → 选中时自动带出 kind + protocol
 *   low           → 同样带出，但标签加问号，提示需要人工确认
 *   none          → kind/protocol 为空，绝不自动填，交给管理员选
 *
 * conflict 表示名称与其它判据（渠道类型、模型端点）矛盾。此时 kind 仍按
 * 模型名给出 —— 名称是唯一直接描述模型的信号，渠道只描述「能承载什么」 ——
 * 但标签必须加问号，不能当成干净结论。
 */
/**
 * 下拉分组的展示分类。比 McpKind 宽：MCP 只服务 image/video 两种协议，
 * 但候选里混着文本、检索、向量模型 —— 它们不适用，却需要被看见，
 * 否则整片都是「未识别?」，真正能声明的模型反而被淹没。
 */
export type HintCategory =
  | 'image'
  | 'video'
  | 'search'
  | 'audio'
  | 'embedding'
  | 'text'
  | 'unknown'

export type ProtocolHint = {
  kind?: McpKind
  protocol?: McpProtocol
  confidence: 'high' | 'medium' | 'low' | 'none'
  conflict?: boolean
  /** 旧版后端没有这个字段，按 unknown 降级。 */
  category?: HintCategory
  reason: string
}
export type RuleScope = 'global' | 'model'

export type ModelProfile = {
  id: number
  model_id: string
  display_name: string
  kind: McpKind
  protocol: McpProtocol
  async: boolean
  enabled: boolean
  // 三态：null = 未声明（不拦截），0 = 明确不支持，n = 上限。
  // 把 null 当成 0 会锁死图生图模型，这是本页最容易犯的错。
  max_ref_images: number | null
  max_ref_videos: number | null
  max_prompt_chars: number | null
  parameters: string
  summary: string
  tags: string
  source: string
  created_at: number
  updated_at: number
  /** 乐观锁版本号，每次写库 +1；旧后端没有这个字段，草稿键退回 updated_at。 */
  version?: number
}

/** 表单里的草稿：新建时 id=0，其余字段与提交体一致。 */
export type ProfileDraft = {
  id: number
  model_id: string
  display_name: string
  kind: McpKind
  protocol: McpProtocol
  async: boolean
  enabled: boolean
  max_ref_images: number | null
  max_ref_videos: number | null
  max_prompt_chars: number | null
  parameters: string
  summary: string
  tags: string
  /** 乐观锁版本号，每次写库 +1；旧后端没有，草稿键退回 updated_at。 */
  version?: number
}

export type Overview = {
  profiles: number
  enabled: number
  kinds: Record<string, number>
  log_db: string
}

export type ErrorRule = {
  id: number
  scope: RuleScope
  model_id: string
  match: string
  retryable: boolean
  retry_action: string
  retry_after_ms: number
  hint: string
  enabled: boolean
}

export type ObservationRow = {
  param: string
  value: string
  ok: number
  fail: number
}

export type Observations = {
  model_id: string
  days: number
  total: number
  stats: ObservationRow[]
}

export type AuditLevel = 'conflict' | 'pending' | 'unprobed' | 'ok'

/**
 * 可以直接写回声明的建议。
 *
 * 数值参数给 max，字符串参数给 enum；两者都没有时后端不下发建议 ——
 * 那说明样本不足以定约束。
 */
export type AuditSuggestion = {
  param: string
  max?: number
  enum?: string[]
}

export type AuditIssue = {
  level: AuditLevel
  title: string
  object: string
  detail: string
  suggestion?: AuditSuggestion
}

export type AuditResult = {
  days: number
  counts: Record<string, number>
  issues: AuditIssue[]
}

/** 采纳建议的响应：写回后的声明 + 实际写入的内容（max 或 enum）。 */
export type AdoptResult = {
  profile: ModelProfile
  applied: AuditSuggestion
}

export const EMPTY_DRAFT: ProfileDraft = {
  id: 0,
  model_id: '',
  display_name: '',
  kind: 'image',
  protocol: 'ark',
  async: false,
  enabled: true,
  max_ref_images: null,
  max_ref_videos: null,
  max_prompt_chars: null,
  parameters: '',
  summary: '',
  tags: '',
}

export const RETRY_ACTIONS: { value: string; label: string }[] = [
  { value: 'change_param', label: '改参数' },
  { value: 'change_prompt', label: '改提示词' },
  { value: 'wait', label: '等待' },
  { value: 'none', label: '停止' },
]

/** 把库里的声明转成表单草稿。 */
export function toDraft(p: ModelProfile): ProfileDraft {
  return {
    id: p.id,
    model_id: p.model_id,
    display_name: p.display_name,
    kind: p.kind,
    protocol: p.protocol,
    async: p.async,
    enabled: p.enabled,
    max_ref_images: p.max_ref_images,
    max_ref_videos: p.max_ref_videos,
    max_prompt_chars: p.max_prompt_chars,
    parameters: p.parameters,
    summary: p.summary,
    tags: p.tags,
  }
}

/** 提交体：模型 ID 之外的字段原样交给后端，enabled 单独走启停接口。 */
export function toPayload(d: ProfileDraft) {
  return {
    model_id: d.model_id.trim(),
    display_name: d.display_name.trim(),
    kind: d.kind,
    protocol: d.protocol,
    async: d.async,
    enabled: d.enabled,
    max_ref_images: d.max_ref_images,
    max_ref_videos: d.max_ref_videos,
    max_prompt_chars: d.max_prompt_chars,
    parameters: d.parameters.trim(),
    summary: d.summary.trim(),
    tags: d.tags.trim(),
  }
}
