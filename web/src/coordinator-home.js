import { coordinatorThreadMarkup } from './coordinator-thread.js';
import { searchRoute } from './session-search-state.js';
import { openSessionViewer } from './session-viewer.js';
import { S, DOM } from './state.js';
import { wingDisplayName } from './helpers.js';
import { escapeMarkup } from './security.js';
import { sendTunnelRequest } from './tunnel.js';
import { openConversationReference } from './conversation-view.js';
import { showSessionChat } from './nav.js';
import { applyConversationRead, restoredConversationState } from './conversation-state.js';
import { pinnedCoordinators, coordinatorHomeSummary } from './coordinator-home-state.js';
import { activityTime, reportChips, timelineMarkup } from './session-activity.js';

const cache = new Map();
const inflight = new Set();
let selected = '';
let selectionUser = '';
let initialized = false;
let fleet = null, fleetOwner = null;

function key(session) { return JSON.stringify([S.currentUser && S.currentUser.id, session.wing_id, session.id]); }
function storageKey(session) { return 'wt_coordinator_home_v1:' + key(session); }
export function setCoordinatorFleet(inventory) {
    fleet = inventory; fleetOwner = S.currentUser?.id;
    renderCoordinatorHome();
}
function candidates() {
    const native = pinnedCoordinators(S.sessionsData, S.wingsData, S.currentUser);
    if (!S.currentUser || S.currentUser.relay_allowed === false || fleetOwner !== S.currentUser.id) return native;
    const rows = new Map(native.map(row => [JSON.stringify([row.wing_id, row.id]), row]));
    for (const row of fleet?.sessions || []) {
        if (!row.pinned || row.role !== 'coordinator' || !row.origin_wing_id || !row.session_id) continue;
        const id = JSON.stringify([row.origin_wing_id, row.session_id]);
        rows.set(id, { ...rows.get(id), ...row, id: row.session_id, wing_id: row.origin_wing_id, fleetRow: row });
    }
    return [...rows.values()];
}
function wingFor(session) {
    const route = session.fleetRow && searchRoute(session.fleetRow, S.wingsData);
    return S.wingsData.find(wing => wing.wing_id === (route?.wing_id || session.wing_id));
}
function machineName(session) { return session.machine_label || wingDisplayName(wingFor(session)) || session.wing_id; }
function selectionKey() { return 'wt_coordinator_selection_v1:' + JSON.stringify(S.currentUser && S.currentUser.id); }

function choose() {
    const user = S.currentUser && S.currentUser.id;
    if (selectionUser !== user) {
        selectionUser = user;
        try { selected = localStorage.getItem(selectionKey()) || ''; } catch { selected = ''; }
    }
    const sessions = candidates();
    return sessions.find(session => key(session) === selected) || sessions[0];
}

function evidenceFor(session) {
    const resource = key(session);
    if (!cache.has(resource)) {
        let saved = null;
        try { saved = JSON.parse(localStorage.getItem(storageKey(session)) || 'null'); } catch { /* Unknown until a read succeeds. */ }
        const record = saved && saved.sessionId === session.id && saved.wingId === session.wing_id ? saved : null;
        cache.set(resource, { ...restoredConversationState(record), observedAt: 0, cached: !!record, readError: '' });
        while (cache.size > 8) cache.delete(cache.keys().next().value);
    }
    return cache.get(resource);
}

function storeEvidence(session, evidence) {
    evidence = { ...evidence, messages: evidence.messages.slice(-200) };
    evidence.seen = new Set(evidence.messages.map(message => message.sequence));
    cache.set(key(session), evidence);
    const record = { sessionId: session.id, wingId: session.wing_id, lifecycle: evidence.lifecycle, cursor: evidence.cursor, messages: evidence.messages.slice(-200) };
    try {
        let encoded = JSON.stringify(record);
        while (encoded.length > 64000 && record.messages.length) { record.messages.shift(); encoded = JSON.stringify(record); }
        if (encoded.length <= 64000) localStorage.setItem(storageKey(session), encoded);
    } catch { /* Current evidence remains visible if storage is unavailable. */ }
}

