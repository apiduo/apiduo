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
import type { Model } from '@/features/models/types'

import type { QuotaDataItem } from '../types'

/**
 * 目录健康的统计口径。
 *
 * 与时间范围无关的部分（覆盖率、缺口集合）只依赖元数据；
 * 与时间范围有关的部分（调用量、占比）来自传入的用量行。
 * 两者在这里显式分开，避免调用方把"覆盖率"也当成会随时间变化的数字。
 */

export type MetadataFieldKey = 'description' | 'vendor' | 'tags'

export type MetadataFieldStat = {
  key: MetadataFieldKey
  label: string
  /** 已填该字段的模型所消耗的调用量占比（0-100） */
  byCalls: number
  /** 已填该字段的模型数占比（0-100） */
  byCount: number
  missing: number
}

export type CatalogGapRow = {
  name: string
  calls: number
  share: number
  missing: string[]
}

export type CatalogHealthStats = {
  totalCalls: number
  totalModels: number
  /** 所有字段都齐的模型所消耗的调用量占比（0-100） */
  completeShare: number
  fields: MetadataFieldStat[]
  gaps: CatalogGapRow[]
  gapCount: number
  missingCount: number
  missingShare: number
  pinnedCount: number
}

const FIELDS: Array<{
  key: MetadataFieldKey
  label: string
  test: (model: Model) => boolean
}> = [
  {
    key: 'description',
    label: 'Description',
    test: (model) => Boolean(model.description?.trim()),
  },
  {
    key: 'vendor',
    label: 'Vendor',
    test: (model) => Boolean(model.vendor_id),
  },
  {
    key: 'tags',
    label: 'Tags',
    test: (model) => Boolean(model.tags?.trim()),
  },
]

const GAP_LIMIT = 20

export function callsByModel(rows: QuotaDataItem[]): Map<string, number> {
  const calls = new Map<string, number>()
  for (const row of rows) {
    const name = row.model_name
    if (!name) continue
    calls.set(name, (calls.get(name) ?? 0) + (row.count ?? 0))
  }
  return calls
}

export function buildCatalogHealth(
  models: Model[],
  rows: QuotaDataItem[],
  missingModels: string[],
  gapLimit: number = GAP_LIMIT
): CatalogHealthStats {
  const calls = callsByModel(rows)
  const totalCalls = Array.from(calls.values()).reduce((a, b) => a + b, 0)
  const totalModels = models.length
  const share = (value: number) =>
    totalCalls > 0 ? (value / totalCalls) * 100 : 0

  const fields: MetadataFieldStat[] = FIELDS.map((field) => {
    let filled = 0
    let filledCalls = 0
    for (const model of models) {
      if (!field.test(model)) continue
      filled += 1
      filledCalls += calls.get(model.model_name) ?? 0
    }
    return {
      key: field.key,
      label: field.label,
      byCalls: share(filledCalls),
      byCount: totalModels > 0 ? (filled / totalModels) * 100 : 0,
      missing: totalModels - filled,
    }
  })

  let completeCalls = 0
  const gaps: CatalogGapRow[] = []
  for (const model of models) {
    const missing = FIELDS.filter((field) => !field.test(model)).map(
      (field) => field.label
    )
    const modelCalls = calls.get(model.model_name) ?? 0
    if (missing.length === 0) {
      completeCalls += modelCalls
      continue
    }
    gaps.push({
      name: model.model_name,
      calls: modelCalls,
      share: share(modelCalls),
      missing,
    })
  }

  const missingCalls = missingModels.reduce(
    (sum, name) => sum + (calls.get(name) ?? 0),
    0
  )

  return {
    totalCalls,
    totalModels,
    completeShare: share(completeCalls),
    fields,
    gaps: gaps.sort((a, b) => b.calls - a.calls).slice(0, gapLimit),
    gapCount: gaps.length,
    missingCount: missingModels.length,
    missingShare: share(missingCalls),
    pinnedCount: models.filter((model) => model.sync_official === 0).length,
  }
}
