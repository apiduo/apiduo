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
import { useQuery } from '@tanstack/react-query'
import { t } from 'i18next'

import type { ApiResponse } from '@/features/profile/types'
import { api } from '@/lib/api'
import { createServerError } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

/** 审计筛选下拉的一行。ref 就是审计里的 token_ref（SHA-256 指纹）。 */
export interface AuditTokenOption {
  ref: string
  name: string
  agent_key?: string
  user_id: number
  username: string
  expires_at: number
}

// 稳定的空数组：每次渲染新建一个会让筛选栏的 memo 全部失效。
const EMPTY_OPTIONS: AuditTokenOption[] = []

/**
 * 候选令牌。范围由后端按角色收窄（管理员全站、普通用户只看自己），
 * 前端不做二次过滤 —— 拿不到的令牌本来就不该出现在这里。
 *
 * queryKey 不带 scope：范围取决于登录角色而不是当前视图，切 all/self 不必重拉。
 */
export function useAuditTokenOptions(enabled = true): AuditTokenOption[] {
  const userId = useAuthStore((state) => state.auth.user?.id)
  const query = useQuery({
    queryKey: ['audit-token-options', userId],
    queryFn: async () => {
      const response = await api.get<
        ApiResponse<{ items: AuditTokenOption[]; truncated: boolean }>
      >('/api/audit/token-options')
      if (!response.data?.success || !response.data.data) {
        throw createServerError(response.data, t('Failed to load tokens'))
      }
      return response.data.data
    },
    staleTime: 60_000,
    retry: false,
    enabled,
  })
  return query.data?.items ?? EMPTY_OPTIONS
}
