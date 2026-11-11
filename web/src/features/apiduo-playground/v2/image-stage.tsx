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
import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { apiduoBlockMark } from '@/features/apiduo-marks/marks'
import type { ModelOption } from '@/features/playground/types'
import { formatCurrencyFromUSD } from '@/lib/currency'

import type { PlaygroundMode } from '../types'
import { findModel, resolveGroupRatio } from '../billing/pricing'
import { BillPanelSlot, MoneyChip } from '../components/message-billing'
import type { BillingLedger } from '../hooks/use-billing-ledger'
import { editImages, generateImages } from '../image/api'
import type { RequestMeta } from '../lib/request-meta'
import { capabilityFor, sizeFor } from './capabilities'
import { Composer } from './composer'
import { ExamplesShelf } from './examples'
import { ResultCard } from './result-card'
import { ambientOf, type RefImage, type StageRun } from './types'

interface ImageStageProps {
  model: string
  group: string
  models: ModelOption[]
  ledger: BillingLedger
  onModelChange: (model: string) => void
  /** 工作区与切换：模型下拉头部的三档共用页面那一个状态。 */
  mode: PlaygroundMode
  onModeChange: (mode: PlaygroundMode) => void
  modeCounts: Record<PlaygroundMode, number>
  /** 计费分组。页头那一个在图片工作区撤掉了，改从价格 chip 上换。 */
  groups: Array<{ label: string; value: string; ratio: number | string }>
  onGroupChange: (group: string) => void
  onOpenRequest: (key: string, meta?: RequestMeta) => void
  onCopyCurl: (key: string, meta?: RequestMeta) => void
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
/* Four decimals either side of the cent: a per-image price in the tenths of a
   cent is common, and rounding it to two makes three models look identical. */
const fmtPrice = (usd: number) =>
  formatCurrencyFromUSD(usd, { digitsLarge: 4, digitsSmall: 4 })

export function ImageStage({
  model,
  group,
  models,
  ledger,
  onModelChange,
  mode,
  onModeChange,
  modeCounts,
  groups,
  onGroupChange,
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
  /* 参考图袋子。文件是请求的一部分（文生图 → 图生图），所以与 prompt 一样
     留在 composer 外面：提交不清空、换模型只按新模型的容量裁剪。 */
  const [refs, setRefs] = useState<RefImage[]>([])
  /* 换模型裁掉别人的上传不能无声：裁了几张写成提示，落在 composer 的
     提示行上（原型 adoptModel 的 refNote），下一次参考图操作或提交时清掉。 */
  const [refNote, setRefNote] = useState('')
  const refSeq = useRef(0)
  const stageRef = useRef<HTMLDivElement | null>(null)
  /* A ref, not the rendered count: two clicks land in the same tick, before
     any state has been committed, and the second one must already see the
     first as in flight. */
  const inFlight = useRef(0)
  const lastSubmit = useRef({ text: '', at: 0 })

  const running = runs.filter((item) => item.status === 'pending').length
  const saturated = running >= IMAGE_CONCURRENCY

  const ambient = ambientOf(runs)

  /* One unit price, quoted the same way wherever it appears: the composer's
     receipt, the model list and the count picker's total must not disagree
     about what a picture costs. The number is kept unformatted so the total
     can multiply it; formatting happens once, at the edge. */
  const unitPriceOf = useCallback(
    (target: string) => {
      const info = findModel(ledger.pricing, target)
      if (!info) return null
      const { effective } = resolveGroupRatio(ledger.pricing, group, target)
      const base = info.quotaType === 1 ? info.modelPrice : info.modelRatio * 2
      return base * effective
    },
    [group, ledger.pricing]
  )
  const priceOf = useCallback(
    (target: string) => {
      const unit = unitPriceOf(target)
      return unit == null ? null : fmtPrice(unit)
    },
    [unitPriceOf]
  )
  const totalOf = useCallback(
    (count: number) => {
      const unit = unitPriceOf(model)
      return unit == null ? null : fmtPrice(unit * count)
    },
    [model, unitPriceOf]
  )
  /* The one-liner under the name comes from model management, not from this
     file: an operator writes "为什么选它" once, and every list row inherits
     it. Models without one fall back to the capability facts in the picker. */
  const isNew = useCallback(
    (target: string) =>
      (findModel(ledger.pricing, target)?.tags ?? []).includes('new'),
    [ledger.pricing]
  )

  const descOf = useCallback(
    (target: string) => findModel(ledger.pricing, target)?.description || null,
    [ledger.pricing]
  )
  /* 加入时过滤非图片并按容量截断：多选 12 张而容量 10 时，静默丢 2 张
     比 400 报错更糟——但要在标签上写清「已满」，所以丢在入口而不是请求里。 */
  const addRefFiles = useCallback(
    (files: File[]) => {
      const cap = capabilityFor(model).maxRefs
      if (cap <= 0) return
      setRefNote('')
      setRefs((prev) => {
        const room = Math.max(0, cap - prev.length)
        if (room === 0) return prev
        const accepted = files
          .filter((file) => file.type.startsWith('image/'))
          .slice(0, room)
        if (accepted.length === 0) return prev
        return [
          ...prev,
          ...accepted.map((file) => {
            refSeq.current += 1
            return {
              id: `ref-${refSeq.current}`,
              file,
              url: URL.createObjectURL(file),
            }
          }),
        ]
      })
    },
    [model]
  )
  const removeRef = useCallback((id: string) => {
    setRefNote('')
    setRefs((prev) => {
      const found = prev.find((item) => item.id === id)
      if (found) URL.revokeObjectURL(found.url)
      return prev.filter((item) => item.id !== id)
    })
  }, [])
  // 卸载时归还所有 object URL：页面级组件不还，浏览器要等到刷新才回收。
  // 走 ref 而不是把 refs 放进依赖——依赖一变清理就会跑，把还在展示的图撤掉。
  const liveRefs = useRef(refs)
  liveRefs.current = refs
  // 换模型换容量：装不下的从头裁、撤销 object URL，再把裁掉的张数说出来。
  // 容量 0 也要裁——不收参考图的模型不能让请求带着 multipart 出门。
  useEffect(() => {
    const cap = capabilityFor(model).maxRefs
    const prev = liveRefs.current
    if (prev.length <= cap) return
    const dropped = prev.length - cap
    for (const item of prev.slice(cap)) URL.revokeObjectURL(item.url)
    setRefs(prev.slice(0, cap))
    setRefNote(
      cap > 0
        ? t('{{model}} takes at most {{max}} references, removed {{n}}', {
            model,
            max: cap,
            n: dropped,
          })
        : t('{{model}} does not support references, removed {{n}}', {
            model,
            n: dropped,
          })
    )
  }, [model, t])
  useEffect(
    () => () => {
      for (const item of liveRefs.current) URL.revokeObjectURL(item.url)
    },
    []
  )
  /* What this run actually asked for, so the drawer and the copied cURL can
     quote it instead of a generic example. */
  const metaOf = useCallback(
    (run: StageRun): RequestMeta => ({
      prompt: run.prompt,
      model: run.model,
      refs: run.refFiles?.length ?? 0,
      params: {
        n: run.count,
        ...(run.size ? { size: run.size } : {}),
      },
    }),
    []
  )
  const priceLabel = priceOf(model)

  const run = useCallback(
    async (input: {
      prompt: string
      ratio: string
      resolution: string
      count: number
      files: File[]
    }) => {
      const trimmed = input.prompt.trim()
      if (!trimmed || inFlight.current >= IMAGE_CONCURRENCY) return
      setRefNote('')

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
          refFiles: input.files,
        },
      ])

      const startedAt = Date.now()
      try {
        // 有参考图走编辑入口（multipart，与对外 /v1/images/edits 同构），
        // 没有走 generations（JSON）。响应形状相同，下游解析共用。
        const result =
          input.files.length > 0
            ? await editImages({
                model,
                prompt: trimmed,
                n: input.count,
                size: size ?? '1024x1024',
                files: input.files,
              })
            : await generateImages({
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
    void run({
      prompt: text,
      ratio,
      resolution,
      count,
      files: refs.map((item) => item.file),
    })
  }, [count, prompt, ratio, refs, resolution, run])

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
                        // 文件留在 run 上（只在内存里）：重试重放的是同一个
                        // 请求，而不是一个只剩文字的同名请求。
                        files: target.refFiles ?? [],
                      })
                    }
                    onReuse={(target) => setPrompt(target.prompt)}
                    run={item}
                  />
                  <BillPanelSlot
                    billing={billing}
                    modelName={item.model}
                    onCopyCurl={() => onCopyCurl(item.id, metaOf(item))}
                    onOpenRequest={() => onOpenRequest(item.id, metaOf(item))}
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
        group={group}
        priceLabel={priceLabel}
        priceOf={priceOf}
        totalOf={totalOf}
        descOf={descOf}
        isNew={isNew}
        groups={groups}
        mode={mode}
        modeCounts={modeCounts}
        onGroupChange={onGroupChange}
        onModeChange={onModeChange}
        refs={refs}
        refNote={refNote}
        onAddRefs={addRefFiles}
        onRemoveRef={removeRef}
        prompt={prompt}
        ratio={ratio}
        resolution={resolution}
        running={running}
      />
    </div>
  )
}
