// 浏览器端到端：主端点 / 图片协议的整条交互链。
//
// 与 primary_adversarial.py 的分工：那边测 API 与上游落点，这边测「人在页面上能不能
// 正确完成这件事」——页签默认值、开关联动、派生增删、保存后回显、脏状态不串渠道、
// 窄屏与深色。断言尽量落在「保存后的真实存储」上，而不是只看页面自己说了什么。
//
// 语言固定 zh：i18next 从 localStorage 读，先写死再进页面，避免跑在英文机器上选择器全挂。
import { createRequire } from 'module'
import { dirname, resolve } from 'path'
import { fileURLToPath } from 'url'

const here = dirname(fileURLToPath(import.meta.url))
const repo = resolve(here, '../../..')
const require = createRequire(resolve(repo, 'web/package.json'))
const puppeteer = require('puppeteer-core')

const BASE = process.env.E2E_BASE || 'http://127.0.0.1:39420'
const USER = process.env.E2E_ADMIN || 'admin'
const PASS = process.env.E2E_ADMIN_PW || 'apiduo-test-2026'
const CHROME = process.env.E2E_CHROME || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'
const ART = process.env.E2E_ARTIFACT_DIR || '/tmp/apiduo-ui'

const rows = []
const check = (name, ok, detail = '') => {
  rows.push({ name, ok })
  console.log((ok ? 'PASS ' : 'FAIL ') + name + (detail ? ' | ' + detail : ''))
}

