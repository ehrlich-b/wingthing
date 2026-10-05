import test from 'node:test';
import assert from 'node:assert/strict';
import { PARENT_DOT_FRESH_MS, parentDotStorageKey, selectParentConversation, readParentSelection, saveParentSelection, activateParentSelection, parentDotPresentation } from '../src/parent-dot-state.js';
import { reconcileWingSessions } from '../src/session-merge.js';

const root = { userId: 'owner', wingId: 'mac', conversationId: 'logical-parent', title: 'Weekend parent' };
const online = [{ wing_id: 'mac', online: true }, { wing_id: 'linux', online: true }];
const now = 100000;
const working = { state: 'working', state_source: 'claude_hook', process_alive: true };
const rootSession = { id: 'same-provider-id', user_id: 'owner', wing_id: 'mac', conversation_id: root.conversationId, lifecycle: working, lifecycle_seen_at: now };
function memoryStorage() {
    const values = new Map();
    return { getItem: key => values.get(key) || null, setItem: (key, value) => values.set(key, value) };
}

test('one user-qualified logical parent survives reload, child selection preserves its title, click resolves its current execution', async () => {
    const storage = memoryStorage(), opened = [];
    assert.equal(saveParentSelection(storage, 'owner', root), true);
    let selected = readParentSelection(storage, 'owner').selection;
    selected = selectParentConversation('owner', selected, { wingId: 'mac', conversationId: 'logical-parent' }); // Child-open event carries its root.
    assert.equal(selected.title, 'Weekend parent');
    assert.equal(saveParentSelection(storage, 'owner', selected), true);
    const restored = readParentSelection(storage, 'owner').selection;
    await activateParentSelection(restored, 'owner', reference => opened.push(reference), () => assert.fail('Unexpected chooser'));
    assert.deepEqual(opened, [{ wingId: 'mac', conversationId: 'logical-parent' }]);
    assert.equal('sessionId' in opened[0], false);
    assert.equal(readParentSelection(storage, 'another-owner').selection, null);
    storage.setItem(parentDotStorageKey('another-owner'), JSON.stringify(root));
    assert.equal(readParentSelection(storage, 'another-owner').selection, null);
    let chose = false;
    activateParentSelection(restored, 'another-owner', () => assert.fail('Cross-user open'), () => { chose = true; });
    assert.equal(chose, true);
    assert.equal(selectParentConversation('owner', selected, { wingId: 'linux', conversationId: root.conversationId }).title, '');
});

test('bad or unavailable browser storage never guesses a root or reports persistence success', () => {
    const broken = { getItem() { throw Error('disabled'); }, setItem() { throw Error('disabled'); } };
    assert.equal(readParentSelection(broken, 'owner').selection, null);
    assert.match(readParentSelection(broken, 'owner').error, /could not be restored/);
    assert.equal(saveParentSelection(broken, 'owner', root), false);
    assert.equal(saveParentSelection(memoryStorage(), 'other', root), false);
    const malformed = memoryStorage();
    malformed.setItem(parentDotStorageKey('owner'), JSON.stringify({ userId: 'owner', conversationId: 'unqualified' }));
    assert.equal(readParentSelection(malformed, 'owner').selection, null);
    assert.equal(selectParentConversation('owner', null, { wingId: '', conversationId: 'root' }), null);
});

test('dot ignores sibling wings, child attention and stale persisted lifecycle', () => {
    const child = { ...rootSession, id: 'child', conversation_id: 'child-logical', root_conversation_id: root.conversationId, parent_conversation_id: root.conversationId, lifecycle: { ...working, state: 'needs_input' } };
    const otherWing = { ...rootSession, wing_id: 'linux', lifecycle: { ...working, state: 'failed' } };
    assert.equal(parentDotPresentation(root, online, [child, otherWing], null, now).state, 'unknown');
    assert.equal(parentDotPresentation(root, online, [child, otherWing, rootSession], null, now).state, 'working');
    for (const session of [{ ...rootSession, lifecycle_seen_at: undefined }, { ...rootSession, lifecycle_seen_at: now - PARENT_DOT_FRESH_MS }, { ...rootSession, lifecycle_seen_at: now + 1 }, { ...rootSession, user_id: 'other' }, { ...rootSession, lifecycle: { state: 'working', state_source: 'unsupported' } }]) {
        assert.equal(parentDotPresentation(root, online, [session], null, now).state, 'unknown');
    }
    assert.equal(parentDotPresentation(root, online, [rootSession, { ...rootSession, id: 'another-execution' }], null, now).state, 'unknown');
});

