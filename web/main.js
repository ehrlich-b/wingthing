import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import { SerializeAddon } from '@xterm/addon-serialize';
import '@xterm/xterm/css/xterm.css';
import { x25519 } from '@noble/curves/ed25519.js';
import { createAiChat } from '@nlux/core';
import '@nlux/themes/nova.css';

// === State ===
let ptyWs = null;
let ptySessionId = null;
let term = null;
let fitAddon = null;
let serializeAddon = null;
let saveBufferTimer = null;
let ctrlActive = false;
let altActive = false;
let currentUser = null;
let e2eKey = null;
let ephemeralPrivKey = null;
let availableAgents = [];
let allProjects = [];
let wingsData = [];
let sessionsData = [];
let sessionNotifications = {};
let activeView = 'home';
let titleFlashTimer = null;
let appWs = null;

// Chat state
let chatWs = null;
let chatSessionId = null;
let chatObserver = null;
let chatInstance = null;
let pendingHistory = null;

// DOM refs
const sessionTabs = document.getElementById('session-tabs');
const newSessionBtn = document.getElementById('new-session-btn');
const homeBtn = document.getElementById('home-btn');
const headerLogo = document.getElementById('header-logo');
const headerTitle = document.getElementById('header-title');
const userInfo = document.getElementById('user-info');
const homeSection = document.getElementById('home-section');
const wingStatusEl = document.getElementById('wing-status');
const sessionsList = document.getElementById('sessions-list');
const emptyState = document.getElementById('empty-state');
const terminalSection = document.getElementById('terminal-section');
const terminalContainer = document.getElementById('terminal-container');
const ptyStatus = document.getElementById('pty-status');
const chatSection = document.getElementById('chat-section');
const chatContainer = document.getElementById('chat-container');
const chatStatus = document.getElementById('chat-status');
const chatDeleteBtn = document.getElementById('chat-delete-btn');

// Palette refs
const commandPalette = document.getElementById('command-palette');
const paletteBackdrop = document.getElementById('palette-backdrop');
const paletteDialog = document.getElementById('palette-dialog');
const paletteSearch = document.getElementById('palette-search');
const paletteResults = document.getElementById('palette-results');
const paletteStatus = document.getElementById('palette-status');
const paletteHints = document.getElementById('palette-hints');

// localStorage keys
var CACHE_KEY = 'wt_sessions';
var WINGS_CACHE_KEY = 'wt_wings';
var LAST_TERM_KEY = 'wt_last_term_agent';
var LAST_CHAT_KEY = 'wt_last_chat_agent';
var TERM_BUF_PREFIX = 'wt_termbuf_';
var WING_ORDER_KEY = 'wt_wing_order';
var TERM_THUMB_PREFIX = 'wt_termthumb_';

function loginRedirect() {
    var host = window.location.hostname.replace(/^app\./, '');
    var port = window.location.port ? ':' + window.location.port : '';
    var loginUrl = window.location.protocol + '//' + host + port +
        '/login?next=' + encodeURIComponent(window.location.origin + '/');
    window.location.href = loginUrl;
}

// === Init ===

async function init() {
    try {
        var resp = await fetch('/api/app/me');
        if (resp.status === 401) { loginRedirect(); return; }
        currentUser = await resp.json();
        userInfo.textContent = currentUser.display_name || 'user';
    } catch (e) { loginRedirect(); return; }

    // Request notification permission
    if ('Notification' in window && Notification.permission === 'default') {
        Notification.requestPermission();
    }

    // Event handlers
    homeBtn.addEventListener('click', showHome);
    newSessionBtn.addEventListener('click', showPalette);

    chatDeleteBtn.addEventListener('click', function () {
        if (chatSessionId) {
            var cached = getCachedSessions().filter(function (s) { return s.id !== chatSessionId; });
            setCachedSessions(cached);
            fetch('/api/app/sessions/' + chatSessionId, { method: 'DELETE' });
            destroyChat();
            showHome();
        }
    });

    // Modifier keys
    document.querySelectorAll('.mod-key').forEach(function (btn) {
        btn.addEventListener('click', function (e) {
            e.preventDefault();
            var key = btn.dataset.key;
            if (key === 'ctrl') {
                ctrlActive = !ctrlActive;
                btn.classList.toggle('active', ctrlActive);
            } else if (key === 'alt') {
                altActive = !altActive;
                btn.classList.toggle('active', altActive);
            } else if (key === 'esc') {
                sendPTYInput('\x1b');
            } else if (key === 'tab') {
                sendPTYInput('\t');
            }
            var seq = btn.dataset.seq;
            if (seq === '\u2191') sendPTYInput('\x1b[A');
            if (seq === '\u2193') sendPTYInput('\x1b[B');
            if (term) term.focus();
        });
    });

    // Keyboard shortcuts
    document.addEventListener('keydown', function(e) {
        if ((e.metaKey || e.ctrlKey) && e.key === 'k') {
            e.preventDefault();
            if (commandPalette.style.display === 'none') showPalette();
            else hidePalette();
        }
        // "." or "+" opens palette when nothing is focused
        if ((e.key === '.' || e.key === '+') && commandPalette.style.display === 'none') {
            var tag = document.activeElement && document.activeElement.tagName;
            if (tag !== 'INPUT' && tag !== 'TEXTAREA' && tag !== 'SELECT' && !document.activeElement.closest('#terminal-container, #chat-container')) {
                e.preventDefault();
                showPalette();
            }
        }
        if (e.key === 'Escape' && commandPalette.style.display !== 'none') {
            hidePalette();
        }
        // Ctrl+. = go back to dashboard from any view
        if ((e.ctrlKey || e.metaKey) && e.key === '.' && activeView !== 'home') {
            e.preventDefault();
            showHome();
        }
    });

    // Palette events
    paletteBackdrop.addEventListener('click', hidePalette);
    paletteSearch.addEventListener('input', function() {
        debouncedDirList(paletteSearch.value);
    });
    paletteSearch.addEventListener('keydown', function(e) {
        if (e.key === 'Enter') {
            e.preventDefault();
            if (dirListPending) return; // wait for results
            var selected = paletteResults.querySelector('.palette-item.selected');
            if (selected) launchFromPalette(selected.dataset.path);
        }
        if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
            e.preventDefault();
            navigatePalette(e.key === 'ArrowDown' ? 1 : -1);
        }
        if (e.key === 'Tab') {
            e.preventDefault();
            if (e.shiftKey) {
                cyclePaletteMode();
            } else {
                cyclePaletteWing();
            }
        }
        if (e.key === '`') {
            e.preventDefault();
            cyclePaletteAgent();
        }
    });

    window.addEventListener('resize', function () {
        if (term && fitAddon) fitAddon.fit();
    });

    initTerminal();
    loadHome();
    setInterval(loadHome, 10000);
    connectAppWS();
}

