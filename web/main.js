import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import { SerializeAddon } from '@xterm/addon-serialize';
import '@xterm/xterm/css/xterm.css';
import { x25519 } from '@noble/curves/ed25519.js';
import { createAiChat } from '@nlux/core';
import '@nlux/themes/nova.css';

// State
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
let availableAgents = []; // [{agent, wingId}]

// Chat state
let chatWs = null;
let chatSessionId = null;
let chatObserver = null;
let chatInstance = null;

// DOM refs
const userInfo = document.getElementById('user-info');
const homeSection = document.getElementById('home-section');
const sessionsList = document.getElementById('sessions-list');
const emptyState = document.getElementById('empty-state');
const terminalSection = document.getElementById('terminal-section');
const terminalContainer = document.getElementById('terminal-container');
const ptyStatus = document.getElementById('pty-status');
const backBtn = document.getElementById('back-btn');
const disconnectBtn = document.getElementById('disconnect-btn');
const chatSection = document.getElementById('chat-section');
const chatContainer = document.getElementById('chat-container');
const chatStatus = document.getElementById('chat-status');
const chatBackBtn = document.getElementById('chat-back-btn');
const chatDeleteBtn = document.getElementById('chat-delete-btn');

// Header buttons (small, shown when wings connected)
const headerButtons = document.getElementById('header-buttons');
const headerTermBtn = document.getElementById('header-term-btn');
const headerTermToggle = document.getElementById('header-term-toggle');
const headerTermMenu = document.getElementById('header-term-menu');
const headerChatBtn = document.getElementById('header-chat-btn');
const headerChatToggle = document.getElementById('header-chat-toggle');
const headerChatMenu = document.getElementById('header-chat-menu');

// Empty state launch buttons (big)
const launchTermBtn = document.getElementById('launch-term-btn');
const launchTermToggle = document.getElementById('launch-term-toggle');
const launchTermMenu = document.getElementById('launch-term-menu');
const launchChatBtn = document.getElementById('launch-chat-btn');
const launchChatToggle = document.getElementById('launch-chat-toggle');
const launchChatMenu = document.getElementById('launch-chat-menu');

function loginRedirect() {
    // Derive the main site login URL from the current hostname.
    // On app.wingthing.ai → login at wingthing.ai; on localhost → same host.
    var host = window.location.hostname.replace(/^app\./, '');
    var port = window.location.port ? ':' + window.location.port : '';
    var loginUrl = window.location.protocol + '//' + host + port +
        '/login?next=' + encodeURIComponent(window.location.origin + '/');
    window.location.href = loginUrl;
}

async function init() {
    try {
        var resp = await fetch('/api/app/me');
        if (resp.status === 401) {
            loginRedirect();
            return;
        }
        currentUser = await resp.json();
        userInfo.textContent = currentUser.display_name || 'user';
    } catch (e) {
        loginRedirect();
        return;
    }

    backBtn.addEventListener('click', showHome);
    disconnectBtn.addEventListener('click', disconnectPTY);
    chatBackBtn.addEventListener('click', showHome);
    chatDeleteBtn.addEventListener('click', function () {
        if (chatSessionId) {
            var cached = getCachedSessions().filter(function (s) { return s.id !== chatSessionId; });
            setCachedSessions(cached);
            fetch('/api/app/sessions/' + chatSessionId, { method: 'DELETE' });
            destroyChat();
            showHome();
        }
    });

    // Terminal buttons — primary click uses last-used agent
    launchTermBtn.addEventListener('click', function () { launchTerminal(getLastTermAgent()); });
    headerTermBtn.addEventListener('click', function () { launchTerminal(getLastTermAgent()); });

    // Chat buttons — primary click uses last-used agent
    launchChatBtn.addEventListener('click', function () { launchChat(getLastChatAgent()); });
    headerChatBtn.addEventListener('click', function () { launchChat(getLastChatAgent()); });

    // Toggle dropdowns
    [launchTermToggle, headerTermToggle].forEach(function (btn) {
        btn.addEventListener('click', function (e) {
            e.stopPropagation();
            closeAllMenus();
            btn.parentElement.querySelector('.split-menu').classList.toggle('open');
        });
    });
    [launchChatToggle, headerChatToggle].forEach(function (btn) {
        btn.addEventListener('click', function (e) {
            e.stopPropagation();
            closeAllMenus();
            btn.parentElement.querySelector('.split-menu').classList.toggle('open');
        });
    });

    // Close dropdowns on outside click
    document.addEventListener('click', closeAllMenus);

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

    window.addEventListener('resize', function () {
        if (term && fitAddon) fitAddon.fit();
    });

    initTerminal();
    loadHome();
    setInterval(loadHome, 10000);
}

