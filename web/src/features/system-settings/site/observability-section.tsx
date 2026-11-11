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
import {
  Check,
  ChevronDown,
  ChevronUp,
  ExternalLink,
  Info,
  Loader2,
} from 'lucide-react'
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
  // Client-only identity for React keys. Rules have no server id, and keying
  // by the editable fields remounts the row on every keystroke — the input
  // loses focus mid-typing and the payload ends up with half a word.
  uid: number
  models: string
  days: number[]
  start: string
  end: string
  exclude: boolean
  metadataRate: number
  contentRate: number
}

type BodyModeId = 'off' | 'headtail' | 'full'

type Draft = {
  metadataRate: number
  contentRate: number
  parentBased: boolean
  timezone: string
  viewerIds: string
  bodyMode: BodyModeId
  headKB: number
  tailKB: number
  fullMB: number
  errorRate: number
  redact: boolean
  environment: string
  exportHost: string
  consoleURL: string
  rules: RuleDraft[]
}

type Payload = {
  metadata_sample_rate: number
  content_sample_rate: number
  parent_based_sampling: boolean
  timezone: string
  trace_viewer_user_ids: number[]
  body_mode: BodyModeId
  body_head_bytes: number
  body_tail_bytes: number
  body_full_bytes: number
  error_sample_rate: number
  redact: boolean
  environment: string
  export_host: string
  console_url: string
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
  /** Browser-facing base — what the drawer's Langfuse link opens. */
  host?: string
  /** What spans are actually POSTed to; may be an internal container name. */
  export_host?: string
  body_mode?: string
  body_head_bytes?: number
  body_tail_bytes?: number
  body_full_bytes?: number
  error_sample_rate?: number
  redact?: boolean
  environment?: string
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

/**
 * The three capture modes, in the order a reader thinks about them: nothing,
 * the useful ends, everything.
 *
 * The middle one is the default because an SSE stream is not information-
 * uniform: role, model and the first delta are at the front, finish_reason and
 * token usage at the back, and the middle is repeated text deltas.
 */
const BODY_MODES = [
  { id: 'off', label: 'No body' },
  { id: 'headtail', label: 'Head + tail' },
  { id: 'full', label: 'Full payload' },
] as const

/**
 * Total head+tail the console may ask for. Mirrors trace.MaxBodyWindowBytes:
 * the write API refuses anything above it, so a form that offered more would
 * only produce a save that cannot succeed.
 */
const MAX_WINDOW_KB = 128

/**
 * Bounds for the full-capture cap, mirroring trace.MinBodyFullBytes and
 * trace.MaxBodyFullBytes. Duplicated because the console cannot import a Go
 * constant; a test pins the pair so a backend change cannot leave this form
 * offering a save the server will refuse.
 */
const MIN_FULL_MB = 1
const MAX_FULL_MB = 16
const DEFAULT_FULL_MB = 2

/**
 * What this page deliberately cannot change.
 *
 * Three reasons, ordered by how much damage a wrong value does: credentials
 * (a bad host silently redirects every span, and the key must never be
 * readable through an admin form), pipeline resources (queue depth is a
 * channel capacity, the flush interval is a ticker — both are fixed when the
 * worker starts), and the master switch (a process that started with tracing
 * off never built the exporter that turning it on would need).
 */
const ENV_ONLY_KEYS = [
  { name: 'LANGFUSE_ENABLED', note: 'master switch' },
  { name: 'LANGFUSE_HOST', note: 'export endpoint' },
  {
    name: 'LANGFUSE_CONSOLE_URL',
    note: 'public link target when export uses an internal host',
  },
  { name: 'LANGFUSE_PUBLIC_KEY / SECRET_KEY', note: 'credentials' },
  { name: 'LANGFUSE_PROJECT_ID', note: 'console project for the jump link' },
  {
    name: 'LANGFUSE_QUEUE_META / QUEUE_CONTENT',
    note: 'queue depth, fixed at startup',
  },
  {
    name: 'LANGFUSE_FLUSH_INTERVAL_MS',
    note: 'batch window, fixed when the worker starts',
  },
  {
    name: 'LANGFUSE_TIMEOUT_SECONDS',
    note: 'export timeout, fixed with the HTTP client',
  },
  { name: 'LANGFUSE_RELEASE', note: 'defaults to the build version' },
] as const

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
  bodyMode: 'headtail',
  headKB: 32,
  tailKB: 16,
  fullMB: DEFAULT_FULL_MB,
  errorRate: 100,
  redact: true,
  environment: 'production',
  exportHost: '',
  consoleURL: '',
  rules: [],
}

