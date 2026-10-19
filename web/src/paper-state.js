// Pure state for the paper shell. A session gets one scoped fact: what its
// agent is doing, or that its machine is away. Shapes travel with the label.

export function machineState(wing) {
    if (!wing) return 'unknown';
    if (wing.online === false || wing.tunnel_error === 'unreachable') return 'offline';
    if (['passkey_required', 'passkey_failed', 'no_passkeys_configured'].includes(wing.tunnel_error)) return 'locked';
    if (wing.tunnel_error === 'not_allowed') return 'no-access';
    if (wing.tunnel_error === 'key_mismatch') return 'key-mismatch';
    if (wing.tunnel_error) return 'unavailable';
    return wing.online === true ? 'connected' : 'checking';
}

export function machineLabel(wing) {
    return { connected: 'Connected', offline: 'Offline', checking: 'Checking', locked: 'Locked', 'no-access': 'No access', 'key-mismatch': 'Key changed', unavailable: 'Unavailable', unknown: 'Unknown' }[machineState(wing)];
}

export function machineGuidance(wing) {
    if (wing?.tunnel_error === 'no_passkeys_configured' && machineState(wing) === 'locked') return 'Set up a passkey to unlock this machine.';
    return { offline: 'Reconnect this machine, then retry.', locked: 'Unlock with your passkey.', 'no-access': 'Ask the owner to grant access.', 'key-mismatch': 'Retry to refresh this machine’s key.', unavailable: 'Retry to check access.', checking: 'Checking this machine…', unknown: 'Machine status unavailable.' }[machineState(wing)] || '';
}

export function sessionState(session, wing) {
    const machine = machineState(wing), status = session.lifecycle && session.lifecycle.status;
    if (!['connected', 'checking'].includes(machine)) return { key: machine, label: machineLabel(wing) };
    if (status === 'blocked' || session.needs_attention) return { key: 'need', label: 'Needs you' };
    if (status === 'working') return { key: 'work', label: 'Working' };
    if (status === 'completed') return { key: 'done', label: 'Done' };
    if (status === 'exited') return { key: 'ended', label: 'Ended' };
    if (status === 'idle') return { key: 'idle', label: 'Idle' };
    return { key: 'unknown', label: 'Unknown' };
}

// Project name only; full paths stay out of rows and titles.
export function projectName(cwd) { return String(cwd || '').split(/[\\/]/).filter(Boolean).pop() || ''; }

export function agentName(agent) {
    return ({ claude: 'Claude', codex: 'Codex', gemini: 'Gemini', cursor: 'Cursor', ollama: 'Ollama', opencode: 'OpenCode' })[agent] || (agent ? agent[0].toUpperCase() + agent.slice(1) : '');
}

// The command a blocked session asked to run, when its reason names one.
export function requestedCommand(reason) {
    return (String(reason || '').match(/(\.\/\S+(?:\s+[\w.-]+)*|\b(?:npm|go|make|git|kubectl|fly|terraform)\s[^\n]+)/) || [])[1] || '';
}

// Last contact is evidence of presence, not evidence of when or why it left.
export function offlineLabel(since) {
    return since ? 'offline · last seen ' + since : 'offline';
}

export function paperRoute(hash) {
    if (!hash || hash === '#') return { view: 'wing' };
    if (hash === '#sessions' || hash === '#machines' || hash === '#pair') return { view: hash.slice(1) };
    const m = hash.match(/^#(session|approve)\/([^/]+)\/([^/?]+)$/);
    if (m) return { view: m[1], wingId: decodeURIComponent(m[2]), sessionId: decodeURIComponent(m[3]) };
    return null;
}
