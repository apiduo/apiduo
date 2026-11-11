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
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { apiduoBlockMark } from '@/features/apiduo-marks/marks'
import type { ModelOption } from '@/features/playground/types'
import { formatCurrencyFromUSD } from '@/lib/currency'

import { findModel, resolveGroupRatio } from '../billing/pricing'
import { BillPanelSlot, MoneyChip } from '../components/message-billing'
import type { BillingLedger } from '../hooks/use-billing-ledger'
import { generateImages } from '../image/api'
import { capabilityFor, sizeFor } from './capabilities'
import { Composer } from './composer'
import { ExamplesShelf } from './examples'
import { ResultCard } from './result-card'
import { ambientOf, type StageRun } from './types'

interface ImageStageProps {
  model: string
  group: string
  models: ModelOption[]
  ledger: BillingLedger
  onModelChange: (model: string) => void
  onOpenRequest: (key: string) => void
  onCopyCurl: (key: string) => void
  expandedBillingKey: string | null
  onToggleBilling: (key: string) => void
}

const AMBIENT = ['g0', 'g1', 'g2', 'g3'] as const

/* Pictures are cheap to run in parallel and slow to wait for one at a time, so
   the mode carries three slots. The cap is also printed in the composer
   (`0/3`), which is the only place the ceiling is ever stated. */
const IMAGE_CONCURRENCY = 3
/* Two identical requests a few hundred ms apart are always a double click, not
   a batch: the batch has a count chip. */
const DUPLICATE_WINDOW_MS = 800

/**
 * The image page: a shelf when there is nothing yet, a stream once there is,
 * and one composer under both.
 *
 * The composer is a sibling of the scroller rather than its last child on
 * purpose — that is what keeps it still while results arrive above it. Every
 * shot is appended, never replaced, so a four-image batch and the batch before
 * it stay comparable.
 */
