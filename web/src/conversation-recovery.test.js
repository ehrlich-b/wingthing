// Fake-only node:test suite for the conversation reader/recovery slice. It
// lives beside its modules in web/src/conversation*; the
// root `node --test` discovery used by `make web` picks it up unchanged.
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { createNavigationGuard, qualifyConversationRead, readConversationTree, resumeInTerminalState, findArchivedExecution, mergeRootRead, mergeWingListing } from './conversation-recovery.js';
const { createConversationReader } = await import(process.env.SESSION_READER_SOURCE || './conversation-recovery.js');
import { applyConversationRead, emptyConversationState, conversationExecutionPresentation, CONVERSATION_FRESH_MS } from './conversation-state.js';
import { conversationTaskAvailability } from './conversation-response.js';
import { readExecution, writeExecution, queuedExecutions, executionKey, readSelection, saveSelection, readTree, saveTree, createPendingInput, updatePendingEvidence, TRANSCRIPT_MESSAGE_LIMIT, EXECUTION_CACHE_LIMIT } from './conversation-recovery-store.js';

function memoryStorage() {
    const values = new Map();
    return {
        getItem: key => values.has(key) ? values.get(key) : null,
        setItem: (key, value) => values.set(key, String(value)),
        removeItem: key => values.delete(key),
        keys: () => [...values.keys()],
        get length() { return values.size; },
        key: index => [...values.keys()][index] ?? null,
    };
}

const flush = () => new Promise(resolve => setImmediate(resolve));

function live(over) {
    return { session_id: 'sess-1', agent: 'claude', provider_session_id: 'prov-1', state: 'idle', state_source: 'claude_hook', ready: true, process_alive: true, head_cursor: 5, state_cursor: 5, cursor: 5, has_more: false, events: [], ...over };
}

function userText(sequence, text) {
    return { sequence: sequence, type: 'message', source: 'claude_transcript', raw: { type: 'user', message: { role: 'user', content: text } } };
}

// A reader wired to a scripted fake wing. No browser, tunnel, provider or
// real timer runs; every request is recorded with its exact wing and payload.
function harness(storage, respond, extra) {
    const calls = [];
    const timers = [];
    let clock = extra?.clockStart ?? 1_000_000;
    let ids = 0;
    let latest = null;
    const reader = createConversationReader({
        storage: storage,
        request: async (wingId, payload) => {
            calls.push({ wingId, payload });
            const result = await respond(wingId, payload, calls.length);
            // Model the real session_prompt envelope, including correlation.
            return result?.receipt ? { ...result, receipt: { request_id: payload.arguments.request_id, session_id: payload.arguments.session, ...result.receipt } } : result;
        },
        now: () => clock,
        schedule: (fn, ms) => { const timer = { fn, ms, cancelled: false }; timer.due = clock + ms; timers.push(timer); return timer; },
        cancel: timer => { timer.cancelled = true; },
        randomId: () => 'request-' + (++ids),
        onChange: snapshot => { latest = snapshot; },
        ...(extra || {}),
    });
    return {
        reader, calls,
        get time() { return clock; },
        get latest() { return latest; },
        async tick(count) {
            for (let i = 0; i < (count || 1); i++) {
                timers.sort((a, b) => a.due - b.due);
                let timer = timers.shift();
                while (timer && timer.cancelled) timer = timers.shift();
                if (!timer) break;
                clock = Math.max(clock, timer.due);
                timer.fn();
                await flush();
            }
        },
        async advance(ms) {
            const end = clock + ms;
            let steps = 0;
            for (;;) {
                timers.sort((a,b) => a.due - b.due);
                while (timers[0]?.cancelled) timers.shift();
                if (!timers[0] || timers[0].due > end) break;
                assert.ok(++steps < 1000, 'timer loop must advance instead of repeatedly checking a zero deadline');
                const timer = timers.shift(); clock = timer.due; timer.fn(); await flush();
            }
            clock = end;
        },
        pendingTimers: () => timers.filter(timer => !timer.cancelled).length,
        operations: () => calls.map(call => call.payload.operation || call.payload.type),
    };
}

const mac = { userId: 'owner', wingId: 'mac', sessionId: 'sess-1', conversationId: 'logical-child' };

for (const agent of ['claude', 'codex']) for (const skew of [60000, -60000]) {
    test(`${agent} waiting-answer countdown ignores browser/server clock skew ${skew}`, async () => {
        const storage = memoryStorage();
        let h, sends = 0, lookups = 0, wallSkew = skew;
        const serverDeadline = 115000;
        const respond = (_wing, payload) => {
            const result = { lifecycle: live({ agent, state_source: agent + '_hook', state: 'needs_input' }) };
            if (payload.operation === 'session_prompt' || payload.arguments.original_request) {
                const statusOnly = !!payload.arguments.original_request;
                if (statusOnly) lookups++; else sends++;
                result.receipt = {
                    request_id: statusOnly ? payload.arguments.original_request.request_id : payload.arguments.request_id,
                    session_id: mac.sessionId, provider_session_id: 'prov-1', answer_to_state_cursor: 5,
                    status: h.time >= serverDeadline ? 'waiting_answer_expired' : 'unconfirmed',
                    transport_enqueued: true, definitely_not_sent: false, provider_request_acknowledged: false,
                    enqueued_at_ms: 100000, waiting_deadline_ms: serverDeadline,
                    observed_at_ms: h.time, remaining_ms: Math.max(0, serverDeadline - h.time),
                };
            }
            return result;
        };
        const make = at => harness(storage, respond, {
            clockStart: at, now: () => h.time + wallSkew, monotonic: () => h.time,
        });
        h = make(103000); h.reader.open(mac); await h.tick();
        assert.equal(await h.reader.send('Yes'), true);
        assert.equal(h.reader.snapshot().waitingRemainingMS, 12000, 'fresh server duration survives a one-minute clock offset');
        assert.equal(h.latest.deliveryUnknown, true);
        await h.advance(2000);
        assert.equal(h.reader.snapshot().waitingRemainingMS, 10000);
        h.reader.close();
        h = make(105000); h.reader.open(mac); await h.tick(); await flush();
        assert.equal(h.reader.snapshot().waitingRemainingMS, 10000, 'reload uses the fresh status duration, with no Send');
        wallSkew = -wallSkew; // A wall-clock correction cannot move a monotonic deadline.
        await h.advance(9999);
        assert.equal(h.reader.snapshot().waitingRemainingMS, 1);
        assert.equal(h.latest.waitingAnswers[0].outcome || '', '');
        await h.advance(1); await flush();
        assert.equal(h.latest.waitingAnswers[0].outcome, 'expired', 'deadline forces a fresh exact-provider status read');
        assert.equal(h.reader.takeDraft(), 'Yes');
        assert.equal(sends, 1);
        assert.ok(lookups > 0);
        h.reader.close();
    });
}

test('Codex hook completion uses one correlated prompt and preserves uncertain delivery across reload', async () => {
    const storage = memoryStorage();
    const respond = (_wing, payload) => payload.operation === 'session_read'
        ? Promise.resolve({ lifecycle: live({ agent: 'codex', state_source: 'codex_hook', state: 'completed' }) })
        : Promise.resolve({ receipt: { status: 'unconfirmed' } });
    const first = harness(storage, respond);
    first.reader.open(mac);
    await first.tick();
    assert.equal(first.latest.presentation.state, 'turn_completed');
    assert.equal(await first.reader.send('Inspect the release'), true);
    assert.equal(first.latest.inputReady, false);
    const original = first.calls.find(call => call.payload.operation === 'session_prompt');
    assert.equal(original.wingId, 'mac');
    assert.equal(original.payload.arguments.session, 'sess-1');
    first.reader.close();
    const next = harness(storage, respond);
    next.reader.open(mac);
    await next.tick(3);
    assert.deepEqual(next.operations(), ['session_read', 'session_read', 'session_read']);
    assert.equal(next.latest.pending.providerSessionId, 'prov-1');
    await next.reader.checkReceipt(next.latest.pending.request_id);
    assert.deepEqual(next.calls.at(-1), original);
    assert.equal(next.latest.inputReady, false);
    next.reader.close();

    const accepted = harness(storage, (_wing, payload) => payload.operation === 'session_read'
        ? respond(_wing, payload)
        : Promise.resolve({ receipt: { status: 'native_receipt_observed', native_receipt_observed: true } }));
    accepted.reader.open(mac);
    await accepted.tick();
    await accepted.reader.checkReceipt(accepted.latest.pending.request_id);
    assert.deepEqual(accepted.calls.at(-1), original);
    assert.equal(accepted.latest.pending, null);
    accepted.reader.close();
});

