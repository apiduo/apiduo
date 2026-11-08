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

  it('分组不可用时不展示折扣', () => {
    const model = makeModel({ user_discount_ratios: { vip: { 'gpt-4o': 0.7 } } })
    expect(getGroupDiscountInfo(model, 'team-x')).toBeNull()
  })

  it('选「全部」时按折扣后最低价归因，不再返回 null', () => {
    // 默认视图就是「全部」。这里返回 null 会让标价停在跨组组价上，
    // 而用户实付更低——标价与账单对不上。
    const model = makeModel({ user_discount_ratios: { vip: { 'gpt-4o': 0.7 } } })
    for (const filter of [undefined, 'all']) {
      const info = getGroupDiscountInfo(model, filter)
      expect(info).not.toBeNull()
      expect(info?.baseRatio).toBe(0.9)
      expect(info?.effectiveRatio).toBe(0.7)
    }
  })

  it('最便宜的组本身没有折扣时不画划线价', () => {
    // default 组组价 1.0、vip 组折扣后 0.7：最便宜的是 vip 的折扣价，应展示。
    // 反过来若 vip 折扣后 0.95 而 default 组价 1.0，最便宜仍来自折扣，也展示。
    // 只有当最便宜的是「没折扣的组价」时，划线价才是凭空造出来的。
    const model = makeModel({
      enable_groups: ['default', 'vip'],
      group_ratio: { default: 1, vip: 0.9 },
      user_discount_ratios: { vip: { 'gpt-4o': 0.95 } },
    })
    // default 组价 1.0 比 vip 的 0.95 更贵，最便宜来自折扣 → 仍展示
    const info = getGroupDiscountInfo(model, 'all')
    expect(info?.effectiveRatio).toBe(0.95)

    const cheaperBase = makeModel({
      enable_groups: ['default', 'vip'],
      group_ratio: { default: 0.8, vip: 0.9 },
      user_discount_ratios: { vip: { 'gpt-4o': 0.85 } },
    })
    // default 组价 0.8 才是最低，用户在该组没有折扣 → 不画
    expect(getGroupDiscountInfo(cheaperBase, 'all')).toBeNull()
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

  it('折扣与组价相等时不标（避免 -0% 的空标签）', () => {
    const same = makeModel({ user_discount_ratios: { vip: { 'gpt-4o': 0.9 } } })
    expect(getGroupDiscountInfo(same, 'vip')).toBeNull()
  })

  it('折扣高于组价时标为涨价，而不是静默', () => {
    // 0.95 > 组倍率 0.9：用户实际在付更贵的价。
    // 返回 null 会让页面按更贵的价展示却不给任何解释，账单来了才发现。
    const higher = makeModel({
      user_discount_ratios: { vip: { 'gpt-4o': 0.95 } },
    })
    const info = getGroupDiscountInfo(higher, 'vip')
    expect(info).not.toBeNull()
    expect(info?.isIncrease).toBe(true)
    expect(info?.offPercent).toBeLessThan(0)
    expect(info?.offPercent).toBeCloseTo(1 - 0.95 / 0.9, 6)
  })

  it('免费模型不标折扣（划线价与现价同为 0）', () => {
    // 0 元模型挂「-44%」既没信息量，又自相矛盾：划掉的价和现价一样。
    for (const model of [
      makeModel({ model_ratio: 0, user_discount_ratios: { vip: { '*': 0.5 } } }),
      makeModel({
        model_ratio: 0,
        model_price: 0,
        user_discount_ratios: { vip: { '*': 0.5 } },
      }),
    ]) {
      expect(getGroupDiscountInfo(model, 'vip')).toBeNull()
    }
  })

  it('降价时 isIncrease 为 false', () => {
    const model = makeModel({ user_discount_ratios: { vip: { 'gpt-4o': 0.7 } } })
    const info = getGroupDiscountInfo(model, 'vip')
    expect(info?.isIncrease).toBe(false)
    expect(info?.offPercent).toBeGreaterThan(0)
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
