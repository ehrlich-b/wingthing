// Loopback Chromium fixtures exercise production handlers, crypto and xterm.
import assert from 'node:assert/strict';
import { readFile, mkdir, writeFile } from 'node:fs/promises';
import { createServer } from 'node:http';
import { once } from 'node:events';
import { createRequire } from 'node:module';
import { after, before, test } from 'node:test';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('../../', import.meta.url));
const source = await readFile(path.join(root, 'web/src/pty.js'), 'utf8');
const terminal = await readFile(path.join(root, 'web/src/terminal.js'), 'utf8');
const nav = await readFile(path.join(root, 'web/src/nav.js'), 'utf8');
const dashboard = await readFile(path.join(root, 'web/src/dashboard.js'), 'utf8');
const main = await readFile(path.join(root, 'web/src/main.js'), 'utf8');
const html = await readFile(path.join(root, 'web/index.html'), 'utf8');
const inputControls = html.slice(html.indexOf('<div id="passkey-overlay"'), html.indexOf('</section>', html.indexOf('<div id="passkey-overlay"')));
const input = terminal.match(/^export function sendPTYInput\(text\) \{[\s\S]*?^\}/m)[0];
const dependencies = new Map();
for (const match of (source + nav + dashboard + await readFile(path.join(root, 'web/src/webrtc.js'), 'utf8')).matchAll(/import \{([^}]+)\} from '([^']+)';/g)) {
    const name = new URL(match[2], 'http://local/src/pty.js').pathname;
    const exports = dependencies.get(name) || new Set();
    match[1].split(',').forEach(value => exports.add(value.trim()));
    dependencies.set(name, exports);
}
const real = new Set(['crypto.js', 'helpers.js', 'security.js', 'session-reference.js', 'terminal-attachment.js', 'output-notice.js', 'output-notice-content.js', 'nav.js', 'session-inventory.js', 'dashboard.js']);
const server = createServer(async (req, res) => {
    try {
        res.setHeader('Content-Type', 'text/javascript');
        if (req.url === '/') {
            res.setHeader('Content-Type', 'text/html');
            res.end(`<!doctype html><meta name="viewport" content="width=device-width, initial-scale=1">
                <title>Terminal recovery</title><link rel="stylesheet" href="/style.css">
                <link rel="stylesheet" href="/node_modules/@xterm/xterm/css/xterm.css">
                <script type="importmap">{"imports":{"@noble/":"/node_modules/@noble/"}}</script>
                <script src="/node_modules/@xterm/xterm/lib/xterm.js"></script>
                <main id="app" style="padding:16px;height:100vh;display:flex;flex-direction:column;gap:12px">
                <header><button id="home-btn">Home</button> Terminal <span id="pty-status"></span></header>
                <section id="home-section" style="display:none">Home</section>
                ${html.slice(html.indexOf('<div id="reconnect-banner"'), html.indexOf('<div id="terminal-control-notice"'))}
                <div id="terminal-control-notice" style="display:none"></div>
                <section id="terminal-section" style="flex:1;min-height:300px">
                <div id="terminal-container"></div>
                <div id="replay-overlay" style="display:none"><div id="replay-bar"><div id="replay-fill"></div></div>
                <div id="replay-label">restoring session...</div></div>
                ${inputControls}</section></main>`);
        } else if (req.url === '/src/pty.js') {
            res.end(source + '\nexport { setupPTYHandlers, ptyReconnectAttach };');
        } else if (req.url === '/src/webrtc.js') {
            res.end(await readFile(path.join(root, 'web/src/webrtc.js'), 'utf8') + '\nexport { handleDCMessage };');
        } else if (req.url === '/src/terminal.js') {
            res.end(`import { S, clearNotification, sendViaDC } from '/fixture.js';
                import { e2eEncrypt } from './crypto.js';
                import { terminalReferenceMatches } from './session-reference.js';
                export const saveTermBuffer = () => {}, clearTermBuffer = () => {};
                ${input}`);
        } else if (req.url === '/src/fixture-controls.js') {
            // Use the production navigation, authentication and mobile input bindings.
            res.end(`import { S, DOM } from './state.js';
                import { showHome } from './nav.js';
                import { handlePTYPasskey, retryReconnect } from './pty.js';
                import { sendPTYInput } from './terminal.js';
                ${main.match(/DOM.homeBtn.addEventListener\('click', showHome\);/)[0]}
                ${main.slice(main.indexOf('// Reconnect button'), main.indexOf('// Passkey button'))}
                ${main.slice(main.indexOf('// Passkey button'), main.indexOf('// Init chat view'))}
                ${main.slice(main.indexOf('// Type overlay (mobile text input)'), main.indexOf('// Keyboard shortcuts'))}`);
        } else if (req.url === '/fixture.js') {
            res.end('export const { S, clearNotification, sendViaDC } = window.fixture;');
        } else if (req.url === '/src/crypto.js') {
            const crypto = await readFile(path.join(root, 'web/src/crypto.js'), 'utf8');
            res.end(crypto.replace('export async function deriveE2EKey(', 'async function deriveKey(')
                .replace('export async function e2eDecrypt(', 'async function decrypt(') + `
                export async function deriveE2EKey(...args) {
                    await window.keyGate;
                    return deriveKey(...args);
                }
                export async function e2eDecrypt(...args) {
                    await window.decodeGates?.get(args[0]);
                    return decrypt(...args);
                }`);
        } else if (req.url.startsWith('/src/') && real.has(req.url.slice(5))) {
            res.end(await readFile(path.join(root, 'web', req.url)));
        } else if (dependencies.has(req.url)) {
            res.end([...dependencies.get(req.url)].map(name =>
                `export const ${name} = window.fixture[${JSON.stringify(name)}] || (() => {});`
            ).join('\n'));
        } else if (/^\/(node_modules|fonts)\/[\w@/.-]+$/.test(req.url) || req.url === '/style.css') {
            if (req.url.endsWith('.css')) res.setHeader('Content-Type', 'text/css');
            if (req.url.endsWith('.woff2')) res.setHeader('Content-Type', 'font/woff2');
            res.end(await readFile(path.join(root, 'web', req.url)));
        } else res.writeHead(404).end();
    } catch (err) { res.writeHead(500).end(String(err)); }
});
let browser, base;
const out = process.env.WT_CORRUPTION_OUT || path.join(root, '.scratch/pty-corruption');
before(async () => {
    await mkdir(out, { recursive: true });
    server.listen(0, '127.0.0.1');
    await once(server, 'listening');
    base = `http://localhost:${server.address().port}`;
    const { chromium } = process.env.PLAYWRIGHT_MODULE ? await import(process.env.PLAYWRIGHT_MODULE)
        : createRequire(path.join(root, 'web/package.json'))('playwright');
    browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH || undefined,
        args: ['--disable-background-networking', '--renderer-process-limit=2'] });
});
after(async () => {
    await browser?.close();
    await new Promise(resolve => server.close(resolve));
});

