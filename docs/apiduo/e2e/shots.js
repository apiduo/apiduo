// 补拍：深色滑块对比度 + 按次计费的对账单
const puppeteer = require('/Users/betterme/PycharmProjects/AI/apiduo/web/node_modules/puppeteer-core')
const BASE = 'http://127.0.0.1:3901'
const wait = (ms) => new Promise((r) => setTimeout(r, ms))

;(async () => {
  const b = await puppeteer.launch({
    executablePath: '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
    headless: 'new',
    args: ['--no-proxy-server', '--hide-scrollbars'],
  })
  const page = await b.newPage()
  page.on('pageerror', (e) => console.log('PAGEERROR', e.message.slice(0, 120)))
  await page.setViewport({ width: 1440, height: 1000, deviceScaleFactor: 2 })
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
    localStorage.setItem('apiduo_playground_mode', 'chat')
  })
  await page.reload({ waitUntil: 'networkidle2' })
  await wait(2600)
  await page.keyboard.press('Escape')

  // 选一个按次计费的模型（生图模型在 mock 里也有单价）
  await page.evaluate(() => {
    const trigger = document.querySelectorAll('[role="combobox"]')[0]
    if (trigger) trigger.click()
  })
  await wait(500)
  const picked = await page.evaluate(() => {
    const items = Array.from(document.querySelectorAll('[role="option"]'))
    const target = items.find((n) => /dall-e|image/i.test(n.textContent || ''))
    if (!target) return null
    target.click()
    return target.textContent.trim()
  })
  console.log('model picked:', picked)
  await wait(700)

  await page.click('textarea')
  await page.type('textarea', '按次计费对账单')
  await page.keyboard.press('Enter')
  await wait(4200)

  await page.evaluate(() => {
    const btn = Array.from(document.querySelectorAll('button[aria-expanded]')).find((n) =>
      /\$|¥/.test(n.textContent || '')
    )
    if (btn) btn.click()
  })
  await wait(900)

  await page.evaluate(() => document.documentElement.classList.add('dark'))
  await wait(700)
  await page.screenshot({ path: '/tmp/apiduo-e2e/final-percall-dark.png' })

  await page.evaluate(() => document.documentElement.classList.remove('dark'))
  await wait(500)
  await page.screenshot({ path: '/tmp/apiduo-e2e/final-percall-light.png' })

  const bill = await page.evaluate(() => {
    const text = document.body.innerText.replace(/\s+/g, ' ')
    return {
      hasTokenRows: /输入 token/.test(text),
      hasPerCallNote: /按次计价，不按 token/.test(text),
      seg: (text.match(/本次请求[\s\S]{0,260}/) || [''])[0].slice(0, 260),
    }
  })
  console.log('bill', JSON.stringify(bill, null, 1))

  await page.evaluate(() => document.documentElement.classList.add('dark'))
  await wait(400)
  await page.screenshot({ path: '/tmp/apiduo-e2e/final-dark-top.png', clip: { x: 0, y: 0, width: 1440, height: 260 } })

  await b.close()
})()
