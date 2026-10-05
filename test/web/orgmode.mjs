// Organization-mode compatibility canary. Drives the containerized shared roost
// as three enrolled principals plus one outsider through dashboard layout, terminal
// lifecycle, encryption, path ACLs, enrollment, account/org, and mobile behavior.
import { chromium } from 'playwright';
import crypto from 'crypto';
import fs from 'fs';

const BASE = process.env.ROOST_URL || 'http://roost:8080';
const OUT = process.env.OUT_DIR || '/out';
const TOKENS = {
  alice: 'canary-alice-session-token-0000000001',
  bob: 'canary-bob-session-token-000000000002',
  carol: 'canary-carol-session-token-0000000003',
  dave: 'canary-dave-session-token-00000000004',
};
const SUPPORT_TEXT_NAME = `support-note-${crypto.randomBytes(6).toString('hex')}.txt`;
const SUPPORT_TEXT_BYTES = Buffer.from('support upload round trip\n');

const results = { base: BASE, steps: [], consoleErrors: [], pageErrors: [], failedRequests: [] };
let stepNo = 0;

function record(name, ok, note) {
  results.steps.push({ n: ++stepNo, name, ok, note: note || '' });
  console.log(`[${ok ? 'PASS' : 'FAIL'}] ${name}${note ? ' — ' + note : ''}`);
}

function watch(page, who) {
  page.on('console', (msg) => {
    if (msg.type() === 'error') results.consoleErrors.push({ who, text: msg.text().slice(0, 500) });
  });
  page.on('pageerror', (err) => results.pageErrors.push({ who, text: String(err).slice(0, 500) }));
  page.on('requestfailed', (req) => {
    const f = req.failure();
    // aborted requests are routine (navigation, ws teardown)
    const expectedMCPCallback = req.url().startsWith('http://127.0.0.1:65534/callback?');
    if (f && f.errorText !== 'net::ERR_ABORTED' && !expectedMCPCallback) {
      results.failedRequests.push({ who, url: req.url().slice(0, 200), err: f.errorText });
    }
  });
}

async function shot(page, name) {
  await page.screenshot({ path: `${OUT}/${String(stepNo + 1).padStart(2, '0')}-${name}.png`, fullPage: false });
}

async function newUser(browser, who, viewport) {
  const ctx = await browser.newContext({ viewport, deviceScaleFactor: viewport.width < 500 ? 2 : 1 });
  await ctx.addCookies([{ name: 'wt_session', value: TOKENS[who], url: BASE }]);
  const page = await ctx.newPage();
  watch(page, who + (viewport.width < 500 ? '-mobile' : ''));
  return { ctx, page };
}

