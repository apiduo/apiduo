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
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { RetentionBoard } from '../retention-board'

// The board used to mount one <tr> per spending customer. With 45 customers the
// whole list must still be reachable, but only one page may be in the DOM — that
// is what keeps a real instance (thousands of customers) from freezing.
const USERS = Array.from({ length: 45 }, (_, index) => ({
  user_id: index + 1,
  username: `customer-${String(index + 1).padStart(2, '0')}`,
  group: 'default',
  quota: 1_000_000 * (index + 1),
  daily: Array.from({ length: 14 }, () => 100_000),
}))

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
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('RetentionBoard pagination', () => {
  it('renders one page of rows and still reports the full customer count', async () => {
    renderBoard()

    await screen.findByText('customer-01')
    // header + 20 rows; the other 25 customers stay out of the DOM.
    expect(screen.getAllByRole('row')).toHaveLength(21)
    expect(screen.getByText('Page 1 of 3')).toBeInTheDocument()
    expect(screen.queryByText('customer-21')).not.toBeInTheDocument()
  })

  it('walks to the next page and shows the tail of the list', async () => {
    const user = userEvent.setup()
    renderBoard()

    await screen.findByText('customer-01')
    await user.click(screen.getByLabelText('Go to next page'))

    await screen.findByText('customer-21')
    expect(screen.getByText('Page 2 of 3')).toBeInTheDocument()
    expect(screen.queryByText('customer-01')).not.toBeInTheDocument()

    await user.click(screen.getByLabelText('Go to next page'))
    await screen.findByText('customer-41')
    expect(screen.getAllByRole('row')).toHaveLength(6)
    expect(screen.getByLabelText('Go to next page')).toBeDisabled()
  })

  it('resets to the first page when the page size grows', async () => {
    const user = userEvent.setup()
    renderBoard()

    await screen.findByText('customer-01')
    await user.click(screen.getByLabelText('Go to next page'))
    await screen.findByText('customer-21')

    await user.selectOptions(screen.getByLabelText('Rows per page'), '50')
    await waitFor(() => {
      expect(screen.getByText('Page 1 of 1')).toBeInTheDocument()
    })
    expect(screen.getByText('customer-45')).toBeInTheDocument()
  })
})
