// 验证：上游报错时不产生消费；正常请求仍正常结算（双向回归）
const puppeteer = require('/Users/betterme/PycharmProjects/AI/apiduo/web/node_modules/puppeteer-core')
const BASE = 'http://127.0.0.1:3901'
const wait = (ms) => new Promise((r) => setTimeout(r, ms))
const results = []
const record = (n, ok, d = '') => {
  results.push({ n, ok: !!ok, d: String(d).slice(0, 200) })
  console.log((ok ? 'PASS' : 'FAIL') + ' | ' + n + ' | ' + String(d).slice(0, 160))
}

;(async () => {
  const browser = await puppeteer.launch({
    executablePath: '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
    headless: 'new',
    args: ['--no-sandbox', '--no-proxy-server', '--hide-scrollbars'],
  })
  const page = await browser.newPage()
  await page.setViewport({ width: 1440, height: 900 })
  const errors = []
  page.on('pageerror', (e) => errors.push(e.message.slice(0, 120)))

  await page.goto(BASE + '/login', { waitUntil: 'networkidle2' })
  await page.evaluate(async () => {
    await fetch('/api/user/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username: 'root', password: 'Apiduo12345' }),
      credentials: 'include',
    })
  })
  await page.goto(BASE + '/playground', { waitUntil: 'networkidle2' })
  await page.evaluate(() => localStorage.clear())
  await page.reload({ waitUntil: 'networkidle2' })
  await wait(2800)
  await page.keyboard.press('Escape')

  const chips = () =>
    page.evaluate(
      () =>
        Array.from(document.querySelectorAll('button[aria-expanded]')).filter((n) =>
          /\$|¥/.test(n.textContent || '')
        ).length
    )
  const spend = () =>
    page.evaluate(() => {
      const t = document.body.innerText.replace(/\s+/g, ' ')
      const m = t.match(/本次会话\s*([^\s·]+)/)
      return m ? m[1] : '?'
    })

  // ① 制造失败
  await page.click('textarea')
  await page.type('textarea', '[fail] 失败不该计费')
  await page.keyboard.press('Enter')
  await wait(9000)

  const failState = await page.evaluate(() => ({
    hasError: /错误|Request error|error occurred/i.test(document.body.innerText),
    text: document.body.innerText.replace(/\s+/g, ' ').slice(-260),
  }))
  const failChips = await chips()
  const failSpend = await spend()

  record('F1 失败请求产生了错误提示', failState.hasError, failState.text.slice(-140))
  record('F2 失败消息没有金额标签', failChips === 0, 'chips=' + failChips)
  record('F3 会话累计仍为「—」', failSpend === '—' || failSpend === '?', 'spend=' + failSpend)

  // ② 正常请求仍然结算
  await page.click('textarea')
  await page.type('textarea', '正常一条')
  await page.keyboard.press('Enter')
  await wait(4500)

  const okChips = await chips()
  const okSpend = await spend()
  record('F4 正常请求仍产生金额标签', okChips === 1, 'chips=' + okChips)
  record('F5 正常请求计入会话累计', okSpend !== '—' && /\$/.test(okSpend), 'spend=' + okSpend)
  record('F6 无未捕获 JS 错误', errors.length === 0, errors.slice(0, 2).join(' | '))

  await page.screenshot({ path: '/tmp/apiduo-e2e/fail-billing.png' })
  await browser.close()
  console.log('\n===== ' + results.filter((r) => r.ok).length + '/' + results.length + ' passed =====')
})()
