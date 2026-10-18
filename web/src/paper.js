// Paper shell: wing thread, sessions, one session, approve, pair, machines and
// empty or asleep states, rendered from the same inventory the legacy views use.
// Terminal take-over hands off to the existing PTY view.
import './paper.css';
import { S } from './state.js';
import { escapeMarkup as esc } from './security.js';
import { wingDisplayName, sessionDisplayName } from './helpers.js';
import { sendTunnelRequest } from './tunnel.js';
import { applyConversationRead, restoredConversationState } from './conversation-state.js';
import { switchToSession, showSessionChat, showHome } from './nav.js';
import { showPalette } from './palette.js';
import { loadHome, probeWing } from './data.js';
import { browserUnavailable, installedAgents, directSetup, agentSetup } from './onboarding.js';
import { paperRoute as route, machineState, sessionState as stateOf, projectName, agentName, requestedCommand, awayLabel } from './paper-state.js';

const reads = new Map();
const terminals = new Map();
const numberWords = ['zero', 'one', 'two', 'three', 'four', 'five', 'six', 'seven', 'eight', 'nine'];
let root, main, side, lastSignature = '', launchWingId = '';
const machineLimit = 5;

export function paperRoute(hash = location.hash) { return route(hash); }
export function isPaperRoute(hash) { return !!paperRoute(hash); }

