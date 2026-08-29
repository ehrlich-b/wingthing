import test from 'node:test';
import assert from 'node:assert/strict';

import { reconcileWingSessions, shouldFetchWingSessions } from '../src/session-merge.js';

test('reconcileWingSessions refreshes one wing and preserves other wings', function() {
    var current = [
        { id: 'keep', wing_id: 'wing-a', cwd: '/old' },
        { id: 'gone', wing_id: 'wing-a' },
        { id: 'other', wing_id: 'wing-b' },
    ];
    var remote = [
        { id: 'keep', wing_id: 'wing-a', cwd: '/new', agent: 'codex' },
        { id: 'added', wing_id: 'wing-a', cwd: '/added' },
    ];

    var result = reconcileWingSessions(current, 'wing-a', remote);
    assert.deepEqual(result.map(function(s) { return s.id; }), ['keep', 'other', 'added']);
    assert.equal(result[0].cwd, '/new');
    assert.equal(result[0].agent, 'codex');
    assert.equal(result[0].swept, true);
    assert.equal(result[2].swept, true);
});

test('reconcileWingSessions removes stale sessions when a wing reports none', function() {
    var result = reconcileWingSessions([
        { id: 'stale', wing_id: 'wing-a' },
        { id: 'other', wing_id: 'wing-b' },
    ], 'wing-a', []);
    assert.deepEqual(result, [{ id: 'other', wing_id: 'wing-b' }]);
});

test('reconcileWingSessions handles object-prototype-shaped session IDs', function() {
    var result = reconcileWingSessions([], 'wing-a', [
        { id: '__proto__', wing_id: 'wing-a' },
        { id: 'constructor', wing_id: 'wing-a' },
    ]);
    assert.deepEqual(result.map(function(s) { return s.id; }), ['__proto__', 'constructor']);
});

test('direct-only hosted accounts never fetch relayed session inventory', function() {
    assert.equal(shouldFetchWingSessions({ relay_allowed: false }, {}), false);
    assert.equal(shouldFetchWingSessions({ relay_allowed: true }, {}), true);
    assert.equal(shouldFetchWingSessions({}, { tunnel_error: 'unreachable' }), false);
    assert.equal(shouldFetchWingSessions(null, {}), true);
});
