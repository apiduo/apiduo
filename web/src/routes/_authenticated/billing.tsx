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
import { createFileRoute } from '@tanstack/react-router'
import { z } from 'zod'

import { Billing } from '@/features/billing'
import type { BillingSearch } from '@/features/billing/types'

const billingSearchSchema = z.object({
  period: z.string().optional(),
  tab: z.enum(['summary', 'detail']).optional(),
  scope: z.enum(['self', 'all']).optional(),
  model: z.string().optional(),
  group: z.string().optional(),
  user_id: z.coerce.number().optional(),
  internal: z.boolean().optional(),
})

// 账期与页签都进 URL：刷新、分享、从邮件点回来都落在同一视图。
export const Route = createFileRoute('/_authenticated/billing')({
  component: RouteComponent,
  validateSearch: billingSearchSchema,
})

// undefined 与空串不写进 URL：它们会被读回来，再走一遍「缺失」的默认值分支，
// 徒增一次不必要的渲染，也让分享出去的链接多出一串空参数。
function cleanSearch(input: BillingSearch): BillingSearch {
  const output: BillingSearch = {}
  for (const [key, value] of Object.entries(input)) {
    if (value === undefined || value === '') continue
    output[key as keyof BillingSearch] = value as never
  }
  return output
}

function RouteComponent() {
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <Billing
      search={search}
      onSearchChange={(next) =>
        navigate({
          search: (prev) => cleanSearch({ ...prev, ...next }),
          replace: true,
        })
      }
    />
  )
}
