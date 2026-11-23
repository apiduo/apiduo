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
import { describe, expect, it } from 'vitest'

import { billingSearchSchema } from '../search'

describe('billing search params', () => {
  it('接受手写链接里的 internal=1', () => {
    expect(billingSearchSchema.parse({ internal: '1' }).internal).toBe(true)
    expect(billingSearchSchema.parse({ internal: '0' }).internal).toBe(false)
  })

  it('接受开关写进 URL 的字符串布尔', () => {
    expect(billingSearchSchema.parse({ internal: 'true' }).internal).toBe(true)
    expect(billingSearchSchema.parse({ internal: 'false' }).internal).toBe(
      false
    )
  })

  it('接受 router 已经解析好的布尔', () => {
    expect(billingSearchSchema.parse({ internal: true }).internal).toBe(true)
    expect(billingSearchSchema.parse({}).internal).toBeUndefined()
  })

  it('user_id 仍然按数字解析', () => {
    expect(billingSearchSchema.parse({ user_id: '62' }).user_id).toBe(62)
  })
})
