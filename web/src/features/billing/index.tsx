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
import { useEffect, useMemo, useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Download, Receipt } from 'lucide-react'
import { toast } from 'sonner'

import {
  StaticDataTable,
  type StaticDataTableColumn,
} from '@/components/data-table/static/static-data-table'
import { EmptyState } from '@/components/empty-state'
import { SectionPageLayout } from '@/components/layout'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { formatTimestampToDate, formatTokens } from '@/lib/format'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import {
  getBillingExport,
  getBillingLines,
  getBillingStatement,
  listBillingStatements,
} from './api'
import {
  billingLineTypeKey,
  currentBillingPeriod,
  formatBillingPeriod,
  formatFileSize,
  formatMoney,
  recentBillingPeriods,
} from './lib/format'
import type {
  BillingExportReply,
  BillingLine,
  BillingScope,
  BillingSearch,
  BillingSummaryItem,
  BillingTab,
} from './types'

type BillingProps = {
  search: BillingSearch
  onSearchChange: (next: BillingSearch) => void
}

/**
 * 月度账单页：一个账期一件事。
 *
 * 页面不做「全部账期」与任意区间：账期用下拉切换，区间流水属于用量日志页。
 * 汇总与明细是同一批数的两种读法，所以下钻只是切页签加筛选，不是另一次查询口径。
 */
