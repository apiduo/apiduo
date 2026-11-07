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
import { ArrowDown, ArrowRight, ArrowUp, Gauge, TrendingUp } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { EmptyState } from '@/components/empty-state'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { getUsageSeries } from '@/features/dashboard/api'
import { ApiduoGlow } from '@/features/apiduo-appearance'
import {
  buildDailySeries,
  forecastUsage,
  type DailyPoint,
  type ForecastResult,
} from '@/features/dashboard/lib/forecast'
import { formatNumber, formatQuota } from '@/lib/format'
import { requireServerSuccess } from '@/lib/server-error-message'
import { cn } from '@/lib/utils'

const HISTORY_DAYS = 14
const HORIZON_OPTIONS = [7, 14] as const
const DEFAULT_HORIZON = 7
const MAX_ROWS = 12

interface ForecastRow {
  label: string
  series: DailyPoint[]
  forecast: ForecastResult
  last7: number
  last14: number
}

function sum(values: number[]): number {
  return values.reduce((total, value) => total + value, 0)
}

function trendIcon(trend: ForecastResult['trend']) {
  if (trend === 'up') return ArrowUp
  if (trend === 'down') return ArrowDown
  return ArrowRight
}

function trendClass(trend: ForecastResult['trend']): string {
  if (trend === 'up') return 'text-warning'
  if (trend === 'down') return 'text-success'
  return 'text-muted-foreground'
}

type Advice = 'prepare' | 'declining' | 'stable' | 'insufficient'

// Advice is an enum, not a localised string: comparing translated text would
// silently break as soon as the operator switches language.
const ADVICE_LABEL: Record<Advice, string> = {
  prepare: 'Prepare capacity',
  declining: 'Usage declining',
  stable: 'Stable',
  insufficient: 'Insufficient samples',
}

const ADVICE_CLASS: Record<Advice, string> = {
  prepare: 'text-warning',
  declining: 'text-success',
  stable: 'text-muted-foreground',
  insufficient: 'text-muted-foreground/70',
}

function adviceOf(row: ForecastRow): Advice {
  const { forecast } = row
  if (forecast.confidence === 'low' || forecast.confidence === 'insufficient') {
    return 'insufficient'
  }
  if (forecast.trend === 'up' && forecast.trendPct > 20) return 'prepare'
  if (forecast.trend === 'down' && forecast.trendPct < -20) return 'declining'
  return 'stable'
}

