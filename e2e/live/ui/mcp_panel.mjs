// MCP 面板（/mcp/admin）的浏览器端验证：真实 Chrome + 真实点击。
//
// 面板是一个自包含的静态页，它不共享 React 应用的 store，而是用 refresh cookie
// 换 access token。所以这里必须真的登录一次再打开面板 —— 直接 goto 只能证明文件能下载，
// 证明不了「登录态复用」这条链路。
//
// 「保存后还在」一律用**新标签页**验证，不用 reload：面板对未保存修改挂了
// beforeunload，reload 会停在确认框上，表现为导航超时（看起来像页面卡死）。
import { createRequire } from 'module';
import { dirname, resolve } from 'path';
import { fileURLToPath } from 'url';
import { mkdirSync } from 'fs';

const here = dirname(fileURLToPath(import.meta.url));
const repo = resolve(here, '../../..');
const require = createRequire(resolve(repo, 'web/package.json'));
const puppeteer = require('puppeteer-core');

const BASE = process.env.E2E_BASE || 'http://127.0.0.1:39420';
const USER = process.env.E2E_ADMIN || 'admin';
const PASS = process.env.E2E_ADMIN_PW || 'apiduo-test-2026';
const CHROME = process.env.E2E_CHROME || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const ART = process.env.E2E_ARTIFACT_DIR || '/tmp';
const MODEL = process.env.E2E_MCP_TARGET || 'mcp-e2e-image';
mkdirSync(ART, { recursive: true });

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const rows = [];
const check = (name, ok, detail = '') => {
  rows.push([ok, name, detail]);
  console.log((ok ? 'PASS ' : 'FAIL ') + name + (detail ? ' | ' + detail : ''));
};

const browser = await puppeteer.launch({
  executablePath: CHROME,
  headless: true,
  args: ['--no-sandbox', '--no-proxy-server', '--disable-gpu', '--window-size=1440,900'],
});

const jsErrors = [];
let view = null;

const openPanel = async () => {
  const p = await browser.newPage();
  p.on('pageerror', (e) => jsErrors.push(String(e.message || e)));
  p.on('console', (m) => {
    if (m.type() === 'error') jsErrors.push('console: ' + m.text());
  });
  await p.setViewport({ width: 1440, height: 900 });
  // 面板在「有未保存修改」时会 confirm()：不接管就整页 JS 挂起，
  // 表现为后面每一步都超时，看起来像页面卡死而不是弹了个框。
  p.on('dialog', (d) => d.accept());
  await p.goto(BASE + '/mcp/admin', { waitUntil: 'domcontentloaded', timeout: 60000 });
  await p.waitForSelector('#plist .item', { timeout: 30000 });
  return p;
};

const text = (p, sel) => p.$eval(sel, (el) => el.innerText.trim()).catch(() => '');
const value = (p, sel) => p.$eval(sel, (el) => el.value).catch(() => null);
const clickText = async (p, sel, label) => {
  for (const h of await p.$$(sel)) {
    if ((await h.evaluate((el) => el.innerText)).trim() === label) {
      await h.click();
      return true;
    }
  }
  return false;
};

// 在页面上下文里读管理 API：拿到的是面板自己那份登录态，
// 所以这条断言能证明「界面没串到别的数据」。
const apiGet = (p, path) => p.evaluate(async (target) => {
  const a = await fetch('/api/user/auth/refresh', { method: 'POST', credentials: 'same-origin' });
  const j = await a.json();
  const token = ((j || {}).data || {}).access_token;
  const r = await fetch(target, {
    headers: token ? { Authorization: 'Bearer ' + token } : {},
    credentials: 'same-origin',
  });
  return r.json();
}, path);

// 「三态」是带「未声明」选项的那个 select。#detail 里第一个 select 是产物类型，
// 用位置取会静默改错控件 —— 值看起来没变，断言却在报「落库不是 0」。
const triSelect = (p) => p.evaluateHandle(() =>
  [...document.querySelectorAll('#detail select')]
    .find((e) => [...e.options].some((o) => o.textContent === '未声明')));