test('Send keeps the draft and reports failure when its reservation cannot be saved', async () => {
    for (const failure of ['storage disabled', 'storage full']) {
        const storage = memoryStorage();
        const h = harness(storage, (wing, payload) => payload.operation === 'session_read'
            ? Promise.resolve({ lifecycle: live() })
            : Promise.resolve({ receipt: { status: 'unconfirmed' } }));
        h.reader.open(mac);
        await h.tick();
        const saved = storage.getItem(executionKey(mac.userId, mac.wingId, mac.sessionId));
        const setItem = storage.setItem;
        storage.setItem = () => { throw new Error(failure); };

        assert.equal(await h.reader.send('keep this draft'), false);
        assert.deepEqual(h.operations(), ['session_read']);
        assert.equal(h.latest.pending, null);
        assert.equal(h.latest.checking, false);
        assert.equal(h.latest.inputReady, true);
        assert.match(h.latest.notice, /not sent/i);
        assert.match(h.latest.storageError, /could not save/i);
        if (failure === 'storage disabled') assert.equal(h.reader.takeDraft(), 'keep this draft');
        assert.equal(storage.getItem(executionKey(mac.userId, mac.wingId, mac.sessionId)), saved);

        // Once storage works again, an explicit retry saves the reservation
        // before transport and can be restored by another reader.
        storage.setItem = setItem;
        assert.equal(await h.reader.send('keep this draft'), true);
        assert.equal(h.calls.at(-1).payload.arguments.request_id, 'request-2');
        assert.equal(readExecution(storage, mac).record.pending.input, 'keep this draft');
        assert.equal(h.reader.takeDraft(), null, 'a successful retry must not restore the earlier unsent draft');
        h.reader.close();
    }
});

test('reopening a reader with unresolved input only reads; Check receipt is explicit and reuses the exact request', async () => {
    const storage = memoryStorage();
    const first = harness(storage, (wing, payload) => payload.operation === 'session_read' ? Promise.resolve({ session: 'sess-1', lifecycle: live() }) : Promise.reject(new Error('tunnel request timeout')));
    first.reader.open(mac);
    await first.tick();
    assert.equal(first.latest.inputReady, true);
    await first.reader.send('  summarize the child  ');
    assert.equal(first.latest.pending.status, 'unconfirmed');
    assert.equal(first.latest.pending.input, '  summarize the child  ');
    assert.equal(first.latest.pending.providerSessionId, 'prov-1');
    first.reader.close();

    // Browser restart: a fresh controller over the same storage.
    const second = harness(storage, () => Promise.resolve({ session: 'sess-1', lifecycle: live() }));
    second.reader.open(mac);
    assert.equal(second.latest.cached, true);
    assert.equal(second.latest.presentation.live, false);
    assert.equal(second.latest.pending.request_id, 'request-1');
    assert.equal(second.latest.inputReady, false);
    await second.tick(4);
    assert.ok(second.calls.length >= 3);
    for (const call of second.calls) {
        assert.equal(call.payload.type, 'session.control');
        assert.equal(call.payload.operation, 'session_read');
        assert.equal(call.wingId, 'mac');
        assert.equal(call.payload.arguments.session, 'sess-1');
    }
    assert.equal(second.latest.pending.status, 'unconfirmed');
    assert.equal(second.latest.inputReady, false, 'unresolved input blocks a second send');

    const third = harness(storage, (wing, payload) => payload.operation === 'session_prompt'
        ? Promise.resolve({ receipt: { status: 'native_receipt_observed', native_receipt_observed: true } })
        : Promise.resolve({ lifecycle: live() }));
    third.reader.open(mac);
    await third.reader.checkReceipt();
    const prompt = third.calls.find(call => call.payload.operation === 'session_prompt');
    assert.deepEqual({ wing: prompt.wingId, ...prompt.payload.arguments }, { wing: 'mac', session: 'sess-1', input: '  summarize the child  ', request_id: 'request-1', timeout_seconds: 3 });
    assert.equal(third.latest.pending, null);
    assert.equal(readExecution(storage, mac).record.pending, null);
});

test('legacy tab-scoped pending input migrates once to the exact execution without being submitted', async () => {
    const storage = memoryStorage(), legacy = memoryStorage();
    legacy.setItem('wt_conversation_prompt:' + JSON.stringify(['owner', 'mac', 'sess-1']), JSON.stringify({ request_id: 'old', input: 'from old tab', status: 'pending' }));
    const h = harness(storage, () => Promise.resolve({ lifecycle: live() }), { legacyStorage: legacy });
    h.reader.open(mac);
    await h.tick(2);
    assert.equal(h.latest.pending.request_id, 'old');
    assert.equal(h.latest.pending.status, 'unconfirmed');
    assert.deepEqual([...new Set(h.operations())], ['session_read']);
    assert.equal(legacy.getItem('wt_conversation_prompt:' + JSON.stringify(['owner', 'mac', 'sess-1'])), null);
    const other = harness(storage, () => Promise.resolve({ lifecycle: live() }), { legacyStorage: legacy });
    other.reader.open({ ...mac, userId: 'someone-else' });
    assert.equal(other.latest.pending, null, 'no bare or cross-user fallback');
});

test('identical session IDs on two wings keep separate transcripts, pending input, trees and selection', async () => {
    const storage = memoryStorage();
    const linux = { ...mac, wingId: 'linux' };
    const h = harness(storage, (wing, payload) => {
        if (payload.operation === 'session_prompt') return Promise.resolve({ receipt: { status: 'unconfirmed' } });
        const text = wing === 'mac' ? 'mac transcript' : 'linux transcript';
        return Promise.resolve({ lifecycle: live({ provider_session_id: 'prov-' + wing, events: [userText(1, text)] }) });
    });
    h.reader.open(mac);
    await h.tick();
    await h.reader.send('only for mac');
    h.reader.open(linux);
    assert.equal(h.latest.pending, null);
    assert.equal(h.latest.messages.length, 0);
    await h.tick();
    assert.deepEqual(h.latest.messages.map(m => m.content), ['linux transcript']);
    assert.equal(readExecution(storage, mac).record.pending.input, 'only for mac');
    assert.equal(readExecution(storage, linux).record.pending, null);
    assert.notEqual(executionKey('owner', 'mac', 'sess-1'), executionKey('owner', 'linux', 'sess-1'));
    assert.deepEqual(readExecution(storage, mac).record.messages.map(m => m.content), ['mac transcript']);

    assert.equal(saveSelection(storage, 'owner', { wingId: 'linux', conversationId: 'logical-child', rootConversationId: 'root' }), true);
    assert.equal(readSelection(storage, 'owner').selection.wingId, 'linux');
    assert.equal(readSelection(storage, 'intruder').selection, null);
    saveTree(storage, 'owner', 'mac', { tasks: [{ conversation: { conversation_id: 'root', wing_id: 'mac' } }, { conversation: { conversation_id: 'stray', wing_id: 'linux' } }], savedAt: 1 });
    assert.deepEqual(readTree(storage, 'owner', 'mac').tasks.map(t => t.conversation.conversation_id), ['root']);
    assert.equal(readTree(storage, 'owner', 'linux').tasks.length, 0);

    const qualified = qualifyConversationRead({ tasks: [{ conversation: { conversation_id: 'logical-child', wing_id: 'linux', session_id: 'sess-1' } }, { conversation: { conversation_id: 'logical-child', wing_id: 'mac', session_id: 'sess-1', root_conversation_id: 'root' }, lifecycle: live({ provider_session_id: 'prov-mac' }) }] }, { wingId: 'mac', conversationId: 'logical-child' });
    assert.deepEqual(qualified.execution, { wingId: 'mac', sessionId: 'sess-1', providerSessionId: 'prov-mac', conversationId: 'logical-child' });
    assert.equal(qualified.tasks.length, 1);
    assert.throws(() => qualifyConversationRead({ conversation: { conversation_id: 'logical-child', wing_id: 'linux' }, tasks: [] }, { wingId: 'mac', conversationId: 'logical-child' }), /another wing/);
});

test('delayed older lifecycle pages cannot replace fresher state but still contribute unseen events', () => {
    let state = applyConversationRead(emptyConversationState(), live({ state: 'working', ready: false, head_cursor: 9, state_cursor: 9, cursor: 9, events: [userText(9, 'newest')] }), { readSeq: 2 });
    state = applyConversationRead(state, live({ state: 'idle', head_cursor: 4, state_cursor: 4, cursor: 4, events: [userText(3, 'older')] }), { readSeq: 1 });
    assert.equal(state.lifecycle.state, 'working');
    assert.equal(state.cursor, 9);
    assert.deepEqual(state.messages.map(m => m.content).sort(), ['newest', 'older']);
    state = applyConversationRead(state, live({ state: 'idle', head_cursor: 8, state_cursor: 8 }), { readSeq: 3 });
    assert.equal(state.lifecycle.state, 'working', 'a lower head cursor is older even with a later request');
    state = applyConversationRead(state, live({ state: 'completed', head_cursor: 10, state_cursor: 10, cursor: 10 }), { readSeq: 4 });
    assert.equal(state.lifecycle.state, 'completed');
});

