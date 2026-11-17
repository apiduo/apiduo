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
import type { TimeGranularity } from '@/lib/time'

// ============================================================================
// Quota & Usage Data Types
// ============================================================================

export interface QuotaDataItem {
  id?: number
  user_id?: number
  username?: string
  model_name?: string
  created_at: number
  cache_read_tokens?: number
  cache_creation_tokens?: number
  token_used?: number
  count?: number
  quota?: number
}

export interface FlowQuotaDataItem {
  user_id?: number
  username?: string
  node_name?: string
  use_group?: string
  token_id?: number
  token_name?: string
  channel_id?: number
  channel_name?: string
  model_name?: string
  token_used?: number
  count?: number
  quota?: number
}

export type FlowMetric = 'quota' | 'tokens' | 'requests'

export type FlowOverflowMode = 'aggregate' | 'hide'

export type FlowRole = 'user' | 'admin' | 'root'

export type FlowNodeKind =
  | 'user'
  | 'node'
  | 'token'
  | 'group'
  | 'model'
  | 'channel'

export interface FlowNodeFilter {
  kind: FlowNodeKind
  id: string
}

export interface FlowLinkSelection {
  source: string
  target: string
}

export interface FlowBuildOptions {
  role?: FlowRole
  selectedUsers?: string[]
  selectedNodes?: FlowNodeFilter[]
  activeNode?: FlowNodeFilter
  activeLink?: FlowLinkSelection
  colorPalette?: readonly string[]
  visibleStages?: FlowNodeKind[]
  topNodeLimit?: number
  overflowMode?: FlowOverflowMode
  // When true, sensitive node labels (users, tokens, nodes, groups, channels)
  // are partially masked in the rendered graph while keeping node identity so
  // the Sankey shape stays intact.
  maskSensitive?: boolean
  // Resolves the label for a token whose record no longer exists (deleted).
  // Lets the caller inject a localized string such as "Deleted (123)".
  deletedTokenLabel?: (tokenId: number) => string
  otherNodeLabel?: (kind: FlowNodeKind) => string
}

export interface DashboardFlowNode {
  id: string
  label: string
  kind: FlowNodeKind
  value: number
  requests: number
  quota: number
  tokens: number
  color: string
  colorKey: string
  highlighted?: boolean
  dimmed?: boolean
}

export interface DashboardFlowLink {
  source: string
  target: string
  value: number
  requests: number
  quota: number
  tokens: number
  sourceLabel: string
  targetLabel: string
  color: string
  linkColor: string
  linkAlpha: number
  hoverColor: string
  colorKey: string
  share: number
  highlighted?: boolean
  dimmed?: boolean
}

export interface DashboardFlowGraph {
  nodes: DashboardFlowNode[]
  links: DashboardFlowLink[]
}

export interface FlowUserFilterOption {
  value: string
  label: string
  valueLabel: string
  valueRaw: number
  color: string
}

export interface FlowNodeFilterOption {
  kind: FlowNodeKind
  value: string
  label: string
  valueLabel: string
  valueRaw: number
  color: string
}

export interface FlowFilterOptions {
  users: FlowUserFilterOption[]
  nodes: FlowNodeFilterOption[]
}

export interface FlowSummary {
  quota: number
  tokens: number
  requests: number
}

export interface ProcessedFlowData {
  summary: FlowSummary
  flow: DashboardFlowGraph
  filterOptions: FlowFilterOptions
}

// ============================================================================
// Uptime Monitoring Types
// ============================================================================

export interface UptimeMonitor {
  name: string
  uptime: number
  status: number
  group?: string
}

export interface UptimeGroupResult {
  categoryName: string
  monitors: UptimeMonitor[]
}

// ============================================================================
// Dashboard Filter Types
// ============================================================================

export interface DashboardFilters {
  start_timestamp?: Date
  end_timestamp?: Date
  time_granularity?: TimeGranularity
  username?: string
}

export type ConsumptionDistributionChartType = 'bar' | 'area'

