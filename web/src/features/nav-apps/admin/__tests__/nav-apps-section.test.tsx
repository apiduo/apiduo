import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import { createInstance } from 'i18next'
import { I18nextProvider, initReactI18next } from 'react-i18next'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { DEFAULT_NAV_APP_GROUP_ID, orderSidebarGroups } from '../../lib/render'
import type { NavApp, NavAppListResponse } from '../../types'

const { mutateAsync, serverData } = vi.hoisted(() => ({
  mutateAsync: vi.fn(),
  serverData: { value: null as NavAppListResponse | null },
}))

vi.mock('../../hooks', () => ({
  useAllNavApps: () => ({ data: serverData.value, isLoading: false }),
  useSaveNavApps: () => ({ mutateAsync, isPending: false }),
}))

vi.mock('@/features/apiduo-appearance', () => ({ ApiduoGlow: () => null }))
vi.mock('@/features/system-settings/components/settings-section', () => ({
  SettingsSection: (props: { children: unknown }) => props.children,
}))

const i18n = createInstance()
await i18n.use(initReactI18next).init({
  lng: 'en',
  resources: {
    en: {
      translation: {
        'Drag {{group}} to reorder': 'Drag {{group}} to reorder',
        'Save navigation entries': 'Save navigation entries',
        'Default group': 'Default group',
        'Built-in': 'Built-in',
        Apps: 'Apps',
      },
    },
  },
})

const { NavAppsSection } = await import('../nav-apps-section')

function navApp(overrides: Partial<NavApp> = {}): NavApp {
  return {
    slug: 'wb',
    name: 'wb',
    name_i18n: '',
    icon: 'globe',
    href: 'https://example.com',
    display_type: 'external',
    placement: 'side',
    min_role: 1,
    sort: 0,
    enabled: true,
    ...overrides,
  }
}

function fixture(
  overrides: Partial<NavAppListResponse> = {}
): NavAppListResponse {
  return {
    success: true,
    message: '',
    data: [navApp()],
    groups: [],
    group_order: ['chat', 'general', 'personal', 'admin'],
    group_title: 'Workxxx',
    revision: 'r1',
    ...overrides,
  }
}

function renderSection() {
  return render(
    <I18nextProvider i18n={i18n}>
      <NavAppsSection />
    </I18nextProvider>
  )
}

beforeEach(() => {
  mutateAsync.mockReset()
  mutateAsync.mockResolvedValue({ success: true, data: [] })
  serverData.value = fixture()
})

describe('sidebar order editor', () => {
  it('shows the default group as a draggable row, named after its heading', () => {
    renderSection()

    // The group holding unassigned custom entries is a real sidebar heading.
    // Before it joined the list it could never be moved off the bottom.
    expect(screen.getByText('Workxxx')).toBeTruthy()
    expect(
      screen.getByRole('button', { name: 'Drag Workxxx to reorder' })
    ).toBeTruthy()
    // "Default group" also appears as a select option, so scope the assertion
    // to the row itself rather than the whole form.
    const row = screen
      .getByRole('button', { name: 'Drag Workxxx to reorder' })
      .closest('li')
    expect(within(row as HTMLElement).getByText('Default group')).toBeTruthy()
    expect(
      screen.getByRole('button', { name: 'Drag wb to reorder' })
    ).toBeTruthy()
  })

  it('saves a reordered default group and keeps each entry in its group', async () => {
    serverData.value = fixture({
      data: [navApp({ group_slug: 'team' })],
      groups: [{ slug: 'team', name: 'Team', name_i18n: '', sort: 0 }],
    })
    renderSection()

    // Keyboard reordering exercises the same moveGroup path the drag gesture
    // ends in, without depending on pointer events in jsdom.
    for (let step = 0; step < 5; step += 1) {
      fireEvent.keyDown(
        screen.getByRole('button', { name: 'Drag Workxxx to reorder' }),
        { key: 'ArrowUp' }
      )
    }

    fireEvent.click(
      screen.getByRole('button', { name: 'Save navigation entries' })
    )
    await waitFor(() => expect(mutateAsync).toHaveBeenCalledTimes(1))

    const payload = mutateAsync.mock.calls[0][0] as {
      apps: NavApp[]
      group_order: string[]
      group_title: string
    }
    expect(payload.group_order[0]).toBe(DEFAULT_NAV_APP_GROUP_ID)
    expect(payload.group_title).toBe('Workxxx')
    expect(payload.apps[0].group_slug).toBe('team')
  })

  it('does not wrap a group past either end of the list', async () => {
    renderSection()
    const handle = () =>
      screen.getByRole('button', { name: 'Drag Chat to reorder' })
    // "Chat" is first; ArrowUp must be a no-op, not a move to the end.
    for (let step = 0; step < 3; step += 1) {
      fireEvent.keyDown(handle(), { key: 'ArrowUp' })
    }
    fireEvent.click(
      screen.getByRole('button', { name: 'Save navigation entries' })
    )
    await waitFor(() => expect(mutateAsync).toHaveBeenCalledTimes(1))
    const payload = mutateAsync.mock.calls[0][0] as { group_order: string[] }
    expect(payload.group_order[0]).toBe('chat')
    expect(new Set(payload.group_order).size).toBe(payload.group_order.length)
  })
})

describe('orderSidebarGroups', () => {
  it('honours a stored position for the default group', () => {
    const groups = [
      { id: 'chat', title: 'Chat', items: [] },
      { id: DEFAULT_NAV_APP_GROUP_ID, title: 'Workxxx', items: [] },
    ]
    expect(
      orderSidebarGroups(groups, [DEFAULT_NAV_APP_GROUP_ID, 'chat']).map(
        (group) => group.id
      )
    ).toEqual([DEFAULT_NAV_APP_GROUP_ID, 'chat'])
  })
})

describe('fresh installation', () => {
  it('still renders sortable group rows when the stored list is null', () => {
    serverData.value = fixture({
      data: null,
      groups: null,
      group_order: null,
    })
    renderSection()

    // Without entries the server serialises `data` as null. The order editor
    // must still seed its rows, otherwise nothing can be dragged before the
    // first entry has ever been saved.
    expect(
      screen.getByRole('button', { name: 'Drag Chat to reorder' })
    ).toBeTruthy()
    expect(
      screen.getByRole('button', { name: 'Drag Workxxx to reorder' })
    ).toBeTruthy()
  })
})

describe('custom icon URL', () => {
  it('sends the icon URL in the saved payload', async () => {
    serverData.value = fixture({
      data: [navApp({ icon_url: 'https://cdn.example.com/logo.png' })],
    })
    renderSection()
    fireEvent.click(
      screen.getByRole('button', { name: 'Save navigation entries' })
    )
    await waitFor(() => expect(mutateAsync).toHaveBeenCalledTimes(1))
    const payload = mutateAsync.mock.calls[0][0] as { apps: NavApp[] }
    expect(payload.apps[0].icon_url).toBe('https://cdn.example.com/logo.png')
  })

  it('refuses an unsafe icon URL before it reaches the server', async () => {
    renderSection()
    const input = document.querySelector(
      'input[placeholder^="https://cdn.example.com"]'
    ) as HTMLInputElement
    expect(input).toBeTruthy()
    fireEvent.change(input, { target: { value: 'http://127.0.0.1/a.png' } })
    fireEvent.click(
      screen.getByRole('button', { name: 'Save navigation entries' })
    )
    expect(mutateAsync).not.toHaveBeenCalled()
  })
})
