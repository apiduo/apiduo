import { describe, expect, it } from 'vitest'

import { classifyNavAppHref, isClientLink } from '../href'

/**
 * The classification decides two things that matter: whether the sidebar opens
 * a tab, and whether the value is allowed to reach an anchor `href` at all. The
 * dangerous-scheme cases are a security boundary, not a formatting preference.
 */

describe('classifyNavAppHref', () => {
  it('treats http(s) addresses as web targets', () => {
    expect(classifyNavAppHref('https://langfuse.example.com')).toBe('web')
    expect(classifyNavAppHref('HTTP://example.com/x')).toBe('web')
  })

  it('treats a scheme with an authority marker as a client link', () => {
    expect(
      classifyNavAppHref('cherrystudio://providers/api-keys?v=1&data=aGVsbG8=')
    ).toBe('app')
    expect(classifyNavAppHref('CherryStudio://providers')).toBe('app')
    expect(classifyNavAppHref('vscode://file/tmp/project')).toBe('app')
  })

  it('rejects a web address with no host', () => {
    expect(classifyNavAppHref('https://')).toBeNull()
  })

  it('rejects a placeholder in the authority', () => {
    // The authority decides where the request goes. A template there would let
    // a stored value claim one host and resolve to another — including this
    // console's own origin.
    expect(classifyNavAppHref('https://{host}/path?k={key}')).toBeNull()
    expect(classifyNavAppHref('https://{gateway.example.com}/admin')).toBeNull()
  })

  it('accepts a placeholder outside the authority', () => {
    expect(classifyNavAppHref('https://chat.example.com/?key={key}')).toBe('web')
    expect(
      classifyNavAppHref(
        'cherrystudio://providers/api-keys?v=1&data={cherryConfig}'
      )
    ).toBe('app')
  })

  it('rejects a scheme without the authority marker', () => {
    // `javascript:alert(1)` parses as a URL with a scheme, which is exactly why
    // the shape check demands `//`, and why this copy must match the server.
    expect(classifyNavAppHref('javascript:alert(1)')).toBeNull()
    expect(classifyNavAppHref('cherrystudio:providers')).toBeNull()
  })

  it('rejects schemes that execute code or read local resources', () => {
    // The first three carry an authority, so the shape check alone would let
    // them through; only the scheme denial stops them.
    for (const href of [
      'javascript://example.com/%0Aalert(1)',
      'data://text/html,<script>alert(1)</script>',
      'file:///etc/passwd',
      'vbscript://msgbox(1)',
      'about://blank',
      'chrome://settings',
      'view-source://https://example.com',
    ]) {
      expect(classifyNavAppHref(href), href).toBeNull()
    }
  })

  it('rejects empty and relative values', () => {
    expect(classifyNavAppHref('')).toBeNull()
    expect(classifyNavAppHref('   ')).toBeNull()
    expect(classifyNavAppHref('/profile')).toBeNull()
    expect(classifyNavAppHref('//example.com/profile')).toBeNull()
  })
})

describe('isClientLink', () => {
  it('is true only for a client deep link', () => {
    expect(isClientLink('cherrystudio://providers/api-keys')).toBe(true)
    expect(isClientLink('https://example.com')).toBe(false)
    expect(isClientLink('javascript://example.com/%0Aalert(1)')).toBe(false)
  })
})
