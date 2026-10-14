import { setCoordinatorFleet } from './coordinator-home.js';
import { initSessionViewer, openSessionViewer, closeSessionViewer } from './session-viewer.js';
import { S } from './state.js';
import { escapeHtml, wingDisplayName } from './helpers.js';
import { sendTunnelRequest } from './tunnel.js';
import { SEARCH_VERSION, sourceProblems, inventoryPresentation, mergeSearchInventories, searchRoute, createSearchController } from './session-search-state.js';

const sourceSuccess=new Map();let sourceOwner=null;
let installed = false, timer, controller, current = null, owner = null, reading = 0, observedWings = '';

export async function searchEverywhere(query) {
    const wings = S.wingsData.filter(wing => wing.wing_id && wingDisplayName(wing));
    const inventories = [], failures = [];
    const userId = S.currentUser?.id;if(sourceOwner!==userId){sourceOwner=userId;sourceSuccess.clear();}
    let cursor = 0;
    // Match the transport pool's fixed concurrency rather than flooding its queue.
    await Promise.all(Array.from({ length: Math.min(4, wings.length) }, async () => {
        while (cursor < wings.length) {
            if (S.currentUser?.id !== userId) return;
            const wing = wings[cursor++];
            const label = wingDisplayName(wing);
            if (wing.online === false) { failures.push({ wing_id:wing.wing_id,machine_label: label, healthy: false, error: 'Computer offline',lastFetchedAt:sourceSuccess.get(wing.wing_id) }); continue; }
            const remotes = wing.user_id === S.currentUser?.id && !wing.org;
            try {
                let result;
                try {
                    result = await sendTunnelRequest(wing.wing_id, { type: query ? 'sessions.search' : 'sessions.inventory', arguments: { query, include_remotes: remotes } }, { skipPasskey: true });
                } catch (error) {
                    if (!remotes) throw error;
                    // Local read remains useful when fleet delegation is disabled.
                    result = await sendTunnelRequest(wing.wing_id, { type: query ? 'sessions.search' : 'sessions.inventory', arguments: { query, include_remotes: false } }, { skipPasskey: true });
                    failures.push({wing_id:wing.wing_id,machine_label:label,healthy:false,error:'Saved remote search unavailable',lastFetchedAt:sourceSuccess.get(wing.wing_id)});
                }
                if (result.version !== SEARCH_VERSION || result.wing_id !== wing.wing_id || !Array.isArray(result.sessions)) throw Error('Search contract or wing identity mismatch');
                if(S.currentUser?.id!==userId)return;
                sourceSuccess.set(wing.wing_id,Date.now());
                inventories.push({...result,sources:(result.sources||[]).map(source=>({...source,retryWingId:wing.wing_id}))});
            } catch (error) { failures.push({wing_id:wing.wing_id,machine_label:label,healthy:false,error:'Search unavailable; verify access and retry',lastFetchedAt:sourceSuccess.get(wing.wing_id)}); }
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
    const presentation = inventoryPresentation(state);
    const active = presentation.visible;
    if (!state.query && !state.loading) setCoordinatorFleet(state);
    results.hidden = !active;
    if (normal) normal.hidden = active && !presentation.fallback;
    if (empty) empty.hidden = active && !presentation.fallback;
    if (!active) { results.replaceChildren(); status.textContent = ''; return; }
    status.textContent = presentation.status + (state.cached&&state.fetchedAt ? ' · last fetched '+new Date(state.fetchedAt).toLocaleString() : '') + (presentation.fallback && !state.loading ? ' · local inventory below may be cached' : '');
    results.innerHTML = '';
    if (state.loading) return;
    results.innerHTML = sourceProblems(state.sources).map(source=>{const retryId=source.retryWingId||source.wing_id,wing=S.wingsData.find(w=>w.wing_id===retryId);return `<section class="search-source-error"><strong>${escapeHtml(source.machine_label)} · Search unavailable</strong><p>${escapeHtml((source.error||'Verify access and retry').replace(/^Search unavailable; /,''))}</p><p>Last successful check: ${escapeHtml(source.lastFetchedAt ? new Date(source.lastFetchedAt).toLocaleString() : 'Unknown')}</p>${wing ? `<button type="button" data-search-retry="${escapeHtml(wing.wing_id)}">Retry ${escapeHtml(source.machine_label||wingDisplayName(wing))}</button>` : ''}</section>`;}).join('') +
        groupedRows(state.sessions).map(({ row, index, heading }) => {
            const route = searchRoute(row, S.wingsData);
            return heading + `<article class="search-session" data-origin="${escapeHtml(row.origin_wing_id)}" data-session="${escapeHtml(row.session_id)}"><h3>${row.pinned && row.role === 'coordinator' ? 'Pinned wing · ' : ''}${escapeHtml(row.name || row.session_id)} · ${escapeHtml(row.agent || 'agent')}</h3>` +
                `<p>${escapeHtml(row.machine_label)} · ${escapeHtml(row.cwd || '')} · ${escapeHtml(row.status)}${row.possible_duplicate ? ' · possible duplicate' : ''}</p>` +
                `<div class="search-identity">${escapeHtml(row.origin_wing_id)} / ${escapeHtml(row.session_id)}</div>` +
                `<p>${(row.routes || []).map(r => `<span class="search-route">${escapeHtml(r.kind === 'via' ? 'via ' + r.machine_label : 'direct')}${r.healthy ? '' : ' · unavailable'}</span>`).join(' ')}</p>` +
                `<p class="text-dim">Matched: ${escapeHtml((row.match_fields || []).join(', '))}${row.search_incomplete ? ' · index incomplete' : ''}</p>` +
                `<button type="button" data-search-open="${index}" ${route ? '' : 'disabled'}>Open session</button> <button type="button" data-search-read="${index}" ${route ? '' : 'disabled'}>Read transcript</button></article>`;
        }).join('') + (!state.sessions?.length ? '<p>' + (state.complete ? 'No sessions matched in the successfully checked sources.' : 'Connect the unavailable computers, then retry.') + '</p>' : '');
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
        controller?.retire(true); reading++; clearTimeout(timer);
        input.value = ''; current = null; observedWings='';
        document.getElementById('session-search-transcript').hidden = true;
        document.getElementById('session-search-transcript').textContent = '';
        showResults({ query: '', sessions: [] });
    }
    const wingSignature=JSON.stringify(S.wingsData.map(w=>[w.wing_id,w.online,wingDisplayName(w),w.user_id,w.org,w.tunnel_error]));
    if (installed) {
        if(wingSignature!==observedWings){observedWings=wingSignature;controller.run(input.value.trim());return;}
        if (current) showResults(current);
        return;
    }
    installed = true;observedWings=wingSignature;
    controller = createSearchController(searchEverywhere, showResults);
    function run() {
        const query = input.value.trim();
        reading++;
        document.getElementById('session-search-transcript').hidden = true;
        controller.run(query);
    }
    run();
    setInterval(() => { if (S.activeView === 'home' && document.visibilityState === 'visible' && !input.value.trim() && !current?.loading) controller.run(''); }, 15000);
    window.addEventListener('online', run);
    input.addEventListener('input', () => {
        controller.retire(); clearTimeout(timer); reading++;
        showResults({ query: input.value.trim(), loading: true, sessions: [] });
        timer = setTimeout(run, 250);
    });
    document.getElementById('session-search-form').addEventListener('submit', event => { event.preventDefault(); clearTimeout(timer); run(); });
    document.getElementById('session-search-results').addEventListener('click', async event => {
        const retry=event.target.closest('[data-search-retry]');
        if(retry&&!retry.disabled){const wing=S.wingsData.find(w=>w.wing_id===retry.dataset.searchRetry),retryOwner=owner,query=current?.query||'';if(!wing)return;retry.disabled=true;try{const result=await sendTunnelRequest(wing.wing_id,{type:query?'sessions.search':'sessions.inventory',arguments:{query,include_remotes:wing.user_id===S.currentUser?.id&&!wing.org}},{skipPasskey:false});if(result.version!==SEARCH_VERSION||result.wing_id!==wing.wing_id||!Array.isArray(result.sessions))throw Error('Search response identity mismatch');if(retryOwner===owner&&owner===S.currentUser?.id)await controller.run(query);}catch(error){if(retryOwner===owner&&owner===S.currentUser?.id){document.getElementById('session-search-status').textContent='Search unavailable for '+wingDisplayName(wing)+'. Verify access and retry.';retry.disabled=false;}}return;}
        const open = event.target.closest('[data-search-open]');
        if (open && !open.disabled) openSessionViewer(current?.sessions?.[Number(open.dataset.searchOpen)]);
        const button = event.target.closest('[data-search-read]');
        if (button && !button.disabled) readResult(Number(button.dataset.searchRead));
    });
}

function groupedRows(rows = []) {
    const groups = new Map();
    rows.forEach((row, index) => {
        const origin = row.origin_wing_id;
        if (!groups.has(origin)) groups.set(origin, []);
        groups.get(origin).push({ row, index });
    });
    return [...groups.values()].flatMap(group => group.map((item, index) => ({ ...item,
        heading: index === 0 ? '<h2 class="fleet-machine">' + escapeHtml(item.row.machine_label || 'Machine') + '</h2>' : '' })));
}
