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
 * Foldable JSON tree for the trace drawer.
 *
 * Why a tree at all: the drawer used to show the raw body in a <pre>. That is
 * fine for a chat payload (the conversation view covers it) and awful for
 * everything else — an image response is one 48 KB line whose only visible
 * part is the first few hundred characters of a base64 blob. Langfuse shows
 * the same payload as `created / data / usage` with the branches collapsed,
 * which is why an operator can answer "did the upstream return an image or a
 * refusal" in one look.
 *
 * Scope rules, so this stays a viewer and not a JSON IDE:
 *   - folded below the first level; expanding is one click
 *   - long strings are cut for layout, with a toggle for the full value
 *   - base64 runs become a size marker (scrubBase64): no amount of scrolling
 *     makes a 1 MB data URI readable, and laying it out freezes the sheet
 *   - a per-container row cap bounds the DOM even if every branch is opened
 */
import { ChevronRight } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { cn } from '@/lib/utils'

import { scrubBase64 } from '../lib/conversation'

/** Folded below this depth: the root and its first level read as the summary. */
const OPEN_DEPTH = 2
/** Rows drawn per container before the rest collapse into a count. */
const ROW_CAP = 200
/** Characters of a string shown before it gets a "show all" toggle. */
const STRING_PREVIEW = 180

type Json = unknown

function isRecord(value: Json): value is Record<string, Json> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function isContainer(value: Json): value is Record<string, Json> | Json[] {
  return Array.isArray(value) || isRecord(value)
}

function entriesOf(value: Json): Array<[string, Json]> {
  if (Array.isArray(value)) return value.map((item, i) => [String(i), item])
  if (isRecord(value)) return Object.entries(value)
  return []
}

function scalarClass(value: Json): string {
  if (typeof value === 'string') return 'text-emerald-700 dark:text-emerald-300'
  if (typeof value === 'number') return 'text-sky-700 dark:text-sky-300'
  if (typeof value === 'boolean') return 'text-amber-700 dark:text-amber-300'
  return 'text-muted-foreground/70 italic'
}

/** One scalar as it appears inline. Strings keep their quotes so `"1"` and
 *  `1` stay distinguishable — the whole reason someone reads this view. */
function scalarText(value: Json, limit = STRING_PREVIEW): string {
  if (typeof value === 'string') {
    const scrubbed = scrubBase64(value)
    if (scrubbed.length <= limit) return JSON.stringify(scrubbed)
    return `${JSON.stringify(scrubbed.slice(0, limit))}…`
  }
  if (value === null) return 'null'
  return String(value)
}

function containerShape(value: Json): string {
  const count = entriesOf(value).length
  return `${Array.isArray(value) ? '[…]' : '{…}'} ${count}`
}

/**
 * Collapsed one-liner: `{ model: "flux-3-image", size: "1024x1024", … }`.
 *
 * Folded rows have to be informative enough that most payloads never need to
 * be opened, which is exactly how Langfuse's preview tab reads.
 */
function summarize(entries: Array<[string, Json]>, isArr: boolean): string {
  const parts = entries.slice(0, 4).map(([key, value]) => {
    if (isContainer(value)) return isArr ? containerShape(value) : `${key}: ${containerShape(value)}`
    const leaf = scalarText(value, 40)
    return isArr ? leaf : `${key}: ${leaf}`
  })
  if (entries.length > 4) parts.push('…')
  return `${isArr ? '[' : '{'} ${parts.join(', ')} ${isArr ? ']' : '}'}`
}

function Node({
  name,
  value,
  depth,
  last,
}: {
  name: string | null
  value: Json
  depth: number
  last: boolean
}) {
  const { t } = useTranslation()
  const container = isContainer(value)
  const entries = useMemo(() => (container ? entriesOf(value) : []), [container, value])
  const [open, setOpen] = useState(depth < OPEN_DEPTH)
  const [full, setFull] = useState(false)

  const bracket = Array.isArray(value)
    ? { open: '[', close: ']' }
    : { open: '{', close: '}' }

  if (!container) {
    const long =
      typeof value === 'string' && scrubBase64(value).length > STRING_PREVIEW
    return (
      <div className='flex items-baseline gap-1 py-0.5 pl-1'>
        {name !== null && (
          <span className='shrink-0'>
            <span className='text-muted-foreground'>{name}</span>
            <span className='text-foreground/30'>:</span>
          </span>
        )}
        <span
          className={cn(
            'min-w-0 break-words whitespace-pre-wrap',
            scalarClass(value)
          )}
        >
          {full ? scalarText(value, Infinity) : scalarText(value)}
        </span>
        {long && (
          <button
            type='button'
            onClick={() => setFull((v) => !v)}
            title={full ? t('Collapse') : t('Expand')}
            className='text-muted-foreground hover:text-foreground shrink-0 rounded px-1 text-[10px] underline-offset-2 hover:underline'
          >
            {full ? '−' : '+'}
          </button>
        )}
        {!last && <span className='text-foreground/20'>,</span>}
      </div>
    )
  }

  const shown = open ? entries.slice(0, ROW_CAP) : []

  return (
    <div>
      <button
        type='button'
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        className='hover:bg-accent/50 -mx-1 flex w-full min-w-0 items-baseline gap-1 rounded px-1 py-0.5 text-left'
      >
        <ChevronRight
          className={cn(
            'text-muted-foreground size-3 shrink-0 translate-y-[2px] transition-transform duration-150 motion-reduce:transition-none',
            open && 'rotate-90'
          )}
          aria-hidden
        />
        {name !== null && (
          <span className='shrink-0'>
            <span className='text-muted-foreground'>{name}</span>
            <span className='text-foreground/30'>:</span>
          </span>
        )}
        {open ? (
          <span className='text-foreground/40 shrink-0'>{bracket.open}</span>
        ) : (
          <span className='text-muted-foreground/80 min-w-0 truncate'>
            {summarize(entries, bracket.open === '[')}
          </span>
        )}
        {!open && (
          <span className='text-muted-foreground/50 shrink-0 pl-1 text-[10px] tabular-nums'>
            {entries.length}
          </span>
        )}
        {!open && !last && <span className='text-foreground/20'>,</span>}
      </button>
      {open && (
        <div className='border-border/60 ml-[7px] border-l pl-2.5'>
          {entries.length === 0 ? (
            <div className='text-muted-foreground/60 py-0.5 pl-1'>
              {bracket.open} {bracket.close}
            </div>
          ) : (
            shown.map(([key, child], i) => (
              <Node
                key={key}
                name={key}
                value={child}
                depth={depth + 1}
                last={i === shown.length - 1 && shown.length === entries.length}
              />
            ))
          )}
          {entries.length > shown.length && (
            <div className='text-muted-foreground/70 py-0.5 pl-1 text-[10.5px]'>
              … +{entries.length - shown.length}
            </div>
          )}
          <div className='text-foreground/40 py-0.5 pl-1'>{bracket.close}</div>
        </div>
      )}
    </div>
  )
}

/**
 * `value` must already be parsed JSON. Parsing the raw string here would mean
 * doing it again on every render, and a failed parse looks identical to an
 * empty payload — the caller decides, this component only draws.
 */
export function JsonTree({ value }: { value: Json }) {
  return (
    <div
      className='bg-muted/30 max-h-80 min-w-0 overflow-auto rounded-lg border p-2 font-mono text-[11.5px] leading-5'
      data-apiduo='json-tree'
    >
      <Node name={null} value={value} depth={0} last />
    </div>
  )
}
