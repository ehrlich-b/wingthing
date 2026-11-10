import { S, DOM } from '../state.js';
import { escapeMarkup as esc } from '../security.js';
import { sessionDisplayName } from '../helpers.js';
import { machineState } from '../paper-state.js';
import { browserUnavailable, directSetup } from '../onboarding.js';
import '../tokens.css';
import '../paper.css';
import './frame.css';
import { paperRoute as route } from '../paper-state.js';
import { resolveShellRoute } from '../session-route.js';
import { patchRegion } from './patch.js';
import { viewAccount } from './views/account.js';
import { viewMachine } from './views/machine.js';
import { viewCanvas } from './views/canvas.js';
import { switchToSession, showHome, showCanvas, navigateToWingDetail, navigateToAccount } from '../nav.js';
import { showPalette } from '../palette.js';
import { loadHome, probeWing } from '../data.js';
import { wingOf, machineName, sessions, coordinator, workSessions, needing, findSession, machines, machineCounts } from './select.js';
import { inputAllowed, selectComposer, squares, lines, terminal, composer, project, ui, drafts } from './common.js';
import { viewWing, viewBrowserAccess, syncHomePolling } from './views/home.js';
import { viewNew } from './views/new.js';
import { viewSessions } from './views/sessions.js';
import { viewMachines } from './views/machines.js';
import { viewSession } from './views/session.js';
import { viewPair } from './views/pair.js';
let root, main, side, lastSignature = '', routing = false;
const homes = new Map();
function paperRoute(hash = location.hash) { return route(hash); }
function isPaperRoute(hash) { return !!paperRoute(hash); }
function head(title, meta, strip) {
    const current = paperRoute();
    const back = current.view === 'session' ? '<a href="#sessions">← Sessions</a>' : current.view === 'machine' ? '<a href="#machines">← Machines</a>' : '';
    const counts = machineCounts();
    const status = !machines().length ? 'Waiting for your first machine…' : counts.connected === machines().length ? 'All machines connected.' : counts.offline ? counts.offline + ' offline. Sessions are kept.' : 'Checking machines…';
    return '<header class="pp-hdr" data-stable><h1>' + title + '</h1><div class="pp-meta">' + meta + '</div></header><div class="pp-strip" data-stable role="status">' + (strip ?? (back || status)) + '</div>';
}
function nav(route) {
    if (browserUnavailable(S.currentUser)) return '<a href="#"' + (route.view !== 'account' ? ' aria-current="page"' : '') + '>Get started</a><a href="#account"' + (route.view === 'account' ? ' aria-current="page"' : '') + '>Account</a>';
    const need = needing().length, here = ['wing', 'approve', 'decisions'].includes(route.view) ? 'wing' : ['machines', 'machine', 'pair'].includes(route.view) ? 'machines' : route.view === 'account' ? 'account' : 'sessions';
    const item = (key, label, href, count, hot) => '<a href="' + href + '" class="pp-nav-' + key + '"' + (here === key ? ' aria-current="page"' : '') + '><span>' + label + '</span><small' + (hot ? ' class="pp-hot"' : '') + '>' + count + '</small></a>';
    return item('wing', 'Home', '#', need ? need + ' needs you' : '', !!need) + item('sessions', 'Sessions', '#sessions', workSessions().length || '', false) + item('machines', 'Machines', '#machines', machines().length || '', false) + item('account', 'Account', '#account', '', false);
}
function frame() {
    root = document.createElement('div');
    root.id = 'paper';
    root.innerHTML = '<aside class="pp-side"><a class="pp-brand" href="#">wing<i>thing</i></a><a class="pp-btn pp-btn-ink pp-new" href="#new">New session</a><nav class="pp-nav" aria-label="Main"></nav><div class="pp-side-squares pp-squares" aria-label="Machines"></div><a class="pp-foot" href="#account"></a></aside><main class="pp-main"></main><nav class="pp-tabs" aria-label="Main"></nav>';
    document.body.prepend(root);
    main = root.querySelector('.pp-main'); side = root.querySelector('.pp-side');
    for (const node of [DOM.accountSection, DOM.wingDetailSection, DOM.terminalSection, DOM.canvasSection]) {
        const anchor = document.createComment('Shell engine mount'); node.before(anchor); homes.set(node, anchor);
    }
    root.addEventListener('click', event => {
        if (event.target.closest('[data-home-needs]')) window.addEventListener('hashchange', () => {
            const input = root.querySelector('#pp-filter');
            if (input) { input.value = 'Needs you'; updateSessionSearch(); }
        }, { once: true });
        if (event.target.closest('a[href="#needs"]') && location.hash === '#needs') main.querySelector('#pp-needs')?.focus({ preventScroll: true });
        if (event.target.closest('[data-clear-search]')) {
            const input = root.querySelector('#pp-filter');
            if (input) { input.value = ''; updateSessionSearch(); input.focus({ preventScroll: true }); }
            return;
        }
        if (event.target.closest('[data-check-receipt]')) {
            if (ui.inputReader && ui.inputSnapshot?.pending && !event.target.disabled) ui.inputReader.checkReceipt(ui.inputSnapshot.pending.request_id);
            return;
        }
        if (event.target.closest('[data-more-machines]')) { ui.allMachines = true; render(true); return; }
        const take = event.target.closest('[data-takeover]');
        if (take) { const [wingId, sessionId] = take.dataset.takeover.split('/'); if (machineState(wingOf(wingId)) !== 'connected') return; switchToSession(sessionId, true, wingId); S.term?.focus(); return; }
        const copy = event.target.closest('[data-copy]');
        if (copy) { navigator.clipboard?.writeText(copy.dataset.copy).then(() => { copy.textContent = 'Copied'; }, () => { copy.textContent = 'Copy failed'; }); return; }
        if (browserUnavailable(S.currentUser)) return;
        if (event.target.closest('[data-add-passkey]')) { navigateToAccount(true); return; }
        const unlock = event.target.closest('[data-unlock-machine]');
        if (unlock) { const w = wingOf(unlock.dataset.unlockMachine); if (w) { unlock.disabled = true; probeWing(w, { authenticate: true }).finally(() => { unlock.disabled = false; render(true); }); } return; }
        if (event.target.closest('[data-new-session]')) { location.hash = '#new'; return; }
        const choose = event.target.closest('[data-choose-project]');
        if (choose) { showPalette(choose.dataset.chooseProject); return; }
        const refresh = event.target.closest('[data-refresh-agents]');
        if (refresh) { const w = wingOf(refresh.dataset.refreshAgents); if (w) { refresh.disabled = true; probeWing(w).finally(() => { refresh.disabled = false; render(true); }); } return; }
        const retry = event.target.closest('[data-retry]');
        if (retry && !ui.retrying) { ui.retrying = true; render(true); loadHome().finally(() => { ui.retrying = false; render(true); }); }
    });
    root.addEventListener('change', event => {
        if (event.target.id === 'pp-launch-machine') { ui.launchWingId = event.target.value; location.hash = '#new/' + encodeURIComponent(ui.launchWingId); render(true); }
    });
    root.addEventListener('input', event => {
        if (event.target.id === 'pp-message') {
            drafts.set(ui.inputKey, event.target.value);
            event.target.form.querySelector('[type="submit"]').disabled = !ui.inputSnapshot?.inputReady || !event.target.value.trim();
            return;
        }
        if (event.target.id !== 'pp-filter') return;
        updateSessionSearch();
    });
    root.addEventListener('submit', event => {
        const form = event.target.closest('[data-composer]'); if (!form) return;
        event.preventDefault();
        const s = sessions().find(s => s.wing_id + '/' + s.id === form.dataset.composer);
        if (!ui.inputReader || !inputAllowed(s) || !ui.inputSnapshot?.inputReady || ui.inputSnapshot.continuationReady) return;
        const text = form.querySelector('textarea').value;
        if (!text.trim()) return;
        ui.inputReader.send(text);
        if (ui.inputSnapshot?.pending?.input === text) drafts.set(ui.inputKey, '');
        render(true);
    });
}
function updateSessionSearch() {
    const input = root.querySelector('#pp-filter');
    if (!input) return;
    const query = input.value.trim().toLowerCase();
    let visible = 0;
    for (const group of root.querySelectorAll('.pp-group')) {
        const rows = [...group.querySelectorAll('.pp-srow')];
        const count = rows.reduce((total, row) => {
            const matches = !query || row.textContent.toLowerCase().includes(query);
            row.hidden = !matches;
            return total + Number(matches);
        }, 0);
        const overflow = group.querySelector('.pp-more-sessions');
        if (overflow) {
            const more = [...overflow.querySelectorAll('.pp-srow')].filter(row => !row.hidden).length;
            overflow.hidden = !!query && more === 0;
            if (query) overflow.open = true;
            overflow.querySelector('summary').textContent = '+' + more + ' more';
        }
        visible += count;
        const countLabel = group.querySelector('[data-group-count]');
        if (countLabel) countLabel.textContent = String(count);
        group.hidden = !!query && count === 0;
    }
    const empty = root.querySelector('[data-search-empty]');
    if (empty) {
        empty.hidden = !query || visible > 0;
        const message = empty.querySelector('[data-search-message]');
        if (message) message.textContent = 'No sessions match “' + input.value.trim() + '”.';
    }
    const summary = root.querySelector('[data-session-summary]');
    if (summary) summary.textContent = query ? visible + (visible === 1 ? ' match' : ' matches') : visible + (visible === 1 ? ' session' : ' sessions');
}
function show() {
    if (!root) return;
    root.hidden = false;
    document.body.classList.add('paper-on');
}

