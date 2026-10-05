export function sessionResourceKey(session) {
    return JSON.stringify([session.wing_id || session.wingId || '', session.id]);
}

export function sessionContentKey(prefix, wingId, sessionId) {
    if (!wingId || !sessionId) return null;
    return prefix + 'v2:' + sessionResourceKey({ wing_id: wingId, id: sessionId });
}

// Terminal content is never migrated by guessing the owner of an old bare ID.
// Legacy content remains dormant; new clients read and write only v2 keys.
export function readSessionContent(storage, prefix, wingId, sessionId) {
    var key = sessionContentKey(prefix, wingId, sessionId);
    if (!key) return null;
    try { return storage.getItem(key); } catch (error) { return null; }
}

export function writeSessionContent(storage, prefix, wingId, sessionId, content) {
    var key = sessionContentKey(prefix, wingId, sessionId);
    if (!key) return false;
    try { storage.setItem(key, content); return true; } catch (error) { return false; }
}

export function clearSessionContent(storage, prefixes, wingId, sessionId) {
    prefixes.forEach(function(prefix) {
        var key = sessionContentKey(prefix, wingId, sessionId);
        if (key) { try { storage.removeItem(key); } catch (error) {} }
    });
}

export function notificationForSession(notifications, session) {
    return !!notifications[sessionResourceKey(session)];
}

// Attention events without a wing must never mark or clear another wing's row.
export function changeSessionNotification(notifications, sessionId, wingId, present) {
    if (!sessionId || !wingId) return false;
    var key = sessionResourceKey({ id: sessionId, wingId: wingId });
    if (!!notifications[key] === present) return false;
    if (present) notifications[key] = true;
    else delete notifications[key];
    return true;
}

// Async encryption/output must still belong to the same terminal connection
// when it completes, even if a new connection has the same provider ID.
export function terminalReferenceMatches(state, sessionId, wingId, socket) {
    return !!wingId && state.ptySessionId === sessionId && state.ptyWingId === wingId && state.ptyWs === socket;
}

export function orderSessionReferences(sessions, order) {
    var positions = new Map(order.map(function(key, index) { return [key, index]; }));
    var counts = new Map();
    sessions.forEach(function(session) { counts.set(session.id, (counts.get(session.id) || 0) + 1); });
    function position(session) {
        var key = sessionResourceKey(session);
        if (positions.has(key)) return positions.get(key);
        // Old preferences contain no content and remain useful only when their
        // bare ID identifies one row. Never apply one position to two wings.
        return counts.get(session.id) === 1 ? positions.get(session.id) : undefined;
    }
    var known = [], unknown = [];
    sessions.forEach(function(session) { (position(session) === undefined ? unknown : known).push(session); });
    known.sort(function(a, b) { return position(a) - position(b); });
    return known.concat(unknown);
}
