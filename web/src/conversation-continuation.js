import { validId } from './conversation-recovery-store.js';

function id(value) { return validId(value) && !/[\x00-\x1f\x7f]/.test(value); }
function validInput(value) { return typeof value === 'string' && !!value.trim() && !value.startsWith('-') && !value.includes('\0') && new TextEncoder().encode(value).length <= 64 << 10; }
function key(ref) { return 'wt_conversation_continuation_v1:' + JSON.stringify([ref.userId, ref.wingId, ref.conversationId]); }

export function continuationAvailability(result, target, lifecycle) {
    var value = result && result.headless_continuation;
    if (!target || !lifecycle || lifecycle.process_alive !== false || lifecycle.agent !== 'claude' || lifecycle.session_id !== target.sessionId ||
        !id(target.providerSessionId) || lifecycle.provider_session_id !== target.providerSessionId || !value || value.available !== true ||
        value.source_session !== target.sessionId || value.conversation_id !== target.conversationId || value.provider_session_id !== target.providerSessionId ||
        typeof value.model !== 'string' || !value.model.startsWith('claude-') || value.model.length > 128 || /[\x00-\x1f\x7f]/.test(value.model)) return null;
    return value;
}

export function createPendingContinuation(ref, input, requestId, savedAt) {
    if (![ref.userId, ref.wingId, ref.sessionId, ref.conversationId, ref.providerSessionId, requestId].every(id) || !validInput(input)) return null;
    return { ...ref, input: input, request_id: requestId, savedAt: savedAt, status: 'pending', lastReason: '' };
}

export function readPendingContinuation(storage, ref) {
    try {
        var value = JSON.parse(storage.getItem(key(ref)) || 'null');
        if (!value || value.userId !== ref.userId || value.wingId !== ref.wingId || value.conversationId !== ref.conversationId ||
            !createPendingContinuation(value, value.input, value.request_id, value.savedAt)) return null;
        return { ...value, status: 'unconfirmed' };
    } catch (error) { return null; }
}

export function savePendingContinuation(storage, value) {
    try {
        var current = JSON.parse(storage.getItem(key(value)) || 'null');
        if (current && ['userId', 'wingId', 'conversationId', 'sessionId', 'providerSessionId', 'request_id', 'input'].some(field => current[field] !== value[field])) return false;
        storage.setItem(key(value), JSON.stringify(value));
        return true;
    } catch (error) { return false; }
}

export function clearPendingContinuation(storage, value) {
    try {
        var current = JSON.parse(storage.getItem(key(value)) || 'null');
        if (current && current.request_id === value.request_id) storage.removeItem(key(value));
    } catch (error) {}
}

export function continuationArguments(pending) {
    return { resume_session: pending.sessionId, conversation_role: 'parent', input: pending.input, request_id: pending.request_id };
}

export async function continuationReceipt(pending, result) {
    var bytes = await globalThis.crypto.subtle.digest('SHA-256', new TextEncoder().encode(pending.input));
    var hash = Array.from(new Uint8Array(bytes), byte => byte.toString(16).padStart(2, '0')).join('');
    var root = pending.conversationId;
    if (!result || result.request_id !== pending.request_id || result.source_session !== pending.sessionId || result.wing_id !== pending.wingId ||
        result.conversation_id !== root || result.root_conversation_id !== root || (result.parent_conversation_id != null && result.parent_conversation_id !== '') ||
        !result.new_turn || result.new_turn.input_sha256 !== hash || !id(result.session) || new TextEncoder().encode(result.session).length > 256 || result.session === pending.sessionId ||
        (result.provider_session_id !== pending.providerSessionId && !(result.launch_state === 'failed' && !result.provider_session_id))) return 'unconfirmed';
    if (result.launch_state === 'started' && result.continuation_state === 'started') return 'started';
    if (result.launch_state === 'failed' && result.continuation_state === 'failed') return 'failed';
    return 'unconfirmed';
}
