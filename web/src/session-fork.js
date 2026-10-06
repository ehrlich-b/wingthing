export function sessionForkAvailable(session, wing, currentUser) {
    return !!session && session.agent === 'claude' && session.forkable === true &&
        !!currentUser && !!session.user_id && session.user_id === currentUser.id &&
        !!wing && wing.online === true && !wing.tunnel_error &&
        Array.isArray(wing.capabilities) && wing.capabilities.includes('session.fork.v1');
}

export function sessionForkControl(session, wing, currentUser) {
    return sessionForkAvailable(session, wing, currentUser)
        ? '<button class="btn-sm session-fork-btn" type="button" data-session-action="fork">Fork</button>'
        : '';
}
