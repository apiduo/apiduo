import { fireEvent, render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'

import { navAppIcon } from '../icons'

describe('custom nav app icon', () => {
  it('renders an image that sends no referrer', () => {
    const Icon = navAppIcon('globe', 'https://cdn.example.com/logo.png')
    const { container } = render(<Icon className='size-4' />)
    const image = container.querySelector('img')

    expect(image?.getAttribute('src')).toBe('https://cdn.example.com/logo.png')
    expect(image?.getAttribute('referrerpolicy')).toBe('no-referrer')
    expect(image?.getAttribute('loading')).toBe('lazy')
    expect(container.querySelector('[data-apiduo-custom-icon]')).toBeTruthy()
  })

  it('keeps component identity stable so re-renders do not restart the load', () => {
    expect(navAppIcon('boxes')).toBe(navAppIcon('boxes'))
    expect(navAppIcon('x', 'https://a.example.com/a.png')).toBe(
      navAppIcon('x', 'https://a.example.com/a.png')
    )
  })

  it('falls back to a preset globe when the image fails', () => {
    const Icon = navAppIcon('globe', 'https://cdn.example.com/broken.png')
    const { container } = render(<Icon />)
    const image = container.querySelector('img')
    expect(image).toBeTruthy()

    fireEvent.error(image as HTMLImageElement)
    expect(container.querySelector('img')).toBeNull()
    expect(container.querySelector('svg')).toBeTruthy()
  })

  it('falls back to the selected preset when the image fails', () => {
    const Icon = navAppIcon('github', 'https://cdn.example.com/broken.png')
    const { container } = render(<Icon />)
    fireEvent.error(container.querySelector('img') as HTMLImageElement)
    const fallbackSvg = container.querySelector('svg')

    const Preset = navAppIcon('github')
    const { container: presetContainer } = render(<Preset className='size-4' />)
    expect(fallbackSvg?.outerHTML).toBe(
      presetContainer.querySelector('svg')?.outerHTML
    )
  })

  it('uses the preset when the URL is blank', () => {
    const Icon = navAppIcon('boxes', '   ')
    const { container } = render(<Icon />)
    expect(container.querySelector('[data-apiduo-custom-icon]')).toBeNull()
    expect(container.querySelector('svg')).toBeTruthy()
  })
})
