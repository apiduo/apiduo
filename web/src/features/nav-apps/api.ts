import { api } from '@/lib/api'

import type { NavAppListResponse, SaveNavAppsRequest } from './types'

/** Entries visible to the signed-in user. Backs the sidebar and header. */
export async function getNavApps(): Promise<NavAppListResponse> {
  const res = await api.get<NavAppListResponse>('/api/nav_apps')
  return res.data
}

/** Every entry, including disabled ones. Admin console only. */
export async function getAllNavApps(): Promise<NavAppListResponse> {
  const res = await api.get<NavAppListResponse>('/api/nav_apps/all')
  return res.data
}

/** Replaces the whole list in one write. */
export async function saveNavApps(
  request: SaveNavAppsRequest
): Promise<NavAppListResponse> {
  const res = await api.put<NavAppListResponse>('/api/nav_apps', request)
  return res.data
}
