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
import { useTranslation } from 'react-i18next'

import { apiduoBlockMark } from '@/features/apiduo-marks/marks'
import { formatCurrencyFromUSD } from '@/lib/currency'
import { cn } from '@/lib/utils'

import { findModel, resolveGroupRatio } from '../billing/pricing'
import { formatQuota } from '../billing/quota'
import type { PricingSnapshot } from '../billing/types'
import type { PlaygroundMode } from '../types'
import { RollingValue } from './rolling-value'

interface PriceStripProps {
  pricing: PricingSnapshot | null
  model: string
  group: string
  mode: PlaygroundMode
}

function priceText(usdPerMillion: number): string {
  return formatCurrencyFromUSD(usdPerMillion, {
    digitsLarge: 2,
    digitsSmall: 4,
    abbreviate: false,
  })
}

/**
 * 发前就知道单价：模型倍率 1 = $2 / 百万 token，所以
 * 输入单价 = modelRatio × 2，输出单价 = 输入单价 × completionRatio。
 * 折后价用实际生效的倍率算，命中个人折扣时就是折扣价。
 */
export function PriceStrip({ pricing, model, group, mode }: PriceStripProps) {
  const { t } = useTranslation()
  const info = findModel(pricing, model)

  if (!info) {
    return (
      <div
        className='apd-fade-up text-muted-foreground flex min-h-5 flex-wrap items-center gap-1.5 text-[10.5px]'
        key={`${model}-${group}-${mode}`}
      >
        <span className='font-mono'>{model}</span>
        <span className='bg-muted rounded px-1.5 py-0.5'>
          {t('Pricing not available for this model')}
        </span>
      </div>
    )
  }

  const { effective, discount } = resolveGroupRatio(pricing, group, model)

  if (mode === 'image' || info.quotaType === 1) {
    const unit = info.quotaType === 1 ? info.modelPrice : info.modelRatio * 2
    const discounted = unit * effective
    return (
      <div
        className='apd-fade-up text-muted-foreground flex min-h-5 flex-wrap items-center gap-1.5 text-[10.5px]'
        key={`${model}-${group}-${mode}`}
      >
        <span className='text-foreground font-mono font-semibold'>{model}</span>
        <span>
          {t('Per image')}{' '}
          <span className='text-foreground/80 font-mono'>
            {formatCurrencyFromUSD(unit, { digitsLarge: 4, digitsSmall: 4 })}
          </span>
        </span>
        {discount != null ? (
          <span
            className='apd-sheen-host rounded bg-emerald-500/12 px-1.5 py-0.5 font-medium text-emerald-700 dark:text-emerald-400'
            key={`discount-${discount}`}
          >
            {group} {t('{{rate}}% off', { rate: Math.round((1 - discount) * 100) })}
          </span>
        ) : (
          <span>
            {t('Group')} <span className='font-mono'>×{effective.toFixed(2)}</span>
          </span>
        )}
        <span>
          {t('After discount')}{' '}
          <span className='text-foreground font-mono font-semibold'>
            {formatCurrencyFromUSD(discounted, {
              digitsLarge: 4,
              digitsSmall: 4,
            })}
          </span>
        </span>
      </div>
    )
  }

  const inputPerM = info.modelRatio * 2
  const outputPerM = inputPerM * info.completionRatio
  const discountedInput = inputPerM * effective
  const discountedOutput = outputPerM * effective

  return (
    <div
      className='apd-fade-up text-muted-foreground flex min-h-5 flex-wrap items-center gap-x-2 gap-y-1 text-[10.5px]'
      data-apiduo={apiduoBlockMark('playground:pricing')}
      key={`${model}-${group}-${mode}`}
    >
      <span className='text-foreground font-mono font-semibold'>{model}</span>
      <span>
        {t('Input')}{' '}
        <span className='text-foreground/80 font-mono'>
          {priceText(inputPerM)}
        </span>
        /M
      </span>
      <span>
        {t('Output')}{' '}
        <span className='text-foreground/80 font-mono'>
          {priceText(outputPerM)}
        </span>
        /M
      </span>
      {discount != null ? (
        <span
          className='apd-sheen-host rounded bg-emerald-500/12 px-1.5 py-0.5 font-medium text-emerald-700 dark:text-emerald-400'
          key={`discount-${discount}`}
        >
          {group} {t('{{rate}}% off', { rate: Math.round((1 - discount) * 100) })}
        </span>
      ) : (
        <span>
          {t('Group')} <span className='font-mono'>×{effective.toFixed(2)}</span>
        </span>
      )}
      <span>
        {t('After discount')}{' '}
        <span className='text-foreground font-mono font-semibold'>
          {priceText(discountedInput)}
        </span>
        {' / '}
        <span className='text-foreground font-mono font-semibold'>
          {priceText(discountedOutput)}
        </span>
        /M
      </span>
    </div>
  )
}

interface SpendBadgeProps {
  quota: number
  count: number
  settled: number
  className?: string
}

export function SpendBadge({
  quota,
  count,
  settled,
  className,
}: SpendBadgeProps) {
  const { t } = useTranslation()
  if (count === 0) {
    return (
      <div className={cn('text-muted-foreground text-[11px]', className)}>
        {t('This session')} <span className='font-mono'>—</span>
      </div>
    )
  }

  return (
    <div
      className={cn(
        'text-muted-foreground flex items-center gap-1.5 text-[11px] whitespace-nowrap',
        className
      )}
      data-apiduo={apiduoBlockMark('playground:billing')}
      title={t('Settled {{settled}} of {{count}} requests', {
        settled,
        count,
      })}
    >
      <span>{t('This session')}</span>
      <RollingValue
        className='text-foreground font-mono text-[12px] font-semibold'
        value={formatQuota(quota)}
      />
      <span>· {t('{{count}} requests', { count })}</span>
    </div>
  )
}
