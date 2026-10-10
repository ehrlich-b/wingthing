import test from 'node:test';
import assert from 'node:assert/strict';
import { moduleContext, memoryStorage, deferred, session } from './support/browser-state.mjs';

function harness(request) {
    const wing = { wing_id: 'mac', public_key: 'key-1', online: true, hostname: 'original' };
    const calls = [], notices = [];
    const S = { wingsData: [wing], sessionsData: [session()], tunnelAuthTokens: { mac: 'new-grant' }, currentUser: { id: 'owner' } };
    const context = moduleContext('data', {
        S, DOM: {}, localStorage: memoryStorage(), CACHE_KEY: 'sessions',
        sendTunnelRequest(...args) { calls.push(args); return request(...args); },
        saveTunnelAuthTokens() { notices.push('save grants'); },
        console: { error() { notices.push('error'); } },
    });
    context.showCryptoToast = message => notices.push(message);
    return { wing, S, calls, notices, context, probe: w => context.probeWing(w || wing) };
}

test('simultaneous probes share one promise even with immediately fulfilled responses', async () => {
    const h = harness(() => Promise.resolve({ hostname: 'current' }));
    const first = h.probe();
    assert.equal(h.probe(), first);
    await first;
    assert.equal(h.calls.length, 1);
    assert.equal(h.wing.hostname, 'current');
    await h.probe();
    assert.equal(h.calls.length, 2, 'a settled probe permits an intentional fresh probe');
});

test('immediate rejection and synchronous failure allow one deduplicated retry', async () => {
    for (const synchronous of [false, true]) {
        let attempts = 0;
        const h = harness(() => {
            if (++attempts > 1) return Promise.resolve({ hostname: 'retry' });
            if (synchronous) throw Error('passkey_required');
            return Promise.reject(Error('passkey_required'));
        });
        const first = h.probe(); assert.equal(h.probe(), first);
        await first;
        assert.equal(h.wing.tunnel_error, 'no_passkeys_configured');
        const retry = h.probe(); assert.equal(h.probe(), retry);
        await retry;
        assert.equal(h.calls.length, 2);
        assert.equal(h.wing.hostname, 'retry');
        assert.equal(h.wing.tunnel_error, undefined);
    }
});

test('disconnect discards a pending success or failure without changing metadata or grants', async () => {
    for (const failure of [false, true]) {
        const pending = deferred(), h = harness(() => pending.promise);
        const probe = h.probe();
        h.wing.online = false;
        if (failure) pending.reject(Error('not_allowed'));
        else pending.resolve({ hostname: 'stale', agents: ['stale'] });
        await probe;
        assert.equal(h.wing.hostname, 'original');
        assert.equal(h.wing.tunnel_error, undefined);
        assert.equal(h.S.tunnelAuthTokens.mac, 'new-grant');
        assert.equal(h.S.sessionsData.length, 1);
        assert.deepEqual(h.notices, []);
    }
});

test('replacement wing identities receive a new probe and earlier results never overwrite them', async () => {
    const older = deferred(), newer = deferred();
    const h = harness(() => h.calls.length === 1 ? older.promise : newer.promise);
    const first = h.probe();
    h.wing.public_key = 'key-2';
    const second = h.probe();
    assert.notEqual(first, second);
    newer.resolve({ hostname: 'newest' }); await second;
    older.resolve({ hostname: 'stale' }); await first;
    assert.equal(h.wing.hostname, 'newest');
    assert.equal(h.calls.length, 2);
});

test('a replacement roster object cannot borrow the removed object\'s in-flight promise', async () => {
    const older = deferred(), newer = deferred();
    const h = harness(() => h.calls.length === 1 ? older.promise : newer.promise);
    const first = h.probe(), replacement = { ...h.wing };
    h.S.wingsData = [replacement];
    const second = h.probe(replacement);
    assert.notEqual(first, second);
    newer.resolve({ hostname: 'new object' }); await second;
    older.resolve({ hostname: 'old object' }); await first;
    assert.equal(replacement.hostname, 'new object');
    assert.equal(h.wing.hostname, 'original');
});

test('explicit cancellation prevents older cleanup from erasing a pending newer probe', async () => {
    const older = deferred(), newer = deferred();
    const h = harness(() => h.calls.length === 1 ? older.promise : newer.promise);
    const first = h.probe();
    h.context.cancelWingProbe('mac');
    const second = h.probe();
    older.reject(Error('decrypt failed')); await first;
    assert.equal(h.probe(), second);
    assert.equal(h.wing.public_key, 'key-1');
    assert.deepEqual(h.notices, []);
    newer.resolve({ hostname: 'newest' }); await second;
    assert.equal(h.wing.hostname, 'newest');
    assert.equal(h.calls.length, 2);
});

test('removed wings and account changes cannot apply stale probe side effects', async () => {
    for (const change of ['remove', 'account']) {
        const pending = deferred(), h = harness(() => pending.promise);
        const probe = h.probe();
        if (change === 'remove') h.S.wingsData = [];
        else h.S.currentUser = { id: 'other' };
        pending.reject(Error('not_allowed')); await probe;
        assert.equal(h.S.sessionsData.length, 1);
        assert.equal(h.S.tunnelAuthTokens.mac, 'new-grant');
        assert.equal(h.wing.tunnel_error, undefined);
    }
});

test('offline and removed wings make no extra requests', async () => {
    const h = harness(() => Promise.resolve({}));
    h.wing.online = false; await h.probe();
    h.wing.online = true; h.S.wingsData = []; await h.probe();
    assert.equal(h.calls.length, 0);
});

test('wing lifecycle events invalidate pending probes before the next probe', () => {
    for (const type of ['wing.online', 'wing.offline', 'wing.config']) {
        const events = [], S = { wingsData: [{ wing_id: 'mac', public_key: 'key', agents: [] }], sessionsData: [session()], activeView: 'home', availableAgents: [], allProjects: [], currentUser: {} };
        const context = moduleContext('dashboard', {
            S, DOM: { commandPalette: { style: { display: 'none' } }, wingStatusEl: null },
            document: { getElementById: () => null }, requestAnimationFrame() {},
            cancelWingProbe: id => events.push(['cancel', id]),
            probeWing: wing => { events.push(['probe', wing.wing_id]); return new Promise(() => {}); },
            saveWingCache() {}, renderSidebar() {}, renderDashboard() {}, tunnelCloseWing() {},
            applyWingEventMetadata() {}, isCanvasActive: () => false,
        });
        context.applyWingEvent({ type, wing_id: 'mac' });
        assert.deepEqual(events[0], ['cancel', 'mac']);
        assert.deepEqual(events.slice(1), type === 'wing.offline' ? [] : [['probe', 'mac']]);
        if (type === 'wing.offline') assert.equal(S.sessionsData[0].swept, false);
    }
});
