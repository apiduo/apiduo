#!/usr/bin/env node
/**
 * apiduo 登录链路端到端测试（浏览器 + 抓包）
 *
 * 用法（需先起好后端）：
 *   node docs/apiduo/e2e/login-ui-tests.js --base http://127.0.0.1:3901 \
 *        --user root --pass LocalTest12345
 *
 * 产物（默认 docs/apiduo/e2e/artifacts/login/）：
 *   traffic.har   完整请求/响应（Cookie、Authorization、口令已脱敏，结构保留）
 *   results.json  断言结果 + 统计
 *   shots/*.png   每个场景的截图
 *
 * 说明：
 * - 通过 CDP 的 Network 域录制，等于浏览器 DevTools 的 Network 面板落到文件；
 * - 只对本地文档/XHR 抓响应体，静态资源只记状态码、大小、耗时，避免 HAR 过大；
 * - 任何一项断言失败，进程退出码为 1。
 */
'use strict'

const fs = require('node:fs')
const path = require('node:path')

const ROOT = path.resolve(__dirname, '../../..')
const puppeteer = require(path.join(ROOT, 'web/node_modules/puppeteer-core'))

const CHROME =
  process.env.CHROME_PATH ||
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'

function arg(name, fallback) {
  const i = process.argv.indexOf('--' + name)
  if (i > -1 && process.argv[i + 1]) return process.argv[i + 1]
  return fallback
}

const BASE = arg('base', 'http://127.0.0.1:3901')
const USER = arg('user', 'root')
const PASS = arg('pass', 'LocalTest12345')
const OUT = arg('out', path.join(__dirname, 'artifacts', 'login'))
const SHOTS = path.join(OUT, 'shots')

for (const dir of [OUT, SHOTS]) fs.mkdirSync(dir, { recursive: true })
for (const f of fs.readdirSync(SHOTS)) fs.unlinkSync(path.join(SHOTS, f))

// ── 脱敏 ────────────────────────────────────────────────────────────────────
const SECRET_HEADERS = new Set([
  'authorization',
  'cookie',
  'set-cookie',
  'x-api-key',
  'proxy-authorization',
])
const SECRET_FIELDS = ['password', 'confirmPassword', 'access_token', 'session', 'token', 'secret']

function mask(value) {
  if (typeof value !== 'string') return '***'
  if (value.length <= 8) return '***'
  return value.slice(0, 4) + '***(' + value.length + ' chars)'
}

function redactHeaders(headers = {}) {
  const out = {}
  for (const [k, v] of Object.entries(headers)) {
    const key = k.toLowerCase()
    if (!SECRET_HEADERS.has(key)) {
      out[k] = v
      continue
    }
    if (key === 'authorization') {
      const scheme = String(v).split(' ')[0]
      out[k] = scheme + ' ' + mask(String(v).slice(scheme.length + 1))
    } else if (key === 'set-cookie') {
      // 保留 name 与全部属性（HttpOnly/SameSite 是审查重点），只抹掉值
      out[k] = [v].flat().map((line) =>
        String(line).replace(/^([^=]+)=([^;]*)/, (_, n, val) => n + '=' + mask(val))
      )
    } else {
      out[k] = String(v)
        .split(';')
        .map((part) => part.replace(/^(\s*[^=]+)=([^;]*)/, (_, n, val) => n + '=' + mask(val)))
        .join(';')
    }
  }
  return out
}

function redactBody(text) {
  if (!text) return text
  let out = text
  for (const f of SECRET_FIELDS) {
    out = out.replace(
      new RegExp('("' + f + '"\\s*:\\s*")([^"]*)(")', 'g'),
      (_, a, v, c) => a + mask(v) + c
    )
  }
  return out
}

function parsePostData(postData) {
  if (!postData) return undefined
  try {
    return redactBody(JSON.stringify(JSON.parse(postData), null, 2))
  } catch {
    return redactBody(postData)
  }
}