async function readHome(session) {
    const resource = key(session), wing = wingFor(session), previous = evidenceFor(session);
    if (!wing || wing.online !== true || wing.tunnel_error || inflight.has(resource) || (previous.observedAt && Date.now() - previous.observedAt < 4500)) return;
    inflight.add(resource);
    const user = S.currentUser && S.currentUser.id;
    const issuedAt = Date.now();
    try {
        let result;
        if (session.fleetRow) {
            const route = searchRoute(session.fleetRow, S.wingsData);
            if (!route) throw Error('No current route to coordinator');
            const response = await sendTunnelRequest(route.wing_id, { type: 'session.control', operation: 'session_action', arguments: {
                origin_wing_id: session.wing_id, session_id: session.id, operation: 'session_read', arguments: { after_cursor: previous.cursor || 0, limit: 100 },
            } });
            if (response.origin_wing_id !== session.wing_id || response.session_id !== session.id || response.route?.wing_id !== route.wing_id) throw Error('Coordinator route identity mismatch');
            result = response.result;
        } else result = await sendTunnelRequest(session.wing_id, { type: 'session.control', operation: 'session_read', arguments: { session: session.id, after_cursor: previous.cursor || 0, limit: 100 } });
        if (user !== (S.currentUser && S.currentUser.id) || !candidates().some(candidate => key(candidate) === resource)) return;
        if (!result.lifecycle || result.lifecycle.session_id !== session.id || (result.session && result.session !== session.id)) throw Error('Session identity did not match the coordinator');
        const next = applyConversationRead(previous, result.lifecycle, { expected: { sessionId: session.id, providerSessionId: previous.lifecycle && previous.lifecycle.provider_session_id } });
        if (next.identityError) throw Error(next.identityError);
        storeEvidence(session, { ...next, observedAt: issuedAt, cached: false, readError: '' });
    } catch (error) {
        if (user === (S.currentUser && S.currentUser.id)) cache.set(resource, { ...previous, observedAt: 0, readError: error.message || 'Could not read coordinator' });
    } finally { inflight.delete(resource); renderCoordinatorHome(false); }
}

async function open(session) {
    if (session.fleetRow) return openSessionViewer(session.fleetRow);
    if (session.conversation_id) return openConversationReference({ wingId: session.wing_id, conversationId: session.conversation_id });
    return showSessionChat(session.id, session.wing_id);
}

function bindOpen(button, session) {
    button.addEventListener('click', async () => {
        selected = key(session);
        try { localStorage.setItem(selectionKey(), selected); } catch { /* Selection still works this visit. */ }
        button.disabled = true;
        try { await open(session); }
        catch (error) { const status = document.getElementById('coordinator-home-error'); if (status) status.textContent = error.message || 'Could not open coordinator'; }
        finally { button.disabled = false; }
    });
}