// === localStorage helpers ===

function getLastTermAgent() {
    try { return localStorage.getItem(LAST_TERM_KEY) || 'claude'; } catch (e) { return 'claude'; }
}
function setLastTermAgent(agent) {
    try { localStorage.setItem(LAST_TERM_KEY, agent); } catch (e) {}
}
function getLastChatAgent() {
    try { return localStorage.getItem(LAST_CHAT_KEY) || 'claude'; } catch (e) { return 'claude'; }
}
function setLastChatAgent(agent) {
    try { localStorage.setItem(LAST_CHAT_KEY, agent); } catch (e) {}
}
function getCachedSessions() {
    try { var raw = localStorage.getItem(CACHE_KEY); return raw ? JSON.parse(raw) : []; }
    catch (e) { return []; }
}
function setCachedSessions(sessions) {
    try { localStorage.setItem(CACHE_KEY, JSON.stringify(sessions)); } catch (e) {}
}
function getCachedWings() {
    try { var raw = localStorage.getItem(WINGS_CACHE_KEY); return raw ? JSON.parse(raw) : []; }
    catch (e) { return []; }
}
function setCachedWings(wings) {
    try { localStorage.setItem(WINGS_CACHE_KEY, JSON.stringify(wings)); } catch (e) {}
}
function getWingOrder() {
    try { var raw = localStorage.getItem(WING_ORDER_KEY); return raw ? JSON.parse(raw) : []; }
    catch (e) { return []; }
}
function setWingOrder(order) {
    try { localStorage.setItem(WING_ORDER_KEY, JSON.stringify(order)); } catch (e) {}
}
function sortWingsByOrder(wings) {
    var order = getWingOrder();
    var orderMap = {};
    order.forEach(function(id, i) { orderMap[id] = i; });
    // Known wings keep stored position, unknown go to end
    var known = [];
    var unknown = [];
    wings.forEach(function(w) {
        if (orderMap.hasOwnProperty(w.machine_id)) {
            known.push(w);
        } else {
            unknown.push(w);
        }
    });
    known.sort(function(a, b) { return orderMap[a.machine_id] - orderMap[b.machine_id]; });
    return known.concat(unknown);
}

// === Dashboard WebSocket (real-time wing status) ===

function connectAppWS() {
    if (appWs) { try { appWs.close(); } catch(e) {} }
    var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    appWs = new WebSocket(proto + '//' + location.host + '/ws/app');
    appWs.onmessage = function(e) {
        try { applyWingEvent(JSON.parse(e.data)); } catch(err) {}
    };
    appWs.onclose = function() {
        appWs = null;
        setTimeout(connectAppWS, 3000);
    };
    appWs.onerror = function() { appWs.close(); };
}

function applyWingEvent(ev) {
    if (ev.type === 'wing.online') {
        var found = false;
        wingsData.forEach(function(w) {
            if (w.machine_id === ev.machine_id) {
                w.online = true;
                w.id = ev.wing_id;
                w.agents = ev.agents || w.agents;
                w.labels = ev.labels || w.labels;
                w.platform = ev.platform || w.platform;
                w.public_key = ev.public_key || w.public_key;
                w.projects = ev.projects || w.projects;
                found = true;
            }
        });
        if (!found) {
            // New wing goes to end
            wingsData.push({
                id: ev.wing_id,
                machine_id: ev.machine_id,
                platform: ev.platform || '',
                online: true,
                agents: ev.agents || [],
                labels: ev.labels || [],
                public_key: ev.public_key,
                projects: ev.projects || [],
            });
        }
    } else if (ev.type === 'wing.offline') {
        wingsData.forEach(function(w) {
            if (w.machine_id === ev.machine_id) {
                w.online = false;
            }
        });
    }

    rebuildAgentLists();
    setCachedWings(wingsData.map(function(w) {
        return { machine_id: w.machine_id, id: w.id, platform: w.platform, agents: w.agents, labels: w.labels, projects: w.projects };
    }));
    if (activeView === 'home') {
        renderDashboard();
        // Subtle dot ping on status change
        pingWingDot(ev.machine_id);
    }
    if (commandPalette.style.display !== 'none') {
        updatePaletteState(true);
    }
}

function pingWingDot(machineId) {
    requestAnimationFrame(function() {
        var card = wingStatusEl.querySelector('.wing-box[data-machine-id="' + machineId + '"]');
        if (!card) return;
        var dot = card.querySelector('.wing-dot');
        if (!dot) return;
        dot.classList.remove('dot-ping');
        void dot.offsetWidth;
        dot.classList.add('dot-ping');
    });
}

function rebuildAgentLists() {
    availableAgents = [];
    allProjects = [];
    var seenAgents = {};
    wingsData.forEach(function(w) {
        if (w.online === false) return;
        (w.agents || []).forEach(function(a) {
            if (!seenAgents[a]) { seenAgents[a] = true; availableAgents.push({ agent: a, wingId: w.id }); }
        });
        (w.projects || []).forEach(function(p) {
            allProjects.push({ name: p.name, path: p.path, wingId: w.id, machine: w.machine_id });
        });
    });
}

// === Data Loading ===

async function loadHome() {
    var sessions = [];
    var wings = [];
    try {
        var [sessResp, wingsResp] = await Promise.all([
            fetch('/api/app/sessions'),
            fetch('/api/app/wings'),
        ]);
        if (sessResp.ok) sessions = await sessResp.json() || [];
        if (wingsResp.ok) wings = await wingsResp.json() || [];
    } catch (e) {
        console.error('load home:', e);
        return;
    }

    sessionsData = sessions;
    setCachedSessions(sessions);

    // Merge live wings with cached wings (stable by machine_id)
    var cached = getCachedWings();
    var merged = {};
    wings.forEach(function (w) {
        w.online = true;
        merged[w.machine_id] = w;
    });
    cached.forEach(function (w) {
        if (!merged[w.machine_id]) {
            w.online = false;
            merged[w.machine_id] = w;
        }
    });
    wingsData = sortWingsByOrder(Object.values(merged));

    // Cache for next load (only essential fields)
    setCachedWings(wingsData.map(function (w) {
        return { machine_id: w.machine_id, id: w.id, platform: w.platform, agents: w.agents, labels: w.labels, projects: w.projects };
    }));

    rebuildAgentLists();

    renderSidebar();
    if (activeView === 'home') renderDashboard();

    // Refresh palette if open (wing may have come online)
    if (commandPalette.style.display !== 'none') {
        updatePaletteState(true);
    }
}

// === Rendering ===

function projectName(cwd) {
    if (!cwd) return '~';
    var parts = cwd.split('/').filter(Boolean);
    return parts[parts.length - 1] || '~';
}

