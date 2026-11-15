// A pinned-source diagnostic, deliberately separate from the ordinary suite.
// It detects known failures and records working safeguards without changing
// production modules or manufacturing a waiting-answer delivery receipt.
import assert from 'node:assert/strict';
import { readFile, writeFile } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import path from 'node:path';
import { pathToFileURL } from 'node:url';

const [source, observationFile, output] = process.argv.slice(2);
assert.ok(source && observationFile && output, 'source directory, transport observation and output required');
const manifest = JSON.parse(await readFile(new URL('./fixtures/session-answer-subject.json', import.meta.url)));
for (const [file, hash] of Object.entries(manifest.files)) {
    assert.equal(createHash('sha256').update(await readFile(path.join(source, file))).digest('hex'), hash, file);
}
const transport = JSON.parse(await readFile(observationFile));
assert.equal(transport.receipt.status, 'unconfirmed');
assert.equal(transport.receipt.transport_enqueued, true);
assert.equal(transport.receipt.native_receipt_observed, false);
assert.equal(transport.replay.status, 'waiting_answer_delivered');
assert.equal(transport.sends, 1);
const { createConversationReader } = await import(pathToFileURL(path.resolve(source, 'web/src/conversation-recovery.js')));
const { readExecution } = await import(pathToFileURL(path.resolve(source, 'web/src/conversation-recovery-store.js')));
const flush = () => new Promise(resolve => setImmediate(resolve));
const ref = { userId: 'owner', wingId: 'machine', sessionId: 'session', conversationId: 'conversation', providerSessionId: 'provider' };
const results = [];

function world(agent = 'claude') {
    const values = new Map(), calls = [], timers = [];
    const storage = { getItem: k => values.get(k) ?? null, setItem: (k, v) => values.set(k, String(v)), removeItem: k => values.delete(k), get length() { return values.size; }, key: i => [...values.keys()][i] ?? null };
    let clock = 1000000, nextId = 0, current = 'working', cursor = 5, handler;
    let answerSends = 0, followUpSends = 0;
    const enqueued = new Set();
    const w = {
        calls, storage, start: clock,
        get time() { return clock; },
        get answerSends() { return answerSends; },
        get followUpSends() { return followUpSends; },
        set state(s) { if (s !== current) cursor++; current = s; },
        get state() { return current; },
        handle(fn) { handler = fn; },
        enqueue(p) { if (!enqueued.has(p.arguments.request_id)) { enqueued.add(p.arguments.request_id); answerSends++; } },
        receipt(p, r) { return { session: p.arguments.session, receipt: { ...r, request_id: p.arguments.request_id, session_id: p.arguments.session } }; },
        reader() {
            return createConversationReader({ storage, now: () => clock, randomId: () => `request-${++nextId}`, canSendQueued: () => true,
                schedule(fn, ms) { const t = { fn, due: clock + ms, cancelled: false }; timers.push(t); return t; },
                cancel(t) { t.cancelled = true; },
                async request(wingId, payload) {
                    calls.push({ wingId, payload, at: clock });
                    if (payload.operation === 'session_read') return { session: payload.arguments.session, lifecycle: {
                        session_id: payload.arguments.session, agent, provider_session_id: 'provider', state: current, state_source: agent + '_hook',
                        process_alive: true, ready: current === 'idle', state_cursor: cursor, cursor, head_cursor: cursor, events: [], has_more: false,
                    } };
                    if (payload.arguments.input === 'Saved follow-up') {
                        assert.ok(!enqueued.has(payload.arguments.request_id), 'follow-up transport must run once');
                        enqueued.add(payload.arguments.request_id); followUpSends++;
                        return w.receipt(payload, { status: 'native_receipt_observed', native_receipt_observed: true });
                    }
                    return handler(payload);
                },
            });
        },
        async advance(ms) {
            const end = clock + ms;
            let iterations = 0;
            for (;;) {
                timers.sort((a, b) => a.due - b.due);
                while (timers[0]?.cancelled) timers.shift();
                if (!timers[0] || timers[0].due > end) break;
                assert.ok(++iterations < 1000, 'timer progress');
                const t = timers.shift(); clock = Math.max(clock, t.due); t.fn(); await flush();
            }
            clock = end; await flush();
        },
    };
    return w;
}