// localStorage session cache
var CACHE_KEY = 'wt_sessions';
var LAST_TERM_KEY = 'wt_last_term_agent';
var LAST_CHAT_KEY = 'wt_last_chat_agent';

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

function closeAllMenus() {
    document.querySelectorAll('.split-menu').forEach(function (m) { m.classList.remove('open'); });
}

function getCachedSessions() {
    try {
        var raw = localStorage.getItem(CACHE_KEY);
        return raw ? JSON.parse(raw) : [];
    } catch (e) { return []; }
}

function setCachedSessions(sessions) {
    try { localStorage.setItem(CACHE_KEY, JSON.stringify(sessions)); } catch (e) {}
}

function renderSessions(sessions, wings) {
    var hasSessions = sessions.length > 0;
    emptyState.style.display = hasSessions ? 'none' : '';
    headerButtons.style.display = wings.length > 0 ? '' : 'none';

    if (!hasSessions) {
        sessionsList.innerHTML = '';
        if (wings.length === 0) {
            emptyState.querySelector('.launch-buttons').style.display = 'none';
        } else {
            emptyState.querySelector('.launch-buttons').style.display = '';
        }
        return;
    }

    var html = sessions.map(function (s) {
        var isActive = s.status === 'active';
        var kind = s.kind || 'terminal';
        var isChat = kind === 'chat';
        var statusClass = isActive ? 'active' : 'detached';
        var statusLabel = isActive ? 'live' : 'detached';
        var label = (isChat ? 'chat' : 'terminal') + ' / ' + (s.agent || 'unknown');
        var shortId = s.id.substring(0, 8);
        var kindBadge = isChat ? '<span class="session-kind chat">chat</span>' : '<span class="session-kind term">term</span>';

        var actionBtn;
        if (isChat) {
            actionBtn = '<button class="btn-sm btn-primary" onclick="window._openChat(\'' + s.id + '\', \'' + escapeHtml(s.agent || 'claude') + '\')">open</button>';
        } else if (isActive) {
            actionBtn = '<button class="btn-sm" onclick="window._viewSession(\'' + s.id + '\')">view</button>';
        } else {
            actionBtn = '<button class="btn-sm btn-primary" onclick="window._reattachSession(\'' + s.id + '\')">reconnect</button>';
        }

        return '<div class="session-card ' + statusClass + '">' +
            '<div class="session-info">' +
                kindBadge +
                '<span class="session-agent">' + escapeHtml(s.agent || 'unknown') + '</span>' +
                '<span class="session-id">' + escapeHtml(shortId) + '</span>' +
                '<span class="session-status ' + statusClass + '">' + statusLabel + '</span>' +
            '</div>' +
            '<div class="session-actions">' +
                actionBtn +
                '<button class="btn-sm btn-danger" onclick="window._deleteSession(\'' + s.id + '\')">delete</button>' +
            '</div>' +
        '</div>';
    }).join('');

    sessionsList.innerHTML = html;
}

// Load sessions + wings, render home
async function loadHome() {
    if (terminalSection.style.display !== 'none') return;
    if (chatSection.style.display !== 'none') return;

    // Render cached sessions instantly (no pop)
    var cached = getCachedSessions();
    if (cached.length > 0) renderSessions(cached, availableAgents.length > 0 ? [1] : []);

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
        return; // keep showing cached data
    }

    setCachedSessions(sessions);

    // Build agent list from wings
    availableAgents = [];
    var seen = {};
    (wings || []).forEach(function (w) {
        (w.agents || []).forEach(function (a) {
            if (!seen[a]) {
                seen[a] = true;
                availableAgents.push({ agent: a, wingId: w.id });
            }
        });
    });
    populateDropdowns();

    renderSessions(sessions, wings);
}

