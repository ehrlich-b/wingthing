import test from 'node:test';
import assert from 'node:assert/strict';
import { trackSessionCompletions, acknowledgeSessionCompletions, unseenSessionCompletions, COMPLETION_LIMIT, COMPLETION_MAX_AGE_MS, COMPLETION_STORAGE_PREFIX } from '../src/session-completion.js';
import { sessionResourceKey } from '../src/session-reference.js';
import { userStorageKey, scopeBrowserStateToUser } from '../src/storage-scope.js';
import { CACHE_OWNER_KEY } from '../src/state.js';

function storage() {
    var values = new Map();
    return { getItem: key => values.get(key) || null, setItem: (key, value) => values.set(key, value), removeItem: key => values.delete(key),
        get length() { return values.size; }, key: index => Array.from(values.keys())[index] || null };
}
const observation = (status, cursor, extra = {}) => ({ id: 'session', wing_id: 'mac', status, cursor, ...extra });
const key = sessionResourceKey(observation('idle', 0));

test('done and idle after working remain unseen across reload until opened or acknowledged', () => {
    const saved = storage();
    assert.equal(trackSessionCompletions(saved, 'owner', [observation('idle', 1)], 1).size, 0);
    trackSessionCompletions(saved, 'owner', [observation('working', 2)], 2);
    assert.equal(trackSessionCompletions(saved, 'owner', [observation('idle', 3)], 3).has(key), true);
    assert.equal(unseenSessionCompletions(saved, 'owner', 4).has(key), true);
    acknowledgeSessionCompletions(saved, 'owner', [observation('idle', 3)], 4);
    assert.equal(trackSessionCompletions(saved, 'owner', [observation('idle', 3)], 5).size, 0);
    assert.equal(trackSessionCompletions(saved, 'owner', [observation('done', 4)], 6).has(key), true);
    assert.equal(trackSessionCompletions(saved, 'owner', [observation('done', 4, { seen: true })], 7).size, 0);
    assert.equal(trackSessionCompletions(saved, 'owner', [observation('done', 4)], 8).size, 0);
    assert.equal(trackSessionCompletions(saved, 'owner', [observation('done', 6)], 9).has(key), true, 'new cursor detects a done transition missed between reads');
});

test('unknown reads preserve working evidence; idle, bells, attachment and exits do not invent completion', () => {
    const saved = storage();
    trackSessionCompletions(saved, 'owner', [observation('working', 1)], 1);
    trackSessionCompletions(saved, 'owner', [observation('unknown', 99)], 2);
    trackSessionCompletions(saved, 'owner', [observation('blocked', 2)], 3);
    assert.equal(trackSessionCompletions(saved, 'owner', [observation('idle', 3)], 4).has(key), true);
    acknowledgeSessionCompletions(saved, 'owner', [observation('idle', 3)], 5);
    for (const status of ['idle', 'exited', 'active', 'detached', 'completed', 'unknown']) {
        assert.equal(trackSessionCompletions(saved, 'owner', [observation(status, 4, { needs_attention: true })], 6).size, 0);
    }
});

test('actively viewed completions are seen while background completions persist through later work and blocking', () => {
    const saved = storage();
    trackSessionCompletions(saved, 'owner', [observation('working', 1, { seen: true })], 1);
    assert.equal(trackSessionCompletions(saved, 'owner', [observation('idle', 2, { seen: true })], 2).size, 0);
    trackSessionCompletions(saved, 'owner', [observation('done', 3)], 3);
    assert.equal(trackSessionCompletions(saved, 'owner', [observation('working', 4)], 4).has(key), true);
    assert.equal(trackSessionCompletions(saved, 'owner', [observation('blocked', 5)], 5).has(key), true);
});

test('older observations and stale acknowledgements cannot erase a newer unseen cursor', () => {
    const saved = storage();
    trackSessionCompletions(saved, 'owner', [observation('done', 10)], 1);
    assert.equal(trackSessionCompletions(saved, 'owner', [observation('working', 5, { seen: true })], 2).has(key), true);
    acknowledgeSessionCompletions(saved, 'owner', [{ ...observation('idle', 5), lifecycle: { state_cursor: 5 } }], 3);
    assert.equal(unseenSessionCompletions(saved, 'owner', 3).has(key), true);
    acknowledgeSessionCompletions(saved, 'owner', [{ ...observation('done', 10), lifecycle: { state_cursor: 10 } }], 4);
    assert.equal(trackSessionCompletions(saved, 'owner', [observation('done', 10)], 5).size, 0);
});