function renderSidebar() {
    var tabs = sessionsData.map(function(s) {
        var name = projectName(s.cwd);
        var letter = name.charAt(0).toUpperCase();
        var isActive = (activeView === 'terminal' && s.id === ptySessionId) ||
                       (activeView === 'chat' && s.id === chatSessionId);
        var needsAttention = sessionNotifications[s.id];
        var dotClass = s.status === 'active' ? 'dot-live' : 'dot-detached';
        if (needsAttention) dotClass = 'dot-attention';
        var kind = s.kind || 'terminal';
        return '<button class="session-tab' + (isActive ? ' active' : '') + '" ' +
            'title="' + escapeHtml(name + ' \u00b7 ' + (s.agent || '?')) + '" ' +
            'data-sid="' + s.id + '" data-kind="' + kind + '" data-agent="' + escapeHtml(s.agent || 'claude') + '">' +
            '<span class="tab-letter">' + escapeHtml(letter) + '</span>' +
            '<span class="tab-dot ' + dotClass + '"></span>' +
        '</button>';
    }).join('');
    sessionTabs.innerHTML = tabs;

    sessionTabs.querySelectorAll('.session-tab').forEach(function(tab) {
        tab.addEventListener('click', function() {
            var sid = tab.dataset.sid;
            var kind = tab.dataset.kind;
            var agent = tab.dataset.agent;
            // Don't reconnect if already viewing this session
            if (kind === 'chat' && sid === chatSessionId && activeView === 'chat') return;
            if (kind !== 'chat' && sid === ptySessionId && activeView === 'terminal') return;
            if (kind === 'chat') {
                window._openChat(sid, agent);
            } else {
                switchToSession(sid);
            }
        });
    });
}

function setupWingDrag() {
    var grid = wingStatusEl.querySelector('.wing-grid');
    if (!grid) return;
    var cards = grid.querySelectorAll('.wing-box');
    var dragSrc = null;

    // Desktop drag
    cards.forEach(function(card) {
        card.addEventListener('dragstart', function(e) {
            dragSrc = card;
            card.classList.add('dragging');
            e.dataTransfer.effectAllowed = 'move';
            e.dataTransfer.setData('text/plain', card.dataset.machineId);
        });
        card.addEventListener('dragend', function() {
            card.classList.remove('dragging');
            cards.forEach(function(c) { c.classList.remove('drag-over'); });
            dragSrc = null;
        });
        card.addEventListener('dragover', function(e) {
            e.preventDefault();
            e.dataTransfer.dropEffect = 'move';
            if (card !== dragSrc) {
                cards.forEach(function(c) { c.classList.remove('drag-over'); });
                card.classList.add('drag-over');
            }
        });
        card.addEventListener('dragleave', function() {
            card.classList.remove('drag-over');
        });
        card.addEventListener('drop', function(e) {
            e.preventDefault();
            card.classList.remove('drag-over');
            if (!dragSrc || dragSrc === card) return;
            grid.insertBefore(dragSrc, card);
            saveWingOrder();
        });
    });

    // Touch drag (mobile)
    var touchSrc = null;

    cards.forEach(function(card) {
        card.addEventListener('touchstart', function(e) {
            if (e.target.closest('.wing-update-btn')) return;
            touchSrc = card;
            card.classList.add('dragging');
        }, { passive: true });
    });

    grid.addEventListener('touchmove', function(e) {
        if (!touchSrc) return;
        e.preventDefault();
        var touch = e.touches[0];
        var target = document.elementFromPoint(touch.clientX, touch.clientY);
        var targetCard = target ? target.closest('.wing-box') : null;
        cards.forEach(function(c) { c.classList.remove('drag-over'); });
        if (targetCard && targetCard !== touchSrc) {
            targetCard.classList.add('drag-over');
        }
    }, { passive: false });

    grid.addEventListener('touchend', function(e) {
        if (!touchSrc) return;
        var touch = e.changedTouches[0];
        var target = document.elementFromPoint(touch.clientX, touch.clientY);
        var targetCard = target ? target.closest('.wing-box') : null;
        cards.forEach(function(c) { c.classList.remove('drag-over'); });
        touchSrc.classList.remove('dragging');
        if (targetCard && targetCard !== touchSrc) {
            grid.insertBefore(touchSrc, targetCard);
            saveWingOrder();
        }
        touchSrc = null;
    }, { passive: true });
}

function saveWingOrder() {
    var order = [];
    wingStatusEl.querySelectorAll('.wing-box').forEach(function(card) {
        if (card.dataset.machineId) order.push(card.dataset.machineId);
    });
    setWingOrder(order);
    // Sync wingsData to match DOM order
    var byMachine = {};
    wingsData.forEach(function(w) { byMachine[w.machine_id] = w; });
    var reordered = [];
    order.forEach(function(mid) { if (byMachine[mid]) reordered.push(byMachine[mid]); });
    // Add any not in order (shouldn't happen, but defensive)
    wingsData.forEach(function(w) { if (order.indexOf(w.machine_id) === -1) reordered.push(w); });
    wingsData = reordered;
}

function renderDashboard() {
    // Wing boxes
    if (wingsData.length > 0) {
        var wingHtml = '<h3 class="section-label">wings</h3><div class="wing-grid">';
        wingHtml += wingsData.map(function(w) {
            var name = w.machine_id || w.id.substring(0, 8);
            var dotClass = w.online !== false ? 'dot-live' : 'dot-offline';
            var projectCount = (w.projects || []).length;
            var plat = w.platform === 'darwin' ? 'mac' : (w.platform || '');
            return '<div class="wing-box" draggable="true" data-machine-id="' + escapeHtml(w.machine_id || '') + '">' +
                '<div class="wing-box-top">' +
                    '<span class="wing-dot ' + dotClass + '"></span>' +
                    '<span class="wing-name">' + escapeHtml(name) + '</span>' +
                '</div>' +
                '<span class="wing-agents">' + escapeHtml((w.agents || []).join(', ')) + '</span>' +
                '<div class="wing-statusbar">' +
                    '<span>' + escapeHtml(plat) + '</span>' +
                    (projectCount ? '<span>' + projectCount + ' proj</span>' : '<span></span>') +
                '</div>' +
            '</div>';
        }).join('');
        wingHtml += '</div>';
        wingStatusEl.innerHTML = wingHtml;

        setupWingDrag();
    } else {
        wingStatusEl.innerHTML = '';
    }

    // Egg boxes (sessions)
    var hasSessions = sessionsData.length > 0;
    emptyState.style.display = hasSessions ? 'none' : '';

    if (!hasSessions) {
        sessionsList.innerHTML = '';
        return;
    }

    var eggHtml = '<h3 class="section-label">eggs</h3><div class="egg-grid">';
    eggHtml += sessionsData.map(function(s) {
        var name = projectName(s.cwd);
        var isActive = s.status === 'active';
        var kind = s.kind || 'terminal';
        var needsAttention = sessionNotifications[s.id];
        var dotClass = isActive ? 'live' : 'detached';
        if (needsAttention) dotClass = 'attention';

        var thumbUrl = '';
        try { thumbUrl = localStorage.getItem(TERM_THUMB_PREFIX + s.id) || ''; } catch(e) {}
        var previewHtml = thumbUrl
            ? '<img src="' + thumbUrl + '" alt="">'
            : '';

        return '<div class="egg-box" data-sid="' + s.id + '" data-kind="' + kind + '" data-agent="' + escapeHtml(s.agent || 'claude') + '">' +
            '<div class="egg-preview">' + previewHtml + '</div>' +
            '<div class="egg-footer">' +
                '<span class="session-dot ' + dotClass + '"></span>' +
                '<span class="egg-label">' + escapeHtml(name) + ' \u00b7 ' + escapeHtml(s.agent || '?') +
                    (needsAttention ? ' \u00b7 !' : '') + '</span>' +
                '<button class="btn-sm btn-danger egg-delete" data-sid="' + s.id + '">x</button>' +
            '</div>' +
        '</div>';
    }).join('');
    eggHtml += '</div>';
    sessionsList.innerHTML = eggHtml;

    sessionsList.querySelectorAll('.egg-box').forEach(function(card) {
        card.addEventListener('click', function(e) {
            if (e.target.closest('.egg-delete')) return;
            var sid = card.dataset.sid;
            var kind = card.dataset.kind;
            var agent = card.dataset.agent;
            if (kind === 'chat') {
                window._openChat(sid, agent);
            } else {
                switchToSession(sid);
            }
        });
    });

    sessionsList.querySelectorAll('.egg-delete').forEach(function(btn) {
        btn.addEventListener('click', function(e) {
            e.stopPropagation();
            window._deleteSession(btn.dataset.sid);
        });
    });
}

