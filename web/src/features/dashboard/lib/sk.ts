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
import type { SkQuotaDataItem, SkQuotaTotals } from '@/features/dashboard/types'

export type SkDimension = 'sk' | 'model' | 'user'
export type SkSortKey = SkDimension | 'quota' | 'count' | 'token_used'
export type SkSortDirection = 'asc' | 'desc'

export interface SkAggregateRow {
  key: string
  label: string
  detail: string
  quota: number
  count: number
  tokens: number
}

export interface SkSummary {
  quota: number
  count: number
  tokens: number
  cacheRead: number
  cacheCreation: number
  skCount: number
  modelCount: number
  userCount: number
  comboCount: number
}

function numberValue(value: unknown): number {
  const parsed = Number(value)
  return Number.isFinite(parsed) ? parsed : 0
}

// skName returns an empty string when usage has no token attached; callers
// substitute a localized placeholder so the pure helpers stay language-free.
export function skName(item: SkQuotaDataItem): string {
  if (numberValue(item.token_id) <= 0) return ''
  return item.token_name || `#${item.token_id}`
}

export function skMask(item: SkQuotaDataItem): string {
  if (numberValue(item.token_id) <= 0) return ''
  return item.token_key
}

export function skDimensionKey(
  item: SkQuotaDataItem,
  dimension: SkDimension
): string {
  if (dimension === 'sk') {
    const tokenID = numberValue(item.token_id)
    return tokenID > 0 ? `token:${tokenID}` : 'token:none'
  }
  if (dimension === 'model') {
    return `model:${item.model_name || ''}`
  }
  const userID = numberValue(item.user_id)
  return userID > 0 ? `user:${userID}` : `user:${item.username || ''}`
}

export function aggregateSkRows(
  items: SkQuotaDataItem[],
  dimension: SkDimension
): SkAggregateRow[] {
  const buckets = new Map<string, SkAggregateRow>()
  for (const item of items) {
    const key = skDimensionKey(item, dimension)
    const current = buckets.get(key)
    const quota = numberValue(item.quota)
    const count = numberValue(item.count)
    const tokens = numberValue(item.token_used)
    if (current) {
      current.quota += quota
      current.count += count
      current.tokens += tokens
      continue
    }
    let label = ''
    let detail = ''
    if (dimension === 'sk') {
      label = skName(item)
      detail = skMask(item)
    } else if (dimension === 'model') {
      label = item.model_name || ''
    } else {
      label = item.username || ''
      detail = `#${numberValue(item.user_id)}`
    }
    buckets.set(key, { key, label, detail, quota, count, tokens })
  }
  return [...buckets.values()].sort(
    (a, b) => b.quota - a.quota || a.label.localeCompare(b.label)
  )
}

export function filterSkRows(
  items: SkQuotaDataItem[],
  query: string
): SkQuotaDataItem[] {
  const needle = query.trim().toLowerCase()
  if (!needle) return items
  return items.filter((item) => {
    const haystack = [
      skName(item),
      item.token_key,
      item.username,
      item.model_name,
      item.token_id > 0 ? String(item.token_id) : '',
    ]
      .join('\n')
      .toLowerCase()
    return haystack.includes(needle)
  })
}

export function sortSkRows(
  items: SkQuotaDataItem[],
  key: SkSortKey,
  direction: SkSortDirection
): SkQuotaDataItem[] {
  const sign = direction === 'asc' ? 1 : -1
  const sorted = [...items]
  sorted.sort((a, b) => {
    if (key === 'sk') {
      // Rows without a token always sink to the bottom, regardless of direction.
      const left = skName(a)
      const right = skName(b)
      if (!left && !right) return 0
      if (!left) return 1
      if (!right) return -1
      return sign * left.localeCompare(right)
    }
    if (key === 'user') {
      return sign * (a.username || '').localeCompare(b.username || '')
    }
    if (key === 'model') {
      return sign * (a.model_name || '').localeCompare(b.model_name || '')
    }
    const left = numberValue(a[key])
    const right = numberValue(b[key])
    return sign * (left - right)
  })
  return sorted
}

export function summarizeSkData(
  items: SkQuotaDataItem[],
  totals?: SkQuotaTotals
): SkSummary {
  const skIDs = new Set<string>()
  const models = new Set<string>()
  const users = new Set<string>()
  let quota = 0
  let count = 0
  let tokens = 0
  let cacheRead = 0
  let cacheCreation = 0

  for (const item of items) {
    quota += numberValue(item.quota)
    count += numberValue(item.count)
    tokens += numberValue(item.token_used)
    cacheRead += numberValue(item.cache_read_tokens)
    cacheCreation += numberValue(item.cache_creation_tokens)
    const tokenID = numberValue(item.token_id)
    if (tokenID > 0) skIDs.add(`token:${tokenID}`)
    if (item.model_name) models.add(item.model_name)
    const userID = numberValue(item.user_id)
    users.add(userID > 0 ? `user:${userID}` : `user:${item.username || ''}`)
  }

  return {
    // Totals come from an ungrouped sum on the server, so they stay correct
    // even when the row list is truncated by the limit.
    quota: totals ? numberValue(totals.quota) : quota,
    count: totals ? numberValue(totals.count) : count,
    tokens: totals ? numberValue(totals.token_used) : tokens,
    cacheRead: totals ? numberValue(totals.cache_read_tokens) : cacheRead,
    cacheCreation: totals
      ? numberValue(totals.cache_creation_tokens)
      : cacheCreation,
    skCount: skIDs.size,
    modelCount: models.size,
    userCount: users.size,
    comboCount: items.length,
  }
}