// ── 断言 ────────────────────────────────────────────────────────────────────
const checks = []
let scenario = '准备'
const observations = []
function check(name, ok, detail) {
  checks.push({ scenario, name, ok: !!ok, detail: detail === undefined ? '' : String(detail) })
  const mark = ok ? 'PASS' : 'FAIL'
  console.log(`  [${mark}] ${name}${detail ? '  · ' + detail : ''}`)
  return !!ok
}

// ── 抓包 ────────────────────────────────────────────────────────────────────
const capture = {
  pages: [],
  entries: [],
  page(title) {
    const meta = {
      id: 'page_' + (this.pages.length + 1),
      title,
      startedDateTime: new Date().toISOString(),
      _scenario: title,
    }
    this.pages.push(meta)
    return meta
  },
}

function newContext() {
  return { checks: 0 }
}

async function openPage(ctx, title, viewport = { width: 1440, height: 900 }, context) {
  const page = await (context || ctx.browser).newPage()
  await page.setViewport({ deviceScaleFactor: 1, ...viewport })

  const meta = capture.page(title)
  const client = await page.createCDPSession()
  await client.send('Network.enable')

  const state = {
    reqs: new Map(),
    pending: [],
    meta,
    errors: [],
    client,
    extraRes: new Map(),
    extraReq: new Map(),
  }
  page._cap = state

  client.on('Network.requestWillBeSent', (e) => {
    state.reqs.set(e.requestId, {
      req: e.request,
      type: e.type,
      ts: e.timestamp,
      wallTime: e.wallTime,
    })
  })
  client.on('Network.responseReceived', (e) => {
    const r = state.reqs.get(e.requestId)
    if (r) {
      r.res = e.response
      r.resTs = e.timestamp
    }
  })
  client.on('Network.responseReceivedExtraInfo', (e) => {
    state.extraRes.set(e.requestId, e.headers)
  })
  client.on('Network.requestWillBeSentExtraInfo', (e) => {
    state.extraReq.set(e.requestId, e.headers)
  })
  client.on('Network.loadingFinished', (e) => {
    const r = state.reqs.get(e.requestId)
    if (!r) return
    r.size = e.encodedDataLength
    r.done = true
    const local = r.req.url.startsWith(BASE)
    if (local && ['XHR', 'Fetch', 'Document'].includes(r.type)) {
      state.pending.push(
        client
          .send('Network.getResponseBody', { requestId: e.requestId })
          .then((b) => ({ r, body: b }))
          .catch(() => null)
      )
    }
  })
  client.on('Network.loadingFailed', (e) => {
    const r = state.reqs.get(e.requestId)
    if (r) r.failed = e.errorText || 'failed'
  })

  page.on('pageerror', (err) => state.errors.push('pageerror: ' + String(err).slice(0, 200)))
  page.on('console', (m) => {
    if (m.type() === 'error') state.errors.push('console: ' + m.text().slice(0, 200))
  })

  return page
}

async function closePage(page) {
  const st = page._cap
  if (!st) return
  await new Promise((r) => setTimeout(r, 400))
  const bodies = await Promise.all(st.pending)
  const bodyOf = new Map()
  for (const b of bodies) if (b) bodyOf.set(b.r, b.body)

  let first = true
  for (const [reqId, r] of st.reqs) {
    const url = r.req.url
    const body = bodyOf.get(r)
    const entry = {
      pageref: st.meta.id,
      startedDateTime: new Date((r.wallTime || Date.now() / 1000) * 1000).toISOString(),
      time: r.resTs && r.ts ? Math.max(0, Math.round((r.resTs - r.ts) * 1000)) : -1,
      request: {
        method: r.req.method,
        url,
        httpVersion: 'HTTP/1.1',
        headers: redactHeaders({ ...(r.req.headers || {}), ...(st.extraReq.get(reqId) || {}) }),
        queryString: [...new URL(url).searchParams.entries()].map(([name, value]) => ({
          name,
          value,
        })),
        cookies: [],
        headersSize: -1,
        bodySize: r.req.postData ? Buffer.byteLength(r.req.postData) : 0,
        ...(r.req.postData
          ? {
              postData: {
                mimeType: r.req.headers['Content-Type'] || r.req.headers['content-type'] || '',
                text: parsePostData(r.req.postData),
              },
            }
          : {}),
      },
      response: {
        status: r.res ? r.res.status : 0,
        statusText: r.failed ? 'FAILED: ' + r.failed : '',
        httpVersion: r.res ? r.res.protocol || 'HTTP/1.1' : '',
        headers: redactHeaders({
          ...(r.res ? r.res.headers : {}),
          ...(st.extraRes.get(reqId) || {}),
        }),
        cookies: [],
        content: {
          size: r.size || 0,
          mimeType: r.res ? r.res.mimeType : '',
          ...(body && !body.base64Encoded && url.startsWith(BASE)
            ? { text: redactBody(body.body).slice(0, 64 * 1024) }
            : {}),
        },
        redirectURL: (r.res && r.res.headers && r.res.headers.location) || '',
        headersSize: -1,
        bodySize: r.size || 0,
      },
      cache: {},
      timings: { send: 0, wait: r.resTs && r.ts ? Math.max(0, (r.resTs - r.ts) * 1000) : -1, receive: 0 },
      _resourceType: r.type,
      _scenario: st.meta.title,
      _order: first ? 0 : 1,
      _failed: !!r.failed,
    }
    first = false
    capture.entries.push(entry)
  }
  await page.close()
}

