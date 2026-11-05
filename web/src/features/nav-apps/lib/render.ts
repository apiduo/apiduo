import type { NavItem, TopNavLink } from '@/components/layout/types'

import { navAppIcon } from './icons'
import type { NavApp } from '../types'

/**
 * Expands one language code into the spellings an operator might reasonably use
 * as a key in `name_i18n`.
 *
 * The console does not use BCP-47 codes: `supportedLngs` declares `zhCN` and
 * `zhTW`, so splitting on "-" yields the code unchanged and a `zh` key would
 * never match. Enumerating the equivalents keeps the field usable regardless of
 * which spelling the operator picked.
 */
function languageCandidates(tag: string): string[] {
  const out = new Set<string>([tag])
  const camel = /^([a-zA-Z]{2})([A-Z]{2})$/.exec(tag)
  if (camel) {
    const [, language, region] = camel
    out.add(`${language}-${region}`)
    out.add(`${language}_${region}`)
    out.add(language.toLowerCase())
    return [...out]
  }
  const parts = tag.split(/[-_]/)
  if (parts.length > 1 && parts[0]) {
    out.add(parts[0])
    out.add(`${parts[0]}-${(parts[1] ?? '').toUpperCase()}`)
  }
  out.add(tag.toLowerCase())
  return [...out]
}

/**
 * Resolves the display name, preferring a localized value when one exists.
 *
 * Administrator-entered text is data, not a translation key: it cannot be
 * added to the shipped locale files, so localization is stored alongside the
 * entry instead.
 */
export function navAppTitle(app: NavApp, languages: readonly string[]): string {
  if (!app.name_i18n) return app.name

  let table: Record<string, string>
  try {
    const parsed = JSON.parse(app.name_i18n) as unknown
    if (!parsed || typeof parsed !== 'object') return app.name
    table = parsed as Record<string, string>
  } catch {
    return app.name
  }

  // Walk the same chain i18next uses to render the surrounding UI. Using only
  // `i18n.language` is not enough: when the configured language has no bundle
  // for a key, the visible text falls back to the fallback language, so a name
  // localised for that fallback would never be picked while the UI still reads
  // Chinese. Matching the whole chain keeps the label consistent with the page.
  for (const tag of languages) {
    if (!tag) continue
    for (const candidate of languageCandidates(tag)) {
      const value = table[candidate]
      if (value) return value
    }
  }
  return app.name
}

/**
 * Whether an embedded target may be rendered in a frame.
 *
 * The backend already rejects same-origin targets, but it cannot know every
 * hostname this deployment is reachable on. Repeating the check in the browser
 * closes that gap: a frame served from our own origin could read our cookies
 * and local storage.
 */
export function canEmbed(href: string): boolean {
  if (typeof window === 'undefined') return false
  try {
    const target = new URL(href)
    if (target.protocol !== 'http:' && target.protocol !== 'https:') return false
    return target.origin !== window.location.origin
  } catch {
    return false
  }
}

/**
 * Approximates the registrable domain (eTLD+1) of a hostname.
 *
 * Browsers key SameSite and third-party cookie decisions on the *site*, not the
 * origin, so `a.example.com` and `b.example.com` count as first-party to each
 * other even though their origins differ. A full public-suffix list is not worth
 * bundling here: the operator's deployments live under one main domain, which
 * the last-two-labels rule classifies correctly. Public suffixes with two labels
 * (`example.co.uk`) are treated as a single site, which can only cause a missed
 * warning, never a wrong one that blocks configuration.
 */
const MULTI_LABEL_PUBLIC_SUFFIXES = new Set([
  'co.uk', 'org.uk', 'ac.uk', 'gov.uk',
  'com.cn', 'net.cn', 'org.cn', 'gov.cn',
  'com.au', 'net.au', 'co.jp', 'co.kr', 'com.br', 'com.tw', 'com.hk',
  'co.nz', 'com.sg', 'com.mx', 'com.tr', 'co.in',
  'github.io', 'vercel.app', 'netlify.app', 'pages.dev', 'herokuapp.com',
])

function siteKey(hostname: string): string {
  const host = hostname.toLowerCase()
  if (host === 'localhost' || /^\d+\.\d+\.\d+\.\d+$/.test(host) || host.includes(':')) {
    return host
  }
  const labels = host.split('.')
  if (labels.length <= 2) return host
  const lastTwo = labels.slice(-2).join('.')
  if (MULTI_LABEL_PUBLIC_SUFFIXES.has(lastTwo)) {
    return labels.slice(-3).join('.')
  }
  return lastTwo
}

/**
 * Whether the target shares a site with the console, which is what decides
 * whether the browser treats its cookies as first-party inside a frame.
 */
export function isSameSite(href: string): boolean {
  if (typeof window === 'undefined') return false
  try {
    return siteKey(new URL(href).hostname) === siteKey(window.location.hostname)
  } catch {
    return false
  }
}

export function navAppToSidebarItem(
  app: NavApp,
  languages: readonly string[]
): NavItem {
  const title = navAppTitle(app, languages)
  const icon = navAppIcon(app.icon)
  // Embedded entries navigate to the in-app container route; everything else
  // opens directly, so no container route exists for it.
  if (app.display_type === 'iframe') {
    return {
      title,
      icon,
      url: `/app/${app.slug}`,
      external: false,
    }
  }
  return {
    title,
    icon,
    url: app.href,
    external: true,
  }
}

export function navAppToTopLink(
  app: NavApp,
  languages: readonly string[]
): TopNavLink {
  const title = navAppTitle(app, languages)
  if (app.display_type === 'iframe') {
    return { title, href: `/app/${app.slug}`, external: false }
  }
  return { title, href: app.href, external: true }
}
