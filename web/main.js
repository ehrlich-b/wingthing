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

// Chat state
let chatWs = null;
let chatSessionId = null;
let chatObserver = null;
let chatInstance = null;
let pendingHistory = null;

// DOM refs
const sessionTabs = document.getElementById('session-tabs');
const newSessionBtn = document.getElementById('new-session-btn');
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
const paletteSearch = document.getElementById('palette-search');
const paletteResults = document.getElementById('palette-results');
const paletteAgent = document.getElementById('palette-agent');
const paletteGo = document.getElementById('palette-go');

// localStorage keys
var CACHE_KEY = 'wt_sessions';
var LAST_TERM_KEY = 'wt_last_term_agent';
var LAST_CHAT_KEY = 'wt_last_chat_agent';
var TERM_BUF_PREFIX = 'wt_termbuf_';

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
    newSessionBtn.addEventListener('click', showPalette);
    headerLogo.addEventListener('click', function(e) { e.preventDefault(); showHome(); });

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
        if (e.key === 'Escape' && commandPalette.style.display !== 'none') {
            hidePalette();
        }
    });

    // Palette events
    paletteBackdrop.addEventListener('click', hidePalette);
    paletteSearch.addEventListener('input', function() {
        renderPaletteResults(paletteSearch.value);
    });
    paletteSearch.addEventListener('keydown', function(e) {
        if (e.key === 'Enter') {
            var selected = paletteResults.querySelector('.palette-item.selected');
            if (selected) launchFromPalette(selected.dataset.path);
        }
        if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
            e.preventDefault();
            navigatePalette(e.key === 'ArrowDown' ? 1 : -1);
        }
    });
    paletteGo.addEventListener('click', function() {
        var selected = paletteResults.querySelector('.palette-item.selected');
        if (selected) launchFromPalette(selected.dataset.path);
    });

    window.addEventListener('resize', function () {
        if (term && fitAddon) fitAddon.fit();
    });

    initTerminal();
    loadHome();
    setInterval(loadHome, 10000);
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
    wingsData = wings;
    setCachedSessions(sessions);

    // Build agent + project lists
    availableAgents = [];
    allProjects = [];
    var seenAgents = {};
    (wings || []).forEach(function (w) {
        (w.agents || []).forEach(function (a) {
            if (!seenAgents[a]) { seenAgents[a] = true; availableAgents.push({ agent: a, wingId: w.id }); }
        });
        (w.projects || []).forEach(function (p) {
            allProjects.push({ name: p.name, path: p.path, wingId: w.id, machine: w.machine_id });
        });
    });

    renderSidebar();
    if (activeView === 'home') renderDashboard();
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
            if (kind === 'chat') {
                window._openChat(sid, agent);
            } else {
                switchToSession(sid);
            }
        });
    });
}

