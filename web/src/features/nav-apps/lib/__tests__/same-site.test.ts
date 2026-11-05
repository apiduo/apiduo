import { afterEach, describe, expect, it, vi } from 'vitest'

import { isSameSite } from '../render'

/**
 * `isSameSite` decides whether the browser treats an embedded target's cookies
 * as first-party, which is what the console shows as a warning about logins
 * dropping inside a frame. A false positive silently hides that warning, so the
 * multi-label public suffix handling is pinned here.
 */

function withPageHost(hostname: string) {
  vi.stubGlobal('location', { hostname, origin: `https://${hostname}` })
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('isSameSite', () => {
  it('treats two subdomains of one registrable domain as same-site', () => {
    withPageHost('newapi.example.com')
    expect(isSameSite('https://langfuse.example.com')).toBe(true)
  })

  it('separates different registrable domains', () => {
    withPageHost('newapi.example.com')
    expect(isSameSite('https://langfuse.other.com')).toBe(false)
  })

  it('separates two different registrable domains under a multi-label suffix', () => {
    // Both end in co.uk; a last-two-labels rule would call these same-site and
    // suppress the warning the operator needs to see.
    withPageHost('console.acme.co.uk')
    expect(isSameSite('https://grafana.other.co.uk')).toBe(false)
    expect(isSameSite('https://grafana.acme.co.uk')).toBe(true)
  })

  it('separates shared-hosting subdomains', () => {
    withPageHost('app.github.io')
    expect(isSameSite('https://other.github.io')).toBe(false)
    expect(isSameSite('https://second.app.github.io')).toBe(true)
  })

  it('treats localhost ports as same-site but 127.0.0.1 as another site', () => {
    withPageHost('localhost')
    expect(isSameSite('http://localhost:3102/')).toBe(true)
    expect(isSameSite('http://127.0.0.1:3102/')).toBe(false)
  })

  it('returns false for an unparseable address', () => {
    withPageHost('newapi.example.com')
    expect(isSameSite('not a url')).toBe(false)
    expect(isSameSite('')).toBe(false)
  })
})
