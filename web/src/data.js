import { S, DOM, CACHE_KEY, WINGS_CACHE_KEY, LAST_TERM_KEY, WING_ORDER_KEY, EGG_ORDER_KEY, WING_SESSIONS_PREFIX } from './state.js';
import { renderSidebar, renderDashboard } from './render.js';
import { renderWingDetailPage } from './render.js';
import { rebuildAgentLists, updateHeaderStatus } from './dashboard.js';
import { updatePaletteState } from './palette.js';
import { refreshSessionFilesButton } from './session-files.js';
import { sendTunnelRequest, saveTunnelAuthTokens } from './tunnel.js';
import { setNotification, clearNotification } from './notify.js';
import { reconcileWingSessions } from './session-merge.js';
import { notificationForSession, sessionResourceKey, orderSessionReferences } from './session-reference.js';
import { sessionIsSelected, sessionIsViewed, sessionInventoryState } from './session-inventory.js';
import { chatSnapshot } from './chat-view.js';
import { trackSessionCompletions, sessionCompletionObservation } from './session-completion.js';
import { browserLocalStorage } from './storage-scope.js';

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
    return orderSessionReferences(sessions, getEggOrder());
}

export function getCachedWingSessions(wingId) {
    try { var raw = localStorage.getItem(WING_SESSIONS_PREFIX + wingId); return raw ? JSON.parse(raw) : null; }
    catch (e) { return null; }
}
export function setCachedWingSessions(wingId, sessions) {
    try { localStorage.setItem(WING_SESSIONS_PREFIX + wingId, JSON.stringify(sessions)); } catch (e) {}
}

export function saveSessionCache() {
    setCachedSessions(S.sessionsData.map(function(s) {
        return { id: s.id, name: s.name, title: s.title, wing_id: s.wing_id, agent: s.agent, cwd: s.cwd, audit: s.audit, user_id: s.user_id, email: s.email, lifecycle: s.lifecycle, conversation_id: s.conversation_id, root_conversation_id: s.root_conversation_id, parent_conversation_id: s.parent_conversation_id, conversation_role: s.conversation_role };
    }));
}

export function saveWingCache() {
    setCachedWings(S.wingsData.map(function(w) {
        return { wing_id: w.wing_id, public_key: w.public_key, wing_label: w.wing_label, hostname: w.hostname, platform: w.platform, agents: w.agents, locked: w.locked || false, spectate: w.spectate || false, user_id: w.user_id, owner: w.owner };
    }));
}

// Crypto error toast — single dismissable popup with incrementing counter

var _cryptoToastCount = 0;
var _cryptoToastTimer = null;

function showCryptoToast(message) {
    _cryptoToastCount++;
    var existing = document.getElementById('crypto-error-toast');
    if (existing) {
        existing.querySelector('.crypto-error-count').textContent = '(' + _cryptoToastCount + ')';
        existing.querySelector('.crypto-error-msg').textContent = message;
        clearTimeout(_cryptoToastTimer);
        _cryptoToastTimer = setTimeout(function() { dismissCryptoToast(); }, 30000);
        return;
    }
    var toast = document.createElement('div');
    toast.id = 'crypto-error-toast';
    toast.className = 'crypto-error-toast';
    var label = document.createElement('span');
    label.className = 'crypto-error-label';
    label.textContent = 'crypto error';
    var count = document.createElement('span');
    count.className = 'crypto-error-count';
    count.textContent = '(' + _cryptoToastCount + ')';
    var msg = document.createElement('span');
    msg.className = 'crypto-error-msg';
    msg.textContent = String(message);
    var dismiss = document.createElement('button');
    dismiss.className = 'crypto-error-dismiss';
    dismiss.type = 'button';
    dismiss.textContent = '\u00d7';
    toast.append(label, ' ', count, ' ', msg, dismiss);
    document.body.appendChild(toast);
    toast.querySelector('.crypto-error-dismiss').addEventListener('click', function() { dismissCryptoToast(); });
    _cryptoToastTimer = setTimeout(function() { dismissCryptoToast(); }, 30000);
}

