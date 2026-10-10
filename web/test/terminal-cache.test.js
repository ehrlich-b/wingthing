import test from 'node:test';
import assert from 'node:assert/strict';
import { moduleContext, memoryStorage, session } from './support/browser-state.mjs';
import { TERM_BUF_PREFIX, TERM_THUMB_PREFIX } from '../src/state.js';
import { sessionContentKey, readSessionContent } from '../src/session-reference.js';

function harness(storage = memoryStorage(), extra = {}) {
    const timers = new Map(); let timer = 0, content = 'snapshot';
    const S = { ptySessionId: 'current', ptyWingId: 'mac', serializeAddon: { serialize: () => content }, term: null, sessionsData: [session('current')] };
    const context = moduleContext('terminal', { S, DOM: {}, TERM_BUF_PREFIX, TERM_THUMB_PREFIX, localStorage: storage,
        setTimeout(fn) { timers.set(++timer, fn); return timer; }, clearTimeout(id) { timers.delete(id); }, ...extra });
    return { S, context, storage, timers, setContent(value) { content = value; },
        async save(id, data, wing = 'mac') {
            S.ptySessionId = id; S.ptyWingId = wing; content = data;
            context.saveTermBuffer();
            const fn = timers.get(S.saveBufferTimer); timers.delete(S.saveBufferTimer);
            await fn();
        },
        read(id, wing = 'mac') { return readSessionContent(storage, TERM_BUF_PREFIX, wing, id); },
    };
}

function measuredBytes(storage) {
    return [...storage.values].reduce((sum, [key, value]) => sum + Math.max(Buffer.byteLength(key), key.length * 2) + Math.max(Buffer.byteLength(value), value.length * 2), 0);
}

test('bounded snapshots share a total storage budget instead of one allowance per session', async () => {
    const h = harness();
    for (let i = 0; i < 12; i++) await h.save(`s${i}`, 'x'.repeat(180000));
    assert.ok(measuredBytes(h.storage) <= 2000000, `actual storage: ${measuredBytes(h.storage)} bytes`);
    assert.equal(h.read('s0'), null);
    assert.ok(h.read('s11'));
});

test('multibyte snapshots respect a byte allowance and never split surrogate pairs', async () => {
    for (const text of ['界'.repeat(80000), '🙂'.repeat(60000), 'a'.repeat(120000)]) {
        const h = harness(); await h.save('current', text);
        const restored = h.read('current');
        assert.ok(Buffer.byteLength(restored) <= 200000);
        assert.ok(restored.length * 2 <= 200000);
        assert.equal(restored.includes('\ufffd'), false);
        assert.equal(Buffer.from(restored).toString(), restored);
        assert.ok(text.endsWith(restored));
    }
});

test('deleting the current session cancels a pending save and keeps an equal-ID sibling', async () => {
    const h = harness();
    await h.save('current', 'linux snapshot', 'linux');
    await h.save('current', 'mac snapshot');
    h.setContent('resurrected'); h.context.saveTermBuffer();
    const pending = h.timers.get(h.S.saveBufferTimer);
    h.context.clearTermBuffer('current', 'mac');
    await pending(); // Even a callback already delivered to the event loop is stale.
    assert.equal(h.read('current'), null);
    assert.equal(h.read('current', 'linux'), 'linux snapshot');
});

test('a pending save cannot cross a session switch or replacement serializer', async () => {
    for (const replacement of ['wing', 'session', 'serializer']) {
        const h = harness(); h.context.saveTermBuffer();
        const pending = h.timers.get(h.S.saveBufferTimer);
        if (replacement === 'wing') h.S.ptyWingId = 'linux';
        if (replacement === 'session') h.S.ptySessionId = 'next';
        if (replacement === 'serializer') h.S.serializeAddon = { serialize: () => 'replacement' };
        await pending();
        assert.equal(h.storage.length, 0);
    }
});

test('storage denial and quota failure do not escape save, restore or deletion', async () => {
    const storage = { get length() { throw Error('unavailable'); }, getItem() { throw Error('unavailable'); },
        setItem() { throw Error('unavailable'); }, removeItem() { throw Error('unavailable'); } };
    const h = harness(storage);
    await h.save('current', 'snapshot');
    assert.doesNotThrow(() => h.context.restoreTermBuffer('current', 'mac'));
    assert.doesNotThrow(() => h.context.clearTermBuffer('current', 'mac'));
});

