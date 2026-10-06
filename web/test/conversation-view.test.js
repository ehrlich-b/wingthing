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
