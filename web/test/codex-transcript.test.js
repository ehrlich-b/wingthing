import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { emptyConversationState, applyConversationRead, restoredConversationState } from '../src/conversation-state.js';

const fixtures = JSON.parse(readFileSync(new URL('./fixtures/codex-prompt-order.json', import.meta.url)));
const rows = state => state.messages.filter(m => ['user', 'assistant'].includes(m.type)).map(m => m.content);
const read = (state, events, now = 1000) => applyConversationRead(state, { agent: 'codex', cursor: Math.max(0, ...events.map(e => e.sequence)), events }, { now });

for (const [name, events] of Object.entries(fixtures)) {
    for (const chunked of [false, true]) {
        test(`${name}: only native rows, once each in transcript order (${chunked ? 'partial reads' : 'one read'})`, () => {
            let state = emptyConversationState();
            if (chunked) {
                state = read(state, events.slice(0, 2));
                assert.deepEqual(rows(state), []);
                assert.equal(state.sending.sequence, 2);
                state = read(state, events.slice(2, 3));
                assert.deepEqual(rows(state), ['repeat']);
                assert.equal(state.sending, null);
                state = read(state, events.slice(3, 4));
                assert.deepEqual(rows(state), ['repeat', 'first reply']);
                state = restoredConversationState(JSON.parse(JSON.stringify(state)));
                state = read(state, events.slice(4));
            } else state = read(state, events);
            const expected = ['repeat', 'first reply', events[4].text, 'second reply'];
            assert.deepEqual(rows(state), expected);
            assert.equal(state.sending, null);
            state = read(state, events);
            assert.deepEqual(rows(state), expected);
            state = restoredConversationState(JSON.parse(JSON.stringify(state)));
            assert.deepEqual(rows(read(state, events)), expected);
        });
    }
}

test('missing IDs on either hook or transcript never change row identity or order', () => {
    for (const missing of ['hooks', 'transcript', 'both']) {
        const events = fixtures.repeated.map(event => {
            const copy = { ...event };
            if (missing === 'both' || (missing === 'hooks') === (event.source === 'codex_hook')) delete copy.turn_id;
            return copy;
        });
        assert.deepEqual(rows(read(emptyConversationState(), events)), ['repeat', 'first reply', 'repeat', 'second reply']);
    }
});

test('the independent no-ID probe produces exactly hello then reply', () => {
    assert.deepEqual(rows(read(emptyConversationState(), [
        { sequence: 1, source: 'codex_hook', type: 'prompt_submitted', text: 'hello' },
        { sequence: 2, source: 'codex_transcript', type: 'message', role: 'user', text: 'hello' },
        { sequence: 3, source: 'codex_transcript', type: 'message', role: 'assistant', text: 'reply' },
    ])), ['hello', 'reply']);
});

test('sending clears on the next native user regardless of text and optional IDs', () => {
    let state = read(emptyConversationState(), fixtures.repeated.slice(0, 1));
    assert.deepEqual(state.messages, []); // Even raw hook JSON is not a row.
    assert.equal(state.sending.expiresAt, 61000);
    state = read(state, [{ sequence: 3, source: 'codex_transcript', type: 'message', role: 'user', text: 'different' }]);
    assert.equal(state.sending, null);
    assert.deepEqual(rows(state), ['different']);
});

test('sending expires after 60 seconds, and hook replay cannot extend it', () => {
    const hook = fixtures.repeated[0];
    let state = read(emptyConversationState(), [hook]);
    state = read(state, [hook], 60999);
    assert.equal(state.sending.expiresAt, 61000);
    state = read(state, [], 61000);
    assert.equal(state.sending, null);
    assert.deepEqual(rows(read(state, [hook], 62000)), []);
    assert.equal(read(state, [hook], 62000).sending, null);
    assert.equal(read(emptyConversationState(), [{ ...hook, timestamp: new Date(1000).toISOString() }], 61000).sending, null);
});

test('delayed hooks and out-of-order pages never rearrange transcript rows', () => {
    let state = read(emptyConversationState(), fixtures.repeated.slice(4));
    state = read(state, fixtures.repeated.slice(0, 4));
    assert.deepEqual(rows(state), ['repeat', 'first reply', 'repeat', 'second reply']);
    assert.equal(state.sending, null);
    state = read(state, [{ sequence: 7, source: 'codex_hook', type: 'prompt_submitted', text: 'repeat' }]);
    assert.deepEqual(rows(state), ['repeat', 'first reply', 'repeat', 'second reply']);
    assert.equal(state.sending.sequence, 7);
});

test('restoring an older cache discards hook fallback rows and transient sending state', () => {
    const state = restoredConversationState({ lifecycle: { agent: 'codex' }, messages: [
        { type: 'user', source: 'codex_hook', sequence: 1, content: 'repeat' },
        { type: 'user', source: 'codex_transcript', sequence: 2, content: 'repeat' },
    ], sending: { sequence: 1, expiresAt: 61000 }, cursor: 2 });
    assert.deepEqual(rows(state), ['repeat']);
    assert.equal(state.sending, null);
});
