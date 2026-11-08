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
import { describe, expect, it } from 'vitest'

import { buildDiscountedPrices } from '../lib/discount-display'
import { getGroupDiscountInfo } from '../lib/model-helpers'
import { formatPrice, formatPriceAtRatio } from '../lib/price'
import type { PricingModel } from '../types'

function makeModel(overrides: Partial<PricingModel> = {}): PricingModel {
  return {
    id: 1,
    model_name: 'gpt-4o',
    quota_type: 0,
    model_ratio: 1,
    completion_ratio: 4,
    enable_groups: ['vip', 'default'],
    group_ratio: { vip: 0.9, default: 1 },
    ...overrides,
  } as PricingModel
}

describe('getGroupDiscountInfo', () => {
  it('算出划线基准价、折后倍率与相对降幅', () => {
    const model = makeModel({ user_discount_ratios: { vip: { 'gpt-4o': 0.7 } } })
    const info = getGroupDiscountInfo(model, 'vip')
    expect(info).not.toBeNull()
    expect(info?.baseRatio).toBe(0.9)
    expect(info?.effectiveRatio).toBe(0.7)
    // 相对「划线原价」降 22.2%，不是相对 1 的 30%
    expect(info?.offPercent).toBeCloseTo(1 - 0.7 / 0.9, 6)
  })

  it('未选分组、选全部、分组不可用时都不展示折扣', () => {
    const model = makeModel({ user_discount_ratios: { vip: { 'gpt-4o': 0.7 } } })
    expect(getGroupDiscountInfo(model)).toBeNull()
    expect(getGroupDiscountInfo(model, 'all')).toBeNull()
    expect(getGroupDiscountInfo(model, 'team-x')).toBeNull()
  })

  it('命中 * 兜底行时按兜底倍率算', () => {
    const model = makeModel({ user_discount_ratios: { vip: { '*': 0.8 } } })
    const info = getGroupDiscountInfo(model, 'vip')
    expect(info?.effectiveRatio).toBe(0.8)
    expect(info?.offPercent).toBeCloseTo(1 - 0.8 / 0.9, 6)
  })

  it('精确模型行优先于 * 兜底行', () => {
    const model = makeModel({
      user_discount_ratios: { vip: { '*': 0.8, 'gpt-4o': 0.6 } },
    })
    expect(getGroupDiscountInfo(model, 'vip')?.effectiveRatio).toBe(0.6)
  })

  it('折扣不低于组价时不标折扣（避免划线与现价相同）', () => {
    const same = makeModel({ user_discount_ratios: { vip: { 'gpt-4o': 0.9 } } })
    expect(getGroupDiscountInfo(same, 'vip')).toBeNull()

    const higher = makeModel({
      user_discount_ratios: { vip: { 'gpt-4o': 0.95 } },
    })
    expect(getGroupDiscountInfo(higher, 'vip')).toBeNull()
  })

  it('非法倍率（0、负数、非有限值、>=1）不生效', () => {
    for (const rate of [0, -0.5, Number.NaN, 1]) {
      const model = makeModel({
        user_discount_ratios: { vip: { 'gpt-4o': rate } },
      })
      expect(getGroupDiscountInfo(model, 'vip')).toBeNull()
    }
  })
})

describe('buildDiscountedPrices', () => {
  it('划线原价等于折扣前的价格（同格式、同单位）', () => {
    const withoutDiscount = makeModel()
    const withDiscount = makeModel({
      user_discount_ratios: { vip: { 'gpt-4o': 0.7 } },
    })

    // 与价格单元格一致：不显示货币符号（符号由外层列头给出）
    const expected = formatPrice(
      withoutDiscount,
      'input',
      'M',
      false,
      1,
      1,
      'vip',
      false
    )
    const prices = buildDiscountedPrices(
      withDiscount,
      ['input'],
      'M',
      false,
      1,
      1,
      'vip'
    )

    expect(prices?.[0].original).toBe(expected)
    expect(prices?.[0].offPercent).toBeCloseTo(1 - 0.7 / 0.9, 6)
  })

  it('折后价严格低于划线原价', () => {
    const model = makeModel({ user_discount_ratios: { vip: { 'gpt-4o': 0.7 } } })
    const discounted = formatPrice(model, 'input', 'M', false, 1, 1, 'vip')
    const original = formatPriceAtRatio(model, 'input', 'M', 0.9, false, 1, 1)

    const toNumber = (text: string) =>
      Number.parseFloat(text.replaceAll(/[^\d.]/g, ''))

    expect(toNumber(discounted)).toBeLessThan(toNumber(original))
    expect(toNumber(discounted) / toNumber(original)).toBeCloseTo(0.7 / 0.9, 2)
  })

  it('多个价格类型各自产出原价，顺序与传入一致', () => {
    const model = makeModel({ user_discount_ratios: { vip: { 'gpt-4o': 0.7 } } })
    const prices = buildDiscountedPrices(
      model,
      ['input', 'output'],
      'M',
      false,
      1,
      1,
      'vip'
    )
    expect(prices).toHaveLength(2)
    expect(prices?.[1].original).toBe(
      formatPriceAtRatio(model, 'output', 'M', 0.9, false, 1, 1)
    )
  })

  it('无折扣时返回 null，调用方据此不渲染任何折扣元素', () => {
    expect(
      buildDiscountedPrices(makeModel(), ['input'], 'M', false, 1, 1, 'vip')
    ).toBeNull()
  })

  it('按次计费模型的原价用 model_price 乘组倍率', () => {
    const model = makeModel({
      quota_type: 1,
      model_price: 0.02,
      user_discount_ratios: { vip: { 'gpt-4o': 0.5 } },
    })
    const prices = buildDiscountedPrices(
      model,
      ['input'],
      'M',
      false,
      1,
      1,
      'vip'
    )
    expect(prices?.[0].original).toBe(
      formatPriceAtRatio(model, 'input', 'M', 0.9, false, 1, 1)
    )
    expect(prices?.[0].offPercent).toBeCloseTo(1 - 0.5 / 0.9, 6)
  })
})
