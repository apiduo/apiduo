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
import type { TFunction } from 'i18next'

import type { AuditLog } from '../api'
import { auditFieldLabel } from './audit-details'

/**
 * `changed` 里的一项是不是「旧值 → 新值」的差值对。
 *
 * 只要求有 after，不要求 before：后端 auditDiff 写 `before: null` 时这个键
 * 会被序列化省掉，于是「未声明 → 0」在库里只有 `{after: 0}`。
 * 用 `'before' in pair` 判定会把这类 diff 当成普通字段，详情里就会同时出现
 * 一份对比表和一坨原始 JSON（线上 0.0.100 实测踩到）。
 *
 * 反过来要求「键只能是 before/after」，是为了不把 `{status: 2}` 这类普通对象
 * 误认成差值对 —— 它们没有 after，直接排除。
 */
export function isAuditDiffPair(value: unknown): value is {
  before?: unknown
  after: unknown
} {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false
  const pair = value as Record<string, unknown>
  if (!('after' in pair)) return false
  return Object.keys(pair).every((key) => key === 'before' || key === 'after')
}

/** `params.changed` 是「字段 → 差值对」的映射（而不是 channel.status_update 那种布尔）。 */
export function isAuditDiffMap(
  value: unknown
): value is Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false
  const entries = Object.values(value as Record<string, unknown>)
  return entries.length > 0 && entries.every(isAuditDiffPair)
}

/** 一个值在审计里的呈现形态。kind 决定渲染成徽章还是等宽文本。 */
export interface AuditChangeValue {
  kind: 'null' | 'boolean' | 'number' | 'string' | 'json'
  text: string
  /** 补在值后面的解释，目前只有三态字段的 0 会用到。 */
  note?: string
}

export interface AuditChange {
  /** 库里真实的字段名，排障时按它搜。 */
  key: string
  /** 人读的标签，来自本地映射或 auditFieldLabel。 */
  label: string
  /** null 表示没有旧值 —— create 事件只有初始状态，画不出 diff。 */
  before: AuditChangeValue | null
  after: AuditChangeValue
}

// mcp 的能力声明字段。上游的 auditFieldLabel 是给 token/渠道/用户那套写的，
// 这里没有对应 case，会原样返回键名。补一张本地表而不是去改上游的 switch：
// 那张 switch 已经几百行，再加十几个 case 既占合并面又容易被上游改乱。
const PROFILE_FIELD_LABELS: Record<string, string> = {
  model_id: 'Model ID',
  display_name: 'Display name',
  kind: 'Kind',
  protocol: 'Protocol',
  async: 'Async',
  enabled: 'Enabled',
  max_ref_images: 'Max reference images',
  max_ref_videos: 'Max reference videos',
  max_prompt_chars: 'Max prompt characters',
  parameters: 'Parameter constraints',
  summary: 'Summary',
  tags: 'Tags',
  source: 'Source',
  version: 'Version',
  via: 'Source channel',
  exists: 'Exists',
  expected_version: 'Expected version',
  current_version: 'Current version',
}

function fieldLabel(key: string, t: TFunction): string {
  const known = PROFILE_FIELD_LABELS[key]
  return known ? t(known) : auditFieldLabel(key, t)
}

// 三态字段：null = 未声明（不拦截），0 = 明确不支持，N = 上限。
//
// 这三个键在审计里必须能区分 0 和 null —— 把参考图上限「从 3 改成 0」
// 实际含义是「禁止垫图」，而 JSON 树里它看起来只是「变成了 0」。
const TRI_STATE_KEYS = new Set([
  'max_ref_images',
  'max_ref_videos',
  'max_prompt_chars',
])

function describeValue(
  key: string,
  raw: unknown,
  t: TFunction
): AuditChangeValue {
  if (raw === null || raw === undefined) {
    return { kind: 'null', text: t('Not declared') }
  }
  if (typeof raw === 'boolean') {
    return { kind: 'boolean', text: raw ? t('Yes') : t('No') }
  }
  if (typeof raw === 'number') {
    return {
      kind: 'number',
      text: String(raw),
      note:
        TRI_STATE_KEYS.has(key) && raw === 0
          ? t('Explicitly unsupported')
          : undefined,
    }
  }
  if (typeof raw === 'string') {
    // 空串在能力声明里就是「没声明」：display_name / parameters / tags 都这样。
    // 留一个空单元格会让人以为是渲染失败（线上创建事件实测如此）。
    if (raw.trim() === '') {
      return { kind: 'null', text: t('Not declared') }
    }
    return { kind: 'string', text: raw }
  }
  return { kind: 'json', text: JSON.stringify(raw) }
}

/**
 * 把 `other.op.params.changed` 解析成变更列表。
 *
 * 只认这一种形状（接口层 auditDiff 写死的 {字段: {before, after}}），
 * 结构对不上就返回空数组 —— 渲染一张错的表比不渲染更糟，那会让人
 * 以为「这次只改了一个字段」。
 */
export function buildAuditChanges(entry: AuditLog, t: TFunction): AuditChange[] {
  const other = entry.other
  if (!other || typeof other !== 'object') return []

  const op = (other as { op?: unknown }).op
  if (!op || typeof op !== 'object') return []

  const params = (op as { params?: unknown }).params
  if (!params || typeof params !== 'object') return []

  const changed = (params as { changed?: unknown }).changed
  if (!changed || typeof changed !== 'object' || Array.isArray(changed)) {
    // create 事件没有 changed，只有 after（全量快照）。列成「初始值」单列，
    // 比渲染一棵 JSON 树有用 —— 它回答的是「刚建出来时是什么样」。
    const after = (params as { after?: unknown }).after
    if (!after || typeof after !== 'object' || Array.isArray(after)) return []
    return Object.entries(after as Record<string, unknown>).map(([key, value]) => ({
      key,
      label: fieldLabel(key, t),
      before: null,
      after: describeValue(key, value, t),
    }))
  }

  const out: AuditChange[] = []
  for (const [key, pair] of Object.entries(changed as Record<string, unknown>)) {
    if (!isAuditDiffPair(pair)) continue
    const { before, after } = pair
    out.push({
      key,
      label: fieldLabel(key, t),
      before: describeValue(key, before, t),
      after: describeValue(key, after, t),
    })
  }
  return out
}

/** 乐观锁冲突的两个版本号。非冲突事件返回 null。 */
export interface AuditConflict {
  expected?: number
  current: number
}

/**
 * 从 params 里读冲突版本。
 *
 * 只认 success=false 且 current_version 是数字的记录 —— 「失败」有很多种，
 * 参数校验失败也失败，但它没有版本可比，不该弹一句「版本冲突」误导人。
 */
export function readAuditConflict(entry: AuditLog): AuditConflict | null {
  if (entry.success) return null
  const other = entry.other
  if (!other || typeof other !== 'object') return null
  const op = (other as { op?: unknown }).op
  if (!op || typeof op !== 'object') return null
  const params = (op as { params?: unknown }).params
  if (!params || typeof params !== 'object') return null

  const current = (params as { current_version?: unknown }).current_version
  if (typeof current !== 'number') return null
  const expected = (params as { expected_version?: unknown }).expected_version
  return {
    expected: typeof expected === 'number' ? expected : undefined,
    current,
  }
}
