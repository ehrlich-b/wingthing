import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import { sessionIsSelected } from '../src/session-inventory.js';
import { terminalControlFailure } from '../src/terminal-attachment.js';

const src = fs.readFileSync(new URL('../src/webrtc.js', import.meta.url), 'utf8');
const dispatch = src.match(/^function handleDCMessage\([^]*?^\}/m)[0];
const refusal = src.slice(src.indexOf('        dc._unconfirmedInputs = 0;'),
    src.indexOf('        dc.onclose = function() {'));
const onclose = src.slice(src.indexOf('        dc.onclose = function() {') + '        dc.onclose = function() {'.length,
    src.indexOf('\n        };', src.indexOf('        dc.onclose = function() {')));

function fixture() {
    const messages = [], dc = {}, key = 'key';
    const S = { ptySessionId: 'session', ptyWingId: 'wing', ptyWs: { dispatchEvent(event) { messages.push(JSON.parse(event.data)); } } };
    const channels = { key: dc }, active = { key: true };
    const close = new Function('S', 'sessionIsSelected', 'dataChannels', 'dcActive', 'dcBuffers', 'sessionWings', 'key', 'dc', 'wingId', 'sessionId',
        dispatch + '\n' + refusal + '\nreturn function() {' + onclose + '};')(
        S, sessionIsSelected, channels, active, {}, {}, key, dc, 'wing', 'session');
    return { close, S, messages, channels, active };
}

test('unexpected data channel loss visibly reports unconfirmed input delivery', async () => {
    const f = peerFixture();
    await f.initWebRTC('wing', 'session', false);
    f.completeMigration('wing', 'session');
    assert.equal(f.sendViaDC('session', { type: 'pty.input', data: 'encrypted' }, 'wing'), true);
    f.channels[0].close();
    await new Promise(resolve => queueMicrotask(resolve));
    assert.equal(f.messages[0].code, 'input_delivery_unconfirmed');
    assert.match(f.messages[0].message, /check terminal before retrying input/);
    assert.equal(f.sendViaDC('session', { type: 'pty.input', data: 'later' }, 'wing'), false);
});

test('intentional cleanup or a stale close cannot overwrite the current terminal', () => {
    for (const change of [f => { delete f.channels.key; }, f => { f.channels.key = {}; },
        f => { f.S.ptyWingId = 'another wing'; }, f => { f.S.ptySessionId = 'another session'; }]) {
        const f = fixture();
        f.channels.key._unconfirmedInputs = 1;
        change(f);
        f.close();
        assert.deepEqual(f.messages, []);
    }
});

test('data channel queue refusals reach the same selected terminal handler', () => {
    const f = fixture();
    const handle = new Function('S', 'sessionIsSelected', dispatch + '\nreturn handleDCMessage;')(f.S, sessionIsSelected);
    const refusal = { type: 'error', session_id: 'session', code: 'session_input_busy', operation: 'pty.input', message: 'message was not delivered' };
    handle('wing', 'session', JSON.stringify(refusal));
    assert.deepEqual(f.messages, [refusal]);
    handle('another wing', 'session', JSON.stringify(refusal));
    assert.equal(f.messages.length, 1);
});

// Evaluate the full production module with only browser/transport dependencies
// substituted. The failed callback, cleanup, send and asynchronous close path
// all run exactly as installed by initWebRTC.
function peerFixture() {
    const messages = [], channels = [], pcs = [];
    const S = { wingsData: [], ptySessionId: 'session', ptyWingId: 'wing',
        ptyWs: { dispatchEvent(event) { messages.push(JSON.parse(event.data)); } } };
    class Peer {
        constructor() { pcs.push(this); }
        createDataChannel() {
            const dc = { readyState: 'open', sent: [], send(data) { this.sent.push(data); },
                close() { this.readyState = 'closed'; queueMicrotask(() => this.onclose()); } };
            channels.push(dc);
            return dc;
        }
        async createOffer() { return { type: 'offer', sdp: 'local' }; }
        async setLocalDescription(offer) {
            this.localDescription = offer;
            this.iceGatheringState = 'complete';
            this.onicegatheringstatechange();
        }
        async setRemoteDescription() {}
        close() {}
        fail() { this.connectionState = 'failed'; this.onconnectionstatechange(); }
    }
    const module = new Function('S', 'sendTunnelRequest', 'sessionResourceKey', 'sessionIsSelected',
        'RTCPeerConnection', 'WebSocket', 'setTimeout', 'clearTimeout',
        src.replace(/^import .*;$/gm, '').replace(/^export /gm, '') +
        '\nreturn { initWebRTC, completeMigration, sendViaDC, cleanupPeer, cleanupSession };')(
        S, async () => ({ sdp: 'answer' }), ({ id, wingId }) => JSON.stringify([wingId, id]),
        sessionIsSelected, Peer, { OPEN: 1 }, () => 1, () => {});
    return { ...module, S, messages, channels, pcs };
}

