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
import { z } from 'zod'

// URL 里的布尔值不一定是 JSON 形态：手写或外部分享的链接常见 `internal=1`。
// z.boolean() 会让这种链接整页崩掉（路由校验抛错 → 错误边界），
// 所以先把常见的字符串写法折叠成真正的布尔，再交给 z.boolean()。
export const urlBoolean = z.preprocess((value) => {
  if (value === '1' || value === 'true' || value === 1) return true
  if (value === '0' || value === 'false' || value === 0) return false
  return value
}, z.boolean().optional())

export const billingSearchSchema = z.object({
  period: z.string().optional(),
  tab: z.enum(['summary', 'detail']).optional(),
  scope: z.enum(['self', 'all']).optional(),
  model: z.string().optional(),
  group: z.string().optional(),
  user_id: z.coerce.number().optional(),
  internal: urlBoolean,
})
