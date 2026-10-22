import assert from 'node:assert/strict';
import { mkdirSync, readFileSync, writeFileSync, existsSync } from 'node:fs';
import { createRequire } from 'node:module';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const require = createRequire(resolve(root, 'package.json'));
const origin = 'http://needs-you-fixture.localhost';
const dist = resolve(process.env.WT_NEEDS_DIST || resolve(root, 'dist'));
const baseline = process.env.WT_NEEDS_BASELINE === '1';
const output = resolve(process.env.WT_NEEDS_EVIDENCE || resolve(root, '../.scratch/needs-you'));
const machineNames = ['Design workstation', 'Research laptop', 'Integration server'];
const sessionNames = ['Customer data migration and compatibility checks', 'Release strategy and integration planning', 'Signing permissions and package verification', 'Accessibility checks for the settings flow', 'Upgrade guidance for existing deployments', 'Mobile reconnect and recovery coverage', 'Documentation examples for new customers', 'Search quality for archived conversations'];
const scenarios = (process.env.WT_NEEDS_CASES || 'populated,volume,zero-attention,missing-transcript,offline,empty,no-sessions,one,plan-limit').split(',');
const requests = ['Allow npm test to verify the release?', 'Choose the migration strategy for customer data.', 'Allow access to the release signing directory?'];

