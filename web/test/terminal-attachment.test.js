import test from 'node:test';
import assert from 'node:assert/strict';
import { terminalControlFailure, terminalControlOptions, ptyAttachRequest, currentTerminalResize, renderTerminalControlNotice } from '../src/terminal-attachment.js';
import { requestCanvasStop } from '../src/canvas-stop.js';
import { createCanvasSessionState } from '../src/canvas-session-state.js';

const user = { id: 'owner', release_channel: 'preview' };
const wing = { wing_id: 'mac', spectate: true };
const busy = 'rpc error: code = FailedPrecondition desc = terminal input owned by cli:owner; use explicit takeover to take control';

test('only actual ownership conflicts/revocations offer input controls', () => {
    assert.deepEqual(terminalControlFailure(busy), { kind: 'busy', text: 'Terminal input is controlled by cli:owner.' });
    assert.equal(terminalControlFailure('rpc error: code = Aborted desc = terminal attachment was taken over; attach again before requesting control').kind, 'revoked');
    assert.equal(terminalControlFailure('rpc error: code = Aborted desc = terminal attachment taken over; attach again to request control').kind, 'revoked');
    for (const error of ['access denied', 'wing offline', 'read-only attachment cannot write or resize', 'maybe terminal input owned by someone']) {
        assert.equal(terminalControlFailure(error), null);
    }
    assert.equal(terminalControlOptions({ ...user, release_channel: 'stable' }, wing, 'takeover'), null);
    assert.equal(terminalControlOptions(user, { ...wing, spectate: false }, 'observe'), null);
    assert.equal(terminalControlOptions(user, wing, 'attach'), null);
});

test('default attach/reconnect never takes over; explicit controls have mutually exclusive wire modes', () => {
    assert.deepEqual(ptyAttachRequest('same-id', 'mac', 'public-key'), {
        type: 'pty.attach', session_id: 'same-id', wing_id: 'mac', public_key: 'public-key'
    });
    const take = ptyAttachRequest('same-id', 'mac', 'public-key', terminalControlOptions(user, wing, 'takeover'));
    assert.equal(take.takeover, true);
    assert.equal(take.spectate, undefined);
    const observe = ptyAttachRequest('same-id', 'mac', 'public-key', terminalControlOptions(user, wing, 'observe'));
    assert.equal(observe.spectate, true);
    assert.equal(observe.takeover, undefined);
    assert.equal(ptyAttachRequest('same-id', 'mac', 'public-key', { spectate: true, takeover: true }).takeover, undefined);
});

test('resize uses its acknowledged controller and cannot borrow a replacement attachment', () => {
    const socket = {};
    const binding = { sessionId: 'same-id', wingId: 'mac', socket, controllerId: 'acknowledged-1' };
    const state = { ptySessionId: 'same-id', ptyWingId: 'mac', ptyWs: socket, ptyControllerId: 'acknowledged-1' };
    const size = { cols: 100, rows: 40 };
    assert.deepEqual(currentTerminalResize(binding, state, size), {
        type: 'pty.resize', session_id: 'same-id', controller_id: 'acknowledged-1', cols: 100, rows: 40
    });
    for (const changed of [{ ptyWingId: 'linux' }, { ptyWs: {} }, { ptyControllerId: 'acknowledged-2' }, { ptyControllerId: null }, { spectating: true }, { ptyInputBlocked: true }]) {
        assert.equal(currentTerminalResize(binding, { ...state, ...changed }, size), null);
    }
    assert.deepEqual(currentTerminalResize({ ...binding, controllerId: null }, { ...state, ptyControllerId: null }, size), {
        type: 'pty.resize', session_id: 'same-id', cols: 100, rows: 40
    }); // Existing stable wings retain their wire format.
});

function element() {
    return { style: {}, children: [], listeners: {}, textContent: '', replaceChildren() { this.children = []; }, appendChild(child) { this.children.push(child); }, addEventListener(type, callback) { this.listeners[type] = callback; } };
}

test('visible controls wait for a click, retain exact target and hide Observe without existing permission', () => {
    globalThis.document = { createElement: element };
    const panel = element(), requests = [];
    const onAction = mode => requests.push(ptyAttachRequest('same-id', 'mac', 'public-key', terminalControlOptions(user, wing, mode)));
    renderTerminalControlNotice(panel, terminalControlFailure(busy), wing, user, onAction);
    assert.equal(requests.length, 0);
    assert.deepEqual(panel.children.slice(1).map(child => child.textContent), ['Observe', 'Take control']);
    panel.children[2].listeners.click({ stopPropagation() {} });
    assert.equal(requests[0].wing_id, 'mac');
    assert.equal(requests[0].session_id, 'same-id');
    assert.equal(requests[0].takeover, true);
    renderTerminalControlNotice(panel, terminalControlFailure(busy), { ...wing, spectate: false }, user, onAction);
    assert.deepEqual(panel.children.slice(1).map(child => child.textContent), ['Take control']);
    renderTerminalControlNotice(panel, null);
    assert.equal(panel.style.display, 'none');
    assert.equal(panel.children.length, 0);
});

test('canvas stop keeps both same-ID cards until one exact wing acknowledges', async () => {
    const canvas = createCanvasSessionState();
    const mac = { id: 'same-id', wingId: 'mac' }, linux = { id: 'same-id', wingId: 'linux' };
    canvas.put(mac); canvas.put(linux);
    let acknowledge;
    const calls = [];
    const request = (wingId, message) => { calls.push({ wingId, message }); return new Promise(resolve => { acknowledge = resolve; }); };
    const stopping = requestCanvasStop(mac, request).then(confirmed => { if (confirmed) canvas.remove(mac.key); });
    assert.equal(mac.stopPending, true);
    assert.equal(canvas.find('same-id', 'mac'), mac);
    assert.equal(canvas.find('same-id', 'linux'), linux);
    assert.deepEqual(calls, [{ wingId: 'mac', message: { type: 'pty.kill', session_id: 'same-id' } }]);
    assert.equal(await requestCanvasStop(mac, request), false);
    assert.equal(calls.length, 1);
    acknowledge({ ok: 'true' });
    await stopping;
    assert.equal(canvas.find('same-id', 'mac'), null);
    assert.equal(canvas.find('same-id', 'linux'), linux);
});

test('failed/unconfirmed canvas stops remain inspectable and temporary launch IDs never reach the wing', async () => {
    const entry = { id: 'same-id', wingId: 'mac' };
    assert.equal(await requestCanvasStop(entry, async () => { throw new Error('wing offline'); }), false);
    assert.equal(entry.stopPending, false);
    assert.match(entry.stopError, /wing offline/);
    assert.equal(await requestCanvasStop(entry, async () => ({ ok: false })), false);
    assert.match(entry.stopError, /did not confirm/);
    const starting = { id: 'canvas-temporary', wingId: 'mac', starting: true };
    let calls = 0;
    assert.equal(await requestCanvasStop(starting, async () => { calls++; }), false);
    assert.equal(calls, 0);
    assert.match(starting.stopError, /Launching/);
});