function updatePrimaryButtons() {
    var termAgent = getLastTermAgent();
    var chatAgent = getLastChatAgent();
    var termLabel = agentTermLabel(termAgent);
    var chatLabel = agentChatLabel(chatAgent);
    launchTermBtn.textContent = termLabel;
    headerTermBtn.textContent = termLabel;
    launchChatBtn.textContent = chatLabel;
    headerChatBtn.textContent = chatLabel;
}

function populateDropdowns() {
    var agents = availableAgents.length > 0
        ? availableAgents.map(function (a) { return a.agent; })
        : ['claude', 'ollama'];

    updatePrimaryButtons();

    // Terminal menus
    [launchTermMenu, headerTermMenu].forEach(function (menu) {
        menu.innerHTML = agents.map(function (a) {
            return '<div class="split-menu-item" data-agent="' + escapeHtml(a) + '">' +
                escapeHtml(agentTermLabel(a)) + '</div>';
        }).join('');
        menu.querySelectorAll('.split-menu-item').forEach(function (item) {
            item.addEventListener('click', function (e) {
                e.stopPropagation();
                closeAllMenus();
                launchTerminal(item.dataset.agent);
            });
        });
    });

    // Chat menus
    [launchChatMenu, headerChatMenu].forEach(function (menu) {
        menu.innerHTML = agents.map(function (a) {
            return '<div class="split-menu-item" data-agent="' + escapeHtml(a) + '">' +
                escapeHtml(agentChatLabel(a)) + '</div>';
        }).join('');
        menu.querySelectorAll('.split-menu-item').forEach(function (item) {
            item.addEventListener('click', function (e) {
                e.stopPropagation();
                closeAllMenus();
                launchChat(item.dataset.agent);
            });
        });
    });
}

function agentTermLabel(agent) {
    return agent + ' terminal';
}

function agentChatLabel(agent) {
    return agent + ' chat';
}

function launchTerminal(agent) {
    setLastTermAgent(agent);
    updatePrimaryButtons();
    showTerminal();
    connectPTY(agent);
}

function showHome() {
    homeSection.style.display = '';
    terminalSection.style.display = 'none';
    chatSection.style.display = 'none';
    destroyChat();
    loadHome();
}

function showTerminal() {
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
    homeSection.style.display = 'none';
    terminalSection.style.display = 'none';
    chatSection.style.display = '';
}

// Expose for inline onclick
window._viewSession = function (sessionId) {
    showTerminal();
    attachPTY(sessionId);
};

window._reattachSession = function (sessionId) {
    showTerminal();
    attachPTY(sessionId);
};

window._openChat = function (sessionId, agent) {
    showChat();
    resumeChat(sessionId, agent);
};

window._deleteSession = function (sessionId) {
    var cached = getCachedSessions().filter(function (s) { return s.id !== sessionId; });
    setCachedSessions(cached);
    clearTermBuffer(sessionId);
    fetch('/api/app/sessions/' + sessionId, { method: 'DELETE' }).then(function () {
        loadHome();
    });
};

// ========================
// Chat (NLUX)
// ========================

function launchChat(agent) {
    setLastChatAgent(agent);
    updatePrimaryButtons();
    showChat();
    chatStatus.textContent = 'connecting...';
    chatDeleteBtn.style.display = 'none';

    var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    var url = proto + '//' + location.host + '/ws/pty';

    chatWs = new WebSocket(url);

    chatWs.onopen = function () {
        chatStatus.textContent = 'starting chat...';
        chatWs.send(JSON.stringify({
            type: 'chat.start',
            agent: agent,
        }));
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
        chatWs.send(JSON.stringify({
            type: 'chat.start',
            session_id: sessionId,
            agent: agent,
        }));
    };

    setupChatHandlers(chatWs, agent, sessionId);
}

var pendingHistory = null;

