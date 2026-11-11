// 可观测性配置页的浏览器端到端套件（手动运行，未接入 run_all.sh）。
//
// 分四层：导航可见性 → 页面状态回显 → 表单交互与持久化 → 权限与中继联动。
// 每一层都断言「浏览器里真的发生了什么」，而不是后端日志里说了什么。
//
// 为什么没进 run_all：这套需要三样 run_all 没有的东西 ——
//   1. 一个 LANGFUSE_ENABLED=false 的第二实例（关闭态文案）；
//   2. 两个非 root 账号（普通管理员 / 普通用户），其中管理员要提权；
//   3. 一个能数 span 的假 Langfuse 目录（采样率是否真的影响导出）。
// 想跑就按下面的 env 自备环境；断言全部通过会打印「N 通过 / 0 失败」。
//
// env：
//   GW_A        默认 http://127.0.0.1:39510，trace 开启、SAMPLE_RATE=10、TZ=Asia/Shanghai
//   GW_B        默认 http://127.0.0.1:39530，同一个库结构，LANGFUSE_ENABLED=false
//   RELAY_KEY   网关的中继 key（G 组要真发请求）
//   E2E_SINK_DIR 假 Langfuse 落盘目录，默认 /tmp/apiduo-observe/sink
//   E2E_SINK    假 Langfuse 地址，默认 http://127.0.0.1:39502
//   E2E_PROJECT Langfuse project id，默认 observe-project
import { createRequire } from 'module';
import { dirname, resolve } from 'path';
import { fileURLToPath } from 'url';

const here = dirname(fileURLToPath(import.meta.url));
const repo = resolve(here, '../../..');
const require = createRequire(resolve(repo, 'web/package.json'));
const puppeteer = require('puppeteer-core');

const A = process.env.GW_A || 'http://127.0.0.1:39510';
const B = process.env.GW_B || 'http://127.0.0.1:39530';
const SINK = process.env.E2E_SINK || 'http://127.0.0.1:39502';
const SINK_DIR = process.env.E2E_SINK_DIR || '/tmp/apiduo-observe/sink';
const PROJECT = process.env.E2E_PROJECT || 'observe-project';
const CHROME = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const PASS = 'E2ePassw0rd!2026';