// ---------- 管理面 ----------
let token = ''
const api = async (method, path, body) => {
  const res = await fetch(BASE + path, {
    method,
    headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  const text = await res.text()
  try {
    return { status: res.status, json: JSON.parse(text) }
  } catch {
    return { status: res.status, json: { raw: text } }
  }
}

const channels = async () => {
  const { json } = await api('GET', '/api/channel/?p=0&page_size=100')
  return Object.fromEntries(((json.data || {}).items || []).map((it) => [it.name, it]))
}
const stored = async (name) => JSON.parse((await channels())[name].settings)

const drop = async (names) => {
  const table = await channels()
  for (const name of names) {
    if (table[name]) await api('DELETE', `/api/channel/${table[name].id}`)
  }
}

const create = async (name, model, settings) =>
  api('POST', '/api/channel/', {
    mode: 'single',
    channel: {
      name,
      type: 58,
      key: 'sk-e2e',
      base_url: 'http://127.0.0.1:1',
      models: model,
      group: 'default',
      priority: 0,
      weight: 0,
      status: 1,
      settings: JSON.stringify(settings),
    },
  })

// ---------- 页面 ----------
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

const login = async (page) => {
  await page.goto(BASE + '/sign-in', { waitUntil: 'networkidle2', timeout: 60000 })
  await page.waitForSelector('input', { timeout: 30000 })
  const inputs = await page.$$('input')
  await inputs[0].type(USER)
  const pwdIdx = await page.$$eval('input', (els) => els.findIndex((e) => e.type === 'password'))
  await inputs[pwdIdx].type(PASS)
  await page.keyboard.press('Enter')
  await page.waitForFunction(() => !location.pathname.startsWith('/sign-in'), { timeout: 60000 })
}

const gotoChannels = async (page) => {
  for (let attempt = 0; attempt < 3; attempt += 1) {
    try {
      await page.goto(BASE + '/channels', { waitUntil: 'networkidle2', timeout: 60000 })
      break
    } catch (e) {
      if (attempt === 2) throw e
      await sleep(800)
    }
  }
  await page.waitForFunction(() => document.body.innerText.includes('渠道'), { timeout: 30000 })
  await sleep(1200)
}

const openDrawer = async (page, name) => {
  const opened = await page.evaluate((channelName) => {
    const leaf = [...document.querySelectorAll('*')].find(
      (n) => n.children.length === 0 && (n.textContent || '').trim() === channelName
    )
    if (!leaf) return 'no-name'
    let card = leaf
    for (let i = 0; i < 8 && card; i += 1) {
      const btn = [...card.querySelectorAll('button')].find((b) =>
        (b.textContent || '').trim().includes('打开菜单')
      )
      if (btn) {
        btn.click()
        return 'menu'
      }
      card = card.parentElement
    }
    return 'no-menu'
  }, name)
  if (opened !== 'menu') throw new Error(`open menu failed: ${opened}`)
  await sleep(600)
  await page.evaluate(() => {
    const item = [...document.querySelectorAll('[role=menuitem]')].find(
      (n) => (n.textContent || '').trim() === '编辑'
    )
    item?.click()
  })
  await page.waitForFunction(() => !!document.querySelector('[role=dialog]'), { timeout: 20000 })
  await sleep(1200)
}

const openRoutesDialog = async (page) => {
  const clicked = await page.evaluate(() => {
    const btn = [...document.querySelectorAll('button')].find(
      (b) => (b.textContent || '').trim() === '配置路由'
    )
    if (!btn) return false
    btn.click()
    return true
  })
  if (!clicked) throw new Error('配置路由 button not found')
  // 抽屉本身也是 role=dialog，必须找「带主端点页签的那个」对话框。
  await page.waitForFunction(
    () =>
      [...document.querySelectorAll('[role=dialog]')].some((dialog) =>
        [...dialog.querySelectorAll('[role=tab]')].some((t) => (t.textContent || '').includes('主端点'))
      ),
    { timeout: 20000 }
  )
  await sleep(900)
}

const activeTab = (page) =>
  page.evaluate(() => {
    const dialogs = [...document.querySelectorAll('[role=dialog]')]
    const dlg = dialogs.find((d) =>
      [...d.querySelectorAll('[role=tab]')].some((t) => (t.textContent || '').includes('主端点'))
    )
    if (!dlg) return ''
    const tab = [...dlg.querySelectorAll('[role=tab]')].find(
      (t) => t.getAttribute('data-active') === 'true' || t.getAttribute('aria-selected') === 'true'
    )
    return (tab?.textContent || '').trim()
  })

const clickTab = async (page, label) => {
  await page.evaluate((text) => {
    const dialogs = [...document.querySelectorAll('[role=dialog]')]
    const dlg = dialogs.find((d) =>
      [...d.querySelectorAll('[role=tab]')].some((t) => (t.textContent || '').includes('主端点'))
    )
    const tab = [...(dlg || document).querySelectorAll('[role=tab]')].find((t) => (t.textContent || '').includes(text))
    tab?.click()
  }, label)
  await sleep(700)
}

const switchHandle = (page, label) =>
  page.evaluateHandle((text) => {
    const list = [...document.querySelectorAll('[role=switch]')]
    return list.find((s) => (s.getAttribute('aria-label') || '').includes(text)) || null
  }, label)

const switchState = (page, label) =>
  page.evaluate((text) => {
    const el = [...document.querySelectorAll('[role=switch]')].find((s) =>
      (s.getAttribute('aria-label') || '').includes(text)
    )
    return el ? el.getAttribute('aria-checked') : null
  }, label)

const toggle = async (page, label) => {
  const handle = await switchHandle(page, label)
  const el = handle.asElement()
  if (!el) throw new Error(`switch not found: ${label}`)
  await el.click()
  await sleep(800)
}

// 顶层（路由）对话框：带主端点页签的那个；抽屉也是 dialog，不能取第一个。
const dialogText = (page) =>
  page.evaluate(() => {
    const dialogs = [...document.querySelectorAll('[role=dialog]')]
    const target = dialogs.find((d) =>
      [...d.querySelectorAll('[role=tab]')].some((t) => (t.textContent || '').includes('主端点'))
    )
    return (target || dialogs[dialogs.length - 1] || {}).innerText || ''
  })

const toasts = (page) =>
  page.evaluate(() =>
    [...document.querySelectorAll('[data-sonner-toast]')].map((n) => (n.textContent || '').trim()).filter(Boolean)
  )

const waitToast = async (page, seconds = 12) => {
  for (let i = 0; i < seconds * 4; i += 1) {
    const list = await toasts(page)
    if (list.length) return list.join(' / ')
    await sleep(250)
  }
  return ''
}

const clearToasts = async (page) => {
  // sonner 的提示会自动消失；这里只等它们退场，避免把上一步的文案当成这一步的
  for (let i = 0; i < 40; i += 1) {
    if (!(await toasts(page)).length) return
    await sleep(250)
  }
}

const saveDialog = async (page) => {
  await clearToasts(page)
  await page.evaluate(() => {
    const btn = [...document.querySelectorAll('button')].find(
      (b) => (b.textContent || '').trim() === '保存更改'
    )
    btn?.click()
  })
  await sleep(600)
}

const saveDrawer = async (page) => {
  await page.evaluate(() => {
    const btn = [...document.querySelectorAll('button')].find(
      (b) => (b.textContent || '').trim() === '更新渠道'
    )
    btn?.click()
  })
  await sleep(2200)
}

const closeDialog = async (page) => {
  await page.keyboard.press('Escape')
  await page
    .waitForFunction(
      () =>
        ![...document.querySelectorAll('[role=dialog]')].some((d) =>
          [...d.querySelectorAll('[role=tab]')].some((t) => (t.textContent || '').includes('主端点'))
        ),
      { timeout: 10000 }
    )
    .catch(() => {})
  await sleep(500)
}

const main = async () => {
  const loginRes = await (async () => {
    const res = await fetch(BASE + '/api/user/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username: USER, password: PASS }),
    })
    return res.json()
  })()
  token = loginRes.data.access_token
  if (!token) throw new Error('admin login failed: ' + JSON.stringify(loginRes).slice(0, 200))

  const names = ['ui-on', 'ui-fresh', 'ui-off']
  await drop(names)

  // ui-on：已声明主端点 + 协议开
  let r = await create('ui-on', 'ui-on', {
    primary_endpoint: '/v1/images/generations',
    image_protocol: { enabled: true, upstream: 'seedream' },
    advanced_custom: {
      advanced_routes: [
        { incoming_path: '/v1/images/generations' },
        { incoming_path: '/v1/chat/completions', upstream_path: '/v1/chat/completions' },
      ],
    },
  })
  check('新建 ui-on（主端点 + 协议）', r.status === 200 && r.json.success === true, JSON.stringify(r.json).slice(0, 120))

  // ui-fresh：只有管理面路由，用来验证「打开开关自动种派生」
  r = await create('ui-fresh', 'ui-fresh', {
    advanced_custom: { advanced_routes: [{ incoming_path: '/v1/models', upstream_path: '/v1/models' }] },
  })
  check('新建 ui-fresh（无业务路由）', r.status === 200 && r.json.success === true, JSON.stringify(r.json).slice(0, 120))

  // ui-off：未声明主端点，路由写实
  r = await create('ui-off', 'ui-off', {
    advanced_custom: {
      advanced_routes: [
        { incoming_path: '/v1/images/generations', upstream_path: '/v1/images/generations' },
        { incoming_path: '/v1/chat/completions', upstream_path: '/v1/chat/completions' },
      ],
    },
  })
  check('新建 ui-off（无主端点）', r.status === 200 && r.json.success === true, JSON.stringify(r.json).slice(0, 120))

  await sleep(2500)

  const browser = await puppeteer.launch({
    executablePath: CHROME,
    headless: true,
    args: ['--no-sandbox', '--no-proxy-server', '--disable-gpu', '--window-size=1440,1000'],
  })
  const page = await browser.newPage()
  const pageErrors = []
  page.on('pageerror', (e) => pageErrors.push(e.message))
  page.on('console', (m) => {
    if (m.type() === 'error') pageErrors.push('console:' + m.text().slice(0, 160))
  })
  await page.setViewport({ width: 1440, height: 1000, deviceScaleFactor: 2 })
  await page.evaluateOnNewDocument(() => localStorage.setItem('i18nextLng', 'zh'))

  try {
    await login(page)
    await gotoChannels(page)
    const namesOnPage = await page.evaluate(() => document.body.innerText)
    check('渠道列表加载', ['ui-on', 'ui-fresh', 'ui-off'].every((n) => namesOnPage.includes(n)))

    // ---------- 1. 抽屉徽标 ----------
    await openDrawer(page, 'ui-on')
    const drawer1 = await page.evaluate(() => (document.querySelector('[role=dialog]') || {}).innerText || '')
    check('uion 抽屉显示主端点徽标', drawer1.includes('主端点：/v1/images/generations'), drawer1.slice(0, 80).replace(/\s+/g, ' '))

    // ---------- 2. 默认页签 ----------
    await openRoutesDialog(page)
    check('已声明主端点 → 默认落「主端点」页签', (await activeTab(page)).includes('主端点'), await activeTab(page))

    // ---------- 3. 开启态渲染 ----------
    const onText = await dialogText(page)
    check('开启态：开关为开', (await switchState(page, '主端点')) === 'true')
    check(
      '开启态：路径回显',
      await page.evaluate(() => {
        const dialogs = [...document.querySelectorAll('[role=dialog]')]
        const dlg = dialogs.find((d) =>
          [...d.querySelectorAll('[role=tab]')].some((t) => (t.textContent || '').includes('主端点'))
        )
        const input = dlg?.querySelector('input.font-mono')
        return !!input && input.value === '/v1/images/generations'
      })
    )
    check('开启态：流向图四个入口齐全', ['文生图', '图生图', '对话', '响应'].every((k) => onText.includes(k)), onText.includes('文生图') ? '' : '缺入口')
    check('开启态：脚注说明 responses 不折叠', onText.includes('响应入口仍按路由表转发'))
    check('开启态：派生计数 2', onText.includes('2') && onText.includes('派生'))

    // ---------- 4. 高级区 ----------
    await page.evaluate(() => {
      const btn = [...document.querySelectorAll('button')].find((b) => (b.textContent || '').includes('派生端点、协议细节'))
      btn?.click()
    })
    await sleep(700)
    const advText = await dialogText(page)
    check('高级区：协议开关为开', (await switchState(page, '图片协议')) === 'true')
    check('高级区：布局回显 seedream', advText.includes('Seedream'), advText.slice(0, 60).replace(/\s+/g, ' '))
    check('高级区：派生表两行', (await page.$$eval('[role=dialog] .divide-y > div', (els) => els.length)) >= 2)

    // 切换布局 → 保存 → 落库
    await page.evaluate(() => {
      const dialogs = [...document.querySelectorAll('[role=dialog]')]
      const dlg = dialogs.find((d) =>
        [...d.querySelectorAll('[role=tab]')].some((t) => (t.textContent || '').includes('主端点'))
      )
      const trigger = [...dlg.querySelectorAll('[role=combobox]')].find((c) =>
        /seedream/i.test(c.textContent || '')
      )
      trigger?.click()
    })
    await sleep(500)
    await page.evaluate(() => {
      const opt = [...document.querySelectorAll('[role=option]')].find((o) => (o.textContent || '').includes('GPT Image 2'))
      opt?.click()
    })
    await sleep(600)
    await saveDialog(page)
    const closedAfterLayout = await page
      .waitForFunction(
        () =>
          ![...document.querySelectorAll('[role=dialog]')].some((d) =>
            [...d.querySelectorAll('[role=tab]')].some((t) => (t.textContent || '').includes('主端点'))
          ),
        { timeout: 8000 }
      )
      .then(() => true)
      .catch(() => false)
    check('保存布局变更后对话框关闭（保存成功）', closedAfterLayout, JSON.stringify(await toasts(page)).slice(0, 100))
    await saveDrawer(page)
    const s1 = await stored('ui-on')
    check('布局变更落库 gpt-image-2', s1.image_protocol?.upstream === 'gpt-image-2', JSON.stringify(s1.image_protocol))

    // ---------- 5. 重开仍是新值（不丢状态） ----------
    await closeDialog(page)
    await page.keyboard.press('Escape')
    await sleep(800)
    await openDrawer(page, 'ui-on')
    await openRoutesDialog(page)
    await page.evaluate(() => {
      const btn = [...document.querySelectorAll('button')].find((b) => (b.textContent || '').includes('派生端点、协议细节'))
      btn?.click()
    })
    await sleep(600)
    check('重开抽屉：布局仍是 gpt-image-2', (await dialogText(page)).includes('GPT Image 2'))

    // ---------- 6. 关闭主端点 → 写实 + 清协议 ----------
    await toggle(page, '主端点')
    await sleep(400)
    check('关闭后开关为关', (await switchState(page, '主端点')) === 'false')
    check('关闭后出现「与 new-api 一致」说明', (await dialogText(page)).includes('与 new-api 完全一致'))
    await saveDialog(page)
    await waitToast(page)
    await saveDrawer(page)
    const s2 = await stored('ui-on')
    const routes2 = (s2.advanced_custom || {}).advanced_routes || []
    check('关闭后移除 primary_endpoint', !('primary_endpoint' in s2), JSON.stringify(s2.primary_endpoint))
    check('关闭后移除 image_protocol', !('image_protocol' in s2), JSON.stringify(s2.image_protocol))
    check('关闭后路由写实为主端点路径',
      routes2.every((row) => (row.upstream_path || '').length > 0), JSON.stringify(routes2))

    // ---------- 7. 徽标与默认页签随之变化 ----------
    await closeDialog(page)
    await page.keyboard.press('Escape')
    await sleep(800)
    await openDrawer(page, 'ui-on')
    const drawer2 = await page.evaluate(() => (document.querySelector('[role=dialog]') || {}).innerText || '')
    check('关闭后抽屉徽标「主端点已关闭」', drawer2.includes('主端点已关闭'), drawer2.slice(0, 80).replace(/\s+/g, ' '))
    await openRoutesDialog(page)
    check('关闭后默认落「路由转发」页签', (await activeTab(page)).includes('路由转发'), await activeTab(page))
    await closeDialog(page)
    await page.keyboard.press('Escape')
    await sleep(600)

    // ---------- 8. 空路由表打开开关 → 自动种派生 ----------
    await openDrawer(page, 'ui-fresh')
    await openRoutesDialog(page)
    check('ui-fresh 默认落「路由转发」', (await activeTab(page)).includes('路由转发'), await activeTab(page))
    await clickTab(page, '主端点')
    await toggle(page, '主端点')
    await sleep(400)
    const seededText = await dialogText(page)
    check('打开开关后出现派生行', seededText.includes('/v1/images/generations'), seededText.slice(0, 100).replace(/\s+/g, ' '))
    await saveDialog(page)
    const toastSeed = await waitToast(page)
    check('空路由表也能保存（自动种派生）', !toastSeed.includes('至少一条路由') && !toastSeed.toLowerCase().includes('required'), toastSeed.slice(0, 90))
    await saveDrawer(page)
    const s3 = await stored('ui-fresh')
    const routes3 = (s3.advanced_custom || {}).advanced_routes || []
    check('种出的派生跟随主端点（upstream 为空）',
      routes3.some((row) => row.incoming_path === '/v1/images/generations' && !row.upstream_path),
      JSON.stringify(routes3))
    check('管理面路由被保留', routes3.some((row) => row.incoming_path === '/v1/models'), JSON.stringify(routes3.map((r) => r.incoming_path)))
    check('主端点落库', s3.primary_endpoint === '/v1/images/generations', JSON.stringify(s3.primary_endpoint))

    // ---------- 9. 非法路径：保存被拦且不落库 ----------
    await closeDialog(page)
    await page.keyboard.press('Escape')
    await sleep(800)
    await openDrawer(page, 'ui-fresh')
    await openRoutesDialog(page)
    const setPrimaryPath = async (value) => {
      await page.evaluate((val) => {
        const dialogs = [...document.querySelectorAll('[role=dialog]')]
        const dlg = dialogs.find((d) =>
          [...d.querySelectorAll('[role=tab]')].some((t) => (t.textContent || '').includes('主端点'))
        )
        const input = dlg.querySelector('input.font-mono')
        const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set
        setter.call(input, val)
        input.dispatchEvent(new Event('input', { bubbles: true }))
      }, value)
      await sleep(500)
    }
    // ---------- 9.5 回归：改主端点不该顺手清掉图片协议 ----------
    // 过去输入框每敲一个字就按「像不像图片路径」清空 image_protocol：清空重填后协议就没了，
    // 保存即静默丢配置。现在只说明折叠去向，开关状态由用户自己决定。
    await page.evaluate(() => {
      const btn = [...document.querySelectorAll('button')].find((b) => (b.textContent || '').includes('派生端点、协议细节'))
      btn?.click()
    })
    await sleep(600)
    check('ui-fresh 保存后协议为开', (await switchState(page, '图片协议')) === 'true')
    await setPrimaryPath('')
    check('清空主端点后协议仍为开（不再被逐字符清掉）', (await switchState(page, '图片协议')) === 'true')
    await setPrimaryPath('/v1/chat/completions')
    check('聊天主端点：协议开关没被改写', (await switchState(page, '图片协议')) === 'true')
    check('聊天主端点：说明折叠仍走图片默认路径', (await dialogText(page)).includes('仍折到'), '')
    await setPrimaryPath('/v1/images/generations')
    // 收回到折叠态：后面第 11 步靠「点一下展开」来数派生行，这里不能替它先展开。
    await page.evaluate(() => {
      const btn = [...document.querySelectorAll('button')].find((b) => (b.textContent || '').includes('派生端点、协议细节'))
      btn?.click()
    })
    await sleep(400)

    await setPrimaryPath('v1/images/generations')
    await saveDialog(page)
    const toastBad = await waitToast(page)
    const rejected =
      /Primary endpoint must be a path starting with \//.test(toastBad) ||
      toastBad.includes('主端点必须以') ||
      toastBad.includes('请修正高亮字段')
    check('缺前导斜杠被拒绝且有明确文案', rejected, toastBad.slice(0, 120))
    const s4 = await stored('ui-fresh')
    check('非法路径未落库', s4.primary_endpoint === '/v1/images/generations', JSON.stringify(s4.primary_endpoint))

    // ---------- 10. 非 generations 路径：红字提示 ----------
    await setPrimaryPath('/v1/images/custom-path')
    await sleep(500)
    check('非 generations 路径出现红字提示', (await dialogText(page)).includes('末段不是 generations'), '')
    await setPrimaryPath('/v1/images/generations')

    // ---------- 11. 派生增删 ----------
    await page.evaluate(() => {
      const btn = [...document.querySelectorAll('button')].find((b) => (b.textContent || '').includes('派生端点、协议细节'))
      btn?.click()
    })
    await sleep(600)
    const countRows = () =>
      page.evaluate(() => [...document.querySelectorAll('[role=dialog] button')].filter((b) => (b.getAttribute('aria-label') || '').includes('移除派生端点')).length)
    const rowsBefore = await countRows()
    await page.evaluate(() => {
      const trigger = [...document.querySelectorAll('[role=dialog] [role=combobox]')].find((c) =>
        (c.textContent || '').includes('添加派生端点')
      )
      trigger?.click()
    })
    await sleep(500)
    await page.evaluate(() => {
      const opt = [...document.querySelectorAll('[role=option]')].find((o) => /Image Edits/i.test(o.textContent || ''))
      opt?.click()
    })
    await sleep(400)
    await page.evaluate(() => {
      const btn = [...document.querySelectorAll('[role=dialog] button')].find((b) => (b.textContent || '').trim() === '添加')
      btn?.click()
    })
    await sleep(600)
    const rowsAfterAdd = await countRows()
    check('添加派生行', rowsAfterAdd === rowsBefore + 1, `${rowsBefore} -> ${rowsAfterAdd}`)

    await page.evaluate(() => {
      const btn = [...document.querySelectorAll('[role=dialog] button')].find(
        (b) => (b.getAttribute('aria-label') || '').includes('移除派生端点')
      )
      btn?.click()
    })
    await sleep(600)
    const rowsAfterRemove = await countRows()
    check('删除派生行', rowsAfterRemove === rowsBefore, `${rowsAfterAdd} -> ${rowsAfterRemove}`)

    // ---------- 12. 换渠道不串状态 ----------
    await closeDialog(page)
    await page.keyboard.press('Escape')
    await sleep(800)
    await openDrawer(page, 'ui-off')
    await openRoutesDialog(page)
    check('未声明主端点的渠道默认落「路由转发」', (await activeTab(page)).includes('路由转发'), await activeTab(page))
    await clickTab(page, '主端点')
    const offText = await dialogText(page)
    check('未声明主端点时显示关闭说明', offText.includes('关闭时'), offText.slice(0, 60).replace(/\s+/g, ' '))

    // ---------- 13. 窄屏 ----------
    await closeDialog(page)
    await page.keyboard.press('Escape')
    await sleep(600)
    await page.setViewport({ width: 390, height: 900, deviceScaleFactor: 2 })
    await openDrawer(page, 'ui-fresh')
    await openRoutesDialog(page)
    const tabs390 = await page.evaluate(() =>
      [...document.querySelectorAll('[role=tab]')].map((t) => (t.textContent || '').trim()).filter(Boolean)
    )
    check('窄屏页签全部渲染', tabs390.some((t) => t.includes('主端点')) && tabs390.some((t) => t.includes('完整 JSON')), JSON.stringify(tabs390).slice(0, 120))
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
    check('窄屏无横向溢出', overflow <= 0, String(overflow))
    await page.screenshot({ path: `${ART}/ui-e2e-390.png`, fullPage: false })

    // ---------- 14. 深色 ----------
    await page.emulateMediaFeatures([{ name: 'prefers-color-scheme', value: 'dark' }])
    await page.setViewport({ width: 1440, height: 1000, deviceScaleFactor: 2 })
    await sleep(700)
    await page.screenshot({ path: `${ART}/ui-e2e-dark.png` })
    check('深色模式无渲染异常', (await dialogText(page)).length > 20)

    // ---------- 15. Escape 关闭 ----------
    await page.keyboard.press('Escape')
    const closed = await page
      .waitForFunction(
        () =>
          ![...document.querySelectorAll('[role=dialog]')].some((d) =>
            [...d.querySelectorAll('[role=tab]')].some((t) => (t.textContent || '').includes('主端点'))
          ),
        { timeout: 5000 }
      )
      .then(() => true)
      .catch(() => false)
    check('Escape 关闭对话框', closed)

    check('全程无 JS 报错', pageErrors.length === 0, pageErrors.slice(0, 3).join(' | '))
  } finally {
    await browser.close()
    await drop(names)
  }

  const failed = rows.filter((r) => !r.ok)
  console.log(`\n共 ${rows.length} 项，失败 ${failed.length}`)
  return failed.length ? 1 : 0
}

main()
  .then((code) => process.exit(code))
  .catch((err) => {
    console.error(err && err.stack ? err.stack : err)
    process.exit(1)
  })
