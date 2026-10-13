import assert from 'node:assert/strict';
import { mkdirSync } from 'node:fs';
import { resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

async function run() {
    const [base, fixture, localOrigin, remoteOrigin, sessionID] = process.argv.slice(2);
    assert.equal(process.env.WT_TAKEOVER_EXPECT_REAL_ROOST, '1');
    assert.ok(base.startsWith('http://127.0.0.1:'));
    const { chromium } = await import(pathToFileURL(resolve(process.env.WT_PLAYWRIGHT_MODULE)).href);
    const shots = process.env.WT_TAKEOVER_SHOTS || resolve('.scratch/takeover-shots');
    mkdirSync(shots, { recursive: true });
    const browser = await chromium.launch({ headless: true, executablePath: process.env.WT_CHROMIUM_EXECUTABLE || undefined, args: ['--disable-background-networking', '--renderer-process-limit=2'] });
    try {
        const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
        const errors = [];
        page.on('pageerror', error => errors.push(error.message));
        await page.route('**/*', route => new URL(route.request().url()).origin === base ? route.continue() : route.abort());
        await page.goto(base);
        await page.waitForFunction(() => document.querySelectorAll('.wing-box').length === 2);
        const search = async () => {
            await page.locator('#session-search-input').fill('DNS helper');
            await page.locator('#session-search-form button').click();
            await page.waitForFunction(() => document.querySelectorAll('.search-session').length === 2);
        };
        const liveInventory = page.locator(`.search-session[data-origin="${remoteOrigin}"][data-session="${sessionID}"]`);
        await liveInventory.waitFor({ state: 'visible' });
        await page.locator('#coordinator-home .thread-bubble').filter({hasText:'The DNS helper is ready.'}).waitFor();
        for (const viewport of [{width:390,height:844},{width:430,height:932},{width:1440,height:900}]) {
            await page.setViewportSize(viewport);
            assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
            assert.equal(await page.locator('.fleet-machine').count(),2);
            assert.ok((await liveInventory.textContent()).includes('via Laptop'));
            await page.screenshot({path:resolve(shots, `pwa-home-${viewport.width}x${viewport.height}.png`)});
        }

        await liveInventory.getByRole('button', { name: 'Open session', exact: true }).click();
        await page.waitForFunction(() => document.querySelector('[data-transcript]').textContent.includes('Desktop DNS helper is ready'));
        assert.equal(await page.locator('#session-viewer [data-route]').textContent(), 'direct');
        await page.screenshot({ path: resolve(shots, '13-5-takeover-inventory-desktop.png') });
        await page.locator('#session-viewer [data-close]').click();
        await search();
        const rows = page.locator('.search-session');
        const target = rows.filter({ hasText: remoteOrigin });
        assert.equal(await rows.count(), 2);
        assert.equal(await target.count(), 1);
        assert.ok((await target.textContent()).includes('via Laptop'));
        assert.ok((await target.textContent()).includes('direct'));
        await target.getByRole('button', { name: 'Open session', exact: true }).click();
        const dialog = page.locator('#session-viewer');
        await dialog.waitFor({ state: 'visible' });
        await page.waitForFunction(() => document.querySelector('[data-transcript]').textContent.includes('Desktop DNS helper is ready'));
        assert.ok((await dialog.locator('[data-identity]').textContent()).includes(remoteOrigin + ' / ' + sessionID));
        assert.equal(await dialog.locator('[data-route]').textContent(), 'direct');
        await page.screenshot({ path: resolve(shots, '13-5-takeover-transcript-desktop.png') });
        await page.setViewportSize({ width: 430, height: 932 });
        await page.screenshot({ path: resolve(shots, 'pwa-transcript-430x932.png') });
        await page.setViewportSize({ width: 390, height: 844 });
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
        await page.screenshot({ path: resolve(shots, '13-5-takeover-transcript-mobile.png') });
        await dialog.locator('#viewer-message').fill('Check the new DNS record first.');
        await dialog.locator('[data-send]').click();
        try { await page.waitForFunction(() => document.querySelector('#session-viewer [data-status]').textContent === 'Message text observed by provider'); } catch (error) { await page.screenshot({path:resolve(shots,'diagnostic-send.png')}); console.log('Send status: '+await page.locator('#session-viewer [data-status]').textContent()); throw error; }
        await page.waitForFunction(() => document.querySelector('[data-transcript]').textContent.includes('Checked the DNS helper: Check the new DNS record first.'));
        await page.screenshot({ path: resolve(shots, '13-5-takeover-message-mobile.png') });
        await dialog.locator('[data-tab="terminal"]').click();
        await dialog.locator('[data-take]').click();
        await page.waitForFunction(() => document.querySelector('#session-viewer [data-status]').textContent === 'You control terminal input');
        await dialog.locator('[data-input]').fill('Human takeover input');
        await dialog.locator('[data-terminal-send]').click();
        await page.waitForFunction(() => document.querySelector('[data-terminal] .xterm-accessibility')?.textContent.includes('Human takeover input'));
        await page.waitForFunction(() => document.querySelector('[data-transcript]').textContent.includes('Human takeover input'));
        await page.screenshot({ path: resolve(shots, '13-5-takeover-control-mobile.png') });
        await page.setViewportSize({ width: 1440, height: 900 });
        await page.screenshot({ path: resolve(shots, '13-5-takeover-control-desktop.png') });
        await dialog.locator('[data-back]').click();
        await page.waitForFunction(() => document.querySelector('#session-viewer [data-status]').textContent.includes('Terminal input released'));
        assert.equal(await dialog.locator('[data-input]').isDisabled(), true);
        assert.equal(await dialog.locator('[data-take]').isVisible(), true);
        await page.screenshot({ path: resolve(shots, '13-5-takeover-handback-desktop.png') });
        await dialog.locator('[data-close]').click();
        // Real direct transport is dropped; the same exact live egg is reachable via gateway.
        assert.equal((await fetch(fixture + '/direct-down', { method: 'POST' })).status, 204);
        await search();
        await target.getByRole('button', { name: 'Open session', exact: true }).click();
        await page.waitForFunction(() => document.querySelector('[data-route]').textContent === 'via Laptop');
        await page.waitForFunction(() => document.querySelector('[data-transcript]').textContent.includes('Human takeover input'));
        await page.screenshot({ path: resolve(shots, '13-5-takeover-gateway-desktop.png') });
        await page.setViewportSize({ width: 390, height: 844 });
        await dialog.locator('[data-tab="terminal"]').click();
        await dialog.locator('[data-take]').click();
        await page.waitForFunction(() => document.querySelector('#session-viewer [data-status]').textContent === 'You control terminal input');
        await dialog.locator('[data-input]').fill('Gateway takeover input');
        await dialog.locator('[data-terminal-send]').click();
        await page.waitForFunction(() => document.querySelector('[data-transcript]').textContent.includes('Gateway takeover input'));
        await page.screenshot({ path: resolve(shots, '13-5-takeover-gateway-mobile.png') });
        await dialog.locator('[data-back]').click();
        await page.waitForFunction(() => document.querySelector('#session-viewer [data-status]').textContent.includes('Terminal input released'));
        await dialog.locator('[data-tab="transcript"]').click();
        await dialog.locator('#viewer-message').fill('Show a permission attention event.');
        await dialog.locator('[data-send]').click();
        await dialog.locator('[data-attention]').waitFor({ state: 'visible' });
        assert.ok((await dialog.locator('[data-attention]').textContent()).includes('no connected approval responder'));
        await page.screenshot({ path: resolve(shots, '13-5-takeover-attention-mobile.png') });
        await dialog.locator('[data-close]').click();
        await page.locator('#session-search-input').fill('');
        await page.waitForFunction(() => document.querySelectorAll('.search-session').length === 2);
        await page.locator('#coordinator-home .thread-bubble').filter({hasText:'Show a permission attention event.'}).waitFor();
        assert.ok((await page.locator('#coordinator-home').textContent()).includes('wing'));
        await page.screenshot({path:resolve(shots,'pwa-home-via-390x844.png')});
        assert.deepEqual(errors, []);
        console.log(JSON.stringify({ checks: ['session-list-entry', 'two-qualified-origins', 'direct-via-merge', 'live-transcript', 'native-message-text-receipt', 'real-PTY-takeover', 'hand-back-keeps-agent', 'direct-down-via-up', 'gateway-PTY-input', 'honest-approval-gap', 'desktop-mobile-layout'], machines: 2, real_build: true, real_encrypted_tunnels: true, fixture_provider: true, fixture_ssh: true, native_iphone: false }));
    } finally { await browser.close(); }
}
if (!process.env.NODE_TEST_CONTEXT) await run();
