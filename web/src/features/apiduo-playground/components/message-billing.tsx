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
import { ChevronDown } from 'lucide-react'
import { AnimatePresence, motion } from 'motion/react'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { apiduoBlockMark } from '@/features/apiduo-marks/marks'
import { cn } from '@/lib/utils'

import { formatQuota, formatUsd } from '../billing/quota'
import type { MessageBilling } from '../hooks/use-billing-ledger'
import { useApiduoSpring } from '../hooks/use-motion'
import { RollingValue } from './rolling-value'

interface MoneyChipProps {
  billing: MessageBilling | undefined
  expanded: boolean
  onToggle: () => void
}

function statusLabel(billing: MessageBilling): string | null {
  if (billing.status === 'estimated') return 'est.'
  return null
}

export function MoneyChip({ billing, expanded, onToggle }: MoneyChipProps) {
  const { t } = useTranslation()
  const [fresh, setFresh] = useState(false)
  const previousStatus = useRef<MessageBilling['status'] | null>(null)
  const status = billing?.status ?? null

  /*
   * The chip is the one element that answers "how much did that cost", so the
   * frame it stops being provisional is worth marking: one ring, once.
   *
   * `previousStatus !== null` keeps a chip that mounts already settled from
   * flashing — that is the state the bill should simply *be*, not an event.
   */
  useEffect(() => {
    if (status === 'settled' && previousStatus.current && previousStatus.current !== 'settled') {
      setFresh(true)
      previousStatus.current = status
      const timer = window.setTimeout(() => setFresh(false), 900)
      return () => window.clearTimeout(timer)
    }
    previousStatus.current = status
  }, [status])

  if (!billing) return null

  const pending = billing.status === 'pending'
  const estimated = billing.status === 'estimated'
  const label = statusLabel(billing)

  return (
    <button
      aria-expanded={expanded}
      className={cn(
        'apd-press inline-flex shrink-0 items-center gap-1.5 rounded-full border px-2 py-0.5 font-mono text-[11px] leading-4',
        'hover:bg-muted/60 focus-visible:ring-ring focus-visible:ring-2 focus-visible:outline-none',
        pending && 'border-muted-foreground/40 text-muted-foreground border-dashed',
        estimated &&
          'border-amber-500/50 text-amber-700 dark:text-amber-400 border-dashed',
        !pending && !estimated && 'border-border bg-background text-foreground',
        fresh && 'apd-halo'
      )}
      data-apiduo={apiduoBlockMark('playground:billing')}
      onClick={onToggle}
      type='button'
    >
      <span
        aria-hidden='true'
        className={cn(
          'size-1.5 shrink-0 rounded-full transition-colors',
          pending && 'bg-amber-500 apd-breathe',
          estimated && 'bg-amber-500',
          !pending && !estimated && 'bg-emerald-500'
        )}
      />
      {pending ? (
        <span>{t('Reconciling…')}</span>
      ) : (
        <RollingValue
          value={`${formatQuota(billing.quota)}${label ? ` ${t(label)}` : ''}`}
        />
      )}
      <ChevronDown
        aria-hidden='true'
        className={cn(
          'size-3 shrink-0 opacity-60 transition-transform duration-300 [transition-timing-function:var(--apd-ease-settle)]',
          expanded && 'rotate-180'
        )}
      />
    </button>
  )
}

interface BillRowProps {
  label: string
  value: string
  note?: string
  emphasis?: boolean
  struck?: boolean
}

function BillRow({ label, value, note, emphasis, struck }: BillRowProps) {
  return (
    <div className='flex flex-wrap items-baseline gap-x-2.5 gap-y-0.5 py-1 text-[11.5px] leading-5'>
      <span className='text-muted-foreground w-[78px] shrink-0'>{label}</span>
      <span
        className={cn(
          'min-w-[72px] font-mono tabular-nums',
          emphasis ? 'text-foreground font-semibold' : 'text-foreground',
          struck && 'text-muted-foreground line-through'
        )}
      >
        {value}
      </span>
      {note ? (
        <span className='text-muted-foreground/80 hidden min-w-0 flex-1 text-[10.5px] leading-4 sm:block'>
          {note}
        </span>
      ) : null}
    </div>
  )
}

interface BillPanelProps {
  billing: MessageBilling
  modelName: string
  vendor?: string
  onOpenRequest: () => void
  onCopyCurl: () => void
}

