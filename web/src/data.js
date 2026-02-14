import { S, DOM, CACHE_KEY, WINGS_CACHE_KEY, LAST_TERM_KEY, WING_ORDER_KEY, EGG_ORDER_KEY, WING_SESSIONS_PREFIX } from './state.js';
import { renderSidebar, renderDashboard } from './render.js';
import { renderWingDetailPage } from './render.js';
import { rebuildAgentLists, updateHeaderStatus } from './dashboard.js';
import { updatePaletteState } from './palette.js';
import { sendTunnelRequest } from './tunnel.js';
import { setNotification, clearNotification } from './notify.js';

// localStorage CRUD

export function getLastTermAgent() {
    try { return localStorage.getItem(LAST_TERM_KEY) || 'claude'; } catch (e) { return 'claude'; }
}
export function setLastTermAgent(agent) {
    try { localStorage.setItem(LAST_TERM_KEY, agent); } catch (e) {}
}
export function getCachedSessions() {
    try { var raw = localStorage.getItem(CACHE_KEY); return raw ? JSON.parse(raw) : []; }
    catch (e) { return []; }
}
export function setCachedSessions(sessions) {
    try { localStorage.setItem(CACHE_KEY, JSON.stringify(sessions)); } catch (e) {}
}
export function getCachedWings() {
    try { var raw = localStorage.getItem(WINGS_CACHE_KEY); return raw ? JSON.parse(raw) : []; }
    catch (e) { return []; }
}
export function setCachedWings(wings) {
    try { localStorage.setItem(WINGS_CACHE_KEY, JSON.stringify(wings)); } catch (e) {}
}
export function getWingOrder() {
    try { var raw = localStorage.getItem(WING_ORDER_KEY); return raw ? JSON.parse(raw) : []; }
    catch (e) { return []; }
}
export function setWingOrder(order) {
    try { localStorage.setItem(WING_ORDER_KEY, JSON.stringify(order)); } catch (e) {}
}
export function sortWingsByOrder(wings) {
    var order = getWingOrder();
    var orderMap = {};
    order.forEach(function(id, i) { orderMap[id] = i; });
    var known = [];
    var unknown = [];
    wings.forEach(function(w) {
        if (orderMap.hasOwnProperty(w.wing_id)) {
            known.push(w);
        } else {
            unknown.push(w);
        }
    });
    known.sort(function(a, b) { return orderMap[a.wing_id] - orderMap[b.wing_id]; });
    return known.concat(unknown);
}
export function getEggOrder() {
    try { var raw = localStorage.getItem(EGG_ORDER_KEY); return raw ? JSON.parse(raw) : []; }
    catch (e) { return []; }
}
export function setEggOrder(order) {
    try { localStorage.setItem(EGG_ORDER_KEY, JSON.stringify(order)); } catch (e) {}
}
export function sortSessionsByOrder(sessions) {
    var order = getEggOrder();
    var orderMap = {};
    order.forEach(function(id, i) { orderMap[id] = i; });
    var known = [];
    var unknown = [];
    sessions.forEach(function(s) {
        if (orderMap.hasOwnProperty(s.id)) {
            known.push(s);
        } else {
            unknown.push(s);
        }
    });
    known.sort(function(a, b) { return orderMap[a.id] - orderMap[b.id]; });
    return known.concat(unknown);
}

export function getCachedWingSessions(wingId) {
    try { var raw = localStorage.getItem(WING_SESSIONS_PREFIX + wingId); return raw ? JSON.parse(raw) : null; }
    catch (e) { return null; }
}
export function setCachedWingSessions(wingId, sessions) {
    try { localStorage.setItem(WING_SESSIONS_PREFIX + wingId, JSON.stringify(sessions)); } catch (e) {}
}

// Data loading

export async function fetchWingSessions(wingId) {
    try {
        var result = await sendTunnelRequest(wingId, { type: 'sessions.list' });
        return (result.sessions || []).map(function(s) {
            return { id: s.session_id, wing_id: (S.wingsData.find(function(w) { return w.wing_id === wingId; }) || {}).id || '', agent: s.agent, cwd: s.cwd, status: 'detached', needs_attention: s.needs_attention, audit: s.audit };
        });
    } catch (e) { return []; }
}

export function mergeWingSessions(allSessions) {
    var seen = {};
    var deduped = [];
    allSessions.forEach(function(s) {
        if (!seen[s.id]) {
            seen[s.id] = true;
            deduped.push(s);
        }
    });
    S.sessionsData = sortSessionsByOrder(deduped);
    setEggOrder(S.sessionsData.map(function(s) { return s.id; }));
}

export async function loadHome() {
    var wings = [];
    try {
        var wingsResp = await fetch('/api/app/wings');
        if (wingsResp.ok) wings = await wingsResp.json() || [];
    } catch (e) {
        wings = [];
    }

    wings.forEach(function (w) { w.online = true; });
    var apiWings = {};
    wings.forEach(function(w) { apiWings[w.wing_id] = true; });
    S.wingsData.forEach(function(ew) {
        if (!apiWings[ew.wing_id]) {
            ew.online = false;
            wings.push(ew);
        }
    });
    S.wingsData = sortWingsByOrder(wings);

    S.wingsData.forEach(function(w) {
        if (w.latest_version) S.latestVersion = w.latest_version;
    });

    setCachedWings(S.wingsData.map(function (w) {
        return { wing_id: w.wing_id, hostname: w.hostname, id: w.id, platform: w.platform, version: w.version, agents: w.agents, labels: w.labels, wing_label: w.wing_label };
    }));

    rebuildAgentLists();
    updateHeaderStatus();

    // Sweep-poll online wings for projects via E2E tunnel
    S.wingsData.filter(function(w) { return w.online !== false && w.wing_id && w.public_key; })
        .forEach(function(w) {
            sendTunnelRequest(w.wing_id, { type: 'projects.list' })
                .then(function(data) {
                    w.projects = data.projects || [];
                    rebuildAgentLists();
                    if (S.activeView === 'home') renderDashboard();
                    if (S.activeView === 'wing-detail' && S.currentWingId === w.wing_id)
                        renderWingDetailPage(w.wing_id);
                })
                .catch(function(e) {
                    w.projects = [];
                    if (e.message && e.message.indexOf('locked') !== -1) {
                        w.tunnel_error = e.message;
                    }
                });
        });

    var onlineWings = S.wingsData.filter(function(w) { return w.online !== false && w.wing_id && !w.pinned; });
    var sessionPromises = onlineWings.map(function(w) { return fetchWingSessions(w.wing_id); });
    var results = await Promise.all(sessionPromises);
    var allSessions = [];
    results.forEach(function(r) { allSessions = allSessions.concat(r); });
    mergeWingSessions(allSessions);

    S.sessionsData.forEach(function(s) {
        if (s.needs_attention && s.id !== S.ptySessionId) {
            setNotification(s.id);
        } else if (!s.needs_attention && S.sessionNotifications[s.id]) {
            clearNotification(s.id);
        }
    });

    renderSidebar();
    if (S.activeView === 'home') renderDashboard();
    if (S.activeView === 'wing-detail' && S.currentWingId) renderWingDetailPage(S.currentWingId);

    if (DOM.commandPalette.style.display !== 'none') {
        updatePaletteState(true);
    }
}
