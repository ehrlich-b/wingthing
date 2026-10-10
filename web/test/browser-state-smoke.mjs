import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { dirname, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const webRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const cloneRoot = resolve(webRoot, '..');
const origin = 'http://wingthing-fixture.localhost';

async function smoke() {
    const staged = resolve(cloneRoot, '.scratch/playwright/node_modules/playwright/index.mjs');
    let playwright;
    try {
        playwright = process.env.WT_PLAYWRIGHT_MODULE
            ? await import(pathToFileURL(resolve(process.env.WT_PLAYWRIGHT_MODULE)).href)
            : existsSync(staged) ? await import(pathToFileURL(staged).href) : await import('playwright');
    } catch (error) {
        throw new Error('REQUIRED-UNRUN: staged Playwright is unavailable; run this coordinator gate with WT_PLAYWRIGHT_MODULE pointing to an offline installation.', { cause: error });
    }
    const require = createRequire(resolve(webRoot, 'package.json'));
    const { build } = require('esbuild');
    const sources = Object.fromEntries(['render', 'data', 'dashboard', 'nav', 'terminal'].map(name => [name,
        readFileSync(resolve(webRoot, `src/${name}.js`), 'utf8').replace(/^import .*;\n/gm, '').replace(/^export /gm, '')]));
    const bundle = await build({ stdin: { contents: `import { bootBrowserState } from './test/support/browser-state-page.js';\nbootBrowserState(${JSON.stringify(sources)}).catch(error => { window.fixtureError = error.stack; });`, resolveDir: webRoot },
        bundle: true, write: false, format: 'iife', platform: 'browser', logLevel: 'silent' });
    const html = readFileSync(resolve(webRoot, 'index.html'), 'utf8').replace(/%BASE_URL%/g, '/')
        .replace('<script type="module" src="src/main.js"></script>', '<script src="/fixture.js"></script>')
        .replace('</head>', '<link rel="stylesheet" href="/xterm.css"></head>');
    let browser;
    try {
        browser = await playwright.chromium.launch({ headless: true, args: ['--disable-background-networking', '--renderer-process-limit=2'] });
    } catch (error) {
        throw new Error('REQUIRED-UNRUN: staged Chromium cannot launch; no browser was downloaded and the smoke gate has not passed.', { cause: error });
    }
    const results = [];
    try {
        for (const viewport of [{ width: 1280, height: 800 }, { width: 390, height: 844 }]) {
            // No app bootstrap or service-worker registration runs in this
            // fresh context. Blocking via Playwright injects code into the
            // app's sandboxed blank preview iframe and creates a false error.
            const context = await browser.newContext({ viewport, hasTouch: viewport.width < 600 });
            const page = await context.newPage(), errors = [], requests = [];
            page.on('pageerror', error => errors.push(error.message));
            const wings = ['mac', 'linux'].map(wing_id => ({ wing_id, public_key: `fixture-${wing_id}`, user_id: 'fixture-owner' }));
            const remoteSessions = wing => [{ session_id: 'same-id', name: `${wing} session`, agent: 'codex', cwd: '/fixture', user_id: 'fixture-owner' }];
            await context.route('**/*', async route => {
                const url = new URL(route.request().url()); requests.push(url.pathname);
                assert.equal(url.origin, origin, 'every browser request stays on the fully fulfilled fixture origin');
                const json = body => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
                if (url.pathname === '/') return route.fulfill({ contentType: 'text/html', body: html });
                if (url.pathname === '/fixture.js') return route.fulfill({ contentType: 'text/javascript', body: bundle.outputFiles[0].text });
                if (url.pathname === '/api/app/wings') return json(wings);
                if (url.pathname === '/fixture/tunnel') {
                    const type = url.searchParams.get('type'), wing = url.searchParams.get('wing');
                    if (type === 'wing.info') return json({ hostname: wing, agents: ['codex'], projects: [], capabilities: ['session.rename.v1'] });
                    if (type === 'sessions.list') return json({ sessions: remoteSessions(wing) });
                    throw Error(`unexpected fixture request: ${type}`);
                }
                let path, contentType;
                if (url.pathname === '/style.css') { path = resolve(webRoot, 'style.css'); contentType = 'text/css'; }
                else if (url.pathname === '/xterm.css') { path = resolve(webRoot, 'node_modules/@xterm/xterm/css/xterm.css'); contentType = 'text/css'; }
                else if (/^\/(fonts|icons)\/[\w.-]+$/.test(url.pathname)) { path = resolve(webRoot, 'public', url.pathname.slice(1)); contentType = url.pathname.endsWith('.woff2') ? 'font/woff2' : 'image/svg+xml'; }
                else if (url.pathname === '/manifest.json') { path = resolve(webRoot, 'manifest.json'); contentType = 'application/json'; }
                if (path && existsSync(path)) return route.fulfill({ contentType, body: readFileSync(path) });
                throw Error(`unexpected local resource: ${url.pathname}`);
            });
            await page.goto(origin);
            await page.waitForFunction(() => window.fixture?.ready || window.fixtureError);
            assert.equal(await page.evaluate(() => window.fixtureError), undefined);
            assert.equal(await page.locator('.session-tab').count(), 2);
            await page.evaluate(() => {
                const { render, S } = fixture;
                for (let i = 0; i < 100; i++) {
                    const retained = document.querySelector('.session-tab');
                    render.renderSidebar(); retained.click();
                }
                if (fixture.actions.length) throw Error('a detached tab dispatched');
                S.activeView = 'home';
            });
            // Real keyboard focus survives refresh and activates one exact wing.
            await page.locator('.session-tab').first().focus();
            await page.keyboard.press('ArrowDown');
            await page.evaluate(() => fixture.render.renderSidebar());
            assert.equal(await page.evaluate(() => document.activeElement.dataset.wingId), 'linux');
            await page.keyboard.press('Enter');
            assert.deepEqual(await page.evaluate(() => fixture.actions), [['linux', 'same-id']]);
            await page.evaluate(() => fixture.nav.showHome(false));
            await page.locator('.session-tab[data-wing-id="mac"]').click();
            assert.deepEqual(await page.evaluate(() => fixture.actions.at(-1)), ['mac', 'same-id']);
            await page.evaluate(() => fixture.nav.showHome(false));
            // An offline event invalidates inventory until a new sweep confirms it.
            await page.evaluate(() => fixture.event({ type: 'wing.offline', wing_id: 'mac' }));
            const count = await page.evaluate(() => fixture.actions.length);
            await page.locator('.session-tab[data-wing-id="mac"]').click();
            assert.equal(await page.evaluate(() => fixture.actions.length), count);
            await page.evaluate(() => fixture.event({ type: 'wing.online', wing_id: 'mac', public_key: 'fixture-mac' }));
            await page.waitForFunction(() => fixture.S.sessionsData.find(s => s.wing_id === 'mac')?.swept);
            // Exercise the real application reconnect callback with a local socket.
            await page.evaluate(() => fixture.S.appWs.close());
            await page.waitForFunction(() => fixture.connections.length === 2);
            await page.evaluate(() => fixture.open());
            assert.equal(await page.locator('#reconnect-banner').isVisible(), false);
            await page.locator('.session-tab[data-wing-id="mac"]').click();
            await page.evaluate(() => fixture.write('restored 界🙂\r\n'));
            await page.evaluate(() => fixture.terminal.saveTermBuffer());
            await page.waitForFunction(() => fixture.buffer('same-id')?.includes('restored 界🙂'));
            assert.ok(await page.evaluate(() => fixture.cacheBytes()) <= 2000000);
            await page.evaluate(async () => {
                fixture.S.term.reset();
                await fixture.terminal.restoreTermBuffer('same-id', 'mac');
                await fixture.write('');
            });
            assert.match(await page.evaluate(() => fixture.screen()), /restored 界🙂/);
            // Current output remains pinned while bounded snapshots force eviction.
            const bytes = await page.evaluate(() => {
                const { terminal } = fixture, now = Date.now();
                for (let i = 0; i < 12; i++) {
                    if (!terminal.writeTerminalCache(localStorage, 'wt_termbuf_', 'mac', `old-${i}`, '界'.repeat(60000), now + i)) throw Error('bounded save failed');
                }
                if (fixture.buffer('old-0') !== null) throw Error('oldest snapshot survived pressure');
                if (!fixture.buffer('same-id')?.includes('restored')) throw Error('selected snapshot was evicted');
                return fixture.cacheBytes();
            });
            assert.ok(bytes <= 2000000);
            // Reload rebuilds metadata from persisted raw content.
            await page.reload();
            await page.waitForFunction(() => window.fixture?.ready || window.fixtureError);
            assert.equal(await page.evaluate(() => window.fixtureError), undefined);
            await page.locator('.session-tab[data-wing-id="mac"]').click();
            await page.evaluate(async () => { await fixture.terminal.restoreTermBuffer('same-id', 'mac'); await fixture.write(''); });
            assert.match(await page.evaluate(() => fixture.screen()), /restored 界🙂/);
            assert.ok(await page.evaluate(() => fixture.cacheBytes()) <= 2000000);
            if (viewport.width < 600) {
                assert.equal(await page.locator('#sidebar').isVisible(), false, 'mobile terminal maximizes width');
                assert.equal(await page.locator('.xterm-helper-textarea').getAttribute('inputmode'), 'text');
                await page.evaluate(() => fixture.nav.showHome(false));
                const sidebar = await page.locator('#sidebar').boundingBox();
                assert.equal(sidebar.width, 52);
                assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
            }
            assert.deepEqual(errors, []);
            results.push({ viewport: `${viewport.width}x${viewport.height}`, renderCycles: 100, cacheBytes: bytes, fulfilledRequests: requests.length });
            await context.close();
        }
    } finally { await browser.close(); }
    console.log(`browser-state-smoke PASS ${JSON.stringify(results)}`);
}

// npm test discovers .mjs files; Chromium remains an explicit standalone gate.
if (!process.env.NODE_TEST_CONTEXT) await smoke();