async function fixture(t, { width = 1440, reattach = true } = {}) {
    const page = await browser.newPage({ viewport: { width, height: 844 } });
    page.setDefaultTimeout(5000);
    t.after(() => page.close());
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    t.after(() => assert.deepEqual(errors, [], 'no unhandled browser errors'));
    await page.route('**/*', route => new URL(route.request().url()).origin === base ? route.continue() : route.abort());
    await page.goto(base);
    await page.evaluate(async ({ reattach, width }) => {
        const noop = () => {};
        const previews = [];
        const term = new Terminal({ cols: width === 390 ? 36 : 100, rows: 28 });
        term.open(document.getElementById('terminal-container'));
        const S = { term, fitAddon: { fit: noop }, ptySessionId: 'session-a', ptyWingId: 'wing-a',
            wingsData: [{ wing_id: 'wing-a', spectate: true }], sessionsData: [], tunnelAuthTokens: {}, currentUser: null };
        const DOM = new Proxy({ ptyStatus: document.getElementById('pty-status'), terminalSection: document.getElementById('terminal-section'),
            homeBtn: document.getElementById('home-btn'), homeSection: document.getElementById('home-section') }, {
            get: (target, name) => target[name] ||= document.createElement('div'),
        });
        window.fixture = { S, DOM, handlePreview: preview => {
            if (window.h.previewError) throw Error('preview rendering failed');
            previews.push(preview);
        }, clearNotification: noop, sendViaDC: () => false, sendTunnelRequest: async () => {},
            resumeAckMatches: () => true, sessionRoute: () => '#terminal',
            findSessionResource: () => null, wingDisplayName: () => 'Local wing', formatSessionTitle: () => 'Terminal' };
        const pty = await import('/src/pty.js');
        const nav = await import('/src/nav.js');
        // Model the persistent shell's navigation boundary while keeping the
        // production cancellation handler and recovery engine under test.
        const shell = document.createElement('main');
        shell.id = 'paper'; shell.hidden = true; shell.textContent = 'Home';
        document.body.append(shell);
        window.addEventListener('shell-navigate', () => { shell.hidden = S.activeView !== 'home'; });
        const p2p = await import('/src/webrtc.js');
        window._deleteSession = noop;
        const cryptoModule = await import('/src/crypto.js');
        const { sendPTYInput } = await import('/src/terminal.js');
        await import('/src/fixture-controls.js');
        term.onData(sendPTYInput);
        const { gcm } = await import('/node_modules/@noble/ciphers/aes.js');
        const { x25519 } = await import('/node_modules/@noble/curves/ed25519.js');
        const { bytesToB64 } = await import('/src/helpers.js');
        const publicKey = bytesToB64(x25519.getPublicKey(x25519.utils.randomSecretKey()));
        const key = await cryptoModule.deriveE2EKey(publicKey, 'wing-a');
        const sent = [], sockets = [], gates = new Map();
        let releaseKey;
        const holdKey = () => { window.keyGate = new Promise(resolve => { releaseKey = resolve; }); };
        const resumeKey = () => { releaseKey?.(); window.keyGate = null; };
        const nativeDecrypt = cryptoModule.e2eDecrypt;
        window.decodeGates = new Map();
        const holdDecode = data => {
            let release;
            window.decodeGates.set(data, new Promise(resolve => { release = resolve; }));
            return release;
        };
        class Socket {
            static OPEN = 1; static CLOSED = 3; readyState = 1;
            constructor() { sockets.push(this); queueMicrotask(() => this.onopen?.()); }
            send(data) { sent.push(JSON.parse(data)); }
            dispatchEvent(event) { this.onmessage(event); return true; }
            close() { this.readyState = 3; setTimeout(() => this.onclose?.(), 0); }
        }
        window.WebSocket = Socket;
        const send = (msg, socket = S.ptyWs) => socket.onmessage({ data: JSON.stringify({ session_id: S.ptySessionId, ...msg }) });
        const attach = (replay = reattach, session = 'session-a') => {
            S.ptySessionId = session;
            S.ptyWs = new Socket();
            pty.setupPTYHandlers(S.ptyWs, replay, session);
        };
        const start = (sessionId = S.ptySessionId) => send({ type: 'pty.started', session_id: sessionId, public_key: publicKey });
        const frame = async (text, compressed = false, corrupt = false) => {
            let bytes = new TextEncoder().encode(text);
            if (compressed) bytes = new Uint8Array(await new Response(new Blob([bytes]).stream().pipeThrough(new CompressionStream('gzip'))).arrayBuffer());
            // Corrupt the checksum BEFORE authenticating; AES-GCM still succeeds.
            if (corrupt === 'truncated') bytes = bytes.slice(0, bytes.length - 4);
            else if (corrupt) bytes[bytes.length - 8] ^= 255;
            const iv = crypto.getRandomValues(new Uint8Array(12));
            const ciphertext = gcm(key, iv).encrypt(bytes);
            const envelope = new Uint8Array(iv.length + ciphertext.length);
            envelope.set(iv); envelope.set(ciphertext, iv.length);
            return { type: 'pty.output', data: bytesToB64(envelope), compressed };
        };
        const text = () => Array.from({ length: term.buffer.active.length }, (_, i) => term.buffer.active.getLine(i).translateToString(true)).join('\n').trim();
        const settle = async () => { for (let i = 0; i < 8; i++) await new Promise(r => setTimeout(r, 0)); };
        const NativeDecompressionStream = window.DecompressionStream;
        window.DecompressionStream = class {
            constructor(format) {
                const native = new NativeDecompressionStream(format);
                this.writable = native.writable;
                this.readable = native.readable.pipeThrough(new TransformStream({ async transform(chunk, controller) {
                    if (gates.has('gunzip')) await gates.get('gunzip').promise;
                    controller.enqueue(chunk);
                } }));
            }
        };
        const hold = () => {
            let resolve;
            gates.set('gunzip', { promise: new Promise(r => { resolve = r; }), release: () => resolve() });
        };
        const release = () => { gates.get('gunzip')?.release(); gates.delete('gunzip'); };
        const reconnect = () => { S.ptyReconnecting = true; pty.ptyReconnectAttach(S.ptySessionId, 0, S.ptyWingId); };
        const realAttach = () => { pty.detachPTY(); sent.length = 0; pty.attachPTY('session-a', 1, 'wing-a'); };
        window.h = { S, DOM, previews, sent, sockets, send, attach, realAttach, start, frame, text, settle, hold, release, input: sendPTYInput, key, nativeDecrypt, detach: pty.detachPTY, reconnect,
            holdDecode, holdKey, resumeKey, disconnect: pty.disconnectPTY,
            replace: () => { nav.showTerminal(); pty.attachPTY('session-a', 1, 'wing-a'); },
            p2p: msg => p2p.handleDCMessage('wing-a', 'session-a', JSON.stringify({ session_id: 'session-a', ...msg })), connect: () => pty.connectPTY('shell', null, 'wing-a') };
        attach();
    }, { reattach, width });
    return page;
}

