import test from 'node:test';
import assert from 'node:assert/strict';

import { scopeBrowserStateToUser } from '../src/storage-scope.js';
import { CACHE_OWNER_KEY, S } from '../src/state.js';

function fakeStorage(initial) {
    var values = new Map(Object.entries(initial || {}));
    return {
        get length() { return values.size; },
        key(index) { return Array.from(values.keys())[index] || null; },
        getItem(key) { return values.has(key) ? values.get(key) : null; },
        setItem(key, value) { values.set(key, String(value)); },
        removeItem(key) { values.delete(key); },
    };
}

test('browser wing and terminal state is cleared when the authenticated user changes', function() {
    globalThis.localStorage = fakeStorage({
        [CACHE_OWNER_KEY]: 'old-user',
        wt_sessions: '[{"id":"private-session"}]',
        wt_wings: '[{"hostname":"private-host"}]',
        wt_wing_identity_pins_v1: '{"private-wing":"key"}',
        unrelated: 'keep',
    });
    globalThis.sessionStorage = fakeStorage({
        wt_auth_tokens: '{"private-wing":"grant"}',
        wt_identity_privkey: 'private-key',
        unrelated_session: 'keep',
    });
    S.wingsData = [{ wing_id: 'private-wing' }];
    S.sessionsData = [{ id: 'private-session' }];
    S.tunnelKeys = { key: 'secret' };
    S.tunnelAuthTokens = { wing: 'grant' };

    assert.equal(scopeBrowserStateToUser('new-user'), true);
    assert.equal(localStorage.getItem(CACHE_OWNER_KEY), 'new-user');
    assert.equal(localStorage.getItem('wt_sessions'), null);
    assert.equal(localStorage.getItem('wt_wing_identity_pins_v1'), null);
    assert.equal(localStorage.getItem('unrelated'), 'keep');
    assert.equal(sessionStorage.getItem('wt_auth_tokens'), null);
    assert.equal(sessionStorage.getItem('wt_identity_privkey'), 'private-key');
    assert.equal(sessionStorage.getItem('unrelated_session'), 'keep');
    assert.deepEqual(S.wingsData, []);
    assert.deepEqual(S.sessionsData, []);
    assert.deepEqual(S.tunnelKeys, {});
    assert.deepEqual(S.tunnelAuthTokens, {});
});

test('same-user reload preserves scoped browser state', function() {
    globalThis.localStorage = fakeStorage({ [CACHE_OWNER_KEY]: 'same-user', wt_sessions: '[{"id":"keep"}]' });
    globalThis.sessionStorage = fakeStorage({ wt_auth_tokens: '{"wing":"keep"}' });
    S.sessionsData = [{ id: 'keep' }];

    assert.equal(scopeBrowserStateToUser('same-user'), false);
    assert.notEqual(localStorage.getItem('wt_sessions'), null);
    assert.notEqual(sessionStorage.getItem('wt_auth_tokens'), null);
    assert.deepEqual(S.sessionsData, [{ id: 'keep' }]);
});
