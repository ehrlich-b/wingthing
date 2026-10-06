import { CONVERSATION_FRESH_MS, applyConversationRead, emptyConversationState } from './conversation-state.js';
import { escapeMarkup } from './security.js';

const STATUSES = ['working', 'blocked', 'idle', 'done', 'exited', 'unknown'];

// Agent status describes the task; native lifecycle separately gates input.
export function coordinatorStatus(lifecycle, evidence) {
    evidence = evidence || {};
    var now = evidence.now === undefined ? Date.now() : evidence.now;
    var fresh = !evidence.cached && !evidence.readError && evidence.observedAt > 0 && evidence.observedAt <= now && now - evidence.observedAt < CONVERSATION_FRESH_MS;
    return fresh && lifecycle && STATUSES.includes(lifecycle.status) ? lifecycle.status : 'unknown';
}

export function coordinatorStatusMarkup(status) {
    status = STATUSES.includes(status) ? status : 'unknown';
    return '<span class="coordinator-status" data-status="' + status + '"><span class="coordinator-dot" aria-hidden="true"></span><span>' + status + '</span></span>';
}

// Preserve each wing's tree on desktop; on phones every root precedes children.
export function orderCoordinatorInventory(rows, mobile) {
    return mobile ? rows.filter(function(row) { return !row.task.conversation.parent_conversation_id; }).concat(rows.filter(function(row) { return !!row.task.conversation.parent_conversation_id; })) : rows.slice();
}

export function lastMessagePreview(messages) {
    for (var i = messages.length - 1; i >= 0; i--) {
        var message = messages[i];
        if ((message.type === 'user' || message.type === 'assistant') && typeof message.content === 'string' && message.content.trim()) {
            return message.content.replace(/\s+/g, ' ').trim().slice(0, 180);
        }
    }
    return '';
}

export function previewReadArguments(task) {
    var lifecycle = task.lifecycle || {};
    return { session: task.conversation.session_id, after_cursor: Math.max(0, (Number.isSafeInteger(lifecycle.head_cursor) ? lifecycle.head_cursor : 0) - 100), limit: 100 };
}

export function previewMessagesFromRead(task, result) {
    var lifecycle = result && result.lifecycle;
    var sessionId = task.conversation.session_id;
    if (!lifecycle || lifecycle.session_id !== sessionId || (result.session && result.session !== sessionId)) return [];
    var state = applyConversationRead(emptyConversationState(), lifecycle, { expected: { sessionId: sessionId, providerSessionId: (task.lifecycle || {}).provider_session_id } });
    return state.identityError ? [] : state.messages.filter(function(message) { return message.type === 'user' || message.type === 'assistant'; });
}

export function previewFromRead(task, result) {
    return lastMessagePreview(previewMessagesFromRead(task, result));
}

export function coordinatorCardHeading(conversation, status, preview) {
    return '<span class="coordinator-name">' + escapeMarkup(conversation.title || conversation.agent || conversation.conversation_id) + '</span>' + coordinatorStatusMarkup(status) +
        (status === 'blocked' ? '<span class="coordinator-blocked">Needs attention</span>' : '') +
        '<span class="coordinator-preview">' + escapeMarkup(preview || 'No message preview yet') + '</span>';
}

export function blockedBadge(statuses) {
    var count = statuses.filter(function(status) { return status === 'blocked'; }).length;
    return count ? count + ' blocked · Needs attention' : '';
}

// Interrupted reads never clear a known transition or trigger repeat alerts.
export function observeCoordinatorStatus(previous, key, status) {
    var prior = previous.get(key);
    if (status === 'unknown') return { blocked: false, cleared: false };
    previous.set(key, status);
    return { blocked: status === 'blocked' && prior !== 'blocked', cleared: prior === 'blocked' && status !== 'blocked' };
}

// The recovery reader owns freshness, drafts and receipt recovery. This only
// presents its decision; sending still goes through that reader unchanged.
export function coordinatorComposer(snapshot, spectating) {
    var ready = !!snapshot && snapshot.inputReady && !spectating;
    var ended = !!snapshot && snapshot.lifecycle && snapshot.lifecycle.process_alive === false;
    var mode = ready ? (ended && snapshot.continuationReady ? 'continuation' : (!ended && !snapshot.continuationReady ? 'session_prompt' : 'unavailable')) : 'unavailable';
    return { mode: mode, ready: mode !== 'unavailable', label: mode === 'continuation' ? 'Ready for follow-up' : mode === 'session_prompt' ? 'Ready for a message' : '', placeholder: mode === 'continuation' ? 'Send a follow-up…' : mode === 'session_prompt' ? 'Send a message…' : snapshot && (snapshot.pending || snapshot.continuation) ? 'Resolve the unconfirmed input above first' : 'Waiting for the coordinator to be ready' };
}

export function chatEnterSubmits(event, touchPrimary) {
    return event.key === 'Enter' && !event.shiftKey && !event.isComposing && !touchPrimary;
}
