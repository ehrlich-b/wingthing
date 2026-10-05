// Browser-local recovery evidence for conversation readers. Every record is
// qualified by the authenticated user plus the exact execution wing and session
// (or logical conversation). There is deliberately no bare-ID fallback: a record
// whose stored identity does not match its key is ignored, never guessed.
//
// Everything restored from here is cached evidence. It can show what this
// browser last observed and which input it may have sent; it can never claim a
// live provider state or that a turn or task completed.

export const EXECUTION_CACHE_LIMIT = 32;
export const TRANSCRIPT_MESSAGE_LIMIT = 200;
export const MESSAGE_CHAR_LIMIT = 16384;
export const EXECUTION_RECORD_BYTES = 262144;
export const TREE_TASK_LIMIT = 256;
export const DELIVERY_EVENT_LIMIT = 20;

// The `wt_` prefix places every key under scopeBrowserStateToUser's reset.
const EXECUTION_PREFIX = 'wt_conversation_execution_v1:';
const INDEX_PREFIX = 'wt_conversation_execution_index_v1:';
const SELECTION_PREFIX = 'wt_conversation_selection_v1:';
const TREE_PREFIX = 'wt_conversation_tree_v1:';
// Pending prompts from the previous release lived in sessionStorage under an
// exact user+wing+session key. They are migrated once, never matched loosely.
const LEGACY_PENDING_PREFIX = 'wt_conversation_prompt:';

export function validId(value) {
    return typeof value === 'string' && value.length > 0 && value.length <= 512 && value.trim() === value;
}

export function executionKey(userId, wingId, sessionId) {
    if (!validId(userId) || !validId(wingId) || !validId(sessionId)) return null;
    return EXECUTION_PREFIX + JSON.stringify([userId, wingId, sessionId]);
}

function indexKey(userId) { return validId(userId) ? INDEX_PREFIX + JSON.stringify([userId]) : null; }
export function selectionKey(userId) { return validId(userId) ? SELECTION_PREFIX + JSON.stringify([userId]) : null; }
export function treeKey(userId, wingId) { return validId(userId) && validId(wingId) ? TREE_PREFIX + JSON.stringify([userId, wingId]) : null; }

function readJSON(storage, key) {
    if (!storage || !key) return { value: null, error: '' };
    try { return { value: JSON.parse(storage.getItem(key) || 'null'), error: '' }; }
    catch (error) { return { value: null, error: 'Saved conversation evidence could not be read in this browser.' }; }
}

function writeJSON(storage, key, value) {
    if (!storage || !key) return false;
    try { storage.setItem(key, JSON.stringify(value)); return true; }
    catch (error) { return false; }
}

function bounded(text, limit) {
    if (typeof text !== 'string') return { text: '', cut: false };
    return text.length > limit ? { text: text.slice(0, limit), cut: true } : { text: text, cut: false };
}

function boundMessage(message) {
    var out = { ...message };
    var cut = false;
    ['content', 'thinking', 'reason'].forEach(function(field) {
        if (typeof out[field] === 'string') {
            var b = bounded(out[field], MESSAGE_CHAR_LIMIT);
            out[field] = b.text;
            cut = cut || b.cut;
        }
    });
    if (Array.isArray(out.toolCalls)) {
        out.toolCalls = out.toolCalls.slice(0, 64).map(function(call) {
            var copy = { ...call };
            if (typeof copy.result === 'string') { var r = bounded(copy.result, MESSAGE_CHAR_LIMIT); copy.result = r.text; cut = cut || r.cut; }
            if (copy.input !== undefined && typeof copy.input !== 'string') {
                try { copy.input = JSON.stringify(copy.input, null, 2); } catch (e) { copy.input = '…'; }
            }
            if (typeof copy.input === 'string') { var i = bounded(copy.input, MESSAGE_CHAR_LIMIT); copy.input = i.text; cut = cut || i.cut; }
            return copy;
        });
    }
    if (cut) out.cacheTruncated = true;
    return out;
}

// Pending input core fields are immutable once reserved. Only receipt evidence
// (status, check count, last reason/time) may change, and only through
// updatePendingEvidence, which refuses to rebind the request.
const PENDING_CORE = ['request_id', 'input', 'userId', 'wingId', 'sessionId', 'providerSessionId', 'conversationId', 'createdAt'];

export function createPendingInput(fields) {
    var pending = { status: 'pending', checks: 0, lastCheckedAt: 0, lastReason: '' };
    PENDING_CORE.forEach(function(key) { pending[key] = fields[key] === undefined ? '' : fields[key]; });
    if (!validId(pending.request_id) || !validId(pending.userId) || !validId(pending.wingId) || !validId(pending.sessionId) || typeof pending.input !== 'string' || !pending.input) return null;
    return pending;
}

