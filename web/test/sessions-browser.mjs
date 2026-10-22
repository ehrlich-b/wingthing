import assert from 'node:assert/strict';
import { mkdirSync, readFileSync, existsSync, writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { execFileSync } from 'node:child_process';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createHash } from 'node:crypto';

async function run() {
    const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
    const require = createRequire(resolve(root, 'web/package.json'));
    const { build } = require('esbuild');
    const { chromium } = require('playwright');
    const baseline = process.argv.includes('--baseline');
    const acceptance = process.argv.find(a => /^A[1-5]$/.test(a));
    assert.ok(process.env.WT_SESSION_SHOTS, 'set an evidence directory outside the checkout');
    const output = resolve(process.env.WT_SESSION_SHOTS);
    assert.ok(!output.startsWith(root + '/'), 'keep evidence outside the checkout');
    mkdirSync(output, { recursive: true });
    if (!baseline && acceptance === 'A5') {
        const before = JSON.parse(readFileSync(resolve(output, '../before/receipt.json'), 'utf8'));
        assert.equal(before.baseline, true);
        assert.equal(before.head, execFileSync('git', ['rev-parse', '925b60b3'], { cwd: root, encoding: 'utf8' }).trim());
        assert.equal(before.screenshots.length, 48, 'all before scenarios have first-screen and full-page captures');
        for (const shot of before.screenshots) {
            assert.equal(createHash('sha256').update(readFileSync(shot.path)).digest('hex'), shot.sha256, shot.path);
        }
        console.log('A5 PASS: 48 original-head before screenshots verified by hash.');
    }
    const dist = resolve(process.env.WT_SESSION_DIST || resolve(root, 'web/dist'));
    const fixturePath = resolve(root, 'web/test/support/sessions-fixture.js');
    const fixture = await build({ entryPoints: [fixturePath], bundle: true, write: false, format: 'iife', platform: 'browser', logLevel: 'silent' });
    const origin = 'http://sessions-fixture.localhost';
    const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH || undefined, args: ['--disable-background-networking', '--renderer-process-limit=2'] });
    const results = [], screenshots = [];
    const matrix = acceptance && acceptance !== 'A5' ? [[390, 'light']] : [390, 1440].flatMap(width => ['light', 'dark'].map(theme => [width, theme]));
    try {
        for (const [width, theme] of matrix) {
            const scenarios = acceptance && acceptance !== 'A5' ? ['sessions'] : ['sessions', 'empty', 'one', 'plan-limit'];
            for (const scenario of scenarios) {
                const context = await browser.newContext({ viewport: { width, height: width === 390 ? 844 : 1000 }, colorScheme: theme, timezoneId: 'America/New_York' });
                await context.addInitScript({ content: 'window.sessionsScenario = ' + JSON.stringify(scenario) + ';\n' + fixture.outputFiles[0].text });
                const page = await context.newPage(), errors = [], requests = [];
                page.setDefaultTimeout(15000);
                page.on('pageerror', error => errors.push(error.message));
                await context.route('**/*', async route => {
                    const url = new URL(route.request().url());
                    assert.equal(url.origin, origin, 'all requests stay in the local fixture');
                    requests.push(url.pathname);
                    const json = body => route.fulfill({ contentType: 'application/json', body: JSON.stringify(body) });
                    if (url.pathname === '/api/app/me') return json({ id: 'fixture-owner', display_name: 'You', relay_allowed: scenario !== 'plan-limit', has_passkeys: true });
                    if (url.pathname === '/api/app/passkey') return json([{ credential_id: 'Zml4dHVyZQ' }]);
                    if (url.pathname === '/api/app/wings') return json(await page.evaluate(() => sessionsFixture.discovery()));
                    const relative = url.pathname === '/app/' ? 'index.html' : url.pathname.replace(/^\/app\//, '');
                    const path = resolve(dist, relative);
                    if (path.startsWith(dist + '/') && existsSync(path)) return route.fulfill({ body: readFileSync(path), contentType: relative.endsWith('.js') ? 'text/javascript' : relative.endsWith('.css') ? 'text/css' : relative.endsWith('.html') ? 'text/html' : 'image/svg+xml' });
                    throw Error('Unexpected fixture resource: ' + url.pathname);
                });
                await page.goto(origin + '/app/#sessions');
                const expectedCount = scenario === 'empty' ? 0 : scenario === 'one' ? 2 : 24;
                if (scenario !== 'plan-limit') await page.waitForFunction(n => JSON.parse(localStorage.getItem('wt_sessions') || '[]').length === n, expectedCount);
                if (scenario !== 'plan-limit') await page.waitForFunction(() => sessionsFixture.calls.filter(c => c.type === 'sessions.list').length >= 2);
                await page.locator('#paper .pp-hdr').waitFor();
                const shot = async name => {
                    await page.evaluate(() => window.scrollTo(0, 0));
                    for (const fullPage of [false, true]) {
                        const path = resolve(output, `${name}-${width}-${theme}${fullPage ? '' : '-first'}.png`);
                        await page.screenshot({ path, fullPage, animations: 'disabled' });
                        const png = readFileSync(path);
                        assert.equal(png.readUInt32BE(16), width);
                        if (!fullPage) assert.equal(png.readUInt32BE(20), width === 390 ? 844 : 1000);
                        screenshots.push({ path, sha256: createHash('sha256').update(png).digest('hex') });
                    }
                };
                const state = () => page.evaluate(() => ({
                    header: document.querySelector('.pp-hdr').innerText,
                    groups: [...document.querySelectorAll('.pp-group')].map(g => ({ heading: g.querySelector('.pp-kick').innerText, count: g.querySelector('[data-group-count]').textContent, visible: !g.hidden, rows: [...g.querySelectorAll('.pp-srow')].filter(r => !r.hidden).length })),
                    rows: [...document.querySelectorAll('.pp-group .pp-srow')].filter(r => !r.hidden).length,
                    pinned: document.querySelectorAll('.pp-pin').length,
                    empty: document.querySelector('[data-search-empty]')?.innerText,
                    emptyVisible: !!document.querySelector('[data-search-empty]') && !document.querySelector('[data-search-empty]').hidden,
                    clear: document.querySelectorAll('[data-clear-search]').length,
                }));
                if (scenario === 'sessions') {
                    await page.locator('.pp-group .pp-st-need').first().waitFor();
                    assert.equal(await page.locator('.pp-group .pp-srow').count(), 23);
                    assert.equal(await page.locator('.pp-pin').count(), 1);
                    const initial = await state();
                    const layout = await page.evaluate(() => {
                        const rect = sel => { const r = document.querySelector(sel)?.getBoundingClientRect(); return r ? { top: r.top, bottom: r.bottom, height: r.height } : null; };
                        return { search: rect('.pp-searchbar'), waiting: rect('.pp-group .pp-st-need'), panel: rect('.pp-col-main .pp-away'), status: rect('.pp-machine-status'), overflow: document.documentElement.scrollWidth > innerWidth };
                    });
                    if (!baseline) {
                        assert.ok(layout.search.bottom < layout.waiting.top);
                        assert.ok(layout.waiting.bottom < (width === 390 ? 844 : 1000), 'waiting session is in the first screen');
                        assert.equal(layout.panel, null, 'no long offline panel in the results');
                        assert.ok(layout.status.top > layout.waiting.bottom);
                        assert.ok(layout.status.height <= (width === 390 ? 70 : 45), 'offline notice stays compact');
                        assert.equal(layout.overflow, false);
                        assert.ok(await page.locator('.pp-machine-status a').evaluate(el => getComputedStyle(el).textDecorationLine.includes('underline')), 'recovery controls look like a link');
                        await page.locator('.pp-machine-status a').click();
                        await page.locator('[data-machine="machine-2"] [data-retry]').waitFor();
                        await page.evaluate(() => { location.hash = '#sessions'; });
                        await page.locator('#pp-filter').waitFor();
                        assert.equal(await page.locator('.pp-group .pp-srow').count(), 23);
                    }
                    await shot('sessions');
                    const assertCounts = data => { for (const g of data.groups) assert.equal(g.count, String(g.rows)); };
                    if (!baseline) {
                        assertCounts(initial);
                        assert.ok(initial.groups.some(g => g.heading.includes('Last seen on Travel notebook')));
                        assert.doesNotMatch(initial.groups.map(g => g.heading).join(' '), /cach|stale/i);
                    }
                    await page.locator('#pp-filter').fill('Travel notebook');
                    const partial = await state();
                    if (!baseline) {
                        assert.equal(partial.rows, 6);
                        assert.match(partial.header, /6 matches/);
                        assertCounts(partial);
                        assert.ok(partial.groups.every(g => g.visible === (g.rows > 0)));
                    }
                    await shot('sessions-filtered');
                    await page.locator('#pp-filter').fill('no-such-session-xyz');
                    const zero = await state();
                    assert.equal(zero.rows, 0);
                    assert.ok(zero.groups.every(g => !g.visible));
                    if (!baseline) {
                        assertCounts(zero);
                        assert.match(zero.header, /0 matches/);
                        assert.equal(await page.locator('[data-search-empty]:visible').count(), 1);
                        assert.match(zero.empty, /No sessions match “no-such-session-xyz”/);
                        assert.equal(zero.clear, 1);
                        assert.equal(zero.pinned, 1);
                    }
                    await shot('sessions-no-results');
                    if (baseline) await page.locator('#pp-filter').fill('');
                    else await page.getByRole('button', { name: 'Clear search', exact: true }).click();
                    const cleared = await state();
                    assert.equal(cleared.rows, 23);
                    assert.deepEqual(cleared.groups, initial.groups);
                    assert.equal(cleared.pinned, 1);
                    if (!baseline) {
                        assert.match(cleared.header, /23 sessions/);
                        assert.equal(await page.locator('#pp-filter').inputValue(), '');
                        assert.equal(await page.locator('#pp-filter').evaluate(el => el === document.activeElement), true);
                        // A live inventory rerender must retain the active search and counts.
                        await page.locator('#pp-filter').fill('Travel notebook');
                        await page.evaluate(() => sessionsFixture.unreachable('machine-1'));
                        await page.waitForFunction(() => [...document.querySelectorAll('.pp-machine-status')].some(el => el.textContent.includes('Research laptop')));
                        assert.equal(await page.locator('#pp-filter').inputValue(), 'Travel notebook');
                        assert.equal((await state()).rows, 6);
                        await page.locator('#pp-filter').fill('<img src=x onerror=alert(1)>');
                        assert.equal(await page.locator('[data-search-message] img').count(), 0);
                        assert.match(await page.locator('[data-search-message]').innerText(), /<img src=x/);
                        await page.locator('#pp-filter').fill('x'.repeat(160));
                        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, 'a long query stays within the screen');
                        await page.getByRole('button', { name: 'Clear search', exact: true }).click();
                    }
                    results.push({ width, theme, scenario, layout, initial, partial, zero, cleared, errors, requests });
                } else {
                    if (scenario === 'empty') {
                        await page.getByRole('heading', { name: 'No sessions yet', exact: true }).waitFor();
                        assert.equal(await page.locator('.pp-group').count(), 0);
                        assert.equal(await page.locator('[data-new-session]').count(), 1);
                    } else if (scenario === 'one') {
                        await page.locator('.pp-group .pp-srow').waitFor();
                        assert.equal(await page.locator('.pp-group .pp-srow').count(), 1);
                        assert.equal(await page.locator('.pp-pin').count(), 1);
                    } else {
                        await page.getByRole('heading', { name: 'Start free with your agent', exact: true }).waitFor();
                        assert.match(await page.locator('.pp-main').innerText(), /requires relay access on the account and permission on the selected machine/);
                        assert.equal(await page.locator('#pp-filter, .pp-group').count(), 0);
                    }
                    await shot('sessions-' + scenario);
                    results.push({ width, theme, scenario, text: await page.locator('.pp-main').innerText(), errors, requests });
                }
                assert.deepEqual(errors, []);
                console.log(`PASS ${baseline ? 'before observations' : acceptance || 'all acceptance'}: ${scenario} ${width} ${theme}`);
                await context.close();
            }
        }
    } finally { await browser.close(); }
    const receipt = { head: execFileSync('git', ['rev-parse', 'HEAD'], { cwd: root, encoding: 'utf8' }).trim(), baseline, acceptance, dist, fixtureSHA256: createHash('sha256').update(readFileSync(fixturePath)).digest('hex'), planLimit: 'This session screen has no numeric plan cap. The existing relay_allowed=false browser restriction is captured without changing its copy or permissions.', results, screenshots };
    writeFileSync(resolve(output, 'receipt.json'), JSON.stringify(receipt, null, 2) + '\n');
}
if (!process.env.NODE_TEST_CONTEXT) await run();
