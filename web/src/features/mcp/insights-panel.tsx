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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Loader2 } from 'lucide-react'
import { useMemo } from 'react'
import { toast } from 'sonner'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { cn } from '@/lib/utils'

import { mcpApi, mcpKeys } from './api'
import { useMcpCopy } from './copy'
import type { AuditLevel, AuditSuggestion, ObservationRow } from './types'

type PanelProps = { profileId: number | null; modelId: string | null }

function Spinner() {
  return (
    <div className='text-muted-foreground flex items-center gap-2 text-xs'>
      <Loader2 className='size-3.5 animate-spin' />
    </div>
  )
}

function EmptyHint({ text }: { text: string }) {
  return (
    <Card>
      <CardContent className='text-muted-foreground py-10 text-center text-sm'>
        {text}
      </CardContent>
    </Card>
  )
}

/** 观察记录：按参数分组的成功/被拒分布。 */
export function ObservationsPanel(props: PanelProps) {
  const t = useMcpCopy()
  const query = useQuery({
    queryKey: mcpKeys.observations(props.profileId ?? 0),
    queryFn: () => mcpApi.observations(props.profileId as number),
    enabled: props.profileId !== null,
    retry: false,
  })

  const groups = useMemo(() => {
    const stats = query.data?.stats ?? []
    const byParam = new Map<string, ObservationRow[]>()
    for (const row of stats) {
      const list = byParam.get(row.param) ?? []
      list.push(row)
      byParam.set(row.param, list)
    }
    return [...byParam.entries()]
  }, [query.data])

  if (props.profileId === null) {
    return <EmptyHint text={t('Pick a model first')} />
  }

  const renderRows = (rows: ObservationRow[]) => {
    const max = Math.max(...rows.map((r) => r.ok + r.fail), 1)
    return rows.map((r) => (
      <div key={`${r.param}-${r.value}`} className='flex items-center gap-3'>
        <span className='w-32 shrink-0 truncate font-mono text-[11px]'>
          {r.value}
        </span>
        <span className='bg-muted flex h-2 min-w-0 flex-1 overflow-hidden rounded-full'>
          <span
            className='h-full bg-emerald-500/70'
            style={{ width: `${(r.ok / max) * 100}%` }}
          />
          <span
            className='bg-destructive/70 h-full'
            style={{ width: `${(r.fail / max) * 100}%` }}
          />
        </span>
        <span className='text-muted-foreground w-28 shrink-0 text-right text-[11px]'>
          {r.ok} ok
          {r.fail > 0
            ? ` · ${r.fail} ${t('Failed samples')}`
            : null}
        </span>
      </div>
    ))
  }

  const renderBody = () => {
    if (query.isLoading) return <Spinner />
    if (groups.length === 0) {
      return (
        <p className='text-muted-foreground text-xs'>
          {t(
            'No samples yet — the distribution appears once parameter snapshots reach the log store.'
          )}
        </p>
      )
    }
    return groups.map(([param, rows]) => (
      <div key={param} className='space-y-1.5'>
        <div className='text-xs font-medium'>{param}</div>
        {renderRows(rows)}
      </div>
    ))
  }

  return (
    <Card>
      <CardHeader className='pb-3'>
        <CardTitle className='font-mono text-sm'>{props.modelId}</CardTitle>
        <p className='text-muted-foreground text-xs'>
          {query.data
            ? `${t('{{count}} samples', { count: query.data.total })} · ${
                query.data.days
              }d`
            : null}
        </p>
      </CardHeader>
      <CardContent className='space-y-5'>{renderBody()}</CardContent>
    </Card>
  )
}

const LEVEL_ORDER: Record<AuditLevel, number> = {
  conflict: 0,
  pending: 1,
  unprobed: 2,
  ok: 3,
}

function levelLabel(level: AuditLevel, t: (key: string) => string): string {
  switch (level) {
    case 'conflict':
      return t('Conflict')
    case 'pending':
      return t('To confirm')
    case 'unprobed':
      return t('To verify')
    default:
      return t('Consistent')
  }
}

