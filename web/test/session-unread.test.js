import test from 'node:test';
import assert from 'node:assert/strict';
import { trackSessionOutput, unreadSessionOutput, acknowledgeSessionOutput, UNREAD_LIMIT, UNREAD_STORAGE_PREFIX } from '../src/session-unread.js';
import { groupSessionInventory, filterSessionInventory, sessionResourceKey } from '../src/session-inventory.js';

const row = (wing = 'a', cursor = 10, epoch = 'execution') => ({ id: 'same', wing_id: wing, output: { epoch, cursor }, swept: true });
function storage() { const values = new Map(); return { getItem: key => values.get(key), setItem: (key, value) => values.set(key, value), values }; }
test('read cursors persist per viewer and exact wing while output and blocked are separate', () => {
    const s = storage(), rows = [row(), row('b')];
    trackSessionOutput(s, 'owner', rows);
    assert.deepEqual([...unreadSessionOutput(s, 'owner')], rows.map(sessionResourceKey));
    assert.equal(unreadSessionOutput(s, 'other').size, 0);
    acknowledgeSessionOutput(s, 'owner', [rows[0]]);
    assert.deepEqual([...unreadSessionOutput(s, 'owner')], [sessionResourceKey(rows[1])]);
    trackSessionOutput(s, 'owner', [row('a', 15)]);
    assert.equal(unreadSessionOutput(s, 'owner').size, 2);
    const sessions = [{ ...row('a', 15), lifecycle: { state_source: 'claude_hook', status: 'working' } },
        { ...row('b'), lifecycle: { state_source: 'claude_hook', status: 'blocked' } }];
    const unread = unreadSessionOutput(s, 'owner');
    const groups = groupSessionInventory(sessions, [], {}, new Set(), undefined, unread);
    assert.equal(groups[0].sessions[0].wing_id, 'b');
    assert.equal(groups[0].rollup.blocked, 1);
    assert.equal(groups[0].rollup.unread, 1);
    assert.equal(groups[0].rollup.unseen, 0, 'output never invents provider completion');
    assert.equal(filterSessionInventory(sessions, [], {}, { status: 'unread', unread }).length, 2);
});
test('a stale tab cannot clear newer bytes or acknowledge a replacement execution', () => {
    const s = storage();
    trackSessionOutput(s, 'owner', [row()]);
    trackSessionOutput(s, 'owner', [row('a', 20)]);
    acknowledgeSessionOutput(s, 'owner', [row()]);
    assert.equal(unreadSessionOutput(s, 'owner').size, 1);
    trackSessionOutput(s, 'owner', [row('a', 8)]);
    acknowledgeSessionOutput(s, 'owner', [row('a', 20)]);
    assert.equal(unreadSessionOutput(s, 'owner').size, 0);
    trackSessionOutput(s, 'owner', [row('a', 2, 'new-execution')]);
    acknowledgeSessionOutput(s, 'owner', [row('a', 20)]);
    assert.equal(unreadSessionOutput(s, 'owner').size, 1);
});
test('unknown legacy cursors, absent accounts and storage failures do not fabricate unread', () => {
    const s = storage();
    trackSessionOutput(s, 'owner', [{ id: 'legacy', wing_id: 'a' }, row('a', -1), row('a', NaN), row('a', 0)]);
    assert.equal(unreadSessionOutput(s, 'owner').size, 0);
    trackSessionOutput(s, null, [row()]);
    trackSessionOutput(null, 'owner', [row()]);
    assert.equal(unreadSessionOutput(null, 'owner').size, 0);
    acknowledgeSessionOutput(null, 'owner', [row()]);
});
test('retention is bounded and does not revive expired records', () => {
    const s = storage();
    trackSessionOutput(s, 'owner', Array.from({ length: UNREAD_LIMIT + 1 }, (_, i) => ({ ...row(), id: String(i) })), 1);
    assert.equal(unreadSessionOutput(s, 'owner', 2).size, UNREAD_LIMIT);
    assert.equal(JSON.parse([...s.values.values()][0]).length, UNREAD_LIMIT);
    assert.equal(unreadSessionOutput(s, 'owner', 31 * 86400000).size, 0);
    s.setItem(UNREAD_STORAGE_PREFIX + '["bad"]', '{}');
    assert.equal(unreadSessionOutput(s, 'bad').size, 0);
});
