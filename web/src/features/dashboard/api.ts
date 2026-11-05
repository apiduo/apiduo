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
import { api } from '@/lib/api'

import type {
  FlowQuotaDataItem,
  QuotaDataItem,
  SeriesGroup,
  SkQuotaData,
  UptimeGroupResult,
  UserBalanceItem,
} from './types'

// ============================================================================
// Dashboard APIs
// ============================================================================

// ----------------------------------------------------------------------------
// Quota & Usage Data
// ----------------------------------------------------------------------------

// Get user quota data within a time range
// Admin users get all users' data by default.
export async function getUserQuotaDates(
  params: {
    start_timestamp: number
    end_timestamp: number
    default_time?: string
    username?: string
  },
  isAdmin = false
) {
  const endpoint = isAdmin ? '/api/data' : '/api/data/self'
  const res = await api.get<{ success: boolean; data: QuotaDataItem[] }>(
    endpoint,
    { params }
  )
  return res.data
}

// ----------------------------------------------------------------------------
// System Monitoring
// ----------------------------------------------------------------------------

export async function getUserQuotaDataByUsers(params: {
  start_timestamp: number
  end_timestamp: number
}) {
  const res = await api.get<{ success: boolean; data: QuotaDataItem[] }>(
    '/api/data/users',
    { params }
  )
  return res.data
}

export async function getFlowQuotaDates(
  params: {
    start_timestamp: number
    end_timestamp: number
    default_time?: string
    username?: string
  },
  isAdmin = false
) {
  const endpoint = isAdmin ? '/api/data/flow' : '/api/data/flow/self'
  const res = await api.get<{
    success: boolean
    data?: FlowQuotaDataItem[]
    message?: string
  }>(endpoint, { params })
  return res.data
}

// Get uptime monitoring status for all services
export async function getUptimeStatus() {
  const res = await api.get<{ success: boolean; data: UptimeGroupResult[] }>(
    '/api/uptime/status'
  )
  return res.data
}

// Get usage grouped by token (SK), model and user. Admin only.
// Rows are the highest-spending buckets; `totals` always covers the full range.
export async function getSkQuotaData(params: {
  start_timestamp: number
  end_timestamp: number
  username?: string
  model?: string
  limit?: number
}) {
  const res = await api.get<{ success: boolean; data: SkQuotaData }>(
    '/api/data/sk',
    { params }
  )
  return res.data
}

// Get daily usage series grouped by model, user or channel. Admin only.
export async function getUsageSeries(params: {
  start_timestamp: number
  end_timestamp: number
  dimension?: 'model' | 'user' | 'channel'
  tz_offset_minutes?: number
}) {
  const res = await api.get<{ success: boolean; data: { groups: SeriesGroup[] } }>(
    '/api/data/series',
    { params }
  )
  return res.data
}

// Get the caller's own daily usage series.
export async function getSelfUsageSeries(params: {
  start_timestamp: number
  end_timestamp: number
  tz_offset_minutes?: number
}) {
  const res = await api.get<{ success: boolean; data: SeriesGroup }>(
    '/api/data/series/self',
    { params }
  )
  return res.data
}

// Get every spending user's balance plus a dense daily series. Admin only.
export async function getUserBalanceSeries(params: {
  days?: number
  end_timestamp?: number
  tz_offset_minutes?: number
}) {
  const res = await api.get<{
    success: boolean
    data: { start_date: string; users: UserBalanceItem[] }
  }>('/api/data/user-balances', { params })
  return res.data
}
