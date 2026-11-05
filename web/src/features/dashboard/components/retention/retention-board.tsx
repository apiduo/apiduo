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
import { Users, Wallet } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { EmptyState } from '@/components/empty-state'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { getUserBalanceSeries } from '@/features/dashboard/api'
import {
  computeRunway,
  roundTopUp,
  runwayLevel,
  type DailyPoint,
  type RunwayLevel,
  type RunwayResult,
} from '@/features/dashboard/lib/forecast'
import type { UserBalanceItem } from '@/features/dashboard/types'
import { getCurrencyDisplay } from '@/lib/currency'
import { formatNumber, formatQuota } from '@/lib/format'
import { requireServerSuccess } from '@/lib/server-error-message'
import { cn } from '@/lib/utils'

import { TopUpDialog } from './topup-dialog'

const HISTORY_DAYS = 14
const TARGET_COVERAGE_DAYS = 30
const LEVEL_ORDER: RunwayLevel[] = [
  'critical',
  'warning',
  'watch',
  'healthy',
  'idle',
]

const LEVEL_STYLE: Record<RunwayLevel, { dot: string; text: string }> = {
  critical: { dot: 'bg-destructive', text: 'text-destructive' },
  warning: { dot: 'bg-warning', text: 'text-warning' },
  watch: { dot: 'bg-warning/60', text: 'text-warning/80' },
  healthy: { dot: 'bg-success', text: 'text-success' },
  idle: { dot: 'bg-muted-foreground', text: 'text-muted-foreground' },
}

const LEVEL_LABEL: Record<RunwayLevel, string> = {
  critical: 'Urgent',
  warning: 'Warning',
  watch: 'Watch',
  healthy: 'Healthy',
  idle: 'No spending',
}

interface RetentionRow {
  user: UserBalanceItem
  runway: RunwayResult
  level: RunwayLevel
}

function toSeries(daily: number[]): DailyPoint[] {
  // computeRunway only reads the quota values, but a dense array keeps the
  // "last 7 days" slice meaningful.
  return daily.map((quota) => ({ date: '', quota, count: 0, tokens: 0 }))
}

