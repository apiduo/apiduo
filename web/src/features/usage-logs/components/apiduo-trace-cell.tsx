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
along with this program. If not <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
/**
 * Trace cell for the usage log table: one icon per row, one drawer per click.
 *
 * Why a column instead of the existing dialog link: the dialog only opens one
 * row at a time, so "which of these 200 requests were slow because of TTFT" is
 * unanswerable. A column keeps the operator in the list they were scanning.
 *
 * The dot encodes only what the log row itself can prove:
 *   - error log type → red (the row already says the request failed)
 *   - no trace id    → dash (metadata sampling dropped it, or admin_info was
 *                      stripped because the viewer is not an admin)
 *   - otherwise      → green
 *
 * "Content sampled or not" needs the trace backend, and asking once per row
 * would hammer it. That answer lives in the drawer.
 */
import { ExternalLink, GitBranch } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { formatLogQuota } from '@/lib/format'
import { cn } from '@/lib/utils'

import { LOG_TYPE_ENUM } from '../constants'
import type { UsageLog } from '../data/schema'
import {
  parseJsonBody,
  parseSseBody,
  payloadFromDecoded,
  type ParsedPayload,
} from '../lib/conversation'
import { scanMedia } from '../lib/payload-media'
import { parseLogOther } from '../lib/format'
import { buildDeepTraceUrl, buildTraceUrl, type TraceConfig } from '../lib/trace-link'
import {
  getTraceDetail,
  type TraceDetailResult,
  type TraceObservation,
} from '../lib/trace-detail-api'
import { MediaStrip } from './apiduo-media-strip'
import { JsonTree } from './payload-tree'

function traceIdOf(log: UsageLog): string {
  return parseLogOther(log.other)?.admin_info?.trace_id?.trim() ?? ''
}

export function ApiduoTraceCell({
  log,
  traceConfig,
}: {
  log: UsageLog
  traceConfig: TraceConfig
}) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const prefetchTimer = useRef<number | null>(null)
  const traceId = traceIdOf(log)
  // Escape hatch. The drawer is a convenience built on top of Langfuse, so
  // when it cannot answer — backend down, trace split across two reads, a
  // payload shape it does not understand — the operator must still be able to
  // reach the source of truth instead of hitting a dead end.
  const consoleUrl = buildTraceUrl(traceConfig, traceId)

  useEffect(
    () => () => {
      if (prefetchTimer.current !== null) {
        window.clearTimeout(prefetchTimer.current)
      }
    },
    []
  )

  // 250 ms of dwell before spending a request: sweeping the cursor down the
  // table should not fire one read per row it crosses.
  const schedulePrefetch = () => {
    if (!traceId || traceCache.has(traceId)) return
    if (prefetchTimer.current !== null) {
      window.clearTimeout(prefetchTimer.current)
    }
    prefetchTimer.current = window.setTimeout(() => {
      loadTrace(traceId).catch(() => {
        // A prefetch that fails is silent: the click path reports the error.
      })
    }, 250)
  }

  const cancelPrefetch = () => {
    if (prefetchTimer.current !== null) {
      window.clearTimeout(prefetchTimer.current)
      prefetchTimer.current = null
    }
  }

  if (!traceId) {
    return (
      <span
        className='text-muted-foreground/30 select-none'
        title={t('No trace was recorded for this request')}
        data-apiduo='trace-absent'
      >
        —
      </span>
    )
  }

  const failed = log.type === LOG_TYPE_ENUM.ERROR

  return (
    <>
      <button
        type='button'
        onClick={() => setOpen(true)}
        onMouseEnter={schedulePrefetch}
        onMouseLeave={cancelPrefetch}
        onFocus={schedulePrefetch}
        onBlur={cancelPrefetch}
        title={t('View trace')}
        data-apiduo='trace-cell'
        className={cn(
          'relative inline-flex size-7 items-center justify-center rounded-md transition-all',
          'hover:bg-accent focus-visible:ring-ring focus-visible:ring-2 focus-visible:outline-none',
          'active:scale-90',
          failed
            ? 'text-[color:var(--destructive)]'
            : 'text-muted-foreground hover:text-[color:var(--apiduo-ink)]'
        )}
      >
        <GitBranch className='size-4' aria-hidden />
        <span
          className={cn(
            'border-background absolute top-0.5 right-0.5 size-[6px] rounded-full border',
            failed ? 'bg-[color:var(--destructive)]' : 'bg-emerald-500'
          )}
          aria-hidden
        />
      </button>
      <ApiduoTraceSheet
        traceId={traceId}
        log={log}
        traceConfig={traceConfig}
        consoleUrl={consoleUrl}
        open={open}
        onOpenChange={setOpen}
      />
    </>
  )
}

