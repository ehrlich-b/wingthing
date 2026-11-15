import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import { terminalReferenceMatches } from '../src/session-reference.js';

const src = fs.readFileSync(new URL('../src/terminal.js', import.meta.url), 'utf8');
const body = src.slice(src.indexOf('function refusePTYInput(')).replace('export function sendPTYInput', 'function sendPTYInput');

function fixture() {
    const sent = [], cleared = [], refused = [];
    const S = { ptySessionId: 'session', ptyWingId: 'wing', ptyControllerId: 'controller',
        e2eKey: {}, ptyWs: { readyState: 1, send: raw => sent.push(JSON.parse(raw)) } };
    const DOM = { ptyStatus: { textContent: '' } };
    let encrypt = async () => 'encrypted', direct = () => false;
    const send = new Function('S', 'DOM', 'terminalReferenceMatches', 'e2eEncrypt', 'clearNotification', 'sendViaDC', 'WebSocket', 'showInputRefusal',
        body + '\nreturn sendPTYInput;')(S, DOM, terminalReferenceMatches, text => encrypt(text),
        (...args) => cleared.push(args), (...args) => direct(...args), { OPEN: 1 }, (...args) => refused.push(args));
    return { S, DOM, sent, cleared, refused, send, encrypt(fn) { encrypt = fn; }, direct(fn) { direct = fn; } };
}

for (const channel of ['stable', 'preview']) {
    test(`${channel} refuses typing while reconnect key is missing`, async () => {
        const f = fixture();
        f.S.currentUser = { release_channel: channel };
        f.S.e2eKey = null;
        f.S.ptyReconnecting = true;
        let encrypted = 0;
        f.encrypt(async () => { encrypted++; });
        await f.send('do not lose this');
        assert.equal(encrypted, 0);
        assert.deepEqual(f.sent, []);
        assert.deepEqual(f.cleared, []);
        assert.match(f.DOM.ptyStatus.textContent, /Input not sent: reconnecting/);
        f.S.e2eKey = {};
        f.S.ptyReconnecting = false;
        f.encrypt(async () => 'replacement encrypted');
        await f.send('new input');
        assert.deepEqual(f.sent, [{ type: 'pty.input', session_id: 'session', data: 'replacement encrypted' }]);
    });
}

test('pending encryption cannot send under a replacement key or controller', async () => {
    for (const changed of [{ e2eKey: {} }, { ptyControllerId: 'replacement' }, { ptyWs: { readyState: 1 } },
        { ptyInputBlocked: true }, { spectating: true }, { ptyReconnecting: true }]) {
        const f = fixture();
        let resolve;
        f.encrypt(() => new Promise(done => { resolve = done; }));
        const pending = f.send('old input');
        Object.assign(f.S, changed);
        resolve('old encrypted');
        await pending;
        assert.deepEqual(f.sent, []);
        assert.deepEqual(f.cleared, []);
        assert.match(f.DOM.ptyStatus.textContent, /Input not sent: terminal connection changed/);
    }
});

for (const changed of [{ ptyWingId: 'another wing' }, { ptySessionId: 'another session' }, { ptySessionId: null, ptyWingId: null }]) {
    test('navigation refuses pending input for its original wing and session: ' + JSON.stringify(changed), async () => {
        const f = fixture();
        let resolve;
        f.encrypt(() => new Promise(done => { resolve = done; }));
        const pending = f.send('old input');
        Object.assign(f.S, changed);
        f.DOM.ptyStatus.textContent = 'another terminal';
        resolve('old encrypted');
        await pending;
        assert.deepEqual(f.sent, []);
        assert.equal(f.DOM.ptyStatus.textContent, 'another terminal');
        assert.deepEqual(f.cleared, []);
        assert.deepEqual(f.refused, [['session', 'wing', 'Input not sent: terminal connection changed.']]);
    });
}

test('encryption failure after navigation visibly refuses input at its origin', async () => {
    const f = fixture();
    let reject;
    f.encrypt(() => new Promise((_, fail) => { reject = fail; }));
    const pending = f.send('old input');
    f.S.ptyWingId = 'another wing';
    reject(Error('key failed'));
    await pending;
    assert.deepEqual(f.sent, []);
    assert.deepEqual(f.refused, [['session', 'wing', 'Input not sent: terminal unavailable.']]);
});

test('disconnected, failed encryption and failed socket writes explicitly refuse input', async () => {
    for (const failure of ['disconnected', 'encrypt', 'socket']) {
        const f = fixture();
        if (failure === 'disconnected') f.S.ptyWs.readyState = 3;
        if (failure === 'encrypt') f.encrypt(async () => { throw Error('key failed'); });
        if (failure === 'socket') f.S.ptyWs.send = () => { throw Error('socket failed'); };
        await f.send('input');
        assert.deepEqual(f.cleared, []);
        assert.match(f.DOM.ptyStatus.textContent, /^Input not sent:/);
    }
});

test('direct sends preserve encrypted payload and clear notification only after sending', async () => {
    const f = fixture(), direct = [];
    f.direct((...args) => { direct.push(args); return true; });
    await f.send('input');
    assert.deepEqual(direct, [['session', { type: 'pty.input', session_id: 'session', data: 'encrypted' }, 'wing']]);
    assert.deepEqual(f.sent, []);
    assert.deepEqual(f.cleared, [['session', 'wing']]);
    assert.equal(f.DOM.ptyStatus.textContent, '');
});
