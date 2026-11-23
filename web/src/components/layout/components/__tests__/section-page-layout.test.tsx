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
import { describe, expect, it } from 'vitest'

import { SectionPageLayout } from '../section-page-layout'

function renderPage(fixedContent: boolean) {
  return render(
    <SectionPageLayout fixedContent={fixedContent}>
      <SectionPageLayout.Title>Service health</SectionPageLayout.Title>
      <SectionPageLayout.Content>
        <div data-testid='content-body'>stacked cards</div>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}

describe('SectionPageLayout scrolling contract', () => {
  it('scrolls the content area by default', () => {
    // Stacked-card pages (服务健康、增长) are taller than the viewport. With
    // `overflow-hidden` the overflow is not just hidden but unreachable, which
    // reads as "the page is frozen"; the default must stay scrollable.
    renderPage(false)

    const wrapper = screen.getByTestId('content-body').parentElement
    expect(wrapper).not.toBeNull()
    expect(wrapper).toHaveClass('overflow-auto')
    expect(wrapper).not.toHaveClass('overflow-hidden')
  })

  it('leaves scrolling to the page when fixedContent is set', () => {
    renderPage(true)

    const wrapper = screen.getByTestId('content-body').parentElement
    expect(wrapper).toHaveClass('overflow-hidden')
  })
})
