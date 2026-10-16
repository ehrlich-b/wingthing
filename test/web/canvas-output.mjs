// Loopback Chromium exercises the real canvas module, xterm and authenticated output.
import assert from 'node:assert/strict';
import { readFile, mkdir, writeFile } from 'node:fs/promises';
import { createServer } from 'node:http';
import { once } from 'node:events';
import { createRequire } from 'node:module';
import { after, before, test } from 'node:test';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('../../', import.meta.url));
const source = await readFile(path.join(root, 'web/src/canvas.js'), 'utf8');
const dependencies = new Map();
for (const match of source.matchAll(/import \{([^}]+)\} from '(\.\/[^']+)';/g)) {
    dependencies.set(new URL(match[2], 'http://local/src/canvas.js').pathname,
        match[1].split(',').map(name => name.trim()));
}
const real = new Set(['crypto.js', 'helpers.js', 'security.js', 'session-reference.js',
    'canvas-session-state.js', 'terminal-attachment.js', 'canvas-stop.js', 'output-notice-content.js']);
const server = createServer(async (req, res) => {
    try {
        res.setHeader('Content-Type', 'text/javascript');
        if (req.url === '/') {
            res.setHeader('Content-Type', 'text/html; charset=utf-8');
            res.end(`<!doctype html><meta name="viewport" content="width=device-width, initial-scale=1">
                <title>Canvas output recovery</title><link rel="stylesheet" href="/style.css">
                <link rel="stylesheet" href="/node_modules/@xterm/xterm/css/xterm.css">
                <script type="importmap">{"imports":{"@noble/":"/node_modules/@noble/",
                "@xterm/xterm":"/node_modules/@xterm/xterm/lib/xterm.mjs",
                "@xterm/addon-fit":"/node_modules/@xterm/addon-fit/lib/addon-fit.mjs",
                "@xterm/addon-serialize":"/node_modules/@xterm/addon-serialize/lib/addon-serialize.mjs"}}</script>
                <main style="height:100vh;display:flex;flex-direction:column">
                <header style="padding:16px">wingthing · Canvas</header>
                <section id="canvas-section"><div id="canvas-viewport"><div id="canvas-world"></div></div></section></main>`);
        } else if (req.url === '/src/canvas.js') {
            res.end(source + '\nexport { canvasAttach, focusSession, removeStoppedCanvasSession, canvasState };');
        } else if (req.url === '/src/crypto.js') {
            const crypto = await readFile(path.join(root, 'web/src/crypto.js'), 'utf8');
            res.end(crypto.replace('export async function deriveE2EKey(', 'async function deriveKey(') + `
                export async function deriveE2EKey(...args) {
                    await window.keyGate;
                    return deriveKey(...args);
                }`);
        } else if (req.url.startsWith('/src/') && real.has(req.url.slice(5))) {
            res.end(await readFile(path.join(root, 'web', req.url)));
        } else if (dependencies.has(req.url)) {
            res.end(dependencies.get(req.url).map(name =>
                `export const ${name} = window.fixture[${JSON.stringify(name)}] || (() => {});`).join('\n'));
        } else if (/^\/(node_modules|fonts)\/[\w@/.-]+$/.test(req.url) || req.url === '/style.css') {
            if (req.url.endsWith('.css')) res.setHeader('Content-Type', 'text/css');
            if (req.url.endsWith('.woff2')) res.setHeader('Content-Type', 'font/woff2');
            res.end(await readFile(path.join(root, 'web', req.url)));
        } else res.writeHead(404).end();
    } catch (error) { res.writeHead(500).end(String(error)); }
});
let browser, base;
const out = process.env.WT_CANVAS_OUT || path.join(root, '.scratch/canvas-output');
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