async function brokenReplay(page) {
    await page.evaluate(async () => {
        const good = await h.frame('before missing output\r\n', true);
        const bad = await h.frame('missing output\r\n', true, true);
        const after = await h.frame('after missing output\r\n');
        h.S.e2eKey = h.key;
        const authenticated = await h.nativeDecrypt(bad.data);
        if (!(authenticated instanceof Uint8Array)) throw Error('frame did not authenticate');
        h.start(); h.send(good); h.send(bad); h.send(after);
        await h.settle();
    });
}

for (const width of [390, 1440]) {
    test(`corrupt replay is visible at ${width}px; Reload reconnects without early or repeated input`, async t => {
        const page = await fixture(t, { width });
        await page.evaluate(async () => { h.start(); await h.settle(); h.S.term.focus(); });
        await page.keyboard.type('already sent');
        await page.keyboard.press('Enter');
        await page.waitForFunction(() => h.sent.some(m => m.type === 'pty.input'));
        const original = await page.evaluate(() => h.sent.filter(m => m.type === 'pty.input'));
        await page.evaluate(() => h.attach(true));
        await brokenReplay(page);
        const notice = page.getByRole('dialog', { name: 'Output incomplete' });
        await notice.waitFor({ state: 'visible' });
        assert.equal(await notice.innerText(), 'Output incomplete. Some terminal output could not be decoded. Reload to restore the session.\n\nReload');
        assert.equal(await notice.getByRole('button', { name: 'Reload', exact: true }).isEnabled(), true);
        assert.equal(await page.locator('#terminal-container').evaluate(el => el.inert), true);
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
        await page.evaluate(() => h.S.term.focus());
        await page.keyboard.type('typed while incomplete');
        await page.keyboard.press('Escape');
        assert.equal(await notice.isVisible(), true, 'notice remains visible during recovery');
        assert.deepEqual(await page.evaluate(() => h.sent.filter(m => m.type === 'pty.input')), original);
        await page.screenshot({ path: path.join(out, `corrupt-replay-${width}.png`), fullPage: true });
        const before = await page.evaluate(() => h.sockets.length);
        await page.evaluate(() => h.holdKey());
        await notice.getByRole('button', { name: 'Reload', exact: true }).click();
        await page.waitForFunction(() => h.sent.some(m => m.type === 'pty.attach'));
        await page.keyboard.type('typed before acknowledgment');
        await page.keyboard.press('Enter');
        await page.evaluate(async () => {
            h.start();
            h.send(await h.frame('fresh output\r\n', true));
            await h.settle();
        });
        await page.keyboard.type('typed during key setup');
        await page.keyboard.press('Enter');
        await page.evaluate(() => h.settle());
        assert.equal(await notice.isVisible(), true, 'notice keeps focus until key setup and replay finish');
        assert.deepEqual(await page.evaluate(() => h.sent.filter(m => m.type === 'pty.input')), original);
        assert.equal(await page.evaluate(() => h.sockets.length), before + 1);
        assert.deepEqual(await page.evaluate(() => h.sent.filter(m => m.type === 'pty.attach').map(m => ({ session: m.session_id, wing: m.wing_id, takeover: !!m.takeover }))),
            [{ session: 'session-a', wing: 'wing-a', takeover: false }]);
        await page.evaluate(() => h.resumeKey());
        await page.waitForFunction(() => h.text() === 'fresh output');
        await notice.waitFor({ state: 'detached' });
        assert.deepEqual(await page.evaluate(() => h.sent.filter(m => m.type === 'pty.input')), original, 'nothing is resent after recovery');
        await page.screenshot({ path: path.join(out, `reloaded-${width}.png`), fullPage: true });
        await writeFile(path.join(out, `recovery-${width}.json`), JSON.stringify(await page.evaluate(() => ({
            text: h.text(), sent: h.sent, sockets: h.sockets.length, keyReady: !!h.S.e2eKey,
        })), null, 2));
        await page.evaluate(() => h.S.term.focus());
        await page.keyboard.type('new input');
        await page.waitForFunction(count => h.sent.filter(m => m.type === 'pty.input').length > count, original.length);
    });

    test(`Reload permits passkey authentication at ${width}px without early terminal input`, async t => {
        const page = await fixture(t, { width });
        const cdp = await page.context().newCDPSession(page);
        await cdp.send('WebAuthn.enable');
        await cdp.send('WebAuthn.addVirtualAuthenticator', { options: {
            protocol: 'ctap2', transport: 'internal', hasResidentKey: true,
            hasUserVerification: true, isUserVerified: true, automaticPresenceSimulation: true,
        } });
        const credentialId = await page.evaluate(async () => {
            const credential = await navigator.credentials.create({ publicKey: {
                challenge: crypto.getRandomValues(new Uint8Array(32)),
                rp: { name: 'Terminal recovery', id: location.hostname },
                user: { id: new Uint8Array([1]), name: 'test', displayName: 'Test' },
                pubKeyCredParams: [{ type: 'public-key', alg: -7 }],
                authenticatorSelection: { userVerification: 'required' },
            } });
            return credential.id;
        });
        await page.route('**/api/app/passkey', route => route.fulfill({ json: [{ credential_id: credentialId }] }));
        await brokenReplay(page);
        await page.evaluate(() => { h.S.e2eKey = null; h.holdKey(); });
        await page.getByRole('button', { name: 'Reload', exact: true }).click();
        await page.waitForFunction(() => h.sent.some(m => m.type === 'pty.attach'));
        await page.evaluate(() => h.send({ type: 'passkey.challenge', session_id: 'session-a',
            viewer_id: 'viewer-a', challenge: 'cmVsb2FkLWNoYWxsZW5nZQ', rp_id: location.hostname }));
        await page.evaluate(() => h.S.term.focus());
        await page.keyboard.type('typed while passkey pending');
        await page.keyboard.press('Enter');
        await page.getByRole('button', { name: 'authenticate with passkey' }).click();
        await page.waitForFunction(() => h.sent.some(m => m.type === 'passkey.response'));
        const response = await page.evaluate(() => h.sent.find(m => m.type === 'passkey.response'));
        assert.equal(response.credential_id, credentialId);
        assert.equal(response.session_id, 'session-a');
        assert.equal(response.viewer_id, 'viewer-a');
        assert.equal(JSON.parse(Buffer.from(response.client_data_json, 'base64')).challenge, 'cmVsb2FkLWNoYWxsZW5nZQ');
        await page.evaluate(async () => { h.start(); h.send(await h.frame('authenticated recovery\r\n', true)); await h.settle(); });
        await page.evaluate(() => h.S.term.focus());
        await page.keyboard.type('typed during key setup');
        await page.keyboard.press('Enter');
        assert.equal(await page.getByRole('dialog').isVisible(), true);
        assert.deepEqual(await page.evaluate(() => h.sent.filter(m => m.type === 'pty.input')), []);
        await page.evaluate(() => h.resumeKey());
        await page.waitForFunction(() => h.text() === 'authenticated recovery');
        assert.equal(await page.getByRole('dialog').count(), 0);
        assert.deepEqual(await page.evaluate(() => h.sent.filter(m => m.type === 'pty.input')), []);
    });
}

