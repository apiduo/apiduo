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
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
/* APIDUO:test-isolation:BEGIN */
import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterAll, afterEach, beforeAll, expect, test, vi } from 'vitest'
/* APIDUO:test-isolation:END */

import { SearchProvider } from '@/context/search-provider'
import { ROLE } from '@/lib/roles'
import { STATUS_QUERY_KEY } from '@/lib/status-query'
import { useAuthStore } from '@/stores/auth-store'

import { CommandMenu } from '../command-menu'
import { Search } from '../search'

/* APIDUO:test-isolation:BEGIN
 * Navigation data comes from queries. In jsdom they fail with 401, and the
 * global handler then clears the auth store — which silently turns every case
 * into a guest. Stub them so the role under test is the role observed.
 */
vi.mock('@/features/nav-apps/hooks', () => ({
  useNavApps: () => [],
  useNavAppGroupOrder: () => [],
  useNavAppGroupTitle: () => '',
  useNavAppGroups: () => [],
}))
vi.mock('@/features/system-settings/models/pricing-issues', () => ({
  useModelPricingIssueBadge: () => 0,
}))
/* APIDUO:test-isolation:END */

const adminEntries = ['Channels', 'Models', 'Users', 'Redemption Codes']
const superAdminEntries = [
  'System Info',
  'Plugins',
  'Request processing',
  'System Settings',
]

// jsdom lacks the Web Animations API the menu's scroll area waits on.
const getAnimationsDescriptor = Object.getOwnPropertyDescriptor(
  HTMLElement.prototype,
  'getAnimations'
)

beforeAll(() => {
  Object.defineProperty(HTMLElement.prototype, 'getAnimations', {
    configurable: true,
    value: () => [],
  })
})

afterAll(() => {
  if (getAnimationsDescriptor) {
    Object.defineProperty(
      HTMLElement.prototype,
      'getAnimations',
      getAnimationsDescriptor
    )
  } else {
    Reflect.deleteProperty(HTMLElement.prototype, 'getAnimations')
  }
})

afterEach(() => {
  useAuthStore.setState(useAuthStore.getInitialState(), true)
  window.localStorage.clear()
})

async function searchEntriesFor(role: number, sidebarModulesAdmin = '') {
  /* APIDUO:test-isolation:BEGIN only one dialog may exist in the document */
  cleanup()
  document.body.innerHTML = ''
  useAuthStore.setState(useAuthStore.getInitialState(), true)
  useAuthStore.getState().auth.setUser({ id: 1, username: 'alice', role })
  /* APIDUO:test-isolation:END */
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  client.setQueryData(STATUS_QUERY_KEY, {
    SidebarModulesAdmin: sidebarModulesAdmin,
  })
  const router = createRouter({
    routeTree: createRootRoute({
      component: () => (
        <SearchProvider>
          <Search />
          <CommandMenu />
        </SearchProvider>
      ),
    }),
    history: createMemoryHistory({ initialEntries: ['/dashboard/overview'] }),
  })
  await router.load()
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )

  await userEvent.click(await screen.findByRole('button', { name: 'Search' }))
  const options = await screen.findAllByRole('option')
  return options.map((option) => option.textContent)
}

test('regular users only find their own console pages in search', async () => {
  const entries = await searchEntriesFor(ROLE.USER)

  expect(entries).toEqual(
    expect.arrayContaining(['API Keys', 'Usage Logs', 'Wallet', 'Profile'])
  )
  for (const entry of [...adminEntries, ...superAdminEntries]) {
    expect(entries).not.toContain(entry)
  }
})

test('admins find admin pages in search but not super admin pages', async () => {
  const entries = await searchEntriesFor(ROLE.ADMIN)

  expect(entries).toEqual(expect.arrayContaining(adminEntries))
  for (const entry of superAdminEntries) {
    expect(entries).not.toContain(entry)
  }
})

test('super admins find Request processing right after Plugins', async () => {
  const entries = await searchEntriesFor(ROLE.SUPER_ADMIN)

  expect(entries).toEqual(expect.arrayContaining(superAdminEntries))
  expect(entries.indexOf('Request processing')).toBe(
    entries.indexOf('Plugins') + 1
  )
})

test('pages hidden in the sidebar settings stay out of search', async () => {
  const entries = await searchEntriesFor(
    ROLE.USER,
    JSON.stringify({ personal: { enabled: true, topup: false } })
  )

  expect(entries).not.toContain('Wallet')
  expect(entries).toContain('Profile')
})
