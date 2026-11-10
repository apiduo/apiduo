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
import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'

import type { MediaItem } from '../../lib/payload-media'
import { MediaStrip } from '../apiduo-media-strip'

const PNG = `data:image/png;base64,${'A'.repeat(64)}`

const ITEMS: MediaItem[] = [
  { key: 'm1', src: PNG, label: 'PNG · 144 B', remote: false },
  { key: 'm2', src: 'https://cdn.test/a.png', label: 'URL', remote: true },
]

describe('MediaStrip', () => {
  it('draws one thumbnail per image and keeps the blob out of the layout', () => {
    const { container } = render(<MediaStrip items={ITEMS} />)
    const images = container.querySelectorAll('img')
    expect(images).toHaveLength(2)
    expect(images[0].getAttribute('src')).toBe(PNG)
    // Lazy so a batch of twelve does not decode all at once, and no referer so
    // an upstream CDN that checks one still serves the thumbnail.
    expect(images[0].getAttribute('loading')).toBe('lazy')
    expect(images[0].getAttribute('referrerpolicy')).toBe('no-referrer')
  })

  it('opens the zoom overlay and closes it on Escape', () => {
    const { container } = render(<MediaStrip items={ITEMS} />)
    fireEvent.click(container.querySelectorAll('button')[0])

    const dialog = screen.getByRole('dialog')
    expect(dialog.querySelector('img')?.getAttribute('src')).toBe(PNG)

    fireEvent.keyDown(window, { key: 'Escape' })
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('swaps in a placeholder when a thumbnail cannot load', () => {
    const { container } = render(<MediaStrip items={ITEMS} />)
    // A remote URL can 403 long after it was written; one broken image must not
    // leave a blank square where the operator expects evidence.
    fireEvent.error(container.querySelectorAll('img')[1])
    expect(container.querySelectorAll('img')).toHaveLength(1)
  })
})
