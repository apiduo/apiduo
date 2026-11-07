#!/usr/bin/env node
/**
 * BrowserSkill regression for the configured navigation entries.
 *
 * This is the durable front-end interaction check: it uses the user's real
 * Chrome (Agent Window) and real press-move-release input, so it catches
 * gesture problems a synthetic pointer script can hide.
 *
 *   BSK_PASSWORD='...' node web/e2e/bsk-nav-apps.mjs
 *
 * Optional env: BSK_BASE_URL (default http://127.0.0.1:3001), BSK_USER,
 * BSK_GROUP_LABEL (default Workxxx), BSK_LOGIN_LABEL (default 登录),
 * BSK_OUT_DIR (default /tmp/apiduo-e2e-data).
 *
 * Prerequisite: a reachable bsk daemon with a connected browser, and a local
 * apiduo instance already initialised with an admin account. Each run flips the
 * default group between the first and last position, so every run exercises a
 * real reorder, a save and a reload without depending on the previous state.
 */
import { execFileSync } from 'node:child_process'
import { mkdirSync } from 'node:fs'
import path from 'node:path'

const BASE = process.env.BSK_BASE_URL ?? 'http://127.0.0.1:3001'
const USER = process.env.BSK_USER ?? 'root'
const PASSWORD = process.env.BSK_PASSWORD ?? ''
const GROUP_LABEL = process.env.BSK_GROUP_LABEL ?? 'Workxxx'
const LOGIN_LABEL = process.env.BSK_LOGIN_LABEL ?? '登录'
const OUT_DIR = process.env.BSK_OUT_DIR ?? '/tmp/apiduo-e2e-data'

function bsk(args) {
  let raw
  try {
    raw = execFileSync('bsk', [...args, '--json'], {
      encoding: 'utf8',
      env: { ...process.env, BSK_AUTO_START: '0' },
      maxBuffer: 32 * 1024 * 1024,
    })
  } catch (error) {
    const detail = `${error.stdout ?? ''}${error.stderr ?? ''}`.trim()
    throw new Error(
      `bsk ${args.join(' ')} failed: ${detail.slice(0, 700) || error.message}`
    )
  }
  try {
    return JSON.parse(raw)
  } catch {
    throw new Error(`bsk returned non-JSON for ${args[0]}: ${raw.slice(0, 200)}`)
  }
}

function sync(session) {
  return bsk(['cooperate', 'sync', '--session', session])
}

/**
 * Untargeted actions (navigate / reload / screenshot) spend a single-use
 * observation receipt. A redirect or an async render can invalidate it before
 * the action lands, so always take a fresh sync and retry that specific class
 * of refusal instead of failing the whole regression.
 */
function untargeted(session, args) {
  for (let attempt = 0; attempt < 3; attempt += 1) {
    const receipt = sync(session)
    try {
      return bsk([
        ...args,
        '--session', session,
        '--context', receipt.context_id,
        '--observation', receipt.observation_id,
      ])
    } catch (error) {
      const retryable = /observation_changed|observation_required|control_context_invalid/.test(error.message)
      if (!retryable || attempt === 2) throw error
      console.log(`  retry ${args[0]} with a fresh observation`)
    }
  }
}

function textOf(receipt) {
  return receipt?.observation?.text ?? ''
}

/** VOM lines carry @eN refs; find the first line containing a known label. */
function refFor(text, needle) {
  for (const line of text.split('\n')) {
    if (line.includes(needle)) {
      const match = /@(e\d+)/.exec(line)
      if (match) return `@${match[1]}`
    }
  }
  throw new Error(`VOM ref not found: ${needle}`)
}

function handleRefs(text) {
  // Restrict refs to the sidebar-order list. Entry cards further down the page
  // have their own drag handles, and those are usually outside the viewport —
  // using one as a drop target makes bsk reject the gesture as not visible.
  const lines = text.split('\n')
  const start = lines.findIndex(
    (line) => /^\s*list "/.test(line) && line.includes('内置')
  )
  if (start < 0) return []
  const baseIndent = /^\s*/.exec(lines[start])[0].length
  const refs = []
  for (let i = start + 1; i < lines.length; i += 1) {
    const line = lines[i]
    const indent = /^\s*/.exec(line)[0].length
    if (line.trim() && indent <= baseIndent) break
    if (line.includes('button') && /重新排序|reorder/i.test(line)) {
      const match = /@(e\d+)/.exec(line)
      if (match) refs.push(`@${match[1]}`)
    }
  }
  return refs
}

