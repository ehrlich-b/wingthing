import test from 'node:test';
import assert from 'node:assert/strict';
import { coordinatorStatus, coordinatorStatusMarkup, coordinatorCardHeading, orderCoordinatorInventory, groupCoordinatorInventory, blockedBadge, observeCoordinatorStatus, coordinatorComposer, chatEnterSubmits, lastMessagePreview, previewReadArguments, previewFromRead } from '../src/coordinator-state.js';
import { createConversationReader } from '../src/conversation-recovery.js';
import { sessionResourceKey } from '../src/session-reference.js';

test('phone and desktop coordinator groups share qualified projects, rollups and attention sorting', () => {
    const row = (id, status, wingId = 'mac', parent = '') => ({ wing: { wing_id: wingId, wing_label: 'Same label', projects: [{ path: '/repo' }] }, depth: parent ? 1 : 0,
        task: { observedAt: 1000, lifecycle: { status, state_cursor: 10 }, conversation: { conversation_id: id, session_id: id, parent_conversation_id: parent, cwd: '/repo/src' } } });
    const rows = [row('idle-root', 'idle'), row('working-root', 'working', 'linux'), row('blocked-child', 'blocked', 'mac', 'idle-root'), row('done-child', 'done', 'mac', 'idle-root'), row('blocked-root', 'blocked', 'other')];
    const unseen = new Set([sessionResourceKey({ id: 'done-child', wing_id: 'mac' })]);
    for (const mobile of [true, false]) {
        const groups = groupCoordinatorInventory(rows, mobile, unseen, 1000);
        assert.deepEqual(groups.map(group => group.wingId), ['mac', 'other', 'linux']);
        assert.equal(groups[0].project, '/repo');
        assert.deepEqual(groups[0].rollup, { blocked: 1, working: 0, idle: 1, unseen: 1 });
        assert.deepEqual(groups[0].sessions.map(session => session.id), ['blocked-child', 'done-child', 'idle-root']);
        assert.equal(groups[0].sessions[0].row.depth, 1);
    }
    assert.match(coordinatorCardHeading({ title: 'Child' }, 'blocked', '', true), /Needs attention.*unseen completion/);
});

test('cached coordinator lifecycle never invents live rollup counts but retains unseen completion', () => {
    const row = { wing: { wing_id: 'mac' }, error: 'Offline', task: { observedAt: 1000, lifecycle: { status: 'working' }, conversation: { conversation_id: 'root', session_id: 'execution', cwd: '/repo' } } };
    const unseen = new Set([sessionResourceKey({ id: 'execution', wing_id: 'mac' })]);
    const [group] = groupCoordinatorInventory([row], true, unseen, 1000);
    assert.deepEqual(group.rollup, { blocked: 0, working: 0, idle: 0, unseen: 1 });
    assert.equal(group.sessions[0].agentStatus, 'unknown');
});

test('phone Return and IME composition keep editing while desktop Enter sends', () => {
    assert.equal(chatEnterSubmits({ key: 'Enter' }, true), false);
    assert.equal(chatEnterSubmits({ key: 'Enter', isComposing: true }, false), false);
    assert.equal(chatEnterSubmits({ key: 'Enter', shiftKey: true }, false), false);
    assert.equal(chatEnterSubmits({ key: 'a' }, false), false);
    assert.equal(chatEnterSubmits({ key: 'Enter' }, false), true);
});

test('phone inventory leads with roots across wings while desktop preserves trees', () => {
    const row = (id, parent = '', wing = 'mac') => ({ wing, task: { conversation: { conversation_id: id, parent_conversation_id: parent } } });
    const rows = [row('root-a'), row('child-a', 'root-a'), row('root-b', '', 'linux'), row('child-b', 'root-b', 'linux'), row('orphan', 'missing')];
    assert.deepEqual(orderCoordinatorInventory(rows, true).map(row => row.task.conversation.conversation_id), ['root-a', 'root-b', 'child-a', 'child-b', 'orphan']);
    assert.deepEqual(orderCoordinatorInventory(rows, false), rows);
    assert.deepEqual(orderCoordinatorInventory([], true), []);
});

test('status dots use fresh agent status and never reinterpret lifecycle state', () => {
    for (const status of ['working', 'blocked', 'idle', 'done', 'exited', 'unknown']) {
        assert.equal(coordinatorStatus({ status, state: 'idle' }, { observedAt: 1000, now: 1000 }), status);
        assert.match(coordinatorStatusMarkup(status), new RegExp('data-status="' + status + '"'));
        assert.match(coordinatorStatusMarkup(status), /aria-hidden="true"/);
    }
    for (const evidence of [{ observedAt: 0 }, { observedAt: 1001 }, { observedAt: 1000, cached: true }, { observedAt: 1000, readError: 'offline' }, { observedAt: 1000, now: 61000 }]) {
        assert.equal(coordinatorStatus({ status: 'working' }, { now: 1000, ...evidence }), 'unknown');
    }
    assert.equal(coordinatorStatus({ state: 'working' }, { observedAt: 1000, now: 1000 }), 'unknown');
    assert.equal(coordinatorStatus({ status: 'future-status' }, { observedAt: 1000, now: 1000 }), 'unknown');
});

