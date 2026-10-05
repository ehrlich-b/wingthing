// A browser selection identifies one logical root, never a provider execution.
export const PARENT_DOT_FRESH_MS = 60000;

function validId(value) {
    return typeof value === 'string' && value.length > 0 && value.length <= 512 && value.trim() === value;
}

export function parentDotStorageKey(userId) {
    return validId(userId) ? 'wt_parent_dot_v1:' + JSON.stringify(userId) : null;
}

export function selectParentConversation(userId, current, reference) {
    if (!validId(userId) || !reference || !validId(reference.wingId) || !validId(reference.conversationId)) return null;
    var same = current && current.userId === userId && current.wingId === reference.wingId && current.conversationId === reference.conversationId;
    var title = typeof reference.title === 'string' ? reference.title.trim().slice(0, 128) : same ? current.title : '';
    return { userId: userId, wingId: reference.wingId, conversationId: reference.conversationId, title: title || '' };
}

export function readParentSelection(storage, userId) {
    var key = parentDotStorageKey(userId);
    if (!key) return { selection: null, error: '' };
    try {
        var value = JSON.parse(storage.getItem(key) || 'null');
        if (!value || value.userId !== userId) return { selection: null, error: '' };
        return { selection: selectParentConversation(userId, null, value), error: '' };
    } catch (error) {
        return { selection: null, error: 'Parent selection could not be restored in this browser.' };
    }
}

export function saveParentSelection(storage, userId, selection) {
    var valid = selection && selection.userId === userId && selectParentConversation(userId, null, selection);
    if (!valid) return false;
    try { storage.setItem(parentDotStorageKey(userId), JSON.stringify(valid)); return true; }
    catch (error) { return false; }
}

export function activateParentSelection(selection, userId, open, choose) {
    var valid = selection && selection.userId === userId && selectParentConversation(userId, null, selection);
    if (!valid) return choose();
    return open({ wingId: valid.wingId, conversationId: valid.conversationId });
}

function isFresh(observedAt, now) {
    return Number.isFinite(observedAt) && observedAt > 0 && observedAt <= now && now - observedAt < PARENT_DOT_FRESH_MS;
}

function rootTask(selection, evidence) {
    var task = evidence && evidence.task;
    var conversation = task && task.conversation;
    if (!conversation || conversation.conversation_id !== selection.conversationId || conversation.parent_conversation_id) return null;
    if (conversation.wing_id && conversation.wing_id !== selection.wingId) return null;
    return task;
}

// Only fresh typed native evidence can light the dot. Cached titles and logical
// IDs survive reload; cached provider state cannot claim the parent is working.
export function parentDotPresentation(selection, wings, sessions, evidence, now) {
    if (!selection) return { state: 'unselected', label: 'select parent', title: 'Select a parent conversation' };
    now = now === undefined ? Date.now() : now;
    var wing = wings.find(function(item) { return item.wing_id === selection.wingId; });
    var title = selection.title || 'Parent conversation';
    if (wing && wing.online === false) return { state: 'offline', label: 'offline', title: title };
    if (wing && wing.tunnel_error) return { state: 'unavailable', label: 'unavailable', title: title };
    if (!wing || wing.online !== true) return { state: 'unknown', label: 'checking', title: title };

    var task = rootTask(selection, evidence);
    var currentExecution = task && task.conversation.session_id;
    if (task && task.conversation.title) title = task.conversation.title;
    // A failed read of this current task must not fall back to an older session
    // label. Missing earlier execution history is a separate inspection issue.
    var failedReference = evidence && evidence.reference;
    var exactFailure = failedReference && failedReference.userId === selection.userId && failedReference.wingId === selection.wingId && failedReference.conversationId === selection.conversationId;
    var readError = (task || exactFailure) && ((task && task.lifecycle_error) || (typeof evidence.readError === 'string' ? evidence.readError : evidence.error));
    if (readError) return { state: 'unknown', label: 'unknown', title: title };
    var matches = sessions.filter(function(session) {
        return session.wing_id === selection.wingId && session.conversation_id === selection.conversationId &&
            !session.parent_conversation_id && (!session.user_id || session.user_id === selection.userId) &&
            (!currentExecution || session.id === currentExecution);
    });
    var native = [];
    if (task && isFresh(evidence.observedAt, now)) native.push({ lifecycle: task.lifecycle, observedAt: evidence.observedAt });
    if (matches.length === 1 && isFresh(matches[0].lifecycle_seen_at, now)) native.push({ lifecycle: matches[0].lifecycle, observedAt: matches[0].lifecycle_seen_at });
    native.sort(function(a, b) { return b.observedAt - a.observedAt; });
    var lifecycle = native.length ? native[0].lifecycle || {} : {};
    var known = ['claude_hook', 'claude_transcript', 'egg_process'].includes(lifecycle.state_source);
    var labels = { starting: 'starting', working: 'working', idle: 'ready', completed: 'completed', needs_input: 'needs input', failed: 'failed' };
    // A successful process exit is availability evidence, not completion of a
    // foreground turn or delegated work. A stopped native turn is archived too.
    if (lifecycle.process_alive === false && !(known && lifecycle.state === 'failed')) return { state: 'archived', label: 'archived', title: title };
    if (!known) return { state: 'unknown', label: 'unknown', title: title };
    if (['idle', 'completed'].includes(lifecycle.state) && (lifecycle.process_alive !== true || lifecycle.state_source === 'egg_process')) return { state: 'unknown', label: 'unknown', title: title };
    var state = labels[lifecycle.state] ? lifecycle.state : 'unknown';
    return { state: state, label: labels[state] || 'unknown', title: title };
}