export function BillPanel({
  billing,
  modelName,
  onOpenRequest,
  onCopyCurl,
}: BillPanelProps) {
  const { t } = useTranslation()
  const breakdown = billing.breakdown
  const settled = billing.status === 'settled'
  const estimated = billing.status === 'estimated'
  // 按次计费没有 token 维度，也没有模型/输出倍率
  const perCall = breakdown?.pricingMode === 'per-call'

  return (
    <div
      className='border-border/70 bg-background overflow-hidden rounded-xl border'
      data-apiduo={apiduoBlockMark('playground:billing')}
    >
      <div className='border-border/70 bg-muted/40 flex items-center gap-2 border-b px-3 py-2 text-[11.5px] font-semibold'>
        <span>{t('This request')}</span>
        <span
          className={cn(
            'ml-auto rounded px-1.5 py-0.5 text-[10px] font-medium',
            settled && 'bg-emerald-500/12 text-emerald-700 dark:text-emerald-400',
            estimated && 'bg-amber-500/15 text-amber-700 dark:text-amber-400',
            billing.status === 'pending' && 'bg-muted text-muted-foreground'
          )}
        >
          {settled && t('Settled')}
          {estimated && t('Estimate · log not written yet')}
          {billing.status === 'pending' && t('Reconciling…')}
        </span>
      </div>

      {/* Rows cascade rather than appear as one slab: the panel exists to be
          read top-to-bottom, and a 26 ms step per row keeps that order legible
          without anyone waiting on it. */}
      <div className='apd-stagger px-3 py-1.5'>
        {perCall ? null : (
          <>
            <BillRow
              label={t('Input tokens')}
              note='prompt_tokens'
              value={billing.promptTokens.toLocaleString()}
            />
            <BillRow
              label={t('Output tokens')}
              note='completion_tokens'
              value={billing.completionTokens.toLocaleString()}
            />
            {billing.cacheTokens > 0 ? (
              <BillRow
                label={t('Cache hit')}
                note={t('Already inside input tokens, billed at cache ratio')}
                value={billing.cacheTokens.toLocaleString()}
              />
            ) : null}
            <BillRow
              label={t('Model ratio')}
              note={modelName}
              value={`×${(breakdown?.modelRatio ?? 0).toFixed(2)}`}
            />
            <BillRow
              label={t('Output ratio')}
              note='completion_ratio'
              value={`×${(breakdown?.completionRatio ?? 0).toFixed(2)}`}
            />
          </>
        )}
        {perCall ? (
          <BillRow
            emphasis
            label={t('Model')}
            note={t('Billed per request, not per token')}
            value={modelName}
          />
        ) : null}
        {breakdown?.discountRate != null ? (
          <>
            <BillRow
              label={t('Group ratio')}
              note={t('Original group ratio, overridden by your discount')}
              struck
              value={`×${breakdown.baseGroupRatio.toFixed(2)}`}
            />
            <BillRow
              emphasis
              label={t('Personal discount')}
              note={t('Matched on this group and model, applied instead of the group ratio')}
              value={`×${breakdown.discountRate.toFixed(2)}`}
            />
          </>
        ) : (
          <BillRow
            label={t('Group ratio')}
            value={`×${(breakdown?.effectiveGroupRatio ?? 1).toFixed(2)}`}
          />
        )}

        {breakdown ? (
          <div className='bg-muted/50 text-muted-foreground mt-1.5 rounded-md px-2.5 py-1.5 font-mono text-[10.5px] leading-4 break-words'>
            {breakdown.formula}
          </div>
        ) : null}

        <div className='border-border/70 mt-2 flex items-baseline gap-2 border-t border-dashed pt-2'>
          <span className='text-muted-foreground text-[11px]'>
            {settled ? t('Actual charge') : t('Estimated charge')}
          </span>
          {settled ? (
            <span className='text-muted-foreground ml-auto font-mono text-[10px]'>
              {formatUsd(billing.quota)}
            </span>
          ) : (
            <span className='text-muted-foreground ml-auto text-[10px]'>
              {billing.source === 'estimate'
                ? t('Token count estimated from text length')
                : t('Waiting for the consume log')}
            </span>
          )}
          <span className='font-mono text-[15px] font-semibold tabular-nums'>
            {formatQuota(billing.quota)}
          </span>
        </div>
      </div>

      <div className='border-border/70 bg-muted/30 text-muted-foreground flex flex-wrap items-center gap-2 border-t px-3 py-2 text-[10.5px]'>
        <span>
          {billing.channelId
            ? t('Channel #{{id}}', { id: billing.channelId })
            : t('Channel pending')}
        </span>
        {billing.requestId ? (
          <span className='truncate font-mono'>{billing.requestId}</span>
        ) : null}
        <span className='ml-auto flex gap-1.5'>
          <button
            className='apd-press border-border bg-background hover:bg-muted rounded-md border px-2 py-0.5 text-[10.5px]'
            onClick={onOpenRequest}
            type='button'
          >
            {t('Request details')}
          </button>
          <button
            className='apd-press border-border bg-background hover:bg-muted rounded-md border px-2 py-0.5 text-[10.5px]'
            onClick={onCopyCurl}
            type='button'
          >
            {t('Copy cURL')}
          </button>
        </span>
      </div>
    </div>
  )
}

interface BillPanelSlotProps extends Omit<BillPanelProps, 'billing'> {
  billing: MessageBilling | undefined
  open: boolean
}

/**
 * Height animation from `auto` to `0`, not from a measured pixel value.
 *
 * Measuring would freeze the panel at today's row count — a bill whose cache
 * row appears a frame later would clip — and `height: auto` is what makes the
 * spring interruptible: collapse it halfway through opening and it reverses
 * from the current height instead of snapping back to a stored number.
 *
 * `marginTop` rides along so the 8 px gap opens with the panel instead of
 * leaving a hole while the height is still 0.
 */
export function BillPanelSlot({
  billing,
  open,
  ...panelProps
}: BillPanelSlotProps) {
  const spring = useApiduoSpring('settle')

  return (
    <AnimatePresence initial={false}>
      {open && billing ? (
        <motion.div
          animate={{ height: 'auto', opacity: 1, marginTop: 8 }}
          className='overflow-hidden'
          // Named so the opened height can be observed from outside; the panel
          // itself has no stable hook now that every mark renders as "text".
          data-apd-slot='bill'
          exit={{ height: 0, opacity: 0, marginTop: 0 }}
          initial={{ height: 0, opacity: 0, marginTop: 0 }}
          key='bill'
          transition={spring}
        >
          <BillPanel {...panelProps} billing={billing} />
        </motion.div>
      ) : null}
    </AnimatePresence>
  )
}