function setupChatHandlers(ws, agent, resumeSessionId) {
    pendingHistory = null;

    ws.onmessage = function (e) {
        var msg = JSON.parse(e.data);
        switch (msg.type) {
            case 'chat.history':
                // Store history for when chat.started arrives
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
                break;

            case 'chat.chunk':
                if (chatObserver) {
                    chatObserver.next(msg.text);
                }
                break;

            case 'chat.done':
                if (chatObserver) {
                    chatObserver.complete();
                    chatObserver = null;
                }
                chatContainer.classList.remove('thinking');
                break;

            case 'error':
                chatStatus.textContent = msg.message;
                chatContainer.classList.remove('thinking');
                if (chatObserver) {
                    chatObserver.error(new Error(msg.message));
                    chatObserver = null;
                }
                break;
        }
    };

    ws.onclose = function () {
        chatStatus.textContent = 'disconnected';
        chatObserver = null;
    };

    ws.onerror = function () {
        chatStatus.textContent = 'connection error';
    };
}

function mountNlux(agent, initialMessages) {
    // Clean up previous instance
    if (chatInstance) {
        chatInstance.unmount();
        chatInstance = null;
    }
    chatContainer.innerHTML = '';

    var adapter = {
        streamText: function (message, observer) {
            chatObserver = observer;
            chatContainer.classList.add('thinking');
            if (chatWs && chatWs.readyState === WebSocket.OPEN && chatSessionId) {
                chatWs.send(JSON.stringify({
                    type: 'chat.message',
                    session_id: chatSessionId,
                    content: message,
                }));
            } else {
                chatContainer.classList.remove('thinking');
                observer.error(new Error('not connected'));
            }
        }
    };

    var chat = createAiChat()
        .withAdapter(adapter)
        .withDisplayOptions({
            colorScheme: 'dark',
            height: '100%',
            width: '100%',
        })
        .withConversationOptions({
            historyPayloadSize: 0, // we manage history server-side
            layout: 'bubbles',
        })
        .withComposerOptions({
            placeholder: 'message ' + agent + '...',
            autoFocus: true,
        })
        .withPersonaOptions({
            assistant: {
                name: agent,
                avatar: 'https://ui-avatars.com/api/?name=' + agent.charAt(0).toUpperCase() + '&background=e94560&color=fff&size=32',
            },
        })
        .withMessageOptions({
            waitTimeBeforeStreamCompletion: 'never',
        });

    if (initialMessages && initialMessages.length > 0) {
        chat = chat.withInitialConversation(initialMessages);
    }

    chat.mount(chatContainer);
    chatInstance = chat;
}

function destroyChat() {
    if (chatInstance) {
        chatInstance.unmount();
        chatInstance = null;
    }
    if (chatWs) {
        chatWs.close();
        chatWs = null;
    }
    chatSessionId = null;
    chatObserver = null;
    chatContainer.innerHTML = '';
}

// ========================
// Terminal (xterm.js)
// ========================

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
                if (code >= 0 && code <= 31) {
                    sendPTYInput(String.fromCharCode(code));
                    return;
                }
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
}

var TERM_BUF_PREFIX = 'wt_termbuf_';

function saveTermBuffer() {
    if (!ptySessionId || !serializeAddon) return;
    clearTimeout(saveBufferTimer);
    saveBufferTimer = setTimeout(function () {
        try {
            var data = serializeAddon.serialize();
            // Cap at 200KB to avoid localStorage bloat
            if (data.length > 200000) data = data.slice(-200000);
            localStorage.setItem(TERM_BUF_PREFIX + ptySessionId, data);
        } catch (e) {}
    }, 500);
}

function restoreTermBuffer(sessionId) {
    try {
        var data = localStorage.getItem(TERM_BUF_PREFIX + sessionId);
        if (data && term) {
            term.write(data);
        }
    } catch (e) {}
}

function clearTermBuffer(sessionId) {
    try { localStorage.removeItem(TERM_BUF_PREFIX + sessionId); } catch (e) {}
}

function sendPTYInput(text) {
    if (!ptyWs || ptyWs.readyState !== WebSocket.OPEN || !ptySessionId) return;
    e2eEncrypt(text).then(function (encoded) {
        ptyWs.send(JSON.stringify({
            type: 'pty.input',
            session_id: ptySessionId,
            data: encoded,
        }));
    });
}

// E2E crypto
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

