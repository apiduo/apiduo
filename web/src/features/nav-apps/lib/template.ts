import {
  chatLinkRequiresApiKey,
  resolveChatUrl,
} from '@/features/chat/lib/chat-links'

/**
 * Placeholders a stored address may contain, filled in the browser when the
 * entry is clicked.
 *
 * The list mirrors `resolveChatUrl` in `features/chat/lib/chat-links.ts` and
 * `navAppTemplateTokens` in `controller/nav_app.go`: the editor advertises it,
 * the resolver consumes it, and the server uses its copy to refuse templates
 * where they cannot work (an embedded page). A token added here without the
 * other two changes would be advertised but never substituted.
 */
export const NAV_APP_PLACEHOLDERS = [
  '{key}',
  '{address}',
  '{base_url}',
  '{cherryConfig}',
  '{aionuiConfig}',
  '{deepchatConfig}',
  '{aqbotConfig}',
] as const

/** Whether the address carries anything to substitute at click time. */
export function navAppUsesTemplate(href: string): boolean {
  return NAV_APP_PLACEHOLDERS.some((token) => href.includes(token))
}

/**
 * Whether the address needs the viewer's own API key.
 *
 * Deliberately the same predicate the chat presets use rather than a second
 * list: `{key}` and the per-client payloads embed a credential, `{address}` and
 * `{base_url}` do not, and a divergence would either leak a key into a link
 * that never asked for one or silently produce a link with an empty key.
 */
export function navAppNeedsToken(href: string): boolean {
  return chatLinkRequiresApiKey(href)
}

/**
 * Substitutes the placeholders using the viewer's key and this deployment's
 * address.
 *
 * Runs in the browser on purpose: the stored value is shared by everyone who
 * can see the entry, so the credential must never be part of it.
 */
export function resolveNavAppHref(params: {
  href: string
  apiKey?: string
  serverAddress: string
}): string {
  return resolveChatUrl({
    template: params.href,
    apiKey: params.apiKey,
    serverAddress: params.serverAddress,
  })
}
