import { render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'

import { AdminOnlyBadge } from '../admin-only-badge'

describe('AdminOnlyBadge', () => {
  test('renders the translated label when the section is admin-only', () => {
    render(<AdminOnlyBadge visible />)
    // The test i18n instance ships no resources, so a missing key comes back
    // verbatim. Asserting the key keeps this test independent of the locale
    // files while still proving the label goes through `t()`.
    expect(screen.getByText('Admin Only')).toBeInTheDocument()
  })

  test('carries the mark attribute the stylesheet keys on', () => {
    const { container } = render(<AdminOnlyBadge visible />)
    expect(container.querySelector('[data-apiduo="admin-badge"]')).not.toBeNull()
  })

  test('renders nothing for a section every user can open', () => {
    const { container } = render(<AdminOnlyBadge visible={false} />)
    expect(container).toBeEmptyDOMElement()
  })

  test('renders nothing when the flag is omitted', () => {
    const { container } = render(<AdminOnlyBadge />)
    expect(container).toBeEmptyDOMElement()
  })
})