async function fixture(t, { width = 1440, launch = false, spectate = false, takeover = false } = {}) {
    const page = await browser.newPage({ viewport: { width, height: 844 } });
    page.setDefaultTimeout(5000);
    t.after(() => page.close());
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    t.after(() => assert.deepEqual(errors, [], 'no unhandled browser errors'));
    await page.route('**/*', route => new URL(route.request().url()).origin === base ? route.continue() : route.abort());
    await page.goto(base);
    await page.evaluate(async ({ width, launch, spectate, takeover }) => {
        const S = { wingsData: [{ wing_id: 'wing-a', hostname: 'Demo wing', spectate: true }],
            sessionsData: [], tunnelAuthTokens: {}, currentUser: { release_channel: 'preview' } };
        const DOM = { canvasWorld: document.getElementById('canvas-world'),
            canvasViewport: document.getElementById('canvas-viewport'), canvasSection: document.getElementById('canvas-section') };
        const sent = [], sockets = [], requests = [];
        window.fixture = { S, DOM, onlineWings: () => [], sendTunnelRequest: async (wing, msg) => {
            requests.push({ wing, ...msg }); return { ok: true };
        }, randomUUID: () => 'synthetic-start' };
        class Socket {
            static OPEN = 1; readyState = 1;
            constructor(url) { this.url = url; sockets.push(this); queueMicrotask(() => this.onopen?.()); }
            send(data) { sent.push({ socket: sockets.indexOf(this), ...JSON.parse(data) }); }
            close() { this.readyState = 3; setTimeout(() => this.onclose?.(), 0); }
        }
        window.WebSocket = Socket;
        const canvas = await import('/src/canvas.js');
        const { gcm } = await import('/node_modules/@noble/ciphers/aes.js');
        const { x25519 } = await import('/node_modules/@noble/curves/ed25519.js');
        const { bytesToB64, b64ToBytes } = await import('/src/helpers.js');
        const { deriveE2EKey } = await import('/src/crypto.js');
        const publicKey = bytesToB64(x25519.getPublicKey(x25519.utils.randomSecretKey()));
        const key = await deriveE2EKey(publicKey, 'wing-a');
        const mobile = width === 390;
        localStorage.setItem('wt_canvas_view', JSON.stringify({ x: mobile ? 15 : 0, y: mobile ? 16 : 0, scale: mobile ? 0.9 : 1 }));
        canvas.showCanvasView();
        const attach = (id = 'session-a', wing = 'wing-a', options = {}) =>
            canvas.canvasAttach(id, 'shell', wing, mobile ? 0 : 1, mobile ? 0 : 1, mobile ? 10 : 15, 16, 'Build output', options);
        let affected = launch ? canvas.canvasConnect('shell', null, 'wing-a', mobile ? 0 : 1, mobile ? 0 : 1)
            : attach('session-a', 'wing-a', { spectate, takeover });
        // Deliberately use the same wire session ID on another wing.
        const healthy = canvas.canvasAttach('session-a', 'shell', 'wing-b', mobile ? 0 : 17, mobile ? 19 : 1, mobile ? 10 : 15, 16, 'Healthy terminal');
        const focus = () => canvas.focusSession(affected.key);
        focus();
        const send = (msg, sess = affected) => sess.ws.onmessage({ data: JSON.stringify({ session_id: sess.id, ...msg }) });
        const start = (sess = affected, pub = publicKey) => send({ type: 'pty.started',
            session_id: sess.starting ? 'session-a' : sess.id, public_key: pub, controller_id: 'synthetic-controller' }, sess);
        const frame = async (text, corruption = null, compressed = !!corruption && corruption !== 'authentication' && corruption !== 'base64') => {
            let bytes = new TextEncoder().encode(text);
            if (compressed) bytes = new Uint8Array(await new Response(new Blob([bytes]).stream().pipeThrough(new CompressionStream('gzip'))).arrayBuffer());
            if (corruption === 'checksum') bytes[bytes.length - 8] ^= 255;
            if (corruption === 'truncated') bytes = bytes.slice(0, -4);
            const iv = crypto.getRandomValues(new Uint8Array(12));
            const cipher = gcm(key, iv).encrypt(bytes);
            const envelope = new Uint8Array(iv.length + cipher.length);
            envelope.set(iv); envelope.set(cipher, iv.length);
            if (corruption === 'authentication') envelope[envelope.length - 1] ^= 255;
            return { type: 'pty.output', data: corruption === 'base64' ? '%%%' : bytesToB64(envelope), compressed };
        };
        const text = (sess = affected) => Array.from({ length: sess.term.buffer.active.length },
            (_, i) => sess.term.buffer.active.getLine(i).translateToString(true)).join('\n').trim();
        const settle = async () => { for (let i = 0; i < 8; i++) await new Promise(resolve => setTimeout(resolve, 0)); };
        const NativeDecompressionStream = window.DecompressionStream;
        let decodeGate, releaseDecode, releaseKey, decodeCalls = 0;
        window.DecompressionStream = class {
            constructor(format) {
                decodeCalls++;
                const native = new NativeDecompressionStream(format);
                const gate = decodeGate;
                this.writable = native.writable;
                this.readable = new ReadableStream({ async start(controller) {
                    await gate;
                    const reader = native.readable.getReader();
                    try {
                        for (;;) {
                            const chunk = await reader.read();
                            if (chunk.done) { controller.close(); break; }
                            controller.enqueue(chunk.value);
                        }
                    } catch (error) { controller.error(error); }
                } });
            }
        };
        window.h = { S, sent, sockets, requests, healthy, send, start, frame, text, settle, focus, attach,
            focusHealthy: () => canvas.focusSession(healthy.key),
            get affected() { return affected; }, get decodeCalls() { return decodeCalls; },
            holdDecode: () => { decodeGate = new Promise(resolve => { releaseDecode = resolve; }); },
            releaseDecode: () => { releaseDecode?.(); decodeGate = null; },
            holdKey: () => { window.keyGate = new Promise(resolve => { releaseKey = resolve; }); },
            releaseKey: () => { releaseKey?.(); window.keyGate = null; },
            replace: (id = affected.id, wing = affected.wingId) => {
                canvas.removeStoppedCanvasSession(affected); affected = attach(id, wing); focus();
            },
            adoptReload: () => { affected = canvas.canvasState.find(affected.id, affected.wingId); focus(); },
            input: data => affected.term.input(data, true),
            decryptInput: data => {
                const bytes = b64ToBytes(data);
                return new TextDecoder().decode(gcm(key, bytes.slice(0, 12)).decrypt(bytes.slice(12)));
            },
            changedKey: () => bytesToB64(x25519.getPublicKey(x25519.utils.randomSecretKey())),
        };
        start(healthy); await settle();
        send(await frame('Healthy output remains available\r\n'), healthy);
    }, { width, launch, spectate, takeover });
    return page;
}

