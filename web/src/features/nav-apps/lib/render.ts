import type { NavGroup, NavItem, TopNavLink } from '@/components/layout/types'

import { classifyNavAppHref, isClientLink } from './href'
import { navAppIcon } from './icons'
import type { NavApp, NavAppGroup } from '../types'

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
export function localizedName(
  name: string,
  nameI18n: string,
  languages: readonly string[]
): string {
  if (!nameI18n) return name

  let table: Record<string, string>
  try {
    const parsed = JSON.parse(nameI18n) as unknown
    if (!parsed || typeof parsed !== 'object') return name
    table = parsed as Record<string, string>
  } catch {
    return name
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
  return name
}

export function navAppTitle(app: NavApp, languages: readonly string[]): string {
  return localizedName(app.name, app.name_i18n, languages)
}

export function navAppGroupTitle(
  group: NavAppGroup,
  languages: readonly string[]
): string {
  return localizedName(group.name, group.name_i18n, languages)
}

/**
 * Whether an embedded target may be rendered in a frame.
 *
 * The backend already rejects same-origin targets, but it cannot know every
 * hostname this deployment is reachable on. Repeating the check in the browser
 * closes that gap: a frame served from our own origin could read our cookies
 * and local storage.
 *
 * A client deep link is excluded for a different reason: it resolves to no
 * document at all, so a frame around it could only ever show an error.
 */
export function canEmbed(href: string): boolean {
  if (typeof window === 'undefined') return false
  if (classifyNavAppHref(href) !== 'web') return false
  try {
    return new URL(href).origin !== window.location.origin
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
      // Every sidebar entry from this feature is apiduo's, however it opens.
      apiduoIconDot: true,
    }
  }
  return {
    title,
    icon,
    url: app.href,
    external: true,
    // Tells the sidebar to skip `target=_blank`: the browser hands a deep link
    // to the operating system and would leave the opened tab behind empty.
    appLink: isClientLink(app.href),
    apiduoIconDot: true,
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
  return {
    title,
    href: app.href,
    external: true,
    appLink: isClientLink(app.href),
  }
}

/** Id of the bucket that receives entries without a group. */
export const DEFAULT_NAV_APP_GROUP_ID = 'apiduo-default'

/**
 * Projects the entry list into sidebar groups, each holding at most two levels.
 *
 * Two independent dimensions are resolved here: `group_slug` decides which
 * heading an entry appears under, and `parent_slug` decides whether it is a
 * collapsible row. A child inherits its parent's group, because a sub-entry's
 * own group field would otherwise split a menu that is meant to stay together.
 *
 * Entries pointing at a missing parent or a missing group fall back to the top
 * level and the default group instead of disappearing: the stored value may
 * predate these rules, and a hidden entry is worse than a misplaced one.
 */
export function navAppsToSidebarGroups(
  apps: NavApp[],
  groups: NavAppGroup[],
  languages: readonly string[],
  defaultTitle: string
): NavGroup[] {
  const bySlug = new Map(apps.map((app) => [app.slug, app]))
  const childrenOf = new Map<string, NavApp[]>()
  const roots: NavApp[] = []

  for (const app of apps) {
    const parentSlug = app.parent_slug?.trim()
    const parent = parentSlug ? bySlug.get(parentSlug) : undefined
    const usableParent =
      parent && parent.slug !== app.slug && !parent.parent_slug?.trim()
    if (!usableParent) {
      roots.push(app)
      continue
    }
    const list = childrenOf.get(parent.slug) ?? []
    list.push(app)
    childrenOf.set(parent.slug, list)
  }

  const knownGroups = new Set(groups.map((group) => group.slug))
  const buckets = new Map<string, NavItem[]>()
  for (const root of roots) {
    const key =
      root.group_slug && knownGroups.has(root.group_slug)
        ? root.group_slug
        : DEFAULT_NAV_APP_GROUP_ID
    const children = childrenOf.get(root.slug)
    const item = buildNavItem(root, children, languages)
    const list = buckets.get(key) ?? []
    list.push(item)
    buckets.set(key, list)
  }

  const projected: NavGroup[] = []
  for (const group of groups) {
    const items = buckets.get(group.slug)
    if (!items?.length) continue
    projected.push({
      id: group.slug,
      title: navAppGroupTitle(group, languages),
      items,
    })
  }
  const defaults = buckets.get(DEFAULT_NAV_APP_GROUP_ID)
  if (defaults?.length) {
    projected.push({
      id: DEFAULT_NAV_APP_GROUP_ID,
      title: defaultTitle,
      items: defaults,
    })
  }
  return projected
}

function buildNavItem(
  root: NavApp,
  children: NavApp[] | undefined,
  languages: readonly string[]
): NavItem {
  const rootItem = navAppToSidebarItem(root, languages)
  if (!children?.length) return rootItem
  return {
    title: rootItem.title,
    icon: rootItem.icon,
    apiduoIconDot: true,
    items: children.map((child) => {
      const childItem = navAppToSidebarItem(child, languages)
      const url = 'url' in childItem ? childItem.url : ''
      return { ...childItem, url }
    }),
  } satisfies NavItem
}

/**
 * Applies the configured top-level order.
 *
 * Ids missing from the order keep their relative position and sort after the
 * listed ones, so a group added by a later release still shows up rather than
 * being dropped by an order list that predates it.
 */
export function orderSidebarGroups(
  groups: NavGroup[],
  order: readonly string[]
): NavGroup[] {
  if (order.length === 0) return groups
  const rank = new Map<string, number>()
  order.forEach((id, index) => rank.set(id, index))
  // NavGroup.id is optional in the layout contract, so an unidentified group
  // simply keeps its original position at the end.
  return [...groups].sort((left, right) => {
    const leftRank = rank.get(left.id ?? '') ?? Number.MAX_SAFE_INTEGER
    const rightRank = rank.get(right.id ?? '') ?? Number.MAX_SAFE_INTEGER
    return leftRank - rightRank
  })
}
