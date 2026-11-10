import test from 'node:test';
import assert from 'node:assert/strict';
import { moduleContext } from './support/browser-state.mjs';
import { machineState, machineLabel, sessionState, requestedCommand, paperRoute } from '../src/paper-state.js';
import { restoredConversationState } from '../src/conversation-state.js';
import { escapeMarkup } from '../src/security.js';

const setup = () => {
    const S = {
        currentUser: { id: 'owner' },
        wingsData: [{ wing_id: 'a', hostname: 'Design computer', online: true }, { wing_id: 'b', hostname: 'Research computer', online: false }],
        sessionsData: [
            { wing_id: 'a', id: 'same/id', name: 'Release signing', needs_attention: true, lifecycle: { status: 'blocked', reason: 'Allow access to signing keys until 4 PM? Your answer grants access to this directory.' } },
            { wing_id: 'b', id: 'same/id', name: 'Data migration', needs_attention: true, lifecycle: { status: 'blocked' } },
        ],
    };
    const context = moduleContext('paper', { S, machineState, machineLabel, stateOf: sessionState, requestedCommand, route: paperRoute, restoredConversationState, esc: escapeMarkup, sendTunnelRequest: () => new Promise(() => {}) });
    return { S, context };
};

test('requests retain their permission effect and expiry, with machine-qualified answer links', () => {
    const { S, context } = setup();
    const row = context.needRow(S.sessionsData[0]);
    assert.match(row, /#approve\/a\/same%2Fid/);
    assert.match(row, /Allow access to signing keys until 4 PM\? Your answer grants access to this directory\./);
    assert.match(row, /Design computer/);
    assert.match(row, /Answer →/);
    assert.doesNotMatch(row, /polling|refresh interval|caching|protocol|state ID/);
});

test('offline requests remain discoverable and missing text has one brief fallback', () => {
    const { S, context } = setup();
    assert.equal(context.needing().length, 2);
    const row = context.needRow(S.sessionsData[1]);
    assert.match(row, /#approve\/b\/same%2Fid/);
    assert.match(row, /Research computer · Offline/);
    assert.match(row, /Request unavailable\. Check the terminal\./);
    assert.equal(context.findSession({ wingId: 'b', sessionId: 'same/id' }), S.sessionsData[1]);
});

test('session and request text cannot inject markup into waiting rows', () => {
    const { S, context } = setup();
    Object.assign(S.sessionsData[0], { name: '<img src=x onerror=alert(1)>', lifecycle: { reason: '"><script>alert(1)</script>' } });
    const row = context.needRow(S.sessionsData[0]);
    assert.doesNotMatch(row, /<img|<script/);
    assert.match(row, /&lt;script&gt;/);
});

test('attention navigation leads to a focusable home summary or the complete waiting list', () => {
    assert.deepEqual(paperRoute('#needs'), { view: 'wing' });
    assert.deepEqual(paperRoute('#decisions'), { view: 'wing' });
});
