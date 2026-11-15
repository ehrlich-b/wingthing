// Offline contract using the real screen and picker with synthetic discovery.
import './first-run-inventory.mjs';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { createServer } from 'node:http';
import { build } from '../../web/node_modules/vite/dist/node/index.js';
import { chromium } from '../../web/node_modules/playwright/index.mjs';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const out = process.env.WT_FIRST_RUN_OUT || path.join(root, '.scratch/first-run');
fs.mkdirSync(out, { recursive: true });
const entry = path.join(root, '.scratch/first-run-entry.mjs');
const transport = path.join(root, '.scratch/first-run-transport.mjs');
fs.mkdirSync(path.dirname(entry), { recursive: true });
fs.writeFileSync(transport, `
export * from '${root}/web/src/tunnel.js';
export async function sendTunnelRequest(wingId, request, options) {
    window.requests.push({wingId, ...request, options});
    if (request.type === 'wing.info') {
        if (window.authError) throw Error(window.authError);
        if (window.discoveryError) throw Error('fixture unavailable');
        return {agents: window.installed ? ['codex'] : []};
    }
    if (request.type === 'dir.list') return {entries: []};
    throw Error('Unexpected request: ' + request.type);
}`);
fs.writeFileSync(entry, `
import { S, initDOM } from '${root}/web/src/state.js';
import { initPaper, render } from '${root}/web/src/paper.js';
import { loadHome } from '${root}/web/src/data.js';
import { renderHostedPlanGuidance } from '${root}/web/src/onboarding.js';
import { showPalette, launchFromPalette, hidePalette } from '${root}/web/src/palette.js';
initDOM(); window.requests = []; window.installed = false;
window.fixture = (relay, count) => {
    document.querySelector('.crypto-error-dismiss')?.click();
    hidePalette(); S.activeView = 'home';
    S.currentUser = {id: 'owner', email: 'owner@example.test', relay_allowed: relay, self_service_plans: false, has_passkeys: true};
    S.wingsData = Array.from({length: count}, (_, i) => ({wing_id: 'machine-' + i, hostname: 'workspace-' + i + '.example.test', wing_label: 'Research workstation for extended migration projects ' + (i+1), online: true, agents: [], public_key: 'synthetic'}));
    S.sessionsData = relay === false && count ? [{id: 'cached', wing_id: 'machine-0', name: 'Cached private session', agent: 'claude'}] : [];
    if (document.getElementById('paper')) render(true);
};
window.hostedGuidance = () => renderHostedPlanGuidance(S.currentUser, document);
window.redraw = () => render(true);
window.picker = showPalette; window.launch = launchFromPalette; window.state = S;
window.readInventory = loadHome;
window.fixture(window.fixtureOptions.relay, window.fixtureOptions.count); initPaper();
`);
const assets = path.join(root, '.scratch/first-run-assets');
await build({configFile: false, root: path.join(root, 'web'), logLevel: 'error', plugins: [{
    name: 'offline-discovery', enforce: 'pre', resolveId(source, importer) {
        if (source === './tunnel.js' && importer !== transport) return transport;
    },
}], build: {outDir: assets, rollupOptions: {input: entry, output: {entryFileNames: 'fixture.js', assetFileNames: '[name][extname]'}}}});
const styles = fs.readdirSync(assets).filter(f => f.endsWith('.css')).map(f => `<link rel="stylesheet" href="${f}">`).join('');
const html = fs.readFileSync(path.join(root, 'web/index.html'), 'utf8').replace('<script type="module" src="src/entry.js"></script>', styles + '<script type="module" src="fixture.js"></script>').replaceAll('%BASE_URL%', '/app/');
const server = createServer((req, res) => {
    const url = new URL(req.url, 'http://127.0.0.1');
    if (url.pathname === '/app/') { res.setHeader('Content-Type', 'text/html'); res.end(html); return; }
    const relative = decodeURIComponent(url.pathname).replace(/^\/app\//, '');
    for (const dir of [assets, path.join(root, 'web/public'), path.join(root, 'web')]) {
        const file = path.resolve(dir, relative);
        if (!file.startsWith(dir + path.sep) || !fs.existsSync(file) || !fs.statSync(file).isFile()) continue;
        res.setHeader('Content-Type', {'.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml', '.woff2': 'font/woff2'}[path.extname(file)] || 'application/octet-stream');
        res.end(fs.readFileSync(file)); return;
    }
    res.writeHead(404); res.end();
});
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
const base = `http://127.0.0.1:${server.address().port}/app/`;
const browser = await chromium.launch({headless: true, executablePath: process.env.CHROMIUM_PATH});
const errors = [], checks = [], captures = [];
try {
    for (const width of [390, 1440]) {
        let context, page, heldInventory;
        // A case owns its entire browser state and outstanding requests. Closing
        // that context before a reset prevents even an already captured response
        // from merging the previous roster into the next case.
        const resetFixture = async (relay, count, configure = () => {}, value) => {
            await context?.close();
            context = await browser.newContext({viewport: {width, height: 900}, colorScheme: 'light'});
            await context.addInitScript(options => { window.fixtureOptions = options; }, {relay, count});
            page = await context.newPage();
            const fixturePage = page;
            fixturePage.on('pageerror', e => { errors.push(String(e)); console.error(String(e)); });
            await fixturePage.route('**/*', async route => {
                const url = new URL(route.request().url());
                if (url.origin !== new URL(base).origin) return route.abort();
                if (url.pathname !== '/api/app/wings') return route.continue();
                const held = heldInventory?.page === fixturePage ? heldInventory : null;
                try {
                    if (held?.stage === 'before snapshot') {
                        held.captured();
                        await held.release;
                    }
                    const roster = await fixturePage.evaluate(() => window.state.wingsData.map(({wing_id, public_key}) => ({wing_id, public_key})));
                    if (held?.stage === 'after snapshot') {
                        held.roster = roster;
                        held.captured();
                        await held.release;
                    }
                    await route.fulfill({contentType: 'application/json', body: JSON.stringify(roster)});
                } catch (error) {
                    // Closing a case cancels its pending route; other failures
                    // still fail the fixture.
                    if (!fixturePage.isClosed()) throw error;
                } finally { held?.finished(); }
            });
            await page.goto(base);
            await page.waitForSelector('#paper h2');
            await page.evaluate(configure, value);
        };
        for (const stage of ['before snapshot', 'after snapshot']) {
            await resetFixture(false, 9);
            let captured, release, finished;
            const capturedRequest = new Promise(resolve => { captured = resolve; });
            const finishedRequest = new Promise(resolve => { finished = resolve; });
            heldInventory = {page, stage, captured, finished, release: new Promise(resolve => { release = resolve; })};
            const previousPage = page;
            // Exercise the real merge caller, not a standalone fetch control.
            await page.evaluate(() => { window.pendingInventory = window.readInventory(); });
            await capturedRequest;
            if (stage === 'after snapshot') assert.equal(heldInventory.roster.length, 9);
            await resetFixture(false, 0);
            assert.equal(previousPage.isClosed(), true);
            release();
            await finishedRequest;
            assert.equal(await page.evaluate(() => window.state.wingsData.length), 0, stage + ': stale poll must not restore machines');
            assert.equal(await page.locator('#pp-launch-machine').count(), 0);
            assert.match(await page.locator('#paper').innerText(), /codex mcp add/);
            heldInventory = null;
        }
        for (const count of [1, 5, 9, 0]) {
            await resetFixture(false, count);
            const roster = await page.evaluate(() => fetch('/api/app/wings').then(response => response.json()));
            assert.deepEqual(roster.map(wing => wing.wing_id), Array.from({length: count}, (_, i) => 'machine-' + i));
            assert.ok(roster.every(wing => wing.public_key === 'synthetic'));
        }
        checks.push(`${width}: inventory follows 9→0 reset before/after response snapshot through loadHome and isolated 1/5/9/0 cases`);
        const shot = async name => {
            assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), name + ' overflows');
            assert.ok(await page.evaluate(() => [...document.querySelectorAll('.pp-side-squares .pp-sq')].every(row => row.getBoundingClientRect().right <= row.closest('.pp-side').getBoundingClientRect().right + 1)), name + ' machine label overlaps content');
            const file = `${name}-${width}.png`;
            await page.screenshot({path: path.join(out, file), fullPage: true}); captures.push(file);
        };
        for (const count of [0, 1, 5, 9]) {
            await resetFixture(false, count);
            for (const hash of ['#', '#sessions', '#machines', '#new', '#session/machine-0/cached', '#approve/machine-0/cached']) {
                await page.evaluate(hash => {location.hash = hash;}, hash);
                await page.waitForTimeout(50);
                const content = await page.locator('#paper').innerText();
                assert.match(content, /codex mcp add/); assert.match(content, /claude mcp add/);
                assert.match(content, /no self-service purchase/); assert.match(content, /account and permission on the selected machine/);
                assert.equal(await page.locator('#paper [data-new-session], #paper [data-takeover], #paper textarea, #paper [data-choose-project]').count(), 0);
            }
            assert.deepEqual(await page.evaluate(() => window.requests), []);
            await shot(`free-${count}-machines`);
            if (count === 9) {
                await page.evaluate(() => window.scrollTo(0, document.body.scrollHeight));
                const file = `free-browser-options-${width}.png`;
                await page.screenshot({path: path.join(out, file)}); captures.push(file);
                await page.evaluate(() => window.scrollTo(0, 0));
            }
        }
        checks.push(`${width}: free guidance on six routes with 0, 1, 5 and 9 cached machines; no runtime reads or controls`);
        for (const mode of ['pro', 'self-hosted']) {
            for (const count of [0, 1, 5]) {
                await resetFixture(true, count, ({mode, count}) => {location.hash = '#'; window.fixture(true, count); window.state.currentUser.roost_mode = mode === 'self-hosted';}, {mode, count});
                await page.waitForTimeout(50);
                await shot(`${mode}-${count}-machines`);
            }
            await resetFixture(true, 9, mode => {location.hash = '#'; window.fixture(true, 9); window.state.currentUser.roost_mode = mode === 'self-hosted'; window.state.currentUser.relay_reason = mode === 'self-hosted' ? 'self-hosted' : 'pro';}, mode);
            await page.getByRole('link', {name: 'New session', exact: true}).first().click();
            await page.waitForSelector('#pp-launch-machine');
            await page.selectOption('#pp-launch-machine', 'machine-8');
            const setup = await page.locator('#paper .pp-setup').first().innerText();
            assert.match(setup, /No agents detected[\s\S]*Research workstation.*9/);
            assert.match(setup, /wt setup/); assert.match(setup, /sign in to its provider/);
            assert.equal(await page.locator('#paper [data-choose-project]').count(), 0);
            await shot(`${mode}-no-agents`);
            await page.evaluate(() => {window.picker('machine-8'); window.launch('/srv/projects/never-launch');});
            assert.equal((await page.evaluate(() => window.requests)).filter(r => r.type.startsWith('pty.')).length, 0);
            assert.equal(await page.locator('#palette-results .palette-item').count(), 0);
            assert.equal(await page.locator('#command-palette').isVisible(), false);
            await page.evaluate(() => {window.discoveryError = true;});
            await page.getByRole('button', {name: 'Refresh agents', exact: true}).click();
            await page.getByRole('heading', {name: 'Machine unavailable'}).waitFor();
            assert.equal(await page.locator('#pp-launch-machine').inputValue(), 'machine-8');
            assert.equal(await page.locator('#paper [data-choose-project]').count(), 0);
            await shot(`${mode}-refresh-unavailable`);
            await resetFixture(true, 9, () => {window.discoveryError = false; window.fixture(true, 9); location.hash = '#new/machine-8'; window.installed = true;});
            await page.getByRole('button', {name: 'Refresh agents', exact: true}).click();
            await page.waitForSelector('[data-choose-project]', {timeout: 5000}).catch(async error => {
                console.error(await page.evaluate(() => ({requests: window.requests, wings: window.state.wingsData, content: document.querySelector('#paper').innerText})));
                throw error;
            });
            assert.match(await page.locator('#paper').innerText(), /Codex/);
            assert.equal(await page.locator('#pp-launch-machine').inputValue(), 'machine-8');
            await shot(`${mode}-agent-ready`);
            await page.getByRole('button', {name: 'Choose project', exact: true}).click();
            assert.ok(await page.locator('#palette-search').isEnabled());
            assert.match(await page.locator('#palette-status').innerText(), /codex/i);
            await resetFixture(true, 9, () => {window.installed = false; window.fixture(true, 9); location.hash = '#';});
            await page.waitForTimeout(50);
            assert.equal(await page.locator('.pp-side-squares .pp-sq').count(), 3);
            assert.equal(await page.locator('.pp-side-squares .pp-more').innerText(), '+6 more');
            await shot(`${mode}-9-machines`);
            checks.push(`${width}: ${mode} inline setup, no invented launch, refreshed Codex, existing path picker, three-machine strip with +6 more`);
        }
        // Both onboarding surfaces follow the deployment's actual plan options.
        for (const plans of [false, true]) {
            await resetFixture(false, 1, plans => {window.fixture(false, 1); window.state.currentUser.self_service_plans = plans; window.hostedGuidance(); window.redraw();}, plans);
            const expected = plans ? /See Account for the available plan options/ : /no self-service purchase/;
            assert.match(await page.locator('#paper').innerText(), expected);
            assert.match(await page.locator('#direct-agent-setup').textContent(), expected);
            if (plans) assert.doesNotMatch(await page.locator('#direct-agent-setup').textContent(), /no self-service purchase/);
        }
        for (const error of ['passkey_required', 'passkey_failed', 'no_passkeys_configured']) {
            await resetFixture(true, 9, error => {
                window.fixture(true, 9); window.requests = []; window.installed = true;
                const wing = window.state.wingsData[8]; delete wing.agents; wing.locked = true; wing.tunnel_error = error;
                window.picker(wing.wing_id);
                window.redraw();
            }, error);
            await page.getByRole('heading', {name: 'Machine locked'}).waitFor();
            assert.equal(await page.locator('#pp-launch-machine').inputValue(), 'machine-8');
            assert.equal(await page.locator('#command-palette').isVisible(), false);
            assert.equal(await page.locator('#paper [data-retry], #paper [data-choose-project], #paper [data-refresh-agents]').count(), 0);
            assert.deepEqual(await page.evaluate(() => window.requests), []);
            await shot(error === 'no_passkeys_configured' ? 'passkey-setup' : 'passkey-locked-' + error);
            if (error === 'no_passkeys_configured') {
                await page.getByRole('button', {name: 'Add passkey', exact: true}).click();
                assert.equal(await page.evaluate(() => location.hash), '#account');
                assert.equal(await page.locator('#account-section').isVisible(), true);
                continue;
            }
            // A cancelled or failed ceremony retains an actionable unlock step.
            await page.evaluate(() => {window.authError = 'passkey_required';});
            await page.getByRole('button', {name: 'Unlock machine', exact: true}).click();
            await page.waitForFunction(() => document.querySelector('[data-unlock-machine]')?.disabled === false);
            assert.equal(await page.locator('#paper [data-retry]').count(), 0);
            await page.evaluate(() => {window.authError = '';});
            await page.getByRole('button', {name: 'Unlock machine', exact: true}).click();
            await page.getByRole('button', {name: 'Choose project', exact: true}).waitFor();
            const probes = await page.evaluate(() => window.requests.filter(request => request.type === 'wing.info'));
            assert.equal(probes.length, 2);
            assert.ok(probes.every(request => request.options.skipPasskey === false));
            assert.match(await page.locator('#paper').innerText(), /Codex/);
            assert.equal(await page.evaluate(() => window.state.wingsData[8].tunnel_error), undefined);
            assert.equal((await page.evaluate(() => window.requests)).filter(request => request.type.startsWith('pty.')).length, 0);
            await shot('passkey-unlocked-' + error);
            await page.getByRole('button', {name: 'Choose project', exact: true}).click();
            assert.equal(await page.locator('#command-palette').isVisible(), true);
            assert.match(await page.locator('#palette-status').innerText(), /codex/i);
        }
        checks.push(`${width}: locked alternate identity authenticates before discovery, cancelled unlock retries, absent passkeys open Account; both hosted guidance surfaces follow plan availability`);
        await context.close();
    }
    assert.deepEqual(errors, []);
    fs.writeFileSync(path.join(out, 'receipt.json'), JSON.stringify({checks, captures, errors, volume: '0, 1, 5 row rail display limit, 9 long machine names; three-machine sidebar strip; no numeric account machine limit exists'}, null, 2) + '\n');
    console.log(JSON.stringify({checks, captures: captures.length, errors}));
} finally { await browser.close(); await new Promise(resolve => server.close(resolve)); }