function mountEngines(view, live) {
    const nodes = new Map([['account', DOM.accountSection], ['machine', DOM.wingDetailSection], ['canvas', DOM.canvasSection], ['terminal', DOM.terminalSection]]);
    for (const [name, node] of nodes) {
        const slot = main.querySelector('[data-engine="' + name + '"]');
        if (slot && (name !== 'terminal' || live)) {
            if (node.parentNode !== slot) slot.append(node);
            node.style.display = '';
        } else if (homes.get(node)) {
            if (node.previousSibling !== homes.get(node)) homes.get(node).after(node);
            node.style.display = 'none';
        }
    }
    if (live) requestAnimationFrame(() => S.fitAddon?.fit());
}

export function routeShell() {
    if (routing || !S.currentUser || !root) return;
    routing = true;
    try {
        const resolved = resolveShellRoute(location.hash, sessions());
        if (location.hash !== resolved.hash) history.replaceState({ focusNeeds: resolved.focusNeeds }, '', resolved.hash);
        const current = resolved.route;
        if (current.view === 'account') {
            if (S.activeView !== 'account' || S.accountExpandSlug !== current.orgSlug) navigateToAccount(false, current.orgSlug);
        } else if (current.view === 'machine') {
            if (S.activeView !== 'wing-detail' || S.currentWingId !== current.wingId) navigateToWingDetail(current.wingId, false);
        } else if (current.view === 'canvas') {
            if (S.activeView !== 'canvas') showCanvas(false);
        } else if (!(current.view === 'session' && S.activeView === 'terminal' && S.ptySessionId === current.sessionId && S.ptyWingId === current.wingId)) {
            if (S.activeView !== 'home') showHome(false);
        }
        render(true);
        if (resolved.focusNeeds) main.querySelector('.pp-need')?.focus({ preventScroll: true });
    } finally { routing = false; }
}

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
function render(force) {
    if (!root) return;
    show();
    const current = paperRoute();
    const denied = browserUnavailable(S.currentUser);
    const live = !denied && S.activeView === 'terminal';
    syncHomePolling(current.view === 'wing' && !denied);
    selectComposer(!denied ? (current.view === 'wing' ? coordinator() : current.view === 'session' && !live ? findSession(current) : null) : null);
    const views = { wing: viewWing, sessions: viewSessions, machines: viewMachines, pair: viewPair, new: viewNew, session: viewSession, account: viewAccount, machine: viewMachine, canvas: viewCanvas };
    const html = denied && current.view !== 'account' ? (current.view === 'wing' ? viewBrowserAccess() : head('Get started', 'Free native access') + directSetup(S.currentUser)) : live ? head(esc(sessionDisplayName(findSession(current) || {}) || 'Session'), esc(machineName(wingOf(S.ptyWingId)))) + '<div class="pp-live" data-engine="terminal" data-stable></div>' : views[current.view](current);
    const sig = location.hash + S.activeView + html;
    renderNavigation(current, denied);
    if (sig === lastSignature) return;
    lastSignature = sig;
    const fresh = main.dataset.hash !== location.hash || main.dataset.mode !== String(live);
    if (fresh) main.scrollTop = 0;
    const conversation = main.querySelector('.pp-conversation');
    const pinned = fresh || !conversation || conversation.scrollHeight - conversation.scrollTop - conversation.clientHeight <= 2;
    const position = conversation?.scrollTop || 0;
    if (fresh) {
        for (const [node, anchor] of homes) if (main.contains(node)) anchor.after(node);
        const template = document.createElement('template'); template.innerHTML = html;
        main.replaceChildren(template.content);
    } else patchRegion(main, html);
    main.dataset.view = current.view; main.dataset.hash = location.hash; main.dataset.mode = String(live);
    mountEngines(current.view, live);
    const nextConversation = main.querySelector('.pp-conversation');
    if (nextConversation) {
        if (pinned && current.view === 'wing') followThread(nextConversation);
        else nextConversation.scrollTop = pinned ? nextConversation.scrollHeight : position;
        nextConversation.onscroll = updateThreadSource;
        updateThreadSource();
    }
    if (history.state?.focusNeeds && !main.dataset.focusedNeeds) {
        const card = main.querySelector('.pp-need');
        if (card) { card.focus({ preventScroll: true }); main.dataset.focusedNeeds = location.hash; }
    }
    if (!history.state?.focusNeeds) delete main.dataset.focusedNeeds;
    const input = root.querySelector('#pp-message');
    if (input && document.activeElement !== input) input.value = drafts.get(ui.inputKey) || '';
    if (current.view === 'sessions') updateSessionSearch();
}
function renderNavigation(route, denied) {
    const nv = denied ? '<a href="#" aria-current="page">Get started</a><a href="#account">Account</a>' : nav(route);
    patchRegion(side.querySelector('.pp-nav'), nv);
    patchRegion(root.querySelector('.pp-tabs'), denied ? nv : nv.replace(/<a href="#account"[\s\S]*?<\/a>$/, '') + '<a href="#new"' + (route.view === 'new' ? ' aria-current="page"' : '') + '>New</a>');
    patchRegion(side.querySelector('.pp-side-squares'), denied ? '' : squares());
    side.querySelector('.pp-foot').textContent = S.currentUser?.display_name || 'Account';
}
function initPaper() {
    if (root) return;
    frame();
    ui.invalidate = () => render(true);
    window.addEventListener('account-rendered', () => render(true));
    window.addEventListener('shell-navigate', () => render(true));
    window.addEventListener('resize', () => { render(true); updateThreadSource(); });
    window.addEventListener('hashchange', () => { ui.allMachines = false; routeShell(); });
    // Data reads and relative-time text updates patch existing nodes.
    setInterval(() => render(false), 1000);
    render(true);
}
export { paperRoute, isPaperRoute, head, render, initPaper };
