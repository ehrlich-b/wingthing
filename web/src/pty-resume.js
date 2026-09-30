export function resumeAckMatches(requestedSessionId, startedMessage) {
    return !requestedSessionId || !!startedMessage && startedMessage.resumed_from_session_id === requestedSessionId;
}