test('stale reader responses never switch the selection and provider identity changes halt the reader', async () => {
    const storage = memoryStorage();
    let releaseOld;
    const h = harness(storage, (wing, payload) => {
        if (payload.arguments.session === 'old') return new Promise(resolve => { releaseOld = () => resolve({ lifecycle: live({ session_id: 'old', events: [userText(1, 'old execution')] }) }); });
        return Promise.resolve({ lifecycle: live({ session_id: 'new', events: [userText(1, 'new execution')] }) });
    });
    h.reader.open({ ...mac, sessionId: 'old' });
    await h.tick();
    h.reader.open({ ...mac, sessionId: 'new' });
    await h.tick();
    releaseOld();
    await flush();
    assert.equal(h.latest.target.sessionId, 'new');
    assert.deepEqual(h.latest.messages.map(m => m.content), ['new execution']);
    assert.equal(readExecution(storage, { ...mac, sessionId: 'old' }).record, null, 'late response was not cached either');

    const swapped = harness(memoryStorage(), (wing, payload, n) => Promise.resolve({ lifecycle: live({ provider_session_id: n === 1 ? 'prov-1' : 'prov-replaced', events: n === 1 ? [] : [userText(7, 'foreign')] }) }));
    swapped.reader.open(mac);
    await swapped.tick();
    await swapped.tick();
    assert.equal(swapped.latest.halted, true);
    assert.match(swapped.latest.identityError, /Provider conversation changed/);
    assert.equal(swapped.latest.messages.length, 0);
    assert.equal(swapped.latest.presentation.live, false);
    assert.equal(swapped.pendingTimers(), 0, 'no automatic reads after an identity mismatch');
    const mismatched = applyConversationRead(emptyConversationState(), live({ session_id: 'other' }), { expected: { sessionId: 'sess-1' } });
    assert.match(mismatched.identityError, /belongs to execution other/);

    const guard = createNavigationGuard();
    const a = guard.begin({ conversationId: 'a' });
    const b = guard.begin({ conversationId: 'b' });
    assert.equal(guard.current(a), false);
    assert.equal(guard.current(b), true);
});

test('pending input stays bound to its original execution after the logical conversation moves to a new execution', async () => {
    const storage = memoryStorage();
    let prompts = 0;
    const h = harness(storage, (wing, payload) => {
        if (payload.operation === 'session_prompt') {
            prompts++;
            if (prompts > 1) return Promise.resolve({ receipt: { status: 'not_sent', definitely_not_sent: true, reason: 'provider exited before input' } });
            return Promise.reject(new Error('ack lost'));
        }
        return Promise.resolve({ lifecycle: live({ session_id: payload.arguments.session, provider_session_id: payload.arguments.session === 'sess-1' ? 'prov-1' : 'prov-2' }) });
    });
    h.reader.open(mac);
    await h.tick();
    await h.reader.send('original target only');
    assert.equal(h.latest.pending.status, 'unconfirmed');

    // Explicit terminal resume created a new execution of the same logical task.
    h.reader.open({ ...mac, sessionId: 'sess-2' });
    assert.equal(h.latest.pending, null);
    assert.equal(h.latest.otherPending.length, 1);
    assert.equal(h.latest.otherPending[0].sessionId, 'sess-1');
    await h.tick();
    assert.equal(h.latest.inputReady, true, 'the new execution is not blocked or targeted by old input');
    await h.reader.checkReceipt('request-1');
    const sent = h.calls.filter(c => c.payload.operation === 'session_prompt');
    assert.equal(sent.length, 2);
    assert.deepEqual([sent[1].wingId, sent[1].payload.arguments.session, sent[1].payload.arguments.request_id, sent[1].payload.arguments.input], ['mac', 'sess-1', 'request-1', 'original target only']);
    assert.equal(h.latest.otherPending.length, 0);
    assert.match(h.latest.notice, /not moved to the current execution/);
    assert.equal(h.reader.takeDraft(), null, 'released text is not placed into the new execution composer');
    assert.equal(h.calls.some(c => c.payload.operation === 'session_prompt' && c.payload.arguments.session === 'sess-2'), false);
    assert.equal(readExecution(storage, mac).record.pending, null);

    const pending = createPendingInput({ request_id: 'r', input: 'x', userId: 'owner', wingId: 'mac', sessionId: 'sess-1', providerSessionId: 'p' });
    const updated = updatePendingEvidence(pending, { status: 'unconfirmed', input: 'changed', sessionId: 'elsewhere' });
    assert.deepEqual([updated.input, updated.sessionId, updated.status], ['x', 'sess-1', 'unconfirmed']);
});

test('a failed stop keeps the execution card, reports the error and keeps reading; an acknowledged stop is not task success', async () => {
    const storage = memoryStorage();
    let killResult = () => Promise.reject(new Error('tunnel ws closed'));
    let alive = true;
    const h = harness(storage, (wing, payload) => payload.type === 'pty.kill' ? killResult() : Promise.resolve({ lifecycle: live({ state: 'working', ready: false, process_alive: alive, head_cursor: alive ? 5 : 6 }) }));
    h.reader.open(mac);
    await h.tick();
    assert.equal(await h.reader.stopExecution(), false);
    assert.match(h.latest.stop.error, /Stop not confirmed: tunnel ws closed/);
    assert.equal(h.latest.target.sessionId, 'sess-1');
    const reads = h.calls.length;
    await h.tick();
    assert.ok(h.calls.length > reads, 'reads continue after a failed stop');

    killResult = () => Promise.resolve({ error: 'access denied' });
    assert.equal(await h.reader.stopExecution(), false);
    assert.match(h.latest.stop.error, /access denied/);

    killResult = () => { alive = false; return Promise.resolve({ ok: 'true' }); };
    assert.equal(await h.reader.stopExecution(), true);
    assert.ok(h.latest.stop.acknowledgedAt > 0);
    const kill = h.calls.filter(c => c.payload.type === 'pty.kill').pop();
    assert.deepEqual([kill.wingId, kill.payload.session_id], ['mac', 'sess-1']);
    await h.tick(3);
    assert.equal(h.latest.presentation.state, 'archived');
    assert.equal(h.latest.presentation.archived, true);
    assert.doesNotMatch(h.latest.presentation.label, /complete|success/);
});

test('process exit is archived evidence; only a fresh live native Stop is a finished turn', () => {
    const now = 500000, fresh = { observedAt: now - 1, now: now };
    const exited = conversationExecutionPresentation(live({ state: 'completed', process_alive: false }), fresh);
    assert.deepEqual([exited.state, exited.archived, exited.live], ['archived', true, false]);
    assert.match(exited.detail, /does not prove the task succeeded/);
    assert.equal(conversationExecutionPresentation(live({ state: 'failed', process_alive: false }), fresh).state, 'failed');
    assert.equal(conversationExecutionPresentation(live({ state: 'completed', process_alive: false }), { cached: true, now }).state, 'archived');

    const turn = conversationExecutionPresentation(live({ state: 'completed' }), fresh);
    assert.deepEqual([turn.state, turn.label, turn.live, turn.inputReady], ['turn_completed', 'turn finished', true, true]);
    assert.match(turn.detail, /not proof that delegated work succeeded/);
    for (const evidence of [{ cached: true, observedAt: now, now }, { observedAt: now - CONVERSATION_FRESH_MS, now }, { observedAt: 0, now }, { ...fresh, readError: 'tunnel ws closed' }]) {
        const shown = conversationExecutionPresentation(live({ state: 'completed' }), evidence);
        assert.equal(shown.state, 'unknown');
        assert.equal(shown.live, false);
        assert.equal(shown.inputReady, false);
    }
    assert.equal(conversationExecutionPresentation(live({ state: 'completed', state_source: 'egg_process' }), fresh).state, 'unknown');
    const attention = conversationExecutionPresentation(live({ state: 'needs_input', reason: 'provider permission decision requested', ready: false }), fresh);
    assert.deepEqual([attention.state, attention.attention, attention.inputReady], ['needs_input', true, false]);
    assert.match(attention.detail, /No provider approval ID/);
    assert.equal(conversationExecutionPresentation(live({ state_source: 'unsupported' }), fresh).state, 'unsupported');

    const task = { conversation: { conversation_id: 'c', launch_state: 'started' }, lifecycle: live({ state: 'working' }), history_unavailable: true };
    const availability = conversationTaskAvailability(task, '', { observedAt: now - 1, now });
    assert.deepEqual([availability.state, availability.error], ['working', '']);
    assert.match(availability.historyIssue, /earlier execution history/);
    assert.equal(conversationTaskAvailability({ ...task, lifecycle: live({ process_alive: false, state: 'completed' }) }, '', { observedAt: now - 1, now }).state, 'archived');
    const offline = conversationTaskAvailability(task, 'Offline · cached tasks', { observedAt: now - 1, now });
    assert.deepEqual([offline.state, offline.live, offline.error], ['unknown', false, 'Offline · cached tasks']);
    assert.equal(conversationTaskAvailability({ conversation: { launch_state: 'failed', launch_error: 'spawn failed' } }, '').state, 'launch_failed');
    assert.equal(conversationTaskAvailability({ conversation: { launch_state: 'starting' } }, '').state, 'launch_unconfirmed');
    assert.equal(conversationTaskAvailability({ conversation: {}, lifecycle_error: 'native read failed' }, '').state, 'unavailable');
});

