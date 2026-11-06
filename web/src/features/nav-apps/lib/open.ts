import { classifyNavAppHref } from './href'
import { navAppNeedsToken, resolveNavAppHref } from './template'

/**
 * What clicking an entry should do, decided before any side effect runs.
 *
 * Keeping this a pure function means the two failure modes — no key to fill in,
 * and an address that is not usable even after substitution — are testable
 * without a browser, and the components stay a thin dispatch layer.
 */
export type NavAppOpenPlan =
  | { action: 'client'; url: string }
  | { action: 'tab'; url: string }
  | { action: 'missing-key' }
  | { action: 'invalid' }

export function planNavAppOpen(params: {
  href: string
  apiKey?: string
  serverAddress: string
}): NavAppOpenPlan {
  const needsToken = navAppNeedsToken(params.href)
  if (needsToken && !params.apiKey) {
    return { action: 'missing-key' }
  }

  const url = resolveNavAppHref({
    href: params.href,
    apiKey: needsToken ? params.apiKey : undefined,
    serverAddress: params.serverAddress,
  })

  const kind = classifyNavAppHref(url)
  if (!kind) return { action: 'invalid' }

  // A deep link is handed to the operating system, so it must not go through
  // window.open: the tab that call creates stays behind empty once the
  // application takes over.
  return kind === 'app' ? { action: 'client', url } : { action: 'tab', url }
}
