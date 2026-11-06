import { afterEach, describe, expect, it, vi } from 'vitest'

import type { NavApp } from '../../types'
import { canEmbed, navAppToSidebarItem, navAppToTopLink } from '../render'

/**
 * `appLink` is what tells the sidebar and the header to skip `target=_blank`.
 * The browser hands a deep link to the operating system, and a tab opened for
 * it would be left behind empty, so the flag must follow the address kind and
 * not the display type.
 */

function navApp(overrides: Partial<NavApp> = {}): NavApp {
  return {
    slug: 'cherry',
    name: 'Cherry Studio',
    name_i18n: '',
    icon: 'boxes',
    href: 'https://example.com',
    display_type: 'external',
    placement: 'side',
    min_role: 1,
    sort: 0,
    enabled: true,
    ...overrides,
  }
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('navAppToSidebarItem', () => {
  it('flags a client deep link without adding a route', () => {
    expect(
      navAppToSidebarItem(
        navApp({ href: 'cherrystudio://providers/api-keys?v=1&data=x' }),
        ['en']
      )
    ).toMatchObject({
      url: 'cherrystudio://providers/api-keys?v=1&data=x',
      external: true,
      appLink: true,
    })
  })

  it('leaves a web target as a plain external link', () => {
    expect(
      navAppToSidebarItem(navApp({ href: 'https://langfuse.example.com' }), ['en'])
    ).toMatchObject({
      url: 'https://langfuse.example.com',
      external: true,
      appLink: false,
    })
  })

  it('routes an embedded entry to the container route', () => {
    expect(
      navAppToSidebarItem(
        navApp({ display_type: 'iframe', slug: 'langfuse' }),
        ['en']
      )
    ).toMatchObject({ url: '/app/langfuse', external: false })
  })
})

describe('navAppToTopLink', () => {
  it('flags a client deep link', () => {
    expect(
      navAppToTopLink(navApp({ href: 'cherrystudio://providers' }), ['en'])
    ).toMatchObject({ href: 'cherrystudio://providers', external: true, appLink: true })
  })

  it('leaves a web target as a plain external link', () => {
    expect(
      navAppToTopLink(navApp({ href: 'https://example.com/x' }), ['en'])
    ).toMatchObject({ external: true, appLink: false })
  })
})

describe('canEmbed', () => {
  it('accepts a web target on another origin', () => {
    vi.stubGlobal('window', { location: { origin: 'https://gateway.example.com' } })
    expect(canEmbed('https://langfuse.example.com')).toBe(true)
  })

  it('rejects a client deep link: there is no document to frame', () => {
    vi.stubGlobal('window', { location: { origin: 'https://gateway.example.com' } })
    expect(canEmbed('cherrystudio://providers/api-keys')).toBe(false)
  })

  it('rejects the console itself', () => {
    vi.stubGlobal('window', { location: { origin: 'https://gateway.example.com' } })
    expect(canEmbed('https://gateway.example.com/app')).toBe(false)
  })
})
