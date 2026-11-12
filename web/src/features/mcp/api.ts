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
import { requireServerSuccess } from '@/lib/server-error-message'

import type {
  AdoptResult,
  AuditResult,
  ErrorRule,
  ModelProfile,
  Observations,
  Overview,
  ProtocolHint,
} from './types'

type Envelope<T> = { success: boolean; message?: string; data: T }

/**
 * MCP 管理接口的三个约定，写在这里免得每个调用点各猜一次：
 *
 * 1. 业务错误是 HTTP 200 + `success:false`，所以判定必须看 success，
 *    不能只看状态码 —— 用 axios 的 2xx 判断会把「被拒绝」当成「成功了」。
 * 2. 成功时数据在 `data` 里，列表接口是 `data.items`。
 * 3. 只有 /api/mcp/* 需要管理员身份；这些调用统一走会话令牌。
 */
async function unwrap<T>(request: Promise<{ data: Envelope<T> }>): Promise<T> {
  const res = await request
  return requireServerSuccess(res.data).data
}

export const mcpApi = {
  overview: () => unwrap<Overview>(api.get('/api/mcp/overview')),

  profiles: () =>
    unwrap<{ items: ModelProfile[]; total: number }>(api.get('/api/mcp/profiles')),

  // 「站点已启用、但还没声明」的模型 ID。只读、零出站，来自本地 abilities 表。
  // suggestions 是模型 ID → 协议建议；旧版后端没有这个字段，按空对象降级。
  candidates: () =>
    unwrap<{ items: string[]; suggestions?: Record<string, ProtocolHint> }>(
      api.get('/api/mcp/candidates')
    ),

  create: (body: Record<string, unknown>) =>
    unwrap<ModelProfile>(api.post('/api/mcp/profiles', body)),

  update: (id: number, body: Record<string, unknown>) =>
    unwrap<ModelProfile>(api.put(`/api/mcp/profiles/${id}`, body)),

  toggle: (id: number, enabled: boolean) =>
    unwrap<ModelProfile>(api.post(`/api/mcp/profiles/${id}/toggle`, { enabled })),

  remove: (id: number) => unwrap<unknown>(api.delete(`/api/mcp/profiles/${id}`)),

  // model_id 为空时只回全局规则。
  rules: (modelId?: string) =>
    unwrap<{ items: ErrorRule[] }>(
      api.get('/api/mcp/error-rules', {
        params: modelId ? { model_id: modelId } : undefined,
      })
    ),

  saveRule: (id: number, body: Record<string, unknown>) =>
    unwrap<unknown>(api.put(`/api/mcp/error-rules/${id}`, body)),

  observations: (id: number, days = 7) =>
    unwrap<Observations>(
      api.get(`/api/mcp/profiles/${id}/observations`, { params: { days } })
    ),

  audit: (id: number, days = 7) =>
    unwrap<AuditResult>(
      api.get(`/api/mcp/profiles/${id}/audit`, { params: { days } })
    ),

  // 只传 param：max / enum 由服务端按当前样本重算，前端不参与决定写什么。
  adopt: (id: number, param: string, days = 7) =>
    unwrap<AdoptResult>(
      api.post(`/api/mcp/profiles/${id}/adopt`, { param, days })
    ),
}

export const mcpKeys = {
  all: ['mcp'] as const,
  overview: () => ['mcp', 'overview'] as const,
  profiles: () => ['mcp', 'profiles'] as const,
  candidates: () => ['mcp', 'candidates'] as const,
  rules: (modelId?: string) => ['mcp', 'rules', modelId ?? 'global'] as const,
  observations: (id: number) => ['mcp', 'observations', id] as const,
  audit: (id: number) => ['mcp', 'audit', id] as const,
}
