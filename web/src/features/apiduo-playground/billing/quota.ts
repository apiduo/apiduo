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
import { formatQuotaWithCurrency, getCurrencyDisplay } from '@/lib/currency'

import { resolveGroupRatio } from './pricing'
import type {
  ChatUsageTokens,
  PricingModel,
  PricingSnapshot,
  QuotaBreakdown,
} from './types'

function quotaPerUnit(): number {
  const { config } = getCurrencyDisplay()
  return config.quotaPerUnit > 0 ? config.quotaPerUnit : 500000
}

function roundQuota(value: number): number {
  if (!Number.isFinite(value) || value <= 0) return 0
  return Math.round(value)
}

/**
 * 复刻 `service/text_quota.go` 的 OpenAI 语义分支。
 *
 * 三个容易算错的地方，任何一处偏移都会让对账单和日志对不上：
 *   1. cacheTokens 已包含在 promptTokens 内，要先减掉再单独乘 cacheRatio
 *   2. completion 按 completionRatio 计，不再乘 modelRatio 之外的系数
 *   3. 组倍率与 modelRatio 相乘后才作用到 (prompt + completion) 整体
 */
export function computeChatQuota(
  usage: ChatUsageTokens,
  model: PricingModel | null,
  snapshot: PricingSnapshot | null,
  group: string,
  modelName: string
): QuotaBreakdown | null {
  if (!model) return null

  const { effective, discount, base } = resolveGroupRatio(
    snapshot,
    group,
    modelName
  )

  if (model.quotaType === 1) {
    const quota = roundQuota(model.modelPrice * quotaPerUnit() * effective)
    return {
      quota,
      usd: quota / quotaPerUnit(),
      effectiveGroupRatio: effective,
      discountRate: discount,
      baseGroupRatio: base,
      modelRatio: model.modelRatio,
      completionRatio: model.completionRatio,
      cacheRatio: model.cacheRatio,
      baseTokens: 0,
      cacheTokens: 0,
      formula: `按次计费 ${model.modelPrice} × ${quotaPerUnit().toLocaleString()} × ${effective.toFixed(2)} = ${quota.toLocaleString()} 额度`,
    }
  }

  const cacheTokens = Math.max(0, usage.cacheTokens)
  const baseTokens = Math.max(0, usage.promptTokens - cacheTokens)
  const cacheRatio = model.cacheRatio > 0 ? model.cacheRatio : 1

  const promptQuota = baseTokens + cacheTokens * cacheRatio
  const completionQuota = usage.completionTokens * model.completionRatio
  const ratio = model.modelRatio * effective
  let quota = roundQuota((promptQuota + completionQuota) * ratio)

  // 后端保证：倍率有效但算出来是 0 时记 1 额度，前端估算要与之一致
  if (ratio > 0 && quota === 0 && promptQuota + completionQuota > 0) {
    quota = 1
  }

  const promptPart =
    cacheTokens > 0
      ? `${baseTokens.toLocaleString()} + ${cacheTokens.toLocaleString()} × ${cacheRatio.toFixed(2)}`
      : baseTokens.toLocaleString()
  const formula = `(${promptPart} + ${usage.completionTokens.toLocaleString()} × ${model.completionRatio.toFixed(2)}) × ${model.modelRatio.toFixed(2)} × ${effective.toFixed(2)} = ${quota.toLocaleString()} 额度`

  return {
    quota,
    usd: quota / quotaPerUnit(),
    effectiveGroupRatio: effective,
    discountRate: discount,
    baseGroupRatio: base,
    modelRatio: model.modelRatio,
    completionRatio: model.completionRatio,
    cacheRatio,
    baseTokens,
    cacheTokens,
    formula,
  }
}

/**
 * 生图按张计费：model_price 是单张价格，张数由 `n` 决定。
 * 部分渠道把尺寸档位折进 model_price，前端不再二次换算。
 */
export function computeImageQuota(
  count: number,
  model: PricingModel | null,
  snapshot: PricingSnapshot | null,
  group: string,
  modelName: string
): QuotaBreakdown | null {
  if (!model) return null

  const { effective, discount, base } = resolveGroupRatio(
    snapshot,
    group,
    modelName
  )
  const images = Math.max(1, Math.floor(count) || 1)
  const unitPrice =
    model.quotaType === 1 ? model.modelPrice : model.modelRatio * 2
  const quota = roundQuota(unitPrice * quotaPerUnit() * effective * images)

  return {
    quota,
    usd: quota / quotaPerUnit(),
    effectiveGroupRatio: effective,
    discountRate: discount,
    baseGroupRatio: base,
    modelRatio: model.modelRatio,
    completionRatio: model.completionRatio,
    cacheRatio: model.cacheRatio,
    baseTokens: 0,
    cacheTokens: 0,
    formula: `${unitPrice} × ${images} 张 × ${effective.toFixed(2)} = ${quota.toLocaleString()} 额度`,
  }
}

/** 金额展示：小额必须留够位数，否则一次生图会显示成 ¥0.00 */
export function formatQuota(quota: number): string {
  return formatQuotaWithCurrency(quota, {
    digitsLarge: 2,
    digitsSmall: 6,
    minimumNonZero: 0.000001,
    abbreviate: false,
  })
}

export function formatQuotaPrecise(quota: number): string {
  return formatQuotaWithCurrency(quota, {
    digitsLarge: 4,
    digitsSmall: 6,
    minimumNonZero: 0.000001,
    abbreviate: false,
  })
}

export function formatUsd(quota: number): string {
  const usd = quota / quotaPerUnit()
  if (usd === 0) return '$0'
  if (usd < 0.000001) return '<$0.000001'
  return `$${usd.toFixed(6)}`
}
