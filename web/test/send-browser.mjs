import assert from 'node:assert/strict';
import { mkdirSync, readFileSync, writeFileSync, existsSync } from 'node:fs';
import { createRequire } from 'node:module';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const require = createRequire(resolve(root, 'package.json'));
const origin = 'http://send-fixture.localhost';
const text = '  Ω inspect the release notes\nkeep the exact spacing\t  \n';

async function run() {
    const { build } = require('esbuild');
    const { chromium } = await import('playwright');
    const bundle = await build({ entryPoints: [resolve(root, 'test/support/send-fixture.js')], bundle: true, write: false, format: 'iife', platform: 'browser', logLevel: 'silent' });
    const output = resolve(process.env.WT_SEND_EVIDENCE || resolve(root, '../.scratch/send'));
    mkdirSync(output, { recursive: true });
    const browser = await chromium.launch({ headless: true, args: ['--disable-background-networking', '--renderer-process-limit=2'] });
    const results = [];
    try {
        for (const width of [390, 1440]) {
            const context = await browser.newContext({ viewport: { width, height: 900 }, hasTouch: width < 600, colorScheme: 'light' });
            let agent = 'claude', state = 'idle', alive = true, mode = 'hold', count = 12, offline = false;
            let delivered = 0;
            const calls = [], ledger = new Map(), events = new Map(), errors = [];
            const lifecycle = (id, after = 0, limit = 100) => {
                const all = events.get(id) || [{ sequence: 1, type: 'message', role: 'assistant', text: 'The release notes and migration checklist are ready for your next instruction.', timestamp: new Date().toISOString() }];
                return { session_id: id, agent, provider_session_id: 'provider-' + id, state, status: alive ? state === 'completed' ? 'done' : state : 'exited', state_source: agent === 'unsupported' ? 'unsupported' : agent + '_hook', ready: alive && ['idle', 'completed'].includes(state), process_alive: alive,
                    head_cursor: all.length, state_cursor: all.length, cursor: all.filter(e => e.sequence > after).slice(0, limit).at(-1)?.sequence || after, events: all.filter(e => e.sequence > after).slice(0, limit) };
            };
            const sessions = () => Array.from({ length: count }, (_, i) => ({ session_id: i === 0 ? 'wing' : 'session-' + i, name: i === 0 ? 'wing' : 'Release migration and customer compatibility investigation ' + i, agent, cwd: '/fixture/release', user_id: 'fixture-owner', pinned: i === 0, role: i === 0 ? 'coordinator' : '', lifecycle: lifecycle(i === 0 ? 'wing' : 'session-' + i) }));
            const answer = call => {
                calls.push(call);
                if (call.type === 'wing.info') return { hostname: 'Release workstation', agents: ['claude', 'codex'], platform: 'linux', capabilities: [] };
                if (call.type === 'sessions.list') return { sessions: sessions() };
                if (call.type === 'sessions.inventory') return { version: 'wingthing.session-inventory.v1', wing_id: call.wingId, sessions: [], complete: true };
                assert.equal(call.type, 'session.control');
                const args = call.arguments, id = args.session;
                if (call.operation === 'terminal_read') return { result: { ansi: 'Release workspace\nReady for your next message.' } };
                if (call.operation === 'session_read') return { session: id, lifecycle: lifecycle(id, args.after_cursor, args.limit) };
                assert.equal(call.operation, 'session_prompt');
                const key = call.wingId + '/' + id + '/' + args.request_id;
                let entry = ledger.get(key);
                if (entry) assert.equal(entry.input, args.input, 'receipt check must preserve text');
                else {
                    entry = { input: args.input, session_id: id, request_id: args.request_id, status: 'unconfirmed', native_receipt_observed: false, transport_enqueued: true };
                    ledger.set(key, entry);
                    if (mode === 'not-sent') Object.assign(entry, { status: 'not_sent', definitely_not_sent: true });
                    else {
                        delivered++;
                        const all = lifecycle(id).events;
                        events.set(id, [...all, { sequence: all.length + 1, type: agent === 'codex' ? 'prompt_submitted' : 'message', source: agent === 'codex' ? 'codex_hook' : 'claude_transcript', role: 'user', text: args.input, timestamp: new Date().toISOString() }]);
                    }
                }
                if (mode === 'receipt' && entry.status !== 'not_sent') Object.assign(entry, { status: 'native_receipt_observed', native_receipt_observed: true, receipt_cursor: 2, reserved_cursor: 1 });
                return { session: id, receipt: { ...entry } };
            };
            await context.exposeBinding('sendFixtureRequest', (_, call) => answer(call));
            await context.addInitScript({ content: bundle.outputFiles[0].text });
            const page = await context.newPage();
            page.on('pageerror', error => errors.push(error.message));
            await context.route('**/*', async route => {
                const url = new URL(route.request().url());
                assert.equal(url.origin, origin);
                const json = body => route.fulfill({ contentType: 'application/json', body: JSON.stringify(body) });
                if (url.pathname === '/api/app/me') return json({ id: 'fixture-owner', email: 'owner@example.com', relay_allowed: true, has_passkeys: true });
                if (url.pathname === '/api/app/wings') return json([{ wing_id: 'machine-a', public_key: await page.evaluate(() => sendFixture.publicKey), user_id: 'fixture-owner', online: !offline }]);
                const file = url.pathname === '/app/' ? resolve(root, 'dist/index.html') : resolve(root, 'dist', url.pathname.replace(/^\/app\//, ''));
                assert.ok(file.startsWith(resolve(root, 'dist')) && existsSync(file), 'fixture asset: ' + url.pathname);
                return route.fulfill({ body: readFileSync(file), contentType: file.endsWith('.js') ? 'text/javascript' : file.endsWith('.css') ? 'text/css' : 'text/html' });
            });
            const input = page.locator('#pp-message'), send = page.locator('#paper [type="submit"]');
            const ready = async () => { await input.waitFor(); await page.waitForFunction(() => !document.querySelector('#pp-message').disabled); };
            const shot = name => page.screenshot({ path: resolve(output, `${name}-${width}.png`), fullPage: true });
            await page.goto(origin + '/app/'); await ready();
            await shot('volume-12');
            if (width === 1440) assert.equal(await page.locator('#paper .pp-mini').count(), 8);
            for (const provider of ['claude', 'codex']) {
                agent = provider; events.clear(); await page.evaluate(() => localStorage.clear()); await page.reload(); await ready();
                for (const target of ['wing', 'session-1']) {
                    await page.evaluate(id => { location.hash = id === 'wing' ? '#' : '#session/machine-a/' + id; }, target);
                    await ready();
                    const start = delivered;
                    await input.fill(' \n\t ');
                    assert.equal(await send.isDisabled(), true);
                    await page.locator('#paper [data-composer]').evaluate(form => form.requestSubmit());
                    assert.equal(delivered, start);
                    await input.fill(text); await send.click();
                    await page.locator('#paper .pp-delivery').filter({ hasText: 'Delivery unconfirmed' }).waitFor();
                    assert.equal(delivered, start + 1);
                    assert.equal(await page.locator('#paper .pp-delivery').filter({ hasText: 'Delivered' }).count(), 0);
                    assert.equal([...ledger.values()].at(-1).input, text);
                    const request = [...ledger.values()].at(-1).request_id;
                    await page.locator('#paper [data-composer]').evaluate(form => form.requestSubmit());
                    assert.equal(delivered, start + 1);
                    await shot(`${provider}-${target}-unconfirmed`);
                    const prompts = calls.filter(c => c.operation === 'session_prompt').length;
                    await page.reload();
                    await page.locator('#paper .pp-delivery').filter({ hasText: 'Delivery unconfirmed' }).waitFor();
                    assert.equal(calls.filter(c => c.operation === 'session_prompt').length, prompts, 'reopen never sends or checks');
                    mode = 'receipt';
                    await page.getByRole('button', { name: 'Check receipt', exact: true }).click();
                    await page.locator('#paper .pp-delivery').filter({ hasText: 'Delivered' }).waitFor();
                    assert.equal(delivered, start + 1);
                    await page.waitForFunction(value => [...document.querySelectorAll('#paper .pp-say')].some(el => el.textContent === value), text);
                    assert.equal(calls.filter(c => c.operation === 'session_prompt').at(-1).arguments.request_id, request);
                    await shot(`${provider}-${target}-delivered`);
                    results.push({ width, provider, target, text, request, sends: delivered - start, receipt: { ...[...ledger.values()].at(-1) } });
                    mode = 'hold';
                }
            }
            // A definite refusal alone restores the original draft for a new deliberate Send.
            mode = 'not-sent'; await ready(); await input.fill(text); await send.click();
            await page.waitForFunction(value => document.querySelector('#pp-message').value === value, text);
            assert.equal(await input.inputValue(), text);
            mode = 'receipt'; await send.click(); await page.locator('#paper .pp-delivery').filter({ hasText: 'Delivered' }).waitFor();
            for (const scenario of ['unsupported', 'ended', 'working', 'offline']) {
                agent = scenario === 'unsupported' ? 'unsupported' : 'codex'; alive = scenario !== 'ended'; state = scenario === 'working' ? 'working' : 'idle'; offline = scenario === 'offline';
                await page.evaluate(() => localStorage.clear()); await page.reload(); await input.waitFor();
                await page.waitForTimeout(1800);
                assert.equal(await send.isDisabled(), true, scenario + ' cannot offer active Send');
                await shot(scenario);
            }
            offline = false; agent = 'claude'; alive = true; state = 'idle';
            // No browser session plan cap is defined; capture zero, one and the
            // eight-row inline display limit, plus the overflow case above.
            for (const amount of [0, 1, 9]) {
                count = amount; await page.evaluate(() => localStorage.clear()); await page.goto(origin + '/app/#'); await page.reload();
                if (amount) await ready(); else await page.getByText('No sessions on Release workstation', { exact: true }).waitFor();
                await shot(amount === 9 ? 'inline-limit-8' : 'volume-' + amount);
            }
            assert.deepEqual(errors, []);
            assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
            writeFileSync(resolve(output, `calls-${width}.json`), JSON.stringify(calls, null, 2) + '\n');
            await context.close();
        }
    } finally { await browser.close(); }
    writeFileSync(resolve(output, 'receipt.json'), JSON.stringify({ fixture: 'production web app and encrypted synthetic wing; provider-native receipt separately verified by Go tests', results }, null, 2) + '\n');
    console.log(`PASS: ${results.length} provider/target/viewport cases, exact text once, explicit retry, empty/duplicate and unavailable states`);
}
if (!process.env.NODE_TEST_CONTEXT) await run();