function summary(w, reader) {
    const s = reader.snapshot();
    return { elapsed: w.time - w.start, state: s.lifecycle?.state, pending: s.pending?.status ?? null,
        enqueueEvidence: s.pending?.transportEnqueued === true, inputReady: s.inputReady,
        // Same working-state edit condition as the session composer.
        composerEditable: s.lifecycle?.state === 'working' && !s.pending,
        queued: s.queued?.input ?? null, notice: s.notice, draft: readExecution(w.storage, ref).record?.draft ?? null,
        answerSends: w.answerSends, followUpSends: w.followUpSends };
}

async function begin(w, saved = true) {
    const r = w.reader(); r.open(ref); await w.advance(0);
    if (saved) assert.equal(r.queue('Saved follow-up'), true);
    w.state = 'needs_input'; r.refresh(); await w.advance(0);
    return r;
}

for (const agent of ['claude', 'codex']) {
    // The terminal received the answer, but the browser lost its response.
    const w = world(agent);
    w.handle(p => { w.enqueue(p); w.state = 'working'; throw Error('response lost after enqueue'); });
    let r = await begin(w);
    await r.send('Yes'); await w.advance(30000);
    const deadlock = summary(w, r);
    assert.equal(deadlock.pending, 'unconfirmed'); assert.equal(deadlock.inputReady, false);
    assert.equal(deadlock.composerEditable, false); assert.equal(deadlock.enqueueEvidence, false);
    assert.equal(deadlock.elapsed, 30000); assert.equal(w.answerSends, 1);
    r.close(); r = w.reader(); r.open(ref); await w.advance(0);
    assert.equal(r.snapshot().pending.status, 'unconfirmed');
    w.state = 'idle'; r.refresh(); await w.advance(6000);
    assert.equal(w.followUpSends, 0, 'saved follow-up is blocked at idle too');
    // The real backend's same-request replay resolves this without another Send.
    const original = w.calls.find(c => c.payload.operation === 'session_prompt');
    w.handle(p => { assert.deepEqual(p, original.payload); return w.receipt(p, transport.replay); });
    await r.checkReceipt(); await w.advance(6000);
    assert.equal(r.snapshot().pending, null); assert.equal(w.answerSends, 1); assert.equal(w.followUpSends, 1);
    r.close(); r = w.reader(); r.open(ref); await w.advance(6000);
    assert.equal(w.followUpSends, 1);
    results.push({ scenario: `${agent}: lost response, working for 30s, reload, idle, exact replay`, finding: 'reproduced composer and follow-up deadlock', deadlock, recovery: summary(w, r), requestIdentity: original.payload.arguments.request_id }); r.close();
}

// Identical fresh working reads also occur if the answer was never enqueued
// and someone answered in the terminal. Those reads cannot establish delivery.
{
    const w = world();
    w.handle(() => { w.state = 'working'; throw Error('no enqueue evidence'); });
    const r = await begin(w, false); await r.send('Yes'); await w.advance(30000);
    assert.equal(r.snapshot().pending.status, 'unconfirmed'); assert.equal(w.answerSends, 0);
    results.push({ scenario: 'fresh working reads without enqueue evidence', finding: 'delivery remains unknown; state alone cannot prove sent', observed: summary(w, r) }); r.close();
}

// Feed the actual Go response after its measured three-second wait. The
// backend enqueued at t=0; the reader incorrectly starts its clock at response.
{
    const w = world(); let release;
    w.handle(p => { w.enqueue(p); return new Promise(resolve => { release = () => resolve(w.receipt(p, transport.receipt)); }); });
    let r = await begin(w, false); const sending = r.send('Yes');
    const delay = transport.response_delay_ms;
    await w.advance(delay); release(); await sending;
    const stamp = r.snapshot().pending.transportEnqueuedAt - w.start;
    assert.equal(stamp, delay);
    r.close(); r = w.reader(); r.open(ref); await w.advance(0);
    await w.advance(15000 - delay);
    const at15s = summary(w, r); assert.equal(at15s.pending, 'unconfirmed');
    await w.advance(delay - 1); assert.ok(r.snapshot().pending);
    await w.advance(1);
    const expired = summary(w, r);
    assert.equal(expired.pending, null); assert.equal(expired.elapsed, delay + 15000);
    assert.equal(expired.notice, 'Not sent - try again'); assert.equal(expired.draft, 'Yes');
    r.close(); r = w.reader(); r.open(ref); await w.advance(0);
    assert.equal(r.takeDraft(), 'Yes'); assert.equal(w.answerSends, 1);
    results.push({ scenario: 'real three-second receipt wait; reload after response and after expiry', finding: 'reproduced late timeout', responseDelayMs: delay, browserEnqueueStampMs: stamp, at15s, expired }); r.close();
}