const results = [];
let currentGroup = '';
const group = (name) => {
  currentGroup = name;
  console.log('\n' + name);
};
const check = (name, ok, detail = '') => {
  results.push({ group: currentGroup, name, ok: !!ok, detail });
  console.log((ok ? '  PASS ' : '  FAIL ') + name + (detail ? ' — ' + detail : ''));
};

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function api(base, method, path, body, token) {
  const headers = { 'Content-Type': 'application/json' };
  if (token) headers.Authorization = 'Bearer ' + token;
  const res = await fetch(base + path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  let json = {};
  try {
    json = await res.json();
  } catch {
    /* 非 JSON（403 页等） */
  }
  return { status: res.status, body: json };
}

async function loginApi(base, user, pass) {
  const res = await api(base, 'POST', '/api/user/login', { username: user, password: pass });
  return res.body?.data?.access_token;
}

/** 新开一个标签页；每个用例用独立 page，避免上一条的 toast/overflow 影响下一条。 */
async function openPage(browser, { zh = true, dark = false } = {}) {
  const ctx = await browser.createBrowserContext();
  const page = await ctx.newPage();
  await page.setViewport({ width: 1440, height: 900 });
  await page.evaluateOnNewDocument(
    ({ zh, dark }) => {
      if (zh) localStorage.setItem('i18nextLng', 'zhCN');
      if (dark) localStorage.setItem('newapi:theme:v1:mode', 'dark');
    },
    { zh, dark }
  );
  const errors = [];
  page.on('console', (m) => {
    if (m.type() === 'error') errors.push(m.text());
  });
  page.on('pageerror', (e) => errors.push('pageerror: ' + e.message));
  page.errors = errors;
  page.dispose = async () => {
    await ctx.close();
  };
  return page;
}

async function signIn(page, base, user = 'root', pass = PASS) {
  await page.goto(base + '/sign-in', { waitUntil: 'networkidle2', timeout: 60000 });
  await page.waitForSelector('input', { timeout: 30000 });
  const inputs = await page.$$('input');
  const pwIdx = await page.$$eval('input', (els) => els.findIndex((e) => e.type === 'password'));
  const userIdx = await page.$$eval(
    'input',
    (els) => els.findIndex((e) => e.type !== 'password' && e.type !== 'hidden')
  );
  await inputs[userIdx].click({ clickCount: 3 });
  await inputs[userIdx].type(user, { delay: 15 });
  await inputs[pwIdx].click({ clickCount: 3 });
  await inputs[pwIdx].type(pass, { delay: 15 });
  const submit = await page.$('button[type=submit]');
  if (submit) await submit.click();
  else await page.keyboard.press('Enter');
  await page.waitForFunction(() => !location.pathname.startsWith('/sign-in'), { timeout: 60000 });
}

const text = (page) => page.evaluate(() => document.body.innerText);

async function clickText(page, label, selectors = 'button,a,[role="button"]') {
  const ok = await page.evaluate(
    ({ label, selectors }) => {
      const els = Array.from(document.querySelectorAll(selectors));
      const hit = els.find((el) => el.textContent.trim() === label);
      if (!hit) return false;
      hit.scrollIntoView({ block: 'center' });
      hit.click();
      return true;
    },
    { label, selectors }
  );
  return ok;
}

async function setInput(page, selector, value) {
  return page.evaluate(
    ({ selector, value }) => {
      const el = document.querySelector(selector);
      if (!el) return false;
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
      setter.call(el, String(value));
      el.dispatchEvent(new Event('input', { bubbles: true }));
      return true;
    },
    { selector, value }
  );
}

async function numberInputs(page) {
  return page.$$eval('input[type=number]', (els) => els.map((e) => e.value));
}

/* ───────────────────────────── 主流程 ───────────────────────────── */

const browser = await puppeteer.launch({
  executablePath: CHROME,
  headless: true,
  args: ['--no-sandbox', '--no-proxy-server', '--disable-gpu', '--window-size=1440,900', '--lang=zh-CN'],
});

const relayKey = process.env.RELAY_KEY;
const rootJwtA = await loginApi(A, 'root', PASS);
const rootJwtB = await loginApi(B, 'root', PASS);

try {
  /* ── A. 导航可见性 ───────────────────────────────────────────── */
  group('A. 导航：配置页在「扩展」分组里');
  {
    const page = await openPage(browser);
    await signIn(page, A);
    await page.goto(A + '/system-settings/site/system-info', { waitUntil: 'networkidle2' });
    await sleep(1500);

    const groups = await page.evaluate(() => {
      const buttons = Array.from(document.querySelectorAll('button'));
      return buttons.map((b) => b.textContent.trim()).filter((t) => t && t.length < 12);
    });
    check('侧边栏分组标题包含「扩展」', groups.includes('扩展'), JSON.stringify(groups.slice(0, 14)));

    const opened = await clickText(page, '扩展');
    check('「扩展」分组可展开', opened);
    await sleep(900);

    const navText = await text(page);
    check('展开后出现 5 个 apiduo 设置项', ['自定义入口', '外观特效', '对象存储', '登录页风格', '可观测性']
      .every((label) => navText.includes(label)));

    const clicked = await clickText(page, '可观测性');
    await page.waitForFunction(() => location.pathname.endsWith('/site/observability'), { timeout: 15000 }).catch(() => {});
    check('点击导航进入 /system-settings/site/observability', page.url().endsWith('/site/observability'), page.url());
    await page.dispose();
  }

  {
    const page = await openPage(browser);
    await signIn(page, A);
    await page.goto(A + '/system-settings/site/observability', { waitUntil: 'networkidle2' });
    await sleep(1200);
    check('深链直接访问可用', (await text(page)).includes('链路采样'));

    await page.reload({ waitUntil: 'networkidle2' });
    await sleep(1000);
    check('刷新后仍停在该页', page.url().endsWith('/site/observability') && (await text(page)).includes('链路采样'));

    const mark = await page.evaluate(() => {
      const scope = document.querySelector('main') || document.body;
      const hit = Array.from(scope.querySelectorAll('[data-apiduo="self"]'))
        .map((e) => e.textContent.trim())
        .find((t) => t === '可观测性');
      return hit || null;
    });
    check('页面标题带 apiduo 标记（data-apiduo=self）', mark === '可观测性', String(mark));

    await page.goto(A + '/system-settings/operations/behavior', { waitUntil: 'networkidle2' });
    await sleep(800);
    const opsOpened = await clickText(page, '运维');
    await sleep(600);
    const opsText = await text(page);
    check('「运维」分组不再包含可观测性', opsOpened && !opsText.includes('可观测性'));
    await page.dispose();
  }

  /* ── B. 状态条回显（trace 开启实例） ─────────────────────────── */
  group('B. 状态条：把「实际在跑什么」显示出来');
  {
    const page = await openPage(browser);
    await signIn(page, A);
    await page.goto(A + '/system-settings/site/observability', { waitUntil: 'networkidle2' });
    await sleep(1500);
    const t = await text(page);
    check('显示「已启用」', t.includes('已启用'));
    check('显示上报地址', t.includes(SINK.replace('http://', '')));
    check('正文模式映射为「首尾截取」', t.includes('首尾截取'));
    check('当前生效 = 10% 有链接 · 100% 含内容', /10%\s*有链接\s*·\s*100%\s*含内容/.test(t),
      (t.match(/当前生效[\s\S]{0,40}/) || [''])[0].replace(/\s+/g, ' '));
    check('规则时钟带 IANA 名与 UTC 偏移', /Asia\/Shanghai \(UTC\+08:00\)/.test(t));

    const nums = await numberInputs(page);
    check('两个输入框预填 env 值 10 / 100', nums[0] === '10' && nums[1] === '100', JSON.stringify(nums));
    await page.dispose();
  }

  /* ── C. 关闭态 ──────────────────────────────────────────────── */
  group('C. LANGFUSE_ENABLED=false 时页面必须说明白');
  {
    const page = await openPage(browser);
    await signIn(page, B);
    await page.goto(B + '/system-settings/site/observability', { waitUntil: 'networkidle2' });
    await sleep(1500);
    const t = await text(page);
    check('显示「已禁用」', t.includes('已禁用'));
    check('提示来自 LANGFUSE_ENABLED', t.includes('LANGFUSE_ENABLED'));
    check('关闭态仍可编辑（输入框存在）', (await numberInputs(page)).length >= 2);

    const api403 = await api(B, 'GET', '/api/apiduo/trace/config', undefined, rootJwtB);
    check('关闭态 config 接口仍可用（enabled=false）', api403.status === 200 && api403.body?.data?.enabled === false,
      'status=' + api403.status);
    await page.dispose();
  }

  /* ── D. 表单交互 ────────────────────────────────────────────── */
  group('D. 表单：边界值、预设、脏状态');
  {
    const page = await openPage(browser);
    await signIn(page, A);
    await page.goto(A + '/system-settings/site/observability', { waitUntil: 'networkidle2' });
    await sleep(1500);

    const metaSel = 'input[type=number]';
    await setInput(page, metaSel, 150);
    await sleep(200);
    let nums = await numberInputs(page);
    check('元数据输入 150 被夹到 100', nums[0] === '100', nums[0]);

    await setInput(page, metaSel, -5);
    await sleep(300);
    nums = await numberInputs(page);
    check('输入 -5 被夹到 0', nums[0] === '0', nums[0]);
    check('0% 出现「日志页不再有任何 trace 链接」警告',
      (await text(page)).includes('日志页不再有任何 trace 链接'));

    // type=number 会拦掉字母，所以「非法输入」的现实形态是清空。
    const allInputs = await page.$$('input[type=number]');
    await allInputs[1].click();
    for (let i = 0; i < 5; i += 1) await page.keyboard.press('Backspace');
    await sleep(400);
    nums = await numberInputs(page);
    check('清空内容框归 0 且不崩', nums[1] === '0', nums[1]);

    check('预设「仅元数据」可点', await clickText(page, '仅元数据'));
    await sleep(300);
    nums = await numberInputs(page);
    check('「仅元数据」= 100 / 0', nums[0] === '100' && nums[1] === '0', JSON.stringify(nums));
    check('出现合规档说明', (await text(page)).includes('合规场景下的推荐档'));

    check('预设「暂停追踪」可点', await clickText(page, '暂停追踪'));
    await sleep(300);
    nums = await numberInputs(page);
    check('「暂停追踪」= 0 / 0', nums[0] === '0' && nums[1] === '0', JSON.stringify(nums));

    check('预设「全量采集」可点', await clickText(page, '全量采集'));
    await sleep(300);
    nums = await numberInputs(page);
    check('「全量采集」= 100 / 100', nums[0] === '100' && nums[1] === '100', JSON.stringify(nums));
    check('出现未保存提示', (await text(page)).includes('未保存的更改'));

    // 改回原值 → 脏状态必须消失（深比较，不是「动过就行」）
    await setInput(page, metaSel, 10);
    await sleep(200);
    const all2 = await page.$$('input[type=number]');
    await all2[1].click({ clickCount: 3 });
    await page.keyboard.type('100');
    await sleep(400);
    check('改回原值后不再提示未保存', !(await text(page)).includes('未保存的更改'));

    // 放弃修改
    await setInput(page, metaSel, 55);
    await sleep(300);
    check('改动后出现「放弃修改」', await clickText(page, '放弃修改'));
    await sleep(400);
    nums = await numberInputs(page);
    check('放弃后回到 10 / 100', nums[0] === '10' && nums[1] === '100', JSON.stringify(nums));
    await page.dispose();
  }

  /* ── E. 规则 ───────────────────────────────────────────────── */
  group('E. 采样规则：增删、持久化、半窗口降级');
  {
    const page = await openPage(browser);
    await signIn(page, A);
    await page.goto(A + '/system-settings/site/observability', { waitUntil: 'networkidle2' });
    await sleep(1500);

    check('规则区初始为空', (await text(page)).includes('暂无规则'));

    check('「添加规则」可点', await clickText(page, '添加规则'));
    await sleep(400);
    check('出现一条规则', (await text(page)).includes('共 1 条规则'), (await text(page)).match(/暂无规则|共 \d+ 条规则/)?.[0]);

    await page.evaluate(() => {
      const el = document.querySelector('input[placeholder="gpt-4o-mini, doubao-*"]');
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
      setter.call(el, 'gpt-4o-mini');
      el.dispatchEvent(new Event('input', { bubbles: true }));
    });
    await page.evaluate(() => {
      const el = document.querySelector('input[placeholder="09:00"]');
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
      setter.call(el, '09:00');
      el.dispatchEvent(new Event('input', { bubbles: true }));
    });
    await page.evaluate(() => {
      const el = document.querySelector('input[placeholder="18:00"]');
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
      setter.call(el, '18:00');
      el.dispatchEvent(new Event('input', { bubbles: true }));
    });
    for (const day of ['周一', '周二', '周三']) {
      await clickText(page, day);
      await sleep(120);
    }
    // 规则行里的两个 number：元数据 50、内容 20。
    // 不能用全局序号：页面里其它卡片（内容采集/失败采样）也有 number 输入，
    // 卡片一变序号就漂移。从本行的时间输入向上爬，找到第一个含两个 number
    // 的祖先容器，速率框必然在其中。
    await page.evaluate(() => {
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
      let el = document.querySelector('input[placeholder="09:00"]');
      let nums = [];
      for (let i = 0; i < 8 && el?.parentElement; i++) {
        el = el.parentElement;
        nums = Array.from(el.querySelectorAll('input[type=number]'));
        if (nums.length >= 2) break;
      }
      if (nums.length < 2) throw new Error('rule rate inputs not found');
      setter.call(nums[nums.length - 2], '50');
      nums[nums.length - 2].dispatchEvent(new Event('input', { bubbles: true }));
      setter.call(nums[nums.length - 1], '20');
      nums[nums.length - 1].dispatchEvent(new Event('input', { bubbles: true }));
    });
    await sleep(400);

    check('保存按钮可点', await clickText(page, '保存'));
    await sleep(2500);
    check('保存后出现成功提示', (await text(page)).includes('已保存'));

    const saved = await api(A, 'GET', '/api/option/', undefined, rootJwtA);
    const rulesRaw = (saved.body?.data || []).find((o) => o.key === 'observability_setting.rules')?.value;
    const rules = JSON.parse(rulesRaw || '[]');
    check('后端存下 1 条规则', rules.length === 1, rulesRaw);
    check('规则内容完整（模型/时间/星期/速率）',
      rules[0]?.models?.[0] === 'gpt-4o-mini' &&
        rules[0]?.start === '09:00' &&
        rules[0]?.end === '18:00' &&
        rules[0]?.days?.join(',') === '1,2,3' &&
        rules[0]?.metadata_rate === 50 &&
        rules[0]?.content_rate === 20,
      JSON.stringify(rules[0]));

    await page.reload({ waitUntil: 'networkidle2' });
    await sleep(1500);
    check('刷新后规则仍在页面里', (await text(page)).includes('共 1 条规则'));
    const values = await page.evaluate(() => ({
      models: document.querySelector('input[placeholder="gpt-4o-mini, doubao-*"]')?.value,
      start: document.querySelector('input[placeholder="09:00"]')?.value,
      end: document.querySelector('input[placeholder="18:00"]')?.value,
    }));
    check('刷新后规则字段还原', values.models === 'gpt-4o-mini' && values.start === '09:00' && values.end === '18:00',
      JSON.stringify(values));

    // 第二条：完全排除
    await clickText(page, '添加规则');
    await sleep(500);
    await page.evaluate(() => {
      const els = Array.from(document.querySelectorAll('input[placeholder="gpt-4o-mini, doubao-*"]'));
      const el = els[els.length - 1];
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
      setter.call(el, 'blocked-model');
      el.dispatchEvent(new Event('input', { bubbles: true }));
    });
    await page.evaluate(() => {
      const switches = Array.from(document.querySelectorAll('[role="switch"], button[data-state]'));
      const last = switches[switches.length - 1];
      if (last) last.click();
    });
    await sleep(400);
    check('新增第二条规则', (await text(page)).includes('共 2 条规则'));

    await clickText(page, '保存');
    await sleep(2500);
    const saved2 = await api(A, 'GET', '/api/option/', undefined, rootJwtA);
    const rules2 = JSON.parse((saved2.body?.data || []).find((o) => o.key === 'observability_setting.rules')?.value || '[]');
    check('两条规则都落库', rules2.length === 2, JSON.stringify(rules2.map((r) => ({ m: r.models, x: r.exclude }))));
    check('第二条是排除规则', rules2[1]?.exclude === true && rules2[1]?.models?.[0] === 'blocked-model',
      JSON.stringify(rules2[1]));

    // 顺序即优先级：上移必须真的改后端顺序，而不是只动界面
    const moved = await page.evaluate(() => {
      const ups = Array.from(document.querySelectorAll('button[aria-label="上移"]'));
      if (ups.length < 2) return false;
      ups[1].click();
      return true;
    });
    check('第二条规则有「上移」可用', moved);
    await sleep(400);
    await clickText(page, '保存');
    await sleep(2500);
    const movedSaved = await api(A, 'GET', '/api/option/', undefined, rootJwtA);
    const movedRules = JSON.parse((movedSaved.body?.data || []).find((o) => o.key === 'observability_setting.rules')?.value || '[]');
    check('上移后后端第一条变成排除规则', movedRules[0]?.models?.[0] === 'blocked-model',
      JSON.stringify(movedRules.map((r) => r.models?.[0])));

    await page.reload({ waitUntil: 'networkidle2' });
    await sleep(1200);
    const movedBack = await page.evaluate(() => {
      const downs = Array.from(document.querySelectorAll('button[aria-label="下移"]'));
      if (!downs.length) return false;
      downs[0].click();
      return true;
    });
    check('「下移」把顺序改回来', movedBack);
    await sleep(400);
    await clickText(page, '保存');
    await sleep(2500);
    const backSaved = await api(A, 'GET', '/api/option/', undefined, rootJwtA);
    const backRules = JSON.parse((backSaved.body?.data || []).find((o) => o.key === 'observability_setting.rules')?.value || '[]');
    check('顺序恢复为 gpt-4o-mini 优先', backRules[0]?.models?.[0] === 'gpt-4o-mini',
      JSON.stringify(backRules.map((r) => r.models?.[0])));

    // 半窗口：只填 start，保存必须被拦下（否则界面显示有值、运行时全丢弃）
    await page.evaluate(() => {
      const els = Array.from(document.querySelectorAll('input[placeholder="09:00"]'));
      const el = els[els.length - 1];
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
      setter.call(el, '22:00');
      el.dispatchEvent(new Event('input', { bubbles: true }));
    });
    await sleep(300);
    await clickText(page, '保存');
    await sleep(900);
    check('半截时间窗被前端拦下并指出第几条',
      (await text(page)).includes('时间窗请两端都填'), (await text(page)).match(/第 \d+ 条规则[^\n]*/)?.[0]);
    const saved3 = await api(A, 'GET', '/api/option/', undefined, rootJwtA);
    const rules3 = JSON.parse((saved3.body?.data || []).find((o) => o.key === 'observability_setting.rules')?.value || '[]');
    check('被拦下时后端规则未被改写', rules3.length === 2 && rules3[1]?.start === '',
      JSON.stringify(rules3.map((r) => ({ s: r.start, e: r.end }))));

    // 绕过 UI 直接提交半窗口：写入阶段就必须拒绝，否则库里留下一个
    // 运行时不会兑现的窗口（界面显示 22:00，匹配器按全天算）。
    const direct = await api(A, 'PUT', '/api/option/', {
      key: 'observability_setting.rules',
      value: JSON.stringify([{ models: ['direct-model'], days: [], start: '22:00', end: '', exclude: false, metadata_rate: 30, content_rate: 30 }]),
    }, rootJwtA);
    check('直连 API 提交半窗口被拒绝', direct.body?.success === false && /both ends/i.test(direct.body?.message || ''),
      JSON.stringify(direct.body?.message || direct.body));
    const afterDirect = await api(A, 'GET', '/api/option/', undefined, rootJwtA);
    const rulesDirect = JSON.parse((afterDirect.body?.data || []).find((o) => o.key === 'observability_setting.rules')?.value || '[]');
    check('被拒绝后库里规则未变', rulesDirect.length === 2 && rulesDirect[1]?.start === '',
      JSON.stringify(rulesDirect.map((r) => ({ s: r.start, e: r.end }))));

    // 一次保存是一个事务：批里只要有一个非法值，其它字段也不能落地
    const beforeBatch = await api(A, 'GET', '/api/option/', undefined, rootJwtA);
    const metaBefore = (beforeBatch.body?.data || []).find((o) => o.key === 'observability_setting.metadata_sample_rate')?.value;
    const batch = await api(A, 'PUT', '/api/option/batch', {
      options: {
        'observability_setting.metadata_sample_rate': 50,
        'observability_setting.rules': JSON.stringify([{ models: ['x'], start: '09:00', end: '' }]),
      },
    }, rootJwtA);
    check('批量保存遇到非法值整批失败', batch.body?.success === false, JSON.stringify(batch.body?.message || batch.body));
    const afterBatch = await api(A, 'GET', '/api/option/', undefined, rootJwtA);
    const metaAfter = (afterBatch.body?.data || []).find((o) => o.key === 'observability_setting.metadata_sample_rate')?.value;
    check('批量失败后同批其它字段未被写入（原子性）', metaAfter === metaBefore, metaBefore + ' → ' + metaAfter);

    // 收尾：删掉两条规则，避免影响后面的采样断言
    await page.reload({ waitUntil: 'networkidle2' });
    await sleep(1200);
    await clickText(page, '移除');
    await sleep(400);
    await clickText(page, '移除');
    await sleep(400);
    await clickText(page, '保存');
    await sleep(2000);
    const saved4 = await api(A, 'GET', '/api/option/', undefined, rootJwtA);
    const rules4 = JSON.parse((saved4.body?.data || []).find((o) => o.key === 'observability_setting.rules')?.value || '[]');
    check('删除后规则清空', rules4.length === 0, JSON.stringify(rules4));
    await page.dispose();
  }

  /* ── E2. 内容采集 + 失败/脱敏 ─────────────────────────────── */
  group('E2. 内容采集与失败/脱敏：预算联动、成本警告、持久化');
  {
    const page = await openPage(browser);
    await signIn(page, A);
    await page.goto(A + '/system-settings/site/observability', { waitUntil: 'networkidle2' });
    await sleep(1500);

    const bodyCard = () => page.evaluate(() => document.body.innerText);

    // 默认值回显：headtail 32/16
    check('内容卡片存在且默认头尾截断', (await bodyCard()).includes('内容采集'));
    const defaults = await page.evaluate(() => ({
      head: document.querySelector('input[aria-label="头部窗口（KB）"]')?.value,
      tail: document.querySelector('input[aria-label="尾部窗口（KB）"]')?.value,
    }));
    check('窗口默认 32 / 16', defaults.head === '32' && defaults.tail === '16', JSON.stringify(defaults));

    // full 模式的上限与成本警告：两个数字都必须跟着表单走
    await clickText(page, '完整内容');
    await sleep(300);
    const capLabel = 'input[aria-label="单请求上限（MB）"]';
    const capDefault = await page.evaluate((sel) => document.querySelector(sel)?.value, capLabel);
    check('完整模式上限默认 2 MB', capDefault === '2', String(capDefault));
    // 旧文案写死「768 MB」，与 2 MB × 输入输出两份对不上；现在按当前值算。
    check('完整模式成本警告按实际值算（3.9 GB）', (await bodyCard()).includes('3.9 GB'));
    await page.evaluate((sel) => {
      const el = document.querySelector(sel);
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
      setter.call(el, '8');
      el.dispatchEvent(new Event('input', { bubbles: true }));
    }, capLabel);
    await sleep(300);
    const capAfter = await page.evaluate((sel) => document.querySelector(sel)?.value, capLabel);
    check('上限可改到 8 MB', capAfter === '8', String(capAfter));
    check('改值后成本警告跟着变（15.6 GB）', (await bodyCard()).includes('15.6 GB'));
    // 改回来，免得后面那次保存把 8 MB 落库
    await page.evaluate((sel) => {
      const el = document.querySelector(sel);
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
      setter.call(el, '2');
      el.dispatchEvent(new Event('input', { bubbles: true }));
    }, capLabel);
    await sleep(200);
    await clickText(page, '头尾截断');
    await sleep(300);

    // 预算联动：头 128 → 尾被压到 0
    await page.evaluate(() => {
      const el = document.querySelector('input[aria-label="头部窗口（KB）"]');
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
      setter.call(el, '128');
      el.dispatchEvent(new Event('input', { bubbles: true }));
    });
    await sleep(300);
    const clamped = await page.evaluate(() => ({
      head: document.querySelector('input[aria-label="头部窗口（KB）"]')?.value,
      tail: document.querySelector('input[aria-label="尾部窗口（KB）"]')?.value,
    }));
    check('头调到 128 时尾被联动压到 0', clamped.head === '128' && clamped.tail === '0', JSON.stringify(clamped));

    // 还原 32 / 16。联动只会压不会弹：头 128→32 时尾停在 0，
    // 所以尾要显式写回，否则落库断言对不上。
    await page.evaluate(() => {
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
      const head = document.querySelector('input[aria-label="头部窗口（KB）"]');
      setter.call(head, '32');
      head.dispatchEvent(new Event('input', { bubbles: true }));
      const tail = document.querySelector('input[aria-label="尾部窗口（KB）"]');
      setter.call(tail, '16');
      tail.dispatchEvent(new Event('input', { bubbles: true }));
    });
    await sleep(300);
    const restored = await page.evaluate(() => ({
      head: document.querySelector('input[aria-label="头部窗口（KB）"]')?.value,
      tail: document.querySelector('input[aria-label="尾部窗口（KB）"]')?.value,
    }));
    check('还原后窗口 32 / 16', restored.head === '32' && restored.tail === '16', JSON.stringify(restored));

    // 脱敏警告
    await page.evaluate(() => {
      const switches = Array.from(document.querySelectorAll('[role="switch"], button[data-state]'));
      const el = switches.find((x) => x.getAttribute('aria-label') === '抹掉凭据与个人信息') || switches[switches.length - 1];
      el?.click();
    });
    await sleep(300);
    check('关闭脱敏出现原样外发警告', (await bodyCard()).includes('原样离开网关'));
    await page.evaluate(() => {
      const switches = Array.from(document.querySelectorAll('[role="switch"], button[data-state]'));
      const el = switches.find((x) => x.getAttribute('aria-label') === '抹掉凭据与个人信息') || switches[switches.length - 1];
      el?.click();
    });
    await sleep(300);

    // 环境变量清单：只读区必须有「需重启」提示
    check('环境变量区标注需重启', (await bodyCard()).includes('需重启'));

    // 保存并核对落库
    check('保存可点', await clickText(page, '保存'));
    await sleep(2500);
    const saved = await api(A, 'GET', '/api/option/', undefined, rootJwtA);
    const opt = (key) => (saved.body?.data || []).find((o) => o.key === key)?.value;
    check('body_mode 落库 = headtail', opt('observability_setting.body_mode') === 'headtail', opt('observability_setting.body_mode'));
    check('头窗口落库 = 32768', opt('observability_setting.body_head_bytes') === '32768', opt('observability_setting.body_head_bytes'));
    check('尾窗口落库 = 16384', opt('observability_setting.body_tail_bytes') === '16384', opt('observability_setting.body_tail_bytes'));
    check('full 上限落库 = 2097152', opt('observability_setting.body_full_bytes') === '2097152', opt('observability_setting.body_full_bytes'));
    check('脱敏落库 = true', opt('observability_setting.redact') === 'true', opt('observability_setting.redact'));
    check('环境标签落库非空', (opt('observability_setting.environment') || '').length > 0, opt('observability_setting.environment'));

    // 「当前生效」行显示失败与脱敏
    await page.reload({ waitUntil: 'networkidle2' });
    await sleep(1500);
    check('当前生效显示失败采样与脱敏', /失败\s*100%\s*·\s*脱敏开启/.test(await bodyCard()),
      (await bodyCard()).match(/失败[\s\S]{0,30}/)?.[0]?.replace(/\s+/g, ' '));
    await page.dispose();
  }

  /* ── F. 权限 ───────────────────────────────────────────────── */
  group('F. 权限：设置页只给超级管理员，trace 按白名单放行');
  {
    const mgrJwt = await loginApi(A, 'mgr', PASS);
    const aliceJwt = await loginApi(A, 'alice', PASS);
    check('mgr / alice 能登录', Boolean(mgrJwt && aliceJwt));

    const mgrConf = await api(A, 'GET', '/api/apiduo/trace/config', undefined, mgrJwt);
    check('未授权管理员调 config 接口 → 403', mgrConf.status === 403, 'status=' + mgrConf.status);
    const aliceConf = await api(A, 'GET', '/api/apiduo/trace/config', undefined, aliceJwt);
    check('普通用户调 config 接口 → 403', aliceConf.status === 403, 'status=' + aliceConf.status);

    const page = await openPage(browser);
    await signIn(page, A, 'mgr', PASS);
    await page.goto(A + '/system-settings/site/observability', { waitUntil: 'networkidle2' });
    await sleep(1200);
    check('管理员打开设置页被挡到 403', page.url().includes('/403') || (await text(page)).includes('403'),
      page.url());
    await page.dispose();

    // root 把 mgr(id=2) 加进白名单，走真实的保存路径
    const set = await api(A, 'PUT', '/api/option/', {
      key: 'observability_setting.trace_viewer_user_ids',
      value: JSON.stringify([2]),
    }, rootJwtA);
    check('白名单保存成功', set.status === 200 && set.body?.success === true, 'status=' + set.status);

    const mgrAfter = await api(A, 'GET', '/api/apiduo/trace/config', undefined, mgrJwt);
    check('白名单内管理员 → 200', mgrAfter.status === 200 && mgrAfter.body?.data?.enabled === true,
      'status=' + mgrAfter.status);
    const aliceAfter = await api(A, 'GET', '/api/apiduo/trace/config', undefined, aliceJwt);
    check('白名单外普通用户仍 403', aliceAfter.status === 403, 'status=' + aliceAfter.status);

    const page2 = await openPage(browser);
    await signIn(page2, A, 'mgr', PASS);
    await page2.goto(A + '/system-settings/site/observability', { waitUntil: 'networkidle2' });
    await sleep(1000);
    check('白名单不放宽设置页权限（仍 403）',
      page2.url().includes('/403') || (await text(page2)).includes('403'), page2.url());
    await page2.dispose();

    // 复位白名单，避免影响其它断言
    await api(A, 'PUT', '/api/option/', {
      key: 'observability_setting.trace_viewer_user_ids',
      value: '[]',
    }, rootJwtA);
  }

  /* ── G. 中继联动：改采样 → 真的影响导出与日志页链接 ─────────── */
  group('G. 端到端联动：采样率 → 导出 → 日志页链接');
  {
    const setRates = async (meta, content) => {
      await api(A, 'PUT', '/api/option/', { key: 'observability_setting.metadata_sample_rate', value: String(meta) }, rootJwtA);
      await api(A, 'PUT', '/api/option/', { key: 'observability_setting.content_sample_rate', value: String(content) }, rootJwtA);
      await sleep(300);
    };
    const relay = async (prompt) => {
      const res = await fetch(A + '/v1/chat/completions', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + relayKey },
        body: JSON.stringify({ model: 'gpt-4o-mini', messages: [{ role: 'user', content: prompt }] }),
      });
      return res.status;
    };
    // /count 数的是 OTLP 请求数，批量导出会把多个 span 合成一个请求，
    // 所以要解开每个请求体数 spans —— 这才是「有没有按采样率导出」。
    const sinkSpans = async () => {
      const fs = await import('fs');
      const dir = SINK_DIR;
      let total = 0;
      for (const f of fs.readdirSync(dir)) {
        if (!f.endsWith('.json')) continue;
        try {
          const body = JSON.parse(fs.readFileSync(dir + '/' + f, 'utf8'));
          for (const rs of body.resourceSpans || []) {
            for (const ss of rs.scopeSpans || []) total += (ss.spans || []).length;
          }
        } catch {
          /* 半写入的文件跳过 */
        }
      }
      return total;
    };

    // 先清掉上一组留下的规则，避免规则盖过默认值
    await api(A, 'PUT', '/api/option/', { key: 'observability_setting.rules', value: '[]' }, rootJwtA);

    await setRates(100, 100);
    const before = await sinkSpans();
    const codes = [await relay('e2e-1'), await relay('e2e-2'), await relay('e2e-3')];
    check('中继请求全部 200', codes.every((c) => c === 200), JSON.stringify(codes));
    await sleep(2000);
    const after = await sinkSpans();
    check('100% 采样下 3 次请求 → sink 收到 3 个 span', after - before === 3, before + ' → ' + after);

    const page = await openPage(browser);
    await signIn(page, A);
    await page.goto(A + '/usage-logs/common', { waitUntil: 'networkidle2' });
    await sleep(2500);
    const opened = await page.evaluate(() => {
      const btn = document.querySelector('tbody tr button[title]');
      if (!btn) return false;
      btn.click();
      return true;
    });
    check('日志表格能点开详情', opened);
    await sleep(1200);
    const dialogText = await text(page);
    check('详情弹窗出现「在 Langfuse 中查看」', dialogText.includes('在 Langfuse 中查看'),
      dialogText.slice(0, 120).replace(/\s+/g, ' '));
    const href = await page.evaluate(() => {
      const a = Array.from(document.querySelectorAll('a')).find((x) => x.textContent.includes('在 Langfuse 中查看'));
      return a ? a.getAttribute('href') : null;
    });
    check('链接指向 project/traces 且带 trace id',
      Boolean(href && href.includes('/project/' + PROJECT + '/traces/')), String(href));
    await page.dispose();

    // 关掉元数据采样：既不该有新 span，日志页也不该再有链接
    await setRates(0, 0);
    const before2 = await sinkSpans();
    const code = await relay('e2e-off');
    check('0% 采样下中继仍然正常返回', code === 200, 'status=' + code);
    await sleep(2000);
    const after2 = await sinkSpans();
    check('0% 采样下 sink 不新增 span', after2 - before2 === 0, before2 + ' → ' + after2);

    const page2 = await openPage(browser);
    await signIn(page2, A);
    await page2.goto(A + '/usage-logs/common', { waitUntil: 'networkidle2' });
    await sleep(2500);
    await page2.evaluate(() => {
      const btn = document.querySelector('tbody tr button[title]');
      if (btn) btn.click();
    });
    await sleep(1200);
    check('0% 采样下最新日志的详情没有 trace 链接', !(await text(page2)).includes('在 Langfuse 中查看'));
    await page2.dispose();

    await setRates(100, 100);
  }

  /* ── H. 视觉与健壮性 ───────────────────────────────────────── */
  group('H. 三档视口、深色、控制台与键盘');
  {
    const page = await openPage(browser);
    await signIn(page, A);
    await page.goto(A + '/system-settings/site/observability', { waitUntil: 'networkidle2' });
    await sleep(1500);
    page.errors.length = 0;

    for (const [w, h] of [[1440, 900], [1024, 800], [390, 844]]) {
      await page.setViewport({ width: w, height: h });
      await sleep(600);
      const overflow = await page.evaluate(() => ({
        doc: document.documentElement.scrollWidth - document.documentElement.clientWidth,
        body: document.body.scrollWidth - document.body.clientWidth,
      }));
      check(w + 'px 无横向溢出', overflow.doc <= 1 && overflow.body <= 1, JSON.stringify(overflow));
    }
    await page.screenshot({ path: '/tmp/apiduo-observe/obs-e2e-390.png', fullPage: true });

    await page.setViewport({ width: 1440, height: 900 });
    await sleep(400);
    await page.screenshot({ path: '/tmp/apiduo-observe/obs-e2e-1440.png', fullPage: true });

    // 深色：写入 localStorage 后重载，断言真的进了 dark 且正文不是深底深字
    const dark = await openPage(browser, { dark: true });
    await signIn(dark, A);
    await dark.goto(A + '/system-settings/site/observability', { waitUntil: 'networkidle2' });
    await sleep(1200);
    const contrast = await dark.evaluate(() => {
      const isDark = document.documentElement.classList.contains('dark');
      const el = document.querySelector('main p, main dd, main label');
      const cs = el ? getComputedStyle(el) : null;
      // 现代浏览器把 token 计算成 lab()/oklch()，不是 rgb()：直接取 L 通道。
      const raw = cs?.color || '';
      const lab = raw.match(/lab\(([\d.]+)/);
      let lum;
      if (lab) {
        lum = Number(lab[1]) / 100;
      } else if (raw.startsWith('rgb')) {
        const rgb = raw.match(/\d+/g).map(Number);
        lum = (rgb[0] * 0.299 + rgb[1] * 0.587 + rgb[2] * 0.114) / 255;
      } else {
        const ok = raw.match(/oklch\(([\d.]+)/);
        lum = ok ? Number(ok[1]) : 0;
      }
      return { isDark, lum: Number(lum.toFixed(2)), color: raw };
    });
    check('深色模式生效', contrast.isDark === true, JSON.stringify(contrast));
    check('深色下正文为浅色（亮度 > 0.5）', contrast.lum > 0.5, 'lum=' + contrast.lum + ' ' + contrast.color);
    await dark.screenshot({ path: '/tmp/apiduo-observe/obs-e2e-dark.png', fullPage: true });
    await dark.dispose();

    const unnamed = await page.evaluate(() => {
      const scope = document.querySelector('main') || document.body;
      const controls = Array.from(scope.querySelectorAll('input, [role="switch"]'));
      return controls
        .filter((el) => !el.getAttribute('aria-label') && !el.getAttribute('aria-labelledby') && !el.id)
        .map((el) => el.getAttribute('placeholder') || el.tagName);
    });
    check('main 里的输入与开关都有可访问名', unnamed.length === 0, JSON.stringify(unnamed));

    const saveBtn = await page.evaluate(() => {
      const btn = Array.from(document.querySelectorAll('button')).find((b) => b.textContent.trim() === '保存');
      return btn ? { disabled: btn.disabled, tabIndex: btn.tabIndex } : null;
    });
    check('保存按钮可聚焦且未禁用', Boolean(saveBtn && !saveBtn.disabled && saveBtn.tabIndex >= 0), JSON.stringify(saveBtn));

    const errs = page.errors.filter((e) => !/favicon|Failed to load resource: the server responded with a status of 401/.test(e));
    check('页面无 JS 报错', errs.length === 0, errs.slice(0, 2).join(' | '));
    await page.dispose();
  }

} catch (err) {
  console.log('\n  ERROR ' + err.message);
  results.push({ group: currentGroup, name: '异常中断', ok: false, detail: err.message });
} finally {
  await browser.close();
}

const failed = results.filter((r) => !r.ok);
console.log('\n==== 汇总：' + (results.length - failed.length) + ' 通过 / ' + failed.length + ' 失败 ====');
if (failed.length) {
  for (const f of failed) console.log('  FAIL [' + f.group + '] ' + f.name + (f.detail ? ' — ' + f.detail : ''));
}
process.exit(failed.length ? 1 : 0);
