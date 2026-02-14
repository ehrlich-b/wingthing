import { S, DOM } from './state.js';
import { wingDisplayName } from './helpers.js';
import { renderDashboard, renderSidebar, renderWingDetailPage } from './render.js';
import { setCachedWings, fetchWingSessions, mergeWingSessions, loadHome, probeWing } from './data.js';
import { updatePaletteState } from './palette.js';
import { tunnelCloseWing } from './tunnel.js';

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
            if (msg.type === 'relay.restart') {
                showReconnectBanner();
                S.appWs = null;
                setTimeout(connectAppWS, 500);
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
                found = true;
            }
        });
        if (!found) {
            S.wingsData.push({
                wing_id: ev.wing_id,
                online: true,
                public_key: ev.public_key,
                projects: [],
                agents: [],
            });
            needsFullRender = true;
        }
    } else if (ev.type === 'wing.offline') {
        S.wingsData.forEach(function(w) {
            if (w.wing_id === ev.wing_id) {
                w.online = false;
            }
        });
        tunnelCloseWing(ev.wing_id);
    }

    setCachedWings(S.wingsData.filter(function(w) {
        return w.tunnel_error !== 'not_allowed' && w.tunnel_error !== 'unreachable';
    }).map(function(w) {
        return { wing_id: w.wing_id, public_key: w.public_key, wing_label: w.wing_label };
    }));
    updateHeaderStatus();

    // wing.online: probe first, then render
    var evWing = S.wingsData.find(function(w) { return w.wing_id === ev.wing_id; });
    if (ev.type === 'wing.online' && evWing && evWing.public_key) {
        probeWing(evWing).then(function() {
            rebuildAgentLists();
            if (S.activeView === 'home') renderDashboard();
            if (S.activeView === 'wing-detail' && S.currentWingId === ev.wing_id)
                renderWingDetailPage(ev.wing_id);
            if (DOM.commandPalette.style.display !== 'none') updatePaletteState(true);
        });

        setTimeout(function() { fetchWingSessions(ev.wing_id).then(function(sessions) {
            if (sessions.length > 0) {
                var otherSessions = S.sessionsData.filter(function(s) {
                    return s.wing_id !== sessions[0].wing_id;
                });
                mergeWingSessions(otherSessions.concat(sessions));
                renderSidebar();
                if (S.activeView === 'home') renderDashboard();
            }
        }).catch(function() {}); }, 2000);
    } else {
        // wing.offline or other: render immediately
        rebuildAgentLists();
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
        dot.classList.toggle('dot-live', w.online !== false);
        dot.classList.toggle('dot-offline', w.online === false);
    }
}

export function updateHeaderStatus() {
    var indicator = document.getElementById('wing-indicator');
    if (!indicator) return;
    var anyOnline = S.wingsData.some(function(w) { return w.online !== false; });
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
            if (!seenAgents[a]) { seenAgents[a] = true; S.availableAgents.push({ agent: a, wingId: w.id }); }
        });
        (w.projects || []).forEach(function(p) {
            S.allProjects.push({ name: p.name, path: p.path, wingId: w.id });
        });
    });
}
