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
export type BillingProjection = 'admin' | 'customer'

export type BillingTab = 'summary' | 'detail'

export type BillingScope = 'self' | 'all'

// 与后端 `controller/billing_statement.go` 的响应一一对应。
// 客户投影下 quota / group / pricing 字段不存在，不是 0：类型上留成可选，
// 渲染时按 projection 判断，而不是按值是否为 0 猜。
export interface BillingOverview {
  period: string
  status: 'draft' | 'issued' | 'paid' | 'void'
  is_final: boolean
  projection: BillingProjection
  user_id: number
  username: string
  period_start: number
  period_end: number
  issued_at?: number
  due_at?: number
  currency: string
  amount: string
  quota?: number
  requests: number
  errors: number
  refunds: number
  input_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  output_tokens: number
  items: BillingSummaryItem[]
  pricing?: {
    quota_per_unit: number
    settle_rate: number
    currency: string
  }
}

export interface BillingSummaryItem {
  model: string
  group?: string
  requests: number
  errors: number
  refunds: number
  input_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  output_tokens: number
  quota?: number
  amount: string
}

export interface BillingLine {
  request_id: string
  created_at: number
  type: number
  token_name: string
  model: string
  group?: string
  endpoint?: string
  input_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  output_tokens: number
  total_tokens: number
  quota?: number
  amount: string
  task_id?: string
  use_time_ms?: number
}

export interface BillingLinesPage {
  items: BillingLine[]
  next_cursor: string
  is_final: boolean
  projection: BillingProjection
}

export interface BillingExportReply {
  download_url: string
  filename: string
  rows: number
  bytes: number
  sha256: string
  is_draft: boolean
  expires_at: number
}

export interface BillingStatementListItem {
  user_id: number
  username: string
  status: string
  amount: string
  quota: number
  requests: number
  errors: number
  refunds: number
  issued_at: number
  due_at: number
  currency: string
  is_issued: boolean
}

export interface BillingStatementList {
  period: string
  items: BillingStatementListItem[]
  pending: boolean
}

export interface BillingSearch {
  period?: string
  tab?: BillingTab
  scope?: BillingScope
  model?: string
  group?: string
  user_id?: number
  internal?: boolean
}
