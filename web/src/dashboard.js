import { S, DOM } from './state.js';
import { wingDisplayName } from './helpers.js';
import { renderDashboard, renderSidebar, renderWingDetailPage } from './render.js';
import { setCachedWings, fetchWingSessions, mergeWingSessions, loadHome } from './data.js';
import { updatePaletteState } from './palette.js';
import { sendTunnelRequest } from './tunnel.js';

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
                if (w.updating_at && ev.version && ev.version !== w.version) {
                    delete w.updating_at;
                }
                w.id = ev.wing_id;
                w.hostname = ev.hostname || w.hostname;
                w.agents = ev.agents || w.agents;
                w.labels = ev.labels || w.labels;
                w.platform = ev.platform || w.platform;
                w.version = ev.version || w.version;
                w.public_key = ev.public_key || w.public_key;
                w.projects = ev.projects || w.projects;
                if (ev.pinned !== undefined) w.pinned = ev.pinned;
                if (ev.pinned_count !== undefined) w.pinned_count = ev.pinned_count;
                found = true;
            }
        });
        if (!found) {
            S.wingsData.push({
                id: ev.wing_id,
                wing_id: ev.wing_id,
                hostname: ev.hostname || '',
                platform: ev.platform || '',
                version: ev.version || '',
                online: true,
                agents: ev.agents || [],
                labels: ev.labels || [],
                public_key: ev.public_key,
                projects: ev.projects || [],
                pinned: ev.pinned || false,
                pinned_count: ev.pinned_count || 0,
            });
            needsFullRender = true;
        }
    } else if (ev.type === 'wing.offline') {
        S.wingsData.forEach(function(w) {
            if (w.wing_id === ev.wing_id) {
                w.online = false;
            }
        });
        var staleWs = S.tunnelWsMap[ev.wing_id];
        if (staleWs) {
            delete S.tunnelWsMap[ev.wing_id];
            try { staleWs.close(); } catch(e) {}
        }
    }

    rebuildAgentLists();
    setCachedWings(S.wingsData.map(function(w) {
        return { wing_id: w.wing_id, hostname: w.hostname, id: w.id, platform: w.platform, version: w.version, agents: w.agents, labels: w.labels, projects: w.projects, wing_label: w.wing_label };
    }));
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
    if (DOM.commandPalette.style.display !== 'none') {
        updatePaletteState(true);
    }

    var evWing = S.wingsData.find(function(w) { return w.wing_id === ev.wing_id; });
    if (ev.type === 'wing.online' && ev.wing_id && !(evWing && evWing.pinned)) {
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
