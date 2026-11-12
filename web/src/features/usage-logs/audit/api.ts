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
import { t } from 'i18next'

import type { ApiResponse } from '@/features/profile/types'
import { api } from '@/lib/api'
import { createServerError } from '@/lib/server-error-message'

export interface AuditLog {
  event_id: string
  user_id: number
  username: string
  actor_role: number
  created_at: number
  category: string
  action: string
  token_ref: string
  // APIDUO:audit-token-label:BEGIN
  // 后端按指纹还原出来的可读身份。令牌已被删除时两者都为空 ——
  // 那时只剩 token_ref 可显示，这是「钥匙没了但记录还在」的正常形态。
  token_label?: string
  token_agent_key?: string
  // APIDUO:audit-token-label:END
  auth_method?: string
  ip: string
  user_agent: string
  method: string
  route: string
  status: number
  success: boolean
  request_id: string
  content: string
  other: Record<string, unknown> | null
}
export interface AuditFilters {
  p: number
  page_size: number
  start_timestamp?: number
  end_timestamp?: number
  success?: string
  category?: string
  token_ref?: string
  exclude_token_ref?: string
  username?: string
  request_id?: string
}
export async function getAuditLogs(
  scope: 'all' | 'self',
  params: AuditFilters
): Promise<{ items: AuditLog[]; total: number }> {
  const response = await api.get<
    ApiResponse<{ items: AuditLog[]; total: number }>
  >(scope === 'all' ? '/api/audit' : '/api/audit/self', { params })
  if (!response.data.success || !response.data.data) {
    throw createServerError(response.data, t('Failed to load audit records'))
  }
  return response.data.data
}
