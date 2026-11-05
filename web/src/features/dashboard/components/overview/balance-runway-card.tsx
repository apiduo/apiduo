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
import { useQuery } from '@tanstack/react-query'
import { CalendarClock, TriangleAlert, Wallet } from 'lucide-react'
import { useMemo, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { Skeleton } from '@/components/ui/skeleton'
import { getSelfUsageSeries } from '@/features/dashboard/api'
import {
  buildDailySeries,
  computeRunway,
  roundTopUp,
  type RunwayResult,
} from '@/features/dashboard/lib/forecast'
import { formatQuota } from '@/lib/format'
import { getCurrencyDisplay } from '@/lib/currency'
import { requireServerSuccess } from '@/lib/server-error-message'
import { cn } from '@/lib/utils'
import { useAuthStore } from '@/stores/auth-store'

const RUNWAY_DAYS = 14
// The average uses the last 7 days; the window above only feeds the chart of
// history behind it, so the label states 7 rather than 14.
const AVERAGE_DAYS = 7
const TARGET_COVERAGE_DAYS = 30

function levelOf(runway: RunwayResult): 'critical' | 'warning' | 'healthy' | 'idle' {
  if (runway.daysLeft === null) return 'idle'
  if (runway.daysLeft < 1) return 'critical'
  if (runway.daysLeft < 3) return 'warning'
  return 'healthy'
}

const LEVEL_CLASS: Record<
  ReturnType<typeof levelOf>,
  { dot: string; text: string }
> = {
  critical: { dot: 'bg-destructive', text: 'text-destructive' },
  warning: { dot: 'bg-warning', text: 'text-warning' },
  healthy: { dot: 'bg-success', text: 'text-success' },
  idle: { dot: 'bg-muted-foreground', text: 'text-muted-foreground' },
}

export function BalanceRunwayCard() {
  const { t } = useTranslation()
  const user = useAuthStore((state) => state.auth.user)
  const balance = Number(user?.quota ?? 0)

  const timeRange = useMemo(() => {
    const end = Math.floor(Date.now() / 1000)
    return { start_timestamp: end - RUNWAY_DAYS * 86400, end_timestamp: end }
  }, [])
  const tzOffset = useMemo(() => -new Date().getTimezoneOffset(), [])

  const query = useQuery({
    queryKey: [
      'dashboard',
      'self-series',
      timeRange.start_timestamp,
      timeRange.end_timestamp,
      tzOffset,
    ],
    queryFn: async () =>
      requireServerSuccess(
        await getSelfUsageSeries({
          ...timeRange,
          tz_offset_minutes: tzOffset,
        })
      ),
    select: (response) => response.data,
    staleTime: 60_000,
  })

  const runway = useMemo(() => {
    const series = buildDailySeries(query.data?.points, {
      days: RUNWAY_DAYS,
    })
    return computeRunway(balance, series, {
      targetDays: TARGET_COVERAGE_DAYS,
    })
  }, [query.data, balance])

  const loading = query.isLoading
  const level = levelOf(runway)
  const palette = LEVEL_CLASS[level]

  let headline = t('Not enough data yet')
  let detail = t('Keep using the API for a few days to get a projection.')
  if (!loading) {
    if (runway.daysLeft === null) {
      headline = t('No recent spending')
      detail = t('Nothing to predict while usage is zero.')
    } else if (level === 'critical') {
      headline = t('Balance is used up')
      detail = t('Top up to keep requests running.')
    } else {
      headline = t('{{days}} days left', {
        days: Math.floor(runway.daysLeft),
      })
      detail = t('Projected to run out on {{date}}', {
        date: runway.emptyDate ?? '',
      })
    }
  }

  // Rounding happens on the currency value, not on quota units: a top-up of
  // "$180" is useful, "175.25" is not, and quota is 500k units per dollar.
  const topUpQuota = useMemo(() => {
    const { config } = getCurrencyDisplay()
    const unit = config.quotaPerUnit > 0 ? config.quotaPerUnit : 1
    const usd = runway.suggestedTopUp / unit
    return roundTopUp(usd) * unit
  }, [runway.suggestedTopUp])

  let topUpDisplay: ReactNode = '--'
  if (loading) {
    topUpDisplay = <Skeleton className='h-6 w-20' />
  } else if (topUpQuota > 0) {
    topUpDisplay = formatQuota(topUpQuota)
  }

  return (
    <section className='bg-card overflow-hidden rounded-2xl border shadow-xs'>
      <div className='flex items-center gap-2 border-b px-4 py-3 sm:px-5'>
        <span className='bg-primary/10 text-primary flex size-7 items-center justify-center rounded-md'>
          <Wallet className='size-4' />
        </span>
        <h3 className='text-sm font-semibold'>{t('Balance runway')}</h3>
        <span className='text-muted-foreground ml-auto text-xs'>
          {t('Based on the last {{days}} days', { days: AVERAGE_DAYS })}
        </span>
      </div>

      <div className='grid gap-4 p-4 sm:grid-cols-3 sm:p-5'>
        <div className='min-w-0'>
          <div className='text-muted-foreground text-[11px] tracking-wide uppercase'>
            {t('Current balance')}
          </div>
          <div className='mt-1 truncate font-mono text-xl font-bold tabular-nums'>
            {loading ? <Skeleton className='h-6 w-24' /> : formatQuota(balance)}
          </div>
        </div>

        <div className='min-w-0'>
          <div className='text-muted-foreground text-[11px] tracking-wide uppercase'>
            {t('Daily average')}
          </div>
          <div className='mt-1 truncate font-mono text-xl font-bold tabular-nums'>
            {loading ? (
              <Skeleton className='h-6 w-20' />
            ) : (
              formatQuota(runway.dailyAverage)
            )}
          </div>
        </div>

        <div className='min-w-0'>
          <div className='text-muted-foreground text-[11px] tracking-wide uppercase'>
            {t('Suggested top-up')}
          </div>
          <div className='mt-1 truncate font-mono text-xl font-bold tabular-nums'>
            {topUpDisplay}
          </div>
          <div className='text-muted-foreground/70 mt-0.5 text-[11px]'>
            {t('Covers {{days}} days at the current pace', {
              days: TARGET_COVERAGE_DAYS,
            })}
          </div>
        </div>
      </div>

      <div className='bg-muted/30 flex flex-wrap items-center gap-x-4 gap-y-2 border-t px-4 py-3 sm:px-5'>
        <span className={cn('flex items-center gap-1.5 text-xs', palette.text)}>
          <span className={cn('size-2 rounded-full', palette.dot)} />
          <span className='font-medium'>{loading ? '--' : headline}</span>
        </span>
        <span className='text-muted-foreground flex items-center gap-1.5 text-xs'>
          <CalendarClock className='size-3.5' />
          {loading ? '--' : detail}
        </span>
        {runway.accelerating && (
          <span className='text-warning flex items-center gap-1.5 text-xs'>
            <TriangleAlert className='size-3.5' />
            {t('Spending is speeding up')}
          </span>
        )}
        {runway.confidence === 'low' && (
          <span className='text-muted-foreground/70 ml-auto text-[11px]'>
            {t('Low confidence: few days with usage')}
          </span>
        )}
      </div>
    </section>
  )
}