async function started(page) {
    await page.evaluate(async () => { h.start(); await h.settle(); });
    await page.waitForFunction(() => h.affected.keyReady);
}
async function corrupt(page, corruption = 'checksum') {
    await page.evaluate(async corruption => h.send(await h.frame('missing output\r\n', corruption)), corruption);
    await page.getByRole('dialog', { name: 'Output incomplete' }).waitFor({ state: 'visible' });
}
const inputs = page => page.evaluate(() => h.sent.filter(msg => msg.type === 'pty.input'));

for (const launch of [false, true]) {
    for (const corruption of ['checksum', 'authentication']) {
        test(`${launch ? 'launched' : 'attached'} background tile preserves keyboard focus after ${corruption} corruption`, async t => {
            const page = await fixture(t, { launch });
            await started(page);
            await page.evaluate(() => {
                h.focusHealthy();
                window.healthyInput = document.activeElement;
            });
            await page.keyboard.type('before');
            await page.waitForFunction(() => h.sent.filter(msg => msg.type === 'pty.input')
                .map(msg => h.decryptInput(msg.data)).join('') === 'before');
            await corrupt(page, corruption);
            // Continue through the browser's focused element without refocusing
            // xterm or invoking its input API; Enter must never activate Reload.
            await page.keyboard.type('after');
            await page.keyboard.press('Enter');
            await page.evaluate(() => h.settle());
            assert.equal(await page.evaluate(() => h.sockets.length), 2, 'Enter does not reconnect the background tile');
            assert.equal(await page.evaluate(() => h.sent.filter(msg => msg.type === 'pty.attach' && msg.wing_id === 'wing-a').length),
                launch ? 0 : 1, 'no recovery attach was sent');
            assert.equal(await page.evaluate(() => document.activeElement === window.healthyInput), true,
                'the healthy terminal retains DOM focus');
            const sent = await inputs(page);
            assert.ok(sent.every(msg => msg.socket === 1), 'only the healthy tile sent input');
            assert.equal(await page.evaluate(messages => messages.map(msg => h.decryptInput(msg.data)).join(''), sent),
                'beforeafter\r', 'all physical keystrokes reach the healthy terminal');
        });
    }
}