function clampPercent(value: string): number {
  const parsed = Number(value)
  if (!Number.isFinite(parsed)) return 0
  return Math.min(100, Math.max(0, Math.round(parsed)))
}

// Two rules created in the same millisecond would collide on Date.now();
// the counter guarantees uniqueness within the draft's lifetime.
let ruleUidCounter = 0
function nextRuleUid(): number {
  ruleUidCounter += 1
  return Date.now() + ruleUidCounter
}

function clampKB(value: string, max: number): number {
  const parsed = Number(value)
  if (!Number.isFinite(parsed)) return 0
  return Math.min(max, Math.max(0, Math.round(parsed)))
}

/** Bytes on the wire, whole KB in the form: nobody wants to type 32768. */
function bytesToKB(bytes: number): number {
  return Math.max(0, Math.round(bytes / 1024))
}

/** Whole MB on the wire too: the cap is 1–16 MB, so a decimal is noise. */
function bytesToMB(bytes: number): number {
  return Math.max(0, Math.round(bytes / (1024 * 1024)))
}

/**
 * Like clampKB, zero is allowed *while typing*: making the field snap to 1 the
 * moment it is emptied turns "clear, type 8" into "18". The blur handler snaps
 * the floor back, and toPayload clamps again so an abandoned 0 can never be
 * stored as a sub-megabyte cap the server would refuse.
 */
function clampMB(value: string): number {
  const parsed = Number(value)
  if (!Number.isFinite(parsed)) return 0
  return Math.min(MAX_FULL_MB, Math.max(0, Math.round(parsed)))
}

