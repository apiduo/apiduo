// 0.0.23 上线核验：页面能否加载、分包是否为新的、动效内核是否仍在
const puppeteer = require('/Users/betterme/PycharmProjects/AI/apiduo/web/node_modules/puppeteer-core')
const fs = require('fs')
const BASE = 'https://apiduo.cn'
const wait = (ms) => new Promise((r) => setTimeout(r, ms))
const acc = JSON.parse(fs.readFileSync('/tmp/apd-live-account.json', 'utf8'))
const results = []
const record = (n, ok, d = '') => {
  results.push({ n, ok: !!ok, d: String(d).slice(0, 240) })
  console.log((ok ? 'PASS' : 'FAIL') + ' | ' + n + ' | ' + String(d).slice(0, 170))
}

;(async () => {
  const browser = await puppeteer.launch({
    executablePath: '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
    headless: 'new',
    userDataDir: '/tmp/apd-chrome-profile',
    args: ['--no-sandbox', '--no-proxy-server', '--hide-scrollbars'],
  })
  const page = await browser.newPage()
  await page.setViewport({ width: 1440, height: 900 })
  const errors = []
  const jsFiles = []
  page.on('pageerror', (e) => errors.push(e.message.slice(0, 140)))
  page.on('response', (r) => {
    const u = r.url()
    if (/\/static\/js\/(async\/)?[a-z0-9]+\.[a-z0-9]+\.js$/.test(u)) {
      jsFiles.push((u.split('/').pop() || '').slice(0, 40))
    }
  })

  await page.goto(BASE + '/playground', { waitUntil: 'domcontentloaded', timeout: 180000 })
  let ok = true
  try {
    await page.waitForSelector('[role="tab"]', { timeout: 120000 })
  } catch {
    ok = false
  }
  await wait(2000)

  let path = await page.evaluate(() => location.pathname)
  if (path === '/sign-in' && ok === false) {
    // 登录态过期，走表单重登
    const u = await page.$('input[type="text"], input:not([type])')
    const p = await page.$('input[type="password"]')
    if (u && p) {
      await u.type(acc.username, { delay: 10 })
      await p.type(acc.password, { delay: 10 })
      await page.evaluate(() => {
        const b = Array.from(document.querySelectorAll('button')).find((x) => /登录|Sign in|Login/i.test(x.textContent || ''))
        if (b) b.click()
      })
      await wait(6000)
      await page.goto(BASE + '/playground', { waitUntil: 'domcontentloaded', timeout: 180000 })
      await page.waitForSelector('[role="tab"]', { timeout: 120000 }).catch(() => {})
      await wait(2000)
      path = await page.evaluate(() => location.pathname)
    }
  }

  const state = await page.evaluate(() => {
    const root = getComputedStyle(document.documentElement)
    const pill = document.querySelector('.apd-tab-pill')
    return {
      path: location.pathname,
      tabs: Array.from(document.querySelectorAll('[role="tab"]')).map((t) => t.textContent.trim()),
      hasPill: !!pill,
      hasTrack: !!document.querySelector('.apd-progress-track, .apd-indeterminate-track'),
      settle: root.getPropertyValue('--apd-ease-settle').trim().slice(0, 30),
      ink: root.getPropertyValue('--apiduo-ink').trim().slice(0, 40),
      marks: document.querySelectorAll('[data-apiduo]').length,
      textHead: document.body.innerText.replace(/\s+/g, ' ').slice(0, 70),
    }
  })

  record('V1 生产 /playground 正常渲染', state.path === '/playground' && state.tabs.length === 3, JSON.stringify(state.tabs) + ' | ' + state.textHead)
  record('V2 tab 指示器在线', state.hasPill === true, 'pill=' + state.hasPill)
  record('V3 弹簧 token 在线', /linear\(/.test(state.settle), state.settle)
  record('V4 --apiduo-ink 已解析', state.ink.length > 3, state.ink)
  record('V5 界面标识在线', state.marks >= 2, 'data-apiduo x' + state.marks)
  record('V6 无未捕获 JS 错误', errors.length === 0, errors.slice(0, 2).join(' | '))

  await page.screenshot({ path: '/tmp/apiduo-e2e/prod-023.png' })
  console.log('JS 加载数:', jsFiles.length)
  console.log('JS 样例:', JSON.stringify(jsFiles.slice(0, 6)))
  await browser.close()
  console.log('\n===== ' + results.filter((r) => r.ok).length + '/' + results.length + ' passed =====')
})()
