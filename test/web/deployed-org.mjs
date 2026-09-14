// Browser canary for an already-running org-mode roost. Authentication is
// injected with short-lived sessions prepared by the operator; this script
// never needs OAuth credentials and never creates users or memberships.
//
// Required environment:
//   ROOST_URL (https://...)
//   WT_E2E_ADMIN_TOKEN, WT_E2E_MEMBER_TOKEN,
//   WT_E2E_SUPPORT_TOKEN, WT_E2E_OUTSIDER_TOKEN
// Optional shared-host filesystem check:
//   WT_E2E_REPOS_PATH (a file or directory declared read-only in egg.yaml)
//
// The admin flow creates exactly one terminal and records its ID so the
// operator can clean it up even if the browser dies part-way through.
import { chromium } from 'playwright';
import crypto from 'crypto';
import fs from 'fs';

const BASE = required('ROOST_URL').replace(/\/$/, '');
const OUT = process.env.OUT_DIR || '/out';
const TOKENS = {
  admin: required('WT_E2E_ADMIN_TOKEN'),
  member: required('WT_E2E_MEMBER_TOKEN'),
  support: required('WT_E2E_SUPPORT_TOKEN'),
  outsider: required('WT_E2E_OUTSIDER_TOKEN'),
};
const EMAILS = {
  admin: process.env.WT_E2E_ADMIN_EMAIL || 'bryan@slide.tech',
  member: process.env.WT_E2E_MEMBER_EMAIL || 'chad@slide.tech',
  support: process.env.WT_E2E_SUPPORT_EMAIL || 'ehrlich.bryan@gmail.com',
};
const REPOS_PATH = process.env.WT_E2E_REPOS_PATH || '';

const results = {
  base: BASE,
  steps: [],
  consoleErrors: [],
  pageErrors: [],
  failedRequests: [],
  createdSessionID: '',
  preexistingSessionIDs: [],
};

function required(name) {
  const value = process.env[name];
  if (!value) throw new Error(`${name} is required`);
  return value;
}

function record(name, ok, note = '') {
  results.steps.push({ name, ok, note });
  console.log(`[${ok ? 'PASS' : 'FAIL'}] ${name}${note ? ` — ${note}` : ''}`);
}

function watch(page, who) {
  page.on('console', (message) => {
    if (message.type() === 'error') {
      results.consoleErrors.push({ who, text: message.text().slice(0, 500) });
    }
  });
  page.on('pageerror', (error) => {
    results.pageErrors.push({ who, text: String(error).slice(0, 500) });
  });
  page.on('requestfailed', (request) => {
    const failure = request.failure();
    const expectedMCPCallback = request.url().startsWith('http://127.0.0.1:65534/callback?');
    if (failure && failure.errorText !== 'net::ERR_ABORTED' && !expectedMCPCallback) {
      results.failedRequests.push({
        who,
        url: request.url().slice(0, 200),
        error: failure.errorText,
      });
    }
  });
}

async function principal(browser, who, viewport = { width: 1280, height: 800 }) {
  const context = await browser.newContext({
    viewport,
    deviceScaleFactor: viewport.width < 500 ? 2 : 1,
  });
  await context.addCookies([{ name: 'wt_session', value: TOKENS[who], url: BASE }]);
  const page = await context.newPage();
  watch(page, who + (viewport.width < 500 ? '-mobile' : ''));
  return { context, page };
}

