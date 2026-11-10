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
import { Check, ChevronDown, ChevronUp, Info, Loader2 } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

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
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { api } from '@/lib/api'
import { handleServerError } from '@/lib/handle-server-error'
import { requireServerSuccess } from '@/lib/server-error-message'

import { updateSystemOptionsBatch } from '../apiduo-option-batch'
import { getSystemOptions } from '../api'

const OPTION_KEY = 'observability_setting'
const WEEKDAYS = [1, 2, 3, 4, 5, 6, 0] // Monday first, 0 = Sunday

type RuleDraft = {
  models: string
  days: number[]
  start: string
  end: string
  exclude: boolean
  metadataRate: number
  contentRate: number
}

type Draft = {
  metadataRate: number
  contentRate: number
  parentBased: boolean
  timezone: string
  viewerIds: string
  rules: RuleDraft[]
}

type Payload = {
  metadata_sample_rate: number
  content_sample_rate: number
  parent_based_sampling: boolean
  timezone: string
  trace_viewer_user_ids: number[]
  rules: Array<{
    models: string[]
    days: number[]
    start: string
    end: string
    exclude: boolean
    metadata_rate: number
    content_rate: number
  }>
}

type TraceConfig = {
  enabled: boolean
  host?: string
  body_mode?: string
  metadata_sample_rate?: number
  content_sample_rate?: number
  server_zone?: string
  server_time?: string
  parent_based_sampling?: boolean
}

/**
 * Starting points for the two rates.
 *
 * They exist because the honest answers to "how much should I sample" are four,
 * not a continuous range: every request, every request with a fraction of the
 * bodies, links only, or nothing. Picking one is a two-second decision; typing
 * two numbers and guessing what they imply is not. Applying a preset only fills
 * the form — saving stays an explicit act, because this page edits production.
 */
const PRESETS = [
  { id: 'full', metadata: 100, content: 100 },
  { id: 'balanced', metadata: 100, content: 10 },
  { id: 'metadata', metadata: 100, content: 0 },
  { id: 'quiet', metadata: 0, content: 0 },
] as const

const PRESET_LABELS: Record<(typeof PRESETS)[number]['id'], string> = {
  full: 'Full capture',
  balanced: 'Links for all, bodies for 10%',
  metadata: 'Metadata only',
  quiet: 'Pause tracing',
}

const TIMEZONE_SHORTCUTS = [
  { label: 'Server local', value: '' },
  { label: 'Asia/Shanghai', value: 'Asia/Shanghai' },
  { label: 'UTC', value: 'UTC' },
] as const

const EMPTY_DRAFT: Draft = {
  metadataRate: 100,
  contentRate: 100,
  parentBased: true,
  timezone: '',
  viewerIds: '',
  rules: [],
}

function clampPercent(value: string): number {
  const parsed = Number(value)
  if (!Number.isFinite(parsed)) return 0
  return Math.min(100, Math.max(0, Math.round(parsed)))
}

function toDraft(raw: unknown): Draft {
  if (!raw || typeof raw !== 'object') return { ...EMPTY_DRAFT }
  const value = raw as Partial<Payload>
  return {
    metadataRate: value.metadata_sample_rate ?? 100,
    contentRate: value.content_sample_rate ?? 100,
    parentBased: value.parent_based_sampling ?? true,
    timezone: value.timezone ?? '',
    viewerIds: (value.trace_viewer_user_ids ?? []).join(', '),
    rules: (value.rules ?? []).map((rule) => ({
      models: (rule.models ?? []).join(', '),
      days: rule.days ?? [],
      start: rule.start ?? '',
      end: rule.end ?? '',
      exclude: Boolean(rule.exclude),
      metadataRate: rule.metadata_rate ?? 0,
      contentRate: rule.content_rate ?? 0,
    })),
  }
}

