/**
 * 公告功能的 UI 层端到端判据。
 *
 * 前置：seed_announcement_ui.py 已造好「E2E 界面公告」与「E2E 功能更新」，
 * 浏览器已登录（脚本内处理）。
 *
 * 只做断言，不修任何东西。退出码 0 = 全通过。
 */
import { writeFileSync } from 'node:fs';

const CDP = process.env.CDP_PORT || 9241;
const results = [];
const check = (name, ok, detail = '') => {
  results.push({ name, ok: Boolean(ok), detail });
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${!ok && detail ? '  << ' + detail : ''}`);
};

const list = await (await fetch(`http://127.0.0.1:${CDP}/json/list`)).json();
const page = list.find((t) => t.type === 'page');
const ws = new WebSocket(page.webSocketDebuggerUrl);
let id = 0;
const pend = new Map();
ws.onmessage = (e) => {
  const m = JSON.parse(e.data);
  if (m.id && pend.has(m.id)) {
    pend.get(m.id)(m.result);
    pend.delete(m.id);
  }
};
await new Promise((r) => (ws.onopen = r));
const send = (method, params) => {
  const mid = ++id;
  ws.send(JSON.stringify({ id: mid, method, params }));
  return new Promise((r) => pend.set(mid, r));
};
const ev = async (expr) => {
  const r = await send('Runtime.evaluate', { expression: expr, returnByValue: true });
  return r.result?.value;
};

await send('Emulation.setDeviceMetricsOverride', {
  width: 1440,
  height: 1200,
  deviceScaleFactor: 2,
  mobile: false,
});
await send('Page.enable');
await send('Page.navigate', { url: 'http://localhost:3000/dashboard/overview' });
await new Promise((r) => setTimeout(r, 6500));

const where = await ev(`JSON.stringify({p:location.pathname,q:!!document.querySelector('input[type="password"]')})`);
if (String(where).includes('"q":true')) {
  await ev(`(function () {
    var set = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set;
    var put = function (n, v) {
      var el = document.querySelector('input[name="' + n + '"]');
      if (!el) return false;
      set.call(el, v); el.dispatchEvent(new Event('input', { bubbles: true })); return true;
    };
    put('username', 'preview'); put('password', 'Passw0rd!2026');
    var b = Array.prototype.slice.call(document.querySelectorAll('button'))
      .filter(function (x) { return x.textContent.trim() === '登录'; })[0];
    if (b) b.click(); return 1;
  })()`);
  await new Promise((r) => setTimeout(r, 7000));
}

// 清已读，让横幅必然出现
await ev(`localStorage.removeItem('notification-storage')`);
await send('Page.navigate', { url: 'http://localhost:3000/dashboard/overview' });
await new Promise((r) => setTimeout(r, 6500));

// ---- 1. 列表渲染 ----
const row = await ev(`(function () {
  var vp = document.querySelector('[data-slot="scroll-area-viewport"]');
  if (!vp) return 'no viewport';
  var rows = [...vp.querySelectorAll('button')];
  var hit = rows.filter(function (b) { return b.textContent.indexOf('E2E 界面公告') >= 0; })[0];
  if (!hit) return 'not found';
  return JSON.stringify({
    text: hit.innerText.replace(/\\n/g, ' | '),
    hasWarning: hit.textContent.indexOf('警告') >= 0,
  });
})()`);
let parsed = {};
try { parsed = JSON.parse(String(row)); } catch { parsed = {}; }
check('列表出现「E2E 界面公告」', parsed.text !== undefined, String(row));
check('  列表带「警告」级别', parsed.hasWarning === true, JSON.stringify(parsed));
check('  列表带日期', /\d{2}-\d{2} \d{2}:\d{2}/.test(parsed.text || ''), parsed.text);

// ---- 1b. 公告面板不得混入 changelog ----
// 这条是 bsk 那边先发现的：useAnnouncements 一开始没按 category 过滤，
// changelog 会同时出现在公告面板和横幅里。CDP 判据原先漏了这条断言。
const panelHasChangelog = await ev(`(function () {
  var vp = document.querySelector('[data-slot="scroll-area-viewport"]');
  if (!vp) return 'no viewport';
  return [...vp.querySelectorAll('button')].some(function (b) {
    return b.textContent.indexOf('E2E 功能更新') >= 0;
  });
})()`);
check('公告面板不含 changelog', panelHasChangelog === false, String(panelHasChangelog));

// ---- 2. 溢出提示（当前 7 条，h-72 放不下） ----
const hint = await ev(`(function () {
  var t = document.body.innerText;
  var m = t.match(/还有 (\\d+) 条/);
  return m ? m[1] : null;
})()`);
check('列表溢出提示存在', hint !== null, `hint=${hint}`);

// ---- 3. 详情弹窗 ----
await ev(`(function () {
  var vp = document.querySelector('[data-slot="scroll-area-viewport"]');
  var hit = [...vp.querySelectorAll('button')].filter(function (b) {
    return b.textContent.indexOf('E2E 界面公告') >= 0;
  })[0];
  if (hit) hit.click();
  return 1;
})()`);
await new Promise((r) => setTimeout(r, 1500));

const dialog = await ev(`(function () {
  var d = document.querySelector('[role="dialog"]');
  if (!d) return 'no dialog';
  var ol = d.querySelector('ol');
  var items = ol ? [...ol.querySelectorAll('li')] : [];
  var heading = (d.querySelector('h2') || {}).textContent || '';
  return JSON.stringify({
    heading: heading.trim(),
    phases: items.map(function (li) { return li.innerText.replace(/\\n/g, ' / '); }),
    hasExtra: d.innerText.indexOf('备注：验证用') >= 0,
  });
})()`);
let dlg = {};
try { dlg = JSON.parse(String(dialog)); } catch { dlg = {}; }
check('弹窗标题即公告标题', dlg.heading === 'E2E 界面公告', String(dialog).slice(0, 120));
check('  时间线 3 个阶段', (dlg.phases || []).length === 3, JSON.stringify(dlg.phases));
// 第二个阶段的时间是「10 分钟前」，按 reachedIndex 的规则它才是当前阶段。
// 这里必须断言具体位置 —— 只查「有没有当前」会让任何三阶段时间线都通过。
check('  「当前」落在第二个阶段',
  String((dlg.phases || [])[1] || '').includes('当前'),
  JSON.stringify(dlg.phases));
check('  之后的阶段不带「当前」',
  (dlg.phases || []).slice(2).every((p) => !p.includes('当前')),
  JSON.stringify(dlg.phases));
check('  附加信息显示', dlg.hasExtra === true);
const shot1 = await send('Page.captureScreenshot', { format: 'png' });
writeFileSync('/tmp/e2e-ui-dialog.png', Buffer.from(shot1.data, 'base64'));

// 关掉弹窗
await ev(`(function () {
  var d = document.querySelector('[role="dialog"]');
  if (!d) return 1;
  var b = [...d.querySelectorAll('button')].filter(function (x) {
    return x.getAttribute('aria-label') === '关闭' || x.textContent.trim() === '关闭';
  })[0];
  if (b) b.click();
  return 1;
})()`);
await new Promise((r) => setTimeout(r, 900));

// ---- 4. 功能更新横幅 ----
const banner = await ev(`(function () {
  var t = document.body.innerText;
  var m = t.match(/(\\d+) 项功能更新/);
  return m ? m[1] : null;
})()`);
check('横幅显示功能更新数量', banner !== null, `banner=${banner}`);

await ev(`(function () {
  var b = [...document.querySelectorAll('button')].filter(function (x) {
    return x.textContent.trim() === '查看';
  })[0];
  if (b) b.click(); return 1;
})()`);
await new Promise((r) => setTimeout(r, 900));

const clRow = await ev(`(function () {
  var li = [...document.querySelectorAll('ul li')].filter(function (x) { return x.querySelector('time'); });
  var hit = li.filter(function (x) { return x.innerText.indexOf('E2E 功能更新') >= 0; })[0];
  if (!hit) return 'not found';
  return hit.innerText.replace(/\\n/g, ' | ');
})()`);
check('横幅里出现「E2E 功能更新」', String(clRow).includes('E2E 功能更新'), String(clRow).slice(0, 80));
check('  带「新增」类型标签', String(clRow).includes('新增'), String(clRow).slice(0, 80));
check('  带 NEW 标记', String(clRow).includes('NEW'), String(clRow).slice(0, 80));
const shot2 = await send('Page.captureScreenshot', { format: 'png' });
writeFileSync('/tmp/e2e-ui-banner.png', Buffer.from(shot2.data, 'base64'));

// ---- 5. 关闭横幅 ----
await ev(`(function () {
  var b = [...document.querySelectorAll('button[aria-label="关闭"]')];
  if (b.length) b[0].click();
  return 1;
})()`);
await new Promise((r) => setTimeout(r, 900));
const afterClose = await ev(`(function () {
  var m = document.body.innerText.match(/(\\d+) 项功能更新/);
  return m ? m[1] : null;
})()`);
check('点关闭后横幅立即消失（不刷新）', afterClose === null, `still=${afterClose}`);

// ---- 汇总 ----
const total = results.length;
const passed = results.filter((r) => r.ok).length;
console.log(`\n${passed}/${total} passed`);
if (passed !== total) {
  console.log('失败项：');
  for (const r of results) if (!r.ok) console.log(`  - ${r.name}: ${r.detail}`);
}
ws.close();
process.exit(passed === total ? 0 : 1);
