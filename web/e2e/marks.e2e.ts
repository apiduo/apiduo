/* eslint-disable no-console -- a test runner reports to the terminal; there is no other sink here. */
import assert from 'node:assert/strict'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'

import type { Page } from 'puppeteer-core'

import { launchBrowser } from './lib/browser'
import { applySession, signIn } from './lib/session'

/**
 * End-to-end checks for the apiduo surface marks.
 *
 * The unit suite already proves the attribute is on the right element. What it
 * cannot prove is that a browser turns that attribute into a visible mark:
 * `background-clip: text` is silently dropped by browsers that do not support
 * it, and the gradient is only real if the computed style says so. That is the
 * one thing this file exists to check.
 *
 * Requires a running instance:
 *   E2E_BASE_URL=http://127.0.0.1:3000 \
 *   E2E_USERNAME=root E2E_PASSWORD=... bun run test:e2e
 */

const BASE_URL = (process.env.E2E_BASE_URL ?? 'http://127.0.0.1:3000').replace(
  /\/$/,
  ''
)
const USERNAME = process.env.E2E_USERNAME ?? 'root'
const PASSWORD = process.env.E2E_PASSWORD ?? ''
const OUTPUT_DIR = process.env.E2E_OUTPUT ?? join(process.cwd(), 'e2e/output')

let failures = 0

function check(label: string, run: () => void): void {
  try {
    run()
    console.log(`  ok    ${label}`)
  } catch (error) {
    failures += 1
    console.log(`  FAIL  ${label}`)
    // Print the whole message, indented. `assert.equal` puts the two values on
    // their own line, so keeping only the headline would hide the difference —
    // which is the one thing a failing run needs to show.
    const detail = (error as Error).message
      .split('\n')
      .map((line) => `        ${line}`)
      .join('\n')
    console.log(detail)
  }
}

async function ensureReachable(): Promise<void> {
  try {
    const response = await fetch(`${BASE_URL}/api/status`, {
      signal: AbortSignal.timeout(5000),
    })
    if (!response.ok) throw new Error(`HTTP ${response.status}`)
  } catch (error) {
    throw new Error(
      `No console answering at ${BASE_URL}.\n` +
        `  Start one, or point E2E_BASE_URL at a running instance.\n` +
        `  ${(error as Error).message}`
    )
  }
}

/** Reads what the browser actually paints, not what the markup claims. */
async function readTabStyles(page: Page) {
  return page.evaluate(() => {
    const read = (element: Element | null) => {
      if (!element) return null
      const style = getComputedStyle(element)
      const dot = getComputedStyle(element, '::before')
      return {
        clip:
          style.webkitBackgroundClip ||
          (style as unknown as { backgroundClip: string }).backgroundClip,
        gradient: style.backgroundImage.includes('gradient'),
        fill: style.webkitTextFillColor,
        color: style.color,
        dotWidth: dot.width,
        dotRadius: dot.borderTopLeftRadius,
        dotImage: dot.backgroundImage,
      }
    }
    return {
      marked: read(document.querySelector('[data-apiduo="source"]')),
      upstream: read(
        document.querySelector(
          '[data-slot="tabs-trigger"]:not([data-apiduo])'
        )
      ),
      markedCount: document.querySelectorAll('[data-apiduo="source"]').length,
      badgeCount: document.querySelectorAll('[data-apiduo="admin-badge"]')
        .length,
    }
  })
}

