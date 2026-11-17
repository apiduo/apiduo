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
import { Megaphone } from 'lucide-react'
/* APIDUO:announcement:BEGIN */
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { IconBadge } from '@/components/ui/icon-badge'
import { ScrollArea } from '@/components/ui/scroll-area'
import { useAnnouncements } from '@/features/dashboard/hooks/use-status-data'
import { getPreviewText } from '@/features/dashboard/lib'
import type { AnnouncementItem } from '@/features/dashboard/types'
import {
  ANNOUNCEMENT_LEVEL_BADGES,
  ANNOUNCEMENT_TEXT_COLORS,
  type AnnouncementType,
} from '@/lib/colors'
import { formatShortDateTime } from '@/lib/time'
import { cn } from '@/lib/utils'
import { useNotificationStore } from '@/stores/notification-store'

import { PanelWrapper } from '../ui/panel-wrapper'
import { AnnouncementDetailModal } from './announcement-detail-dialog'

export function AnnouncementsPanel() {
  const { t } = useTranslation()
  const { items: list, loading } = useAnnouncements()
  const { markAnnouncementsRead } = useNotificationStore()
  const [selectedAnnouncement, setSelectedAnnouncement] =
    useState<AnnouncementItem | null>(null)
  const [isDialogOpen, setIsDialogOpen] = useState(false)
  // 被遮住的条数。固定 h-72 里放不下时给个可见的交代 ——
  // macOS 的覆盖式滚动条平时不显示，没有提示用户会以为已经到底了。
  const scrollHostRef = useRef<HTMLDivElement>(null)
  const [hiddenCount, setHiddenCount] = useState(0)

  useEffect(() => {
    const viewport = scrollHostRef.current?.querySelector<HTMLElement>(
      '[data-slot="scroll-area-viewport"]'
    )
    if (!viewport) return

    const measure = () => {
      const rows = [...viewport.querySelectorAll('button')]
      if (!rows.length) {
        setHiddenCount(0)
        return
      }
      // 底边超出视口就算一条，半条也算 —— 那正是要提示的情况
      const bounds = viewport.getBoundingClientRect()
      setHiddenCount(
        rows.filter((row) => row.getBoundingClientRect().bottom > bounds.bottom + 1)
          .length
      )
    }

    // 首帧时列表刚挂上去，尺寸还没稳定；等下一帧再量。
    // 滚动时不需要延迟，直接在事件里量。
    const firstMeasure = requestAnimationFrame(measure)
    viewport.addEventListener('scroll', measure, { passive: true })
    const observer = new ResizeObserver(measure)
    observer.observe(viewport)
    return () => {
      cancelAnimationFrame(firstMeasure)
      viewport.removeEventListener('scroll', measure)
      observer.disconnect()
    }
  }, [list.length])

  const handleAnnouncementClick = (item: AnnouncementItem) => {
    // 与铃铛共用同一套已读 key（`id:<id>`），点开后未读数立刻下来
    if (item.id !== undefined && item.id !== null) {
      markAnnouncementsRead([`id:${item.id}`])
    }
/* APIDUO:announcement:END */
    setSelectedAnnouncement(item)
    setIsDialogOpen(true)
  }

  return (
    <PanelWrapper
      title={
        <span className='flex items-center gap-2'>
          <IconBadge tone='warning' size='sm'>
            <Megaphone />
          </IconBadge>
          {t('Announcements')}
        </span>
      }
      description={t('Latest platform updates and notices')}
      loading={loading}
      empty={!list.length}
      emptyMessage={t('No announcements at this time')}
      height='h-72'
      contentClassName='p-0'
    >
/* APIDUO:announcement:BEGIN */
      <div ref={scrollHostRef} className='relative'>
        <ScrollArea className='h-72'>
        <div>
          {list.map((item: AnnouncementItem, idx: number) => {
            const key = item.id ?? `announcement-${idx}`
            const type = (item.type || 'default') as AnnouncementType
            // 普通公告不打标签：给「没有级别」也贴一个标签，只是多一行噪音
            const showLevel = type !== 'default'
            return (
              <button
                key={key}
                type='button'
                onClick={() => handleAnnouncementClick(item)}
                className={cn(
                  'hover:bg-muted/40 focus-visible:ring-ring w-full px-3 py-3 text-left transition-colors focus-visible:ring-2 focus-visible:outline-none sm:px-5 sm:py-3.5',
                  idx < list.length - 1 && 'border-border/60 border-b'
                )}
              >
                <p className='line-clamp-1 text-sm font-medium'>
                  {item.title?.trim() || getPreviewText(item.content)}
                </p>
                {(item.publishDate || showLevel) && (
                  <div className='mt-0.5 flex items-center gap-1.5'>
                    {item.publishDate && (
                      <time className='text-muted-foreground font-mono text-xs tabular-nums'>
                        {formatShortDateTime(new Date(item.publishDate))}
                      </time>
                    )}
                    {item.publishDate && showLevel && (
                      <span aria-hidden className='text-[10px] opacity-40'>
                        ·
                      </span>
                    )}
                    {showLevel && (
                      <span
                        className='text-[11.5px] font-semibold'
                        style={{ color: ANNOUNCEMENT_TEXT_COLORS[type] }}
                      >
                        {t(ANNOUNCEMENT_LEVEL_BADGES[type].label)}
                      </span>
                    )}
                  </div>
                )}
              </button>
            )
          })}
        </div>
        </ScrollArea>
        {hiddenCount > 0 && (
          <div className='pointer-events-none absolute inset-x-0 bottom-0 flex h-14 items-end justify-center bg-gradient-to-t from-card via-card/85 to-transparent pb-1.5'>
            <span className='bg-card text-muted-foreground rounded-full px-2 py-0.5 text-[11px] tabular-nums'>
              {t('{{count}} more', { count: hiddenCount })}
            </span>
          </div>
        )}
      </div>
/* APIDUO:announcement:END */

      <AnnouncementDetailModal
        open={isDialogOpen}
        onOpenChange={setIsDialogOpen}
        announcement={selectedAnnouncement}
      />
    </PanelWrapper>
  )
}
