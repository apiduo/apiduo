import { describe, expect, it } from 'vitest'

import { planNavAppOpen } from '../open'

const SERVER = 'https://gateway.example.com'
const KEY = 'sk-abc123'

describe('planNavAppOpen', () => {
  it('sends a plain deep link to the client', () => {
    expect(
      planNavAppOpen({
        href: 'cherrystudio://providers/api-keys',
        serverAddress: SERVER,
      })
    ).toEqual({ action: 'client', url: 'cherrystudio://providers/api-keys' })
  })

  it('opens a plain web target in a tab', () => {
    expect(
      planNavAppOpen({
        href: 'https://langfuse.example.com',
        serverAddress: SERVER,
      })
    ).toEqual({ action: 'tab', url: 'https://langfuse.example.com' })
  })

  it('fills a client payload with the viewer key and this address', () => {
    const plan = planNavAppOpen({
      href: 'cherrystudio://providers/api-keys?v=1&data={cherryConfig}',
      apiKey: KEY,
      serverAddress: SERVER,
    })

    if (plan.action !== 'client') throw new Error(`unexpected ${plan.action}`)
    const data = new URL(plan.url).searchParams.get('data') as string
    const payload = JSON.parse(Buffer.from(data, 'base64').toString('utf-8'))
    expect(payload).toEqual({
      id: 'new-api',
      baseUrl: SERVER,
      apiKey: KEY,
    })
  })

  it('reports a missing key instead of producing a blank credential', () => {
    expect(
      planNavAppOpen({
        href: 'https://chat.example.com/?key={key}',
        serverAddress: SERVER,
      })
    ).toEqual({ action: 'missing-key' })
  })

  it('treats {address} as needing no key', () => {
    const plan = planNavAppOpen({
      href: 'https://chat.example.com/?base={address}',
      serverAddress: SERVER,
    })
    expect(plan).toEqual({
      action: 'tab',
      url: 'https://chat.example.com/?base=https%3A%2F%2Fgateway.example.com',
    })
  })

  it('reports an address that stays unusable after substitution', () => {
    // The server rejects this on save; a stored value could still come from an
    // older release or a direct database write.
    expect(
      planNavAppOpen({
        href: 'javascript://example.com/%0Aalert(1)',
        serverAddress: SERVER,
      })
    ).toEqual({ action: 'invalid' })
  })
})
