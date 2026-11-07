import { render } from '@testing-library/react'
import { describe, expect, test } from 'vitest'

import { MultiKeyModeHint } from '../multi-key-mode-hint'

/**
 * The sticky strategy is the only apiduo-owned option in this selector, so it
 * is the only one that may carry the origin mark. An upstream strategy picking
 * up `data-apiduo` would claim apiduo wrote it — a false credit is worse than a
 * missing one because it survives review.
 */
describe('MultiKeyModeHint', () => {
  test('marks the sticky strategy as an apiduo surface', () => {
    const { container } = render(<MultiKeyModeHint mode='sticky' />)
    expect(container.querySelector('[data-apiduo="text"]')).not.toBeNull()
    expect(container.textContent).toContain('One token sticks to one key')
  })

  test('leaves upstream strategies unmarked', () => {
    for (const mode of ['random', 'polling', undefined]) {
      const { container } = render(<MultiKeyModeHint mode={mode} />)
      expect(container.querySelector('[data-apiduo]')).toBeNull()
    }
  })

  test('keeps the polling warning', () => {
    const { container } = render(<MultiKeyModeHint mode='polling' />)
    expect(container.textContent).toContain('Polling mode requires Redis')
    expect(container.querySelector('.text-warning')).not.toBeNull()
  })
})