export function Billing({ search, onSearchChange }: BillingProps) {
  const { t } = useTranslation()
  const auth = useAuthStore((state) => state.auth)
  const isAdmin = (auth?.user?.role ?? 0) >= ROLE.ADMIN

  const period = search.period || currentBillingPeriod()
  // 白名单兜底，不用 `??`：URL 缺失、空串、非法值都必须落到安全默认。
  // 这里踩过一次 —— scope 落成空串后 `enabled: scope === 'self'` 为 false，
  // 汇总查询根本没启用，页面只剩标题，看起来像渲染坏了。
  const tab: BillingTab = search.tab === 'detail' ? 'detail' : 'summary'
  const scope: BillingScope = search.scope === 'all' ? 'all' : 'self'
  const internal = isAdmin && search.internal === true
  const targetUserId = scope === 'self' ? search.user_id : undefined

  const [cursor, setCursor] = useState('')
  const [cursorStack, setCursorStack] = useState<string[]>([])
  const [lastExport, setLastExport] = useState<BillingExportReply | null>(null)

  // 换账期、换用户、换筛选都要回到第一页；沿用旧游标会读到另一个账期的位置。
  const filterKey = [
    period,
    targetUserId ?? '',
    internal ? '1' : '0',
    search.model ?? '',
    search.group ?? '',
  ].join('|')
  useEffect(() => {
    setCursor('')
    setCursorStack([])
    setLastExport(null)
  }, [filterKey])

  const overviewQuery = useQuery({
    queryKey: ['billing', 'overview', period, targetUserId ?? null, internal],
    queryFn: () =>
      getBillingStatement({ period, userId: targetUserId, internal }),
    enabled: scope === 'self',
    staleTime: 30_000,
  })

  const linesQuery = useQuery({
    queryKey: [
      'billing',
      'lines',
      period,
      targetUserId ?? null,
      internal,
      search.model ?? '',
      search.group ?? '',
      cursor,
    ],
    queryFn: () =>
      getBillingLines({
        period,
        userId: targetUserId,
        internal,
        cursor,
        pageSize: 50,
        model: search.model,
        group: search.group,
      }),
    enabled: scope === 'self' && tab === 'detail',
    placeholderData: (previous) => previous,
  })

  const statementsQuery = useQuery({
    queryKey: ['billing', 'statements', period],
    queryFn: () => listBillingStatements(period),
    enabled: scope === 'all' && isAdmin,
  })

  // 全站人数是几十到几百，本地过滤比每次输入都打一次后端划算，也没有抖动。
  const [userFilter, setUserFilter] = useState('')
  const filteredUsers = useMemo(() => {
    const list = statementsQuery.data?.items ?? []
    const keyword = userFilter.trim().toLowerCase()
    if (!keyword) return list
    return list.filter(
      (row) =>
        row.username.toLowerCase().includes(keyword) ||
        String(row.user_id) === keyword
    )
  }, [statementsQuery.data, userFilter])

  const exportMutation = useMutation({
    mutationFn: () =>
      getBillingExport({ period, userId: targetUserId, internal }),
    onSuccess: (reply) => {
      setLastExport(reply)
      // 走已有下载端点的签名地址：前端不拼文件路径，也不持有文件内容。
      const link = document.createElement('a')
      link.href = reply.download_url
      link.download = reply.filename
      document.body.appendChild(link)
      link.click()
      link.remove()
    },
    onError: (error: Error) => toast.error(error.message),
  })

  const overview = overviewQuery.data
  const isFinal = overview?.is_final ?? false
  // 未出账有两种：当月（金额随用量变化）与历史漏跑（等出账任务补）。
  // 同一句「当月为预估值」放在 10 月的账单上会让人以为看错了月份。
  const isCurrentPeriod = period === currentBillingPeriod()
  const draftHint = isCurrentPeriod
    ? t('Draft: the amount changes with usage until it is issued.')
    : t('This period has not been issued; figures are a live aggregate.')

  const summaryColumns = useMemo<StaticDataTableColumn<BillingSummaryItem>[]>(
    () => [
      {
        id: 'model',
        header: t('Model'),
        cell: (row) => (
          <button
            type='button'
            className='text-primary text-left font-medium hover:underline'
            onClick={() =>
              onSearchChange({
                ...search,
                period,
                tab: 'detail',
                model: row.model,
                group: row.group,
              })
            }
          >
            {row.model}
          </button>
        ),
      },
      ...(internal
        ? [
            {
              id: 'group',
              header: t('Group'),
              cell: (row: BillingSummaryItem) => row.group || '-',
            },
          ]
        : []),
      {
        id: 'requests',
        header: t('Requests'),
        cellClassName: 'text-right tabular-nums',
        cell: (row) => row.requests.toLocaleString(),
      },
      {
        id: 'errors',
        header: t('Errors'),
        cellClassName: 'text-right tabular-nums',
        cell: (row) => row.errors.toLocaleString(),
      },
      {
        id: 'refunds',
        header: t('Refund'),
        cellClassName: 'text-right tabular-nums',
        cell: (row) => row.refunds.toLocaleString(),
      },
      {
        id: 'input',
        header: t('Input tokens'),
        cellClassName: 'text-right tabular-nums',
        cell: (row) => formatTokens(row.input_tokens),
      },
      {
        id: 'cache_read',
        header: t('Cache read tokens'),
        cellClassName: 'text-right tabular-nums',
        cell: (row) => formatTokens(row.cache_read_tokens),
      },
      {
        id: 'cache_write',
        header: t('Cache write tokens'),
        cellClassName: 'text-right tabular-nums',
        cell: (row) => formatTokens(row.cache_write_tokens),
      },
      {
        id: 'output',
        header: t('Output tokens'),
        cellClassName: 'text-right tabular-nums',
        cell: (row) => formatTokens(row.output_tokens),
      },
      ...(internal
        ? [
            {
              id: 'quota',
              header: t('Quota'),
              cellClassName: 'text-right tabular-nums',
              cell: (row: BillingSummaryItem) => (row.quota ?? 0).toLocaleString(),
            },
          ]
        : []),
      {
        id: 'amount',
        header: t('Amount'),
        cellClassName: 'text-right tabular-nums',
        cell: (row) => formatMoney(row.amount, overview?.currency ?? 'CNY'),
      },
    ],
    [internal, onSearchChange, overview?.currency, period, search, t]
  )

  const lineColumns = useMemo<StaticDataTableColumn<BillingLine>[]>(
    () => [
      {
        id: 'time',
        header: t('Time'),
        cell: (row) => (
          <span className='whitespace-nowrap'>
            {formatTimestampToDate(row.created_at)}
          </span>
        ),
      },
      {
        id: 'request_id',
        header: t('Request ID'),
        cell: (row) => (
          <span className='font-mono text-xs' title={row.request_id}>
            {row.request_id || '-'}
          </span>
        ),
      },
      {
        id: 'type',
        header: t('Type'),
        cell: (row) => (
          <Badge
            variant={
              row.type === 6
                ? 'secondary'
                : row.type === 5
                  ? 'destructive'
                  : 'outline'
            }
          >
            {t(billingLineTypeKey(row.type))}
          </Badge>
        ),
      },
      {
        id: 'token',
        header: t('Token'),
        cell: (row) => row.token_name || '-',
      },
      {
        id: 'model',
        header: t('Model'),
        cell: (row) => row.model || '-',
      },
      ...(internal
        ? [
            {
              id: 'group',
              header: t('Group'),
              cell: (row: BillingLine) => row.group || '-',
            },
            {
              id: 'endpoint',
              header: t('Endpoint'),
              cell: (row: BillingLine) => (
                <span className='font-mono text-xs'>{row.endpoint || '-'}</span>
              ),
            },
          ]
        : []),
      {
        id: 'input',
        header: t('Input tokens'),
        cellClassName: 'text-right tabular-nums',
        cell: (row) => formatTokens(row.input_tokens),
      },
      {
        id: 'cache_read',
        header: t('Cache read tokens'),
        cellClassName: 'text-right tabular-nums',
        cell: (row) => formatTokens(row.cache_read_tokens),
      },
      {
        id: 'cache_write',
        header: t('Cache write tokens'),
        cellClassName: 'text-right tabular-nums',
        cell: (row) => formatTokens(row.cache_write_tokens),
      },
      {
        id: 'output',
        header: t('Output tokens'),
        cellClassName: 'text-right tabular-nums',
        cell: (row) => formatTokens(row.output_tokens),
      },
      {
        id: 'total',
        header: t('Total tokens'),
        cellClassName: 'text-right tabular-nums',
        cell: (row) => formatTokens(row.total_tokens),
      },
      ...(internal
        ? [
            {
              id: 'quota',
              header: t('Quota'),
              cellClassName: 'text-right tabular-nums',
              cell: (row: BillingLine) => (row.quota ?? 0).toLocaleString(),
            },
          ]
        : []),
      {
        id: 'amount',
        header: t('Amount'),
        cellClassName: 'text-right tabular-nums',
        cell: (row) => formatMoney(row.amount, overview?.currency ?? 'CNY'),
      },
      {
        id: 'task',
        header: t('Task ID'),
        cell: (row) => (
          <span className='font-mono text-xs' title={row.task_id}>
            {row.task_id || '-'}
          </span>
        ),
      },
      {
        id: 'duration',
        header: t('Duration (ms)'),
        cellClassName: 'text-right tabular-nums',
        cell: (row) => (row.use_time_ms ? row.use_time_ms : '-'),
      },
    ],
    [internal, overview?.currency, t]
  )

  const periodOptions = useMemo(() => recentBillingPeriods(24), [])
  if (!periodOptions.includes(period)) {
    periodOptions.unshift(period)
  }

  const clearFilters = () =>
    onSearchChange({ ...search, period, model: undefined, group: undefined })

  const body = (() => {
    if (scope === 'all') {
      const list = statementsQuery.data
      if (statementsQuery.isLoading) return <TableSkeleton />
      if (statementsQuery.isError) {
        return <ErrorBlock message={(statementsQuery.error as Error).message} />
      }
      if (!list) return null
      const hasKeyword = userFilter.trim() !== ''
      if (list.items.length === 0 && !hasKeyword) {
        return (
          <EmptyState
            icon={Receipt}
            title={t('Statement not issued yet')}
            description={t('Statements are issued on the 1st of each month.')}
          />
        )
      }
      return (
        <div className='space-y-3'>
          <div className='flex flex-wrap items-center gap-2'>
            <Input
              value={userFilter}
              onChange={(event) => setUserFilter(event.target.value)}
              placeholder={t('Filter by username or user ID')}
              className='h-8 w-[260px]'
            />
            <Badge variant={list.is_draft ? 'outline' : 'secondary'}>
              {list.is_draft ? t('Live') : t('Issued')}
            </Badge>
            <span className='text-muted-foreground text-xs'>
              {filteredUsers.length} / {list.items.length}
            </span>
          </div>
          <StaticDataTable
          data={filteredUsers}
          getRowKey={(row) => row.user_id}
          columns={[
            {
              id: 'user',
              header: t('User'),
              cell: (row) => (
                <button
                  type='button'
                  className='text-primary font-medium hover:underline'
                  onClick={() =>
                    onSearchChange({
                      ...search,
                      scope: 'self',
                      period,
                      user_id: row.user_id,
                      tab: 'summary',
                      model: undefined,
                      group: undefined,
                    })
                  }
                >
                  {row.username || `#${row.user_id}`}
                </button>
              ),
            },
            {
              id: 'requests',
              header: t('Requests'),
              cellClassName: 'text-right tabular-nums',
              cell: (row) => row.requests.toLocaleString(),
            },
            {
              id: 'errors',
              header: t('Errors'),
              cellClassName: 'text-right tabular-nums',
              cell: (row) => row.errors.toLocaleString(),
            },
            {
              id: 'refunds',
              header: t('Refund'),
              cellClassName: 'text-right tabular-nums',
              cell: (row) => row.refunds.toLocaleString(),
            },
            {
              id: 'amount',
              header: t('Amount'),
              cellClassName: 'text-right tabular-nums',
              cell: (row) => formatMoney(row.amount, row.currency),
            },
            {
              id: 'status',
              header: t('Status'),
              cell: (row) => (
                <Badge variant={row.is_issued ? 'secondary' : 'outline'}>
                  {row.is_issued ? t('Issued') : t('In progress (estimated)')}
                </Badge>
              ),
            },
          ]}
          />
        </div>
      )
    }

    if (overviewQuery.isLoading) {
      return (
        <div className='space-y-4'>
          <div className='grid gap-3 sm:grid-cols-2 lg:grid-cols-4'>
            {Array.from({ length: 4 }, (_, index) => (
              <Skeleton key={index} className='h-24 w-full' />
            ))}
          </div>
          <TableSkeleton />
        </div>
      )
    }
    if (overviewQuery.isError) {
      return <ErrorBlock message={(overviewQuery.error as Error).message} />
    }
    if (!overview) return null

    const totalTokens =
      overview.input_tokens +
      overview.cache_read_tokens +
      overview.cache_write_tokens +
      overview.output_tokens

    return (
      <div className='space-y-4'>
        <div className='grid gap-3 sm:grid-cols-2 lg:grid-cols-4'>
          <MetricCard
            label={t('Net amount')}
            value={formatMoney(overview.amount, overview.currency)}
            hint={isFinal ? t('Issued') : draftHint}
            emphasis
          />
          <MetricCard
            label={t('Requests')}
            value={overview.requests.toLocaleString()}
            hint={`${t('Failed')} ${overview.errors.toLocaleString()} · ${t('Refund')} ${overview.refunds.toLocaleString()}`}
          />
          <MetricCard
            label={t('Total tokens')}
            value={formatTokens(totalTokens)}
            hint={`${t('Input tokens')} ${formatTokens(overview.input_tokens)} · ${t('Output tokens')} ${formatTokens(overview.output_tokens)}`}
          />
          <MetricCard
            label={t('Period')}
            value={formatBillingPeriod(overview.period)}
            hint={
              overview.issued_at
                ? `${t('Issued')} ${formatTimestampToDate(overview.issued_at)}`
                : t('In progress (estimated)')
            }
          />
        </div>

        {internal && overview.pricing ? (
          <div className='text-muted-foreground flex flex-wrap items-center gap-x-4 gap-y-1 text-xs'>
            <span>
              {t('Quota per unit')}: {overview.pricing.quota_per_unit}
            </span>
            <span>
              {t('Settle rate')}: {overview.pricing.settle_rate}
            </span>
            <span>
              {t('Internal quota')}: {(overview.quota ?? 0).toLocaleString()}
            </span>
          </div>
        ) : null}

        <Card>
          <div className='flex flex-col gap-2 px-4 pt-4 sm:flex-row sm:items-center sm:justify-between'>
            <div className='flex flex-wrap items-center gap-2'>
              <h3 className='text-base font-medium'>
                {tab === 'summary' ? t('Summary') : t('Details')}
              </h3>
              {search.model || search.group ? (
                <button
                  type='button'
                  className='text-muted-foreground hover:text-foreground text-xs underline'
                  onClick={clearFilters}
                >
                  {t('Clear filters')}
                </button>
              ) : null}
            </div>
            <Tabs
              value={tab}
              onValueChange={(value) =>
                onSearchChange({ ...search, period, tab: value as BillingTab })
              }
            >
              <TabsList>
                <TabsTrigger value='summary'>{t('Summary')}</TabsTrigger>
                <TabsTrigger value='detail'>{t('Details')}</TabsTrigger>
              </TabsList>
            </Tabs>
          </div>
          <CardContent className='space-y-3'>
            {tab === 'summary' ? (
              overview.items.length === 0 ? (
                <EmptyState title={t('No data')} className='min-h-[220px]' />
              ) : (
                <StaticDataTable
                  data={overview.items}
                  getRowKey={(row) => `${row.model}|${row.group ?? ''}`}
                  tableClassName={internal ? 'min-w-[900px]' : 'min-w-[760px]'}
                  columns={summaryColumns}
                />
              )
            ) : linesQuery.isLoading ? (
              <TableSkeleton />
            ) : linesQuery.isError ? (
              <ErrorBlock message={(linesQuery.error as Error).message} />
            ) : (linesQuery.data?.items.length ?? 0) === 0 ? (
              <EmptyState title={t('No data')} className='min-h-[220px]' />
            ) : (
              <>
                <StaticDataTable
                  data={linesQuery.data?.items ?? []}
                  getRowKey={(row) =>
                    `${row.request_id}-${row.created_at}-${row.type}`
                  }
                  tableClassName={internal ? 'min-w-[1400px]' : 'min-w-[1080px]'}
                  columns={lineColumns}
                />
                <div className='flex items-center justify-between'>
                  <span className='text-muted-foreground text-xs'>
                    {t('Page')} {cursorStack.length + 1}
                  </span>
                  <div className='flex gap-2'>
                    <Button
                      variant='outline'
                      size='sm'
                      disabled={cursorStack.length === 0}
                      onClick={() => {
                        const stack = [...cursorStack]
                        const previous = stack.pop() ?? ''
                        setCursorStack(stack)
                        setCursor(previous)
                      }}
                    >
                      {t('Previous page')}
                    </Button>
                    <Button
                      variant='outline'
                      size='sm'
                      disabled={!linesQuery.data?.next_cursor}
                      onClick={() => {
                        setCursorStack([...cursorStack, cursor])
                        setCursor(linesQuery.data?.next_cursor ?? '')
                      }}
                    >
                      {t('Next page')}
                    </Button>
                  </div>
                </div>
              </>
            )}
          </CardContent>
        </Card>
      </div>
    )
  })()

  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>{t('Billing statement')}</SectionPageLayout.Title>
      <SectionPageLayout.Actions>
        <Select
          value={period}
          onValueChange={(value) => {
            if (value) onSearchChange({ ...search, period: value })
          }}
        >
          <SelectTrigger className='w-[150px]' size='sm'>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {periodOptions.map((option) => (
              <SelectItem key={option} value={option}>
                {formatBillingPeriod(option)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        {isAdmin ? (
          <Button
            variant={internal ? 'default' : 'outline'}
            size='sm'
            onClick={() =>
              onSearchChange({ ...search, period, internal: !internal })
            }
          >
            {t('Internal view')}
          </Button>
        ) : null}
        <Button
          size='sm'
          disabled={scope !== 'self' || exportMutation.isPending}
          onClick={() => exportMutation.mutate()}
        >
          <Download className='size-4' />
          {exportMutation.isPending ? t('Preparing the file...') : t('Export')}
        </Button>
      </SectionPageLayout.Actions>
      <SectionPageLayout.Content>
        <div className='space-y-4'>
          {isAdmin ? (
            <Tabs
              value={scope}
              onValueChange={(value) =>
                onSearchChange({
                  ...search,
                  period,
                  scope: value as BillingScope,
                  user_id: undefined,
                  model: undefined,
                  group: undefined,
                })
              }
            >
              <TabsList>
                <TabsTrigger value='self'>{t('My statement')}</TabsTrigger>
                <TabsTrigger value='all'>{t('All users')}</TabsTrigger>
              </TabsList>
            </Tabs>
          ) : null}
          {lastExport ? (
            <div className='text-muted-foreground flex flex-wrap items-center gap-x-3 gap-y-1 text-xs'>
              <span>{lastExport.filename}</span>
              <span>
                {lastExport.rows.toLocaleString()} {t('Rows')}
              </span>
              <span>{formatFileSize(lastExport.bytes)}</span>
              {lastExport.is_draft ? <Badge variant='outline'>{t('Draft')}</Badge> : null}
            </div>
          ) : null}
          {body}
        </div>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}

function MetricCard(props: {
  label: string
  value: string
  hint?: string
  emphasis?: boolean
}) {
  return (
    <Card>
      <CardContent className='space-y-1 py-4'>
        <p className='text-muted-foreground text-xs'>{props.label}</p>
        <p
          className={
            props.emphasis
              ? 'text-2xl font-semibold tabular-nums'
              : 'text-xl font-medium tabular-nums'
          }
        >
          {props.value}
        </p>
        {props.hint ? (
          <p className='text-muted-foreground line-clamp-2 text-xs'>
            {props.hint}
          </p>
        ) : null}
      </CardContent>
    </Card>
  )
}

function TableSkeleton() {
  return (
    <div className='space-y-2'>
      {Array.from({ length: 6 }, (_, index) => (
        <Skeleton key={index} className='h-10 w-full' />
      ))}
    </div>
  )
}

function ErrorBlock(props: { message: string }) {
  return (
    <div className='border-destructive/40 bg-destructive/5 text-destructive rounded-lg border p-4 text-sm'>
      {props.message}
    </div>
  )
}
