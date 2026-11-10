import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { parseJSONL } from '../src/chat.js';
import { applyConversationRead, emptyConversationState, conversationExecutionPresentation, conversationInputReady } from '../src/conversation-state.js';

const fixture = JSON.parse(readFileSync(new URL('./fixtures/codex-native.json', import.meta.url)));
const fresh = { observedAt: 100, now: 100 };
const hook = { ...fixture, state: 'completed', state_source: 'codex_hook', ready: true };

test('native Codex prompt, text, tool, completion and future records remain ordered across replay and delayed pages', () => {
    const first = applyConversationRead(emptyConversationState(), { ...fixture, events: fixture.events.slice(2) });
    const state = applyConversationRead(first, fixture);
    assert.deepEqual(state.messages.filter(m => m.type !== 'activity').map(m => [m.sequence, m.type]), [
        [1, 'user'], [2, 'assistant'], [3, 'assistant'], [4, 'assistant'], [5, 'provider_record'], [5, 'lifecycle'], [6, 'provider_record'],
    ]);
    assert.equal(state.messages.find(m => m.sequence === 3).toolCalls[1].result, JSON.stringify({ content: 'The checklist contains eight remaining validation tasks.' }, null, 2));
    assert.match(state.messages.at(-1).content, /futureEvidence/);
    assert.match(state.messages.at(-1).content, /<script>/);
    assert.equal(conversationExecutionPresentation(state.lifecycle, fresh).inputReady, false);
    assert.deepEqual(applyConversationRead(state, fixture).messages, state.messages);
});

test('only fresh exact Codex hook state enables the existing prompt path', () => {
    for (const state of ['idle', 'completed']) {
        assert.equal(conversationInputReady({ ...hook, state }), true);
        assert.equal(conversationExecutionPresentation({ ...hook, state }, fresh).inputReady, true);
    }
    for (const changes of [{ provider_session_id: '' }, { ready: false }, { state: 'working' }, { state: 'needs_input' }, { state: 'unknown' }, { state_source: 'codex_app_server' }, { state_source: '' }, { state_source: 'claude_hook' }, { agent: 'future' }, { process_alive: false }]) {
        assert.equal(conversationInputReady({ ...hook, ...changes }), false, JSON.stringify(changes));
    }
    for (const evidence of [{ cached: true }, { readError: 'offline' }, { now: 60100 }]) {
        assert.equal(conversationExecutionPresentation(hook, { ...fresh, ...evidence }).inputReady, false);
    }
    assert.equal(conversationExecutionPresentation(hook, fresh).state, 'turn_completed');
    assert.equal(conversationExecutionPresentation({ ...hook, state_source: 'codex_app_server' }, fresh).state, 'unknown');
});

test('normalized hook tools, completion and unknown variants stay visible without raw JSON', () => {
    const state = applyConversationRead(emptyConversationState(), { ...hook, events: [
        { sequence: 0, type: 'prompt_submitted', state: 'working', source: 'codex_hook', text: 'Exact accepted prompt' },
        { sequence: 1, type: 'tool_activity', state: 'working', source: 'codex_hook' },
        { sequence: 2, type: 'turn_completed', state: 'completed', source: 'codex_hook' },
        { sequence: 3, type: 'future_hook', detail: 'inspect me' },
    ] });
    assert.equal(state.messages.some(m => m.type === 'user'), false);
    assert.equal(state.sending.sequence, 0);
    assert.equal(state.messages.find(m => m.sequence === 1 && m.type === 'lifecycle').kind, 'tool');
    assert.equal(state.messages.find(m => m.sequence === 2 && m.type === 'lifecycle').kind, 'turn');
    assert.match(state.messages.at(-1).content, /inspect me/);
});

test('Codex partial and unfamiliar content is inspectable and cannot masquerade as a finished answer', () => {
    for (const raw of [null, { method: 'item/started', params: { item: { type: 'agentMessage', text: 'partial' } } }, { method: 'item/completed', params: { item: { type: 'userMessage', content: [{ type: 'image', url: 'fixture' }] } } }, { method: 'future/event' }]) {
        assert.equal(parseJSONL(JSON.stringify(raw), 'codex')[0].type, 'provider_record');
    }
});

test('Claude parser output stays identical for text, thinking, tools and ignored snapshots', () => {
    const records = [
        { type: 'user', message: { role: 'user', content: 'Inspect the checklist' } },
        { type: 'assistant', message: { role: 'assistant', content: [{ type: 'text', text: 'Checking' }, { type: 'thinking', thinking: 'Inspect first' }, { type: 'tool_use', id: 'read-1', name: 'Read', input: { path: 'checklist.md' } }] } },
        { type: 'user', message: { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'read-1', content: 'Ready' }] } },
        { type: 'file-history-snapshot' }, { type: 'summary' },
    ];
    assert.deepEqual(parseJSONL(records.map(record => JSON.stringify(record)).join('\n'), 'claude'), [
        { type: 'user', content: 'Inspect the checklist' },
        { type: 'assistant', content: 'Checking', thinking: 'Inspect first', toolCalls: [{ id: 'read-1', name: 'Read', input: { path: 'checklist.md' } }] },
        { type: 'tool_result', name: 'read-1', content: 'Ready' },
    ]);
});