// === Command Palette ===

var paletteMode = 'terminal'; // 'terminal' or 'chat'
var paletteWingIndex = 0;
var paletteAgentIndex = 0;
var paletteSelectedIndex = 0;
var dirListTimer = null;
var dirListAbort = null;
var dirListPending = false; // true while waiting for remote dir results
var dirListCache = [];      // last server results (full entries)
var dirListCacheDir = '';    // the directory those results are for
var dirListQuery = '';       // current query string for stale-check

function currentPaletteWing() {
    var online = onlineWings();
    if (online.length === 0) return null;
    return online[paletteWingIndex % online.length];
}

function currentPaletteAgents() {
    var wing = currentPaletteWing();
    if (!wing || !wing.agents || wing.agents.length === 0) return ['claude'];
    return wing.agents;
}

function currentPaletteAgent() {
    var agents = currentPaletteAgents();
    return agents[paletteAgentIndex % agents.length];
}

function cyclePaletteAgent() {
    var agents = currentPaletteAgents();
    if (agents.length <= 1) return;
    paletteAgentIndex = (paletteAgentIndex + 1) % agents.length;
    renderPaletteStatus();
}

function onlineWings() {
    return wingsData.filter(function(w) { return w.online !== false; });
}

function showPalette() {
    commandPalette.style.display = '';
    paletteSearch.value = '';
    paletteSearch.focus();
    updatePaletteState();
}

function updatePaletteState(isOpen) {
    var online = onlineWings();
    var alive = online.length > 0;
    var wasWaiting = paletteDialog.classList.contains('palette-waiting');

    paletteSearch.disabled = !alive;
    paletteDialog.classList.toggle('palette-waiting', !alive);

    if (alive) {
        if (wasWaiting) {
            paletteDialog.classList.add('palette-awake');
            setTimeout(function() { paletteDialog.classList.remove('palette-awake'); }, 800);
            paletteSearch.focus();
        }
        // Only reset agent selection when palette first opens, not on background updates
        if (!isOpen) {
            var agents = currentPaletteAgents();
            var last = paletteMode === 'chat' ? getLastChatAgent() : getLastTermAgent();
            var idx = agents.indexOf(last);
            paletteAgentIndex = idx >= 0 ? idx : 0;
        }
        renderPaletteStatus();
        if (!isOpen) {
            if (paletteMode === 'chat') {
                paletteResults.innerHTML = '<div class="palette-empty">enter to start chat</div>';
            } else {
                renderPaletteResults(paletteSearch.value);
            }
        }
    } else {
        paletteStatus.innerHTML = '<span class="palette-waiting-text">no wings online</span>';
        paletteResults.innerHTML = '<div class="palette-waiting-msg">' +
            '<div class="waiting-dot"></div>' +
            '<div>no wings online</div>' +
            '<div class="palette-waiting-hint"><a href="https://wingthing.ai/install" target="_blank">install wt</a> and run <code>wt start</code></div>' +
        '</div>';
    }
}

function hidePalette() {
    commandPalette.style.display = 'none';
    if (dirListTimer) { clearTimeout(dirListTimer); dirListTimer = null; }
    if (dirListAbort) { dirListAbort.abort(); dirListAbort = null; }
    dirListPending = false;
    dirListCache = [];
    dirListCacheDir = '';
    dirListQuery = '';
}

function cyclePaletteWing() {
    var online = onlineWings();
    if (online.length <= 1) return;
    paletteWingIndex = (paletteWingIndex + 1) % online.length;
    paletteAgentIndex = 0; // reset agent for new wing
    renderPaletteStatus();
    renderPaletteResults('');
    paletteSearch.value = '';
}

function cyclePaletteMode() {
    paletteMode = paletteMode === 'terminal' ? 'chat' : 'terminal';
    renderPaletteStatus();
    if (paletteMode === 'chat') {
        paletteResults.innerHTML = '<div class="palette-empty">enter to start chat</div>';
    } else {
        renderPaletteResults(paletteSearch.value);
    }
}

function renderPaletteStatus() {
    var wing = currentPaletteWing();
    var wingName = wing ? (wing.machine_id || wing.id.substring(0, 8)) : '?';
    var agent = currentPaletteAgent();
    paletteStatus.innerHTML = '<span class="accent">' + escapeHtml(wingName) + '</span> &middot; ' +
        escapeHtml(paletteMode) + ' &middot; <span class="accent">' + escapeHtml(agent) + '</span>';
}

function renderPaletteResults(filter) {
    var wing = currentPaletteWing();
    var wingId = wing ? wing.id : '';
    var wingProjects = wingId
        ? allProjects.filter(function(p) { return p.wingId === wingId; })
        : allProjects;

    var items = [];

    // Always show "home" option when no filter
    if (!filter) {
        items.push({ name: '~', path: '', isDir: true });
    }

    // Filter projects
    var filtered = wingProjects;
    if (filter) {
        var lower = filter.toLowerCase();
        filtered = wingProjects.filter(function(p) {
            return p.name.toLowerCase().indexOf(lower) !== -1 ||
                   p.path.toLowerCase().indexOf(lower) !== -1;
        });
    }
    filtered.forEach(function(p) {
        items.push({ name: p.name, path: p.path, isDir: true });
    });

    renderPaletteItems(items);
}