for (const phase of ['notice', 'authentication', 'failed reload']) {
    test(`Home leaves a corrupted terminal during ${phase} and cancels recovery`, async t => {
        const page = await fixture(t);
        await brokenReplay(page);
        await page.evaluate(() => { window.oldReload = document.querySelector('#pty-output-notice button'); });
        if (phase !== 'notice') {
            await page.getByRole('button', { name: 'Reload', exact: true }).click();
            await page.waitForFunction(() => h.sent.some(m => m.type === 'pty.attach'));
            await page.evaluate(phase => {
                if (phase === 'authentication') h.send({ type: 'passkey.challenge', challenge: 'cmVsb2Fk', rp_id: location.hostname });
                else h.send({ type: 'error', message: 'wing unavailable' });
            }, phase);
        }
        await page.keyboard.press('Escape');
        await page.getByRole('button', { name: 'Home', exact: true }).click();
        assert.equal(await page.evaluate(() => h.S.activeView), 'home');
        assert.equal(await page.evaluate(() => h.S.ptySessionId), null);
        assert.equal(await page.getByRole('dialog').count(), 0);
        assert.equal(await page.locator('#paper').isVisible(), true);
        assert.equal(await page.locator('#home-section').isVisible(), false);
        assert.equal(await page.locator('#terminal-section').isVisible(), false);
        const socketCount = await page.evaluate(() => { window.oldReload.click(); return h.sockets.length; });
        // The failed reconnect already armed a one-second retry; Home must cancel it.
        if (phase === 'failed reload') await page.waitForTimeout(1200);
        assert.equal(await page.evaluate(() => h.sockets.length), socketCount);
        assert.deepEqual(await page.evaluate(() => h.sent.filter(m => m.type === 'pty.input')), []);
        assert.equal(await page.evaluate(() => document.querySelector('#terminal-container').inert), false);
    });
}

test('mobile input controls stay inaccessible until recovery finishes', async t => {
    const page = await fixture(t, { width: 390 });
    await page.getByRole('button', { name: 'type', exact: true }).click();
    await page.locator('#type-input').fill('unsent mobile text');
    await brokenReplay(page);
    await page.evaluate(() => { h.S.e2eKey = null; h.holdKey(); });
    for (const phase of ['notice', 'reload', 'key setup']) {
        if (phase === 'reload') {
            await page.getByRole('button', { name: 'Reload', exact: true }).click();
            await page.waitForFunction(() => h.sent.some(m => m.type === 'pty.attach'));
        }
        if (phase === 'key setup') {
            await page.evaluate(async () => { h.start(); h.send(await h.frame('mobile recovery\r\n')); await h.settle(); });
        }
        // Real pointer events aimed at inert mobile controls must not send input.
        for (const selector of ['#type-send', '[data-key="esc"]', '[data-seq="↑"]']) {
            const box = await page.locator(selector).boundingBox();
            await page.mouse.click(box.x + box.width / 2, box.y + box.height / 2);
        }
        await page.locator('#type-input').evaluate(input => input.focus());
        await page.keyboard.type('early input');
        await page.keyboard.press('Enter');
        await page.evaluate(() => h.settle());
        assert.deepEqual(await page.evaluate(() => h.sent.filter(m => m.type === 'pty.input')), [], phase);
    }
    await page.evaluate(() => h.resumeKey());
    await page.waitForFunction(() => h.text() === 'mobile recovery');
    await page.locator('#type-send').click();
    await page.waitForFunction(() => h.sent.some(m => m.type === 'pty.input'));
    assert.equal(await page.evaluate(async () => new TextDecoder().decode(await h.nativeDecrypt(h.sent.find(m => m.type === 'pty.input').data))), 'unsent mobile text\r');
});

