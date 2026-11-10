import { S } from '../../state.js';
import { escapeMarkup as esc } from '../../security.js';
import { sessionDisplayName } from '../../helpers.js';
import { agentName, machineState, requestedCommand, sessionState as reportedState } from '../../paper-state.js';
import { browserUnavailable } from '../../onboarding.js';
import { loadHome } from '../../data.js';
import { head, render, paperRoute } from '../frame.js';
import { wingOf, machineName, sessions, workSessions, coordinator, sessionState, needing, machines } from '../select.js';
import { lines, request, composer, sessionHref, chip, lastSeen, ui } from '../common.js';
import '../home.css';

const install = 'curl -fsSL https://wingthing.ai/install.sh | sh\nwt login\nwt start';
let firstRunPoll;
// The frame calls this for every route, including policy and inventory changes.
// The ordinary inventory poll remains responsible after onboarding.
export function syncHomePolling(active) {
    const wanted = active && !!S.currentUser && !browserUnavailable(S.currentUser) && !sessions().length;
    if (!wanted && firstRunPoll) { clearInterval(firstRunPoll); firstRunPoll = undefined; }
    if (wanted && !firstRunPoll) firstRunPoll = setInterval(() => {
        loadHome().finally(() => render(true));
    }, 3000);
}

function connection() {
    const inventory = machines(), connected = inventory.filter(w => machineState(w) === 'connected');
    if (!inventory.length) return 'Waiting for your first machine…';
    if (inventory.every(w => machineState(w) === 'offline')) return 'No machines are connected. Sessions are kept.';
    if (connected.length === inventory.length) return inventory.length === 1 ? machineName(connected[0]) + ' is connected.' : 'All machines connected.';
    const away = inventory.find(w => machineState(w) === 'offline');
    if (away) return machineName(away) + ' is offline' + (lastSeen(away) ? ', last seen ' + lastSeen(away) : '') + '.';
    const unavailable = inventory.find(w => machineState(w) !== 'connected');
    return machineName(unavailable) + (machineState(unavailable) === 'locked' ? ' is locked.' : ' is connecting…');
}
function homeHead(meta = '') {
    const offline = machines().length && machines().every(w => machineState(w) === 'offline');
    const retry = offline ? '<button type="button" class="pp-btn" data-retry' + (ui.retrying ? ' disabled' : '') + '>' + (ui.retrying ? 'Checking…' : 'Retry') + '</button>' : '';
    return head('Home', meta, esc(connection()) + retry);
}
function step(number, title, body, done, active) {
    return '<li class="home-step' + (active ? ' home-step-active' : '') + '" data-k="step-' + number + '"><span class="home-number' + (done ? ' home-tick' : '') + '" aria-label="' + (done ? 'Complete' : 'Step ' + number) + '">' + (done ? '✓' : number) + '</span><div><h2 data-step-title="' + number + '">' + esc(title) + '</h2>' + body + '</div></li>';
}
export function viewEmpty() {
    const connected = machines().find(w => machineState(w) === 'connected');
    const person = S.currentUser?.display_name;
    const view = paperRoute().view;
    const header = view === 'wing' ? homeHead() : head(({ sessions: 'Sessions', machines: 'Machines', new: 'New session' })[view] || 'Home', '');
    return header + '<ol class="home-steps" aria-label="Get started">' +
        step(1, 'Signed in', '<p>' + (person ? 'You’re in as ' + esc(person) + '.' : 'You’re signed in.') + '</p>', true, false) +
        step(2, connected ? machineName(connected) + ' connected' : 'Connect a machine', '<p>Run this on the computer where your code and your agent’s login live. It appears here by itself.</p><pre class="home-commands">' + install.split('\n').map(command => '<span><i aria-hidden="true">$ </i>' + esc(command) + '</span>').join('') + '</pre><button type="button" class="pp-btn" data-copy="' + esc(install) + '">Copy</button>', !!connected, !connected) +
        step(3, 'Start a session', '<p>Pick a project folder and an agent. You’ll watch it here and on your phone.</p>' + (connected ? '<a class="pp-btn pp-btn-ink" href="#new/' + encodeURIComponent(connected.wing_id) + '">Start a session</a>' : ''), false, !!connected) + '</ol>';
}
export function viewBrowserAccess() {
    const commands = ['codex mcp add wingthing -- wt mcp connect --client codex', 'claude mcp add --scope user wingthing -- wt mcp connect --client claude'];
    return head('Home', '', 'Your agents can connect from a terminal.') + '<section class="home-access"><h2>Use wingthing with your agent</h2><p>Your account includes native agent access. Connect Claude or Codex on your machines. Browser control needs a plan with browser access.</p>' + commands.map(command => '<pre class="home-commands">' + esc(command) + '</pre>').join('') + '<a class="pp-btn pp-btn-ink" href="#account">See plans</a></section>';
}
// Preserve a group's surviving order across data refreshes; new members append.
const orders = new Map();
function ordered(key, rows) {
    const ids = new Map(rows.map(s => [s.wing_id + '/' + s.id, s]));
    const order = (orders.get(key) || []).filter(id => ids.has(id));
    for (const id of ids.keys()) if (!order.includes(id)) order.push(id);
    orders.set(key, order);
    return order.map(id => ids.get(id));
}
function section(title, rows, limit, draw) {
    if (!rows.length) return '';
    const key = title.toLowerCase().replaceAll(' ', '-');
    const needs = title === 'Needs you';
    return '<section class="home-section" data-k="' + key + '" aria-labelledby="home-' + key + '"><h2 id="home-' + key + '" data-stable>' + title + '<small aria-hidden="true">' + rows.length + '</small></h2><div class="home-list">' + rows.slice(0, limit).map(draw).join('') + '</div>' + (rows.length > limit ? '<a class="home-more pp-more" href="' + (needs ? '#sessions?state=need' : '#sessions') + '"' + (needs ? ' data-home-needs' : '') + '>+' + (rows.length - limit) + ' more</a>' : '') + '</section>';
}
function needCard(s) {
    const r = request(s), command = r.command || requestedCommand(s.lifecycle?.reason);
    return '<article class="home-need" data-k="' + esc(s.wing_id + '/' + s.id) + '"><div><h3 title="' + esc(sessionDisplayName(s)) + '">' + esc(sessionDisplayName(s)) + '</h3><p>' + (command ? 'Wants to run a command on ' : 'Wants an answer on ') + esc(machineName(wingOf(s.wing_id))) + '.</p>' + '<pre class="home-commands" aria-hidden="' + !command + '" title="' + esc(command || '') + '">' + (command ? '<i>$ </i>' + esc(command) : '') + '</pre>' + '</div><button type="button" class="pp-btn pp-btn-ink" data-takeover="' + esc(s.wing_id + '/' + s.id) + '">Answer</button></article>';
}
function relative(at) {
    const ms = Date.parse(at);
    if (!Number.isFinite(ms)) return '';
    const minutes = Math.max(0, Math.floor((Date.now() - ms) / 60000));
    return minutes < 1 ? 'Now' : minutes < 60 ? minutes + 'm' : minutes < 1440 ? Math.floor(minutes / 60) + 'h' : Math.floor(minutes / 1440) + 'd';
}
function row(s) {
    const st = sessionState(s), last = lines(s).filter(l => l.who === 'agent').pop();
    return '<a class="home-row' + (st.key === 'offline' ? ' home-offline' : '') + '" data-k="' + esc(s.wing_id + '/' + s.id) + '" data-stable href="' + sessionHref(s) + '">' + chip(st) + '<span class="home-identity"><b>' + esc(sessionDisplayName(s)) + '</b><small>' + esc([agentName(s.agent), machineName(wingOf(s.wing_id))].filter(Boolean).join(' · ')) + '</small></span><span class="home-line">' + esc(last?.text || '') + '</span><time>' + esc(relative(last?.at)) + '</time></a>';
}
export function viewWing() {
    if (browserUnavailable(S.currentUser)) return viewBrowserAccess();
    if (!sessions().length) return viewEmpty();
    const all = workSessions(), coord = coordinator();
    const need = ordered('need', needing().filter(s => sessionState(s).key === 'need'));
    const work = ordered('work', all.filter(s => {
        const key = sessionState(s).key;
        return key === 'work' || key === 'offline' && ['work', 'need'].includes(reportedState(s, { online: true }).key);
    }));
    const recent = ordered('recent', all.filter(s => !need.includes(s) && !work.includes(s)));
    const workingCount = work.filter(s => sessionState(s).key === 'work').length;
    const meta = [workingCount ? workingCount + ' working' : '', need.length ? need.length + ' needs you' : ''].filter(Boolean).join(' · ');
    return homeHead(esc(meta)) + '<div class="home-columns"><div class="home-work">' + section('Needs you', need, 3, needCard) + section('Working', work, 8, row) + section('Recent', recent, 5, row) + '</div><aside class="home-rail" data-k="wing" data-stable><h2>' + (coord ? 'Message wing' : 'Your wing') + '</h2>' + (coord ? '<p>Your wing on ' + esc(machineName(wingOf(coord.wing_id))) + ' summarises the others and can start work for you.</p>' + composer('Message wing', coord) : '<p>A wing is an agent that watches the others and summarises here.</p><a href="/docs#wing-setup">Set one up</a>') + '</aside></div>';
}
