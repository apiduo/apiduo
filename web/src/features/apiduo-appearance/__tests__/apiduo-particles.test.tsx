import { render } from '@testing-library/react'
import { beforeEach, expect, test, vi } from 'vitest'

import { ApiduoParticles } from '../components/apiduo-particles'
import { ApiduoHolidayContext } from '../context'

function mockMatchMedia(matches: boolean) {
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    value: vi.fn().mockImplementation((query: string) => ({
      matches,
      media: query,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      addListener: vi.fn(),
      removeListener: vi.fn(),
    })),
  })
}

beforeEach(() => {
  // The component portals into document.body; wiping body.innerHTML here would
  // orphan the portal host and crash React's cleanup with NotFoundError.
  mockMatchMedia(false)
})

test('no holiday mounts nothing at all', () => {
  render(
    <ApiduoHolidayContext value=''>
      <ApiduoParticles />
    </ApiduoHolidayContext>
  )
  expect(document.querySelector('.ap-particles')).toBeNull()
  expect(document.querySelector('.ap-lanterns')).toBeNull()
})

test('christmas mounts the canvas but no lanterns', () => {
  render(
    <ApiduoHolidayContext value='christmas'>
      <ApiduoParticles />
    </ApiduoHolidayContext>
  )
  expect(document.querySelector('.ap-particles')).not.toBeNull()
  expect(document.querySelector('.ap-lanterns')).toBeNull()
})

test('spring festival mounts the canvas and the lanterns', () => {
  render(
    <ApiduoHolidayContext value='spring-festival'>
      <ApiduoParticles />
    </ApiduoHolidayContext>
  )
  expect(document.querySelector('.ap-particles')).not.toBeNull()
  expect(document.querySelectorAll('.ap-lanterns span')).toHaveLength(3)
})

test('reduced-motion wins even over an active holiday', () => {
  mockMatchMedia(true)
  render(
    <ApiduoHolidayContext value='christmas'>
      <ApiduoParticles />
    </ApiduoHolidayContext>
  )
  expect(document.querySelector('.ap-particles')).toBeNull()
})