/**
 * Session-scoped cache for opened traces.
 *
 * A trace is immutable once written, and reading one costs a blob-store
 * round-trip on the Langfuse side (the v4 exporter parks large bodies in
 * object storage, so a 48 KB input took ~2.3 s end to end). Opening the same
 * row twice — compare a request against its response, then re-open it — must
 * not pay that twice.
 *
 * A plain Map with a size cap, not a time-based cache: entries never go stale,
 * they just get evicted when the reader has moved on.
 */
const TRACE_CACHE_LIMIT = 20
const traceCache = new Map<string, TraceDetailResult>()
// Requests already on the wire, so a hovering cursor and a click cannot start
// two reads of the same trace.
const inflightTraces = new Map<string, Promise<TraceDetailResult>>()

function rememberTrace(id: string, value: TraceDetailResult) {
  if (traceCache.size >= TRACE_CACHE_LIMIT) {
    const oldest = traceCache.keys().next().value
    if (oldest !== undefined) traceCache.delete(oldest)
  }
  traceCache.set(id, value)
}

/**
 * Single entry point for reading a trace: memory first, then the request that
 * is already running, then a new request.
 *
 * Langfuse answers in ~1.2 s even for an empty result (host load, not query
 * time), so the same trace must never be paid for twice — not by re-opening
 * the drawer, and not by hovering a row and then clicking it.
 */
function loadTrace(id: string): Promise<TraceDetailResult> {
  const cached = traceCache.get(id)
  if (cached) return Promise.resolve(cached)

  const running = inflightTraces.get(id)
  if (running) return running

  const request = getTraceDetail(id)
    .then((result) => {
      if (result.observation) rememberTrace(id, result)
      return result
    })
    .finally(() => {
      inflightTraces.delete(id)
    })
  inflightTraces.set(id, request)
  return request
}