export function ImageStage({
  model,
  group,
  models,
  ledger,
  onModelChange,
  onOpenRequest,
  onCopyCurl,
  expandedBillingKey,
  onToggleBilling,
}: ImageStageProps) {
  const { t } = useTranslation()
  const [prompt, setPrompt] = useState('')
  const [ratio, setRatio] = useState('1:1')
  const [resolution, setResolution] = useState('1024')
  const [count, setCount] = useState(1)
  const [runs, setRuns] = useState<StageRun[]>([])
  const stageRef = useRef<HTMLDivElement | null>(null)
  /* A ref, not the rendered count: two clicks land in the same tick, before
     any state has been committed, and the second one must already see the
     first as in flight. */
  const inFlight = useRef(0)
  const lastSubmit = useRef({ text: '', at: 0 })

  const running = runs.filter((item) => item.status === 'pending').length
  const saturated = running >= IMAGE_CONCURRENCY

  const ambient = ambientOf(runs)

  const price = useMemo(() => {
    const info = findModel(ledger.pricing, model)
    if (!info) return { unit: null as string | null }
    const { effective } = resolveGroupRatio(ledger.pricing, group, model)
    const base = info.quotaType === 1 ? info.modelPrice : info.modelRatio * 2
    return {
      unit: formatCurrencyFromUSD(base * effective, {
        digitsLarge: 4,
        digitsSmall: 4,
      }),
    }
  }, [group, ledger.pricing, model])

  const run = useCallback(
    async (input: {
      prompt: string
      ratio: string
      resolution: string
      count: number
    }) => {
      const trimmed = input.prompt.trim()
      if (!trimmed || inFlight.current >= IMAGE_CONCURRENCY) return

      const id = `img-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`
      const size = sizeFor(input.ratio, input.resolution)

      inFlight.current += 1
      setRuns((prev) => [
        ...prev,
        {
          id,
          createdAt: Date.now(),
          prompt: trimmed,
          model,
          ratio: input.ratio,
          resolution: input.resolution,
          size,
          count: input.count,
          items: [],
          status: 'pending',
        },
      ])

      const startedAt = Date.now()
      try {
        const result = await generateImages({
          model,
          prompt: trimmed,
          n: input.count,
          size: size ?? '1024x1024',
        })

        if (result.error) {
          setRuns((prev) =>
            prev.map((item) =>
              item.id === id
                ? { ...item, status: 'error', error: result.error }
                : item
            )
          )
          toast.error(result.error)
          return
        }

        setRuns((prev) =>
          prev.map((item) =>
            item.id === id
              ? { ...item, status: 'done', items: result.items }
              : item
          )
        )

        ledger.settle(id, {
          usage: null,
          fallbackPrompt: trimmed.length,
          requestId: result.requestId,
          stopped: false,
          durationMs: Date.now() - startedAt,
          firstTokenMs: null,
          context: { model, group },
          kind: 'image',
          imageCount: input.count,
        })
      } catch (error) {
        const message = error instanceof Error ? error.message : 'Request failed'
        setRuns((prev) =>
          prev.map((item) =>
            item.id === id ? { ...item, status: 'error', error: message } : item
          )
        )
        toast.error(message)
      } finally {
        inFlight.current -= 1
        // Let the new card mount before measuring: scrolling to the height of a
        // node that does not exist yet lands short. Only follow the stream when
        // the reader is already at the bottom — yanking the viewport while
        // someone is comparing earlier results is the rudest thing a batch of
        // three can do.
        requestAnimationFrame(() => {
          const node = stageRef.current
          if (!node) return
          const atBottom =
            node.scrollHeight - node.scrollTop - node.clientHeight < 160
          if (atBottom) {
            node.scrollTo({ top: node.scrollHeight, behavior: 'smooth' })
          }
        })
      }
    },
    [group, ledger, model]
  )

  const submit = useCallback(() => {
    const text = prompt.trim()
    if (!text || inFlight.current >= IMAGE_CONCURRENCY) return
    const at = Date.now()
    if (text === lastSubmit.current.text && at - lastSubmit.current.at < DUPLICATE_WINDOW_MS) {
      return
    }
    lastSubmit.current = { text, at }
    setPrompt('')
    void run({ prompt: text, ratio, resolution, count })
  }, [count, prompt, ratio, resolution, run])

  useEffect(() => {
    // A model swap can retire both the current ratio and the current bucket
    // (DALL·E has no 16:9 and no 2K). Normalising here, rather than in each
    // picker, is what keeps an invalid pair from ever reaching the wire.
    const capability = capabilityFor(model)
    if (!capability.ratios.includes(ratio)) setRatio(capability.ratios[0] ?? '1:1')
    if (!capability.resolutions.includes(resolution)) {
      setResolution(capability.resolutions[0] ?? '1024')
    }
  }, [model, ratio, resolution])

  return (
    <div className='apg' data-apiduo={apiduoBlockMark('playground:image')}>
      <div aria-hidden='true' className='apg-ambient'>
        {AMBIENT.map((family) => (
          <i
            className={family + (family === ambient ? ' on' : '')}
            key={family}
          />
        ))}
      </div>

      <div className='apg-stage' ref={stageRef}>
        <div
          className='apg-stage-inner'
          data-empty={runs.length === 0 ? 'true' : 'false'}
        >
          {runs.length === 0 ? (
            <ExamplesShelf
              available={models.map((item) => item.value)}
              model={model}
              onPick={({ prompt: next, ratio: nextRatio, model: nextModel }) => {
                // A card is a claim about one model in one shape. Taking the
                // prompt but leaving the composer on another model would run
                // exactly the combination the card never showed — and the
                // badge would name a model that is not about to be called.
                if (nextModel !== model) onModelChange(nextModel)
                setPrompt(next)
                setRatio(nextRatio)
              }}
            />
          ) : (
            runs.map((item) => {
              const billing = ledger.billingByKey[item.id]
              const expanded = expandedBillingKey === item.id
              return (
                <div key={item.id}>
                  <ResultCard
                    meta={
                      billing ? (
                        <MoneyChip
                          billing={billing}
                          expanded={expanded}
                          onToggle={() => onToggleBilling(item.id)}
                        />
                      ) : null
                    }
                    onRetry={(target) =>
                      void run({
                        prompt: target.prompt,
                        ratio: target.ratio,
                        resolution: target.resolution,
                        count: target.count,
                      })
                    }
                    onReuse={(target) => setPrompt(target.prompt)}
                    run={item}
                  />
                  <BillPanelSlot
                    billing={billing}
                    modelName={item.model}
                    onCopyCurl={() => onCopyCurl(item.id)}
                    onOpenRequest={() => onOpenRequest(item.id)}
                    open={expanded}
                  />
                </div>
              )
            })
          )}
        </div>
      </div>

      <Composer
        busy={saturated}
        cap={IMAGE_CONCURRENCY}
        count={count}
        hasResults={runs.length > 0}
        onClear={() => setRuns([])}
        model={model}
        models={models}
        onCountChange={setCount}
        onModelChange={onModelChange}
        onPromptChange={setPrompt}
        onRatioChange={setRatio}
        onResolutionChange={setResolution}
        onSubmit={submit}
        placeholder={
          runs.length === 0
            ? t('Describe the picture you want…')
            : t('Describe the next one…')
        }
        priceLabel={price.unit}
        prompt={prompt}
        ratio={ratio}
        resolution={resolution}
        running={running}
      />
    </div>
  )
}
