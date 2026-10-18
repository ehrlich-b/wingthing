// Offline contract using the real screen and picker with synthetic discovery.
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
export async function sendTunnelRequest(wingId, request) {
    window.requests.push({wingId, ...request});
    if (request.type === 'wing.info') {
        if (window.discoveryError) throw Error('fixture unavailable');
        return {agents: window.installed ? ['codex'] : []};
    }
    if (request.type === 'dir.list') return {entries: []};
    throw Error('Unexpected request: ' + request.type);
}`);
fs.writeFileSync(entry, `
import { S, initDOM } from '${root}/web/src/state.js';
import { initPaper, render } from '${root}/web/src/paper.js';
import { showPalette, launchFromPalette, hidePalette } from '${root}/web/src/palette.js';
initDOM(); window.requests = []; window.installed = false;
window.fixture = (relay, count) => {
    hidePalette(); S.activeView = 'home';
    S.currentUser = {id: 'owner', email: 'owner@example.test', relay_allowed: relay, self_service_plans: false};
    S.wingsData = Array.from({length: count}, (_, i) => ({wing_id: 'machine-' + i, hostname: 'workspace-' + i + '.example.test', wing_label: 'Research workstation for extended migration projects ' + (i+1), online: true, agents: [], public_key: 'synthetic'}));
    S.sessionsData = relay === false && count ? [{id: 'cached', wing_id: 'machine-0', name: 'Cached private session', agent: 'claude'}] : [];
    if (document.getElementById('paper')) render(true);
};
window.picker = showPalette; window.launch = launchFromPalette; window.state = S;
window.fixture(false, 0); initPaper();
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
        const context = await browser.newContext({viewport: {width, height: 900}, colorScheme: 'light'});
        const page = await context.newPage();
        page.on('pageerror', e => { errors.push(String(e)); console.error(String(e)); });
        await page.route('**/*', route => new URL(route.request().url()).origin === new URL(base).origin ? route.continue() : route.abort());
        await page.goto(base);
        await page.waitForSelector('#paper h2');
        const shot = async name => {
            assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), name + ' overflows');
            assert.ok(await page.evaluate(() => [...document.querySelectorAll('.pp-side-squares .pp-sq')].every(row => row.getBoundingClientRect().right <= row.closest('.pp-side').getBoundingClientRect().right + 1)), name + ' machine label overlaps content');
            const file = `${name}-${width}.png`;
            await page.screenshot({path: path.join(out, file), fullPage: true}); captures.push(file);
        };
        for (const count of [0, 1, 5, 9]) {
            await page.evaluate(count => window.fixture(false, count), count);
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
                await page.evaluate(({mode, count}) => {location.hash = '#'; window.fixture(true, count); window.state.currentUser.roost_mode = mode === 'self-hosted';}, {mode, count});
                await page.waitForTimeout(50);
                await shot(`${mode}-${count}-machines`);
            }
            await page.evaluate(mode => {location.hash = '#'; window.fixture(true, 9); window.state.currentUser.roost_mode = mode === 'self-hosted'; window.state.currentUser.relay_reason = mode === 'self-hosted' ? 'self-hosted' : 'pro';}, mode);
            await page.getByRole('button', {name: 'New session', exact: true}).click();
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
            await page.evaluate(() => {window.discoveryError = false; window.fixture(true, 9); location.hash = '#new'; window.installed = true;});
            await page.getByRole('button', {name: 'Refresh agents', exact: true}).click();
            await page.waitForSelector('[data-choose-project]', {timeout: 5000}).catch(async error => {
                console.error(await page.evaluate(() => ({requests: window.requests, wings: window.state.wingsData, content: document.querySelector('#paper').innerText})));
                throw error;
            });
            assert.match(await page.locator('#paper').innerText(), /Codex/);
            await shot(`${mode}-agent-ready`);
            await page.getByRole('button', {name: 'Choose project', exact: true}).click();
            assert.ok(await page.locator('#palette-search').isEnabled());
            assert.match(await page.locator('#palette-status').innerText(), /codex/i);
            await page.evaluate(() => {window.installed = false; window.fixture(true, 9); location.hash = '#';});
            await page.waitForTimeout(50);
            assert.match(await page.locator('#paper').innerText(), /\+4 more/);
            await shot(`${mode}-9-machines`);
            checks.push(`${width}: ${mode} inline setup, no invented launch, refreshed Codex, existing path picker, +4 more`);
        }
        await context.close();
    }
    assert.deepEqual(errors, []);
    fs.writeFileSync(path.join(out, 'receipt.json'), JSON.stringify({checks, captures, errors, volume: '0, 1, 5 row display limit, 9 long machine names; no numeric account machine limit exists'}, null, 2) + '\n');
    console.log(JSON.stringify({checks, captures: captures.length, errors}));
} finally { await browser.close(); await new Promise(resolve => server.close(resolve)); }
