// Organization-mode compatibility canary. Drives the containerized shared roost
// as three enrolled principals plus one outsider through dashboard layout, terminal
// lifecycle, encryption, path ACLs, enrollment, account/org, and mobile behavior.
import { chromium } from 'playwright';
import fs from 'fs';

const BASE = process.env.ROOST_URL || 'http://roost:8080';
const OUT = process.env.OUT_DIR || '/out';
const TOKENS = {
  alice: 'canary-alice-session-token-0000000001',
  bob: 'canary-bob-session-token-000000000002',
  carol: 'canary-carol-session-token-0000000003',
  dave: 'canary-dave-session-token-00000000004',
};

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
    if (f && f.errorText !== 'net::ERR_ABORTED') {
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

// The roost is served over plain HTTP inside the docker network, which is NOT
// a secure browser context. That is deliberate: the web app supports
// insecure-origin roosts (pure-JS crypto, guarded randomUUID), and this suite
// regresses that support — no secure-context-only web API may be load-bearing.
const browser = await chromium.launch({
  args: ['--disable-dev-shm-usage', '--no-sandbox'],
});
fs.mkdirSync(OUT, { recursive: true });

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
    } catch (e) {
      record('alice: end-session removes the durable terminal from the UI', false, String(e).slice(0, 200));
    }
  }

  await alice.ctx.close();
  await bob.ctx.close();
} finally {
  await browser.close();
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
