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

import type { SkQuotaDataItem } from '@/features/dashboard/types'
import {
  aggregateSkRows,
  buildSkStackRows,
  filterSkRows,
  quotaShare,
  skMask,
  skName,
  skRowsToCsv,
  sortSkRows,
  summarizeSkData,
} from '@/features/dashboard/lib/sk'

function row(overrides: Partial<SkQuotaDataItem>): SkQuotaDataItem {
  return {
    token_id: 0,
    token_name: '',
    token_key: '',
    user_id: 0,
    username: '',
    model_name: '',
    count: 0,
    quota: 0,
    token_used: 0,
    cache_read_tokens: 0,
    cache_creation_tokens: 0,
    ...overrides,
  }
}

const ROWS: SkQuotaDataItem[] = [
  row({
    token_id: 11,
    token_name: 'primary',
    token_key: 'sk-p**********mary',
    user_id: 1,
    username: 'alice',
    model_name: 'gpt-a',
    count: 3,
    quota: 150,
    token_used: 50,
  }),
  row({
    token_id: 11,
    token_name: 'primary',
    token_key: 'sk-p**********mary',
    user_id: 1,
    username: 'alice',
    model_name: 'gpt-b',
    count: 1,
    quota: 70,
    token_used: 20,
  }),
  row({
    token_id: 22,
    token_name: 'backup',
    token_key: 'sk-b**********kup',
    user_id: 2,
    username: 'bob',
    model_name: 'gpt-a',
    count: 1,
    quota: 30,
    token_used: 5,
  }),
  row({ model_name: 'gpt-c', username: 'carol', count: 1, quota: 5 }),
]

describe('sk identity helpers', () => {
  test('names the token, falling back to its id, and leaves tokenless rows blank', () => {
    expect(skName(ROWS[0])).toBe('primary')
    expect(skName(row({ token_id: 33 }))).toBe('#33')
    expect(skName(ROWS[3])).toBe('')
  })

  test('exposes the server-side mask as-is and never reconstructs a key', () => {
    expect(skMask(ROWS[0])).toBe('sk-p**********mary')
    expect(skMask(ROWS[3])).toBe('')
  })
})

describe('aggregateSkRows', () => {
  test('merges the same token across models', () => {
    const bySK = aggregateSkRows(ROWS, 'sk')
    expect(bySK).toHaveLength(3)
    expect(bySK[0]).toMatchObject({
      key: 'token:11',
      label: 'primary',
      quota: 220,
      count: 4,
      tokens: 70,
    })
  })

  test('merges the same model across tokens', () => {
    const byModel = aggregateSkRows(ROWS, 'model')
    const gptA = byModel.find((bucket) => bucket.key === 'model:gpt-a')
    expect(gptA).toMatchObject({ quota: 180, count: 4 })
    // Sorted by quota desc, so the biggest model leads.
    expect(byModel[0].key).toBe('model:gpt-a')
  })

  test('keeps tokenless usage in a single explicit bucket', () => {
    const bySK = aggregateSkRows(ROWS, 'sk')
    const noToken = bySK.find((bucket) => bucket.key === 'token:none')
    expect(noToken).toMatchObject({ label: '', quota: 5 })
  })

  test('aggregates users by id so renames do not split the bucket', () => {
    const byUser = aggregateSkRows(
      [
        row({ user_id: 1, username: 'alice', quota: 10 }),
        row({ user_id: 1, username: 'alice-renamed', quota: 20 }),
      ],
      'user'
    )
    expect(byUser).toHaveLength(1)
    expect(byUser[0].quota).toBe(30)
  })
})

describe('sortSkRows', () => {
  test('sorts numeric columns in both directions', () => {
    const desc = sortSkRows(ROWS, 'quota', 'desc').map((item) => item.quota)
    expect(desc).toEqual([150, 70, 30, 5])
    const asc = sortSkRows(ROWS, 'quota', 'asc').map((item) => item.quota)
    expect(asc).toEqual([5, 30, 70, 150])
  })

  test('keeps tokenless rows last even when sorting SK ascending', () => {
    const sorted = sortSkRows(ROWS, 'sk', 'asc')
    expect(sorted.at(-1)?.token_id).toBe(0)
  })
})

describe('filterSkRows', () => {
  test('matches name, masked key, user and model', () => {
    expect(filterSkRows(ROWS, 'primary')).toHaveLength(2)
    expect(filterSkRows(ROWS, 'sk-b***')).toHaveLength(1)
    expect(filterSkRows(ROWS, 'bob')).toHaveLength(1)
    expect(filterSkRows(ROWS, 'gpt-c')).toHaveLength(1)
    expect(filterSkRows(ROWS, '   ')).toHaveLength(4)
  })
})