function toBodyMode(value: unknown): BodyModeId {
  return value === 'off' || value === 'full' ? value : 'headtail'
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
    bodyMode: toBodyMode(value.body_mode),
    headKB: bytesToKB(value.body_head_bytes ?? 32768),
    tailKB: bytesToKB(value.body_tail_bytes ?? 16384),
    fullMB: bytesToMB(value.body_full_bytes ?? DEFAULT_FULL_MB * 1024 * 1024),
    errorRate: value.error_sample_rate ?? 100,
    redact: value.redact ?? true,
    environment: value.environment ?? 'production',
    exportHost: value.export_host ?? '',
    consoleURL: value.console_url ?? '',
    rules: (value.rules ?? []).map((rule, i) => ({
      uid: i,
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
    body_mode: draft.bodyMode,
    body_head_bytes: Math.round(draft.headKB * 1024),
    body_tail_bytes: Math.round(draft.tailKB * 1024),
    body_full_bytes:
      Math.round(Math.max(MIN_FULL_MB, draft.fullMB)) * 1024 * 1024,
    error_sample_rate: clampPercent(String(draft.errorRate)),
    redact: draft.redact,
    environment: draft.environment.trim(),
    // Empty is a meaningful value here ("keep following LANGFUSE_*"), so it
    // must survive the round trip instead of being filled in from the running
    // config — otherwise opening the page and saving would pin today's address.
    export_host: draft.exportHost.trim(),
    console_url: draft.consoleURL.trim(),
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
        body_mode: values.get(`${OPTION_KEY}.body_mode`) ?? 'headtail',
        body_head_bytes: Number(
          values.get(`${OPTION_KEY}.body_head_bytes`) ?? 32768
        ),
        body_tail_bytes: Number(
          values.get(`${OPTION_KEY}.body_tail_bytes`) ?? 16384
        ),
        body_full_bytes: Number(
          values.get(`${OPTION_KEY}.body_full_bytes`) ?? 2097152
        ),
        error_sample_rate: Number(
          values.get(`${OPTION_KEY}.error_sample_rate`) ?? 100
        ),
        redact: values.get(`${OPTION_KEY}.redact`) !== 'false',
        environment: values.get(`${OPTION_KEY}.environment`) ?? 'production',
        export_host: values.get(`${OPTION_KEY}.export_host`) ?? '',
        console_url: values.get(`${OPTION_KEY}.console_url`) ?? '',
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
        [`${OPTION_KEY}.body_mode`]: payload.body_mode,
        [`${OPTION_KEY}.body_head_bytes`]: String(payload.body_head_bytes),
        [`${OPTION_KEY}.body_tail_bytes`]: String(payload.body_tail_bytes),
        [`${OPTION_KEY}.body_full_bytes`]: String(payload.body_full_bytes),
        [`${OPTION_KEY}.error_sample_rate`]: String(
          payload.error_sample_rate
        ),
        [`${OPTION_KEY}.redact`]: String(payload.redact),
        [`${OPTION_KEY}.environment`]: payload.environment,
        [`${OPTION_KEY}.export_host`]: payload.export_host,
        [`${OPTION_KEY}.console_url`]: payload.console_url,
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

  const updateHead = (kb: number) =>
    update({ headKB: kb, tailKB: Math.min(current.tailKB, MAX_WINDOW_KB - kb) })

  const updateTail = (kb: number) =>
    update({ tailKB: kb, headKB: Math.min(current.headKB, MAX_WINDOW_KB - kb) })

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
  // Two addresses, two jobs: export_host is where spans are POSTed (often an
  // internal container name), host is what a browser can reach. The page used
  // to label the browser-facing one "export endpoint", which is how an operator
  // ends up editing the wrong string.
  const runningExportHost = status?.export_host?.trim() ?? ''
  const consoleBase = status?.host?.trim() ?? ''
  // Rendering an operator-supplied value straight into an href would turn a
  // misconfiguration into a clickable javascript: URL, so only http(s) becomes
  // a link and everything else stays plain text.
  const consoleIsLink = /^https?:\/\//i.test(consoleBase)
  const zone = status?.server_zone ?? t('server local zone')
  const zoneTime = status?.server_time ?? ''
  const runningMeta = status?.metadata_sample_rate
  const runningContent = status?.content_sample_rate

  const bodyModeLabel = useMemo(() => {
    const active = BODY_MODES.find((mode) => mode.id === status?.body_mode)
    if (!active) return '—'
    // The running window belongs next to the mode: "Head + tail" alone does
    // not say whether the gateway keeps 48 KB or 8 KB per request, and that
    // number is what explains a missing finish_reason in a truncated trace.
    if (active.id === 'headtail' && status?.body_head_bytes != null) {
      return t('{{mode}} · {{head}}/{{tail}} KB', {
        mode: t(active.label),
        head: bytesToKB(status.body_head_bytes),
        tail: bytesToKB(status.body_tail_bytes ?? 0),
      })
    }
    return t(active.label)
  }, [status?.body_mode, status?.body_head_bytes, status?.body_tail_bytes, t])

  // One budget, two fields: raising head has to lower tail in the form, or
  // the save would be refused by the cross-key check after the operator
  // already saw both numbers on screen.
  const windowKB = current.headKB + current.tailKB
  const windowMB = Math.round((windowKB * 1000) / 1024)
  // Prompt and answer are buffered separately, so one request occupies twice
  // the cap; 1000 concurrent streams is the same reference point the head/tail
  // hint uses. The old copy hardcoded "2 MB … 768 MB", which was already wrong
  // by ~5x and would have gone stale the moment the cap became editable.
  const fullBudgetGB = Math.round(((current.fullMB * 2 * 1000) / 1024) * 10) / 10

  // Early returns instead of a nested ternary: the two sentences are
  // separate translations anyway, and nesting them hides that from the
  // reader.
  const failureSummary = (() => {
    if (status?.error_sample_rate == null) return '—'
    if (status.redact) {
      return t('{{rate}}% failed · redaction on', {
        rate: status.error_sample_rate,
      })
    }
    return t('{{rate}}% failed · redaction off', {
      rate: status.error_sample_rate,
    })
  })()

  const ruleSummary = useMemo(() => {
    if (current.rules.length === 0) {
      return t('No rules yet — defaults apply to every request.')
    }
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
                {runningExportHost || '—'}
              </dd>
            </div>
            <div className='space-y-1'>
              <dt className='text-muted-foreground text-xs'>
                {t('Console URL')}
              </dt>
              <dd className='font-mono text-xs'>
                {consoleIsLink ? (
                  // The whole row is the hit target, but only the trailing icon
                  // is coloured: an underlined URL next to a dot-separated list
                  // of settings reads as body text, while the ↗ matches how the
                  // console already marks "this leaves the app".
                  <a
                    href={consoleBase}
                    target='_blank'
                    rel='noopener noreferrer'
                    title={t('Open in a new tab')}
                    data-apiduo='text'
                    className='group inline-flex max-w-full items-center gap-1.5 align-bottom hover:underline'
                  >
                    <span className='break-all'>{consoleBase}</span>
                    <ExternalLink
                      className='size-3.5 shrink-0 transition-transform group-hover:-translate-y-0.5'
                      style={{ color: 'var(--apiduo-ink)' }}
                      aria-hidden
                    />
                  </a>
                ) : (
                  <span className='break-all'>{consoleBase || '—'}</span>
                )}
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
                {t('Failures and redaction')}
              </dt>
              <dd className='tabular-nums'>{failureSummary}</dd>
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
          <CardTitle>{t('Connection')}</CardTitle>
          <CardDescription>
            {t(
              'Where traces are written and where the log drawer links to. Leave a field empty to keep following the deployment environment — the greyed text is what is running right now. Saving a new export endpoint takes effect on the next batch, no restart.'
            )}
          </CardDescription>
        </CardHeader>
        <CardContent className='grid gap-4 sm:grid-cols-2'>
          <div className='space-y-1.5'>
            <Label htmlFor='apiduo-export-host'>{t('Export endpoint')}</Label>
            <Input
              id='apiduo-export-host'
              value={current.exportHost}
              placeholder={runningExportHost || t('follows the environment')}
              onChange={(event) => update({ exportHost: event.target.value })}
              autoComplete='off'
              spellCheck={false}
              className='font-mono text-xs'
              data-apiduo='text'
            />
            <p className='text-muted-foreground text-[11px] leading-relaxed'>
              {t(
                'Base URL only — spans are POSTed to {host}/api/public/otel/v1/traces. An internal container name is fine and usually faster.',
                { host: current.exportHost.trim() || runningExportHost || '…' }
              )}
            </p>
          </div>
          <div className='space-y-1.5'>
            <Label htmlFor='apiduo-console-url'>{t('Console URL')}</Label>
            <Input
              id='apiduo-console-url'
              value={current.consoleURL}
              placeholder={consoleBase || t('same as the export endpoint')}
              onChange={(event) => update({ consoleURL: event.target.value })}
              autoComplete='off'
              spellCheck={false}
              className='font-mono text-xs'
              data-apiduo='text'
            />
            <p className='text-muted-foreground text-[11px] leading-relaxed'>
              {t(
                'What the "Langfuse" button in the log drawer opens. Only needed when the export endpoint is reachable from the gateway but not from a browser.'
              )}
            </p>
          </div>
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
          <CardTitle>{t('Captured body')}</CardTitle>
          <CardDescription>
            {t(
              'Sampling decides whether a trace keeps a body at all; this decides how much of it survives. The head carries role, model and the first delta, the tail carries finish_reason and token usage, and the middle is repeated text.'
            )}
          </CardDescription>
        </CardHeader>
        <CardContent className='space-y-5'>
          <div className='flex flex-wrap items-center gap-2'>
            {BODY_MODES.map((mode) => {
              const active = current.bodyMode === mode.id
              return (
                <Button
                  key={mode.id}
                  type='button'
                  size='sm'
                  variant={active ? 'default' : 'outline'}
                  className='h-7 gap-1 px-2.5 text-xs'
                  aria-pressed={active}
                  onClick={() => update({ bodyMode: mode.id })}
                >
                  {active ? <Check className='size-3' /> : null}
                  {t(mode.label)}
                </Button>
              )
            })}
          </div>

          {current.bodyMode === 'headtail' ? (
            <div className='grid gap-5 sm:grid-cols-2'>
              <div className='space-y-2'>
                <Label>{t('Head window (KB)')}</Label>
                <Input
                  type='number'
                  min={0}
                  max={MAX_WINDOW_KB}
                  aria-label={t('Head window (KB)')}
                  value={current.headKB}
                  onChange={(e) =>
                    updateHead(clampKB(e.target.value, MAX_WINDOW_KB))
                  }
                />
                <p className='text-muted-foreground text-xs'>
                  {t('Kept from the start: role, model, the first delta.')}
                </p>
              </div>
              <div className='space-y-2'>
                <Label>{t('Tail window (KB)')}</Label>
                <Input
                  type='number'
                  min={0}
                  max={MAX_WINDOW_KB}
                  aria-label={t('Tail window (KB)')}
                  value={current.tailKB}
                  onChange={(e) =>
                    updateTail(clampKB(e.target.value, MAX_WINDOW_KB))
                  }
                />
                <p className='text-muted-foreground text-xs'>
                  {t('Kept from the end: finish_reason and token usage.')}
                </p>
              </div>
            </div>
          ) : null}

          {current.bodyMode === 'off' ? (
            <p className='bg-muted/40 flex items-start gap-2 rounded-lg border p-3 text-xs'>
              <Info className='mt-0.5 size-3.5 shrink-0' />
              {t(
                'Traces keep timing, model, token usage and status, but neither prompt nor answer. The content sampling rate has no effect in this mode, and the log page link keeps working.'
              )}
            </p>
          ) : null}

          {current.bodyMode === 'headtail' ? (
            <p className='text-muted-foreground text-xs'>
              {t(
                'At most {{kb}} KB kept per request — roughly {{mb}} MB of capture buffers at 1000 concurrent streams. Cutting to whole frames, so a single oversized frame is kept intact instead of being sliced into invalid JSON.',
                { kb: windowKB, mb: windowMB }
              )}
            </p>
          ) : null}

          {current.bodyMode === 'full' ? (
            <div className='space-y-3'>
              <div className='max-w-xs space-y-2'>
                <Label>{t('Single-request cap (MB)')}</Label>
                <Input
                  type='number'
                  min={0}
                  max={MAX_FULL_MB}
                  step={1}
                  aria-label={t('Single-request cap (MB)')}
                  value={current.fullMB}
                  onChange={(e) => update({ fullMB: clampMB(e.target.value) })}
                  onBlur={() => {
                    if (current.fullMB < MIN_FULL_MB) {
                      update({ fullMB: MIN_FULL_MB })
                    }
                  }}
                />
                <p className='text-muted-foreground text-xs'>
                  {t(
                    'Prompt and answer are capped one by one, so one request holds at most twice this much. The cap applies before images are externalized: past it the body stops being valid JSON, and the images behind the cut are neither stored nor shown.'
                  )}
                </p>
              </div>
              <p className='flex items-start gap-2 rounded-lg border border-amber-500/30 bg-amber-500/5 p-3 text-xs text-amber-700 dark:text-amber-300'>
                <Info className='mt-0.5 size-3.5 shrink-0' />
                {t(
                  'Full capture keeps up to {{mb}} MB per request — roughly {{gb}} GB of capture buffers at 1000 concurrent streams. Turn it on while diagnosing one specific request, then switch back.',
                  { mb: current.fullMB, gb: fullBudgetGB }
                )}
              </p>
            </div>
          ) : null}

          <p className='text-muted-foreground text-xs'>
            {t(
              'The two windows share a {{cap}} KB budget. A save above it is refused, so what the form shows is what gets stored.',
              { cap: MAX_WINDOW_KB }
            )}
          </p>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{t('Failed requests and redaction')}</CardTitle>
          <CardDescription>
            {t(
              'Failures get their own rate because they are the rows worth reading: when normal sampling drops one, this rate brings it back. Redaction scrubs credentials before anything leaves the gateway.'
            )}
          </CardDescription>
        </CardHeader>
        <CardContent className='space-y-5'>
          <div className='grid gap-5 sm:grid-cols-2'>
            <div className='space-y-2'>
              <Label>{t('Failed request sampling (%)')}</Label>
              <Input
                type='number'
                min={0}
                max={100}
                aria-label={t('Failed request sampling (%)')}
                value={current.errorRate}
                onChange={(e) =>
                  update({ errorRate: clampPercent(e.target.value) })
                }
              />
              <p className='text-muted-foreground text-xs'>
                {t(
                  'Default 100%: a failure the rate above dropped is sampled again here. Set 0 to keep only the failures normal sampling already kept.'
                )}
              </p>
            </div>
            <div className='space-y-2'>
              <Label>{t('Environment tag')}</Label>
              <Input
                placeholder='production'
                aria-label={t('Environment tag')}
                value={current.environment}
                onChange={(e) => update({ environment: e.target.value })}
              />
              <p className='text-muted-foreground text-xs'>
                {t(
                  'Written to every span as langfuse.environment, so production and staging can be filtered apart. Leave empty for no tag at all.'
                )}
              </p>
            </div>
          </div>

          <div className='flex items-start justify-between gap-4 rounded-lg border p-3'>
            <div className='space-y-1'>
              <Label>{t('Redact credentials and personal data')}</Label>
              <p className='text-muted-foreground text-xs'>
                {t(
                  'Default on. Scrubs API keys, phone numbers, ID card numbers and email addresses before export. Turning it off sends prompt and answer verbatim to the tracing backend.'
                )}
              </p>
            </div>
            <Switch
              aria-label={t('Redact credentials and personal data')}
              checked={current.redact}
              onCheckedChange={(checked) => update({ redact: checked })}
            />
          </div>

          {!current.redact ? (
            <p className='flex items-start gap-2 rounded-lg border border-amber-500/30 bg-amber-500/5 p-3 text-xs text-amber-700 dark:text-amber-300'>
              <Info className='mt-0.5 size-3.5 shrink-0' />
              {t(
                'Redaction is off: prompts and answers leave the gateway exactly as received, including any credential a user pasted into the conversation.'
              )}
            </p>
          ) : null}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{t('Environment variables (need a restart)')}</CardTitle>
          <CardDescription>
            {t(
              'Deliberately not on this page: they either rebuild the export pipeline or carry credentials, and a bad value would silently redirect or stop exports. Change them in the deployment and recreate the container — docker restart does not re-read env.'
            )}
          </CardDescription>
        </CardHeader>
        <CardContent className='space-y-3 text-xs'>
          <dl className='grid gap-x-6 gap-y-2 sm:grid-cols-2'>
            {ENV_ONLY_KEYS.map((item) => (
              <div key={item.name} className='flex flex-wrap gap-x-2'>
                <dt className='font-mono'>{item.name}</dt>
                <dd className='text-muted-foreground'>{t(item.note)}</dd>
              </div>
            ))}
          </dl>
          <p className='text-muted-foreground'>
            {t(
              'Saving this page overrides the matching environment variables immediately; until you save, the environment is what runs.'
            )}
          </p>
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
            <div key={rule.uid} className='space-y-3 rounded-lg border p-3'>
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
                    uid: nextRuleUid(),
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
