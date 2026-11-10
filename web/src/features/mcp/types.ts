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

export type McpKind = 'image' | 'video' | 'search'
export type McpProtocol = 'openai_image' | 'openai_video'

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
export type ProtocolHint = {
  kind?: McpKind
  protocol?: McpProtocol
  confidence: 'high' | 'medium' | 'low' | 'none'
  conflict?: boolean
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

export type AuditIssue = {
  level: AuditLevel
  title: string
  object: string
  detail: string
}

export type AuditResult = {
  days: number
  counts: Record<string, number>
  issues: AuditIssue[]
}

export const EMPTY_DRAFT: ProfileDraft = {
  id: 0,
  model_id: '',
  display_name: '',
  kind: 'image',
  protocol: 'openai_image',
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
