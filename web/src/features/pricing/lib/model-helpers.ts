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
import { EXCLUDED_GROUPS, FILTER_ALL, QUOTA_TYPE_VALUES } from '../constants'
import type { PricingModel } from '../types'

// ----------------------------------------------------------------------------
// Model Helper Utilities
// ----------------------------------------------------------------------------

/**
 * Get available groups for a model
 */
export function getAvailableGroups(
  model: PricingModel,
  usableGroup: Record<string, { desc: string; ratio: number }>
): string[] {
  const modelEnableGroups = Array.isArray(model.enable_groups)
    ? model.enable_groups
    : []

  return Object.keys(usableGroup)
    .filter((g) => !EXCLUDED_GROUPS.includes(g))
    .filter((g) => modelEnableGroups.includes(g))
}

/**
 * Read a configured group ratio while preserving valid zero ratios.
 */
export function getConfiguredGroupRatio(
  groupRatio: Record<string, number>,
  group: string
): number {
  const ratio = groupRatio[group]
  return typeof ratio === 'number' && Number.isFinite(ratio) ? ratio : 1
}

/**
 * Resolve the group ratio used by model square summary prices.
 *
 * When no specific group is selected, the model square shows the best price
 * available to the viewer. When a group filter is active, it shows that
 * group's price instead.
 */
export function getDisplayGroupRatio(
  model: PricingModel,
  selectedGroup?: string
): number {
  const modelEnableGroups = Array.isArray(model.enable_groups)
    ? model.enable_groups
    : []
  const groupRatio = model.group_ratio || {}

  if (
    selectedGroup &&
    selectedGroup !== FILTER_ALL &&
    modelEnableGroups.includes(selectedGroup)
  ) {
    // APIDUO:discount:BEGIN
    const discountByGroup = model.user_discount_ratios?.[selectedGroup]
    const discountRate =
      discountByGroup?.[model.model_name] ?? discountByGroup?.['*']
    if (typeof discountRate === 'number' && discountRate > 0) {
      return discountRate
    }
    // APIDUO:discount:END
    return getConfiguredGroupRatio(groupRatio, selectedGroup)
  }

  if (modelEnableGroups.length === 0) {
    return 1
  }

  let minRatio = Number.POSITIVE_INFINITY

  for (const group of modelEnableGroups) {
    // APIDUO:discount:BEGIN
    // 折扣优先于组价：这一步决定「全部」视图的标价。
    // 少了它，标价会比用户实付高（折扣只在选中具体分组时才体现）。
    const ratio = discountRateForModelGroup(model, group) ?? groupRatio[group]
    // APIDUO:discount:END
    if (
      typeof ratio === 'number' &&
      Number.isFinite(ratio) &&
      ratio < minRatio
    ) {
      minRatio = ratio
    }
  }

  return minRatio === Number.POSITIVE_INFINITY ? 1 : minRatio
}

/**
 * Replace model placeholder in endpoint path
 */
export function replaceModelInPath(path: string, modelName: string): string {
  return path.replaceAll('{model}', modelName)
}

/**
 * Check if model is token-based pricing
 */
export function isTokenBasedModel(model: PricingModel): boolean {
  return model.quota_type === QUOTA_TYPE_VALUES.TOKEN
}

// APIDUO:discount:BEGIN
/**
 * 取某模型在某组下的个人折扣率；没有折扣或倍率非法时返回 null。
 *
 * 抽成函数是因为「全部」视图要在多个组上重复做同一件事：
 * 先看该组有没有折扣，没有才回落到组价。
 */
function discountRateForModelGroup(
  model: PricingModel,
  group: string
): number | null {
  const discountByGroup = model.user_discount_ratios?.[group]
  const rate = discountByGroup?.[model.model_name] ?? discountByGroup?.['*']
  if (typeof rate !== 'number' || !Number.isFinite(rate)) return null
  if (rate <= 0 || rate >= 1) return null
  return rate
}

/**
 * 「全部」视图下最便宜的那个组。
 *
 * 把折扣算进跨组比较，才能让标价等于用户实际能拿到的最低价。
 * 只在最便宜的价确实来自个人折扣时才算「有折扣」——若最便宜的组本来
 * 就是组价（用户在该组没有折扣），画划线价会凭空造出一个不存在的优惠。
 */