test('closing a direct channel without input never reports input loss', async () => {
    for (const channel of ['stable', 'preview']) for (const migrated of [false, true]) {
        for (const exit of ['close', 'peer failure', 'session cleanup', 'peer cleanup']) {
            const f = peerFixture();
            f.S.currentUser = { release_channel: channel };
            await f.initWebRTC('wing', 'session', false);
            if (migrated) {
                f.completeMigration('wing', 'session');
                assert.equal(f.sendViaDC('session', { type: 'pty.resize', cols: 80, rows: 24 }, 'wing'), true);
            }
            const dc = f.channels[0];
            assert.equal(dc._unconfirmedInputs, 0);
            if (exit === 'close') dc.close();
            else if (exit === 'peer failure') f.pcs[0].fail();
            else if (exit === 'session cleanup') f.cleanupSession('session', 'wing');
            else f.cleanupPeer('wing');
            await new Promise(resolve => queueMicrotask(resolve));
            f.cleanupSession('session', 'wing');
            f.cleanupPeer('wing');
            assert.deepEqual(f.messages, [], `${channel} migrated=${migrated} ${exit} must stay quiet without input`);
            assert.equal(dc._refusedInputs, 0);
            console.log(`${channel} migrated=${migrated} ${exit}: input sends=0 warnings=${f.messages.length}`);
        }
    }
});

test('direct channel loss during reconnect displays one refusal on the replacement socket before started', async () => {
    const pty = fs.readFileSync(new URL('../src/pty.js', import.meta.url), 'utf8');
    const wrapper = pty.slice(pty.indexOf('    var origMsg = innerWs.onmessage;'),
        pty.indexOf('\n}\n\nexport function retryReconnect'));
    const warningHelpers = pty.slice(pty.indexOf('var unconfirmedInputWarnings ='), pty.indexOf('function clearTerminalControl'));
    const errorCase = pty.slice(pty.indexOf("            case 'error':"),
        pty.indexOf('\n        }\n    };', pty.indexOf("            case 'error':")));
    for (const channel of ['stable', 'preview']) for (const readyState of [0, 1]) {
        const f = peerFixture();
        f.S.currentUser = { release_channel: channel };
        await f.initWebRTC('wing', 'session', false);
        f.completeMigration('wing', 'session');
        assert.equal(f.sendViaDC('session', { type: 'pty.input', session_id: 'session', data: 'encrypted' }, 'wing'), true);
        const dc = f.channels[0];
        const unconfirmed = dc._unconfirmedInputs;
        const DOM = { ptyStatus: { textContent: 'reconnecting...' } };
        const displayed = [];
        let closed = 0;
        const display = new Function('S', 'DOM', 'handleTerminalControlError', warningHelpers + `
            return function(msg) {
                var ws = S.ptyWs, requestedSessionId = 'session', connectedWingId = 'wing';
                switch (msg.type) { ${errorCase} }
            };`)(f.S, DOM, () => false);
        // Relay loss leaves the migrated channel open while a replacement
        // socket connects. Dispatch must use that socket's installed wrapper.
        const replacement = { readyState, close() { closed++; },
            dispatchEvent(event) { this.onmessage(event); },
            onmessage(event) {
                const msg = JSON.parse(event.data);
                display(msg);
                displayed.push({ msg, status: DOM.ptyStatus.textContent });
            } };
        f.S.ptyWs = replacement;
        f.S.ptyReconnecting = true;
        new Function('innerWs', 'S', 'sessionId', 'hideReconnectBanner', 'terminalControlFailure', wrapper)(
            replacement, f.S, 'session', () => {}, terminalControlFailure);
        dc.close();
        await new Promise(resolve => queueMicrotask(resolve));
        console.log(`${channel} readyState=${readyState}: unconfirmed input=${unconfirmed}; displayed warnings=${displayed.length}; reconnect socket closes=${closed}`);
        assert.equal(unconfirmed, 1);
        assert.equal(displayed.length, 1, 'unconfirmed input must produce one visible warning during reconnect');
        assert.equal(displayed[0].status, 'Connection lost: check terminal before retrying input.');
        assert.equal(displayed[0].msg.operation, 'pty.input');
        assert.equal(displayed[0].msg.session_id, 'session');
        assert.equal(displayed[0].msg.code, 'input_delivery_unconfirmed');
        assert.equal(closed, 0, 'input refusal must not abort the pending attach');
        assert.equal(f.S.ptyReconnecting, true);
        assert.deepEqual(f.messages, [], 'old socket must receive no warning');
        f.cleanupSession('session', 'wing');
        f.pcs[0].fail();
        assert.equal(displayed.length, 1, 'late cleanup must not duplicate the warning');
    }
});