function toPayload(draft: Draft): Payload {
  return {
    metadata_sample_rate: clampPercent(String(draft.metadataRate)),
    content_sample_rate: clampPercent(String(draft.contentRate)),
    parent_based_sampling: draft.parentBased,
    timezone: draft.timezone.trim(),
    trace_viewer_user_ids: draft.viewerIds
      .split(',')
      .map((id) => Number(id.trim()))
      .filter((id) => Number.isInteger(id) && id > 0),
    rules: draft.rules.map((rule) => ({
      models: rule.models
        .split(',')
        .map((model) => model.trim())
        .filter(Boolean),
      days: rule.days,
      start: rule.start,
      end: rule.end,
      exclude: rule.exclude,
      metadata_rate: clampPercent(String(rule.metadataRate)),
      content_rate: clampPercent(String(rule.contentRate)),
    })),
  }
}

/** Deep compare so "changed it back" clears the dirty flag. */
function sameDraft(a: Draft, b: Draft): boolean {
  return JSON.stringify(toPayload(a)) === JSON.stringify(toPayload(b))
}

export function ObservabilitySection() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [draft, setDraft] = useState<Draft | null>(null)

  // The option system stores one row per struct field ("module.field"), not a
  // single JSON blob, so the draft is assembled from the individual keys. Posting
  // a blob under "observability_setting" would land in the option map but never
  // reach the registered config, silently doing nothing.
  const optionsQuery = useQuery({
    queryKey: ['system-options', OPTION_KEY],
    queryFn: async () => {
      const body = await getSystemOptions()
      const values = new Map(
        (body.data ?? []).map((item) => [item.key, item.value])
      )
      const readJSON = (key: string): unknown => {
        const raw = values.get(key)
        if (!raw) return undefined
        try {
          return JSON.parse(raw)
        } catch {
          return undefined
        }
      }
      return toDraft({
        metadata_sample_rate: Number(
          values.get(`${OPTION_KEY}.metadata_sample_rate`) ?? 100
        ),
        content_sample_rate: Number(
          values.get(`${OPTION_KEY}.content_sample_rate`) ?? 100
        ),
        parent_based_sampling:
          values.get(`${OPTION_KEY}.parent_based_sampling`) !== 'false',
        timezone: values.get(`${OPTION_KEY}.timezone`) ?? '',
        rules: readJSON(`${OPTION_KEY}.rules`) ?? [],
        trace_viewer_user_ids:
          readJSON(`${OPTION_KEY}.trace_viewer_user_ids`) ?? [],
      } as Payload)
    },
    staleTime: 30 * 1000,
  })

  const traceConfig = useQuery({
    queryKey: ['apiduo-trace-config-zone'],
    queryFn: async () => {
      try {
        const res = await api.get<{ success: boolean; data?: TraceConfig }>(
          '/api/apiduo/trace/config'
        )
        return res.data?.data ?? null
      } catch {
        return null
      }
    },
    staleTime: 60 * 1000,
    retry: false,
  })

  const current = draft ?? optionsQuery.data ?? EMPTY_DRAFT
  const saved = optionsQuery.data ?? EMPTY_DRAFT
  const dirty = draft !== null && !sameDraft(draft, saved)

  const save = useMutation({
    mutationFn: async (payload: Payload) => {
      // One transaction for the whole form. Sending the six fields one by one
      // would commit them one by one too: a value the backend rejects in the
      // middle (a rule with half a time window) would leave the rest applied —
      // new rates against old rules is worse than a refused save.
      const options: Record<string, string> = {
        [`${OPTION_KEY}.metadata_sample_rate`]: String(
          payload.metadata_sample_rate
        ),
        [`${OPTION_KEY}.content_sample_rate`]: String(
          payload.content_sample_rate
        ),
        [`${OPTION_KEY}.parent_based_sampling`]: String(
          payload.parent_based_sampling
        ),
        [`${OPTION_KEY}.timezone`]: payload.timezone,
        [`${OPTION_KEY}.rules`]: JSON.stringify(payload.rules),
        [`${OPTION_KEY}.trace_viewer_user_ids`]: JSON.stringify(
          payload.trace_viewer_user_ids
        ),
      }
      await requireServerSuccess(await updateSystemOptionsBatch(options))
    },
    onSuccess: async () => {
      toast.success(t('Saved. Sampling changes apply immediately.'))
      setDraft(null)
      await queryClient.invalidateQueries({ queryKey: ['system-options'] })
      await queryClient.invalidateQueries({
        queryKey: ['apiduo-trace-config-zone'],
      })
    },
    onError: (error) => handleServerError(error),
  })

  const update = (patch: Partial<Draft>) =>
    setDraft((prev) => ({ ...(prev ?? current), ...patch }))

  const moveRule = (index: number, delta: number) =>
    setDraft((prev) => {
      const base = prev ?? current
      const target = index + delta
      if (target < 0 || target >= base.rules.length) return base
      const rules = [...base.rules]
      ;[rules[index], rules[target]] = [rules[target], rules[index]]
      return { ...base, rules }
    })

  const updateRule = (index: number, patch: Partial<RuleDraft>) =>
    setDraft((prev) => {
      const base = prev ?? current
      const rules = base.rules.map((rule, i) =>
        i === index ? { ...rule, ...patch } : rule
      )
      return { ...base, rules }
    })

  const status = traceConfig.data
  const zone = status?.server_zone ?? t('server local zone')
  const zoneTime = status?.server_time ?? ''
  const runningMeta = status?.metadata_sample_rate
  const runningContent = status?.content_sample_rate

  const bodyModeLabel = useMemo(() => {
    switch (status?.body_mode) {
      case 'off':
        return t('No body')
      case 'full':
        return t('full payload')
      case 'headtail':
        return t('head + tail')
      default:
        return '—'
    }
  }, [status?.body_mode, t])

  const ruleSummary = useMemo(() => {
    if (current.rules.length === 0)
      return t('No rules yet — defaults apply to every request.')
    return t('{{count}} rule(s), evaluated top to bottom, first match wins', {
      count: current.rules.length,
    })
  }, [current.rules.length, t])

  const activePreset = PRESETS.find(
    (preset) =>
      preset.metadata === current.metadataRate &&
      preset.content === current.contentRate
  )

  // A one-ended window is not "from 09:00 onwards" — the backend drops both
  // ends and the rule then matches all day. Submitting that would show a saved
  // value in the form while the runtime ignored it, so it is refused here with
  // the rule number instead of being silently repaired.
  const halfWindowedRule = current.rules.findIndex(
    (rule) => Boolean(rule.start.trim()) !== Boolean(rule.end.trim())
  )

  // Do not render an editable form over placeholder values. EMPTY_DRAFT is
  // 100/100 while the real rows may be 10/100: one keystroke before the query
  // resolves would make 100/100 the base of the save and silently raise every
  // other field. A spinner costs a moment; a wrong save costs an incident.
  if (optionsQuery.data === undefined) {
    return (
      <div className='text-muted-foreground flex items-center gap-2 p-6 text-sm'>
        {optionsQuery.isError ? (
          <>
            <Info className='size-4' />
            {t('Cannot read the current settings. Reload the page to retry.')}
          </>
        ) : (
          <>
            <Loader2 className='size-4 animate-spin' />
            {t('Loading settings...')}
          </>
        )}
      </div>
    )
  }

  return (
    <div className='space-y-6'>
      {/* What is actually running. Without this strip the page can be edited
          while nothing is exported at all (LANGFUSE_ENABLED is off), and every
          number below would look applied while being inert. */}
      <Card>
        <CardHeader>
          <CardTitle className='flex items-center gap-2'>
            {t('Current export')}
            <Badge variant={status?.enabled ? 'default' : 'secondary'}>
              {status?.enabled ? t('enabled') : t('disabled')}
            </Badge>
          </CardTitle>
          <CardDescription>
            {status?.enabled
              ? t(
                  'Requests that pass sampling are exported to the tracing backend below.'
                )
              : t(
                  'Tracing is off because LANGFUSE_ENABLED is not set in the environment. Sampling below has no effect until it is turned on and the service restarted.'
                )}
          </CardDescription>
        </CardHeader>
        <CardContent>
          <dl className='grid gap-3 text-sm sm:grid-cols-2 lg:grid-cols-4'>
            <div className='space-y-1'>
              <dt className='text-muted-foreground text-xs'>
                {t('Export endpoint')}
              </dt>
              <dd className='font-mono text-xs break-all'>
                {status?.host || '—'}
              </dd>
            </div>
            <div className='space-y-1'>
              <dt className='text-muted-foreground text-xs'>
                {t('Captured body')}
              </dt>
              <dd>{bodyModeLabel}</dd>
            </div>
            <div className='space-y-1'>
              <dt className='text-muted-foreground text-xs'>
                {t('Default when no rule matches')}
              </dt>
              <dd className='tabular-nums'>
                {runningMeta == null
                  ? '—'
                  : t('{{meta}}% links · {{content}}% content', {
                      meta: runningMeta,
                      content: runningContent ?? 0,
                    })}
              </dd>
            </div>
            <div className='space-y-1'>
              <dt className='text-muted-foreground text-xs'>
                {t('Rule clock')}
              </dt>
              <dd className='tabular-nums'>
                {zone}
                {zoneTime ? ` · ${zoneTime.slice(11, 16)}` : ''}
              </dd>
            </div>
          </dl>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{t('Trace sampling')}</CardTitle>
          <CardDescription>
            {t(
              'Metadata decides whether a request produces a trace at all — that is also what makes the log page link work. Content decides whether that trace carries the prompt and answer. Both default to 100%.'
            )}
          </CardDescription>
        </CardHeader>
        <CardContent className='space-y-5'>
          <div className='flex flex-wrap items-center gap-2'>
            <span className='text-muted-foreground mr-1 text-xs'>
              {t('Quick presets')}
            </span>
            {PRESETS.map((preset) => {
              const active = activePreset?.id === preset.id
              return (
                <Button
                  key={preset.id}
                  type='button'
                  size='sm'
                  variant={active ? 'default' : 'outline'}
                  className='h-7 gap-1 px-2.5 text-xs'
                  onClick={() =>
                    update({
                      metadataRate: preset.metadata,
                      contentRate: preset.content,
                    })
                  }
                >
                  {active ? <Check className='size-3' /> : null}
                  {t(PRESET_LABELS[preset.id])}
                </Button>
              )
            })}
          </div>

          <div className='grid gap-5 sm:grid-cols-2'>
            <div className='space-y-2'>
              <Label>{t('Metadata sampling (%)')}</Label>
              <Input
                type='number'
                min={0}
                max={100}
                aria-label={t('Metadata sampling (%)')}
                value={current.metadataRate}
                onChange={(e) =>
                  update({ metadataRate: clampPercent(e.target.value) })
                }
              />
              <p className='text-muted-foreground text-xs'>
                {t('Share of requests that appear in the tracing backend.')}
              </p>
            </div>
            <div className='space-y-2'>
              <Label>{t('Content sampling (%)')}</Label>
              <Input
                type='number'
                min={0}
                max={100}
                aria-label={t('Content sampling (%)')}
                value={current.contentRate}
                onChange={(e) =>
                  update({ contentRate: clampPercent(e.target.value) })
                }
              />
              <p className='text-muted-foreground text-xs'>
                {t(
                  'Share of traced requests that include prompt and answer. Metadata-only spans stay linkable.'
                )}
              </p>
            </div>
          </div>

          {current.metadataRate === 0 ? (
            <p className='flex items-start gap-2 rounded-lg border border-amber-500/30 bg-amber-500/5 p-3 text-xs text-amber-700 dark:text-amber-300'>
              <Info className='mt-0.5 size-3.5 shrink-0' />
              {t(
                'At 0% the log page shows no trace links at all. Keep this only while you deliberately want tracing to stop without touching the deployment.'
              )}
            </p>
          ) : null}

          {current.metadataRate > 0 && current.contentRate === 0 ? (
            <p className='bg-muted/40 flex items-start gap-2 rounded-lg border p-3 text-xs'>
              <Info className='mt-0.5 size-3.5 shrink-0' />
              {t(
                'Every trace will carry timing, model, token usage and status, but no prompt or answer. This is the compliance-safe setting.'
              )}
            </p>
          ) : null}

          <div className='flex items-start justify-between gap-4 rounded-lg border p-3'>
            <div className='space-y-1'>
              <Label>{t('Follow client traceparent')}</Label>
              <p className='text-muted-foreground text-xs'>
                {t(
                  'When an agent sends a traceparent, honour its sampled bit instead of drawing locally, so one trace stays one tree.'
                )}
              </p>
            </div>
            <Switch
              aria-label={t('Follow client traceparent')}
              checked={current.parentBased}
              onCheckedChange={(checked) => update({ parentBased: checked })}
            />
          </div>

          <div className='grid gap-5 sm:grid-cols-2'>
            <div className='space-y-2'>
              <Label>{t('Schedule timezone')}</Label>
              <Input
                placeholder='Asia/Shanghai'
                aria-label={t('Schedule timezone')}
                value={current.timezone}
                onChange={(e) => update({ timezone: e.target.value })}
              />
              <div className='flex flex-wrap items-center gap-1.5'>
                {TIMEZONE_SHORTCUTS.map((item) => {
                  const active = current.timezone === item.value
                  return (
                    <Button
                      key={item.label}
                      type='button'
                      size='sm'
                      variant={active ? 'secondary' : 'ghost'}
                      className='h-6 px-2 text-xs'
                      onClick={() => update({ timezone: item.value })}
                    >
                      {t(item.label)}
                    </Button>
                  )
                })}
              </div>
              <p className='text-muted-foreground text-xs'>
                {t('Empty = server local zone.')}{' '}
                {t('Rules are read in {{zone}} now {{time}}.', {
                  zone,
                  time: zoneTime ? zoneTime.slice(11, 16) : '—',
                })}
              </p>
            </div>
            <div className='space-y-2'>
              <Label>{t('Trace visibility')}</Label>
              <div className='flex items-center gap-2'>
                <Badge variant='secondary'>{t('root only by default')}</Badge>
                {current.viewerIds.trim() ? (
                  <Badge variant='outline'>{t('extra users allowed')}</Badge>
                ) : null}
              </div>
              <Input
                placeholder={t(
                  'Administrator user IDs, comma separated (optional)'
                )}
                aria-label={t('Trace visibility')}
                value={current.viewerIds}
                onChange={(e) => update({ viewerIds: e.target.value })}
              />
              <p className='text-muted-foreground text-xs'>
                {t(
                  'Root always sees traces. Other users need to be listed here; leaving it empty keeps root-only access.'
                )}
              </p>
            </div>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{t('Sampling rules')}</CardTitle>
          <CardDescription>
            {t(
              'Rules narrow sampling by model, weekday and time window. Conditions in one rule are ANDed; the list is evaluated top to bottom and the first match wins. Leave a field empty to ignore it.'
            )}
          </CardDescription>
        </CardHeader>
        <CardContent className='space-y-4'>
          <p className='text-muted-foreground text-xs'>{ruleSummary}</p>

          {current.rules.map((rule, index) => (
            <div key={index} className='space-y-3 rounded-lg border p-3'>
              <div className='flex items-center justify-between'>
                <span className='text-muted-foreground text-xs'>
                  {t('Rule {{index}}', { index: index + 1 })}
                </span>
                {/* Order is the priority: the first match wins, so an operator
                    must be able to move a rule without deleting and retyping
                    it. */}
                <div className='flex items-center gap-1'>
                  <Button
                    type='button'
                    size='icon'
                    variant='ghost'
                    className='size-6'
                    aria-label={t('Move rule up')}
                    disabled={index === 0}
                    onClick={() => moveRule(index, -1)}
                  >
                    <ChevronUp className='size-3.5' />
                  </Button>
                  <Button
                    type='button'
                    size='icon'
                    variant='ghost'
                    className='size-6'
                    aria-label={t('Move rule down')}
                    disabled={index === current.rules.length - 1}
                    onClick={() => moveRule(index, 1)}
                  >
                    <ChevronDown className='size-3.5' />
                  </Button>
                </div>
              </div>
              <div className='grid gap-3 sm:grid-cols-2'>
                <div className='space-y-1'>
                  <Label className='text-xs'>
                    {t('Models (comma separated)')}
                  </Label>
                  <Input
                    placeholder='gpt-4o-mini, doubao-*'
                    aria-label={t('Models (comma separated)')}
                    value={rule.models}
                    onChange={(e) =>
                      updateRule(index, { models: e.target.value })
                    }
                  />
                </div>
                <div className='space-y-1'>
                  <Label className='text-xs'>{t('Time window (HH:MM)')}</Label>
                  <div className='flex items-center gap-2'>
                    <Input
                      placeholder='09:00'
                      aria-label={t('Window start')}
                      value={rule.start}
                      onChange={(e) =>
                        updateRule(index, { start: e.target.value })
                      }
                    />
                    <span className='text-muted-foreground text-xs'>–</span>
                    <Input
                      placeholder='18:00'
                      aria-label={t('Window end')}
                      value={rule.end}
                      onChange={(e) =>
                        updateRule(index, { end: e.target.value })
                      }
                    />
                  </div>
                </div>
              </div>

              <div className='flex flex-wrap items-center gap-1.5'>
                <span className='text-muted-foreground mr-1 text-xs'>
                  {t('Days')}
                </span>
                {WEEKDAYS.map((day) => {
                  const active = rule.days.includes(day)
                  return (
                    <Button
                      key={day}
                      type='button'
                      size='sm'
                      variant={active ? 'default' : 'outline'}
                      className='h-7 px-2 text-xs'
                      onClick={() =>
                        updateRule(index, {
                          days: active
                            ? rule.days.filter((d) => d !== day)
                            : [...rule.days, day],
                        })
                      }
                    >
                      {t(['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'][day])}
                    </Button>
                  )
                })}
              </div>

              <div className='grid gap-3 sm:grid-cols-3'>
                <div className='space-y-1'>
                  <Label className='text-xs'>{t('Metadata (%)')}</Label>
                  <Input
                    type='number'
                    min={0}
                    max={100}
                    aria-label={t('Rule metadata sampling (%)')}
                    value={rule.metadataRate}
                    onChange={(e) =>
                      updateRule(index, {
                        metadataRate: clampPercent(e.target.value),
                      })
                    }
                  />
                </div>
                <div className='space-y-1'>
                  <Label className='text-xs'>{t('Content (%)')}</Label>
                  <Input
                    type='number'
                    min={0}
                    max={100}
                    aria-label={t('Rule content sampling (%)')}
                    value={rule.contentRate}
                    onChange={(e) =>
                      updateRule(index, {
                        contentRate: clampPercent(e.target.value),
                      })
                    }
                  />
                </div>
                <div className='flex items-end justify-between gap-2'>
                  <div className='flex items-center gap-2'>
                    <Switch
                      aria-label={t('Never trace')}
                      checked={rule.exclude}
                      onCheckedChange={(checked) =>
                        updateRule(index, { exclude: checked })
                      }
                    />
                    <Label className='text-xs'>{t('Never trace')}</Label>
                  </div>
                  <Button
                    type='button'
                    size='sm'
                    variant='ghost'
                    onClick={() =>
                      setDraft((prev) => {
                        const base = prev ?? current
                        return {
                          ...base,
                          rules: base.rules.filter((_, i) => i !== index),
                        }
                      })
                    }
                  >
                    {t('Remove')}
                  </Button>
                </div>
              </div>
            </div>
          ))}

          <Button
            type='button'
            variant='outline'
            size='sm'
            onClick={() =>
              setDraft((prev) => ({
                ...(prev ?? current),
                rules: [
                  ...(prev ?? current).rules,
                  {
                    models: '',
                    days: [],
                    start: '',
                    end: '',
                    exclude: false,
                    metadataRate: 100,
                    contentRate: 100,
                  },
                ],
              }))
            }
          >
            {t('Add rule')}
          </Button>
        </CardContent>
      </Card>

      <div className='flex flex-wrap items-center gap-3'>
        <Button
          onClick={() => {
            if (halfWindowedRule >= 0) {
              toast.error(
                t(
                  'Rule {{index}}: fill both ends of the time window, or neither.',
                  { index: halfWindowedRule + 1 }
                )
              )
              return
            }
            save.mutate(toPayload(current))
          }}
          disabled={save.isPending}
        >
          {save.isPending ? t('Saving...') : t('Save')}
        </Button>
        {dirty ? (
          <Button
            variant='ghost'
            onClick={() => setDraft(null)}
            disabled={save.isPending}
          >
            {t('Discard changes')}
          </Button>
        ) : null}
        <span className='text-muted-foreground text-xs'>
          {dirty
            ? t('Unsaved changes')
            : t('Changes apply within one flush interval; no restart needed.')}
        </span>
      </div>
    </div>
  )
}
