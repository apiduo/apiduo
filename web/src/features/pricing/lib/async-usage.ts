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
import { resolveLocalizedText } from '@/lib/localized-text'

import type { BillingUsageSchema, PricingModel } from '../types'
import { compileBillingExpression } from './billing-expression/parser'
import { visitExpression } from './billing-expression/types'

/**
 * Usage facts a non-plugin task channel (type 8001) publishes for itself.
 *
 * The keys mirror `setting/billing_setting.tiered_billing.go`'s
 * `advancedAsyncUsageSchema`. Only `factor` is not a quantity: type 8001
 * multiplies the per-unit price by it (resolution tiers), so a price that
 * reads it must be shown as "per unit × multiplier" instead of a second
 * per-unit rate.
 */
export const ASYNC_USAGE_SCHEMA: BillingUsageSchema = {
  count: {
    type: 'number',
    unit: 'count',
    unitLabel: { en: 'request', zh: '次' },
    description: { en: 'Per request', zh: '每次请求' },
  },
  seconds: {
    type: 'number',
    unit: 'second',
    description: { en: 'Duration', zh: '时长' },
  },
  tokens: {
    type: 'number',
    unit: 'token',
    description: { en: 'Token usage', zh: 'Token 用量' },
  },
  factor: {
    type: 'number',
    unit: 'count',
    unitLabel: { en: '×', zh: '×' },
    description: { en: 'Price multiplier', zh: '计价倍率' },
  },
}

/** Type 8001 facts that scale a price instead of being billed per unit. */
export const ASYNC_MULTIPLIER_KEYS: ReadonlySet<string> = new Set(['factor'])

/** Literal `u("<key>")` keys of an expression; null when it does not compile. */
export function usageFactKeys(expression: string): Set<string> | null {
  const compiled = compileBillingExpression(expression)
  if (compiled.status !== 'ready') return null
  const keys = new Set<string>()
  visitExpression(compiled.ast, (node) => {
    if (node.kind !== 'call' || node.name !== 'u') return
    const key = node.args[0]
    if (key?.kind === 'literal' && typeof key.value === 'string') {
      keys.add(key.value)
    }
  })
  return keys
}

/**
 * Usage schema for a model's expression: the plugin schema when the model has
 * one, otherwise the type 8001 built-in facts. The built-in table only applies
 * when every `u()` key is one of its own, so a plugin expression can never be
 * displayed against the wrong fact table.
 */
export function resolveUsageSchema(
  model: PricingModel
): BillingUsageSchema | undefined {
  if (
    model.billing_usage_schema &&
    Object.keys(model.billing_usage_schema).length > 0
  ) {
    return model.billing_usage_schema
  }
  if (model.billing_mode !== 'tiered_expr' || !model.billing_expr) {
    return undefined
  }
  const keys = usageFactKeys(model.billing_expr)
  if (!keys?.size) return undefined
  for (const key of keys) {
    if (!Object.hasOwn(ASYNC_USAGE_SCHEMA, key)) return undefined
  }
  return ASYNC_USAGE_SCHEMA
}

/** Whether the price reads a type 8001 multiplier, i.e. is not a flat rate. */
export function usesAsyncMultiplier(expression: string): boolean {
  const keys = usageFactKeys(expression)
  if (!keys) return false
  return [...keys].some((key) => ASYNC_MULTIPLIER_KEYS.has(key))
}

const USAGE_CONDITION = /^\(u\("([^"]+)"\)\s*(>=|<=|>|<|==|!=)\s*(-?[\d.]+)\)$/

/**
 * Name the usage threshold a tier depends on, e.g. `u("tokens") > 0`: the
 * per-token rate applies only when the vendor reported usage. Returns null for
 * every other condition so the caller keeps its generic formatter.
 */
export function formatAsyncUsageCondition(
  conditionText: string | undefined,
  schema: BillingUsageSchema | undefined,
  language: string,
  t: (key: string, options?: Record<string, unknown>) => string
): string | null {
  if (!conditionText || !schema) return null
  const text = conditionText.trim()
  const negated = text.startsWith('!')
  const body = negated ? text.slice(1).trim() : text
  const match = USAGE_CONDITION.exec(body)
  if (!match) return null
  const [, field, operator, value] = match
  const definition = schema[field]
  if (!definition) return null
  if (negated && operator === '>') {
    return t('Without {{field}}', {
      field: resolveLocalizedText(definition.description, language) || field,
    })
  }
  return t('{{field}} {{operator}} {{value}}', {
    field: resolveLocalizedText(definition.description, language) || field,
    operator,
    value,
  })
}

/** A tier priced from usage units, as opposed to a fixed per-request price. */
export type UsagePricedTier = {
  label: string
  conditions: { field: string; value: string }[]
  constant: number
  unitPrices: Record<string, number>
  multipliers?: string[]
  conditionText?: string
}

function isUsagePricedTier(tier: unknown): tier is UsagePricedTier {
  return (
    typeof tier === 'object' &&
    tier !== null &&
    Object.hasOwn(tier, 'unitPrices') &&
    typeof (tier as UsagePricedTier).unitPrices === 'object'
  )
}

/**
 * One tier carrying every field any tier bills, so a fallback chain
 * (`tokens > 0 ? per-token : per-call`) lists both prices instead of only the
 * branch the summary happens to end on. Values are the per-tier minimums; the
 * caller widens them into a range.
 */
export function mergeTaskPricingTiers(
  tiers: unknown[]
): UsagePricedTier | null {
  const priced = tiers.filter(isUsagePricedTier)
  if (priced.length === 0) return null
  const unitPrices: Record<string, number> = {}
  const multipliers = new Set<string>()
  for (const tier of priced) {
    for (const [field, value] of Object.entries(tier.unitPrices)) {
      const current = unitPrices[field]
      unitPrices[field] = current === undefined ? value : Math.min(current, value)
    }
    for (const field of tier.multipliers ?? []) multipliers.add(field)
  }
  return {
    label: priced[0].label,
    conditions: [],
    constant: priced[0].constant,
    unitPrices,
    ...(multipliers.size > 0 ? { multipliers: [...multipliers] } : {}),
  }
}