// Observe the final status through the production takeover caller and error
// dispatch, including the warning emitted synchronously by channel cleanup.
test('preview takeover preserves the unconfirmed input warning after cleanup', async () => {
    const pty = fs.readFileSync(new URL('../src/pty.js', import.meta.url), 'utf8');
    const control = pty.match(/^function handleTerminalControlError\([^]*?^\}/m)[0];
    const warningHelpers = pty.slice(pty.indexOf('var unconfirmedInputWarnings ='), pty.indexOf('function clearTerminalControl'));
    const errorCase = pty.slice(pty.indexOf("            case 'error':"),
        pty.indexOf('\n        }\n    };', pty.indexOf("            case 'error':")));
    for (const migrated of [false, true]) {
        const f = peerFixture();
        f.S.currentUser = { release_channel: 'preview' };
        const DOM = { ptyStatus: { textContent: '' } };
        const notices = [];
        const handle = new Function('S', 'DOM', 'terminalControlFailure', 'cleanupSession',
            'clearOutputIncomplete', 'hideReconnectBanner', 'hideReplayOverlay',
            'renderTerminalControlNotice', 'document', warningHelpers + control + `
            return function(ws, msg) {
                var requestedSessionId = 'session', connectedWingId = 'wing';
                switch (msg.type) { ${errorCase} }
            };`)(f.S, DOM, terminalControlFailure, f.cleanupSession,
            () => {}, () => {}, () => {}, (_, failure) => notices.push(failure),
            { getElementById() { return null; } });
        const ws = f.S.ptyWs;
        const record = ws.dispatchEvent;
        ws.dispatchEvent = event => { record(event); handle(ws, JSON.parse(event.data)); };
        await f.initWebRTC('wing', 'session', false);
        if (migrated) {
            f.completeMigration('wing', 'session');
            assert.equal(f.sendViaDC('session', { type: 'pty.input', session_id: 'session', data: 'encrypted' }, 'wing'), true);
        }
        handle(ws, { type: 'error', session_id: 'session',
            message: 'terminal attachment was taken over; attach again to request control' });
        const expected = migrated ? 'Connection lost: check terminal before retrying input.' : 'control changed';
        assert.equal(DOM.ptyStatus.textContent, expected, 'takeover must not overwrite the input warning');
        assert.equal(f.S.ptyInputBlocked, true);
        assert.equal(f.S.ptyControllerId, null);
        assert.equal(notices.at(-1).kind, 'revoked');
        await new Promise(resolve => queueMicrotask(resolve));
        assert.equal(DOM.ptyStatus.textContent, expected);
        assert.equal(f.messages.length, migrated ? 1 : 0);
        if (migrated) {
            assert.equal(f.channels[0].sent.length, 1);
            assert.equal(f.channels[0]._refusedInputs, 1);
            console.log('takeover before async close: input sends=1 visible refusals=1 final status=' + DOM.ptyStatus.textContent);
        }
    }
});