export function RetentionBoard() {
  const { t } = useTranslation()
  const [filter, setFilter] = useState<RunwayLevel | 'all'>('all')
  const [topUpRow, setTopUpRow] = useState<RetentionRow | null>(null)
  const tzOffset = useMemo(() => -new Date().getTimezoneOffset(), [])

  const query = useQuery({
    queryKey: ['dashboard', 'user-balances', HISTORY_DAYS, tzOffset],
    queryFn: async () =>
      requireServerSuccess(
        await getUserBalanceSeries({
          days: HISTORY_DAYS,
          tz_offset_minutes: tzOffset,
        })
      ),
    select: (response) => response.data,
    staleTime: 60_000,
  })

  const rows = useMemo<RetentionRow[]>(() => {
    const users = query.data?.users ?? []
    const mapped = users.map((user) => {
      const runway = computeRunway(user.quota, toSeries(user.daily), {
        targetDays: TARGET_COVERAGE_DAYS,
      })
      return { user, runway, level: runwayLevel(runway) }
    })
    // Most urgent first; customers with no recent spend sink to the bottom.
    mapped.sort((a, b) => {
      const byLevel =
        LEVEL_ORDER.indexOf(a.level) - LEVEL_ORDER.indexOf(b.level)
      if (byLevel !== 0) return byLevel
      const left = a.runway.daysLeft ?? Number.POSITIVE_INFINITY
      const right = b.runway.daysLeft ?? Number.POSITIVE_INFINITY
      return left - right
    })
    return mapped
  }, [query.data])

  const counts = useMemo(() => {
    const result: Record<RunwayLevel, number> = {
      critical: 0,
      warning: 0,
      watch: 0,
      healthy: 0,
      idle: 0,
    }
    for (const row of rows) result[row.level]++
    return result
  }, [rows])

  const totalBalance = useMemo(
    () => rows.reduce((total, row) => total + Number(row.user.quota || 0), 0),
    [rows]
  )

  const { config } = getCurrencyDisplay()
  const quotaPerUnit = config.quotaPerUnit > 0 ? config.quotaPerUnit : 1

  const visibleRows =
    filter === 'all' ? rows : rows.filter((row) => row.level === filter)
  const loading = query.isLoading

  if (query.isError) {
    return (
      <div className='space-y-2 rounded-lg border px-4 py-10 text-center'>
        <div className='text-sm font-medium'>
          {t('Failed to load customer balances')}
        </div>
        <div className='text-muted-foreground text-xs'>
          {(query.error as Error)?.message}
        </div>
        <Button variant='outline' size='sm' onClick={() => query.refetch()}>
          {t('Retry')}
        </Button>
      </div>
    )
  }

  return (
    <div className='space-y-3 sm:space-y-4'>
      <div className='flex flex-wrap items-center gap-2'>
        <div className='text-muted-foreground flex items-center gap-1.5 text-xs'>
          <Wallet className='size-3.5' />
          {t('Total balance across {{count}} customers', {
            count: formatNumber(rows.length),
          })}
          <span className='text-foreground font-mono font-medium'>
            {loading ? '--' : formatQuota(totalBalance)}
          </span>
        </div>
        {filter !== 'all' && (
          <Button
            variant='ghost'
            size='sm'
            className='ml-auto h-7 text-xs'
            onClick={() => setFilter('all')}
          >
            {t('Show all')}
          </Button>
        )}
      </div>

      <div className='overflow-hidden rounded-lg border'>
        <div className='divide-border/60 grid grid-cols-2 divide-x sm:grid-cols-5'>
          {LEVEL_ORDER.map((level) => {
            const active = filter === level
            return (
              <button
                key={level}
                type='button'
                onClick={() => setFilter(active ? 'all' : level)}
                className={cn(
                  'min-w-0 px-3 py-2.5 text-left transition-colors',
                  active ? 'bg-muted/60' : 'hover:bg-muted/30'
                )}
              >
                <div className='flex items-center gap-1.5'>
                  <span
                    className={cn('size-2 rounded-full', LEVEL_STYLE[level].dot)}
                  />
                  <span className='text-muted-foreground truncate text-[10px] font-medium tracking-wide uppercase sm:text-[11px]'>
                    {t(LEVEL_LABEL[level])}
                  </span>
                </div>
                <div className='mt-0.5 font-mono text-base font-bold tabular-nums sm:text-lg'>
                  {loading ? (
                    <Skeleton className='h-5 w-10' />
                  ) : (
                    formatNumber(counts[level])
                  )}
                </div>
              </button>
            )
          })}
        </div>
      </div>

      <div className='overflow-hidden rounded-lg border'>
        <div className='flex flex-wrap items-center justify-between gap-2 border-b px-3 py-2 sm:px-4'>
          <div className='flex items-center gap-2'>
            <Users className='text-muted-foreground size-4' />
            <span className='text-sm font-semibold'>
              {t('Customer balance health')}
            </span>
            <span className='text-muted-foreground text-xs'>
              {t('{{count}} customers', {
                count: formatNumber(visibleRows.length),
              })}
            </span>
          </div>
          <span className='text-muted-foreground text-[11px]'>
            {t('Sorted by urgency, closest to running out first')}
          </span>
        </div>

        {loading && (
          <div className='space-y-2 p-4'>
            {Array.from({ length: 6 }, (_, index) => (
              <Skeleton key={index} className='h-10 w-full' />
            ))}
          </div>
        )}
        {!loading && visibleRows.length === 0 && (
          <EmptyState
            icon={Users}
            title={t('No Data')}
            description={t('No customers with usage in this window.')}
            className='border-0'
          />
        )}
        {!loading && visibleRows.length > 0 && (
          <div className='overflow-x-auto'>
            <div className='w-full min-w-[980px]'>
              <table className='w-full text-sm'>
                <thead>
                  <tr className='border-b'>
                    <th className='text-muted-foreground px-3 py-2 text-left text-[11px] font-medium'>
                      {t('Customer')}
                    </th>
                    <th className='text-muted-foreground px-3 py-2 text-left text-[11px] font-medium'>
                      {t('Group')}
                    </th>
                    <th className='text-muted-foreground px-3 py-2 text-right text-[11px] font-medium'>
                      {t('Balance')}
                    </th>
                    <th className='text-muted-foreground px-3 py-2 text-right text-[11px] font-medium'>
                      {t('Daily spend')}
                    </th>
                    <th className='text-muted-foreground px-3 py-2 text-right text-[11px] font-medium'>
                      {t('Days left')}
                    </th>
                    <th className='text-muted-foreground px-3 py-2 text-right text-[11px] font-medium'>
                      {t('Runs out')}
                    </th>
                    <th className='text-muted-foreground px-3 py-2 text-right text-[11px] font-medium'>
                      {t('Suggested top-up')}
                    </th>
                    <th className='px-3 py-2'>
                      <span className='sr-only'>{t('Actions')}</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {visibleRows.map((row) => {
                    const style = LEVEL_STYLE[row.level]
                    const topUp =
                      roundTopUp(row.runway.suggestedTopUp / quotaPerUnit) *
                      quotaPerUnit
                    let daysLabel = '--'
                    if (row.runway.daysLeft !== null) {
                      daysLabel =
                        row.runway.daysLeft < 1
                          ? t('Less than a day')
                          : formatNumber(Math.floor(row.runway.daysLeft))
                    }
                    return (
                      <tr
                        key={row.user.user_id}
                        className='border-b last:border-0'
                      >
                        <td className='max-w-[220px] px-3 py-2'>
                          <div className='flex items-center gap-2'>
                            <span
                              className={cn(
                                'size-1.5 shrink-0 rounded-full',
                                style.dot
                              )}
                            />
                            <span
                              className='truncate font-medium'
                              title={row.user.username}
                            >
                              {row.user.username || `#${row.user.user_id}`}
                            </span>
                          </div>
                        </td>
                        <td className='text-muted-foreground px-3 py-2'>
                          {row.user.group || '--'}
                        </td>
                        <td className='px-3 py-2 text-right font-mono tabular-nums'>
                          {formatQuota(row.user.quota)}
                        </td>
                        <td className='text-muted-foreground px-3 py-2 text-right font-mono tabular-nums'>
                          {formatQuota(row.runway.dailyAverage)}
                        </td>
                        <td
                          className={cn(
                            'px-3 py-2 text-right font-mono tabular-nums',
                            style.text
                          )}
                        >
                          {daysLabel}
                        </td>
                        <td className='text-muted-foreground px-3 py-2 text-right font-mono text-xs tabular-nums'>
                          {row.runway.emptyDate ?? '--'}
                        </td>
                        <td className='px-3 py-2 text-right font-mono font-semibold tabular-nums'>
                          {topUp > 0 ? formatQuota(topUp) : '--'}
                        </td>
                        <td className='px-3 py-2 text-right'>
                          <Button
                            variant='outline'
                            size='sm'
                            className='h-7 px-2 text-xs'
                            aria-label={t('Top up {{name}}', {
                              name:
                                row.user.username || `#${row.user.user_id}`,
                            })}
                            onClick={() => setTopUpRow(row)}
                          >
                            {t('Top up')}
                          </Button>
                        </td>
                      </tr>
                    )
                  })}
                </tbody>
              </table>
            </div>
          </div>
        )}
      </div>

      {topUpRow && (
        <TopUpDialog
          key={topUpRow.user.user_id}
          open
          onOpenChange={(open) => {
            if (!open) setTopUpRow(null)
          }}
          user={topUpRow.user}
          runway={topUpRow.runway}
          onSuccess={() => query.refetch()}
        />
      )}
    </div>
  )
}