function ApiduoTraceSheet({
  traceId,
  log,
  traceConfig,
  consoleUrl,
  open,
  onOpenChange,
}: {
  traceId: string
  log: UsageLog
  traceConfig: TraceConfig
  consoleUrl: string | null
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const [state, setState] = useState<
    | { kind: 'idle' }
    | { kind: 'loading' }
    | { kind: 'ready'; result: TraceDetailResult }
    | { kind: 'error' }
  >({ kind: 'idle' })

  useEffect(() => {
    if (!open) return

    const cached = traceCache.get(traceId)
    if (cached) {
      setState({ kind: 'ready', result: cached })
      return
    }

    let cancelled = false
    setState({ kind: 'loading' })
    // Only a real answer is cached; a "not found" would hide a trace that lands
    // a second later when the export queue catches up.
    loadTrace(traceId)
      .then((result) => {
        if (!cancelled) setState({ kind: 'ready', result })
      })
      .catch(() => {
        if (!cancelled) setState({ kind: 'error' })
      })
    return () => {
      cancelled = true
    }
  }, [open, traceId])

  const obs = state.kind === 'ready' ? state.result.observation : null

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        side='right'
        className='flex w-full flex-col gap-0 sm:max-w-xl'
        data-apiduo='trace-sheet'
      >
        <SheetHeader className='border-b pb-3'>
          {/* 右上角留给 Radix 的关闭按钮，标题行必须让位，否则图标会被压在下面点不到。 */}
          <SheetTitle className='flex items-center gap-2 pr-7 text-sm'>
            {obs ? <LevelDot level={obs.level} /> : null}
            <span className='truncate'>
              {obs?.name || log.model_name || t('Trace')}
            </span>
          </SheetTitle>
          <div className='flex items-center gap-2'>
            <SheetDescription className='min-w-0 font-mono text-[11px] break-all'>
              {traceId}
            </SheetDescription>
            {/* 常驻出口：指标和对话留在抽屉里，属性/评分这类深挖去 Langfuse。
                深链带 peek 参数，能直接落在那一条 span 上，不用在列表里再找。
                放文字不放纯图标：这里跟关闭按钮只隔几像素，两个同尺寸图标
                并排分不清谁是谁。 */}
            {(() => {
              const deep = obs
                ? buildDeepTraceUrl(traceConfig, obs.trace_id, obs.observation_id, obs.start_time)
                : null
              const url = deep || buildTraceUrl(traceConfig, traceId)
              if (!url) return null
              return (
                <a
                  href={url}
                  target='_blank'
                  rel='noopener noreferrer'
                  data-apiduo='text'
                  className='text-muted-foreground hover:text-foreground hover:bg-muted ml-auto inline-flex shrink-0 items-center gap-1 rounded px-1.5 py-0.5 text-[11px] font-medium'
                >
                  Langfuse
                  <ExternalLink className='size-3' aria-hidden />
                </a>
              )
            })()}
          </div>
        </SheetHeader>

        <div className='flex-1 space-y-5 overflow-y-auto px-4 py-4'>
          {state.kind === 'loading' && (
            <p className='text-muted-foreground text-sm'>
              {t('Loading trace…')}
            </p>
          )}

          {state.kind === 'error' && (
            <Notice tone='error' title={t('Trace backend unavailable')}>
              {t(
                'The gateway could not reach the trace store. The log row itself is unaffected.'
              )}
              <FallbackLink url={consoleUrl} />
            </Notice>
          )}

          {state.kind === 'ready' && state.result.reason === 'disabled' && (
            <Notice tone='info' title={t('Trace export is turned off')}>
              {t(
                'Turn it on in System Settings → Observability to collect traces.'
              )}
            </Notice>
          )}

          {state.kind === 'ready' && state.result.reason === 'not_found' && (
            <Notice
              tone='info'
              title={t('No trace was recorded for this request')}
            >
              {t(
                'Metadata sampling dropped this request, or the trace has aged out of retention. This is not a capture failure.'
              )}
              <FallbackLink url={consoleUrl} />
            </Notice>
          )}

          {obs && <TraceBody obs={obs} log={log} />}
        </div>
      </SheetContent>
    </Sheet>
  )
}