function renderDashboard() {
    // Wing status cards
    if (wingsData.length > 0) {
        wingStatusEl.innerHTML = wingsData.map(function(w) {
            var name = w.machine_id || w.id.substring(0, 8);
            var projectCount = (w.projects || []).length;
            return '<div class="wing-card">' +
                '<span class="wing-dot"></span>' +
                '<span class="wing-name">' + escapeHtml(name) + '</span>' +
                '<span class="wing-detail">' + escapeHtml((w.agents || []).join(', ')) +
                    (projectCount ? ' \u00b7 ' + projectCount + ' projects' : '') + '</span>' +
            '</div>';
        }).join('');
    } else {
        wingStatusEl.innerHTML = '';
    }

    // Session cards
    var hasSessions = sessionsData.length > 0;
    emptyState.style.display = hasSessions ? 'none' : '';

    if (!hasSessions) {
        sessionsList.innerHTML = '';
        return;
    }

    sessionsList.innerHTML = sessionsData.map(function(s) {
        var name = projectName(s.cwd);
        var isActive = s.status === 'active';
        var kind = s.kind || 'terminal';
        var needsAttention = sessionNotifications[s.id];
        var dotClass = isActive ? 'live' : 'detached';
        if (needsAttention) dotClass = 'attention';

        return '<div class="session-card" data-sid="' + s.id + '" data-kind="' + kind + '" data-agent="' + escapeHtml(s.agent || 'claude') + '">' +
            '<span class="session-dot ' + dotClass + '"></span>' +
            '<div class="session-info">' +
                '<div class="session-project">' + escapeHtml(name) + '</div>' +
                '<div class="session-meta">' + escapeHtml(s.agent || '?') + ' ' + kind +
                    (needsAttention ? ' \u00b7 needs attention' : '') + '</div>' +
            '</div>' +
            '<div class="session-actions">' +
                '<button class="btn-sm btn-danger" onclick="event.stopPropagation(); window._deleteSession(\'' + s.id + '\')">x</button>' +
            '</div>' +
        '</div>';
    }).join('');

    sessionsList.querySelectorAll('.session-card').forEach(function(card) {
        card.addEventListener('click', function() {
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
}

// === Command Palette ===

function showPalette() {
    if (availableAgents.length === 0 && wingsData.length === 0) return;
    commandPalette.style.display = '';
    paletteSearch.value = '';
    paletteSearch.focus();

    // Populate agent selector
    var agents = availableAgents.length > 0
        ? availableAgents.map(function(a) { return a.agent; })
        : ['claude'];
    var lastAgent = getLastTermAgent();
    paletteAgent.innerHTML = agents.map(function(a) {
        return '<option value="' + escapeHtml(a) + '"' + (a === lastAgent ? ' selected' : '') + '>' + escapeHtml(a) + '</option>';
    }).join('');

    renderPaletteResults('');
}

function hidePalette() {
    commandPalette.style.display = 'none';
}

var paletteSelectedIndex = 0;

function renderPaletteResults(filter) {
    var items = [];

    // Always offer a "default directory" option (no cwd = wing's working dir)
    if (!filter) {
        items.push({ name: 'default directory', path: '', selected: true });
    }

    var filtered = allProjects;
    if (filter) {
        var lower = filter.toLowerCase();
        filtered = allProjects.filter(function(p) {
            return p.name.toLowerCase().indexOf(lower) !== -1 ||
                   p.path.toLowerCase().indexOf(lower) !== -1;
        });
    }

    filtered.forEach(function(p) {
        items.push({ name: p.name, path: p.path, selected: items.length === 0 });
    });

    // Custom path fallback — only if it looks like an absolute path
    if (filter && filtered.length === 0 && (filter.charAt(0) === '/' || filter.charAt(0) === '~')) {
        items.push({ name: 'custom path', path: filter, selected: items.length === 0 });
    }

    paletteSelectedIndex = 0;

    if (items.length === 0) {
        paletteResults.innerHTML = '<div class="palette-empty">type an absolute path (e.g. /home/user/project)</div>';
        return;
    }

    paletteResults.innerHTML = items.map(function(item, i) {
        return '<div class="palette-item' + (item.selected ? ' selected' : '') + '" data-path="' + escapeHtml(item.path) + '" data-index="' + i + '">' +
            '<span class="palette-name">' + escapeHtml(item.name) + '</span>' +
            '<span class="palette-path">' + escapeHtml(shortenPath(item.path)) + '</span>' +
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
    var agent = paletteAgent.value;
    hidePalette();
    setLastTermAgent(agent);
    showTerminal();
    // Only pass CWD if it's an absolute path
    connectPTY(agent, (cwd && cwd.charAt(0) === '/') ? cwd : '');
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
        } catch (e) {}
    }, 500);
}

function restoreTermBuffer(sessionId) {
    try {
        var data = localStorage.getItem(TERM_BUF_PREFIX + sessionId);
        if (data && term) term.write(data);
    } catch (e) {}
}

function clearTermBuffer(sessionId) {
    try { localStorage.removeItem(TERM_BUF_PREFIX + sessionId); } catch (e) {}
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
    ws.onmessage = function (e) {
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
                        ptyStatus.textContent = key ? '\uD83D\uDD12' : '';
                    }).catch(function () { ptyStatus.textContent = ''; });
                } else {
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
                e2eDecrypt(msg.data).then(function (bytes) {
                    term.write(bytes);
                    saveTermBuffer();
                    // Check for notification patterns
                    try {
                        var text = new TextDecoder().decode(bytes);
                        if (checkForNotification(text)) {
                            setNotification(msg.session_id || ptySessionId);
                        }
                    } catch (ex) {}
                }).catch(function (err) {
                    console.error('decrypt error:', err);
                    var binary = atob(msg.data);
                    var bytes = new Uint8Array(binary.length);
                    for (var i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
                    term.write(bytes);
                    saveTermBuffer();
                });
                break;

            case 'pty.exited':
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
        ptyStatus.textContent = '';
        ptySessionId = null;
        renderSidebar();
    };

    ws.onerror = function () {
        ptyStatus.textContent = 'error';
    };
}

function connectPTY(agent, cwd) {
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
        ptyWs.send(JSON.stringify(startMsg));
    };

    setupPTYHandlers(ptyWs, false);
}

function attachPTY(sessionId) {
    var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    var url = proto + '//' + location.host + '/ws/pty';

    term.clear();
    restoreTermBuffer(sessionId);
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
