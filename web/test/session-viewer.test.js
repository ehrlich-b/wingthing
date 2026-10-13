import test from 'node:test';
import assert from 'node:assert/strict';
import { createSessionViewer } from '../src/session-viewer-state.js';
import { sessionPromptStore } from '../src/session-viewer-prompts.js';

function fixture() {
    const clock = Date.now();
    const route = (wing_id, kind = 'direct') => ({ wing_id, kind, healthy: true, observed_at: new Date(clock).toISOString(), machine_label: 'Laptop' });
    const rows = [
        { origin_wing_id: 'desktop', session_id: 'same-full-session-id', live: true, routes: [route('desktop'), route('laptop', 'via')] },
        { origin_wing_id: 'laptop', session_id: 'same-full-session-id', live: true, routes: [route('laptop')] },
    ];
    const wings = [{ wing_id: 'desktop', online: true }, { wing_id: 'laptop', online: true }];
    let user = { id: 'owner' }, counter = 0;
    const calls = [], updates = [];
    const memory = new Map();
    const prompts = sessionPromptStore({ getItem: key => memory.get(key), setItem: (key, value) => memory.set(key, value), removeItem: key => memory.delete(key) });
    let reply = async (wing, envelope) => {
        const a = envelope.arguments;
        const qualified = { origin_wing_id: a.origin_wing_id, session_id: a.session_id, route: route(wing, wing === a.origin_wing_id ? 'direct' : 'via') };
        if (envelope.operation.startsWith('fleet_')) return { ...qualified, attachment_id: a.attachment_id, ok: true, screen: 'Real terminal output', read_only: false };
        if (a.operation === 'session_read') return { ...qualified, result: { lifecycle: { events: [], cursor: a.arguments.after_cursor, has_more: false, state: 'idle' } } };
        if (a.operation === 'terminal_read') return { ...qualified, result: { ansi: 'Terminal snapshot' } };
        return { ...qualified, result: { receipt: { session_id: a.session_id, request_id: a.arguments.request_id, native_receipt_observed: true } } };
    };
    const deps = { inventory: async () => ({ sessions: rows }), request: async (...args) => { calls.push(args); return reply(...args); }, user: () => user, wings: () => wings, update: value => updates.push(value), uuid: () => `attachment-0000000${++counter}`, now: () => clock, prompts };
    return { viewer: createSessionViewer(deps), deps, rows, wings, calls, updates, prompts, setReply: fn => { reply = fn; }, switchOwner: () => { user = { id: 'other' }; } };
}

test('qualified viewer chooses direct, falls back before dispatch and never acts on a colliding origin', async () => {
    const f = fixture();
    await f.viewer.open(f.rows[0]);
    assert.equal(f.viewer.current().route.kind, 'direct');
    assert.equal(f.calls[0][1].arguments.origin_wing_id, 'desktop');
    f.wings[0].online = false;
    await f.viewer.open(f.rows[0]);
    assert.equal(f.viewer.current().route.kind, 'via');
    await f.viewer.sendMessage('Check the new DNS record');
    const mutation = f.calls.find(([, envelope]) => envelope.arguments.operation === 'session_prompt');
    assert.equal(mutation[0], 'laptop');
    assert.equal(mutation[1].arguments.origin_wing_id, 'desktop');
    assert.equal(mutation[1].arguments.session_id, 'same-full-session-id');
    assert.equal(f.viewer.current().receipt.native_receipt_observed, true);
});

test('takeover pins one capability and hand-back releases it without a stop or repeat', async () => {
    const f = fixture();
    await f.viewer.open(f.rows[0]);
    await f.viewer.takeControl();
    const capability = f.viewer.current().attachment;
    await f.viewer.terminalInput('typed once\r');
    await f.viewer.handBack();
    assert.equal(f.viewer.current().mode, 'observe');
    assert.equal(f.viewer.current().attachment, null);
    const attachmentCalls = f.calls.filter(([, envelope]) => envelope.operation.startsWith('fleet_'));
    assert.deepEqual(attachmentCalls.map(([, envelope]) => envelope.operation), ['fleet_attach', 'fleet_input', 'fleet_detach']);
    assert.ok(attachmentCalls.every(([, envelope]) => envelope.arguments.attachment_id === capability));
    assert.equal(attachmentCalls[0][1].arguments.takeover, true);
    assert.equal(f.calls.some(([, envelope]) => envelope.arguments.operation === 'terminal_stop'), false);
});

