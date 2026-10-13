import assert from 'node:assert/strict';
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { dirname, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const require = createRequire(resolve(root, 'package.json'));
const origin = 'http://wingthing-fixture.localhost';

async function run() {
    const { build } = require('esbuild');
    const modulePath = process.env.WT_PLAYWRIGHT_MODULE;
    const { chromium } = modulePath ? await import(pathToFileURL(resolve(modulePath))) : await import('playwright');
    const fixture = await build({ entryPoints: [resolve(root, 'test/support/coordinator-fixture.js')], bundle: true, write: false, format: 'iife', platform: 'browser', logLevel: 'silent' });
    const output = resolve(process.env.WT_COORDINATOR_SHOTS || resolve(root, '../.scratch/coordinator-home-shots'));
    mkdirSync(output, { recursive: true });
    const html = readFileSync(resolve(root, 'dist/index.html'));
    const browser = await chromium.launch({ headless: true, executablePath: process.env.WT_CHROMIUM_EXECUTABLE || undefined, args: ['--disable-background-networking', '--renderer-process-limit=2'] });
    const results = [];
    try {
        for (const viewport of [{ width: 1440, height: 900 }, { width: 390, height: 844 }]) {
            const size = `${viewport.width}x${viewport.height}`;
            const context = await browser.newContext({ viewport, hasTouch: viewport.width < 600, timezoneId: 'America/New_York' });
            await context.addInitScript({ content: fixture.outputFiles[0].text });
            const page = await context.newPage(), errors = [];
            page.on('pageerror', error => errors.push(error.message));
            await context.route('**/*', async route => {
                const url = new URL(route.request().url());
                assert.equal(url.origin, origin, 'browser requests stay inside the fulfilled fixture origin');
                const json = body => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
                if (url.pathname === '/api/app/me') return json({ id: 'fixture-owner', display_name: 'You', relay_allowed: true, has_passkeys: true });
                if (url.pathname === '/api/app/wings') return json(await page.evaluate(() => coordinatorFixture.discovery()));
                if (url.pathname === '/app/' || url.pathname === '/') return route.fulfill({ contentType: 'text/html', body: html });
                const relative = url.pathname.startsWith('/app/') ? url.pathname.slice(5) : url.pathname.slice(1);
                if (!relative.includes('..')) {
                    const path = resolve(root, 'dist', relative);
                    if (existsSync(path)) return route.fulfill({ body: readFileSync(path), contentType: relative.endsWith('.js') ? 'text/javascript' : relative.endsWith('.css') ? 'text/css' : relative.endsWith('.woff2') ? 'font/woff2' : relative.endsWith('.svg') ? 'image/svg+xml' : 'application/json' });
                }
                throw Error('Unexpected fixture resource: ' + url.pathname);
            });
            await page.goto(origin + '/app/');
            await page.locator('#coordinator-home h1').filter({ hasText: 'Keep the next release moving' }).waitFor();
            assert.equal(await page.locator('#coordinator-home .coordinator-presence').innerText(), 'awake');
            assert.equal(await page.locator('#coordinator-nav button').count(), 1);
            assert.ok(await page.evaluate(() => document.getElementById('coordinator-nav').compareDocumentPosition(document.getElementById('session-tabs')) & Node.DOCUMENT_POSITION_FOLLOWING));
            assert.equal(await page.locator('.egg-box').count(), 2);
            assert.equal(await page.locator('.coordinator-heartbeat strong').count(), 2);
            assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
            assert.ok((await page.evaluate(() => coordinatorFixture.calls)).every(call => call.type !== 'session.control' || call.operation === 'session_read'));
            await page.screenshot({ path: resolve(output, `home-${size}.png`) });
            await page.locator('.coordinator-message').click();
            await page.locator('#chat-messages').getByText('I’m checking the remaining sessions.', { exact: false }).waitFor();
            assert.equal(await page.locator('#chat-input').isDisabled(), true, 'native work cannot receive an extra prompt');
            await page.screenshot({ path: resolve(output, `conversation-${size}.png`) });
            await page.locator('[data-session-view="activity"]').click();
            await page.locator('#session-activity [data-kind="approval"]').waitFor();
            assert.equal(await page.locator('#session-activity [data-artifact-path]').count(), 1);
            assert.equal(await page.locator('#session-activity a').getAttribute('rel'), 'noopener noreferrer');
            const count = await page.locator('#session-activity li').count();
            await page.waitForTimeout(2200);
            assert.equal(await page.locator('#session-activity li').count(), count, 'cursor polling does not duplicate timeline entries');
            await page.screenshot({ path: resolve(output, `activity-${size}.png`) });
            await page.locator('#session-activity [data-artifact-path]').click();
            assert.equal(await page.locator('#session-file-path').inputValue(), 'reports/release-brief.md');
            assert.equal(await page.locator('#session-files-panel').isVisible(), true);
            await page.locator('#session-files-close').click();
            await page.locator('.session-chat-back').click();
            await page.locator('.egg-box[data-wing-id="machine-b"] .egg-activity').click();
            await page.locator('#session-activity').getByText('This is the separate session on the second machine.', { exact: false }).waitFor();
            assert.ok((await page.evaluate(() => coordinatorFixture.calls)).some(call => call.wingId === 'machine-b' && call.operation === 'session_read' && call.arguments.session === 'same-id'));
            // Reload an open qualified text route, rather than returning Home.
            assert.equal(await page.evaluate(() => location.hash), '#chat/same-id?wing=machine-b');
            await page.reload();
            await page.locator('#chat-messages').getByText('This is the separate session on the second machine.', { exact: false }).waitFor();
            assert.equal(await page.evaluate(() => location.hash), '#chat/same-id?wing=machine-b');
            const resumedAt = await page.evaluate(() => coordinatorFixture.calls.length);
            await page.waitForFunction(after => coordinatorFixture.calls.slice(after).some(call => call.operation === 'session_read' && call.wingId === 'machine-b'), resumedAt);
            assert.ok((await page.evaluate(after => coordinatorFixture.calls.slice(after), resumedAt)).filter(call => call.operation === 'session_read').every(call => call.wingId === 'machine-b'));
            await page.locator('[data-session-view="activity"]').click();
            await page.locator('#session-activity').getByText('This is the separate session on the second machine.', { exact: false }).waitFor();
            await page.screenshot({ path: resolve(output, `qualified-reload-${size}.png`) });
            await page.locator('.session-chat-back').click();
            await page.reload();
            await page.locator('#coordinator-home h1').filter({ hasText: 'Keep the next release moving' }).waitFor();
            await page.locator('#coordinator-home .coordinator-presence').filter({ hasText: 'awake' }).waitFor();
            await page.evaluate(() => coordinatorFixture.transition('idle', { presence: 'asleep', last_wake: new Date(Date.now() - 10000).toISOString(), next_wake: new Date(Date.now() + 3600000).toISOString() }));
            await page.locator('#coordinator-home .coordinator-presence').filter({ hasText: 'asleep' }).waitFor();
            await page.screenshot({ path: resolve(output, `asleep-${size}.png`) });
            await page.evaluate(() => coordinatorFixture.transition('idle', { presence: 'asleep', next_wake: new Date(Date.now() - 1000).toISOString(), last_wake: new Date(Date.now() - 10000).toISOString() }));
            await page.locator('#coordinator-home .coordinator-presence').filter({ hasText: 'overdue' }).waitFor();
            await page.screenshot({ path: resolve(output, `overdue-${size}.png`) });
            await page.evaluate(() => coordinatorFixture.transition('exited'));
            await page.locator('#coordinator-home .coordinator-presence').filter({ hasText: 'stopped' }).waitFor();
            await page.screenshot({ path: resolve(output, `stopped-${size}.png`) });
            await page.evaluate(() => coordinatorFixture.offline(true));
            await page.locator('#coordinator-home .coordinator-presence').filter({ hasText: 'offline' }).waitFor();
            assert.equal(await page.locator('#coordinator-home .coordinator-task-state').innerText(), 'unknown');
            await page.screenshot({ path: resolve(output, `offline-${size}.png`) });
            assert.deepEqual(errors, []);
            results.push({ viewport: size, activityEntries: count, fulfilledTunnelCalls: (await page.evaluate(() => coordinatorFixture.calls)).length, errors });
            await context.close();
        }
    } finally { await browser.close(); }
    writeFileSync(resolve(output, 'receipt.json'), JSON.stringify({ build: 'web/dist', fixture: 'local encrypted wing peer', results }, null, 2) + '\n');
    console.log('coordinator-home-browser PASS ' + JSON.stringify(results));
}

// npm test discovers .mjs; browser validation is an explicit Make target.
if (!process.env.NODE_TEST_CONTEXT) await run();