function renderPaletteItems(items) {
    paletteSelectedIndex = 0;

    if (items.length === 0) {
        paletteResults.innerHTML = '<div class="palette-empty">no matches</div>';
        return;
    }

    paletteResults.innerHTML = items.map(function(item, i) {
        var icon = item.isDir ? '/' : '';
        return '<div class="palette-item' + (i === 0 ? ' selected' : '') + '" data-path="' + escapeHtml(item.path) + '" data-index="' + i + '">' +
            '<span class="palette-name">' + escapeHtml(item.name) + icon + '</span>' +
            (item.path ? '<span class="palette-path">' + escapeHtml(shortenPath(item.path)) + '</span>' : '') +
        '</div>';
    }).join('');

    paletteResults.querySelectorAll('.palette-item').forEach(function(item) {
        item.addEventListener('click', function() {
            launchFromPalette(item.dataset.path);
        });
        item.addEventListener('mouseenter', function() {
            paletteResults.querySelectorAll('.palette-item').forEach(function(el) { el.classList.remove('selected'); });
            item.classList.add('selected');
            paletteSelectedIndex = parseInt(item.dataset.index);
        });
    });
}

function dirParent(value) {
    // Return the directory portion and trailing prefix for client-side filtering.
    // "~/repos/cin" → { dir: "~/repos/", prefix: "cin" }
    // "~/repos/"    → { dir: "~/repos/", prefix: "" }
    var last = value.lastIndexOf('/');
    if (last === -1) return { dir: value, prefix: '' };
    return { dir: value.substring(0, last + 1), prefix: value.substring(last + 1).toLowerCase() };
}

function filterCachedItems(prefix) {
    var items = dirListCache;
    if (prefix) {
        items = items.filter(function(e) {
            return e.name.toLowerCase().indexOf(prefix) === 0;
        });
    }
    return items;
}

function debouncedDirList(value) {
    if (dirListTimer) clearTimeout(dirListTimer);
    if (paletteMode === 'chat') return;

    // Abort any in-flight fetch immediately on new input
    if (dirListAbort) { dirListAbort.abort(); dirListAbort = null; }

    // If not a path, filter projects locally
    if (!value || (value.charAt(0) !== '/' && value.charAt(0) !== '~')) {
        dirListPending = false;
        dirListCache = [];
        dirListCacheDir = '';
        renderPaletteResults(value);
        return;
    }

    dirListQuery = value;
    var parsed = dirParent(value);

    // If we have cached results for this directory, filter client-side immediately
    if (dirListCacheDir && dirListCacheDir === parsed.dir) {
        dirListPending = false;
        renderPaletteItems(filterCachedItems(parsed.prefix));
        return; // no need to re-fetch the same directory
    }

    // Show filtered cache while waiting (if the base directory changed, stale but better than nothing)
    if (dirListCache.length > 0) {
        renderPaletteItems(filterCachedItems(parsed.prefix));
    }

    // Debounce remote directory listing — always fetch the DIRECTORY, not the prefix
    dirListPending = true;
    dirListTimer = setTimeout(function() { fetchDirList(parsed.dir); }, 150);
}

function fetchDirList(dirPath) {
    var wing = currentPaletteWing();
    if (!wing) { dirListPending = false; return; }

    if (dirListAbort) dirListAbort.abort();
    dirListAbort = new AbortController();
    dirListPending = true;

    fetch('/api/app/wings/' + wing.id + '/ls?path=' + encodeURIComponent(dirPath), {
        signal: dirListAbort.signal
    }).then(function(r) { return r.json(); }).then(function(entries) {
        dirListPending = false;

        // Stale check: if user changed to a different directory, discard
        var currentParsed = dirParent(paletteSearch.value);
        if (currentParsed.dir !== dirPath) return;

        if (!entries || !Array.isArray(entries)) {
            dirListCache = [];
            dirListCacheDir = '';
            renderPaletteItems([]);
            return;
        }
        var items = entries.map(function(e) {
            return { name: e.name, path: e.path, isDir: e.is_dir };
        });
        items.sort(function(a, b) {
            if (a.isDir !== b.isDir) return a.isDir ? -1 : 1;
            return a.name.localeCompare(b.name);
        });

        // Cache full directory listing
        dirListCache = items;
        dirListCacheDir = dirPath;

        // Filter for current prefix
        renderPaletteItems(filterCachedItems(currentParsed.prefix));
    }).catch(function(err) {
        if (err && err.name === 'AbortError') return;
        dirListPending = false;
    });
}

function navigatePalette(dir) {
    var items = paletteResults.querySelectorAll('.palette-item');
    if (items.length === 0) return;
    items[paletteSelectedIndex].classList.remove('selected');
    paletteSelectedIndex = (paletteSelectedIndex + dir + items.length) % items.length;
    items[paletteSelectedIndex].classList.add('selected');
    items[paletteSelectedIndex].scrollIntoView({ block: 'nearest' });
}

function shortenPath(path) {
    if (path.indexOf('/Users/') === 0) {
        var parts = path.split('/');
        return '~/' + parts.slice(3).join('/');
    }
    if (path.indexOf('/home/') === 0) {
        var parts = path.split('/');
        return '~/' + parts.slice(3).join('/');
    }
    return path;
}

function launchFromPalette(cwd) {
    if (onlineWings().length === 0) return;
    var wing = currentPaletteWing();
    if (!wing) return;
    var wingId = wing.id;
    var agent = currentPaletteAgent();
    // Validate: only send absolute paths (wing returns these from dir listing)
    var validCwd = (cwd && cwd.charAt(0) === '/') ? cwd : '';
    hidePalette();
    if (paletteMode === 'chat') {
        launchChat(agent);
    } else {
        setLastTermAgent(agent);
        showTerminal();
        connectPTY(agent, validCwd, wingId);
    }
}

// === Notifications ===

function checkForNotification(text) {
    var tail = text.slice(-300);
    if (/Allow .+\?/.test(tail)) return true;
    if (/\[Y\/n\]\s*$/.test(tail)) return true;
    if (/\[y\/N\]\s*$/.test(tail)) return true;
    if (/Press Enter/i.test(tail)) return true;
    if (/approve|permission|confirm/i.test(tail) && /\?\s*$/.test(tail)) return true;
    return false;
}