test('production peer failure refuses every unconfirmed send once before asynchronous close', async () => {
    const f = peerFixture();
    await f.initWebRTC('wing', 'session', false);
    f.completeMigration('wing', 'session');
    const dc = f.channels[0];
    for (let i = 0; i < 3; i++) {
        assert.equal(f.sendViaDC('session', { type: 'pty.input', data: 'encrypted-' + i }, 'wing'), true);
    }
    assert.equal(dc._unconfirmedInputs, 3);
    f.pcs[0].fail();
    assert.equal(f.messages.length, 1, 'cleanup must warn before the asynchronous close callback');
    assert.equal(dc._refusedInputs, 3);
    assert.equal(dc._unconfirmedInputs, 0);
    await new Promise(resolve => queueMicrotask(resolve));
    f.pcs[0].fail();
    f.cleanupPeer('wing');
    assert.equal(f.messages.length, 1);
    assert.equal(f.messages[0].operation, 'pty.input');
    assert.equal(f.messages[0].code, 'input_delivery_unconfirmed');
    assert.equal(f.messages[0].session_id, 'session');
});

test('unexpected close and stale peer failure cannot double-report or affect a replacement', async () => {
    const f = peerFixture();
    await f.initWebRTC('wing', 'session', false);
    f.completeMigration('wing', 'session');
    const old = f.channels[0];
    f.sendViaDC('session', JSON.stringify({ type: 'pty.input', data: 'encrypted' }), 'wing');
    old.close();
    await new Promise(resolve => queueMicrotask(resolve));
    f.pcs[0].fail();
    assert.equal(f.messages.length, 1);
    await f.initWebRTC('wing', 'session', false);
    f.completeMigration('wing', 'session');
    old.onclose();
    f.pcs[0].fail();
    assert.equal(f.messages.length, 1);
    assert.equal(f.sendViaDC('session', { type: 'pty.input', data: 'replacement' }, 'wing'), true);
    assert.equal(f.channels[1]._unconfirmedInputs, 1);
});

test('cleanup and peer failure for a nonselected session leave the selected terminal alone', async () => {
    for (const detach of [true, false]) {
        const f = peerFixture();
        await f.initWebRTC('wing', 'session', false);
        f.completeMigration('wing', 'session');
        f.sendViaDC('session', { type: 'pty.input', data: 'encrypted' }, 'wing');
        f.S.ptyWingId = 'other-wing';
        if (detach) f.cleanupSession('session', 'wing');
        f.pcs[0].fail();
        await new Promise(resolve => queueMicrotask(resolve));
        assert.deepEqual(f.messages, []);
    }
});


test('relay fallback cleanup refuses one unconfirmed input before asynchronous close', async () => {
    for (const closeFirst of [false, true]) {
        const f = peerFixture();
        await f.initWebRTC('wing', 'session', false);
        f.completeMigration('wing', 'session');
        const dc = f.channels[0];
        assert.equal(f.sendViaDC('session', { type: 'pty.input', session_id: 'session', data: 'encrypted' }, 'wing'), true);
        if (closeFirst) {
            dc.close();
            await new Promise(resolve => queueMicrotask(resolve));
        }
        // pty.fallback invokes this production entry point before the browser
        // necessarily delivers the asynchronous data-channel close event.
        f.cleanupSession('session', 'wing');
        assert.equal(dc.sent.length, 1);
        assert.equal(f.messages.length, 1, 'fallback must visibly refuse input before removing its channel');
        assert.equal(dc._refusedInputs, 1);
        assert.equal(dc._unconfirmedInputs, 0);
        assert.equal(f.messages[0].code, 'input_delivery_unconfirmed');
        assert.equal(f.messages[0].operation, 'pty.input');
        assert.equal(f.messages[0].session_id, 'session');
        await new Promise(resolve => queueMicrotask(resolve));
        f.cleanupSession('session', 'wing');
        f.cleanupPeer('wing');
        assert.equal(f.messages.length, 1, 'late close and repeated cleanup must not report twice');
        console.log('fallback ' + (closeFirst ? 'after' : 'before') + ' async close: input sends=1 visible refusals=' + f.messages.length);
    }
});
