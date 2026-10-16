// Exercise the production PTY handlers in Chromium with deterministic async gates.
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { createServer } from 'node:http';
import { once } from 'node:events';
import { createRequire } from 'node:module';
import { after, before, test } from 'node:test';

const source = await readFile(new URL('../../web/src/pty.js', import.meta.url), 'utf8');
const webrtc = await readFile(new URL('../../web/src/webrtc.js', import.meta.url), 'utf8');
const dependencies = new Map();
for (const match of (source + webrtc).matchAll(/import \{([^}]+)\} from '([^']+)';/g)) {
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
        res.end(source + '\nexport { setupPTYHandlers, ptyReconnectAttach };\n');
    } else if (req.url === '/src/webrtc.js') {
        res.end(webrtc);
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
    const { chromium } = process.env.PLAYWRIGHT_MODULE
        ? await import(process.env.PLAYWRIGHT_MODULE)
        : createRequire(new URL('../../web/package.json', import.meta.url))('playwright');
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
            ptyWingId: 'wing-a', ptySessionId: 'session-a', wingsData: [], sessionsData: [], e2eKey: {},
            currentUser: null, fitAddon: { fit: noop },
            term: {
                cols: 80, rows: 24, reset: () => { output.length = 0; }, clear: noop,
                writeln: text => output.push(...encoder.encode(text + '\r\n')),
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
            sessionResourceKey: session => JSON.stringify([session.wing_id || session.wingId, session.id]),
            sessionIsSelected: (session, id, wing) => session.id === id && session.wing_id === wing,
            sendTunnelRequest: async () => ({ sdp: 'fixture answer' }),
            terminalReferenceMatches: (s, session, wing, socket) => s.ptySessionId === session && s.ptyWingId === wing && s.ptyWs === socket,
            deriveE2EKey: async () => {
                await wait('key');
                if (options.keyFailure) throw Error('key setup failed');
                return {};
            },
            e2eDecrypt: async data => {
                if (typeof data !== 'string') throw Error('ciphertext must be a string');
                started.push(data);
                await wait(data);
                return Uint8Array.from(atob(data), c => c.charCodeAt(0));
            },
        };
        window._deleteSession = noop;
        const { setupPTYHandlers, ptyReconnectAttach } = await import('/src/pty.js');
        const send = message => S.ptyWs.onmessage({ data: JSON.stringify({ session_id: S.ptySessionId, ...message }) });
        const attach = (reattach = false, session = 'session-a') => {
            S.ptySessionId = session;
            S.ptyWs = { readyState: WebSocket.OPEN, send: noop, close: noop,
                dispatchEvent(event) { this.onmessage(event); return true; } };
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
        const sockets = [];
        let channel;
        // Keep the production DataChannel buffering, migration and dispatch handlers.
        window.RTCPeerConnection = class {
            createDataChannel() { return channel = { readyState: 'open', close: noop, send: noop }; }
            async createOffer() { return { sdp: 'fixture offer' }; }
            async setLocalDescription(offer) {
                this.localDescription = offer;
                this.iceGatheringState = 'complete';
                this.onicegatheringstatechange();
            }
            async setRemoteDescription() {}
            close() {}
        };
        const { initWebRTC } = await import('/src/webrtc.js');
        const dc = message => channel.onmessage({ data: JSON.stringify({ session_id: S.ptySessionId, ...message }) });
        const migrate = async messages => {
            await initWebRTC(S.ptyWingId, S.ptySessionId);
            channel.onopen();
            messages.forEach(dc);
            send({ type: 'pty.migrated' });
        };
        const reconnect = () => {
            S.ptyReconnecting = true;
            window.WebSocket = class {
                static OPEN = 1;
                readyState = 1;
                constructor() { sockets.push(this); }
                send() {}
                close() {
                    this.readyState = 3;
                    setTimeout(() => this.onclose?.(), 0);
                }
            };
            ptyReconnectAttach(S.ptySessionId, 0, S.ptyWingId);
        };
        window.harness = { S, output, errors, started, hold, release, settle, send, attach, start, frame, reconnect, sockets, dc, migrate };
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

const ended = '\r\n\x1b[2m--- session ended ---\x1b[0m\r\n';
for (const transport of ['migration', 'active P2P']) {
    for (const phase of ['decrypt', 'gunzip', 'terminal-write', 'key']) {
        test(`${transport} exit drains buffered final output during ${phase}`, async t => {
            const page = await fixture(t);
            await page.evaluate(async ({ transport, phase }) => {
                const h = harness;
                if (phase === 'key') h.hold('key');
                h.start(); await h.settle();
                const first = await h.frame('final compressed λ\r\n', true);
                const messages = [first, await h.frame('final plain\r\n'),
                    { type: 'pty.exited' }, await h.frame('after exit\r\n')];
                if (phase !== 'key') h.hold(phase === 'decrypt' ? first.data : phase);
                window.exitFrame = first.data;
                if (transport === 'migration') await h.migrate(messages);
                else { await h.migrate([]); messages.forEach(h.dc); }
                await h.settle();
            }, { transport, phase });
            assert.equal(await page.evaluate(() => harness.S.ptySessionId), 'session-a');
            if (phase === 'gunzip') await page.waitForFunction(() => harness.started.includes('gunzip'));
            if (phase === 'terminal-write') await page.waitForFunction(() => harness.output.length > 0);
            await page.evaluate(() => {
                harness.release(exitFrame); harness.release('gunzip');
                harness.release('terminal-write'); harness.release('key');
            });
            await check(page, 'final compressed λ\r\nfinal plain\r\n' + ended);
            assert.deepEqual(await page.evaluate(() => [harness.S.ptySessionId, harness.S.e2eKey]), [null, null]);
        });
    }
}

for (const reattach of [false, true]) {
    test(`migration preserves ${reattach ? 'replay' : 'relay'} output before final P2P output and exit`, async t => {
        const page = await fixture(t, { reattach });
        await page.evaluate(async reattach => {
            const h = harness;
            if (reattach) h.hold('key');
            h.start(); await h.settle();
            const first = await h.frame('before migration\r\n', true);
            h.hold(first.data); h.send(first);
            await h.migrate([await h.frame('after migration\r\n'), { type: 'pty.exited' }]);
            h.release(first.data); h.release('key');
        }, reattach);
        await check(page, 'before migration\r\nafter migration\r\n' + ended);
    });

    test(`migration exit completes after ${reattach ? 'replay' : 'live'} key failure`, async t => {
        const page = await fixture(t, { reattach, keyFailure: true });
        await page.evaluate(async () => {
            const h = harness;
            h.hold('key'); h.start();
            await h.migrate([await h.frame('unreadable\r\n'), { type: 'pty.exited' }]);
            await h.settle();
        });
        assert.equal(await page.evaluate(() => harness.S.ptySessionId), 'session-a');
        await page.evaluate(() => harness.release('key'));
        await check(page, ended);
        assert.deepEqual(await page.evaluate(() => [harness.S.ptySessionId, harness.S.e2eKey]), [null, null]);
    });
}

for (const reattach of [false, true]) {
    test(`${reattach ? 'replay' : 'live'} exit completes after key failure with queued output`, async t => {
        const page = await fixture(t, { reattach, keyFailure: true });
        await page.evaluate(async () => {
            const h = harness;
            h.deleted = [];
            window._deleteSession = (...args) => h.deleted.push(args);
            h.S.ptyWs.close = function () {
                this.readyState = WebSocket.CLOSED;
                setTimeout(() => this.onclose(), 0);
            };
            h.hold('key'); h.start();
            h.send(await h.frame('unreadable output\r\n'));
            h.send({ type: 'pty.exited' });
            await h.settle();
        });
        assert.equal(await page.evaluate(() => harness.S.ptySessionId), 'session-a');
        await page.evaluate(async () => { harness.release('key'); await harness.settle(); });
        await check(page, ended);
        assert.deepEqual(await page.evaluate(() => ({
            session: harness.S.ptySessionId,
            key: harness.S.e2eKey,
            closed: harness.S.ptyWs.readyState === WebSocket.CLOSED,
            reconnecting: !!harness.S.ptyReconnecting,
            deleted: harness.deleted,
            decrypted: harness.started,
        })), {
            session: null, key: null, closed: true, reconnecting: false,
            deleted: [['session-a', true, 'wing-a']], decrypted: [],
        });
    });
}

for (const reattach of [false, true]) {
    for (const phase of ['decrypt', 'gunzip', 'terminal-write', 'key']) {
        test(`${reattach ? 'replay' : 'live'} exit drains accepted output during ${phase}`, async t => {
            const page = await fixture(t, { reattach });
            await page.evaluate(async ({ reattach, phase }) => {
                const h = harness;
                if (reattach || phase === 'key') h.hold('key');
                if (phase === 'terminal-write') h.hold('terminal-write');
                if (phase === 'gunzip') h.hold('gunzip');
                h.start(); await h.settle();
                const first = await h.frame('final compressed λ\r\n', true);
                if (phase === 'decrypt') h.hold(first.data);
                h.send(first); h.send(await h.frame('final plain\r\n'));
                window.exitFrame = first.data;
                if (reattach && phase !== 'key') h.release('key');
            }, { reattach, phase });
            if (phase === 'gunzip') await page.waitForFunction(() => harness.started.includes('gunzip'));
            if (phase === 'terminal-write') await page.waitForFunction(() => harness.output.length > 0);
            await page.evaluate(async () => {
                const h = harness;
                h.send({ type: 'pty.exited' }); await h.settle();
            });
            assert.equal(await page.evaluate(() => harness.S.ptySessionId), 'session-a');
            assert.ok(await page.evaluate(() => harness.S.e2eKey));
            await page.evaluate(async () => {
                const h = harness;
                // Frames after exit were not accepted and must not delay cleanup.
                h.send(await h.frame('after exit\r\n'));
                h.release(exitFrame); h.release('gunzip'); h.release('terminal-write'); h.release('key');
            });
            await check(page, 'final compressed λ\r\nfinal plain\r\n' + ended);
            assert.deepEqual(await page.evaluate(() => [harness.S.ptySessionId, harness.S.e2eKey]), [null, null]);
        });
    }
}

test('socket close during exit drains without reconnecting', async t => {
    const page = await fixture(t);
    await page.evaluate(async () => {
        const h = harness;
        h.start(); await h.settle(); h.hold('gunzip');
        h.send(await h.frame('last\r\n', true));
    });
    await page.waitForFunction(() => harness.started.includes('gunzip'));
    await page.evaluate(() => {
        harness.send({ type: 'pty.exited' });
        harness.S.ptyWs.onclose();
        harness.release('gunzip');
    });
    await check(page, 'last\r\n' + ended);
    assert.notEqual(await page.evaluate(() => harness.S.ptyReconnecting), true);
});

for (const phase of ['gunzip', 'terminal-write']) {
    test(`reconnected socket close preserves pending exit during ${phase}`, async t => {
        const page = await fixture(t);
        await page.evaluate(async phase => {
            const h = harness;
            h.reconnect(); h.start(); await h.settle(); h.hold(phase);
            h.send(await h.frame('final after reconnect\r\n', true));
            h.send(await h.frame('final queued\r\n'));
        }, phase);
        if (phase === 'gunzip') await page.waitForFunction(() => harness.started.includes('gunzip'));
        else await page.waitForFunction(() => harness.output.length > 0);
        await page.evaluate(() => {
            harness.send({ type: 'pty.exited' });
            harness.S.ptyWs.close();
        });
        // Keep accepted output blocked beyond the production reconnect delay.
        await new Promise(resolve => setTimeout(resolve, 1400));
        const pending = await page.evaluate(() => ({
            sockets: harness.sockets.length,
            reconnecting: harness.S.ptyReconnecting,
            session: harness.S.ptySessionId,
            hasKey: !!harness.S.e2eKey,
        }));
        await page.evaluate(phase => harness.release(phase), phase);
        await check(page, 'final after reconnect\r\nfinal queued\r\n' + ended);
        assert.deepEqual(pending, { sockets: 1, reconnecting: false, session: 'session-a', hasKey: true });
        assert.deepEqual(await page.evaluate(() => [harness.S.ptySessionId, harness.S.e2eKey]), [null, null]);
    });
}

for (const event of ['relay.restart', 'wing.offline']) {
    for (const beforeExit of [true, false]) {
        for (const phase of ['gunzip', 'terminal-write']) {
            test(`${event} ${beforeExit ? 'before' : 'after'} exit waits for ${phase} drain`, async t => {
                const page = await fixture(t);
                await page.evaluate(async phase => {
                    const h = harness;
                    h.reconnect(); h.start(); await h.settle(); h.hold(phase);
                    h.send(await h.frame('final compressed λ\r\n', true));
                    h.send(await h.frame('final queued\r\n'));
                }, phase);
                if (phase === 'gunzip') await page.waitForFunction(() => harness.started.includes('gunzip'));
                else await page.waitForFunction(() => harness.output.length > 0);
                await page.evaluate(({ event, beforeExit }) => {
                    if (beforeExit) harness.send({ type: event });
                    harness.send({ type: 'pty.exited' });
                    if (!beforeExit) harness.send({ type: event });
                    harness.S.ptyWs.close();
                }, { event, beforeExit });
                // Both an already armed timer and one armed during exit must wait.
                await new Promise(resolve => setTimeout(resolve, 1400));
                const pending = await page.evaluate(() => ({
                    sockets: harness.sockets.length,
                    session: harness.S.ptySessionId,
                    hasKey: !!harness.S.e2eKey,
                }));
                await page.evaluate(phase => harness.release(phase), phase);
                const expected = 'final compressed λ\r\nfinal queued\r\n' + ended;
                await check(page, expected);
                assert.deepEqual(pending, { sockets: 1, session: 'session-a', hasKey: true });
                assert.deepEqual(await page.evaluate(() => [harness.S.ptySessionId, harness.S.e2eKey]), [null, null]);
                // A deferred timer must recheck identity after exit cleanup.
                await new Promise(resolve => setTimeout(resolve, 1200));
                await check(page, expected);
                assert.equal(await page.evaluate(() => harness.sockets.length), 1);
            });
        }
    }

    test(`${event} still reconnects without an exit`, async t => {
        const page = await fixture(t);
        await page.evaluate(async event => {
            harness.reconnect(); harness.start(); await harness.settle();
            harness.send({ type: event });
        }, event);
        await page.waitForFunction(() => harness.sockets.length === 2);
        assert.equal(await page.evaluate(() => harness.S.ptySessionId), 'session-a');
    });
}

test('reconnected socket still retries a close without an exit', async t => {
    const page = await fixture(t);
    await page.evaluate(async () => {
        harness.reconnect(); harness.start(); await harness.settle();
        harness.S.ptyWs.close();
    });
    await page.waitForFunction(() => harness.sockets.length === 2);
    assert.equal(await page.evaluate(() => harness.S.ptySessionId), 'session-a');
});

for (const sameSocket of [false, true]) {
    test(`replacement ${sameSocket ? 'session' : 'socket'} cancels a pending exit`, async t => {
        const page = await fixture(t);
        await page.evaluate(async sameSocket => {
            const h = harness;
            h.start(); await h.settle();
            const old = await h.frame('old\r\n');
            h.hold(old.data); h.send(old); await h.settle();
            h.send({ type: 'pty.exited' });
            if (sameSocket) h.S.ptySessionId = 'session-b';
            else h.attach(false, 'session-b');
            h.start(false); h.send(await h.frame('new\r\n'));
            await h.settle(); h.release(old.data); await h.settle();
        }, sameSocket);
        await check(page, 'new\r\n');
        assert.equal(await page.evaluate(() => harness.S.ptySessionId), 'session-b');
    });
}

test('exit drains frames queued behind the replay write before the crash marker', async t => {
    const page = await fixture(t, { reattach: true });
    await page.evaluate(async () => {
        const h = harness;
        h.hold('key'); h.hold('terminal-write'); h.start();
        h.send(await h.frame('replay\r\n', true)); h.release('key');
    });
    await page.waitForFunction(() => harness.output.length > 0);
    await page.evaluate(async () => {
        const h = harness;
        h.send(await h.frame('queued compressed\r\n', true));
        h.send(await h.frame('queued plain\r\n'));
        h.send({ type: 'pty.exited', error: 'failure\ndetail' });
        await h.settle(); h.release('terminal-write');
    });
    await check(page, 'replay\r\nqueued compressed\r\nqueued plain\r\n' +
        '\r\n\x1b[31;1m--- egg crashed ---\x1b[0m\r\n' +
        '\x1b[2mfailure\r\ndetail\x1b[0m\r\n\r\n' +
        '\x1b[33mPlease report this bug: https://github.com/ehrlich-b/wingthing/issues\x1b[0m\r\n');
});

for (const reattach of [false, true]) {
    for (const started of [false, true]) {
        test(`empty ${reattach ? 'reattached' : 'new'} session exits ${started ? 'during key derivation' : 'before startup'}`, async t => {
            const page = await fixture(t, { reattach });
            await page.evaluate(async started => {
                const h = harness;
                h.hold('key');
                if (started) h.start();
                h.send({ type: 'pty.exited' });
                await h.settle(); h.release('key'); await h.settle();
            }, started);
            await check(page, ended);
            assert.deepEqual(await page.evaluate(() => [harness.S.ptySessionId, harness.S.e2eKey]), [null, null]);
        });
    }
}