function TraceBody({ obs, log }: { obs: TraceObservation; log: UsageLog }) {
  const { t } = useTranslation()
  const total = obs.latency_s || log.use_time || 0
  // A TTFT greater than the total means the two timestamps disagree (a probe
  // wrote completion_start_time outside the span, or the clock jumped). Showing
  // "first token at 166s" on a 2-second request is worse than showing nothing.
  const ttft = obs.ttft_s > 0 && obs.ttft_s <= total ? obs.ttft_s : 0
  const ttftPct = total > 0 && ttft > 0 ? Math.min(100, (ttft / total) * 100) : 0
  // A local upstream answers in 1ms, and "0.00s" reads as "not measured".
  // Two decimals below 10ms is the honest floor, not a rounded zero.
  const ttftText = ttft > 0 && ttft < 0.01 ? '<0.01' : ttft.toFixed(2)
  const promptTokens = obs.prompt_tokens || log.prompt_tokens || 0
  const outputTokens = obs.completion_tokens || log.completion_tokens || 0
  // Two different reasons for an empty body, and they read the same unless we
  // separate them here. `consume <model>` spans come from RecordConsume — the
  // settlement path (async tasks, MJ, channel tests) that never receives a
  // request body at all, so "sampling stripped it" is simply false there.
  const settlement = /^consume\b/.test(obs.name)
  const hasBody = obs.content_sampled || obs.input !== '' || obs.output !== ''

  return (
    <>
      {/* Wait-for-first-byte vs generating: the single most useful shape when a
          stream feels slow but the total looks fine. */}
      <section>
        <div className='bg-muted flex h-2 overflow-hidden rounded-full'>
          {ttftPct > 0 && (
            <div
              className='bg-[color:var(--apiduo-ink)]/35'
              style={{ width: `${ttftPct}%` }}
            />
          )}
          <div className='flex-1 bg-[color:var(--apiduo-ink)]' />
        </div>
        <div className='text-muted-foreground mt-1.5 flex justify-between text-[11px] tabular-nums'>
          <span>0s</span>
          <span>
            {ttft > 0
              ? `${t('Time to first token')} ${ttftText}s`
              : t('No first-token timing')}
          </span>
          <span>{total > 0 ? `${total.toFixed(1)}s` : '—'}</span>
        </div>
      </section>

      <section className='grid grid-cols-3 gap-px overflow-hidden rounded-lg border'>
        <Metric
          label={t('Total latency')}
          value={total > 0 ? total.toFixed(2) : '—'}
          unit={total > 0 ? 's' : ''}
        />
        <Metric
          label={t('Time to first token')}
          value={ttft > 0 ? ttftText : '—'}
          unit={ttft > 0 ? 's' : ''}
        />
        <Metric label={t('Level')} value={obs.level || 'DEFAULT'} />
        <Metric
          label={t('Input tokens')}
          value={promptTokens.toLocaleString()}
        />
        <Metric
          label={t('Output tokens')}
          value={outputTokens.toLocaleString()}
        />
        <Metric label={t('Cost')} value={formatLogQuota(log.quota)} />
      </section>

      {!obs.content_sampled && (
        <Notice tone='info' title={t('Content was not captured')}>
          {settlement
            ? t(
                'This row was written when the request settled — async task settlement, a channel test, or a fee adjustment. Nothing in that path hands a prompt to the gateway, so there is no body to show; the cost and tokens above are the whole record.'
              )
            : t(
                'Metadata sampling kept the span but content sampling stripped the body. Raise the content rate in System Settings → Observability if you need prompts for this model.'
              )}
        </Notice>
      )}

      {obs.status_message && (
        <section>
          <SectionTitle>{t('Failure reason')}</SectionTitle>
          <pre className='bg-destructive/5 text-destructive border-destructive/30 max-h-40 overflow-auto rounded-lg border p-3 text-xs whitespace-pre-wrap break-words'>
            {obs.status_message}
          </pre>
        </section>
      )}

      {/* 正文没有就不画两个空框：抽屉宽度只有 520px，两个 "Not captured"
          占了首屏一半，却不比上面那句解释多说一个字。 */}
      {hasBody && (
        <section>
          <SectionTitle meta={t('Input')}>{t('Request')}</SectionTitle>
          <PayloadView text={obs.input} empty={t('Not captured')} kind='request' />
        </section>
      )}

      {hasBody && (
        <section>
          <SectionTitle meta={t('Output')}>{t('Response')}</SectionTitle>
          <PayloadView text={obs.output} empty={t('Not captured')} kind='response' />
        </section>
      )}

      {/* 元信息（渠道/令牌/环境/release）不再整块列出：日志行本身有渠道和
          令牌，attributes 全量在 Langfuse 侧。抽屉只留直观的三样：时间轴、
          指标、对话。深链在标题右上角，落在 Langfuse 的 preview 页签。 */}
    </>
  )
}

/** Shown only when the drawer cannot answer, so it never competes with the
 *  built-in view during normal browsing. */
function FallbackLink({ url }: { url: string | null }) {
  const { t } = useTranslation()
  if (!url) return null
  return (
    <a
      href={url}
      target='_blank'
      rel='noopener noreferrer'
      data-apiduo='text'
      className='mt-1.5 flex w-fit items-center gap-1 text-[11px] font-medium underline underline-offset-2'
      style={{ color: 'var(--apiduo-ink)' }}
    >
      {t('View trace in Langfuse')}
      <ExternalLink className='size-3' aria-hidden />
    </a>
  )
}

