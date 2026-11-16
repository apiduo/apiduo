export type GrowthTier = 'K' | 'A' | 'B' | 'S' | ''

export interface GrowthOverview {
  week_start: number
  weekly_billed_usd: number
  prev_week_billed_usd: number
  week_over_week_pct: number
  week_active_users: number
  month_active_users: number
  registered_users: number
  paying_users: number
  top10_share: number
  tier_counts: Record<string, number>
  tier_quota: Record<string, number>
  data_fresh_at: number
}

export interface GrowthBoardRow {
  user_id: number
  username: string
  display_name: string
  user_group: string
  status: number
  created_at: number
  quota_7d: number
  quota_30d: number
  quota_90d: number
  requests_7d: number
  requests_30d: number
  tokens_30d: number
  quota_prev_7d: number
  trend: string
  failures_7d: number
  balance: number
  topup_total: number
  last_topup_at: number
  first_active_at: number
  last_active_at: number
  tier: string
  manual_tier: string
  manual_reason: string
  owner_id: number
  last_touch_at: number
  computed_at: number
}

export interface GrowthAlertItem {
  user_id: number
  kind: string
  severity: number
  detail: string
  triggered_at: number
  status: string
  username: string
  display_name: string
  quota: number
  tier: string
}

export interface GrowthAlertStats {
  kind: string
  total: number
  resolved: number
}

export interface GrowthTimelineEvent {
  at: number
  kind: string
  title: string
  detail: string
}

export interface GrowthTouch {
  id: number
  user_id: number
  method: string
  contact_role: string
  feedback: string
  conclusion: string
  follow_up_at: number
  operator_id: number
  operator_name: string
  created_at: number
}

export interface GrowthBatch {
  id: number
  batch_no: string
  name: string
  criteria: string
  benefit_type: string
  benefit_amount: number
  target_count: number
  cost: number
  window_days: number
  incremental_quota: number
  roi: number
  status: string
  started_at: number
  settled_at: number
}

export interface GrowthProfile {
  user: {
    id: number
    username: string
    display_name: string
    quota: number
    status: number
    created_at: number
    remark: string
  }
  stat: GrowthBoardRow | null
  alerts: GrowthAlertItem[]
  timeline: GrowthTimelineEvent[]
  touches: GrowthTouch[]
}

export interface GrowthListResponse<T> {
  items: T[]
  total: number
  page: number
  page_size: number
}

export interface GrowthSettings {
  tier_key_percent: number
  tier_active_percent: number
  tier_key_cap: number
  silent_days: number
  balance_warn_days: number
  incident_rate_pct: number
  decline_pct: number
  dormant_days: number
}

export interface GrowthModelStat {
  model_name: string
  quota: number
  usd: number
  users: number
  requests: number
  share_pct: number
}