test('old wings without state cursors track status transitions without repeat completions', () => {
    const saved = storage();
    trackSessionCompletions(saved, 'owner', [observation('working')], 1);
    trackSessionCompletions(saved, 'owner', [observation('idle')], 2);
    acknowledgeSessionCompletions(saved, 'owner', [observation('idle')], 3);
    assert.equal(trackSessionCompletions(saved, 'owner', [observation('idle')], 4).size, 0);
    trackSessionCompletions(saved, 'owner', [observation('working')], 5);
    assert.equal(trackSessionCompletions(saved, 'owner', [observation('idle')], 6).has(key), true);
});

test('seen state is qualified by user and wing and reset by storage-scope on account changes', () => {
    const saved = storage();
    trackSessionCompletions(saved, 'owner', [observation('done', 1), observation('done', 1, { wing_id: 'linux' })], 1);
    acknowledgeSessionCompletions(saved, 'owner', [observation('done', 1)], 2);
    assert.deepEqual([...unseenSessionCompletions(saved, 'owner', 2)], [sessionResourceKey({ id: 'session', wing_id: 'linux' })]);
    assert.equal(unseenSessionCompletions(saved, 'other', 2).size, 0);
    trackSessionCompletions(saved, 'other', [observation('done', 1)], 2);
    assert.equal(unseenSessionCompletions(saved, 'other', 2).has(key), true);
    globalThis.localStorage = saved;
    globalThis.sessionStorage = storage();
    saved.setItem(CACHE_OWNER_KEY, 'owner');
    scopeBrowserStateToUser('other');
    assert.equal(unseenSessionCompletions(saved, 'owner', 3).size, 0);
    assert.equal(unseenSessionCompletions(saved, 'other', 3).size, 0);
});

test('the global per-user bound evicts least recently changed sessions across wings', () => {
    const saved = storage();
    for (let i = 0; i < COMPLETION_LIMIT; i++) {
        trackSessionCompletions(saved, 'owner', [observation('done', 1, { id: 's' + i, wing_id: i % 2 ? 'linux' : 'mac' })], i + 1);
    }
    acknowledgeSessionCompletions(saved, 'owner', [{ id: 's0', wing_id: 'mac' }], COMPLETION_LIMIT + 1);
    // An unchanged refresh must not keep an old record ahead of an acknowledgement.
    trackSessionCompletions(saved, 'owner', [observation('done', 1, { id: 's1', wing_id: 'linux' })], COMPLETION_LIMIT + 2);
    trackSessionCompletions(saved, 'owner', [observation('done', 1, { id: 'new' })], COMPLETION_LIMIT + 3);
    const records = JSON.parse(saved.getItem(userStorageKey(COMPLETION_STORAGE_PREFIX, 'owner')));
    assert.equal(records.length, COMPLETION_LIMIT);
    assert.equal(records.some(record => record.id === 's0'), true);
    assert.equal(records.some(record => record.id === 's1'), false);
    assert.equal(records.some(record => record.id === 'new'), true);
    assert.equal(saved.length, 1, 'wing scopes cannot create unbounded storage keys');
});

test('expired records are evicted and malformed/disabled storage degrades safely', () => {
    const saved = storage();
    trackSessionCompletions(saved, 'owner', [observation('done', 1)], 1);
    assert.equal(unseenSessionCompletions(saved, 'owner', COMPLETION_MAX_AGE_MS + 2).size, 0);
    trackSessionCompletions(saved, 'owner', [observation('idle', 1, { id: 'new' })], COMPLETION_MAX_AGE_MS + 2);
    assert.equal(JSON.parse(saved.getItem(userStorageKey(COMPLETION_STORAGE_PREFIX, 'owner'))).length, 1);
    for (const value of ['broken JSON', '{}', '[null, {}, {"id":"bad"}]']) {
        saved.setItem(userStorageKey(COMPLETION_STORAGE_PREFIX, 'owner'), value);
        assert.equal(unseenSessionCompletions(saved, 'owner', 1).size, 0);
    }
    assert.equal(unseenSessionCompletions(null, 'owner').size, 0);
    assert.equal(trackSessionCompletions(null, 'owner', [observation('done', 1)]).has(key), true);
    acknowledgeSessionCompletions(null, 'owner', [observation('done', 1)]);
    assert.equal(trackSessionCompletions(saved, '', [observation('done', 1)]).size, 0);
});
