import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import { sessionIsSelected } from '../src/session-inventory.js';
import { sessionResourceKey } from '../src/session-reference.js';
import { terminalControlFailure } from '../src/terminal-attachment.js';

// Exercise the installed reconnect wrapper without requiring a DOM or network.
const src = fs.readFileSync(new URL('../src/pty.js', import.meta.url), 'utf8');
const body = src.slice(src.indexOf('    var origMsg = innerWs.onmessage;'),
    src.indexOf('\n}\n\nexport function retryReconnect'));

for (const channel of ['stable', 'preview']) {
    test(`recovered ${channel} socket displays input refusal without reconnecting`, () => {
        const displayed = [];
        let closed = 0;
        const normal = e => displayed.push(JSON.parse(e.data));
        const socket = { onmessage: normal, close() { closed++; } };
        const S = { ptyWs: socket, ptyReconnecting: true, currentUser: { release_channel: channel } };
        new Function('innerWs', 'S', 'sessionId', 'hideReconnectBanner', 'terminalControlFailure', body)(socket, S, 'session', () => {}, () => null);
        const send = msg => socket.onmessage({ data: JSON.stringify(msg) });
        send({ type: 'pty.started', session_id: 'session' });
        const refusal = { type: 'error', session_id: 'session', code: 'session_input_busy', message: 'session input is busy; message was not delivered' };
        send(refusal);
        assert.equal(S.ptyReconnecting, false);
        assert.equal(socket.onmessage, normal);
        assert.deepEqual(displayed[1], refusal);
        assert.equal(closed, 0);
    });
}

test('attach failure before recovery still retries', () => {
    let closed = 0, displayed = 0;
    const socket = { onmessage() { displayed++; }, close() { closed++; } };
    const S = { ptyWs: socket, ptyReconnecting: true, currentUser: { release_channel: 'stable' } };
    new Function('innerWs', 'S', 'sessionId', 'hideReconnectBanner', 'terminalControlFailure', body)(socket, S, 'session', () => {}, () => null);
    socket.onmessage({ data: JSON.stringify({ type: 'error', message: 'wing offline' }) });
    assert.equal(closed, 1);
    assert.equal(displayed, 0);
});

// Run the actual installed handler and asynchronous key derivation. The DOM
// substitute exposes both header status and the persistent inline warning.
function warningFixture(channel) {
    const element = () => ({ textContent: '', hidden: true, style: {}, dataset: {},
        classList: { add() {}, remove() {} }, querySelector() { return null; } });
    const DOM = Object.fromEntries(['ptyStatus', 'headerTitle', 'sessionCloseBtn', 'terminalCopyBtn',
        'ptyInputNotice', 'ptyInputWarning', 'ptyInputDismiss'].map(name => [name, element()]));
    const elements = new Map();
    const document = { visibilityState: 'hidden', getElementById(id) {
        if (!elements.has(id)) elements.set(id, element());
        return elements.get(id);
    } };
    let finishKey;
    const key = new Promise(done => { finishKey = done; });
    const socket = { readyState: 1, close() {}, send() {},
        dispatchEvent(event) { this.onmessage(event); } };
    const S = { ptyWs: socket, ptySessionId: 'session', ptyWingId: 'wing', sessionsData: [],
        wingsData: [], tunnelAuthTokens: {}, currentUser: { release_channel: channel },
        term: { reset() {}, focus() {}, onResize() { return { dispose() {} }; } }, fitAddon: { fit() {} } };
    const noop = () => {};
    const env = { S, DOM, document, WebSocket: { OPEN: 1 }, setInterval: () => 1,
        clearInterval: noop, setTimeout: () => 1, deriveE2EKey: () => key,
        hideReconnectBanner: noop, hideReplayOverlay: noop, cleanupSession: noop,
        clearOutputIncomplete: noop, renderTerminalControlNotice: noop,
        setPreviewSession: noop, findSessionResource: () => null, formatSessionTitle: () => 'Terminal',
        refreshSessionFilesButton: noop, hideSessionFiles: noop, renderSidebar: noop, loadHome: noop,
        currentTerminalResize: () => null, terminalControlFailure };
    const module = new Function(...Object.keys(env), src.replace(/^import .*;$/gm, '').replace(/^export /gm, '') +
        '\nreturn { setupPTYHandlers, detachPTY };')(...Object.values(env));
    module.setupPTYHandlers(socket, true, 'session');
    S.ptyReconnecting = true;
    new Function('innerWs', 'S', 'sessionId', 'hideReconnectBanner', 'terminalControlFailure', body)(
        socket, S, 'session', noop, () => null);
    const send = msg => socket.onmessage({ data: JSON.stringify(msg) });
    const warning = 'Connection lost: check terminal before retrying input.';
    const visible = () => DOM.ptyStatus.textContent === warning ||
        (!DOM.ptyInputNotice.hidden && DOM.ptyInputWarning.textContent === warning);
    return { S, DOM, send, finishKey, warning, visible, select(sessionId, wingId) {
        S.ptySessionId = sessionId;
        S.ptyWingId = wingId;
        module.setupPTYHandlers(socket, true, sessionId);
    } };
}

// Keep A's direct channel alive while the selected terminal reconnects as B.
// Only the peer boundary is substituted: production ingress, migration, dispatch,
// reconnect wrapper, terminal handler and asynchronous key setup all run.
function survivingChannel(S) {
    const source = fs.readFileSync(new URL('../src/webrtc.js', import.meta.url), 'utf8');
    let dc;
    class Peer {
        createDataChannel() {
            return dc = { readyState: 'open', sent: [], send(raw) { this.sent.push(raw); }, close() {} };
        }
        createOffer() { return new Promise(() => {}); }
        close() {}
    }
    const module = new Function('S', 'sessionIsSelected', 'sessionResourceKey', 'RTCPeerConnection',
        source.replace(/^import .*;$/gm, '').replace(/^export /gm, '') +
        '\nreturn { initWebRTC, completeMigration, sendViaDC };')(S, sessionIsSelected, sessionResourceKey, Peer);
    module.initWebRTC('wing', 'session', false);
    module.completeMigration('wing', 'session');
    return { ...module, dc };
}

