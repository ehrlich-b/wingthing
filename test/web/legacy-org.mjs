// Backward-compatibility canary for existing shared-roost deployments that do
// not configure WT_ROOST_ALLOWED_EMAILS. Historically every authenticated
// OAuth account could use the embedded service wing; the new opt-in enrollment
// boundary must leave that exact empty-configuration behavior unchanged.
import { chromium } from 'playwright';
import fs from 'fs';

const BASE = process.env.ROOST_URL || 'http://roost:8080';
const OUT = process.env.OUT_DIR || '/out';
const TOKEN = 'canary-dave-session-token-00000000004';
const results = { base: BASE, steps: [], consoleErrors: [], pageErrors: [], failedRequests: [] };

function record(name, ok, note = '') {
  results.steps.push({ name, ok, note });
  console.log(`[${ok ? 'PASS' : 'FAIL'}] ${name}${note ? ' — ' + note : ''}`);
}

fs.mkdirSync(OUT, { recursive: true });
const browser = await chromium.launch({ args: ['--disable-dev-shm-usage', '--no-sandbox'] });

try {
  const context = await browser.newContext({ viewport: { width: 1280, height: 800 } });
  await context.addCookies([{ name: 'wt_session', value: TOKEN, url: BASE }]);
  const page = await context.newPage();
  page.on('console', (message) => {
    if (message.type() === 'error') results.consoleErrors.push(message.text().slice(0, 500));
  });
  page.on('pageerror', (error) => results.pageErrors.push(String(error).slice(0, 500)));
  page.on('requestfailed', (request) => {
    const failure = request.failure();
    if (failure && failure.errorText !== 'net::ERR_ABORTED') {
      results.failedRequests.push({ url: request.url().slice(0, 200), error: failure.errorText });
    }
  });

  const meResponse = await context.request.get(BASE + '/api/app/me');
  const meBody = await meResponse.json().catch(() => ({}));
  record('legacy org: existing authenticated account remains admitted without an allowlist',
    meResponse.ok() && meBody.id === 'u-dave' && meBody.roost_mode === true,
    `status=${meResponse.status()} id=${JSON.stringify(meBody.id)} roost_mode=${JSON.stringify(meBody.roost_mode)}`);

  const wingsResponse = await context.request.get(BASE + '/api/app/wings');
  const wingsBody = await wingsResponse.json().catch(() => []);
  record('legacy org: admitted account can access the embedded shared wing',
    wingsResponse.ok() && Array.isArray(wingsBody) && wingsBody.length >= 1,
    `status=${wingsResponse.status()} wings=${Array.isArray(wingsBody) ? wingsBody.length : 'invalid'}`);

  try {
    await page.goto(BASE + '/app/', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#wing-status .wing-box', { timeout: 30000 });
    record('legacy org: dashboard renders the shared wing', true);
  } catch (error) {
    record('legacy org: dashboard renders the shared wing', false, String(error).slice(0, 200));
  }
  await page.screenshot({ path: `${OUT}/legacy-org-dashboard.png`, fullPage: false });
  await context.close();
} finally {
  await browser.close();
}

record('legacy org: no browser console errors', results.consoleErrors.length === 0,
  JSON.stringify(results.consoleErrors).slice(0, 300));
record('legacy org: no uncaught page errors', results.pageErrors.length === 0,
  JSON.stringify(results.pageErrors).slice(0, 300));
record('legacy org: no failed requests', results.failedRequests.length === 0,
  JSON.stringify(results.failedRequests).slice(0, 300));

fs.writeFileSync(`${OUT}/legacy-org-results.json`, JSON.stringify(results, null, 2));
if (results.steps.some((step) => !step.ok)) process.exit(1);
