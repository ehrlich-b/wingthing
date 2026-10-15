// Exercise the production PTY handlers in Chromium with deterministic async gates.
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { createServer } from 'node:http';
import { once } from 'node:events';
import { after, before, test } from 'node:test';

const source = await readFile(new URL('../../web/src/pty.js', import.meta.url), 'utf8');
const dependencies = new Map();
for (const match of source.matchAll(/import \{([^}]+)\} from '([^']+)';/g)) {
    const name = new URL(match[2], 'http://local/src/pty.js').pathname;
    const exports = dependencies.get(name) || new Set();
    match[1].split(',').forEach(value => exports.add(value.trim()));
    dependencies.set(name, exports);
}
const server = createServer((req, res) => {
    res.setHeader('Content-Type', 'text/javascript');
    if (req.url === '/') {
        res.setHeader('Content-Type', 'text/html');
        res.end('<!doctype html><title>Local PTY ordering fixture</title>');
    } else if (req.url === '/src/pty.js') {
        // Only expose the private entry point; keep its implementation intact.
        res.end(source + '\nexport { setupPTYHandlers };\n');
    } else if (dependencies.has(req.url)) {
        res.end([...dependencies.get(req.url)].map(name =>
            `export const ${name} = window.fixture[${JSON.stringify(name)}] || (() => {});`
        ).join('\n'));
    } else {
        res.writeHead(404).end();
    }
});
let browser, base;
before(async () => {
    server.listen(0, '127.0.0.1');
    await once(server, 'listening');
    base = `http://127.0.0.1:${server.address().port}`;
    const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright');
    browser = await chromium.launch({
        headless: true, executablePath: process.env.CHROMIUM_PATH || undefined,
        args: ['--disable-background-networking', '--renderer-process-limit=2'],
    });
});
after(async () => {
    await browser?.close();
    await new Promise(resolve => server.close(resolve));
});

async function fixture(t, options = {}) {
    const page = await browser.newPage();
    t.after(() => page.close());
    await page.route('**/*', route => new URL(route.request().url()).origin === base ? route.continue() : route.abort());
    await page.goto(base);
    await page.evaluate(async options => {
        const noop = () => {};
        const encoder = new TextEncoder();
        const output = [], errors = [], started = [], gates = new Map();
        const deferred = () => {
            let resolve;
            const promise = new Promise(done => { resolve = done; });
            return { promise, resolve };
        };
        const hold = name => gates.set(name, deferred());
        const wait = name => gates.get(name)?.promise;
        const release = name => gates.get(name)?.resolve();
        const settle = async () => { for (let i = 0; i < 6; i++) await new Promise(done => setTimeout(done, 0)); };
        const S = {
            ptyWingId: 'wing-a', ptySessionId: 'session-a', wingsData: [], sessionsData: [],
            currentUser: null, fitAddon: { fit: noop },
            term: {
                cols: 80, rows: 24, reset: () => { output.length = 0; }, clear: noop,
                focus: noop, onResize: () => ({ dispose: noop }),
                write: (bytes, callback) => {
                    output.push(...bytes);
                    if (callback) Promise.resolve(wait('terminal-write')).then(callback);
                },
            },
        };
        const element = () => ({ style: {}, dataset: {}, classList: { add: noop }, querySelector: () => null });
        const DOM = new Proxy({}, { get: (target, name) => target[name] ||= element() });
        document.getElementById = () => element();
        console.error = (...args) => errors.push(args.map(String).join(' '));
        const NativeDecompressionStream = window.DecompressionStream;
        window.DecompressionStream = class {
            constructor(format) {
                const native = new NativeDecompressionStream(format);
                this.writable = native.writable;
                this.readable = native.readable.pipeThrough(new TransformStream({
                    async transform(chunk, controller) {
                        started.push('gunzip');
                        await wait('gunzip');
                        controller.enqueue(chunk);
                    },
                }));
            }
        };
        window.fixture = {
            S, DOM, identityPubKey: '', resumeAckMatches: () => true,
            sessionRoute: () => '#terminal', findSessionResource: () => null,
            terminalReferenceMatches: (s, session, wing, socket) => s.ptySessionId === session && s.ptyWingId === wing && s.ptyWs === socket,
            deriveE2EKey: async () => { await wait('key'); return {}; },
            e2eDecrypt: async data => {
                if (typeof data !== 'string') throw Error('ciphertext must be a string');
                started.push(data);
                await wait(data);
                return Uint8Array.from(atob(data), c => c.charCodeAt(0));
            },
        };
        const { setupPTYHandlers } = await import('/src/pty.js');
        const send = message => S.ptyWs.onmessage({ data: JSON.stringify({ session_id: S.ptySessionId, ...message }) });
        const attach = (reattach = false, session = 'session-a') => {
            S.ptySessionId = session;
            S.ptyWs = { readyState: WebSocket.OPEN, send: noop, close: noop };
            setupPTYHandlers(S.ptyWs, reattach, session);
        };
        const start = (encrypted = true) => send({ type: 'pty.started', public_key: encrypted ? 'fixture-key' : undefined });
        const frame = async (text, compressed = false) => {
            let bytes = encoder.encode(text);
            if (compressed) {
                const stream = new Blob([bytes]).stream().pipeThrough(new CompressionStream('gzip'));
                bytes = new Uint8Array(await new Response(stream).arrayBuffer());
            }
            return { type: 'pty.output', data: btoa(String.fromCharCode(...bytes)), compressed };
        };
        window.harness = { S, output, errors, started, hold, release, settle, send, attach, start, frame };
        attach(options.reattach);
    }, options);
    return page;
}

