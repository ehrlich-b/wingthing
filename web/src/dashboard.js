import { S, DOM } from './state.js';
import { wingDisplayName } from './helpers.js';
import { renderDashboard, renderSidebar, renderWingDetailPage } from './render.js';
import { getCachedWings, fetchWingSessions, mergeWingSessions, loadHome, probeWing, saveWingCache } from './data.js';
import { updatePaletteState } from './palette.js';
import { tunnelCloseWing } from './tunnel.js';
import { setNotification } from './notify.js';

var reconnectBannerTimer = null;

export function showReconnectBanner() {
    if (reconnectBannerTimer) return;
    reconnectBannerTimer = setTimeout(function() {
        var banner = document.getElementById('reconnect-banner');
        if (banner) banner.style.display = '';
    }, 2000);
}

export function hideReconnectBanner() {
    if (reconnectBannerTimer) { clearTimeout(reconnectBannerTimer); reconnectBannerTimer = null; }
    var banner = document.getElementById('reconnect-banner');
    if (banner) banner.style.display = 'none';
}

export function connectAppWS() {
    if (S.appWs) { try { S.appWs.close(); } catch(e) {} }
    var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    S.appWs = new WebSocket(proto + '//' + location.host + '/ws/app');
    S.appWs.onopen = function() {
        S.appWsBackoff = 1000;
        hideReconnectBanner();
        loadHome();
    };
    S.appWs.onmessage = function(e) {
        try {
            var msg = JSON.parse(e.data);
            console.log('[app-ws] event:', msg.type, 'wing_id:', msg.wing_id, 'locked:', msg.locked, 'allowed_count:', msg.allowed_count, msg);
            if (msg.type === 'relay.restart') {
                S.appWs = null;
                setTimeout(connectAppWS, 500);
                return;
            }
            if (msg.type === 'org.changed') {
                // Org membership changed — reconnect WS to pick up new org subscriptions
                S.appWs.close();
                return;
            }
            applyWingEvent(msg);
        } catch(err) {}
    };
    S.appWs.onclose = function() {
        S.appWs = null;
        showReconnectBanner();
        setTimeout(connectAppWS, S.appWsBackoff);
        S.appWsBackoff = Math.min(S.appWsBackoff * 2, 10000);
    };
    S.appWs.onerror = function() { S.appWs.close(); };
}