for (const reattach of [false, true]) {
    for (const corruption of ['checksum', 'truncated', 'authentication', 'base64']) {
        test(`${reattach ? 'replay' : 'live'} ${corruption} failure shows output incomplete`, async t => {
            const page = await fixture(t, { reattach });
            await page.evaluate(async ({ reattach, corruption }) => {
                if (reattach) h.holdKey();
                h.start();
                if (!reattach) await h.settle();
                const bad = await h.frame('missing\r\n', true, corruption === 'checksum' || corruption === 'truncated' ? corruption : false);
                if (corruption === 'authentication') bad.data = bad.data.slice(0, -8) + 'AAAAAAA=';
                if (corruption === 'base64') bad.data = '%%%';
                h.send(bad);
                h.send(await h.frame('following output\r\n'));
                if (reattach) h.resumeKey();
            }, { reattach, corruption });
            await page.getByRole('dialog', { name: 'Output incomplete' }).waitFor({ state: 'visible' });
            await page.waitForFunction(() => h.text().includes('following output'));
        });
    }
}

for (const reattach of [true, false]) {
    for (const width of [390, 1440]) {
        test(`corrupt ${reattach ? 'gzip replay' : 'live output'} followed by exit permits reading but no input at ${width}px`, async t => {
            const page = await fixture(t, { reattach, width });
            await page.evaluate(async reattach => {
                if (reattach) h.holdKey();
                h.start();
                if (!reattach) await h.settle();
                h.send(await h.frame(Array.from({ length: 90 }, (_, i) => `retained line ${String(i).padStart(2, '0')}\r\n`).join(''), reattach));
                const bad = await h.frame('missing final output\r\n', reattach, reattach);
                if (!reattach) bad.data = bad.data.slice(0, -8) + 'AAAAAAA=';
                h.send(bad);
                h.send({ type: 'pty.exited' });
                if (reattach) h.resumeKey();
            }, reattach);
            await page.waitForFunction(() => h.text().includes('--- session ended ---') && h.DOM.ptyStatus.textContent === 'exited');
            const notice = page.getByRole('dialog', { name: 'Output incomplete' });
            assert.equal(await notice.isVisible(), true);
            assert.equal(await notice.innerText(), 'Output incomplete. ' + (reattach ? 'Some terminal output could not be decoded.' : "Can't decrypt output.") + ' The session has ended.');
            assert.equal(await notice.getByRole('button', { name: 'Reload', exact: true }).count(), 0);
            assert.equal(await page.locator('#terminal-section').isVisible(), true);
            assert.equal(await page.evaluate(() => h.S.ptySessionId), null);
            assert.equal(await page.locator('#terminal-container').evaluate(el => el.inert), false);
            await page.waitForFunction(() => h.S.term.buffer.active.viewportY > 0);
            const beforeScroll = await page.evaluate(() => h.S.term.buffer.active.viewportY);
            const screen = await page.locator('.xterm-screen').boundingBox();
            await page.mouse.move(screen.x + screen.width / 2, screen.y + screen.height / 2);
            await page.mouse.wheel(0, -500);
            await page.waitForFunction(before => h.S.term.buffer.active.viewportY < before, beforeScroll);
            const selectedLine = await page.evaluate(() => h.S.term.buffer.active.getLine(h.S.term.buffer.active.viewportY).translateToString(true));
            assert.match(selectedLine, /^retained line \d{2}$/);
            const { cols, rows } = await page.evaluate(() => ({ cols: h.S.term.cols, rows: h.S.term.rows }));
            const y = screen.y + screen.height / rows / 2;
            await page.mouse.move(screen.x + 1, y);
            await page.mouse.down();
            await page.mouse.move(screen.x + screen.width / cols * 16, y, { steps: 16 });
            await page.mouse.up();
            assert.equal(await page.evaluate(() => h.S.term.getSelection()), selectedLine, 'pointer selection works on ended output');
            await page.screenshot({ path: path.join(out, `corrupt-${reattach ? 'replay' : 'live'}-exited-${width}.png`), fullPage: true });
            const before = await page.evaluate(() => ({ sent: h.sent, sockets: h.sockets.length }));
            await page.evaluate(() => h.S.term.focus());
            await page.keyboard.type('typed after exit');
            await page.keyboard.press('Enter');
            await page.evaluate(async () => { h.input('direct input after exit'); await h.settle(); });
            assert.deepEqual(await page.evaluate(() => ({ sent: h.sent, sockets: h.sockets.length })), before, 'ended session cannot send input or reconnect');
            // Departure and starting a replacement session still clear the old notice.
            if (reattach) await page.getByRole('button', { name: 'Home', exact: true }).click();
            else await page.evaluate(() => h.connect());
            assert.equal(await notice.count(), 0);
        });
    }
}

test('Reload retains observation mode and never takes over input', async t => {
    const page = await fixture(t);
    await page.evaluate(() => { h.S.spectating = true; });
    await brokenReplay(page);
    await page.getByRole('button', { name: 'Reload', exact: true }).click();
    await page.waitForFunction(() => h.sent.some(m => m.type === 'pty.attach'));
    assert.deepEqual(await page.evaluate(() => h.sent.filter(m => m.type === 'pty.attach').map(m => ({ spectate: m.spectate, takeover: !!m.takeover }))), [{ spectate: true, takeover: false }]);
});

test('a fresh replay that is also corrupted keeps its own notice', async t => {
    const page = await fixture(t);
    await brokenReplay(page);
    await page.getByRole('button', { name: 'Reload', exact: true }).click();
    await page.evaluate(() => h.settle());
    await brokenReplay(page);
    await page.getByRole('dialog', { name: 'Output incomplete' }).waitFor({ state: 'visible' });
    assert.equal(await page.getByRole('button', { name: 'Reload', exact: true }).isEnabled(), true);
});

for (const change of ['session', 'socket', 'wing']) {
    test(`late successful and corrupt output from a replaced ${change} is ignored`, async t => {
        const page = await fixture(t, { reattach: false });
        await page.evaluate(async change => {
            h.start(); await h.settle(); h.hold();
            const old = h.S.ptyWs;
            h.send(await h.frame('stale\r\n', true));
            h.send(await h.frame('stale corruption\r\n', true, true));
            await h.settle();
            if (change === 'wing') h.S.ptyWingId = 'wing-b';
            h.attach(false, change === 'session' ? 'session-b' : 'session-a');
            h.start(); await h.settle();
            // Exercise both queued work and a late event on the replaced socket.
            h.send(await h.frame('late stale event\r\n'), old);
            h.release();
            h.send(await h.frame('replacement output\r\n'));
            await h.settle();
        }, change);
        await page.waitForFunction(() => h.text() === 'replacement output');
        assert.equal(await page.getByRole('dialog').count(), 0);
    });
}

