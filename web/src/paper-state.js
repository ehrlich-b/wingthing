// Pure state for the paper shell. A session gets one scoped fact: what its
// agent is doing, or that its machine is away. Shapes travel with the label.

export function machineState(wing) {
    if (!wing) return 'unknown';
    if (wing.online === false) return 'asleep';
    if (wing.tunnel_error) return 'unreachable';
    return wing.online === true ? 'connected' : 'checking';
}

export function sessionState(session, wing) {
    const machine = machineState(wing), status = session.lifecycle && session.lifecycle.status;
    if (machine === 'asleep') return { key: 'asleep', label: 'Asleep' };
    if (machine === 'unreachable') return { key: 'asleep', label: 'Unreachable' };
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

// One word for a machine that stopped reporting, plus when it was last seen.
export function awayLabel(state, since) {
    const word = state === 'unreachable' ? 'unreachable' : 'asleep';
    return since ? word + ' since ' + since : word;
}

export function paperRoute(hash) {
    if (!hash || hash === '#') return { view: 'wing' };
    if (hash === '#sessions' || hash === '#machines' || hash === '#pair') return { view: hash.slice(1) };
    const m = hash.match(/^#(session|approve)\/([^/]+)\/([^/?]+)$/);
    if (m) return { view: m[1], wingId: decodeURIComponent(m[2]), sessionId: decodeURIComponent(m[3]) };
    return null;
}