function dismissCryptoToast() {
    var el = document.getElementById('crypto-error-toast');
    if (el) el.remove();
    _cryptoToastCount = 0;
    clearTimeout(_cryptoToastTimer);
    _cryptoToastTimer = null;
}

// Tunnel probe — populates wing metadata or sets tunnel_error
// Deduplicate: if a probe is already in-flight for this wing, return the same promise

var _probeInflight = new Map();

// Invalidate the result without changing transport ownership. A late response
// or rejection must not edit a reconnected wing or a replacement account.
export function cancelWingProbe(wingId) {
    _probeInflight.delete(wingId);
}

function currentWingProbe(w, probe) {
    return _probeInflight.get(w.wing_id) === probe && w.online !== false &&
        S.wingsData.find(function(wing) { return wing.wing_id === w.wing_id; }) === w &&
        w.public_key === probe.publicKey && S.currentUser === probe.user;
}

export function probeWing(w) {
    if (w.online === false || S.wingsData.find(function(wing) { return wing.wing_id === w.wing_id; }) !== w) {
        return Promise.resolve();
    }
    var existing = _probeInflight.get(w.wing_id);
    if (existing && currentWingProbe(w, existing)) return existing.promise;
    var probe = { publicKey: w.public_key, user: S.currentUser, promise: null };
    _probeInflight.set(w.wing_id, probe);
    probe.promise = _probeWingInner(w, probe).finally(function() {
        if (_probeInflight.get(w.wing_id) === probe) _probeInflight.delete(w.wing_id);
    });
    return probe.promise;
}

async function _probeWingInner(w, probe) {
    try {
        var data = await sendTunnelRequest(w.wing_id, { type: 'wing.info' }, { skipPasskey: true });
        if (!currentWingProbe(w, probe)) return;
        w.hostname = data.hostname || w.hostname;
        w.platform = data.platform || w.platform;
        w.version = data.version || w.version;
        if (data.wing_label) w.wing_label = data.wing_label;
        w.agents = data.agents || [];
        w.projects = data.projects || [];
        w.locked = data.locked || false;
        w.spectate = data.spectate || false;
        w.capabilities = Array.isArray(data.capabilities) ? data.capabilities : [];
        w.exports = Array.isArray(data.exports) ? data.exports : [];
        w.file_limits = data.file_limits && typeof data.file_limits === 'object' ? data.file_limits : {};
        w.allowed_count = data.allowed_count || 0;
        w.passkey_enrolled = !!data.passkey_enrolled;
        delete w.tunnel_error;
    } catch (e) {
        if (!currentWingProbe(w, probe)) return;
        var msg = e.message || '';
        if (msg.indexOf('not_allowed') !== -1) {
            w.tunnel_error = 'not_allowed';
            delete S.tunnelAuthTokens[w.wing_id];
            saveTunnelAuthTokens();
            // Clear sessions from revoked wing
            S.sessionsData = S.sessionsData.filter(function(s) { return s.wing_id !== w.wing_id; });
            saveSessionCache();
        } else if (msg.indexOf('passkey_required') !== -1) {
            w.tunnel_error = (S.currentUser && !S.currentUser.has_passkeys) ? 'no_passkeys_configured' : 'passkey_required';
            if (e.metadata) {
                w.hostname = e.metadata.hostname || w.hostname;
                w.platform = e.metadata.platform || w.platform;
                w.version = e.metadata.version || w.version;
                w.locked = e.metadata ? (e.metadata.locked !== false) : true;
            }
        } else if (msg.indexOf('decrypt') !== -1) {
            console.error('[wt] tunnel decrypt failed for', w.wing_id, '— clearing cached key');
            w.tunnel_error = 'key_mismatch';
            delete w.public_key;
            showCryptoToast(msg);
        } else {
            console.error('[wt] wing probe failed for', w.wing_id, e);
            w.tunnel_error = 'unreachable';
            showCryptoToast(msg || 'unknown tunnel error');
        }
    }
}

