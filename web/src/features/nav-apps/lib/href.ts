/**
 * What an entry's address points at.
 *
 * `web` is an ordinary http(s) URL: the browser can render it, so it may open
 * in a tab or be embedded in a frame. `app` is a client deep link such as
 * `cherrystudio://providers/api-keys`: the browser cannot render it, and hands
 * the address to the operating system, which starts the application registered
 * for that scheme.
 */
export type NavAppHrefKind = 'web' | 'app'

/**
 * Schemes that would execute script in this console's origin or read local
 * resources once the value lands in an anchor `href`.
 *
 * The `//` requirement below already rejects the common `javascript:alert(1)`
 * shape, but `javascript://example.com/%0Aalert(1)` carries an authority and
 * browsers have historically executed it. Denying these by name does not depend
 * on parser quirks, and it also keeps schemes that can never work from a page
 * (`chrome://`, `about://`) out of the list.
 *
 * Mirrors `navAppBlockedSchemes` in `controller/nav_app.go`.
 */
const BLOCKED_SCHEMES = new Set([
  'about',
  'blob',
  'chrome',
  'chrome-extension',
  'data',
  'file',
  'filesystem',
  'jar',
  'javascript',
  'mozilla',
  'ms-browser-extension',
  'resource',
  'vbscript',
  'view-source',
])

/** `scheme://rest`, with the scheme limited to what RFC 3986 allows. */
const APP_LINK = /^([a-z][a-z0-9+.-]{1,31}):\/\/\S*$/i

/**
 * Classifies an address, returning `null` when it is neither a usable web URL
 * nor an allowed client link.
 *
 * This mirrors the server-side check. The server is the boundary that matters;
 * the copy exists so the form reports the problem before the round trip instead
 * of after it, and so rendering can pick the right anchor attributes.
 */
export function classifyNavAppHref(href: string): NavAppHrefKind | null {
  const value = href.trim()
  if (!value) return null

  if (/^https?:\/\//i.test(value)) {
    // A placeholder in the authority decides where the request goes, so it
    // would let a template claim one host and resolve to another — including
    // this console's own origin. The server rejects it for the same reason
    // (`url.Parse` refuses the `{`); checking here keeps the message inline.
    const authority = value.slice(value.indexOf('://') + 3).split(/[/?#]/, 1)[0]
    if (authority.includes('{')) return null

    try {
      return new URL(value).host ? 'web' : null
    } catch {
      return null
    }
  }

  const match = APP_LINK.exec(value)
  if (!match) return null
  return BLOCKED_SCHEMES.has(match[1].toLowerCase()) ? null : 'app'
}

/**
 * Whether the address is a client deep link.
 *
 * Consumers use this to drop the chrome that only makes sense for a web page. A
 * deep link must not open in a new tab in particular: once the browser hands the
 * address to the application, the tab it opened stays behind empty.
 */
export function isClientLink(href: string): boolean {
  return classifyNavAppHref(href) === 'app'
}