test('typed read pins the current execution and fresh native state, cached read never lights dot', () => {
    const task = { conversation: { conversation_id: root.conversationId, wing_id: 'mac', session_id: 'current-execution', title: 'Resumed parent' }, lifecycle: { ...working, state: 'needs_input' } };
    const evidence = { task, observedAt: now, error: '' };
    assert.deepEqual(parentDotPresentation(root, online, [rootSession], evidence, now), { state: 'needs_input', label: 'needs input', title: 'Resumed parent' });
    assert.equal(parentDotPresentation(root, online, [rootSession], { ...evidence, observedAt: 0 }, now).state, 'unknown'); // Older execution must not win.
    assert.equal(parentDotPresentation(root, online, [], { ...evidence, error: 'offline' }, now).state, 'unknown');
    assert.equal(parentDotPresentation(root, online, [], { ...evidence, task: { ...task, conversation: { ...task.conversation, wing_id: 'linux' } } }, now).state, 'unknown');
    assert.equal(parentDotPresentation(root, online, [], { ...evidence, task: { ...task, conversation: { ...task.conversation, parent_conversation_id: 'other-root' } } }, now).state, 'unknown');
    assert.equal(parentDotPresentation(root, [{ wing_id: 'mac', online: false }], [rootSession], evidence, now).state, 'offline');
    assert.equal(parentDotPresentation(root, [{ wing_id: 'mac', online: true, tunnel_error: 'unreachable' }], [rootSession], evidence, now).state, 'unavailable');
    assert.equal(parentDotPresentation(root, [], [rootSession], evidence, now).label, 'checking');
    assert.equal(parentDotPresentation(root, online, [{ ...rootSession, lifecycle: { ...working, process_alive: false } }], null, now).state, 'archived');
});

test('fresh inventory reconciliation updates only the exact wing and invalidates old freshness when absent', () => {
    const mac = { ...rootSession, lifecycle_seen_at: 1 }, linux = { ...rootSession, wing_id: 'linux', lifecycle_seen_at: 2 };
    let sessions = reconcileWingSessions([mac, linux], 'mac', [{ ...rootSession, lifecycle_seen_at: now }]);
    assert.equal(sessions[0].lifecycle_seen_at, now);
    assert.equal(sessions[1].lifecycle_seen_at, 2);
    assert.equal(parentDotPresentation(root, online, sessions, null, now).state, 'working');
    sessions = reconcileWingSessions(sessions, 'mac', [{ ...rootSession, lifecycle_seen_at: undefined }]);
    assert.equal(parentDotPresentation(root, online, sessions, null, now).state, 'unknown');
});

test('provider exits archive the parent; only a live native foreground turn can be completed or ready', () => {
    const present = lifecycle => parentDotPresentation(root, online, [{ ...rootSession, lifecycle }], null, now);
    assert.equal(present({ state: 'completed', state_source: 'egg_process', process_alive: false }).state, 'archived');
    assert.equal(present({ state: 'completed', state_source: 'claude_hook', process_alive: false }).state, 'archived');
    assert.equal(present({ state: 'completed', state_source: 'claude_transcript', process_alive: false }).state, 'archived');
    assert.equal(present({ state: 'unknown', state_source: 'unsupported', process_alive: false }).state, 'archived');
    assert.equal(present({ state: 'failed', state_source: 'egg_process', process_alive: false }).state, 'failed');
    assert.equal(present({ state: 'completed', state_source: 'claude_hook', process_alive: true }).state, 'completed');
    assert.equal(present({ state: 'idle', state_source: 'claude_hook', process_alive: true }).label, 'ready');
    assert.equal(present({ state: 'completed', state_source: 'egg_process', process_alive: true }).state, 'unknown');
    assert.equal(present({ state: 'idle', state_source: 'claude_hook' }).state, 'unknown');
});

test('current task read errors suppress session-label fallback; unrelated history and wings cannot suppress live native evidence', () => {
    const task = { conversation: { conversation_id: root.conversationId, wing_id: 'mac', session_id: rootSession.id }, lifecycle: working };
    const evidence = { task, observedAt: now - 1, error: '' };
    for (const failure of [{ ...evidence, error: 'Connection interrupted', observedAt: 0 }, { ...evidence, readError: 'Native lifecycle unavailable' }, { ...evidence, task: { ...task, lifecycle_error: 'Native read failed' } }]) {
        assert.equal(parentDotPresentation(root, online, [rootSession], failure, now).state, 'unknown');
    }
    const historyOnly = { ...evidence, readError: '', error: 'Some earlier execution history is unavailable.', task: { ...task, history_unavailable: true } };
    assert.equal(parentDotPresentation(root, online, [rootSession], historyOnly, now).state, 'working');
    assert.equal(parentDotPresentation(root, online, [], historyOnly, now).state, 'working');
    const otherWingError = { ...evidence, readError: 'Connection interrupted', task: { ...task, conversation: { ...task.conversation, wing_id: 'linux' } } };
    assert.equal(parentDotPresentation(root, online, [rootSession], otherWingError, now).state, 'working');
    const childError = { ...evidence, readError: 'Connection interrupted', task: { ...task, conversation: { ...task.conversation, parent_conversation_id: root.conversationId } } };
    assert.equal(parentDotPresentation(root, online, [rootSession], childError, now).state, 'working');
    assert.equal(parentDotPresentation(root, online, [rootSession], { reference: root, readError: 'Open failed' }, now).state, 'unknown');
    assert.equal(parentDotPresentation(root, online, [rootSession], { reference: { ...root, wingId: 'linux' }, readError: 'Open failed' }, now).state, 'working');
    assert.equal(parentDotPresentation(root, online, [rootSession], { reference: { ...root, userId: 'other' }, readError: 'Open failed' }, now).state, 'working');
});
