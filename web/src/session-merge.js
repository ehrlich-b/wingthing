// Pure session reconciliation shared by the dashboard and Node tests.

export function reconcileWingSessions(existingSessions, wingId, remoteSessions) {
    var remoteMap = new Map();
    remoteSessions.forEach(function(session) {
        remoteMap.set(session.id, session);
    });

    var kept = [];
    existingSessions.forEach(function(session) {
        if (session.wing_id !== wingId) {
            kept.push(session);
            return;
        }

        var remote = remoteMap.get(session.id);
        if (!remote) return; // The wing says this session is gone.

        session.agent = remote.agent;
        session.name = remote.name;
        session.cwd = remote.cwd;
        session.needs_attention = remote.needs_attention;
        session.audit = remote.audit;
        session.user_id = remote.user_id;
        session.email = remote.email;
        session.lifecycle = remote.lifecycle;
        session.lifecycle_seen_at = remote.lifecycle_seen_at;
        session.conversation_id = remote.conversation_id;
        session.root_conversation_id = remote.root_conversation_id;
        session.parent_conversation_id = remote.parent_conversation_id;
        session.conversation_role = remote.conversation_role;
        session.swept = true;
        kept.push(session);
        remoteMap.delete(session.id);
    });

    remoteMap.forEach(function(session) {
        session.swept = true;
        kept.push(session);
    });
    return kept;
}

// Hosted direct-only accounts use the portal for authenticated discovery and
// WebRTC signaling, not terminal/session payload transport. Keep every refresh
// path aligned so a live wing event cannot reintroduce denied inventory calls.
export function shouldFetchWingSessions(currentUser, wing) {
    if (currentUser && currentUser.relay_allowed === false) return false;
    return !!wing && !wing.tunnel_error;
}