async function check(page, expected) {
    await page.waitForFunction(length => harness.output.length >= length || harness.errors.length, new TextEncoder().encode(expected).length, { timeout: 3000 }).catch(() => {});
    const actual = await page.evaluate(() => ({ bytes: harness.output, errors: harness.errors }));
    assert.deepEqual(actual, { bytes: [...new TextEncoder().encode(expected)], errors: [] });
}

test('live output keeps wire order when the first decrypt is delayed', async t => {
    const page = await fixture(t);
    await page.evaluate(async () => {
        const h = harness;
        h.start(); await h.settle();
        const first = await h.frame('first\r\n'), second = await h.frame('second\r\n');
        h.hold(first.data); h.send(first); h.send(second);
        await h.settle(); h.release(first.data);
    });
    await check(page, 'first\r\nsecond\r\n');
});

test('live output waits for gzip before rendering the next frame', async t => {
    const page = await fixture(t);
    await page.evaluate(async () => {
        const h = harness;
        h.start(); await h.settle(); h.hold('gunzip');
        h.send(await h.frame('compressed\r\n', true));
        h.send(await h.frame('plain\r\n'));
    });
    await page.waitForFunction(() => harness.started.includes('gunzip'));
    await page.evaluate(async () => { await harness.settle(); harness.release('gunzip'); });
    await check(page, 'compressed\r\nplain\r\n');
});

test('replay drains mixed queued frames before subsequent live output', async t => {
    const page = await fixture(t, { reattach: true });
    await page.evaluate(async () => {
        const h = harness;
        h.hold('key'); h.hold('terminal-write'); h.start();
        h.send(await h.frame('replay one\r\n', true));
        h.send(await h.frame('replay two\r\n'));
        h.release('key');
    });
    await page.waitForFunction(() => harness.output.length > 0);
    await page.evaluate(async () => {
        const h = harness, first = await h.frame('queued compressed\r\n', true);
        h.hold(first.data); h.send(first); h.send(await h.frame('queued plain\r\n'));
        h.release('terminal-write'); await h.settle();
        h.send(await h.frame('live \u03bb\r\n')); h.release(first.data);
    });
    await check(page, 'replay one\r\nreplay two\r\nqueued compressed\r\nqueued plain\r\nlive \u03bb\r\n');
});

