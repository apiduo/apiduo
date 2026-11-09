// 生图失败态：不该再有"生成中"转圈
const puppeteer = require('/Users/betterme/PycharmProjects/AI/apiduo/web/node_modules/puppeteer-core')
const BASE = 'http://127.0.0.1:3901'
const wait = (ms) => new Promise((r) => setTimeout(r, ms))
const results = []
const record = (n, ok, d = '') => {
  results.push({ n, ok: !!ok, d: String(d).slice(0, 200) })
  console.log((ok ? 'PASS' : 'FAIL') + ' | ' + n + ' | ' + String(d).slice(0, 150))
}

;(async () => {
  const browser = await puppeteer.launch({
    executablePath: '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
    headless: 'new',
    args: ['--no-sandbox', '--no-proxy-server', '--hide-scrollbars'],
  })
  const page = await browser.newPage()
  await page.setViewport({ width: 1440, height: 900 })
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
  await page.evaluate(() => {
    localStorage.clear()
    localStorage.setItem('apiduo_playground_mode', 'image')
  })
  await page.reload({ waitUntil: 'networkidle2' })
  await wait(3000)
  await page.keyboard.press('Escape')

  await page.click('textarea')
  await page.type('textarea', '[fail] 生图失败')
  await page.keyboard.press('Enter')
  await wait(6000)

  const fail = await page.evaluate(() => ({
    text: document.body.innerText.replace(/\s+/g, ' ').slice(-200),
    generating: /生成中|Generating/.test(document.body.innerText),
    spinners: document.querySelectorAll('svg.animate-spin').length,
    chips: Array.from(document.querySelectorAll('button[aria-expanded]')).filter((n) => /\$|¥/.test(n.textContent || '')).length,
  }))
  record('I1 生图失败显示错误', /错误|error|exploded/i.test(fail.text), fail.text.slice(-110))
  record('I2 失败后不再显示「生成中」', fail.generating === false, 'generating=' + fail.generating)
  record('I3 失败后没有残留转圈', fail.spinners === 0, 'spinners=' + fail.spinners)
  record('I4 失败不产生金额标签', fail.chips === 0, 'chips=' + fail.chips)

  await page.screenshot({ path: '/tmp/apiduo-e2e/image-fail.png' })
  await browser.close()
  console.log('\n===== ' + results.filter((r) => r.ok).length + '/' + results.length + ' passed =====')
})()
