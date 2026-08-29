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
  const publicContext = await browser.newContext({
    viewport: { width: 390, height: 844 },
    deviceScaleFactor: 2,
  });
  const publicPage = await publicContext.newPage();
  await publicPage.goto(BASE + '/', { waitUntil: 'domcontentloaded' });
  const publicState = await publicPage.evaluate(() => {
    const text = (element) => element ? element.textContent.trim().replace(/\s+/g, ' ') : '';
    const isVisible = (element) => {
      if (!element) return false;
      const rect = element.getBoundingClientRect();
      const style = getComputedStyle(element);
      return rect.width > 0 && rect.height > 0 && style.display !== 'none' && style.visibility !== 'hidden';
    };
    const comesBefore = (first, second) => Boolean(first && second &&
      (first.compareDocumentPosition(second) & Node.DOCUMENT_POSITION_FOLLOWING));
    const nav = document.querySelector('nav.site-nav');
    const logo = nav.querySelector('.logo').getBoundingClientRect();
    const links = nav.querySelector('.nav-links');
    const linkElements = Array.from(links.querySelectorAll('a'));
    const linkRects = linkElements.map((link) => link.getBoundingClientRect());
    const main = document.querySelector('main');
    const mainLinks = main ? Array.from(main.querySelectorAll('a[href]')) : [];
    const heading = main ? main.querySelector('h1') : null;
    const heroVideo = main ? main.querySelector('video, iframe[title*="video" i], [data-hero-video]') : null;
    const installCTA = mainLinks.find((link) => /\binstall\b/i.test(text(link)));
    const patternsLink = Array.from(document.querySelectorAll('a[href]')).find((link) => {
      const url = new URL(link.getAttribute('href'), location.href);
      return url.origin === location.origin && url.pathname.replace(/\/$/, '') === '/patterns' &&
        text(link).toLowerCase() === 'patterns';
    });
    const hierarchyAnchor = heroVideo || installCTA;
    const introElements = main ? Array.from(main.querySelectorAll('h1, p'))
      .filter((element) => comesBefore(element, hierarchyAnchor)) : [];
    const introText = introElements.map(text).join(' ');
    return {
      navLabels: linkElements.map((link) => link.textContent.trim()),
      ctaLabels: linkElements.filter((link) => link.classList.contains('nav-cta')).map((link) => link.textContent.trim()),
      flexWrap: getComputedStyle(links).flexWrap,
      linksBelowLogo: links.getBoundingClientRect().top >= logo.bottom,
      linksInsideViewport: linkRects.every((rect) => rect.width > 0 && rect.left >= 0 && rect.right <= innerWidth + 0.5),
      noHorizontalOverflow: document.documentElement.scrollWidth <= innerWidth,
      headingCount: main ? main.querySelectorAll('h1').length : 0,
      introText,
      routeMapCount: document.querySelectorAll('.route-map').length,
      dataRouteCount: document.querySelectorAll('[data-route]').length,
      patternsHref: patternsLink ? new URL(patternsLink.getAttribute('href'), location.href).pathname : '',
      patternsVisible: isVisible(patternsLink),
      heroConfigured: Boolean(heroVideo),
      heroVisible: isVisible(heroVideo),
      introBeforeHero: comesBefore(heading, heroVideo),
      heroBeforeInstall: comesBefore(heroVideo, installCTA),
      introBeforeInstall: comesBefore(heading, installCTA),
      installCTAText: text(installCTA),
      installCTAHref: installCTA ? installCTA.getAttribute('href') : '',
      installCTAVisible: isVisible(installCTA),
    };
  });
  record('public mobile: nav wraps below the logo without clipping and keeps neutral actions',
    JSON.stringify(publicState.navLabels) === JSON.stringify(['patterns', 'docs', 'github', 'install locally', 'login']) &&
      JSON.stringify(publicState.ctaLabels) === JSON.stringify(['install locally']) &&
      publicState.flexWrap === 'wrap' && publicState.linksBelowLogo &&
      publicState.linksInsideViewport && publicState.noHorizontalOverflow,
    JSON.stringify(publicState));
  record('public mobile: home keeps the concise local-agent hierarchy without the detailed route map',
    publicState.headingCount === 1 &&
      /\blocal\b/i.test(publicState.introText) && /\bagents?\b/i.test(publicState.introText) &&
      publicState.routeMapCount === 0 && publicState.dataRouteCount === 0 &&
      publicState.patternsHref === '/patterns' && publicState.patternsVisible &&
      (!publicState.heroConfigured || (publicState.heroVisible && publicState.introBeforeHero && publicState.heroBeforeInstall)) &&
      publicState.introBeforeInstall && publicState.installCTAVisible &&
      /\binstall\b/i.test(publicState.installCTAText) && Boolean(publicState.installCTAHref),
    JSON.stringify(publicState));
  const patternsResponse = await publicContext.request.get(BASE + publicState.patternsHref);
  record('public mobile: patterns remains the reachable detailed-route destination',
    publicState.patternsHref === '/patterns' && patternsResponse.ok(),
    JSON.stringify({ href: publicState.patternsHref, status: patternsResponse.status() }));
  await publicPage.screenshot({ path: `${OUT}/public-mobile-nav.png`, fullPage: false });
  await publicContext.close();

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