function setNotification(sessionId) {
    if (!sessionId || sessionNotifications[sessionId]) return;
    sessionNotifications[sessionId] = true;
    renderSidebar();
    if (activeView === 'home') renderDashboard();

    // Browser notification
    if (document.hidden && 'Notification' in window && Notification.permission === 'granted') {
        new Notification('wingthing', { body: 'A session needs your attention' });
    }

    // Flash title
    if (!titleFlashTimer) {
        var on = true;
        titleFlashTimer = setInterval(function() {
            document.title = on ? '(!) wingthing' : 'wingthing';
            on = !on;
            if (!Object.keys(sessionNotifications).length) {
                clearInterval(titleFlashTimer);
                titleFlashTimer = null;
                document.title = 'wingthing';
            }
        }, 1000);
    }
}

function clearNotification(sessionId) {
    if (!sessionId || !sessionNotifications[sessionId]) return;
    delete sessionNotifications[sessionId];
    renderSidebar();
    if (activeView === 'home') renderDashboard();
    if (!Object.keys(sessionNotifications).length) {
        document.title = 'wingthing';
    }
}

// === Navigation ===

function showHome() {
    activeView = 'home';
    homeSection.style.display = '';
    terminalSection.style.display = 'none';
    chatSection.style.display = 'none';
    headerTitle.textContent = '';
    ptyStatus.textContent = '';
    // Mark detaching session as yellow immediately (don't wait for poll)
    var detachingId = ptySessionId;
    detachPTY();
    if (detachingId) {
        var s = sessionsData.find(function(s) { return s.id === detachingId; });
        if (s) s.status = 'detached';
    }
    destroyChat();
    renderSidebar();
    renderDashboard();
}

function showTerminal() {
    activeView = 'terminal';
    homeSection.style.display = 'none';
    terminalSection.style.display = '';
    chatSection.style.display = 'none';
    destroyChat();
    if (term && fitAddon) {
        fitAddon.fit();
        term.focus();
    }
}

function showChat() {
    activeView = 'chat';
    homeSection.style.display = 'none';
    terminalSection.style.display = 'none';
    chatSection.style.display = '';
}

function switchToSession(sessionId) {
    detachPTY();
    showTerminal();
    attachPTY(sessionId);
}

function detachPTY() {
    if (ptyWs) {
        if (ptySessionId && ptyWs.readyState === WebSocket.OPEN) {
            ptyWs.send(JSON.stringify({ type: 'pty.detach', session_id: ptySessionId }));
        }
        ptyWs.close();
        ptyWs = null;
    }
    ptySessionId = null;
    e2eKey = null;
    ephemeralPrivKey = null;
}

// Expose for inline onclick
window._openChat = function (sessionId, agent) {
    showChat();
    resumeChat(sessionId, agent);
};

window._deleteSession = function (sessionId) {
    var cached = getCachedSessions().filter(function (s) { return s.id !== sessionId; });
    setCachedSessions(cached);
    clearTermBuffer(sessionId);
    delete sessionNotifications[sessionId];
    fetch('/api/app/sessions/' + sessionId, { method: 'DELETE' }).then(function () {
        loadHome();
    });
};

// === Chat (NLUX) ===

function launchChat(agent) {
    setLastChatAgent(agent);
    showChat();
    chatStatus.textContent = 'connecting...';
    chatDeleteBtn.style.display = 'none';

    var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    var url = proto + '//' + location.host + '/ws/pty';
    chatWs = new WebSocket(url);
    chatWs.onopen = function () {
        chatStatus.textContent = 'starting chat...';
        chatWs.send(JSON.stringify({ type: 'chat.start', agent: agent }));
    };
    setupChatHandlers(chatWs, agent, null);
}

function resumeChat(sessionId, agent) {
    chatStatus.textContent = 'loading...';
    chatDeleteBtn.style.display = 'none';

    var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    var url = proto + '//' + location.host + '/ws/pty';
    chatWs = new WebSocket(url);
    chatWs.onopen = function () {
        chatStatus.textContent = 'loading history...';
        chatWs.send(JSON.stringify({ type: 'chat.start', session_id: sessionId, agent: agent }));
    };
    setupChatHandlers(chatWs, agent, sessionId);
}

function setupChatHandlers(ws, agent, resumeSessionId) {
    pendingHistory = null;
    ws.onmessage = function (e) {
        var msg = JSON.parse(e.data);
        switch (msg.type) {
            case 'chat.history':
                pendingHistory = (msg.messages || []).map(function (m) {
                    return { role: m.role, message: m.content };
                });
                break;
            case 'chat.started':
                chatSessionId = msg.session_id;
                chatStatus.textContent = msg.agent + ' chat';
                chatDeleteBtn.style.display = '';
                mountNlux(agent, pendingHistory);
                pendingHistory = null;
                renderSidebar();
                break;
            case 'chat.chunk':
                if (chatObserver) chatObserver.next(msg.text);
                break;
            case 'chat.done':
                if (chatObserver) { chatObserver.complete(); chatObserver = null; }
                chatContainer.classList.remove('thinking');
                break;
            case 'error':
                chatStatus.textContent = msg.message;
                chatContainer.classList.remove('thinking');
                if (chatObserver) { chatObserver.error(new Error(msg.message)); chatObserver = null; }
                break;
        }
    };
    ws.onclose = function () { chatStatus.textContent = 'disconnected'; chatObserver = null; };
    ws.onerror = function () { chatStatus.textContent = 'connection error'; };
}

function mountNlux(agent, initialMessages) {
    if (chatInstance) { chatInstance.unmount(); chatInstance = null; }
    chatContainer.innerHTML = '';

    var adapter = {
        streamText: function (message, observer) {
            chatObserver = observer;
            chatContainer.classList.add('thinking');
            if (chatWs && chatWs.readyState === WebSocket.OPEN && chatSessionId) {
                chatWs.send(JSON.stringify({ type: 'chat.message', session_id: chatSessionId, content: message }));
            } else {
                chatContainer.classList.remove('thinking');
                observer.error(new Error('not connected'));
            }
        }
    };

    var chat = createAiChat()
        .withAdapter(adapter)
        .withDisplayOptions({ colorScheme: 'dark', height: '100%', width: '100%' })
        .withConversationOptions({ historyPayloadSize: 0, layout: 'bubbles' })
        .withComposerOptions({ placeholder: 'message ' + agent + '...', autoFocus: true })
        .withPersonaOptions({
            assistant: {
                name: agent,
                avatar: 'https://ui-avatars.com/api/?name=' + agent.charAt(0).toUpperCase() + '&background=e94560&color=fff&size=32',
            },
        })
        .withMessageOptions({ waitTimeBeforeStreamCompletion: 'never' });

    if (initialMessages && initialMessages.length > 0) {
        chat = chat.withInitialConversation(initialMessages);
    }

    chat.mount(chatContainer);
    chatInstance = chat;
}

function destroyChat() {
    if (chatInstance) { chatInstance.unmount(); chatInstance = null; }
    if (chatWs) { chatWs.close(); chatWs = null; }
    chatSessionId = null;
    chatObserver = null;
    chatContainer.innerHTML = '';
}

