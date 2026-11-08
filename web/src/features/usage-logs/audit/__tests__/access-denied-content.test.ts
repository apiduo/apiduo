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

import { renderAuditContent } from '../../lib/format'

/** 用最简 mustache 替换，模拟 i18next 的插值行为。 */
function fakeT(key: string, opts?: Record<string, unknown>): string {
  return key.replace(/\{\{(\w+)\}\}/g, (_, name: string) =>
    String(opts?.[name] ?? '')
  )
}

describe('access.denied 审计文案', () => {
  it('模板已注册，且把方法/路由插进文案', () => {
    // 没有模板时 renderAuditContent 返回 null，页面会退回显示原始路由，
    // 运营看不出「谁在试哪个接口」——等于没记。
    const out = renderAuditContent(
      {
        op: {
          action: 'access.denied',
          params: {
            method: 'PUT',
            route: '/api/user/:id/discounts',
            role: 1,
            required: 10,
          },
        },
      } as never,
      fakeT
    )

    expect(out).toBe(
      'Denied access to PUT /api/user/:id/discounts (insufficient privileges)'
    )
  })

  it('缺少 method/route 时不会渲染出 undefined', () => {
    const out = renderAuditContent(
      { op: { action: 'access.denied', params: {} } } as never,
      fakeT
    )
    expect(out).not.toBeNull()
    expect(out).not.toContain('undefined')
  })
})