export type ModelAnalyticsChartTab = 'trend' | 'proportion' | 'top'

export interface DashboardChartPreferences {
  consumptionDistributionChart: ConsumptionDistributionChartType
  modelAnalyticsChart: ModelAnalyticsChartTab
  defaultTimeRangeDays: number
  defaultTimeGranularity: TimeGranularity
}

// User analytics selections are held by the dashboard parent so they survive
// switching between dashboard sub-sections, matching the model/flow filters.
export interface UserChartsFilters {
  timeGranularity: TimeGranularity
  selectedRange: number
  topUserLimit: number
}

// ============================================================================
// API Info Types
// ============================================================================

export interface ApiInfoItem {
  url: string
  route: string
  description: string
  color: string
}

export interface PingStatus {
  latency: number | null
  testing: boolean
  error: boolean
}

export type PingStatusMap = Record<string, PingStatus>

// ============================================================================
// Chart Types
// ============================================================================

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type VChartSpec = Record<string, any>

export interface ProcessedChartData {
  spec_pie: VChartSpec
  spec_line: VChartSpec
  spec_area: VChartSpec
  spec_model_line: VChartSpec
  spec_rank_bar: VChartSpec
  totalQuotaDisplay: string
  totalCountDisplay: string
}

export interface ProcessedUserChartData {
  spec_user_rank: VChartSpec
  spec_user_trend: VChartSpec
}

// ============================================================================
// Announcement Types
// ============================================================================

/* APIDUO:announcement:BEGIN */
/**
 * 发布公告时间线上的一个阶段。
 *
 * `at` 用 RFC3339，与 `publishDate` 同一格式；后端 `validateAnnouncementPhases`
 * 按这个格式校验。前端只显示到分钟，完整时刻保留在数据里。
 *
 * 阶段不需要单独存「已完成 / 进行中」，这两态由 `at` 与当前时间比较得出 ——
 * 存状态就要有人定时改它，定时任务一停状态就错。
 */
export interface AnnouncementPhase {
  at: string
  title: string
  desc?: string
}

export interface AnnouncementItem {
  id?: number
  title?: string
  /** 缺省视为公告。changelog 只出现在顶部功能更新横幅，不进公告面板。 */
  category?: 'announcement' | 'changelog'
  /** 仅 changelog 使用：变更类型（新增/优化/修复）。 */
  kind?: 'feature' | 'improvement' | 'fix'
  content: string
  publishDate?: string
  /** 失效时间。到点后后端不再下发，前端只做展示，不需要自己过滤。 */
  expiresAt?: string
  type?: 'default' | 'ongoing' | 'success' | 'warning' | 'error'
  extra?: string
  /** 可选时间线。存量公告没有这个字段，组件遇到就不渲染。 */
  phases?: AnnouncementPhase[]
}

// ============================================================================
// FAQ Types
// ============================================================================

export interface FAQItem {
  id?: number
  question: string
  answer: string
}

// ============================================================================
// SK Analytics Types
// ============================================================================

export interface SkQuotaDataItem {
  token_id: number
  token_name: string
  token_key: string
  user_id: number
  username: string
  model_name: string
  count: number
  quota: number
  token_used: number
  cache_read_tokens: number
  cache_creation_tokens: number
}

export interface SkQuotaTotals {
  quota: number
  count: number
  token_used: number
  cache_read_tokens: number
  cache_creation_tokens: number
}

export interface SkQuotaData {
  items: SkQuotaDataItem[]
  totals: SkQuotaTotals
  truncated: boolean
  limit: number
}

// ============================================================================
// Usage Series / Forecast Types
// ============================================================================

export interface SeriesPoint {
  date: string
  quota: number
  count: number
  token_used: number
}

export interface SeriesGroup {
  key: string
  label: string
  points: SeriesPoint[]
}

// One spending user with their current balance and a dense daily series.
export interface UserBalanceItem {
  user_id: number
  username: string
  group: string
  quota: number
  daily: number[]
}
/* APIDUO:announcement:END */