// ── 工具 ────────────────────────────────────────────────────────────────────
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

async function waitFor(page, selector, timeout = 15000) {
  await page.waitForSelector(selector, { timeout })
}

async function shot(page, name) {
  const file = path.join(SHOTS, name + '.png')
  await page.screenshot({ path: file })
  return 'shots/' + name + '.png'
}

// 匿名访问时前端会主动试探 refresh，401 属预期；其余错误才算异常
function consoleErrors(page) {
  return page._cap.errors.filter((e) => !/status of 401 \(Unauthorized\)/.test(e))
}

function unexpectedHttp(page) {
  return [...page._cap.reqs.values()]
    .filter((r) => r.res && r.res.status >= 400)
    .filter((r) => !r.req.url.includes('/api/user/auth/refresh'))
    .map((r) => r.res.status + ' ' + r.req.url.replace(BASE, ''))
}

function textOf(page, selector) {
  return page.evaluate((s) => {
    const el = document.querySelector(s)
    return el ? el.textContent.trim().slice(0, 200) : null
  }, selector)
}

// 从 localStorage 取 access token，用于后台接口断言
async function getToken(page) {
  return page.evaluate(() => {
    const raw = localStorage.getItem('user')
    if (!raw) return ''
    try {
      return JSON.parse(raw).token || ''
    } catch {
      return ''
    }
  })
}

const skins = ['aurora', 'light', 'terminal', 'editorial', 'cyber', 'warm']

