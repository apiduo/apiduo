import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import {
  CartesianGrid,
  ComposedChart,
  Legend,
  Line,
  ReferenceLine,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts'

import { SectionPageLayout } from '@/components/layout'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'

import {
  claimOpsIncident,
  closeOpsIncident,
  createOpsIncident,
  getOpsBreakdown,
  getOpsChanges,
  getOpsClusters,
  getOpsIncidents,
  getOpsMargins,
  getOpsRateLimits,
  getOpsSeries,
  getOpsShift,
  startOpsShift,
  putOpsChannelCost,
  reassignOpsIncident,
} from './api'
import { CapacityPlanner } from './components/capacity/capacity-planner'

const RANGES = [
  { key: '1h', label: '1h', span: 3600, bucket: 300 },
  { key: '6h', label: '6h', span: 6 * 3600, bucket: 600 },
  { key: '24h', label: '24h', span: 24 * 3600, bucket: 1800 },
  { key: '7d', label: '7d', span: 7 * 86400, bucket: 21600 },
] as const

function pct(v: number) {
  return `${(v * 100).toFixed(1)}%`
}

function clock(ts: number) {
  const d = new Date(ts * 1000)
  return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
}

export function ServiceHealth() {
  const { t } = useTranslation()
  const qc = useQueryClient()
  // 默认最近一小时：这个页面是「现在出什么事了」，不是「这个月怎么样」。
  // 24 小时窗口会把刚发生的变化稀释成一条几乎水平的线。
  const [rangeKey, setRangeKey] = useState<string>('1h')
  const [dim, setDim] = useState<'model' | 'channel' | 'group'>('model')
  // 维度选定后还要能筛具体值：一个分组下有几十个模型时，
  // 逐个 눈找太慢，直接按关键字过滤这一列。
  const [dimFilter, setDimFilter] = useState('')

  const range = RANGES.find((r) => r.key === rangeKey) ?? RANGES[0]
  const to = Math.floor(Date.now() / 1000)
  const from = to - range.span

  const series = useQuery({
    queryKey: ['ops-series', rangeKey],
    queryFn: () => getOpsSeries({ from, to, bucket: range.bucket }),
    refetchInterval: 60_000,
  })

  const margins = useQuery({
    queryKey: ['ops-margins', rangeKey],
    queryFn: () => getOpsMargins({ from, to }),
    refetchInterval: 60_000,
  })

  const rateLimits = useQuery({
    queryKey: ['ops-ratelimits'],
    queryFn: getOpsRateLimits,
    refetchInterval: 30_000,
  })

  const clusters = useQuery({
    queryKey: ['ops-clusters', rangeKey],
    queryFn: () => getOpsClusters({ from, to }),
    refetchInterval: 60_000,
  })

  const changes = useQuery({
    queryKey: ['ops-changes', rangeKey],
    queryFn: () => getOpsChanges({ from, to }),
    refetchInterval: 60_000,
  })

  const openIncident = useMutation({
    mutationFn: (body: { title: string; severity: number; detail: string }) =>
      createOpsIncident(body),
    onSuccess: (inc) => {
      void qc.invalidateQueries({ queryKey: ['ops-incidents'] })
      // 必须给反馈：新条目落在页面底部的值班看板里，而按钮就在手边，
      // 不提示的话用户看到按钮毫无变化，会以为没点上，然后重复点。
      toast.success(t('Incident opened') + (inc?.id ? ` #${inc.id}` : ''))
    },
    onError: () => toast.error(t('Failed to open incident')),
  })

  const breakdown = useQuery({
    queryKey: ['ops-breakdown', rangeKey, dim],
    queryFn: () => getOpsBreakdown({ from, to, dim }),
    refetchInterval: 60_000,
  })

  const breakdownRows = useMemo(() => {
    const rows = breakdown.data?.rows ?? []
    const needle = dimFilter.trim().toLowerCase()
    if (!needle) return rows
    return rows.filter((r) => r.key.toLowerCase().includes(needle))
  }, [breakdown.data?.rows, dimFilter])

  // 两条线量纲不同（比例 vs 毫秒），必须双轴。
  // 合成单轴的话，P95 会把错误率压成一条贴地的直线。
  const points = (series.data?.points ?? []).map((p) => ({
    ...p,
    label: clock(p.ts),
    failPct: Number((p.fail_rate * 100).toFixed(2)),
  }))

  const totals = points.reduce(
    (acc, p) => ({
      requests: acc.requests + p.requests,
      failures: acc.failures + p.failures,
    }),
    { requests: 0, failures: 0 }
  )
  const overallRate =
    totals.requests + totals.failures > 0
      ? totals.failures / (totals.requests + totals.failures)
      : 0
  const worstP95 = points.reduce((m, p) => Math.max(m, p.p95_ms), 0)

  return (
    <SectionPageLayout fixedContent>
      <SectionPageLayout.Title>{t('Service health')}</SectionPageLayout.Title>
      <SectionPageLayout.Actions>
        <Tabs value={rangeKey} onValueChange={(v) => setRangeKey(String(v))}>
          <TabsList>
            {RANGES.map((r) => (
              <TabsTrigger key={r.key} value={r.key}>
                {r.label}
              </TabsTrigger>
            ))}
          </TabsList>
        </Tabs>
      </SectionPageLayout.Actions>
      <SectionPageLayout.Content>
        <div className='space-y-3'>
          <div className='grid gap-3 sm:grid-cols-3'>
            <Card>
              <CardContent className='py-4'>
                <p className='text-muted-foreground text-xs'>
                  {t('Failure rate')}
                </p>
                <p className='mt-1 text-2xl font-semibold tabular-nums'>
                  {pct(overallRate)}
                </p>
              </CardContent>
            </Card>
            <Card>
              <CardContent className='py-4'>
                <p className='text-muted-foreground text-xs'>{t('Requests')}</p>
                <p className='mt-1 text-2xl font-semibold tabular-nums'>
                  {totals.requests.toLocaleString()}
                </p>
              </CardContent>
            </Card>
            <Card>
              <CardContent className='py-4'>
                <p className='text-muted-foreground text-xs'>
                  {t('Worst P95')}
                </p>
                <p className='mt-1 text-2xl font-semibold tabular-nums'>
                  {(worstP95 / 1000).toFixed(1)}s
                </p>
              </CardContent>
            </Card>
          </div>

          <Card>
            <CardHeader>
              <CardTitle>{t('Failures and latency')}</CardTitle>
              <CardDescription>
                {t(
                  'Two axes on purpose: a shared axis would flatten the failure rate against millisecond latency'
                )}
              </CardDescription>
            </CardHeader>
            <CardContent>
              {series.isLoading ? (
                <Skeleton className='h-72 w-full' />
              ) : points.length === 0 ? (
                <p className='text-muted-foreground py-16 text-center text-sm'>
                  {t('No data in this window')}
                </p>
              ) : (
                <ResponsiveContainer width='100%' height={300}>
                  <ComposedChart data={points}>
                    <CartesianGrid strokeDasharray='3 3' opacity={0.3} />
                    <XAxis dataKey='label' tick={{ fontSize: 11 }} minTickGap={24} />
                    <YAxis
                      yAxisId='rate'
                      tick={{ fontSize: 11 }}
                      unit='%'
                      width={52}
                    />
                    <YAxis
                      yAxisId='lat'
                      orientation='right'
                      tick={{ fontSize: 11 }}
                      width={60}
                    />
                    {/* 两个系列单位不同（% 与 ms），不能给图例设统一 unit，
                        只能在 tooltip 里按系列名分别格式化。 */}
                    <Tooltip
                      formatter={
                        ((value: unknown, name: unknown) => {
                          const series = String(name)
                          const suffix =
                            series === t('Failure rate') ? '%' : 'ms'
                          return [`${value}${suffix}`, series]
                        }) as never
                      }
                    />
                    <Legend wrapperStyle={{ fontSize: 12 }} />
                    {/* 变更竖线：把「什么时候坏的」和「之前改过什么」放到同一张图上。
                        没有它，排障只能靠人回忆。 */}
                    {(changes.data?.events ?? []).slice(0, 12).map((ev) => {
                      const point = points.find(
                        (p) => Math.abs(p.ts - ev.occurred_at) <= range.bucket
                      )
                      if (!point) return null
                      return (
                        <ReferenceLine
                          key={ev.id}
                          x={point.label}
                          yAxisId='rate'
                          stroke='#888780'
                          strokeDasharray='4 4'
                          label={{
                            value: ev.kind === 'deploy' ? 'deploy' : 'chg',
                            position: 'top',
                            fontSize: 10,
                            fill: '#5F5E5A',
                          }}
                        />
                      )
                    })}
                    <Line
                      yAxisId='rate'
                      type='monotone'
                      dataKey='failPct'
                      name={t('Failure rate')}
                      stroke='#cc2222'
                      strokeWidth={2}
                      dot={false}
                    />
                    <Line
                      yAxisId='lat'
                      type='monotone'
                      dataKey='p95_ms'
                      name={t('P95 latency')}
                      stroke='#2b6cb0'
                      strokeWidth={2}
                      dot={false}
                    />
                  </ComposedChart>
                </ResponsiveContainer>
              )}
            </CardContent>
          </Card>

          {(clusters.data?.clusters ?? []).length > 0 && (
            <Card>
              <CardHeader>
                <CardTitle>{t('Error clusters')}</CardTitle>
                <CardDescription>
                  {t('Grouped by message shape, not raw text, so the same root cause lands in one row')}
                </CardDescription>
              </CardHeader>
              <CardContent>
                <table className='w-full text-sm'>
                  <thead>
                    <tr className='text-muted-foreground border-b text-left text-xs'>
                      <th className='py-2 font-medium'>{t('Root cause')}</th>
                      <th className='py-2 text-right font-medium'>
                        {t('Requests')}
                      </th>
                      <th className='py-2 text-right font-medium'>{t('Users')}</th>
                      <th className='py-2 text-right font-medium'>
                        {t('Channels')}
                      </th>
                      <th className='py-2 text-right font-medium'>
                        {t('Last seen')}
                      </th>
                    </tr>
                  </thead>
                  <tbody>
                    {(clusters.data?.clusters ?? []).map((cl) => (
                      <tr key={cl.fingerprint} className='border-b last:border-0'>
                        <td className='max-w-[420px] py-2'>
                          <p
                            className='truncate font-mono text-xs'
                            title={cl.fingerprint}
                          >
                            {cl.fingerprint}
                          </p>
                          {cl.sample && cl.sample !== cl.fingerprint && (
                            <p
                              className='text-muted-foreground truncate text-[11px]'
                              title={cl.sample}
                            >
                              {t('e.g.')} {cl.sample}
                            </p>
                          )}
                          {/* 归一化后 502/503 会合并成 SERVERERROR，
                              具体码在这里补回来：处置 502（网关挂）和 503（过载）不是一回事。 */}
                          {cl.status_codes && cl.status_codes.length > 0 && (
                            <p className='mt-0.5 flex flex-wrap gap-1'>
                              {cl.status_codes.map((sc) => (
                                <span
                                  key={sc.code}
                                  className='bg-muted rounded px-1 font-mono text-[10px]'
                                  title={t('{{count}} occurrences in this cluster', {
                                    count: sc.count,
                                  })}
                                >
                                  {sc.code}
                                  <span className='text-muted-foreground'>
                                    ×{sc.count}
                                  </span>
                                </span>
                              ))}
                            </p>
                          )}
                        </td>
                        <td className='py-2 text-right font-mono tabular-nums'>
                          {cl.requests}
                        </td>
                        <td className='text-muted-foreground py-2 text-right tabular-nums'>
                          {cl.users}
                        </td>
                        <td className='text-muted-foreground py-2 text-right tabular-nums'>
                          {cl.channels}
                        </td>
                        <td className='text-muted-foreground py-2 text-right text-xs tabular-nums'>
                          {clock(cl.last_seen)}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </CardContent>
            </Card>
          )}

          {(changes.data?.events ?? []).length > 0 && (
            <Card>
              <CardHeader>
                <CardTitle>{t('Changes in this window')}</CardTitle>
                <CardDescription>
                  {t('Deploys, channel switches and manual notes, oldest first')}
                </CardDescription>
              </CardHeader>
              <CardContent className='space-y-2'>
                {(changes.data?.events ?? []).map((ev) => (
                  <div key={ev.id} className='flex items-start gap-3 text-sm'>
                    <span className='text-muted-foreground w-14 shrink-0 font-mono text-xs tabular-nums'>
                      {clock(ev.occurred_at)}
                    </span>
                    <Badge variant='outline' className='shrink-0 text-[10px]'>
                      {ev.kind}
                    </Badge>
                    <div className='min-w-0'>
                      <p className='truncate'>{ev.title}</p>
                      {ev.detail && (
                        <p className='text-muted-foreground truncate text-xs'>
                          {ev.detail}
                        </p>
                      )}
                    </div>
                    <span className='text-muted-foreground ml-auto shrink-0 text-xs'>
                      {ev.actor}
                    </span>
                  </div>
                ))}
              </CardContent>
            </Card>
          )}

          <Card>
            <CardHeader className='flex flex-row items-start justify-between gap-4'>
              <div>
                <CardTitle>{t('Breakdown')}</CardTitle>
                <CardDescription>
                  {t('Sorted by request volume, slow and failing are different columns')}
                </CardDescription>
              </div>
              <Tabs
                value={dim}
                onValueChange={(v) =>
                  setDim(String(v) as 'model' | 'channel' | 'group')
                }
              >
                <TabsList>
                  <TabsTrigger value='model'>{t('By model')}</TabsTrigger>
                  <TabsTrigger value='channel'>{t('By channel')}</TabsTrigger>
                  <TabsTrigger value='group'>{t('By group')}</TabsTrigger>
                </TabsList>
              </Tabs>
              <input
                className='border-input bg-background h-8 w-full rounded-md border px-2 text-xs sm:w-44'
                placeholder={t('Filter this column')}
                value={dimFilter}
                onChange={(e) => setDimFilter(e.target.value)}
              />
            </CardHeader>
            <CardContent>
              {breakdown.isLoading ? (
                <Skeleton className='h-40 w-full' />
              ) : (
                <table className='w-full text-sm'>
                  <thead>
                    <tr className='text-muted-foreground border-b text-left text-xs'>
                      {/* 表头要跟着维度变：写死 "密钥" 时切到按渠道，
                          第一列是 "#27" 却标着 "密钥"，读起来像数据错了。 */}
                      <th className='py-2 font-medium'>
                        {dim === 'channel'
                          ? t('Channel')
                          : dim === 'group'
                            ? t('Group')
                            : t('Model')}
                      </th>
                      <th className='py-2 text-right font-medium'>
                        {t('Requests')}
                      </th>
                      <th className='py-2 text-right font-medium'>
                        {t('Failures')}
                      </th>
                      <th className='py-2 text-right font-medium'>
                        {t('Failure rate')}
                      </th>
                      <th className='py-2 text-right font-medium'>P50</th>
                      <th className='py-2 text-right font-medium'>P95</th>
                      <th className='py-2 text-right font-medium' />
                    </tr>
                  </thead>
                  <tbody>
                    {breakdownRows.map((r) => (
                      <tr key={r.key} className='border-b last:border-0'>
                        {/* 渠道维度加 # 前缀：毛利表和值班看板都用 #3 这种写法，
                            同一页里一处裸数字一处带井号，读起来像两个不同的东西。 */}
                        <td className='py-2 font-mono text-xs'>
                          {dim === 'channel' ? `#${r.key}` : r.key}
                        </td>
                        <td className='py-2 text-right tabular-nums'>
                          {r.requests}
                        </td>
                        <td className='py-2 text-right tabular-nums'>
                          {r.failures}
                        </td>
                        <td className='py-2 text-right tabular-nums'>
                          {r.fail_rate >= 0.2 ? (
                            <Badge variant='destructive'>
                              {pct(r.fail_rate)}
                            </Badge>
                          ) : (
                            pct(r.fail_rate)
                          )}
                        </td>
                        <td className='text-muted-foreground py-2 text-right tabular-nums'>
                          {r.p50_ms.toFixed(0)}ms
                        </td>
                        <td className='py-2 text-right tabular-nums'>
                          {r.p95_ms.toFixed(0)}ms
                        </td>
                        <td className='py-2 text-right'>
                          {/* 一键开单：把「看到了」变成「有人负责」。
                              标题带上维度和数字，值班不必再手打一遍。 */}
                          <Button
                            variant='ghost'
                            size='sm'
                            className='text-xs'
                            disabled={openIncident.isPending}
                            onClick={() =>
                              openIncident.mutate({
                                title: `${dim === 'model' ? t('Model') : dim === 'group' ? t('Group') : t('Channel')} ${r.key} · ${t('failure')} ${pct(r.fail_rate)} · P95 ${r.p95_ms.toFixed(0)}ms`,
                                severity: r.fail_rate >= 0.2 ? 1 : 2,
                                detail: t('Opened from the breakdown table'),
                              })
                            }
                          >
                            {t('Open incident')}
                          </Button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </CardContent>
          </Card>
          {(margins.data ?? []).length > 0 && (
            <Card>
              <CardHeader>
                <CardTitle>{t('Channel margin')}</CardTitle>
                <CardDescription>
                  {t('Revenue from logs, cost from the unit prices below. Unpriced channels show no cost, not zero cost')}
                </CardDescription>
              </CardHeader>
              <CardContent>
                <table className='w-full text-sm'>
                  <thead>
                    <tr className='text-muted-foreground border-b text-left text-xs'>
                      <th className='py-2 font-medium'>{t('Channel')}</th>
                      <th className='py-2 text-right font-medium'>
                        {t('Revenue')}
                      </th>
                      <th className='py-2 text-right font-medium'>{t('Cost')}</th>
                      <th className='py-2 text-right font-medium'>{t('Margin')}</th>
                      <th className='py-2 text-right font-medium'>{t('Requests')}</th>
                      <th className='py-2 text-right font-medium'>{t('Priced')}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {(margins.data ?? []).map((m) => (
                      <tr key={m.channel_id} className='border-b last:border-0'>
                        <td className='py-2 font-mono text-xs'>#{m.channel_id}</td>
                        {/* 三列统一：两位小数、货币符号紧贴数字。
                            同一行里 $ 1035.1919 / $1823.3970 / -$788.21 三种写法
                            会让人以为它们是不同精度的东西。 */}
                        <td className='py-2 text-right tabular-nums'>
                          {`$${m.revenue_usd.toFixed(2)}`}
                        </td>
                        <td className='text-muted-foreground py-2 text-right tabular-nums'>
                          {m.priced ? `$${m.cost_usd.toFixed(2)}` : '—'}
                        </td>
                        <td
                          className={
                            m.priced && m.margin_usd < 0
                              ? 'py-2 text-right font-semibold text-red-600 tabular-nums'
                              : 'py-2 text-right tabular-nums'
                          }
                        >
                          {/* 负号在前、货币符号在后：负毛利写成 $-788 是错的，
                              财务上读作「美元负七百八十八」，而实际是「负七百八十八美元」。 */}
                          {m.priced
                            ? m.margin_usd < 0
                              ? `-$${Math.abs(m.margin_usd).toFixed(2)}`
                              : `$${m.margin_usd.toFixed(2)}`
                            : '—'}
                          {m.priced && (
                            <span className='text-muted-foreground ml-1 text-xs'>
                              {m.margin_pct.toFixed(0)}%
                            </span>
                          )}
                        </td>
                        <td className='text-muted-foreground py-2 text-right tabular-nums'>
                          {m.requests}
                        </td>
                        <td className='py-2 text-right'>
                          {m.priced ? (
                            <Badge variant='outline' className='text-[10px]'>
                              {t('Yes')}
                            </Badge>
                          ) : (
                            <span className='text-muted-foreground/70 text-xs'>
                              {t('Not set')}
                            </span>
                          )}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
                <CostEditor />
              </CardContent>
            </Card>
          )}

          {(rateLimits.data ?? []).length > 0 && (
            <Card>
              <CardHeader>
                <CardTitle>{t('Upstream quota headroom')}</CardTitle>
                <CardDescription>
                  {t('Read from upstream rate-limit headers on every call, kept in memory, most constrained first')}
                </CardDescription>
              </CardHeader>
              <CardContent className='space-y-2'>
                {(rateLimits.data ?? []).map((r) => {
                  const pctLeft =
                    r.limit > 0 ? (r.remaining / r.limit) * 100 : null
                  const tight = pctLeft !== null && pctLeft <= 10
                  return (
                    <div
                      key={r.channel_id}
                      className='flex items-center gap-3 text-sm'
                    >
                      <span className='w-20 shrink-0 font-mono text-xs'>
                        #{r.channel_id}
                      </span>
                      <div className='bg-muted h-2 flex-1 overflow-hidden rounded-full'>
                        <div
                          className={
                            tight
                              ? 'bg-destructive h-full rounded-full'
                              : 'bg-primary h-full rounded-full'
                          }
                          style={{ width: `${Math.max(2, pctLeft ?? 100)}%` }}
                        />
                      </div>
                      <span className='text-muted-foreground w-28 text-right text-xs tabular-nums'>
                        {r.remaining}
                        {r.limit > 0 ? ` / ${r.limit}` : ''}
                      </span>
                      <span className='text-muted-foreground w-16 text-right text-[11px]'>
                        {r.reset || '—'}
                      </span>
                      <span className='text-muted-foreground/70 w-16 text-right text-[11px]'>
                        {r.source}
                      </span>
                    </div>
                  )
                })}
              </CardContent>
            </Card>
          )}

          <CapacityPlanner />

          <IncidentBoard />
        </div>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}

// 值班闭环：认领、结单、备注。
//
// 没有这一块，这个台子只是「看一眼就走」的监控；
// 有它，才知道上一班处理到哪一步、谁在负责。
// 单价配置：运营一次性维护的动作，做成内联一行比开弹窗省事。
function CostEditor() {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const [channel, setChannel] = useState('')
  const [input, setInput] = useState('')
  const [output, setOutput] = useState('')

  const save = useMutation({
    mutationFn: () =>
      putOpsChannelCost({
        channel_id: Number(channel),
        input_per_million: Number(input) || 0,
        output_per_million: Number(output) || 0,
      }),
    onSuccess: () => {
      setChannel('')
      setInput('')
      setOutput('')
      void qc.invalidateQueries({ queryKey: ['ops-margins'] })
    },
  })

  return (
    <div className='mt-3 flex flex-wrap items-center gap-2'>
      <input
        className='border-input bg-background h-8 w-20 rounded-md border px-2 text-xs'
        placeholder={t('Channel #')}
        value={channel}
        onChange={(e) => setChannel(e.target.value)}
      />
      <input
        className='border-input bg-background h-8 w-28 rounded-md border px-2 text-xs'
        placeholder={t('Input $/M')}
        value={input}
        onChange={(e) => setInput(e.target.value)}
      />
      <input
        className='border-input bg-background h-8 w-28 rounded-md border px-2 text-xs'
        placeholder={t('Output $/M')}
        value={output}
        onChange={(e) => setOutput(e.target.value)}
      />
      <Button
        size='sm'
        variant='outline'
        disabled={!channel || save.isPending}
        onClick={() => save.mutate()}
      >
        {t('Save price')}
      </Button>
    </div>
  )
}

function IncidentBoard() {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const [showClosed, setShowClosed] = useState(false)
  const [title, setTitle] = useState('')
  const [note, setNote] = useState('')

  const shift = useQuery({
    queryKey: ['ops-shift'],
    queryFn: getOpsShift,
    refetchInterval: 60_000,
  })

  const takeShift = useMutation({
    mutationFn: () => startOpsShift(note),
    onSuccess: () => {
      setNote('')
      void qc.invalidateQueries({ queryKey: ['ops-shift'] })
    },
  })

  const incidents = useQuery({
    queryKey: ['ops-incidents', showClosed],
    queryFn: () => getOpsIncidents(showClosed),
    refetchInterval: 30_000,
  })

  const invalidate = () =>
    void qc.invalidateQueries({ queryKey: ['ops-incidents'] })

  const create = useMutation({
    mutationFn: () => createOpsIncident({ title, severity: 2 }),
    onSuccess: (inc) => {
      toast.success(t('Incident opened') + (inc?.id ? ` #${inc.id}` : ''))
      setTitle('')
      invalidate()
    },
  })

  const claim = useMutation({
    mutationFn: (id: number) => claimOpsIncident(id),
    onSuccess: invalidate,
    onError: () => invalidate(),
  })

  const takeOver = useMutation({
    mutationFn: (id: number) => reassignOpsIncident(id),
    onSuccess: invalidate,
    onError: () => invalidate(),
  })

  const close = useMutation({
    mutationFn: (args: { id: number; resolution: string }) =>
      closeOpsIncident(args.id, 'resolved', args.resolution),
    onSuccess: invalidate,
    onError: () => invalidate(),
  })

  const items = incidents.data?.items ?? []
  const severityTone = ['', '紧急', '关注', '普通']

  return (
    <Card>
      <CardHeader className='flex flex-row items-start justify-between gap-4'>
        <div>
          <CardTitle>{t('On-call board')}</CardTitle>
          <CardDescription>
            {t('Claim before acting, close with a note, so the next shift can pick up')}
          </CardDescription>
        </div>
        <Button
          variant='ghost'
          size='sm'
          onClick={() => setShowClosed((v) => !v)}
        >
          {showClosed ? t('Hide closed') : t('Show closed')}
        </Button>
      </CardHeader>
      <CardContent className='space-y-3'>
        {/* 交接条：谁在值班、从几点开始、上一班留下了什么话。
            没有这一行，「接手」按钮只是把 owner 改掉，
            下一班看不出前一个人做到哪一步。 */}
        <div className='bg-muted/40 flex flex-wrap items-center gap-2 rounded-lg px-3 py-2 text-xs'>
          {shift.data?.current ? (
            <span>
              {t('On shift')}:{' '}
              <strong>{shift.data.current.owner_name}</strong>{' '}
              <span className='text-muted-foreground'>
                {t('since')} {clock(shift.data.current.started_at)}
              </span>
            </span>
          ) : (
            <span className='text-muted-foreground'>{t('Nobody on shift')}</span>
          )}
          <input
            className='border-input bg-background ml-auto h-7 w-56 rounded-md border px-2 text-xs'
            placeholder={t('Handover note for the next shift')}
            value={note}
            onChange={(e) => setNote(e.target.value)}
          />
          <Button
            size='sm'
            variant='secondary'
            disabled={takeShift.isPending}
            onClick={() => takeShift.mutate()}
          >
            {t('Take shift')}
          </Button>
        </div>

        {/* 上一班的留言：接班后最该先看的东西 */}
        {(() => {
          const closed = (shift.data?.shifts ?? []).find((s) => s.ended_at > 0)
          if (!closed?.handover_note) return null
          return (
            <div className='bg-muted/30 rounded-lg px-3 py-2 text-xs'>
              <span className='text-muted-foreground'>
                {closed.owner_name} · {clock(closed.started_at)}–
                {clock(closed.ended_at)}:
              </span>{' '}
              {closed.handover_note}
            </div>
          )
        })()}

        <div className='flex gap-2'>
          <input
            className='border-input bg-background h-9 flex-1 rounded-md border px-3 text-sm'
            placeholder={t('Open an incident, e.g. gpt-4o 5xx on all channels')}
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && title.trim()) create.mutate()
            }}
          />
          <Button
            size='sm'
            disabled={!title.trim() || create.isPending}
            onClick={() => create.mutate()}
          >
            {t('Open')}
          </Button>
        </div>

        {items.length === 0 ? (
          <p className='text-muted-foreground py-6 text-center text-sm'>
            {t('Nothing open. Since')} {clock(Math.floor(Date.now() / 1000))}
          </p>
        ) : (
          items.map((inc) => (
            <div
              key={inc.id}
              className='flex items-start gap-3 rounded-lg border p-3 text-sm'
            >
              <Badge
                variant={inc.severity === 1 ? 'destructive' : 'secondary'}
                className='shrink-0'
              >
                {severityTone[inc.severity] ?? '普通'}
              </Badge>
              <div className='min-w-0 flex-1'>
                <p className='truncate font-medium'>{inc.title}</p>
                <p className='text-muted-foreground text-xs'>
                  {clock(inc.started_at)}
                  {inc.owner_name ? ` · ${inc.owner_name}` : ''}
                  {inc.resolution ? ` · ${inc.resolution}` : ''}
                </p>
              </div>
              {inc.status === 'open' && (
                <Button
                  size='sm'
                  variant='outline'
                  disabled={claim.isPending}
                  onClick={() => claim.mutate(inc.id)}
                >
                  {t('Claim')}
                </Button>
              )}
              {inc.status === 'claimed' && (
                <>
                  {/* 交接：不是自己名下的事项也能接手，
                      否则上一班下班后这条只能挂到第二天。 */}
                  <Button
                    size='sm'
                    variant='ghost'
                    className='text-xs'
                    disabled={takeOver.isPending}
                    onClick={() => takeOver.mutate(inc.id)}
                  >
                    {t('Take over')}
                  </Button>
                  <Button
                    size='sm'
                    variant='outline'
                    disabled={close.isPending}
                    onClick={() =>
                      close.mutate({ id: inc.id, resolution: t('Resolved') })
                    }
                  >
                    {t('Resolve')}
                  </Button>
                </>
              )}
              {inc.status === 'resolved' && (
                <Badge variant='outline'>{t('Resolved')}</Badge>
              )}
            </div>
          ))
        )}
      </CardContent>
    </Card>
  )
}
