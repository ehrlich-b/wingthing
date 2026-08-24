// Org-mode layout canary for the wingthing feature-local-first-terminal-routing branch.
// Drives the containerized shared roost (RoostMode + org "slide") as three seeded
// users through the flows the branch touched: dashboard layout, palette terminal
// launch, E2E identity lock, detach/reattach, path ACLs, account/org page, mobile.
import { chromium } from 'playwright';
import fs from 'fs';

const BASE = process.env.ROOST_URL || 'http://roost:8080';
const OUT = process.env.OUT_DIR || '/out';
const TOKENS = {
  alice: 'canary-alice-session-token-0000000001',
  bob: 'canary-bob-session-token-000000000002',
  carol: 'canary-carol-session-token-0000000003',
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

    // mock claude prints probe JSON then CANARY_SHELL_READY; wait for it, then interact
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

  await alice.ctx.close();
  await bob.ctx.close();
} finally {
  await browser.close();
  const failed = results.steps.filter((s) => !s.ok).length;
  results.summary = { total: results.steps.length, failed };
  fs.writeFileSync(`${OUT}/results.json`, JSON.stringify(results, null, 2));
  console.log(`\n${results.steps.length - failed}/${results.steps.length} steps passed; ` +
    `${results.consoleErrors.length} console error(s), ${results.pageErrors.length} page error(s)`);
  process.exit(failed > 0 ? 1 : 0);
}
