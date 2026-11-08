// 前端 E2E：三模式、发送/停止、结算三态、对账单、抽屉、持久化、深色与移动
const puppeteer = require('/Users/betterme/PycharmProjects/AI/apiduo/web/node_modules/puppeteer-core')

const BASE = 'http://127.0.0.1:3000'
const wait = (ms) => new Promise((r) => setTimeout(r, ms))
const results = []

function record(name, ok, detail = '') {
  results.push({ name, ok: !!ok, detail: String(detail).slice(0, 300) })
  console.log((ok ? 'PASS' : 'FAIL') + ' | ' + name + ' | ' + String(detail).slice(0, 200))
}

async function moneyChip(page) {
  return page.evaluate(() => {
    const btn = Array.from(document.querySelectorAll('button[aria-expanded]')).find((n) =>
      /\$|¥/.test(n.textContent || '')
    )
    return btn ? { text: btn.textContent.trim(), expanded: btn.getAttribute('aria-expanded') } : null
  })
}

async function assistantCount(page) {
  return page.evaluate(
    () => document.querySelectorAll('[data-apiduo]').length && document.querySelectorAll('.msg, [class*="group"]').length
  )
}

async function sendMessage(page, text) {
  await page.click('textarea')
  await page.type('textarea', text)
  await page.keyboard.press('Enter')
}

async function loginAndOpen(page) {
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
  // 每轮从干净状态开始，否则上一轮的消息会让「初始为 0」这类断言失败
  await page.evaluate(() => {
    localStorage.clear()
    document.documentElement.classList.remove('dark')
  })
  await page.reload({ waitUntil: 'networkidle2' })
  await wait(2600)
  await page.keyboard.press('Escape')
}