test('native lifecycle markers and non-Claude records stay inspectable provider data', () => {
    const state = applyConversationRead(emptyConversationState(), live({ events: [
        { sequence: 1, type: 'input_requested', state: 'needs_input', source: 'claude_hook', reason: 'provider permission decision requested' },
        { sequence: 2, type: 'tool_activity', state: 'working', source: 'claude_hook' },
        { sequence: 3, type: 'turn_completed', state: 'completed', source: 'claude_hook', reason: 'native Stop observed' },
    ] }));
    assert.deepEqual(state.messages.filter(m => m.type === 'lifecycle').map(m => [m.type, m.kind]), [['lifecycle', 'attention'], ['lifecycle', 'turn']]);
    const codex = applyConversationRead(emptyConversationState(), live({ agent: 'codex', provider_session_id: '', events: [{ sequence: 4, type: 'provider_item', raw: { type: 'mcpToolCall', tool: 'session_read', result: { content: 'evidence' } } }] }));
    assert.equal(codex.messages[0].type, 'provider_record');
    assert.match(codex.messages[0].content, /session_read/);
});

test('execution cache is bounded, never evicts unresolved input, and ignores records under the wrong identity', () => {
    const storage = memoryStorage();
    const messages = Array.from({ length: TRANSCRIPT_MESSAGE_LIMIT + 50 }, (_, i) => ({ type: 'assistant', content: 'x'.repeat(20000), sequence: i + 1 }));
    writeExecution(storage, mac, { messages, savedAt: 1, pending: createPendingInput({ request_id: 'keep', input: 'held', userId: 'owner', wingId: 'mac', sessionId: 'sess-1' }), conversationId: 'logical-child' });
    const record = readExecution(storage, mac).record;
    assert.ok(record.messages.length <= TRANSCRIPT_MESSAGE_LIMIT);
    assert.equal(record.trimmed, true);
    assert.equal(record.pending.request_id, 'keep');
    for (let i = 0; i < EXECUTION_CACHE_LIMIT + 5; i++) writeExecution(storage, { userId: 'owner', wingId: 'mac', sessionId: 'filler-' + i }, { messages: [], savedAt: i + 2 });
    assert.equal(readExecution(storage, mac).record.pending.input, 'held');
    assert.equal(readExecution(storage, { userId: 'owner', wingId: 'mac', sessionId: 'filler-0' }).record, null);
    assert.ok(storage.keys().filter(k => k.startsWith('wt_conversation_execution_v1:')).length <= EXECUTION_CACHE_LIMIT);

    storage.setItem(executionKey('owner', 'mac', 'forged'), JSON.stringify({ v: 1, userId: 'owner', wingId: 'linux', sessionId: 'forged', messages: [] }));
    assert.equal(readExecution(storage, { userId: 'owner', wingId: 'mac', sessionId: 'forged' }).record, null);
    const full = { getItem: () => null, setItem: () => { throw new Error('QuotaExceededError'); }, removeItem() {} };
    assert.match(writeExecution(full, mac, { messages: [] }).error, /could not save/);
});

test('root with two children: tree reads only read and never list checkpointed deliveries as outstanding', async () => {
    const calls = [];
    const node = (id, over) => ({ conversation: { conversation_id: id, root_conversation_id: 'root', wing_id: 'mac', session_id: 'exec-' + id, ...over } });
    const result = {
        conversation: { conversation_id: 'root', root_conversation_id: 'root', wing_id: 'mac', delivered_cursor: 7 },
        tasks: [node('root', { delivered_cursor: 7 }), { ...node('child-a', { parent_conversation_id: 'root' }), lifecycle: live({ state: 'needs_input' }) }, { ...node('child-b', { parent_conversation_id: 'root' }), lifecycle: live({ process_alive: false, state: 'completed' }) }, node('foreign', { wing_id: 'linux', parent_conversation_id: 'root' })],
        events: [{ sequence: 6, conversation_id: 'child-b', state: 'completed' }, { sequence: 8, conversation_id: 'child-a', state: 'needs_input' }],
        has_more: false, next_cursor: 8,
    };
    const fake = (wing, payload) => { calls.push([wing, payload]); return Promise.resolve(result); };
    const tree = await readConversationTree(fake, 'mac', { conversation_id: 'root' });
    assert.deepEqual(tree.tasks.map(t => t.conversation.conversation_id), ['root', 'child-a', 'child-b']);
    assert.deepEqual(tree.delivery.events.map(e => e.sequence), [8]);
    await readConversationTree(fake, 'mac', { conversation_id: 'root', delivered_cursor: 7 });
    assert.deepEqual(calls.map(c => [c[0], c[1].operation, c[1].arguments.after_cursor]), [['mac', 'conversation_read', undefined], ['mac', 'conversation_read', 7]]);
    const now = 9000;
    const states = tree.tasks.map(t => conversationTaskAvailability(t, '', { observedAt: now - 1, now }).state);
    assert.deepEqual(states, ['unknown', 'needs_input', 'archived']);
});

test('Resume in terminal is offered only for a freshly archived exact execution with an owned resumable archive', async () => {
    const execution = { wingId: 'mac', sessionId: 'sess-1', conversationId: 'logical-child', agent: 'claude' };
    const archived = { archived: true };
    const entry = { session_id: 'sess-1', conversation_id: 'logical-child', user_id: 'owner', resumable: true, agent: 'claude' };
    const user = { id: 'owner' };
    assert.equal(resumeInTerminalState(execution, archived, false, entry, user, true).available, true);
    assert.equal(resumeInTerminalState(execution, { archived: false }, false, entry, user, true).available, false);
    assert.equal(resumeInTerminalState(execution, archived, true, entry, user, true).available, false);
    assert.equal(resumeInTerminalState(execution, archived, false, { ...entry, session_id: 'sess-2' }, user, true).available, false);
    assert.equal(resumeInTerminalState(execution, archived, false, { ...entry, conversation_id: 'other' }, user, true).available, false);
    assert.equal(resumeInTerminalState(execution, archived, false, { ...entry, user_id: 'other' }, user, true).available, false);
    assert.equal(resumeInTerminalState(execution, archived, false, { ...entry, resumable: false }, user, true).available, false);
    assert.equal(resumeInTerminalState(execution, archived, false, entry, user, false).available, false);
    assert.equal(resumeInTerminalState(execution, archived, false, null, user, true).available, false);
    // The provider is exact: never a default, never a different one.
    assert.match(resumeInTerminalState(execution, archived, false, { ...entry, agent: 'codex' }, user, true).reason, /another provider/);
    assert.match(resumeInTerminalState({ ...execution, agent: '' }, archived, false, { ...entry, agent: '' }, user, true).reason, /does not report its provider/);
    assert.equal(resumeInTerminalState({ ...execution, agent: '' }, archived, false, entry, user, true).available, true);

    const pages = [Array.from({ length: 50 }, (_, i) => ({ session_id: 'other-' + i })), [entry]];
    const requests = [];
    const found = await findArchivedExecution((wing, payload) => { requests.push([wing, payload.type, payload.offset]); return Promise.resolve({ sessions: pages[payload.offset / 50] || [] }); }, 'mac', 'sess-1');
    assert.equal(found, entry);
    assert.deepEqual(requests, [['mac', 'sessions.history', 0], ['mac', 'sessions.history', 50]]);
});

test('a task with no recorded execution is never presented as an archived exit', () => {
    // conversation_read's zero-valued lifecycle for a node without an execution.
    const zero = { session_id: '', agent: '', state: '', state_source: '', state_cursor: 0, ready: false, process_alive: false, cursor: 0, head_cursor: 0, has_more: false, events: [] };
    const now = 1000, at = { observedAt: now - 1, now };
    assert.equal(conversationTaskAvailability({ conversation: { launch_state: 'starting' }, lifecycle: zero }, '', at).state, 'launch_unconfirmed');
    assert.equal(conversationTaskAvailability({ conversation: { launch_state: 'failed', launch_error: 'spawn failed' }, lifecycle: zero }, '', at).state, 'launch_failed');
    const started = conversationTaskAvailability({ conversation: { launch_state: 'started', session_id: 'sess-1' }, lifecycle: zero }, '', at);
    assert.deepEqual([started.state, started.archived, started.live], ['unknown', false, false]);
    assert.equal(conversationTaskAvailability({ conversation: { launch_state: 'started' }, lifecycle: live({ process_alive: false, state: 'idle' }) }, '', at).state, 'archived');
});

// Fake origin quota counted in UTF-16 code units of keys plus values.
function quotaStorage(limit) {
    const base = memoryStorage();
    const used = () => base.keys().reduce((n, key) => n + key.length + base.getItem(key).length, 0);
    return {
        ...base,
        setItem(key, value) {
            const prior = base.getItem(key);
            if (used() - (prior === null ? 0 : key.length + prior.length) + key.length + String(value).length > limit) throw new Error('QuotaExceededError');
            base.setItem(key, value);
        },
    };
}