test('blocked roots and children have visible badges and one alert per transition', () => {
    assert.equal(blockedBadge(['idle', 'blocked', 'working', 'blocked']), '2 blocked · Needs attention');
    assert.equal(blockedBadge(['idle', 'done']), '');
    assert.match(coordinatorCardHeading({ title: 'Coordinator' }, 'blocked', 'Waiting for approval'), /Needs attention/);
    assert.doesNotMatch(coordinatorCardHeading({ title: 'Coordinator' }, 'working', ''), /Needs attention/);
    const previous = new Map();
    assert.equal(observeCoordinatorStatus(previous, 'owner/mac/root', 'blocked').blocked, true);
    assert.equal(observeCoordinatorStatus(previous, 'owner/mac/root', 'blocked').blocked, false);
    observeCoordinatorStatus(previous, 'owner/mac/root', 'unknown');
    assert.equal(observeCoordinatorStatus(previous, 'owner/mac/root', 'blocked').blocked, false);
    assert.equal(observeCoordinatorStatus(previous, 'owner/mac/child', 'blocked').blocked, true);
    assert.equal(observeCoordinatorStatus(previous, 'other/mac/root', 'blocked').blocked, true);
    assert.equal(observeCoordinatorStatus(previous, 'owner/mac/root', 'working').cleared, true);
    assert.equal(observeCoordinatorStatus(previous, 'owner/mac/root', 'blocked').blocked, true);
});

test('message previews are bounded, escaped and tied to the exact execution', () => {
    const messages = [{ type: 'assistant', content: 'Earlier' }, { type: 'user', content: '  Last\nmessage  ' }, { type: 'tool_result', content: 'secret tool output' }];
    assert.equal(lastMessagePreview(messages), 'Last message');
    assert.equal(lastMessagePreview([{ type: 'assistant', content: 'x'.repeat(300) }]).length, 180);
    assert.equal(lastMessagePreview([]), '');
    const task = { conversation: { session_id: 'current' }, lifecycle: { head_cursor: 450, provider_session_id: 'provider' } };
    assert.deepEqual(previewReadArguments(task), { session: 'current', after_cursor: 350, limit: 100 });
    const lifecycle = { session_id: 'current', agent: 'claude', provider_session_id: 'provider', events: [{ type: 'message', role: 'assistant', sequence: 450, text: 'Latest answer' }] };
    assert.equal(previewFromRead(task, { lifecycle }), 'Latest answer');
    assert.equal(previewFromRead(task, { lifecycle: { ...lifecycle, session_id: 'sibling' } }), '');
    assert.equal(previewFromRead(task, { lifecycle: { ...lifecycle, provider_session_id: 'sibling' } }), '');
    assert.equal(previewFromRead(task, { lifecycle, session: 'sibling' }), '');
    const markup = coordinatorCardHeading({ title: '<script>name</script>' }, 'blocked', '<img onerror="bad">');
    assert.doesNotMatch(markup, /<script>|<img/);
    assert.match(markup, /&lt;img/);
});

test('composer presents live prompts and exact ended continuations through the unchanged reader', async () => {
    const target = { userId: 'owner', wingId: 'mac', sessionId: 'execution', conversationId: 'root', providerSessionId: 'provider' };
    for (const ended of [false, true]) {
        const calls = [], timers = [], values = new Map();
        const lifecycle = { session_id: 'execution', agent: 'claude', provider_session_id: 'provider', state: 'idle', status: ended ? 'exited' : 'idle', state_source: 'claude_hook', process_alive: !ended, ready: !ended, cursor: 0, events: [] };
        const reader = createConversationReader({
            storage: { getItem: key => values.get(key) || null, setItem: (key, value) => values.set(key, value), removeItem: key => values.delete(key) },
            randomId: () => 'saved-request', now: () => 1000, schedule: fn => { timers.push(fn); return fn; }, cancel: () => {},
            request: async (wing, payload) => {
                calls.push(payload);
                if (payload.operation !== 'session_read') throw Error('lost acknowledgement');
                return { lifecycle, headless_continuation: ended ? { available: true, source_session: 'execution', conversation_id: 'root', provider_session_id: 'provider', model: 'claude-opus-4-6' } : undefined };
            },
        });
        reader.open(target);
        assert.equal(coordinatorComposer(reader.snapshot()).mode, 'unavailable');
        timers.shift()();
        await new Promise(resolve => setImmediate(resolve));
        const composer = coordinatorComposer(reader.snapshot());
        assert.equal(composer.mode, ended ? 'continuation' : 'session_prompt');
        assert.equal(composer.label, ended ? 'Ready for follow-up' : 'Ready for a message');
        assert.equal(coordinatorComposer(reader.snapshot(), true).ready, false);
        await reader.send('Follow up');
        assert.equal(calls.at(-1).operation, ended ? 'agent_start' : 'session_prompt');
        assert.equal(calls.at(-1).arguments.request_id, 'saved-request');
        assert.equal(coordinatorComposer(reader.snapshot()).ready, false, 'unconfirmed input cannot be sent twice');
        reader.close();
        reader.open(target);
        assert.equal(coordinatorComposer(reader.snapshot()).ready, false, 'reopened cached evidence cannot enable Send');
        reader.close();
    }
    assert.equal(coordinatorComposer({ inputReady: true, continuationReady: false, lifecycle: { process_alive: false } }).ready, false);
    assert.equal(coordinatorComposer({ inputReady: true, continuationReady: true, lifecycle: { process_alive: true } }).ready, false);
});
