import test from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { createConversationReader } from '../src/conversation-recovery.js';
import { continuationAvailability, createPendingContinuation, continuationReceipt, readPendingContinuation, savePendingContinuation } from '../src/conversation-continuation.js';

const target = { userId: 'owner', wingId: 'wing', sessionId: 'source', conversationId: 'root', providerSessionId: 'provider' };
const ended = { session_id: 'source', agent: 'claude', provider_session_id: 'provider', state: 'completed', state_source: 'claude_hook', process_alive: false, ready: false, cursor: 1, head_cursor: 1, events: [], has_more: false };
const advertisement = { available: true, source_session: 'source', conversation_id: 'root', provider_session_id: 'provider', model: 'claude-opus-4-6' };

function storage() {
    const values = new Map();
    return { getItem: key => values.get(key) || null, setItem: (key, value) => values.set(key, value), removeItem: key => values.delete(key) };
}

function acknowledgement(pending, changes = {}) {
    return { request_id: pending.request_id, source_session: pending.sessionId, session: 'next', wing_id: pending.wingId,
        conversation_id: pending.conversationId, root_conversation_id: pending.conversationId, provider_session_id: pending.providerSessionId,
        parent_conversation_id: '', new_turn: { input_sha256: createHash('sha256').update(pending.input).digest('hex') },
        launch_state: 'started', continuation_state: 'started', ...changes };
}

const flush = () => new Promise(resolve => setImmediate(resolve));

function harness(saved, respond, options = {}) {
    const calls = [], timers = [], started = [];
    let snapshot;
    const reader = createConversationReader({
        storage: saved, randomId: () => 'request-1', now: () => 1000,
        request: async (wing, payload) => { calls.push({ wing, payload }); return respond(payload, calls.length); },
        schedule: fn => { const timer = { fn }; timers.push(timer); return timer; }, cancel: timer => { timer.cancelled = true; },
        onChange: value => { snapshot = value; }, onContinuationStarted: (source, result) => started.push({ source, result }), ...options,
    });
    return { reader, calls, started, get snapshot() { return snapshot; }, async tick() {
        let timer = timers.shift();
        while (timer && timer.cancelled) timer = timers.shift();
        if (timer) timer.fn();
        await flush();
    } };
}

test('follow-up composer needs a fresh exact ended Claude advertisement', async () => {
    assert.ok(continuationAvailability({ headless_continuation: advertisement }, target, ended));
    for (const change of [{ available: false }, { source_session: 'other' }, { conversation_id: 'child' }, { provider_session_id: 'other' }, { model: 'opus' }, { model: 'claude-\ninvalid' }]) {
        assert.equal(continuationAvailability({ headless_continuation: { ...advertisement, ...change } }, target, ended), null);
    }
    assert.equal(continuationAvailability({ headless_continuation: advertisement }, target, { ...ended, process_alive: true }), null);
    assert.equal(continuationAvailability({ headless_continuation: advertisement }, target, { ...ended, agent: 'codex' }), null);
    let read = { lifecycle: ended };
    const h = harness(storage(), () => read);
    h.reader.open(target);
    await h.tick();
    assert.equal(h.snapshot.inputReady, false);
    assert.equal(await h.reader.send('Follow up'), false);
    read = { lifecycle: ended, headless_continuation: advertisement };
    await h.tick();
    assert.equal(h.snapshot.continuationReady, true);
    assert.equal(h.snapshot.inputReady, true);
    read = null;
    await h.tick();
    assert.equal(h.snapshot.continuationReady, false);
    assert.equal(h.snapshot.inputReady, false, 'a failed read invalidates capability evidence');
});

test('follow-up capability expires and is never restored from cached transcript', async () => {
    const saved = storage();
    let now = 1000;
    const h = harness(saved, () => ({ lifecycle: ended, headless_continuation: advertisement }), { now: () => now });
    h.reader.open(target);
    await h.tick();
    assert.equal(h.reader.snapshot().continuationReady, true);
    now += 60001;
    assert.equal(h.reader.snapshot().continuationReady, false);
    assert.equal(await h.reader.send('Stale send'), false);
    h.reader.close();
    h.reader.open(target);
    assert.equal(h.snapshot.cached, true);
    assert.equal(h.snapshot.continuationReady, false);
    assert.equal(h.snapshot.inputReady, false);
});

test('lost follow-up acknowledgement survives reopen and explicit check reuses the four exact fields', async () => {
    const saved = storage();
    const text = '  Exact message 🦉\nsecond line  ';
    const first = harness(saved, payload => {
        if (payload.operation === 'session_read') return { lifecycle: ended, headless_continuation: advertisement };
        assert.deepEqual(Object.keys(payload.arguments).sort(), ['conversation_role', 'input', 'request_id', 'resume_session']);
        assert.deepEqual(payload.arguments, { resume_session: 'source', conversation_role: 'parent', input: text, request_id: 'request-1' });
        assert.equal(readPendingContinuation(saved, target).input, text, 'saved before transport');
        throw new Error('lost reply');
    });
    first.reader.open(target);
    await first.tick();
    const send = first.reader.send(text);
    assert.equal(first.snapshot.continuation.input, text);
    assert.equal(first.snapshot.inputReady, false);
    await send;
    assert.equal(first.snapshot.continuation.status, 'unconfirmed');
    assert.equal(await first.reader.send('duplicate'), false);
    first.reader.close();

    const intent = readPendingContinuation(saved, target);
    const second = harness(saved, payload => payload.operation === 'session_read'
        ? { lifecycle: { ...ended, session_id: 'next' } }
        : acknowledgement(intent));
    second.reader.open({ ...target, sessionId: 'next' });
    assert.equal(second.snapshot.continuation.request_id, intent.request_id);
    assert.equal(second.snapshot.continuationReady, false);
    await second.tick();
    assert.deepEqual(second.calls.map(call => call.payload.operation), ['session_read'], 'reopen only reads');
    assert.equal(await second.reader.checkReceipt(), true);
    assert.deepEqual(second.calls[1], { wing: 'wing', payload: { type: 'session.control', operation: 'agent_start', arguments: {
        resume_session: 'source', conversation_role: 'parent', input: text, request_id: 'request-1',
    } } });
    assert.equal(second.snapshot.continuation, null);
    assert.equal(readPendingContinuation(saved, target), null);
    assert.equal(second.started[0].source.sessionId, 'source');
});

