// Render the real sharing component in the app shell with local provider fixtures.
// No roost, provider or external request is allowed in this layout contract.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { createServer } from 'node:http';
import { build } from '../../web/node_modules/vite/dist/node/index.js';
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { chromium } from '../../web/node_modules/playwright/index.mjs';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const out = process.env.WT_SHARE_OUT || path.join(root, '.scratch/share-confirmation');
fs.mkdirSync(out, { recursive: true });
const accounts = {
    google: { account: 'alice.robertson.preview.access.confirmation@identity-fixtures.example.test', subject: 'sample-google-alice-42' },
    github: { account: 'alice-robertson-preview-access-confirmation-fixture', subject: 'sample-github-alice-17' },
};
const host = 'preview-host.identity-fixtures.example.test';
const wingId = 'fixture-host-001';
const sessionId = 'fixture-session-001';
const sessionName = 'Review private app changes';
const cwd = '/srv/projects/private-preview/access-confirmation/branches/recipient-resource-check/application';
const entry = path.join(root, '.scratch/share-confirmation-entry.mjs');
fs.mkdirSync(path.dirname(entry), { recursive: true });
fs.writeFileSync(entry, `
    import '${root}/web/src/paper.css';
    import { S } from '${root}/web/src/state.js';
    import { initReachShare, refreshReachShare } from '${root}/web/src/reach-share.js';
    S.currentUser = { id: 'fixture-owner', reach_share_preview: true };
    S.wingsData = [
        { wing_id: '${wingId}', wing_label: 'Preview host', hostname: '${host}', online: true },
        { wing_id: 'fixture-host-002', wing_label: 'Preview host', hostname: 'other.example.test', online: true },
    ];
    S.sessionsData = [
        { wing_id: '${wingId}', id: '${sessionId}', name: '${sessionName}', cwd: '${cwd}' },
        { wing_id: 'fixture-host-002', id: '${sessionId}', name: '${sessionName}', cwd: '/srv/other' },
    ];
    initReachShare();
    document.addEventListener('fixture-disable-sharing', () => {
        S.currentUser.reach_share_preview = false;
        document.activeElement?.blur(); refreshReachShare();
    });
`);
const errors = [], captures = [], network = [];
const assets = path.join(root, '.scratch/share-confirmation-assets');
await build({ configFile: false, root: path.join(root, 'web'), logLevel: 'error', plugins: [{
    name: 'synthetic-accounts',
    transform(code, id) {
        if (id === path.join(root, 'web/src/reach-share-state.js')) return code.replaceAll('alice@example.test', accounts.google.account).replaceAll('alice-example', accounts.github.account);
    },
}], build: { outDir: assets, emptyOutDir: true, rollupOptions: { input: entry, output: { entryFileNames: 'fixture.js', assetFileNames: '[name][extname]' } } } });
const styles = fs.readdirSync(assets).filter(file => file.endsWith('.css')).map(file => `<link rel="stylesheet" href="${file}">`).join('');
const html = fs.readFileSync(path.join(root, 'web/index.html'), 'utf8').replace('<script type="module" src="src/entry.js"></script>', styles + '<script type="module" src="fixture.js"></script>').replaceAll('%BASE_URL%', '/app/');
const server = createServer((req, res) => {
    const pathname = new URL(req.url, 'http://127.0.0.1').pathname;
    if (pathname === '/app/share-confirmation-fixture') {
        res.writeHead(200, { 'Content-Type': 'text/html' }); res.end(html); return;
    }
    const relative = decodeURIComponent(pathname).replace(/^\/app\//, '').replace(/^\//, '');
    for (const directory of [assets, path.join(root, 'web'), path.join(root, 'web/public')]) {
        const file = path.resolve(directory, relative);
        if (!file.startsWith(directory + path.sep) || !fs.existsSync(file) || !fs.statSync(file).isFile()) continue;
        const mime = { '.js': 'text/javascript', '.css': 'text/css', '.woff2': 'font/woff2', '.svg': 'image/svg+xml', '.png': 'image/png' }[path.extname(file)] || 'application/octet-stream';
        res.writeHead(200, { 'Content-Type': mime }); res.end(fs.readFileSync(file)); return;
    }
    res.writeHead(404); res.end();
});
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
const base = `http://127.0.0.1:${server.address().port}/app/`;
const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH });
let completed = false;
try {
    for (const width of [390, 430, 1440]) for (const colorScheme of ['light', 'dark']) {
        const viewport = { width, height: width === 390 ? 844 : width === 430 ? 932 : 900 };
        const context = await browser.newContext({ viewport, colorScheme });
        const page = await context.newPage();
        page.on('pageerror', error => errors.push(String(error)));
        page.on('console', message => { if (message.type() === 'error') console.error(message.text()); });
        await page.route('**/*', async route => {
            if (new URL(route.request().url()).origin !== new URL(base).origin) {
                network.push(route.request().url()); return route.abort();
            }
            return route.continue();
        });
        await page.goto(base + 'share-confirmation-fixture');
        await page.locator('#reach-open').waitFor({ timeout: 10000 });
        await page.click('#reach-open');
        await page.getByRole('tab', { name: 'Private sharing', exact: true }).click();
        assert.ok(await page.locator('#share-invite').isDisabled());
        assert.ok(await page.locator('#share-observe').isDisabled());
        async function capture(selector, action, kind, provider, label = kind) {
            const card = page.locator(selector);
            const text = await card.innerText();
            for (const value of [accounts[provider].account, accounts[provider].subject, host, wingId, kind === 'app' ? 'preview-1' : sessionName, kind === 'app' ? 'port 65535' : sessionId, kind === 'app' ? 'View app' : 'Observe session']) assert.ok(text.includes(value), value);
            if (kind === 'session') assert.ok(text.includes(cwd));
            assert.equal(await card.locator(action).count(), 1, 'action stays in the exact access card');
            await card.evaluate(el => el.scrollIntoView({ block: 'start' }));
            const geometry = await card.evaluate(el => {
                const card = el.getBoundingClientRect();
                const spans = [...el.querySelectorAll('.reach-kv span')].map(span => {
                    const range = document.createRange(); range.selectNodeContents(span);
                    return { text: span.textContent, style: { whiteSpace: getComputedStyle(span).whiteSpace, overflow: getComputedStyle(span).overflow, textOverflow: getComputedStyle(span).textOverflow }, rects: [...range.getClientRects()].map(r => ({ x: r.x, y: r.y, right: r.right, bottom: r.bottom })) };
                });
                return { card: { x: card.x, y: card.y, right: card.right, bottom: card.bottom }, spans, background: getComputedStyle(el).backgroundColor };
            });
            assert.ok(geometry.card.y >= 36 && geometry.card.bottom <= viewport.height, 'full review and action fit in the viewport');
            for (const span of geometry.spans) {
                assert.equal(span.style.whiteSpace, 'normal'); assert.equal(span.style.overflow, 'visible'); assert.equal(span.style.textOverflow, 'clip');
                for (const rect of span.rects) assert.ok(rect.x >= geometry.card.x && rect.right <= geometry.card.right + 1 && rect.y >= geometry.card.y && rect.bottom <= geometry.card.bottom, 'every text fragment is visible inside the card');
            }
            assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1));
            const file = `${width}-${colorScheme}-${label}-${action.includes('revoke') ? 'revoke' : 'grant'}.png`;
            await page.screenshot({ path: path.join(out, file) });
            captures.push({ file, viewport, colorScheme, kind, provider, text, geometry });
        }
        await page.fill('#share-port', '65535'); await page.click('#share-publish');
        await capture('#share-access-review .share-access-review', '#share-invite', 'app', 'google');
        await page.click('#share-invite');
        // Revoke must show the granted account even after the selector changes.
        await page.selectOption('#share-provider', 'github');
        await capture('#share-grants .share-access-review', '[data-revoke]', 'app', 'google');
        await page.click('[data-revoke]');
        assert.ok(await page.locator('[data-revoke]').isDisabled());
        await page.click('#share-alice');
        assert.equal(await page.locator('.reach-preview-app').count(), 0);
        await page.click('#share-owner');
        await page.selectOption('#share-session', JSON.stringify([wingId, sessionId]));
        await capture('#share-observer-review .share-access-review', '#share-observe', 'session', 'github');
        await page.click('#share-observe');
        await page.click('#share-alice');
        assert.match(await page.locator('.reach-preview-app').innerText(), /Observe session/);
        assert.ok(await page.getByRole('button', { name: 'Send terminal input', exact: true }).isDisabled());
        await page.click('#share-owner');
        await page.selectOption('#share-provider', 'google');
        await capture('#share-grants .share-access-review', '[data-revoke]', 'session', 'github');
        await page.click('[data-revoke]');
        await page.selectOption('#share-provider', 'github');
        await page.click('#share-alice');
        assert.equal(await page.locator('.reach-preview-app').count(), 0);
        await page.click('#share-owner');
        // The long email and long path must also fit together, including revoke.
        await page.selectOption('#share-provider', 'google');
        await capture('#share-observer-review .share-access-review', '#share-observe', 'session', 'google', 'session-email');
        await page.click('#share-observe');
        await page.selectOption('#share-provider', 'github');
        await capture('#share-grants .share-access-review:has([data-revoke]:enabled)', '[data-revoke]', 'session', 'google', 'session-email');
        await page.locator('[data-revoke]:enabled').click();
        await page.selectOption('#share-provider', 'google');
        await page.click('#share-alice');
        assert.equal(await page.locator('.reach-preview-app').count(), 0);
        await page.click('#share-owner');
        // The server-controlled dormant gate still removes every sharing control.
        await page.evaluate(() => document.dispatchEvent(new Event('fixture-disable-sharing')));
        await page.waitForFunction(() => !document.querySelector('#share-invite'));
        assert.equal(await page.locator('#share-invite, #share-observe, [data-revoke]').count(), 0);
        assert.equal(await page.getByRole('tab', { name: 'Private sharing', exact: true }).count(), 0);
        console.log(`PASS ${width} ${colorScheme}: full app and session grant/revoke, identity binding, observer and dormant gates`);
        await context.close();
    }
    assert.deepEqual(errors, []); assert.deepEqual(network, []);
    assert.notEqual(captures.find(c => c.colorScheme === 'light').geometry.background, captures.find(c => c.colorScheme === 'dark').geometry.background);
    completed = true;
} finally {
    await browser.close(); await new Promise(resolve => server.close(resolve));
    fs.writeFileSync(path.join(out, 'receipt.json'), JSON.stringify({ completed, captures, errors, blockedExternalRequests: network, source: spawnSync('git', ['rev-parse', 'HEAD'], { cwd: root, encoding: 'utf8' }).stdout.trim(), diffSHA256: createHash('sha256').update(spawnSync('git', ['diff', 'HEAD'], { cwd: root }).stdout).digest('hex') }, null, 2));
}
