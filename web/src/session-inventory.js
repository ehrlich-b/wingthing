import { sessionDisplayName, wingDisplayName } from './helpers.js';
import { notificationForSession } from './session-reference.js';
export { sessionResourceKey } from './session-reference.js';

var agentLabels = {
    starting: 'starting', working: 'working', idle: 'ready',
    completed: 'completed', needs_input: 'needs input', failed: 'failed', unknown: 'agent state unknown'
};

export function findSessionResource(sessions, id, wingId) {
    var matches = sessions.filter(function(session) { return session.id === id && (!wingId || session.wing_id === wingId); });
    // Old unqualified deep links remain usable when their ID is unambiguous.
    return matches.length === 1 ? matches[0] : null;
}

export function sessionIsSelected(session, sessionId, wingId) {
    return !!wingId && session.id === sessionId && session.wing_id === wingId;
}

// Connection, attachment and provider state are different facts. In particular,
// a detached or quiet terminal never proves that an agent finished its work.
export function sessionInventoryState(session, wing, attention) {
    var connection = 'available';
    if (!wing || wing.online === undefined || !session.swept) connection = 'checking';
    if (wing && wing.tunnel_error) connection = 'unreachable';
    if (wing && ['passkey_required', 'passkey_failed', 'no_passkeys_configured', 'not_allowed'].includes(wing.tunnel_error)) connection = 'locked';
    if (wing && wing.online === false) connection = 'offline';

    var lifecycle = session.lifecycle || {};
    var knownSource = ['claude_hook', 'claude_transcript', 'egg_process'].includes(lifecycle.state_source);
    var state = knownSource && agentLabels[lifecycle.state] ? lifecycle.state : 'unknown';
    var needsAttention = state === 'needs_input' || !!attention || !!session.needs_attention;
    var agentLabel = agentLabels[state];
    if (connection !== 'available' && state !== 'unknown') agentLabel = 'last reported: ' + agentLabel;
    if (needsAttention && state !== 'needs_input') agentLabel += ' · attention signal';
    return {
        connection: connection,
        connectionLabel: { available: 'wing online', checking: 'checking connection', unreachable: 'wing unreachable', locked: 'wing locked', offline: 'wing offline' }[connection],
        attachment: session.status === 'active' ? 'attached' : 'detached',
        state: state,
        agentLabel: agentLabel,
        attention: needsAttention,
        tone: connection !== 'available' ? 'offline' : needsAttention ? 'attention' : state === 'failed' ? 'failed' : 'live',
        canAttach: connection === 'available'
    };
}

export function sessionInventoryActions(session, wing, currentUser, attention) {
    var presentation = sessionInventoryState(session, wing, attention);
    var owns = !!currentUser && !!session.user_id && session.user_id === currentUser.id;
    var available = presentation.canAttach;
    var capabilities = wing && Array.isArray(wing.capabilities) ? wing.capabilities : [];
    return {
        attach: available,
        rename: available && owns && capabilities.includes('session.rename.v1'),
        // sessions.list already filters members to their own sessions. Admins
        // can inspect and stop other visible sessions; preserve that contract.
        stop: available && !!currentUser,
        unavailableReason: !available ? presentation.connectionLabel : !owns ? 'Only the session owner can change it' : ''
    };
}

export function filterSessionInventory(sessions, wings, notifications, filters) {
    filters = filters || {};
    var words = (filters.query || '').trim().toLocaleLowerCase().split(/\s+/).filter(Boolean);
    return sessions.filter(function(session) {
        var wing = wings.find(function(item) { return item.wing_id === session.wing_id; });
        var state = sessionInventoryState(session, wing, notifications && notificationForSession(notifications, session));
        if (filters.wing && session.wing_id !== filters.wing) return false;
        if (filters.agent && session.agent !== filters.agent) return false;
        if (filters.status === 'attention' && !state.attention) return false;
        if (filters.status === 'available' && !state.canAttach) return false;
        if (filters.status === 'offline' && state.connection === 'available') return false;
        var text = [sessionDisplayName(session), session.id, session.agent, session.cwd, session.email,
            wing && wingDisplayName(wing), wing && wing.hostname, session.wing_id, session.conversation_role].join(' ').toLocaleLowerCase();
        return words.every(function(word) { return text.includes(word); });
    });
}

// Keep focus on the same qualified row/action after a status refresh. Do not
// interpolate resource IDs into CSS selectors: IDs arrive from remote wings.
export function captureSessionFocus(container, activeElement) {
    if (!activeElement || !container.contains(activeElement)) return null;
    var row = activeElement.closest('[data-sid]');
    if (!row) return null;
    return { id: row.dataset.sid, wingId: row.dataset.wingId || '', action: activeElement.dataset.sessionAction || '', index: Array.from(container.querySelectorAll('[data-sid]')).indexOf(row) };
}

export function restoreSessionFocus(container, focus) {
    if (!focus) return false;
    var rows = Array.from(container.querySelectorAll('[data-sid]'));
    var row = rows.find(function(item) {
        return item.dataset.sid === focus.id && (item.dataset.wingId || '') === focus.wingId;
    });
    if (!row) {
        var next = rows[Math.min(Math.max(focus.index || 0, 0), rows.length - 1)];
        if (next) { next.focus({ preventScroll: true }); return true; }
        return false;
    }
    var action = Array.from(row.querySelectorAll('[data-session-action]')).find(function(item) {
        return item.dataset.sessionAction === focus.action && !item.disabled;
    });
    (action || row).focus({ preventScroll: true });
    return true;
}

export function navigateSessionRows(event, rows, currentRow) {
    var index = rows.indexOf(currentRow);
    if (index < 0) return false;
    var next;
    if (event.key === 'ArrowDown') next = Math.min(index + 1, rows.length - 1);
    else if (event.key === 'ArrowUp') next = Math.max(index - 1, 0);
    else if (event.key === 'Home') next = 0;
    else if (event.key === 'End') next = rows.length - 1;
    else return false;
    event.preventDefault();
    rows[next].focus();
    return true;
}
