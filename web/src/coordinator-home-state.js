import { CONVERSATION_FRESH_MS } from './conversation-state.js';

export function pinnedCoordinators(sessions, wings, user) {
    if (!user || user.relay_allowed === false) return [];
    return sessions.filter(session => session.pinned === true && session.role === 'coordinator' &&
        wings.some(wing => wing.wing_id === session.wing_id && wing.tunnel_error !== 'not_allowed'))
        .sort((a, b) => JSON.stringify([a.wing_id, a.id]).localeCompare(JSON.stringify([b.wing_id, b.id])));
}

export function coordinatorHomeSummary(session, wing, evidence = {}, now = Date.now()) {
    const lifecycle = evidence.lifecycle || session.lifecycle || {};
    const report = lifecycle.report || {};
    const observedAt = evidence.observedAt || session.lifecycle_seen_at || 0;
    const connected = wing && wing.online === true && !wing.tunnel_error;
    const fresh = connected && !evidence.cached && !evidence.readError && observedAt > 0 && observedAt <= now && now - observedAt < CONVERSATION_FRESH_MS;
    const status = fresh && ['working', 'idle', 'blocked', 'done', 'exited', 'unknown'].includes(lifecycle.status) ? lifecycle.status : 'unknown';
    const reportAt = Date.parse(lifecycle.reported_at);
    const lastWake = Date.parse(report.last_wake);
    const nextWake = Date.parse(report.next_wake);
    const newerNativeState = Number.isSafeInteger(lifecycle.state_cursor) && Number.isSafeInteger(evidence.reportSequence) && lifecycle.state_cursor > evidence.reportSequence;
    let presence = 'unknown';
    let detail = 'No current heartbeat';
    if (wing && wing.online === false) { presence = 'offline'; detail = 'Execution wing is offline'; }
    else if (!fresh) detail = evidence.readError || 'Waiting for a current session observation';
    else if (lifecycle.process_alive === false) { presence = 'stopped'; detail = 'Coordinator process has exited'; }
    else if (status === 'working') { presence = 'awake'; detail = 'Native session is working'; }
    else if (status === 'blocked') { presence = 'awake'; detail = 'Waiting for your input'; }
    else if (Number.isFinite(nextWake) && nextWake < now && !(Number.isFinite(lastWake) && lastWake >= nextWake)) { presence = 'overdue'; detail = 'Planned wake has passed; no new wake reported'; }
    else if (!newerNativeState && Number.isFinite(reportAt) && reportAt <= now && now - reportAt < CONVERSATION_FRESH_MS && ['awake', 'asleep'].includes(report.presence)) {
        presence = report.presence; detail = 'Reported by the coordinator';
    } else if (report.presence) detail = 'Heartbeat report is stale; session is reachable';
    const stopped=lifecycle.process_alive===false;
    const messages = evidence.messages || [];
    const firstRequest = messages.find(message => message.type === 'user' && message.content);
    const latest = messages.filter(message => message.type === 'assistant' && message.content).at(-1);
    return { title: session.name || 'wing', goal: report.goal || (firstRequest && firstRequest.content.slice(0, 2048)) || 'No goal reported yet',
        update: report.update || (latest && latest.content.slice(0, 4096)) || 'No update reported yet',
        nextAction: stopped ? 'Inspect the stopped session before starting another.' : report.next_action || lifecycle.reason || 'Open the conversation to check in', status, presence, detail: stopped && fresh ? 'Coordinator process has exited. No automatic restart is confirmed.' : detail, fresh, historicalUpdate:stopped||!fresh,
        observedAt, reportedAt: Number.isFinite(reportAt) ? reportAt : null,
        lastWake: Number.isFinite(lastWake) && lastWake <= now ? lastWake : null,
        nextWake: !stopped && Number.isFinite(nextWake) ? nextWake : null,
        artifacts: Array.isArray(report.artifacts) ? report.artifacts.slice(0, 8) : [],
        links: Array.isArray(report.links) ? report.links.slice(0, 8) : [] };
}