describe('summarizeSkData', () => {
  test('derives counts from rows when totals are absent', () => {
    const summary = summarizeSkData(ROWS)
    expect(summary).toMatchObject({
      quota: 255,
      count: 6,
      skCount: 2,
      modelCount: 3,
      userCount: 3,
      comboCount: 4,
    })
  })

  test('prefers server totals so truncation cannot understate the range', () => {
    const summary = summarizeSkData([ROWS[0]], {
      quota: 255,
      count: 6,
      token_used: 76,
      cache_read_tokens: 9,
      cache_creation_tokens: 4,
    })
    expect(summary.quota).toBe(255)
    expect(summary.count).toBe(6)
    expect(summary.cacheRead).toBe(9)
    expect(summary.comboCount).toBe(1)
  })
})

describe('quotaShare', () => {
  test('never divides by zero', () => {
    expect(quotaShare(10, 0)).toBe(0)
    expect(quotaShare(10, 40)).toBe(25)
  })
})

describe('buildSkStackRows', () => {
  test('splits one SK bar by model and keeps shares summing to 100', () => {
    const data = buildSkStackRows(
      [
        row({ token_id: 11, token_name: 'primary', model_name: 'gpt-4o', quota: 300 }),
        row({
          token_id: 11,
          token_name: 'primary',
          model_name: 'claude-3-5-sonnet',
          quota: 100,
        }),
      ],
      { maxRows: 5, maxSegments: 4 }
    )
    expect(data.rows).toHaveLength(1)
    expect(data.rows[0]).toMatchObject({ key: 'token:11', total: 400 })
    expect(data.rows[0].segments).toHaveLength(2)
    expect(data.rows[0].segments[0]).toMatchObject({
      model: 'gpt-4o',
      quota: 300,
      share: 75,
    })
    const sum = data.rows[0].segments.reduce((s, seg) => s + seg.share, 0)
    expect(Math.round(sum)).toBe(100)
  })

  test('folds models beyond the segment budget into the overflow bucket', () => {
    const items = ['a', 'b', 'c', 'd'].map((model, index) =>
      row({
        token_id: 11,
        token_name: 'primary',
        model_name: model,
        quota: 1000 - index * 100,
      })
    )
    const data = buildSkStackRows(items, { maxRows: 5, maxSegments: 3 })
    // Two named models plus the overflow bucket.
    expect(data.models).toEqual(['a', 'b', 'Other'])
    const segments = data.rows[0].segments
    expect(segments.map((seg) => seg.model)).toContain('Other')
    const other = segments.find((seg) => seg.model === 'Other')
    expect(other?.quota).toBe(1500)
  })

  test('collapses SKs beyond the row budget instead of dropping them', () => {
    const items = Array.from({ length: 8 }, (_, index) =>
      row({
        token_id: index + 1,
        token_name: `sk-${index}`,
        model_name: 'gpt-4o',
        quota: (8 - index) * 100,
      })
    )
    const data = buildSkStackRows(items, { maxRows: 5 })
    expect(data.rows).toHaveLength(5)
    expect(data.hiddenSkCount).toBe(3)
    // The three smallest SKs: 300 + 200 + 100.
    expect(data.hiddenQuota).toBe(600)
    // Row shares are relative to the whole range, not to the visible rows.
    expect(data.rows[0].share).toBeCloseTo((800 / 3600) * 100, 5)
  })

  test('returns an empty stack for an empty range', () => {
    const data = buildSkStackRows([])
    expect(data.rows).toHaveLength(0)
    expect(data.hiddenQuota).toBe(0)
  })
})

describe('skRowsToCsv', () => {
  test('neutralizes spreadsheet formulas and escapes quotes', () => {
    const csv = skRowsToCsv(
      [
        row({
          token_id: 1,
          token_name: '=cmd|calc',
          user_id: 1,
          username: 'a"b',
          model_name: 'gpt-a',
          quota: 500000,
        }),
      ],
      500000
    )
    const lines = csv.split('\n')
    expect(lines[0]).toContain('"SK name"')
    expect(lines[1]).toContain('"\'=cmd|calc"')
    expect(lines[1]).toContain('"a""b"')
    // Cost is a bare number so the spreadsheet can sum it.
    expect(lines[1]).toContain('"1.000000"')
  })
})
