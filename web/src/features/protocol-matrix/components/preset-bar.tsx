import { X } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Popover,
  PopoverContent,
  PopoverTitle,
  PopoverTrigger,
} from '@/components/ui/popover'
import { cn } from '@/lib/utils'

import { modeLabel } from '../lib/mode-label'
import type { StoredPreset } from '../lib/preset-api'
import { type Preset, type PresetEntry, MATRIX_PRESETS } from '../lib/presets'

/**
 * 预设条：内置五条 + 自定义模板 + 「存为模板」。
 *
 * 自定义模板存的是**当前渠道的形状**（端点 / 转换器 / 上游路径），不含模型清单
 * ——套到新渠道时模型名几乎一定不同。所以这里不做逐项编辑器：把渠道配好，
 * 点「存为模板」，名字一填就完事。
 */
export function PresetBar({
  custom,
  entries,
  onApply,
  onSave,
  onDelete,
}: {
  custom: StoredPreset[]
  /** 当前路由收成的模板内容，用于预览和禁用保存 */
  entries: PresetEntry[]
  onApply: (preset: Preset) => void
  onSave: (name: string) => Promise<void>
  onDelete: (id: string) => Promise<void>
}) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)

  const canSave = entries.length > 0 && name.trim().length > 0

  const submit = async () => {
    if (!canSave || busy) return
    setBusy(true)
    try {
      await onSave(name.trim())
      setOpen(false)
      setName('')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className='flex items-start justify-between gap-2'>
      <div className='flex min-w-0 flex-1 flex-wrap items-center gap-1.5'>
        <span className='text-muted-foreground text-xs'>{t('Presets')}</span>

        {MATRIX_PRESETS.map((preset) => (
          <Button
            key={preset.id}
            type='button'
            variant='outline'
            size='sm'
            title={preset.hint ? t(preset.hint) : undefined}
            onClick={() => onApply(preset)}
          >
            {t(preset.label)}
          </Button>
        ))}

        {custom.map((stored) => (
          <span
            key={stored.id}
            className={cn(
              'border-border inline-flex items-stretch overflow-hidden rounded-md border text-xs'
            )}
          >
            <button
              type='button'
              className='hover:bg-muted px-2 py-1'
              title={stored.entries
                .map(
                  (entry) =>
                    `${entry.endpoint} → ${modeLabel(entry.mode, t('Not supported'))}`
                )
                .join('\n')}
              onClick={() =>
                onApply({
                  id: `custom:${stored.id}`,
                  label: stored.name,
                  entries: stored.entries,
                  builtIn: false,
                })
              }
            >
              {stored.name}
            </button>
            <button
              type='button'
              aria-label={t('Delete preset')}
              className='border-border text-muted-foreground hover:bg-destructive/10 hover:text-destructive flex w-6 items-center justify-center border-l'
              onClick={() => void onDelete(stored.id)}
            >
              <X className='size-3' aria-hidden='true' />
            </button>
          </span>
        ))}
      </div>

      {/*
       * 用 Popover 而不是 Dialog：这个组件是挂在「高级自定义路由」的 Dialog 里面的，
       * 再嵌一层 Dialog 会在关闭时把父级的 Tabs 一起重挂载，用户存完模板就被弹回
       * 「路由转发」。Popover 没这个问题，而且和格子里选转换器的交互一致。
       */}
      <Popover open={open} onOpenChange={setOpen}>
        <PopoverTrigger
          render={
            <Button
              type='button'
              variant='ghost'
              size='sm'
              disabled={entries.length === 0}
            />
          }
        >
          {t('Save as preset')}
        </PopoverTrigger>
        <PopoverContent align='start' className='w-80'>
          <PopoverTitle className='text-sm font-medium'>
            {t('Save as preset')}
          </PopoverTitle>
          <p className='text-muted-foreground text-xs leading-relaxed'>
            {t(
              'Stores the shape of the current routes: endpoint, converter, upstream path. Model lists are not stored.'
            )}
          </p>
          <Input
            value={name}
            maxLength={60}
            placeholder={t('Preset name')}
            onChange={(event) => setName(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === 'Enter') void submit()
            }}
          />
          <ul className='max-h-48 overflow-auto rounded-md border p-2 text-xs'>
            {entries.map((entry) => (
              <li
                key={entry.endpoint}
                className='flex flex-wrap items-baseline gap-x-2 py-0.5'
              >
                <code className='font-mono'>{entry.endpoint}</code>
                <span className='text-muted-foreground'>
                  {modeLabel(entry.mode, t('Not supported'))}
                </span>
                {entry.upstreamPath &&
                  entry.upstreamPath !== entry.endpoint && (
                    <code className='text-muted-foreground font-mono'>
                      → {entry.upstreamPath}
                    </code>
                  )}
              </li>
            ))}
          </ul>
          <div className='flex justify-end gap-2'>
            <Button
              type='button'
              variant='outline'
              size='sm'
              onClick={() => setOpen(false)}
            >
              {t('Cancel')}
            </Button>
            <Button
              type='button'
              size='sm'
              disabled={!canSave || busy}
              onClick={() => void submit()}
            >
              {t('Save')}
            </Button>
          </div>
        </PopoverContent>
      </Popover>
    </div>
  )
}
