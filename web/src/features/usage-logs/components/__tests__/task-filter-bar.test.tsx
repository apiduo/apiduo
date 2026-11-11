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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { getCoreRowModel, useReactTable } from '@tanstack/react-table'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18next from 'i18next'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import zh from '@/i18n/locales/zh.json'
import { useAuthStore } from '@/stores/auth-store'

import { TaskLogsFilterBar } from '../task-logs-filter-bar'
import { UsageLogsProvider } from '../usage-logs-provider'

const captureDescriptor = Object.getOwnPropertyDescriptor(
  HTMLElement.prototype,
  'setPointerCapture'
)

beforeEach(async () => {
  Object.defineProperty(HTMLElement.prototype, 'setPointerCapture', {
    configurable: true,
    value: vi.fn(),
  })
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
  // 全局 setup 只装了空 en，这里补中文包再切过去：断言用中文文案，
  // 顺带验证 key 在 zh 里真的存在（缺 key 会退回英文，测试立刻红）。
  i18next.addResourceBundle('zh', 'translation', zh.translation, true, true)
  await i18next.changeLanguage('zh')
  useAuthStore.getState().auth.setUser({ id: 1, username: 'admin', role: 10 })
})

afterEach(() => {
  if (captureDescriptor) {
    Object.defineProperty(
      HTMLElement.prototype,
      'setPointerCapture',
      captureDescriptor
    )
  } else Reflect.deleteProperty(HTMLElement.prototype, 'setPointerCapture')
  useAuthStore.getState().auth.setUser(null)
})

type Category = 'task' | 'drawing'

function renderFilterBar(options: { category?: Category; search?: string }) {
  const category = options.category ?? 'task'
  function Fixture() {
    const table = useReactTable({
      data: [],
      columns: [],
      getCoreRowModel: getCoreRowModel(),
    })
    return (
      <UsageLogsProvider>
        <TaskLogsFilterBar table={table} logCategory={category} />
      </UsageLogsProvider>
    )
  }

  const root = createRootRoute()
  const auth = createRoute({ getParentRoute: () => root, id: '_authenticated' })
  const logs = createRoute({
    getParentRoute: () => auth,
    path: '/usage-logs/$section',
    component: Fixture,
    validateSearch: (search: Record<string, unknown>) => search,
  })
  const router = createRouter({
    routeTree: root.addChildren([auth.addChildren([logs])]),
    history: createMemoryHistory({
      initialEntries: [`/usage-logs/${category}${options.search ?? ''}`],
    }),
  })

  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  return router
}

async function typeInto(label: string, value: string) {
  const input = await screen.findByLabelText(label)
  fireEvent.change(input, { target: { value } })
  return input
}

it('submits the model and status filters through the URL', async () => {
  const router = renderFilterBar({})

  await typeInto('模型名称', 'hailuo')
  fireEvent.click(await screen.findByRole('button', { name: '搜索' }))

  await waitFor(() => {
    expect(router.state.location.search).toMatchObject({ model: 'hailuo' })
  })
})

it('restores the model filter and the queued status group from the URL', async () => {
  renderFilterBar({
    search: `?model=hailuo&status=${encodeURIComponent('SUBMITTED,QUEUED,NOT_START')}`,
  })

  expect(await screen.findByLabelText('模型名称')).toHaveProperty(
    'value',
    'hailuo'
  )
  // 一个选项背后是三个库内状态，回显必须认这个逗号串，否则筛选看起来没生效。
  expect(await screen.findByRole('combobox', { name: '状态' })).toHaveTextContent(
    '排队中'
  )
})

it('sends one comma separated value for the queued group', async () => {
  const router = renderFilterBar({})
  // Base UI 的下拉靠 pointer 事件驱动，fireEvent.click 不会改变选中值。
  const user = userEvent.setup()
  await user.click(await screen.findByRole('combobox', { name: '状态' }))
  await user.click(await screen.findByRole('option', { name: '排队中' }))
  await user.click(await screen.findByRole('button', { name: '搜索' }))

  await waitFor(() => {
    expect(router.state.location.search).toMatchObject({
      status: 'SUBMITTED,QUEUED,NOT_START',
    })
  })
})

it('clears model and status when resetting', async () => {
  const router = renderFilterBar({
    search: '?model=hailuo&status=FAILURE',
  })
  fireEvent.click(await screen.findByRole('button', { name: '重置' }))

  await waitFor(() => {
    expect(router.state.location.search).not.toMatchObject({ model: 'hailuo' })
  })
  expect(await screen.findByRole('combobox', { name: '状态' })).toHaveTextContent(
    '所有状态'
  )
})

it('does not offer task-only dimensions for drawing logs', async () => {
  renderFilterBar({ category: 'drawing' })

  await screen.findByRole('textbox', { name: '任务 ID' })
  expect(screen.queryByLabelText('模型名称')).toBeNull()
  expect(screen.queryByRole('combobox', { name: '状态' })).toBeNull()
})
