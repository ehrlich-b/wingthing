import assert from 'node:assert/strict';
import { mkdirSync, readFileSync, existsSync, writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { execFileSync } from 'node:child_process';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

async function run() {
    const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
    const require = createRequire(resolve(root, 'package.json'));
    const { build } = require('esbuild'), { chromium } = require('playwright');
    const fixture = await build({ entryPoints: [resolve(root, 'test/support/machine-detail-fixture.js')], bundle: true, write: false, format: 'iife', platform: 'browser', logLevel: 'silent' });
    const output = process.env.WT_MACHINE_SHOTS;
    assert.ok(output, 'WT_MACHINE_SHOTS must name an evidence directory');
    mkdirSync(output, { recursive: true });
    const before = process.env.WT_MACHINE_PHASE === 'before';
    const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH || undefined, args: ['--disable-background-networking', '--renderer-process-limit=2'] });
    const results = [];
    try {
        for (const width of [390, 1440]) for (const theme of ['light', 'dark']) for (const state of (process.env.WT_MACHINE_STATES || 'online,offline,empty,one,plan,overflow').split(',')) {
            const count = state === 'empty' ? 0 : state === 'one' ? 1 : state === 'overflow' ? 10 : 8;
            const context = await browser.newContext({ viewport: { width, height: width === 390 ? 844 : 1000 }, colorScheme: theme, timezoneId: 'America/New_York' });
            await context.addInitScript({ content: 'window.machineSessionCount = ' + count + ';\n' + fixture.outputFiles[0].text });
            const page = await context.newPage(), errors = [];
            page.on('pageerror', error => errors.push(error.message));
            await context.route('**/*', async route => {
                const url = new URL(route.request().url());
                assert.equal(url.origin, 'http://machine-fixture.localhost', 'local resources only');
                const json = body => route.fulfill({ contentType: 'application/json', body: JSON.stringify(body) });
                if (url.pathname === '/api/app/me') return json({ id: 'fixture-owner', display_name: 'You', relay_allowed: state !== 'plan', has_passkeys: true });
                if (url.pathname === '/api/app/wings') return json(await page.evaluate(() => presenceFixture.discovery()));
                if (url.pathname === '/api/app/passkey') return json([{credential_id:'Zml4dHVyZQ'}]);
                const relative = url.pathname === '/app/' ? 'index.html' : url.pathname.replace(/^\/app\//, '');
                const path = resolve(process.env.WT_MACHINE_DIST || resolve(root, 'dist'), relative);
                if (path.startsWith((process.env.WT_MACHINE_DIST || resolve(root, 'dist')) + '/') && existsSync(path)) return route.fulfill({ body: readFileSync(path), contentType: relative.endsWith('.js') ? 'text/javascript' : relative.endsWith('.css') ? 'text/css' : relative.endsWith('.html') ? 'text/html' : 'image/svg+xml' });
                throw Error('Unexpected resource ' + url.pathname);
            });
            await page.goto('http://machine-fixture.localhost/app/#w/machine-' + (state === 'offline' ? 1 : 0));
            if (state === 'plan') await page.getByRole('heading', {name:'Want browser control?'}).waitFor();
            else {
                await page.locator('#wd-name').waitFor();
                await page.waitForFunction(() => presenceFixture.calls.some(c => c.type === 'sessions.list'));
                if (state !== 'offline') await page.locator('#wd-past-sessions').getByText('no audited sessions', {exact:true}).waitFor({state:'attached'});
                assert.equal(await page.locator('.wd-session-row').count(), count);
            }
            if (!before && state !== 'plan') {
                assert.equal(await page.locator('#paper').isVisible(), true);
                assert.equal(await page.locator('#app').isVisible(), false);
                const text = await page.locator('.wd-page').innerText();
                assert.match(text, state === 'offline' ? /Offline[\s\S]*Last seen[\s\S]*Last seen sessions/ : /Connected[\s\S]*Claude, Codex[\s\S]*Sessions/);
                assert.equal(await page.locator('details.wd-details').first().getAttribute('open'), null);
                assert.doesNotMatch(text, /NaN|polling|refresh interval|protocol|cache|state ID/i);
                assert.ok(!text.includes('0.148.0') && !text.includes('/fixture/projects'));
                assert.equal(await page.locator('.wd-session-name').evaluateAll(nodes => nodes.every(n => n.scrollWidth <= n.clientWidth)), true, 'names wrap fully');
                await page.waitForFunction(() => {
                    const hero = document.querySelector('.wd-hero');
                    if (!hero) return false;
                    const bounds = hero.getBoundingClientRect();
                    return bounds.height > 0 && bounds.y + bounds.height < innerHeight;
                });
                if (state === 'offline') {
                    assert.equal(await page.locator('.wd-kill-btn').count(), 0);
                    assert.match(text, /Reconnect this machine, then retry/);
                    assert.equal(await page.locator('[data-choose-project]').count(), 0);
                }
                if (state === 'overflow') {
                    assert.equal(await page.locator('.wd-session-row:visible').count(), 8);
                    await page.getByText('+2 more sessions', {exact:true}).click();
                    assert.equal(await page.locator('.wd-session-row:visible').count(), 10);
                    await page.getByText('+2 more sessions', {exact:true}).click();
                }
                assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'no horizontal overflow');
            }
            const stem = `${before ? 'before' : 'after'}-${state}-${width}-${theme}`;
            await page.screenshot({path:resolve(output, stem + '-first.png')});
            await page.screenshot({path:resolve(output, stem + '.png'),fullPage:true});
            if (!before && state !== 'plan') {
                await page.locator('details.wd-details > summary').first().click();
                assert.match(await page.locator('details.wd-details').first().innerText(), /0.148.0[\s\S]*public key[\s\S]*\/fixture\/projects\/customer-reporting-service/);
                await page.screenshot({path:resolve(output,stem+'-details.png'),fullPage:true});
            }
            if (!before && width === 390 && theme === 'light' && state === 'online') {
                await page.locator('.wd-kill-btn').first().click();
                const confirmation = page.locator('.wd-session-row').first().locator('.inventory-action-status');
                await confirmation.waitFor();
                assert.match(await confirmation.innerText(), /Prepare the customer reporting migration/);
                assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'stop confirmation fits phone');
                assert.ok((await page.evaluate(() => presenceFixture.calls)).every(c => c.type !== 'session.stop'), 'first click never stops');
                await page.screenshot({path:resolve(output,'after-stop-confirmation-390-light.png'),fullPage:true});
                await page.getByRole('button',{name:'Keep running',exact:true}).click();
                await page.getByRole('link', {name:'New session',exact:true}).click();
                await page.getByRole('heading', {name:'New session',exact:true}).waitFor();
                assert.equal(await page.locator('#pp-launch-machine').inputValue(), 'machine-0');
                await page.evaluate(() => { location.hash = '#w/machine-0'; });
                await page.locator('#wd-name').waitFor();
                await page.locator('.wd-session-row').first().click();
                await page.locator('.pp-main[data-view="session"]').waitFor();
                assert.match(page.url(), /#session\/machine-0\/session-0/);
                await page.evaluate(() => { location.hash = '#w/machine-0'; });
                await page.locator('#wd-name').waitFor();
            }
            if (!before && width === 390 && theme === 'light' && state === 'offline') {
                const identity = await page.evaluate(() => presenceFixture.wings[1].public_key);
                await page.evaluate(() => presenceFixture.reconnect('machine-1'));
                assert.match(await page.locator('.wd-connection').innerText(), /Offline/);
                await page.getByRole('button', {name:'Retry connection',exact:true}).click();
                await page.locator('.wd-connection').filter({hasText:'Connected'}).waitFor();
                assert.equal(await page.evaluate(() => presenceFixture.wings[1].public_key), identity);
                assert.equal(await page.locator('.wd-session-row').count(), 8);
                const calls = await page.evaluate(() => presenceFixture.calls);
                assert.ok(calls.every(c => ['wing.info','sessions.list','sessions.inventory','sessions.history','allow.list','paths.list'].includes(c.type)), 'recovery only reads');
            }
            assert.deepEqual(errors, []);
            results.push({ width, theme, state, count, errors, screenshots: [stem+'-first.png',stem+'.png'] });
            await context.close();
        }
    } finally { await browser.close(); }
    writeFileSync(resolve(output,'receipt.json'),JSON.stringify({head:execFileSync('git',['rev-parse','HEAD'],{cwd:before && process.env.WT_MACHINE_DIST ? resolve(process.env.WT_MACHINE_DIST,'../..') : root,encoding:'utf8'}).trim(), phase:before?'before':'after', transport:'Local encrypted peers; no fixture transport failure is evidence of a product connectivity defect.', plan:'Existing relay_allowed=false hosted access boundary; there is no machine session-count plan limit.',results},null,2)+'\n');
    console.log('machine-detail-browser PASS: 0/1/8/10 sessions, inline overflow, offline, hosted entitlement, themes, details and responsive shell');
}
if (!process.env.NODE_TEST_CONTEXT) await run();
