import test from 'node:test';
import assert from 'node:assert/strict';
import { dormantPreviewEnabled, selectReachRoute, ReachPreview, SharePreview, connectionMeasurement } from '../src/reach-share-state.js';

const wing = { registered: true, pro: true, awake: true, pinMatches: true, revoked: false };
const alice = { provider: 'google', subject: 'alice', name: 'Alice' };
test('paid previews stay off unless the server explicitly returns the local dev flag', () => {
    for (const user of [null, {}, { is_pro: true }, { reach_share_preview: 'true' }, { reach_share_preview: false }]) assert.equal(dormantPreviewEnabled(user), false);
    assert.equal(dormantPreviewEnabled({ reach_share_preview: true }), true);
});
test('reach prefers LAN, tailnet, entitled relay and stops at identity or revocation failure', () => {
    assert.equal(selectReachRoute(wing, { lan: true, tailnet: true, relay: true }).route, 'LAN');
    assert.equal(selectReachRoute(wing, { tailnet: true, relay: true }).route, 'tailnet');
    assert.equal(selectReachRoute(wing, { relay: true }).route, 'relay');
    for (const change of [{ pinMatches: false }, { revoked: true }, { awake: false }]) assert.equal(selectReachRoute({ ...wing, ...change }, { lan: true, relay: true }).route, null);
    assert.equal(selectReachRoute({ ...wing, pro: false }, { lan: true }).route, 'LAN');
    assert.equal(selectReachRoute({ ...wing, registered: false }, { tailnet: true }).route, 'tailnet');
    for (const change of [{ pro: false }, { registered: false }]) assert.equal(selectReachRoute({ ...wing, ...change }, { relay: true }).route, null);
    assert.match(selectReachRoute(wing, {}).reason, /interrupted/);
});
test('register once affects only that machine and never creates device authority', () => {
    const r = new ReachPreview(); r.register('mac'); r.register('mac');
    assert.equal(r.wings.size, 1); assert.equal(r.wing('other').registered, false);
    assert.equal(r.wing('mac').deviceCredential, undefined);
});
test('route changes preserve immutable original input and reconnect never submits again', () => {
    const r = new ReachPreview();
    const pending = r.beginInput('mac', 'same-id', 'keep this draft', 'intent-1');
    assert.ok(Object.isFrozen(pending));
    assert.equal(r.reconnect(), pending); assert.equal(r.reconnect(), pending);
    assert.equal(r.submissions, 1);
    assert.throws(() => r.beginInput('other', 'same-id', 'retry', 'intent-2'), /original receipt/);
    assert.throws(() => r.checkReceipt({ ...pending, wingId: 'other', status: 'acknowledged' }), /does not match/);
    assert.throws(() => r.checkReceipt({ ...pending, intentId: 'intent-2', status: 'acknowledged' }), /does not match/);
    assert.equal(r.checkReceipt({ ...pending, status: 'not found' }).status, 'delivery unknown');
    assert.equal(r.checkReceipt({ ...pending, status: 'acknowledged' }).status, 'acknowledged');
    assert.equal(r.submissions, 1);
});
test('private app grants bind machine, port, provider and verified subject', () => {
    const s = new SharePreview('owner');
    const mac = s.publish('owner', 'mac', 3000), cloud = s.publish('owner', 'cloud', 3000);
    assert.notEqual(mac.id, cloud.id); assert.equal(s.publish('owner', 'mac', 3000), mac);
    assert.deepEqual(s.access(mac.key, alice).status, 'denied');
    s.invite('owner', mac.key, alice, 'view');
    assert.equal(s.access(mac.key, alice).status, 'allowed');
    for (const who of [{ ...alice, provider: 'github' }, { ...alice, subject: 'other' }]) assert.equal(s.access(mac.key, who).status, 'denied');
    assert.equal(s.access(cloud.key, alice).status, 'denied');
    assert.throws(() => s.publish('alice', 'mac', 3000), /Owner/);
    assert.throws(() => s.invite('alice', mac.key, alice, 'view'), /Owner/);
    assert.throws(() => s.publish('owner', 'mac', 0), /valid port/);
});
test('Alice preview never includes inventory and revoke closes exact resource viewers', () => {
    const s = new SharePreview('owner');
    const app = s.publish('owner', 'mac', 3000), other = s.publish('owner', 'mac', 3001);
    const grant = s.invite('owner', app.key, alice, 'view');
    s.invite('owner', other.key, alice, 'view');
    assert.equal(s.open(app.key, alice).status, 'allowed');
    s.open(app.key, alice);
    assert.equal(s.revoke('owner', grant.key), 2);
    assert.deepEqual(s.access(app.key, alice), { status: 'denied', message: 'Access ended or this identity was not invited. Ask the owner for a new grant.' });
    assert.equal(s.access(other.key, alice).status, 'allowed');
});
test('upstream and entitlement failures do not reveal metadata to uninvited accounts', () => {
    const s = new SharePreview('owner'), app = s.publish('owner', 'mac', 3000);
    s.invite('owner', app.key, alice, 'view'); app.up = false;
    assert.equal(s.access(app.key, alice).status, 'unavailable');
    const denied = s.access(app.key, { ...alice, subject: 'other' });
    assert.equal(denied.status, 'denied'); assert.equal(denied.resource, undefined);
    assert.equal(s.access(app.key, alice, false).status, 'denied');
});
test('session observers receive no input authority and an app grant never shares a session', () => {
    const s = new SharePreview('owner'), app = s.publish('owner', 'mac', 3000);
    s.invite('owner', app.key, alice, 'view');
    const session = s.session('owner', 'mac', 'session');
    assert.equal(s.access(session.key, alice).status, 'denied');
    assert.throws(() => s.invite('owner', session.key, alice, 'controller'), /Separate controller/);
    s.invite('owner', session.key, alice, 'observer');
    assert.equal(s.access(session.key, alice).canInput, false);
    assert.equal(s.access(s.session('owner', 'other', 'session').key, alice).status, 'denied');
});
test('latency measurement uses observed time and rejects invalid clock intervals', () => {
    assert.deepEqual(connectionMeasurement(100, 184.3, 'encrypted tunnel', 123), { ms: 84, route: 'encrypted tunnel', measuredAt: 123 });
    for (const end of [NaN, Infinity, 99]) assert.throws(() => connectionMeasurement(100, end, 'tunnel', 123));
});
