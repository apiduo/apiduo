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
import { useQuery } from '@tanstack/react-query'
import { useState, useMemo } from 'react'

import { useStatus } from '@/hooks/use-status'
import { getNotice } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'
import { isChangelog } from '@/features/dashboard/lib/announcement-category'
import { useNotificationStore } from '@/stores/notification-store'

function hashString(input: string): string {
  let hash = 0
  if (!input) return '0'

  for (let i = 0; i < input.length; i += 1) {
    const chr = input.charCodeAt(i)
    hash = (hash << 5) - hash + chr
    hash |= 0
  }

  return hash.toString(36)
}

/**
 * Generate a unique key for an announcement
 * Prefer backend id, fall back to a content hash so edits register
 */
function getAnnouncementKey(item: Record<string, unknown>): string {
  if (!item) return ''

  if (item.id !== undefined && item.id !== null) {
    return `id:${item.id}`
  }

  const fingerprint = JSON.stringify({
    publishDate: (item?.publishDate as string) || '',
    content: ((item?.content as string) || '').trim(),
    extra: ((item?.extra as string) || '').trim(),
    type: (item?.type as string) || '',
    title: ((item?.title as string) || '').trim(),
    link: ((item?.link as string) || '').trim(),
  })
  return `hash:${hashString(fingerprint)}`
}

/**
 * Hook to manage notifications (Notice + Announcements)
 * Provides unread counts and read status management
 */
export function useNotifications() {
  const [popoverOpen, setPopoverOpen] = useState(false)
  const [activeTab, setActiveTab] = useState<'notice' | 'announcements'>(
    'notice'
  )

  // Fetch Notice from API
  const {
    data: noticeResponse,
    isLoading: noticeLoading,
    refetch: refetchNotice,
  } = useQuery({
    queryKey: ['notice'],
    queryFn: async () => requireServerSuccess(await getNotice()),
    staleTime: 1000 * 60 * 5, // 5 minutes
  })

  // Fetch Announcements from status
  const { status, loading: statusLoading } = useStatus()
  const announcementsEnabled = status?.announcements_enabled ?? false
  // 两者共用同一个数组，按 category 分流：
  // 公告进面板与铃铛，功能更新进顶部的横幅。
  const announcements = useMemo<Record<string, unknown>[]>(() => {
    if (!announcementsEnabled) return []
    return ((status?.announcements || []) as Record<string, unknown>[])
      .filter((item) => !isChangelog(item))
      .slice(0, 20)
  }, [announcementsEnabled, status?.announcements])

  const changelogs = useMemo<Record<string, unknown>[]>(() => {
    if (!announcementsEnabled) return []
    return ((status?.announcements || []) as Record<string, unknown>[])
      .filter((item) => isChangelog(item))
      .slice(0, 20)
  }, [announcementsEnabled, status?.announcements])

  // Notification store
  const {
    lastReadNotice,
    readAnnouncementKeys,
    markNoticeRead,
    markAnnouncementsRead,
  } = useNotificationStore()

  // Extract notice content
  const noticeContent = noticeResponse?.success
    ? (noticeResponse.data || '').trim()
    : ''

  // 未读的功能更新条目本身也要给横幅用，不只是数量
  // 依赖 readAnnouncementKeys 而不是 isAnnouncementRead：后者的函数引用在
  // store 里是稳定的，用它做依赖会让这个 memo 永远不重算 ——
  // 点「关闭」标记已读后横幅不消失，就是这个原因。
  const unreadChangelogs = useMemo(
    () =>
      changelogs.filter(
        (item) => !readAnnouncementKeys.includes(getAnnouncementKey(item))
      ),
    [changelogs, readAnnouncementKeys]
  )

  // Calculate unread counts
  const unreadCounts = useMemo(() => {
    const noticeUnread =
      noticeContent && noticeContent !== lastReadNotice ? 1 : 0

    const announcementsUnread = announcements.filter(
      (item: Record<string, unknown>) =>
        !readAnnouncementKeys.includes(getAnnouncementKey(item))
    ).length

    // 功能更新不进 total：铃铛每次发版都变红，两周后用户就无视它了。
    // 它的提示位是顶部横幅，只报数量。
    return {
      notice: noticeUnread,
      announcements: announcementsUnread,
      changelog: unreadChangelogs.length,
      total: noticeUnread + announcementsUnread,
    }
  }, [
    noticeContent,
    lastReadNotice,
    announcements,
    unreadChangelogs,
    readAnnouncementKeys,
  ])

  const markAnnouncementsAsRead = () => {
    if (announcements.length > 0) {
      const allKeys = announcements.map((item: Record<string, unknown>) =>
        getAnnouncementKey(item)
      )
      markAnnouncementsRead(allKeys)
    }
  }

  // Handle popover open
  const handleOpenPopover = (tab?: 'notice' | 'announcements') => {
    const nextTab = tab || activeTab

    // Mark currently visible content as read when opening the notification center
    if (noticeContent) {
      markNoticeRead(noticeContent)
    }
    if (nextTab === 'announcements') {
      markAnnouncementsAsRead()
    }

    setActiveTab(nextTab)
    setPopoverOpen(true)
  }

  const handlePopoverOpenChange = (open: boolean) => {
    if (open) {
      handleOpenPopover(activeTab)
      return
    }

    setPopoverOpen(false)
  }

  // Handle tab change - mark announcements as read when switching to that tab
  const handleTabChange = (tab: 'notice' | 'announcements') => {
    setActiveTab(tab)

    if (tab === 'announcements') {
      markAnnouncementsAsRead()
    }
  }

  return {
    // Data
    notice: noticeContent,
    announcements,
    loading: noticeLoading || statusLoading,

    // Unread counts
    unreadCount: unreadCounts.total,
    unreadNoticeCount: unreadCounts.notice,
    unreadAnnouncementsCount: unreadCounts.announcements,

    // 功能更新：只服务顶部横幅，与铃铛分开计数
    unreadChangelogs,
    unreadChangelogCount: unreadCounts.changelog,
    markChangelogsAsRead: () => {
      const keys = unreadChangelogs.map((item) => getAnnouncementKey(item))
      if (keys.length > 0) markAnnouncementsRead(keys)
    },

    // Popover state
    popoverOpen,
    setPopoverOpen: handlePopoverOpenChange,
    activeTab,
    setActiveTab: handleTabChange,

    // Actions
    openPopover: handleOpenPopover,
    closePopover: () => setPopoverOpen(false),
    refetchNotice,
  }
}
