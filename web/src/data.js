import { S, DOM, CACHE_KEY, WINGS_CACHE_KEY, LAST_TERM_KEY, WING_ORDER_KEY, EGG_ORDER_KEY, WING_SESSIONS_PREFIX } from './state.js';
import { renderSidebar, renderDashboard } from './render.js';
import { renderWingDetailPage } from './render.js';
import { rebuildAgentLists, updateHeaderStatus } from './dashboard.js';
import { updatePaletteState } from './palette.js';
import { sendTunnelRequest, saveTunnelAuthTokens } from './tunnel.js';
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

// Tunnel probe — populates wing metadata or sets tunnel_error

export async function probeWing(w) {
    try {
        var data = await sendTunnelRequest(w.wing_id, { type: 'wing.info' }, { skipPasskey: true });
        w.hostname = data.hostname || w.hostname;
        w.platform = data.platform || w.platform;
        w.version = data.version || w.version;
        w.agents = data.agents || [];
        w.projects = data.projects || [];
        w.locked = data.locked || false;
        w.allowed_count = data.allowed_count || 0;
        delete w.tunnel_error;
    } catch (e) {
        var msg = e.message || '';
        if (msg.indexOf('not_allowed') !== -1) {
            w.tunnel_error = 'not_allowed';
            delete S.tunnelAuthTokens[w.wing_id];
            saveTunnelAuthTokens();
            // Clear sessions from revoked wing
            S.sessionsData = S.sessionsData.filter(function(s) { return s.wing_id !== w.wing_id; });
        } else if (msg.indexOf('passkey_required') !== -1) {
            w.tunnel_error = 'passkey_required';
            if (e.metadata) {
                w.hostname = e.metadata.hostname || w.hostname;
                w.platform = e.metadata.platform || w.platform;
                w.version = e.metadata.version || w.version;
                w.locked = true;
            }
        } else {
            w.tunnel_error = 'unreachable';
        }
    }
}

// Data loading

export async function fetchWingSessions(wingId) {
    try {
        var result = await sendTunnelRequest(wingId, { type: 'sessions.list' }, { skipPasskey: true });
        return (result.sessions || []).map(function(s) {
            return { id: s.session_id, wing_id: (S.wingsData.find(function(w) { return w.wing_id === wingId; }) || {}).wing_id || '', agent: s.agent, cwd: s.cwd, status: 'detached', needs_attention: s.needs_attention, audit: s.audit };
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

    // Render immediately with basic wing data before probing
    rebuildAgentLists();
    updateHeaderStatus();
    renderSidebar();
    if (S.activeView === 'home') renderDashboard();
    if (S.activeView === 'wing-detail' && S.currentWingId) renderWingDetailPage(S.currentWingId);

    // Probe online wings in background, re-render as probes complete
    var onlineWings = S.wingsData.filter(function(w) { return w.online !== false && w.wing_id && w.public_key; });
    onlineWings.forEach(function(w) {
        probeWing(w).then(function() {
            setCachedWings(S.wingsData.filter(function(w) {
                return w.tunnel_error !== 'not_allowed' && w.tunnel_error !== 'unreachable';
            }).map(function(w) {
                return { wing_id: w.wing_id, public_key: w.public_key, wing_label: w.wing_label };
            }));
            rebuildAgentLists();
            renderSidebar();
            if (S.activeView === 'home') renderDashboard();
            if (S.activeView === 'wing-detail' && S.currentWingId) renderWingDetailPage(S.currentWingId);
            if (DOM.commandPalette.style.display !== 'none') updatePaletteState(true);

            if (!w.tunnel_error) {
                fetchWingSessions(w.wing_id).then(function(sessions) {
                    if (sessions.length > 0) {
                        var otherSessions = S.sessionsData.filter(function(s) {
                            return s.wing_id !== sessions[0].wing_id;
                        });
                        mergeWingSessions(otherSessions.concat(sessions));

                        S.sessionsData.forEach(function(s) {
                            if (s.needs_attention && s.id !== S.ptySessionId) {
                                setNotification(s.id);
                            } else if (!s.needs_attention && S.sessionNotifications[s.id]) {
                                clearNotification(s.id);
                            }
                        });

                        renderSidebar();
                        if (S.activeView === 'home') renderDashboard();
                    }
                }).catch(function() {});
            }
        });
    });
}