async function run() {
    mkdirSync(output, { recursive: true });
    const { build } = require('esbuild');
    const { chromium } = require('playwright');
    const bundle = await build({ entryPoints: [resolve(root, 'test/support/send-fixture.js')], bundle: true, write: false, format: 'iife', platform: 'browser', logLevel: 'silent' });
    const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH || undefined, args: ['--disable-background-networking', '--renderer-process-limit=2'] });
    const results = [];
    try {
        for (const width of [390, 1440]) for (const theme of ['light', 'dark']) {
            for (const scenario of scenarios) {
                const context = await browser.newContext({ viewport: { width, height: width === 390 ? 844 : 1000 }, colorScheme: theme, timezoneId: 'America/New_York' });
                const count = scenario === 'empty' || scenario === 'no-sessions' ? 0 : scenario === 'one' ? 1 : 24;
                const waiting = scenario === 'zero-attention' ? 0 : scenario === 'volume' ? 8 : scenario === 'one' ? 1 : Math.min(3, count);
                let extra = false;
                const calls = [], errors = [];
                const sessions = Array.from({ length: count }, (_, i) => ({
                    session_id: i < 3 ? 'same-provider-id' : 'session-' + i,
                    wing_id: 'machine-' + (i % 3),
                    name: i === 23 ? 'wing' : sessionNames[i % sessionNames.length] + ' ' + (i + 1),
                    agent: i % 2 ? 'codex' : 'claude', cwd: '/fixture/release', user_id: 'fixture-owner',
                    pinned: i === 23, role: i === 23 ? 'coordinator' : '',
                    needs_attention: i < waiting,
                    lifecycle: { status: i < waiting ? 'blocked' : 'working', state: i < waiting ? 'needs_input' : 'working', reason: scenario === 'missing-transcript' ? '' : i < waiting ? requests[i % 3] : '', process_alive: true },
                }));
                const wings = scenario === 'empty' ? [] : machineNames.map((name, i) => ({ wing_id: 'machine-' + i, name, hostname: name, user_id: 'fixture-owner', online: scenario !== 'offline' || i !== 2, agents: ['claude', 'codex'], last_seen: '2026-10-20T16:00:00Z' }));
                const lifecycle = (wingId, id, after = 0) => {
                    const s = sessions.find(s => s.wing_id === wingId && s.session_id === id);
                    assert.ok(s, 'qualified session must exist');
                    const messages = id === 'session-23' ? Array.from({ length: 14 + Number(extra) }, (_, i) => ({ sequence: i + 1, type: 'message', role: 'assistant', text: i === 14 ? 'A new progress message arrived while you were reading.' : 'Update ' + (i + 1) + ': The compatibility checks are progressing. Release notes, migration examples and customer integration checks are ready for the next review.', timestamp: new Date(Date.UTC(2026, 9, 21, 14, i)).toISOString() })) : [];
                    const events = scenario === 'missing-transcript' ? [] : messages;
                    return { ...s.lifecycle, session_id: id, provider_session_id: id, agent: s.agent, state_source: 'claude_hook', ready: false, cursor: events.length, head_cursor: events.length, events: events.filter(e => e.sequence > after) };
                };
                await context.exposeBinding('sendFixtureRequest', (_, call) => {
                    calls.push(call);
                    const { wingId } = call;
                    if (call.type === 'wing.info') return { hostname: wings.find(w => w.wing_id === wingId).name, agents: ['claude', 'codex'], platform: 'linux', projects: [] };
                    if (call.type === 'sessions.list') return { sessions: sessions.filter(s => s.wing_id === wingId) };
                    if (call.type === 'sessions.inventory') return { version: 'wingthing.session-inventory.v1', wing_id: wingId, sessions: [], complete: true };
                    assert.equal(call.type, 'session.control');
                    if (call.operation === 'terminal_read') return { ansi: 'Check the current permission prompt before answering.' };
                    assert.equal(call.operation, 'session_read', 'the fixture permits reads only');
                    return { lifecycle: lifecycle(wingId, call.arguments.session, call.arguments.after_cursor) };
                });
                await context.addInitScript({ content: bundle.outputFiles[0].text });
                await context.addInitScript(({ wings, sessions }) => {
                    if (window.top !== window) return;
                    localStorage.setItem('wt_cache_owner_v1', 'fixture-owner');
                    localStorage.setItem('wt_wings', JSON.stringify(wings.map(w => ({ ...w, online: undefined }))));
                    localStorage.setItem('wt_sessions', JSON.stringify(sessions.map(s => ({ ...s, id: s.session_id }))));
                }, { wings, sessions });
                const page = await context.newPage();
                page.on('pageerror', e => errors.push(e.message));
                await context.route('**/*', async route => {
                    const url = new URL(route.request().url());
                    assert.equal(url.origin, origin);
                    const json = body => route.fulfill({ contentType: 'application/json', body: JSON.stringify(body) });
                    if (url.pathname === '/api/app/me') return json({ id: 'fixture-owner', email: 'owner@example.test', relay_allowed: scenario !== 'plan-limit', has_passkeys: true });
                    const peerKey = url.pathname === '/api/app/wings' ? await page.evaluate(() => sendFixture.publicKey) : null;
                    if (url.pathname === '/api/app/wings') return json(wings.filter(w => w.online).map(w => ({ ...w, public_key: peerKey })));
                    if (url.pathname === '/api/app/passkey') return json([]);
                    const relative = url.pathname === '/app/' ? 'index.html' : url.pathname.replace(/^\/app\//, '');
                    const file = resolve(dist, relative);
                    assert.ok(file.startsWith(dist + '/') && existsSync(file), 'fixture asset ' + relative);
                    return route.fulfill({ body: readFileSync(file), contentType: file.endsWith('.js') ? 'text/javascript' : file.endsWith('.css') ? 'text/css' : 'text/html' });
                });
                await page.goto(origin + '/app/');
                await page.locator('#paper:not([hidden])').waitFor();
                await page.waitForTimeout(1200);
                if (scenario !== 'plan-limit') {
                    assert.equal(await page.evaluate(() => JSON.parse(localStorage.wt_sessions || '[]').length), count, 'exact fixture session volume');
                    assert.equal(await page.evaluate(() => JSON.parse(localStorage.wt_wings || '[]').length), wings.length, 'exact fixture machine volume');
                }
                const shot = async name => {
                    await page.screenshot({ path: resolve(output, `${name}-${width}-${theme}-first.png`) });
                    await page.screenshot({ path: resolve(output, `${name}-${width}-${theme}-full.png`), fullPage: true });
                };
                await shot(scenario);
                assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, 'no horizontal overflow');
                if (!baseline && ['populated', 'volume', 'zero-attention', 'missing-transcript', 'offline', 'one'].includes(scenario)) {
                    const summary = page.locator('#pp-needs');
                    await summary.waitFor();
                    assert.equal(await summary.locator('.pp-need-row').count(), Math.min(waiting, 2));
                    assert.equal(await page.locator('.pp-conversation .pp-need').count(), 0, 'decision cards never fill the progress stream');
                    const bounds = await summary.boundingBox();
                    assert.ok(bounds.y >= 0 && bounds.y + bounds.height < (width === 390 ? 790 : 1000), 'summary fits first screen');
                    if (!waiting) assert.match(await summary.innerText(), /Nothing waiting/);
                    if (scenario === 'missing-transcript') assert.match(await summary.innerText(), /Request unavailable. Check the terminal./);
                    if (waiting) {
                        assert.equal(await summary.locator('a[href="#approve/machine-0/same-provider-id"]').count(), 1);
                        if (waiting > 2) assert.match(await summary.innerText(), new RegExp('\\+' + (waiting - 2) + ' more'));
                        const before = await summary.innerText();
                        await page.locator('.pp-conversation').evaluate(el => { el.scrollTop = 0; });
                        assert.equal(await summary.innerText(), before);
                        if (scenario === 'populated') {
                            assert.equal(await page.locator('.pp-conversation .pp-row').count(), 14);
                            extra = true;
                            await page.waitForFunction(() => [...document.querySelectorAll('.pp-say')].some(el => el.textContent.startsWith('A new progress message')), null, { timeout: 15000 });
                            assert.equal(await summary.innerText(), before, 'new messages preserve the summary');
                            await shot('new-message');
                        }
                        const nav = page.locator(width === 390 ? '.pp-tabs .pp-nav-wing' : '.pp-nav .pp-nav-wing');
                        await nav.click();
                        assert.equal(new URL(page.url()).hash, '#needs');
                        await page.waitForFunction(() => document.activeElement?.id === 'pp-needs');
                        if (waiting > 2) {
                            await page.locator('#pp-needs a[href="#decisions"]').click();
                            await page.waitForFunction(() => document.querySelector('.pp-main')?.dataset.view === 'decisions');
                            assert.equal(await page.locator('.pp-need-row').count(), waiting);
                            if (scenario === 'offline') assert.match(await page.locator('.pp-need-row').last().innerText(), /Offline/);
                            await shot(scenario + '-all');
                            await page.locator('a[href="#approve/machine-1/same-provider-id"]').click();
                            await page.waitForFunction(() => document.querySelector('.pp-main')?.dataset.view === 'approve');
                            assert.match(await page.locator('.pp-hdr').innerText(), /Research laptop/);
                            assert.ok(calls.some(c => c.wingId === 'machine-1' && c.operation === 'session_read' && c.arguments.session === 'same-provider-id'));
                        }
                    }
                }
                if (!baseline && scenario === 'plan-limit') {
                    assert.match(await page.locator('.pp-main').innerText(), /Get started/);
                    assert.equal(await page.locator('.pp-need-row').count(), 0);
                }
                if (!baseline && scenario === 'empty') await page.getByRole('heading', { name: 'No machines yet' }).waitFor();
                if (!baseline && scenario === 'no-sessions') await page.getByRole('heading', { name: 'No sessions yet' }).waitFor();
                assert.deepEqual(errors, []);
                results.push({ width, theme, scenario, sessions: count, machines: wings.length, waiting, calls: calls.length });
                writeFileSync(resolve(output, `calls-${scenario}-${width}-${theme}.json`), JSON.stringify(calls, null, 2) + '\n');
                await context.close();
            }
        }
    } finally { await browser.close(); }
    writeFileSync(resolve(output, 'results.json'), JSON.stringify({ baseline, results }, null, 2) + '\n');
    console.log(`PASS: ${results.length} local production-app scenarios with viewport and full-page captures`);
}
if (!process.env.NODE_TEST_CONTEXT) await run();
