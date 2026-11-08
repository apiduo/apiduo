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
along with this program. If not <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'

import { ApiduoTraceLink } from '../trace-link'
import { buildTraceUrl } from '../../lib/trace-link'

// The component fetches its config through the shared api client; mock the
// feature api module so tests never touch the network.
vi.mock('../../api', async (importOriginal) => {
  const mod = await importOriginal<typeof import('../../api')>()
  return {
    ...mod,
    getTraceConfig: vi.fn(),
  }
})

import { getTraceConfig } from '../../api'

const mockConfig = vi.mocked(getTraceConfig)

const VALID_ID = 'd60b7f3c843d6a7b9e3a12bf6a942c4d'
const GOOD_CONFIG = {
  enabled: true,
  host: 'https://langfuse.internal',
  project_id: 'proj-7',
  body_mode: 'headtail' as const,
  sample_rate: 10,
}

function renderLink(traceId?: string, isAdmin = true) {
  return render(<ApiduoTraceLink traceId={traceId} isAdmin={isAdmin} />)
}

afterEach(() => {
  mockConfig.mockReset()
})

describe('buildTraceUrl', () => {
  test('assembles host/project/trace', () => {
    expect(buildTraceUrl(GOOD_CONFIG, VALID_ID)).toBe(
      `https://langfuse.internal/project/proj-7/traces/${VALID_ID}`
    )
  })

  test('trims trailing slashes on the host', () => {
    expect(
      buildTraceUrl({ ...GOOD_CONFIG, host: 'https://h.example//' }, VALID_ID)
    ).toBe(`https://h.example/project/proj-7/traces/${VALID_ID}`)
  })

  test('null when disabled, hostless, or projectless', () => {
    expect(buildTraceUrl({ ...GOOD_CONFIG, enabled: false }, VALID_ID)).toBeNull()
    expect(buildTraceUrl({ ...GOOD_CONFIG, host: '' }, VALID_ID)).toBeNull()
    expect(buildTraceUrl({ ...GOOD_CONFIG, project_id: '' }, VALID_ID)).toBeNull()
  })

  test('null for malformed trace ids — never a dead link', () => {
    expect(buildTraceUrl(GOOD_CONFIG, undefined)).toBeNull()
    expect(buildTraceUrl(GOOD_CONFIG, '')).toBeNull()
    expect(buildTraceUrl(GOOD_CONFIG, 'not-hex!')).toBeNull()
    expect(buildTraceUrl(GOOD_CONFIG, 'short32hex'.repeat(4))).toBeNull()
  })

  test('projectId is URL-encoded into the path', () => {
    expect(
      buildTraceUrl({ ...GOOD_CONFIG, project_id: 'a/b c' }, VALID_ID)
    ).toContain('/project/a%2Fb%20c/traces/')
  })
})

describe('ApiduoTraceLink', () => {
  test('renders the link for an admin with a config', async () => {
    mockConfig.mockResolvedValue(GOOD_CONFIG)
    renderLink(VALID_ID)
    await waitFor(() => {
      expect(screen.getByRole('link')).toBeTruthy()
    })
    const link = screen.getByRole('link') as HTMLAnchorElement
    expect(link.href).toBe(
      `https://langfuse.internal/project/proj-7/traces/${VALID_ID}`
    )
    expect(link.target).toBe('_blank')
    expect(link.rel).toContain('noopener')
    expect(link.rel).toContain('noreferrer')
    expect(link.getAttribute('data-apiduo')).toBe('text')
  })

  test('hidden for non-admins even when the id leaks through', async () => {
    // The backend already strips admin_info for user-scope responses; the
    // isAdmin gate is the second lock.
    mockConfig.mockResolvedValue(GOOD_CONFIG)
    const { container } = renderLink(VALID_ID, false)
    await waitFor(() => expect(mockConfig).not.toHaveBeenCalled())
    expect(container.querySelector('a')).toBeNull()
  })

  test('hidden without a trace id (failure rows, unsampled requests)', async () => {
    mockConfig.mockResolvedValue(GOOD_CONFIG)
    const { container } = renderLink(undefined)
    expect(container.querySelector('a')).toBeNull()
    expect(mockConfig).not.toHaveBeenCalled()
  })

  test('fails closed when the config request errors', async () => {
    mockConfig.mockResolvedValue(null)
    const { container } = renderLink(VALID_ID)
    await waitFor(() => expect(mockConfig).toHaveBeenCalled())
    // Wait past the resolution microtask; still nothing rendered.
    await waitFor(() =>
      expect(container.querySelector('a')).toBeNull()
    )
  })

  test('hidden when tracing is disabled on the server', async () => {
    mockConfig.mockResolvedValue({ ...GOOD_CONFIG, enabled: false })
    const { container } = renderLink(VALID_ID)
    await waitFor(() => expect(mockConfig).toHaveBeenCalled())
    await waitFor(() => expect(container.querySelector('a')).toBeNull())
  })
})