for (const launch of [false, true]) {
    for (const corruption of ['checksum', 'truncated', 'authentication', 'base64']) {
        test(`${launch ? 'launched' : 'attached'} tile reports ${corruption} corruption only on its own wing`, async t => {
            const page = await fixture(t, { launch });
            await started(page);
            await corrupt(page, corruption);
            assert.equal(await page.getByRole('dialog').count(), 1);
            assert.equal(await page.evaluate(() => !!h.affected.el.querySelector('dialog')), true);
            assert.equal(await page.evaluate(() => !!h.healthy.el.querySelector('dialog')), false);
            await page.evaluate(async () => {
                h.send(await h.frame('following output\r\n'));
                h.focusHealthy(); h.healthy.term.input('healthy input', true); h.focus();
            });
            await page.waitForFunction(() => h.text().includes('following output'));
            await page.evaluate(() => h.input('blocked on damaged tile'));
            const sent = await inputs(page);
            assert.equal(sent.length, 1, 'healthy tile still accepts input');
            assert.equal(sent[0].socket, 1, 'only the healthy tile sent input');
        });
    }
}

test('delayed gzip stays ordered while another tile continues independently', async t => {
    const page = await fixture(t);
    await started(page);
    await page.evaluate(async () => {
        h.holdDecode(); h.send(await h.frame('first\r\n', null, true));
        h.send(await h.frame('second\r\n'));
        h.send(await h.frame('other tile continues\r\n'), h.healthy);
    });
    await page.waitForFunction(() => h.decodeCalls === 1 && h.text(h.healthy).includes('other tile continues'));
    assert.equal(await page.evaluate(() => h.text()), '');
    assert.equal(await page.getByRole('dialog').count(), 0);
    await page.evaluate(() => h.releaseDecode());
    await page.waitForFunction(() => h.text() === 'first\nsecond');
});

for (const change of ['attachment', 'session', 'wing']) {
    for (const corruption of [null, 'checksum']) {
        test(`late ${corruption || 'successful'} decode cannot affect a replaced ${change}`, async t => {
            const page = await fixture(t);
            await started(page);
            await page.evaluate(async corruption => {
                window.oldTile = h.affected;
                h.holdDecode(); h.send(await h.frame('stale output\r\n', corruption, true));
            }, corruption);
            await page.waitForFunction(() => h.decodeCalls === 1);
            await page.evaluate(async change => {
                h.replace(change === 'session' ? 'session-b' : 'session-a', change === 'wing' ? 'wing-c' : 'wing-a');
                h.start(); await h.settle();
                h.send(await h.frame('late socket event\r\n', 'authentication'), window.oldTile);
                h.releaseDecode();
                h.send(await h.frame('replacement output\r\n'));
            }, change);
            await page.waitForFunction(() => h.text() === 'replacement output');
            assert.equal(await page.getByRole('dialog').count(), 0);
        });
    }
}

for (const width of [390, 1440]) {
    test(`Reload at ${width}px uses the same session without resending input or repeating takeover`, async t => {
        const page = await fixture(t, { width, takeover: true });
        await started(page);
        await page.evaluate(() => h.input('original input\r'));
        await page.waitForFunction(() => h.sent.some(msg => msg.type === 'pty.input'));
        const original = await inputs(page);
        assert.equal(await page.evaluate(data => h.decryptInput(data), original[0].data), 'original input\r');
        await page.evaluate(async () => h.send(await h.frame('Build started\r\nRetained output before the missing chunk\r\n')));
        await corrupt(page);
        await page.screenshot({ path: path.join(out, `notice-${width}.png`), fullPage: true });
        await page.evaluate(() => { window.oldReload = h.affected.el.querySelector('dialog button'); h.holdKey(); });
        await page.getByRole('button', { name: 'Reload', exact: true }).click();
        await page.evaluate(() => { h.adoptReload(); window.oldReload.click(); window.beforeKeyRequests = h.requests.length; });
        assert.equal(await page.getByRole('dialog').count(), 0);
        assert.equal(await page.evaluate(() => h.sockets.length), 3);
        await page.evaluate(() => h.affected.term.focus());
        await page.keyboard.type('before acknowledgment');
        await page.keyboard.press('Enter');
        await page.evaluate(async () => { h.start(); h.send(await h.frame('restored snapshot\r\n')); await h.settle(); });
        await page.keyboard.type('during key setup');
        await page.keyboard.press('Enter');
        await page.evaluate(() => h.input('direct early input'));
        assert.deepEqual(await inputs(page), original);
        assert.equal(await page.evaluate(() => h.requests.length), await page.evaluate(() => window.beforeKeyRequests), 'no resize before key setup');
        await page.evaluate(() => h.releaseKey());
        await page.waitForFunction(() => h.text() === 'restored snapshot');
        assert.deepEqual(await inputs(page), original);
        const attaches = await page.evaluate(() => h.sent.filter(msg => msg.type === 'pty.attach' && msg.wing_id === 'wing-a'));
        assert.equal(attaches.length, 2);
        assert.equal(attaches[0].takeover, true);
        assert.equal(!!attaches[1].takeover, false);
        assert.equal(attaches[1].session_id, 'session-a');
        assert.equal(attaches[1].wing_id, 'wing-a');
        await page.evaluate(() => h.input('new input'));
        await page.waitForFunction(() => h.sent.filter(msg => msg.type === 'pty.input').length === 2);
        await writeFile(path.join(out, `recovery-${width}.json`), JSON.stringify(await page.evaluate(() => ({
            sent: h.sent, requests: h.requests, text: h.text(), keyReady: h.affected.keyReady,
        })), null, 2));
    });
}

