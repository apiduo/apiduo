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
import { describe, expect, it, vi } from 'vitest'

import { resolveGroupRatio } from '../pricing'
import { computeChatQuota, computeImageQuota, formatUsd } from '../quota'
import type { PricingModel, PricingSnapshot } from '../types'

// vitest 会把 vi.mock 提升到所有 import 之前，所以这里放后面也能生效
vi.mock('@/lib/currency', () => ({
  getCurrencyDisplay: () => ({
    config: { quotaPerUnit: 500000 },
    meta: {
      kind: 'currency',
      symbol: '$',
      currencyCode: 'USD',
      exchangeRate: 1,
    },
  }),
  formatQuotaWithCurrency: (quota: number) => `quota:${quota}`,
}))

function model(overrides: Partial<PricingModel> = {}): PricingModel {
  return {
    modelName: 'gpt-4o',
    quotaType: 0,
    modelRatio: 1.25,
    modelPrice: 0,
    completionRatio: 4,
    cacheRatio: 0.5,
    enableGroups: ['default', 'vip'],
    endpointTypes: ['openai'],
    ...overrides,
  }
}

function snapshot(overrides: Partial<PricingSnapshot> = {}): PricingSnapshot {
  return {
    models: {},
    groupRatio: { default: 1, vip: 1.2 },
    userDiscount: {},
    fetchedAt: Date.now(),
    ...overrides,
  }
}

describe('computeChatQuota', () => {
  it('按 (prompt + completion × completionRatio) × modelRatio × groupRatio 计算', () => {
    const result = computeChatQuota(
      { promptTokens: 1200, completionTokens: 380, cacheTokens: 0 },
      model(),
      snapshot(),
      'default',
      'gpt-4o'
    )

    // (1200 + 380×4) × 1.25 × 1 = 3400
    expect(result?.quota).toBe(3400)
    expect(result?.effectiveGroupRatio).toBe(1)
    expect(result?.discountRate).toBeNull()
  })

  it('缓存命中先从输入里扣掉，再按 cacheRatio 单独计', () => {
    const result = computeChatQuota(
      { promptTokens: 1200, completionTokens: 380, cacheTokens: 800 },
      model(),
      snapshot(),
      'default',
      'gpt-4o'
    )

    // base 400 + 800×0.5 = 800; (800 + 1520) × 1.25 = 2900
    expect(result?.quota).toBe(2900)
    expect(result?.baseTokens).toBe(400)
    expect(result?.cacheTokens).toBe(800)
  })

  it('个人折扣覆盖组倍率，而不是相乘', () => {
    const result = computeChatQuota(
      { promptTokens: 1000, completionTokens: 0, cacheTokens: 0 },
      model({ cacheRatio: 1 }),
      snapshot({ userDiscount: { vip: { 'gpt-4o': 0.8 } } }),
      'vip',
      'gpt-4o'
    )

    // 1000 × 1.25 × 0.8 = 1000（若相乘会得到 1200）
    expect(result?.quota).toBe(1000)
    expect(result?.effectiveGroupRatio).toBe(0.8)
    expect(result?.discountRate).toBe(0.8)
    expect(result?.baseGroupRatio).toBe(1.2)
  })

  it('按次计费走 model_price × quotaPerUnit × 组倍率', () => {
    const result = computeChatQuota(
      { promptTokens: 999, completionTokens: 999, cacheTokens: 0 },
      model({ quotaType: 1, modelPrice: 0.04 }),
      snapshot(),
      'default',
      'gpt-4o'
    )

    // 0.04 × 500000 × 1 = 20000
    expect(result?.quota).toBe(20000)
    expect(result?.formula).toContain('按次计费')
  })

  it('倍率有效但额度算成 0 时记 1，与后端一致', () => {
    const result = computeChatQuota(
      { promptTokens: 0, completionTokens: 0, cacheTokens: 0 },
      model(),
      snapshot(),
      'default',
      'gpt-4o'
    )
    expect(result?.quota).toBe(0)
  })

  it('模型缺失时返回 null，避免展示假数据', () => {
    expect(
      computeChatQuota(
        { promptTokens: 10, completionTokens: 10, cacheTokens: 0 },
        null,
        snapshot(),
        'default',
        'unknown'
      )
    ).toBeNull()
  })
})

describe('computeImageQuota', () => {
  it('按张数线性放大', () => {
    const one = computeImageQuota(
      1,
      model({ quotaType: 1, modelPrice: 0.04 }),
      snapshot(),
      'default',
      'gpt-image-1'
    )
    const three = computeImageQuota(
      3,
      model({ quotaType: 1, modelPrice: 0.04 }),
      snapshot(),
      'default',
      'gpt-image-1'
    )

    expect(one?.quota).toBe(20000)
    expect(three?.quota).toBe(60000)
  })
})

describe('resolveGroupRatio', () => {
  it('折扣为 0 或负数时视为无规则，回退组倍率', () => {
    const result = resolveGroupRatio(
      snapshot({ userDiscount: { vip: { 'gpt-4o': 0 } } }),
      'vip',
      'gpt-4o'
    )
    expect(result.effective).toBe(1.2)
    expect(result.discount).toBeNull()
  })

  it('分组缺失时按 1 处理', () => {
    const result = resolveGroupRatio(snapshot(), 'ghost', 'gpt-4o')
    expect(result.effective).toBe(1)
  })
})

describe('formatUsd', () => {
  it('极小金额不四舍五入成 0', () => {
    expect(formatUsd(1)).toBe('$0.000002')
    expect(formatUsd(0)).toBe('$0')
  })
})