test('continuation acknowledgement binds source, wing, root, provider, new execution and exact UTF-8 hash', async () => {
    const pending = createPendingContinuation(target, 'Message 🦉', 'immutable', 1000);
    const result = acknowledgement(pending);
    assert.equal(await continuationReceipt(pending, result), 'started');
    for (const changes of [{ request_id: 'other' }, { source_session: 'other' }, { wing_id: 'other' }, { conversation_id: 'other' },
        { root_conversation_id: 'other' }, { parent_conversation_id: 'child' }, { provider_session_id: 'other' },
        { session: 'source' }, { session: '  ' }, { session: '🦉'.repeat(65) }, { parent_conversation_id: false }, { parent_conversation_id: 0 },
        { new_turn: { input_sha256: '0'.repeat(64) } }, { continuation_state: 'starting' }]) {
        assert.equal(await continuationReceipt(pending, { ...result, ...changes }), 'unconfirmed');
    }
    assert.equal(await continuationReceipt(pending, { ...result, launch_state: 'failed', continuation_state: 'failed', provider_session_id: '' }), 'failed');
});

test('confirmed launch failure restores the exact message and allows a new explicit send', async () => {
    const saved = storage();
    const h = harness(saved, payload => payload.operation === 'session_read'
        ? { lifecycle: ended, headless_continuation: advertisement }
        : acknowledgement(readPendingContinuation(saved, target), { launch_state: 'failed', continuation_state: 'failed', launch_error: 'No slot' }));
    h.reader.open(target);
    await h.tick();
    await h.reader.send('  Try again  ');
    assert.equal(h.snapshot.continuation, null);
    assert.equal(h.reader.takeDraft(), '  Try again  ');
    assert.equal(h.snapshot.continuationReady, true);
    assert.equal(readPendingContinuation(saved, target), null);
});

test('unsaved and invalid follow-ups never reach agent_start', async () => {
    const h = harness({ getItem: () => null, setItem: () => { throw new Error('quota'); } }, () => ({ lifecycle: ended, headless_continuation: advertisement }));
    h.reader.open(target);
    await h.tick();
    assert.equal(await h.reader.send('Keep this draft'), false);
    assert.match(h.snapshot.storageError, /could not be saved/);
    assert.equal(h.snapshot.continuation, null);
    for (const text of ['', '  ', '-flag', 'nul\0', 'x'.repeat(65537)]) {
        assert.equal(createPendingContinuation(target, text, 'request', 1000), null);
    }
    assert.deepEqual(h.calls.map(call => call.payload.operation), ['session_read']);
});

test('another tab cannot replace an immutable unconfirmed follow-up', async () => {
    const saved = storage();
    const first = createPendingContinuation(target, 'Original intent', 'original', 1000);
    assert.equal(savePendingContinuation(saved, first), true);
    assert.equal(savePendingContinuation(saved, { ...first, input: 'Changed intent' }), false);
    const h = harness(saved, () => ({ lifecycle: ended, headless_continuation: advertisement }));
    h.reader.open(target);
    await h.tick();
    // Simulate another tab saving after this reader's last observation.
    h.reader.close();
    saved.removeItem('wt_conversation_continuation_v1:' + JSON.stringify(['owner', 'wing', 'root']));
    h.reader.open(target);
    await h.tick();
    assert.equal(savePendingContinuation(saved, first), true);
    assert.equal(await h.reader.send('New intent'), false);
    assert.equal(h.snapshot.continuation.input, 'Original intent');
    assert.equal(readPendingContinuation(saved, target).request_id, 'original');
    assert.deepEqual(h.calls.map(call => call.payload.operation), ['session_read', 'session_read']);
});

test('a reply after navigating away settles its saved intent without switching the new reader', async () => {
    const saved = storage();
    let resolve;
    const h = harness(saved, payload => payload.operation === 'session_read'
        ? { lifecycle: ended, headless_continuation: advertisement }
        : new Promise(done => { resolve = done; }));
    h.reader.open(target);
    await h.tick();
    const send = h.reader.send('Follow up');
    const intent = readPendingContinuation(saved, target);
    h.reader.open({ ...target, sessionId: 'unrelated', conversationId: 'other' });
    resolve(acknowledgement(intent));
    await send;
    assert.equal(readPendingContinuation(saved, target), null);
    assert.equal(h.snapshot.target.sessionId, 'unrelated');
    assert.equal(h.started.length, 0);
});
