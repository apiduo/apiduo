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

import {
  ASYNC_USAGE_SCHEMA,
  formatAsyncUsageCondition,
  resolveUsageSchema,
  usesAsyncMultiplier,
} from '../lib/async-usage'
import { getDynamicPricingSummary } from '../lib/dynamic-price'
import { getTaskPricingDisplayTiers } from '../lib/task-matrix-display'
import type { PricingModel } from '../types'

function asyncModel(expr: string, overrides: Partial<PricingModel> = {}): PricingModel {
  return {
    model_name: 'async-model',
    quota_type: 0,
    model_ratio: 37.5,
    model_price: 0,
    completion_ratio: 1,
    enable_groups: ['default'],
    billing_mode: 'tiered_expr',
    billing_expr: expr,
    ...overrides,
  } as PricingModel
}

const options = {
  tokenUnit: 'M' as const,
  showRechargePrice: false,
  priceRate: 1,
  usdExchangeRate: 1,
}

describe('type 8001 built-in usage facts', () => {
  test('resolves the built-in table for an expression without a plugin schema', () => {
    expect(resolveUsageSchema(asyncModel('tier("request", u("count") * 0.1)'))).toBe(
      ASYNC_USAGE_SCHEMA
    )
  })

  test('declines a table when the expression reads a key outside the built-in facts', () => {
    expect(
      resolveUsageSchema(asyncModel('tier("base", u("frames") * 0.1)'))
    ).toBeUndefined()
  })

  test('keeps an explicit plugin schema authoritative', () => {
    const schema = { seconds: { type: 'number' as const, unit: 'second' as const } }
    expect(
      resolveUsageSchema(asyncModel('tier("base", u("seconds") * 0.1)', { billing_usage_schema: schema }))
    ).toBe(schema)
  })

  test('reports which expressions scale their rate by a multiplier', () => {
    expect(usesAsyncMultiplier('tier("base", u("seconds") * u("factor") * 0.01)')).toBe(true)
    expect(usesAsyncMultiplier('tier("request", u("count") * 0.1)')).toBe(false)
  })
})

describe('type 8001 price summaries', () => {
  test('shows a per-request price with its request unit', () => {
    const summary = getDynamicPricingSummary(
      asyncModel('tier("request", u("count") * 0.10)'),
      options
    )

    expect(summary?.isSpecialExpression).toBe(false)
    expect(summary?.isTaskUsage).toBe(true)
    expect(summary?.primaryEntries).toHaveLength(1)
    expect(summary?.primaryEntries[0]?.field).toBe('count')
    expect(summary?.primaryEntries[0]?.value).toBe(0.1)
    expect(summary?.primaryEntries[0]?.unit).toBe('count')
    expect(summary?.primaryEntries[0]?.unitLabel).toEqual({ en: 'request', zh: '次' })
  })

  test('shows a per-second price and flags the resolution multiplier', () => {
    const summary = getDynamicPricingSummary(
      asyncModel('tier("base", u("seconds") * u("factor") * 0.01)'),
      options
    )

    expect(summary?.isSpecialExpression).toBe(false)
    expect(summary?.isAsyncMultiplier).toBe(true)
    // The multiplier scales the rate; it is never a second billed quantity.
    expect(summary?.entries.map((entry) => entry.field)).toEqual(['seconds'])
    const seconds = summary?.entries[0]
    expect(seconds?.value).toBe(0.01)
    expect(seconds?.unit).toBe('second')
  })

  test('lists both sides of a token fallback chain', () => {
    const summary = getDynamicPricingSummary(
      asyncModel(
        'u("tokens") > 0 ? tier("token", u("tokens") * 10.0 / 1000000) : tier("call", u("count") * 0.20)'
      ),
      options
    )

    expect(summary?.isSpecialExpression).toBe(false)
    expect(summary?.hasTokenUsageFallback).toBe(true)
    expect(summary?.tierCount).toBe(2)
    const byField = new Map(summary?.entries.map((entry) => [entry.field, entry]))
    // A token price is quoted per million tokens; the fallback stays per call.
    expect(byField.get('tokens')?.value).toBe(10)
    expect(byField.get('count')?.value).toBe(0.2)
    expect(byField.get('tokens')?.formattedRange).toBeUndefined()
    expect(byField.get('count')?.formattedRange).toBeUndefined()
  })

  test('expands without a schema, matching the marketplace data loader', () => {
    const tiers = getTaskPricingDisplayTiers(
      'u("tokens") > 0 ? tier("token", u("tokens") * 10.0 / 1000000) : tier("call", u("count") * 0.20)',
      ASYNC_USAGE_SCHEMA
    )

    expect(tiers).toHaveLength(2)
    expect(tiers[0]?.unitPrices).toEqual({ tokens: 10 })
    expect(tiers[1]?.unitPrices).toEqual({ count: 0.2 })
  })

  test('names a usage threshold instead of quoting expression source', () => {
    const t = (key: string, values?: Record<string, unknown>) =>
      values ? `${key}|${JSON.stringify(values)}` : key

    expect(
      formatAsyncUsageCondition('(u("tokens") > 0)', ASYNC_USAGE_SCHEMA, 'en', t)
    ).toContain('Token usage')
    expect(
      formatAsyncUsageCondition('!(u("tokens") > 0)', ASYNC_USAGE_SCHEMA, 'en', t)
    ).toContain('Without {{field}}')
    expect(
      formatAsyncUsageCondition('(u("missing") > 0)', ASYNC_USAGE_SCHEMA, 'en', t)
    ).toBeNull()
  })
})