function LevelDot({ level }: { level: string }) {
  let color = 'bg-emerald-500'
  if (level === 'ERROR') {
    color = 'bg-[color:var(--destructive)]'
  } else if (level === 'WARNING') {
    color = 'bg-amber-500'
  }
  return (
    <span className={cn('size-2 shrink-0 rounded-full', color)} aria-hidden />
  )
}

function Metric({
  label,
  value,
  unit,
}: {
  label: string
  value: string
  unit?: string
}) {
  return (
    <div className='bg-background p-2.5'>
      <div className='text-muted-foreground mb-0.5 text-[10.5px]'>{label}</div>
      <div className='text-sm font-semibold tabular-nums'>
        {value}
        {unit ? (
          <span className='text-muted-foreground ml-0.5 text-[11px] font-normal'>
            {unit}
          </span>
        ) : null}
      </div>
    </div>
  )
}

function SectionTitle({
  children,
  meta,
}: {
  children: React.ReactNode
  meta?: string
}) {
  return (
    <div className='mb-2 flex items-baseline justify-between'>
      <h4 className='text-xs font-semibold'>{children}</h4>
      {meta ? (
        <span className='text-muted-foreground text-[10.5px]'>{meta}</span>
      ) : null}
    </div>
  )
}

// Rendering cap. A b64_json image response is 1-2 MB on its own; putting that
// in a <pre> freezes the sheet long before scrolling becomes useful. The full
// text already travelled over the wire, so this only bounds DOM work.
const MAX_PAYLOAD_CHARS = 6000

// Parsing is synchronous, so the ceiling has to be a size the main thread can
// swallow in one frame. 2.5 MB of JSON is ~25 ms, which is inside a single
// drawer open, and it covers the 2 MB cap that body_mode=full writes. The
// previous 192 KB ceiling was sized for headtail capture (48 KB); leaving it
// there meant an image response captured in full could never be parsed, so its
// base64 was never reachable by the viewer below.
const MAX_PARSE_CHARS = 2500 * 1024

// One message longer than this gets folded; the drawer is 520px wide and a
// pasted document would push everything else out of reach.
const TURN_COLLAPSE_AT = 400

// Frames drawn when a stream could not be assembled. Each frame is an open
// object in the tree, so this is ~6 rows apiece; 30 keeps the sheet responsive
// while still showing the opening events where the request echo lives.
const FRAME_ROWS = 30

/**
 * Says where the view above came from. An assembled answer is a
 * reconstruction, not the bytes on the wire — an operator reading a truncated
 * answer has to know that, or a missing sentence looks like a model failure
 * instead of a capture window.
 */
function StreamNote({
  frames,
  bad,
  shown,
  assembled,
}: {
  frames: number
  bad: number
  shown?: number
  assembled?: boolean
}) {
  const { t } = useTranslation()
  return (
    <p className='text-muted-foreground mt-1 text-[10.5px]'>
      {assembled
        ? t('Rebuilt from {{count}} stream events', { count: frames })
        : t('{{count}} stream events · first {{shown}} shown', {
            count: frames,
            shown: shown ?? frames,
          })}
      {bad > 0 ? ` · ${t('{{count}} frame(s) did not decode', { count: bad })}` : ''}
    </p>
  )
}

/**
 * Payload with a conversation view on top and the raw body one click away.
 *
 * The drawer exists so an operator can answer "what did we send, what came
 * back". Reading that out of a JSON blob means scanning for `messages[].content`
 * every time. Parsing is best-effort: a truncated body (headtail capture cuts
 * bytes) or an error string is not a conversation, and those fall back to the
 * raw text rather than being reshaped into something that looks complete.
 */
