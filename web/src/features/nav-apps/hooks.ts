import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import type { AxiosError } from 'axios'
import i18next from 'i18next'
import { useCallback, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { fetchActiveChatKey } from '@/features/chat/hooks/use-active-chat-key'
import { useChatPresets } from '@/features/chat/hooks/use-chat-presets'
import { handleServerError } from '@/lib/handle-server-error'
import { useAuthStore } from '@/stores/auth-store'

import { getAllNavApps, getNavApps, saveNavApps } from './api'
import { planNavAppOpen } from './lib/open'
import { navAppNeedsToken } from './lib/template'
import type { NavApp, NavAppGroup, NavAppListResponse } from './types'

export const NAV_APPS_QUERY_KEY = ['nav-apps'] as const

/** Stable reference so consumers can depend on it in memo arrays. */
const EMPTY_APPS: NavApp[] = []
const EMPTY_GROUPS: NavAppGroup[] = []
const EMPTY_ORDER: string[] = []

/**
 * Entries the signed-in user may see.
 *
 * Disabled while signed out: the header renders on public pages too, and the
 * endpoint requires authentication.
 */
export function useNavAppsQuery() {
  const isAuthenticated = useAuthStore((state) => !!state.auth.user)
  return useQuery({
    queryKey: [...NAV_APPS_QUERY_KEY, 'visible'],
    queryFn: getNavApps,
    enabled: isAuthenticated,
    // Short enough that an admin edit becomes visible without a reload, long
    // enough that navigating between pages does not refetch.
    staleTime: 60_000,
  })
}

/**
 * Heading of the group that holds the configured entries.
 *
 * Empty means the administrator kept the default, and the caller falls back to
 * the translated label.
 */
export function useNavAppGroupTitle(): string {
  const { data } = useNavAppsQuery()
  return typeof data?.group_title === 'string' ? data.group_title.trim() : ''
}

/** Administrator-defined groups, in configured order. */
export function useNavAppGroups(): NavAppGroup[] {
  const { data } = useNavAppsQuery()
  return Array.isArray(data?.groups) ? data.groups : EMPTY_GROUPS
}

/** Top-level sidebar order, as group ids. */
export function useNavAppGroupOrder(): string[] {
  const { data } = useNavAppsQuery()
  return Array.isArray(data?.group_order) ? data.group_order : EMPTY_ORDER
}

export function useNavApps(): NavApp[] {
  const { data } = useNavAppsQuery()
  // Array.isArray rather than a truthiness check: the renderer calls array
  // methods directly, so an unexpected payload shape must degrade to the empty
  // list instead of throwing inside navigation.
  return data?.success && Array.isArray(data.data) ? data.data : EMPTY_APPS
}

/** Every entry, including disabled ones. Admin console only. */
export function useAllNavApps() {
  return useQuery({
    queryKey: [...NAV_APPS_QUERY_KEY, 'all'],
    queryFn: getAllNavApps,
    staleTime: 0,
  })
}

/** Translates a server response into a message the console can show. */
export function navAppErrorMessage(data: NavAppListResponse): string {
  if (data.message_key) {
    return String(i18next.t(data.message_key, data.message_params as never))
  }
  return data.message || i18next.t('Failed to save navigation entries')
}

export function useSaveNavApps() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: saveNavApps,
    onSuccess: (data) => {
      if (!data.success) {
        toast.error(navAppErrorMessage(data))
        return
      }
      // Invalidates both the visible and the admin list.
      queryClient.invalidateQueries({ queryKey: NAV_APPS_QUERY_KEY })
      toast.success(i18next.t('Navigation entries saved'))
    },
    onError: (error: AxiosError<NavAppListResponse>) => {
      // A 409 carries the concurrency message; anything else falls back to the
      // generic failure text so the reason is never swallowed.
      const payload = error.response?.data
      if (payload && (payload.message || payload.message_key)) {
        toast.error(navAppErrorMessage(payload))
        return
      }
      toast.error(i18next.t('Failed to save navigation entries'))
    },
  })
}

/**
 * Opens a configured entry, filling any placeholder with the viewer's own key.
 *
 * The key is fetched on click rather than stored with the entry: the list is
 * shared by everyone who can see it, so a credential baked into it would be
 * handed to every one of them.
 *
 * One click at a time is enforced because the fetch takes a round trip; a
 * second click while it is in flight would otherwise launch the client twice.
 */
export function useNavAppOpener() {
  const { t } = useTranslation()
  const { serverAddress } = useChatPresets()
  const busy = useRef(false)

  return useCallback(
    async (href: string) => {
      // A second click while the key is in flight would launch the client
      // twice; dropping it is quieter than a toast the user cannot act on.
      if (busy.current) return

      let apiKey: string | undefined
      if (navAppNeedsToken(href)) {
        busy.current = true
        try {
          apiKey = await fetchActiveChatKey()
        } catch (error) {
          handleServerError(
            error,
            t(
              'Unable to prepare chat link. Please ensure you have an enabled API key.'
            )
          )
          return
        } finally {
          busy.current = false
        }
      }

      const plan = planNavAppOpen({ href, apiKey, serverAddress })
      switch (plan.action) {
        case 'missing-key':
          toast.error(
            t(
              'Unable to prepare chat link. Please ensure you have an enabled API key.'
            )
          )
          return
        case 'invalid':
          toast.error(
            t('Invalid chat link. Please contact the administrator.')
          )
          return
        case 'client':
          // Assigning location keeps the click's user activation, which the
          // browser's external-protocol prompt requires. window.open would also
          // leave an empty tab behind once the application takes over.
          window.location.href = plan.url
          return
        case 'tab':
          window.open(plan.url, '_blank', 'noopener')
          return
      }
    },
    [serverAddress, t]
  )
}