test('a full browser quota evicts older transcript-only records and never drops unresolved input', () => {
    const storage = quotaStorage(80000);
    const big = (count) => Array.from({ length: count }, (_, i) => ({ type: 'assistant', content: 'y'.repeat(10000), sequence: i + 1 }));
    const held = (sessionId, request) => createPendingInput({ request_id: request, input: 'held ' + request, userId: 'owner', wingId: 'mac', sessionId });
    const other = { userId: 'owner', wingId: 'mac', sessionId: 'other-pending' };
    assert.equal(writeExecution(storage, other, { messages: [], pending: held('other-pending', 'r-other'), savedAt: 1 }).ok, true);
    for (let i = 0; i < 3; i++) assert.equal(writeExecution(storage, { userId: 'owner', wingId: 'mac', sessionId: 'filler-' + i }, { messages: big(2), savedAt: 2 + i }).error, '');

    const first = writeExecution(storage, mac, { messages: big(3), pending: held('sess-1', 'r-1'), savedAt: 10 });
    assert.deepEqual([first.ok, first.error], [true, '']);
    assert.equal(readExecution(storage, { userId: 'owner', wingId: 'mac', sessionId: 'filler-0' }).record, null, 'oldest transcript-only record evicted');
    assert.equal(readExecution(storage, mac).record.messages.length, 3);

    const huge = { userId: 'owner', wingId: 'mac', sessionId: 'sess-big' };
    const second = writeExecution(storage, huge, { messages: big(10), pending: held('sess-big', 'r-big'), savedAt: 11 });
    assert.equal(second.ok, true);
    assert.match(second.error, /transcript was not cached, but its identity and unresolved input were/);
    const kept = readExecution(storage, huge).record;
    assert.deepEqual([kept.messages.length, kept.trimmed, kept.pending.input], [0, true, 'held r-big']);
    assert.equal(readExecution(storage, other).record.pending.request_id, 'r-other');
    assert.equal(readExecution(storage, mac).record.pending.request_id, 'r-1');
});

test('a definite not-sent answer to an explicit Check receipt hands the text back; a closed reader stays silent', async () => {
    const storage = memoryStorage();
    let answer = () => Promise.reject(new Error('ack lost'));
    const h = harness(storage, (wing, payload) => payload.operation === 'session_prompt' ? answer() : Promise.resolve({ lifecycle: live() }));
    h.reader.open(mac);
    await h.tick();
    await h.reader.send('hand me back');
    assert.equal(h.latest.pending.status, 'unconfirmed');
    assert.equal(h.reader.takeDraft(), null);
    answer = () => Promise.resolve({ receipt: { status: 'not_sent', definitely_not_sent: true, reason: 'writer busy' } });
    await h.reader.checkReceipt(h.latest.pending.request_id);
    assert.equal(h.latest.pending, null);
    assert.equal(h.reader.takeDraft(), 'hand me back');
    assert.match(h.latest.notice, /writer busy/);
    assert.equal(readExecution(storage, mac).record.pending, null);

    let release;
    answer = () => new Promise(resolve => { release = () => resolve({ receipt: { status: 'unconfirmed' } }); });
    const sending = h.reader.send('in flight at close');
    h.reader.close();
    const before = h.latest;
    release();
    await sending;
    assert.equal(h.latest, before, 'no snapshot after close');
    assert.deepEqual([readExecution(storage, mac).record.pending.input, readExecution(storage, mac).record.pending.status], ['in flight at close', 'unconfirmed']);
});

test('a delayed tree response never replaces a subtree read issued later', () => {
    const node = (id, root, session) => ({ conversation: { conversation_id: id, root_conversation_id: root, wing_id: 'mac', session_id: session } });
    const sessions = tree => tree.tasks.map(t => t.conversation.session_id).sort();
    let tree = mergeRootRead({ tasks: [], deliveries: {} }, { rootId: 'root', tasks: [node('root', 'root', 'new'), node('child', 'root', 'child-new')], delivery: { events: [{ sequence: 9 }] }, issuedAt: 200 });
    assert.equal(mergeRootRead(tree, { rootId: 'root', tasks: [node('root', 'root', 'old')], delivery: { events: [] }, issuedAt: 100 }), null);
    tree = mergeRootRead(tree, { rootId: 'later-root', tasks: [node('later-root', 'later-root', 'later')], delivery: null, issuedAt: 300 });

    // An inventory listing issued before those reads keeps the fresher
    // subtree, its deliveries, and the root first observed after it began.
    const listed = mergeWingListing(tree, [
        { rootId: 'root', tasks: [node('root', 'root', 'old')], delivery: { events: [] }, issuedAt: 150 },
        { rootId: 'other', tasks: [node('other', 'other', 'o')], delivery: { events: [] }, issuedAt: 160 },
    ], 140);
    assert.deepEqual(sessions(listed), ['child-new', 'later', 'new', 'o']);
    assert.deepEqual(listed.deliveries.root.events.map(e => e.sequence), [9]);

    // A later listing is authoritative: it replaces and drops older roots.
    const fresh = mergeWingListing(listed, [{ rootId: 'root', tasks: [node('root', 'root', 'newest')], delivery: { events: [] }, issuedAt: 400 }], 390);
    assert.deepEqual(sessions(fresh), ['newest']);
    assert.equal(fresh.tasks[0].observedAt, 400);
});

for (const agent of ['claude', 'codex']) {
    test(`${agent} sends exact composed text once, waits for receipt and rejects duplicate/empty input`, async () => {
        let release;
        const gate = new Promise(resolve => { release = resolve; });
        const h = harness(memoryStorage(), async (wing, payload) => {
            if (payload.operation === 'session_read') return { lifecycle: live({ agent, state_source: agent + '_hook' }) };
            await gate;
            return { receipt: { status: 'native_receipt_observed', native_receipt_observed: true, receipt_cursor: 6 } };
        });
        h.reader.open(mac); await h.tick();
        for (const empty of ['', ' \n\t ']) assert.equal(await h.reader.send(empty), false);
        const text = '  Ω exact message\nsecond line\twith tabs  \n';
        const sending = h.reader.send(text);
        assert.equal(h.latest.pending.input, text);
        assert.equal(h.latest.deliveryReceipt, null);
        assert.equal(h.latest.inputReady, false);
        assert.equal(await h.reader.send(text), false);
        assert.equal(await h.reader.checkReceipt(), false);
        assert.equal(h.calls.filter(c => c.payload.operation === 'session_prompt').length, 1);
        assert.equal(h.calls.at(-1).payload.arguments.input, text);
        release(); await sending;
        assert.equal(h.latest.pending, null);
        assert.equal(h.latest.deliveryReceipt.request_id, 'request-1');
        assert.equal(h.latest.deliveryReceipt.session_id, 'sess-1');
    });
}

test('transport enqueue and foreign receipts never report delivery or release the original request', async () => {
    for (const receipt of [
        { status: 'unconfirmed', transport_enqueued: true },
        { status: 'native_receipt_observed', native_receipt_observed: false },
        { status: 'native_receipt_observed', native_receipt_observed: true, request_id: 'foreign' },
        { status: 'native_receipt_observed', native_receipt_observed: true, session_id: 'foreign' },
    ]) {
        const h = harness(memoryStorage(), async (wing, payload) => payload.operation === 'session_read' ? { lifecycle: live() } : { receipt });
        h.reader.open(mac); await h.tick(); await h.reader.send('original');
        assert.equal(h.latest.deliveryReceipt, null);
        assert.equal(h.latest.pending.request_id, 'request-1');
        assert.equal(h.latest.pending.input, 'original');
        assert.equal(h.latest.inputReady, false);
        h.reader.close();
    }
});

test('invalid and oversized messages stay unsent without a reservation', async () => {
    const h = harness(memoryStorage(), async () => ({ lifecycle: live() }));
    h.reader.open(mac); await h.tick();
    for (const text of ['Ω'.repeat(32769), 'bad\x1b[31m', 'bad\rreturn']) {
        assert.equal(await h.reader.send(text), false);
        assert.equal(h.latest.pending, null);
        assert.match(h.latest.notice, /at most 64 KiB/);
    }
    assert.deepEqual(h.operations(), ['session_read']);
});

test('Codex reader exposes sending separately from transcript rows and clears it on native input', async () => {
    let events = [{ sequence: 6, source: 'codex_hook', type: 'prompt_submitted', text: 'repeat' }];
    const h = harness(memoryStorage(), () => ({ lifecycle: live({ agent: 'codex', state_source: 'codex_hook', cursor: events.at(-1).sequence, events }) }));
    h.reader.open(mac);
    await h.tick();
    assert.equal(h.latest.sendingUntil, 1_060_000);
    assert.equal(h.latest.messages.some(m => m.type === 'user'), false);
    events = [{ sequence: 7, source: 'codex_transcript', type: 'message', role: 'user', text: 'repeat' }];
    await h.tick();
    assert.equal(h.latest.sendingUntil, 0);
    assert.deepEqual(h.latest.messages.filter(m => m.type === 'user').map(m => m.content), ['repeat']);
    h.reader.close();
});

