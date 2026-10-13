import { initSessionViewer, openSessionViewer, closeSessionViewer } from './session-viewer.js';
import { S } from './state.js';
import { escapeHtml, wingDisplayName } from './helpers.js';
import { sendTunnelRequest } from './tunnel.js';
import { SEARCH_VERSION, mergeSearchInventories, searchRoute, createSearchController } from './session-search-state.js';

let installed = false, timer, controller, current = null, owner = null, reading = 0;

export async function searchEverywhere(query) {
    const wings = S.wingsData.filter(wing => wing.wing_id && wingDisplayName(wing));
    const inventories = [], failures = [];
    const userId = S.currentUser?.id;
    let cursor = 0;
    // Match the transport pool's fixed concurrency rather than flooding its queue.
    await Promise.all(Array.from({ length: Math.min(4, wings.length) }, async () => {
        while (cursor < wings.length) {
            if (S.currentUser?.id !== userId) return;
            const wing = wings[cursor++];
            const label = wingDisplayName(wing);
            if (wing.online === false) { failures.push({ machine_label: label, healthy: false, error: 'offline' }); continue; }
            const remotes = wing.user_id === S.currentUser?.id && !wing.org;
            try {
                let result;
                try {
                    result = await sendTunnelRequest(wing.wing_id, { type: 'sessions.search', arguments: { query, include_remotes: remotes } }, { skipPasskey: true });
                } catch (error) {
                    if (!remotes) throw error;
                    // Local read remains useful when fleet delegation is disabled.
                    failures.push({ machine_label: label, healthy: false, error: 'Configured remote search unavailable' });
                    result = await sendTunnelRequest(wing.wing_id, { type: 'sessions.search', arguments: { query, include_remotes: false } }, { skipPasskey: true });
                }
                if (result.version !== SEARCH_VERSION || result.wing_id !== wing.wing_id || !Array.isArray(result.sessions)) throw Error('Search contract or wing identity mismatch');
                inventories.push(result);
            } catch (error) { failures.push({ machine_label: label, healthy: false, error: 'Search unavailable; unlock or refresh this wing' }); }
        }
    }));
    if (!wings.length) failures.push({ machine_label: 'Wings', healthy: false, error: 'No authorized wings available' });
    return mergeSearchInventories(inventories, failures);
}

function showResults(state) {
    if (owner !== S.currentUser?.id) return;
    current = state;
    const results = document.getElementById('session-search-results');
    const status = document.getElementById('session-search-status');
    const normal = document.getElementById('sessions-list');
    const empty = document.getElementById('empty-state');
    const active = Boolean(state.query);
    results.hidden = !active;
    if (normal) normal.hidden = active;
    if (empty) empty.hidden = active;
    if (!active) { results.replaceChildren(); status.textContent = ''; return; }
    status.textContent = state.loading ? 'Searching sessions…' :
        (state.error || `${state.sessions.length} sessions · ${state.complete ? 'coverage complete' : 'coverage incomplete'}`);
    results.innerHTML = '';
    if (state.loading) return;
    results.innerHTML = (state.sources || []).filter(source => !source.healthy).map(source =>
        `<p class="search-source-error">${escapeHtml(source.machine_label)} · ${escapeHtml(source.error || 'unavailable')}</p>`).join('') +
        state.sessions.map((row, index) => {
            const route = searchRoute(row, S.wingsData);
            return `<article class="search-session"><h3>${row.pinned && row.role === 'coordinator' ? 'Pinned wing · ' : ''}${escapeHtml(row.name || row.session_id)} · ${escapeHtml(row.agent || 'agent')}</h3>` +
                `<p>${escapeHtml(row.machine_label)} · ${escapeHtml(row.cwd || '')} · ${escapeHtml(row.status)}</p>` +
                `<div class="search-identity">${escapeHtml(row.origin_wing_id)} / ${escapeHtml(row.session_id)}</div>` +
                `<p>${(row.routes || []).map(r => `<span class="search-route">${escapeHtml(r.kind === 'via' ? 'via ' + r.machine_label : 'direct')}${r.healthy ? '' : ' · unavailable'}</span>`).join(' ')}</p>` +
                `<p class="text-dim">Matched: ${escapeHtml((row.match_fields || []).join(', '))}${row.search_incomplete ? ' · index incomplete' : ''}</p>` +
                `<button type="button" data-search-open="${index}" ${route ? '' : 'disabled'}>Open session</button> <button type="button" data-search-read="${index}" ${route ? '' : 'disabled'}>Read transcript</button></article>`;
        }).join('') + (!state.sessions.length ? '<p>No matching sessions in the sources checked.</p>' : '');
}

