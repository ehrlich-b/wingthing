import assert from 'node:assert/strict';
import { mkdirSync, readFileSync, existsSync, writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { execFileSync } from 'node:child_process';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

async function run() {
    const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
    const require = createRequire(resolve(root, 'package.json'));
    const { build } = require('esbuild');
    const { chromium } = require('playwright');
    const fixture = await build({ entryPoints: [resolve(root, 'test/support/presence-fixture.js')], bundle: true, write: false, format: 'iife', platform: 'browser', logLevel: 'silent' });
    const output = resolve(process.env.WT_PRESENCE_SHOTS || resolve(root, '../.scratch/presence-shots'));
    mkdirSync(output, { recursive: true });
    const origin = 'http://presence-fixture.localhost';
    const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH || undefined, args: ['--disable-background-networking', '--renderer-process-limit=2'] });
    const results = [];
    async function serve(context, page, hasPasskeys = true, discoveryWait = null) {
        await context.route('**/*', async route => {
            const url = new URL(route.request().url());
            assert.equal(url.origin, origin, 'all browser resources are local fixtures');
            const json = body => route.fulfill({ contentType: 'application/json', body: JSON.stringify(body) });
            if (url.pathname === '/api/app/me') return json({ id: 'fixture-owner', display_name: 'You', relay_allowed: true, has_passkeys: hasPasskeys });
            if (url.pathname === '/api/app/passkey') return json(hasPasskeys ? [{ credential_id: 'Zml4dHVyZQ' }] : []);
            if (url.pathname === '/api/app/wings') { if (discoveryWait) await discoveryWait; return json(await page.evaluate(() => presenceFixture.discovery())); }
            const relative = url.pathname === '/app/' ? 'index.html' : url.pathname.replace(/^\/app\//, '');
            const path = resolve(root, 'dist', relative);
            if (path.startsWith(resolve(root, 'dist') + '/') && existsSync(path)) return route.fulfill({ body: readFileSync(path), contentType: relative.endsWith('.js') ? 'text/javascript' : relative.endsWith('.css') ? 'text/css' : relative.endsWith('.html') ? 'text/html' : 'image/svg+xml' });
            throw Error('Unexpected fixture resource: ' + url.pathname);
        });
    }
    try {
        for (const width of [390, 1440]) {
            for (const count of [0, 1, 6, 8]) {
                const context = await browser.newContext({ viewport: { width, height: width === 390 ? 844 : 1000 }, timezoneId: 'America/New_York' });
                await context.addInitScript({ content: 'window.presenceCount = ' + count + ';\n' + fixture.outputFiles[0].text });
                const page = await context.newPage(), errors = [];
                page.setDefaultTimeout(20000);
                console.log('presence fixture', width, count);
                page.on('console', msg => { if (msg.type() === 'error') console.log('browser error:', msg.text()); });
                page.on('pageerror', error => errors.push(error.message));
                await serve(context, page);
                await page.goto(origin + '/app/#machines');
                if (!count) await page.getByRole('heading', { name: 'Connect a machine', exact: true }).waitFor();
                else {
                    await page.waitForFunction(n => JSON.parse(localStorage.getItem('wt_sessions') || '[]').length === n + 1 && presenceFixture.calls.filter(c => c.type === 'sessions.list').length >= Math.min(2, n), count).catch(async error => { console.log(JSON.stringify(await page.evaluate(() => ({ cache: localStorage.wt_sessions, calls: presenceFixture.calls, wings: localStorage.wt_wings, text: document.body.innerText })), null, 2)); throw error; });
                    await page.locator('[data-machine="machine-0"] .pp-mname b').getByText('Design workstation', { exact: false }).waitFor();
                }
                if (count === 8) {
                    assert.equal(await page.locator('[data-machine]').count(), 6);
                    await page.getByRole('button', { name: '+2 more machines', exact: true }).waitFor();
                }
                await page.screenshot({ path: resolve(output, `machines-${count}-${width}.png`), fullPage: true });
                assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'no horizontal overflow');
                if (count === 8) {
                    await page.getByRole('button', { name: '+2 more machines', exact: true }).click();
                    assert.equal(await page.locator('[data-machine]').count(), 8);
                    const before = await page.evaluate(() => ({ wings: JSON.parse(localStorage.wt_wings), sessions: JSON.parse(localStorage.wt_sessions) }));
                    // Ensure a real fixture peer socket is open before closing just this wing.
                    await page.evaluate(() => { location.hash = '#session/machine-0/same-id'; });
                    await page.locator('[data-takeover]').waitFor({ state: 'attached' });
                    await page.evaluate(() => presenceFixture.disconnect('machine-0'));
                    await page.locator('.pp-away').filter({ hasText: 'is offline' }).waitFor();
                    assert.equal(await page.locator('#paper [data-composer], #paper [data-takeover]').count(), 0);
                    assert.equal(await page.evaluate(() => JSON.parse(localStorage.wt_wings).find(w => w.wing_id === 'machine-0').last_seen), before.wings.find(w => w.wing_id === 'machine-0').last_seen, 'disconnect preserves last contact');
                    assert.match(await page.locator('.pp-main').innerText(), /Last reported: Working · stale/);
                    assert.match(await page.locator('.pp-away').innerText(), /Last seen.*Sessions are kept/);
                    assert.doesNotMatch(await page.locator('.pp-main').innerText(), /Asleep|deleted/i);
                    await page.screenshot({ path: resolve(output, `offline-session-${width}.png`), fullPage: true });
                    await page.evaluate(() => { location.hash = '#'; });
                    await page.locator('.home-offline[data-k="machine-0/same-id"]').waitFor();
                    assert.match(await page.locator('.pp-strip').innerText(), /is offline/);
                    assert.equal(await page.locator('#paper [data-composer]').count(), 0, 'offline coordinator has no composer');
                    // Retry from the list while another machine remains the selected route.
                    await page.evaluate(() => { location.hash = '#session/machine-1/same-id'; });
                    await page.locator('#paper [data-composer]').waitFor();
                    await page.locator('#pp-message').fill('Keep this unsent draft');
                    await page.evaluate(() => { location.hash = '#machines'; });
                    const retry = page.locator('[data-machine="machine-0"] [data-retry]');
                    await retry.waitFor();
                    const startCalls = await page.evaluate(() => presenceFixture.calls.length);
                    // Keep a selected session route while invoking the existing retry button.
                    await page.evaluate(() => { history.replaceState(null, '', '#session/machine-1/same-id'); document.querySelector('[data-machine="machine-0"] [data-retry]').click(); });
                    await page.locator('#paper [data-composer]').waitFor();
                    assert.equal(new URL(page.url()).hash, '#session/machine-1/same-id');
                    await page.waitForFunction(start => presenceFixture.calls.slice(start).some(c => c.type === 'sessions.list'), startCalls);
                    assert.equal(await page.locator('#pp-message').inputValue(), 'Keep this unsent draft');
                    const retryCalls = await page.evaluate(start => presenceFixture.calls.slice(start), startCalls);
                    assert.ok(retryCalls.every(c => ['wing.info', 'sessions.list', 'sessions.inventory'].includes(c.type) || c.type === 'session.control' && (c.operation === 'session_read' || c.operation === 'session_action' && c.arguments.operation === 'terminal_read' && c.arguments.origin_wing_id === c.wingId && Object.keys(c.arguments.arguments).length === 0)), 'retry only reads inventory and output');
                    assert.ok(retryCalls.every(c => c.wingId !== 'machine-0'), 'retry does not send to disconnected peer: ' + JSON.stringify(retryCalls));
                    const listedBeforeRecovery = await page.evaluate(() => presenceFixture.calls.filter(c => c.wingId === 'machine-0' && c.type === 'sessions.list').length);
                    await page.evaluate(() => presenceFixture.restore('machine-0'));
                    await page.waitForFunction(n => presenceFixture.calls.filter(c => c.wingId === 'machine-0' && c.type === 'sessions.list').length > n, listedBeforeRecovery);
                    assert.equal(new URL(page.url()).hash, '#session/machine-1/same-id', 'recovery preserves the other selection');
                    const after = await page.evaluate(() => ({ wings: JSON.parse(localStorage.wt_wings), sessions: JSON.parse(localStorage.wt_sessions), closed: presenceFixture.closedPeers }));
                    assert.ok(after.closed.includes('machine-0'));
                    assert.deepEqual(after.wings.map(w => [w.wing_id, w.public_key, w.hostname]), before.wings.map(w => [w.wing_id, w.public_key, w.hostname]));
                    assert.deepEqual(after.sessions.map(s => [s.wing_id, s.id]).sort(), before.sessions.map(s => [s.wing_id, s.id]).sort());
                    await page.evaluate(() => { location.hash = '#session/machine-0/same-id'; });
                    await page.locator('[data-takeover]').waitFor({ state: 'attached' });
                    await page.screenshot({ path: resolve(output, `recovered-session-${width}.png`), fullPage: true });
                    await page.evaluate(() => presenceFixture.unreachable('machine-0'));
                    await page.locator('.pp-away').waitFor().catch(async error => { console.log(JSON.stringify(await page.evaluate(() => ({ calls: presenceFixture.calls.slice(-20), text: document.querySelector('.pp-main').innerText })), null, 2)); throw error; });
                    assert.equal(await page.locator('#paper [data-composer], #paper [data-takeover]').count(), 0);
                    await page.evaluate(() => { location.hash = '#machines'; });
                    await page.locator('[data-machine="machine-0"] [data-retry]').waitFor();
                    assert.equal(await page.locator('#crypto-error-toast').count(), 0, 'ordinary connection loss is not a crypto error');
                    await page.screenshot({ path: resolve(output, `unreachable-machines-${width}.png`), fullPage: true });
                    results.push({ width, count, closedPeers: after.closed, identitiesPreserved: true, lastSeenPreserved: true, selectionPreserved: true, retryCalls, before, after, errors });
                } else results.push({ width, count, errors });
                assert.deepEqual(errors, []);
                await context.close();
            }
        }
        for (const width of [390, 1440]) {
            for (const [error, label, guidance] of [
                ['passkey_required', 'Locked', 'Unlock with your passkey.'],
                ['no_passkeys_configured', 'Locked', 'Set up a passkey to unlock this machine.'],
                ['not_allowed', 'No access', 'Ask the owner to grant access.'],
                ['decrypt', 'Key changed', 'Retry to refresh this machine’s key.'],
            ]) {
                const context = await browser.newContext({ viewport: { width, height: 1000 } });
                const wireError = error === 'no_passkeys_configured' ? 'passkey_required' : error;
                await context.addInitScript({ content: 'window.presenceCount = 8; window.presenceErrors = ' + JSON.stringify({ 'machine-1': wireError }) + ';\n' + fixture.outputFiles[0].text });
                const page = await context.newPage();
                await serve(context, page, error !== 'no_passkeys_configured');
                await page.goto(origin + '/app/#machines');
                const row = page.locator('[data-machine="machine-1"]');
                await row.locator('.pp-mname i').getByText(label, { exact: true }).waitFor();
                assert.ok((await row.innerText()).includes(guidance));
                assert.doesNotMatch(await row.innerText(), /Offline|Reconnect|Sessions kept/);
                assert.match(await page.locator('.pp-hdr').innerText(), /1 connected · 6 offline/);
                if (error === 'not_allowed') {
                    assert.equal(await row.locator('li').count(), 0);
                    assert.match(await row.innerText(), /Sessions unavailable/);
                } else assert.equal(await row.locator('.pp-st').innerText(), label);
                await page.screenshot({ path: resolve(output, `access-${error}-${width}.png`), fullPage: true });
                if (error !== 'not_allowed') {
                    await page.evaluate(() => { location.hash = '#sessions'; });
                    await page.locator('.pp-group').filter({ hasText: 'Access needed' }).waitFor();
                    await page.evaluate(() => { location.hash = '#session/machine-1/same-id'; });
                    await page.locator('.pp-hdr .pp-st').getByText(label, { exact: true }).waitFor();
                    assert.equal(await page.locator('#paper [data-composer], #paper [data-takeover]').count(), 0);
                    assert.ok((await page.locator('.pp-away').innerText()).includes(guidance));
                    assert.doesNotMatch(await page.locator('.pp-away').innerText(), /offline|Reconnect|Sessions.*kept/i);
                    await page.evaluate(() => { location.hash = '#approve/machine-1/same-id'; });
                    await page.waitForFunction(() => location.hash === '#session/machine-1/same-id');
                    await page.locator('.pp-away').waitFor();
                    assert.ok((await page.locator('.pp-away').innerText()).includes(guidance));
                    assert.doesNotMatch(await page.locator('.pp-away').innerText(), /offline|reconnect/i);
                    assert.equal(await page.locator('#paper [data-takeover]').count(), 0);
                }
                if (error !== 'decrypt') {
                    await page.evaluate(() => { location.hash = '#machines'; });
                    await page.locator('[data-machine="machine-1"] a').getByText('Machine access', { exact: true }).click();
                    const banner = page.locator('.wd-lock-banner');
                    await banner.waitFor();
                    assert.match(await banner.innerText(), error === 'not_allowed' ? /Ask the owner/ : error === 'no_passkeys_configured' ? /Configure a passkey/ : /Authenticate/);
                }
                results.push({ width, error, label, guidance, accessDistinctFromOffline: true });
                await context.close();
            }
            const context = await browser.newContext({ viewport: { width, height: 1000 } });
            await context.addInitScript({ content: 'window.presenceCount = 8; window.presenceUnnamed = true;\n' + fixture.outputFiles[0].text });
            const page = await context.newPage();
            let release;
            const discoveryWait = new Promise(resolve => { release = resolve; });
            await serve(context, page, true, discoveryWait);
            await page.goto(origin + '/app/#session/machine-0/same-id');
            await page.locator('.pp-hdr .pp-st-work').waitFor();
            assert.doesNotMatch(await page.locator('.pp-main').innerText(), /Unknown|stale/);
            assert.equal(await page.locator('#paper [data-composer], #paper [data-takeover]').count(), 0, 'checking does not offer runtime controls');
            assert.equal(await page.locator('.pp-side-squares .pp-more').innerText(), '+5 more', 'unnamed machines do not inflate the strip count');
            await page.screenshot({ path: resolve(output, `checking-session-${width}.png`), fullPage: true });
            release();
            await page.locator('#paper [data-takeover]').waitFor({ state: 'attached' });
            results.push({ width, checkingPreservesLifecycle: true, unnamedStripCount: '+5 more' });
            await context.close();
        }
    } finally { await browser.close(); }
    writeFileSync(resolve(output, 'receipt.json'), JSON.stringify({ sourceHead: execFileSync('git', ['rev-parse', 'HEAD'], { cwd: root, encoding: 'utf8' }).trim(), fixture: 'production web/dist with local encrypted wing peers', displayLimit: 6, planLimit: 'No machine-count plan limit exists in this head; captured the six-row display boundary.', results }, null, 2) + '\n');
    console.log('presence-browser PASS: offline socket, access failures, checking, strip count, stale state, controls, retry, identity and selection; 390/1440px, 0/1/6/8 machines');
}

if (!process.env.NODE_TEST_CONTEXT) await run();
