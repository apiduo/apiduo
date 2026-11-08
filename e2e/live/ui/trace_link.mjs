// 浏览器端验证「在 Langfuse 打开」跳转：真实 Chrome + 真实点击。
//
// 单元测试只能证明 URL 拼接函数正确，证明不了「这个链接真的出现在日志详情里、
// 且带着这条日志的 trace id」。这里登录真网关、点开行、读 DOM。
import { createRequire } from 'module';
import { dirname, resolve } from 'path';
import { fileURLToPath } from 'url';

const here = dirname(fileURLToPath(import.meta.url));
const repo = resolve(here, '../../..');
const require = createRequire(resolve(repo, 'web/package.json'));
const puppeteer = require('puppeteer-core');

const BASE = process.env.E2E_BASE || 'http://127.0.0.1:39410';
const CONSOLE = (process.env.E2E_CONSOLE || 'http://127.0.0.1:39402').replace(/\/+$/, '');
const PROJECT = process.env.E2E_PROJECT_ID || 'e2e-project';
const USER = process.env.E2E_ADMIN || 'root';
const PASS = process.env.E2E_ADMIN_PW || 'E2ePassw0rd!2026';
const CHROME = process.env.E2E_CHROME || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';

const browser = await puppeteer.launch({
  executablePath: CHROME,
  headless: true,
  args: ['--no-sandbox', '--no-proxy-server', '--disable-gpu', '--window-size=1440,900'],
});
const page = await browser.newPage();
await page.setViewport({ width: 1440, height: 900 });

const closeDialog = async () => {
  await page.keyboard.press('Escape');
  // 每行都渲染自己的 DetailsDialog，遮罩会吞掉下一次点击，必须等它消失
  try {
    await page.waitForFunction(() => !document.querySelector('[role=dialog]'), { timeout: 3000 });
  } catch {}
};

try {
  await page.goto(BASE + '/sign-in', { waitUntil: 'networkidle2', timeout: 60000 });
  await page.waitForSelector('input', { timeout: 30000 });
  const inputs = await page.$$('input');
  const passwordIdx = await page.$$eval('input', (els) => els.findIndex((e) => e.type === 'password'));
  const userIdx = await page.$$eval('input', (els) => els.findIndex((e) => e.type !== 'password' && e.type !== 'hidden'));
  await inputs[userIdx].click({ clickCount: 3 });
  await inputs[userIdx].type(USER, { delay: 20 });
  await inputs[passwordIdx].click({ clickCount: 3 });
  await inputs[passwordIdx].type(PASS, { delay: 20 });
  const submit = await page.$('button[type=submit]');
  if (submit) await submit.click();
  else await page.keyboard.press('Enter');
  await page.waitForFunction(() => !location.pathname.startsWith('/sign-in'), { timeout: 60000 });

  await page.goto(BASE + '/usage-logs', { waitUntil: 'networkidle2', timeout: 60000 });
  await page.waitForSelector('table tbody tr', { timeout: 60000 });
  const rowCount = await page.$$eval('table tbody tr', (rows) => rows.length);

  let link = null;
  for (let i = 0; i < Math.min(rowCount, 10) && !link; i++) {
    const rows = await page.$$('table tbody tr');
    const btn = await rows[i].$('button.group');
    if (!btn) continue;
    try {
      await btn.click();
      await page.waitForSelector('[role=dialog]', { timeout: 5000 });
    } catch {
      await closeDialog();
      continue;
    }
    link = await page.evaluate(() => {
      const dialog = document.querySelector('[role=dialog]');
      if (!dialog) return null;
      const anchor = [...dialog.querySelectorAll('a')].find((a) => /langfuse/i.test(a.textContent || ''));
      return anchor ? { text: anchor.textContent.trim(), href: anchor.href, rel: anchor.rel, target: anchor.target } : null;
    });
    if (!link) await closeDialog();
  }

  if (!link) {
    await page.screenshot({ path: '/tmp/apiduo-e2e/ui-fail.png' });
    console.log('FAIL 日志详情里没有 Langfuse 链接');
    process.exit(1);
  }
  await page.screenshot({ path: '/tmp/apiduo-e2e/ui-dialog.png' });

  const checks = {
    text: /langfuse/i.test(link.text),
    host: link.href.startsWith(CONSOLE + '/'),
    project: link.href.includes('/project/' + PROJECT + '/'),
    traceId: /\/traces\/[0-9a-f]{32}(\?|$)/.test(link.href),
    safe: link.rel.includes('noopener') && link.target === '_blank',
  };
  console.log('  link:', JSON.stringify(link));
  console.log('  checks:', JSON.stringify(checks));
  const ok = Object.values(checks).every(Boolean);
  console.log(ok ? 'PASS' : 'FAIL');
  process.exit(ok ? 0 : 1);
} finally {
  await browser.close();
}
