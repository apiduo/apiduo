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
import { Loader2Icon, Trash2Icon } from 'lucide-react'
import { motion } from 'motion/react'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { apiduoBlockMark } from '@/features/apiduo-marks/marks'
import { formatCurrencyFromUSD } from '@/lib/currency'
import { cn } from '@/lib/utils'

import { BillPanelSlot, MoneyChip } from '../components/message-billing'
import { Composer } from '../v2/composer'
import { ExamplesShelf } from '../v2/examples'
import type { BillingLedger } from '../hooks/use-billing-ledger'
import { useApiduoSpring } from '../hooks/use-motion'
import {
  findModel,
  fixedUnitPrice,
  resolveGroupRatio,
} from '../billing/pricing'
import type { RequestMeta } from '../lib/request-meta'
import type { PlaygroundMode, VideoTask, VideoTaskStatus } from '../types'
import type { RefImage } from '../v2/types'
import {
  createVideoTask,
  fetchVideoTask,
  fileToDataUrl,
  VIDEO_DURATIONS,
  VIDEO_RESOLUTIONS,
} from './api'

interface VideoWorkspaceProps {
  model: string
  group: string
  /** 可选的模型与计费分组：视频输入区自带这两个选择器，页头不再重复。 */
  models: Array<{ label: string; value: string }>
  groups: Array<{ label: string; value: string; ratio: number | string }>
  onModelChange: (value: string) => void
  onGroupChange: (value: string) => void
  mode: PlaygroundMode
  modeCounts: Record<PlaygroundMode, number>
  onModeChange: (mode: PlaygroundMode) => void
  ledger: BillingLedger
  onOpenRequest: (key: string, meta?: RequestMeta) => void
  onCopyCurl: (key: string, meta?: RequestMeta) => void
  expandedBillingKey: string | null
  onToggleBilling: (key: string) => void
}

const VIDEO_CONCURRENCY = 2

/* The locale files spell these four keys unevenly (Queued / Running / Failed,
   but succeeded lowercase), so the mapping is explicit rather than derived. */
const STATUS_KEY: Record<VideoTaskStatus, string> = {
  queued: 'Queued',
  running: 'Running',
  succeeded: 'succeeded',
  failed: 'Failed',
}
const POLL_INTERVAL_MS = 2000
/** 视频任务动辄几分钟，超过这个时间就不再自动轮询，避免长挂页面 */
const MAX_POLL_MS = 10 * 60 * 1000

const STATUS_STYLE: Record<string, string> = {
  queued: 'bg-muted text-muted-foreground',
  running: 'bg-sky-500/12 text-sky-700 dark:text-sky-400',
  succeeded: 'bg-emerald-500/12 text-emerald-700 dark:text-emerald-400',
  failed: 'bg-destructive/10 text-destructive',
}

