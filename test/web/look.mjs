// Screen captures for the web app and PWA look. Real built roost + encrypted
// fixture machines, same harness as reach-share.mjs. No provider, OAuth or
// public publication. Fixtures are not live proof.
import fs from 'node:fs';
import path from 'node:path';
import { spawn, spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { removeState } from './fixture-state.mjs';
import { x25519 } from '../../web/node_modules/@noble/curves/ed25519.js';
import { hkdf } from '../../web/node_modules/@noble/hashes/hkdf.js';
import { sha256 } from '../../web/node_modules/@noble/hashes/sha2.js';
import { gcm } from '../../web/node_modules/@noble/ciphers/aes.js';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const shots = process.env.WT_LOOK_SHOTS || path.join(root, '.scratch/look');
const only = (process.env.WT_LOOK_ONLY || '').split(',').filter(Boolean);
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright');
fs.mkdirSync(shots, { recursive: true });
fs.mkdirSync(path.join(root, '.scratch'), { recursive: true });
const state = fs.mkdtempSync(path.join(root, '.scratch/look-'));
const port = Number(process.env.WT_LOOK_PORT || 18191);
const base = 'http://127.0.0.1:' + port;
const env = { PATH: process.env.PATH, HOME: path.join(state, 'home'), WINGTHING_DIR: state };
fs.mkdirSync(env.HOME);
fs.writeFileSync(path.join(state, 'wing.yaml'), 'label: Laptop\n', { mode: 0o600 });
const log = fs.openSync(path.join(state, 'roost.log'), 'w');
const roost = spawn('nice', ['-n', '10', process.env.WT_LOOK_BIN || path.join(root, 'wt'), 'roost', 'start', '--foreground', '--dev', '--addr', '127.0.0.1:' + port], { cwd: root, env, stdio: ['ignore', log, log] });
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
const sockets = new Map();
const sent = [];

function encrypt(key, value) {
    const iv = crypto.getRandomValues(new Uint8Array(12));
    const data = gcm(key, iv).encrypt(new TextEncoder().encode(JSON.stringify(value)));
    return Buffer.concat([iv, data]).toString('base64');
}

const at = minutes => new Date(Date.now() - minutes * 60000).toISOString();
const msg = (sequence, role, text, minutes) => ({ sequence, type: 'message', role, text, timestamp: at(minutes) });
const row = (id, name, agent, status, project, extra = {}) => ({
    session_id: id, name, agent, cwd: '/Users/example/code/' + project, user_id: 'local', email: 'owner@example.test',
    output: { epoch: 'look', cursor: 0 },
    lifecycle: { status, state: status === 'blocked' ? 'needs_input' : status, state_source: agent === 'claude' ? 'claude_hook' : 'codex_hook', state_cursor: 1 },
    ...extra,
});
const machines = {
    studio: { name: 'Studio', rows: [
        { ...row('wing', 'wing', 'claude', 'working', 'wingthing'), pinned: true, role: 'coordinator' },
        row('redirect', 'fix-login-redirect', 'claude', 'blocked', 'wingthing'),
        row('migrate', 'migrate-sessions-table', 'claude', 'working', 'wingthing'),
    ] },
    macbook: { name: 'MacBook', rows: [
        row('ratelimit', 'add-rate-limit-to-api', 'codex', 'working', 'wingthing'),
        row('notes', 'write-release-notes', 'claude', 'idle', 'wingthing'),
    ] },
    mini: { name: 'Mini', rows: [
        row('playwright', 'bump-playwright', 'codex', 'completed', 'wingthing'),
        row('docs', 'docs-cleanup', 'claude', 'idle', 'docs'),
    ] },
};
const events = {
    wing: [
        msg(1, 'user', 'Fix the redirect loop after login on /settings. Deploy to staging once tests pass.', 21),
        msg(5, 'assistant', 'Deploy script is ready on Studio and needs your go. The rate limit work on MacBook is still going, so I would deploy the redirect fix on its own.', 1),
    ],
    redirect: [
        msg(1, 'user', 'Fix the redirect loop after login on /settings. Deploy to staging once tests pass.', 21),
        msg(2, 'assistant', 'Reproduced it. Logging in from /settings returns to /, then / sends the request back to /settings.', 17),
        msg(3, 'assistant', 'Changed the redirect handler to keep the return path in the session cookie and read it once after login. Added a test for the loop.', 9),
        msg(4, 'assistant', 'Tests pass, 41 of 41. Asking to run ./scripts/deploy.sh staging on Studio.', 2),
        { sequence: 5, type: 'input_requested', state: 'needs_input', reason: 'Run ./scripts/deploy.sh staging', timestamp: at(2) },
    ],
    migrate: [
        msg(1, 'user', 'Add an index on sessions.user_id and backfill the new column on staging.', 40),
        msg(2, 'assistant', 'Wrote migration 0042. It adds the column, then the index concurrently so writes keep going.', 22),
        msg(3, 'assistant', 'Migration 0042 applied on the staging database. Backfilling 18,204 rows, about four minutes left.', 6),
    ],
    ratelimit: [msg(1, 'assistant', 'Added token-bucket middleware: 100 requests a minute per key, 429 with Retry-After. Three files changed.', 13)],
    notes: [msg(1, 'assistant', 'Draft is in CHANGELOG.md. Waiting for your edits.', 14)],
    playwright: [msg(1, 'assistant', 'Done. Playwright 1.49 to 1.50, all 212 tests green.', 41)],
    docs: [msg(1, 'assistant', 'Rewrote the pairing guide. Two sections left.', 120)],
};
const screens = {
    migrate: 'psql staging -f migrations/0042_sessions_user.sql\r\nALTER TABLE\r\nCREATE INDEX\r\n\r\nbackfill: 9,860 / 18,204 rows  (54%)\r\nbackfill: 11,402 / 18,204 rows (63%)\r\n',
    redirect: 'go test ./auth/...\r\nok   wingthing/auth   2.41s\r\n\r\n> Tests pass (41/41). Run ./scripts/deploy.sh staging?\r\n  1. Yes\r\n  2. No, and tell Claude what to do differently\r\n',
};
const fallback = name => [msg(1, 'assistant', name + ' is running.', 30)];

async function machine(id) {
    const fixture = machines[id];
    const token = 'local-look-' + id;
    const seed = spawnSync('python3', ['-c', 'import sqlite3,sys\nc=sqlite3.connect(sys.argv[1]);c.execute("INSERT INTO device_tokens(token,user_id,device_id,expires_at) VALUES (?, ?, ?, datetime(\'now\',\'+1 hour\'))",(sys.argv[2],"local",sys.argv[3]));c.commit()', path.join(state, 'roost.db'), token, id], { encoding: 'utf8' });
    if (seed.status !== 0) throw Error(seed.stderr);
    const priv = x25519.utils.randomSecretKey();
    const pub = Buffer.from(x25519.getPublicKey(priv)).toString('base64');
    const socket = new WebSocket(base.replace('http:', 'ws:') + '/ws/wing?token=' + encodeURIComponent(token));
    sockets.set(id, socket);
    await new Promise((resolve, reject) => {
        socket.onopen = () => socket.send(JSON.stringify({ type: 'wing.register', wing_id: id, public_key: pub, agents: ['claude', 'codex'], locked: false, purpose_binding: true }));
        socket.onerror = reject;
        socket.onmessage = e => { if (JSON.parse(e.data).type === 'registered') resolve(); };
    });
    socket.onmessage = e => {
        const outer = JSON.parse(e.data);
        if (outer.type !== 'tunnel.req') return;
        const key = hkdf(sha256, x25519.getSharedSecret(priv, Buffer.from(outer.sender_pub, 'base64')), new Uint8Array(32), new TextEncoder().encode('wt-tunnel'), 32);
        const encrypted = Buffer.from(outer.payload, 'base64');
        const inner = JSON.parse(new TextDecoder().decode(gcm(key, encrypted.subarray(0, 12)).decrypt(encrypted.subarray(12))));
        let response;
        if (inner.type === 'wing.info') response = { hostname: fixture.name, wing_label: fixture.name, platform: 'darwin', agents: ['claude', 'codex'], projects: [{ name: 'wingthing', path: '/Users/example/code/wingthing' }], capabilities: ['session.rename.v1'] };
        else if (inner.type === 'sessions.list') response = { sessions: fixture.rows };
        else if (inner.type === 'session.control' && inner.operation === 'session_read') {
            const selected = fixture.rows.find(item => item.session_id === inner.arguments.session);
            const list = selected && (events[selected.session_id] || fallback(selected.name));
            response = selected ? { session: selected.session_id, lifecycle: { ...selected.lifecycle, session_id: selected.session_id, agent: selected.agent, provider_session_id: 'provider-' + selected.session_id, process_alive: true, ready: true, cursor: list.length, head_cursor: list.length, has_more: false, report_cursor: list.length, reported_at: at(1), report: { goal: 'Fix the redirect loop and deploy to staging', update: 'Deploy script is ready on Studio and needs your go.', presence: 'awake' }, events: list } } : { error: 'Session not found' };
        }
        else if (inner.type === 'session.control' && inner.operation === 'terminal_read') response = { result: { ansi: (screens[inner.arguments.session] || '$ ').replace(/\r/g, '') } };
        else if (inner.type === 'session.control' && inner.operation === 'terminal_send') { sent.push({ machine: id, ...inner.arguments }); response = { session: inner.arguments.session, bytes_sent: inner.arguments.input.length }; }
        else if (inner.type === 'sessions.history') response = { sessions: [] };
        else if (inner.type === 'conversations.list') response = { conversations: [] };
        else response = { error: 'unsupported fixture request' };
        socket.send(JSON.stringify({ type: 'tunnel.res', request_id: outer.request_id, payload: encrypt(key, response) }));
    };
}

async function shoot(page, name, suffix) {
    if (only.length && !only.includes(name)) return;
    await page.waitForTimeout(700);
    const file = name + suffix + '.png';
    await page.screenshot({ path: path.join(shots, file) });
    console.log('SHOT ' + file);
}
async function go(page, hash) {
    await page.goto(base + '/app/' + hash);
    await page.waitForLoadState('networkidle').catch(() => {});
    await page.waitForTimeout(900);
}

// A real one-time invitation from a second, non-dev roost's own wing (the
// same setup as browser-join.mjs), compared but never confirmed: the terminal
// never gets Enter, so nothing is paired.
const joinState = fs.mkdtempSync(path.join(root, '.scratch/lj-'));
const joinBase = 'http://127.0.0.1:' + (port + 1);
const joinEnv = { PATH: process.env.PATH, HOME: path.join(state, 'join-home'), WINGTHING_DIR: joinState, WT_BASE_URL: joinBase };
fs.mkdirSync(joinEnv.HOME);
fs.writeFileSync(path.join(joinState, 'wing.yaml'), 'label: Studio\n', { mode: 0o600 });
let joinRoost;
async function joinShot(page, suffix) {
    if (!joinRoost) {
        joinRoost = spawn('nice', ['-n', '10', process.env.WT_LOOK_BIN || path.join(root, 'wt'), 'roost', 'start', '--foreground', '--addr', '127.0.0.1:' + (port + 1)], { cwd: root, env: joinEnv, stdio: ['ignore', log, log] });
        for (let i = 0; i < 150; i++) { try { if ((await fetch(joinBase + '/health')).ok) break; } catch {} await sleep(100); }
    }
    const child = spawn('nice', ['-n', '10', process.env.WT_LOOK_BIN || path.join(root, 'wt'), 'pair', '--show-link'], { cwd: root, env: joinEnv, stdio: ['pipe', 'pipe', 'pipe'] });
    let output = '', link;
    child.stdout.on('data', b => output += b); child.stderr.on('data', () => {});
    try {
        for (let i = 0; i < 100 && !link; i++) { link = output.match(/wingthing:\/\/pair#v1z\.[A-Za-z0-9_-]+/)?.[0]; if (!link) await sleep(100); }
        if (!link) throw Error('wt pair did not issue a link');
        await page.goto(joinBase + '/app/#pair');
        await page.getByRole('heading', {name:'Join from this browser'}).waitFor();
        await page.locator('#bp-link').fill(link);
        await page.locator('#bp-name').fill('Browser');
        await page.getByRole('button', {name:'Check invitation'}).click();
        await page.getByRole('button', {name:'Match · Join'}).waitFor({ timeout: 15000 });
        await shoot(page, '05-pair-match', suffix);
        await page.getByRole('button', {name:'Cancel', exact:true}).click();
    } finally { child.kill('SIGINT'); }
}

const viewports = [{ width: 1440, height: 900, suffix: '-desktop' }, { width: 390, height: 844, suffix: '-phone' }];
let browser;
try {
    for (let i = 0; i < 100; i++) { try { if ((await fetch(base + '/health')).ok) break; } catch {} await sleep(100); }
    browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH, args: ['--disable-background-networking','--disable-component-update','--disable-default-apps','--disable-sync','--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE 127.0.0.1'] });
    const contexts = [];
    for (const v of viewports) contexts.push(await browser.newContext({ viewport: { width: v.width, height: v.height } }));
    const dark = [{ width: 1440, height: 900, suffix: '-desktop-dark' }, { width: 390, height: 844, suffix: '-phone-dark' }];
    for (const v of dark) contexts.push(await browser.newContext({ viewport: { width: v.width, height: v.height }, colorScheme: 'dark' }));
    const themes = [...viewports, ...dark];
    // Empty state: signed in, no machine has connected yet.
    for (const [index, v] of themes.entries()) {
        const page = await contexts[index].newPage();
        await go(page, '');
        await shoot(page, '08-empty', v.suffix);
        await page.close();
    }
    for (const id of Object.keys(machines)) await machine(id);
    for (const [index, v] of themes.entries()) {
        const page = await contexts[index].newPage();
        page.on('pageerror', error => console.log('PAGEERROR ' + error));
        await go(page, '');
        await page.waitForSelector('[data-wing-id="studio"]', { timeout: 15000 }).catch(() => {});
        await page.waitForTimeout(1500);
        await shoot(page, '01-wing', v.suffix);
        await go(page, '#sessions');
        await shoot(page, '02-sessions', v.suffix);
        await go(page, process.env.WT_LOOK_LEGACY ? '#chat/migrate?wing=studio' : '#session/studio/migrate');
        await shoot(page, '03-session', v.suffix);
        await go(page, process.env.WT_LOOK_LEGACY ? '#chat/redirect?wing=studio' : '#approve/studio/redirect');
        await shoot(page, '04-approve', v.suffix);
        if (!process.env.WT_LOOK_LEGACY) {
            await page.getByRole('heading', {name:'Answer in terminal',exact:true}).waitFor();
            await page.getByRole('button', {name:'Answer in terminal',exact:true}).waitFor();
            if (await page.locator('[data-answer]').count()) throw Error('browser offered an approval decision');
            if (sent.length) throw Error('browser guessed terminal input: ' + JSON.stringify(sent));
            const marks = await page.evaluate(() => document.querySelectorAll('#paper .pp-st-need, #paper .pp-hot').length);
            if (!marks) throw Error('terminal fallback incorrectly cleared attention');
            await shoot(page, '04-answer-in-terminal', v.suffix);
        }
        await go(page, '#pair');
        await page.getByRole('heading', {name:'Join from this browser'}).waitFor();
        await shoot(page, '05-pair', v.suffix);
        // Leaving Pair by hash (Back or an app link) must load the app again.
        await go(page, '#machines');
        await page.getByRole('heading', {name:'Machines', exact:true}).waitFor({ timeout: 10000 });
        if (!process.env.WT_LOOK_LEGACY && (!only.length || only.includes('05-pair-match'))) await joinShot(page, v.suffix);
        await go(page, '#machines');
        await shoot(page, '07-machines', v.suffix);
        await page.close();
    }
    // Mini goes to sleep: its socket closes and the app keeps its last-seen rows.
    sockets.get('mini').close();
    await sleep(1500);
    for (const [index, v] of themes.entries()) {
        const page = await contexts[index].newPage();
        await go(page, '#sessions');
        await page.waitForTimeout(1500);
        await shoot(page, '06-offline', v.suffix);
        await page.close();
    }
    // Phone app with no network: the worker's constant disconnected page.
    for (const [index, v] of themes.entries()) {
        const page = await contexts[index].newPage();
        await go(page, '');
        await page.evaluate(() => navigator.serviceWorker && navigator.serviceWorker.ready).catch(() => {});
        await contexts[index].setOffline(true);
        try { await page.reload({ timeout: 8000 }); await shoot(page, '06-pwa-offline', v.suffix); }
        catch (error) { console.log('NOSHOT 06-pwa-offline' + v.suffix + ' ' + error.message.split('\n')[0]); }
        await contexts[index].setOffline(false);
        await page.close();
    }
} finally {
    if (browser) await browser.close();
    for (const socket of sockets.values()) try { socket.close(); } catch {}
    roost.kill('SIGTERM');
    if (joinRoost) joinRoost.kill('SIGTERM');
    await removeState(joinState);
    await removeState(state);
}