// APIDUO:async-usage:BEGIN
describe('type 8001 prices after a JSON round trip', () => {
  // /api/pricing serializes the schema, so the browser never sees the module
  // constant. Display must not depend on object identity.
  const roundTripped = JSON.parse(
    JSON.stringify(ASYNC_USAGE_SCHEMA)
  ) as typeof ASYNC_USAGE_SCHEMA

  test('keeps the resolution multiplier out of the billed quantities', () => {
    const tiers = getTaskPricingDisplayTiers(
      'tier("base", u("seconds") * u("factor") * 0.01)',
      roundTripped
    )

    expect(tiers).toHaveLength(1)
    expect(tiers[0]?.unitPrices).toEqual({ seconds: 0.01 })
    expect(tiers[0]?.multipliers).toEqual(['factor'])
  })

  test('still expands the per-request and token fallback shapes', () => {
    expect(
      getTaskPricingDisplayTiers(
        'tier("request", u("count") * 0.10)',
        roundTripped
      ).map((tier) => tier.unitPrices)
    ).toEqual([{ count: 0.1 }])

    expect(
      getTaskPricingDisplayTiers(
        'u("tokens") > 0 ? tier("token", u("tokens") * 10.0 / 1000000) : tier("call", u("count") * 0.20)',
        roundTripped
      ).map((tier) => tier.unitPrices)
    ).toEqual([{ tokens: 10 }, { count: 0.2 }])
  })
})
// APIDUO:async-usage:END

// APIDUO:async-usage:BEGIN
describe('gateway-serialized built-in facts', () => {
  test('restore the console labels the gateway does not publish', () => {
    // The gateway publishes structure only (type/unit), never the labels.
    const gatewaySchema = Object.fromEntries(
      Object.entries(ASYNC_USAGE_SCHEMA).map(([key, definition]) => [
        key,
        { type: definition.type, unit: definition.unit },
      ])
    )
    const schema = resolveUsageSchema(
      asyncModel('tier("base", u("seconds") * u("factor") * 0.01)', {
        billing_usage_schema: gatewaySchema,
      })
    )

    // Without this the card would read "seconds" instead of the localized name.
    expect(schema?.seconds?.description).toEqual({ en: 'Duration', zh: '时长' })
    expect(schema?.count?.unitLabel).toEqual({ en: 'request', zh: '次' })
  })

  test('leave a plugin schema exactly as declared', () => {
    const pluginSchema = { seconds: { type: 'number' as const, unit: 'second' as const } }
    expect(
      resolveUsageSchema(
        asyncModel('tier("base", u("seconds") * 0.1)', {
          billing_usage_schema: pluginSchema,
        })
      )
    ).toBe(pluginSchema)
  })
})
// APIDUO:async-usage:END