async function readResult(index) {
    const row = current?.sessions?.[index];
    if (!row) return;
    const route = searchRoute(row, S.wingsData);
    const ticket = ++reading;
    const viewer = document.getElementById('session-search-transcript');
    viewer.hidden = false;
    viewer.textContent = route ? 'Reading transcript…' : 'Refresh search before reading this session.';
    if (!route) return;
    const id = owner;
    try {
        const result = await sendTunnelRequest(route.wing_id, { type: 'session.control', operation: 'session_action', arguments: {
            origin_wing_id: row.origin_wing_id, session_id: row.session_id, operation: 'session_read', arguments: { after_cursor: 0, limit: 200 },
        } });
        if (ticket !== reading || id !== S.currentUser?.id) return;
        if (result.origin_wing_id !== row.origin_wing_id || result.session_id !== row.session_id) throw Error('Transcript identity mismatch');
        const events = result.result?.lifecycle?.events || [];
        viewer.textContent = `${row.machine_label} / ${row.session_id} · ${route.kind === 'via' ? 'via ' + route.machine_label : 'direct'}\n\n` +
            events.map(event => `${event.role || event.type}: ${event.text || ''}`).join('\n\n') +
            (result.result?.lifecycle?.has_more ? '\n\nMore transcript is available through wt session read.' : '') +
            (!events.length ? 'No recorded conversation text.' : '');
    } catch (error) { if (ticket === reading && id === S.currentUser?.id) viewer.textContent = 'Transcript unavailable. Refresh search and retry.'; }
}

export function initSessionSearch() {
    const input = document.getElementById('session-search-input');
    if (!input) return;
    initSessionViewer(searchEverywhere);
    if (owner !== S.currentUser?.id) {
        closeSessionViewer();
        owner = S.currentUser?.id;
        controller?.retire(); reading++; clearTimeout(timer);
        input.value = ''; current = null;
        document.getElementById('session-search-transcript').hidden = true;
        document.getElementById('session-search-transcript').textContent = '';
        showResults({ query: '', sessions: [] });
    }
    if (installed) {
        if (current) showResults(current);
        return;
    }
    installed = true;
    controller = createSearchController(searchEverywhere, showResults);
    function run() {
        const query = input.value.trim();
        reading++;
        document.getElementById('session-search-transcript').hidden = true;
        if (query) controller.run(query); else { controller.retire(); showResults({ query: '', sessions: [] }); }
    }
    input.addEventListener('input', () => {
        controller.retire(); clearTimeout(timer); reading++;
        showResults({ query: input.value.trim(), loading: true, sessions: [] });
        timer = setTimeout(run, 250);
    });
    document.getElementById('session-search-form').addEventListener('submit', event => { event.preventDefault(); clearTimeout(timer); run(); });
    document.getElementById('session-search-results').addEventListener('click', event => {
        const open = event.target.closest('[data-search-open]');
        if (open && !open.disabled) openSessionViewer(current?.sessions?.[Number(open.dataset.searchOpen)]);
        const button = event.target.closest('[data-search-read]');
        if (button && !button.disabled) readResult(Number(button.dataset.searchRead));
    });
}
