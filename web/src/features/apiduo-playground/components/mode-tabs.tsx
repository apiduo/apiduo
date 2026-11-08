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
import { useLayoutEffect, useRef, useState } from 'react'
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

interface PillGeometry {
  x: number
  width: number
}

export function ModeTabs({ value, onChange, disabled }: ModeTabsProps) {
  const { t } = useTranslation()
  const hostRef = useRef<HTMLDivElement | null>(null)
  const buttonRefs = useRef(new Map<PlaygroundMode, HTMLButtonElement>())
  const [pill, setPill] = useState<PillGeometry | null>(null)

  /*
   * The indicator is one element that travels, instead of a background class
   * that hops between buttons. Hopping reads as two separate states; travelling
   * reads as one object moving, which is what tells the user the three tabs are
   * the same control.
   *
   * Measurement is layout-effect + ResizeObserver rather than a fixed width per
   * tab: the labels are translated (聊天 / 图片 / 视频 vs Chat / Image / Video)
   * and a hardcoded width would be wrong in five of the seven locales.
   */
  useLayoutEffect(() => {
    const host = hostRef.current
    if (!host) return

    const measure = () => {
      const node = buttonRefs.current.get(value)
      if (!node) return
      const hostRect = host.getBoundingClientRect()
      const rect = node.getBoundingClientRect()
      setPill({
        x: rect.left - hostRect.left - host.clientLeft,
        width: rect.width,
      })
    }

    measure()
    const observer = new ResizeObserver(measure)
    observer.observe(host)
    for (const node of buttonRefs.current.values()) observer.observe(node)
    return () => observer.disconnect()
  }, [value])

  return (
    <div
      className='bg-muted/60 relative inline-flex shrink-0 items-center gap-0.5 rounded-lg p-0.5'
      data-apiduo={apiduoBlockMark('playground:modes')}
      ref={hostRef}
      role='tablist'
    >
      <span
        aria-hidden='true'
        className='apd-tab-pill'
        style={
          pill
            ? { transform: `translateX(${pill.x}px)`, width: pill.width }
            : // First frame: the geometry is not measured yet. Starting hidden
              // avoids a pill flashing at x=0 and then sliding across.
              { opacity: 0 }
        }
      />
      {MODES.map((mode) => {
        const Icon = mode.icon
        const active = mode.value === value
        return (
          <button
            aria-selected={active}
            className={cn(
              // Weight stays constant across all three: a bolder active label
              // changes the button width, which would drag its neighbours
              // sideways on every switch. The travelling pill carries "active".
              'apd-press relative inline-flex items-center gap-1.5 rounded-md px-2.5 py-1 text-[11.5px] font-medium',
              'focus-visible:ring-ring focus-visible:ring-2 focus-visible:outline-none',
              active ? 'text-foreground' : 'text-muted-foreground hover:text-foreground',
              disabled && 'cursor-not-allowed opacity-50'
            )}
            disabled={disabled}
            key={mode.value}
            onClick={() => onChange(mode.value)}
            ref={(node) => {
              if (node) buttonRefs.current.set(mode.value, node)
              else buttonRefs.current.delete(mode.value)
            }}
            role='tab'
            type='button'
          >
            <Icon
              aria-hidden='true'
              className={cn(
                'size-3.5 transition-colors',
                active && 'text-[var(--apiduo-ink)]'
              )}
            />
            {t(mode.label)}
          </button>
        )
      })}
    </div>
  )
}