test('new session drains frames queued while deriving the key', async t => {
    const page = await fixture(t);
    await page.evaluate(async () => {
        const h = harness;
        h.hold('key'); h.start();
        h.send(await h.frame('queued compressed\r\n', true));
        h.send(await h.frame('queued plain\r\n'));
        h.release('key'); await h.settle(); h.send(await h.frame('live\r\n'));
    });
    await check(page, 'queued compressed\r\nqueued plain\r\nlive\r\n');
});

test('unkeyed replay and startup frames drain on pty.started', async t => {
    const page = await fixture(t, { reattach: true });
    await page.evaluate(async () => {
        const h = harness;
        h.send(await h.frame('replay\r\n', true)); h.start(false);
        await h.settle(); h.send(await h.frame('live\r\n'));
    });
    await check(page, 'replay\r\nlive\r\n');
});

for (const phase of ['decrypt', 'gunzip', 'replay-write', 'key']) {
    test(`socket replacement discards old ${phase} work and its queued frames`, async t => {
        const page = await fixture(t, { reattach: phase === 'replay-write' });
        await page.evaluate(async phase => {
            const h = harness;
            if (phase === 'key') h.hold('key');
            if (phase === 'replay-write') { h.hold('key'); h.hold('terminal-write'); }
            h.start(); await h.settle();
            const old = await h.frame('old\r\n', phase === 'gunzip');
            if (phase === 'decrypt') h.hold(old.data);
            if (phase === 'gunzip') h.hold('gunzip');
            h.send(old); h.send(await h.frame('old queued\r\n'));
            if (phase === 'replay-write') h.release('key');
            window.oldFrame = old.data;
        }, phase);
        if (phase === 'gunzip') await page.waitForFunction(() => harness.started.includes('gunzip'));
        if (phase === 'replay-write') await page.waitForFunction(() => harness.output.length > 0);
        await page.evaluate(async () => {
            const h = harness;
            h.attach(false, 'session-b'); h.output.length = 0; h.start(false);
            h.send(await h.frame('new\r\n')); await h.settle();
            h.release(oldFrame); h.release('gunzip'); h.release('key'); h.release('terminal-write');
            await h.settle();
        });
        await check(page, 'new\r\n');
    });
}

test('session replacement on the same socket discards pending decrypts', async t => {
    const page = await fixture(t);
    await page.evaluate(async () => {
        const h = harness;
        h.start(); await h.settle();
        const old = await h.frame('old session\r\n'); h.hold(old.data); h.send(old);
        h.S.ptySessionId = 'session-b';
        h.release(old.data); await h.settle();
    });
    const actual = await page.evaluate(() => ({ bytes: harness.output, errors: harness.errors }));
    assert.deepEqual(actual, { bytes: [], errors: [] });
});

test('same-socket replacement starts a fresh queue before old decrypt completes', async t => {
    const page = await fixture(t);
    await page.evaluate(async () => {
        const h = harness;
        h.start(); await h.settle();
        const old = await h.frame('old session\r\n');
        h.hold(old.data); h.send(old); h.send(await h.frame('old queued\r\n'));
        await h.settle();
        window.oldFrame = old.data;
        h.S.ptySessionId = 'session-b'; h.start(false);
        h.send(await h.frame('new session\r\n'));
    });
    await check(page, 'new session\r\n');
    await page.evaluate(async () => { harness.release(oldFrame); await harness.settle(); });
    await check(page, 'new session\r\n');
    assert.equal(await page.evaluate(() => harness.started.includes(btoa('old queued\r\n'))), false);
});

test('session-routed attachment works before a wing identity is known', async t => {
    const page = await fixture(t);
    await page.evaluate(async () => {
        const h = harness;
        h.S.ptyWingId = null; h.attach(); h.start(false);
        h.send(await h.frame('session route\r\n'));
    });
    await check(page, 'session route\r\n');
});