test('oldest saved entries evict deterministically while the current session stays pinned', () => {
    const storage = memoryStorage(), h = harness(storage);
    h.S.ptySessionId = 'pinned';
    const put = (id, savedAt) => h.context.writeTerminalCache(storage, TERM_BUF_PREFIX, 'mac', id, 'x'.repeat(100000), savedAt);
    assert.equal(put('pinned', 1000), true);
    assert.equal(put('z-old', 2000), true);
    assert.equal(put('a-old', 2000), true);
    for (let i = 0; i < 7; i++) assert.equal(put(`new-${i}`, 3000 + i), true);
    assert.equal(h.read('a-old'), null, 'equal timestamps break ties by qualified key');
    assert.ok(h.read('z-old'));
    assert.ok(h.read('pinned'), 'the selected session wins over an older timestamp');
    assert.ok(measuredBytes(storage) <= 2000000);
});

test('snapshot and thumbnail eviction/deletion is qualified and does not touch unrelated storage', () => {
    const h = harness(); h.storage.setItem('application-setting', 'keep');
    const put = (prefix, wing, id, content, now = 1000) => h.context.writeTerminalCache(h.storage, prefix, wing, id, content, now);
    put(TERM_BUF_PREFIX, 'mac', 'same', 'mac replay');
    put(TERM_THUMB_PREFIX, 'mac', 'same', 'data:image/webp;base64,mac');
    put(TERM_BUF_PREFIX, 'linux', 'same', 'linux replay');
    put(TERM_THUMB_PREFIX, 'linux', 'same', 'data:image/webp;base64,linux');
    for (let i = 0; i < 10; i++) put(TERM_BUF_PREFIX, 'mac', `new-${i}`, 'x'.repeat(100000), 2000 + i);
    assert.equal(h.read('same'), null);
    assert.equal(readSessionContent(h.storage, TERM_THUMB_PREFIX, 'mac', 'same'), null);
    assert.equal(h.storage.getItem('application-setting'), 'keep');
    assert.ok(measuredBytes(h.storage) <= 2000000);
});

test('reload preserves raw replay strings and expires old buffers and thumbnails', async () => {
    const storage = memoryStorage(); let now = 1000;
    const clock = { now: () => now };
    const original = harness(storage, { Date: clock });
    await original.save('current', '\x1b[31mred 界🙂\x1b[0m');
    original.context.writeTerminalCache(storage, TERM_THUMB_PREFIX, 'mac', 'current', 'data:image/webp;base64,fixture');
    const replayed = [], reload = harness(storage, { Date: clock });
    reload.S.term = { write: text => replayed.push(text) };
    await reload.context.restoreTermBuffer('current', 'mac');
    assert.deepEqual(replayed, ['\x1b[31mred 界🙂\x1b[0m']);
    now += reload.context.TERMINAL_CACHE_MAX_AGE;
    await reload.context.restoreTermBuffer('current', 'mac');
    assert.equal(replayed.length, 1);
    assert.equal(reload.read('current'), null);
    assert.equal(readSessionContent(storage, TERM_THUMB_PREFIX, 'mac', 'current'), null);
});

test('legacy snapshots stay dormant while adopted v2 content remains bounded on restore', async () => {
    const storage = memoryStorage(), h = harness(storage);
    storage.setItem(TERM_BUF_PREFIX + 'current', 'legacy bare ID');
    storage.setItem(sessionContentKey(TERM_BUF_PREFIX, 'mac', 'current'), '界'.repeat(80000));
    const replayed = []; h.S.term = { write: text => replayed.push(text) };
    await h.context.restoreTermBuffer('current', 'mac');
    assert.equal(replayed.length, 1);
    assert.ok(Buffer.byteLength(replayed[0]) <= 200000);
    assert.ok(replayed[0].startsWith('界'));
    assert.equal(storage.getItem(TERM_BUF_PREFIX + 'current'), 'legacy bare ID');
});

