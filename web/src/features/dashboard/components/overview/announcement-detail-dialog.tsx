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
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { RichContent } from '@/components/rich-content'
/* APIDUO:announcement:BEGIN */
import { StatusBadge } from '@/components/status-badge'
import { ScrollArea } from '@/components/ui/scroll-area'
import { ANNOUNCEMENT_LEVEL_BADGES } from '@/lib/colors'
import { formatDateTimeObject } from '@/lib/time'

import type { AnnouncementPhase } from '../../types'
import { AnnouncementTimeline } from './announcement-timeline'

interface AnnouncementDetailModalProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  announcement: {
    title?: string
    content?: string
    tag?: string
    publishDate?: string
    extra?: string
    type?: string
    phases?: AnnouncementPhase[]
  } | null
}

/**
 * 列表里只靠色条区分级别，弹窗里必须补上文字 —— 否则点进来反而看不出来。
 * 文案复用管理页已有的级别 key。
 */
export function AnnouncementDetailModal({
  open,
  onOpenChange,
  announcement,
}: AnnouncementDetailModalProps) {
  const { t } = useTranslation()
  const level = announcement?.type
    ? ANNOUNCEMENT_LEVEL_BADGES[announcement.type]
    : undefined

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      // 有标题就用它：列表点进来时标题还在视野里，但正文往往很长，
      // 弹窗里没有标题就只能靠"已发布"那行来确认读的是哪一条。
      title={announcement?.title?.trim() || t('Announcement Details')}
      titleClassName='line-clamp-2'
      description={
        announcement?.publishDate
          ? `${t('Published:')} ${formatDateTimeObject(new Date(announcement.publishDate))}`
          : undefined
      }
      contentClassName='sm:max-w-lg'
      contentHeight='auto'
      bodyClassName='space-y-4'
    >
      <ScrollArea className='max-h-[min(58vh,520px)] pr-4'>
        <div className='space-y-4'>
          {level && (
            <div>
              <StatusBadge
                label={t(level.label)}
                variant={level.variant}
                copyable={false}
              />
            </div>
          )}
          {announcement?.content && (
            <div>
              <h4 className='mb-2 font-medium'>{t('Content')}</h4>
              <RichContent breaks content={announcement.content} />
            </div>
          )}
          {announcement?.phases && announcement.phases.length > 0 && (
            <div>
              <h4 className='mb-2 font-medium'>{t('Timeline')}</h4>
              <AnnouncementTimeline phases={announcement.phases} />
            </div>
          )}
{/* APIDUO:announcement:END */}
          {announcement?.extra && (
            <div>
              <h4 className='mb-2 font-medium'>
                {t('Additional Information')}
              </h4>
              <RichContent
                breaks
                content={announcement.extra}
                className='text-muted-foreground'
              />
            </div>
          )}
        </div>
      </ScrollArea>
    </Dialog>
  )
}
