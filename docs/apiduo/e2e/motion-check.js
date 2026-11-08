// 动效验证：指示器、进场、落定、展开、降级。断言基于计算样式与逐帧采样，
// 不靠肉眼看截图。
const puppeteer = require('/Users/betterme/PycharmProjects/AI/apiduo/web/node_modules/puppeteer-core')

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
  await page.evaluate(() => localStorage.clear())
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
  const pageErrors = []
  page.on('pageerror', (e) => pageErrors.push(e.message.slice(0, 160)))
  const httpErrors = []
  page.on('console', (m) => {
    if (m.type() === 'error') pageErrors.push('console: ' + m.text().slice(0, 160))
  })
  page.on('response', (r) => {
    if (r.status() >= 400) httpErrors.push(r.status() + ' ' + r.url().replace(BASE, ''))
  })

  await page.setViewport({ width: 1440, height: 1000, deviceScaleFactor: 1 })
  await login(page)

  // ── M1 指示器几何 ──
  const pill = await page.evaluate(() => {
    const node = document.querySelector('.apd-tab-pill')
    if (!node) return null
    const active = Array.from(document.querySelectorAll('[role="tab"]')).find(
      (t) => t.getAttribute('aria-selected') === 'true'
    )
    const pr = node.getBoundingClientRect()
    const ar = active.getBoundingClientRect()
    const cs = getComputedStyle(node)
    return {
      dx: Math.round(pr.left - ar.left),
      dw: Math.round(pr.width - ar.width),
      transition: cs.transitionProperty,
      opacity: cs.opacity,
    }
  })
  record('M1 指示器严丝合缝贴合当前 tab', pill && Math.abs(pill.dx) <= 1 && Math.abs(pill.dw) <= 1, JSON.stringify(pill))
  record('M1 用 transition 而非 keyframe 跳动', pill && /transform/.test(pill.transition || ''), pill && pill.transition)

  // ── M2 指示器滑动（逐帧采样，必须出现中间位置）──
  await page.evaluate(() => {
    window.__pill = []
    const node = document.querySelector('.apd-tab-pill')
    const tick = () => {
      window.__pill.push(Math.round(node.getBoundingClientRect().left))
      if (window.__pill.length < 30) requestAnimationFrame(tick)
    }
    requestAnimationFrame(tick)
  })
  await (await page.$$('[role="tab"]'))[1].click()
  await wait(760)
  const slide = await page.evaluate(() => {
    const s = window.__pill || []
    const steps = new Set(s).size
    return { samples: s.length, distinct: steps, first: s[0], last: s[s.length - 1] }
  })
  record('M2 指示器是滑动过去，不是瞬移', slide.distinct >= 5, JSON.stringify(slide))

  // ── M3 空状态 stagger ──
  const stagger = await page.evaluate(() => {
    const host = document.querySelector('.apd-stagger')
    if (!host) return null
    return Array.from(host.children).map((child) => {
      const cs = getComputedStyle(child)
      return { name: cs.animationName, delay: cs.animationDelay }
    })
  })
  record(
    'M3 空状态逐项进场且延迟递增',
    Array.isArray(stagger) && stagger.length >= 2 && stagger.every((s) => s.name === 'apd-fade-up') &&
      stagger[1].delay !== stagger[0].delay,
    JSON.stringify(stagger)
  )

  // ── M4 工作区容器确实由 motion 接管（inline style 由它写入）──
  const workspace = await page.evaluate(() => {
    const header = document.querySelector('[role="tablist"]').closest('header')
    const node = header?.nextElementSibling?.nextElementSibling
    if (!node) return null
    return {
      cls: node.className,
      inlineOpacity: node.style.opacity,
      inlineTransform: node.style.transform,
    }
  })
  record(
    'M4 工作区容器由 motion 接管',
    workspace && workspace.inlineOpacity === '1' && /overflow-hidden/.test(workspace.cls),
    JSON.stringify(workspace)
  )

  // ── M5 切回对话，发一条，抓结算光环 ──
  await (await page.$$('[role="tab"]'))[0].click()
  await wait(900)
  await page.evaluate(() => {
    window.__seen = { halo: 0, rise: 0, mediaIn: 0 }
    const note = (node) => {
      if (!(node instanceof Element)) return
      if (node.classList.contains('apd-halo')) window.__seen.halo += 1
      if (node.classList.contains('apd-rise')) window.__seen.rise += 1
      if (node.classList.contains('apd-media-in')) window.__seen.mediaIn += 1
      node.querySelectorAll?.('.apd-halo').forEach(() => (window.__seen.halo += 1))
      node.querySelectorAll?.('.apd-media-in').forEach(() => (window.__seen.mediaIn += 1))
    }
    const observer = new MutationObserver((records) => {
      for (const r of records) {
        if (r.type === 'attributes') note(r.target)
        else r.addedNodes.forEach(note)
      }
    })
    observer.observe(document.body, {
      subtree: true,
      attributes: true,
      attributeFilter: ['class'],
      childList: true,
    })
  })

  await page.click('textarea')
  await page.type('textarea', '动效验收')
  await page.keyboard.press('Enter')
  await wait(4200)

  const seen = await page.evaluate(() => window.__seen)
  record('M5 消息以 apd-rise 进场', seen.rise >= 2, JSON.stringify(seen))
  record('M5 结算瞬间出现一次光环', seen.halo >= 1, JSON.stringify(seen))

  const chipSel = await page.evaluate(() => {
    const btn = Array.from(document.querySelectorAll('button[aria-expanded]')).find((n) =>
      /\$|¥/.test(n.textContent || '')
    )
    if (!btn) return null
    const cs = getComputedStyle(btn)
    return { transition: cs.transitionProperty, text: btn.textContent.trim() }
  })
  record('M5 金额 chip 具备按压过渡', chipSel && /transform/.test(chipSel.transition || ''), JSON.stringify(chipSel))

  // ── M6 对账单展开：逐帧高度采样 ──
  await page.evaluate(() => {
    window.__h = []
    const tick = () => {
      const slot = document.querySelector('[data-apd-slot="bill"]')
      window.__h.push(slot ? Math.round(slot.getBoundingClientRect().height) : -1)
      if (window.__h.length < 26) requestAnimationFrame(tick)
    }
    requestAnimationFrame(tick)
  })
  await page.evaluate(() => {
    const btn = Array.from(document.querySelectorAll('button[aria-expanded]')).find((n) =>
      /\$|¥/.test(n.textContent || '')
    )
    if (btn) btn.click()
  })
  await wait(800)
  const heights = await page.evaluate(() => {
    const s = window.__h || []
    return { samples: s.length, distinct: new Set(s).size, max: Math.max(...s), min: Math.min(...s) }
  })
  record('M6 对账单高度连续展开（非瞬显）', heights.distinct >= 4, JSON.stringify(heights))

  const staggerRows = await page.evaluate(() => {
    const host = document.querySelector('.apd-stagger')
    if (!host) return null
    return Array.from(host.children).slice(0, 3).map((c) => getComputedStyle(c).animationDelay)
  })
  record('M6 对账单行有 stagger', Array.isArray(staggerRows) && new Set(staggerRows).size >= 2, JSON.stringify(staggerRows))

  await page.screenshot({ path: '/tmp/apiduo-e2e/motion-chat.png' })

  // ── M7 图片 shimmer + 呈现 ──
  await (await page.$$('[role="tab"]'))[1].click()
  await wait(1100)
  await page.evaluate(() => {
    window.__shimmer = 0
    const observer = new MutationObserver(() => {
      if (document.querySelector('.apd-shimmer')) window.__shimmer = 1
    })
    observer.observe(document.body, { subtree: true, childList: true, attributes: true })
    window.__shimmerObserver = observer
  })
  await page.click('textarea')
  await page.type('textarea', 'motion test image')
  await page.keyboard.press('Enter')
  await wait(3000)

  const imageState = await page.evaluate(() => ({
    mediaIn: document.querySelectorAll('.apd-media-in').length,
    tiles: document.querySelectorAll('img.apd-media-in').length,
    animName: (() => {
      const img = document.querySelector('img.apd-media-in')
      return img ? getComputedStyle(img).animationName : null
    })(),
    shimmerSeen: window.__shimmer,
  }))
  record('M7 图片以 apd-media-in 呈现', imageState.animName === 'apd-media-in', JSON.stringify(imageState))

  // ── M8 视频：进度条 motion + 状态 nudge ──
  await (await page.$$('[role="tab"]'))[2].click()
  await wait(1100)
  await page.screenshot({ path: '/tmp/apiduo-e2e/motion-video-empty.png' })

  // ── M9 会话累计是滚动数字 ──
  await (await page.$$('[role="tab"]'))[0].click()
  await wait(900)
  const rolling = await page.evaluate(() => {
    const nodes = Array.from(document.querySelectorAll('header span.relative.inline-flex'))
    return nodes.map((n) => ({
      overflow: getComputedStyle(n).overflow,
      text: n.textContent.trim(),
    }))
  })
  record('M9 累计金额用裁剪窗口做滚动', rolling.length >= 1 && rolling.every((r) => r.overflow === 'hidden'), JSON.stringify(rolling))

  // ── M10 深色 + 三档视口 ──
  await page.evaluate(() => document.documentElement.classList.add('dark'))
  await wait(600)
  await page.screenshot({ path: '/tmp/apiduo-e2e/motion-dark.png' })
  await page.evaluate(() => document.documentElement.classList.remove('dark'))

  await page.setViewport({ width: 1024, height: 900, deviceScaleFactor: 2 })
  await wait(700)
  await page.screenshot({ path: '/tmp/apiduo-e2e/motion-1024.png' })

  await page.setViewport({ width: 390, height: 860, deviceScaleFactor: 2 })
  await wait(700)
  const mobile = await page.evaluate(() => ({
    sw: document.documentElement.scrollWidth,
    cw: document.documentElement.clientWidth,
  }))
  await page.screenshot({ path: '/tmp/apiduo-e2e/motion-390.png' })
  record('M10 窄屏无横向溢出', mobile.sw === mobile.cw, JSON.stringify(mobile))

  // ── M11 reduced motion 降级 ──
  await page.setViewport({ width: 1440, height: 1000, deviceScaleFactor: 1 })
  await page.emulateMediaFeatures([{ name: 'prefers-reduced-motion', value: 'reduce' }])
  await page.reload({ waitUntil: 'networkidle2' })
  await wait(2600)
  const reduced = await page.evaluate(() => {
    const rise = document.querySelector('.apd-rise')
    const pill = document.querySelector('.apd-tab-pill')
    const probe = document.createElement('div')
    probe.className = 'apd-fade-up'
    document.body.append(probe)
    const fade = getComputedStyle(probe)
    const out = {
      rise: rise ? getComputedStyle(rise).animationName : 'no-node',
      pillTransition: pill ? getComputedStyle(pill).transitionProperty : 'no-node',
      fade: fade.animationName,
      fadeOpacity: fade.opacity,
    }
    probe.remove()
    return out
  })
  record('M11 减动效下关掉位移动画', reduced.rise === 'none' && reduced.fade === 'none', JSON.stringify(reduced))
  record('M11 减动效下指示器不再滑动', reduced.pillTransition === 'none', reduced.pillTransition)
  record('M11 减动效下内容仍然可见', reduced.fadeOpacity === '1', reduced.fadeOpacity)
  await page.emulateMediaFeatures([])

  // ── M12 无报错 ──
  const unexpected = httpErrors.filter(
    (line) => !line.includes('/api/user/auth/refresh') && !line.includes('/api/user/self')
  )
  record(
    'M12 无未捕获 JS 错误',
    pageErrors.filter((e) => !e.includes('401')).length === 0,
    pageErrors.slice(0, 3).join(' | ')
  )
  record('M12 无意外 4xx/5xx（refresh 竞态除外）', unexpected.length === 0, unexpected.slice(0, 4).join(' | ') + ' || 全部:' + httpErrors.slice(0, 4).join(' | '))

  const ok = results.filter((r) => r.ok).length
  console.log('\n===== ' + ok + '/' + results.length + ' passed =====')
  require('fs').writeFileSync('/tmp/apiduo-e2e/motion-results.json', JSON.stringify(results, null, 1))
  await browser.close()
})()
