// 渲染进度条验收：不确定态 → 增长 → 填满 → 收掉，外加失败态。
const puppeteer = require('/Users/betterme/PycharmProjects/AI/apiduo/web/node_modules/puppeteer-core')
const fs = require('fs')
const BASE = 'http://127.0.0.1:3901'
const wait = (ms) => new Promise((r) => setTimeout(r, ms))
const results = []

function record(name, ok, detail = '') {
  results.push({ name, ok: !!ok, detail: String(detail).slice(0, 260) })
  console.log((ok ? 'PASS' : 'FAIL') + ' | ' + name + ' | ' + String(detail).slice(0, 180))
}

async function login(page) {
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
  await wait(2800)
  await page.keyboard.press('Escape')
  await wait(400)
  for (let i = 0; i < 4; i += 1) {
    const hasDialog = await page.evaluate(
      () => !!document.querySelector('[role="dialog"], [role="alertdialog"]')
    )
    if (!hasDialog) break
    await page.evaluate(() => {
      const d = document.querySelector('[role="dialog"], [role="alertdialog"]')
      const btn = d && Array.from(d.querySelectorAll('button')).find((b) => /稍后|关闭|知道了|Later|Close/i.test(b.textContent || ''))
      if (btn) btn.click()
    })
    await page.keyboard.press('Escape')
    await wait(400)
  }
}

const probe = () =>
  ({
    hasTrack: !!document.querySelector('.apd-indeterminate-track'),
    hasHead: !!document.querySelector('.apd-progress-head'),
    barWidth: (() => {
      const head = document.querySelector('.apd-progress-head')
      const fill = head && head.parentElement
      const track = fill && fill.parentElement
      if (!fill || !track) return null
      return Math.round((fill.getBoundingClientRect().width / track.getBoundingClientRect().width) * 100)
    })(),
    failed: !!document.querySelector('.apd-progress-failed'),
    done: !!document.querySelector('.apd-progress-done'),
  })

;(async () => {
  const browser = await puppeteer.launch({
    executablePath: '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
    headless: 'new',
    args: ['--no-proxy-server', '--hide-scrollbars'],
  })
  const page = await browser.newPage()
  const errors = []
  page.on('pageerror', (e) => errors.push(e.message.slice(0, 140)))
  await page.setViewport({ width: 1440, height: 900, deviceScaleFactor: 2 })
  await login(page)

  fs.rmSync('/tmp/apiduo-e2e/frames-progress', { recursive: true, force: true })
  fs.mkdirSync('/tmp/apiduo-e2e/frames-progress', { recursive: true })

  await page.click('textarea')
  await page.type('textarea', '[slow] 看一下渲染进度条')
  await page.keyboard.press('Enter')

  const snapshots = []
  const shoot = async (tag) => {
    const state = await page.evaluate(probe)
    snapshots.push({ tag, ...state })
    // 跟着进度条本身走：长回复时它会随自动滚动往下移，固定区域拍不到
    const box = await page.evaluate(() => {
      const main = document.querySelector('main') || document.body
      const left = main.getBoundingClientRect().left + 24
      const bar = document.querySelector('.apd-progress-track')
      const top = bar ? bar.getBoundingClientRect().top : window.innerHeight * 0.55
      return {
        x: Math.max(0, left),
        y: Math.max(0, top - 300),
        width: Math.min(900, window.innerWidth - left - 24),
        height: 380,
      }
    })
    await page.screenshot({ path: `/tmp/apiduo-e2e/frames-progress/${tag}.png`, clip: box })
    return state
  }

  await wait(450)
  await shoot('a-loading')

  // 增长阶段逐帧采样宽度
  await page.evaluate(() => {
    window.__w = []
    window.__probeTimer = setInterval(() => {
      const head = document.querySelector('.apd-progress-head')
      const fill = head && head.parentElement
      const track = fill && fill.parentElement
      if (!fill || !track) return
      window.__w.push(
        Math.round((fill.getBoundingClientRect().width / track.getBoundingClientRect().width) * 100)
      )
    }, 120)
  })

  await wait(1500)
  await shoot('b-growing-early')
  await wait(1200)
  await shoot('c-growing-mid')
  await wait(1200)
  await shoot('d-growing-late')

  // 完成瞬间
  // 完成动画会把 94 → 100 的中间帧也采进来，先收采样窗口
  await page.evaluate(() => clearInterval(window.__probeTimer))

  await wait(900)
  const finishing = await shoot('e-finishing')
  await wait(900)
  const gone = await page.evaluate(probe)
  await wait(400)
  await shoot('f-done')

  const widths = await page.evaluate(() => window.__w || [])
  const clean = widths.filter((n) => Number.isFinite(n))
  const monotonic = clean.every((v, i) => i === 0 || v >= clean[i - 1] - 3)

  record('P1 首字未到时是扫描带（不确定态）', snapshots[0].hasTrack === true, JSON.stringify(snapshots[0]))
  record('P2 有内容后切到确定态并出现笔头', snapshots[1].hasHead === true && snapshots[1].hasTrack === false, JSON.stringify(snapshots[1]))
  record('P3 进度随内容单调增长', monotonic && clean.length > 8, `samples=${clean.length} ${clean.join(',')}`)
  record(
    'P4 生成途中不提前满格（封顶 94%）',
    clean.length > 10 && Math.max(...clean) <= 94,
    `samples=${clean.length} max=${clean.length ? Math.max(...clean) : 0}`
  )
  record('P5 完成时填满并闪一次', finishing.done === true || (finishing.barWidth ?? 0) >= 99, JSON.stringify(finishing))
  record('P6 收尾后彻底移除', gone.hasTrack === false && gone.hasHead === false, JSON.stringify(gone))
  record('P7 无未捕获 JS 错误', errors.length === 0, errors.slice(0, 3).join(' | '))

  await browser.close()
  console.log('\n===== ' + results.filter((r) => r.ok).length + '/' + results.length + ' passed =====')
  console.log('widths:', clean.join(','))
})()