for (const reload of [false, true]) {
    const w = world(); let release;
    w.handle(p => { w.enqueue(p); return new Promise(resolve => { release = () => resolve(w.receipt(p, transport.receipt)); }); });
    let r = await begin(w); const sending = r.send('Yes');
    if (reload) { r.close(); r = w.reader(); r.open(ref); await w.advance(0); }
    w.state = 'working'; await w.advance(30000);
    assert.equal(r.snapshot().pending.status, 'pending');
    assert.equal(r.takeDraft(), null);
    release(); await sending; await w.advance(1500);
    if (reload) {
        // A replacement controller never reloads the old controller's updated
        // storage evidence; a later full reopen can recover it.
        assert.ok(r.snapshot().pending); const stuck = summary(w, r);
        r.close(); r = w.reader(); r.open(ref); await w.advance(0);
        assert.equal(r.snapshot().pending, null);
        results.push({ scenario: 'reload before delayed response', finding: 'new controller retains stale reservation until another reopen', observed: stuck });
    } else assert.equal(r.snapshot().pending, null);
    w.state = 'idle'; r.refresh(); await w.advance(6000);
    assert.equal(w.followUpSends, 1); assert.equal(w.answerSends, 1);
    results.push({ scenario: `delayed response after working; reload=${reload}`, safeguard: 'no false unsent draft and follow-up drains exactly once after evidence recovery', observed: summary(w, r) }); r.close();
}

{
    const w = world(); let dispatch;
    w.handle(p => {
        const reserved = readExecution(w.storage, ref).record.pending;
        assert.equal(reserved.request_id, p.arguments.request_id);
        assert.equal(reserved.answerToStateCursor, p.arguments.answer_to_state_cursor);
        return new Promise(resolve => { dispatch = () => {
            assert.equal(w.state, 'working', 'terminal resolved prompt before input dispatch');
            resolve(w.receipt(p, { status: 'not_sent', definitely_not_sent: true }));
        }; });
    });
    let r = await begin(w, false); const sending = r.send('Yes');
    r.close(); r = w.reader(); r.open(ref); await w.advance(0);
    assert.ok(r.snapshot().pending); assert.equal(w.answerSends, 0);
    w.state = 'working'; dispatch(); await sending; await w.advance(30000);
    assert.equal(w.answerSends, 0);
    r.close(); r = w.reader(); r.open(ref); await w.advance(0);
    assert.equal(r.takeDraft(), null); assert.equal(r.snapshot().pending.input, 'Yes');
    results.push({ scenario: 'reload after durable reservation but before enqueue', finding: 'replacement reader overwrites late not-sent evidence and restored draft with its stale reservation', observed: summary(w, r) }); r.close();
}

{
    const w = world();
    w.handle(p => { w.state = 'idle'; return w.receipt(p, { status: 'not_sent', definitely_not_sent: true }); });
    const r = await begin(w, false); await r.send('Yes'); await w.advance(30000);
    assert.equal(w.answerSends, 0); assert.equal(r.takeDraft(), 'Yes'); assert.equal(r.snapshot().pending, null);
    results.push({ scenario: 'prompt resolved in terminal before transport', safeguard: 'explicit not-sent evidence returns the draft without a send', observed: summary(w, r) }); r.close();
}

{
    const w = world(); let release;
    w.handle(p => { w.enqueue(p); return new Promise(resolve => { release = () => resolve(w.receipt(p, transport.receipt)); }); });
    const r = await begin(w, false), sending = r.send('Yes');
    r.open({ ...ref, userId: 'other-owner', wingId: 'other-machine', sessionId: 'other-session' }); await w.advance(0);
    release(); await sending;
    const stored = readExecution(w.storage, ref).record.pending;
    assert.equal(stored.input, 'Yes'); assert.equal(stored.sessionId, ref.sessionId); assert.equal(stored.wingId, ref.wingId); assert.equal(stored.providerSessionId, ref.providerSessionId);
    assert.equal(r.snapshot().pending, null); assert.equal(r.takeDraft(), null);
    results.push({ scenario: 'navigation while transport response is delayed', safeguard: 'late evidence stays with original user, machine, session, provider and request', original: stored }); r.close();
}

const report = { subject: manifest.head, realTransport: transport, results };
await writeFile(output, JSON.stringify(report, null, 2) + '\n');
for (const row of results) console.log(`${row.scenario}: ${row.finding || row.safeguard}`);
console.log(`Verified ${results.length} diagnostic scenarios against ${manifest.head}`);
