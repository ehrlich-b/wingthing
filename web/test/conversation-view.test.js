import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';
import { createNavigationGuard, taskRootId, mergeRootRead, mergeWingListing } from '../src/conversation-recovery.js';
import { coordinatorStatus, observeCoordinatorStatus, lastMessagePreview, previewReadArguments, previewMessagesFromRead } from '../src/coordinator-state.js';
import { sessionIsViewed } from '../src/session-inventory.js';
import { sessionResourceKey } from '../src/session-reference.js';
import { trackSessionCompletions, sessionCompletionObservation, unseenSessionCompletions } from '../src/session-completion.js';

const source = readFileSync(new URL('../src/conversation-view.js', import.meta.url), 'utf8')
    .replace(/^import .*;\n/gm, '').replace(/^export /gm, '');

function conversationHarness() {
    const values = new Map(), notifications = [], cleared = [];
    const storage = { getItem: key => values.get(key) || null, setItem: (key, value) => values.set(key, value), removeItem: key => values.delete(key) };
    const context = vm.createContext({
        S: { currentUser: { id: 'owner' }, activeView: 'terminal', wingsData: [{ wing_id: 'mac', capabilities: ['conversation.personal.v1'] }] },
        window: { localStorage: storage, addEventListener() {}, dispatchEvent() {} },
        document: { visibilityState: 'visible', getElementById() { return null; } },
        TextEncoder,
        setTimeout() { return 1; }, clearTimeout() {},
        chatSnapshot: () => ({ target: { userId: 'owner', wingId: 'mac', sessionId: 'root-session' } }),
        chatSelectionVersion: () => 1,
        createNavigationGuard, taskRootId, mergeRootRead, mergeWingListing,
        coordinatorStatus, observeCoordinatorStatus, lastMessagePreview, previewReadArguments, previewMessagesFromRead,
        sessionIsViewed, trackSessionCompletions, sessionCompletionObservation,
        readTree: () => ({ tasks: [], deliveries: {} }), saveTree() {},
        setNotification: (...args) => notifications.push(args), clearNotification: (...args) => cleared.push(args),
    });
    vm.runInContext(source, context, { filename: 'conversation-view.js' });
    return { context, storage, notifications, cleared };
}

function task(id, status, cursor = 1) {
    return { conversation: { conversation_id: id, root_conversation_id: 'root', parent_conversation_id: id === 'root' ? '' : 'root', session_id: id + '-session' },
        lifecycle: { session_id: id + '-session', status, state_cursor: cursor } };
}

test('panel refresh records unseen child completions and blocked alerts from stamped merged tasks', async () => {
    const h = conversationHarness(), { context } = h;
    const state = { wingId: 'mac', rootId: 'root', conversation: task('root', 'working').conversation, version: 1 };
    context.panel = state;
    let tasks = [task('root', 'working'), task('child', 'working')];
    context.readConversationTree = async () => ({ tasks });
    context.renderPanelState = () => {};
    await context.refreshPanelTree(state);
    tasks = [task('root', 'working'), task('child', 'idle', 2), task('blocked', 'blocked')];
    await context.refreshPanelTree(state);
    const unseen = unseenSessionCompletions(h.storage, 'owner');
    assert.equal(unseen.has(sessionResourceKey({ id: 'child-session', wing_id: 'mac' })), true);
    assert.equal(unseen.has(sessionResourceKey({ id: 'root-session', wing_id: 'mac' })), false);
    assert.equal(h.notifications.length, 1);
    assert.equal(h.notifications[0][0], 'blocked-session');
    assert.equal(h.notifications[0][1], 'mac');
    assert.equal(h.notifications[0][2].rootConversationId, 'root');
    await context.refreshPanelTree(state);
    assert.equal(h.notifications.length, 1, 'unchanged blocking cannot notify twice');
    tasks = [task('root', 'working'), task('child', 'done', 3), task('blocked', 'working', 2)];
    await context.refreshPanelTree(state);
    assert.deepEqual(h.cleared, [['blocked-session', 'mac']]);
});

function previewTasks(h, count, content = 'Recent answer') {
    const tasks = Array.from({ length: count }, (_, index) => task('preview-' + index, 'idle'));
    tasks.forEach(task => { task.lifecycle.head_cursor = 1; });
    h.context.cacheWingTree('mac', { tasks, deliveries: {}, observedAt: Date.now() });
    h.context.sendTunnelRequest = async (_wingId, payload) => ({ lifecycle: {
        session_id: payload.arguments.session, head_cursor: 1,
        events: [{ type: 'message', role: 'assistant', sequence: 1, text: content }],
    } });
    return tasks;
}