function PayloadView({
  text,
  empty,
  kind,
}: {
  text: string
  empty: string
  kind: 'request' | 'response'
}) {
  const { t } = useTranslation()
  const [raw, setRaw] = useState(false)
  const tooLarge = text.length > MAX_PARSE_CHARS
  // One decode, shared by every view below. A 2 MB body used to be parsed
  // twice inside a single open — once to look for a conversation, once to
  // build the tree.
  const decoded = useMemo(
    () => (text && !tooLarge ? parseJsonBody(text) : null),
    [text, tooLarge]
  )
  const parsed = useMemo(
    () => (decoded ? payloadFromDecoded(decoded, kind) : null),
    [decoded, kind]
  )
  // Images are lifted before the tree is drawn: scanMedia returns the body
  // with every blob swapped for a short marker, so the tree never lays out a
  // megabyte-long line.
  const media = useMemo(
    () => (decoded && !parsed ? scanMedia(decoded) : null),
    [decoded, parsed]
  )
  // A body that is valid JSON but not a conversation — an image response is
  // `{created, data, usage}` — is the case the tree exists for. Parsing it
  // only when the conversation view declined keeps the common path at one
  // decode instead of two.
  const json = useMemo(
    () => (decoded && !parsed ? (media?.view ?? decoded) : null),
    [decoded, parsed, media]
  )
  // A streaming answer is neither one JSON document nor a conversation: it is
  // one object per delta. Both views above decline it, which is why a streamed
  // reply used to land in the raw <pre> as a wall of frames.
  const sse = useMemo(
    () => (text && !tooLarge && !parsed && !json ? parseSseBody(text) : null),
    [text, tooLarge, parsed, json]
  )
  const streamTurns = sse?.parsed ?? null
  // No assembled answer? Then the frames themselves are the evidence — an
  // error frame mid-stream, or a protocol the assembler does not know.
  const streamFrames = !streamTurns && sse ? sse.frames : null

  if (!text) {
    return (
      <div className='text-muted-foreground rounded-lg border border-dashed p-3 text-xs'>
        {empty}
      </div>
    )
  }

  const truncated = text.length > MAX_PAYLOAD_CHARS
  const body = truncated ? text.slice(0, MAX_PAYLOAD_CHARS) : text
  const previewable = parsed !== null || json !== null || sse !== null
  const showTree = !raw && json !== null
  const showStream = !raw && sse !== null

  // Which view wins, in order of how much it says: a conversation, a stream
  // rebuilt into one, a tree, the frames that never assembled, then the bytes.
  // Written as early returns rather than a chained ternary — four conditions
  // deep, the ternary stopped being readable and started being a lint error.
  const content = (() => {
    if (raw) {
      return (
        <pre className='bg-muted/50 max-h-52 overflow-auto rounded-lg border p-3 text-xs whitespace-pre-wrap break-words'>
          {body}
        </pre>
      )
    }
    if (parsed) return <Turns parsed={parsed} />
    if (streamTurns) {
      return (
        <>
          <Turns parsed={streamTurns} />
          <StreamNote
            frames={sse?.frames.length ?? 0}
            bad={sse?.badFrames ?? 0}
            assembled
          />
        </>
      )
    }
    if (json !== null) return <JsonTree value={json} />
    if (streamFrames) {
      const shown = Math.min(streamFrames.length, FRAME_ROWS)
      return (
        <>
          <JsonTree value={streamFrames.slice(0, FRAME_ROWS)} />
          <StreamNote
            frames={streamFrames.length}
            bad={sse?.badFrames ?? 0}
            shown={shown}
          />
        </>
      )
    }
    return (
      <pre className='bg-muted/50 max-h-52 overflow-auto rounded-lg border p-3 text-xs whitespace-pre-wrap break-words'>
        {body}
      </pre>
    )
  })()

  return (
    <>
      {media && media.items.length > 0 && <MediaStrip items={media.items} />}
      {(previewable || tooLarge) && (
        <div className='mb-1.5 flex items-center justify-end gap-2'>
          {tooLarge && (
            <span className='text-muted-foreground text-[10.5px]'>
              {t('Too large to preview')}
            </span>
          )}
          {previewable && (
            <button
              type='button'
              onClick={() => setRaw((v) => !v)}
              className='text-muted-foreground hover:text-foreground rounded px-1.5 py-0.5 text-[10.5px] underline-offset-2 hover:underline'
            >
              {raw ? t('Preview') : t('Raw')}
            </button>
          )}
        </div>
      )}
      {content}
      {truncated && !showTree && !showStream && (
        <p className='text-muted-foreground mt-1 text-[10.5px]'>
          … {Math.round(text.length / 1024)} KB · {t('Truncated for display')}
        </p>
      )}
    </>
  )
}