test('upgrading a legacy Codex cache rereads native history and preserves unresolved input', async () => {
    const fixture = JSON.parse(readFileSync(new URL('../test/fixtures/codex-legacy-cache.json', import.meta.url)));
    for (const catchingUp of [false, true]) {
        const storage = memoryStorage();
        const pending = createPendingInput({ ...mac, request_id: 'unresolved', input: 'keep pending', providerSessionId: 'prov-1' });
        writeExecution(storage, mac, { ...fixture, ...(catchingUp ? { cursor: 2, messages: fixture.messages.slice(0, 2) } : {}), pending });
        const h = harness(storage, (_wing, payload) => {
            const events = fixture.events.filter(event => event.sequence > payload.arguments.after_cursor);
            return { lifecycle: live({ agent: 'codex', state_source: 'codex_hook', cursor: 6, events }) };
        });
        h.reader.open(mac);
        assert.deepEqual(h.latest.messages, []);
        assert.equal(h.latest.pending.request_id, 'unresolved');
        await h.tick();
        assert.equal(h.calls[0].payload.arguments.after_cursor, 0);
        const expected = ['repeat', 'first reply', 'repeat', 'second reply'];
        assert.deepEqual(h.latest.messages.filter(m => ['user', 'assistant'].includes(m.type)).map(m => m.content), expected);
        assert.equal(h.latest.pending.request_id, 'unresolved');
        h.reader.close();
        const reopened = harness(storage, () => ({ lifecycle: live({ agent: 'codex', state_source: 'codex_hook', cursor: 6 }) }));
        reopened.reader.open(mac);
        await reopened.tick();
        assert.equal(reopened.calls[0].payload.arguments.after_cursor, 6);
        assert.deepEqual(reopened.latest.messages.filter(m => ['user', 'assistant'].includes(m.type)).map(m => m.content), expected);
        assert.equal(reopened.latest.pending.request_id, 'unresolved');
        reopened.reader.close();
    }
});

test('queued follow-up survives reload and becomes one durable pending request before transport', async () => {
    const storage = memoryStorage();
    let state = 'working';
    const respond = (_wing, payload) => {
        if (payload.operation === 'session_read') return { lifecycle: live({ state }) };
        const record = readExecution(storage, mac).record;
        assert.equal(record.queued, null);
        assert.equal(record.pending.input, 'Follow up after this turn');
        return { receipt: { status: 'unconfirmed' } };
    };
    const first = harness(storage, respond, { canSendQueued: () => true });
    first.reader.open(mac); await first.tick();
    assert.equal(first.reader.queue('Follow up after this turn'), true);
    const id = first.latest.queued.request_id;
    assert.equal(first.operations().includes('session_prompt'), false);
    assert.equal(queuedExecutions(storage, 'someone-else').length, 0);
    first.reader.close();
    const refs = queuedExecutions(storage, mac.userId);
    assert.equal(refs.length, 1);
    const next = harness(storage, respond, { canSendQueued: () => true });
    next.reader.open(refs[0]); await next.tick();
    assert.equal(next.latest.queued.input, 'Follow up after this turn');
    state = 'idle'; await next.tick(3);
    assert.equal(next.calls.filter(c => c.payload.operation === 'session_prompt').length, 1);
    assert.equal(next.latest.pending.request_id, id);
    next.reader.close();
    const last = harness(storage, respond, { canSendQueued: () => true });
    last.reader.open(mac); await last.tick(3);
    assert.equal(last.latest.pending.request_id, id);
    assert.equal(last.operations().includes('session_prompt'), false);
});

test('queue validates before acceptance and retains saved input when reservation storage fails', async () => {
    const storage = memoryStorage();
    let state = 'working';
    const h = harness(storage, () => ({ lifecycle: live({ state }) }), { canSendQueued: () => true });
    h.reader.open(mac); await h.tick();
    for (const text of ['x'.repeat(65537), 'bad\x1binput']) {
        assert.equal(h.reader.queue(text), false);
        assert.match(h.latest.notice, /64 KiB/);
        assert.equal(h.latest.queued, null);
    }
    assert.equal(h.reader.queue('Keep this follow-up'), true);
    storage.setItem = () => { throw Error('full'); };
    state = 'idle'; await h.tick();
    assert.equal(h.latest.queued.input, 'Keep this follow-up');
    assert.equal(h.latest.pending, null);
    assert.equal(h.operations().includes('session_prompt'), false);
    assert.equal(readExecution(storage, mac).record.queued.input, 'Keep this follow-up');
});

test('disabled storage never accepts a queue or clears the caller draft', async () => {
    const storage = memoryStorage();
    const h = harness(storage, () => ({ lifecycle: live({ state: 'working' }) }));
    h.reader.open(mac); await h.tick();
    storage.setItem = () => { throw Error('disabled'); };
    assert.equal(h.reader.queue('Keep my draft'), false);
    assert.equal(h.latest.queued, null);
    assert.match(h.latest.notice, /not queued/);
});

test('queued sends wait for permission and cannot move to another provider execution', async () => {
    const storage = memoryStorage();
    let state = 'working', allowed = false;
    const h = harness(storage, () => ({ lifecycle: live({ state }) }), { canSendQueued: () => allowed });
    h.reader.open(mac); await h.tick();
    assert.equal(h.reader.queue('Exact provider only'), true);
    state = 'idle'; await h.tick(2);
    assert.equal(h.operations().includes('session_prompt'), false);
    h.reader.close();
    allowed = true;
    const next = harness(storage, () => ({ lifecycle: live({ provider_session_id: 'other-provider' }) }), { canSendQueued: () => allowed });
    next.reader.open(queuedExecutions(storage, mac.userId)[0]); await next.tick(2);
    assert.equal(next.latest.halted, true);
    assert.equal(next.latest.queued.input, 'Exact provider only');
    assert.equal(next.operations().includes('session_prompt'), false);
});


test('a background queued send that is definitely rejected restores its draft after reload', async () => {
    const storage = memoryStorage();
    let state = 'working', resolvePrompt;
    const h = harness(storage, (_wing, payload) => payload.operation === 'session_read'
        ? { lifecycle: live({ state }) } : new Promise(resolve => { resolvePrompt = resolve; }), { canSendQueued: () => true });
    h.reader.open(mac); await h.tick();
    assert.equal(h.reader.queue('Retain rejected follow-up'), true);
    state = 'idle'; await h.tick();
    h.reader.close();
    resolvePrompt({ receipt: { status: 'not_sent', definitely_not_sent: true } }); await flush();
    const next = harness(storage, () => ({ lifecycle: live() }));
    next.reader.open(mac); await next.tick();
    assert.equal(next.reader.takeDraft(), 'Retain rejected follow-up');
    assert.equal(next.latest.pending, null);
    assert.equal(next.latest.queued, null);
});


test('reload discovers durable queued input even if the browser index update failed', async () => {
    const storage = memoryStorage();
    const set = storage.setItem;
    storage.setItem = (key, value) => {
        if (key.startsWith('wt_conversation_execution_index_v1:')) throw Error('index quota');
        set(key, value);
    };
    const h = harness(storage, () => ({ lifecycle: live({ state: 'working' }) }));
    h.reader.open(mac); await h.tick();
    assert.equal(h.reader.queue('Recover without an index'), true);
    h.reader.close();
    assert.equal(queuedExecutions(storage, mac.userId).length, 1);
    assert.equal(queuedExecutions(storage, 'someone-else').length, 0);
});


// Waiting answers and saved follow-ups have separate durable identities.
for (const agent of ['claude', 'codex']) {
    for (const newerInput of [false, true]) {
        test(`${agent} migrated rejection restores its draft across reload; newer input=${newerInput}`, async () => {
            const storage = memoryStorage();
            const original = { ...mac, providerSessionId: 'prov-1' };
            const answer = createPendingInput({ ...original, request_id: 'saved-answer', input: 'Yes', answerToStateCursor: 5, createdAt: 1 });
            const followUp = createPendingInput({ ...original, request_id: 'saved-follow-up', input: 'Later work', createdAt: 2 });
            // This is the old durable format: a pending answer without the
            // answers map or latest-input pointer introduced by newer clients.
            writeExecution(storage, original, { providerSessionId: 'prov-1', pending: answer, queued: followUp,
                ...(newerInput ? { lastInputRequestId: 'newer-input', draftRequestId: 'newer-input', draft: 'Keep my new draft' } : {}) });
            let release;
            const respond = (_wing, payload) => {
                assert.equal(payload.operation, 'session_read', 'reopening must never send an answer or follow-up');
                const result = { lifecycle: live({ agent, state_source: agent + '_hook', state: 'needs_input', ready: false }) };
                if (!payload.arguments.original_request) return result;
                assert.deepEqual(payload.arguments.original_request, { request_id: answer.request_id, provider_session_id: 'prov-1', answer_to_state_cursor: 5 });
                return new Promise(resolve => { release = () => resolve({ ...result, receipt: {
                    request_id: answer.request_id, session_id: original.sessionId, provider_session_id: 'prov-1', answer_to_state_cursor: 5,
                    status: 'not_sent', definitely_not_sent: true,
                } }); });
            };
            const first = harness(storage, respond);
            first.reader.open(original); await first.tick();
            first.reader.close();
            const reopened = harness(storage, respond);
            reopened.reader.open(original); await reopened.tick();
            await reopened.advance(4000);
            release(); await flush();
            assert.equal(reopened.latest.waitingAnswers[0].outcome, 'not_sent');
            assert.equal(reopened.reader.takeDraft(), newerInput ? 'Keep my new draft' : 'Yes');
            assert.equal(reopened.reader.takeDraft(), null);
            assert.equal(reopened.latest.queued.request_id, followUp.request_id);
            assert.equal(reopened.latest.inputReady, true);
            reopened.reader.close();
            const restored = harness(storage, respond);
            restored.reader.open(original); await restored.tick();
            assert.equal(restored.reader.takeDraft(), newerInput ? 'Keep my new draft' : 'Yes', 'the restored text remains durable');
            restored.reader.close();
        });
    }
}

