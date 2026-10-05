import test from 'node:test';
import assert from 'node:assert/strict';
import { sessionInventoryState, sessionInventoryActions, filterSessionInventory, captureSessionFocus, restoreSessionFocus, navigateSessionRows, findSessionResource, sessionIsSelected, sessionResourceKey } from '../src/session-inventory.js';
import { sessionRoute, parseSessionRoute } from '../src/session-route.js';

const wing = { wing_id: 'mac', wing_label: 'Personal Mac', hostname: 'mac-mini', online: true, capabilities: ['session.rename.v1'] };
const session = { id: 'session-1', wing_id: 'mac', user_id: 'owner', agent: 'claude', name: 'release-notes', cwd: '/home/bryan/repos/wingthing', swept: true, status: 'detached' };

test('terminal attachment, silence and bell signals never invent provider completion', () => {
    for (const status of ['active', 'detached', 'idle', 'completed']) {
        const state = sessionInventoryState({ ...session, status, idle_seconds: 1000 }, wing, true);
        assert.equal(state.state, 'unknown');
        assert.equal(state.agentLabel, 'agent state unknown · attention signal');
        assert.equal(state.attachment, status === 'active' ? 'attached' : 'detached');
    }
    assert.equal(sessionInventoryState({ ...session, lifecycle: { state: 'completed', state_source: 'terminal_idle' } }, wing).state, 'unknown');
});

test('provider hooks report agent state independently of detached attachment', () => {
    const state = sessionInventoryState({ ...session, lifecycle: { state: 'needs_input', state_source: 'claude_hook' } }, wing);
    assert.equal(state.state, 'needs_input');
    assert.equal(state.agentLabel, 'needs input');
    assert.equal(state.attention, true);
    assert.equal(state.attachment, 'detached');
    assert.equal(state.canAttach, true);
});

test('disconnects disable attachment and label provider events as last reported', () => {
    const running = { ...session, lifecycle: { state: 'working', state_source: 'claude_transcript' } };
    const offline = sessionInventoryState(running, { ...wing, online: false });
    assert.equal(offline.connection, 'offline');
    assert.equal(offline.agentLabel, 'last reported: working');
    assert.equal(offline.canAttach, false);
    assert.equal(sessionInventoryState(session, { ...wing, tunnel_error: 'passkey_required' }).connection, 'locked');
    assert.equal(sessionInventoryState(session, { ...wing, tunnel_error: 'unreachable' }).connection, 'unreachable');
    assert.equal(sessionInventoryState({ ...session, swept: false }, wing).connection, 'checking');
    assert.equal(sessionInventoryState(session, undefined).connection, 'checking');
});

test('row controls preserve visible-session stopping and exact-owner renaming', () => {
    assert.deepEqual(sessionInventoryActions(session, wing, { id: 'owner' }), { attach: true, rename: true, stop: true, unavailableReason: '' });
    const observer = sessionInventoryActions(session, wing, { id: 'observer' });
    assert.equal(observer.attach, true);
    assert.equal(observer.rename, false);
    assert.equal(observer.stop, true);
    assert.equal(sessionInventoryActions(session, wing, null).stop, false);
    assert.equal(sessionInventoryActions(session, { ...wing, capabilities: [] }, { id: 'owner' }).rename, false);
    const offline = sessionInventoryActions(session, { ...wing, online: false }, { id: 'owner' });
    assert.equal(offline.attach, false);
    assert.equal(offline.rename, false);
    assert.equal(offline.stop, false);
});

test('search combines name, exact wing, agent and workspace terms without changing order', () => {
    const second = { ...session, id: 'session-2', wing_id: 'linux', name: 'review', agent: 'codex', cwd: '/srv/other' };
    const wings = [wing, { wing_id: 'linux', hostname: 'wsl', online: false }];
    const sessions = [session, second];
    assert.deepEqual(filterSessionInventory(sessions, wings, {}, { query: 'PERSONAL wingthing' }), [session]);
    assert.deepEqual(filterSessionInventory(sessions, wings, {}, { query: 'wsl', agent: 'codex', wing: 'linux', status: 'offline' }), [second]);
    assert.deepEqual(filterSessionInventory(sessions, wings, { [sessionResourceKey(session)]: true }, { status: 'attention' }), [session]);
    assert.deepEqual(filterSessionInventory(sessions, wings, {}, { status: 'available' }), [session]);
    assert.deepEqual(filterSessionInventory(sessions, wings, {}, { query: 'release-notes review' }), []);
    assert.deepEqual(sessions, [session, second]);
});

