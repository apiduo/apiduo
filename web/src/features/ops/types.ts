export interface OpsSeriesPoint {
  ts: number
  requests: number
  failures: number
  fail_rate: number
  p50_ms: number
  p95_ms: number
}

export interface OpsSeriesResponse {
  from: number
  to: number
  bucket: number
  points: OpsSeriesPoint[]
  note: string
}

export interface OpsBreakdownRow {
  key: string
  requests: number
  failures: number
  fail_rate: number
  p50_ms: number
  p95_ms: number
}

export interface OpsBreakdownResponse {
  dim: string
  rows: OpsBreakdownRow[]
}

export interface OpsChangeEvent {
  id: number
  kind: string
  title: string
  detail: string
  occurred_at: number
  actor: string
}

export interface OpsChangesResponse {
  events: OpsChangeEvent[]
}

export interface OpsIncident {
  id: number
  title: string
  severity: number
  status: 'open' | 'claimed' | 'resolved' | 'ignored'
  owner_id: number
  owner_name: string
  detail: string
  resolution: string
  started_at: number
  claimed_at: number
  closed_at: number
}

export interface OpsIncidentsResponse {
  items: OpsIncident[]
}

export interface OpsErrorCluster {
  fingerprint: string
  sample: string
  requests: number
  users: number
  channels: number
  first_seen: number
  last_seen: number
}

export interface OpsClustersResponse {
  clusters: OpsErrorCluster[]
}

export interface OpsRateLimitSnapshot {
  channel_id: number
  limit: number
  remaining: number
  reset: string
  source: string
  updated_at: number
}

export interface OpsChannelMargin {
  channel_id: number
  revenue_usd: number
  cost_usd: number
  margin_usd: number
  margin_pct: number
  requests: number
  priced: boolean
}

export interface OpsShift {
  id: number
  owner_id: number
  owner_name: string
  started_at: number
  ended_at: number
  handover_note: string
}

export interface OpsShiftResponse {
  current: OpsShift | null
  shifts: OpsShift[]
}
