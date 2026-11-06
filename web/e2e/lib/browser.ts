import { existsSync } from 'node:fs'

import puppeteer, { type Browser } from 'puppeteer-core'

/**
 * Where Chrome might live, in the order we look for it.
 *
 * `puppeteer-core` deliberately ships no browser binary, so the suite borrows
 * the one the machine already has instead of downloading a second copy. The
 * environment variable exists for images that install Chrome somewhere unusual;
 * the rest are the defaults of macOS, Debian/Ubuntu and Alpine's chromium.
 */
const CHROME_PATHS = [
  process.env.E2E_CHROME,
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  '/usr/bin/google-chrome',
  '/usr/bin/chromium',
  '/usr/bin/chromium-browser',
].filter((path): path is string => Boolean(path))

export async function launchBrowser(): Promise<Browser> {
  const executablePath = CHROME_PATHS.find((path) => existsSync(path))
  if (!executablePath) {
    throw new Error(
      [
        'No Chrome found. Looked in:',
        ...CHROME_PATHS.map((path) => `  ${path}`),
        'Set E2E_CHROME to point at your browser.',
      ].join('\n')
    )
  }

  return puppeteer.launch({
    executablePath,
    headless: true,
    args: [
      '--no-sandbox',
      '--disable-gpu',
      '--hide-scrollbars',
      // The suite talks to a loopback instance. A developer shell that exports
      // HTTP_PROXY would otherwise route those requests through it and fail
      // with an opaque connection error.
      '--no-proxy-server',
    ],
  })
}
