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
// APIDUO:discount:BEGIN
import type { PricingModel, TokenUnit, PriceType } from '../types'
import { getGroupDiscountInfo } from './model-helpers'
import { formatPriceAtRatio } from './price'

export type DiscountedPrice = {
  /** 划线原价：用该组配置的 group_ratio 计算，不含个人折扣 */
  original: string
  /** 折后价相对划线原价降了多少，0.3 表示 -30% */
  offPercent: number
}

/**
 * 为价格单元格准备「划线原价 + 折扣幅度」。
 *
 * 只负责原价：折后价仍由调用方用 formatPrice / formatRequestPrice 生成，
 * 那两个函数走 getDisplayGroupRatio，已经含折扣。两边共用同一套格式参数，
 * 保证展示出的两个数字只差倍率。
 *
 * 返回 null 表示不展示折扣：未选中具体分组、该组没配折扣、或折扣并不低于组价。
 */
export function buildDiscountedPrices(
  model: PricingModel,
  types: PriceType[],
  tokenUnit: TokenUnit,
  showRechargePrice?: boolean,
  priceRate?: number,
  usdExchangeRate?: number,
  selectedGroup?: string
): DiscountedPrice[] | null {
  const info = getGroupDiscountInfo(model, selectedGroup)
  if (!info) return null

  return types.map((type) => ({
    original: formatPriceAtRatio(
      model,
      type,
      tokenUnit,
      info.baseRatio,
      showRechargePrice,
      priceRate,
      usdExchangeRate,
      false
    ),
    offPercent: info.offPercent,
  }))
}
// APIDUO:discount:END
