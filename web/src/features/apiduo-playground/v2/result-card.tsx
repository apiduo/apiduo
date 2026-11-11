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
import {
  DownloadIcon,
  Loader2Icon,
  Maximize2Icon,
  RotateCcwIcon,
  Undo2Icon,
} from 'lucide-react'
import { useCallback, useMemo, useRef } from 'react'
import { useTranslation } from 'react-i18next'

import { cn } from '@/lib/utils'

import { ratioOf } from './capabilities'
import { cssVars } from './css-vars'
import { glowOf, type StageRun } from './types'

interface ResultCardProps {
  run: StageRun
  onReuse: (run: StageRun) => void
  onRetry: (run: StageRun) => void
  meta?: React.ReactNode
}

const TOOL_ICON = 'size-[15px]'

/**
 * One run, one picture (or grid).
 *
 * The tool shelf only carries actions that work today. The prototype also
 * sketches 变高清 / 去水印 / 抠图 / 扩图 as "coming soon", but a production
 * toolbar that is 60 % disabled teaches people to stop reading it — those
 * arrive when the relay can actually serve them.
 */
function Tools({
  items,
  run,
  onReuse,
}: {
  items: { url: string }[]
  run: StageRun
  onReuse: (run: StageRun) => void
}) {
  const { t } = useTranslation()
  const first = items[0]?.url

  return (
    <div aria-label={t('Image tools')} className='apg-tools' role='toolbar'>
      {first ? (
        <a
          aria-label={t('Open full size')}
          className={cn('apg-tool')}
          href={first}
          rel='noreferrer'
          style={cssVars({ '--ti': 0 })}
          target='_blank'
        >
          <Maximize2Icon className={TOOL_ICON} />
        </a>
      ) : null}
      <button
        aria-label={t('Use this prompt again')}
        className='apg-tool'
        onClick={() => onReuse(run)}
        style={cssVars({ '--ti': 1 })}
        type='button'
      >
        <Undo2Icon className={TOOL_ICON} />
      </button>
      <span aria-hidden='true' className='apg-tool-sep' />
      {first ? (
        <a
          aria-label={t('Download')}
          className='apg-tool'
          download={`apiduo-${run.id}.png`}
          href={first}
          style={cssVars({ '--ti': 2 })}
        >
          <DownloadIcon className={TOOL_ICON} />
        </a>
      ) : null}
    </div>
  )
}

export function ResultCard({ run, onReuse, onRetry, meta }: ResultCardProps) {
  const { t } = useTranslation()
  const artRef = useRef<HTMLDivElement | null>(null)

  // One listener on the card, not one per picture: the highlight is a pair of
  // custom properties, and writing them per pointermove is cheaper than
  // asking the browser to re-layout anything.
  const track = useCallback((event: React.PointerEvent<HTMLDivElement>) => {
    const node = artRef.current
    if (!node) return
    const rect = node.getBoundingClientRect()
    node.style.setProperty('--mx', `${event.clientX - rect.left}px`)
    node.style.setProperty('--my', `${event.clientY - rect.top}px`)
  }, [])

  const ratio = ratioOf(run.ratio)
  const ar = run.items.length > 1 ? 1 : ratio.ar
  const pending = run.items.length === 0 && run.status === 'pending'

  // Two identical URLs in one batch are legal (a repeated seed, a cached
  // result), so the tail alone is not unique. Counting repeats keeps every key
  // stable across re-renders without falling back to the array index.
  const keys = useMemo(() => {
    const seen = new Map<string, number>()
    return run.items.map((item) => {
      const base = item.url.slice(-40)
      const hit = seen.get(base) ?? 0
      seen.set(base, hit + 1)
      return hit === 0 ? base : `${base}#${hit}`
    })
  }, [run.items])

  return (
    <div
      className='apg-round'
      /* One picture: the meta row and the tool shelf share its width, so the
         block reads as one object. A two-up grid keeps the full column, because
         there the picture and the caption are already the same width. */
      data-solo={run.items.length === 1}
      style={cssVars({
        '--arn': ar,
        '--glow': glowOf(run),
        '--cols': Math.min(run.items.length || 1, 2),
      })}
    >
      <div className='apg-round-meta'>
        <span className='mono'>
          {new Date(run.createdAt).toLocaleTimeString()}
        </span>
        <span className='sep'>·</span>
        <span>{run.size ?? run.ratio}</span>
        <span className='sep'>·</span>
        <span>
          {run.items.length > 0
            ? t('{{count}} images', { count: run.items.length })
            : t('{{count}} requested', { count: run.count })}
        </span>
        <div className='apg-round-meta-right'>
          {run.error ? (
            <span className='text-destructive'>{run.error}</span>
          ) : null}
          {meta}
          {run.error ? (
            <button
              className='apg-chip'
              onClick={() => onRetry(run)}
              type='button'
            >
              <RotateCcwIcon className='size-3' />
              {t('Retry')}
            </button>
          ) : null}
        </div>
      </div>

      {pending ? (
        <div className='apg-pending' style={{ aspectRatio: ar }}>
          <span className='apg-pending-label'>
            <Loader2Icon className='size-3 animate-spin' />
            {t('Generating…')}
          </span>
        </div>
      ) : (
        <div
          className={cn('apg-grid')}
          data-count={Math.min(run.items.length || 1, 4)}
        >
          {run.items.map((item, index) => (
            <div
              className={cn('apg-art', index === 0 && 'apg-frame')}
              data-auto={run.ratio === 'auto'}
              key={keys[index]}
              onPointerMove={track}
              ref={index === 0 ? artRef : undefined}
            >
              <img alt={item.revisedPrompt ?? run.prompt} src={item.url} />
              <i aria-hidden='true' className='land' />
              {index === 0 ? (
                <Tools items={run.items} onReuse={onReuse} run={run} />
              ) : null}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