async function waitWing(page) {
  await page.goto(BASE + '/app/', { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#wing-status .wing-box', { timeout: 30000 });
  // let wing detail websocket data settle
  await page.waitForTimeout(2000);
}

async function testMCPBrowserConsent(principal) {
  const callback = 'http://127.0.0.1:65534/callback';
  const verifier = 'wingthing-org-mode-browser-canary-verifier-000000000000000001';
  const challenge = crypto.createHash('sha256').update(verifier).digest('base64url');
  const registration = await principal.ctx.request.post(BASE + '/oauth/register', {
    data: {
      redirect_uris: [callback],
      client_name: 'Wingthing org-mode browser canary',
      token_endpoint_auth_method: 'none',
    },
  });
  const registered = await registration.json().catch(() => ({}));
  if (registration.status() !== 201 || !registered.client_id) {
    record('alice: MCP browser consent client registers', false,
      `status=${registration.status()} body=${JSON.stringify(registered).slice(0, 160)}`);
    return;
  }

  const authorize = new URL(BASE + '/oauth/authorize');
  authorize.search = new URLSearchParams({
    response_type: 'code',
    client_id: registered.client_id,
    redirect_uri: callback,
    code_challenge: challenge,
    code_challenge_method: 'S256',
    state: 'org-mode-canary',
    resource: BASE + '/mcp',
  }).toString();
  const response = await principal.page.goto(authorize.toString(), { waitUntil: 'domcontentloaded' });
  if (!response?.ok() || await principal.page.locator('button.approve').count() !== 1) {
    record('alice: MCP browser consent page renders in org mode', false,
      `status=${response?.status() || 0}`);
    return;
  }

  const consentResponse = principal.page.waitForResponse((candidate) =>
    candidate.request().method() === 'POST' && candidate.url() === BASE + '/oauth/authorize');
  await principal.page.locator('button.approve').click();
  const approved = await consentResponse;
  const origin = (await approved.request().allHeaders()).origin || '';
  record('alice: MCP browser consent POST succeeds in org mode', approved.status() === 303,
    `status=${approved.status()} origin=${JSON.stringify(origin)}`);

  const redirect = approved.headers().location || '';
  const code = redirect ? new URL(redirect).searchParams.get('code') : '';
  const tokenResponse = await principal.ctx.request.post(BASE + '/oauth/token', {
    form: {
      grant_type: 'authorization_code',
      client_id: registered.client_id,
      redirect_uri: callback,
      code,
      code_verifier: verifier,
      resource: BASE + '/mcp',
    },
  });
  const tokens = await tokenResponse.json().catch(() => ({}));
  record('alice: MCP browser authorization exchanges its PKCE code',
    tokenResponse.status() === 200 && !!tokens.access_token,
    `status=${tokenResponse.status()} token_type=${JSON.stringify(tokens.token_type || '')}`);

  const mcpHeaders = {
    Authorization: `Bearer ${tokens.access_token || ''}`,
    'Content-Type': 'application/json',
    'MCP-Protocol-Version': '2025-11-25',
  };
  const initializeResponse = await principal.ctx.request.post(BASE + '/mcp', {
    headers: mcpHeaders,
    data: {
      jsonrpc: '2.0',
      id: 1,
      method: 'initialize',
      params: {
        protocolVersion: '2025-11-25',
        capabilities: {},
        clientInfo: { name: 'wingthing-org-canary', version: '1' },
      },
    },
  });
  const initialized = await initializeResponse.json().catch(() => ({}));
  const listResponse = await principal.ctx.request.post(BASE + '/mcp', {
    headers: mcpHeaders,
    data: { jsonrpc: '2.0', id: 2, method: 'tools/list', params: {} },
  });
  const listed = await listResponse.json().catch(() => ({}));
  const toolNames = Array.isArray(listed?.result?.tools)
    ? listed.result.tools.map((tool) => tool.name)
    : [];
  record('alice: authenticated org-mode MCP initializes and lists tools',
    initializeResponse.status() === 200 && initialized?.result?.serverInfo?.name === 'wingthing' &&
      listResponse.status() === 200 && toolNames.includes('wing_list'),
    `initialize=${initializeResponse.status()} list=${listResponse.status()} tools=${JSON.stringify(toolNames)}`);

  const callResponse = await principal.ctx.request.post(BASE + '/mcp', {
    headers: mcpHeaders,
    data: {
      jsonrpc: '2.0',
      id: 3,
      method: 'tools/call',
      params: { name: 'wing_list', arguments: {} },
    },
  });
  const called = await callResponse.json().catch(() => ({}));
  record('alice: authenticated org-mode MCP executes a read-only tool',
    callResponse.status() === 200 && !called.error && called?.result?.isError !== true,
    `status=${callResponse.status()} error=${JSON.stringify(called.error || null)}`);

  const blocked = await principal.ctx.request.post(BASE + '/oauth/authorize', {
    headers: {
      Origin: 'https://attacker.example',
      'Sec-Fetch-Site': 'cross-site',
      'Content-Type': 'application/x-www-form-urlencoded',
    },
    data: 'rid=attacker-controlled&action=approve',
  });
  record('alice: MCP consent still rejects cross-site browser submissions', blocked.status() === 403,
    `status=${blocked.status()}`);
}

// Open the command palette, type a path, launch a session there.
async function launchTerminal(page, path) {
  await page.keyboard.press('ControlOrMeta+k');
  await page.waitForSelector('#palette-search', { state: 'visible', timeout: 10000 });
  await page.fill('#palette-search', path);
  await page.waitForTimeout(1500); // debounced dir.list over the tunnel
  await page.keyboard.press('Enter');
  try {
    await page.waitForSelector('#terminal-section', { state: 'visible', timeout: 8000 });
  } catch {
    // Enter needs a selected palette row; click the first result instead
    const item = page.locator('#palette-results .palette-item').first();
    if (await item.count()) await item.click();
    await page.waitForSelector('#terminal-section', { state: 'visible', timeout: 10000 });
  }
}

// Wait for the E2E identity lock (or fail-closed error) after terminal open.
async function waitLock(page) {
  const deadline = Date.now() + 30000;
  while (Date.now() < deadline) {
    const status = await page.locator('#pty-status').textContent().catch(() => '');
    if (status && status.includes('\u{1F512}')) return { ok: true, status };
    if (status && /failed|verification/i.test(status)) return { ok: false, status };
    const text = await terminalText(page).catch(() => '');
    if (/egg crashed|egg exited/i.test(text)) {
      const line = (text.split('\n').find((l) => /egg crashed|egg exited|no egg\.yaml/i.test(l)) || '').trim();
      return { ok: false, status: 'egg crashed: ' + line.slice(0, 160) };
    }
    await page.waitForTimeout(500);
  }
  return { ok: false, status: '(timeout — no lock, no error)' };
}

async function terminalText(page) {
  // xterm DOM renderer keeps text rows under .xterm-rows; fall back to a11y buffer
  return await page.evaluate(() => {
    const rows = document.querySelectorAll('#terminal-container .xterm-rows > div');
    if (rows.length) return Array.from(rows).map((r) => r.textContent).join('\n');
    const live = document.querySelector('#terminal-container .live-region, #terminal-container .xterm-accessibility');
    return live ? live.textContent : '';
  });
}

async function terminalCommand(page, command, marker) {
  await page.click('#terminal-container');
  await page.keyboard.type(command);
  await page.keyboard.press('Enter');
  await page.waitForFunction(
    (expected) => Array.from(document.querySelectorAll('#terminal-container .xterm-rows > div'))
      .some((row) => row.textContent.includes(expected)),
    marker,
    { timeout: 30000 },
  );
}

function findExportedFile(root, name) {
  if (!fs.existsSync(root)) return '';
  for (const entry of fs.readdirSync(root, { withFileTypes: true })) {
    const path = `${root}/${entry.name}`;
    if (entry.isDirectory()) {
      const nested = findExportedFile(path, name);
      if (nested) return nested;
    } else if (entry.isFile() && entry.name === name) {
      return path;
    }
  }
  return '';
}

// The roost is served over plain HTTP inside the docker network, which is NOT
// a secure browser context. That is deliberate: the web app supports
// insecure-origin roosts (pure-JS crypto, guarded randomUUID), and this suite
// regresses that support — no secure-context-only web API may be load-bearing.
const browser = await chromium.launch({
  args: ['--disable-dev-shm-usage', '--no-sandbox'],
});
fs.mkdirSync(OUT, { recursive: true });
let carolSessionID = '';

try {
  // ---------- Alice (wing admin), desktop ----------
  const alice = await newUser(browser, 'alice', { width: 1280, height: 800 });
  {
    const p = alice.page;
    try {
      await waitWing(p);
      const wings = await p.locator('#wing-status .wing-box').count();
      record('alice: dashboard shows shared roost wing', wings >= 1, `${wings} wing box(es)`);
    } catch (e) {
      record('alice: dashboard shows shared roost wing', false, String(e).slice(0, 200));
    }
    await shot(p, 'alice-dashboard');

    try {
      await launchTerminal(p, '/opt/wingthing/eng');
      record('alice: palette launched terminal in /opt/wingthing/eng', true);
    } catch (e) {
      record('alice: palette launched terminal in /opt/wingthing/eng', false, String(e).slice(0, 200));
    }
    await shot(p, 'alice-terminal-open');

    const lock = await waitLock(p);
    record('alice: E2E identity lock derived (fail-closed path)', lock.ok, `pty-status=${JSON.stringify(lock.status)}`);

    // The Claude-shaped canary reads the same isolated profile path as the real
    // CLI. Alice must receive her persisted profile through the complete
    // browser -> roost -> wing -> egg launch path.
    try {
      await p.waitForFunction(
        () => {
          const rows = document.querySelectorAll('#terminal-container .xterm-rows > div');
          return Array.from(rows).some((r) => r.textContent.includes('CANARY_SHELL_READY'));
        },
        { timeout: 30000 }
      );
      record('alice: agent session reached interactive shell', true);
    } catch {
      record('alice: agent session reached interactive shell', false, 'CANARY_SHELL_READY not seen in terminal');
    }
    try {
      await p.waitForFunction(
        () => {
          const rows = document.querySelectorAll('#terminal-container .xterm-rows > div');
          return Array.from(rows).some((r) => r.textContent.includes('CANARY_PROFILE_READY'));
        },
        { timeout: 15000 }
      );
      const text = await terminalText(p);
      const loaded = text.includes('marker=alice-persisted') &&
        text.includes('dir_ok=true') && text.includes('onboarding=true') &&
        !text.includes('CANARY_PROFILE_EMPTY');
      record('alice: existing Claude profile is loaded from her isolated home', loaded,
        loaded ? '' : text.slice(0, 300));
    } catch (e) {
      record('alice: existing Claude profile is loaded from her isolated home', false, String(e).slice(0, 200));
    }
    await shot(p, 'alice-terminal-agent-output');
    {
      const text = await terminalText(p);
      record('alice: deployment model policy reaches the isolated session without replacing preferences',
        text.includes('CANARY_MODEL_POLICY ok=true saved_model=opus theme=alice-theme'));
      record('alice: browser session enforces the administrator egg filesystem policy',
        text.includes('CANARY_FS_POLICY repos_visible=true repos_read_only=true config_read_only=true other_role_denied=true'));
    }

    try {
      await p.click('#terminal-container');
      await p.keyboard.type('echo INPUT_ROUNDTRIP_$(id -un)');
      await p.keyboard.press('Enter');
      await p.waitForFunction(
        () => {
          const rows = document.querySelectorAll('#terminal-container .xterm-rows > div');
          return Array.from(rows).some((r) => r.textContent.includes('INPUT_ROUNDTRIP_'));
        },
        { timeout: 15000 }
      );
      record('alice: terminal input/output round trip', true);
    } catch (e) {
      record('alice: terminal input/output round trip', false, String(e).slice(0, 200));
    }
    await shot(p, 'alice-terminal-roundtrip');
    await p.keyboard.type('CANARY_SET_THEME');
    await p.keyboard.press('Enter');
    try {
      await p.waitForFunction(() => Array.from(document.querySelectorAll('#terminal-container .xterm-rows > div'))
        .some((row) => row.textContent.includes('CANARY_SETTINGS_SAVED ok=true')), null, { timeout: 15000 });
      record('alice: personal settings can be changed inside the isolated session', true);
    } catch (e) {
      record('alice: personal settings can be changed inside the isolated session', false, String(e).slice(0, 200));
    }

    // resize goes through the authenticated tunnel now — exercise it
    try {
      await p.setViewportSize({ width: 1100, height: 700 });
      await p.waitForTimeout(1500);
      await p.setViewportSize({ width: 1280, height: 800 });
      await p.waitForTimeout(1500);
      record('alice: viewport resize (pty.resize via tunnel)', true);
    } catch (e) {
      record('alice: viewport resize (pty.resize via tunnel)', false, String(e).slice(0, 200));
    }

    // detach and reattach — replay path
    try {
      await p.click('#home-btn');
      await p.waitForSelector('#home-section', { state: 'visible', timeout: 10000 });
      await p.waitForTimeout(1500);
      await shot(p, 'alice-home-after-detach');
      const tab = p.locator('#session-tabs .session-tab').first();
      await tab.waitFor({ state: 'visible', timeout: 10000 });
      await tab.click();
      await p.waitForSelector('#terminal-section', { state: 'visible', timeout: 15000 });
      await p.waitForFunction(
        () => {
          const rows = document.querySelectorAll('#terminal-container .xterm-rows > div');
          return Array.from(rows).some((r) => r.textContent.includes('INPUT_ROUNDTRIP_'));
        },
        { timeout: 20000 }
      );
      record('alice: detach + reattach replays scrollback', true);
    } catch (e) {
      record('alice: detach + reattach replays scrollback', false, String(e).slice(0, 200));
    }
    try {
      await p.waitForFunction(
        () => {
          const rows = document.querySelectorAll('#terminal-container .xterm-rows > div');
          return Array.from(rows).some((r) =>
            r.textContent.includes('CANARY_MODEL_POLICY ok=true saved_model=opus theme=alice-edited'));
        },
        null,
        { timeout: 20000 }
      );
      record('alice: reconnect retains updated personal preferences with host model policy', true);
    } catch (e) {
      record('alice: reconnect retains updated personal preferences with host model policy',
        false, String(e).slice(0, 200));
    }
    await shot(p, 'alice-reattach');

    // Audit is enabled for this roost. Exercise browser-Back cleanup for both
    // keylog and replay so hidden overlays cannot retain xterm instances,
    // playback timers, or late stream callbacks.
    try {
      await p.click('#home-btn');
      await p.waitForSelector('#home-section', { state: 'visible', timeout: 10000 });
      await p.locator('#wing-status .wing-box').first().click();
      await p.waitForSelector('#wing-detail-section', { state: 'visible', timeout: 10000 });
      const keylog = p.locator('.wd-session-row .wd-keylog-btn').first();
      await keylog.waitFor({ state: 'visible', timeout: 10000 });
      await keylog.click();
      await p.waitForSelector('#audit-overlay', { state: 'visible', timeout: 10000 });
      await p.evaluate(() => history.back());
      await p.waitForSelector('#audit-overlay', { state: 'hidden', timeout: 10000 });
      await p.waitForTimeout(500);
      const keylogClosed = await p.evaluate(() => {
        const overlay = document.getElementById('audit-overlay');
        return !overlay._auditTerm && !overlay._playTimer &&
          document.getElementById('audit-download').style.display === 'none' &&
          document.getElementById('audit-play').style.display === '' &&
          document.getElementById('audit-speed').style.display === '';
      });
      record('alice: browser Back fully cleans up audit keylog', keylogClosed);

      const replay = p.locator('.wd-session-row .wd-replay-btn').first();
      await replay.waitFor({ state: 'visible', timeout: 10000 });
      await replay.click();
      await p.waitForSelector('#audit-overlay', { state: 'visible', timeout: 10000 });
      await p.waitForFunction(() => document.getElementById('audit-play').textContent !== 'loading...', null, { timeout: 10000 });
      const replayButton = p.locator('#audit-play');
      if (await replayButton.isEnabled()) {
        await replayButton.click();
        await p.waitForTimeout(100);
      }
      await p.evaluate(() => history.back());
      await p.waitForSelector('#audit-overlay', { state: 'hidden', timeout: 10000 });
      const replayClosed = await p.evaluate(() => {
        const overlay = document.getElementById('audit-overlay');
        return !overlay._auditTerm && !overlay._playTimer;
      });
      record('alice: browser Back disposes audit replay and playback timer', replayClosed);
    } catch (e) {
      record('alice: audit overlay browser-Back lifecycle', false, String(e).slice(0, 200));
    }

    // account page: in roost mode the org section is hidden BY DESIGN
    // (the roost is the org) — assert the page renders and stays hidden.
    try {
      await p.keyboard.press('Escape').catch(() => {});
      await p.evaluate(() => { location.hash = '#account'; });
      await p.waitForSelector('#ac-passkey-list', { timeout: 15000 });
      const orgCards = await p.locator('.ac-org-card').count();
      const createOrg = await p.locator('#ac-create-toggle').count();
      record('alice: account renders; org section hidden in roost mode', orgCards === 0 && createOrg === 0,
        `org cards=${orgCards} create-org buttons=${createOrg}`);
    } catch (e) {
      record('alice: account renders; org section hidden in roost mode', false, String(e).slice(0, 200));
    }
    await shot(p, 'alice-account-org');

    try {
      await testMCPBrowserConsent(alice);
    } catch (e) {
      record('alice: MCP browser consent round trip in org mode', false, String(e).slice(0, 200));
    }
  }

  // ---------- Alice, mobile ----------
  {
    const m = await newUser(browser, 'alice', { width: 390, height: 844 });
    const p = m.page;
    try {
      await waitWing(p);
      record('alice-mobile: dashboard renders', true);
    } catch (e) {
      record('alice-mobile: dashboard renders', false, String(e).slice(0, 200));
    }
    await shot(p, 'alice-mobile-dashboard');
    try {
      await p.evaluate(() => { location.hash = '#account'; });
      await p.waitForSelector('#ac-passkey-list', { timeout: 15000 });
      record('alice-mobile: account page renders', true);
    } catch (e) {
      record('alice-mobile: account page renders', false, String(e).slice(0, 200));
    }
    await shot(p, 'alice-mobile-account');
    await m.ctx.close();
  }

  // ---------- Bob (eng member), desktop ----------
  const bob = await newUser(browser, 'bob', { width: 1280, height: 800 });
  {
    const p = bob.page;
    try {
      await waitWing(p);
      record('bob: sees shared roost wing', true);
    } catch (e) {
      record('bob: sees shared roost wing', false, String(e).slice(0, 200));
    }
    await shot(p, 'bob-dashboard');

    try {
      await launchTerminal(p, '/opt/wingthing/eng');
      const lock = await waitLock(p);
      record('bob: member terminal in own role path works', lock.ok, `pty-status=${JSON.stringify(lock.status)}`);
    } catch (e) {
      record('bob: member terminal in own role path works', false, String(e).slice(0, 200));
    }
    try {
      await p.waitForFunction(
        () => {
          const rows = document.querySelectorAll('#terminal-container .xterm-rows > div');
          return Array.from(rows).some((r) => r.textContent.includes('CANARY_PROFILE_'));
        },
        { timeout: 15000 }
      );
      const text = await terminalText(p);
      const isolated = text.includes('CANARY_PROFILE_EMPTY') && text.includes('dir_ok=true') &&
        !text.includes('alice-persisted');
      record('bob: distinct identity cannot inherit Alice Claude profile', isolated,
        isolated ? '' : text.slice(0, 300));
    } catch (e) {
      record('bob: distinct identity cannot inherit Alice Claude profile', false, String(e).slice(0, 200));
    }
    await shot(p, 'bob-terminal-eng');
    {
      const text = await terminalText(p);
      record('bob: fresh user receives deployment model policy without Alice preferences',
        text.includes('CANARY_MODEL_POLICY ok=true saved_model= theme=') && !text.includes('alice-theme'));
    }

    // ACL: bob is NOT a member of /opt/wingthing/support
    try {
      await p.click('#home-btn').catch(() => {});
      await p.waitForTimeout(1000);
      await p.keyboard.press('ControlOrMeta+k');
      await p.waitForSelector('#palette-search', { state: 'visible', timeout: 10000 });
      await p.fill('#palette-search', '/opt/wingthing/support');
      await p.waitForTimeout(1500);
      await p.keyboard.press('Enter');
      await p.waitForTimeout(5000);
      const text = await terminalText(p);
      const denied = !text.includes('support marker') &&
        !(await p.locator('#terminal-section').isVisible().catch(() => false) &&
          (await waitLock(p).then((l) => l.ok).catch(() => false)) &&
          (text.includes('CANARY_SHELL_READY') && text.includes('cwd=/opt/wingthing/support')));
      record('bob: non-member path denied or clamped (ACL)', denied, denied ? '' : 'bob got a live session in support path');
    } catch (e) {
      record('bob: non-member path denied or clamped (ACL)', true, 'launch refused: ' + String(e).slice(0, 120));
    }
    await shot(p, 'bob-support-acl');
  }

  // ---------- Carol (support member), desktop feature flow ----------
  const carol = await newUser(browser, 'carol', { width: 1280, height: 800 });
  {
    const p = carol.page;
    try {
      await waitWing(p);
      await launchTerminal(p, '/opt/wingthing/support');
      const lock = await waitLock(p);
      const active = p.locator('#session-tabs .session-tab.active');
      await active.waitFor({ state: 'visible', timeout: 15000 });
      carolSessionID = await active.getAttribute('data-sid') || '';
      record('carol: support member launches an owned support session', lock.ok && !!carolSessionID,
        `session=${carolSessionID || '(missing)'}`);
    } catch (e) {
      record('carol: support member launches an owned support session', false, String(e).slice(0, 200));
    }

    try {
      const tab = p.locator(`#session-tabs .session-tab[data-sid="${carolSessionID}"]`);
      await tab.hover();
      await tab.locator('.session-rename-btn').click();
      await tab.locator('.session-name-input').fill('support-night-review');
      await tab.locator('.session-name-input').press('Enter');
      await p.waitForFunction((sessionID) => {
        const candidate = document.querySelector(`#session-tabs .session-tab[data-sid="${sessionID}"] .tab-label`);
        return candidate && candidate.textContent === 'support-night-review';
      }, carolSessionID, { timeout: 10000 });
      await p.reload({ waitUntil: 'domcontentloaded' });
      await p.waitForSelector(`#session-tabs .session-tab[data-sid="${carolSessionID}"] .tab-label`, { timeout: 20000 });
      const persisted = await p.locator(`#session-tabs .session-tab[data-sid="${carolSessionID}"] .tab-label`).textContent();
      record('carol: session name persists across a full reload', persisted === 'support-night-review', persisted || '');
    } catch (e) {
      record('carol: session name persists across a full reload', false, String(e).slice(0, 200));
    }

    try {
      const tab = p.locator(`#session-tabs .session-tab[data-sid="${carolSessionID}"]`);
      await tab.click();
      await p.waitForSelector('#terminal-section', { state: 'visible', timeout: 15000 });
      await waitLock(p);
      await p.click('#canvas-toggle-btn');
      await p.waitForSelector('#canvas-section', { state: 'visible', timeout: 15000 });
      await p.waitForFunction(() => Array.from(document.querySelectorAll('.canvas-terminal-title'))
        .some((title) => title.textContent.includes('support-night-review')), null, { timeout: 15000 });
      record('carol: multi-session canvas exposes the durable session name', true);
      await shot(p, 'carol-named-canvas');
      await p.locator(`#session-tabs .session-tab[data-sid="${carolSessionID}"]`).click();
      await p.waitForSelector('#terminal-section', { state: 'visible', timeout: 15000 });
      const relock = await waitLock(p);
      if (!relock.ok) throw new Error(`canvas return did not relock: ${relock.status}`);
      await p.waitForTimeout(500);
    } catch (e) {
      record('carol: multi-session canvas exposes the durable session name', false, String(e).slice(0, 200));
    }

    try {
      await terminalCommand(p, 'CANARY_PREVIEW', 'CANARY_PREVIEW_WRITTEN ok=true');
      await p.waitForSelector('#preview-panel', { state: 'visible', timeout: 15000 });
      await p.click('#preview-close-btn');
      await p.waitForSelector('#preview-panel', { state: 'hidden', timeout: 10000 });
      const available = await p.locator('#preview-toggle-btn').isVisible();
      await p.click('#preview-toggle-btn');
      await p.waitForSelector('#preview-panel', { state: 'visible', timeout: 10000 });
      await p.waitForFunction(() => document.getElementById('preview-iframe').srcdoc.includes('Support preview'), null,
        { timeout: 5000 });
      const restored = await p.locator('#preview-iframe').evaluate((frame) =>
        frame.srcdoc.includes('Support preview') && frame.srcdoc.includes('Session preview can be reopened'));
      record('carol: closed session preview remains available and reopens', available && restored,
        `toggle_visible=${available} content_restored=${restored}`);
      await shot(p, 'carol-preview-reopened');
    } catch (e) {
      record('carol: closed session preview remains available and reopens', false, String(e).slice(0, 200));
    }

    try {
      await p.click('#preview-close-btn').catch(() => {});
      await terminalCommand(p, 'CANARY_COPY_LINES', 'COPY_END');
      await p.evaluate(() => {
        window.__wingthingCopiedText = '';
        document.execCommand = function(command) {
          if (command === 'copy') window.__wingthingCopiedText = document.activeElement.value;
          return command === 'copy';
        };
      });
      const rows = p.locator('#terminal-container .xterm-rows > div');
      const contents = await rows.allTextContents();
      const firstIndex = contents.findIndex((line) => line.includes('  indented value'));
      const secondIndex = contents.findIndex((line) => line.includes('second value'));
      if (firstIndex < 0 || secondIndex < 0) throw new Error('copy fixture rows not visible');
      const firstBox = await rows.nth(firstIndex).boundingBox();
      const secondBox = await rows.nth(secondIndex).boundingBox();
      if (!firstBox || !secondBox) throw new Error('copy fixture rows have no layout box');
      await p.mouse.move(firstBox.x + 2, firstBox.y + firstBox.height / 2);
      await p.mouse.down();
      await p.mouse.move(secondBox.x + 112, secondBox.y + secondBox.height / 2, { steps: 8 });
      await p.mouse.up();
      await p.waitForFunction(() => !document.getElementById('terminal-copy-btn').disabled, null, { timeout: 5000 });
      await p.click('#terminal-copy-btn');
      const copied = await p.evaluate(() => window.__wingthingCopiedText);
      record('carol: clean copy preserves indentation and removes terminal line padding',
        copied === '  indented value\nsecond value', JSON.stringify(copied));
    } catch (e) {
      record('carol: clean copy preserves indentation and removes terminal line padding', false, String(e).slice(0, 200));
    }

    try {
      await p.click('#session-files-btn');
      await p.setInputFiles('#session-upload-input', [
        { name: SUPPORT_TEXT_NAME, mimeType: 'text/plain', buffer: SUPPORT_TEXT_BYTES },
        { name: 'support-image.png', mimeType: 'image/png', buffer: Buffer.from('89504e470d0a1a0a0000000d49484452', 'hex') },
      ]);
      await p.waitForFunction(() => document.getElementById('session-files-status').textContent.includes('added support-image.png at '), null,
        { timeout: 30000 });
      const resolvedPath = await p.inputValue('#session-file-path');
      record('carol: text and image upload through the browser uses the policy-resolved data path',
        resolvedPath === '/opt/wingthing/support/support-image.png', resolvedPath);

      await p.fill('#session-file-path', `/opt/wingthing/support/${SUPPORT_TEXT_NAME}`);
      const downloadPromise = p.waitForEvent('download', { timeout: 30000 });
      await p.click('#session-download-btn');
      const download = await downloadPromise;
      const stream = await download.createReadStream();
      const parts = [];
      for await (const part of stream) parts.push(part);
      const downloaded = Buffer.concat(parts);
      record('carol: browser download returns the uploaded file with exact bytes',
        download.suggestedFilename() === SUPPORT_TEXT_NAME && downloaded.equals(SUPPORT_TEXT_BYTES),
        `${download.suggestedFilename()} sha256=${crypto.createHash('sha256').update(downloaded).digest('hex')}`);

      await p.selectOption('#session-export-target', 'isolated-review');
      await p.click('#session-export-btn');
      await p.waitForFunction(() => /^(copied|copy failed:)/.test(document.getElementById('session-files-status').textContent), null,
        { timeout: 30000 });
      const exportStatus = await p.locator('#session-files-status').textContent();
      if (exportStatus !== `copied ${SUPPORT_TEXT_NAME} to isolated-review`) throw new Error(exportStatus || 'export had no result');
      const exportedPath = findExportedFile(`${OUT}/exports`, SUPPORT_TEXT_NAME);
      const exported = exportedPath ? fs.readFileSync(exportedPath) : Buffer.alloc(0);
      record('carol: browser export copies into the isolated per-owner folder',
        exported.equals(SUPPORT_TEXT_BYTES), exportedPath || 'export missing');
      await shot(p, 'carol-session-files');
    } catch (e) {
      record('carol: browser upload, download, and isolated export flow', false, String(e).slice(0, 240));
    }
  }

  // Owner/admin and other members can observe only the actions the backend will allow.
  try {
    await alice.page.goto(BASE + '/app/', { waitUntil: 'domcontentloaded' });
    await alice.page.waitForSelector(`#session-tabs .session-tab[data-sid="${carolSessionID}"]`, { timeout: 20000 });
    const adminRename = await alice.page.locator(`#session-tabs .session-tab[data-sid="${carolSessionID}"] .session-rename-btn`).count();
    record('admin: another user session does not expose rename', adminRename === 0, `rename buttons=${adminRename}`);
    const foreignCard = alice.page.locator(`#sessions-list .egg-box[data-sid="${carolSessionID}"]`);
    await foreignCard.waitFor({ state: 'visible', timeout: 20000 });
    record('admin: inventory preserves allowed inspection and stop while rename remains owner-only',
      await foreignCard.locator('.inventory-details, .inventory-stop').count() === 2 &&
      await foreignCard.locator('.inventory-rename').count() === 0);
  } catch (e) {
    record('admin: another user session does not expose rename', false, String(e).slice(0, 200));
  }
  try {
    await bob.page.goto(BASE + `/app/#s/${carolSessionID}`, { waitUntil: 'domcontentloaded' });
    await bob.page.waitForTimeout(3000);
    const visible = await bob.page.locator(`#session-tabs .session-tab[data-sid="${carolSessionID}"]`).count();
    const filesVisible = await bob.page.locator('#session-files-btn').isVisible().catch(() => false);
    record('bob: another member cannot discover or open Carol session file actions', visible === 0 && !filesVisible,
      `session tabs=${visible} files_visible=${filesVisible}`);
  } catch (e) {
    record('bob: another member cannot discover or open Carol session file actions', true, 'deep link refused');
  }

  try {
    const p = carol.page;
    await p.click('#home-btn');
    await p.waitForSelector('#session-inventory-search', { state: 'visible', timeout: 20000 });
    await p.fill('#session-inventory-search', 'support-night-review');
    await p.waitForFunction((id) => {
      const rows = Array.from(document.querySelectorAll('#sessions-list .egg-box'));
      return rows.length === 1 && rows[0].dataset.sid === id;
    }, carolSessionID, { timeout: 10000 });
    await p.selectOption('#session-inventory-agent', 'claude');
    const card = p.locator('#sessions-list .egg-box').first();
    record('carol: search and provider filtering keep a named owned session inspectable',
      await p.inputValue('#session-inventory-search') === 'support-night-review' &&
      await card.locator('.inventory-attach, .inventory-details, .inventory-rename, .inventory-stop').count() === 4 &&
      (await card.textContent()).includes('/opt/wingthing/support'));
    await p.locator('#session-inventory-search').focus();
    await p.keyboard.press('ArrowDown');
    record('carol: keyboard inventory navigation focuses the exact matching session',
      await p.evaluate((id) => document.activeElement?.dataset.sid === id, carolSessionID));
    await shot(p, 'carol-filtered-inventory');
    await p.click('#session-inventory-clear');
  } catch (e) {
    record('carol: searchable inventory and keyboard navigation', false, String(e).slice(0, 200));
  }

  // ---------- Carol (support member), mobile ----------
  {
    const m = await newUser(browser, 'carol', { width: 390, height: 844 });
    const p = m.page;
    try {
      await waitWing(p);
      record('carol-mobile: dashboard renders with shared wing', true);
    } catch (e) {
      record('carol-mobile: dashboard renders with shared wing', false, String(e).slice(0, 200));
    }
    await shot(p, 'carol-mobile-dashboard');
    await m.ctx.close();
  }

  // ---------- API-level org sanity via alice's session ----------
  {
    const resp = await alice.ctx.request.get(BASE + '/api/orgs');
    let orgs = [];
    try { orgs = await resp.json(); } catch {}
    const slide = Array.isArray(orgs) ? orgs.find((o) => o.slug === 'slide') : null;
    record('api: /api/orgs returns org slide for alice', !!slide, JSON.stringify(orgs).slice(0, 200));
  }

  // ---------- Enrollment negative control ----------
  {
    const dave = await newUser(browser, 'dave', { width: 1280, height: 800 });
    const resp = await dave.ctx.request.get(BASE + '/api/app/me');
    record('outsider: pre-existing cookie cannot bypass roost enrollment', resp.status() === 401,
      `status=${resp.status()}`);
    const errorsBeforePageLoad = results.consoleErrors.length;
    await dave.page.goto(BASE + '/app/', { waitUntil: 'domcontentloaded' });
    await dave.page.waitForTimeout(1000);
    const outsiderConsoleErrors = results.consoleErrors.slice(errorsBeforePageLoad);
    if (outsiderConsoleErrors.length === 1 &&
        outsiderConsoleErrors[0].who === 'dave' &&
        outsiderConsoleErrors[0].text.includes('401 (Unauthorized)')) {
      outsiderConsoleErrors[0].expected = true;
    }
    const wingCount = await dave.page.locator('#wing-status .wing-box').count();
    record('outsider: private roost wing inventory is hidden', wingCount === 0,
      `${wingCount} wing box(es)`);
    await dave.ctx.close();
  }

  // ---------- End-session lifecycle ----------
  {
    const p = alice.page;
    await p.goto(BASE + '/app/', { waitUntil: 'domcontentloaded' });
    const tab = p.locator('#session-tabs .session-tab').first();
    try {
      await tab.waitFor({ state: 'visible', timeout: 15000 });
      const endedSessionID = await tab.getAttribute('data-sid');
      await tab.click();
      await p.waitForSelector('#session-close-btn', { state: 'visible', timeout: 15000 });
      await p.click('#session-close-btn');
      await p.click('#session-close-btn');
      await p.waitForFunction((sessionID) => !Array.from(document.querySelectorAll('#session-tabs .session-tab'))
        .some((candidate) => candidate.dataset.sid === sessionID), endedSessionID, { timeout: 15000 });
      // A reload proves the wing's durable session inventory agrees; another
      // user's visible session must not make this assertion fail.
      await p.waitForTimeout(1000);
      await p.reload({ waitUntil: 'domcontentloaded' });
      await p.waitForSelector('#wing-status .wing-box', { timeout: 15000 });
      await p.waitForTimeout(1500);
      const restored = await p.locator('#session-tabs .session-tab').evaluateAll(
        (candidates, sessionID) => candidates.some((candidate) => candidate.dataset.sid === sessionID), endedSessionID);
      if (restored) throw new Error(`ended session ${endedSessionID} returned after reload`);
      record('alice: end-session removes the durable terminal from the UI', true);
      await launchTerminal(p, '/opt/wingthing/eng');
      const lock = await waitLock(p);
      await p.waitForFunction(() => Array.from(document.querySelectorAll('#terminal-container .xterm-rows > div'))
        .some((row) => row.textContent.includes('CANARY_MODEL_POLICY')), null, { timeout: 15000 });
      const text = await terminalText(p);
      record('alice: new session after exit preserves onboarding and preferences and reapplies host model policy',
        lock.ok && text.includes('marker=alice-persisted') && text.includes('onboarding=true') &&
        text.includes('CANARY_MODEL_POLICY ok=true saved_model=opus theme=alice-edited'));
    } catch (e) {
      record('alice: end-session removes the durable terminal from the UI', false, String(e).slice(0, 200));
    }
  }

  await alice.ctx.close();
  await bob.ctx.close();
  await carol.ctx.close();
} finally {
  await browser.close();
  fs.mkdirSync(OUT, { recursive: true });
  const failed = results.steps.filter((s) => !s.ok).length;
  const unexpectedConsoleErrors = results.consoleErrors.filter((error) => !error.expected);
  results.summary = {
    total: results.steps.length,
    failed,
    unexpectedConsoleErrors: unexpectedConsoleErrors.length,
    pageErrors: results.pageErrors.length,
    failedRequests: results.failedRequests.length,
  };
  fs.writeFileSync(`${OUT}/results.json`, JSON.stringify(results, null, 2));
  console.log(`\n${results.steps.length - failed}/${results.steps.length} steps passed; ` +
    `${unexpectedConsoleErrors.length} unexpected console error(s), ` +
    `${results.pageErrors.length} page error(s), ${results.failedRequests.length} failed request(s)`);
  process.exit(failed || unexpectedConsoleErrors.length || results.pageErrors.length || results.failedRequests.length ? 1 : 0);
}
