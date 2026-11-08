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

import type { DiscountRule, UserDiscountsPayload } from './types'

type ApiResponse<T> = {
  success: boolean
  message?: string
  data?: T
}

/**
 * 读取某个用户的折扣规则。
 *
 * 响应里带 usable_groups（按该用户算），前端据此标出「用不到的组」——
 * 这类配置不会报错，只是永远不命中，必须在界面上显性提示。
 */
export async function getUserDiscounts(
  userId: number
): Promise<ApiResponse<UserDiscountsPayload>> {
  const res = await api.get(`/api/user/${userId}/discounts`)
  return res.data
}

/** 覆盖式写入某个用户的折扣规则，空数组表示清除。 */
export async function updateUserDiscounts(
  userId: number,
  rules: DiscountRule[]
): Promise<ApiResponse<DiscountRule[]>> {
  const res = await api.put(`/api/user/${userId}/discounts`, { rules })
  return res.data
}

export { requireServerSuccess }
