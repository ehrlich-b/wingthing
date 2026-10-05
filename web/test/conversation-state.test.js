import test from 'node:test';
import assert from 'node:assert/strict';
import { emptyConversationState, applyConversationRead, conversationInputReady, orderConversationTree } from '../src/conversation-state.js';

test('assistant transcript chunk leaves working state until native completion', function() {
    var view = { agent: 'claude', state_source: 'claude_hook', state: 'working', ready: false, process_alive: true, cursor: 2, events: [{ sequence: 2, raw: { type: 'assistant', message: { role: 'assistant', content: [{ type: 'text', text: 'Still using tools' }] } } }] };
    var state = applyConversationRead(emptyConversationState(), view);
    assert.equal(state.messages[0].content, 'Still using tools');
    assert.equal(conversationInputReady(state.lifecycle), false);
    state = applyConversationRead(state, { ...view, state: 'completed', ready: true, cursor: 3, events: [{ sequence: 3, type: 'turn_completed' }] });
    assert.equal(conversationInputReady(state.lifecycle), true);
});

test('reconnect replay deduplicates events and preserves bounded cursor progression', function() {
    var view = { agent: 'claude', cursor: 1, events: [{ sequence: 1, type: 'message', role: 'user', text: 'hello' }] };
    var state = applyConversationRead(emptyConversationState(), view);
    state = applyConversationRead(state, view);
    assert.equal(state.messages.length, 1);
    assert.equal(state.cursor, 1);
    state = applyConversationRead(state, { cursor: 0, events: [] });
    assert.equal(state.cursor, 1);
});

test('tool responses are inspectable provider data rather than new user prompts', function() {
    var state = applyConversationRead(emptyConversationState(), { agent: 'claude', cursor: 1, events: [{ sequence: 1, raw: { type: 'user', message: { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'read-1', content: 'full tool evidence' }] } } }] });
    assert.equal(state.messages[0].type, 'tool_result');
    assert.equal(state.messages[0].content, 'full tool evidence');
});

test('task tree orders children beneath durable parent and retains orphans', function() {
    var rows = orderConversationTree([{ conversation_id: 'child', parent_conversation_id: 'root' }, { conversation_id: 'orphan', parent_conversation_id: 'missing' }, { conversation_id: 'root' }]);
    assert.deepEqual(rows.map(function(r) { return [r.conversation.conversation_id, r.depth]; }), [['root', 0], ['child', 1], ['orphan', 0]]);
});

test('unsupported providers and stopped processes cannot receive chat input', function() {
    assert.equal(conversationInputReady({ agent: 'codex', state: 'idle', ready: true, process_alive: true }), false);
    assert.equal(conversationInputReady({ agent: 'claude', state_source: 'claude_transcript', state: 'idle', ready: true, process_alive: true }), false);
    assert.equal(conversationInputReady({ agent: 'claude', state: 'completed', ready: true, process_alive: false }), false);
});