function bestPricedGroup(
  model: PricingModel,
  groups: string[]
): { baseRatio: number; effectiveRatio: number; discounted: boolean } | null {
  const groupRatio = model.group_ratio || {}
  let best: {
    baseRatio: number
    effectiveRatio: number
    discounted: boolean
  } | null = null

  for (const group of groups) {
    const baseRatio = getConfiguredGroupRatio(groupRatio, group)
    if (!Number.isFinite(baseRatio) || baseRatio <= 0) continue
    const rate = discountRateForModelGroup(model, group)
    const effectiveRatio = rate ?? baseRatio
    if (best && effectiveRatio >= best.effectiveRatio) continue
    best = {
      baseRatio,
      effectiveRatio,
      // 折扣率恰好等于组价时视作无折扣，避免 -0% 的空标签
      discounted: rate !== null && rate !== baseRatio,
    }
  }
  return best
}

/**
 * 个人折扣信息：划线原价与折后价都用同一把尺子，保证三个数字自洽。
 *
 * baseRatio      该组配置的 group_ratio（划线原价）
 * effectiveRatio 折后倍率（个人折扣覆盖后的值）
 * offPercent     相对划线原价的变动（0.3 表示 -30%，负数表示涨价）
 * isIncrease     折扣倍率高于组倍率，即用户其实在付更贵的价
 *
 * 选中具体分组时按该组算；选「全部」时取折扣后最便宜的组，
 * 只有当最便宜价确实来自个人折扣才返回（否则画出来的划线价是假的）。
 */
export type GroupDiscountInfo = {
  baseRatio: number
  effectiveRatio: number
  /** 负数表示涨价（折扣倍率高于组倍率），正数表示降价 */
  offPercent: number
  /** 折扣倍率高于组倍率：用户支付得更多，必须显式提示而不是静默 */
  isIncrease: boolean
}

export function getGroupDiscountInfo(
  model: PricingModel,
  selectedGroup?: string
): GroupDiscountInfo | null {
  // 免费模型不打折扣标：基准价为 0 时现价和划线价都是 0，
  // 却挂着「-44%」，既是噪音也自相矛盾（划掉的价和现价一样）。
  const perRequest = model.quota_type === QUOTA_TYPE_VALUES.REQUEST
  const baseUnitPrice = perRequest ? model.model_price : model.model_ratio
  if (
    typeof baseUnitPrice !== 'number' ||
    !Number.isFinite(baseUnitPrice) ||
    baseUnitPrice <= 0
  ) {
    return null
  }

  const modelEnableGroups = Array.isArray(model.enable_groups)
    ? model.enable_groups
    : []

  let baseRatio: number
  let effectiveRatio: number

  if (selectedGroup === FILTER_ALL || !selectedGroup) {
    // 「全部」：取折扣后最便宜的组，只有它确实来自个人折扣时才画划线价。
    // 不给折扣就会显示「跨组最低的组价」，而用户实付更低，标价与账单对不上。
    const best = bestPricedGroup(model, modelEnableGroups)
    if (!best || !best.discounted) return null
    baseRatio = best.baseRatio
    effectiveRatio = best.effectiveRatio
  } else {
    if (!modelEnableGroups.includes(selectedGroup)) return null
    const rate = discountRateForModelGroup(model, selectedGroup)
    if (rate === null) return null
    effectiveRatio = rate
    baseRatio = getConfiguredGroupRatio(model.group_ratio || {}, selectedGroup)
    if (!Number.isFinite(baseRatio) || baseRatio <= 0) return null
  }

  const offPercent = 1 - effectiveRatio / baseRatio
  if (offPercent === 0) return null

  // 折扣倍率高于组倍率时不能返回 null：那样页面会静默按更贵的价展示，
  // 用户既看不到涨价原因，也无从判断账单。改为标记 isIncrease 交给上层提示。
  return {
    baseRatio,
    effectiveRatio,
    offPercent,
    isIncrease: offPercent < 0,
  }
}
// APIDUO:discount:END