export function updatePendingEvidence(pending, evidence) {
    var next = { ...pending };
    ['status', 'checks', 'lastCheckedAt', 'lastReason'].forEach(function(key) { if (evidence[key] !== undefined) next[key] = evidence[key]; });
    PENDING_CORE.forEach(function(key) { next[key] = pending[key]; });
    return next;
}

export function pendingMatchesExecution(pending, ref) {
    return !!pending && pending.userId === ref.userId && pending.wingId === ref.wingId && pending.sessionId === ref.sessionId;
}

function validRecord(record, ref) {
    return !!record && record.v === 1 && record.userId === ref.userId && record.wingId === ref.wingId && record.sessionId === ref.sessionId;
}

export function readExecution(storage, ref) {
    var key = executionKey(ref.userId, ref.wingId, ref.sessionId);
    if (!key) return { record: null, error: '' };
    var read = readJSON(storage, key);
    if (read.error) return { record: null, error: read.error };
    if (!validRecord(read.value, ref)) return { record: null, error: '' };
    var record = read.value;
    if (record.pending && !pendingMatchesExecution(record.pending, ref)) record.pending = null;
    return { record: record, error: '' };
}

function readIndex(storage, userId) {
    var read = readJSON(storage, indexKey(userId));
    return Array.isArray(read.value) ? read.value.filter(function(entry) { return Array.isArray(entry) && validId(entry[0]) && validId(entry[1]); }) : [];
}

function trySet(storage, key, encoded) {
    try { storage.setItem(key, encoded); return true; }
    catch (error) { return false; }
}

// Bound by count and serialized size. Oldest messages go first; a pending input
// is never trimmed and an execution holding one is never evicted. When the
// browser quota is full, older transcript-only records are evicted first and,
// as a last resort, the identity and pending input are kept without transcript.
export function writeExecution(storage, ref, fields) {
    var key = executionKey(ref.userId, ref.wingId, ref.sessionId);
    if (!key) return { ok: false, error: 'Conversation identity is incomplete; nothing was saved.' };
    var messages = (fields.messages || []).slice(-TRANSCRIPT_MESSAGE_LIMIT).map(boundMessage);
    var trimmed = !!fields.trimmed || (fields.messages || []).length > messages.length;
    var record = {
        v: 1, userId: ref.userId, wingId: ref.wingId, sessionId: ref.sessionId,
        conversationId: fields.conversationId || '', providerSessionId: fields.providerSessionId || '', agent: fields.agent || '',
        cursor: Number.isSafeInteger(fields.cursor) ? fields.cursor : 0,
        lifecycle: fields.lifecycle ? { ...fields.lifecycle, events: [] } : null,
        observedAt: fields.observedAt || 0, savedAt: fields.savedAt || 0,
        messages: messages, trimmed: trimmed, pending: fields.pending || null,
    };
    var encoded = JSON.stringify(record);
    while (encoded.length > EXECUTION_RECORD_BYTES && record.messages.length) {
        record.messages = record.messages.slice(Math.max(1, Math.floor(record.messages.length / 4)));
        record.trimmed = true;
        encoded = JSON.stringify(record);
    }
    var index = readIndex(storage, ref.userId).filter(function(entry) { return !(entry[0] === ref.wingId && entry[1] === ref.sessionId); });
    var warning = '';
    var saved = trySet(storage, key, encoded);
    while (!saved) {
        var victim = index.findIndex(function(entry) { return !entry[4]; });
        if (victim < 0) break;
        try { storage.removeItem(executionKey(ref.userId, index[victim][0], index[victim][1])); } catch (e) {}
        index.splice(victim, 1);
        saved = trySet(storage, key, encoded);
    }
    if (!saved && record.messages.length) {
        record.messages = [];
        record.trimmed = true;
        saved = trySet(storage, key, JSON.stringify(record));
        if (saved) warning = 'Browser storage is full; this execution’s transcript was not cached, but its identity' + (record.pending ? ' and unresolved input were' : ' was') + '.';
    }
    if (!saved) {
        writeJSON(storage, indexKey(ref.userId), index);
        return { ok: false, error: 'This browser could not save conversation evidence (storage full or disabled).' };
    }
    index.push([ref.wingId, ref.sessionId, record.savedAt, record.conversationId, record.pending ? 1 : 0]);
    var overflow = index.length - EXECUTION_CACHE_LIMIT;
    if (overflow > 0) {
        var keep = [];
        index.forEach(function(entry) {
            if (overflow > 0 && !entry[4] && !(entry[0] === ref.wingId && entry[1] === ref.sessionId)) {
                try { storage.removeItem(executionKey(ref.userId, entry[0], entry[1])); } catch (e) {}
                overflow--;
                return;
            }
            keep.push(entry);
        });
        index = keep;
    }
    if (!writeJSON(storage, indexKey(ref.userId), index) && !warning) warning = 'Conversation evidence was saved, but its browser index could not be updated.';
    return { ok: true, error: warning };
}

