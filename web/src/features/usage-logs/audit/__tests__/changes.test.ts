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
import type { TFunction } from 'i18next'
import { describe, expect, it } from 'vitest'

import type { AuditLog } from '../api'
import { buildAuditChanges, readAuditConflict } from '../lib/audit-changes'

// 只回显 key 就够了：这里断言的是「选对了哪个 key」，不是译文。
const t = ((key: string) => key) as unknown as TFunction

function entryWith(params: Record<string, unknown>): AuditLog {
  return {
    event_id: 'e',
    user_id: 1,
    username: 'admin',
    actor_role: 1,
    created_at: 0,
    category: 'operation',
    action: 'mcp.profile_update',
    token_ref: '',
    ip: '',
    user_agent: '',
    method: 'PUT',
    route: '',
    status: 200,
    success: true,
    request_id: 'r',
    content: '',
    other: { op: { action: 'mcp.profile_update', params } },
  }
}

describe('buildAuditChanges', () => {
  it('把 {before, after} 摊成一行一个字段', () => {
    const changes = buildAuditChanges(
      entryWith({
        changed: {
          display_name: { before: 'old', after: 'new' },
          async: { before: true, after: false },
        },
      }),
      t
    )
    expect(changes).toHaveLength(2)
    expect(changes[0]).toMatchObject({
      key: 'display_name',
      // mcp 字段走 audit-changes 里的本地标签表，不再原样返回键名。
      label: 'Display name',
      before: { kind: 'string', text: 'old' },
      after: { kind: 'string', text: 'new' },
    })
    expect(changes[1]).toMatchObject({
      key: 'async',
      before: { kind: 'boolean', text: 'Yes' },
      after: { kind: 'boolean', text: 'No' },
    })
  })

  it('三态字段区分「未声明」和「明确不支持」', () => {
    const [change] = buildAuditChanges(
      entryWith({ changed: { max_ref_images: { before: null, after: 0 } } }),
      t
    )
    expect(change.before).toEqual({ kind: 'null', text: 'Not declared' })
    expect(change.after).toMatchObject({
      kind: 'number',
      text: '0',
      note: 'Explicitly unsupported',
    })
  })

  it('普通数字不带三态注解', () => {
    const [change] = buildAuditChanges(
      entryWith({ changed: { max_prompt_chars: { before: 100, after: 0 } } }),
      t
    )
    expect(change.after.note).toBe('Explicitly unsupported')

    const [plain] = buildAuditChanges(
      entryWith({ changed: { quota: { before: 100, after: 0 } } }),
      t
    )
    expect(plain.after.note).toBeUndefined()
  })

  it('结构对不上就返回空，不渲染半张表', () => {
    expect(buildAuditChanges(entryWith({}), t)).toEqual([])
    expect(buildAuditChanges(entryWith({ changed: [] }), t)).toEqual([])
    expect(buildAuditChanges(entryWith({ changed: 'x' }), t)).toEqual([])
    expect(
      buildAuditChanges(entryWith({ changed: { a: 'not-a-pair' } }), t)
    ).toEqual([])
  })

  it('没有 other 也不炸', () => {
    expect(buildAuditChanges({ ...entryWith({}), other: null }, t)).toEqual([])
  })

  it('嵌套对象按 JSON 呈现', () => {
    const [change] = buildAuditChanges(
      entryWith({ changed: { parameters: { before: null, after: { size: '1K' } } } }),
      t
    )
    expect(change.after.kind).toBe('json')
    expect(change.after.text).toBe('{"size":"1K"}')
  })
})

describe('readAuditConflict', () => {
  it('从 params 里读出两个版本号', () => {
    const conflict = readAuditConflict({
      ...entryWith({ model_id: 'm', expected_version: 9, current_version: 12 }),
      success: false,
    })
    expect(conflict).toEqual({ expected: 9, current: 12 })
  })

  it('调用方没带版本号时 expected 为空，不编造', () => {
    const conflict = readAuditConflict({
      ...entryWith({ model_id: 'm', current_version: 12 }),
      success: false,
    })
    expect(conflict).toEqual({ expected: undefined, current: 12 })
  })

  it('成功的事件即使有版本号也不算冲突', () => {
    expect(
      readAuditConflict(entryWith({ current_version: 12 }))
    ).toBeNull()
  })

  it('没有 current_version 的失败（参数校验）不算冲突', () => {
    expect(
      readAuditConflict({ ...entryWith({ name: 'x' }), success: false })
    ).toBeNull()
  })
})
