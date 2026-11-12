import test from 'node:test';
import assert from 'node:assert/strict';
import vm from 'node:vm';
import { moduleContext } from './support/browser-state.mjs';
import { machineState } from '../src/paper-state.js';
import { escapeMarkup } from '../src/security.js';

test('paper Send stays disabled for read-only accounts, spectators and offline wings', () => {
    const S = { currentUser: { id: 'owner' }, wingsData: [{ wing_id: 'machine', online: true }] };
    const context = moduleContext('paper', { S, machineState, esc: escapeMarkup });
    const session = { wing_id: 'machine', id: 'execution' };
    vm.runInContext("ui.inputSnapshot = { inputReady: true }; ui.inputKey = 'selected'; drafts.set(ui.inputKey, 'draft');", context);
    assert.doesNotMatch(context.composer('Message', session), /disabled/);
    for (const scenario of ['spectator', 'account', 'offline']) {
        S.spectating = scenario === 'spectator';
        S.currentUser.relay_allowed = scenario !== 'account';
        S.wingsData[0].online = scenario !== 'offline';
        if (scenario === 'offline') { assert.equal(context.composer('Message', session), ''); continue; }
        assert.match(context.composer('Message', session), /type="submit"[^>]+disabled/);
        assert.match(context.composer('Message', session), /Read only/);
    }
});


test('paper readers stop on connection loss and reopen only on the selected connected machine', () => {
    const S = { currentUser: { id: 'owner' }, wingsData: [{ wing_id: 'machine', online: false }] };
    const opened = []; let closed = 0;
    const context = moduleContext('paper', { S, machineState, esc: escapeMarkup, window: {},
        sendTunnelRequest() {}, randomUUID() {}, createConversationReader: () => ({ open: target => opened.push(target), close: () => closed++ }) });
    const session = { wing_id: 'machine', id: 'execution' };
    context.selectComposer(session);
    assert.equal(opened.length, 0);
    S.wingsData[0].online = true;
    context.selectComposer(session);
    assert.equal(opened.length, 1);
    assert.equal(opened[0].wingId, 'machine');
    assert.equal(opened[0].sessionId, 'execution');
    S.wingsData[0].online = false;
    context.selectComposer(session);
    assert.equal(closed, 1);
    assert.equal(vm.runInContext('ui.inputReader', context), null);
    context.selectComposer(session);
    assert.equal(opened.length, 1);
    S.wingsData[0].online = true;
    context.selectComposer(session);
    assert.equal(opened.length, 2);
});