// Other executions of one logical conversation that still hold unresolved
// input. Their requests stay bound to those exact executions.
export function pendingForConversation(storage, userId, wingId, conversationId, exceptSessionId) {
    if (!validId(conversationId)) return [];
    return readIndex(storage, userId).filter(function(entry) {
        return entry[0] === wingId && entry[3] === conversationId && entry[4] && entry[1] !== exceptSessionId;
    }).map(function(entry) {
        return readExecution(storage, { userId: userId, wingId: entry[0], sessionId: entry[1] }).record;
    }).filter(function(record) { return record && record.pending && record.conversationId === conversationId; }).map(function(record) { return record.pending; });
}

export function migrateLegacyPending(sessionStore, ref) {
    if (!sessionStore || !executionKey(ref.userId, ref.wingId, ref.sessionId)) return null;
    var key = LEGACY_PENDING_PREFIX + JSON.stringify([ref.userId, ref.wingId, ref.sessionId]);
    var read = readJSON(sessionStore, key);
    var old = read.value;
    if (!old || !validId(old.request_id) || typeof old.input !== 'string' || !old.input) return null;
    var pending = createPendingInput({ request_id: old.request_id, input: old.input, userId: ref.userId, wingId: ref.wingId, sessionId: ref.sessionId, providerSessionId: '', conversationId: ref.conversationId || '', createdAt: 0 });
    if (pending) pending = updatePendingEvidence(pending, { status: 'unconfirmed', lastReason: 'Restored from an earlier browser tab; provider identity was not recorded.' });
    try { sessionStore.removeItem(key); } catch (e) {}
    return pending;
}

export function readSelection(storage, userId) {
    var read = readJSON(storage, selectionKey(userId));
    var value = read.value;
    if (read.error) return { selection: null, error: 'Conversation selection could not be restored in this browser.' };
    if (!value || value.userId !== userId || !validId(value.wingId) || !validId(value.conversationId)) return { selection: null, error: '' };
    return { selection: normalizeSelection(userId, value), error: '' };
}

export function normalizeSelection(userId, value) {
    if (!validId(userId) || !value || !validId(value.wingId) || !validId(value.conversationId)) return null;
    return {
        userId: userId, wingId: value.wingId, conversationId: value.conversationId,
        rootConversationId: validId(value.rootConversationId) ? value.rootConversationId : value.conversationId,
        sessionId: validId(value.sessionId) ? value.sessionId : '',
        title: typeof value.title === 'string' ? value.title.trim().slice(0, 128) : '',
        savedAt: Number.isFinite(value.savedAt) ? value.savedAt : 0,
    };
}

export function saveSelection(storage, userId, value) {
    var selection = normalizeSelection(userId, value);
    return !!selection && writeJSON(storage, selectionKey(userId), selection);
}

function boundTask(task) {
    var out = { conversation: task.conversation };
    if (task.lifecycle) out.lifecycle = { ...task.lifecycle, events: [] };
    if (task.lifecycle_error) out.lifecycle_error = bounded(String(task.lifecycle_error), 2048).text;
    if (task.history_unavailable) out.history_unavailable = true;
    return out;
}

export function boundDelivery(result) {
    var events = Array.isArray(result && result.events) ? result.events : [];
    return { events: events.slice(-DELIVERY_EVENT_LIMIT), has_more: !!(result && result.has_more) || events.length > DELIVERY_EVENT_LIMIT, next_cursor: result && Number.isSafeInteger(result.next_cursor) ? result.next_cursor : 0 };
}

export function readTree(storage, userId, wingId) {
    var read = readJSON(storage, treeKey(userId, wingId));
    var value = read.value;
    if (!value || value.userId !== userId || value.wingId !== wingId || !Array.isArray(value.tasks)) return { tasks: [], deliveries: {}, savedAt: 0, error: read.error };
    var tasks = value.tasks.filter(function(task) {
        return task && task.conversation && validId(task.conversation.conversation_id) && (!task.conversation.wing_id || task.conversation.wing_id === wingId);
    });
    return { tasks: tasks, deliveries: value.deliveries && typeof value.deliveries === 'object' ? value.deliveries : {}, savedAt: value.savedAt || 0, error: '' };
}

export function saveTree(storage, userId, wingId, tree) {
    var tasks = (tree.tasks || []).slice(0, TREE_TASK_LIMIT).map(boundTask);
    var deliveries = {};
    Object.keys(tree.deliveries || {}).slice(0, TREE_TASK_LIMIT).forEach(function(root) { deliveries[root] = boundDelivery(tree.deliveries[root]); });
    return writeJSON(storage, treeKey(userId, wingId), { userId: userId, wingId: wingId, tasks: tasks, deliveries: deliveries, savedAt: tree.savedAt || 0 });
}
