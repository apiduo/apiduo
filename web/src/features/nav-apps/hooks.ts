import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import type { AxiosError } from 'axios'
import i18next from 'i18next'
import { toast } from 'sonner'

import { useAuthStore } from '@/stores/auth-store'

import { getAllNavApps, getNavApps, saveNavApps } from './api'
import type { NavApp, NavAppListResponse } from './types'

export const NAV_APPS_QUERY_KEY = ['nav-apps'] as const

/** Stable reference so consumers can depend on it in memo arrays. */
const EMPTY_APPS: NavApp[] = []

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