export function VideoWorkspace({
  model,
  group,
  models,
  groups,
  onModelChange,
  onGroupChange,
  mode,
  modeCounts,
  onModeChange,
  ledger,
  onOpenRequest,
  onCopyCurl,
  expandedBillingKey,
  onToggleBilling,
}: VideoWorkspaceProps) {
  const { t } = useTranslation()
  const [prompt, setPrompt] = useState('')

  /* One clip, one price: the same base the image stage quotes, before the
     count (this mode has no count). Takes a model id so the picker can quote
     every row, not just the selected one. */
  const priceOf = useCallback(
    (target: string) => {
      const info = findModel(ledger.pricing, target)
      if (!info) return ''
      const { effective } = resolveGroupRatio(ledger.pricing, group, target)
      const unit =
        fixedUnitPrice(info) ??
        (info.quotaType === 1 ? info.modelPrice : info.modelRatio * 2)
      return formatCurrencyFromUSD(unit * effective, {
        digitsLarge: 4,
        digitsSmall: 4,
      })
    },
    [group, ledger.pricing]
  )
  const priceLabel = useMemo(() => priceOf(model), [priceOf, model])
  const isNew = useCallback(
    (target: string) =>
      (findModel(ledger.pricing, target)?.tags ?? []).includes('new'),
    [ledger.pricing]
  )
  /* 运营在模型管理里写的一句话，列表第二行就是它；没写才回落到能力事实。 */
  const descOf = useCallback(
    (target: string) => findModel(ledger.pricing, target)?.description || null,
    [ledger.pricing]
  )
  const [duration, setDuration] = useState<number>(VIDEO_DURATIONS[0])
  const [resolution, setResolution] = useState<string>(VIDEO_RESOLUTIONS[0])
  const [frames, setFrames] = useState<{
    first: RefImage | null
    last: RefImage | null
  }>({ first: null, last: null })
  const [tasks, setTasks] = useState<VideoTask[]>([])
  /* 上限是 2 条并发，不是「一次只能发一条」：任务进入队列后立刻释放名额，
     所以用「尚未到终态的任务数」算占用，另外用一个 ref 挡住同一帧的连点。 */
  const inFlight = useRef(0)
  const running = tasks.filter(
    (task) => task.status !== 'succeeded' && task.status !== 'failed'
  ).length
  const scrollRef = useRef<HTMLDivElement | null>(null)
  // Progress arrives in jumps every 2 s; a spring turns each jump into motion
  // that decelerates into the new value instead of teleporting the bar.
  const progressSpring = useApiduoSpring('settle')

  /* 槽位是一次替换：换图/清除都要把上一条 object URL 归还，否则每换一张
     都会在页面存活期间漏掉一份文件。 */
  const framesRef = useRef(frames)
  framesRef.current = frames
  const setFrame = useCallback((slot: 'first' | 'last', file: File | null) => {
    const current = framesRef.current[slot]
    if (current) URL.revokeObjectURL(current.url)
    const next = file
      ? {
          id: `frame-${slot}-${Date.now()}`,
          file,
          url: URL.createObjectURL(file),
        }
      : null
    setFrames((prev) => ({ ...prev, [slot]: next }))
  }, [])
  useEffect(() => {
    return () => {
      const live = framesRef.current
      if (live.first) URL.revokeObjectURL(live.first.url)
      if (live.last) URL.revokeObjectURL(live.last.url)
    }
  }, [])

  const patchTask = useCallback((id: string, patch: Partial<VideoTask>) => {
    setTasks((prev) =>
      prev.map((task) => (task.id === id ? { ...task, ...patch } : task))
    )
  }, [])

  // 只轮询「还没到终态」的任务；终态任务保留在列表里供回看
  useEffect(() => {
    const active = tasks.filter(
      (task) =>
        task.status !== 'succeeded' &&
        task.status !== 'failed' &&
        // 提交还没回来时任务带着本地占位 id，拿它去查询必然 400。
        // 等 createVideoTask 把真实 id 写回来再开始轮询。
        !task.id.startsWith('vid-local-') &&
        Date.now() - task.createdAt < MAX_POLL_MS
    )
    if (active.length === 0) return

    let cancelled = false
    const timer = window.setInterval(async () => {
      if (cancelled) return
      for (const task of active) {
        const snapshot = await fetchVideoTask(task.id)
        if (cancelled || !snapshot) continue
        patchTask(task.id, {
          status: snapshot.status,
          progress: snapshot.progress,
          url: snapshot.url || task.url,
          error: snapshot.error || task.error,
        })
        if (snapshot.status === 'succeeded') {
          ledger.settle(task.id, {
            usage: null,
            fallbackPrompt: task.prompt.length,
            requestId: task.requestId,
            stopped: false,
            durationMs: Date.now() - task.createdAt,
            firstTokenMs: null,
            context: { model: task.model, group },
            kind: 'image',
            imageCount: 1,
          })
        }
      }
    }, POLL_INTERVAL_MS)

    return () => {
      cancelled = true
      window.clearInterval(timer)
    }
  }, [tasks, ledger, model, group, patchTask])

  const handleSubmit = async () => {
    const trimmed = prompt.trim()
    if (!trimmed) return
    if (
      running >= VIDEO_CONCURRENCY ||
      inFlight.current >= VIDEO_CONCURRENCY
    ) {
      return
    }

    inFlight.current += 1
    const createdAt = Date.now()
    const localId = `vid-local-${createdAt}`
    setTasks((prev) => [
      ...prev,
      {
        id: localId,
        prompt: trimmed,
        model,
        createdAt,
        status: 'queued',
        progress: 0,
        url: '',
        error: '',
        quota: 0,
        requestId: '',
        durationSeconds: duration,
        resolution,
        frameCount: (frames.first ? 1 : 0) + (frames.last ? 1 : 0),
        hasFirstFrame: Boolean(frames.first),
        hasLastFrame: Boolean(frames.last),
      },
    ])

    try {
      // 链路只认「URL 或 Base64」：data URL 是浏览器手里唯一不用先上传的形状。
      const firstFrame = frames.first
        ? await fileToDataUrl(frames.first.file)
        : ''
      const lastFrame = frames.last
        ? await fileToDataUrl(frames.last.file)
        : ''
      const { snapshot, error } = await createVideoTask({
        model,
        prompt: trimmed,
        duration,
        resolution,
        firstFrame,
        lastFrame,
      })

      if (error || !snapshot) {
        patchTask(localId, { status: 'failed', error: error ?? 'Request failed' })
        toast.error(error ?? 'Request failed')
        return
      }

      const realId = snapshot.taskId || localId
      patchTask(localId, {
        id: realId,
        status: snapshot.status,
        progress: snapshot.progress,
        url: snapshot.url,
        requestId: snapshot.requestId,
      })
      setPrompt('')
    } catch (error) {
      const message = error instanceof Error ? error.message : 'Request failed'
      patchTask(localId, { status: 'failed', error: message })
      toast.error(message)
    } finally {
      inFlight.current -= 1
      requestAnimationFrame(() => {
        const node = scrollRef.current
        if (node) node.scrollTop = node.scrollHeight
      })
    }
  }

  return (
    <div
      className='apg flex min-h-0 flex-1 flex-col overflow-hidden'
      data-apiduo={apiduoBlockMark('playground:video')}
    >
      <div className='apg-stage' ref={scrollRef}>
        <div className='apg-stage-inner'>
        {tasks.length === 0 ? (
          <ExamplesShelf
            available={models.map((item) => item.value)}
            kind='video'
            model={model}
            onPick={({ prompt: next, ratio: nextRatio, model: nextModel }) => {
              // A card is a claim about one model in one shape. 16:9 and 9:16
              // are the two sizes this API accepts, so the badge on the card
              // picks the segment rather than being a caption nobody follows.
              if (nextModel !== model && models.some((item) => item.value === nextModel)) {
                onModelChange(nextModel)
              }
              setPrompt(next)
              setResolution(
                nextRatio === '9:16'
                  ? VIDEO_RESOLUTIONS[1]
                  : VIDEO_RESOLUTIONS[0]
              )
            }}
          />
        ) : (
          <div className='flex w-full flex-col gap-3'>
            {tasks.map((task) => {
              const billing = ledger.billingByKey[task.id]
              const expanded = expandedBillingKey === task.id
              const running =
                task.status !== 'succeeded' && task.status !== 'failed'

              return (
                <div
                  className='apd-rise border-border/60 bg-background rounded-xl border p-3'
                  key={task.id}
                >
                  <div className='mb-2 flex flex-wrap items-center gap-2'>
                    <span
                      className={cn(
                        'apd-nudge rounded px-1.5 py-0.5 text-[10px] font-medium capitalize',
                        STATUS_STYLE[task.status]
                      )}
                      key={task.status}
                    >
                      {t(STATUS_KEY[task.status])}
                    </span>
                    <span className='text-muted-foreground font-mono text-[10.5px]'>
                      {task.durationSeconds}s · {task.resolution}
                    </span>
                    {/* The inputs belong on the receipt: after a few clips nobody
                        remembers which one used frames. */}
                    {task.frameCount ? (
                      <span className='text-muted-foreground text-[10.5px]'>
                        {task.frameCount >= 2
                          ? t('First and last frame')
                          : t('Image to video')}
                      </span>
                    ) : null}
                    <span className='text-muted-foreground truncate text-[11px]'>
                      {task.prompt}
                    </span>
                    <div className='ml-auto flex items-center gap-2'>
                      {running ? (
                        <span className='text-muted-foreground flex items-center gap-1.5 font-mono text-[11px]'>
                          <Loader2Icon className='size-3 animate-spin' />
                          {task.progress > 0 ? `${Math.round(task.progress)}%` : t('Queued')}
                        </span>
                      ) : null}
                      {billing ? (
                        <MoneyChip
                          billing={billing}
                          expanded={expanded}
                          onToggle={() => onToggleBilling(task.id)}
                        />
                      ) : null}
                      <button
                        aria-label={t('Remove')}
                        className='text-muted-foreground hover:text-destructive transition-colors'
                        onClick={() =>
                          setTasks((prev) =>
                            prev.filter((item) => item.id !== task.id)
                          )
                        }
                        type='button'
                      >
                        <Trash2Icon className='size-3.5' />
                      </button>
                    </div>
                  </div>

                  {running ? (
                    <div className='bg-muted h-1.5 w-full overflow-hidden rounded-full'>
                      <motion.div
                        animate={{ width: `${Math.max(6, task.progress)}%` }}
                        className='bg-primary h-full rounded-full'
                        initial={false}
                        transition={progressSpring}
                      />
                    </div>
                  ) : null}

                  {task.error ? (
                    <p className='text-destructive mt-2 text-[11.5px]'>
                      {task.error}
                    </p>
                  ) : null}

                  {task.url ? (
                    <video
                      className='apd-fade-up bg-muted mt-2 w-full rounded-lg'
                      controls
                      playsInline
                      src={task.url}
                    />
                  ) : null}

                  <BillPanelSlot
                    billing={billing}
                    modelName={model}
                    onCopyCurl={() =>
                      onCopyCurl(task.id, {
                        prompt: task.prompt,
                        model: task.model,
                        params: {
                          seconds: task.durationSeconds,
                          size: task.resolution,
                          firstFrame: task.hasFirstFrame ? 1 : 0,
                          lastFrame: task.hasLastFrame ? 1 : 0,
                        },
                      })
                    }
                    onOpenRequest={() =>
                      onOpenRequest(task.id, {
                        prompt: task.prompt,
                        model: task.model,
                        params: {
                          seconds: task.durationSeconds,
                          size: task.resolution,
                          firstFrame: task.hasFirstFrame ? 1 : 0,
                          lastFrame: task.hasLastFrame ? 1 : 0,
                        },
                      })
                    }
                    open={expanded}
                  />
                </div>
              )
            })}
          </div>
        )}
        </div>
      </div>

      {/* One composer for the page: the same shell the image workspace uses,
          with this mode's two extra choices as inline segments. */}
      <Composer
        busy={running >= VIDEO_CONCURRENCY}
        cap={VIDEO_CONCURRENCY}
        count={1}
        descOf={descOf}
        isNew={isNew}
        group={group}
        groups={groups}
        hasResults={tasks.length > 0}
        mode={mode}
        modeCounts={modeCounts}
        model={model}
        models={models.map((item) => ({ ...item, label: item.label }))}
        allowRefs={false}
        onAddRefs={() => undefined}
        onVideoFrame={setFrame}
        videoFrames={frames}
        onClear={() => setTasks([])}
        onCountChange={() => undefined}
        onGroupChange={onGroupChange}
        onModeChange={onModeChange}
        onModelChange={onModelChange}
        onPromptChange={setPrompt}
        onRatioChange={() => undefined}
        onRemoveRef={() => undefined}
        onResolutionChange={() => undefined}
        onSubmit={() => void handleSubmit()}
        onVideoDurationChange={setDuration}
        onVideoResolutionChange={setResolution}
        placeholder={t('Describe the shot you want')}
        priceLabel={priceLabel}
        priceOf={priceOf}
        priceUnit={t('per clip')}
        prompt={prompt}
        ratio='16:9'
        refNote=''
        refs={[]}
        resolution='1024'
        running={running}
        totalOf={() => ''}
        videoDuration={duration}
        videoDurations={VIDEO_DURATIONS}
        videoResolution={resolution}
        videoResolutions={VIDEO_RESOLUTIONS}
      />
    </div>
  )
}