// Data loading

export async function fetchWingSessions(wingId) {
    try {
        var result = await sendTunnelRequest(wingId, { type: 'sessions.list' }, { skipPasskey: true });
        return (result.sessions || []).map(function(s) {
            return { id: s.session_id, name: s.name, title: s.title, wing_id: (S.wingsData.find(function(w) { return w.wing_id === wingId; }) || {}).wing_id || '', agent: s.agent, cwd: s.cwd, status: 'detached', needs_attention: s.needs_attention, audit: s.audit, user_id: s.user_id, email: s.email, lifecycle: s.lifecycle, lifecycle_seen_at: Date.now(), conversation_id: s.conversation_id, root_conversation_id: s.root_conversation_id, parent_conversation_id: s.parent_conversation_id, conversation_role: s.conversation_role, forkable: s.forkable, fork_unavailable_reason: s.fork_unavailable_reason };
        });
    } catch (e) { return null; }
}

export function mergeWingSessions(wingId, remoteSessions) {
    S.sessionsData = sortSessionsByOrder(reconcileWingSessions(S.sessionsData, wingId, remoteSessions));
    var wing = S.wingsData.find(function(wing) { return wing.wing_id === wingId; });
    var chat = chatSnapshot();
    trackSessionCompletions(browserLocalStorage(), S.currentUser && S.currentUser.id, remoteSessions.map(function(session) {
        var seen = sessionIsViewed(session, S, document.visibilityState === 'visible', chat && chat.target);
        return sessionCompletionObservation(session, sessionInventoryState(session, wing).status, seen);
    }));
    setEggOrder(S.sessionsData.map(sessionResourceKey));
    saveSessionCache();
}

var _loadHomeLock = false;

export async function loadHome() {
    if (_loadHomeLock) return;
    _loadHomeLock = true;
    try { await _loadHomeInner(); } finally { _loadHomeLock = false; }
}

