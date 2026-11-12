import { S } from '../state.js';
import { escapeMarkup as esc } from '../security.js';
import { wingDisplayName, sessionDisplayName } from '../helpers.js';
import { sendTunnelRequest, randomUUID } from '../tunnel.js';
import { applyConversationRead, restoredConversationState } from '../conversation-state.js';
import { createConversationReader } from '../conversation-recovery.js';
import { machineState, sessionState as stateOf, projectName, agentName, requestedCommand, offlineLabel, machineLabel, machineGuidance } from '../paper-state.js';
import { wingOf, machineName, sessions, sessionState, needing, machines, machineSessions } from './select.js';
const reads = new Map(), terminals = new Map();
const drafts = new Map();
const numberWords = ['zero', 'one', 'two', 'three', 'four', 'five', 'six', 'seven', 'eight', 'nine'];
const machineLimit = 5, machineListLimit = 6;
const ui = { allMachines: false, retrying: false, launchWingId: '', inputReader: null, inputKey: '', inputSnapshot: null, invalidate: () => {} };
function render() { ui.invalidate(); }
const sessionHref = (s, view = 'session') => '#' + view + '/' + encodeURIComponent(s.wing_id) + '/' + encodeURIComponent(s.id);
const project = s => projectName(s.cwd);
const chip = st => '<span class="pp-st pp-st-' + st.key + '">' + esc(st.label) + '</span>';
const clock = t => { const ms = typeof t === 'number' ? t : Date.parse(t); return Number.isFinite(ms) && ms > 0 ? new Date(ms).toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' }) : ''; };
const ago = t => { const ms = Date.parse(t); if (!Number.isFinite(ms)) return ''; const m = Math.max(0, Math.round((Date.now() - ms) / 60000)); return m < 60 ? m + 'm' : m < 1440 ? Math.round(m / 60) + 'h' : Math.round(m / 1440) + 'd'; };
const prose = text => esc(text || '').replace(/`([^`]+)`/g, '<code>$1</code>').replace(/(^|\s)(\.\/[\w./-]+|[\w-]+\/[\w./-]+\.\w+)/g, '$1<code>$2</code>');
const lastSeen = w => { const ms = Date.parse(w.last_seen); return Number.isFinite(ms) ? new Date(ms).toLocaleString([], { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' }) : ''; };
const awayText = w => offlineLabel(lastSeen(w));
const isAway = w => machineState(w) === 'offline';
const staleState = s => !['connected', 'checking'].includes(machineState(wingOf(s.wing_id))) ? 'Last reported: ' + stateOf(s, { online: true }).label + ' · stale' : '';
const retryButton = w => '<button type="button" class="pp-btn" data-retry aria-label="' + esc('Retry ' + machineName(w)) + '"' + (ui.retrying ? ' disabled' : '') + '>' + (ui.retrying ? 'Checking…' : 'Retry') + '</button>';
function storage(kind) { try { return window[kind]; } catch { return null; } }
function inputAllowed(s) {
    return !!s && machineState(wingOf(s.wing_id)) === 'connected' && !S.spectating && S.currentUser?.relay_allowed !== false;
}
function selectComposer(s) {
    const target = s && S.currentUser ? { userId: S.currentUser.id, wingId: s.wing_id, sessionId: s.id } : null;
    const key = target ? JSON.stringify(target) : '';
    if (key === ui.inputKey) return;
    if (ui.inputReader) ui.inputReader.close();
    ui.inputKey = key; ui.inputSnapshot = null;
    if (!target) return;
    ui.inputReader = createConversationReader({
        storage: storage('localStorage'), legacyStorage: storage('sessionStorage'),
        request: sendTunnelRequest, randomId: randomUUID,
        onChange(next) {
            ui.inputSnapshot = next;
            queueMicrotask(() => {
                if (ui.inputSnapshot !== next) return;
                const draft = ui.inputReader.takeDraft();
                if (draft !== null && !drafts.get(ui.inputKey)) drafts.set(ui.inputKey, draft);
                render(true);
            });
        },
    });
    ui.inputReader.open(target);
}
function offlinePanel(w) {
    return '<section class="pp-away" role="status"><b>' + esc(machineName(w)) + ' is offline</b><p>' + esc(lastSeen(w) ? 'Last seen ' + lastSeen(w) + '.' : 'Last seen time unavailable.') + ' Sessions are kept here.</p><p class="pp-quiet">Reconnect this machine, then retry.</p><div class="pp-actions">' + retryButton(w) + '</div></section>';
}
function accessPanel(w) {
    if (isAway(w)) return offlinePanel(w);
    if (machineState(w) === 'connected' || machineState(w) === 'checking') return '';
    return '<section class="pp-away" role="status"><b>' + esc(machineName(w) + ' · ' + machineLabel(w)) + '</b><p>' + esc(machineGuidance(w)) + '</p><div class="pp-actions">' + machineActions(w) + '</div></section>';
}
function machineActions(w) {
    const m = machineState(w);
    if (m === 'locked' || m === 'no-access') return '<a class="pp-btn" href="#machine/' + encodeURIComponent(w.wing_id) + '">Machine access</a>';
    return retryButton(w);
}
function squares() {
    const named = machines().filter(w => wingDisplayName(w));
    return named.slice(0, 3).map(w => { const m = machineState(w); return '<a data-key="' + esc(w.wing_id) + '" href="#machine/' + encodeURIComponent(w.wing_id) + '" class="pp-sq pp-sq-' + m + '" title="' + esc(machineName(w) + ' · ' + machineLabel(w)) + '"><i aria-hidden="true"></i><span class="pp-sq-name">' + esc(machineName(w)) + '</span><span class="pp-sr"> ' + esc(machineLabel(w)) + '</span></a>'; }).join('') + (named.length > 3 ? '<a class="pp-more" href="#machines">+' + (named.length - 3) + ' more</a>' : '');
}
function read(s) {
    if (ui.inputSnapshot?.target && ui.inputSnapshot.target.userId === S.currentUser?.id && ui.inputSnapshot.target.wingId === s.wing_id && ui.inputSnapshot.target.sessionId === s.id) {
        return { state: { messages: ui.inputSnapshot.messages, cursor: ui.inputSnapshot.cursor }, at: ui.inputSnapshot.observedAt, error: ui.inputSnapshot.readError };
    }
    const k = s.wing_id + '/' + s.id, w = wingOf(s.wing_id);
    let entry = reads.get(k);
    if (!entry) { entry = { state: restoredConversationState(null), at: 0, busy: false, error: '' }; reads.set(k, entry); }
    if (!entry.busy && machineState(w) === 'connected' && Date.now() - entry.at > 8000) {
        entry.busy = true;
        sendTunnelRequest(s.wing_id, { type: 'session.control', operation: 'session_read', arguments: { session: s.id, after_cursor: entry.state.cursor || 0, limit: 60 } })
            .then(result => {
                if (!result.lifecycle || result.lifecycle.session_id !== s.id) throw Error('Session did not match');
                entry.state = applyConversationRead(entry.state, result.lifecycle, { expected: { sessionId: s.id } }); entry.error = '';
            })
            .catch(error => { entry.error = error.message || 'Could not read session'; })
            .finally(() => { entry.at = Date.now(); entry.busy = false; render(true); });
    }
    return entry;
}
function lines(s) {
    return read(s).state.messages.filter(m => (m.type === 'user' || m.type === 'assistant') && m.content || m.type === 'activity' && m.report && m.report.update)
        .map(m => ({ id: m.sequence + '/' + (m.itemIndex || 0), who: m.type === 'user' ? 'you' : 'agent', text: m.report ? m.report.update : m.content, at: m.timestamp }));
}
function request(s) {
    const marker = read(s).state.messages.filter(m => m.type === 'lifecycle' && m.kind === 'attention' || m.type === 'activity' && m.state === 'needs_input').pop();
    const reason = (marker && marker.reason) || (s.lifecycle && s.lifecycle.reason) || '';
    const command = requestedCommand(reason);
    return { reason, command, at: marker && marker.timestamp };
}
function terminal(s) {
    const k = s.wing_id + '/' + s.id;
    let entry = terminals.get(k);
    if (!entry) { entry = { text: '', at: 0, busy: false, error: '' }; terminals.set(k, entry); }
    if (!entry.busy && machineState(wingOf(s.wing_id)) === 'connected' && Date.now() - entry.at > 1000) {
        entry.busy = true;
        sendTunnelRequest(s.wing_id, { type: 'session.control', operation: 'session_action', arguments: { origin_wing_id: s.wing_id, session_id: s.id, operation: 'terminal_read', arguments: {} } })
            .then(result => { const ansi = result.result ? result.result.ansi : result.ansi; if (typeof ansi !== 'string') throw Error('No terminal preview'); entry.text = ansi.replace(/\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07]*\x07/g, ''); entry.error = ''; })
            .catch(error => { entry.error = 'Terminal unavailable: ' + (error.message || 'Reconnect this machine.'); })
            .finally(() => { entry.at = Date.now(); entry.busy = false; render(true); });
    }
    return entry;
}
function needCard(s, primary = 'Answer in terminal') {
    const r = request(s), w = wingOf(s.wing_id);
    return '<article class="pp-need" data-key="' + esc(s.wing_id + '/' + s.id) + '" tabindex="-1"><div class="pp-need-top"><span class="pp-st pp-st-need">Needs you</span><time>' + esc(clock(r.at)) + '</time></div>' +
        '<h3>' + esc(sessionDisplayName(s)) + ' is waiting for you</h3>' +
        '<p>' + (r.command ? 'It asks to run this on ' + esc(machineName(w)) + '.' : r.reason ? prose(r.reason) + ' on ' + esc(machineName(w)) + '.' : 'The session on ' + esc(machineName(w)) + ' asked for a decision.') + '</p>' +
        (r.command ? '<pre class="pp-cmd"><span class="pp-prompt">$</span> ' + esc(r.command) + '</pre>' : '') +
        '<div class="pp-actions"><a class="pp-btn pp-btn-ink" href="' + sessionHref(s, 'approve') + '">' + esc(primary) + '</a><a class="pp-btn" href="' + sessionHref(s) + '">Open session</a></div></article>';
}
function needRow(s) {
    const r = request(s), w = wingOf(s.wing_id);
    const line = r.reason || 'Request unavailable. Check the terminal.';
    const where = machineName(w) + (machineState(w) === 'connected' ? '' : ' · ' + machineLabel(w));
    return '<a class="pp-need-row" data-key="' + esc(s.wing_id + '/' + s.id) + '" href="' + sessionHref(s, 'approve') + '">' +
        '<span class="pp-need-identity"><b>' + esc(sessionDisplayName(s)) + '</b><small>' + esc(where) + '</small></span>' +
        '<span class="pp-need-request" title="' + esc(line) + '">' + esc(line) + '</span><span class="pp-need-answer">Answer →</span></a>';
}
function needsSummary() {
    const need = needing();
    return '<section class="pp-needs" id="pp-needs" tabindex="-1" aria-labelledby="pp-needs-title">' +
        '<div class="pp-needs-heading"><h2 id="pp-needs-title">Needs you <span>' + need.length + '</span></h2>' +
        (need.length > 2 ? '<a class="pp-more" href="#sessions">+' + (need.length - 2) + ' more</a>' : '') + '</div>' +
        (need.length ? need.slice(0, 2).map(needRow).join('') : '<p class="pp-quiet">Nothing waiting</p>') + '</section>';
}
function kicker(left, right = '') { return '<div class="pp-kick"><span>' + left + '</span><span>' + right + '</span></div>'; }
function composer(label, s) {
    if (machineState(wingOf(s.wing_id)) !== 'connected') return '';
    const snap = ui.inputSnapshot, pending = snap?.pending, receipt = snap?.deliveryReceipt;
    const allowed = inputAllowed(s), ready = allowed && snap?.inputReady && !snap?.continuationReady;
    const unavailable = snap?.presentation?.archived ? 'Session ended' : snap?.presentation?.state === 'unsupported' ? 'Use terminal' : snap?.lifecycle?.state === 'working' ? 'Agent is working' : 'Messaging unavailable';
    const status = pending ? (snap.checking ? 'Checking delivery…' : 'Delivery unconfirmed') : receipt ? 'Delivered' : !allowed ? 'Read only' : snap?.notice || (ready ? '' : unavailable);
    return '<form class="pp-composer" data-composer="' + esc(s.wing_id + '/' + s.id) + '"><label class="pp-sr" for="pp-message">' + esc(label) + '</label>' +
        '<div class="pp-compose-entry"><textarea id="pp-message" rows="1" placeholder="' + esc(label) + '"' + (ready ? '' : ' disabled') + '></textarea><button type="submit" class="pp-btn pp-btn-ink"' + (ready && drafts.get(ui.inputKey)?.trim() ? '' : ' disabled') + '>Send</button></div>' +
        '<div class="pp-delivery" role="status">' + esc(status) + (pending || receipt ? '<small>Request ' + esc(pending?.request_id || receipt.request_id) + '</small>' : '') + '</div>' +
        (pending ? '<pre class="pp-pending-text">' + esc(pending.input) + '</pre><button type="button" class="pp-btn" data-check-receipt' + (allowed && !snap.checking ? '' : ' disabled') + '>Check receipt</button>' : '') + '</form>';
}
function sessionRow(s) {
    const st = sessionState(s), last = lines(s).filter(l => l.who === 'agent').pop(), w = wingOf(s.wing_id);
    const line = st.key === 'need' ? (request(s).reason || (last && last.text) || '') : last ? last.text : '';
    return '<a class="pp-srow" data-key="' + esc(s.wing_id + '/' + s.id) + '" data-k="' + esc(s.wing_id + '/' + s.id) + '" href="' + sessionHref(s) + '">' + chip(st) +
        '<span class="pp-sname"><b>' + esc(sessionDisplayName(s)) + '</b><i>' + esc([agentName(s.agent), machineName(w), project(s)].filter(Boolean).join(' · ')) + '</i></span>' +
        '<span class="pp-sline">' + esc(staleState(s) || line) + '</span><time>' + esc(ago(last && last.at)) + '</time></a>';
}
function machineRow(w, detail = '') {
    const m = machineState(w), n = machineSessions(w.wing_id).length;
    const label = isAway(w) ? awayText(w).replace(/^./, c => c.toUpperCase()) : machineLabel(w);
    return '<div class="pp-mrow" data-key="' + esc(w.wing_id) + '"><span class="pp-sq pp-sq-' + m + '"><i aria-hidden="true"></i></span><span class="pp-mname"><b>' + esc(machineName(w)) + '</b><i>' + esc(label) + '</i>' + (detail ? '<span class="pp-mdetail">' + esc(detail) + '</span>' : '') + '</span><small>' + n + (n === 1 ? ' session' : ' sessions') + '</small></div>';
}
function machineOverflow() { return machines().length > machineLimit ? '<a class="pp-more" href="#machines">+' + (machines().length - machineLimit) + ' more</a>' : ''; }

export { storage, inputAllowed, selectComposer, offlinePanel, accessPanel, machineActions, squares, read, lines, request, terminal, needCard, needRow, needsSummary, kicker, composer, sessionRow, machineRow, machineOverflow, sessionHref, project, chip, clock, ago, prose, lastSeen, awayText, isAway, staleState, retryButton, ui, drafts, numberWords, machineLimit, machineListLimit };
