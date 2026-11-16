import { api } from '@/lib/api'

import type {
  GrowthAlertItem,
  GrowthSettings,
  GrowthAlertStats,
  GrowthBatch,
  GrowthBoardRow,
  GrowthListResponse,
  GrowthOverview,
  GrowthModelStat,
  GrowthProfile,
  GrowthTouch,
} from './types'

interface Envelope<T> {
  success: boolean
  message?: string
  data: T
}

function unwrap<T>(payload: Envelope<T>): T {
  return payload.data
}

export async function getGrowthOverview(): Promise<GrowthOverview> {
  const res = await api.get<Envelope<GrowthOverview>>('/api/growth/overview')
  return unwrap(res.data)
}

export async function getGrowthUsers(params: {
  page?: number
  pageSize?: number
  tier?: string
  keyword?: string
  sortBy?: string
  sortOrder?: 'asc' | 'desc'
}): Promise<GrowthListResponse<GrowthBoardRow>> {
  const res = await api.get<Envelope<GrowthListResponse<GrowthBoardRow>>>(
    '/api/growth/users',
    {
      params: {
        p: params.page ?? 1,
        page_size: params.pageSize ?? 20,
        tier: params.tier || undefined,
        keyword: params.keyword || undefined,
        sort_by: params.sortBy || undefined,
        sort_order: params.sortOrder || undefined,
      },
    }
  )
  return unwrap(res.data)
}

export async function getGrowthAlerts(includeClosed = false): Promise<{
  items: GrowthAlertItem[]
  stats: GrowthAlertStats[]
  follow_ups: GrowthTouch[]
}> {
  const res = await api.get<
    Envelope<{
      items: GrowthAlertItem[]
      stats: GrowthAlertStats[]
      follow_ups: GrowthTouch[]
    }>
  >('/api/growth/alerts', { params: { status: includeClosed ? 'all' : 'open' } })
  return unwrap(res.data)
}

export async function updateGrowthAlertStatus(
  userId: number,
  kind: string,
  status: 'done' | 'ignore' | 'open'
): Promise<void> {
  await api.post(`/api/growth/alerts/${userId}/${kind}/status`, { status })
}

export async function getGrowthProfile(userId: number): Promise<GrowthProfile> {
  const res = await api.get<Envelope<GrowthProfile>>(
    `/api/growth/users/${userId}`
  )
  return unwrap(res.data)
}

export async function createGrowthTouch(
  userId: number,
  body: {
    method: string
    contact_role: string
    feedback: string
    conclusion: string
    follow_up_at: number
  }
): Promise<GrowthTouch> {
  const res = await api.post<Envelope<GrowthTouch>>(
    `/api/growth/users/${userId}/touches`,
    body
  )
  return unwrap(res.data)
}

export async function updateGrowthTier(
  userId: number,
  tier: string,
  reason: string
): Promise<void> {
  await api.put(`/api/growth/users/${userId}/tier`, { tier, reason })
}

export async function getGrowthBatches(): Promise<GrowthBatch[]> {
  const res = await api.get<Envelope<GrowthBatch[]>>('/api/growth/batches')
  return unwrap(res.data)
}

export async function createGrowthBatch(body: {
  name: string
  criteria: string
  benefit_type: string
  benefit_amount: number
  window_days: number
  tier: string
}): Promise<GrowthBatch> {
  const res = await api.post<Envelope<GrowthBatch>>(
    '/api/growth/batches',
    body
  )
  return unwrap(res.data)
}

export async function recomputeGrowth(): Promise<void> {
  await api.post('/api/growth/recompute')
}

export async function getGrowthSettings(): Promise<GrowthSettings> {
  const res = await api.get<Envelope<GrowthSettings>>('/api/growth/settings')
  return unwrap(res.data)
}

export async function updateGrowthSettings(
  body: GrowthSettings
): Promise<GrowthSettings> {
  const res = await api.put<Envelope<GrowthSettings>>(
    '/api/growth/settings',
    body
  )
  return unwrap(res.data)
}

export async function stopGrowthBatch(id: number): Promise<void> {
  await api.post(`/api/growth/batches/${id}/stop`)
}

export async function getGrowthModels(): Promise<GrowthModelStat[]> {
  const res = await api.get<Envelope<GrowthModelStat[]>>('/api/growth/models')
  return unwrap(res.data)
}