export function quotaShare(quota: number, total: number): number {
  if (!Number.isFinite(quota) || !Number.isFinite(total) || total <= 0) return 0
  return (quota / total) * 100
}

export interface SkStackSegment {
  model: string
  quota: number
  share: number
}

export interface SkStackRow {
  key: string
  label: string
  mask: string
  total: number
  share: number
  segments: SkStackSegment[]
}

export interface SkStackData {
  rows: SkStackRow[]
  // Legend order: the largest models first, then the overflow bucket.
  models: string[]
  hiddenSkCount: number
  hiddenQuota: number
}

// buildSkStackRows turns the flat buckets into one bar per SK, split by model.
// It answers "who spends" and "on what" in a single glance, which two separate
// ranking lists could not do without doubling the page height.
export function buildSkStackRows(
  items: SkQuotaDataItem[],
  options: { maxRows?: number; maxSegments?: number; otherLabel?: string } = {}
): SkStackData {
  const maxRows = Math.max(1, options.maxRows ?? 5)
  const maxSegments = Math.max(2, options.maxSegments ?? 4)
  const otherLabel = options.otherLabel ?? 'Other'

  const bySK = new Map<
    string,
    { label: string; mask: string; total: number; models: Map<string, number> }
  >()
  const modelTotals = new Map<string, number>()

  for (const item of items) {
    const key = skDimensionKey(item, 'sk')
    const quota = numberValue(item.quota)
    const model = item.model_name || ''
    let bucket = bySK.get(key)
    if (!bucket) {
      bucket = { label: skName(item), mask: skMask(item), total: 0, models: new Map() }
      bySK.set(key, bucket)
    }
    bucket.total += quota
    bucket.models.set(model, (bucket.models.get(model) ?? 0) + quota)
    modelTotals.set(model, (modelTotals.get(model) ?? 0) + quota)
  }

  const rankedModels = [...modelTotals.entries()]
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
    .map(([model]) => model)
  const topModels = rankedModels.slice(0, maxSegments - 1)
  const topModelSet = new Set(topModels)

  const sortedSK = [...bySK.entries()].sort(
    (a, b) => b[1].total - a[1].total || a[1].label.localeCompare(b[1].label)
  )
  const totalAll = sortedSK.reduce((sum, [, bucket]) => sum + bucket.total, 0)
  const hidden = sortedSK.slice(maxRows)

  const rows = sortedSK.slice(0, maxRows).map(([key, bucket]) => {
    const segments: SkStackSegment[] = []
    let otherQuota = 0
    for (const [model, quota] of bucket.models) {
      if (topModelSet.has(model)) {
        segments.push({ model, quota, share: quotaShare(quota, bucket.total) })
      } else {
        otherQuota += quota
      }
    }
    if (otherQuota > 0) {
      segments.push({
        model: otherLabel,
        quota: otherQuota,
        share: quotaShare(otherQuota, bucket.total),
      })
    }
    segments.sort((a, b) => b.quota - a.quota)
    return {
      key,
      label: bucket.label,
      mask: bucket.mask,
      total: bucket.total,
      share: quotaShare(bucket.total, totalAll),
      segments,
    }
  })

  return {
    rows,
    models: [...topModels, otherLabel],
    hiddenSkCount: hidden.length,
    hiddenQuota: hidden.reduce((sum, [, bucket]) => sum + bucket.total, 0),
  }
}

function csvCell(value: string | number): string {
  const text = String(value)
  // Neutralize spreadsheet formula injection before quoting.
  const safe = /^[=+\-@\t\r]/.test(text) ? `'${text}` : text
  return `"${safe.replaceAll('"', '""')}"`
}

export function skRowsToCsv(
  items: SkQuotaDataItem[],
  quotaPerUnit: number
): string {
  const header = [
    'SK name',
    'SK key',
    'User',
    'Model',
    'Requests',
    'Tokens',
    'Quota',
    'Cost',
  ]
  const lines = [header.map(csvCell).join(',')]
  const divisor = quotaPerUnit > 0 ? quotaPerUnit : 1
  for (const item of items) {
    lines.push(
      [
        skName(item) || 'No token',
        skMask(item),
        item.username || '',
        item.model_name || '',
        numberValue(item.count),
        numberValue(item.token_used),
        numberValue(item.quota),
        (numberValue(item.quota) / divisor).toFixed(6),
      ]
        .map(csvCell)
        .join(',')
    )
  }
  return lines.join('\n')
}
