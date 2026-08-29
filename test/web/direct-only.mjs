import { chromium } from 'playwright';
import fs from 'fs';

const BASE = process.env.ROOST_URL || 'http://hosted:8080';
const OUT = process.env.OUT_DIR || '/out';
const TOKENS = {
  direct: 'canary-direct-session-token-000000000',
  migration: 'canary-migration-session-token-0000000',
  pro: 'canary-pro-session-token-000000000000',
};
const results = { base: BASE, steps: [], consoleErrors: [], pageErrors: [], failedRequests: [] };

function record(name, ok, note = '') {
  results.steps.push({ name, ok, note });
  console.log(`[${ok ? 'PASS' : 'FAIL'}] ${name}${note ? ' — ' + note : ''}`);
}

async function contextFor(browser, token) {
  const context = await browser.newContext({ viewport: { width: 1280, height: 800 } });
  await context.addCookies([{ name: 'wt_session', value: token, url: BASE }]);
  return context;
}

const browser = await chromium.launch({ args: ['--disable-dev-shm-usage', '--no-sandbox'] });
fs.mkdirSync(OUT, { recursive: true });
try {
  const direct = await contextFor(browser, TOKENS.direct);
  await direct.addInitScript(function() {
    // Context init scripts also run in the opaque-origin preview iframe. Seed
    // portal cache state only in the top-level app document; localStorage is
    // intentionally unavailable to the sandboxed preview.
    if (window.top !== window) return;
    localStorage.setItem('wt_wings', JSON.stringify([{
      wing_id: 'cached-direct-wing',
      public_key: 'cached-public-key',
      wing_label: 'cached direct wing',
      hostname: 'cached-host',
      agents: ['claude'],
    }]));
  });
  const page = await direct.newPage();
  const directFrames = [];
  page.on('websocket', (socket) => {
    socket.on('framesent', (event) => directFrames.push(String(event.payload)));
  });
  page.on('console', (msg) => { if (msg.type() === 'error') results.consoleErrors.push(msg.text()); });
  page.on('pageerror', (err) => results.pageErrors.push(String(err)));
  page.on('requestfailed', (req) => {
    const failure = req.failure();
    if (failure && failure.errorText !== 'net::ERR_ABORTED') {
      results.failedRequests.push({ url: req.url().slice(0, 200), error: failure.errorText });
    }
  });
  await page.goto(BASE + '/app/', { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#direct-agent-setup', { state: 'visible', timeout: 15000 });

  const meResp = await direct.request.get(BASE + '/api/app/me');
  const me = await meResp.json();
  record('direct free: API denies hosted relay with explicit reason',
    meResp.ok() && me.relay_allowed === false && me.relay_reason === 'direct-only-free' && me.default_transport === 'direct' && me.self_service_plans === false,
    JSON.stringify(me));
  record('direct free: readiness page replaces terminal controls',
    await page.locator('#direct-agent-setup').isVisible() && !(await page.locator('#new-session-btn').isVisible()) && !(await page.locator('#canvas-toggle-btn').isVisible()));
  record('direct free: empty state does not advertise a forbidden browser launch',
    (await page.locator('#empty-no-sessions-title').innerText()).includes('ready for direct agent control') &&
      (await page.locator('#empty-no-sessions-hint').innerText()).includes('MCP connector') &&
      !(await page.locator('#empty-no-sessions').innerText()).includes('press . to start'));
  const setupText = await page.locator('#direct-agent-setup').innerText();
  record('direct free: setup gives both native connector commands without fallback claim',
    setupText.includes('wt mcp connect --client codex') && setupText.includes('wt mcp connect --client claude') && !setupText.includes('relay fallback'));

  await page.goto(BASE + '/app/#canvas', { waitUntil: 'domcontentloaded' });
  await page.waitForTimeout(500);
  record('direct free: canvas deep link fails closed to home',
    await page.locator('#home-section').isVisible() &&
      !(await page.locator('#canvas-section').isVisible()) &&
      !page.url().includes('#canvas'));

  await page.goto(BASE + '/app/#w/cached-direct-wing', { waitUntil: 'domcontentloaded' });
  await page.waitForTimeout(500);
  record('direct free: wing deep link fails closed and removes forbidden route',
    await page.locator('#home-section').isVisible() &&
      !(await page.locator('#wing-detail-section').isVisible()) &&
      !page.url().includes('#w/'));

  await page.locator('.wing-box[data-wing-id="cached-direct-wing"]').click();
  record('direct free: cached wing cards cannot expose browser terminal controls',
    await page.locator('#home-section').isVisible() &&
      !(await page.locator('#wing-detail-section').isVisible()) &&
      !(await page.locator('#new-session-btn').isVisible()) &&
      !(await page.locator('#canvas-toggle-btn').isVisible()) &&
      !page.url().includes('#w/'));

  await page.keyboard.press('Control+K');
  record('direct free: keyboard launcher remains unavailable',
    !(await page.locator('#command-palette').isVisible()) &&
      !(await page.locator('#new-session-btn').isVisible()));

  await page.goto(BASE + '/app/#account', { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#account-plan-note', { state: 'visible', timeout: 15000 });
  record('direct free: account explains provisioning without an upgrade control',
    (await page.locator('#account-plan-note').innerText()).includes('hosted relay is provisioned separately') &&
      (await page.locator('#account-upgrade').count()) === 0);

  const upgradeResp = await direct.request.post(BASE + '/api/app/upgrade');
  const upgradeBody = await upgradeResp.json();
  const afterUpgradeResp = await direct.request.get(BASE + '/api/app/me');
  const afterUpgrade = await afterUpgradeResp.json();
  record('direct free: plan mutation API cannot self-grant relay access',
    upgradeResp.status() === 403 &&
      typeof upgradeBody.error === 'string' &&
      afterUpgradeResp.ok() &&
      afterUpgrade.tier === 'free' &&
      afterUpgrade.relay_allowed === false,
    JSON.stringify({ upgrade: upgradeBody, after: afterUpgrade }));
  const forbiddenFrames = directFrames.filter((payload) => {
    try {
      const message = JSON.parse(payload);
      return message.purpose === 'wing-control' ||
        (typeof message.type === 'string' && message.type.startsWith('pty.'));
    } catch (_) {
      return false;
    }
  });
  record('direct free: browser sends no hosted terminal or general-control frames',
    forbiddenFrames.length === 0,
    forbiddenFrames.join('\n').slice(0, 500));
  await page.screenshot({ path: `${OUT}/direct-free-readiness.png`, fullPage: false });
  await direct.close();

  for (const [name, expectedReason] of [['migration', 'temporary-migration'], ['pro', 'pro']]) {
    const context = await contextFor(browser, TOKENS[name]);
    const response = await context.request.get(BASE + '/api/app/me');
    const body = await response.json();
    record(`${name}: hosted relay entitlement remains available`,
      response.ok() && body.relay_allowed === true && body.relay_reason === expectedReason,
      JSON.stringify(body));
    await context.close();
  }
} finally {
  await browser.close();
  const failed = results.steps.filter((step) => !step.ok).length;
  results.summary = {
    total: results.steps.length,
    failed,
    consoleErrors: results.consoleErrors.length,
    pageErrors: results.pageErrors.length,
    failedRequests: results.failedRequests.length,
  };
  fs.writeFileSync(`${OUT}/direct-results.json`, JSON.stringify(results, null, 2));
  process.exit(failed || results.consoleErrors.length || results.pageErrors.length || results.failedRequests.length ? 1 : 0);
}
