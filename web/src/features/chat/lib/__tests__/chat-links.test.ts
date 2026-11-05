import { describe, expect, it } from 'vitest'

import { chatLinkRequiresApiKey, resolveChatUrl } from '../chat-links'

const ADDRESS = 'https://api.example.com'
const KEY = 'sk-abc123'

describe('resolveChatUrl', () => {
  it('percent-encodes {address} for plain query values', () => {
    expect(
      resolveChatUrl({
        template: 'https://chat.example.com/?base={address}',
        serverAddress: ADDRESS,
      })
    ).toBe('https://chat.example.com/?base=https%3A%2F%2Fapi.example.com')
  })

  it('keeps {base_url} raw so JSON payloads survive one decode', () => {
    // This is the shape used by clients such as aiaw: the address sits inside a
    // JSON string that is carried as a single query parameter. Percent-encoding
    // it would be decoded once by the query parser and then read literally as
    // `https%3A%2F%2F...` by the app.
    const template =
      'https://aiaw.app/set-provider?provider={"type":"openai","settings":{"apiKey":"{key}","baseURL":"{base_url}","compatibility":"strict"}}'

    const url = resolveChatUrl({ template, apiKey: KEY, serverAddress: ADDRESS })
    const raw = new URL(url).searchParams.get('provider')
    expect(raw).not.toBeNull()

    const provider = JSON.parse(raw as string)
    expect(provider.settings.baseURL).toBe(ADDRESS)
    expect(provider.settings.apiKey).toBe(KEY)
    expect(provider.settings.compatibility).toBe('strict')
  })

  it('{address} also survives the single decode of a JSON payload', () => {
    // Verified against a running instance: the query parser decodes the value
    // once, which turns `http%3A%2F%2F...` back into the address. The encoded
    // form therefore works here too — it is the safer choice whenever the
    // address might contain `&` or `#`.
    const template =
      'https://aiaw.app/set-provider?provider={"type":"openai","settings":{"apiKey":"{key}","baseURL":"{address}"}}'
    const url = resolveChatUrl({ template, apiKey: KEY, serverAddress: ADDRESS })
    const provider = JSON.parse(new URL(url).searchParams.get('provider') as string)
    expect(provider.settings.baseURL).toBe(ADDRESS)
  })

  it('resolves both spellings in one template independently', () => {
    const url = resolveChatUrl({
      template: 'https://x.example.com/?raw={base_url}&enc={address}',
      serverAddress: ADDRESS,
    })
    const params = new URL(url).searchParams
    expect(params.get('raw')).toBe(ADDRESS)
    expect(params.get('enc')).toBe(ADDRESS)
  })

  it('normalizes a bare key with the sk- prefix', () => {
    expect(
      resolveChatUrl({
        template: 'https://x.example.com/?k={key}',
        apiKey: 'bare-key',
        serverAddress: ADDRESS,
      })
    ).toBe('https://x.example.com/?k=sk-bare-key')
  })

  it('leaves unknown placeholders untouched', () => {
    expect(
      resolveChatUrl({
        template: 'https://x.example.com/?a={unknown}',
        serverAddress: ADDRESS,
      })
    ).toBe('https://x.example.com/?a={unknown}')
  })
})

describe('chatLinkRequiresApiKey', () => {
  it('detects key-dependent templates', () => {
    expect(chatLinkRequiresApiKey('https://x/?k={key}')).toBe(true)
    expect(chatLinkRequiresApiKey('https://x/?k={cherryConfig}')).toBe(true)
  })

  it('does not treat an address-only template as key-dependent', () => {
    expect(chatLinkRequiresApiKey('https://x/?b={base_url}')).toBe(false)
    expect(chatLinkRequiresApiKey('https://x/?b={address}')).toBe(false)
  })
})
