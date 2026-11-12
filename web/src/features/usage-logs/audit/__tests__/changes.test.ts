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
import {
  buildAuditChanges,
  isAuditDiffMap,
  isAuditDiffPair,
  readAuditConflict,
} from '../lib/audit-changes'
import { buildAuditDetails } from '../lib/audit-details'

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

// 线上实测的形状：后端 auditDiff 写 before=null 时这个键被整个省掉，
// 「未声明 → 0」在库里只有 {after: 0}。判定要求双键会让这类 diff
// 既进表、又留在通用字段里，详情页渲染两遍。
describe('缺 before 键的差值对（线上 0.0.100 的真实形状）', () => {
  it('buildAuditChanges 仍按「未声明 → 值」渲染', () => {
    const changes = buildAuditChanges(
      entryWith({ changed: { max_ref_videos: { after: 0 } } }),
      t
    )
    expect(changes).toHaveLength(1)
    expect(changes[0].key).toBe('max_ref_videos')
    // changed 分支里 describeValue 一定返回对象（undefined → kind=null），
    // 只有 created 分支才是字面量 null；两者不要混。
    expect(changes[0].before?.kind).toBe('null')
    expect(changes[0].after.text).toBe('0')
  })

  it('buildAuditDetails 必须把整块 changed 摘掉，不留原始 JSON', () => {
    const detail = buildAuditDetails(
      entryWith({
        model_id: 'flux-3-image',
        changed: { max_ref_videos: { after: 0 } },
      }),
      t
    )
    expect(detail.fields.map((field) => field.label)).toEqual(['model_id'])
    expect(detail.fields.some((field) => field.label === 'State changed')).toBe(
      false
    )
  })

  it('普通对象不会被误判成差值对', () => {
    expect(isAuditDiffPair({ status: 2 })).toBe(false)
    expect(isAuditDiffPair({ after: 0 })).toBe(true)
    expect(isAuditDiffPair({ before: 1, after: 0 })).toBe(true)
    expect(isAuditDiffMap({ changed: false })).toBe(false)
    expect(isAuditDiffMap({})).toBe(false)
  })
})

// 创建事件的 params 里 after 是全量快照，已经画成「初始值」单列表；
// 通用字段若再渲染一棵 after 子树，同一份信息就出现两遍（线上实测）。
describe('after 全量快照', () => {
  it('create 事件把 after 摘掉，只留业务键', () => {
    const detail = buildAuditDetails(
      entryWith({
        model_id: 'e2e-probe',
        after: { kind: 'image', protocol: 'ark', enabled: true },
      }),
      t
    )
    expect(detail.fields.map((field) => field.label)).toEqual(['model_id'])
  })

  it('空串按未声明渲染，而不是空格子', () => {
    const changes = buildAuditChanges(
      entryWith({ after: { display_name: '', summary: '有内容' } }),
      t
    )
    const byKey = Object.fromEntries(changes.map((c) => [c.key, c.after]))
    expect(byKey.display_name.kind).toBe('null')
    expect(byKey.display_name.text).toBe('Not declared')
    expect(byKey.summary.text).toBe('有内容')
  })
})
