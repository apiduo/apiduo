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
import {
  ArrowDown,
  ArrowUp,
  ChevronsUpDown,
  Download,
  KeyRound,
  Search,
} from 'lucide-react'
import { useCallback, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { EmptyState } from '@/components/empty-state'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { getSelfSkQuotaData, getSkQuotaData } from '@/features/dashboard/api'
import { ApiduoGlow } from '@/features/apiduo-appearance'
import { TIME_RANGE_PRESETS } from '@/features/dashboard/constants'
import {
  buildSkStackRows,
  filterSkRows,
  quotaShare,
  skMask,
  skName,
  skRowsToCsv,
  sortSkRows,
  summarizeSkData,
  type SkSortDirection,
  type SkSortKey,
  type SkStackData,
} from '@/features/dashboard/lib/sk'
import type { SkQuotaDataItem } from '@/features/dashboard/types'
import { getCurrencyDisplay } from '@/lib/currency'
import { formatNumber, formatQuota } from '@/lib/format'
import { requireServerSuccess } from '@/lib/server-error-message'
import { getRollingDateRange } from '@/lib/time'
import { cn } from '@/lib/utils'

const PAGE_SIZE = 8
const DEFAULT_RANGE_DAYS = 7

function SortHeader(props: {
  label: string
  sortKey: SkSortKey
  activeKey: SkSortKey
  direction: SkSortDirection
  onToggle: (key: SkSortKey) => void
  align?: 'left' | 'right'
}) {
  const active = props.activeKey === props.sortKey
  let Icon = ChevronsUpDown
  if (active) Icon = props.direction === 'desc' ? ArrowDown : ArrowUp
  return (
    <button
      type='button'
      onClick={() => props.onToggle(props.sortKey)}
      className={cn(
        'inline-flex w-full items-center gap-1 text-inherit transition-colors hover:text-foreground',
        props.align === 'right' && 'justify-end'
      )}
    >
      <span>{props.label}</span>
      <Icon className={cn('size-3.5 shrink-0', !active && 'opacity-50')} />
    </button>
  )
}

function SkStackChart(props: {
  data: SkStackData
  loading: boolean
  colors: Map<string, string>
  title: string
}) {
  const { t } = useTranslation()

  if (props.loading) {
    return (
      <div className='space-y-2 rounded-lg border p-3 sm:p-4'>
        {Array.from({ length: 5 }, (_, index) => (
          <Skeleton key={index} className='h-6 w-full' />
        ))}
      </div>
    )
  }

  if (props.data.rows.length === 0) {
    return (
      <div className='text-muted-foreground rounded-lg border p-6 text-center text-xs'>
        {t('No Data')}
      </div>
    )
  }

  return (
    <div className='rounded-lg border p-3 sm:p-4'>
      <div className='flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1'>
        <span className='text-sm font-semibold'>{props.title}</span>
        <span className='text-muted-foreground text-[11px]'>
          {t('Bar length is cost, segments are models')}
        </span>
      </div>

      <div className='mt-2 flex flex-wrap gap-x-3 gap-y-1'>
        {props.data.models.map((model) => (
          <span
            key={model}
            className='text-muted-foreground flex items-center gap-1 text-[11px]'
          >
            <span
              className='size-2 shrink-0 rounded-sm'
              style={{ background: props.colors.get(model) }}
            />
            {model || t('Unknown model')}
          </span>
        ))}
      </div>

      <div className='mt-3 space-y-2'>
        {props.data.rows.map((row) => (
          <div key={row.key} className='flex items-center gap-2 sm:gap-3'>
            <div className='w-24 min-w-0 shrink-0 sm:w-40'>
              <div
                className='truncate text-xs font-medium'
                title={row.label || t('No token')}
              >
                {row.label || t('No token')}
              </div>
              <div className='text-muted-foreground truncate font-mono text-[10px]'>
                {row.mask}
              </div>
            </div>
            {/* The outer width carries the SK's share of the whole range, so a
                bar twice as wide really is twice the money. Segments inside
                then split that width by model. */}
            <div className='h-4 min-w-0 flex-1'>
              <div
                className='flex h-full gap-0.5 overflow-hidden rounded-sm'
                style={{ width: `${Math.max(1, row.share)}%` }}
              >
                {row.segments.map((segment) => (
                  <div
                    key={segment.model}
                    className='h-full first:rounded-l-sm last:rounded-r-sm'
                    style={{
                      width: `${segment.share}%`,
                      background: props.colors.get(segment.model),
                    }}
                    title={`${segment.model || t('Unknown model')} · ${formatQuota(segment.quota)} · ${segment.share.toFixed(1)}%`}
                  />
                ))}
              </div>
            </div>
            <div className='w-14 shrink-0 text-right font-mono text-xs font-semibold tabular-nums sm:w-16'>
              {formatQuota(row.total)}
            </div>
          </div>
        ))}
      </div>

      {props.data.hiddenSkCount > 0 && (
        <div className='text-muted-foreground mt-2 border-t pt-2 text-[11px]'>
          {t('And {{count}} more SKs, {{cost}} total', {
            count: props.data.hiddenSkCount,
            cost: formatQuota(props.data.hiddenQuota),
          })}
        </div>
      )}
    </div>
  )
}

interface SkAnalyticsProps {
  /**
   * admin aggregates every user; self is hard-scoped server-side to the caller's
   * own keys, which is what a reseller needs (their customers are the keys).
   */
  scope?: 'admin' | 'self'
}

export function SkAnalytics({ scope = 'admin' }: SkAnalyticsProps = {}) {
  const { t } = useTranslation()
  const isSelf = scope === 'self'
  const [rangeDays, setRangeDays] = useState(DEFAULT_RANGE_DAYS)
  const [search, setSearch] = useState('')
  const [page, setPage] = useState(1)
  const [sort, setSort] = useState<{
    key: SkSortKey
    direction: SkSortDirection
  }>({ key: 'quota', direction: 'desc' })

  const timeRange = useMemo(() => {
    const { start, end } = getRollingDateRange(rangeDays)
    return {
      start_timestamp: Math.floor(start.getTime() / 1000),
      end_timestamp: Math.floor(end.getTime() / 1000),
    }
  }, [rangeDays])

  const query = useQuery({
    queryKey: [
      'dashboard',
      'sk-analytics',
      scope,
      timeRange.start_timestamp,
      timeRange.end_timestamp,
    ],
    queryFn: async () =>
      requireServerSuccess(
        isSelf
          ? await getSelfSkQuotaData(timeRange)
          : await getSkQuotaData(timeRange)
      ),
    select: (response) => response.data,
    staleTime: 60_000,
  })

  const items = useMemo(() => query.data?.items ?? [], [query.data])
  const totals = query.data?.totals
  const loading = query.isLoading
  const summary = useMemo(() => summarizeSkData(items, totals), [items, totals])

  // The stack chart follows the search box too: leaving it on the full set
  // would show SKs the table below has already filtered out.
  const filteredItems = useMemo(
    () => filterSkRows(items, search),
    [items, search]
  )

  const otherLabel = t('Other')
  const stackData = useMemo(
    () =>
      buildSkStackRows(filteredItems, {
        maxRows: 5,
        maxSegments: 4,
        otherLabel,
      }),
    [filteredItems, otherLabel]
  )
  // Colours come from the theme's chart ramp so the chart follows the active
  // preset instead of hardcoding a palette.
  const stackColors = useMemo(() => {
    const palette = [
      'var(--chart-1)',
      'var(--chart-2)',
      'var(--chart-3)',
      'var(--chart-4)',
      'var(--chart-5)',
    ]
    const colors = new Map<string, string>()
    stackData.models.forEach((model, index) => {
      colors.set(
        model,
        model === otherLabel
          ? 'var(--muted-foreground)'
          : palette[index % palette.length]
      )
    })
    return colors
  }, [stackData.models, otherLabel])

  const visibleRows = useMemo(
    () => sortSkRows(filteredItems, sort.key, sort.direction),
    [filteredItems, sort]
  )
  const pageCount = Math.max(1, Math.ceil(visibleRows.length / PAGE_SIZE))
  const currentPage = Math.min(page, pageCount)
  const pageRows = visibleRows.slice(
    (currentPage - 1) * PAGE_SIZE,
    currentPage * PAGE_SIZE
  )

  const toggleSort = useCallback((key: SkSortKey) => {
    setPage(1)
    setSort((prev) => {
      if (prev.key === key) {
        return { key, direction: prev.direction === 'desc' ? 'asc' : 'desc' }
      }
      const textKey = key === 'sk' || key === 'user' || key === 'model'
      return { key, direction: textKey ? 'asc' : 'desc' }
    })
  }, [])

  const handleSearch = useCallback((value: string) => {
    setSearch(value)
    setPage(1)
  }, [])

  const handleRangeChange = useCallback((value: string) => {
    setRangeDays(Number(value))
    setPage(1)
  }, [])

  const handleExport = useCallback(() => {
    const { config } = getCurrencyDisplay()
    const csv = skRowsToCsv(visibleRows, config.quotaPerUnit)
    const blob = new Blob([`\ufeff${csv}`], {
      type: 'text/csv;charset=utf-8',
    })
    const url = URL.createObjectURL(blob)
    const anchor = document.createElement('a')
    anchor.href = url
    anchor.download = `${isSelf ? 'key-usage' : 'sk-analytics'}-${rangeDays}d-${new Date()
      .toISOString()
      .slice(0, 10)}.csv`
    anchor.click()
    URL.revokeObjectURL(url)
  }, [visibleRows, rangeDays, isSelf])

  const noTokenLabel = t('No token')
  const columns = [
    {
      id: 'sk',
      header: (
        <SortHeader
          label={isSelf ? t('Key') : t('SK')}
          sortKey='sk'
          activeKey={sort.key}
          direction={sort.direction}
          onToggle={toggleSort}
        />
      ),
      cell: (row: SkQuotaDataItem) => (
        <div className='min-w-0'>
          <div className='truncate text-sm font-medium' title={skName(row)}>
            {skName(row) || noTokenLabel}
          </div>
          <div className='text-muted-foreground truncate font-mono text-[11px]'>
            {skMask(row) || t('Usage recorded without a token')}
          </div>
        </div>
      ),
    },
    {
      id: 'user',
      header: (
        <SortHeader
          label={t('User')}
          sortKey='user'
          activeKey={sort.key}
          direction={sort.direction}
          onToggle={toggleSort}
        />
      ),
      cell: (row: SkQuotaDataItem) => (
        <div className='min-w-0'>
          <div className='truncate text-sm' title={row.username}>
            {row.username || '--'}
          </div>
          <div className='text-muted-foreground text-[11px]'>
            #{row.user_id}
          </div>
        </div>
      ),
    },
    {
      id: 'model',
      header: (
        <SortHeader
          label={t('Model')}
          sortKey='model'
          activeKey={sort.key}
          direction={sort.direction}
          onToggle={toggleSort}
        />
      ),
      cell: (row: SkQuotaDataItem) => (
        <span className='truncate text-sm' title={row.model_name}>
          {row.model_name || '--'}
        </span>
      ),
    },
    {
      id: 'count',
      header: (
        <SortHeader
          label={t('Requests')}
          sortKey='count'
          activeKey={sort.key}
          direction={sort.direction}
          onToggle={toggleSort}
          align='right'
        />
      ),
      cell: (row: SkQuotaDataItem) => (
        <div className='text-right font-mono text-sm tabular-nums'>
          {formatNumber(row.count)}
        </div>
      ),
    },
    {
      id: 'tokens',
      header: (
        <SortHeader
          label={t('Tokens')}
          sortKey='token_used'
          activeKey={sort.key}
          direction={sort.direction}
          onToggle={toggleSort}
          align='right'
        />
      ),
      cell: (row: SkQuotaDataItem) => (
        <div className='text-right font-mono text-sm tabular-nums'>
          {formatNumber(row.token_used)}
        </div>
      ),
    },
    {
      id: 'quota',
      header: (
        <SortHeader
          label={t('Cost')}
          sortKey='quota'
          activeKey={sort.key}
          direction={sort.direction}
          onToggle={toggleSort}
          align='right'
        />
      ),
      cell: (row: SkQuotaDataItem) => (
        <div className='text-right font-mono text-sm font-semibold tabular-nums'>
          {formatQuota(row.quota)}
        </div>
      ),
    },
    {
      id: 'share',
      header: <div className='text-right'>{t('Share')}</div>,
      cell: (row: SkQuotaDataItem) => {
        const share = quotaShare(row.quota, summary.quota)
        return (
          <div className='flex items-center justify-end gap-2'>
            <div className='bg-foreground/10 h-1.5 w-24 overflow-hidden rounded-full'>
              <div
                className='bg-primary h-full rounded-full'
                style={{ width: `${Math.min(100, share)}%` }}
              />
            </div>
            <span className='text-muted-foreground w-12 text-right font-mono text-[11px] tabular-nums'>
              {share.toFixed(1)}%
            </span>
          </div>
        )
      },
    },
  ].filter((column) => !isSelf || column.id !== 'user')

  const searchPlaceholder = isSelf
    ? t('Filter by key or model')
    : t('Filter by SK, user or model')
  let emptyTitle = t('No Data')
  if (search) emptyTitle = t('No matching combinations')

  let emptyDescription = t('No usage was recorded in this time range.')
  if (search) {
    emptyDescription = isSelf
      ? t('Try a different key or model.')
      : t('Try a different SK, user or model.')
  }

  let tableBody
  if (query.isError) {
    tableBody = (
      <div className='space-y-2 px-4 py-10 text-center'>
        <div className='text-sm font-medium'>
          {isSelf
            ? t('Failed to load key usage')
            : t('Failed to load SK analytics')}
        </div>
        <div className='text-muted-foreground text-xs'>
          {(query.error as Error)?.message}
        </div>
        <Button variant='outline' size='sm' onClick={() => query.refetch()}>
          {t('Retry')}
        </Button>
      </div>
    )
  } else if (loading) {
    tableBody = (
      <div className='space-y-2 p-4'>
        {Array.from({ length: 6 }, (_, index) => (
          <Skeleton key={index} className='h-10 w-full' />
        ))}
      </div>
    )
  } else if (pageRows.length === 0) {
    tableBody = (
      <EmptyState
        icon={KeyRound}
        title={emptyTitle}
        description={emptyDescription}
        className='border-0'
      />
    )
  } else {
    // Eight rows plus the header fit inside the cap, so the page stays about
    // one screen tall. The cap is a safety net: it stops a future row-height
    // change from stretching the page again. A sticky header is not an option
    // here, because the Table primitive wraps itself in an overflow-hidden div
    // that becomes the sticky containing block.
    tableBody = (
      <div className='max-h-[500px] overflow-auto'>
        <div className='min-w-[880px]'>
          <StaticDataTable
            columns={columns}
            data={pageRows}
            getRowKey={(row) =>
              `${row.token_id}:${row.user_id}:${row.model_name}`
            }
            tableClassName='text-sm'
          />
        </div>
      </div>
    )
  }

  // Four metrics keep the 2x2 mobile grid balanced; the user count already has
  // its own ranking panel below.
  const metrics = [
    { key: 'cost', label: t('Total cost'), value: formatQuota(summary.quota) },
    {
      key: 'requests',
      label: t('Requests'),
      value: formatNumber(summary.count),
    },
    {
      key: 'tokens',
      label: t('Tokens'),
      value: formatNumber(summary.tokens),
    },
    {
      key: 'sk',
      label: isSelf ? t('Active keys') : t('Active SK'),
      value: formatNumber(summary.skCount),
    },
  ]

  return (
    <div className='space-y-3 sm:space-y-4'>
      <div className='flex flex-wrap items-center gap-1.5 sm:gap-2'>
        <Tabs value={String(rangeDays)} onValueChange={handleRangeChange}>
          <TabsList>
            {TIME_RANGE_PRESETS.map((preset) => (
              <TabsTrigger
                key={preset.days}
                value={String(preset.days)}
                className='px-2.5 text-xs'
              >
                {t(preset.label)}
              </TabsTrigger>
            ))}
          </TabsList>
        </Tabs>

        <div className='relative ml-auto w-full sm:w-64'>
          <Search className='text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2' />
          <Input
            value={search}
            onChange={(event) => handleSearch(event.target.value)}
            placeholder={searchPlaceholder}
            className='h-8 pl-8 text-xs'
            aria-label={searchPlaceholder}
          />
        </div>

        <Button
          variant='outline'
          size='sm'
          onClick={handleExport}
          disabled={visibleRows.length === 0}
        >
          <Download className='mr-1.5 size-3.5' />
          {t('Export CSV')}
        </Button>
      </div>

      {isSelf && (
        <p className='text-muted-foreground text-[11px]'>
          {t(
            'Each key stands for one customer. Rename keys in API Keys to match your customer names.'
          )}
        </p>
      )}

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
            </div>
          ))}
        </div>
      </div>

      <SkStackChart
        data={stackData}
        loading={loading}
        colors={stackColors}
        title={isSelf ? t('Key spend by model') : t('SK spend by model')}
      />

      <div className='overflow-hidden rounded-lg border'>
        <div className='flex flex-wrap items-center justify-between gap-2 border-b px-3 py-2 sm:px-4'>
          <div className='flex items-center gap-2'>
            <KeyRound className='text-muted-foreground size-4' />
            <span className='text-sm font-semibold'>
              {isSelf ? t('Key × model') : t('SK × model × user')}
            </span>
            <span className='text-muted-foreground text-xs'>
              {t('{{count}} combinations', {
                count: formatNumber(visibleRows.length),
              })}
            </span>
          </div>
          {query.data?.truncated && (
            <span className='text-warning text-[11px]'>
              {t('Showing the top {{limit}} combinations by cost', {
                limit: formatNumber(query.data.limit),
              })}
            </span>
          )}
        </div>

        {tableBody}

        {visibleRows.length > PAGE_SIZE && (
          <div className='flex items-center justify-between border-t px-3 py-2 text-xs sm:px-4'>
            <span className='text-muted-foreground'>
              {t('Page {{page}} of {{total}}', {
                page: currentPage,
                total: pageCount,
              })}
            </span>
            <div className='flex items-center gap-1.5'>
              <Button
                variant='outline'
                size='sm'
                disabled={currentPage <= 1}
                onClick={() => setPage(currentPage - 1)}
              >
                {t('Previous')}
              </Button>
              <Button
                variant='outline'
                size='sm'
                disabled={currentPage >= pageCount}
                onClick={() => setPage(currentPage + 1)}
              >
                {t('Next')}
              </Button>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}