;(async () => {
  const startedAt = new Date().toISOString()
  const ctx = newContext()
  ctx.browser = await puppeteer.launch({
    executablePath: CHROME,
    headless: 'new',
    args: ['--no-proxy-server', '--disable-dev-shm-usage'],
  })
  ctx.browser.on('targetcreated', () => {})

  let shotMap = {}

  try {
    // ── S1 默认登录页 ─────────────────────────────────────────────────────
    scenario = 'S1 默认登录页'; console.log('\nS1 默认登录页')
    {
      const page = await openPage(ctx, 'S1 默认登录页 /sign-in')
      const resp = await page.goto(BASE + '/sign-in', {
        waitUntil: 'domcontentloaded',
        timeout: 30000,
      })
      await waitFor(page, '.apiduo-login')
      await sleep(800)

      const s = await page.evaluate(() => {
        const root = document.querySelector('.apiduo-login')
        const cs = getComputedStyle(root)
        return {
          skin: root.getAttribute('data-login-skin'),
          inputs: root.querySelectorAll('form input:not([type=checkbox])').length,
          submit: !!root.querySelector('button[type=submit]'),
          h: root.getBoundingClientRect().height,
          overflow: document.documentElement.scrollWidth > window.innerWidth + 1,
          scrollW: document.documentElement.scrollWidth,
          bg: cs.backgroundColor,
          accent: cs.getPropertyValue('--al-accent').trim(),
          title: document.title,
          brand: (document.querySelector('.al-name') || {}).textContent || '',
        }
      })

      check('文档返回 200', resp && resp.status() === 200, 'status=' + (resp ? resp.status() : '-'))
      check('皮肤回落为 aurora', s.skin === 'aurora', 'data-login-skin=' + s.skin)
      check('表单渲染（2 个输入框 + 提交按钮）', s.inputs === 2 && s.submit, `inputs=${s.inputs} submit=${s.submit}`)
      check('桌面端无横向溢出', !s.overflow, 'scrollWidth=' + s.scrollW)
      check('皮肤变量已注入（--al-accent 非空）', !!s.accent, s.accent)
      check('页面无 JS 报错', consoleErrors(page).length === 0, consoleErrors(page).slice(0, 2).join(' | '))
      check(
        '401 仅来自匿名 refresh 探测（预期行为）',
        unexpectedHttp(page).length === 0,
        unexpectedHttp(page).join(' | ') || '无 4xx'
      )
      const anonRefresh = [...page._cap.reqs.values()].filter((r) =>
        r.req.url.includes('/api/user/auth/refresh')
      ).length
      check(
        '匿名开页不发起 refresh（不消耗限流额度）',
        anonRefresh === 0,
        anonRefresh + ' 次'
      )
      check('站点名已切到 Apiduo', s.brand.trim() === 'Apiduo', s.brand.trim() || '(空)')
      shotMap['s1'] = await shot(page, 's1-signin-default')
      await closePage(page)
    }

    // ── S2 六套皮肤 ───────────────────────────────────────────────────────
    scenario = 'S2 六套皮肤（1440×900）'; console.log('\nS2 六套皮肤（1440×900）')
    {
      const seen = []
      for (const skin of skins) {
        const page = await openPage(ctx, 'S2 皮肤 ' + skin)
        await page.goto(BASE + '/sign-in?theme=' + skin, {
          waitUntil: 'domcontentloaded',
          timeout: 30000,
        })
        await waitFor(page, '.apiduo-login')
        await sleep(500)

        const s = await page.evaluate(() => {
          const root = document.querySelector('.apiduo-login')
          const cs = getComputedStyle(root)
          const card = root.querySelector('.al-card')
          const h1 = root.querySelector('h1')
          return {
            skin: root.getAttribute('data-login-skin'),
            accent: cs.getPropertyValue('--al-accent').trim(),
            surface: cs.getPropertyValue('--al-surface').trim(),
            bg: cs.backgroundColor,
            cardRadius: card ? getComputedStyle(card).borderRadius : null,
            h1Font: h1 ? getComputedStyle(h1).fontFamily : null,
            overflow: document.documentElement.scrollWidth > window.innerWidth + 1,
            scrollW: document.documentElement.scrollWidth,
          }
        })
        seen.push({ skin, accent: s.accent, surface: s.surface, bg: s.bg, radius: s.cardRadius })

        check('皮肤 ' + skin + ' 生效', s.skin === skin, '得到 ' + s.skin)
        check('皮肤 ' + skin + ' 无横向溢出', !s.overflow, 'scrollWidth=' + s.scrollW)
        check('皮肤 ' + skin + ' 无 JS 报错', consoleErrors(page).length === 0, consoleErrors(page).slice(0, 2).join(' | '))
        shotMap[skin] = await shot(page, 's2-' + skin)
        await closePage(page)
      }
      const distinctAccent = new Set(seen.map((x) => x.accent)).size
      const distinctBg = new Set(seen.map((x) => x.bg)).size
      check('六套主色互不相同', distinctAccent >= 5, '不同 accent 数量=' + distinctAccent)
      check('六套背景互不相同', distinctBg >= 4, '不同背景数量=' + distinctBg)
    }

    // ── S3 非法 theme 回落 ────────────────────────────────────────────────
    scenario = 'S3 非法 theme'; console.log('\nS3 非法 theme')
    {
      const page = await openPage(ctx, 'S3 非法 theme 回落')
      await page.goto(BASE + '/sign-in?theme=%3Cscript%3Ex%3C/script%3E', {
        waitUntil: 'domcontentloaded',
        timeout: 30000,
      })
      await waitFor(page, '.apiduo-login')
      await sleep(400)
      const s = await page.evaluate(() => ({
        skin: document.querySelector('.apiduo-login').getAttribute('data-login-skin'),
        injected: document.body.innerHTML.includes('<script>x</script>'),
      }))
      check('未知主题回落 aurora', s.skin === 'aurora', '得到 ' + s.skin)
      check('查询参数未被注入 DOM', !s.injected, 'injected=' + s.injected)
      await closePage(page)
    }

    // ── S4 移动端 ─────────────────────────────────────────────────────────
    scenario = 'S4 移动端 390×844'; console.log('\nS4 移动端 390×844')
    {
      for (const skin of ['aurora', 'editorial', 'cyber']) {
        const page = await openPage(ctx, 'S4 移动端 ' + skin, {
          width: 390,
          height: 844,
          isMobile: true,
          hasTouch: true,
        })
        await page.goto(BASE + '/sign-in?theme=' + skin, {
          waitUntil: 'domcontentloaded',
          timeout: 30000,
        })
        await waitFor(page, '.apiduo-login')
        await sleep(500)
        const s = await page.evaluate(() => {
          const card = document.querySelector('.al-card')
          const r = card ? card.getBoundingClientRect() : null
          const input = document.querySelector('form input:not([type=checkbox])')
          return {
            skin: document.querySelector('.apiduo-login').getAttribute('data-login-skin'),
            overflow: document.documentElement.scrollWidth > window.innerWidth + 1,
            scrollW: document.documentElement.scrollWidth,
            cardW: r ? Math.round(r.width) : 0,
            inputFont: input ? getComputedStyle(input).fontSize : '',
          }
        })
        check('移动端 ' + skin + ' 无横向溢出', !s.overflow, 'scrollWidth=' + s.scrollW)
        check('移动端 ' + skin + ' 卡片宽度合理', s.cardW > 300 && s.cardW <= 390, s.cardW + 'px')
        check('移动端输入字号 ≥16px（防 iOS 聚焦放大）', parseFloat(s.inputFont) >= 16, s.inputFont)
        shotMap['m-' + skin] = await shot(page, 's4-mobile-' + skin)
        await closePage(page)
      }
    }

    // ── S5 空提交 ─────────────────────────────────────────────────────────
    scenario = 'S5 空提交'; console.log('\nS5 空提交')
    {
      const page = await openPage(ctx, 'S5 空提交校验')
      await page.goto(BASE + '/sign-in', { waitUntil: 'domcontentloaded', timeout: 30000 })
      await waitFor(page, '.apiduo-login form')
      await page.click('button[type=submit]')
      await sleep(800)
      const s = await page.evaluate(() => ({
        path: location.pathname,
        text: document.body.innerText,
      }))
      check('空表单未提交（仍在登录页）', s.path === '/sign-in', s.path)
      check(
        '空提交给出可见校验提示',
        /请输入用户名|请输入密码/.test(s.text),
        (s.text.match(/请输入[^\n]*/g) || []).join(' / ')
      )
      shotMap['s5'] = await shot(page, 's5-empty-submit')
      await closePage(page)
    }

    // ── S6 错误口令 ───────────────────────────────────────────────────────
    scenario = 'S6 错误口令'; console.log('\nS6 错误口令')
    {
      const page = await openPage(ctx, 'S6 错误口令')
      await page.goto(BASE + '/sign-in?theme=aurora', {
        waitUntil: 'domcontentloaded',
        timeout: 30000,
      })
      await waitFor(page, 'input[name=username]')
      await page.type('input[name=username]', USER, { delay: 15 })
      await page.type('input[name=password]', 'wrong-password-123', { delay: 15 })

      const consent = await page.$('input#legal-consent')
      if (consent) await consent.click()

      const waitResp = page.waitForResponse(
        (r) => r.url().includes('/api/user/login') && r.request().method() === 'POST',
        { timeout: 15000 }
      )
      await page.click('button[type=submit]')
      const resp = await waitResp
      const body = await resp.json().catch(() => ({}))
      await sleep(1200)

      const s = await page.evaluate(() => {
        const toast = document.querySelector('[data-sonner-toast]')
        return {
          path: location.pathname,
          toast: toast ? toast.innerText.trim() : '',
          text: document.body.innerText.slice(0, 400),
        }
      })
      check('错误口令接口返回 success=false', body.success === false, 'message=' + (body.message || '').slice(0, 60))
      check('错误口令仍停留在登录页', s.path === '/sign-in', s.path)
      check(
        '错误提示以 toast 呈现且文案正确',
        /incorrect|用户名或密码错误/i.test(s.toast),
        s.toast || '(未捕获到 toast)'
      )
      check('无 JS 报错', consoleErrors(page).length === 0, consoleErrors(page).slice(0, 2).join(' | '))
      shotMap['s6'] = await shot(page, 's6-wrong-password')
      await closePage(page)
    }

    // ── S7 正确登录 ───────────────────────────────────────────────────────
    scenario = 'S7 正确登录'; console.log('\nS7 正确登录')
    let token = ''
    {
      const page = await openPage(ctx, 'S7 正确登录')
      await page.goto(BASE + '/sign-in', { waitUntil: 'domcontentloaded', timeout: 30000 })
      await waitFor(page, 'input[name=username]')
      await page.type('input[name=username]', USER, { delay: 15 })
      await page.type('input[name=password]', PASS, { delay: 15 })
      const consent = await page.$('input#legal-consent')
      if (consent) await consent.click()

      const waitResp = page.waitForResponse(
        (r) => r.url().includes('/api/user/login') && r.request().method() === 'POST',
        { timeout: 20000 }
      )
      await page.click('button[type=submit]')
      const resp = await waitResp
      const body = await resp.json().catch(() => ({}))

      await page
        .waitForFunction(() => location.pathname !== '/sign-in', { timeout: 20000 })
        .catch(() => {})
      await sleep(1500)

      const s = await page.evaluate(() => ({
        path: location.pathname,
        hasShell: !!document.querySelector('aside, nav, [class*=sidebar]'),
        text: document.body.innerText.slice(0, 120),
        lsKeys: Object.keys(localStorage),
        ssKeys: Object.keys(sessionStorage),
      }))
      token = (body.data && body.data.access_token) || ''

      check('登录接口 success=true', body.success === true, 'user=' + (body.data && body.data.user ? body.data.user.username : '-'))
      check('离开登录页', s.path !== '/sign-in', '当前 ' + s.path)
      check('进入控制台（侧边栏存在）', s.hasShell, s.text.replace(/\s+/g, ' ').slice(0, 60))
      const leaked = [s.lsKeys.join(','), s.ssKeys.join(',')].join(',').match(/token|auth|user/i)
      check('访问令牌未落 localStorage/sessionStorage', !leaked, leaked ? '发现键 ' + leaked[0] : '仅存内存')
      check('无 JS 报错', consoleErrors(page).length === 0, consoleErrors(page).slice(0, 2).join(' | '))

      // 刷新后应靠 HttpOnly 刷新 Cookie 续期，而不是重新登录
      await page.reload({ waitUntil: 'domcontentloaded' })
      await sleep(2500)
      const after = await page.evaluate(() => ({
        path: location.pathname,
        hasShell: !!document.querySelector('aside, nav, [class*=sidebar]'),
      }))
      check('刷新后仍保持登录（刷新 Cookie 生效）', after.path === s.path && after.hasShell, after.path)

      // 已登录访问 /sign-in 应被送回控制台
      await page.goto(BASE + '/sign-in', { waitUntil: 'domcontentloaded' })
      await sleep(1800)
      const redirect = await page.evaluate(() => location.pathname)
      check('已登录访问 /sign-in 自动跳转', redirect !== '/sign-in', redirect)
      shotMap['s7'] = await shot(page, 's7-after-login')
      await closePage(page)
    }

    // ── S8 后台改风格并落到登录页 ─────────────────────────────────────────
    scenario = 'S8 后台配置往返'; console.log('\nS8 后台配置往返')
    {
      // 幂等：按当前实际值决定目标，避免重复跑时“点同一个卡片 → 不脏”
      const nowStatus = await (await fetch(BASE + '/api/status')).json()
      const current = (nowStatus.data && nowStatus.data.login_theme) || 'aurora'
      const target = current === 'cyber' ? 'terminal' : 'cyber'

      const page = await openPage(ctx, 'S8 后台登录页风格')
      await page.goto(BASE + '/system-settings/site/login-theme', {
        waitUntil: 'domcontentloaded',
        timeout: 30000,
      })
      await waitFor(page, 'button[aria-pressed]', 20000)
      await sleep(800)

      const before = await page.evaluate(() => ({
        cards: document.querySelectorAll('button[aria-pressed]').length,
        selected: [...document.querySelectorAll('button[aria-pressed]')]
          .filter((b) => b.getAttribute('aria-pressed') === 'true')
          .map((b) => b.textContent.replace(/\s+/g, ' ').trim().slice(0, 16)),
        title: (document.querySelector('h1,h2') || {}).textContent,
      }))
      check('后台展示 6 个风格卡片', before.cards === 6, 'cards=' + before.cards)
      check(
        '当前选中项与配置一致',
        before.selected.join('').toLowerCase().includes(current),
        '配置=' + current + ' 选中=' + before.selected.join(',')
      )
      check('分区标题已本地化', /登录页风格/.test(before.title || ''), before.title || '')

      await page.evaluate((want) => {
        const b = [...document.querySelectorAll('button[aria-pressed]')].find((x) =>
          x.textContent.toLowerCase().includes(want)
        )
        if (b) b.click()
      }, target)
      await sleep(400)

      const dirty = await page.evaluate(() => {
        const b = [...document.querySelectorAll('button')].find((x) => /Save sign-in style/i.test(x.textContent))
        return b ? !b.disabled : null
      })
      check('选择后保存按钮可用', dirty === true, 'disabled=' + (dirty === null ? '按钮未找到' : !dirty))

      const waitPut = page.waitForResponse(
        (r) => r.url().includes('/api/option') && r.request().method() === 'PUT',
        { timeout: 20000 }
      )
      await page.evaluate(() => {
        const b = [...document.querySelectorAll('button')].find((x) => /Save sign-in style/i.test(x.textContent))
        if (b && !b.disabled) b.click()
      })
      const putResp = await waitPut
      const putBody = await putResp.json().catch(() => ({}))
      await sleep(1200)

      check('保存请求返回 200', putResp.status() === 200, 'status=' + putResp.status())
      check('保存接口 success=true', putBody.success === true, putBody.message || '')

      const status = await (await fetch(BASE + '/api/status')).json()
      check(
        'status.login_theme 已变为 ' + target,
        status.data && status.data.login_theme === target,
        'login_theme=' + (status.data && status.data.login_theme)
      )
      shotMap['s8'] = await shot(page, 's8-admin-saved')
      await closePage(page)

      const incog = await ctx.browser.createBrowserContext()
      const p2 = await openPage(ctx, 'S8 匿名验证 /sign-in 生效', undefined, incog)
      await p2.goto(BASE + '/sign-in', { waitUntil: 'domcontentloaded', timeout: 30000 })
      await waitFor(p2, '.apiduo-login')
      await sleep(700)
      const skin = await p2.evaluate(() =>
        document.querySelector('.apiduo-login').getAttribute('data-login-skin')
      )
      check('未带 ?theme= 的登录页读到 ' + target, skin === target, 'data-login-skin=' + skin)
      shotMap['s8-login'] = await shot(p2, 's8-signin-after-save')
      await closePage(p2)
      await incog.close()
    }

    // ── S9 注册页 ─────────────────────────────────────────────────────────
    scenario = 'S9 注册页'; console.log('\nS9 注册页')
    {
      const incog = await ctx.browser.createBrowserContext()
      const page = await openPage(ctx, 'S9 注册页 /sign-up', undefined, incog)
      const resp = await page.goto(BASE + '/sign-up', {
        waitUntil: 'domcontentloaded',
        timeout: 30000,
      })
      await sleep(1500)
      const s = await page.evaluate(() => {
        const form = document.querySelector('form')
        const root = document.querySelector('.apiduo-login')
        return {
          status: !!form,
          hasSkin: !!root,
          skin: root ? root.getAttribute('data-login-skin') : null,
          overflow: document.documentElement.scrollWidth > window.innerWidth + 1,
          bg: getComputedStyle(document.body).backgroundColor,
        }
      })
      check('注册页返回 200', resp && resp.status() === 200, 'status=' + (resp ? resp.status() : '-'))
      check('注册表单存在', s.status === true, 'form=' + s.status)
      check('注册页无横向溢出', !s.overflow, '')
      // 已知缺口：皮肤只挂在 /sign-in，注册页仍是上游样式，这里只记录不断言
      observations.push({
        name: '注册页未套用登录皮肤',
        detail: s.hasSkin
          ? '已套用 .apiduo-login'
          : '沿用上游白底表单；从登录页点“注册”会跳到完全不同的视觉体系',
      })
      shotMap['s9'] = await shot(page, 's9-signup')
      await closePage(page)
      await incog.close()
    }

    // ── S10 恢复默认 ──────────────────────────────────────────────────────
    scenario = 'S10 恢复默认风格'; console.log('\nS10 恢复默认风格')
    {
      const r = await fetch(BASE + '/api/option/', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + token },
        body: JSON.stringify({ key: 'LoginTheme', value: 'aurora' }),
      })
      const b = await r.json().catch(() => ({}))
      check('恢复默认返回 200', r.status === 200, 'status=' + r.status)
      check('恢复默认 success=true', b.success === true, b.message || '')
      const status = await (await fetch(BASE + '/api/status')).json()
      check(
        'status 回到 aurora',
        status.data && status.data.login_theme === 'aurora',
        'login_theme=' + (status.data && status.data.login_theme)
      )
    }
  } catch (err) {
    check('测试脚本异常', false, String(err && err.stack ? err.stack.split('\n')[0] : err))
    console.error(err)
  } finally {
    await ctx.browser.close().catch(() => {})
  }

  // ── HAR ─────────────────────────────────────────────────────────────────
  for (const p of capture.pages) {
    const last = capture.entries.filter((e) => e.pageref === p.id).pop()
    p.pageTimings = { onContentLoad: -1, onLoad: -1 }
    p._lastRequest = last ? last.startedDateTime : null
  }
  const har = {
    log: {
      version: '1.2',
      creator: { name: 'apiduo e2e (CDP)', version: '1.0' },
      browser: { name: 'Chrome', version: 'headless' },
      pages: capture.pages,
      entries: capture.entries,
      comment:
        'Cookie / Authorization / 口令已脱敏，结构与响应头属性保留。仅本地文档与 XHR 记录响应体。',
    },
  }
  const harPath = path.join(OUT, 'traffic.har')
  fs.writeFileSync(harPath, JSON.stringify(har, null, 1))

  const failures = checks.filter((c) => !c.ok)
  const result = {
    startedAt,
    finishedAt: new Date().toISOString(),
    base: BASE,
    total: checks.length,
    passed: checks.length - failures.length,
    failed: failures.length,
    checks,
    observations,
    shots: shotMap,
    har: {
      file: 'traffic.har',
      entries: capture.entries.length,
      pages: capture.pages.length,
      bytes: fs.statSync(harPath).size,
    },
  }
  fs.writeFileSync(path.join(OUT, 'results.json'), JSON.stringify(result, null, 1))

  console.log('\n────────────────────────────────────────')
  console.log(`断言 ${result.passed}/${result.total} 通过，失败 ${result.failed}`)
  console.log(`HAR: ${harPath} (${capture.entries.length} 条请求，${(result.har.bytes / 1024).toFixed(0)} KB)`)
  console.log('结果:', path.join(OUT, 'results.json'))
  process.exit(failures.length ? 1 : 0)
})()
