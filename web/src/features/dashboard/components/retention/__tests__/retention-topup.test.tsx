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
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { adjustUserQuota } from '@/features/users/api'
import { api } from '@/lib/api'
import { parseQuotaFromDollars } from '@/lib/format'

import { RetentionBoard } from '../retention-board'

vi.mock('@/features/users/api', () => ({
  adjustUserQuota: vi.fn(),
}))

// 14 dense days. carol is nearly out of balance, david has a comfortable buffer,
// so the board must list carol first regardless of who has more money.
const USERS = [
  {
    user_id: 4,
    username: 'carol-lab',
    group: 'default',
    quota: 400_000,
    daily: Array.from({ length: 14 }, (_, index) => (index >= 7 ? 800_000 : 0)),
  },
  {
    user_id: 5,
    username: 'david-ai',
    group: 'vip',
    quota: 10_000_000,
    daily: Array.from({ length: 14 }, () => 1_000_000),
  },
]

let client: QueryClient

function renderBoard() {
  return render(
    <QueryClientProvider client={client}>
      <RetentionBoard />
    </QueryClientProvider>
  )
}

beforeEach(() => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: { start_date: '2026-10-23', users: USERS } },
  } as never)
  vi.mocked(adjustUserQuota).mockResolvedValue({ success: true } as never)
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('RetentionBoard top-up', () => {
  it('lists the closest-to-empty customer first', async () => {
    renderBoard()

    await screen.findByText('carol-lab')
    const rows = screen.getAllByRole('row')
    // rows[0] is the header row.
    expect(rows[1]).toHaveTextContent('carol-lab')
    expect(rows[2]).toHaveTextContent('david-ai')
  })

  it('pre-fills the suggested amount and submits it as quota units', async () => {
    const user = userEvent.setup()
    renderBoard()

    await screen.findByText('carol-lab')
    await user.click(
      screen.getByRole('button', { name: 'Top up carol-lab' })
    )

    // Daily spend is 800,000/day, balance 400,000, target 30 days:
    // 30 * 800,000 - 400,000 = 23,600,000 units, which is $47.20 at the default
    // 500,000 units/USD rate, rounded up to a human-looking $50.
    const amount = screen.getByLabelText('Amount') as HTMLInputElement
    expect(Number(amount.value)).toBe(50)

    await user.click(screen.getByRole('button', { name: 'Confirm' }))

    await waitFor(() => {
      expect(adjustUserQuota).toHaveBeenCalledTimes(1)
    })
    expect(adjustUserQuota).toHaveBeenCalledWith({
      id: 4,
      action: 'add_quota',
      mode: 'add',
      value: parseQuotaFromDollars(50),
    })
  })

  it('sends a hand-typed amount and refreshes the board afterwards', async () => {
    const user = userEvent.setup()
    renderBoard()

    await screen.findByText('carol-lab')
    await user.click(
      screen.getByRole('button', { name: 'Top up carol-lab' })
    )

    const amount = screen.getByLabelText('Amount') as HTMLInputElement
    await user.clear(amount)
    await user.type(amount, '125')
    await user.click(screen.getByRole('button', { name: 'Confirm' }))

    await waitFor(() => {
      expect(adjustUserQuota).toHaveBeenCalledWith({
        id: 4,
        action: 'add_quota',
        mode: 'add',
        value: parseQuotaFromDollars(125),
      })
    })
    // One initial load plus one refetch after the credit landed.
    await waitFor(() => {
      expect(vi.mocked(api.get).mock.calls.length).toBeGreaterThanOrEqual(2)
    })
  })

  it('refuses to submit an empty amount', async () => {
    const user = userEvent.setup()
    renderBoard()

    await screen.findByText('carol-lab')
    await user.click(
      screen.getByRole('button', { name: 'Top up carol-lab' })
    )

    const amount = screen.getByLabelText('Amount') as HTMLInputElement
    await user.clear(amount)

    expect(screen.getByRole('button', { name: 'Confirm' })).toBeDisabled()
    expect(adjustUserQuota).not.toHaveBeenCalled()
  })
})