test('quota pressure evicts oldest other sessions, and unavailable storage does not trigger eviction', () => {
    for (const denied of [false, true]) {
        const storage = memoryStorage(), h = harness(storage);
        h.context.writeTerminalCache(storage, TERM_BUF_PREFIX, 'mac', 'older', 'o'.repeat(1000), Date.now() - 1000);
        h.context.writeTerminalCache(storage, TERM_BUF_PREFIX, 'mac', 'current', 'previous');
        const set = storage.setItem;
        storage.setItem = (key, value) => {
            const size = measuredBytes(storage) - (storage.getItem(key) === null ? 0 : Math.max(Buffer.byteLength(key), key.length * 2) + Math.max(Buffer.byteLength(storage.getItem(key)), storage.getItem(key).length * 2))
                + Math.max(Buffer.byteLength(key), key.length * 2) + Math.max(Buffer.byteLength(value), value.length * 2);
            if (denied || size > 3000) { const error = Error('storage full'); error.name = denied ? 'SecurityError' : 'QuotaExceededError'; throw error; }
            set(key, value);
        };
        assert.equal(h.context.writeTerminalCache(storage, TERM_BUF_PREFIX, 'mac', 'current', 'n'.repeat(1000)), !denied);
        assert.equal(h.read('older'), denied ? 'o'.repeat(1000) : null);
        assert.equal(h.read('current'), denied ? 'previous' : 'n'.repeat(1000));
        if (!denied) assert.ok(measuredBytes(storage) <= 3000);
    }
});

test('a quota that cannot fit the pinned session skips the update and retains its previous replay', () => {
    const h = harness();
    h.context.writeTerminalCache(h.storage, TERM_BUF_PREFIX, 'mac', 'current', 'previous');
    const set = h.storage.setItem;
    h.storage.setItem = (key, value) => {
        if (key.startsWith(TERM_BUF_PREFIX)) { const error = Error('full'); error.name = 'QuotaExceededError'; throw error; }
        set(key, value);
    };
    assert.equal(h.context.writeTerminalCache(h.storage, TERM_BUF_PREFIX, 'mac', 'current', 'new'), false);
    assert.equal(h.read('current'), 'previous');
});

test('simultaneous tab saves serialize through Web Locks and rebuild the shared index', async () => {
    const storage = memoryStorage(), seed = harness(storage);
    for (let i = 0; i < 9; i++) await seed.save(`seed-${i}`, 'x'.repeat(100000));
    let queue = Promise.resolve(), active = 0, maxActive = 0, locks = 0;
    const navigator = { locks: { request(name, callback) {
        assert.equal(name, 'wt-terminal-cache'); locks++;
        const next = queue.then(async () => {
            maxActive = Math.max(maxActive, ++active);
            try { return await callback(); } finally { active--; }
        });
        queue = next.catch(() => {}); return next;
    } } };
    const first = harness(storage, { navigator }), second = harness(storage, { navigator });
    await Promise.all([first.save('first', '界'.repeat(60000)), second.save('second', '🙂'.repeat(40000), 'linux')]);
    assert.equal(locks, 2); assert.equal(maxActive, 1);
    assert.ok(first.read('first')); assert.ok(second.read('second', 'linux'));
    assert.ok(measuredBytes(storage) <= 2000000);
    const index = JSON.parse(storage.getItem('wt_terminal_cache_v1'));
    for (const key of storage.values.keys()) {
        if (key.startsWith(TERM_BUF_PREFIX)) assert.equal(typeof index[key], 'number');
    }
});

test('a queued save or restore cannot write after deletion or terminal replacement', async () => {
    let release;
    const navigator = { locks: { request(_, callback) { return new Promise(resolve => { release = () => resolve(callback()); }); } } };
    const h = harness(memoryStorage(), { navigator });
    const save = h.save('current', 'stale');
    const runSave = release;
    h.context.clearTermBuffer('current', 'mac');
    runSave(); await save;
    assert.equal(h.read('current'), null);
    const replayed = [];
    h.storage.setItem(sessionContentKey(TERM_BUF_PREFIX, 'mac', 'current'), 'cached');
    h.S.term = { write: text => replayed.push(text) };
    const restore = h.context.restoreTermBuffer('current', 'mac');
    h.S.ptyWingId = 'linux'; release(); await restore;
    assert.deepEqual(replayed, []);
});
