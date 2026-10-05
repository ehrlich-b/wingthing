import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';
import { createCanvasSessionState } from '../src/canvas-session-state.js';
import { sessionResourceKey } from '../src/session-reference.js';
import { requestCanvasStop } from '../src/canvas-stop.js';

function element() {
    const el = {
        className: '', style: {}, dataset: {}, children: [], listeners: {}, textContent: '',
        setAttribute() {},
        addEventListener(type, callback) { this.listeners[type] = callback; },
        appendChild(child) { child.parentNode = this; this.children.push(child); },
        removeChild(child) { this.children = this.children.filter(item => item !== child); child.parentNode = null; },
        querySelector(selector) { return this.children.find(child => child.classList.contains(selector.slice(1))) || this.children.map(child => child.querySelector(selector)).find(Boolean) || null; },
    };
    el.classList = {
        contains: name => el.className.split(' ').includes(name),
        add(...names) { el.className = [...new Set([...el.className.split(' '), ...names])].join(' '); },
        remove(...names) { el.className = el.className.split(' ').filter(name => !names.includes(name)).join(' '); },
    };
    return el;
}

function canvasHarness() {
    class Terminal {
        cols = 80; rows = 24; lines = []; disposed = false;
        loadAddon() {} open() {} focus() {} onData() {} onResize() {} attachCustomKeyEventHandler() {}
        writeln(text) { this.lines.push(text); }
        dispose() { this.disposed = true; }
    }
    class Addon { fit() {} }
    class WebSocket {
        static OPEN = 1;
        readyState = 1; sent = []; closed = false;
        send(data) { this.sent.push(JSON.parse(data)); }
        close() { this.closed = true; this.readyState = 3; if (this.onclose) this.onclose(); }
        receive(msg) { this.onmessage({ data: JSON.stringify(msg) }); }
    }
    const requests = [], cleared = [], deleted = [];
    const context = vm.createContext({
        Terminal, FitAddon: Addon, SerializeAddon: Addon, WebSocket,
        createCanvasSessionState, sessionResourceKey, requestCanvasStop,
        S: { wingsData: [], tunnelAuthTokens: {}, currentUser: { release_channel: 'preview' } },
        DOM: { canvasWorld: element() },
        document: { createElement: element },
        location: { protocol: 'http:', host: 'canvas.test' },
        localStorage: { setItem() {} },
        window: { _deleteSession: (...args) => deleted.push(args) },
        setTimeout() { return 1; }, clearTimeout() {},
        randomUUID: () => 'temporary', identityPubKey: 'browser-key',
        clearNotification: (...args) => cleared.push(args), clearTermBuffer() {},
        terminalControlFailure: () => null,
        sendTunnelRequest: async (...args) => { requests.push(args); return { ok: true }; },
        ptyResizeRequest: session_id => ({ type: 'pty.resize', session_id }),
    });
    // Execute the real canvas module with its imports supplied by the fake
    // browser. No xterm, network, DOM package, or real timers are needed.
    const source = readFileSync(new URL('../src/canvas.js', import.meta.url), 'utf8')
        .replace(/^import .*;\n/gm, '').replace(/^export /gm, '');
    vm.runInContext(source, context, { filename: 'canvas.js' });
    const sess = context.canvasConnect('claude', '/project', 'mac', 0, 0);
    sess.ws.onopen();
    const dismiss = () => sess.closeBtn.listeners.click({ stopPropagation() {}, preventDefault() {} });
    return { context, sess, requests, cleared, deleted, dismiss };
}

test('a launching socket accepts assigned-ID failures and its failed tile is dismissible', () => {
    for (const frame of [
        { type: 'pty.exited', session_id: 'assigned', exit_code: 1, error: 'agent executable missing' },
        { type: 'error', session_id: 'assigned', message: 'access denied' },
        { type: 'error', message: 'wing offline' },
        { type: 'pty.exited', exit_code: 1, error: 'agent executable missing' },
    ]) {
        const h = canvasHarness(), { sess } = h;
        assert.equal(sess.starting, true);
        assert.equal(sess.closeBtn.disabled, true);
        sess.ws.receive(frame);
        assert.equal(sess.starting, false);
        assert.equal(sess.dead, true);
        assert.equal(sess.closeBtn.disabled, false);
        assert.match(sess.term.lines.join('\n'), new RegExp(frame.error || frame.message));
        assert.doesNotMatch(sess.stopNotice.textContent, /Launching/);
        sess.ws.receive({ type: 'pty.started', session_id: 'late-start' });
        assert.equal(sess.attached, false, 'a late frame cannot revive a failed launch');
        h.dismiss();
        assert.equal(h.context.canvasState.sessions[sess.key], undefined);
        assert.equal(sess.term.disposed, true);
        assert.equal(sess.el.parentNode, null);
        assert.deepEqual(h.requests, [], 'dismiss must never kill a temporary or rejected session ID');
        assert.deepEqual(h.deleted, [], 'dismiss must never delete an unconfirmed backend session');
    }
});

test('connection failure during canvas launch also enables local dismissal', () => {
    for (const event of ['onclose', 'onerror']) {
        const h = canvasHarness();
        h.sess.ws[event]();
        assert.equal(h.sess.starting, false);
        assert.equal(h.sess.closeBtn.disabled, false);
        h.dismiss();
        assert.equal(h.context.canvasState.sessions[h.sess.key], undefined);
        assert.deepEqual(h.requests, []);
    }
});

test('canvas launch still rejects foreign data and failures after its session is acknowledged', () => {
    const h = canvasHarness(), { sess } = h;
    sess.ws.receive({ type: 'pty.output', session_id: 'other', data: 'a' });
    assert.equal(sess.starting, true);
    sess.ws.receive({ type: 'pty.started', session_id: 'assigned', controller_id: 'controller' });
    assert.equal(sess.id, 'assigned');
    assert.equal(sess.attached, true);
    assert.equal(sess.starting, false);
    for (const type of ['pty.exited', 'error']) sess.ws.receive({ type, session_id: 'other', message: 'foreign', error: 'foreign' });
    assert.equal(sess.dead, false);
    assert.equal(sess.attached, true);
    assert.deepEqual(sess.term.lines, []);
});
