import type { Page } from 'puppeteer-core'

type Cookie = {
  name: string
  value: string
  domain: string
  path: string
  httpOnly: boolean
}

export type Session = {
  cookies: Cookie[]
  accessToken: string
}

/**
 * Signs in through the management API and keeps the cookies it returns.
 *
 * Going through the API instead of the login form keeps the suite independent
 * of how the console renders its sign-in screen — that screen is upstream's to
 * change, and a selector break there should not fail a test about tab colours.
 */
export async function signIn(
  baseUrl: string,
  username: string,
  password: string
): Promise<Session> {
  const response = await fetch(`${baseUrl}/api/user/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, password }),
  })

  if (!response.ok) {
    throw new Error(`Login returned HTTP ${response.status}`)
  }

  const payload = (await response.json()) as {
    success?: boolean
    message?: string
    data?: { access_token?: string }
  }
  if (!payload.success) {
    throw new Error(`Login rejected: ${payload.message ?? 'unknown reason'}`)
  }

  return {
    cookies: parseSetCookie(response.headers.getSetCookie(), new URL(baseUrl).hostname),
    accessToken: payload.data?.access_token ?? '',
  }
}

/**
 * `Set-Cookie` carries a path per cookie: the refresh token is scoped to
 * `/api/user/auth` while the session flag covers the whole origin. Dropping the
 * path would make the browser send neither where the application expects it.
 */
function parseSetCookie(headers: string[], domain: string): Cookie[] {
  return headers.map((raw) => {
    const [pair, ...attributes] = raw.split(';')
    const separator = pair.indexOf('=')
    const cookie: Cookie = {
      name: pair.slice(0, separator).trim(),
      value: pair.slice(separator + 1).trim(),
      domain,
      path: '/',
      httpOnly: attributes.some(
        (attribute) => attribute.trim().toLowerCase() === 'httponly'
      ),
    }
    const path = attributes.find((attribute) =>
      attribute.trim().toLowerCase().startsWith('path=')
    )
    if (path) cookie.path = path.split('=')[1].trim()
    return cookie
  })
}

export async function applySession(page: Page, session: Session): Promise<void> {
  const client = await page.createCDPSession()
  await client.send('Network.enable')
  for (const cookie of session.cookies) {
    await client.send('Network.setCookie', cookie)
  }
}
