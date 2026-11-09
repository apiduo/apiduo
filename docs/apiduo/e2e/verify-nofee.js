// 线上验证核心修复：失败请求不产生消费（测试账号 quota=0，必然走失败路径）
const puppeteer = require('/Users/betterme/PycharmProjects/AI/apiduo/web/node_modules/puppeteer-core')
const fs = require('fs')
const BASE = 'https://apiduo.cn'
const wait = (ms) => new Promise((r) => setTimeout(r, ms))
const acc = JSON.parse(fs.readFileSync('/tmp/apd-live-account.json', 'utf8'))
const results = []
const record = (n, ok, d = '') => {
  results.push({ n, ok: !!ok, d: String(d).slice(0, 260) })
  console.log((ok ? 'PASS' : 'FAIL') + ' | ' + n + ' | ' + String(d).slice(0, 180))
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
  page.on('pageerror', (e) => errors.push(e.message.slice(0, 140)))

  await page.goto(BASE + '/playground', { waitUntil: 'domcontentloaded', timeout: 180000 })
  await page.waitForSelector('[role="tab"]', { timeout: 120000 }).catch(() => {})
  await wait(2500)
  await page.keyboard.press('Escape')
  await wait(500)

  const before = await page.evaluate(() => {
    const t = document.body.innerText.replace(/\s+/g, ' ')
    const m = t.match(/本次会话\s*([^\s·]+)/)
    return { spend: m ? m[1] : '?', chips: document.querySelectorAll('button[aria-expanded]').length }
  })
  record('N1 发送前会话累计为空', before.spend === '—' || before.spend === '?', JSON.stringify(before))

  await page.click('textarea')
  await page.type('textarea', '验证失败不计费')
  await page.keyboard.press('Enter')
  await wait(9000)

  const after = await page.evaluate(() => {
    const t = document.body.innerText.replace(/\s+/g, ' ')
    const m = t.match(/本次会话\s*([^\s·]+)/)
    const moneyChips = Array.from(document.querySelectorAll('button[aria-expanded]')).filter((n) =>
      /[$¥]/.test(n.textContent || '')
    )
    return {
      spend: m ? m[1] : '?',
      moneyChips: moneyChips.map((n) => n.textContent.trim()),
      allExpanded: document.querySelectorAll('button[aria-expanded]').length,
      errHint: (t.match(/额度|quota|Insufficient|无可用渠道|错误/g) || []).slice(0, 3),
      tail: t.slice(-180),
    }
  })
  console.log('after', JSON.stringify(after))

  record('N2 请求确实失败了（有错误提示或错误态）', after.errHint.length > 0, after.errHint.join('/'))
  record('N3 失败消息没有金额标签', after.moneyChips.length === 0, 'chips=' + JSON.stringify(after.moneyChips))
  record('N4 会话累计仍为空（未计入失败请求）', after.spend === '—' || after.spend === '0', 'spend=' + after.spend)
  record('N5 无未捕获 JS 错误', errors.length === 0, errors.slice(0, 2).join(' | '))

  await page.screenshot({ path: '/tmp/apiduo-e2e/prod-023-nofee.png' })
  await browser.close()
  console.log('\n===== ' + results.filter((r) => r.ok).length + '/' + results.length + ' passed =====')
})()