for (const agent of ['claude', 'codex']) {
    for (const reloadAt of [null, 0, 2000, 4000, 14000, 16000]) {
        test(`${agent} waiting preserves one saved follow-up across reload at ${reloadAt}`, async () => {
            const storage = memoryStorage();
            let state = 'working';
            const prompts = [];
            const respond = (_wing, payload) => {
                if (payload.operation === 'session_read') return { lifecycle: live({ agent, state_source: agent + '_hook', state, ready: state === 'idle' }) };
                prompts.push(payload.arguments);
                // Only the ordinary follow-up gets a real native-text receipt.
                assert.equal(state, 'idle');
                assert.equal(payload.arguments.answer_to_state_cursor, undefined);
                return { receipt: { status: 'native_receipt_observed', native_receipt_observed: true } };
            };
            let h = harness(storage, respond, { canSendQueued: () => true });
            h.reader.open(mac); await h.tick();
            assert.equal(h.reader.queue('Saved follow-up'), true);
            const id = h.latest.queued.request_id;
            state = 'needs_input'; await h.tick();
            assert.equal(h.latest.inputReady, true);
            assert.equal(h.reader.queue('Yes'), false);
            if (reloadAt !== null) {
                await h.advance(reloadAt);
                h.reader.close();
                h = harness(storage, respond, { canSendQueued: () => true });
                h.reader.open(mac); await h.tick();
            }
            await h.advance(30000);
            assert.equal(h.reader.takeDraft(), null);
            assert.equal(h.latest.pending, null);
            assert.equal(h.latest.queued.request_id, id);
            assert.equal(prompts.length, 0);
            state = 'working'; await h.tick();
            assert.equal(h.latest.presentation.state, 'working');
            assert.equal(h.latest.queued.input, 'Saved follow-up');
            // The working shell can edit/queue, but the saved input waits for idle.
            state = 'idle'; await h.tick(4);
            assert.equal(h.latest.inputReady, true);
            assert.equal(h.latest.queued, null);
            assert.deepEqual(prompts.map(p => [p.input, p.request_id]), [['Saved follow-up', id]]);
            h.reader.close(); h.reader.open(mac); await h.tick(4);
            assert.equal(prompts.length, 1);
            h.reader.close();
        });
    }

    for (const mode of ['lost response', 'no enqueue', 'delayed before 15s', 'delayed after 15s', 'three second wait', 'checking', 'working', 'before transport', 'terminal resolved', 'navigation']) {
        test(`${agent} waiting-answer failure input: ${mode}`, async () => {
            const storage = memoryStorage();
            const original = {...mac,providerSessionId:'prov-1'};
            let state = 'working', cursor = 5, release, answer, sends = 0, followUps = 0, statusReads = 0;
            const noEnqueue = ['no enqueue','before transport','terminal resolved'].includes(mode);
            let evidence = null;
            const respond = (_wing,payload) => {
                const args = payload.arguments;
                if (payload.operation === 'session_read') {
                    const result = {lifecycle:live({agent,state_source:agent+'_hook',state,state_cursor:cursor,ready:state==='idle'})};
                    if (args.original_request) {
                        statusReads++;
                        assert.equal(args.original_request.request_id,answer.request_id);
                        result.receipt = {session_id:original.sessionId,request_id:answer.request_id,provider_session_id:'prov-1',answer_to_state_cursor:5,
                            status: state==='working' && !noEnqueue ? 'waiting_answer_delivered' : 'unconfirmed',transport_enqueued:!noEnqueue,
                            ...(evidence || {})};
                    }
                    return result;
                }
                if (args.input === 'Saved follow-up') {
                    assert.equal(state,'idle'); followUps++;
                    return {receipt:{status:'native_receipt_observed',native_receipt_observed:true}};
                }
                answer = args;
                assert.equal(args.input,'Yes'); assert.equal(args.answer_to_state_cursor,5);
                const saved = readExecution(storage,original).record;
                assert.equal(saved.answers[args.request_id].input,'Yes','reservation precedes transport');
                assert.equal(saved.queued.input,'Saved follow-up');
                if (!noEnqueue) sends++;
                if (['delayed before 15s','delayed after 15s','three second wait','before transport','navigation'].includes(mode)) return new Promise(resolve=>{release=resolve;});
                throw Error('response lost');
            };
            let h = harness(storage,respond,{canSendQueued:()=>true});
            h.reader.open(original); await h.tick();
            assert.equal(h.reader.queue('Saved follow-up'),true);
            state='needs_input'; await h.tick();
            assert.equal(h.latest.inputReady,true,'waiting accepts a typed answer despite saved follow-up');
            const sending=h.reader.send('Yes'); await flush();
            assert.equal(h.latest.inputReady,false,'one answer per prompt');
            h.reader.close(); h=harness(storage,respond,{canSendQueued:()=>true});
            h.reader.open(original); await h.tick();
            assert.equal(h.calls.some(c=>c.payload.operation==='session_prompt'),false,'reopening only reads receipts');
            if (mode==='terminal resolved' || mode==='before transport') evidence={status:'not_sent',definitely_not_sent:true};
            if (release) {
                await h.advance(mode==='delayed after 15s'?16000:mode==='three second wait'?3000:2000);
                release({receipt:{session_id:original.sessionId,request_id:answer.request_id,provider_session_id:'prov-1',answer_to_state_cursor:5,status:'unconfirmed',transport_enqueued:!noEnqueue}});
            }
            await sending;
            state='working';cursor++;
            await h.advance(30000); await h.tick();
            assert.equal(h.latest.pending,null);
            assert.equal(h.latest.inputReady,true,'working composer stays enabled');
            assert.equal(h.latest.deliveryUnknown,noEnqueue && !evidence);
            if (evidence) assert.equal(h.reader.takeDraft(),'Yes');
            else assert.equal(h.reader.takeDraft(),null,'fresh working alone cannot return a draft');
            assert.ok(statusReads>0,'automatic status recovery');
            if (mode==='navigation') {
                h.reader.open({...original,wingId:'sibling'});await h.tick();
                assert.equal(h.latest.waitingAnswers.length,0);assert.equal(h.latest.queued,null);
                h.reader.open(original);await h.tick();
            }
            state='idle';cursor++;await h.tick(4);
            assert.equal(followUps,1);assert.equal(sends,noEnqueue?0:1);
            h.reader.close();h.reader.open(original);await h.tick(4);
            assert.equal(followUps,1);assert.equal(h.calls.some(c=>c.payload.operation==='session_prompt' && c.payload.arguments.input==='Yes'),false);
            h.reader.close();
        });
    }
}

for (const receipt of [
    { status: 'native_receipt_observed', native_receipt_observed: true },
    { status: 'not_sent', definitely_not_sent: true, reason: 'Writer changed' },
    { status: 'unconfirmed' },
]) {
    test(`saved follow-up reload merges a delayed ${receipt.status} receipt without another Send`, async () => {
        const storage = memoryStorage();
        let state = 'working', release;
        const prompts = [];
        const respond = (_wing, payload) => {
            if (payload.operation === 'session_read') return { lifecycle: live({ state, ready: state === 'idle' }) };
            prompts.push(payload.arguments);
            return new Promise(resolve => { release = resolve; });
        };
        const first = harness(storage, respond, { canSendQueued: () => true });
        first.reader.open(mac); await first.tick();
        first.reader.queue('Saved follow-up');
        state = 'needs_input'; await first.tick(3);
        first.reader.close();
        const next = harness(storage, respond, { canSendQueued: () => true });
        next.reader.open(mac); await next.tick();
        state = 'working'; await next.tick();
        assert.equal(next.latest.pending, null);
        state = 'idle'; await next.tick();
        assert.equal(prompts.length, 1);
        next.reader.close();
        const reopened = harness(storage, respond, { canSendQueued: () => true });
        reopened.reader.open(mac); await reopened.tick();
        await reopened.advance(4000);
        release({ receipt }); await flush();
        await reopened.tick(4);
        assert.equal(prompts.length, 1);
        assert.equal(reopened.latest.pending?.status || null, receipt.status === 'unconfirmed' ? 'unconfirmed' : null);
        assert.equal(reopened.reader.takeDraft(), receipt.status === 'not_sent' ? 'Saved follow-up' : null);
        reopened.reader.close(); reopened.reader.open(mac); await reopened.tick(4);
        assert.equal(prompts.length, 1);
        reopened.reader.close();
    });
}

test('competing readers reserve a saved follow-up once and preserve a newer draft', async () => {
    const storage = memoryStorage();
    let state = 'working', release;
    const calls = [];
    const respond = (_wing, payload) => payload.operation === 'session_read'
        ? { lifecycle: live({ state }) }
        : (calls.push(payload), new Promise(resolve => { release = resolve; }));
    const first = harness(storage, respond, { canSendQueued: () => true });
    first.reader.open(mac); await first.tick();
    first.reader.queue('Saved follow-up');
    const second = harness(storage, respond, { canSendQueued: () => true });
    second.reader.open(mac); await second.tick();
    state = 'idle'; await first.tick(); await second.tick();
    assert.equal(calls.length, 1);
    release({ receipt: { status: 'not_sent', definitely_not_sent: true } }); await flush();
    await second.tick();
    assert.equal(second.reader.takeDraft(), 'Saved follow-up');
    assert.equal(second.latest.pending, null);
    first.reader.close(); second.reader.close();
});