test('semantic needs-input participates in attention filtering without a terminal bell', () => {
    const child = { ...session, conversation_role: 'child', lifecycle: { state: 'needs_input', state_source: 'claude_hook' } };
    assert.deepEqual(filterSessionInventory([child], [wing], {}, { query: 'child', status: 'attention' }), [child]);
});

test('refresh restores focused action on its exact wing, including IDs unsafe in selectors', () => {
    const id = 'session-"[remote]';
    let focused;
    const action = { dataset: { sessionAction: 'stop' }, disabled: false, focus: () => { focused = 'stop'; } };
    const row = { dataset: { sid: id, wingId: 'mac' }, querySelectorAll: () => [action], focus: () => { focused = 'row'; } };
    const wrongWing = { ...row, dataset: { sid: id, wingId: 'linux' }, focus: () => { focused = 'wrong'; }, querySelectorAll: () => [] };
    action.closest = () => row;
    const container = { contains: () => true, querySelectorAll: () => [wrongWing, row] };
    const focus = captureSessionFocus(container, action);
    restoreSessionFocus(container, focus);
    assert.equal(focused, 'stop');
    action.disabled = true;
    restoreSessionFocus(container, focus);
    assert.equal(focused, 'row');
    assert.equal(captureSessionFocus({ contains: () => false }, action), null);
});

test('keyboard navigation respects list edges and does not consume terminal input keys', () => {
    let focused = -1;
    let prevented = 0;
    const rows = [0, 1, 2].map(index => ({ focus: () => { focused = index; } }));
    const press = (key, row) => navigateSessionRows({ key, preventDefault: () => { prevented++; } }, rows, rows[row]);
    assert.equal(press('ArrowDown', 0), true);
    assert.equal(focused, 1);
    press('End', 0);
    assert.equal(focused, 2);
    press('ArrowDown', 2);
    assert.equal(focused, 2);
    press('Home', 2);
    assert.equal(focused, 0);
    press('ArrowUp', 0);
    assert.equal(focused, 0);
    assert.equal(press('x', 0), false);
    assert.equal(prevented, 5);
});

test('equal session IDs on two wings resolve actions and selection to the exact wing', () => {
    const other = { ...session, wing_id: 'wsl', user_id: 'someone-else' };
    const sessions = [session, other];
    assert.equal(findSessionResource(sessions, session.id), null);
    assert.equal(findSessionResource(sessions, session.id, 'missing-wing'), null);
    assert.equal(findSessionResource(sessions, session.id, 'wsl'), other);
    assert.equal(sessionIsSelected(session, session.id, 'wsl'), false);
    assert.equal(sessionIsSelected(other, session.id, 'wsl'), true);
    assert.equal(sessionIsSelected(other, session.id, undefined), false);
    assert.notEqual(sessionResourceKey(session), sessionResourceKey(other));
    const actionTarget = findSessionResource(sessions, session.id, 'wsl');
    assert.equal(sessionInventoryActions(actionTarget, { ...wing, wing_id: 'wsl' }, { id: 'owner' }).rename, false);
    assert.equal(sessionInventoryActions(findSessionResource(sessions, session.id, 'mac'), wing, { id: 'owner' }).rename, true);
});

test('qualified links survive reload/history and legacy links remain readable', () => {
    const route = sessionRoute('session-"[remote]', 'wsl:private');
    assert.deepEqual(parseSessionRoute(route), { sessionId: 'session-"[remote]', wingId: 'wsl:private' });
    assert.deepEqual(parseSessionRoute('#s/legacy'), { sessionId: 'legacy', wingId: undefined });
    assert.equal(parseSessionRoute('#s/'), null);
    assert.equal(parseSessionRoute('#s/%broken'), null);
    assert.equal(parseSessionRoute('#w/wing'), null);
});

test('removed focused rows recover focus to the nearest remaining row', () => {
    let focused = '';
    const row = { dataset: { sid: 'remaining', wingId: 'mac' }, focus: () => { focused = 'remaining'; } };
    assert.equal(restoreSessionFocus({ querySelectorAll: () => [row] }, { id: 'gone', wingId: 'wsl', action: 'stop', index: 2 }), true);
    assert.equal(focused, 'remaining');
    assert.equal(restoreSessionFocus({ querySelectorAll: () => [] }, { id: 'gone', wingId: 'wsl' }), false);
});