for (const channel of ['stable', 'preview']) for (const timing of ['before started', 'after started']) {
    test(`queued A input refusal stays visible on reconnected B: ${channel}, ${timing}`, async () => {
        const f = warningFixture(channel);
        f.S.ptyControllerId = 'A';
        const a = survivingChannel(f.S);
        assert.equal(a.sendViaDC('session', { type: 'pty.input', session_id: 'session', data: 'A-ciphertext' }, 'wing'), true);
        assert.equal(a.dc.sent.length, 1, 'A input admitted on the original channel');
        // Reconnect cleared A's controller while awaiting B's pty.started.
        f.S.ptyControllerId = null;
        const message = 'terminal is controlled elsewhere; input was not delivered';
        const refuse = () => a.dc.onmessage({ data: JSON.stringify({ type: 'error',
            operation: 'pty.input', session_id: 'session', controller_id: 'A',
            code: 'controller_replaced', message }) });
        if (timing === 'before started') refuse();
        f.send({ type: 'pty.started', session_id: 'session', public_key: 'replacement', controller_id: 'B' });
        f.finishKey({});
        await Promise.resolve();
        await Promise.resolve();
        assert.ok(f.S.e2eKey, 'B key derived');
        assert.equal(f.S.ptyReconnecting, false);
        if (timing === 'after started') refuse();
        console.log(`${channel}, ${timing}: status=${f.DOM.ptyStatus.textContent}; warning=${f.DOM.ptyInputWarning.textContent}`);
        assert.equal(f.DOM.ptyInputNotice.hidden, false, 'A refusal must be visible after B starts');
        assert.equal(f.DOM.ptyInputWarning.textContent, message);
        assert.equal(f.DOM.ptyStatus.textContent, '🔒', 'secure header stays compact while the refusal remains visible');
        assert.equal(f.S.ptyControllerId, 'B', 'A refusal must not revoke B');
        assert.equal(f.S.ptyInputBlocked, false, 'B input remains authorized');
        f.DOM.ptyInputDismiss.onclick();
        assert.equal(f.DOM.ptyInputNotice.hidden, true, 'explicit dismissal clears the refusal');
        assert.equal(f.DOM.ptyStatus.textContent, '🔒', 'dismissal clears a duplicate header refusal');
        refuse();
        // Wrong session/wing messages and obsolete control replies remain fenced.
        a.dc.onmessage({ data: JSON.stringify({ type: 'error', operation: 'pty.input',
            session_id: 'another-session', controller_id: 'A', message: 'wrong session' }) });
        f.send({ type: 'error', session_id: 'session', controller_id: 'A',
            message: 'terminal attachment was taken over; attach again to request control' });
        assert.equal(f.DOM.ptyInputWarning.textContent, message);
        assert.equal(f.S.ptyControllerId, 'B');
        f.select('session', 'another-wing');
        a.dc.onmessage({ data: JSON.stringify({ type: 'error', operation: 'pty.input',
            session_id: 'session', controller_id: 'A', message: 'wrong wing' }) });
        assert.equal(f.DOM.ptyInputNotice.hidden, true);
        f.select('session', 'wing');
        assert.equal(f.DOM.ptyInputWarning.textContent, message, 'refusal follows its qualified terminal');
        f.DOM.ptyInputDismiss.onclick();
        assert.equal(f.DOM.ptyInputNotice.hidden, true, 'explicit dismissal clears the refusal');
    });
}

for (const channel of ['stable', 'preview']) {
    test(`unconfirmed input warning survives pty.started until dismissal on ${channel}`, async () => {
        const f = warningFixture(channel);
        f.send({ type: 'error', session_id: 'session', operation: 'pty.input',
            code: 'input_delivery_unconfirmed', message: f.warning });
        assert.equal(f.visible(), true, 'warning visible before pty.started');
        f.send({ type: 'pty.started', session_id: 'session', public_key: 'replacement', controller_id: 'B' });
        f.finishKey({});
        await Promise.resolve();
        await Promise.resolve();
        assert.ok(f.S.e2eKey, 'replacement key derived');
        assert.equal(f.S.ptyReconnecting, false, 'reconnect completed');
        console.log(`${channel}: key ready; unconfirmed warning visible=${f.visible()}`);
        assert.equal(f.visible(), true, 'unconfirmed warning still shown after pty.started');
        assert.equal(f.DOM.ptyInputNotice.hidden, false);
        f.DOM.ptyInputDismiss.onclick();
        assert.equal(f.DOM.ptyInputNotice.hidden, true, 'person can dismiss the warning');
        assert.equal(f.DOM.ptyStatus.textContent, '🔒');
    });
}

test('unconfirmed warning follows its qualified terminal across reattach', () => {
    const f = warningFixture('preview');
    f.send({ type: 'error', session_id: 'session', operation: 'pty.input',
        code: 'input_delivery_unconfirmed', message: f.warning });
    assert.equal(f.DOM.ptyInputNotice.hidden, false);
    f.select('session', 'another-wing');
    assert.equal(f.DOM.ptyInputNotice.hidden, true);
    f.select('session', 'wing');
    assert.equal(f.DOM.ptyInputNotice.hidden, false);
    f.DOM.ptyInputDismiss.onclick();
    f.select('session', 'wing');
    assert.equal(f.DOM.ptyInputNotice.hidden, true, 'dismissal survives reattach');
});