for (const reattach of [false, true]) {
    test(`socket loss during ${reattach ? 'replay' : 'live output'} still reconnects automatically`, async t => {
        const page = await fixture(t, { reattach });
        await page.evaluate(async () => {
            h.hold(); h.start(); h.send(await h.frame('pending output\r\n', true));
            await h.settle(); h.S.ptyWs.close(); h.release();
        });
        await page.waitForFunction(() => h.sockets.length === 2);
        assert.equal(await page.getByRole('dialog').count(), 0);
    });
}

test('socket loss while the notice is up still follows automatic reconnect', async t => {
    const page = await fixture(t);
    await brokenReplay(page);
    await page.evaluate(() => h.S.ptyWs.close());
    await page.waitForFunction(() => h.sockets.length === 2);
    assert.equal(await page.getByRole('dialog').isVisible(), true);
    await page.evaluate(async () => { h.start(); h.send(await h.frame('automatic recovery\r\n')); });
    await page.waitForFunction(() => h.text() === 'automatic recovery');
    assert.equal(await page.getByRole('dialog').count(), 0);
});

test('P2P decode failures show the same notice and reload through the full reconnect', async t => {
    const page = await fixture(t, { reattach: false });
    await page.evaluate(async () => { h.start(); await h.settle(); h.p2p(await h.frame('bad P2P\r\n', true, true)); });
    await page.getByRole('button', { name: 'Reload', exact: true }).click();
    await page.waitForFunction(() => h.sent.some(m => m.type === 'pty.attach'));
    await page.evaluate(async () => { h.start(); h.send(await h.frame('recovered from P2P corruption\r\n')); });
    await page.waitForFunction(() => h.text() === 'recovered from P2P corruption');
    assert.equal(await page.getByRole('dialog').count(), 0);
});

test('late P2P decode failure cannot show a notice over a replacement session', async t => {
    const page = await fixture(t, { reattach: false });
    await page.evaluate(async () => {
        h.start(); await h.settle(); h.hold();
        h.p2p(await h.frame('stale P2P\r\n', true, true));
        h.attach(false, 'session-b'); h.start(); h.release(); await h.settle();
        h.send(await h.frame('new session\r\n'));
    });
    await page.waitForFunction(() => h.text() === 'new session');
    assert.equal(await page.getByRole('dialog').count(), 0);
});

test('leaving a corrupted terminal removes the notice and its old action cannot reconnect', async t => {
    const page = await fixture(t);
    await brokenReplay(page);
    await page.evaluate(() => {
        const button = document.querySelector('#pty-output-notice button');
        h.detach(); h.attach(false, 'session-b'); h.start(); button.click();
    });
    assert.equal(await page.getByRole('dialog').count(), 0);
    assert.equal(await page.evaluate(() => h.sent.filter(m => m.type === 'pty.attach').length), 0);
});

test('initial attach continues to ignore typing before acknowledgment', async t => {
    const page = await fixture(t);
    await page.evaluate(() => { h.realAttach(); h.S.term.focus(); });
    await page.keyboard.type('hunter2');
    await page.keyboard.press('Enter');
    await page.evaluate(() => h.settle());
    assert.equal(await page.evaluate(() => h.sent.filter(m => m.type === 'pty.input').length), 0);
    assert.deepEqual(await page.evaluate(() => h.sent.map(m => m.type)), ['pty.attach']);
});

for (const transport of ['relay', 'P2P']) {
    test(`in-flight ${transport} corruption cannot affect a new session on the same socket`, async t => {
        const page = await fixture(t, { reattach: false });
        await page.evaluate(async transport => {
            h.start(); await h.settle();
            const bad = await h.frame('old authenticated corruption\r\n', true, true);
            const release = h.holdDecode(bad.data);
            if (transport === 'P2P') h.p2p(bad); else h.send(bad);
            await h.settle();
            h.start('session-b'); await h.settle();
            h.send(await h.frame('new session output\r\n'));
            await h.settle(); release(); await h.settle();
        }, transport);
        await page.waitForFunction(() => h.text() === 'new session output');
        assert.equal(await page.getByRole('dialog').count(), 0);
    });
}

test('session-routed replay corruption is visible before the wing is known', async t => {
    const page = await fixture(t);
    await page.evaluate(async () => { h.S.ptyWingId = null; h.attach(true); });
    await brokenReplay(page);
    await page.getByRole('dialog', { name: 'Output incomplete' }).waitFor({ state: 'visible' });
});

