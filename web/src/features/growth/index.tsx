import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ListPager } from '@/components/list-pager'
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
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { useDebounce } from '@/hooks'
import { formatQuotaWithCurrency } from '@/lib/currency'
import { DEFAULT_PAGE_SIZE } from '@/lib/pagination'

import { RetentionBoard } from './components/retention/retention-board'
import {
  createGrowthBatch,
  createGrowthTouch,
  getGrowthAlerts,
  getGrowthBatches,
  getGrowthModels,
  getGrowthOverview,
  getGrowthProfile,
  getGrowthSettings,
  getGrowthUsers,
  recomputeGrowth,
  stopGrowthBatch,
  updateGrowthAlertStatus,
  updateGrowthSettings,
  updateGrowthTier,
} from './api'
import type {
  GrowthAlertItem,
  GrowthBatch,
  GrowthBoardRow,
  GrowthModelStat,
  GrowthProfile,
  GrowthSettings,
} from './types'

const TIERS = ['K', 'A', 'B', 'S'] as const

// 小额消耗用两位小数会被抹成 0，首屏最重要的数字显示为零等于没数据。
// 低于 1 美元给四位小数，其余保持两位。
function fmtUsd(v: number): string {
  if (!v) return '0.00'
  return v < 1 ? v.toFixed(4) : v.toFixed(2)
}

function tierVariant(tier: string) {
  switch (tier) {
    case 'K':
      return 'default' as const
    case 'A':
      return 'secondary' as const
    case 'B':
      return 'outline' as const
    default:
      return 'ghost' as const
  }
}

function severityVariant(severity: number) {
  if (severity === 1) return 'destructive' as const
  if (severity === 2) return 'warning' as const
  return 'outline' as const
}

function formatDate(ts: number) {
  if (!ts) return '-'
  return new Date(ts * 1000).toLocaleDateString()
}

function formatDateTime(ts: number) {
  if (!ts) return '-'
  return new Date(ts * 1000).toLocaleString()
}

function trendLabel(trend: string) {
  switch (trend) {
    case 'up':
      return '↑'
    case 'down':
      return '↓'
    case 'flat':
      return '→'
    default:
      return '-'
  }
}

// 查询 key 统一收在这里：失效（invalidate）时按前缀清，
// 写死字符串很容易漏掉某一个，然后出现「点了但页面没变」。
const growthKeys = {
  overview: ['growth', 'overview'] as const,
  alerts: ['growth', 'alerts'] as const,
  users: ['growth', 'users'] as const,
  models: ['growth', 'models'] as const,
  batches: ['growth', 'batches'] as const,
  settings: ['growth', 'settings'] as const,
  profile: (userId: number) => ['growth', 'profile', userId] as const,
}

export function Growth() {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const [tab, setTab] = useState('today')
  const [selectedUserId, setSelectedUserId] = useState<number | null>(null)
  const [profileOpen, setProfileOpen] = useState(false)
  const [rulesOpen, setRulesOpen] = useState(false)

  // 档案不再占一个页签：它必须先选人才能看，做成 tab 永远是空页。
  // 改成抽屉后，从今日队列和分层表点进来都是同一条路径。
  const openProfile = useCallback((userId: number) => {
    setSelectedUserId(userId)
    setProfileOpen(true)
  }, [])

  // 重算是一次全量聚合，耗时以秒计。按钮必须锁住并给回执：
  // 没有 pending 态时运营会连点，几个全表扫描叠在一起，
  // 接着整个看板的请求都在后面排队——现象就是「页面卡死」。
  const recompute = useMutation({
    mutationFn: recomputeGrowth,
    onSuccess: (result) => {
      if (result?.already_running) {
        toast.info(t('A recompute is already running.'))
      } else {
        toast.success(t('Recompute started. Numbers update in a few minutes.'))
      }
      // 聚合在后台跑。这里只标脏、不立刻重取：重算还没结束就抓，
      // 拿到的还是旧数，等于在数据库最忙的时候再叠 5 个全量查询。
      void qc.invalidateQueries({ queryKey: ['growth'], refetchType: 'none' })
    },
    onError: () => toast.error(t('Failed to start recompute')),
  })

  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>{t('Growth')}</SectionPageLayout.Title>
      <SectionPageLayout.Actions>
        <Button
          variant='outline'
          size='sm'
          disabled={recompute.isPending}
          onClick={() => recompute.mutate()}
        >
          {recompute.isPending ? t('Recomputing') : t('Recompute')}
        </Button>
        <Button variant='outline' size='sm' onClick={() => setRulesOpen(true)}>
          {t('Rules')}
        </Button>
      </SectionPageLayout.Actions>
      <SectionPageLayout.Content>
        <Tabs value={tab} onValueChange={(value) => setTab(String(value))}>
          {/* 页签要跟着内容滚：切换到「客户」后页面很长，
              滚到一半想换页签不该先滚回顶部。 */}
          <TabsList className='bg-background sticky top-0 z-10'>
            <TabsTrigger value='today'>{t('Today')}</TabsTrigger>
            <TabsTrigger value='segments'>{t('Customers')}</TabsTrigger>
            <TabsTrigger value='batches'>{t('Reach')}</TabsTrigger>
          </TabsList>
          <TabsContent value='today'>
            <TodayPanel onOpenProfile={openProfile} />
          </TabsContent>
          <TabsContent value='segments'>
            <SegmentPanel onOpenProfile={openProfile} />
            <div className='mt-3'>
              <ModelShareCard />
            </div>
            <div className='mt-3'>
              <RetentionBoard />
            </div>
          </TabsContent>

          <TabsContent value='batches'>
            <BatchPanel />
          </TabsContent>
        </Tabs>

        <Sheet open={profileOpen} onOpenChange={setProfileOpen}>
          <SheetContent className='w-full overflow-y-auto sm:max-w-2xl'>
            <SheetHeader>
              <SheetTitle>{t('Account')}</SheetTitle>
              <SheetDescription>
                {t('Business events and every manual touch')}
              </SheetDescription>
            </SheetHeader>
            <div className='mt-4'>
              <ProfilePanel userId={selectedUserId} />
            </div>
          </SheetContent>
        </Sheet>

        <Sheet open={rulesOpen} onOpenChange={setRulesOpen}>
          <SheetContent className='w-full overflow-y-auto sm:max-w-xl'>
            <SheetHeader>
              <SheetTitle>{t('Rules')}</SheetTitle>
              <SheetDescription>
                {t('Tier and alert thresholds')}
              </SheetDescription>
            </SheetHeader>
            <div className='mt-4'>
              <RulePanel />
            </div>
          </SheetContent>
        </Sheet>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}

