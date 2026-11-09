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
import { Loader2Icon, Trash2Icon, VideoIcon } from 'lucide-react'
import { motion } from 'motion/react'
import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { apiduoBlockMark } from '@/features/apiduo-marks/marks'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

import { BillPanelSlot, MoneyChip } from '../components/message-billing'
import type { BillingLedger } from '../hooks/use-billing-ledger'
import { useApiduoSpring } from '../hooks/use-motion'
import type { VideoTask } from '../types'
import {
  createVideoTask,
  fetchVideoTask,
  VIDEO_DURATIONS,
  VIDEO_RESOLUTIONS,
} from './api'

interface VideoWorkspaceProps {
  model: string
  group: string
  ledger: BillingLedger
  onOpenRequest: (key: string) => void
  onCopyCurl: (key: string) => void
  expandedBillingKey: string | null
  onToggleBilling: (key: string) => void
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
  ledger,
  onOpenRequest,
  onCopyCurl,
  expandedBillingKey,
  onToggleBilling,
}: VideoWorkspaceProps) {
  const { t } = useTranslation()
  const [prompt, setPrompt] = useState('')
  const [duration, setDuration] = useState<number>(VIDEO_DURATIONS[0])
  const [resolution, setResolution] = useState<string>(VIDEO_RESOLUTIONS[0])
  const [tasks, setTasks] = useState<VideoTask[]>([])
  const [busy, setBusy] = useState(false)
  const scrollRef = useRef<HTMLDivElement | null>(null)
  // Progress arrives in jumps every 2 s; a spring turns each jump into motion
  // that decelerates into the new value instead of teleporting the bar.
  const progressSpring = useApiduoSpring('settle')

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
    if (!trimmed || busy) return

    setBusy(true)
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
      },
    ])

    try {
      const { snapshot, error } = await createVideoTask({
        model,
        prompt: trimmed,
        duration,
        resolution,
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
      setBusy(false)
      requestAnimationFrame(() => {
        const node = scrollRef.current
        if (node) node.scrollTop = node.scrollHeight
      })
    }
  }

  return (
    <div
      className='flex min-h-0 flex-1 flex-col overflow-hidden'
      data-apiduo={apiduoBlockMark('playground:video')}
    >
      <div
        className='flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto px-4 py-4'
        ref={scrollRef}
      >
        {tasks.length === 0 ? (
          <div className='apd-stagger text-muted-foreground mx-auto flex w-full max-w-4xl flex-col items-center gap-3 py-16 text-center'>
            <div className='bg-muted flex size-11 items-center justify-center rounded-xl'>
              <VideoIcon className='size-5' />
            </div>
            <p className='text-foreground text-sm font-medium'>
              {t('Describe a shot to generate')}
            </p>
            <p className='max-w-sm text-[11.5px] leading-5'>
              {t(
                'Video runs as an asynchronous task: submit it, then poll for progress. Billing settles when the task finishes.'
              )}
            </p>
          </div>
        ) : (
          <div className='mx-auto flex w-full max-w-4xl flex-col gap-3'>
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
                      {t(task.status)}
                    </span>
                    <span className='text-muted-foreground font-mono text-[10.5px]'>
                      {task.durationSeconds}s · {resolution}
                    </span>
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
                    onCopyCurl={() => onCopyCurl(task.id)}
                    onOpenRequest={() => onOpenRequest(task.id)}
                    open={expanded}
                  />
                </div>
              )
            })}
          </div>
        )}
      </div>

      <div className='mx-auto w-full max-w-4xl px-1 pb-4'>
        <div className='border-border/70 bg-background/95 rounded-xl border shadow-sm'>
          <textarea
            className='placeholder:text-muted-foreground block min-h-20 w-full resize-none bg-transparent px-4 py-3 text-sm leading-6 outline-none'
            onChange={(event) => setPrompt(event.target.value)}
            onKeyDown={(event) => {
              if (
                event.key === 'Enter' &&
                !event.shiftKey &&
                !event.nativeEvent.isComposing
              ) {
                event.preventDefault()
                void handleSubmit()
              }
            }}
            placeholder={t('Describe the shot you want')}
            value={prompt}
          />
          <div className='border-border/60 flex flex-wrap items-center gap-2 border-t px-3 py-2'>
            <div className='bg-muted/60 inline-flex items-center gap-0.5 rounded-md p-0.5'>
              {VIDEO_DURATIONS.map((value) => (
                <button
                  className={cn(
                    'rounded px-2 py-0.5 font-mono text-[11px] transition-colors',
                    duration === value
                      ? 'bg-background text-foreground font-semibold shadow-sm'
                      : 'text-muted-foreground hover:text-foreground'
                  )}
                  key={value}
                  onClick={() => setDuration(value)}
                  type='button'
                >
                  {value}s
                </button>
              ))}
            </div>

            <div className='bg-muted/60 inline-flex items-center gap-0.5 rounded-md p-0.5'>
              {VIDEO_RESOLUTIONS.map((value) => (
                <button
                  className={cn(
                    'rounded px-2 py-0.5 font-mono text-[11px] transition-colors',
                    resolution === value
                      ? 'bg-background text-foreground font-semibold shadow-sm'
                      : 'text-muted-foreground hover:text-foreground'
                  )}
                  key={value}
                  onClick={() => setResolution(value)}
                  type='button'
                >
                  {value}
                </button>
              ))}
            </div>

            <div className='ml-auto flex items-center gap-2'>
              <Button
                className='apd-press h-7 text-[11.5px]'
                disabled={busy || !prompt.trim()}
                onClick={() => void handleSubmit()}
                size='sm'
              >
                {busy ? t('Submitting…') : t('Generate')}
              </Button>
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}
