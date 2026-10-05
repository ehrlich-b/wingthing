import { parseJSONL } from './chat.js';

// Typed native observations older than this are no longer current state.
export const CONVERSATION_FRESH_MS = 60000;

// Pure reducer: a transcript chunk never implies turn completion. Only the
// provider-backed lifecycle controls readiness, and sequence IDs deduplicate
// replay after a tunnel reconnect.
export function emptyConversationState() {
    return { cursor: 0, messages: [], seen: new Set(), lifecycle: null, readSeq: 0, identityError: '' };
}

// Restore cached transcript evidence. The lifecycle is kept only as labeled
// last-observed evidence; callers must present it as cached, never live.
export function restoredConversationState(record) {
    var state = emptyConversationState();
    if (!record) return state;
    state.messages = Array.isArray(record.messages) ? record.messages.slice() : [];
    state.messages.forEach(function(message) { if (Number.isSafeInteger(message.sequence)) state.seen.add(message.sequence); });
    state.cursor = Number.isSafeInteger(record.cursor) ? record.cursor : 0;
    state.lifecycle = record.lifecycle || null;
    return state;
}

// Exact execution identity: one wing session and, once reported, one provider
// conversation. A mismatch is rejected rather than merged or re-targeted.
export function conversationReadIdentityError(previous, view, expected) {
    expected = expected || {};
    if (expected.sessionId && view.session_id && view.session_id !== expected.sessionId) {
        return 'Reader response belongs to execution ' + view.session_id + ', not ' + expected.sessionId + '; it was discarded.';
    }
    var prior = previous.lifecycle || {};
    if (prior.agent && view.agent && prior.agent !== view.agent) return 'Provider changed from ' + prior.agent + ' to ' + view.agent + ' for one execution; the response was discarded.';
    var provider = expected.providerSessionId || prior.provider_session_id || '';
    if (provider && view.provider_session_id && view.provider_session_id !== provider) {
        return 'Provider conversation changed for this execution; the response was discarded. Reopen the logical conversation to inspect its current execution.';
    }
    return '';
}

function lifecycleIsOlder(previous, view, readSeq) {
    if (Number.isSafeInteger(readSeq) && readSeq < previous.readSeq) return true;
    var current = previous.lifecycle;
    if (!current) return false;
    if (Number.isSafeInteger(current.head_cursor) && Number.isSafeInteger(view.head_cursor) && view.head_cursor < current.head_cursor) return true;
    return Number.isSafeInteger(current.state_cursor) && Number.isSafeInteger(view.state_cursor) && view.state_cursor < current.state_cursor;
}

// Native lifecycle records rendered inline as inspectable evidence. Tool
// activity is already visible through transcript tool calls.
var LIFECYCLE_MARKERS = { input_requested: 'attention', turn_completed: 'turn', turn_failed: 'turn_failed', provider_warning: 'warning', provider_session_end: 'exit' };

function markerFor(event) {
    var kind = LIFECYCLE_MARKERS[event.type];
    if (!kind && event.type === 'notification' && event.state === 'needs_input') kind = 'attention';
    if (!kind) return null;
    return { type: 'lifecycle', kind: kind, state: event.state || '', reason: event.reason || '', source: event.source || '', sequence: event.sequence, timestamp: event.timestamp || '' };
}

// meta: { readSeq, expected: { sessionId, providerSessionId } }. A delayed
// older page may still contribute unseen events, but never replaces a fresher
// lifecycle observation.
export function applyConversationRead(previous, view, meta) {
    meta = meta || {};
    var identityError = conversationReadIdentityError(previous, view, meta.expected);
    if (identityError) return { ...previous, identityError: identityError };
    var older = lifecycleIsOlder(previous, view, meta.readSeq);
    var next = {
        cursor: previous.cursor,
        messages: previous.messages.slice(),
        seen: new Set(previous.seen),
        lifecycle: older ? previous.lifecycle : view,
        readSeq: !older && Number.isSafeInteger(meta.readSeq) ? meta.readSeq : previous.readSeq,
        identityError: '',
    };
    var agent = view.agent || (previous.lifecycle && previous.lifecycle.agent) || '';
    (view.events || []).forEach(function(event) {
        if (!Number.isSafeInteger(event.sequence) || next.seen.has(event.sequence)) return;
        next.seen.add(event.sequence);
        if (event.raw) {
            var parsed = parseJSONL(JSON.stringify(event.raw), agent);
            parsed.forEach(function(message, index) {
                message.sequence = event.sequence;
                message.itemIndex = index;
                message.truncated = event.truncated === true;
                next.messages.push(message);
            });
            // Non-Claude native items without a chat shape stay inspectable as
            // provider data instead of being dropped or reinterpreted.
            if (!parsed.length && agent !== 'claude') {
                var record = '';
                try { record = JSON.stringify(event.raw, null, 2); } catch (e) { record = String(event.raw); }
                next.messages.push({ type: 'provider_record', name: event.type || 'provider record', content: record, sequence: event.sequence, truncated: event.truncated === true });
            }
        } else if (event.type === 'message' && (event.role === 'user' || event.role === 'assistant')) {
            next.messages.push({ type: event.role, content: event.text || '', sequence: event.sequence, truncated: event.truncated === true });
        } else {
            var marker = markerFor(event);
            if (marker) next.messages.push(marker);
        }
    });
    if (Number.isSafeInteger(view.cursor) && view.cursor >= previous.cursor) next.cursor = view.cursor;
    return next;
}