function TodayPanel({
  onOpenProfile,
}: {
  onOpenProfile: (userId: number) => void
}) {
  const { t } = useTranslation()
  const qc = useQueryClient()

  const alerts = useQuery({
    queryKey: growthKeys.alerts,
    queryFn: () => getGrowthAlerts(),
    staleTime: 30_000,
  })
  const overview = useQuery({
    queryKey: growthKeys.overview,
    queryFn: getGrowthOverview,
    staleTime: 60_000,
  })

  const items = alerts.data?.items ?? []
  const stats = alerts.data?.stats ?? []
  const followUps = alerts.data?.follow_ups ?? []
  const ov = overview.data ?? null

  const mark = useMutation({
    mutationFn: (args: { item: GrowthAlertItem; status: 'done' | 'ignore' }) =>
      updateGrowthAlertStatus(args.item.user_id, args.item.kind, args.status),
    onSuccess: () => {
      // 标记之后队列和统计会变；客户列表的「最近触达」也变了。
      // 概览指标不随预警状态动，别为一次点击再拉一遍全量聚合。
      void qc.invalidateQueries({ queryKey: growthKeys.alerts })
      void qc.invalidateQueries({ queryKey: growthKeys.users })
    },
    onError: () => toast.error(t('Failed to update the item')),
  })

  const total = stats.reduce((acc, s) => acc + s.total, 0)
  const resolved = stats.reduce((acc, s) => acc + s.resolved, 0)

  return (
    <div className='space-y-3'>
      {ov && (
        <Card>
          <CardContent className='flex flex-wrap items-end justify-between gap-4 py-5'>
            <div>
              <p className='text-muted-foreground text-xs'>
                {t('Weekly billed consumption')}
              </p>
              <p className='mt-1 text-3xl font-semibold tabular-nums'>
                ${fmtUsd(ov.weekly_billed_usd)}
              </p>
              <p className='text-muted-foreground mt-1 text-xs'>
                {t('Last week same period')} $
                {fmtUsd(ov.prev_week_billed_usd)}
              </p>
            </div>
            <div className='text-right'>
              <p
                className={`text-lg font-medium tabular-nums ${
                  ov.week_over_week_pct > 0
                    ? 'text-emerald-600'
                    : ov.week_over_week_pct < 0
                      ? 'text-destructive'
                      : 'text-muted-foreground'
                }`}
              >
                {ov.week_over_week_pct > 0 ? '+' : ''}
                {ov.week_over_week_pct.toFixed(1)}%
              </p>
              <p className='text-muted-foreground text-xs'>
                {t('Week over week')}
              </p>
            </div>
          </CardContent>
        </Card>
      )}

      <div className='grid gap-3 sm:grid-cols-4'>
        <MetricCard label={t('Pending')} value={String(items.length)} />
        <MetricCard label={t('Triggered')} value={String(total)} />
        <MetricCard label={t('Resolved')} value={String(resolved)} />
        <MetricCard label={t('Due follow-ups')} value={String(followUps.length)} />
      </div>
      {followUps.length > 0 && (
        <Card>
          <CardHeader>
            <CardTitle>{t('Due follow-ups')}</CardTitle>
            <CardDescription>
              {t('Promises made earlier that are now due')}
            </CardDescription>
          </CardHeader>
          <CardContent className='space-y-2'>
            {followUps.map((touch) => (
              <div
                key={touch.id}
                className='flex flex-wrap items-start justify-between gap-2 rounded-lg border border-warning/40 bg-warning/5 p-3'
              >
                <div className='min-w-0 flex-1 space-y-1'>
                  <div className='flex items-center gap-2'>
                    <button
                      type='button'
                      className='text-sm font-medium hover:underline'
                      onClick={() => onOpenProfile(touch.user_id)}
                    >
                      #{touch.user_id}
                    </button>
                    <span className='text-muted-foreground text-xs'>
                      {t('Due')} {formatDate(touch.follow_up_at)}
                    </span>
                  </div>
                  <p className='text-muted-foreground text-xs leading-relaxed'>
                    {touch.feedback}
                  </p>
                </div>
                <Button
                  size='sm'
                  variant='outline'
                  onClick={() => onOpenProfile(touch.user_id)}
                >
                  {t('Record result')}
                </Button>
              </div>
            ))}
          </CardContent>
        </Card>
      )}
      <Card>
        <CardHeader>
          <CardTitle>{t('Action queue')}</CardTitle>
          <CardDescription>
            {t('Sorted by potential loss, not by trigger time')}
          </CardDescription>
        </CardHeader>
        <CardContent className='space-y-2'>
          {alerts.isPending && (
            <p className='text-muted-foreground text-sm'>{t('Loading')}</p>
          )}
          {alerts.isError && (
            <p className='text-destructive text-sm'>
              {t('Failed to load the queue')}
            </p>
          )}
          {!alerts.isPending && items.length === 0 && (
            <p className='text-muted-foreground text-sm'>
              {t('No pending items')}
            </p>
          )}
          {items.map((item) => (
            <div
              key={`${item.user_id}-${item.kind}`}
              className='flex flex-wrap items-start justify-between gap-3 rounded-lg border p-3'
            >
              <div className='min-w-0 flex-1 space-y-1'>
                <div className='flex flex-wrap items-center gap-2'>
                  <Badge variant={severityVariant(item.severity)}>
                    {item.severity === 1
                      ? t('Urgent')
                      : item.severity === 2
                        ? t('Watch')
                        : t('Normal')}
                  </Badge>
                  <button
                    type='button'
                    className='text-sm font-medium hover:underline'
                    onClick={() => onOpenProfile(item.user_id)}
                  >
                    {item.display_name || item.username || `#${item.user_id}`}
                  </button>
                  {item.tier && (
                    <Badge variant={tierVariant(item.tier)}>{item.tier}</Badge>
                  )}
                  <span className='text-muted-foreground text-xs'>
                    {t(item.kind)}
                  </span>
                </div>
                <p className='text-muted-foreground text-xs leading-relaxed'>
                  {item.detail}
                </p>
                <p className='text-muted-foreground text-xs'>
                  {t('Balance')}: {formatQuotaWithCurrency(item.quota)}
                </p>
              </div>
              <div className='flex shrink-0 items-center gap-2'>
                <Button
                  size='sm'
                  variant='outline'
                  onClick={() => onOpenProfile(item.user_id)}
                >
                  {t('Open')}
                </Button>
                <Button
                  size='sm'
                  disabled={mark.isPending}
                  onClick={() => mark.mutate({ item, status: 'done' })}
                >
                  {t('Handled')}
                </Button>
                <Button
                  size='sm'
                  variant='ghost'
                  disabled={mark.isPending}
                  onClick={() => mark.mutate({ item, status: 'ignore' })}
                >
                  {t('Ignore')}
                </Button>
              </div>
            </div>
          ))}
        </CardContent>
      </Card>
    </div>
  )
}