test('Reload preserves observation mode and never requests input control', async t => {
    const page = await fixture(t, { spectate: true });
    await started(page); await corrupt(page, 'authentication');
    await page.getByRole('button', { name: 'Reload', exact: true }).click();
    await page.evaluate(() => h.adoptReload());
    await started(page);
    await page.evaluate(() => h.input('observer input'));
    assert.deepEqual(await inputs(page), []);
    const messages = await page.evaluate(() => h.sent.filter(msg => msg.type === 'pty.attach' && msg.wing_id === 'wing-a'));
    assert.deepEqual(messages.map(msg => ({ spectate: msg.spectate, takeover: !!msg.takeover })),
        [{ spectate: true, takeover: false }, { spectate: true, takeover: false }]);
    assert.equal(await page.evaluate(() => h.requests.filter(msg => msg.wing === 'wing-a').length), 0);
});

test('Reload rejects a changed wing identity without plaintext input', async t => {
    const page = await fixture(t);
    await started(page); await corrupt(page);
    await page.getByRole('button', { name: 'Reload', exact: true }).click();
    await page.evaluate(async () => { h.adoptReload(); h.start(h.affected, h.changedKey()); await h.settle(); });
    await page.waitForFunction(() => h.text().includes('wing identity changed'));
    await page.evaluate(() => h.input('must stay private'));
    assert.deepEqual(await inputs(page), []);
    assert.equal(await page.evaluate(() => h.affected.keyReady), false);
    assert.equal(await page.evaluate(() => h.affected.ws.readyState), 3);
});

for (const launch of [false, true]) {
    test(`a replaced ${launch ? 'launch' : 'attachment'} ignores delayed key setup`, async t => {
        const page = await fixture(t, { launch });
        await page.evaluate(async () => {
            h.holdKey(); h.start();
            h.send(await h.frame('old corrupt replay\r\n', 'authentication'));
            h.replace('session-b'); h.start();
            h.send(await h.frame('new replay\r\n'));
            h.releaseKey();
        });
        await page.waitForFunction(() => h.text() === 'new replay');
        assert.equal(await page.getByRole('dialog').count(), 0);
    });
}