function groupOrderLine(text) {
  const line = text.split('\n').find((row) => /^\s*list "/.test(row) && row.includes('内置'))
  return line ? line.trim() : ''
}

function assert(condition, message) {
  if (!condition) throw new Error(message)
  console.log(`  PASS ${message}`)
}

async function loginToken() {
  const login = await fetch(`${BASE}/api/user/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username: USER, password: PASSWORD }),
  })
  const { data } = await login.json()
  return data?.access_token ?? ''
}

async function storedConfiguration(token) {
  const response = await fetch(`${BASE}/api/nav_apps/all`, {
    headers: { Authorization: `Bearer ${token}` },
  })
  return response.json()
}

async function main() {
  mkdirSync(OUT_DIR, { recursive: true })
  const status = bsk(['status'])
  assert(status.browsers?.length > 0, 'bsk daemon has a connected browser')

  const session = bsk(['session', 'start']).session_id
  assert(Boolean(session), 'agent-window session started')
  console.log(`  session ${session}`)

  try {
    bsk(['navigate', `${BASE}/sign-in`, '--session', session])
    // A fresh Agent Window may or may not reuse a stored session, and a page
    // that is still mounting has neither the login form nor the shell. Wait for
    // one of the two real states instead of treating "no form" as signed in.
    const shellVisible = (t) =>
      t.includes('Toggle Sidebar') || t.includes('link "控制台"')
    let receipt = sync(session)
    let text = textOf(receipt)
    let phase = 'loading'
    for (let attempt = 0; attempt < 16; attempt += 1) {
      text = textOf(receipt)
      if (text.includes(`button "${LOGIN_LABEL}"`)) {
        phase = 'login'
        break
      }
      if (shellVisible(text)) {
        phase = 'signed-in'
        break
      }
      await new Promise((resolve) => setTimeout(resolve, 500))
      receipt = sync(session)
    }
    assert(phase !== 'loading', 'sign-in page settled')

    if (phase === 'login') {
      bsk([
        'fill', refFor(text, 'textbox'), '--value', USER,
        '--session', session, '--context', receipt.context_id,
      ])
      bsk([
        'fill', 'input[name="password"]', '--value', PASSWORD,
        '--session', session, '--context', receipt.context_id,
      ])
      bsk([
        'click', refFor(text, `button "${LOGIN_LABEL}"`),
        '--session', session, '--context', receipt.context_id,
      ])
      // Login is asynchronous. Navigating before the token is stored makes
      // the route guard bounce straight back to /sign-in, so wait for the
      // authenticated shell, not merely for the form to disappear.
      let signedIn = false
      for (let attempt = 0; attempt < 16; attempt += 1) {
        await new Promise((resolve) => setTimeout(resolve, 500))
        receipt = sync(session)
        if (shellVisible(textOf(receipt))) {
          signedIn = true
          break
        }
      }
      assert(signedIn, 'login completed before navigating')
    } else {
      assert(phase === 'signed-in', 'profile session already signed in')
    }
    untargeted(session, ['navigate', `${BASE}/system-settings/site/nav-apps`])
    const ready = bsk([
      'wait-for-element', '#nav-app-group-title',
      '--state', 'attached', '--timeout', '20s', '--session', session,
    ])
    if (ready.satisfied !== true) {
      const probe = sync(session)
      console.log('  DEBUG ready:', JSON.stringify(ready).slice(0, 500))
      console.log('  DEBUG page:', textOf(probe).slice(0, 700))
    }
    assert(ready.satisfied === true, 'settings form finished loading')
    receipt = sync(session)
    text = textOf(receipt)

    const before = groupOrderLine(text)
    assert(before.includes(GROUP_LABEL), 'sidebar order list is rendered')
    const refs = handleRefs(text)
    assert(refs.length >= 2, 'drag handles are visible')
    const groupHandle = refFor(text, `拖动 ${GROUP_LABEL} 以重新排序`)
    const lastHandle = refs[refs.length - 1]
    console.log(`  before: ${before}`)

    // One gesture towards the opposite end. Two drags in one run would need
    // fresh VOM refs after the first one, and the rebuilt endpoint can fall
    // outside the visible viewport; flipping once is both simpler and enough
    // to prove that a real press-move-release was accepted.
    const wasLast = before.trim().endsWith(`${GROUP_LABEL} 默认分组"`)
    const target = wasLast ? refs[0] : refs[refs.length - 1]
    const expectedIndex = wasLast ? 0 : -1

    bsk([
      'drag', groupHandle, '--to', target,
      '--session', session, '--context', receipt.context_id,
      '--hold', '200ms', '--duration', '1200ms',
    ])
    receipt = sync(session)
    text = textOf(receipt)
    const after = groupOrderLine(text)
    console.log(`  after:  ${after}`)
    assert(Boolean(after) && after !== before,
      'real mouse drag changed the sidebar order')

    bsk([
      'click', refFor(text, 'button "保存导航入口"'),
      '--session', session, '--context', receipt.context_id,
    ])
    receipt = sync(session)

    // Saving is an async PUT. Verify it landed in the backend before reloading,
    // otherwise a fast reload can cancel the request and produce a false
    // "order not persisted" failure.
    const token = await loginToken()
    assert(Boolean(token), 'admin login succeeds')
    let persisted = false
    let stored = null
    for (let attempt = 0; attempt < 12; attempt += 1) {
      await new Promise((resolve) => setTimeout(resolve, 500))
      stored = await storedConfiguration(token)
      const order = stored.group_order ?? []
      const actual = order.indexOf('apiduo-default')
      const wanted = expectedIndex === -1 ? order.length - 1 : expectedIndex
      if (actual >= 0 && actual === wanted) {
        persisted = true
        break
      }
    }
    assert(persisted, 'backend persisted the reordered group list')

    untargeted(session, ['reload'])
    let reloaded = ''
    for (let attempt = 0; attempt < 16; attempt += 1) {
      await new Promise((resolve) => setTimeout(resolve, 500))
      receipt = sync(session)
      const current = textOf(receipt)
      reloaded = groupOrderLine(current)
      if (reloaded && !current.includes('加载中')) break
    }
    if (reloaded !== after) {
      console.log(`  DEBUG reloaded: ${reloaded} | expected: ${after}`)
    }
    assert(reloaded === after, 'order survives a full page reload')

    const custom = (stored.data ?? []).find((app) => app.icon_url)
    assert(Boolean(custom) && custom.icon_url.startsWith('https://') && Boolean(custom.icon),
      'preset and custom https icon URL coexist in stored data')

    untargeted(session, [
      'screenshot', '--out', path.join(OUT_DIR, 'bsk-regression.png'),
    ])
    console.log(`  screenshot ${path.join(OUT_DIR, 'bsk-regression.png')}`)
    console.log('RESULT: PASS')
  } finally {
    bsk(['session', 'stop', session])
    console.log(`  session ${session} stopped`)
  }
}

main().catch((error) => {
  console.error(`RESULT: FAIL — ${error.message}`)
  process.exitCode = 1
})
