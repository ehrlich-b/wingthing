// Local browser contract. Real built roost + encrypted fixture wings; no
// provider, OAuth, email, public publication or SSH. Fixtures are not live proof.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { spawn, spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { x25519 } from '../../web/node_modules/@noble/curves/ed25519.js';
import { hkdf } from '../../web/node_modules/@noble/hashes/hkdf.js';
import { sha256 } from '../../web/node_modules/@noble/hashes/sha2.js';
import { gcm } from '../../web/node_modules/@noble/ciphers/aes.js';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const out = process.env.WT_REACH_OUT || path.join(root, '.scratch/reach-evidence');
const shots = process.env.WT_REACH_SHOTS || path.join(out, 'shots');
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright');
fs.mkdirSync(shots, { recursive: true });
const state = fs.mkdtempSync(path.join(root, '.scratch/reach-'));
const port = Number(process.env.WT_REACH_PORT || 18181);
const base = 'http://127.0.0.1:' + port;
const processEnv = { PATH: process.env.PATH, HOME: path.join(state, 'home'), WINGTHING_DIR: state };
fs.mkdirSync(processEnv.HOME);
const log = fs.openSync(path.join(out, 'roost.log'), 'w');
const roost = spawn('nice', ['-n', '10', path.join(root, 'wt'), 'roost', 'start', '--foreground', '--dev', '--addr', '127.0.0.1:' + port], { cwd: root, env: processEnv, stdio: ['ignore', log, log] });
const steps = [];
const captured = [];
const pages = [];
const sockets = [];
const errors = [];
let browser;
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
function record(name) { steps.push(name); console.log('PASS ' + name); }
async function ready() {
    for (let i = 0; i < 100; i++) {
        if (roost.exitCode !== null) throw new Error('Fixture roost exited');
        try { if ((await fetch(base + '/health')).ok) return; } catch {}
        await sleep(100);
    }
    throw new Error('Fixture roost did not become ready');
}
function encrypt(key, value) {
    const iv = crypto.getRandomValues(new Uint8Array(12));
    const data = gcm(key, iv).encrypt(new TextEncoder().encode(JSON.stringify(value)));
    return Buffer.concat([iv, data]).toString('base64');
}
const fixtureRows = new Map();
const requests = [];
let stopMode = 'legacy';
let stopVerificationReads = 0;
async function fixtureWing(id, name, platform, rows) {
    fixtureRows.set(id, rows);
    // Only device_tokens are touched in this disposable DB. Key files and
    // ambient state are never read. Fixture token is not printed or captured.
    const token = 'local-fixture-' + id;
    const seed = spawnSync('python3', ['-c', 'import sqlite3,sys\nc=sqlite3.connect(sys.argv[1]);c.execute("INSERT INTO device_tokens(token,user_id,device_id,expires_at) VALUES (?, ?, ?, datetime(\'now\',\'+1 hour\'))",(sys.argv[2],"local",sys.argv[3]));c.commit()', path.join(state, 'roost.db'), token, id], { encoding: 'utf8' });
    assert.equal(seed.status, 0, seed.stderr);
    const priv = x25519.utils.randomSecretKey();
    const pub = Buffer.from(x25519.getPublicKey(priv)).toString('base64');
    const socket = new WebSocket(base.replace('http:', 'ws:') + '/ws/wing?token=' + encodeURIComponent(token));
    sockets.push(socket);
    await new Promise((resolve, reject) => {
        socket.onopen = () => socket.send(JSON.stringify({ type: 'wing.register', wing_id: id, public_key: pub, agents: ['claude', 'codex'], locked: false, purpose_binding: true }));
        socket.onerror = reject;
        socket.onmessage = e => { const message = JSON.parse(e.data); if (message.type === 'registered') resolve(); };
    });
    socket.onmessage = e => {
        const outer = JSON.parse(e.data);
        if (outer.type !== 'tunnel.req') return;
        const key = hkdf(sha256, x25519.getSharedSecret(priv, Buffer.from(outer.sender_pub, 'base64')), new Uint8Array(32), new TextEncoder().encode('wt-tunnel'), 32);
        const encrypted = Buffer.from(outer.payload, 'base64');
        const inner = JSON.parse(new TextDecoder().decode(gcm(key, encrypted.subarray(0, 12)).decrypt(encrypted.subarray(12))));
        requests.push({ wingId: id, type: inner.type, sessionId: inner.session_id });
        let response;
        if (inner.type === 'wing.info') response = { hostname: name, wing_label: name, platform, agents: ['claude', 'codex'], projects: [{ name: 'pairing', path: platform === 'darwin' ? '/Users/example/pairing' : '/home/example/pairing' }], capabilities: ['session.rename.v1'] };
        else if (inner.type === 'sessions.list') {
            if (id === 'fixture-mac' && stopMode === 'verifying' && ++stopVerificationReads >= 2) {
                fixtureRows.set(id, fixtureRows.get(id).filter(row => row.session_id !== 'same-id'));
            }
            response = { sessions: fixtureRows.get(id) };
        }
        else if (inner.type === 'sessions.history') response = { sessions: [] };
        else if (inner.type === 'conversations.list') response = { conversations: [] };
        else if (inner.type === 'pty.kill') {
            assert.ok(fixtureRows.get(id).some(row => row.session_id === inner.session_id));
            if (stopMode === 'legacy') response = { ok: 'true' };
            else {
                stopMode = 'verifying'; stopVerificationReads = 0;
                response = { ok: 'true', process_exited: true };
            }
        } else response = { error: 'unsupported fixture request' };
        socket.send(JSON.stringify({ type: 'tunnel.res', request_id: outer.request_id, payload: encrypt(key, response) }));
    };
    return socket;
}
const row = (id, name, status, cwd) => ({ session_id: id, name, agent: 'claude', cwd, user_id: 'local', email: 'owner@example.test', output: { epoch: 'fixture-execution', cursor: 0 }, lifecycle: { status, state: status === 'blocked' ? 'needs_input' : status, state_source: 'claude_hook', state_cursor: 1 } });
async function screenshot(page, flow, card, slug, suffix = '') {
    // Fixed viewport captures must show the behavior being reviewed, including
    // dashboard rows below the mobile fold. Companion frames cover unread and
    // blocked separately without changing the running UI or viewport contract.
    if (flow === 3 || flow === 5) {
        let target = page.locator('#sessions-list');
        if (slug === 'unread-output') target = page.locator('.egg-box[data-sid="review"]');
        else if (card === 7) target = page.locator('.egg-box[data-sid="same-id"][data-wing-id="' + (slug === 'stop-verified' ? 'fixture-remote' : 'fixture-mac') + '"]');
        else if (card === 6) target = page.locator('.egg-box[data-wing-id="fixture-remote"]').first();
        await target.scrollIntoViewIfNeeded();
    }
    await page.waitForTimeout(100);
    const file = `${String(flow).padStart(2, '0')}-${card}-${slug}${suffix}.png`;
    await page.screenshot({ path: path.join(shots, file) });
    captured.push({ flow, card, file, viewport: page.viewportSize() });
}
async function tab(page, text) { await page.getByRole('tab', { name: text, exact: true }).click(); }
async function verifyNoOverflow(page) {
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), 'horizontal overflow');
}
try {
    await ready();
    const me = await (await fetch(base + '/api/app/me')).json();
    assert.equal(me.reach_share_preview, true);
    record('existing --dev and local mode expose dormant UI');
    const macID = 'fixture-mac', remoteID = 'fixture-remote';
    await fixtureWing(macID, 'EhrlichMac', 'darwin', [row('same-id', 'pairing', 'blocked', '/Users/example/pairing'), row('coordinator', 'coordinator', 'working', '/Users/example/pairing')]);
    const remote = await fixtureWing(remoteID, 'forge', 'linux', [row('same-id', 'pairing', 'working', '/home/example/pairing'), row('review', 'review', 'idle', '/home/example/pairing')]);
    browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH });
    for (const viewport of [{ width: 1440, height: 900 }, { width: 390, height: 844 }]) {
        const context = await browser.newContext({ viewport });
        const page = await context.newPage(); pages.push(page);
        page.on('pageerror', error => errors.push(String(error)));
        await page.goto(base + '/app/');
        await page.waitForSelector('.egg-box[data-wing-id="fixture-mac"]');
        const suffix = viewport.width === 390 ? '-mobile' : '';
        assert.equal(await page.locator('.egg-box[data-sid="same-id"]').count(), 2);
        assert.match(await page.locator('#sessions-list').innerText(), /blocked/);
        await verifyNoOverflow(page);
        record(`${viewport.width}: qualified overlapping IDs and lifecycle groups`);
        await screenshot(page, 3, 1, 'dashboard-groups', suffix);
        await page.fill('#inventory-query', 'forge');
        assert.equal(await page.locator('.egg-box[data-wing-id="fixture-mac"]').count(), 0);
        await page.click('#inventory-clear');
        await page.selectOption('#inventory-status', 'attention');
        assert.equal(await page.locator('.egg-box').count(), 1);
        await page.click('#inventory-clear');
        const reviewRow = fixtureRows.get(remoteID).find(row => row.session_id === 'review');
        reviewRow.output.cursor += 64;
        await page.reload();
        await page.locator('.egg-box[data-sid="review"][data-output-cursor="' + reviewRow.output.cursor + '"]').waitFor();
        assert.match(await page.locator('.egg-box[data-sid="review"]').innerText(), /unread output/);
        assert.match(await page.locator('.egg-box[data-wing-id="fixture-mac"][data-sid="same-id"]').innerText(), /blocked/);
        assert.doesNotMatch(await page.locator('.egg-box[data-sid="review"]').innerText(), /unseen completion/);
        await screenshot(page, 3, 3, 'dashboard-attention', suffix);
        await screenshot(page, 3, 3, 'unread-output', suffix);
        await page.selectOption('#inventory-status', 'unread');
        assert.equal(await page.locator('.egg-box').count(), 1);
        assert.equal(await page.locator('.egg-box[data-sid="review"]').count(), 1);
        await page.getByRole('button', { name: /^Mark displayed output/ }).click();
        assert.equal(await page.locator('.egg-box').count(), 0);
        await page.click('#inventory-clear');
        await page.reload();
        await page.waitForSelector('.egg-box[data-sid="review"]');
        assert.doesNotMatch(await page.locator('.egg-box[data-sid="review"]').innerText(), /unread output/);
        reviewRow.output.cursor += 64;
        await page.reload();
        await page.locator('.egg-box[data-sid="review"][data-output-cursor="' + reviewRow.output.cursor + '"]').waitFor();
        assert.match(await page.locator('.egg-box[data-sid="review"]').innerText(), /unread output/);
        record(`${viewport.width}: output cursor, explicit read, reload persistence and new unread without completion`);
        await screenshot(page, 5, 5, 'dashboard-sessions', suffix);
        await page.click('#reach-open');
        await page.selectOption('#reach-wing', macID);
        await page.click('#reach-measure');
        await page.waitForFunction(() => document.querySelector('#reach-measurement').textContent.includes(' ms'));
        assert.match(await page.locator('#reach-measurement').innerText(), /encrypted wing.info/);
        await screenshot(page, 8, 4, 'measured-latency', suffix);
        record(`${viewport.width}: actual encrypted request round-trip measurement`);
        await tab(page, 'Privacy');
        assert.match(await page.locator('.reach-panel').innerText(), /TOFU/);
        await screenshot(page, 8, 3, 'connection-privacy', suffix);
        await tab(page, 'Reach preview');
        await page.click('#reach-register'); await page.click('#reach-register');
        assert.match(await page.locator('.reach-panel').innerText(), /registration retained/);
        await screenshot(page, 8, 1, 'register-once-dormant', suffix);
        await page.selectOption('#reach-session', 'same-id');
        await page.fill('#reach-input', 'Keep the review focused.');
        await page.selectOption('#reach-network', 'cellular');
        assert.equal(await page.inputValue('#reach-session'), 'same-id');
        assert.equal(await page.inputValue('#reach-input'), 'Keep the review focused.');
        assert.match(await page.locator('.reach-panel').innerText(), /Relay · encrypted/);
        await screenshot(page, 8, 2, 'lan-to-cellular-dormant', suffix);
        for (const [failure, expected] of [['asleep', /Wing unavailable/], ['relay', /Relay interrupted/], ['expired', /entitlement expired/], ['pin', /fingerprint changed/], ['revoked', /Device revoked/]]) {
            await page.selectOption('#reach-failure', failure);
            assert.match(await page.locator('.reach-panel').innerText(), expected);
            assert.ok(await page.locator('#reach-send').isDisabled());
            await screenshot(page, 8, 5, 'reach-failure-' + failure, suffix);
        }
        await page.selectOption('#reach-failure', 'healthy');
        await page.click('#reach-send');
        await page.click('#reach-reconnect'); await page.click('#reach-reconnect');
        assert.match(await page.locator('#reach-delivery').innerText(), /delivery unknown/);
        assert.match(await page.locator('#reach-delivery').innerText(), /submissions: 1/);
        assert.ok(await page.locator('#reach-input').isDisabled());
        await page.locator('#reach-delivery').scrollIntoViewIfNeeded();
        await screenshot(page, 8, 6, 'unknown-delivery-dormant', suffix);
        await page.click('#reach-receipt');
        assert.match(await page.locator('#reach-delivery').innerText(), /acknowledged/);
        record(`${viewport.width}: route/failure preservation and no input replay`);
        await tab(page, 'Private sharing');
        await page.click('#share-publish');
        await screenshot(page, 9, 1, 'private-publish-dormant', suffix);
        await page.click('#share-invite');
        assert.match(await page.locator('#share-grants').innerText(), /Google|google/);
        await screenshot(page, 9, 2, 'identity-invite-dormant', suffix);
        await page.click('#share-alice');
        assert.equal(await page.locator('.reach-preview-app').count(), 1);
        assert.equal(await page.locator('#reach-wing').count(), 0);
        assert.equal(await page.locator('#reach-os-user').count(), 0);
        assert.equal(await page.locator('#sidebar').isVisible(), false);
        assert.equal(await page.locator('#sessions-list').isVisible(), false);
        assert.equal(await page.getByRole('tab').count(), 0);
        await screenshot(page, 9, 3, 'alice-preview-dormant', suffix);
        await page.click('#share-owner');
        await page.getByRole('button', { name: 'Revoke Alice', exact: true }).click();
        assert.match(await page.locator('#reach-action-status').innerText(), /1 active fixture viewer\(s\) closed/);
        await page.locator('#share-grants').scrollIntoViewIfNeeded();
        await screenshot(page, 9, 4, 'revoke-grant-dormant', suffix);
        await page.click('#share-alice');
        assert.equal(await page.locator('.reach-preview-app').count(), 0);
        assert.match(await page.locator('.reach-panel').innerText(), /Access ended/);
        await screenshot(page, 9, 6, 'revoked-link-dormant', suffix);
        await page.click('#share-owner'); await page.click('#share-invite');
        // A provider switch cannot inherit the Google subject's grant.
        await page.selectOption('#share-provider', 'github');
        await page.click('#share-alice');
        assert.equal(await page.locator('.reach-preview-app').count(), 0);
        await page.click('#share-owner'); await page.click('#share-invite');
        await page.click('#share-alice');
        assert.equal(await page.locator('.reach-preview-app').count(), 1);
        await page.click('#share-owner');
        for (const [failure, expected] of [['app', /Application unavailable/], ['wrong', /Access ended/], ['expired', /Access ended/]]) {
            await page.selectOption('#share-failure', failure); await page.click('#share-check');
            assert.match(await page.locator('#share-failure-result').innerText(), expected);
            await page.locator('#share-failure-result').scrollIntoViewIfNeeded();
            await screenshot(page, 9, 6, 'share-failure-' + failure, suffix);
        }
        await page.selectOption('#reach-wing', remoteID); await page.click('#share-publish');
        assert.equal(await page.locator('#share-resource option').count(), 3);
        await page.selectOption('#share-session', JSON.stringify([remoteID, 'same-id']));
        await page.click('#share-observe');
        assert.ok(await page.getByRole('button', { name: 'Send terminal input', exact: true }).isDisabled());
        await page.locator('#share-session').scrollIntoViewIfNeeded();
        await screenshot(page, 9, 7, 'qualified-resources-observer-dormant', suffix);
        await page.click('#share-alice');
        assert.match(await page.locator('.reach-preview-app').innerText(), /read-only fixture/);
        assert.ok(await page.getByRole('button', { name: 'Send terminal input', exact: true }).isDisabled());
        await screenshot(page, 9, 7, 'alice-observer-dormant', suffix);
        await page.click('#share-owner');
        await tab(page, 'SSH preview');
        await page.fill('#reach-os-user', 'example'); await page.click('#reach-ssh');
        assert.match(await page.locator('#reach-ssh-command').innerText(), /wt ssh forge --user example/);
        assert.match(await page.locator('#reach-ssh-command').innerText(), /no shell opened/);
        await screenshot(page, 9, 5, 'ssh-review-dormant', suffix);
        record(`${viewport.width}: resource/identity grants, preview, revoke, failures, observer and SSH review`);
        // Actual fixture stop requests stay qualified. First click arms only;
        // Enter cancels. The second explicit click then sends exactly once.
        await page.click('#reach-close');
        const stop = page.locator('.egg-box[data-wing-id="fixture-mac"][data-sid="same-id"] .egg-delete');
        await stop.click();
        assert.match(await page.locator('.egg-box[data-wing-id="fixture-mac"][data-sid="same-id"]').innerText(), /Stop pairing on EhrlichMac · same-id/);
        await screenshot(page, 3, 7, 'qualified-stop-confirmation', suffix);
        await stop.press('Enter');
        assert.equal(requests.filter(r => r.type === 'pty.kill').length, 0);
        await verifyNoOverflow(page);
        await context.close();
    }
    // Execute the stop boundary once, after both viewport screenshot passes.
    const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
    const page = await context.newPage();
    await page.goto(base + '/app/');
    const stop = page.locator('.egg-box[data-wing-id="fixture-mac"][data-sid="same-id"] .egg-delete');
    await stop.waitFor(); await stop.click(); await stop.click();
    await page.waitForFunction(() => document.querySelector('.egg-box[data-wing-id="fixture-mac"][data-sid="same-id"]')?.textContent.includes('process exit unverified'));
    assert.equal(await page.locator('.egg-box[data-sid="same-id"]').count(), 2);
    for (const viewport of [{ width: 1440, height: 900 }, { width: 390, height: 844 }]) {
        await page.setViewportSize(viewport);
        await screenshot(page, 3, 7, 'stop-unverified', viewport.width === 390 ? '-mobile' : '');
    }
    record('legacy signal acknowledgement preserves the session and reports unverified exit');
    stopMode = 'verified';
    await stop.click(); await stop.click();
    await page.waitForFunction(() => !document.querySelector('.egg-box[data-wing-id="fixture-mac"][data-sid="same-id"]'));
    assert.deepEqual(requests.filter(r => r.type === 'pty.kill'), Array(2).fill({ wingId: 'fixture-mac', type: 'pty.kill', sessionId: 'same-id' }));
    assert.ok(stopVerificationReads >= 2);
    assert.equal(await page.locator('.egg-box[data-wing-id="fixture-remote"][data-sid="same-id"]').count(), 1);
    for (const viewport of [{ width: 1440, height: 900 }, { width: 390, height: 844 }]) {
        await page.setViewportSize(viewport);
        await screenshot(page, 3, 7, 'stop-verified', viewport.width === 390 ? '-mobile' : '');
    }
    record('exit receipt plus fresh disappearance routes to exact wing; verification never resends');
    remote.close();
    await page.waitForFunction(() => document.querySelector('.egg-box[data-wing-id="fixture-remote"]')?.textContent.includes('wing offline'));
    await page.selectOption('#inventory-status', 'offline');
    for (const viewport of [{ width: 1440, height: 900 }, { width: 390, height: 844 }]) {
        await page.setViewportSize(viewport); await verifyNoOverflow(page);
        const suffix = viewport.width === 390 ? '-mobile' : '';
        assert.equal(await page.locator('.egg-box[data-wing-id="fixture-remote"] .egg-delete').count(), 0);
        await screenshot(page, 3, 6, 'stale-inventory', suffix);
    }
    record('real fixture disconnect retains stale rows and disables actions');
    assert.equal(requests.filter(r => r.type === 'pty.input' || r.type === 'session_prompt').length, 0);
    assert.deepEqual(errors, []);
    await context.close();
} finally {
    await browser?.close();
    sockets.forEach(socket => { try { socket.close(); } catch {} });
    roost.kill('SIGINT');
    await new Promise(resolve => { if (roost.exitCode !== null) resolve(); else roost.once('exit', resolve); });
    fs.closeSync(log);
    fs.writeFileSync(path.join(out, 'browser-receipt.json'), JSON.stringify({ steps, captured, requests, errors, state, source: spawnSync('git', ['rev-parse', 'HEAD'], { cwd: root, encoding: 'utf8' }).stdout.trim(), generatedAt: new Date().toISOString() }, null, 2));
}