export function conversationInputReady(view) {
    return !!view && view.agent === 'claude' && view.state_source === 'claude_hook' && view.process_alive === true && view.ready === true &&
        (view.state === 'idle' || view.state === 'completed');
}

function observedFresh(evidence, now) {
    var limit = evidence.freshMs || CONVERSATION_FRESH_MS;
    return !evidence.cached && Number.isFinite(evidence.observedAt) && evidence.observedAt > 0 && evidence.observedAt <= now && now - evidence.observedAt < limit;
}

function presentation(state, label, detail, extra) {
    return { state: state, label: label, detail: detail, live: false, archived: false, attention: false, inputReady: false, ...(extra || {}) };
}

// One truthful reading of an execution lifecycle:
// - a dead process is archived evidence (a failed turn stays failed), never
//   task success;
// - a live native foreground completion is one finished turn, not the outcome
//   of delegated work;
// - cached, stale or interrupted observations are unknown, never live.
// evidence: { observedAt, now, cached, readError, freshMs }
export function conversationExecutionPresentation(view, evidence) {
    evidence = evidence || {};
    var now = evidence.now === undefined ? Date.now() : evidence.now;
    if (!view) {
        if (evidence.readError) return presentation('unknown', 'unknown', 'No native observation yet: ' + evidence.readError);
        return presentation('connecting', 'connecting', 'Reading native session state…');
    }
    var fresh = observedFresh(evidence, now) && !evidence.readError;
    var source = view.state_source || '';
    var when = evidence.cached ? 'Saved in this browser; not a live observation.' : (fresh ? '' : 'Last native observation is no longer current.');
    if (source === 'unsupported') return presentation('unsupported', 'no reader', 'This provider has no native conversation reader. Inspect its terminal.');
    if (view.process_alive === false) {
        var failed = view.state === 'failed' && source !== '';
        return presentation(failed ? 'failed' : 'archived', failed ? 'failed · archived' : 'archived',
            (failed ? 'Native provider reported a failed turn and the process has exited.' : 'The execution process has exited. Its transcript is archived evidence; exit does not prove the task succeeded.') + (when ? ' ' + when : ''),
            { archived: true });
    }
    if (!fresh) {
        var last = view.state ? 'Last observed: ' + view.state + (view.reason ? ' · ' + view.reason : '') + '. ' : '';
        return presentation('unknown', evidence.cached ? 'cached · unknown' : 'unknown', last + (evidence.readError ? 'Connection interrupted: ' + evidence.readError + '. ' : '') + (when || 'Current state is unknown.'));
    }
    var hook = source === 'claude_hook';
    var live = { live: true, inputReady: conversationInputReady(view) };
    switch (view.state) {
    case 'needs_input':
        return presentation('needs_input', 'needs input', 'Native provider reports it needs a human decision' + (view.reason ? ' (' + view.reason + ')' : '') + '. No provider approval ID is exposed here; respond in the terminal.', { ...live, attention: true });
    case 'completed':
        if (!hook) break;
        return presentation('turn_completed', 'turn finished', 'The foreground turn finished (native Stop). This is not proof that delegated work succeeded.', live);
    case 'idle':
        if (!hook) break;
        return presentation('ready', 'ready', view.reason || 'Native provider is idle and accepts input.', live);
    case 'working':
    case 'starting':
        return presentation(view.state, view.state, view.reason || '', live);
    case 'failed':
        return presentation('turn_failed', 'turn failed', view.reason || 'Native provider reported a failed turn; the process is still running.', live);
    }
    return presentation('unknown', 'unknown', 'Native state unavailable (' + (view.state || 'none') + ' from ' + (source || 'no source') + '). Inspect the terminal.', { live: true });
}

export function orderConversationTree(conversations) {
    var byParent = new Map();
    conversations.forEach(function(c) {
        var key = c.parent_conversation_id || '';
        if (!byParent.has(key)) byParent.set(key, []);
        byParent.get(key).push(c);
    });
    var out = [];
    var seen = new Set();
    function visit(c, depth) {
        if (seen.has(c.conversation_id)) return;
        seen.add(c.conversation_id);
        out.push({ conversation: c, depth: depth });
        (byParent.get(c.conversation_id) || []).forEach(function(child) { visit(child, depth + 1); });
    }
    (byParent.get('') || []).forEach(function(c) { visit(c, 0); });
    conversations.forEach(function(c) { if (!seen.has(c.conversation_id)) visit(c, 0); });
    return out;
}
