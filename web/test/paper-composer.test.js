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
    vm.runInContext("inputSnapshot = { inputReady: true }; inputKey = 'selected'; drafts.set(inputKey, 'draft');", context);
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