async function _loadHomeInner() {
    // Never render a relayed terminal cache to an account whose current policy
    // denies browser payload transport, even briefly while wing probes run.
    if (S.currentUser && S.currentUser.relay_allowed === false) {
        S.sessionsData = [];
        setCachedSessions([]);
    }
    // Step 1: Hydrate wings from cache if empty (online=undefined for gray dots)
    if (S.wingsData.length === 0) {
        var cached = getCachedWings();
        S.wingsData = sortWingsByOrder(cached);
    }

    // Step 1b: Hydrate sessions from cache if empty
    if (S.sessionsData.length === 0 && !(S.currentUser && S.currentUser.relay_allowed === false)) {
        var cachedSessions = getCachedSessions();
        if (cachedSessions.length > 0) {
            cachedSessions.forEach(function(s) { s.status = 'detached'; s.swept = true; delete s.lifecycle_seen_at; });
            S.sessionsData = sortSessionsByOrder(cachedSessions);
        }
    }

    // Step 2: Render NOW (gray dots, instant from cache)
    rebuildAgentLists();
    updateHeaderStatus();
    renderSidebar();
    refreshSessionFilesButton();
    if (S.activeView === 'home') renderDashboard();
    if (S.activeView === 'wing-detail' && S.currentWingId) renderWingDetailPage(S.currentWingId);

    // Step 3: Fetch online wings from API
    var apiWings = [];
    try {
        var wingsResp = await fetch('/api/app/wings');
        if (wingsResp.ok) apiWings = await wingsResp.json() || [];
    } catch (e) {}

    // Step 4: In-place merge (sets online = true → green, false → yellow)
    var apiMap = {};
    apiWings.forEach(function(aw) { apiMap[aw.wing_id] = aw; });
    var rosterIds = {};
    S.wingsData.forEach(function(w) {
        rosterIds[w.wing_id] = true;
        var aw = apiMap[w.wing_id];
        if (aw) {
            w.online = true;
            if (aw.public_key) w.public_key = aw.public_key;
            if (aw.user_id) w.user_id = aw.user_id;
            if (aw.owner) w.owner = aw.owner;
        } else {
            w.online = false;
            cancelWingProbe(w.wing_id);
        }
    });

    // Add new wings from API not already in roster
    var added = false;
    var cache = getCachedWings();
    var cacheMap = {};
    cache.forEach(function(c) { cacheMap[c.wing_id] = c; });
    apiWings.forEach(function(aw) {
        if (!rosterIds[aw.wing_id]) {
            var c = cacheMap[aw.wing_id];
            S.wingsData.push({
                wing_id: aw.wing_id,
                online: true,
                public_key: aw.public_key,
                wing_label: c ? c.wing_label : undefined,
                hostname: c ? c.hostname : undefined,
                platform: c ? c.platform : undefined,
                agents: (c && c.agents) || [],
                projects: [],
                user_id: aw.user_id,
                owner: aw.owner,
            });
            added = true;
        }
    });
    if (added) S.wingsData = sortWingsByOrder(S.wingsData);

    S.wingsData.forEach(function(w) {
        if (w.latest_version) S.latestVersion = w.latest_version;
    });

    // Step 5: Save cache, render (green/yellow dots)
    saveWingCache();
    rebuildAgentLists();
    updateHeaderStatus();
    renderSidebar();
    if (S.activeView === 'home') renderDashboard();
    if (S.activeView === 'wing-detail' && S.currentWingId) renderWingDetailPage(S.currentWingId);

    // Step 6: Probe all online wings in parallel
    var onlineWings = S.wingsData.filter(function(w) { return w.online !== false && w.wing_id && w.public_key; });
    await Promise.all(onlineWings.map(function(w) { return probeWing(w); }));

    // Step 7: Save cache, render once after all probes
    saveWingCache();
    rebuildAgentLists();
    renderSidebar();
    refreshSessionFilesButton();
    if (S.activeView === 'home') renderDashboard();
    if (S.activeView === 'wing-detail' && S.currentWingId) renderWingDetailPage(S.currentWingId);
    if (DOM.commandPalette.style.display !== 'none') updatePaletteState(true);

    // Direct-only hosted accounts get coordination and bounded wing.info probes
    // here, not browser terminal/session relaying. Do not render stale terminal
    // cache entries or request the session APIs that the server will deny.
    if (S.currentUser && S.currentUser.relay_allowed === false) {
        S.sessionsData = [];
        setCachedSessions([]);
        renderSidebar();
        if (S.activeView === 'home') renderDashboard();
        return;
    }

    // Step 8: Fetch sessions from accessible wings in parallel
    var accessibleWings = onlineWings.filter(function(w) { return !w.tunnel_error; });
    var allResults = await Promise.all(accessibleWings.map(function(w) {
        return fetchWingSessions(w.wing_id).then(function(sessions) {
            return { wing_id: w.wing_id, sessions: sessions };
        });
    }));

    // Step 9: Per-wing merge, notifications, render once
    allResults.forEach(function(r) {
        if (r.sessions !== null) mergeWingSessions(r.wing_id, r.sessions);
    });

    S.sessionsData.forEach(function(s) {
        var blocked = sessionInventoryState(s, S.wingsData.find(function(wing) { return wing.wing_id === s.wing_id; })).status === 'blocked';
        if (s.needs_attention && !sessionIsSelected(s, S.ptySessionId, S.ptyWingId)) {
            setNotification(s.id, s.wing_id);
        } else if (!s.needs_attention && !blocked && notificationForSession(S.sessionNotifications, s)) {
            clearNotification(s.id, s.wing_id);
        }
    });

    renderSidebar();
    if (S.activeView === 'home') renderDashboard();
}
