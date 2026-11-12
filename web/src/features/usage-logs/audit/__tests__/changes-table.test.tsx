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
import { render, screen } from '@testing-library/react'
import { createInstance } from 'i18next'
import type { TFunction } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { expect, it } from 'vitest'

import zh from '@/i18n/locales/zh.json'

import type { AuditLog } from '../api'
import { AuditChangesTable } from '../components/audit-changes-table'
import { buildAuditChanges } from '../lib/audit-changes'

async function renderChanges(params: Record<string, unknown>) {
  const i18n = createInstance()
  await i18n.init({ lng: 'zh', resources: { zh } })
  const entry: AuditLog = {
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
  const changes = buildAuditChanges(entry, i18n.t.bind(i18n) as TFunction)
  render(
    <I18nextProvider i18n={i18n}>
      <AuditChangesTable changes={changes} />
    </I18nextProvider>
  )
}

// 三态是这一页最容易看错的地方：0 不是「小一点的上限」，是「禁止」。
// 端到端断言它真的渲染成「未声明 / 0 明确不支持」，而不是两个数字。
it('三态字段渲染出「未声明」与「明确不支持」', async () => {
  await renderChanges({ changed: { max_ref_images: { before: null, after: 0 } } })

  expect(screen.getByText('变更')).toBeVisible()
  expect(screen.getByText('max_ref_images')).toBeVisible()
  expect(screen.getByText('未声明')).toBeVisible()
  expect(screen.getByText('0')).toBeVisible()
  expect(screen.getByText('明确不支持')).toBeVisible()
})

// 布尔值要有可读形态，而不是 true/false。
it('布尔值渲染成是/否', async () => {
  await renderChanges({ changed: { async: { before: true, after: false } } })

  expect(screen.getByText('是')).toBeVisible()
  expect(screen.getByText('否')).toBeVisible()
})

// 没有变更就别渲染一张空表 —— 空表比不显示更让人怀疑「是不是加载失败」。
it('没有变更时不渲染表头', async () => {
  await renderChanges({})
  expect(screen.queryByText('变更')).not.toBeInTheDocument()
})

// create 事件只有初始状态：画单列，不摆一个空的「旧值 →」。
it('created 事件渲染成单列初始值', async () => {
  await renderChanges({ after: { display_name: '新模型', kind: 'image' } })

  // 标题「初始值」与表头「初始值」都是同一个中文词，必须用 getAllByText。
  expect(screen.getAllByText('初始值').length).toBeGreaterThanOrEqual(2)
  expect(screen.getByText('显示名称')).toBeVisible()
  expect(screen.getByText('新模型')).toBeVisible()
  expect(screen.queryByText('变更')).not.toBeInTheDocument()
})

// 失败但没有版本号时不该编造「你提交的是第 N 版」。
it('没有变更也没有 after 时不渲染任何表', async () => {
  await renderChanges({ id: 42 })
  expect(screen.queryByText('变更')).not.toBeInTheDocument()
  expect(screen.queryByText('初始值')).not.toBeInTheDocument()
})
