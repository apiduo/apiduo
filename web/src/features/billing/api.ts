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
  BillingExportReply,
  BillingLinesPage,
  BillingOverview,
  BillingStatementList,
} from './types'

// 账单只有四个只读端点。投影由服务端决定，这里唯一能做的是「要不要请求内部视图」，
// 而 internal=1 对非管理员无效 —— 前端参数改变不了看到的列。

type Envelope<T> = { success?: boolean; message?: string; data?: T }

type Query = Record<string, string | number | boolean | undefined>

function buildQuery(input: Query): string {
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(input)) {
    if (value === undefined || value === '') continue
    params.set(key, String(value))
  }
  return params.toString()
}

function unwrap<T>(response: Envelope<T>): T {
  const body = requireServerSuccess(response)
  if (body.data === undefined) {
    throw new Error(body.message ?? 'Request failed')
  }
  return body.data
}

export async function getBillingStatement(input: {
  period: string
  userId?: number
  internal?: boolean
}): Promise<BillingOverview> {
  const query = buildQuery({
    period: input.period,
    user_id: input.userId,
    internal: input.internal ? 1 : undefined,
  })
  const response = await api.get(`/api/billing/statement?${query}`)
  return unwrap<BillingOverview>(response.data)
}

export async function getBillingLines(input: {
  period: string
  userId?: number
  internal?: boolean
  cursor?: string
  pageSize?: number
  model?: string
  group?: string
}): Promise<BillingLinesPage> {
  const query = buildQuery({
    period: input.period,
    user_id: input.userId,
    internal: input.internal ? 1 : undefined,
    cursor: input.cursor,
    page_size: input.pageSize,
    model: input.model,
    group: input.group,
  })
  const response = await api.get(`/api/billing/statement/lines?${query}`)
  return unwrap<BillingLinesPage>(response.data)
}

export async function getBillingExport(input: {
  period: string
  userId?: number
  internal?: boolean
}): Promise<BillingExportReply> {
  const query = buildQuery({
    period: input.period,
    user_id: input.userId,
    internal: input.internal ? 1 : undefined,
  })
  const response = await api.get(`/api/billing/statement/export?${query}`)
  return unwrap<BillingExportReply>(response.data)
}

export async function listBillingStatements(
  period: string
): Promise<BillingStatementList> {
  const response = await api.get(
    `/api/billing/statements?${buildQuery({ period })}`
  )
  return unwrap<BillingStatementList>(response.data)
}