const bridgeTraces = process.env.SESSION_ANSWER_BRIDGE
    ? JSON.parse((await import('node:fs')).readFileSync(process.env.SESSION_ANSWER_BRIDGE, 'utf8')) : [];
for (const trace of bridgeTraces) for (const reloadAt of [0,2000,4000,14000,16000]) for (const responseDelay of [3000,16000]) {
    test(`${trace.agent} production bridge sends an explicit answer; reload=${reloadAt}; delay=${responseDelay}`, async () => {
        const storage = memoryStorage();
        const ref = {userId:'owner',wingId:'bridge-machine',sessionId:trace.receipt.session_id,providerSessionId:trace.receipt.provider_session_id};
        const start = trace.receipt.enqueued_at_ms;
        let h, release, prompts=0, lookups=0, resolved=false;
        const respond = (_wing,payload) => {
            if (payload.operation==='session_prompt') {
                prompts++;
                assert.equal(payload.arguments.answer_to_state_cursor,trace.initial.state_cursor);
                assert.equal(payload.arguments.timeout_seconds,3);
                const saved = readExecution(storage,ref).record;
                assert.equal(saved.answers[payload.arguments.request_id].input,'Yes');
                return new Promise(resolve=>{release=()=>resolve({receipt:{...trace.receipt,request_id:payload.arguments.request_id}});});
            }
            assert.equal(payload.operation,'session_read');
            const result={session:ref.sessionId,lifecycle:resolved?trace.working:trace.initial};
            if(payload.arguments.original_request) {
                lookups++;
                const id=payload.arguments.original_request.request_id;
                const elapsed=h.time-start;
                const receipt=resolved?trace.delivered:elapsed<3000?trace.missing:[...trace.statuses].reverse().find(s=>s.observed_at_ms<=h.time)||trace.receipt;
                if (!resolved && elapsed >= 3000) assert.equal(receipt.observed_at_ms, h.time, 'receipt-only lookup returns a fresh server observation');
                result.receipt={...receipt,request_id:id};
            }
            return result;
        };
        const make = at => harness(storage,respond,{canSendQueued:()=>true,clockStart:at});
        h=make(start);h.reader.open(ref);await h.tick();
        assert.equal(h.latest.inputReady,true);
        const sending=h.reader.send('Yes');await flush();
        const id=readExecution(storage,ref).record.lastInputRequestId;
        await h.advance(Math.min(reloadAt,responseDelay));
        if(responseDelay<=reloadAt) {release();await sending;await h.advance(reloadAt-responseDelay);}
        const at=h.time;h.reader.close();h=make(at);h.reader.open(ref);await h.tick();
        assert.equal(prompts,1,'reopening never sends');
        if(responseDelay>reloadAt) {await h.advance(responseDelay-reloadAt);release();await sending;await h.tick();}
        assert.equal(prompts,trace.sends);
        if(h.time<start+15000) {
            assert.ok(h.latest.waitingRemainingMS<=start+15000-h.time,'deadline never restarts on response/reload');
            await h.advance(start+15000-h.time);await flush();
            assert.equal(h.latest.waitingAnswers[0].outcome,'expired','deadline forces a fresh receipt read at t=15');
        }
        await h.tick();
        assert.equal(h.latest.waitingAnswers[0].outcome,'expired');
        assert.equal(h.latest.waitingAnswers[0].transportEnqueued,true);
        assert.equal(h.latest.waitingAnswers[0].receipt.definitely_not_sent,false);
        assert.equal(h.reader.takeDraft(),'Yes');
        assert.equal(h.latest.inputReady,true);
        assert.equal(h.latest.notice,'Not sent - try again');
        resolved=true;await h.tick(3);
        assert.equal(h.latest.deliveryReceipt.status,'waiting_answer_delivered');
        assert.equal(h.latest.deliveryReceipt.provider_request_acknowledged,false);
        assert.equal(h.reader.takeDraft(),null,'late delivery clears only the original draft');
        assert.ok(lookups>0);assert.equal(prompts,1);
        h.reader.close();h.reader.open(ref);await h.tick(3);
        assert.equal(h.reader.takeDraft(),null);assert.equal(prompts,1);h.reader.close();
    });
}

for (const agent of ['claude', 'codex']) for (const navigate of [false, true]) {
    test(`${agent} saved follow-up requeues when needs_input wins the dispatch race; navigate=${navigate}`, async () => {
        const storage = memoryStorage();
        let phase = 'working', release, cursor = 5;
        const receipts = new Map();
        const dispatch = new Promise(resolve => { release = resolve; });
        const respond = async (_wing, payload) => {
            if (payload.operation === 'session_read') return { lifecycle: live({agent,state:phase,state_source:agent+'_hook',state_cursor:cursor}) };
            const id = payload.arguments.request_id;
            if (receipts.has(id)) return {receipt:receipts.get(id)};
            if (receipts.size === 0) {
                await dispatch;
                receipts.set(id,{status:'not_sent',definitely_not_sent:true,reason:'Not sent - try again'});
            } else receipts.set(id,{status:'native_receipt_observed',native_receipt_observed:true});
            return {receipt:receipts.get(id)};
        };
        const first = harness(storage,respond,{canSendQueued:()=>true});
        first.reader.open(mac); await first.tick();
        assert.equal(first.reader.queue('Saved follow-up'),true);
        phase='idle'; cursor++; await first.tick();
        assert.equal(first.calls.filter(c=>c.payload.operation==='session_prompt').length,1);
        phase='needs_input'; cursor++;
        if (navigate) first.reader.close(); // rejection can arrive after leaving the page
        release(); await flush();
        const rejected=readExecution(storage,mac).record;
        assert.equal(rejected.pending,null);
        assert.equal(rejected.queued.input,'Saved follow-up');
        assert.equal(rejected.draft,null);
        first.reader.close();
        const next=harness(storage,respond,{canSendQueued:()=>true});
        next.reader.open(mac); await next.tick(3);
        assert.equal(next.latest.queued.input,'Saved follow-up');
        assert.equal(next.calls.filter(c=>c.payload.operation==='session_prompt').length,0);
        phase='working'; cursor++; await next.tick(2);
        assert.equal(next.calls.filter(c=>c.payload.operation==='session_prompt').length,0);
        phase='idle'; cursor++; await next.tick(3);
        const sends=next.calls.filter(c=>c.payload.operation==='session_prompt');
        assert.equal(sends.length,1);
        assert.equal(sends[0].payload.arguments.input,'Saved follow-up');
        assert.notEqual(sends[0].payload.arguments.request_id,first.calls.find(c=>c.payload.operation==='session_prompt').payload.arguments.request_id);
        assert.equal(next.latest.queued,null);
        assert.equal(next.latest.pending,null);
        next.reader.close();
        const reopened=harness(storage,respond,{canSendQueued:()=>true});
        reopened.reader.open(mac); await reopened.tick(4);
        assert.equal(reopened.calls.filter(c=>c.payload.operation==='session_prompt').length,0);
        reopened.reader.close();
    });
}

for (const agent of ['claude','codex']) test(`${agent} expiry and late delivery never overwrite newer pending input`, async () => {
    const storage=memoryStorage(), original={...mac,providerSessionId:'prov-1'};
    let state='needs_input',cursor=5, status='unconfirmed', oldId, releaseNew;
    const receipt = () => ({session_id:mac.sessionId,request_id:oldId,provider_session_id:'prov-1',answer_to_state_cursor:5,status,transport_enqueued:true,definitely_not_sent:false});
    const h=harness(storage,(_wing,payload)=>{
        if(payload.operation==='session_read') return {lifecycle:live({agent,state_source:agent+'_hook',state,state_cursor:cursor}),...(payload.arguments.original_request ? {receipt:receipt()} : {})};
        if(payload.arguments.answer_to_state_cursor===5) {oldId=payload.arguments.request_id;return {receipt:receipt()};}
        return new Promise(resolve=>{releaseNew=resolve;});
    },{canSendQueued:()=>true});
    h.reader.open(original);await h.tick();await h.reader.send('Yes');
    status='waiting_answer_expired';await h.advance(2000);assert.equal(h.reader.takeDraft(),'Yes');
    state='idle';cursor++;await h.tick();
    const sending=h.reader.send('New work');await flush();
    assert.equal(h.latest.pending.input,'New work');
    status='waiting_answer_delivered';await h.advance(4000);
    assert.equal(h.latest.pending.input,'New work');assert.equal(h.reader.takeDraft(),null);
    h.reader.close();h.reader.open(original);await h.tick();
    assert.equal(h.latest.pending.input,'New work');assert.equal(h.reader.takeDraft(),null);
    releaseNew({receipt:{status:'unconfirmed'}});await sending;h.reader.close();
});
