import test from 'node:test';
import assert from 'node:assert/strict';
import { sessionForkAvailable, sessionForkControl } from '../src/session-fork.js';
import { reconcileWingSessions } from '../src/session-merge.js';

const owner = { id: 'alice' };
const session = { id: 'source', agent: 'claude', user_id: 'alice', forkable: true };
const wing = { online: true, capabilities: ['session.fork.v1'] };

test('Fork is visible for owned supported live and ended Claude sessions', () => {
    for (const source of [session, { ...session, session_id: 'ended', id: undefined }]) {
        assert.equal(sessionForkAvailable(source, wing, owner), true);
        assert.match(sessionForkControl(source, wing, owner), /title="Start a new session from a copy of this conversation\. This session keeps running\.">fork<\/button>/);
    }
});

test('Fork is hidden for unsupported agents, owners, policies and old or offline wings', () => {
    for (const [source, host, user] of [
        [{ ...session, agent: 'codex' }, wing, owner],
        [{ ...session, agent: 'ollama' }, wing, owner],
        [{ ...session, forkable: false }, wing, owner],
        [{ ...session, forkable: undefined }, wing, owner],
        [{ ...session, user_id: 'bob' }, wing, owner],
        [{ ...session, user_id: undefined }, wing, owner],
        [session, wing, null],
        [session, { ...wing, capabilities: [] }, owner],
        [session, { ...wing, online: false }, owner],
        [session, { ...wing, online: undefined }, owner],
        [session, { ...wing, tunnel_error: 'not_allowed' }, owner],
    ]) {
        assert.equal(sessionForkAvailable(source, host, user), false);
        assert.equal(sessionForkControl(source, host, user), '');
    }
});

test('inventory refresh revokes stale fork eligibility without changing another wing', () => {
    const other = { ...session, wing_id: 'other' };
    const result = reconcileWingSessions([{ ...session, wing_id: 'wing' }, other], 'wing', [{ ...session, forkable: false }]);
    assert.equal(result[0].forkable, false);
    assert.equal(result[1].forkable, true);
    assert.equal(sessionForkControl(result[0], wing, owner), '');
});