const wingOf = id => S.wingsData.find(w => w.wing_id === id);
const machineName = w => wingDisplayName(w, S.wingsData) || 'Machine';
const sessionHref = (s, view = 'session') => '#' + view + '/' + encodeURIComponent(s.wing_id) + '/' + encodeURIComponent(s.id);
const isCoordinator = s => s.pinned === true && s.role === 'coordinator';
const project = s => projectName(s.cwd);
const chip = st => '<span class="pp-st pp-st-' + st.key + '">' + esc(st.label) + '</span>';
const clock = t => { const ms = typeof t === 'number' ? t : Date.parse(t); return Number.isFinite(ms) && ms > 0 ? new Date(ms).toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' }) : ''; };
const ago = t => { const ms = Date.parse(t); if (!Number.isFinite(ms)) return ''; const m = Math.max(0, Math.round((Date.now() - ms) / 60000)); return m < 60 ? m + 'm' : m < 1440 ? Math.round(m / 60) + 'h' : Math.round(m / 1440) + 'd'; };
// Inline code for commands and file names; everything else stays prose.
const prose = text => esc(text || '').replace(/`([^`]+)`/g, '<code>$1</code>').replace(/(^|\s)(\.\/[\w./-]+|[\w-]+\/[\w./-]+\.\w+)/g, '$1<code>$2</code>');
const awayText = w => awayLabel(machineState(w), clock(w.last_seen));
const squares = () => S.wingsData.filter(w => wingDisplayName(w)).slice(0, machineLimit).map(w => { const m = machineState(w); return '<span class="pp-sq pp-sq-' + m + '" title="' + esc(machineName(w) + ' · ' + m) + '"><i aria-hidden="true"></i>' + esc(machineName(w)) + '<span class="pp-sr"> ' + m + '</span></span>'; }).join('');

function sessions() { return S.sessionsData.filter(s => (s.kind || 'terminal') !== 'chat' && wingOf(s.wing_id)); }
function coordinator() { return sessions().find(isCoordinator); }
function workSessions() { return sessions().filter(s => !isCoordinator(s)); }
function sessionState(s) { return stateOf(s, wingOf(s.wing_id)); }
function needing() { return workSessions().filter(s => sessionState(s).key === 'need'); }

// Read the session's own transcript evidence; cached briefly per session.
function read(s) {
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
        .map(m => ({ who: m.type === 'user' ? 'you' : 'agent', text: m.report ? m.report.update : m.content, at: m.timestamp }));
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
    if (!entry.busy && machineState(wingOf(s.wing_id)) === 'connected' && Date.now() - entry.at > 4000) {
        entry.busy = true;
        sendTunnelRequest(s.wing_id, { type: 'session.control', operation: 'terminal_read', arguments: { session: s.id } })
            .then(result => { const ansi = result.result ? result.result.ansi : result.ansi; if (typeof ansi !== 'string') throw Error('No terminal preview'); entry.text = ansi.replace(/\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07]*\x07/g, ''); entry.error = ''; })
            .catch(() => { entry.error = 'Preview unavailable from this machine.'; })
            .finally(() => { entry.at = Date.now(); entry.busy = false; render(true); });
    }
    return entry;
}

function needCard(s, primary = 'Answer in terminal') {
    const r = request(s), w = wingOf(s.wing_id);
    return '<article class="pp-need"><div class="pp-need-top"><span class="pp-st pp-st-need">Needs you</span><time>' + esc(clock(r.at)) + '</time></div>' +
        '<h3>' + esc(sessionDisplayName(s)) + ' is waiting for you</h3>' +
        '<p>' + (r.command ? 'It asks to run this on ' + esc(machineName(w)) + '.' : r.reason ? prose(r.reason) + ' on ' + esc(machineName(w)) + '.' : 'The session on ' + esc(machineName(w)) + ' asked for a decision.') + '</p>' +
        (r.command ? '<pre class="pp-cmd"><span class="pp-prompt">$</span> ' + esc(r.command) + '</pre>' : '') +
        '<div class="pp-actions"><a class="pp-btn pp-btn-ink" href="' + sessionHref(s, 'approve') + '">' + esc(primary) + '</a><a class="pp-btn" href="' + sessionHref(s) + '">Open session</a></div></article>';
}
function head(title, meta) { return '<header class="pp-hdr"><h1>' + title + '</h1><div class="pp-meta">' + meta + '</div></header>'; }
function kicker(left, right = '') { return '<div class="pp-kick"><span>' + left + '</span><span>' + right + '</span></div>'; }

function viewEmpty() {
    if (!S.wingsData.length) return head('wing', '') + '<section class="pp-empty"><h2>No machines yet</h2><p>Install wt on a computer where your agents run, then sign in and start it. It shows up here when it connects.</p>' +
        '<pre class="pp-cmd"><span class="pp-prompt">$</span> curl -fsSL https://wingthing.ai/install.sh | sh\n<span class="pp-prompt">$</span> wt login\n<span class="pp-prompt">$</span> wt start</pre>' +
        '<div class="pp-actions"><button type="button" class="pp-btn" data-copy="curl -fsSL https://wingthing.ai/install.sh | sh">Copy install command</button><a class="pp-btn" href="#pair">Pair a machine</a></div></section>';
    return head('wing', '') + '<div class="pp-cols"><section class="pp-empty"><h2>No sessions yet</h2><p>Choose a machine and set up an agent to begin.</p>' +
        '<div class="pp-actions"><button type="button" class="pp-btn pp-btn-ink" data-new-session>New session</button></div></section>' +
        '<aside class="pp-col-rail">' + kicker('Machines', String(S.wingsData.length)) + S.wingsData.slice(0, machineLimit).map(w => machineRow(w)).join('') + machineOverflow() +
        kicker('From a terminal') + '<pre class="pp-cmd">wt setup</pre><p class="pp-quiet pp-gap">Install an agent and sign in to its provider on that machine.</p></aside></div>';
}

// Setup answers which machine can start an agent; project selection stays in
// the existing picker once a detected provider is available.
function viewNew(route) {
    if (!S.wingsData.length) return viewEmpty();
    const w = S.wingsData.find(w => w.wing_id === (route.wingId || launchWingId)) || S.wingsData.find(w => machineState(w) === 'connected') || S.wingsData[0];
    launchWingId = w.wing_id;
    return head('New session', 'Choose a machine') + '<section class="pp-setup"><label for="pp-launch-machine">Machine</label><select id="pp-launch-machine">' +
        S.wingsData.map(item => '<option value="' + esc(item.wing_id) + '"' + (item === w ? ' selected' : '') + '>' + esc(machineName(item)) + '</option>').join('') + '</select>' +
        (machineState(w) !== 'connected' ? '<h2>Machine unavailable</h2><p>Reconnect ' + esc(machineName(w)) + ', then try again.</p><button type="button" class="pp-btn" data-retry>Retry</button>' :
            installedAgents(w).length ? '<h2>Ready on ' + esc(machineName(w)) + '</h2><p>' + esc(installedAgents(w).map(agentName).join(', ')) + '</p><button type="button" class="pp-btn pp-btn-ink" data-choose-project="' + esc(w.wing_id) + '">Choose project</button>' : agentSetup(w, machineName(w))) + '</section>';
}

function viewWing() {
    const work = workSessions(), coord = coordinator();
    if (!work.length && !coord) return viewEmpty();
    const rows = [];
    if (coord) for (const l of lines(coord)) rows.push({ who: l.who === 'you' ? 'you' : 'wing', text: l.text, at: l.at, where: machineName(wingOf(coord.wing_id)) });
    for (const s of work) {
        const last = lines(s).filter(l => l.who === 'agent').pop();
        if (last) rows.push({ who: 'session', s, text: last.text, at: last.at, where: agentName(s.agent) + ' · ' + machineName(wingOf(s.wing_id)) });
    }
    rows.sort((a, b) => (Date.parse(a.at) || 0) - (Date.parse(b.at) || 0));
    const reporting = new Set(rows.filter(r => r.s).map(r => r.s.id)).size;
    const need = needing();
    const thread = rows.slice(-14).map(r => '<div class="pp-row pp-row-' + r.who + '" data-source="' + esc((r.s ? sessionDisplayName(r.s) : r.who) + ' · ' + (r.s ? machineName(wingOf(r.s.wing_id)) : r.where)) + '">' +
        '<div class="pp-who">' + (r.s ? '<a href="' + sessionHref(r.s) + '">' + esc(sessionDisplayName(r.s)) + '</a>' + chip(sessionState(r.s)) : '<b>' + r.who + '</b>') +
        '<small>' + esc([clock(r.at), r.who === 'you' ? '' : r.where].filter(Boolean).join(' · ')) + '</small></div>' +
        '<div class="pp-say">' + prose(r.text) + '</div></div>').join('');
    const meta = '<b>wing</b>' + (coord ? ' · ' + esc(machineName(wingOf(coord.wing_id))) : '') + (coord ? ' · ' + esc(agentName(coord.agent)) + chip(sessionState(coord)) : '') + (need.length ? ' · <em>' + need.length + ' needs you</em>' : '');
    return head('wing', meta) + '<div class="pp-cols"><div class="pp-col-main">' +
        '<div class="pp-thread-heading">' + kicker('<span data-thread-source>Today</span>', reporting + (reporting === 1 ? ' session' : ' sessions') + ' reporting') + '</div><div class="pp-conversation" tabindex="0" aria-label="Wing messages">' +
        (thread || '<p class="pp-quiet">Wing has not reported yet. Its updates and each session\'s latest line appear here.</p>') +
        need.map(s => needCard(s)).join('') + '</div>' + (coord ? composer('Message wing', coord) : '') + '</div>' +
        '<aside class="pp-col-rail">' + kicker('Sessions', String(work.length)) + work.map(s => '<a class="pp-mini" href="' + sessionHref(s) + '">' + chip(sessionState(s)) + '<span>' + esc(sessionDisplayName(s)) + '</span><small>' + esc(machineName(wingOf(s.wing_id))) + '</small></a>').join('') +
        kicker('Machines', String(S.wingsData.length)) + '<div class="pp-squares">' + squares() + '</div></aside></div>';
}

function composer(label, s) {
    return '<form class="pp-composer" data-composer="' + esc(s.wing_id + '/' + s.id) + '"><label class="pp-sr" for="pp-message">' + esc(label) + '</label>' +
        '<textarea id="pp-message" rows="1" placeholder="' + esc(label) + '"></textarea><button type="submit" class="pp-btn pp-btn-ink">Send</button></form>';
}

function sessionRow(s) {
    const st = sessionState(s), last = lines(s).filter(l => l.who === 'agent').pop(), w = wingOf(s.wing_id);
    const line = st.key === 'need' ? (request(s).reason || (last && last.text) || '') : last ? last.text : '';
    return '<a class="pp-srow" href="' + sessionHref(s) + '">' + chip(st) +
        '<span class="pp-sname"><b>' + esc(sessionDisplayName(s)) + '</b><i>' + esc([agentName(s.agent), machineName(w), project(s)].filter(Boolean).join(' · ')) + '</i></span>' +
        '<span class="pp-sline">' + esc(line) + '</span><time>' + esc(ago(last && last.at)) + '</time></a>';
}
function group(title, list, right) { return list.length ? '<section class="pp-group">' + kicker(title, right || String(list.length)) + list.map(sessionRow).join('') + '</section>' : ''; }

function viewSessions() {
    const work = workSessions();
    if (!work.length) return viewEmpty();
    const by = k => work.filter(s => sessionState(s).key === k);
    const asleepWings = S.wingsData.filter(w => machineState(w) === 'asleep' || machineState(w) === 'unreachable');
    const awake = S.wingsData.filter(w => machineState(w) === 'connected').map(machineName);
    const banner = asleepWings.map(w => { const n = work.filter(s => s.wing_id === w.wing_id).length; const name = machineName(w);
        return '<section class="pp-away"><div class="pp-need-top"><span class="pp-st pp-st-asleep">' + esc(name + ' is ' + awayLabel(machineState(w))) + '</span><time>' + esc(w.last_seen ? 'since ' + clock(w.last_seen) : '') + '</time></div>' +
        '<p>' + (n ? (n === 1 ? 'Its session is' : 'Its ' + (numberWords[n] || n) + ' sessions are') + ' shown as last seen. Nothing on ' + esc(name) + ' can be opened or messaged until it reconnects.' : 'Nothing on ' + esc(name) + ' can be opened until it reconnects.') + '</p>' +
        '<div class="pp-actions"><button type="button" class="pp-btn" data-retry>Retry ' + esc(name) + '</button></div></section>'; }).join('');
    const away = work.filter(s => sessionState(s).key === 'asleep');
    const meta = '<b>' + work.length + '</b> on ' + S.wingsData.length + (S.wingsData.length === 1 ? ' machine' : ' machines') + ' · ' + awake.length + ' connected' + (asleepWings.length ? ' · <span class="pp-away-word">' + asleepWings.map(w => esc(machineName(w) + ' ' + awayLabel(machineState(w))) + (w.last_seen ? '<span class="pp-since"> since ' + esc(clock(w.last_seen)) + '</span>' : '')).join(', ') + '</span>' : '');
    const coord = coordinator(), coordLast = coord && lines(coord).filter(l => l.who === 'agent').pop();
    return head('Sessions', meta) + '<div class="pp-cols"><div class="pp-col-main">' + banner +
        '<div class="pp-searchbar"><label class="pp-sr" for="pp-filter">Search sessions</label><input id="pp-filter" type="search" placeholder="Search work or machines" autocomplete="off"><button type="button" class="pp-btn" data-new-session>New session</button></div>' +
        group('Needs you', by('need')) + group('Working', by('work')) + group('Idle and done', [...by('idle'), ...by('done'), ...by('ended'), ...by('unknown')]) +
        asleepWings.map(w => group('Last seen on ' + esc(machineName(w)), away.filter(s => s.wing_id === w.wing_id), 'Cached')).join('') + '</div>' +
        '<aside class="pp-col-rail">' + (coord ? kicker('Pinned') + '<div class="pp-pin"><p><b class="pp-wing">wing</b> <small>' + esc(clock(coordLast && coordLast.at)) + '</small></p><p>' + prose(coordLast ? coordLast.text : 'No update yet.') + '</p><a class="pp-btn" href="#">Open wing</a></div>' : '') +
        kicker('Machines', String(S.wingsData.length)) + S.wingsData.slice(0, machineLimit).map(w => machineRow(w)).join('') + machineOverflow() + '</aside></div>';
}

function machineOverflow() { return S.wingsData.length > machineLimit ? '<a class="pp-more" href="#machines">+' + (S.wingsData.length - machineLimit) + ' more</a>' : ''; }

function machineRow(w, detail = '') {
    const m = machineState(w), n = workSessions().filter(s => s.wing_id === w.wing_id).length;
    const label = m === 'asleep' || m === 'unreachable' ? awayText(w).replace(/^./, c => c.toUpperCase()) : { connected: 'Connected', checking: 'Checking', unknown: 'Unknown' }[m];
    return '<div class="pp-mrow"><span class="pp-sq pp-sq-' + m + '"><i aria-hidden="true"></i></span><span class="pp-mname"><b>' + esc(machineName(w)) + '</b><i>' + esc(label) + '</i>' + (detail ? '<span class="pp-mdetail">' + esc(detail) + '</span>' : '') + '</span><small>' + n + (n === 1 ? ' session' : ' sessions') + '</small></div>';
}

function viewMachines() {
    if (!S.wingsData.length) return viewEmpty();
    const rows = S.wingsData.map(w => { const m = machineState(w), list = workSessions().filter(s => s.wing_id === w.wing_id);
        return '<section class="pp-machine">' + machineRow(w, [w.platform === 'darwin' ? 'macOS' : w.platform === 'linux' ? 'Linux' : w.platform, (w.agents || []).map(agentName).join(', ')].filter(Boolean).join(' · ')) +
        (list.length ? '<ul>' + list.map(s => '<li><a href="' + sessionHref(s) + '">' + esc(sessionDisplayName(s)) + '</a> ' + chip(sessionState(s)) + '</li>').join('') + '</ul>' : '<p class="pp-quiet">No sessions on ' + esc(machineName(w)) + '.</p>') +
        (m === 'connected' && !installedAgents(w).length ? agentSetup(w, machineName(w)) : '') +
        (m === 'asleep' || m === 'unreachable' ? '<button type="button" class="pp-btn" data-retry>Retry ' + esc(machineName(w)) + '</button>' : '') + '</section>'; }).join('');
    return head('Machines', '<span class="pp-squares">' + squares() + '</span>') + '<div class="pp-cols"><div class="pp-col-main">' + kicker('Your machines', String(S.wingsData.length)) + rows + '</div>' +
        '<aside class="pp-col-rail">' + kicker('Add a device') + '<p>Run <code>wt start</code> on another computer to add it here.</p><p class="pp-quiet">To list a machine\'s sessions in another browser without signing in, run <code>wt pair --show-link</code> on it, then open Pair.</p><a class="pp-btn" href="#pair">Pair a machine</a></aside></div>';
}

// The accountless Pair entry is handled by entry.js before account login.
function viewPair() {
    return head('Pair a machine', 'On the computer') + '<p>Open the Pair page to join this machine.</p><a href="#pair" onclick="location.reload()">Open Pair</a>';
}

function findSession(route) { return sessions().find(s => s.id === route.sessionId && s.wing_id === route.wingId); }
function missing() { return head('Session', '') + '<section class="pp-empty"><h2>This session is not listed</h2><p>It may have ended, or its machine has not reported yet.</p><div class="pp-actions"><a class="pp-btn" href="#sessions">All sessions</a></div></section>'; }

function viewSession(route) {
    const s = findSession(route); if (!s) return missing();
    const w = wingOf(s.wing_id), st = sessionState(s), entry = read(s), term = terminal(s);
    const msgs = lines(s).slice(-24).map(l => '<div class="pp-row pp-row-' + (l.who === 'you' ? 'you' : 'session') + '"><div class="pp-who"><b>' + (l.who === 'you' ? 'you' : 'session') + '</b><small>' + esc(clock(l.at)) + '</small></div><div class="pp-say">' + prose(l.text) + '</div></div>').join('');
    const live = machineState(w) === 'connected', phone = matchMedia('(max-width: 760px)').matches;
    const meta = esc([agentName(s.agent), machineName(w), project(s)].filter(Boolean).join(' · ')) + chip(st);
    return head(esc(sessionDisplayName(s)), meta) + '<div class="pp-cols pp-split"><div class="pp-col-main"><div class="pp-conversation" tabindex="0" aria-label="Session messages">' + kicker('Messages', entry.error ? esc(entry.error) : '') +
        (msgs || '<p class="pp-quiet">' + (entry.at ? 'No messages in this session yet.' : 'Reading the session…') + '</p>') +
        (st.key === 'need' ? needCard(s) : '') + '</div>' + (machineState(w) === 'connected' ? composer('Message ' + sessionDisplayName(s), s) : '') + '</div>' +
        '<aside class="pp-col-rail pp-term-rail">' + (phone ? '<details class="pp-terminal"><summary>Show terminal</summary>' : '<div class="pp-terminal">') + kicker('Terminal', live ? 'Agent has control' : 'Last seen') +
        '<pre class="pp-term" aria-label="Terminal preview">' + esc((term.text || '').split('\n').slice(-18).join('\n').trimEnd() || term.error || (live ? 'Waiting for terminal output…' : machineName(w) + ' is ' + awayText(w) + '. The last screen is not cached.')) + '</pre>' +
        (live ? '<button type="button" class="pp-btn" data-takeover="' + esc(s.wing_id + '/' + s.id) + '">Take over terminal</button><p class="pp-quiet">Your keys go to the session until you hand control back. Messages wait while you have control.</p>' : '') + (phone ? '</details>' : '</div>') + '</aside></div>';
}

function viewApprove(route) {
    const s = findSession(route); if (!s) return missing();
    const w = wingOf(s.wing_id), r = request(s), st = sessionState(s), where = machineName(w);
    const why = lines(s).filter(l => l.who === 'agent').slice(-3);
    const live = machineState(w) === 'connected';
    const facts = [['Where', '<b>' + esc(where) + '</b>' + (project(s) ? ' · ' + esc(project(s)) : '')], ['Agent', esc(agentName(s.agent))], ['Asked', esc(clock(r.at) || 'Time not recorded')]];
    const id = esc(s.wing_id + '/' + s.id);
    const actions = (live && st.key === 'need' ? '<button type="button" class="pp-btn pp-btn-ink" data-takeover="' + id + '">Answer in terminal</button>' : '') + '<a class="pp-btn" href="' + sessionHref(s) + '">Open session</a>';
    const note = !live ? where + ' is ' + awayText(w) + '. Open its terminal when it reconnects.' : 'Opens the terminal on ' + where + '. Check the current prompt there before you answer.';
    return head('Answer in terminal', esc(sessionDisplayName(s)) + ' · ' + esc(where)) + '<div class="pp-cols"><div class="pp-col-main">' +
        kicker('Request from ' + esc(sessionDisplayName(s)), chip(st)) +
        '<p class="pp-lead">' + (st.key === 'need' ? esc(sessionDisplayName(s)) + ' is waiting for an answer on ' + esc(where) + '. The current prompt and its choices are in its terminal.' : 'This recorded request may have changed. Check its current status in the terminal.') + '</p>' +
        kicker('Recorded request') + '<p>' + prose(r.reason || 'The session did not record a request.') + '</p>' +
        '<dl class="pp-facts">' + facts.map(f => '<dt>' + f[0] + '</dt><dd>' + f[1] + '</dd>').join('') + '</dl>' +
        '<div class="pp-actions">' + actions + '</div><p class="pp-quiet pp-note" role="status">' + esc(note) + '</p></div>' +
        '<aside class="pp-col-rail">' + kicker('Recent messages') + (why.length ? why.map(l => '<div class="pp-why"><small>' + esc(clock(l.at)) + '</small><p>' + prose(l.text) + '</p></div>').join('') : '<p class="pp-quiet">No earlier messages were recorded.</p>') + '</aside></div>';
}

function nav(route) {
    const need = needing().length, here = route.view === 'wing' || route.view === 'approve' ? 'wing' : route.view === 'machines' || route.view === 'pair' ? 'machines' : 'sessions';
    const item = (key, label, href, count, hot) => '<a href="' + href + '" class="pp-nav-' + key + '"' + (here === key ? ' aria-current="page"' : '') + '><span>' + label + '</span><small' + (hot ? ' class="pp-hot"' : '') + '>' + count + '</small></a>';
    return item('wing', 'wing', '#', need ? need + ' needs you' : '', !!need) + item('sessions', 'Sessions', '#sessions', workSessions().length || '', false) + item('machines', 'Machines', '#machines', S.wingsData.length || '', false);
}

function frame() {
    root = document.createElement('div');
    root.id = 'paper';
    root.innerHTML = '<aside class="pp-side"><a class="pp-brand" href="#">wing<i>thing</i></a><p class="pp-date"></p><nav class="pp-nav" aria-label="Main"></nav><div class="pp-side-squares pp-squares" aria-label="Machines"></div><p class="pp-foot"></p></aside><main class="pp-main"></main><nav class="pp-tabs" aria-label="Main"></nav>';
    document.body.prepend(root);
    main = root.querySelector('.pp-main'); side = root.querySelector('.pp-side');
    root.addEventListener('click', event => {
        const take = event.target.closest('[data-takeover]');
        if (take) { const [wingId, sessionId] = take.dataset.takeover.split('/'); show(false); switchToSession(sessionId, true, wingId); return; }
        const copy = event.target.closest('[data-copy]');
        if (copy) { navigator.clipboard?.writeText(copy.dataset.copy).then(() => { copy.textContent = 'Copied'; }, () => { copy.textContent = 'Copy failed'; }); return; }
        if (browserUnavailable(S.currentUser)) return;
        if (event.target.closest('[data-new-session]')) { location.hash = '#new'; return; }
        const choose = event.target.closest('[data-choose-project]');
        if (choose) { showPalette(choose.dataset.chooseProject); return; }
        const refresh = event.target.closest('[data-refresh-agents]');
        if (refresh) { const w = wingOf(refresh.dataset.refreshAgents); if (w) { refresh.disabled = true; probeWing(w).finally(() => { refresh.disabled = false; render(true); }); } return; }
        const retry = event.target.closest('[data-retry]');
        if (retry) { retry.disabled = true; retry.textContent = 'Checking…'; loadHome().finally(() => render(true)); }
    });
    root.addEventListener('change', event => {
        if (event.target.id === 'pp-launch-machine') { launchWingId = event.target.value; location.hash = '#new/' + encodeURIComponent(launchWingId); render(true); }
    });
    root.addEventListener('input', event => {
        if (event.target.id !== 'pp-filter') return;
        const q = event.target.value.trim().toLowerCase();
        root.querySelectorAll('.pp-srow').forEach(row => { row.hidden = q && !row.textContent.toLowerCase().includes(q); });
    });
    root.addEventListener('submit', event => {
        const form = event.target.closest('[data-composer]'); if (!form) return;
        event.preventDefault();
        const [wingId, sessionId] = form.dataset.composer.split('/'), text = form.querySelector('textarea').value;
        // Delivery and its confirmation stay in the conversation view.
        show(false);
        if (showSessionChat(sessionId, wingId)) { const input = document.getElementById('chat-input'); if (input) { input.value = text; input.focus(); } }
    });
}

function show(on) {
    if (!root) return;
    root.hidden = !on;
    document.body.classList.toggle('paper-on', on);
}

// Text ranges, rather than row bounds, exclude the empty space beneath short
// reports in the desktop identity column. Merge inline-code fragments by line.
function reportLines(row) {
    const range = document.createRange();
    const say = row.querySelector('.pp-say');
    range.selectNodeContents(say);
    const height = parseFloat(getComputedStyle(say).lineHeight);
    const lines = [];
    for (const rect of range.getClientRects()) {
        if (!rect.width || !rect.height) continue;
        const top = rect.top - Math.max(0, height - rect.height) / 2;
        if (!lines.some(line => Math.abs(line.top - top) < 2)) lines.push({ top, bottom: top + height });
    }
    return lines.sort((a, b) => a.top - b.top);
}

// Only automatic following adjusts to a readable boundary. Extra end space
// lets us advance past an empty remnant without cutting off the latest update.
function followThread(conversation) {
    conversation.style.paddingBottom = '';
    conversation.scrollTop = conversation.scrollHeight;
    const top = conversation.getBoundingClientRect().top;
    const boundaries = [];
    for (const row of conversation.querySelectorAll('[data-source]')) {
        const start = row.getBoundingClientRect().top;
        const identityEnd = row.querySelector('.pp-who').getBoundingClientRect().bottom;
        if (start >= top - 1) boundaries.push(start);
        for (const line of reportLines(row)) {
            if (line.top >= top - 1 && line.top >= identityEnd) boundaries.push(line.top);
        }
    }
    const boundary = Math.min(...boundaries);
    if (!Number.isFinite(boundary)) return;
    const advance = Math.max(0, boundary - top);
    const position = conversation.scrollTop;
    conversation.style.paddingBottom = Math.ceil(advance) + 'px';
    conversation.scrollTop = position + advance;
}

// Attribution stays outside the transcript and belongs to the first complete
// visible body line, never to an empty row remnant. Identities remain readable.
function updateThreadSource() {
    const label = main.querySelector('[data-thread-source]');
    if (!label) return;
    const bounds = main.querySelector('.pp-conversation').getBoundingClientRect();
    let source = '';
    for (const row of main.querySelectorAll('.pp-conversation [data-source]')) {
        if (!reportLines(row).some(line => line.top >= bounds.top - 1 && line.bottom <= bounds.bottom + 1)) continue;
        const identity = row.querySelector('.pp-who').getBoundingClientRect();
        if (identity.top < bounds.top - 1) source = row.dataset.source;
        break;
    }
    label.textContent = source ? source + ' · continued' : 'Today';
    label.toggleAttribute('data-continued', !!source);
}

export function render(force) {
    const route = paperRoute();
    const on = !!route && S.activeView === 'home';
    show(on);
    if (!on) return;
    const views = { wing: viewWing, sessions: viewSessions, machines: viewMachines, pair: viewPair, new: viewNew, session: viewSession, approve: viewApprove };
    const denied = browserUnavailable(S.currentUser);
    const html = denied ? head('Get started', 'Free native access') + directSetup(S.currentUser) : views[route.view](route);
    const sig = location.hash + html;
    if (sig === lastSignature) return;
    lastSignature = sig;
    const draft = root.querySelector('#pp-message')?.value, filter = root.querySelector('#pp-filter')?.value, focused = document.activeElement && document.activeElement.id;
    const fresh = main.dataset.hash !== location.hash;
    const conversation = main.querySelector('.pp-conversation');
    const position = conversation?.scrollTop || 0;
    const endSpace = conversation?.style.paddingBottom || '';
    const pinned = fresh || !conversation || conversation.scrollHeight - position - conversation.clientHeight <= 2;
    const terminalOpen = main.querySelector('.pp-terminal')?.open;
    main.innerHTML = html;
    main.dataset.view = route.view; main.dataset.hash = location.hash;
    // Only the transcript follows new output; identity and controls stay in view.
    const nextTerminal = main.querySelector('.pp-terminal');
    if (!fresh && nextTerminal && terminalOpen !== undefined) nextTerminal.open = terminalOpen;
    const nextConversation = main.querySelector('.pp-conversation');
    if (nextConversation) {
        if (pinned && route.view === 'wing') followThread(nextConversation);
        else {
            nextConversation.style.paddingBottom = pinned ? '' : endSpace;
            nextConversation.scrollTop = pinned ? nextConversation.scrollHeight : position;
        }
        nextConversation.addEventListener('scroll', updateThreadSource, { passive: true });
        updateThreadSource();
    }
    const nv = denied ? '<a href="#" aria-current="page">Get started</a><a href="#account">Account</a>' : nav(route);
    side.querySelector('.pp-nav').innerHTML = nv;
    root.querySelector('.pp-tabs').innerHTML = nv;
    side.querySelector('.pp-date').textContent = new Date().toLocaleDateString([], { weekday: 'long', day: 'numeric', month: 'long' });
    side.querySelector('.pp-side-squares').innerHTML = denied ? '' : squares() + machineOverflow();
    side.querySelector('.pp-foot').textContent = S.currentUser && S.currentUser.email || '';
    if (draft) root.querySelector('#pp-message') && (root.querySelector('#pp-message').value = draft);
    if (filter) { const f = root.querySelector('#pp-filter'); if (f) { f.value = filter; f.dispatchEvent(new Event('input', { bubbles: true })); } }
    if (focused && (focused === 'pp-message' || focused === 'pp-filter')) root.querySelector('#' + focused)?.focus({ preventScroll: true });
}

export function initPaper() {
    if (root) return;
    frame();
    window.addEventListener('resize', () => { render(true); updateThreadSource(); });
    window.addEventListener('hashchange', () => { lastSignature = ''; if (isPaperRoute() && S.activeView !== 'home') showHome(false); render(true); window.scrollTo(0, 0); });
    window.addEventListener('popstate', () => setTimeout(() => render(true), 0));
    setInterval(() => render(false), 1000);
    render(true);
}