// === Terminal (xterm.js) ===

function initTerminal() {
    term = new Terminal({
        cursorBlink: true,
        fontSize: 14,
        fontFamily: "'SF Mono', 'Fira Code', 'Cascadia Code', monospace",
        theme: {
            background: '#1a1a2e',
            foreground: '#eee',
            cursor: '#e94560',
            selectionBackground: '#0f3460',
        },
        allowProposedApi: true,
    });
    fitAddon = new FitAddon();
    serializeAddon = new SerializeAddon();
    term.loadAddon(fitAddon);
    term.loadAddon(serializeAddon);
    term.open(terminalContainer);
    fitAddon.fit();

    // Ctrl+. = go back to dashboard (intercepted before PTY)
    term.attachCustomKeyEventHandler(function (e) {
        if (e.type === 'keydown' && (e.ctrlKey || e.metaKey) && e.key === '.') {
            e.preventDefault();
            showHome();
            return false;
        }
        return true;
    });

    term.onData(function (data) {
        if (ctrlActive) {
            ctrlActive = false;
            document.querySelector('[data-key="ctrl"]').classList.remove('active');
            if (data.length === 1) {
                var code = data.toUpperCase().charCodeAt(0) - 64;
                if (code >= 0 && code <= 31) { sendPTYInput(String.fromCharCode(code)); return; }
            }
        }
        if (altActive) {
            altActive = false;
            document.querySelector('[data-key="alt"]').classList.remove('active');
            sendPTYInput('\x1b' + data);
            return;
        }
        sendPTYInput(data);
    });

    // Bell = notification
    term.onBell(function() {
        if (ptySessionId) setNotification(ptySessionId);
    });
}

function saveTermBuffer() {
    if (!ptySessionId || !serializeAddon) return;
    clearTimeout(saveBufferTimer);
    saveBufferTimer = setTimeout(function () {
        try {
            var data = serializeAddon.serialize();
            if (data.length > 200000) data = data.slice(-200000);
            localStorage.setItem(TERM_BUF_PREFIX + ptySessionId, data);
            saveTermThumb();
        } catch (e) {}
    }, 500);
}

var ANSI_PALETTE = [
    '#000','#c33','#3c3','#cc3','#33c','#c3c','#3cc','#ccc',
    '#888','#f66','#6f6','#ff6','#66f','#f6f','#6ff','#fff'
];

function cellFgColor(cell) {
    if (cell.isFgDefault()) return '#eee';
    if (cell.isFgRGB()) {
        var c = cell.getFgColor();
        return '#' + ((c >> 16) & 0xff).toString(16).padStart(2, '0') +
               ((c >> 8) & 0xff).toString(16).padStart(2, '0') +
               (c & 0xff).toString(16).padStart(2, '0');
    }
    if (cell.isFgPalette()) {
        var idx = cell.getFgColor();
        if (idx < 16) return ANSI_PALETTE[idx];
        return '#eee';
    }
    return '#eee';
}

function saveTermThumb() {
    if (!ptySessionId || !term) return;
    try {
        var W = 480, H = 260;
        var c = document.createElement('canvas');
        c.width = W; c.height = H;
        var ctx = c.getContext('2d');
        ctx.fillStyle = '#1a1a2e';
        ctx.fillRect(0, 0, W, H);

        var buffer = term.buffer.active;
        var charW = 5.6;
        var lineH = 11;
        var padX = 4, padY = 10;
        var maxRows = Math.min(term.rows, Math.floor((H - padY) / lineH));
        var maxCols = Math.min(term.cols, Math.floor((W - padX) / charW));
        ctx.font = '9px monospace';
        ctx.textBaseline = 'top';

        var nullCell = buffer.getNullCell();
        for (var y = 0; y < maxRows; y++) {
            var line = buffer.getLine(buffer.viewportY + y);
            if (!line) continue;
            var lastColor = '';
            var run = '';
            var runX = 0;
            for (var x = 0; x < maxCols; x++) {
                var cell = line.getCell(x, nullCell);
                if (!cell) continue;
                var ch = cell.getChars() || ' ';
                var fg = cell.isDim() ? '#666' : cellFgColor(cell);
                if (fg !== lastColor) {
                    if (run) { ctx.fillStyle = lastColor; ctx.fillText(run, padX + runX * charW, padY + y * lineH); }
                    lastColor = fg;
                    run = ch;
                    runX = x;
                } else {
                    run += ch;
                }
            }
            if (run) { ctx.fillStyle = lastColor; ctx.fillText(run, padX + runX * charW, padY + y * lineH); }
        }

        localStorage.setItem(TERM_THUMB_PREFIX + ptySessionId, c.toDataURL('image/webp', 0.5));
    } catch (e) {}
}

function restoreTermBuffer(sessionId) {
    try {
        var data = localStorage.getItem(TERM_BUF_PREFIX + sessionId);
        if (data && term) term.write(data);
    } catch (e) {}
}

function clearTermBuffer(sessionId) {
    try { localStorage.removeItem(TERM_BUF_PREFIX + sessionId); } catch (e) {}
    try { localStorage.removeItem(TERM_THUMB_PREFIX + sessionId); } catch (e) {}
}

function sendPTYInput(text) {
    if (!ptyWs || ptyWs.readyState !== WebSocket.OPEN || !ptySessionId) return;
    clearNotification(ptySessionId);
    e2eEncrypt(text).then(function (encoded) {
        ptyWs.send(JSON.stringify({ type: 'pty.input', session_id: ptySessionId, data: encoded }));
    });
}

// === E2E Crypto ===

function b64ToBytes(b64) {
    return Uint8Array.from(atob(b64), function(c) { return c.charCodeAt(0); });
}

function bytesToB64(bytes) {
    var binary = '';
    for (var i = 0; i < bytes.length; i++) binary += String.fromCharCode(bytes[i]);
    return btoa(binary);
}

async function deriveE2EKey(wingPublicKeyB64) {
    if (!ephemeralPrivKey) return null;
    var wingPubBytes = b64ToBytes(wingPublicKeyB64);
    var shared = x25519.getSharedSecret(ephemeralPrivKey, wingPubBytes);
    var salt = new Uint8Array(32);
    var keyMaterial = await crypto.subtle.importKey('raw', shared, 'HKDF', false, ['deriveKey']);
    var enc = new TextEncoder();
    return crypto.subtle.deriveKey(
        { name: 'HKDF', hash: 'SHA-256', salt: salt, info: enc.encode('wt-pty') },
        keyMaterial,
        { name: 'AES-GCM', length: 256 },
        false,
        ['encrypt', 'decrypt']
    );
}