test('uncertain prompt persists through reopen and explicit reconciliation uses the original immutable ID and input', async () => {
    const f = fixture();
    await f.viewer.open(f.rows[0]);
    f.setReply(async () => { throw Error('connection lost after dispatch'); });
    await f.viewer.sendMessage('Original message');
    const prompt = f.viewer.current().pendingPrompt;
    assert.ok(prompt?.request_id);
    assert.match(f.viewer.current().status, /outcome unknown/);
    assert.equal(f.calls.filter(([, e]) => e.arguments.operation === 'session_prompt').length, 1);
    await f.viewer.sendMessage('Must not duplicate');
    assert.equal(f.calls.filter(([, e]) => e.arguments.operation === 'session_prompt').length, 1);
    await f.viewer.close();
    const reopened = createSessionViewer(f.deps);
    await reopened.open(f.rows[0]);
    assert.equal(reopened.current().pendingPrompt.request_id, prompt.request_id);
    await reopened.sendMessage('Changed message', true);
    const mutations = f.calls.filter(([, e]) => e.arguments.operation === 'session_prompt');
    assert.equal(mutations.length, 2);
    assert.deepEqual(mutations[0], mutations[1]);
});

test('late owner/session reads and an old delayed close cannot replace a newly selected session', async () => {
    const f = fixture();
    await f.viewer.open(f.rows[0]);
    await f.viewer.takeControl();
    let release;
    const normal = f.deps.request;
    f.setReply(async (wing, envelope) => {
        if (envelope.operation === 'fleet_detach') return new Promise(resolve => { release = resolve; });
        const a = envelope.arguments;
        return { origin_wing_id: a.origin_wing_id, session_id: a.session_id, route: { wing_id: wing }, result: { lifecycle: { events: [], cursor: 0 } } };
    });
    const first = f.viewer.open(f.rows[0]);
    await Promise.resolve();
    await f.viewer.open(f.rows[1]);
    release({ origin_wing_id: 'desktop', session_id: 'same-full-session-id', attachment_id: 'attachment-00000001', route: { wing_id: 'desktop' } });
    await first;
    assert.equal(f.viewer.current().row.origin_wing_id, 'laptop');
    f.switchOwner();
    await f.viewer.read();
    assert.equal(f.viewer.current(), null);
});

test('wrong-origin transcript and attachment acknowledgements fail closed', async () => {
    const f = fixture();
    f.setReply(async () => ({ origin_wing_id: 'laptop', session_id: 'same-full-session-id', route: { wing_id: 'desktop' }, result: { lifecycle: { events: [{ text: 'foreign' }], cursor: 1 } } }));
    await f.viewer.open(f.rows[0]);
    assert.equal(f.viewer.current().events.length, 0);
    assert.match(f.viewer.current().status, /identity mismatch/);
    await f.viewer.takeControl();
    assert.equal(f.viewer.current().mode, 'lost');
    await f.viewer.read();
    assert.equal(f.calls.filter(([, e]) => e.operation === 'fleet_attach').length, 1);
});

test('browser pending messages are bounded and isolated across owners', () => {
    const f = fixture();
    const row = f.rows[0];
    f.prompts.set('one', row, { request_id: 'original', input: 'private text', route: { wing_id: 'desktop' }, timeout_seconds: 5 });
    assert.equal(f.prompts.get('two', row), null);
    assert.equal(f.prompts.get('one', row).request_id, 'original');
    for (let i = 1; i < 32; i++) f.prompts.set('one', { ...row, session_id: `session-${i}` }, { request_id: `request-${i}`, input: 'text', route: { wing_id: 'desktop' }, timeout_seconds: 5 });
    assert.throws(() => f.prompts.set('one', { ...row, session_id: 'overflow' }, {}), /Reconcile/);
});

 test('corrupt saved message state permits reads and blocks untracked sends', async () => {
    const f = fixture();
    f.deps.prompts = { get() { throw Error('corrupt store'); }, set() { throw Error('corrupt store'); } };
    const viewer = createSessionViewer(f.deps);
    await viewer.open(f.rows[0]);
    assert.equal(viewer.current().promptStorageError, true);
    await viewer.sendMessage('Must not dispatch');
    assert.equal(f.calls.some(([, e]) => e.arguments.operation === 'session_prompt'), false);
    assert.ok(f.calls.some(([, e]) => e.arguments.operation === 'session_read'));
 });