function setupPTYHandlers(ws, reattach) {
    ws.onmessage = function (e) {
        var msg = JSON.parse(e.data);
        switch (msg.type) {
            case 'pty.started':
                ptySessionId = msg.session_id;
                if (msg.public_key) {
                    deriveE2EKey(msg.public_key).then(function (key) {
                        e2eKey = key;
                        ptyStatus.textContent = msg.agent + (key ? ' (encrypted)' : ' (live)');
                    }).catch(function () {
                        ptyStatus.textContent = msg.agent + ' (live)';
                    });
                } else {
                    ptyStatus.textContent = msg.agent + ' (live)';
                }
                disconnectBtn.style.display = '';
                if (!reattach) term.clear();
                term.focus();

                term.onResize(function (size) {
                    if (ptyWs && ptyWs.readyState === WebSocket.OPEN && ptySessionId) {
                        ptyWs.send(JSON.stringify({
                            type: 'pty.resize',
                            session_id: ptySessionId,
                            cols: size.cols,
                            rows: size.rows,
                        }));
                    }
                });
                fitAddon.fit();
                break;

            case 'pty.output':
                e2eDecrypt(msg.data).then(function (bytes) {
                    term.write(bytes);
                    saveTermBuffer();
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
                ptyStatus.textContent = 'exited (code ' + msg.exit_code + ')';
                if (msg.session_id) clearTermBuffer(msg.session_id);
                ptySessionId = null;
                e2eKey = null;
                ephemeralPrivKey = null;
                disconnectBtn.style.display = 'none';
                term.writeln('\r\n\x1b[1;31m--- session ended ---\x1b[0m');
                break;

            case 'error':
                ptyStatus.textContent = msg.message;
                break;
        }
    };

    ws.onclose = function () {
        ptyStatus.textContent = 'disconnected';
        ptySessionId = null;
        disconnectBtn.style.display = 'none';
    };

    ws.onerror = function () {
        ptyStatus.textContent = 'connection error';
    };
}

function connectPTY(agent) {
    var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    var url = proto + '//' + location.host + '/ws/pty';

    ptyStatus.textContent = 'connecting...';

    ephemeralPrivKey = x25519.utils.randomSecretKey();
    var ephemeralPubKey = x25519.getPublicKey(ephemeralPrivKey);
    var pubKeyB64 = bytesToB64(ephemeralPubKey);
    e2eKey = null;

    ptyWs = new WebSocket(url);

    ptyWs.onopen = function () {
        ptyStatus.textContent = 'starting ' + agent + '...';
        ptyWs.send(JSON.stringify({
            type: 'pty.start',
            agent: agent,
            cols: term.cols,
            rows: term.rows,
            public_key: pubKeyB64,
        }));
    };

    setupPTYHandlers(ptyWs, false);
}

function attachPTY(sessionId) {
    var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    var url = proto + '//' + location.host + '/ws/pty';

    // Restore last known terminal content instantly
    term.clear();
    restoreTermBuffer(sessionId);

    ptyStatus.textContent = 'reconnecting...';

    ephemeralPrivKey = x25519.utils.randomSecretKey();
    var ephemeralPubKey = x25519.getPublicKey(ephemeralPrivKey);
    var pubKeyB64 = bytesToB64(ephemeralPubKey);
    e2eKey = null;

    ptyWs = new WebSocket(url);

    ptyWs.onopen = function () {
        ptyStatus.textContent = 'reattaching...';
        ptyWs.send(JSON.stringify({
            type: 'pty.attach',
            session_id: sessionId,
            public_key: pubKeyB64,
        }));
    };

    setupPTYHandlers(ptyWs, true);
}

function disconnectPTY() {
    if (ptyWs && ptyWs.readyState === WebSocket.OPEN && ptySessionId) {
        ptyWs.send(JSON.stringify({
            type: 'pty.kill',
            session_id: ptySessionId,
        }));
    }
    if (ptyWs) {
        ptyWs.close();
        ptyWs = null;
    }
    ptySessionId = null;
    e2eKey = null;
    ephemeralPrivKey = null;
    disconnectBtn.style.display = 'none';
    ptyStatus.textContent = 'disconnected';
}

function escapeHtml(str) {
    var div = document.createElement('div');
    div.textContent = str;
    return div.innerHTML;
}

if ('serviceWorker' in navigator) {
    navigator.serviceWorker.register('sw.js').catch(function () {});
}

init();