const selectModel = async (p, model) => {
  await p.waitForSelector('#plist .item', { timeout: 30000 });
  await p.$$eval('#plist .item', (els, target) => {
    const hit = els.find((el) => el.querySelector('.item-name').innerText.trim() === target);
    if (hit) hit.click();
  }, model);
  await p.waitForFunction(
    (m) => (document.getElementById('detail') || {}).innerText?.includes(m),
    { timeout: 20000 }, model,
  );
};

try {
  // ── 1. 登录（给面板留下 refresh cookie） ──────────────────────
  view = await browser.newPage();
  view.on('pageerror', (e) => jsErrors.push(String(e.message || e)));
  await view.setViewport({ width: 1440, height: 900 });
  await view.goto(BASE + '/sign-in', { waitUntil: 'networkidle2', timeout: 60000 });
  await view.waitForSelector('input', { timeout: 30000 });
  const inputs = await view.$$('input');
  const pwIdx = await view.$$eval('input', (els) => els.findIndex((e) => e.type === 'password'));
  const userIdx = await view.$$eval('input', (els) =>
    els.findIndex((e) => e.type !== 'password' && e.type !== 'hidden'));
  await inputs[userIdx].click({ clickCount: 3 });
  await inputs[userIdx].type(USER, { delay: 15 });
  await inputs[pwIdx].click({ clickCount: 3 });
  await inputs[pwIdx].type(PASS, { delay: 15 });
  const submit = await view.$('button[type=submit]');
  if (submit) await submit.click();
  else await view.keyboard.press('Enter');
  await view.waitForFunction(() => !location.pathname.startsWith('/sign-in'), { timeout: 60000 });
  check('登录网关成功', true, view.url());
  await view.close();

  // ── 2. 面板：登录态复用 ──────────────────────────────────────
  view = await openPanel();
  await view.waitForFunction(
    () => (document.getElementById('summary') || {}).textContent?.includes('日志库'),
    { timeout: 30000 },
  ).catch(() => {});
  const summary = await text(view, '#summary');
  check('面板复用登录态（概览不是「未登录」）', summary.includes('日志库'), summary);
  check('概览显示模型数与启用数', /\d+ 个模型 · 已启用 \d+/.test(summary), summary);
  check('概览显示日志库类型', /日志库 (sql|clickhouse)/.test(summary), summary);
  check('标题正确', (await text(view, 'h1')) === 'MCP 能力注册', await text(view, 'h1'));
  // apiduo 新增的用户可见功能必须在界面上带标识，否则用户分不清哪些不是上游原生能力
  const mark = await view.$eval('[data-apiduo]', (el) => ({
    attr: el.getAttribute('data-apiduo'),
    text: el.textContent.trim(),
  })).catch(() => null);
  check('页面带 apiduo 扩展标识', !!mark && mark.text.includes('apiduo'), JSON.stringify(mark));
  check('标识不被读屏软件当正文（有 title 说明）',
    !!(mark && await view.$eval('[data-apiduo]', (el) => el.title)));

  // ── 3. 列表与详情 ───────────────────────────────────────────
  const items = await view.$$eval('#plist .item .item-name', (els) => els.map((e) => e.innerText.trim()));
  check('列表渲染出已注册模型', items.length > 0, `${items.length} 项：${items.slice(0, 4).join(', ')}`);
  check('列表含目标模型', items.includes(MODEL), items.join(', '));

  await selectModel(view, MODEL);
  check('点击后详情打开', (await value(view, '#f-model_id')) === MODEL, await value(view, '#f-model_id'));
  check('模型 ID 在编辑态是只读（改名等于换模型）',
    await view.$eval('#f-model_id', (el) => el.readOnly));
  check('三态控件存在（未声明/不支持/上限）',
    (await view.$$eval('#detail select',
      (els) => els.filter((e) => [...e.options].some((o) => o.textContent === '未声明')).length)) >= 2);

  // ── 4. 编辑并保存（用新标签页验证持久化） ───────────────────
  const stamp = 'UI 改名 ' + Date.now().toString().slice(-5);
  await view.$eval('#f-display_name', (el) => {
    el.value = '';
    el.dispatchEvent(new Event('input', { bubbles: true }));
  });
  await view.click('#f-display_name');
  await view.type('#f-display_name', stamp, { delay: 10 });
  check('找到保存按钮', await clickText(view, '#detail button', '保存'));
  await view.waitForFunction(
    () => !document.querySelector('#err.on'), { timeout: 15000 },
  ).catch(() => {});
  const errAfterSave = await text(view, '#err');
  check('保存没有报错', errAfterSave === '', errAfterSave.slice(0, 120));

  const far = await apiGet(view, '/api/mcp/profiles');
  const savedRow = ((far.data || {}).items || []).find((x) => x.model_id === MODEL) || {};
  check('保存真的落库（管理 API 回读到新值）', savedRow.display_name === stamp,
    String(savedRow.display_name));

  const fresh = await openPanel();
  await selectModel(fresh, MODEL);
  check('新开页面仍显示新值（不是本地态）', (await value(fresh, '#f-display_name')) === stamp,
    await value(fresh, '#f-display_name'));
  await view.close();
  view = fresh;

  // ── 5. 参数 JSON 非法时被拦在本地 ───────────────────────────
  await view.$eval('#f-parameters', (el) => {
    el.value = '{not json';
    el.dispatchEvent(new Event('input', { bubbles: true }));
  });
  await clickText(view, '#detail button', '保存');
  await view.waitForSelector('#err.on', { timeout: 8000 }).catch(() => {});
  const errText = await text(view, '#err');
  check('非法参数 JSON 被拦并给出原因', errText.includes('JSON'), errText.slice(0, 120));
  const stillOld = await apiGet(view, `/api/mcp/profiles`);
  const rowNow = ((stillOld.data || {}).items || []).find((x) => x.model_id === MODEL) || {};
  check('被拦下的修改没有写进库', rowNow.parameters !== '{not json', String(rowNow.parameters).slice(0, 60));

  // ── 6. 三态：改成「不支持」并存住 ──────────────────────────
  // $eval 的回调在浏览器上下文执行，Node 侧的变量必须显式传参
  await view.$eval('#f-parameters', (el, val) => {
    el.value = val;
    el.dispatchEvent(new Event('input', { bubbles: true }));
  }, rowNow.parameters || '');
  const tri = await triSelect(view);
  await tri.asElement().select('zero');
  await clickText(view, '#detail button', '保存');
  await sleep(1500);
  const afterZero = await apiGet(view, `/api/mcp/profiles`);
  const zeroRow = ((afterZero.data || {}).items || []).find((x) => x.model_id === MODEL) || {};
  check('三态「不支持」落库成 0（不是 null）', zeroRow.max_ref_images === 0,
    JSON.stringify(zeroRow.max_ref_images));

  const fresh2 = await openPanel();
  await selectModel(fresh2, MODEL);
  const tri2 = await triSelect(fresh2);
  check('重开后三态回显为「不支持」',
    (await tri2.asElement().evaluate((el) => el.value)) === 'zero',
    await tri2.asElement().evaluate((el) => el.value));
  // 还原成上限 2，避免影响后续套件
  await tri2.asElement().evaluate((el) => {
    el.value = 'n';
    el.dispatchEvent(new Event('change', { bubbles: true }));
  });
  await fresh2.$eval('#f-max_ref_images-n', (el) => {
    el.value = '2';
    el.dispatchEvent(new Event('input', { bubbles: true }));
  });
  await clickText(fresh2, '#detail button', '保存');
  await sleep(1500);
  await view.close();
  view = fresh2;

  // ── 7. 启停 ────────────────────────────────────────────────
  check('详情标题是模型名', (await text(view, '#detail .card-title h2')) === MODEL,
    await text(view, '#detail .card-title h2'));
  if (await clickText(view, '#detail button', '停用')) {
    await sleep(1800);
    const badge = await view.$$eval('#plist .item', (els, m) =>
      els.filter((el) => el.querySelector('.item-name').innerText.trim() === m)
        .map((el) => (el.querySelector('.badge') || {}).innerText || '')[0], MODEL);
    check('停用后列表出现「停用」标记', badge === '停用', String(badge));
    const afterToggle = await apiGet(view, '/api/mcp/profiles');
    const togRow = ((afterToggle.data || {}).items || []).find((x) => x.model_id === MODEL) || {};
    check('停用真的落库', togRow.enabled === false, JSON.stringify(togRow.enabled));
    await clickText(view, '#detail button', '启用');
    await sleep(1500);
    const reOn = await apiGet(view, '/api/mcp/profiles');
    const onRow = ((reOn.data || {}).items || []).find((x) => x.model_id === MODEL) || {};
    check('再点启用能翻回来', onRow.enabled === true, JSON.stringify(onRow.enabled));
  } else {
    check('找到停用按钮', false);
  }

  // ── 8. 错误规则页 ──────────────────────────────────────────
  await view.click('nav button[data-tab="errors"]');
  await view.waitForSelector('#rules-global table tbody tr', { timeout: 20000 });
  const ruleCount = await view.$$eval('#rules-global table tbody tr', (els) => els.length);
  check('全局规则表渲染', ruleCount >= 7, `${ruleCount} 行`);
  const matches = await view.$$eval('#rules-global table tbody tr td:first-child', (els) =>
    els.map((e) => e.innerText.trim()));
  check('规则覆盖 429 / 5xx', matches.includes('429') && matches.includes('5xx'), matches.join(','));
  check('规则行带可重试下拉', (await view.$$('#rules-global select')).length > 0);

  const firstSave = (await view.$$('button[data-save]'))[0];
  if (firstSave) {
    await firstSave.click();
    await view.waitForFunction(
      () => [...document.querySelectorAll('button[data-save]')].some((b) => b.textContent === '已存'),
      { timeout: 10000 },
    ).catch(() => {});
    const savedBtns = await view.$$eval('button[data-save]', (els) => els.map((b) => b.innerText));
    check('保存规则后有「已存」回执', savedBtns.includes('已存'), savedBtns.slice(0, 4).join(','));
  }

  // ── 9. 观察页与巡检页 ──────────────────────────────────────
  await view.click('nav button[data-tab="obs"]');
  await view.waitForFunction(
    () => !(document.getElementById('obs-body') || {}).innerText?.includes('查询中'),
    { timeout: 20000 },
  ).catch(() => {});
  const obsText = await text(view, '#obs-body');
  const obsHint = await text(view, '#obs-hint');
  check('观察页给出样本数', /近 7 天 \d+ 次样本/.test(obsHint), obsHint);
  check('观察页有分布或明确的空态', obsText.length > 0 && !/查询失败/.test(obsText),
    obsText.replace(/\s+/g, ' ').slice(0, 120));

  await view.click('nav button[data-tab="audit"]');
  await view.waitForFunction(
    () => !(document.getElementById('audit-body') || {}).innerText?.includes('运行中'),
    { timeout: 20000 },
  ).catch(() => {});
  const auditText = await text(view, '#audit-body');
  check('巡检页有结论或明确的空态', auditText.length > 0 && !/巡检失败/.test(auditText),
    auditText.replace(/\s+/g, ' ').slice(0, 120));

  // ── 10. 新建 → 删除闭环 ────────────────────────────────────
  await view.click('nav button[data-tab="models"]');
  await view.click('#btn-new');
  await view.waitForSelector('#f-model_id', { timeout: 10000 });
  const newModel = 'mcp-ui-temp-' + Date.now().toString().slice(-6);
  check('新建态模型 ID 可编辑',
    (await view.$eval('#f-model_id', (el) => el.readOnly)) === false);
  await view.type('#f-model_id', newModel, { delay: 10 });
  await view.select('#f-kind', 'video');
  await sleep(300);
  check('切换产物类型会自动改协议',
    (await value(view, '#f-protocol')) === 'openai_video', await value(view, '#f-protocol'));
  await clickText(view, '#detail button', '保存');
  await view.waitForFunction(
    (m) => [...document.querySelectorAll('#plist .item .item-name')].some((e) => e.innerText.trim() === m),
    { timeout: 20000 }, newModel,
  ).catch(() => {});
  const listNow = await view.$$eval('#plist .item .item-name', (els) => els.map((e) => e.innerText.trim()));
  check('新建后出现在列表', listNow.includes(newModel), listNow.slice(-3).join(','));

  const created = await apiGet(view, '/api/mcp/profiles');
  const newRow = ((created.data || {}).items || []).find((x) => x.model_id === newModel) || {};
  check('新建落库且协议正确', newRow.protocol === 'openai_video', JSON.stringify(newRow.protocol));
  // collect() 漏 enabled 时，新建出来的模型默认停用：列表里看不出，agent 那边直接看不到
  check('新建的模型默认是启用的', newRow.enabled === true, JSON.stringify(newRow.enabled));

  await clickText(view, '#detail button', '删除');
  await view.waitForFunction(
    (m) => ![...document.querySelectorAll('#plist .item .item-name')].some((e) => e.innerText.trim() === m),
    { timeout: 20000 }, newModel,
  ).catch(() => {});
  const afterDel = await view.$$eval('#plist .item .item-name', (els) => els.map((e) => e.innerText.trim()));
  check('删除后从列表消失', !afterDel.includes(newModel), afterDel.slice(-3).join(','));
  const gone = await apiGet(view, '/api/mcp/profiles');
  check('删除是软删/真删都已从库里消失',
    !((gone.data || {}).items || []).some((x) => x.model_id === newModel));

  // ── 11. 多视口 / 深色 / 截图 ───────────────────────────────
  const shots = [];
  for (const [w, h, name] of [[1440, 900, 'desktop'], [1024, 800, 'tablet'], [390, 844, 'mobile']]) {
    await view.setViewport({ width: w, height: h });
    await view.goto(BASE + '/mcp/admin', { waitUntil: 'domcontentloaded' });
    await view.waitForSelector('#plist .item', { timeout: 30000 });
    const overflow = await view.evaluate(() =>
      document.documentElement.scrollWidth - document.documentElement.clientWidth);
    check(`${name} ${w}px 无横向溢出`, overflow <= 1, `overflow=${overflow}px`);
    const path = `${ART}/mcp-panel-${name}.png`;
    await view.screenshot({ path, fullPage: true });
    shots.push(path);
  }

  await view.setViewport({ width: 1440, height: 900 });
  await view.emulateMediaFeatures([{ name: 'prefers-color-scheme', value: 'dark' }]);
  await view.goto(BASE + '/mcp/admin', { waitUntil: 'domcontentloaded' });
  await view.waitForSelector('#plist .item', { timeout: 30000 });
  const dark = await view.evaluate(() => {
    const s = getComputedStyle(document.body);
    return { bg: s.backgroundColor, fg: s.color };
  });
  const lum = (rgb) => {
    const m = rgb.match(/\d+/g) || [0, 0, 0];
    return (Number(m[0]) * 299 + Number(m[1]) * 587 + Number(m[2]) * 114) / 1000;
  };
  check('深色模式下背景真的变深', lum(dark.bg) < 60, JSON.stringify(dark));
  check('深色模式下文字是浅色（可读）', lum(dark.fg) > 180, JSON.stringify(dark));
  const darkShot = `${ART}/mcp-panel-dark.png`;
  await view.screenshot({ path: darkShot, fullPage: true });
  shots.push(darkShot);

  // ── 12. 未登录时的自解释 ──────────────────────────────────
  const incognito = await browser.createBrowserContext();
  const anon = await incognito.newPage();
  await anon.setViewport({ width: 1440, height: 900 });
  await anon.goto(BASE + '/mcp/admin', { waitUntil: 'domcontentloaded' });
  await anon.waitForFunction(
    () => (document.getElementById('summary') || {}).textContent === '未登录', { timeout: 20000 },
  ).catch(() => {});
  const anonSummary = await anon.$eval('#summary', (el) => el.textContent.trim()).catch(() => '');
  const anonErr = await anon.$eval('#err', (el) => el.innerText.trim()).catch(() => '');
  check('未登录时概览写「未登录」', anonSummary === '未登录', anonSummary);
  check('未登录时给出可操作提示', anonErr.includes('登录'), anonErr.slice(0, 80));
  await anon.screenshot({ path: `${ART}/mcp-panel-anon.png`, fullPage: true });
  await incognito.close();

  check('运行期间没有 JS 异常', jsErrors.length === 0, jsErrors.slice(0, 3).join(' | '));
} catch (err) {
  check('脚本执行完成', false, String(err).slice(0, 300));
} finally {
  await browser.close();
}

const failed = rows.filter(([ok]) => !ok);
console.log('');
if (failed.length) {
  console.log(`MCP 面板套件：${rows.length - failed.length} 通过 / ${failed.length} 失败`);
  for (const [, name] of failed) console.log('  失败：' + name);
  process.exit(1);
}
console.log(`MCP 面板套件：${rows.length} 条全通过`);
