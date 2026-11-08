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
import { ImageIcon, MessageSquareIcon, VideoIcon } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { apiduoBlockMark } from '@/features/apiduo-marks/marks'
import { cn } from '@/lib/utils'

import type { PlaygroundMode } from '../types'

const MODES: Array<{
  value: PlaygroundMode
  label: string
  icon: typeof MessageSquareIcon
}> = [
  { value: 'chat', label: 'Chat', icon: MessageSquareIcon },
  { value: 'image', label: 'Image', icon: ImageIcon },
  { value: 'video', label: 'Video', icon: VideoIcon },
]

interface ModeTabsProps {
  value: PlaygroundMode
  onChange: (mode: PlaygroundMode) => void
  disabled?: boolean
}

export function ModeTabs({ value, onChange, disabled }: ModeTabsProps) {
  const { t } = useTranslation()

  return (
    <div
      className='bg-muted/60 inline-flex shrink-0 items-center gap-0.5 rounded-lg p-0.5'
      data-apiduo={apiduoBlockMark('playground:modes')}
      role='tablist'
    >
      {MODES.map((mode) => {
        const Icon = mode.icon
        const active = mode.value === value
        return (
          <button
            aria-selected={active}
            className={cn(
              'inline-flex items-center gap-1.5 rounded-md px-2.5 py-1 text-[11.5px] transition-colors',
              'focus-visible:ring-ring focus-visible:ring-2 focus-visible:outline-none',
              active
                ? 'bg-background text-foreground font-semibold shadow-sm'
                : 'text-muted-foreground hover:text-foreground',
              disabled && 'cursor-not-allowed opacity-50'
            )}
            disabled={disabled}
            key={mode.value}
            onClick={() => onChange(mode.value)}
            role='tab'
            type='button'
          >
            <Icon aria-hidden='true' className='size-3.5' />
            {t(mode.label)}
          </button>
        )
      })}
    </div>
  )
}
