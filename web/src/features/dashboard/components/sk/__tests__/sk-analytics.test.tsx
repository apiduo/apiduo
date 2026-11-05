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

import { api } from '@/lib/api'

import { SkAnalytics } from '../sk-analytics'
import type { SkQuotaData } from '@/features/dashboard/types'

const ITEMS: SkQuotaData['items'] = [
  {
    token_id: 11,
    token_name: 'primary-prod',
    token_key: 'sk-p**********cdef',
    user_id: 1,
    username: 'alice',
    model_name: 'gpt-4o',
    count: 100,
    quota: 500000,
    token_used: 12000,
    cache_read_tokens: 3000,
    cache_creation_tokens: 500,
  },
  {
    token_id: 22,
    token_name: 'backup-ci',
    token_key: 'sk-b**********dcba',
    user_id: 2,
    username: 'bob',
    model_name: 'gpt-4o-mini',
    count: 30,
    quota: 150000,
    token_used: 5000,
    cache_read_tokens: 0,
    cache_creation_tokens: 0,
  },
  {
    token_id: 0,
    token_name: '',
    token_key: '',
    user_id: 3,
    username: 'carol',
    model_name: 'gpt-4o',
    count: 10,
    quota: 5000,
    token_used: 1000,
    cache_read_tokens: 0,
    cache_creation_tokens: 0,
  },
]

let client: QueryClient
let response: SkQuotaData

function renderSkAnalytics() {
  return render(
    <QueryClientProvider client={client}>
      <SkAnalytics />
    </QueryClientProvider>
  )
}

beforeEach(() => {
  response = {
    items: ITEMS,
    totals: {
      quota: 655000,
      count: 140,
      token_used: 18000,
      cache_read_tokens: 3000,
      cache_creation_tokens: 500,
    },
    truncated: false,
    limit: 5000,
  }
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  vi.spyOn(api, 'get').mockImplementation(async () => ({
    data: { success: true, data: response },
  }))
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('SkAnalytics', () => {
  it('renders the SK, the masked key and an explicit placeholder for tokenless usage', async () => {
    renderSkAnalytics()

    // The same SK also appears in the stack chart, so multiple matches are
    // expected here.
    expect((await screen.findAllByText('primary-prod')).length).toBeGreaterThan(
      0
    )
    expect(screen.getAllByText('sk-p**********cdef').length).toBeGreaterThan(0)
    expect(screen.getAllByText('backup-ci').length).toBeGreaterThan(0)
    // The tokenless bucket has no name, so it falls back to the localized label
    // both in the table row and in the chart.
    expect(screen.getAllByText('No token').length).toBeGreaterThan(0)
    // The raw key never appears.
    expect(screen.queryByText(/sk-primary0123456789/)).toBeNull()
  })

  it('narrows both the chart and the table to the search term', async () => {
    const user = userEvent.setup()
    renderSkAnalytics()

    expect((await screen.findAllByText('primary-prod')).length).toBeGreaterThan(
      0
    )
    await user.type(screen.getByLabelText(/Filter by SK, user or model/), 'bob')

    await waitFor(() => {
      expect(screen.queryByText('primary-prod')).toBeNull()
    })
    expect(screen.getAllByText('backup-ci').length).toBeGreaterThan(0)
  })

  it('warns when the row list is truncated', async () => {
    response = { ...response, truncated: true, limit: 5000 }
    renderSkAnalytics()

    expect(
      await screen.findByText(/Showing the top .* combinations by cost/)
    ).toBeInTheDocument()
  })
})
