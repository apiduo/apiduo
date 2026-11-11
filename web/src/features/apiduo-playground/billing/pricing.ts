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
import { api } from '@/lib/api'

import type { PricingModel, PricingSnapshot } from './types'

/**
 * 折扣规则带有效期，服务端按「当前时间」展开成 group→model→rate 的静态表。
 * 因此这张表不能长期缓存：一次会话内有效即可，跨会话必须重新拉。
 */
const SNAPSHOT_TTL_MS = 5 * 60 * 1000

let cached: PricingSnapshot | null = null
let inflight: Promise<PricingSnapshot> | null = null

function toNumber(value: unknown, fallback = 0): number {
  const num = typeof value === 'number' ? value : Number(value)
  return Number.isFinite(num) ? num : fallback
}

/** Tags arrive as a comma/semicolon separated string; lower-case them once so
 *  every reader agrees on what "new" means. */
function parseTags(raw: unknown): string[] {
  let values: string[] = []
  if (Array.isArray(raw)) {
    values = raw.filter((item): item is string => typeof item === 'string')
  } else if (typeof raw === 'string') {
    values = raw.split(/[,;]/)
  }
  return values.map((item) => item.trim().toLowerCase()).filter(Boolean)
}

function parseModel(raw: Record<string, unknown>): PricingModel | null {
  const modelName = typeof raw.model_name === 'string' ? raw.model_name : ''
  if (!modelName) return null

  const groups = Array.isArray(raw.enable_groups)
    ? raw.enable_groups.filter((g): g is string => typeof g === 'string')
    : []
  const endpointTypes = Array.isArray(raw.supported_endpoint_types)
    ? raw.supported_endpoint_types.filter(
        (item): item is string => typeof item === 'string'
      )
    : []

  return {
    modelName,
    quotaType: toNumber(raw.quota_type),
    modelRatio: toNumber(raw.model_ratio),
    modelPrice: toNumber(raw.model_price),
    // 按次计费的模型不带 completion_ratio，缺省 1 不会影响按次公式
    completionRatio: toNumber(raw.completion_ratio, 1),
    cacheRatio: toNumber(raw.cache_ratio, 1),
    enableGroups: groups,
    endpointTypes,
    description:
      typeof raw.description === 'string' ? raw.description.trim() : '',
    tags: parseTags(raw.tags),
    billingMode:
      typeof raw.billing_mode === 'string' ? raw.billing_mode.trim() : '',
    billingExpr:
      typeof raw.billing_expr === 'string' ? raw.billing_expr.trim() : '',
  }
}

function parseSnapshot(body: unknown): PricingSnapshot {
  const payload = (body ?? {}) as Record<string, unknown>
  const list = Array.isArray(payload.data) ? payload.data : []

  const models: Record<string, PricingModel> = {}
  for (const item of list) {
    if (!item || typeof item !== 'object') continue
    const parsed = parseModel(item as Record<string, unknown>)
    if (parsed) models[parsed.modelName] = parsed
  }

  const groupRatio: Record<string, number> = {}
  const rawGroupRatio = (payload.group_ratio ?? {}) as Record<string, unknown>
  for (const [group, ratio] of Object.entries(rawGroupRatio)) {
    groupRatio[group] = toNumber(ratio, 1)
  }

  const userDiscount: Record<string, Record<string, number>> = {}
  const rawDiscount = (payload.user_discount_ratios ?? {}) as Record<
    string,
    unknown
  >
  for (const [group, models_] of Object.entries(rawDiscount)) {
    if (!models_ || typeof models_ !== 'object') continue
    const rates: Record<string, number> = {}
    for (const [model, rate] of Object.entries(
      models_ as Record<string, unknown>
    )) {
      rates[model] = toNumber(rate, 1)
    }
    userDiscount[group] = rates
  }

  return { models, groupRatio, userDiscount, fetchedAt: Date.now() }
}

/** 拉取并缓存定价快照；并发调用共享同一次请求 */
export function loadPricing(force = false): Promise<PricingSnapshot> {
  if (!force && cached && Date.now() - cached.fetchedAt < SNAPSHOT_TTL_MS) {
    return Promise.resolve(cached)
  }
  if (!force && inflight) return inflight

  inflight = api
    .get('/api/pricing')
    .then((res) => {
      const snapshot = parseSnapshot(res.data)
      cached = snapshot
      return snapshot
    })
    .finally(() => {
      inflight = null
    })

  return inflight
}

export function getCachedPricing(): PricingSnapshot | null {
  return cached
}

export function clearPricingCache(): void {
  cached = null
  inflight = null
}

export function findModel(
  snapshot: PricingSnapshot | null,
  modelName: string
): PricingModel | null {
  if (!snapshot || !modelName) return null
  return snapshot.models[modelName] ?? null
}

/**
 * 组倍率与个人折扣只取其一：折扣命中时**覆盖**组倍率，不相乘。
 * 与 `pkg/apiduo/discount` 的 Resolve 语义一致，写错会让对账单和日志差一个数量级。
 */
export function resolveGroupRatio(
  snapshot: PricingSnapshot | null,
  group: string,
  modelName: string
): { effective: number; discount: number | null; base: number } {
  const base = snapshot?.groupRatio[group] ?? 1
  const discount = snapshot?.userDiscount[group]?.[modelName] ?? null
  if (discount === null || !Number.isFinite(discount) || discount <= 0) {
    return { effective: base, discount: null, base }
  }
  return { effective: discount, discount, base }
}

/**
 * Where a model is billed by an expression, the ratio table is not the price:
 * production's `flux-3-image` declares `tier("base", fixed(0.2))` and no usable
 * ratio, so quoting `model_ratio * 2` would print a per-million-token number
 * for a per-image model. Only the simple `fixed(x)` shape is understood here;
 * any other expression falls back to the ratio table.
 */
const FIXED_EXPR = /fixed\(\s*([0-9]+(?:\.[0-9]+)?)\s*\)/i

export function fixedUnitPrice(info: PricingModel): number | null {
  if (info.billingMode !== 'tiered_expr') return null
  const match = FIXED_EXPR.exec(info.billingExpr)
  if (!match) return null
  const value = Number(match[1])
  return Number.isFinite(value) ? value : null
}
