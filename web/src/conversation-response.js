import { conversationExecutionPresentation } from './conversation-state.js';

// Late responses must stay with the reader selection that requested them.
export function conversationResponseCurrent(requested, current) {
    return requested.userId === current.userId && requested.viewGeneration === current.viewGeneration && requested.hash === current.hash;
}

// Only explicit evidence that Send was never attempted releases an uncertain
// request for a fresh deliberate send. Zero bytes alone is not that evidence.
// The request identity, input and exact target are never changed here.
export function applyPromptReceipt(pending, receipt) {
    if (receipt.status === 'native_receipt_observed' && receipt.native_receipt_observed === true) {
        return { pending: null, draft: '', notice: '' };
    }
    if (receipt.status === 'not_sent' && receipt.definitely_not_sent === true) {
        return { pending: null, draft: pending.input, notice: receipt.reason || 'Input was not sent. Try again when the session is ready.' };
    }
    return { pending: { ...pending, status: 'unconfirmed', lastReason: receipt.reason || '' }, draft: null, notice: '' };
}

export function canStartFreshLaunch(result) { return !!result && result.launch_state === 'failed'; }

var LAUNCH_ARG_KEYS = ['agent', 'label', 'cwd', 'conversation_role', 'parent_conversation_id', 'model'];

function boundedText(value, max) {
    return typeof value === 'string' && value !== '' && value.trim() === value && value.length <= max && !/[\x00-\x1f\x7f]/.test(value);
}

// Semantic agent_start arguments of one linked launch, in canonical order.
// The model is optional and only sent when the user names one.
export function launchArgs(fields) {
    var args = { agent: 'claude', label: fields.label, cwd: fields.cwd, conversation_role: fields.parentConversationId ? 'child' : 'parent' };
    if (fields.parentConversationId) args.parent_conversation_id = fields.parentConversationId;
    var model = (fields.model || '').trim();
    if (model) args.model = model;
    return args;
}

// Canonical spec of a stored attempt, or '' when it is not a recognized launch.
function canonicalSpec(text) {
    var args;
    try { args = JSON.parse(text); } catch (e) { return ''; }
    if (!args || typeof args !== 'object' || Array.isArray(args)) return '';
    // Earlier builds stored a non-semantic empty request_id placeholder (and
    // never a model); the identity itself always lived in requestId.
    if (Object.prototype.hasOwnProperty.call(args, 'request_id')) {
        if (args.request_id !== '' || Object.prototype.hasOwnProperty.call(args, 'model')) return '';
        delete args.request_id;
    }
    for (var key in args) if (LAUNCH_ARG_KEYS.indexOf(key) < 0) return '';
    if (args.agent !== 'claude' || typeof args.label !== 'string' || typeof args.cwd !== 'string') return '';
    var parent = args.parent_conversation_id;
    if (parent !== undefined && !boundedText(parent, 256)) return '';
    if (args.conversation_role !== (parent ? 'child' : 'parent')) return '';
    if (args.model !== undefined && !boundedText(args.model, 128)) return '';
    return JSON.stringify(launchArgs({ label: args.label, cwd: args.cwd, parentConversationId: parent, model: args.model }));
}

// Reads the stored launch attempt text. Returns null when nothing is stored,
// { pending } for a usable attempt, or { malformed } when a record exists but
// cannot be trusted. A malformed record is never sent or silently replaced; it
// needs an explicit discard after the user inspects existing conversations.
export function readPendingLaunch(text) {
    if (text === null || text === undefined || text === '') return null;
    var raw;
    try { raw = JSON.parse(text); } catch (e) { return { malformed: { requestId: '' } }; }
    if (raw === null) return null;
    var requestId = raw && typeof raw.requestId === 'string' ? raw.requestId : '';
    var spec = raw && typeof raw.spec === 'string' ? canonicalSpec(raw.spec) : '';
    if (typeof raw !== 'object' || !boundedText(requestId, 128) || !boundedText(raw.wingId, 256) || !spec) return { malformed: { requestId: requestId } };
    var pending = { requestId: requestId, spec: spec, wingId: raw.wingId };
    if (raw.confirmedFailed === true) pending.confirmedFailed = true;
    return { pending: pending };
}

// Plans one Create submission against the stored attempt. Only an identical
// attempt reuses its request identity; a changed launch after an uncertain
// result is refused, and a malformed record must be discarded explicitly.
// Nothing may be sent unless action is 'send'.
export function planLaunchStart(fields, stored, newRequestId) {
    if (stored && stored.malformed) return { action: 'malformed', requestId: stored.malformed.requestId };
    var args = launchArgs(fields);
    var spec = JSON.stringify(args);
    var held = stored && stored.pending;
    if (held && (held.spec !== spec || held.wingId !== fields.wingId)) {
        if (!held.confirmedFailed) return { action: 'refused', pending: held };
        held = null;
    }
    var pending = held || { requestId: newRequestId(), spec: spec, wingId: fields.wingId };
    if (!boundedText(pending.requestId, 128)) throw new Error('Could not create a launch request identity.');
    args.request_id = pending.requestId;
    return { action: 'send', args: args, pending: pending };
}

// Form values of a stored attempt, so an uncertain launch can be retried as-is.
export function launchFieldsFromPending(pending) {
    var args = JSON.parse(pending.spec);
    return { wingId: pending.wingId, label: args.label, cwd: args.cwd, model: args.model || '', parentConversationId: args.parent_conversation_id || '' };
}

// Current task state comes from the current execution's native lifecycle and
// observation freshness. Missing earlier execution history is a separate,
// inspectable issue and does not make the current state unavailable.
// evidence: { observedAt, now, cached }
export function conversationTaskAvailability(task, inventoryError, evidence) {
    evidence = evidence || {};
    var historyIssue = task && task.history_unavailable ? 'Some earlier execution history is unavailable. Current state still comes from the current execution.' : '';
    var base = { state: 'unknown', label: 'unknown', detail: '', error: '', historyIssue: historyIssue, archived: false, attention: false, live: false };
    if (!task) return { ...base, error: inventoryError || '' };
    if (task.lifecycle_error && !inventoryError) {
        return { ...base, state: 'unavailable', label: 'state unavailable', error: task.lifecycle_error };
    }
    // conversation_read reports an empty lifecycle (no session, state or
    // source) for a task with no recorded execution. That is not an exit.
    var lifecycle = task.lifecycle;
    if (lifecycle && !lifecycle.session_id && !lifecycle.state && !lifecycle.state_source) lifecycle = null;
    if (!lifecycle) {
        var launch = task.conversation && task.conversation.launch_state;
        if (launch === 'failed') return { ...base, state: 'launch_failed', label: 'launch failed', error: inventoryError || task.conversation.launch_error || 'Launch failed.' };
        if (launch === 'starting') return { ...base, state: 'launch_unconfirmed', label: 'launch not confirmed', error: inventoryError || 'Launch reserved but not confirmed. Inspect the session before retrying.' };
        return { ...base, error: inventoryError || task.lifecycle_error || '' };
    }
    var shown = conversationExecutionPresentation(lifecycle, { observedAt: inventoryError ? 0 : evidence.observedAt, now: evidence.now, cached: !!inventoryError || !!evidence.cached });
    return { ...base, state: shown.state, label: shown.label, detail: shown.detail, error: inventoryError || '', archived: shown.archived, attention: shown.attention, live: shown.live };
}