/**
 * 按采纳后会写成什么，拼一句悬浮说明。
 *
 * 按钮本身只能写「采纳」两个字 —— 写的是 max 还是枚举，决定了这次点击是
 * 「放宽到 8」还是「只留 1:1」，差别很大，必须让人点之前就看得到。
 */
function suggestionHint(suggestion: AuditSuggestion, label: string): string {
  if (suggestion.max !== undefined) {
    return `${label} · max=${suggestion.max}`
  }
  return `${label} · enum=${(suggestion.enum ?? []).join(' / ')}`
}

/** 巡检：声明与观察到的实际取值是否一致。 */
export function AuditPanel(props: PanelProps) {
  const t = useMcpCopy()
  const queryClient = useQueryClient()
  // 采纳：只把 param 发给后端，max / enum 由后端按当前样本重算。
  // 这样页面上展示的建议和真正写进库的值永远同源，不会出现「看到 8、写进 9」。
  const adopt = useMutation({
    mutationFn: (param: string) =>
      mcpApi.adopt(props.profileId as number, param),
    onSuccess: (res) => {
      const { param, max, enum: values } = res.applied
      const what = max !== undefined ? `max=${max}` : (values ?? []).join(' / ')
      toast.success(`${t('Adopted')} ${param}${what ? ` · ${what}` : ''}`)
      void queryClient.invalidateQueries({
        queryKey: mcpKeys.audit(props.profileId as number),
      })
      void queryClient.invalidateQueries({
        queryKey: mcpKeys.observations(props.profileId as number),
      })
      void queryClient.invalidateQueries({ queryKey: mcpKeys.profiles() })
    },
    onError: (error) =>
      toast.error(error instanceof Error ? error.message : String(error)),
  })
  const query = useQuery({
    queryKey: mcpKeys.audit(props.profileId ?? 0),
    queryFn: () => mcpApi.audit(props.profileId as number),
    enabled: props.profileId !== null,
    retry: false,
  })

  if (props.profileId === null) {
    return <EmptyHint text={t('Pick a model first')} />
  }

  const issues = [...(query.data?.issues ?? [])].sort(
    (a, b) => LEVEL_ORDER[a.level] - LEVEL_ORDER[b.level]
  )

  const renderBody = () => {
    if (query.isLoading) return <Spinner />
    if (issues.length === 0) {
      return (
        <p className='text-muted-foreground text-xs'>
          {t('No inconsistency found')}
        </p>
      )
    }
    return issues.map((issue) => (
      <div
        key={`${issue.level}-${issue.title}-${issue.object}`}
        className='flex items-start justify-between gap-3 rounded-md border px-3 py-2'
      >
        <div className='min-w-0'>
          <div className='text-xs font-medium'>{issue.title}</div>
          <div className='text-muted-foreground font-mono text-[11px]'>
            {issue.object}
          </div>
          <div className='text-muted-foreground mt-1 text-[11px]'>
            {issue.detail}
          </div>
        </div>
        <div className='flex shrink-0 flex-col items-end gap-1.5'>
          <Badge
            variant={issue.level === 'conflict' ? 'destructive' : 'secondary'}
            className={cn(
              'shrink-0',
              issue.level === 'ok' &&
                'border-emerald-500/40 text-emerald-600 dark:text-emerald-400'
            )}
          >
            {levelLabel(issue.level, t)}
          </Badge>
          {/* 只有后端给出结构化建议时才给按钮：没建议的条目需要人判断，
              放个灰按钮只会让人反复点。 */}
          {issue.suggestion && (
            <Button
              size='sm'
              variant='outline'
              className='h-6 px-2 text-[11px]'
              disabled={adopt.isPending}
              onClick={() => adopt.mutate(issue.suggestion!.param)}
              title={suggestionHint(issue.suggestion, t('Adopt'))}
            >
              {t('Adopt')}
            </Button>
          )}
        </div>
      </div>
    ))
  }

  return (
    <Card>
      <CardHeader className='pb-3'>
        <CardTitle className='font-mono text-sm'>{props.modelId}</CardTitle>
        <p className='text-muted-foreground text-xs'>
          {t('Inspection')} · {query.data?.days ?? 7}d
        </p>
      </CardHeader>
      <CardContent className='space-y-2'>{renderBody()}</CardContent>
    </Card>
  )
}