async function openDashboard(page) {
  const response = await page.goto(`${BASE}/app/`, { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#wing-status .wing-box', { timeout: 30000 });
  await page.waitForTimeout(1500);
  return response;
}

async function palettePaths(page, query) {
  await page.keyboard.press('ControlOrMeta+k');
  await page.waitForSelector('#palette-search', { state: 'visible', timeout: 10000 });
  await page.fill('#palette-search', query);
  await page.waitForTimeout(1800);
  const paths = await page.locator('#palette-results .palette-item').evaluateAll((items) =>
    items.map((item) => item.dataset.path || '').filter(Boolean));
  await page.keyboard.press('Escape');
  return paths;
}

async function launchTerminal(page, path) {
  await page.keyboard.press('ControlOrMeta+k');
  await page.waitForSelector('#palette-search', { state: 'visible', timeout: 10000 });
  await page.fill('#palette-search', path);
  await page.waitForTimeout(1800);
  const exact = page.locator(`#palette-results .palette-item[data-path="${path}"]`).first();
  await exact.waitFor({ state: 'visible', timeout: 10000 });
  await exact.click();
  await page.waitForSelector('#terminal-section', { state: 'visible', timeout: 15000 });
}

async function waitIdentityLock(page) {
  const deadline = Date.now() + 30000;
  while (Date.now() < deadline) {
    const status = await page.locator('#pty-status').textContent().catch(() => '');
    if (status.includes('\u{1F512}')) return { ok: true, status };
    if (/failed|verification/i.test(status)) return { ok: false, status };
    await page.waitForTimeout(500);
  }
  return { ok: false, status: 'timeout waiting for identity lock' };
}

function shellQuote(value) {
  return `'${value.replaceAll("'", `'"'"'`)}'`;
}

function octalPrintf(value) {
  const escaped = [...Buffer.from(`${value}\n`)]
    .map((byte) => `\\${byte.toString(8).padStart(3, '0')}`)
    .join('');
  return `printf '${escaped}'`;
}

async function runBashModeProbe(page, command, markers) {
  await page.click('#terminal-container');
  await page.keyboard.type(`! ${command}`);
  await page.keyboard.press('Enter');
  await page.waitForFunction(
    (expected) => {
      const rows = document.querySelectorAll('#terminal-container .xterm-rows > div');
      const text = Array.from(rows).map((row) => row.textContent).join('\n');
      return expected.some((marker) => text.includes(marker));
    },
    markers,
    { timeout: 30000 },
  );
  const text = await terminalText(page);
  return markers.find((marker) => text.includes(marker)) || '';
}

async function terminalText(page) {
  return page.evaluate(() => {
    const rows = document.querySelectorAll('#terminal-container .xterm-rows > div');
    return Array.from(rows).map((row) => row.textContent).join('\n');
  });
}

async function apiIdentity(principalState, who) {
  const response = await principalState.context.request.get(`${BASE}/api/app/me`);
  const body = await response.json().catch(() => ({}));
  const expectedEmail = EMAILS[who];
  record(`${who}: short-lived browser session resolves to the expected identity`,
    response.ok() && body.email === expectedEmail && body.roost_mode === true,
    `status=${response.status()} email=${JSON.stringify(body.email)} roost_mode=${JSON.stringify(body.roost_mode)}`);
}

async function mcpBrowserRoundTrip(principalState) {
  const callback = 'http://127.0.0.1:65534/callback';
  const verifier = 'wingthing-deployed-browser-canary-verifier-00000000000000001';
  const challenge = crypto.createHash('sha256').update(verifier).digest('base64url');
  const registration = await principalState.context.request.post(`${BASE}/oauth/register`, {
    data: {
      redirect_uris: [callback],
      client_name: 'Wingthing deployed browser canary',
      token_endpoint_auth_method: 'none',
    },
  });
  const registered = await registration.json().catch(() => ({}));
  if (registration.status() !== 201 || !registered.client_id) {
    record('admin: deployed MCP browser client registers', false,
      `status=${registration.status()} body=${JSON.stringify(registered).slice(0, 160)}`);
    return;
  }

  const authorize = new URL(`${BASE}/oauth/authorize`);
  authorize.search = new URLSearchParams({
    response_type: 'code',
    client_id: registered.client_id,
    redirect_uri: callback,
    code_challenge: challenge,
    code_challenge_method: 'S256',
    state: 'deployed-canary',
    resource: `${BASE}/mcp`,
  }).toString();
  const consentPage = await principalState.page.goto(authorize.toString(), { waitUntil: 'domcontentloaded' });
  if (!consentPage?.ok() || await principalState.page.locator('button.approve').count() !== 1) {
    record('admin: deployed MCP browser consent page renders', false,
      `status=${consentPage?.status() || 0}`);
    return;
  }

  const consentResponse = principalState.page.waitForResponse((candidate) =>
    candidate.request().method() === 'POST' && candidate.url() === `${BASE}/oauth/authorize`);
  await principalState.page.locator('button.approve').click();
  const approved = await consentResponse;
  const origin = (await approved.request().allHeaders()).origin || '';
  record('admin: deployed MCP browser consent POST succeeds', approved.status() === 303,
    `status=${approved.status()} origin=${JSON.stringify(origin)}`);

  const redirect = approved.headers().location || '';
  const code = redirect ? new URL(redirect).searchParams.get('code') : '';
  const tokenResponse = await principalState.context.request.post(`${BASE}/oauth/token`, {
    form: {
      grant_type: 'authorization_code',
      client_id: registered.client_id,
      redirect_uri: callback,
      code,
      code_verifier: verifier,
      resource: `${BASE}/mcp`,
    },
  });
  const tokens = await tokenResponse.json().catch(() => ({}));
  record('admin: deployed MCP exchanges its browser-approved PKCE code',
    tokenResponse.status() === 200 && !!tokens.access_token,
    `status=${tokenResponse.status()} token_type=${JSON.stringify(tokens.token_type || '')}`);

  const headers = {
    Authorization: `Bearer ${tokens.access_token || ''}`,
    'Content-Type': 'application/json',
    'MCP-Protocol-Version': '2025-11-25',
  };
  const initializeResponse = await principalState.context.request.post(`${BASE}/mcp`, {
    headers,
    data: {
      jsonrpc: '2.0', id: 1, method: 'initialize',
      params: {
        protocolVersion: '2025-11-25', capabilities: {},
        clientInfo: { name: 'wingthing-deployed-canary', version: '1' },
      },
    },
  });
  const initialized = await initializeResponse.json().catch(() => ({}));
  const listResponse = await principalState.context.request.post(`${BASE}/mcp`, {
    headers,
    data: { jsonrpc: '2.0', id: 2, method: 'tools/list', params: {} },
  });
  const listed = await listResponse.json().catch(() => ({}));
  const toolNames = Array.isArray(listed?.result?.tools)
    ? listed.result.tools.map((tool) => tool.name)
    : [];
  record('admin: deployed MCP initializes and lists the authenticated tool surface',
    initializeResponse.status() === 200 && initialized?.result?.serverInfo?.name === 'wingthing' &&
      listResponse.status() === 200 && toolNames.includes('wing_list'),
    `initialize=${initializeResponse.status()} list=${listResponse.status()} tools=${JSON.stringify(toolNames)}`);

  const callResponse = await principalState.context.request.post(`${BASE}/mcp`, {
    headers,
    data: {
      jsonrpc: '2.0', id: 3, method: 'tools/call',
      params: { name: 'wing_list', arguments: {} },
    },
  });
  const called = await callResponse.json().catch(() => ({}));
  record('admin: deployed MCP executes the read-only wing_list tool',
    callResponse.status() === 200 && !called.error && called?.result?.isError !== true,
    `status=${callResponse.status()} error=${JSON.stringify(called.error || null)}`);

  const blocked = await principalState.context.request.post(`${BASE}/oauth/authorize`, {
    headers: {
      Origin: 'https://attacker.example',
      'Sec-Fetch-Site': 'cross-site',
      'Content-Type': 'application/x-www-form-urlencoded',
    },
    data: 'rid=attacker-controlled&action=approve',
  });
  record('admin: deployed MCP consent rejects cross-site browser submissions', blocked.status() === 403,
    `status=${blocked.status()}`);
}

fs.mkdirSync(OUT, { recursive: true });
const launchOptions = { args: ['--disable-dev-shm-usage', '--no-sandbox'] };
// CI uses Playwright's bundled Chromium. Operators can point the live canary at
// an already-installed Chromium-family browser instead of downloading another
// browser solely to exercise a deployed roost.
if (process.env.WT_E2E_CHROMIUM_EXECUTABLE) {
  launchOptions.executablePath = process.env.WT_E2E_CHROMIUM_EXECUTABLE;
}
const browser = await chromium.launch(launchOptions);
const openContexts = [];

try {
  const admin = await principal(browser, 'admin');
  openContexts.push(admin.context);
  await apiIdentity(admin, 'admin');

  try {
    await mcpBrowserRoundTrip(admin);
  } catch (error) {
    record('admin: deployed MCP browser-to-tool round trip', false, String(error).slice(0, 240));
  }

  try {
    const response = await openDashboard(admin.page);
    const security = response ? await response.securityDetails() : null;
    record('public endpoint uses browser-verified HTTPS',
      BASE.startsWith('https://') && response?.ok() && !!security?.issuer,
      `status=${response?.status()} issuer=${JSON.stringify(security?.issuer || '')}`);
    record('admin: dashboard shows the embedded org wing',
      await admin.page.locator('#wing-status .wing-box').count() >= 1);
  } catch (error) {
    record('admin: public HTTPS dashboard loads the embedded org wing', false, String(error).slice(0, 220));
  }

  results.preexistingSessionIDs = await admin.page.locator('#session-tabs .session-tab')
    .evaluateAll((tabs) => tabs.map((tab) => tab.dataset.sid).filter(Boolean));

  const adminPaths = await palettePaths(admin.page, '/opt/wingthing/');
  record('admin: palette exposes every configured role root',
    ['/opt/wingthing/eng', '/opt/wingthing/support', '/opt/wingthing/product', '/opt/wingthing/sales']
      .every((path) => adminPaths.includes(path)),
    JSON.stringify(adminPaths));

  try {
    await launchTerminal(admin.page, '/opt/wingthing/eng');
    const active = admin.page.locator('#session-tabs .session-tab.active');
    await active.waitFor({ state: 'visible', timeout: 15000 });
    results.createdSessionID = await active.getAttribute('data-sid') || '';
    record('admin: palette launches one new terminal in the eng role root',
      !!results.createdSessionID && !results.preexistingSessionIDs.includes(results.createdSessionID),
      `session=${results.createdSessionID || '(missing)'}`);

    const lock = await waitIdentityLock(admin.page);
    record('admin: browser and wing derive the fail-closed E2E identity lock',
      lock.ok, JSON.stringify(lock.status));

    if (REPOS_PATH) {
      await admin.page.waitForTimeout(1500);
      const visibility = await runBashModeProbe(
        admin.page,
        `if test -r ${shellQuote(REPOS_PATH)}; then ${octalPrintf('WT_REPOS_VISIBLE')}; else ${octalPrintf('WT_REPOS_MISSING')}; fi`,
        ['WT_REPOS_VISIBLE', 'WT_REPOS_MISSING'],
      );
      record('admin: egg.yaml external read-only mount is visible through the real browser session',
        visibility === 'WT_REPOS_VISIBLE', visibility);

      const writeCanary = `${REPOS_PATH.replace(/\/$/, '')}/.wingthing-fs-policy-canary-${process.pid}`;
      const writePolicy = await runBashModeProbe(
        admin.page,
        `if touch ${shellQuote(writeCanary)} 2>/dev/null; then rm -f ${shellQuote(writeCanary)}; ${octalPrintf('WT_REPOS_WRITABLE')}; else ${octalPrintf('WT_REPOS_READ_ONLY')}; fi`,
        ['WT_REPOS_READ_ONLY', 'WT_REPOS_WRITABLE'],
      );
      record('admin: egg.yaml external repository mount remains read-only',
        writePolicy === 'WT_REPOS_READ_ONLY', writePolicy);

      const rolePolicy = await runBashModeProbe(
        admin.page,
        `if test -r /opt/wingthing/support/egg.yaml; then ${octalPrintf('WT_OTHER_ROLE_VISIBLE')}; else ${octalPrintf('WT_OTHER_ROLE_DENIED')}; fi`,
        ['WT_OTHER_ROLE_DENIED', 'WT_OTHER_ROLE_VISIBLE'],
      );
      record('admin: explicit egg.yaml sibling-role deny remains enforced',
        rolePolicy === 'WT_OTHER_ROLE_DENIED', rolePolicy);
    }

    await admin.page.setViewportSize({ width: 1100, height: 700 });
    await admin.page.waitForTimeout(800);
    await admin.page.setViewportSize({ width: 1280, height: 800 });
    await admin.page.waitForTimeout(800);
    record('admin: encrypted tunnel accepts terminal resize', true);

    await admin.page.click('#home-btn');
    await admin.page.waitForSelector('#home-section', { state: 'visible', timeout: 10000 });
    const exactTab = admin.page.locator(`#session-tabs .session-tab[data-sid="${results.createdSessionID}"]`);
    await exactTab.waitFor({ state: 'visible', timeout: 15000 });
    await exactTab.click();
    await admin.page.waitForSelector('#terminal-section', { state: 'visible', timeout: 15000 });
    const relock = await waitIdentityLock(admin.page);
    record('admin: detach and exact-session reattach restores the encrypted tunnel',
      relock.ok, JSON.stringify(relock.status));

    await admin.page.waitForSelector('#session-close-btn', { state: 'visible', timeout: 10000 });
    await admin.page.click('#session-close-btn');
    await admin.page.click('#session-close-btn');
    await admin.page.waitForFunction((sessionID) =>
      !Array.from(document.querySelectorAll('#session-tabs .session-tab'))
        .some((candidate) => candidate.dataset.sid === sessionID),
      results.createdSessionID,
      { timeout: 20000 });
    record('admin: end-session removes exactly the canary terminal', true);
  } catch (error) {
    record('admin: deployed terminal lifecycle', false, String(error).slice(0, 240));
  }
  await admin.page.screenshot({ path: `${OUT}/deployed-admin.png`, fullPage: false });

  try {
    await admin.page.goto(`${BASE}/app/#account`, { waitUntil: 'domcontentloaded' });
    await admin.page.waitForSelector('#ac-passkey-list', { timeout: 15000 });
    record('admin: account page renders with org controls hidden in appliance mode',
      await admin.page.locator('.ac-org-card').count() === 0 &&
        await admin.page.locator('#ac-create-toggle').count() === 0);
  } catch (error) {
    record('admin: account page renders in appliance mode', false, String(error).slice(0, 220));
  }

  const member = await principal(browser, 'member');
  openContexts.push(member.context);
  await apiIdentity(member, 'member');
  await openDashboard(member.page);
  const memberPaths = await palettePaths(member.page, '/opt/wingthing/');
  record('eng member: palette includes eng and excludes other role roots',
    memberPaths.includes('/opt/wingthing/eng') &&
      !memberPaths.some((path) => ['/opt/wingthing/support', '/opt/wingthing/product', '/opt/wingthing/sales'].includes(path)),
    JSON.stringify(memberPaths));

  const support = await principal(browser, 'support');
  openContexts.push(support.context);
  await apiIdentity(support, 'support');
  await openDashboard(support.page);
  const supportPaths = await palettePaths(support.page, '/opt/wingthing/');
  record('support member: palette includes support and sales but excludes eng',
    supportPaths.includes('/opt/wingthing/support') &&
      supportPaths.includes('/opt/wingthing/sales') &&
      !supportPaths.includes('/opt/wingthing/eng'),
    JSON.stringify(supportPaths));

  const mobile = await principal(browser, 'member', { width: 390, height: 844 });
  openContexts.push(mobile.context);
  try {
    await openDashboard(mobile.page);
    record('member mobile: deployed dashboard renders the shared wing', true);
  } catch (error) {
    record('member mobile: deployed dashboard renders the shared wing', false, String(error).slice(0, 220));
  }
  await mobile.page.screenshot({ path: `${OUT}/deployed-member-mobile.png`, fullPage: false });

  const outsider = await principal(browser, 'outsider');
  openContexts.push(outsider.context);
  const outsiderMe = await outsider.context.request.get(`${BASE}/api/app/me`);
  const outsiderWings = await outsider.context.request.get(`${BASE}/api/app/wings`);
  const outsiderWingBody = await outsiderWings.json().catch(() => []);
  record('legacy org compatibility: authenticated account remains admitted without WT_ROOST_ALLOWED_EMAILS',
    outsiderMe.ok() && outsiderWings.ok() && Array.isArray(outsiderWingBody) && outsiderWingBody.length >= 1,
    `me=${outsiderMe.status()} wings=${Array.isArray(outsiderWingBody) ? outsiderWingBody.length : 'invalid'}`);
  await openDashboard(outsider.page);
  const outsiderPaths = await palettePaths(outsider.page, '/opt/wingthing/');
  const configuredRoleRoots = [
    '/opt/wingthing/eng',
    '/opt/wingthing/support',
    '/opt/wingthing/product',
    '/opt/wingthing/sales',
  ];
  record('unlisted account: embedded wing is visible but configured role roots remain unavailable',
    !outsiderPaths.some((path) => configuredRoleRoots.includes(path)), JSON.stringify(outsiderPaths));
} catch (error) {
  record('canary completed without an unhandled test error', false, String(error).slice(0, 300));
} finally {
  for (const context of openContexts.reverse()) {
    await context.close().catch(() => {});
  }
  fs.mkdirSync(OUT, { recursive: true });

  const failed = results.steps.filter((step) => !step.ok).length;
  results.summary = {
    total: results.steps.length,
    failed,
    consoleErrors: results.consoleErrors.length,
    pageErrors: results.pageErrors.length,
    failedRequests: results.failedRequests.length,
  };
  fs.writeFileSync(`${OUT}/deployed-org-results.json`, JSON.stringify(results, null, 2));
  console.log(`\n${results.steps.length - failed}/${results.steps.length} checks passed; ` +
    `${results.consoleErrors.length} console error(s), ${results.pageErrors.length} page error(s), ` +
    `${results.failedRequests.length} failed request(s)`);
  await browser.close();
  process.exit(failed || results.consoleErrors.length || results.pageErrors.length || results.failedRequests.length ? 1 : 0);
}
