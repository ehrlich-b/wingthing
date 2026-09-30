export function historyResumeState(session, currentUser, capabilityAvailable) {
    if (!capabilityAvailable) {
        return { available: false, reason: 'Update this wing to resume provider sessions' };
    }
    if (!session.user_id || !currentUser || session.user_id !== currentUser.id) {
        return { available: false, reason: 'Only the session owner can resume it' };
    }
    if (!session.resumable) {
        return { available: false, reason: session.resume_unavailable_reason || 'This provider session cannot be resumed' };
    }
    return { available: true, reason: '' };
}
