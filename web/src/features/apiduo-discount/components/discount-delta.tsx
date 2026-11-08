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
import { getGroupDiscountInfo } from '@/features/pricing/lib/model-helpers'
import { formatPriceAtRatio } from '@/features/pricing/lib/price'
import type { ModelPriceCellOptions } from '@/features/pricing/components/model-price-cell'
import type { PriceType, PricingModel, TokenUnit } from '@/features/pricing/types'

export type DiscountDeltaProps = {
  model: PricingModel
  type: PriceType
  tokenUnit: TokenUnit
  options: ModelPriceCellOptions
}

/**
 * 贴在现价后面的「划线原价 + 折扣标」。
 *
 * 卡片视图和缓存价列都在自己的 JSX 里算价格，不经过 ModelPriceCell，
 * 因此折扣要以独立组件的形式插进去，而不是只在表格视图生效——
 * 否则同一个模型在卡片和表格里显示两种价，用户会以为折扣丢了。
 *
 * 无折扣时返回 null，DOM 里不会留下空节点。
 */
export function DiscountDelta({
  model,
  type,
  tokenUnit,
  options,
}: DiscountDeltaProps) {
  const { showRechargePrice, priceRate, usdExchangeRate, selectedGroup } =
    options
  const info = getGroupDiscountInfo(model, selectedGroup)
  if (!info) return null

  const original = formatPriceAtRatio(
    model,
    type,
    tokenUnit,
    info.baseRatio,
    showRechargePrice,
    priceRate,
    usdExchangeRate,
    false
  )

  // 涨价要显式提示：折扣倍率高于组倍率时用户其实在付更贵的价，
  // 如果只画一条更低的划线价而不标方向，会被误读成「有优惠」。
  const percent = Math.round(Math.abs(info.offPercent) * 100)

  // 独占一行：卡片的宽度装不下「现价 + 单位 + 划线价 + 折扣标」四段，
  // 挤在一行会把主价格的字号压下去，层级反而乱。
  return (
    <div className='flex flex-wrap items-center gap-1'>
      <s className='text-muted-foreground text-xs font-normal'>{original}</s>
      <span
        className={
          info.isIncrease
            ? 'bg-destructive/10 text-destructive rounded px-1 py-px text-[10px] leading-4 font-medium'
            : 'bg-primary/10 text-primary rounded px-1 py-px text-[10px] leading-4 font-medium'
        }
      >
        {info.isIncrease ? '+' : '-'}
        {percent}%
      </span>
    </div>
  )
}