async function main(): Promise<void> {
  if (!PASSWORD) {
    throw new Error(
      'E2E_PASSWORD is required. It is the password of an administrator on the target instance.'
    )
  }

  await ensureReachable()
  const session = await signIn(BASE_URL, USERNAME, PASSWORD)
  console.log(`signed in as ${USERNAME} on ${BASE_URL}\n`)

  const browser = await launchBrowser()
  try {
    const page = await browser.newPage()
    await page.setViewport({ width: 1440, height: 900, deviceScaleFactor: 2 })
    await applySession(page, session)

    await page.goto(`${BASE_URL}/dashboard/sk`, {
      waitUntil: 'networkidle2',
      timeout: 60000,
    })
    await page.waitForSelector('[data-slot="tabs-list"]', { timeout: 30000 })

    const styles = await readTabStyles(page)

    console.log('dashboard section tabs')
    check('three apiduo tabs are marked', () => {
      assert.equal(styles.markedCount, 3)
    })
    check('the admin-only badge is present', () => {
      assert.equal(styles.badgeCount, 1)
    })
    check('a marked tab paints gradient text', () => {
      assert.ok(styles.marked, 'no marked tab rendered')
      assert.ok(
        styles.marked.gradient,
        'background-image has no gradient — the stylesheet did not load'
      )
      assert.equal(
        styles.marked.clip,
        'text',
        `background-clip is ${styles.marked.clip}, so the gradient paints behind the text instead of inside it`
      )
      assert.equal(
        styles.marked.fill,
        'rgba(0, 0, 0, 0)',
        'the text fill is opaque, so the gradient would not show through'
      )
    })
    check('a marked tab carries a 5px gradient scan dot', () => {
      assert.ok(styles.marked, 'no marked tab rendered')
      assert.equal(
        styles.marked.dotWidth,
        '5px',
        'the dot is not 5px — it would not read as a mark at tab size'
      )
      assert.equal(
        styles.marked.dotRadius,
        '999px',
        'the dot is not round, which is the square-glyph regression'
      )
      assert.ok(
        styles.marked.dotImage.includes('radial-gradient'),
        `the dot lost its radial highlight, got ${styles.marked.dotImage}`
      )
    })
    check('an upstream tab stays a plain label', () => {
      assert.ok(styles.upstream, 'no upstream tab rendered')
      assert.notEqual(
        styles.upstream.clip,
        'text',
        'an unmarked tab picked up the marked treatment'
      )
      assert.equal(styles.upstream.gradient, false)
    })

    mkdirSync(OUTPUT_DIR, { recursive: true })
    const shot = join(OUTPUT_DIR, 'dashboard-tabs.png')
    const tabs = await page.$('[data-slot="tabs-list"]')
    if (tabs) await tabs.screenshot({ path: shot })
    console.log(`\nscreenshot: ${shot}`)

    /*
     * The settings screen is the case a unit test cannot see: the mark has to
     * survive a container that also owns an icon. Two elements carry it — the
     * sidebar entry the reader clicks and the page title they land on — and
     * both must agree, or the screen looks half-marked.
     */
    await page.goto(`${BASE_URL}/system-settings/site/nav-apps`, {
      waitUntil: 'networkidle2',
      timeout: 60000,
    })
    await new Promise((resolve) => setTimeout(resolve, 1500))

    const settings = await page.evaluate(() => {
      const marked = [...document.querySelectorAll('[data-apiduo="self"]')]
      return marked.map((element) => {
        const label = element.querySelector('span')
        const style = label ? getComputedStyle(label) : null
        const icon = element.querySelector('svg')
        return {
          text: label?.textContent?.trim() ?? '',
          fill: style?.webkitTextFillColor ?? '',
          gradient: style ? style.backgroundImage.includes('gradient') : false,
          iconStroke: icon ? getComputedStyle(icon).stroke : null,
        }
      })
    })

    console.log('\nsettings surface')
    check('the sidebar entry and the page title are both marked', () => {
      // Counted by role, not by total: every apiduo sub-item in the sidebar
      // (one per marked section) carries the icon, the page title does not.
      // A hard total would break the day a second section ships.
      const sidebarEntries = settings.filter((item) => item.iconStroke)
      const titles = settings.filter((item) => !item.iconStroke)
      assert.ok(
        sidebarEntries.length >= 1,
        `no marked sidebar entry found among ${settings.length}`
      )
      assert.equal(
        titles.length,
        1,
        `expected exactly the page title marked, got ${titles.length}`
      )
      assert.ok(
        titles[0]?.text.length,
        'the marked title rendered without text'
      )
    })
    check('both settings marks paint gradient text', () => {
      for (const item of settings) {
        assert.ok(item.gradient, `${item.text} has no gradient`)
        assert.equal(
          item.fill,
          'rgba(0, 0, 0, 0)',
          `${item.text} is not using text-fill-color, so the gradient is hidden`
        )
      }
    })
    check('the sidebar entry carries the gradient-stroked icon', () => {
      // The icon is the structural half of the mark: upstream sub-items are
      // bare text, so the icon alone identifies the entry as apiduo's.
      for (const item of settings.filter((entry) => entry.iconStroke)) {
        assert.ok(
          (item.iconStroke ?? '').includes('url('),
          `${item.text} icon stroke is ${item.iconStroke}, not a gradient reference`
        )
      }
    })

    mkdirSync(OUTPUT_DIR, { recursive: true })
    const settingsShot = join(OUTPUT_DIR, 'settings-mark.png')
    await page.screenshot({ path: settingsShot })
    console.log(`screenshot: ${settingsShot}`)
  } finally {
    await browser.close()
  }

  if (failures > 0) {
    console.log(`\n${failures} check(s) failed`)
    process.exitCode = 1
    return
  }
  console.log('\nall checks passed')
}

await main()
