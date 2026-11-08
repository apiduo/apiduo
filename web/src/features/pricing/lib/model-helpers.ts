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
    const ratio = groupRatio[group]
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
 * 个人折扣信息：划线原价与折后价都用同一把尺子，保证三个数字自洽。
 *
 * baseRatio      该组配置的 group_ratio（划线原价）
 * effectiveRatio 折后倍率（个人折扣覆盖后的值）
 * offPercent     折后价相对划线原价降了多少（0.3 表示 -30%）
 *
 * 只在「显式选中了某个分组、且该组确实配了折扣」时返回；选「全部」返回 null，
 * 因为此时展示的是跨组最低价，折扣无法归因到某一个组。
 */
export type GroupDiscountInfo = {
  baseRatio: number
  effectiveRatio: number
  offPercent: number
}

export function getGroupDiscountInfo(
  model: PricingModel,
  selectedGroup?: string
): GroupDiscountInfo | null {
  const modelEnableGroups = Array.isArray(model.enable_groups)
    ? model.enable_groups
    : []

  if (
    !selectedGroup ||
    selectedGroup === FILTER_ALL ||
    !modelEnableGroups.includes(selectedGroup)
  ) {
    return null
  }

  const discountByGroup = model.user_discount_ratios?.[selectedGroup]
  if (!discountByGroup) return null

  const effectiveRatio =
    discountByGroup[model.model_name] ?? discountByGroup['*']
  if (
    typeof effectiveRatio !== 'number' ||
    !Number.isFinite(effectiveRatio) ||
    effectiveRatio <= 0 ||
    effectiveRatio >= 1
  ) {
    return null
  }

  const baseRatio = getConfiguredGroupRatio(
    model.group_ratio || {},
    selectedGroup
  )
  if (!Number.isFinite(baseRatio) || baseRatio <= 0) return null
  if (effectiveRatio >= baseRatio) return null

  return {
    baseRatio,
    effectiveRatio,
    offPercent: 1 - effectiveRatio / baseRatio,
  }
}
// APIDUO:discount:END
