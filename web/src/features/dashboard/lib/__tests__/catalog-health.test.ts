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
import { describe, expect, test } from 'vitest'

import type { Model } from '@/features/models/types'

import type { QuotaDataItem } from '../../types'
import { buildCatalogHealth } from '../catalog-health'

function model(overrides: Partial<Model> & { model_name: string }): Model {
  return {
    id: 1,
    status: 1,
    sync_official: 1,
    name_rule: 0,
    created_time: 0,
    updated_time: 0,
    description: 'desc',
    tags: 'tag',
    vendor_id: 1,
    ...overrides,
  }
}

function usage(modelName: string, count: number): QuotaDataItem {
  return { model_name: modelName, count, created_at: 0 }
}

describe('buildCatalogHealth', () => {
  test('complete metadata yields full coverage and no gaps', () => {
    const stats = buildCatalogHealth(
      [model({ model_name: 'a' }), model({ model_name: 'b' })],
      [usage('a', 60), usage('b', 40)],
      []
    )

    expect(stats.completeShare).toBe(100)
    expect(stats.gapCount).toBe(0)
    expect(stats.gaps).toHaveLength(0)
    expect(stats.fields.every((field) => field.byCalls === 100)).toBe(true)
  })

  test('a gap on the busiest model lowers by-calls more than by-count', () => {
    const stats = buildCatalogHealth(
      [
        model({ model_name: 'big', description: '' }),
        model({ model_name: 'small' }),
        model({ model_name: 'tiny' }),
      ],
      [usage('big', 900), usage('small', 60), usage('tiny', 40)],
      []
    )

    const description = stats.fields.find((field) => field.key === 'description')
    expect(description?.byCount).toBeCloseTo((2 / 3) * 100)
    expect(description?.byCalls).toBeCloseTo(10)
    expect(description?.byCalls).toBeLessThan(description?.byCount ?? 0)

    // 只有 small + tiny 是完整的，占 100/1000
    expect(stats.completeShare).toBeCloseTo(10)
    expect(stats.gaps[0]).toMatchObject({ name: 'big', calls: 900 })
  })

  test('missing models count toward their own share, not the gap list', () => {
    const stats = buildCatalogHealth(
      [model({ model_name: 'a' })],
      [usage('a', 50), usage('ghost', 50)],
      ['ghost']
    )

    expect(stats.missingCount).toBe(1)
    expect(stats.missingShare).toBeCloseTo(50)
    // ghost 不在元数据表里，因此不会出现在缺口表
    expect(stats.gaps).toHaveLength(0)
  })

  test('pinned models are counted and gaps are capped', () => {
    const models = Array.from({ length: 25 }, (_, index) =>
      model({ model_name: `m${index}`, tags: '', sync_official: index === 0 ? 0 : 1 })
    )
    const stats = buildCatalogHealth(models, models.map((item) => usage(item.model_name, 1)), [])

    expect(stats.pinnedCount).toBe(1)
    expect(stats.gapCount).toBe(25)
    expect(stats.gaps).toHaveLength(20)
  })

  test('zero traffic does not divide by zero', () => {
    const stats = buildCatalogHealth([model({ model_name: 'a' })], [], [])
    expect(Number.isNaN(stats.completeShare)).toBe(false)
    expect(stats.completeShare).toBe(0)
  })
})