for (const width of [390, 1440]) {
    for (const phase of ['notice', 'decode', 'key setup']) {
        test(`exit during ${phase} at ${width}px keeps incomplete output scrollable and selectable`, async t => {
            const page = await fixture(t, { width });
            if (phase === 'key setup') await page.evaluate(() => h.holdKey());
            await page.evaluate(async phase => {
                h.start(); await h.settle();
                h.send(await h.frame(Array.from({ length: 90 }, (_, i) => `retained line ${String(i).padStart(2, '0')}\r\n`).join('')));
                if (phase === 'decode') h.holdDecode();
                h.send(await h.frame('missing final output\r\n', phase === 'key setup' ? 'authentication' : 'checksum'));
            }, phase);
            if (phase === 'notice') await page.getByRole('dialog').waitFor({ state: 'visible' });
            if (phase === 'decode') await page.waitForFunction(() => h.decodeCalls === 1);
            await page.evaluate(phase => {
                window.oldReload = h.affected.el.querySelector('dialog button');
                h.send({ type: 'pty.exited' });
                if (phase === 'decode') h.releaseDecode();
                if (phase === 'key setup') h.releaseKey();
            }, phase);
            await page.waitForFunction(() => h.text().includes('--- session ended ---'));
            const notice = page.getByRole('dialog', { name: 'Output incomplete' });
            assert.equal(await notice.isVisible(), true);
            assert.match(await notice.innerText(), /The session has ended\./);
            assert.equal(await notice.getByRole('button').count(), 0);
            assert.equal(await page.evaluate(() => h.text().endsWith('--- session ended ---')), true);
            assert.equal(await page.evaluate(() => h.healthy.el.querySelectorAll('dialog').length), 0);
            await page.evaluate(() => h.focus());
            await page.waitForFunction(() => h.affected.term.buffer.active.viewportY > 0);
            const before = await page.evaluate(() => h.affected.term.buffer.active.viewportY);
            const screen = await page.locator('[data-wing-id="wing-a"] .xterm-screen').boundingBox();
            await page.mouse.move(screen.x + screen.width / 2, screen.y + screen.height / 2);
            await page.mouse.wheel(0, -500);
            await page.waitForFunction(before => h.affected.term.buffer.active.viewportY < before, before);
            const line = await page.evaluate(() => h.affected.term.buffer.active.getLine(h.affected.term.buffer.active.viewportY).translateToString(true));
            assert.match(line, /^retained line \d{2}$/);
            const rows = await page.evaluate(() => h.affected.term.rows);
            const y = screen.y + screen.height / rows / 2;
            await page.mouse.click(screen.x + 2, y, { clickCount: 3 });
            assert.equal(await page.evaluate(() => h.affected.term.getSelection().trim()), line);
            if (phase === 'notice') await page.screenshot({ path: path.join(out, `ended-${width}.png`), fullPage: true });
            const beforeInput = await page.evaluate(() => ({ sent: h.sent, sockets: h.sockets.length }));
            await page.evaluate(() => {
                h.input('after exit'); window.oldReload?.click();
                h.send({ type: 'pty.started', public_key: 'late stale start' });
            });
            await page.keyboard.type('typed after exit'); await page.keyboard.press('Enter');
            assert.deepEqual(await page.evaluate(() => ({ sent: h.sent, sockets: h.sockets.length })), beforeInput);
            await page.evaluate(() => h.replace('replacement'));
            assert.equal(await notice.count(), 0);
        });
    }
}

for (const launch of [false, true]) {
    test(`fresh ${launch ? 'launch' : 'attach'} sends no input or resize before key setup`, async t => {
        const page = await fixture(t, { launch });
        await page.evaluate(() => { h.holdKey(); h.input('before acknowledgment'); h.start(); });
        await page.evaluate(async () => { await h.settle(); h.input('during key setup'); });
        assert.deepEqual(await inputs(page), []);
        assert.equal(await page.evaluate(() => h.requests.filter(msg => msg.wing === 'wing-a').length), 0);
        await page.evaluate(() => h.releaseKey());
        await page.waitForFunction(() => h.affected.keyReady);
        await page.evaluate(() => h.input('encrypted after setup'));
        const sent = await inputs(page);
        assert.equal(sent.length, 1);
        assert.equal(await page.evaluate(data => h.decryptInput(data), sent[0].data), 'encrypted after setup');
    });
}

test('tile replacement clears its live notice and invalidates the old Reload action', async t => {
    const page = await fixture(t);
    await started(page); await corrupt(page);
    await page.evaluate(() => {
        const reload = h.affected.el.querySelector('dialog button');
        h.replace('replacement'); reload.click();
    });
    assert.equal(await page.getByRole('dialog').count(), 0);
    assert.equal(await page.evaluate(() => h.sockets.length), 3);
});

test('disconnected output can reload and a corrupt replacement gets its own notice', async t => {
    const page = await fixture(t);
    await started(page); await corrupt(page);
    await page.evaluate(() => h.affected.ws.close());
    await page.waitForFunction(() => h.affected.dead);
    assert.equal(await page.getByRole('button', { name: 'Reload', exact: true }).isVisible(), true);
    await page.getByRole('button', { name: 'Reload', exact: true }).click();
    await page.evaluate(() => h.adoptReload());
    await started(page); await corrupt(page);
    assert.equal(await page.getByRole('dialog').count(), 1);
    assert.equal(await page.evaluate(() => h.sockets.length), 3);
});
