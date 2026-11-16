import { Megaphone, X } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { getPreviewText } from '@/features/dashboard/lib'
import { useNotifications } from '@/hooks/use-notifications'
import { formatShortDateTime } from '@/lib/time'

/**
 * 一条 changelog 的标题，没有就用正文摘要。
 */
function changelogTitle(item: Record<string, unknown>): string {
  const title = typeof item.title === 'string' ? item.title.trim() : ''
  if (title) return title
  return getPreviewText(typeof item.content === 'string' ? item.content : '')
}

/**
 * 顶部的功能更新横幅。
 *
 * 与公告面板的分工是刻意的：
 *   - 公告是「需要你知道/行动」，常驻在仪表盘面板，进铃铛未读
 *   - 功能更新是「有什么新东西」，出现一次、看过即消失，不进未读总数
 *
 * 所以这里不罗列条目，只报一个数量；展开才看得到具体内容。
 * 三条和三十条都占一行，这是它能承受高频更新的原因。
 */
export function AnnouncementBanner() {
  const { t } = useTranslation()
  const { unreadChangelogs, markChangelogsAsRead } = useNotifications()
  const [expanded, setExpanded] = useState(false)

  if (unreadChangelogs.length === 0) return null

  const latest = unreadChangelogs[0]

  return (
    <div className='bg-muted/40 border-b'>
      <div className='flex items-center gap-3 px-4 py-1.5 sm:px-6'>
        <Megaphone className='text-muted-foreground size-4 shrink-0' aria-hidden />
        <p className='min-w-0 flex-1 truncate text-sm'>
          <span className='font-medium'>
            {t('{{count}} new feature updates', {
              count: unreadChangelogs.length,
            })}
          </span>
          <span className='text-muted-foreground ml-2 text-xs'>
            {changelogTitle(latest)}
          </span>
        </p>
        <Button
          size='sm'
          variant='ghost'
          className='shrink-0'
          onClick={() => setExpanded((value) => !value)}
        >
          {expanded ? t('Hide') : t('View')}
        </Button>
        <Button
          size='icon'
          variant='ghost'
          className='size-7 shrink-0'
          aria-label={t('Close')}
          onClick={() => {
            markChangelogsAsRead()
            setExpanded(false)
          }}
        >
          <X className='size-4' />
        </Button>
      </div>
      {expanded && (
        <ul className='max-h-64 overflow-y-auto border-t px-4 py-1 sm:px-6'>
          {unreadChangelogs.map((item, index) => {
            const published =
              typeof item.publishDate === 'string' ? item.publishDate : ''
            const body = typeof item.content === 'string' ? item.content : ''
            return (
              <li
                key={typeof item.id === 'number' ? item.id : index}
                className='flex gap-3 border-b py-2 last:border-0'
              >
                <time className='text-muted-foreground shrink-0 font-mono text-xs whitespace-nowrap tabular-nums'>
                  {published ? formatShortDateTime(new Date(published)) : ''}
                </time>
                <div className='min-w-0'>
                  <p className='text-sm font-medium'>{changelogTitle(item)}</p>
                  {body && (
                    <p className='text-muted-foreground mt-0.5 text-xs whitespace-pre-line'>
                      {body}
                    </p>
                  )}
                </div>
              </li>
            )
          })}
        </ul>
      )}
    </div>
  )
}