function SegmentPanel({
  onOpenProfile,
}: {
  onOpenProfile: (userId: number) => void
}) {
  const { t } = useTranslation()
  // 「第几页」和筛选条件放在同一个状态里：分开存就必须靠 effect 在筛选变化后
  // 把页码拉回第一页，而 effect 晚一拍——会先按旧页码发一次请求（p=2&keyword=新词），
  // 再按 p=1 发第二次。一次搜索两次全量聚合，白花一次。
  const [query, setQuery] = useState({
    page: 1,
    pageSize: DEFAULT_PAGE_SIZE,
    tier: '',
    keyword: '',
  })
  const [keywordInput, setKeywordInput] = useState('')
  // 每个按键都发请求 = 每个按键一次全量聚合。输入一个 8 字符的用户名
  // 会排队十几个请求，运营感受是「打字卡、翻页慢」。
  const debouncedKeyword = useDebounce(keywordInput.trim(), 300)

  useEffect(() => {
    setQuery((prev) =>
      prev.keyword === debouncedKeyword
        ? prev
        : { ...prev, keyword: debouncedKeyword, page: 1 }
    )
  }, [debouncedKeyword])

  const overview = useQuery({
    queryKey: growthKeys.overview,
    queryFn: getGrowthOverview,
    staleTime: 60_000,
  })

  const users = useQuery({
    queryKey: [...growthKeys.users, query],
    queryFn: () => getGrowthUsers(query),
    // 翻页时保留上一页的行：清空成骨架屏再填回来，视觉上像页面卡了一下。
    placeholderData: keepPreviousData,
    staleTime: 30_000,
  })

  const rows: GrowthBoardRow[] = users.data?.items ?? []
  const total = users.data?.total ?? 0
  const maxPages = Math.max(1, Math.ceil(total / query.pageSize))
  const currentPage = Math.min(query.page, maxPages)

  const stale = useMemo(() => {
    const freshAt = overview.data?.data_fresh_at
    if (!freshAt) return true
    return Date.now() / 1000 - freshAt > 7200
  }, [overview.data])

  return (
    <div className='space-y-3'>
      {stale && (
        <div className='rounded-lg border border-warning/40 bg-warning/10 p-3 text-xs'>
          {t(
            'Growth data may be stale. Check that data export is enabled, then recompute.'
          )}
        </div>
      )}
      <div className='grid gap-3 sm:grid-cols-2 lg:grid-cols-4'>
        <MetricCard
          label={t('Weekly active users')}
          value={String(overview.data?.week_active_users ?? 0)}
        />
        <MetricCard
          label={t('Monthly active users')}
          value={String(overview.data?.month_active_users ?? 0)}
        />
        <MetricCard
          label={t('Top 10 revenue share')}
          value={`${(overview.data?.top10_share ?? 0).toFixed(1)}%`}
        />
        <MetricCard
          label={t('Paying users')}
          value={String(overview.data?.paying_users ?? 0)}
        />
      </div>

      <Card>
        <CardHeader className='flex flex-wrap items-center justify-between gap-2'>
          <div>
            <CardTitle>{t('Users')}</CardTitle>
            <CardDescription>
              {t('Click a row to open the account timeline')}
            </CardDescription>
          </div>
          <div className='flex flex-wrap items-center gap-2'>
            <Input
              className='h-8 w-40'
              placeholder={t('Search username')}
              value={keywordInput}
              onChange={(e) => setKeywordInput(e.target.value)}
            />
            <div className='flex gap-1'>
              <Button
                size='sm'
                variant={query.tier === '' ? 'default' : 'outline'}
                onClick={() =>
                  setQuery((prev) => ({ ...prev, tier: '', page: 1 }))
                }
              >
                {t('All')}
              </Button>
              {TIERS.map((item) => (
                <Button
                  key={item}
                  size='sm'
                  variant={query.tier === item ? 'default' : 'outline'}
                  onClick={() =>
                    setQuery((prev) => ({ ...prev, tier: item, page: 1 }))
                  }
                >
                  {item}
                </Button>
              ))}
            </div>
          </div>
        </CardHeader>
        <CardContent className='overflow-x-auto'>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t('Tier')}</TableHead>
                <TableHead>{t('Username')}</TableHead>
                <TableHead className='text-right'>{t('30-day usage')}</TableHead>
                <TableHead className='text-right'>{t('Trend')}</TableHead>
                <TableHead className='text-right'>{t('Requests')}</TableHead>
                <TableHead className='text-right'>{t('Failed')}</TableHead>
                <TableHead className='text-right'>{t('Balance')}</TableHead>
                <TableHead>{t('Last active')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {users.isPending &&
                Array.from({ length: 5 }, (_, index) => (
                  <TableRow key={`skeleton-${index}`}>
                    <TableCell colSpan={8}>
                      <Skeleton className='h-5 w-full' />
                    </TableCell>
                  </TableRow>
                ))}
              {rows.map((row) => (
                <TableRow
                  key={row.user_id}
                  className='cursor-pointer'
                  onClick={() => onOpenProfile(row.user_id)}
                >
                  <TableCell>
                    <Badge variant={tierVariant(row.manual_tier || row.tier)}>
                      {row.manual_tier || row.tier || '-'}
                    </Badge>
                  </TableCell>
                  <TableCell>
                    <div className='flex flex-col'>
                      <span className='text-sm'>
                        {row.display_name || row.username}
                      </span>
                      <span className='text-muted-foreground text-xs'>
                        #{row.user_id}
                      </span>
                    </div>
                  </TableCell>
                  <TableCell className='text-right tabular-nums'>
                    {formatQuotaWithCurrency(row.quota_30d)}
                  </TableCell>
                  <TableCell className='text-right'>
                    {trendLabel(row.trend)}
                  </TableCell>
                  <TableCell className='text-right tabular-nums'>
                    {row.requests_30d}
                  </TableCell>
                  <TableCell className='text-right tabular-nums'>
                    {row.failures_7d}
                  </TableCell>
                  <TableCell className='text-right tabular-nums'>
                    {formatQuotaWithCurrency(row.balance ?? 0)}
                  </TableCell>
                  <TableCell className='text-xs'>
                    {formatDate(row.last_active_at)}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
          <ListPager
            className='mt-3'
            page={currentPage}
            pageSize={query.pageSize}
            total={total}
            totalLabel={t('users')}
            onPageChange={(next) =>
              setQuery((prev) => ({ ...prev, page: next }))
            }
            onPageSizeChange={(size) =>
              setQuery((prev) => ({ ...prev, pageSize: size, page: 1 }))
            }
          />
        </CardContent>
      </Card>
    </div>
  )
}

function ProfilePanel({ userId }: { userId: number | null }) {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const [method, setMethod] = useState('phone')
  const [contactRole, setContactRole] = useState('decision')
  const [feedback, setFeedback] = useState('')
  const [conclusion, setConclusion] = useState('follow_up')
  const [followUpAt, setFollowUpAt] = useState('')

  const profileQuery = useQuery({
    queryKey: growthKeys.profile(userId ?? 0),
    queryFn: () => getGrowthProfile(userId as number),
    enabled: !!userId,
    staleTime: 30_000,
  })

  const invalidateProfile = () => {
    if (!userId) return
    void qc.invalidateQueries({ queryKey: growthKeys.profile(userId) })
    void qc.invalidateQueries({ queryKey: growthKeys.alerts })
    void qc.invalidateQueries({ queryKey: growthKeys.users })
  }

  const save = useMutation({
    mutationFn: async () => {
      if (!userId) return
      await createGrowthTouch(userId, {
        method,
        contact_role: contactRole,
        feedback,
        conclusion,
        follow_up_at: followUpAt
          ? Math.floor(new Date(followUpAt).getTime() / 1000)
          : 0,
      })
    },
    onSuccess: () => {
      setFeedback('')
      setFollowUpAt('')
      invalidateProfile()
    },
    onError: () => toast.error(t('Failed to save the record')),
  })

  const setTier = useMutation({
    mutationFn: (value: string) =>
      updateGrowthTier(userId as number, value, value ? 'manual' : ''),
    onSuccess: invalidateProfile,
    onError: () => toast.error(t('Failed to update the tier')),
  })

  if (!userId) {
    return (
      <Card>
        <CardContent className='text-muted-foreground py-10 text-center text-sm'>
          {t('Pick a user from the segments tab first')}
        </CardContent>
      </Card>
    )
  }

  const profile: GrowthProfile | null = profileQuery.data ?? null
  const stat = profile?.stat

  return (
    <div className='space-y-3'>
      <Card>
        <CardHeader>
          <CardTitle>
            {profile?.user.display_name ||
              profile?.user.username ||
              `#${userId}`}
          </CardTitle>
          <CardDescription>
            #{userId} · {t('Registered')} {formatDate(profile?.user.created_at ?? 0)}
            {stat && ` · ${t('Tier')} ${stat.manual_tier || stat.tier || '-'}`}
          </CardDescription>
        </CardHeader>
        <CardContent className='grid gap-3 sm:grid-cols-4'>
          <MetricCard
            label={t('30-day usage')}
            value={formatQuotaWithCurrency(stat?.quota_30d ?? 0)}
          />
          <MetricCard
            label={t('90-day usage')}
            value={formatQuotaWithCurrency(stat?.quota_90d ?? 0)}
          />
          <MetricCard
            label={t('Lifetime top-up')}
            value={formatQuotaWithCurrency(stat?.topup_total ?? 0)}
          />
          <MetricCard
            label={t('Last active')}
            value={formatDate(stat?.last_active_at ?? 0)}
          />
        </CardContent>
      </Card>

      <div className='grid gap-3 lg:grid-cols-2'>
        <Card>
          <CardHeader>
            <CardTitle>{t('Timeline')}</CardTitle>
            <CardDescription>
              {t('Business events and every manual touch')}
            </CardDescription>
          </CardHeader>
          <CardContent className='max-h-[420px] space-y-3 overflow-y-auto'>
            {(profile?.timeline ?? []).map((event, index) => (
              <div key={`${event.at}-${index}`} className='border-l-2 pl-3'>
                <p className='text-muted-foreground text-xs'>
                  {formatDateTime(event.at)}
                </p>
                <p className='text-sm font-medium'>{event.title}</p>
                {event.detail && (
                  <p className='text-muted-foreground text-xs leading-relaxed'>
                    {event.detail}
                  </p>
                )}
              </div>
            ))}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>{t('Log a touch')}</CardTitle>
            <CardDescription>
              {t('Not recorded means not happened')}
            </CardDescription>
          </CardHeader>
          <CardContent className='space-y-2'>
            <div className='flex flex-wrap gap-1'>
              {[
                ['phone', t('Phone')],
                ['wechat', t('WeChat')],
                ['email', t('Email')],
              ].map(([value, label]) => (
                <Button
                  key={value}
                  size='sm'
                  variant={method === value ? 'default' : 'outline'}
                  onClick={() => setMethod(value)}
                >
                  {label}
                </Button>
              ))}
            </div>
            <div className='flex flex-wrap gap-1'>
              {[
                ['decision', t('Decision maker')],
                ['tech', t('Engineer')],
              ].map(([value, label]) => (
                <Button
                  key={value}
                  size='sm'
                  variant={contactRole === value ? 'default' : 'outline'}
                  onClick={() => setContactRole(value)}
                >
                  {label}
                </Button>
              ))}
            </div>
            <textarea
              className='border-input bg-background min-h-24 w-full rounded-md border p-2 text-sm'
              placeholder={t('What did they actually say')}
              value={feedback}
              onChange={(e) => setFeedback(e.target.value)}
            />
            <div className='flex flex-wrap gap-1'>
              {[
                ['renew', t('Renew')],
                ['follow_up', t('Follow up')],
                ['churn', t('Churn')],
              ].map(([value, label]) => (
                <Button
                  key={value}
                  size='sm'
                  variant={conclusion === value ? 'default' : 'outline'}
                  onClick={() => setConclusion(value)}
                >
                  {label}
                </Button>
              ))}
            </div>
            <Input
              type='date'
              value={followUpAt}
              onChange={(e) => setFollowUpAt(e.target.value)}
            />
            <Button
              className='w-full'
              disabled={save.isPending || !feedback.trim()}
              onClick={() => save.mutate()}
            >
              {save.isPending
                ? t('Saving')
                : t('Save and schedule follow-up')}
            </Button>
          </CardContent>
        </Card>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>{t('Touch history')}</CardTitle>
        </CardHeader>
        <CardContent className='overflow-x-auto'>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t('Date')}</TableHead>
                <TableHead>{t('Method')}</TableHead>
                <TableHead>{t('Operator')}</TableHead>
                <TableHead>{t('Feedback')}</TableHead>
                <TableHead>{t('Conclusion')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {(profile?.touches ?? []).map((touch) => (
                <TableRow key={touch.id}>
                  <TableCell className='text-xs'>
                    {formatDate(touch.created_at)}
                  </TableCell>
                  <TableCell className='text-xs'>{touch.method}</TableCell>
                  <TableCell className='text-xs'>
                    {touch.operator_name}
                  </TableCell>
                  <TableCell className='max-w-[420px] text-xs'>
                    {touch.feedback}
                  </TableCell>
                  <TableCell className='text-xs'>
                    {touch.conclusion}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{t('Manual tier override')}</CardTitle>
          <CardDescription>
            {t('Machine tiers misjudge POCs and seasonal accounts')}
          </CardDescription>
        </CardHeader>
        <CardContent className='flex flex-wrap gap-2'>
          {['K', 'A', 'B', 'S'].map((tier) => (
            <Button
              key={tier}
              size='sm'
              variant='outline'
              disabled={setTier.isPending}
              onClick={() => setTier.mutate(tier)}
            >
              {t('Set')} {tier}
            </Button>
          ))}
          <Button
            size='sm'
            variant='ghost'
            disabled={setTier.isPending}
            onClick={() => setTier.mutate('')}
          >
            {t('Clear override')}
          </Button>
        </CardContent>
      </Card>
    </div>
  )
}

function BatchPanel() {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const [name, setName] = useState('')
  const [tier, setTier] = useState('A')
  const [amount, setAmount] = useState('20')
  const [windowDays, setWindowDays] = useState('14')

  const batches = useQuery({
    queryKey: growthKeys.batches,
    queryFn: getGrowthBatches,
    staleTime: 30_000,
  })

  const invalidate = () =>
    void qc.invalidateQueries({ queryKey: growthKeys.batches })

  const create = useMutation({
    mutationFn: () =>
      createGrowthBatch({
        name: name || `Batch ${tier}`,
        criteria: `tier=${tier}`,
        benefit_type: 'quota_credit',
        benefit_amount: Number(amount) || 0,
        window_days: Number(windowDays) || 14,
        tier,
      }),
    onSuccess: () => {
      setName('')
      invalidate()
    },
    onError: () => toast.error(t('Failed to create the batch')),
  })

  const stop = useMutation({
    mutationFn: (id: number) => stopGrowthBatch(id),
    onSuccess: invalidate,
    onError: () => toast.error(t('Failed to stop the batch')),
  })

  const rows: GrowthBatch[] = batches.data ?? []

  return (
    <div className='space-y-3'>
      <Card>
        <CardHeader>
          <CardTitle>{t('Batches')}</CardTitle>
          <CardDescription>
            {t('ROI = incremental usage divided by benefit cost')}
          </CardDescription>
        </CardHeader>
        <CardContent className='overflow-x-auto'>
          {batches.isPending && (
            <div className='space-y-2'>
              {Array.from({ length: 3 }, (_, index) => (
                <Skeleton key={index} className='h-8 w-full' />
              ))}
            </div>
          )}
          {!batches.isPending && rows.length === 0 && (
            <p className='text-muted-foreground py-6 text-center text-sm'>
              {t('No batches yet. Create one below to start measuring.')}
            </p>
          )}
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t('Batch')}</TableHead>
                <TableHead>{t('Name')}</TableHead>
                <TableHead className='text-right'>{t('Users')}</TableHead>
                <TableHead className='text-right'>{t('Cost')}</TableHead>
                <TableHead className='text-right'>
                  {t('Incremental usage')}
                </TableHead>
                <TableHead className='text-right'>ROI</TableHead>
                <TableHead>{t('Status')}</TableHead>
                <TableHead className='w-20' />
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((batch) => (
                <TableRow key={batch.id}>
                  <TableCell className='font-mono text-xs'>
                    {batch.batch_no}
                  </TableCell>
                  <TableCell className='text-sm'>{batch.name}</TableCell>
                  <TableCell className='text-right tabular-nums'>
                    {batch.target_count}
                  </TableCell>
                  <TableCell className='text-right tabular-nums'>
                    {formatQuotaWithCurrency(batch.cost)}
                  </TableCell>
                  <TableCell className='text-right tabular-nums'>
                    {formatQuotaWithCurrency(batch.incremental_quota)}
                  </TableCell>
                  <TableCell
                    className={`text-right tabular-nums ${
                      batch.roi < 1 && batch.status === 'settled'
                        ? 'text-destructive'
                        : ''
                    }`}
                  >
                    {batch.roi.toFixed(2)}
                  </TableCell>
                  <TableCell className='text-xs'>{batch.status}</TableCell>
                  <TableCell>
                    {batch.status === 'running' && (
                      <Button
                        size='sm'
                        variant='outline'
                        disabled={stop.isPending}
                        onClick={() => stop.mutate(batch.id)}
                      >
                        {t('Stop')}
                      </Button>
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{t('New batch')}</CardTitle>
          <CardDescription>
            {t('Snapshots the audience so the result can be attributed')}
          </CardDescription>
        </CardHeader>
        <CardContent className='flex flex-wrap items-end gap-2'>
          <div className='flex flex-col gap-1'>
            <span className='text-muted-foreground text-xs'>{t('Name')}</span>
            <Input
              className='h-8 w-44'
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </div>
          <div className='flex flex-col gap-1'>
            <span className='text-muted-foreground text-xs'>{t('Tier')}</span>
            <div className='flex gap-1'>
              {TIERS.map((item) => (
                <Button
                  key={item}
                  size='sm'
                  variant={tier === item ? 'default' : 'outline'}
                  onClick={() => setTier(item)}
                >
                  {item}
                </Button>
              ))}
            </div>
          </div>
          <div className='flex flex-col gap-1'>
            <span className='text-muted-foreground text-xs'>{t('Amount')}</span>
            <Input
              className='h-8 w-24'
              value={amount}
              onChange={(e) => setAmount(e.target.value)}
            />
          </div>
          <div className='flex flex-col gap-1'>
            <span className='text-muted-foreground text-xs'>
              {t('Window (days)')}
            </span>
            <Input
              className='h-8 w-24'
              value={windowDays}
              onChange={(e) => setWindowDays(e.target.value)}
            />
          </div>
          <Button disabled={create.isPending} onClick={() => create.mutate()}>
            {t('Create')}
          </Button>
        </CardContent>
      </Card>
    </div>
  )
}

function RulePanel() {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const [draft, setDraft] = useState<GrowthSettings | null>(null)
  const [savedAt, setSavedAt] = useState(0)

  const settings = useQuery({
    queryKey: growthKeys.settings,
    queryFn: getGrowthSettings,
    staleTime: 60_000,
  })

  // 表单用本地副本：直接绑定查询结果的话，后台一刷新就会把正在编辑的数字冲掉。
  useEffect(() => {
    if (settings.data) setDraft(settings.data)
  }, [settings.data])

  const save = useMutation({
    mutationFn: (body: GrowthSettings) => updateGrowthSettings(body),
    onSuccess: (next) => {
      setDraft(next)
      setSavedAt(Date.now())
      void qc.invalidateQueries({ queryKey: growthKeys.settings })
    },
    onError: () => toast.error(t('Failed to save the rules')),
  })

  const recompute = useMutation({
    mutationFn: recomputeGrowth,
    onSuccess: (result) =>
      result?.already_running
        ? toast.info(t('A recompute is already running.'))
        : toast.success(
            t('Recompute started. Numbers update in a few minutes.')
          ),
    onError: () => toast.error(t('Failed to start recompute')),
  })

  if (!draft) {
    return (
      <Card>
        <CardContent className='text-muted-foreground py-8 text-center text-sm'>
          {t('Loading')}
        </CardContent>
      </Card>
    )
  }

  const patch = (key: keyof GrowthSettings, value: number) => {
    setDraft((prev) => (prev ? { ...prev, [key]: value } : prev))
  }

  const rows: Array<{
    key: keyof GrowthSettings
    label: string
    hint: string
    unit: string
    step?: number
  }> = [
    {
      key: 'tier_key_percent',
      label: t('K tier percentile'),
      hint: t('Share of active accounts counted as key accounts'),
      unit: '%',
    },
    {
      key: 'tier_active_percent',
      label: t('A tier percentile'),
      hint: t('Cumulative share covering key and growth accounts'),
      unit: '%',
    },
    {
      key: 'tier_key_cap',
      label: t('K tier cap'),
      hint: t('Hard ceiling so the key tier stays small enough to know by name'),
      unit: t('people'),
    },
    {
      key: 'silent_days',
      label: t('Silent threshold'),
      hint: t('No successful call for this many days marks the account silent'),
      unit: t('days'),
    },
    {
      key: 'balance_warn_days',
      label: t('Balance warning window'),
      hint: t('Warn when the remaining balance covers fewer days than this'),
      unit: t('days'),
      step: 0.5,
    },
    {
      key: 'incident_rate_pct',
      label: t('Incident failure rate'),
      hint: t('Failure share above this becomes an incident alert'),
      unit: '%',
    },
    {
      key: 'decline_pct',
      label: t('Decline threshold'),
      hint: t('Week-over-week drop that counts as declining'),
      unit: '%',
    },
    {
      key: 'dormant_days',
      label: t('Dormant days'),
      hint: t('No successful call for this many days after signup becomes dormant'),
      unit: t('days'),
    },
  ]

  return (
    <div className='space-y-3'>
      <Card>
        <CardHeader>
          <CardTitle>{t('Tier and alert thresholds')}</CardTitle>
          <CardDescription>
            {t(
              'Saved to the options table and applied on the next recompute. Values outside a safe range are clamped.'
            )}
          </CardDescription>
        </CardHeader>
        <CardContent className='space-y-1'>
          {rows.map((row) => (
            <div
              key={row.key}
              className='flex flex-wrap items-center gap-3 border-b py-2 last:border-b-0'
            >
              <div className='min-w-0 flex-1'>
                <p className='text-sm font-medium'>{row.label}</p>
                <p className='text-muted-foreground text-xs'>{row.hint}</p>
              </div>
              <Input
                type='number'
                step={row.step ?? 1}
                className='h-8 w-24 text-right'
                value={String(draft[row.key])}
                onChange={(e) => patch(row.key, Number(e.target.value))}
              />
              <span className='text-muted-foreground w-10 text-xs'>
                {row.unit}
              </span>
            </div>
          ))}
          <div className='flex items-center gap-3 pt-3'>
            <Button
              disabled={save.isPending}
              onClick={() => save.mutate(draft)}
            >
              {save.isPending ? t('Saving') : t('Save')}
            </Button>
            {savedAt > 0 && (
              <span className='text-muted-foreground text-xs'>
                {t('Saved. Recompute to apply.')}
              </span>
            )}
            <Button
              variant='outline'
              disabled={recompute.isPending}
              onClick={() => recompute.mutate()}
            >
              {t('Recompute now')}
            </Button>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{t('How this board is computed')}</CardTitle>
          <CardDescription>
            {t('Derived data only. Nothing here touches billing or routing.')}
          </CardDescription>
        </CardHeader>
        <CardContent className='text-muted-foreground space-y-2 text-sm leading-relaxed'>
          <p>
            {t(
              'Usage and request counts come from quota_data; failures come from logs because failed calls produce no consumption.'
            )}
          </p>
          <p>
            {t(
              'Accounts with no successful call inside the silent threshold are excluded from ranking.'
            )}
          </p>
          <p>
            {t(
              'The board is rebuilt on a schedule. Manual overrides and alert states survive the rebuild.'
            )}
          </p>
        </CardContent>
      </Card>
    </div>
  )
}

function ModelShareCard() {
  const { t } = useTranslation()
  const models = useQuery({
    queryKey: growthKeys.models,
    queryFn: getGrowthModels,
    staleTime: 5 * 60_000,
  })

  const rows: GrowthModelStat[] = models.data ?? []
  if (rows.length === 0) return null
  const max = Math.max(...rows.map((r) => r.share_pct), 1)

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('Model mix (30 days)')}</CardTitle>
        <CardDescription>
          {t('Consumption share, distinct users and request count per model')}
        </CardDescription>
      </CardHeader>
      <CardContent className='space-y-2'>
        {rows.slice(0, 8).map((r) => (
          <div key={r.model_name} className='flex items-center gap-3 text-sm'>
            <span className='w-44 truncate font-mono text-xs'>{r.model_name}</span>
            <div className='bg-muted h-2 flex-1 overflow-hidden rounded-full'>
              <div
                className='bg-primary h-full rounded-full'
                style={{ width: `${(r.share_pct / max) * 100}%` }}
              />
            </div>
            <span className='text-muted-foreground w-14 text-right text-xs tabular-nums'>
              {r.share_pct.toFixed(1)}%
            </span>
            <span className='text-muted-foreground w-16 text-right text-xs tabular-nums'>
              {r.users}
            </span>
            <span className='text-muted-foreground w-14 text-right text-xs tabular-nums'>
              {r.requests}
            </span>
          </div>
        ))}
      </CardContent>
    </Card>
  )
}

function MetricCard({ label, value }: { label: string; value: string }) {
  return (
    <div className='rounded-lg border p-3'>
      <p className='text-muted-foreground text-xs'>{label}</p>
      <p className='mt-1 text-lg font-semibold tabular-nums'>{value}</p>
    </div>
  )
}
