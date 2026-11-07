import { render } from '@testing-library/react'
import { expect, test } from 'vitest'

import type { ApiduoAppearance } from '../types'
import { ApiduoGlow } from '../components/apiduo-glow'
import { ApiduoAppearanceContext } from '../context'

function renderGlow(appearance: Partial<ApiduoAppearance>) {
  const value: ApiduoAppearance = {
    enabled: true,
    style: 'flow',
    color: '',
    speed_ms: 3200,
    ring_px: 1.5,
    tail_deg: 120,
    glow: true,
    holiday: '',
    holiday_from: '',
    holiday_to: '',
    ...appearance,
  }
  return render(
    <ApiduoAppearanceContext value={value}>
      <div data-ap-surface=''>
        <ApiduoGlow />
      </div>
    </ApiduoAppearanceContext>
  )
}

test('flow renders the border layer and keeps the glow switch on it', () => {
  const { container } = renderGlow({ style: 'flow', glow: true })
  const layer = container.querySelector('.ap-glow')
  expect(layer).not.toBeNull()
  expect(layer?.getAttribute('data-ap-glow')).toBe('on')
  expect(layer?.hasAttribute('aria-hidden')).toBe(true)
})

test('the glow switch is carried on the layer, not in a class name', () => {
  const { container } = renderGlow({ style: 'flow', glow: false })
  expect(container.querySelector('.ap-glow')?.getAttribute('data-ap-glow')).toBe(
    'off'
  )
})

test('rainbow keeps the same mask and swaps the colour stops', () => {
  const { container } = renderGlow({ style: 'rainbow' })
  const layer = container.querySelector('.ap-glow')
  expect(layer).not.toBeNull()
  expect(layer?.classList.contains('ap-glow-rainbow')).toBe(true)
})

test.each([
  ['spotlight', '.ap-spot'],
  ['sheen', '.ap-sheen'],
  ['pulse', '.ap-pulse'],
] as const)('%s renders only its own layer', (style, selector) => {
  const { container } = renderGlow({ style })
  expect(container.querySelector(selector)).not.toBeNull()
  expect(container.querySelector('.ap-glow')).toBeNull()
})

test('a disabled configuration costs no DOM at all', () => {
  const { container } = renderGlow({ enabled: false })
  expect(container.querySelector('.ap-glow')).toBeNull()
  expect(container.querySelector('.ap-spot')).toBeNull()
})

test('style none renders nothing rather than an empty layer', () => {
  const { container } = renderGlow({ style: 'none' })
  expect(container.querySelector('[class^="ap-"]')).toBeNull()
})
