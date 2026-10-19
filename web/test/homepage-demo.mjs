// Run explicitly after `make web`; npm test only checks ordinary unit tests.
// Every browser request is fulfilled locally, including the encrypted peer.
import assert from 'node:assert/strict';
import { mkdirSync, readFileSync, writeFileSync, existsSync } from 'node:fs';
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { createRequire } from 'node:module';
import { dirname, resolve, extname } from 'node:path';
import { fileURLToPath } from 'node:url';

async function run() {
    const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
    const require = createRequire(resolve(root, 'package.json'));
    const { build } = require('esbuild');
    const { chromium } = require('playwright');
    const output = resolve(process.env.WT_DEMO_OUT || resolve(root, '../.scratch/homepage-demo'));
    const media = resolve(root, 'public/demo');
    mkdirSync(output, { recursive: true });
    const record = process.argv.includes('--record');
    if (record) mkdirSync(media, { recursive: true });
    const bundle = await build({ entryPoints: [resolve(root, 'test/support/homepage-demo-fixture.js')], bundle: true, write: false, format: 'iife', platform: 'browser', logLevel: 'silent' });
    const origin = 'http://demo.localhost';
    const browser = await chromium.launch({ headless: true, args: ['--disable-background-networking', '--renderer-process-limit=2'] });
    const results = [];
    try {
        for (const width of [1440, 390]) {
            for (const count of [0, 1, 8]) {
                const viewport = { width, height: width === 390 ? 844 : 900 };
                const recording = record && width === 1440 && count === 8;
                const context = await browser.newContext({ viewport, reducedMotion: 'reduce', timezoneId: 'UTC', ...(recording ? { recordVideo: { dir: resolve(output, 'raw-video'), size: viewport } } : {}) });
                await context.addInitScript({ content: bundle.outputFiles[0].text });
                await context.addInitScript(value => { window.homepageDemo.setCount(value); }, count);
                const started = Date.now(), cues = [];
                let trimStart = 0;
                const cue = text => cues.push({ seconds: (Date.now() - started) / 1000, text });
                const page = await context.newPage();
                cue('Find a session across Studio and Laptop. Example sessions, fictional data.');
                const errors = [];
                page.on('pageerror', error => errors.push(error.message));
                await page.route('**/*', async route => {
                    const url = new URL(route.request().url());
                    assert.equal(url.origin, origin, 'no external requests');
                    const json = body => route.fulfill({ contentType: 'application/json', body: JSON.stringify(body) });
                    if (url.pathname === '/api/app/me') return json({ id: 'example-owner', display_name: 'You', relay_allowed: true, has_passkeys: true });
                    if (url.pathname === '/api/app/wings') return json(await page.evaluate(() => homepageDemo.wings));
                    const relative = url.pathname === '/app/' ? 'index.html' : url.pathname.replace(/^\/app\//, '');
                    const file = resolve(root, 'dist', relative);
                    assert.ok(file.startsWith(resolve(root, 'dist') + '/') && existsSync(file), 'unexpected resource ' + url.pathname);
                    const types = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.woff2': 'font/woff2', '.svg': 'image/svg+xml', '.png': 'image/png' };
                    return route.fulfill({ body: readFileSync(file), contentType: types[extname(file)] || 'application/octet-stream' });
                });
                await page.goto(origin + '/app/');
                await page.locator('a[href="#sessions"]:visible').click();
                if (count) await page.locator('.pp-srow').nth(count - 1).waitFor();
                else await page.locator('.pp-main').getByText('No sessions on Studio, Laptop', { exact: true }).waitFor();
                await page.waitForTimeout(800);
                assert.equal(await page.locator('.pp-srow').count(), count);
                assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'no horizontal overflow');
                const inventory = `${width}-${count}-sessions.png`;
                await page.screenshot({ path: resolve(output, inventory), fullPage: true });
                if (count === 8) {
                    if (recording) {
                        await page.screenshot({ path: resolve(media, 'poster.png') });
                        trimStart = (Date.now() - started) / 1000;
                        await page.waitForTimeout(3500);
                    }
                    await page.locator('.pp-srow[href="#session/studio/example-0"]').click();
                    await page.locator('.pp-conversation').getByText('All 18,204 rows', { exact: false }).waitFor();
                    cue('Read its progress: 18,204 rows verified and migration tests passing.');
                    await page.screenshot({ path: resolve(output, `${width}-read-progress.png`), fullPage: true });
                    if (recording) await page.waitForTimeout(4500);
                    const input = page.locator('#pp-message');
                    await input.fill('Switch reads to the new table, then run the migration tests.');
                    cue('Send the next instruction to this session.');
                    if (recording) await page.waitForTimeout(2500);
                    await page.locator('.pp-composer button[type="submit"]').click();
                    await page.locator('#chat-send').click();
                    await page.locator('#chat-messages').getByText('Reads now use the new table.', { exact: false }).waitFor();
                    cue('The session confirms: reads switched, tests passing, rollback documented.');
                    const prompts = await page.evaluate(() => homepageDemo.calls.filter(call => call.operation === 'session_prompt'));
                    assert.equal(prompts.length, 1);
                    assert.equal(prompts[0].wingId, 'studio');
                    assert.equal(prompts[0].arguments.session, 'example-0');
                    await page.screenshot({ path: resolve(output, `${width}-next-instruction.png`), fullPage: true });
                    if (recording) await page.waitForTimeout(5000);
                }
                assert.deepEqual(errors, []);
                const video = page.video();
                const ended = (Date.now() - started) / 1000;
                await context.close();
                if (recording) {
                    const raw = await video.path();
                    // Remove app bootstrap screens; retain the actual recorded
                    // interaction, with no cuts after the inventory is ready.
                    execFileSync(process.env.FFMPEG || 'ffmpeg', ['-y', '-hide_banner', '-ss', String(trimStart), '-i', raw, '-t', String(ended - trimStart), '-an', '-c:v', 'libvpx-vp9', '-deadline', 'realtime', '-cpu-used', '4', '-b:v', '0', '-crf', '32', resolve(media, 'homepage.webm')], { stdio: 'pipe' });
                    const time = seconds => new Date(Math.round(seconds * 1000)).toISOString().slice(11, 23);
                    cues.forEach(item => { item.seconds = Math.max(0, item.seconds - trimStart); });
                    writeFileSync(resolve(media, 'captions.vtt'), 'WEBVTT\n\n' + cues.map((item, index) => `${time(item.seconds)} --> ${time(cues[index + 1]?.seconds || ended - trimStart)}\n${item.text}\n`).join('\n'));
                }
                results.push({ width, count, inventory, errors, journey: count === 8 ? 'read progress; send one instruction to the selected session; observe reply' : 'inventory' });
            }
        }
    } finally { await browser.close(); }
    const sha256 = file => createHash('sha256').update(readFileSync(file)).digest('hex');
    writeFileSync(resolve(output, 'receipt.json'), JSON.stringify({ fixture: 'fictional data; real production web build; local encrypted peer', buildSha256: sha256(resolve(root, 'dist/index.html')), videoSha256: existsSync(resolve(media, 'homepage.webm')) ? sha256(resolve(media, 'homepage.webm')) : null, results }, null, 2) + '\n');
    console.log('homepage-demo PASS: ' + results.length + ' scenarios');
}

if (!process.env.NODE_TEST_CONTEXT) await run();