async function e2eEncrypt(plaintext) {
    if (!e2eKey) return btoa(unescape(encodeURIComponent(plaintext)));
    var enc = new TextEncoder();
    var iv = crypto.getRandomValues(new Uint8Array(12));
    var ciphertext = await crypto.subtle.encrypt({ name: 'AES-GCM', iv: iv }, e2eKey, enc.encode(plaintext));
    var result = new Uint8Array(iv.length + ciphertext.byteLength);
    result.set(iv);
    result.set(new Uint8Array(ciphertext), iv.length);
    return bytesToB64(result);
}

async function e2eDecrypt(encoded) {
    if (!e2eKey) {
        var binary = atob(encoded);
        var bytes = new Uint8Array(binary.length);
        for (var i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
        return bytes;
    }
    var data = b64ToBytes(encoded);
    var iv = data.slice(0, 12);
    var ciphertext = data.slice(12);
    var plaintext = await crypto.subtle.decrypt({ name: 'AES-GCM', iv: iv }, e2eKey, ciphertext);
    return new Uint8Array(plaintext);
}

// === PTY WebSocket ===

function setupPTYHandlers(ws, reattach) {
    var pendingOutput = [];
    var keyReady = false;

    function processOutput(dataStr) {
        e2eDecrypt(dataStr).then(function (bytes) {
            term.write(bytes);
            saveTermBuffer();
            try {
                var text = new TextDecoder().decode(bytes);
                if (checkForNotification(text)) {
                    setNotification(ptySessionId);
                }
            } catch (ex) {}
        }).catch(function (err) {
            console.error('decrypt error, dropping frame:', err);
        });
    }

    ws.onmessage = function (e) {
        if (ws !== ptyWs) return; // stale WebSocket
        var msg = JSON.parse(e.data);
        switch (msg.type) {
            case 'pty.started':
                ptySessionId = msg.session_id;
                var sessionCWD = msg.cwd || '';
                var pName = projectName(sessionCWD);
                headerTitle.textContent = pName !== '~' ? pName + ' \u00b7 ' + msg.agent : msg.agent;

                if (msg.public_key) {
                    deriveE2EKey(msg.public_key).then(function (key) {
                        e2eKey = key;
                        keyReady = true;
                        ptyStatus.textContent = key ? '\uD83D\uDD12' : '';
                        // Flush any output that arrived before key was ready
                        pendingOutput.forEach(processOutput);
                        pendingOutput = [];
                    }).catch(function () {
                        keyReady = true;
                        ptyStatus.textContent = '';
                        pendingOutput.forEach(processOutput);
                        pendingOutput = [];
                    });
                } else {
                    keyReady = true;
                    ptyStatus.textContent = '';
                }

                if (!reattach) term.clear();
                term.focus();
                renderSidebar();
                loadHome();

                term.onResize(function (size) {
                    if (ptyWs && ptyWs.readyState === WebSocket.OPEN && ptySessionId) {
                        ptyWs.send(JSON.stringify({ type: 'pty.resize', session_id: ptySessionId, cols: size.cols, rows: size.rows }));
                    }
                });
                fitAddon.fit();
                break;

            case 'pty.output':
                if (!keyReady) {
                    pendingOutput.push(msg.data);
                } else {
                    processOutput(msg.data);
                }
                break;

            case 'pty.exited':
                // Ignore exited events from stale or unknown sessions
                if (!ptySessionId || msg.session_id !== ptySessionId) break;
                headerTitle.textContent = '';
                ptyStatus.textContent = 'exited';
                if (msg.session_id) clearTermBuffer(msg.session_id);
                clearNotification(msg.session_id);
                ptySessionId = null;
                e2eKey = null;
                ephemeralPrivKey = null;
                term.writeln('\r\n\x1b[2m--- session ended ---\x1b[0m');
                renderSidebar();
                loadHome();
                break;

            case 'error':
                ptyStatus.textContent = msg.message;
                break;
        }
    };

    ws.onclose = function () {
        // Ignore close from stale WebSocket (user switched sessions)
        if (ws !== ptyWs) return;
        ptyStatus.textContent = '';
        ptySessionId = null;
        renderSidebar();
    };

    ws.onerror = function () {
        if (ws !== ptyWs) return;
        ptyStatus.textContent = 'error';
    };
}

function connectPTY(agent, cwd, wingId) {
    // Detach any existing PTY connection first
    detachPTY();

    var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    var url = proto + '//' + location.host + '/ws/pty';

    headerTitle.textContent = 'connecting...';
    ptyStatus.textContent = '';

    ephemeralPrivKey = x25519.utils.randomSecretKey();
    var ephemeralPubKey = x25519.getPublicKey(ephemeralPrivKey);
    var pubKeyB64 = bytesToB64(ephemeralPubKey);
    e2eKey = null;

    ptyWs = new WebSocket(url);
    ptyWs.onopen = function () {
        headerTitle.textContent = 'starting ' + agent + '...';
        var startMsg = {
            type: 'pty.start',
            agent: agent,
            cols: term.cols,
            rows: term.rows,
            public_key: pubKeyB64,
        };
        if (cwd) startMsg.cwd = cwd;
        if (wingId) startMsg.wing_id = wingId;
        ptyWs.send(JSON.stringify(startMsg));
    };

    setupPTYHandlers(ptyWs, false);
}

function attachPTY(sessionId) {
    var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    var url = proto + '//' + location.host + '/ws/pty';

    term.clear();
    clearNotification(sessionId);

    // Find session info for header
    var sess = sessionsData.find(function(s) { return s.id === sessionId; });
    headerTitle.textContent = sess ? projectName(sess.cwd) + ' \u00b7 ' + (sess.agent || '?') : 'reconnecting...';
    ptyStatus.textContent = '';

    ephemeralPrivKey = x25519.utils.randomSecretKey();
    var ephemeralPubKey = x25519.getPublicKey(ephemeralPrivKey);
    var pubKeyB64 = bytesToB64(ephemeralPubKey);
    e2eKey = null;

    ptyWs = new WebSocket(url);
    ptyWs.onopen = function () {
        ptyWs.send(JSON.stringify({ type: 'pty.attach', session_id: sessionId, public_key: pubKeyB64 }));
    };

    setupPTYHandlers(ptyWs, true);
}

function disconnectPTY() {
    if (ptyWs && ptyWs.readyState === WebSocket.OPEN && ptySessionId) {
        ptyWs.send(JSON.stringify({ type: 'pty.kill', session_id: ptySessionId }));
    }
    if (ptyWs) { ptyWs.close(); ptyWs = null; }
    ptySessionId = null;
    e2eKey = null;
    ephemeralPrivKey = null;
    ptyStatus.textContent = '';
    headerTitle.textContent = '';
}

// === Helpers ===

function escapeHtml(str) {
    var div = document.createElement('div');
    div.textContent = str;
    return div.innerHTML;
}

if ('serviceWorker' in navigator) {
    navigator.serviceWorker.register('sw.js').catch(function () {});
}

init();
