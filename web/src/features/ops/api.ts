import { api } from '@/lib/api'

import type {
  OpsBreakdownResponse,
  OpsIncident,
  OpsChangesResponse,
  OpsClustersResponse,
  OpsIncidentsResponse,
  OpsChannelMargin,
  OpsRateLimitSnapshot,
  OpsShiftResponse,
  OpsSeriesResponse,
} from './types'

interface Envelope<T> {
  success: boolean
  data: T
}

export async function getOpsSeries(params: {
  from: number
  to: number
  bucket: number
}): Promise<OpsSeriesResponse> {
  const res = await api.get<Envelope<OpsSeriesResponse>>('/api/ops/series', {
    params,
  })
  return res.data.data
}

export async function getOpsBreakdown(params: {
  from: number
  to: number
  dim: 'model' | 'channel'
}): Promise<OpsBreakdownResponse> {
  const res = await api.get<Envelope<OpsBreakdownResponse>>(
    '/api/ops/breakdown',
    { params }
  )
  return res.data.data
}

export async function getOpsChanges(params: {
  from: number
  to: number
}): Promise<OpsChangesResponse> {
  const res = await api.get<Envelope<OpsChangesResponse>>('/api/ops/changes', {
    params,
  })
  return res.data.data
}

export async function getOpsIncidents(
  includeClosed: boolean
): Promise<OpsIncidentsResponse> {
  const res = await api.get<Envelope<OpsIncidentsResponse>>(
    '/api/ops/incidents',
    { params: { all: includeClosed ? '1' : '0' } }
  )
  return res.data.data
}

export async function createOpsIncident(body: {
  title: string
  severity?: number
  detail?: string
}): Promise<OpsIncident> {
  const res = await api.post<Envelope<OpsIncident>>('/api/ops/incidents', body)
  return res.data.data
}

export async function claimOpsIncident(id: number): Promise<void> {
  await api.post(`/api/ops/incidents/${id}/claim`)
}

export async function closeOpsIncident(
  id: number,
  status: 'resolved' | 'ignored',
  resolution: string
): Promise<void> {
  await api.post(`/api/ops/incidents/${id}/close`, { status, resolution })
}

export async function getOpsClusters(params: {
  from: number
  to: number
}): Promise<OpsClustersResponse> {
  const res = await api.get<Envelope<OpsClustersResponse>>('/api/ops/clusters', {
    params,
  })
  return res.data.data
}

export async function reassignOpsIncident(id: number): Promise<void> {
  await api.post(`/api/ops/incidents/${id}/assign`)
}

export async function getOpsRateLimits(): Promise<OpsRateLimitSnapshot[]> {
  const res = await api.get<
    Envelope<{ channels: OpsRateLimitSnapshot[] }>
  >('/api/ops/ratelimits')
  return res.data.data.channels
}

export async function getOpsMargins(params: {
  from: number
  to: number
}): Promise<OpsChannelMargin[]> {
  const res = await api.get<
    Envelope<{ channels: OpsChannelMargin[] }>
  >('/api/ops/margins', { params })
  return res.data.data.channels
}

export async function putOpsChannelCost(body: {
  channel_id: number
  input_per_million: number
  output_per_million: number
  note?: string
}): Promise<void> {
  await api.put('/api/ops/costs', body)
}

export async function getOpsShift(): Promise<OpsShiftResponse> {
  const res = await api.get<Envelope<OpsShiftResponse>>('/api/ops/shift')
  return res.data.data
}

export async function startOpsShift(note: string): Promise<void> {
  await api.post('/api/ops/shift', { note })
}