const ROLE_TONE: Record<string, string> = {
  system: 'bg-slate-400',
  user: 'bg-blue-500',
  assistant: 'bg-emerald-500',
  tool: 'bg-amber-500',
}

function Turns({ parsed }: { parsed: ParsedPayload }) {
  return (
    <div className='space-y-1.5'>
      {parsed.turns.map((turn) => (
        // 同一段 system 可能出现两次，内容也不是天然唯一的；用「角色 + 开头
        // 40 字」当身份，重复时 React 只会警告，展示不受影响。
        <TurnBubble
          key={`${turn.role}:${turn.text.slice(0, 40)}`}
          turn={turn}
        />
      ))}
      {parsed.extras.length > 0 && (
        <div className='text-muted-foreground flex flex-wrap gap-x-3 gap-y-0.5 pt-0.5 text-[10.5px]'>
          {parsed.extras.map((extra) => (
            <span key={extra.key}>
              {extra.key}{' '}
              <span className='text-foreground font-medium'>{extra.value}</span>
            </span>
          ))}
        </div>
      )}
    </div>
  )
}

/**
 * One message. Long bodies start folded: a pasted document or a 20-turn history
 * otherwise fills the sheet with text the reader has to scroll past, and every
 * extra paragraph costs layout work while the sheet is animating in.
 */
function TurnBubble({ turn }: { turn: ParsedPayload['turns'][number] }) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const long = turn.text.length > TURN_COLLAPSE_AT

  return (
    <div className='bg-background overflow-hidden rounded-lg border'>
      <div className='bg-muted/40 flex items-center gap-1.5 border-b px-2.5 py-1'>
        <span
          className={cn(
            'size-1.5 rounded-full',
            ROLE_TONE[turn.role] ?? 'bg-muted-foreground'
          )}
          aria-hidden
        />
        <span className='text-[11px] font-semibold capitalize'>{turn.role}</span>
        {turn.name ? (
          <span className='text-muted-foreground truncate text-[10.5px]'>
            {turn.name}
          </span>
        ) : null}
        {long && (
          <button
            type='button'
            onClick={() => setOpen((v) => !v)}
            className='text-muted-foreground hover:text-foreground ml-auto rounded px-1 text-[10px] underline-offset-2 hover:underline'
          >
            {open
              ? t('Collapse')
              : `${t('Expand')} · ${(turn.text.length / 1000).toFixed(1)}K`}
          </button>
        )}
      </div>
      <div className='relative'>
        {/* Folding cuts the string, it does not just clip it: `max-height` keeps
            every character in the DOM, and the browser still pays for line
            breaking a 100 KB paragraph before hiding it. Slicing means the
            folded state really is cheap; the full text appears only when the
            reader asks for it. */}
        <div className='px-2.5 py-2 text-xs whitespace-pre-wrap break-words'>
          {open || !long ? turn.text : `${turn.text.slice(0, TURN_COLLAPSE_AT)}…`}
        </div>
        {long && !open && (
          <div className='from-background pointer-events-none absolute inset-x-0 bottom-0 h-8 bg-gradient-to-t to-transparent' />
        )}
      </div>
    </div>
  )
}

function Notice({
  tone,
  title,
  children,
}: {
  tone: 'info' | 'error'
  title: string
  children: React.ReactNode
}) {
  return (
    <div
      className={cn(
        'flex gap-2.5 rounded-lg border p-3 text-xs leading-relaxed',
        tone === 'error'
          ? 'border-destructive/30 bg-destructive/5 text-destructive'
          : 'border-primary/20 bg-primary/5'
      )}
    >
      <div className='min-w-0'>
        <p className='mb-0.5 font-semibold'>{title}</p>
        <div className='text-muted-foreground'>{children}</div>
      </div>
    </div>
  )
}