function applyWingEvent(ev) {
    var needsFullRender = false;
    if (ev.type === 'wing.online') {
        var found = false;
        S.wingsData.forEach(function(w) {
            if (w.wing_id === ev.wing_id) {
                w.online = true;
                w.public_key = ev.public_key || w.public_key;
                if (ev.locked !== undefined) {
                    w.locked = ev.locked;
                    w.allowed_count = ev.allowed_count || 0;
                    if (!ev.locked) delete w.tunnel_error;
                }
                found = true;
            }
        });
        if (!found) {
            // Hydrate from cache so label survives reconnect
            var cached = getCachedWings();
            var c = cached.find(function(cw) { return cw.wing_id === ev.wing_id; });
            var newWing = {
                wing_id: ev.wing_id,
                online: true,
                public_key: ev.public_key,
                locked: ev.locked,
                allowed_count: ev.allowed_count || 0,
                wing_label: c ? c.wing_label : undefined,
                hostname: c ? c.hostname : undefined,
                platform: c ? c.platform : undefined,
                projects: [],
                agents: [],
            };
            if (wingDisplayName(newWing)) {
                S.wingsData.push(newWing);
                needsFullRender = true;
            } else if (ev.public_key) {
                // No cached name — probe first, add only after we get metadata
                tunnelCloseWing(ev.wing_id);
                probeWing(newWing).then(function() {
                    if (newWing.tunnel_error === 'not_allowed') return;
                    if (!wingDisplayName(newWing)) return;
                    S.wingsData.push(newWing);
                    saveWingCache();
                    rebuildAgentLists();
                    updateHeaderStatus();
                    if (S.activeView === 'home') renderDashboard();
                    if (S.activeView === 'wing-detail' && S.currentWingId === ev.wing_id)
                        renderWingDetailPage(ev.wing_id);
                    if (DOM.commandPalette.style.display !== 'none') updatePaletteState(true);
                    if (!newWing.tunnel_error) {
                        fetchWingSessions(ev.wing_id).then(function(sessions) {
                            if (sessions) {
                                mergeWingSessions(ev.wing_id, sessions);
                                renderSidebar();
                                if (S.activeView === 'home') renderDashboard();
                            }
                        }).catch(function() {});
                    }
                });
                return;
            }
        }
    } else if (ev.type === 'wing.config') {
        S.wingsData.forEach(function(w) {
            if (w.wing_id === ev.wing_id) {
                if (ev.locked !== undefined) {
                    w.locked = ev.locked;
                    w.allowed_count = ev.allowed_count || 0;
                    if (!ev.locked) delete w.tunnel_error;
                }
                if (ev.public_key) w.public_key = ev.public_key;
            }
        });
    } else if (ev.type === 'wing.offline') {
        S.wingsData.forEach(function(w) {
            if (w.wing_id === ev.wing_id) {
                w.online = false;
            }
        });
        S.sessionsData.forEach(function(s) {
            if (s.wing_id === ev.wing_id) s.swept = false;
        });
        tunnelCloseWing(ev.wing_id);
        // DON'T clear sessions — wing might reconnect momentarily
    } else if (ev.type === 'session.attention' && ev.session_id) {
        setNotification(ev.session_id);
        renderSidebar();
        return;
    }

    // Render immediately with current wing data
    saveWingCache();
    rebuildAgentLists();
    updateHeaderStatus();
    if (S.activeView === 'home') {
        if (needsFullRender) {
            renderDashboard();
        } else {
            updateWingCardStatus(ev.wing_id);
        }
        pingWingDot(ev.wing_id);
    } else if (S.activeView === 'wing-detail' && S.currentWingId === ev.wing_id) {
        renderWingDetailPage(S.currentWingId);
    }
    if (DOM.commandPalette.style.display !== 'none') updatePaletteState(true);

    // Probe in background, re-render when done
    var evWing = S.wingsData.find(function(w) { return w.wing_id === ev.wing_id; });
    if ((ev.type === 'wing.online' || ev.type === 'wing.config') && evWing && evWing.public_key) {
        tunnelCloseWing(ev.wing_id);
        probeWing(evWing).then(function() {
            saveWingCache();
            rebuildAgentLists();
            if (S.activeView === 'home') renderDashboard();
            if (S.activeView === 'wing-detail' && S.currentWingId === ev.wing_id)
                renderWingDetailPage(ev.wing_id);
            if (DOM.commandPalette.style.display !== 'none') updatePaletteState(true);

            if (!evWing.tunnel_error) {
                fetchWingSessions(ev.wing_id).then(function(sessions) {
                    if (sessions) {
                        mergeWingSessions(ev.wing_id, sessions);
                        renderSidebar();
                        if (S.activeView === 'home') renderDashboard();
                    }
                }).catch(function() {});
            }
        });
    }
}

export function pingWingDot(wingId) {
    requestAnimationFrame(function() {
        var card = DOM.wingStatusEl.querySelector('.wing-box[data-wing-id="' + wingId + '"]');
        if (!card) return;
        var dot = card.querySelector('.wing-dot');
        if (!dot) return;
        dot.classList.remove('dot-ping');
        void dot.offsetWidth;
        dot.classList.add('dot-ping');
    });
}

export function updateWingCardStatus(wingId) {
    var card = DOM.wingStatusEl.querySelector('.wing-box[data-wing-id="' + wingId + '"]');
    if (!card) {
        renderDashboard();
        return;
    }
    var w = S.wingsData.find(function(w) { return w.wing_id === wingId; });
    if (!w) return;
    var dot = card.querySelector('.wing-dot');
    if (dot) {
        if (w.online === undefined) {
            dot.classList.remove('dot-live', 'dot-offline');
        } else {
            dot.classList.toggle('dot-live', w.online === true);
            dot.classList.toggle('dot-offline', w.online !== true);
        }
    }
}

export function updateHeaderStatus() {
    var indicator = document.getElementById('wing-indicator');
    if (!indicator) return;
    var anyOnline = S.wingsData.some(function(w) { return w.online === true; });
    indicator.classList.toggle('dot-live', anyOnline);
    indicator.classList.toggle('dot-offline', !anyOnline);
    indicator.style.display = S.wingsData.length > 0 ? '' : 'none';
}

export function rebuildAgentLists() {
    S.availableAgents = [];
    S.allProjects = [];
    var seenAgents = {};
    S.wingsData.forEach(function(w) {
        if (w.online === false) return;
        (w.agents || []).forEach(function(a) {
            if (!seenAgents[a]) { seenAgents[a] = true; S.availableAgents.push({ agent: a, wingId: w.wing_id }); }
        });
        (w.projects || []).forEach(function(p) {
            S.allProjects.push({ name: p.name, path: p.path, wingId: w.wing_id });
        });
    });
}