for (const reattach of [false, true]) {
    for (const exit of ['Home', 'Home before display', 'disconnect', 'replacement']) {
        test(`${reattach ? 'attached' : 'started'} terminal clears its key-error banner on ${exit}`, async t => {
            const page = await fixture(t, { reattach });
            await page.evaluate(async exit => {
                h.send({ type: 'pty.started', public_key: '%%%' });
                if (exit !== 'Home before display') return;
                // Cancel after failure, before the pending display timer can run.
                for (let i = 0; i < 100 && !h.S.ptyInputBlocked; i++) await Promise.resolve();
                if (!h.S.ptyInputBlocked) throw Error('key failure did not finish');
                if (document.getElementById('reconnect-banner').style.display !== 'none') throw Error('banner timer already ran');
                h.DOM.homeBtn.click();
                await h.settle();
            }, exit);
            if (exit !== 'Home before display') {
                await page.getByRole('button', { name: 'retry', exact: true }).waitFor({ state: 'visible' });
                if (exit === 'Home') await page.getByRole('button', { name: 'Home', exact: true }).click();
                if (exit === 'disconnect') await page.evaluate(() => h.disconnect());
            }
            if (exit !== 'replacement') {
                assert.equal(await page.locator('#reconnect-banner').isVisible(), false);
                assert.equal(await page.evaluate(() => h.S.ptySessionId), null);
            }
            // Use ordinary attachment, without the Retry wrapper's banner cleanup.
            await page.evaluate(async () => {
                h.replace(); await h.settle(); h.start('session-a'); await h.settle(); h.S.term.focus();
            });
            assert.equal(await page.locator('#reconnect-banner').isVisible(), false);
            await page.keyboard.type('healthy input');
            await page.waitForFunction(() => h.sent.some(msg => msg.type === 'pty.input'));
            assert.equal(await page.evaluate(async () => {
                const messages = h.sent.filter(msg => msg.type === 'pty.input');
                return (await Promise.all(messages.map(async msg => new TextDecoder().decode(await h.nativeDecrypt(msg.data))))).join('');
            }), 'healthy input');
        });
    }
    for (const [keyCase, publicKey] of ['missing', null, '', '%%%', 'YWJj', 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=', 42].entries()) {
        test(`${reattach ? 'attached' : 'started'} terminal rejects key ${JSON.stringify(publicKey)} and Retry reconnects securely`, async t => {
            const page = await fixture(t, { reattach });
            await page.evaluate(async publicKey => {
                h.send({ type: 'pty.started', ...(publicKey === 'missing' ? {} : { public_key: publicKey }) });
                h.send({ type: 'pty.output', data: btoa('INJECTED WITHOUT VALID KEY') });
                h.send({ type: 'pty.preview', data: btoa('{"html":"INJECTED WITHOUT VALID KEY"}') });
                h.S.term.focus();
                await h.settle();
            }, publicKey);
            const retry = page.getByRole('button', { name: 'retry', exact: true });
            await retry.waitFor({ state: 'visible' });
            assert.match(await page.locator('#reconnect-text').innerText(), /Can't decrypt output.*Cannot secure terminal/);
            assert.doesNotMatch(await page.evaluate(() => h.text()), /INJECTED/);
            assert.deepEqual(await page.evaluate(() => h.previews), []);
            await page.keyboard.type('must stay private');
            await page.keyboard.press('Enter');
            await page.evaluate(() => h.input('direct private input'));
            assert.deepEqual(await page.evaluate(() => h.sent.filter(msg => msg.type === 'pty.input')), []);
            assert.deepEqual(await page.evaluate(() => ({ key: h.S.e2eKey, blocked: h.S.ptyInputBlocked,
                socket: h.S.ptyWs.readyState })), { key: null, blocked: true, socket: 3 });
            await page.screenshot({ path: path.join(out, `key-error-${reattach ? 'attach' : 'start'}-${keyCase}.png`), fullPage: true });
            await retry.click();
            await page.waitForFunction(() => h.sent.some(msg => msg.type === 'pty.attach'));
            await page.evaluate(() => h.S.term.focus());
            await page.keyboard.type('before new key');
            assert.deepEqual(await page.evaluate(() => h.sent.filter(msg => msg.type === 'pty.input')), []);
            await page.evaluate(async () => { h.start(); await h.settle(); h.S.term.focus(); });
            await page.keyboard.type('secure input');
            await page.waitForFunction(() => h.sent.some(msg => msg.type === 'pty.input'));
            assert.equal(await page.evaluate(async () => {
                const messages = h.sent.filter(msg => msg.type === 'pty.input');
                return (await Promise.all(messages.map(async msg => new TextDecoder().decode(await h.nativeDecrypt(msg.data))))).join('');
            }), 'secure input');
            await writeFile(path.join(out, `key-recovery-${reattach ? 'attach' : 'start'}-${keyCase}.json`),
                JSON.stringify(await page.evaluate(() => ({ sent: h.sent, blocked: h.S.ptyInputBlocked, keyReady: !!h.S.e2eKey })), null, 2));
        });
    }
}


for (const reattach of [false, true]) {
    for (const keyAvailable of [false, true]) {
        test(`${reattach ? 'replay' : 'live'} plaintext output is dropped with ${keyAvailable ? 'a key' : 'no key'}`, async t => {
            const page = await fixture(t, { reattach });
            await page.evaluate(async ({ keyAvailable, reattach }) => {
                const injection = { type: 'pty.output', data: btoa('INJECTED TERMINAL OUTPUT') };
                if (reattach) h.send(injection);
                h.start(); await h.settle();
                if (!keyAvailable) h.S.e2eKey = null;
                if (!reattach || !keyAvailable) h.send(injection);
            }, { keyAvailable, reattach });
            const notice = page.getByRole('dialog', { name: 'Output incomplete' });
            await notice.waitFor({ state: 'visible' });
            assert.match(await notice.innerText(), /Can't decrypt output/);
            assert.doesNotMatch(await page.evaluate(() => h.text()), /INJECTED/);
            await page.screenshot({ path: path.join(out, `plaintext-${reattach ? 'replay' : 'live'}-${keyAvailable ? 'key' : 'keyless'}.png`), fullPage: true });
            await page.evaluate(async () => {
                h.S.e2eKey = h.key;
                h.send(await h.frame('Authenticated output still renders\r\n'));
            });
            await page.waitForFunction(() => h.text().includes('Authenticated output still renders'));
        });
    }
}

for (const transport of ['relay', 'p2p']) {
    for (const keyAvailable of [false, true]) {
        test(`${transport} rejects plaintext preview with ${keyAvailable ? 'a key' : 'no key'}`, async t => {
            const page = await fixture(t, { reattach: false });
            await page.evaluate(async ({ transport, keyAvailable }) => {
                h.start(); await h.settle();
                if (!keyAvailable) h.S.e2eKey = null;
                const message = { type: 'pty.preview', data: btoa(JSON.stringify({ html: 'INJECTED PREVIEW' })) };
                (transport === 'p2p' ? h.p2p : h.send)(message);
            }, { transport, keyAvailable });
            const notice = page.getByRole('dialog', { name: 'Output incomplete' });
            await notice.waitFor({ state: 'visible' });
            assert.match(await notice.innerText(), /Can't decrypt output/);
            assert.deepEqual(await page.evaluate(() => h.previews), []);
            await page.screenshot({ path: path.join(out, `plaintext-preview-${transport}-${keyAvailable ? 'key' : 'keyless'}.png`), fullPage: true });
            await page.evaluate(async transport => {
                h.S.e2eKey = h.key;
                const frame = await h.frame(JSON.stringify({ html: 'Authenticated preview' }));
                (transport === 'p2p' ? h.p2p : h.send)({ ...frame, type: 'pty.preview' });
            }, transport);
            await page.waitForFunction(() => h.previews.length === 1);
            assert.deepEqual(await page.evaluate(() => h.previews), [{ html: 'Authenticated preview' }]);
        });
    }
}

test('plaintext preview during key setup is dropped and reported', async t => {
    const page = await fixture(t, { reattach: false });
    await page.evaluate(async () => {
        h.holdKey(); h.start();
        h.send({ type: 'pty.preview', data: btoa('{"html":"INJECTED BEFORE KEY"}') });
        await h.settle();
    });
    assert.deepEqual(await page.evaluate(() => h.previews), []);
    assert.match(await page.getByRole('dialog').innerText(), /Can't decrypt output/);
    await page.evaluate(() => h.resumeKey());
});


for (const transport of ['relay', 'p2p']) {
    for (const failure of ['invalid JSON', 'handler exception']) {
        test(`${transport} authenticated preview with ${failure} logs and skips without locking input`, async t => {
            const page = await fixture(t, { reattach: false });
            const logged = page.waitForEvent('console', {
                predicate: message => message.type() === 'error' && message.text().startsWith('preview '),
            });
            await page.evaluate(async ({ transport, failure }) => {
                h.start(); await h.settle();
                h.previewError = failure === 'handler exception';
                const bad = await h.frame(failure === 'invalid JSON' ? '{invalid' : JSON.stringify({ html: 'Rejected preview' }));
                (transport === 'p2p' ? h.p2p : h.send)({ ...bad, type: 'pty.preview' });
            }, { transport, failure });
            assert.match((await logged).text(), failure === 'invalid JSON' ? /SyntaxError/ : /preview rendering failed/);
            assert.equal(await page.getByRole('dialog').count(), 0);
            assert.equal(await page.locator('#terminal-container').evaluate(el => el.inert), false);
            assert.deepEqual(await page.evaluate(() => h.previews), []);
            await page.evaluate(async transport => {
                h.previewError = false;
                const good = await h.frame(JSON.stringify({ html: 'Following preview' }));
                (transport === 'p2p' ? h.p2p : h.send)({ ...good, type: 'pty.preview' });
                h.send(await h.frame('Following terminal output\r\n'));
                h.S.term.focus();
            }, transport);
            await page.waitForFunction(() => h.previews.length === 1 && h.text() === 'Following terminal output');
            assert.deepEqual(await page.evaluate(() => h.previews), [{ html: 'Following preview' }]);
            await page.keyboard.type('still usable');
            await page.waitForFunction(() => h.sent.some(msg => msg.type === 'pty.input'));
            assert.equal(await page.evaluate(async () => {
                const messages = h.sent.filter(msg => msg.type === 'pty.input');
                return (await Promise.all(messages.map(async msg => new TextDecoder().decode(await h.nativeDecrypt(msg.data))))).join('');
            }), 'still usable');
            await page.screenshot({ path: path.join(out, `preview-error-${transport}-${failure.replaceAll(' ', '-')}.png`), fullPage: true });
        });
    }
}

for (const reattach of [false, true]) {
    test(`${reattach ? 'replay' : 'live'} decryption notice upgrades decode wording and survives later decode failures`, async t => {
        const page = await fixture(t, { reattach });
        await page.evaluate(async reattach => {
            if (reattach) h.holdKey();
            h.start();
            if (!reattach) await h.settle();
            h.releaseAuthentication = h.holdDecode('%%%');
            const later = await h.frame('later corrupt gzip\r\n', true, true);
            h.releaseLater = h.holdDecode(later.data);
            h.send(await h.frame('first corrupt gzip\r\n', true, true));
            h.send({ type: 'pty.output', data: '%%%' });
            h.send(later);
            h.send(await h.frame('Remaining output\r\n'));
            if (reattach) h.resumeKey();
        }, reattach);
        const notice = page.getByRole('dialog', { name: 'Output incomplete' });
        await notice.waitFor({ state: 'visible' });
        assert.match(await notice.innerText(), /Some terminal output could not be decoded/);
        await page.evaluate(() => h.releaseAuthentication());
        await page.waitForFunction(() => document.querySelector('#pty-output-notice p')?.textContent.includes("Can't decrypt output"));
        await page.evaluate(async () => { h.releaseLater(); await h.settle(); });
        await page.waitForFunction(() => h.text().includes('Remaining output'));
        assert.match(await notice.innerText(), /Can't decrypt output/);
        assert.doesNotMatch(await notice.innerText(), /could not be decoded/);
        assert.equal(await page.locator('#terminal-container').evaluate(el => el.inert), true);
        await page.evaluate(() => h.S.term.focus());
        await page.keyboard.type('blocked input');
        await page.evaluate(() => h.settle());
        assert.deepEqual(await page.evaluate(() => h.sent.filter(msg => msg.type === 'pty.input')), []);
        await page.screenshot({ path: path.join(out, `sticky-decryption-${reattach ? 'replay' : 'live'}.png`), fullPage: true });
        await notice.getByRole('button', { name: 'Reload', exact: true }).click();
        await page.waitForFunction(() => h.sent.some(msg => msg.type === 'pty.attach'));
        await page.evaluate(async () => {
            h.holdKey(); h.start();
            h.send(await h.frame('corrupt reload\r\n', true, true));
            h.send(await h.frame('Partial recovery\r\n'));
            h.resumeKey();
        });
        await page.waitForFunction(() => h.text() === 'Partial recovery');
        assert.match(await notice.innerText(), /Can't decrypt output/);
        assert.equal(await page.locator('#terminal-container').evaluate(el => el.inert), true);
        await notice.getByRole('button', { name: 'Reload', exact: true }).click();
        await page.waitForFunction(() => h.sent.filter(msg => msg.type === 'pty.attach').length === 2);
        await page.evaluate(async () => { h.start(); h.send(await h.frame('Recovered output\r\n')); });
        await page.waitForFunction(() => h.text() === 'Recovered output');
        assert.equal(await notice.count(), 0);
        assert.equal(await page.locator('#terminal-container').evaluate(el => el.inert), false);
    });
}
