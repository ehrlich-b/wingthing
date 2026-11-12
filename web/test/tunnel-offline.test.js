import test from 'node:test';
import assert from 'node:assert/strict';
import { moduleContext, deferred } from './support/browser-state.mjs';

function harness() {
    const wing = { wing_id: 'selected', public_key: 'key', online: true };
    const S = { wingsData: [wing], tunnelAuthTokens: {} };
    const context = moduleContext('tunnel', { S, Date, TextEncoder, TextDecoder,
        assertWingIdentity() {}, identityPubKey: 'sender', crypto: { randomUUID: () => 'request' } });
    context.deriveE2ETunnelKey = () => 'key';
    context.tunnelEncrypt = () => 'ciphertext';
    const sends = [];
    context.acquireConn = async () => ({ pending: {}, ws: { send: frame => sends.push(frame) } });
    return { context, S, wing, sends };
}

test('queued requests do not open a tunnel after the selected wing disconnects', async () => {
    const h = harness(), token = deferred(); let connections = 0;
    h.context.acquireToken = () => token.promise;
    h.context.acquireConn = async () => { connections++; throw Error('must not connect'); };
    const request = h.context.sendTunnelRequest('selected', { type: 'sessions.list' });
    h.wing.online = false; token.resolve();
    await assert.rejects(request, /wing offline/);
    assert.equal(connections, 0);
    assert.deepEqual(h.sends, []);
});

for (const stream of [false, true]) for (const change of ['offline', 'identity', 'removed']) {
    test(`${stream ? 'stream' : 'request'} rechecks ${change} after connection setup`, async () => {
        const h = harness(), connection = deferred(), connecting = deferred();
        h.context.acquireToken = async () => {};
        h.context.acquireConn = () => { connecting.resolve(); return connection.promise; };
        const request = stream ? h.context.sendTunnelStream('selected', { type: 'sessions.list' }, () => {})
            : h.context.sendTunnelRequest('selected', { type: 'sessions.list' });
        await connecting.promise;
        if (change === 'offline') h.wing.online = false;
        if (change === 'identity') h.wing.public_key = 'replacement';
        if (change === 'removed') h.S.wingsData = [];
        connection.resolve({ pending: {}, ws: { send: frame => h.sends.push(frame) } });
        await assert.rejects(request, change === 'identity' ? /wing identity changed/ : /wing offline/);
        assert.deepEqual(h.sends, []);
    });
}