export function CapacityPlanner() {
  const { t } = useTranslation()
  const [horizon, setHorizon] = useState<number>(DEFAULT_HORIZON)
  const [dimension, setDimension] = useState<'model' | 'channel'>('model')

  const timeRange = useMemo(() => {
    const end = Math.floor(Date.now() / 1000)
    const start = end - HISTORY_DAYS * 86400
    return { start_timestamp: start, end_timestamp: end }
  }, [])

  const tzOffset = useMemo(() => -new Date().getTimezoneOffset(), [])

  const query = useQuery({
    queryKey: [
      'dashboard',
      'capacity',
      timeRange.start_timestamp,
      timeRange.end_timestamp,
      tzOffset,
      dimension,
    ],
    queryFn: async () =>
      requireServerSuccess(
        await getUsageSeries({
          ...timeRange,
          dimension,
          tz_offset_minutes: tzOffset,
        })
      ),
    select: (response) => response.data.groups,
    staleTime: 5 * 60_000,
  })

  const rows = useMemo<ForecastRow[]>(() => {
    const groups = query.data ?? []
    return groups
      .map((group) => {
        const series = buildDailySeries(group.points, { days: HISTORY_DAYS })
        const forecast = forecastUsage(series, { horizonDays: horizon })
        const values = series.map((point) => point.quota)
        return {
          label: group.label || group.key || t('Unknown model'),
          series,
          forecast,
          last7: sum(values.slice(-7)),
          last14: sum(values),
        }
      })
      .filter((row) => row.last14 > 0)
      .sort((a, b) => b.forecast.predictedTotal - a.forecast.predictedTotal)
  }, [query.data, horizon, t])

  const totals = useMemo(() => {
    const projected = rows.reduce(
      (total, row) => total + row.forecast.predictedTotal,
      0
    )
    const lower = rows.reduce((total, row) => total + row.forecast.lowerTotal, 0)
    const upper = rows.reduce((total, row) => total + row.forecast.upperTotal, 0)
    const last7 = rows.reduce((total, row) => total + row.last7, 0)
    // Compare like with like: both sides are a 7-day window.
    const baseline7 = last7
    const projected7 = projected * (7 / Math.max(1, horizon))
    const changePct =
      baseline7 > 0 ? ((projected7 - baseline7) / baseline7) * 100 : 0
    const growing = rows.filter(
      (row) =>
        row.forecast.trend === 'up' &&
        row.forecast.confidence !== 'insufficient' &&
        row.forecast.predictedTotal > projected * 0.02
    ).length
    const weak = rows.filter(
      (row) => row.forecast.confidence === 'low' || row.forecast.confidence === 'insufficient'
    ).length
    return { projected, lower, upper, last7, changePct, growing, weak }
  }, [rows, horizon])

  const visibleRows = rows.slice(0, MAX_ROWS)
  const hiddenRows = rows.slice(MAX_ROWS)
  const hiddenProjected = hiddenRows.reduce(
    (total, row) => total + row.forecast.predictedTotal,
    0
  )

  const loading = query.isLoading
  const metrics = [
    {
      key: 'projected',
      label: t('Projected spend · next {{days}} days', { days: horizon }),
      value: formatQuota(totals.projected),
      hint: `${formatQuota(totals.lower)} ~ ${formatQuota(totals.upper)}`,
    },
    {
      key: 'actual',
      label: t('Actual · last 7 days'),
      value: formatQuota(totals.last7),
      hint: t('Baseline for the comparison'),
    },
    {
      key: 'change',
      label: t('Change vs last 7 days'),
      value: `${totals.changePct >= 0 ? '+' : ''}${totals.changePct.toFixed(1)}%`,
      hint: t('Projection normalised to 7 days'),
    },
    {
      key: 'models',
      label:
        dimension === 'channel' ? t('Growing channels') : t('Growing models'),
      value: formatNumber(totals.growing),
      hint: t('{{count}} with weak samples', { count: totals.weak }),
    },
  ]

  if (query.isError) {
    return (
      <div className='space-y-2 rounded-lg border px-4 py-10 text-center'>
        <div className='text-sm font-medium'>
          {t('Failed to load capacity data')}
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
        <Tabs
          value={dimension}
          onValueChange={(value) =>
            setDimension(value as 'model' | 'channel')
          }
        >
          <TabsList>
            <TabsTrigger value='model' className='px-2.5 text-xs'>
              {t('By model')}
            </TabsTrigger>
            <TabsTrigger value='channel' className='px-2.5 text-xs'>
              {t('By channel')}
            </TabsTrigger>
          </TabsList>
        </Tabs>
        <div className='text-muted-foreground hidden items-center gap-1.5 text-xs sm:flex'>
          <Gauge className='size-3.5' />
          {t('History window: {{days}} days', { days: HISTORY_DAYS })}
        </div>
        <div className='ml-auto flex items-center gap-2'>
          <span className='text-muted-foreground text-xs'>
            {t('Forecast horizon')}
          </span>
          <Tabs
            value={String(horizon)}
            onValueChange={(value) => setHorizon(Number(value))}
          >
            <TabsList>
              {HORIZON_OPTIONS.map((days) => (
                <TabsTrigger
                  key={days}
                  value={String(days)}
                  className='px-2.5 text-xs'
                >
                  {t('{{days}} days', { days })}
                </TabsTrigger>
              ))}
            </TabsList>
          </Tabs>
        </div>
      </div>

      <div className='overflow-hidden rounded-lg border' data-ap-surface=''>
        <ApiduoGlow />
        <div className='divide-border/60 grid grid-cols-2 divide-x sm:grid-cols-4'>
          {metrics.map((metric) => (
            <div key={metric.key} className='min-w-0 px-3 py-2.5'>
              <div className='text-muted-foreground text-[10px] font-medium tracking-wide uppercase sm:text-[11px]'>
                {metric.label}
              </div>
              <div className='mt-0.5 truncate font-mono text-base font-bold tabular-nums sm:text-lg'>
                {loading ? <Skeleton className='h-5 w-20' /> : metric.value}
              </div>
              <div className='text-muted-foreground/70 mt-0.5 hidden truncate text-[11px] sm:block'>
                {loading ? '' : metric.hint}
              </div>
            </div>
          ))}
        </div>
      </div>

      <div className='overflow-hidden rounded-lg border'>
        <div className='flex flex-wrap items-center justify-between gap-2 border-b px-3 py-2 sm:px-4'>
          <div className='flex items-center gap-2'>
            <TrendingUp className='text-muted-foreground size-4' />
            <span className='text-sm font-semibold'>
              {dimension === 'channel'
                ? t('Channel capacity plan')
                : t('Model capacity plan')}
            </span>
            <span className='text-muted-foreground text-xs'>
              {dimension === 'channel'
                ? t('{{count}} channels with usage', {
                    count: formatNumber(rows.length),
                  })
                : t('{{count}} models with usage', {
                    count: formatNumber(rows.length),
                  })}
            </span>
          </div>
          <span className='text-muted-foreground text-[11px]'>
            {t('Linear fit over the history window, 95% band')}
          </span>
        </div>

        {loading && (
          <div className='space-y-2 p-4'>
            {Array.from({ length: 6 }, (_, index) => (
              <Skeleton key={index} className='h-10 w-full' />
            ))}
          </div>
        )}
        {!loading && rows.length === 0 && (
          <EmptyState
            icon={Gauge}
            title={t('No Data')}
            description={t('No usage was recorded in this time range.')}
            className='border-0'
          />
        )}
        {!loading && rows.length > 0 && (
          <div className='overflow-x-auto'>
            <div className='w-full min-w-[900px]'>
              <table className='w-full text-sm'>
                <thead>
                  <tr className='border-b'>
                    <th className='text-muted-foreground px-3 py-2 text-left text-[11px] font-medium'>
                      {dimension === 'channel' ? t('Channel') : t('Model')}
                    </th>
                    <th className='text-muted-foreground px-3 py-2 text-right text-[11px] font-medium'>
                      {t('Last 7 days')}
                    </th>
                    <th className='text-muted-foreground px-3 py-2 text-right text-[11px] font-medium'>
                      {t('Daily average')}
                    </th>
                    <th className='text-muted-foreground px-3 py-2 text-left text-[11px] font-medium'>
                      {t('Trend')}
                    </th>
                    <th className='text-muted-foreground px-3 py-2 text-right text-[11px] font-medium'>
                      {t('Next {{days}} days', { days: horizon })}
                    </th>
                    <th className='text-muted-foreground px-3 py-2 text-right text-[11px] font-medium'>
                      {t('95% range')}
                    </th>
                    <th className='text-muted-foreground px-3 py-2 text-left text-[11px] font-medium'>
                      {t('Recommendation')}
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {visibleRows.map((row) => {
                    const Icon = trendIcon(row.forecast.trend)
                    const advice = adviceOf(row)
                    return (
                      <tr key={row.label} className='border-b last:border-0'>
                        <td className='max-w-[220px] truncate px-3 py-2 font-medium' title={row.label}>
                          {row.label}
                        </td>
                        <td className='px-3 py-2 text-right font-mono tabular-nums'>
                          {formatQuota(row.last7)}
                        </td>
                        <td className='text-muted-foreground px-3 py-2 text-right font-mono tabular-nums'>
                          {formatQuota(row.forecast.mean7)}
                        </td>
                        <td className='px-3 py-2'>
                          <span
                            className={cn(
                              'inline-flex items-center gap-1 font-mono text-xs',
                              trendClass(row.forecast.trend)
                            )}
                          >
                            <Icon className='size-3.5' />
                            {row.forecast.trendPct >= 0 ? '+' : ''}
                            {row.forecast.trendPct.toFixed(0)}%
                          </span>
                        </td>
                        <td className='px-3 py-2 text-right font-mono font-semibold tabular-nums'>
                          {formatQuota(row.forecast.predictedTotal)}
                        </td>
                        <td className='text-muted-foreground px-3 py-2 text-right font-mono text-xs tabular-nums'>
                          {formatQuota(row.forecast.lowerTotal)} ~{' '}
                          {formatQuota(row.forecast.upperTotal)}
                        </td>
                        <td className='px-3 py-2'>
                          <span
                            className={cn(
                              'text-xs',
                              ADVICE_CLASS[advice]
                            )}
                          >
                            {t(ADVICE_LABEL[advice])}
                          </span>
                        </td>
                      </tr>
                    )
                  })}
                </tbody>
              </table>
            </div>
          </div>
        )}

        {hiddenRows.length > 0 && (
          <div className='text-muted-foreground border-t px-3 py-2 text-[11px] sm:px-4'>
            {dimension === 'channel'
              ? t('And {{count}} more channels, {{cost}} projected', {
                  count: hiddenRows.length,
                  cost: formatQuota(hiddenProjected),
                })
              : t('And {{count}} more models, {{cost}} projected', {
                  count: hiddenRows.length,
                  cost: formatQuota(hiddenProjected),
                })}
          </div>
        )}
      </div>
    </div>
  )
}
