import test from 'node:test';
import assert from 'node:assert/strict';
import { stopSessionVerified } from '../src/session-stop.js';
const session = { id: 'same-id', wing_id: 'a' };
test('stop verifies exit and fresh inventory without repeating or changing the qualified mutation', async () => {
    const calls = []; let reads = 0;
    const result = await stopSessionVerified(session, async (wing, payload) => {
        calls.push({ wing, ...payload });
        return payload.type === 'pty.kill' ? { process_exited: true } : { sessions: ++reads === 1 ? [{ session_id: session.id }] : [] };
    }, { wait: async () => {} });
    assert.equal(result.process_exited, true);
    assert.deepEqual(calls, [{ wing: 'a', type: 'pty.kill', session_id: 'same-id' },
        { wing: 'a', type: 'sessions.list' }, { wing: 'a', type: 'sessions.list' }]);
});
test('signal ack, missing inventory, request denial and still-live inventory never report success', async () => {
    for (const mode of ['legacy', 'missing', 'denied', 'live']) {
        let kills = 0;
        await assert.rejects(stopSessionVerified(session, async (_wing, payload) => {
            if (payload.type === 'pty.kill') {
                kills++;
                if (mode === 'denied') throw new Error('not allowed');
                return mode === 'legacy' ? { ok: 'true' } : { process_exited: true };
            }
            return mode === 'missing' ? {} : { sessions: [{ session_id: session.id }] };
        }, { attempts: 2, wait: async () => {} }));
        assert.equal(kills, 1);
    }
});
