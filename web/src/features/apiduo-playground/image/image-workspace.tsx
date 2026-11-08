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
import { DownloadIcon, ImageIcon, Loader2Icon, RotateCcwIcon } from 'lucide-react'
import { useCallback, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { apiduoBlockMark } from '@/features/apiduo-marks/marks'
import { Button } from '@/components/ui/button'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { cn } from '@/lib/utils'

import { BillPanel, MoneyChip } from '../components/message-billing'
import type { BillingLedger } from '../hooks/use-billing-ledger'
import type { ImageGenerationResult } from '../types'
import { generateImages, IMAGE_SIZES } from './api'

interface ImageWorkspaceProps {
  model: string
  group: string
  ledger: BillingLedger
  onOpenRequest: (key: string) => void
  onCopyCurl: (key: string) => void
  expandedBillingKey: string | null
  onToggleBilling: (key: string) => void
}

const PROMPT_SUGGESTIONS = [
  'A glass teapot on a windowsill, morning light',
  'Isometric server room, teal and amber lighting',
  'Hand-drawn map of a coastal town, ink on paper',
]

export function ImageWorkspace({
  model,
  group,
  ledger,
  onOpenRequest,
  onCopyCurl,
  expandedBillingKey,
  onToggleBilling,
}: ImageWorkspaceProps) {
  const { t } = useTranslation()
  const [prompt, setPrompt] = useState('')
  const [size, setSize] = useState<string>(IMAGE_SIZES[0])
  const [count, setCount] = useState(1)
  const [results, setResults] = useState<ImageGenerationResult[]>([])
  const [busy, setBusy] = useState(false)
  const scrollRef = useRef<HTMLDivElement | null>(null)

  const run = useCallback(
    async (text: string, imageCount: number, imageSize: string) => {
      const trimmed = text.trim()
      if (!trimmed || busy) return

      setBusy(true)
      const id = `img-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`
      setResults((prev) => [
        ...prev,
        {
          id,
          createdAt: Date.now(),
          items: [],
          quota: 0,
          status: 'pending',
          requestId: '',
        },
      ])

      const startedAt = Date.now()
      try {
        const result = await generateImages({
          model,
          prompt: trimmed,
          n: imageCount,
          size: imageSize,
        })

        if (result.error) {
          setResults((prev) =>
            prev.map((item) =>
              item.id === id
                ? { ...item, status: 'estimated', error: result.error }
                : item
            )
          )
          toast.error(result.error)
          return
        }

        setResults((prev) =>
          prev.map((item) =>
            item.id === id ? { ...item, items: result.items } : item
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
          imageCount,
        })
      } catch (error) {
        const message =
          error instanceof Error ? error.message : 'Request failed'
        setResults((prev) =>
          prev.map((item) =>
            item.id === id
              ? { ...item, status: 'estimated', error: message }
              : item
          )
        )
        toast.error(message)
      } finally {
        setBusy(false)
        requestAnimationFrame(() => {
          const node = scrollRef.current
          if (node) node.scrollTop = node.scrollHeight
        })
      }
    },
    [busy, model, group, ledger]
  )

  const handleSubmit = () => {
    if (!prompt.trim() || busy) return
    void run(prompt, count, size)
    setPrompt('')
  }

  return (
    <div
      className='flex min-h-0 flex-1 flex-col overflow-hidden'
      data-apiduo={apiduoBlockMark('playground:image')}
    >
      <div
        className='flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-4 py-4'
        ref={scrollRef}
      >
        {results.length === 0 ? (
          <div className='text-muted-foreground mx-auto flex w-full max-w-4xl flex-col items-center gap-3 py-16 text-center'>
            <div className='bg-muted flex size-11 items-center justify-center rounded-xl'>
              <ImageIcon className='size-5' />
            </div>
            <p className='text-foreground text-sm font-medium'>
              {t('Describe an image to generate')}
            </p>
            <div className='flex flex-wrap justify-center gap-1.5'>
              {PROMPT_SUGGESTIONS.map((item) => (
                <button
                  className='border-border hover:bg-muted rounded-full border px-2.5 py-1 text-[11.5px] transition-colors'
                  key={item}
                  onClick={() => setPrompt(item)}
                  type='button'
                >
                  {t(item)}
                </button>
              ))}
            </div>
          </div>
        ) : (
          <div className='mx-auto flex w-full max-w-4xl flex-col gap-4'>
            {results.map((result) => {
              const billing = ledger.billingByKey[result.id]
              const expanded = expandedBillingKey === result.id
              return (
                <div
                  className='border-border/60 bg-background rounded-xl border p-3'
                  key={result.id}
                >
                  <div className='mb-2 flex flex-wrap items-center gap-2 text-[11px] text-muted-foreground'>
                    <span className='font-mono'>
                      {new Date(result.createdAt).toLocaleTimeString()}
                    </span>
                    <span>·</span>
                    <span>{size}</span>
                    <span>·</span>
                    <span>{t('{{count}} images', { count: result.items.length || count })}</span>
                    <div className='ml-auto flex items-center gap-2'>
                      {result.error ? (
                        <span className='text-destructive'>{result.error}</span>
                      ) : null}
                      {billing ? (
                        <MoneyChip
                          billing={billing}
                          expanded={expanded}
                          onToggle={() => onToggleBilling(result.id)}
                        />
                      ) : (
                        <span className='flex items-center gap-1.5 font-mono'>
                          <Loader2Icon className='size-3 animate-spin' />
                          {t('Generating…')}
                        </span>
                      )}
                    </div>
                  </div>

                  {result.items.length === 0 && !result.error ? (
                    <div className='bg-muted/40 flex aspect-video w-full items-center justify-center rounded-lg'>
                      <Loader2Icon className='text-muted-foreground size-5 animate-spin' />
                    </div>
                  ) : (
                    <div
                      className={cn(
                        'grid gap-2',
                        result.items.length > 1
                          ? 'grid-cols-2 lg:grid-cols-3'
                          : 'grid-cols-1'
                      )}
                    >
                      {result.items.map((item) => (
                        <div
                          className='group bg-muted/40 relative overflow-hidden rounded-lg'
                          key={item.url}
                        >
                          <img
                            alt={item.revisedPrompt ?? ''}
                            className='h-full w-full object-cover'
                            src={item.url}
                          />
                          <a
                            className='bg-background/90 absolute right-1.5 bottom-1.5 rounded-md p-1 opacity-0 transition-opacity group-hover:opacity-100'
                            download={`image-${result.id}.png`}
                            href={item.url}
                          >
                            <DownloadIcon className='size-3.5' />
                          </a>
                        </div>
                      ))}
                    </div>
                  )}

                  {billing && expanded ? (
                    <BillPanel
                      billing={billing}
                      modelName={model}
                      onCopyCurl={() => onCopyCurl(result.id)}
                      onOpenRequest={() => onOpenRequest(result.id)}
                    />
                  ) : null}
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
              if (event.key === 'Enter' && !event.shiftKey && !event.isComposing) {
                event.preventDefault()
                handleSubmit()
              }
            }}
            placeholder={t('Describe the image you want')}
            value={prompt}
          />
          <div className='border-border/60 flex flex-wrap items-center gap-2 border-t px-3 py-2'>
            <Select onValueChange={setSize} value={size}>
              <SelectTrigger className='h-7 w-[118px] text-[11.5px]'>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {IMAGE_SIZES.map((item) => (
                  <SelectItem key={item} value={item}>
                    {item}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>

            <div className='bg-muted/60 inline-flex items-center gap-0.5 rounded-md p-0.5'>
              {[1, 2, 3, 4].map((value) => (
                <button
                  className={cn(
                    'rounded px-2 py-0.5 font-mono text-[11px] transition-colors',
                    count === value
                      ? 'bg-background text-foreground font-semibold shadow-sm'
                      : 'text-muted-foreground hover:text-foreground'
                  )}
                  key={value}
                  onClick={() => setCount(value)}
                  type='button'
                >
                  ×{value}
                </button>
              ))}
            </div>

            <div className='ml-auto flex items-center gap-2'>
              {results.length > 0 ? (
                <Button
                  className='h-7 gap-1 text-[11.5px]'
                  onClick={() => setResults([])}
                  size='sm'
                  variant='ghost'
                >
                  <RotateCcwIcon className='size-3.5' />
                  {t('Clear')}
                </Button>
              ) : null}
              <Button
                className='h-7 text-[11.5px]'
                disabled={busy || !prompt.trim()}
                onClick={handleSubmit}
                size='sm'
              >
                {busy ? t('Generating…') : t('Generate')}
              </Button>
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}