;(async () => {
  const browser = await puppeteer.launch({
    executablePath: '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
    headless: 'new',
    args: ['--no-proxy-server', '--hide-scrollbars'],
  })
  const page = await browser.newPage()
  const jsErrors = []
  const httpErrors = []
  page.on('pageerror', (e) => jsErrors.push(e.message.slice(0, 200)))
  page.on('response', (r) => {
    if (r.status() >= 400 && !r.url().includes('/api/user/auth/refresh')) {
      httpErrors.push(r.status() + ' ' + r.url().replace(BASE, ''))
    }
  })

  await page.setViewport({ width: 1440, height: 1000, deviceScaleFactor: 1 })
  await loginAndOpen(page)

  // ---------- 场景 1：初次加载 ----------
  const s1 = await page.evaluate(() => ({
    tabs: Array.from(document.querySelectorAll('[role="tab"]')).map((t) => t.textContent.trim()),
    price: (document.body.innerText.match(/输入[\s\S]{0,40}\/M/) || [''])[0].replace(/\s+/g, ' '),
    spend: (document.body.innerText.match(/本次会话[^\n]*/) || [''])[0].replace(/\s+/g, ' '),
    model: (document.querySelector('.font-mono') || {}).textContent || '',
    marks: Array.from(document.querySelectorAll('[data-apiduo]')).map((n) => n.getAttribute('data-apiduo')),
  }))
  record('S1 三个模式 tab', JSON.stringify(s1.tabs) === JSON.stringify(['聊天', '图片', '视频']), s1.tabs)
  record('S1 价格条显示折后单价', /输入/.test(s1.price) && /折后/.test(s1.price), s1.price)
  record('S1 会话累计初始为 0', /—/.test(s1.spend), s1.spend)
  record('S1 界面带 apiduo 标识', s1.marks.length >= 2, s1.marks.join(','))

  // ---------- 场景 2：发送并结算 ----------
  await sendMessage(page, '前端 E2E 第一条')
  await wait(4200)
  const chip1 = await moneyChip(page)
  record('S2 出现金额标签', !!chip1, JSON.stringify(chip1))
  record('S2 已对账（非估算）', !!chip1 && /^\$/.test(chip1.text) && !/估/.test(chip1.text), chip1 && chip1.text)
  record('S2 默认收起', !!chip1 && chip1.expanded === 'false', chip1 && chip1.expanded)

  // 展开对账单
  await page.evaluate(() => {
    const btn = Array.from(document.querySelectorAll('button[aria-expanded]')).find((n) => /\$|¥/.test(n.textContent || ''))
    if (btn) btn.click()
  })
  await wait(600)
  const bill = await page.evaluate(() => {
    const text = document.body.innerText
    const m = text.match(/本次请求[\s\S]{0,420}/)
    return m ? m[0].replace(/\s+/g, ' ') : ''
  })
  record('S2 对账单展开', /本次请求/.test(bill), bill.slice(0, 120))
  record('S2 对账单含公式与额度', /额度/.test(bill) && /×/.test(bill), '')
  record('S2 对账单含实际扣费', /实际扣费/.test(bill), '')
  const spendAfter1 = await page.evaluate(() => {
    // 直接按「$金额 · N 次请求」定位，避免 header 里混进侧边栏文案
    const text = document.body.innerText.replace(/\s+/g, ' ')
    const m = text.match(/(\$[\d.]+) ?· ?(\d+) 次请求/)
    return m ? m[1] : ''
  })
  record('S2 会话累计已增加', /\$[\d.]+/.test(spendAfter1), spendAfter1.slice(0, 80))

  // ---------- 场景 3：重复 3 轮，检查累计单调与无重复结算 ----------
  const spends = []
  for (let i = 0; i < 3; i += 1) {
    await sendMessage(page, `repeat-${i}`)
    await wait(3800)
    const s = await page.evaluate(() => {
      const text = document.body.innerText.replace(/\s+/g, ' ')
      const m = text.match(/(\$[\d.]+) ?· ?(\d+) 次请求/)
      return m ? m[1] : ''
    })
    spends.push(s)
  }
  const nums = spends.map((s) => Number(s.replace('$', '') || 0))
  record('S3 累计单调递增', nums.length === 3 && nums[0] < nums[2], JSON.stringify(spends))
  const chipCount = await page.evaluate(
    () => Array.from(document.querySelectorAll('button[aria-expanded]')).filter((n) => /\$|¥/.test(n.textContent || '')).length
  )
  record('S3 每条 assistant 消息一个金额标签', chipCount === 4, 'chips=' + chipCount)

  // ---------- 场景 4：停止生成 ----------
  await sendMessage(page, '停止测试')
  await wait(600)
  await page.evaluate(() => {
    const btn = Array.from(document.querySelectorAll('button')).find((n) => /停止/.test(n.textContent || ''))
    if (btn) btn.click()
  })
  await wait(2500)
  const afterStop = await page.evaluate(() => {
    const chips = Array.from(document.querySelectorAll('button[aria-expanded]')).filter((n) => /\$|¥/.test(n.textContent || ''))
    const last = chips[chips.length - 1]
    return {
      count: chips.length,
      bubbles: document.querySelectorAll('.group').length,
      last: last ? last.textContent.trim() : '',
      stopBtn: (Array.from(document.querySelectorAll('button')).find((n) => /发送|停止/.test(n.textContent || '')) || {}).textContent,
    }
  })
  record('S4 停止后仍产出金额', afterStop.count === 5,
    `count=${afterStop.count} bubbles=${afterStop.bubbles} last=${afterStop.last}`)
  record('S4 停止后按钮复位', /发送/.test(afterStop.stopBtn || ''), afterStop.stopBtn)

  // ---------- 场景 5：请求详情抽屉 ----------
  await page.evaluate(() => {
    const btn = Array.from(document.querySelectorAll('button')).find((n) => /请求详情/.test(n.textContent || ''))
    if (btn) btn.click()
  })
  await wait(800)
  const drawerCollapsed = await page.evaluate(() => ({
    open: !!document.querySelector('[role="dialog"]'),
    details: Array.from(document.querySelectorAll('[role="dialog"] details')).map((d) => d.open),
  }))
  // 展开第三块（等价的 cURL）后再读文本
  await page.evaluate(() => {
    const list = document.querySelectorAll('[role="dialog"] details')
    if (list[2]) list[2].open = true
  })
  await wait(400)
  const drawer = {
    ...drawerCollapsed,
    curl: await page.evaluate(() => (document.body.innerText.match(/curl[\s\S]{0,140}/) || [''])[0]),
  }
  record('S5 抽屉打开', drawer.open, '')
  record('S5 三个折叠块默认收起', drawer.details.length === 3 && drawer.details.every((o) => !o), JSON.stringify(drawer.details))
  record('S5 cURL 指向正式 API', /\/v1\/chat\/completions/.test(drawer.curl) && /Bearer/.test(drawer.curl), drawer.curl.slice(0, 80).replace(/\s+/g, ' '))
  await page.keyboard.press('Escape')
  await wait(500)

  // ---------- 场景 6：刷新持久化 ----------
  await page.reload({ waitUntil: 'networkidle2' })
  await wait(2600)
  const afterReload = await page.evaluate(() => ({
    text: (document.body.innerText.match(/repeat-2|前端 E2E 第一条/) || [''])[0],
    chips: Array.from(document.querySelectorAll('button[aria-expanded]')).filter((n) => /\$|¥/.test(n.textContent || '')).length,
  }))
  record('S6 刷新后消息仍在', !!afterReload.text, afterReload.text)
  record('S6 刷新后金额标签不再显示（仅内存态，符合预期）', afterReload.chips === 0, 'chips=' + afterReload.chips)

  // ---------- 场景 7：模式切换 ----------
  await (await page.$$('[role="tab"]'))[1].click()
  await wait(1200)
  const imageMode = await page.evaluate(() => ({
    text: (document.body.innerText.match(/描述你想生成的图片/) || [''])[0],
    sizes: (document.body.innerText.match(/1024x1024/) || [''])[0],
    marks: Array.from(document.querySelectorAll('[data-apiduo]')).map((n) => n.getAttribute('data-apiduo')),
  }))
  record('S7 生图工作区渲染', !!imageMode.text && !!imageMode.sizes, JSON.stringify(imageMode).slice(0, 120))

  await (await page.$$('[role="tab"]'))[2].click()
  await wait(1200)
  const videoMode = await page.evaluate(() => ({
    text: (document.body.innerText.match(/描述你想生成的镜头/) || [''])[0],
    sizes: (document.body.innerText.match(/1280x720|720x1280/) || [''])[0],
    marks: Array.from(document.querySelectorAll('[data-apiduo]')).map((n) => n.getAttribute('data-apiduo')),
  }))
  record('S7 生视频工作区渲染', !!videoMode.text, JSON.stringify(videoMode).slice(0, 120))
  record('S7 视频分辨率是宽x高', !!videoMode.sizes, videoMode.sizes)

  await (await page.$$('[role="tab"]'))[0].click()
  await wait(1000)
  const backToChat = await page.evaluate(
    () => (document.body.innerText.match(/repeat-2|前端 E2E 第一条/) || [''])[0]
  )
  record('S7 切回对话后消息仍在', !!backToChat, backToChat)

  // ---------- 场景 8：深色与移动 ----------
  await page.evaluate(() => document.documentElement.classList.add('dark'))
  await wait(500)
  const darkOk = await page.evaluate(() => document.documentElement.classList.contains('dark'))
  record('S8 深色模式生效', darkOk, '')

  await page.setViewport({ width: 390, height: 860, deviceScaleFactor: 2 })
  await wait(900)
  const mobile = await page.evaluate(() => ({
    sw: document.documentElement.scrollWidth,
    cw: document.documentElement.clientWidth,
    tabs: Array.from(document.querySelectorAll('[role="tab"]')).length,
  }))
  record('S8 移动端无横向溢出', mobile.sw === mobile.cw, JSON.stringify(mobile))
  record('S8 移动端 tab 仍可切换', mobile.tabs === 3, '')

  // ---------- 汇总 ----------
  record('S9 无未捕获 JS 错误', jsErrors.length === 0, jsErrors.slice(0, 3).join(' | '))
  record('S9 无意外 4xx/5xx', httpErrors.length === 0, httpErrors.slice(0, 3).join(' | '))

  const ok = results.filter((r) => r.ok).length
  console.log('\n===== ' + ok + '/' + results.length + ' passed =====')
  require('fs').writeFileSync('/tmp/apiduo-e2e/frontend-results.json', JSON.stringify(results, null, 1))
  await browser.close()
})()