export function renderCoordinatorHome(read = true) {
    const mount = document.getElementById('coordinator-home'), nav = document.getElementById('coordinator-nav');
    if (!mount || !nav) return;
    const sessions = candidates(), session = choose();
    mount.parentElement.classList.toggle('has-pinned-coordinator', Boolean(session));
    if (session && mount.parentElement.firstElementChild !== mount) mount.parentElement.prepend(mount);
    const search=document.querySelector('.session-search');if(session&&search&&mount.nextElementSibling!==search)mount.insertAdjacentElement('afterend',search);
    const otherCoordinators = document.getElementById('conversation-inventory');
    if (session && otherCoordinators && otherCoordinators.parentElement === mount.parentElement) mount.parentElement.append(otherCoordinators);
    nav.hidden = !sessions.length;
    const navSignature = JSON.stringify(sessions.map(item => [key(item), item.name]));
    if (nav.dataset.signature !== navSignature) {
        nav.dataset.signature = navSignature;
        nav.innerHTML = sessions.map((item, index) => '<button type="button" class="coordinator-nav-thread" data-pin-index="' + index + '" title="Open ' + escapeMarkup(item.name || 'wing') + ' on ' + escapeMarkup(wingDisplayName(wingFor(item)) || item.wing_id) + '"><span class="coordinator-avatar" aria-hidden="true">w</span><span class="coordinator-nav-label">' + escapeMarkup(item.name || 'wing') + '<small>pinned coordinator</small></span></button>').join('');
        nav.querySelectorAll('button').forEach((button, index) => bindOpen(button, sessions[index]));
    }
    mount.hidden = !session;
    if (!session) { mount.innerHTML = ''; return; }
    const evidence = evidenceFor(session), wing = wingFor(session);
    const summary = coordinatorHomeSummary(session, wing, { ...evidence, reportSequence: evidence.lifecycle && evidence.lifecycle.report_cursor });
    const signature = JSON.stringify([key(session), summary, evidence.cursor, evidence.messages, sessions.length, evidence.readError]);
    if (mount.dataset.signature !== signature) {
        mount.dataset.signature = signature;
        const expanded = !!mount.querySelector('details[open]');
        const focus = mount.contains(document.activeElement) && document.activeElement.dataset.homeAction;
        mount.innerHTML = '<div class="coordinator-home-kicker"><span>YOUR COORDINATOR</span><span class="coordinator-presence" data-presence="' + summary.presence + '"><i aria-hidden="true"></i>' + escapeMarkup(summary.presence) + '</span></div>' +
            '<div class="coordinator-home-heading"><span class="coordinator-avatar" aria-hidden="true">w</span><div><h2>' + escapeMarkup(summary.title) + '</h2><p>' + escapeMarkup(machineName(session)) + ' · pinned thread</p></div></div>' +
            '<h1 class="coordinator-goal">' + escapeMarkup(summary.goal) + '</h1>' +
            '<span class="coordinator-task-state" data-status="' + summary.status + '">' + escapeMarkup(summary.status) + '</span>' +
            (summary.historicalUpdate ? '<p class="coordinator-history-label">Last recorded update</p>' : '') + coordinatorThreadMarkup(evidence.messages, summary, 1) +
            (summary.presence==='stopped' ? '<p class="coordinator-next-action">'+escapeMarkup(summary.nextAction)+'</p>' : '') +
            '<div class="coordinator-heartbeat"><div><span>Last wake</span><strong>' + (summary.lastWake ? escapeMarkup(activityTime(summary.lastWake)) : 'Not reported') + '</strong></div><div><span>Next wake</span><strong>' + (summary.presence==='stopped' ? 'No restart confirmed' : summary.nextWake ? escapeMarkup(activityTime(summary.nextWake)) : 'Not reported') + '</strong></div></div>' +
            '<p class="coordinator-heartbeat-detail">' + escapeMarkup(summary.detail) + (summary.observedAt ? ' · checked ' + escapeMarkup(activityTime(summary.observedAt)) : '') + '</p>' +
            '<div class="coordinator-home-actions"><button type="button" class="coordinator-message" data-home-action="message">' + (summary.presence==='stopped' ? 'Inspect stopped session' : 'Message '+escapeMarkup(summary.title)) + ' <span aria-hidden="true">↗</span></button>' +
            (sessions.length > 1 ? '<label>Coordinator<select aria-label="Choose pinned coordinator" data-home-action="choose">' + sessions.map(item => '<option value="' + escapeMarkup(key(item)) + '"' + (key(item) === key(session) ? ' selected' : '') + '>' + escapeMarkup((item.name || 'wing') + ' · ' + (wingDisplayName(wingFor(item)) || item.wing_id)) + '</option>').join('') + '</select></label>' : '') + '</div>' +
            '<p id="coordinator-home-error" role="status"></p><details class="coordinator-recent"' + (expanded ? ' open' : '') + '><summary>Recent activity</summary>' + timelineMarkup(evidence.messages, 5) + '</details>';
        bindOpen(mount.querySelector('.coordinator-message'), session);
        mount.querySelectorAll('[data-review-request]').forEach(button=>bindOpen(button,session));
        const select = mount.querySelector('select');
        if (select) select.addEventListener('change', () => { selected = select.value; try { localStorage.setItem(selectionKey(), selected); } catch {} renderCoordinatorHome(); });
        mount.querySelectorAll('[data-artifact-path]').forEach(button => button.addEventListener('click', async () => {
            try { if (session.fleetRow && !searchRoute(session.fleetRow, S.wingsData)) throw Error('Reconnect to open this file'); if (session.fleetRow && searchRoute(session.fleetRow, S.wingsData).kind === 'via') throw Error('File download requires a direct connection to this machine'); if (!showSessionChat(session.id, session.wing_id)) return; DOM.sessionFilePath.value = button.dataset.artifactPath; if (DOM.sessionFilesPanel.style.display === 'none') DOM.sessionFilesBtn.click(); } catch (error) { const status = document.getElementById('coordinator-home-error'); if (status) status.textContent = error.message || 'Could not open artifact'; }
        }));
        if (focus) mount.querySelector('[data-home-action="' + focus + '"]')?.focus({ preventScroll: true });
    }
    if (read && S.activeView === 'home') readHome(session);
}

export function initCoordinatorHome() {
    if (initialized) return;
    initialized = true;
    window.addEventListener('wingthing:conversation-inventory-updated', () => renderCoordinatorHome());
    setInterval(() => renderCoordinatorHome(S.activeView === 'home' && document.visibilityState === 'visible'), 5000);
    renderCoordinatorHome();
}
