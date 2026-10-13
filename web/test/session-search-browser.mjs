import assert from 'node:assert/strict';
import { mkdirSync } from 'node:fs';
import { resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

async function run() {
const [base, fixture, localOrigin, remoteOrigin] = process.argv.slice(2);
assert.equal(process.env.WT_SEARCH_EXPECT_REAL_ROOST, '1', 'run through the isolated Go fixture');
assert.ok(base?.startsWith('http://127.0.0.1:'));
const { chromium } = await import(pathToFileURL(resolve(process.env.WT_PLAYWRIGHT_MODULE)).href);
const shots = process.env.WT_SEARCH_SHOTS || resolve('.scratch/search-shots');
mkdirSync(shots, { recursive: true });
const browser = await chromium.launch({ headless: true, executablePath: process.env.WT_CHROMIUM_EXECUTABLE || undefined, args: ['--disable-background-networking', '--renderer-process-limit=2'] });
try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    // No public service, provider, login or live state may participate.
    await page.route('**/*', route => {
        const url = new URL(route.request().url());
        if (url.origin === base) return route.continue();
        return route.abort();
    });
    await page.goto(base);
    await page.waitForSelector('.wing-box');
    const input = page.locator('#session-search-input');
    await input.fill('DNS helper');
    await page.locator('#session-search-form button').click();
    await page.waitForFunction(() => document.getElementById('session-search-status').textContent === '2 sessions · coverage complete');
    const rows = page.locator('.search-session');
    assert.equal(await rows.count(), 2);
    assert.ok((await rows.nth(0).textContent()).includes(localOrigin));
    assert.ok((await rows.nth(1).textContent()).includes(remoteOrigin));
    assert.ok((await rows.nth(1).textContent()).includes('via Laptop'));
    assert.equal(await page.locator('#sessions-list').isVisible(), false);
    await page.screenshot({ path: resolve(shots, '13-4-search-sessions-desktop.png'), fullPage: false });
    await page.setViewportSize({ width: 390, height: 844 });
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
    await page.screenshot({ path: resolve(shots, '13-4-search-sessions-mobile.png'), fullPage: false });
    await rows.nth(1).getByRole('button', { name: 'Read transcript' }).click();
    await page.waitForFunction(() => document.getElementById('session-search-transcript').textContent.includes('Fixed the DNS helper on Desktop.'));
    assert.ok((await page.locator('#session-search-transcript').textContent()).includes('via Laptop'));
    await page.screenshot({ path: resolve(shots, '13-4-search-transcript-mobile.png'), fullPage: false });
    await input.fill('sk-fixtureSECRET123456789');
    await page.locator('#session-search-form button').click();
    await page.waitForFunction(() => document.getElementById('session-search-status').textContent === '0 sessions · coverage complete');
    await input.fill('DNS helper');
    await page.locator('#session-search-form button').click();
    await page.waitForFunction(() => document.getElementById('session-search-status').textContent === '2 sessions · coverage complete');
    const response = await fetch(fixture + '/offline', { method: 'POST' });
    assert.equal(response.status, 204);
    await page.locator('#session-search-form button').click();
    await page.waitForFunction(() => document.getElementById('session-search-status').textContent === '1 sessions · coverage incomplete');
    assert.equal(await rows.count(), 1);
    assert.ok((await rows.first().textContent()).includes(localOrigin));
    await page.screenshot({ path: resolve(shots, '13-11-search-remote-unreachable-mobile.png'), fullPage: false });
    await input.fill('');
    await page.waitForFunction(() => document.getElementById('session-search-results').hidden);
    assert.deepEqual(errors, []);
    console.log(JSON.stringify({ checks: ['two-origins', 'exact-remote-read', 'redaction', 'outage-local-survival', 'mobile-layout'], surfaces: ['desktop', 'mobile-width'], real_build: true, real_tunnels: true, machines: 2, fixture_ssh: true }));
} finally { await browser.close(); }

}
if (!process.env.NODE_TEST_CONTEXT) await run();