function previewBytes(previews) {
    return [...previews].reduce((total, [key, value]) => total + Buffer.byteLength(key + JSON.stringify(value)), 0);
}

test('preview cache bounds entry count and retains recently refreshed executions', async () => {
    const h = conversationHarness(), { context } = h, tasks = previewTasks(h, 33);
    for (const task of tasks.slice(0, 32)) await context.refreshRootPreview('mac', task);
    tasks[0].lifecycle.head_cursor++;
    await context.refreshRootPreview('mac', tasks[0]);
    await context.refreshRootPreview('mac', tasks[32]);
    assert.equal(context.previews.size, 32);
    assert.equal(context.previews.has(context.previewKey('mac', tasks[0].conversation.session_id)), true);
    assert.equal(context.previews.has(context.previewKey('mac', tasks[1].conversation.session_id)), false);
    assert.equal(context.previews.has(context.previewKey('mac', tasks[32].conversation.session_id)), true);
});

test('preview cache bounds UTF-8 bytes and rejects an oversized transcript without evicting other entries', async () => {
    const h = conversationHarness(), { context } = h, tasks = previewTasks(h, 8, '🙂'.repeat(12000));
    for (const task of tasks) await context.refreshRootPreview('mac', task);
    assert.ok(previewBytes(context.previews) <= 256 * 1024);
    assert.ok(context.previews.size < tasks.length);
    assert.equal(context.previews.has(context.previewKey('mac', tasks[7].conversation.session_id)), true);
    const before = [...context.previews.keys()];
    context.sendTunnelRequest = async (_wingId, payload) => ({ lifecycle: { session_id: payload.arguments.session,
        events: [{ type: 'message', role: 'assistant', sequence: 1, text: '🙂'.repeat(65536) }] } });
    tasks[0].lifecycle.head_cursor++;
    await context.refreshRootPreview('mac', tasks[0]);
    assert.deepEqual([...context.previews.keys()], before);
});

test('root and full inventory updates evict obsolete executions while preserving other wings', async () => {
    const h = conversationHarness(), { context } = h, [oldTask] = previewTasks(h, 1);
    context.S.wingsData.push({ wing_id: 'linux', capabilities: ['conversation.personal.v1'] });
    context.cacheWingTree('linux', { tasks: [oldTask], deliveries: {}, observedAt: Date.now() });
    await context.refreshRootPreview('mac', oldTask);
    await context.refreshRootPreview('linux', oldTask);
    const next = { ...oldTask, conversation: { ...oldTask.conversation, session_id: 'continued' }, lifecycle: { ...oldTask.lifecycle, session_id: 'continued' } };
    context.mergeRootTree('mac', 'root', [next], null, Date.now());
    assert.equal(context.previews.has(context.previewKey('mac', oldTask.conversation.session_id)), false);
    assert.equal(context.previews.has(context.previewKey('linux', oldTask.conversation.session_id)), true);
    await context.refreshRootPreview('mac', next);
    const merged = mergeWingListing(context.cachedWingTree('mac'), [], Date.now());
    context.cacheWingTree('mac', { ...merged, observedAt: Date.now() });
    assert.equal(context.previews.has(context.previewKey('mac', next.conversation.session_id)), false);
    assert.equal(context.previews.has(context.previewKey('linux', oldTask.conversation.session_id)), true);
});

test('a late preview response cannot restore an obsolete execution or cross an account change', async () => {
    for (const change of ['execution', 'user', 'wing']) {
        const h = conversationHarness(), { context } = h, [task] = previewTasks(h, 1);
        let finish;
        context.sendTunnelRequest = () => new Promise(resolve => { finish = resolve; });
        const pending = context.refreshRootPreview('mac', task);
        if (change === 'user') context.S.currentUser = { id: 'other' };
        else if (change === 'wing') context.S.wingsData = [];
        else context.cacheWingTree('mac', { tasks: [], deliveries: {}, observedAt: Date.now() });
        finish({ lifecycle: { session_id: task.conversation.session_id, events: [{ type: 'message', role: 'assistant', sequence: 1, text: 'Old transcript' }] } });
        await pending;
        assert.equal(context.previews.size, 0);
    }
});

test('inventory refresh evicts previews when their wing disappears', async () => {
    const h = conversationHarness(), { context } = h, [task] = previewTasks(h, 1);
    await context.refreshRootPreview('mac', task);
    assert.equal(context.previews.size, 1);
    context.document.getElementById = () => ({ innerHTML: '' });
    context.S.wingsData = [];
    await context.refreshConversationInventory();
    assert.equal(context.previews.size, 0);
});
