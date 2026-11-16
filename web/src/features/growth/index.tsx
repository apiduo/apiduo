import { useCallback, useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { SectionPageLayout } from '@/components/layout'
import { RetentionBoard } from './components/retention/retention-board'
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
import { formatQuotaWithCurrency } from '@/lib/currency'

import {
  createGrowthBatch,
  createGrowthTouch,
  getGrowthModels,
  getGrowthSettings,
  stopGrowthBatch,
  updateGrowthSettings,
  getGrowthAlerts,
  getGrowthBatches,
  getGrowthOverview,
  getGrowthProfile,
  getGrowthUsers,
  recomputeGrowth,
  updateGrowthAlertStatus,
  updateGrowthTier,
} from './api'
import type {
  GrowthAlertItem,
  GrowthAlertStats,
  GrowthBatch,
  GrowthBoardRow,
  GrowthModelStat,
  GrowthOverview,
  GrowthProfile,
  GrowthSettings,
  GrowthTouch,
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

export function Growth() {
  const { t } = useTranslation()
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

  return (
    <SectionPageLayout fixedContent>
      <SectionPageLayout.Title>{t('Growth')}</SectionPageLayout.Title>
      <SectionPageLayout.Actions>
        <Button
          variant='outline'
          size='sm'
          onClick={() => {
            void recomputeGrowth()
          }}
        >
          {t('Recompute')}
        </Button>
        <Button variant='outline' size='sm' onClick={() => setRulesOpen(true)}>
          {t('Rules')}
        </Button>
      </SectionPageLayout.Actions>
      <SectionPageLayout.Content>
        <Tabs value={tab} onValueChange={(value) => setTab(String(value))}>
          <TabsList>
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
  const [items, setItems] = useState<GrowthAlertItem[]>([])
  const [stats, setStats] = useState<GrowthAlertStats[]>([])
  const [followUps, setFollowUps] = useState<GrowthTouch[]>([])
  const [overview, setOverview] = useState<GrowthOverview | null>(null)
  const [loading, setLoading] = useState(true)

  const reload = useCallback(async () => {
    setLoading(true)
    try {
      const [data, ov] = await Promise.all([
        getGrowthAlerts(),
        getGrowthOverview().catch(() => null),
      ])
      setOverview(ov)
      setItems(data.items)
      setStats(data.stats)
      setFollowUps(data.follow_ups ?? [])
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void reload()
  }, [reload])

  const handle = async (
    item: GrowthAlertItem,
    status: 'done' | 'ignore'
  ) => {
    await updateGrowthAlertStatus(item.user_id, item.kind, status)
    await reload()
  }

  const total = stats.reduce((acc, s) => acc + s.total, 0)
  const resolved = stats.reduce((acc, s) => acc + s.resolved, 0)

  return (
    <div className='space-y-3'>
      {overview && (
        <Card>
          <CardContent className='flex flex-wrap items-end justify-between gap-4 py-5'>
            <div>
              <p className='text-muted-foreground text-xs'>
                {t('Weekly billed consumption')}
              </p>
              <p className='mt-1 text-3xl font-semibold tabular-nums'>
                ${fmtUsd(overview.weekly_billed_usd)}
              </p>
              <p className='text-muted-foreground mt-1 text-xs'>
                {t('Last week same period')} $
                {fmtUsd(overview.prev_week_billed_usd)}
              </p>
            </div>
            <div className='text-right'>
              <p
                className={`text-lg font-medium tabular-nums ${
                  overview.week_over_week_pct > 0
                    ? 'text-emerald-600'
                    : overview.week_over_week_pct < 0
                      ? 'text-destructive'
                      : 'text-muted-foreground'
                }`}
              >
                {overview.week_over_week_pct > 0 ? '+' : ''}
                {overview.week_over_week_pct.toFixed(1)}%
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
          {loading && <p className='text-muted-foreground text-sm'>{t('Loading')}</p>}
          {!loading && items.length === 0 && (
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
                <Button size='sm' onClick={() => void handle(item, 'done')}>
                  {t('Handled')}
                </Button>
                <Button
                  size='sm'
                  variant='ghost'
                  onClick={() => void handle(item, 'ignore')}
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
  const [overview, setOverview] = useState<GrowthOverview | null>(null)
  const [rows, setRows] = useState<GrowthBoardRow[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [tier, setTier] = useState('')
  const [keyword, setKeyword] = useState('')
  const pageSize = 20

  const reload = useCallback(async () => {
    const [ov, list] = await Promise.all([
      getGrowthOverview(),
      getGrowthUsers({ page, pageSize, tier, keyword }),
    ])
    setOverview(ov)
    setRows(list.items)
    setTotal(list.total)
  }, [page, tier, keyword])

  useEffect(() => {
    void reload()
  }, [reload])

  const stale = useMemo(() => {
    if (!overview?.data_fresh_at) return true
    return Date.now() / 1000 - overview.data_fresh_at > 7200
  }, [overview])

  const maxPages = Math.max(1, Math.ceil(total / pageSize))

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
          value={String(overview?.week_active_users ?? 0)}
        />
        <MetricCard
          label={t('Monthly active users')}
          value={String(overview?.month_active_users ?? 0)}
        />
        <MetricCard
          label={t('Top 10 revenue share')}
          value={`${(overview?.top10_share ?? 0).toFixed(1)}%`}
        />
        <MetricCard
          label={t('Paying users')}
          value={String(overview?.paying_users ?? 0)}
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
              value={keyword}
              onChange={(e) => {
                setPage(1)
                setKeyword(e.target.value)
              }}
            />
            <div className='flex gap-1'>
              <Button
                size='sm'
                variant={tier === '' ? 'default' : 'outline'}
                onClick={() => {
                  setPage(1)
                  setTier('')
                }}
              >
                {t('All')}
              </Button>
              {TIERS.map((item) => (
                <Button
                  key={item}
                  size='sm'
                  variant={tier === item ? 'default' : 'outline'}
                  onClick={() => {
                    setPage(1)
                    setTier(item)
                  }}
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
          <div className='mt-3 flex items-center justify-between text-xs'>
            <span className='text-muted-foreground'>
              {total} {t('users')}
            </span>
            <div className='flex items-center gap-2'>
              <Button
                size='sm'
                variant='outline'
                disabled={page <= 1}
                onClick={() => setPage((p) => Math.max(1, p - 1))}
              >
                {t('Previous')}
              </Button>
              <span>
                {page} / {maxPages}
              </span>
              <Button
                size='sm'
                variant='outline'
                disabled={page >= maxPages}
                onClick={() => setPage((p) => p + 1)}
              >
                {t('Next')}
              </Button>
            </div>
          </div>
        </CardContent>
      </Card>
    </div>
  )
}

function ProfilePanel({ userId }: { userId: number | null }) {
  const { t } = useTranslation()
  const [profile, setProfile] = useState<GrowthProfile | null>(null)
  const [method, setMethod] = useState('phone')
  const [contactRole, setContactRole] = useState('decision')
  const [feedback, setFeedback] = useState('')
  const [conclusion, setConclusion] = useState('follow_up')
  const [followUpAt, setFollowUpAt] = useState('')
  const [saving, setSaving] = useState(false)

  const reload = useCallback(async () => {
    if (!userId) {
      setProfile(null)
      return
    }
    setProfile(await getGrowthProfile(userId))
  }, [userId])

  useEffect(() => {
    void reload()
  }, [reload])

  if (!userId) {
    return (
      <Card>
        <CardContent className='text-muted-foreground py-10 text-center text-sm'>
          {t('Pick a user from the segments tab first')}
        </CardContent>
      </Card>
    )
  }

  const submit = async () => {
    if (!feedback.trim()) return
    setSaving(true)
    try {
      await createGrowthTouch(userId, {
        method,
        contact_role: contactRole,
        feedback,
        conclusion,
        follow_up_at: followUpAt
          ? Math.floor(new Date(followUpAt).getTime() / 1000)
          : 0,
      })
      setFeedback('')
      setFollowUpAt('')
      await reload()
    } finally {
      setSaving(false)
    }
  }

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
              disabled={saving || !feedback.trim()}
              onClick={() => void submit()}
            >
              {saving ? t('Saving') : t('Save and schedule follow-up')}
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
              onClick={() => {
                void updateGrowthTier(userId, tier, 'manual').then(reload)
              }}
            >
              {t('Set')} {tier}
            </Button>
          ))}
          <Button
            size='sm'
            variant='ghost'
            onClick={() => {
              void updateGrowthTier(userId, '', '').then(reload)
            }}
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
  const [batches, setBatches] = useState<GrowthBatch[]>([])
  const [name, setName] = useState('')
  const [tier, setTier] = useState('A')
  const [amount, setAmount] = useState('20')
  const [windowDays, setWindowDays] = useState('14')

  const reload = useCallback(async () => {
    setBatches(await getGrowthBatches())
  }, [])

  useEffect(() => {
    void reload()
  }, [reload])

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
          {batches.length === 0 && (
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
              {batches.map((batch) => (
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
                        onClick={() => {
                          void stopGrowthBatch(batch.id).then(reload)
                        }}
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
          <Button
            onClick={() => {
              void createGrowthBatch({
                name: name || `Batch ${tier}`,
                criteria: `tier=${tier}`,
                benefit_type: 'quota_credit',
                benefit_amount: Number(amount) || 0,
                window_days: Number(windowDays) || 14,
                tier,
              }).then(reload)
            }}
          >
            {t('Create')}
          </Button>
        </CardContent>
      </Card>
    </div>
  )
}

function RulePanel() {
  const { t } = useTranslation()
  const [settings, setSettings] = useState<GrowthSettings | null>(null)
  const [saving, setSaving] = useState(false)
  const [savedAt, setSavedAt] = useState(0)

  useEffect(() => {
    void getGrowthSettings().then(setSettings)
  }, [])

  const patch = (key: keyof GrowthSettings, value: number) => {
    setSettings((prev) => (prev ? { ...prev, [key]: value } : prev))
  }

  const save = async () => {
    if (!settings) return
    setSaving(true)
    try {
      setSettings(await updateGrowthSettings(settings))
      setSavedAt(Date.now())
    } finally {
      setSaving(false)
    }
  }

  if (!settings) {
    return (
      <Card>
        <CardContent className='text-muted-foreground py-8 text-center text-sm'>
          {t('Loading')}
        </CardContent>
      </Card>
    )
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
                value={String(settings[row.key])}
                onChange={(e) => patch(row.key, Number(e.target.value))}
              />
              <span className='text-muted-foreground w-10 text-xs'>
                {row.unit}
              </span>
            </div>
          ))}
          <div className='flex items-center gap-3 pt-3'>
            <Button disabled={saving} onClick={() => void save()}>
              {saving ? t('Saving') : t('Save')}
            </Button>
            {savedAt > 0 && (
              <span className='text-muted-foreground text-xs'>
                {t('Saved. Recompute to apply.')}
              </span>
            )}
            <Button
              variant='outline'
              onClick={() => {
                void recomputeGrowth()
              }}
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
  const [rows, setRows] = useState<GrowthModelStat[]>([])

  useEffect(() => {
    void getGrowthModels()
      .then(setRows)
      .catch(() => setRows([]))
  }, [])

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
