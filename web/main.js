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
let ptyWingId = null;
let term = null;
let fitAddon = null;
let serializeAddon = null;
let saveBufferTimer = null;
let ctrlActive = false;
let altActive = false;
let currentUser = null;
let e2eKey = null;
let availableAgents = [];
let allProjects = [];
let wingsData = [];
let sessionsData = [];
let sessionNotifications = {};
let activeView = 'home';
let titleFlashTimer = null;
let appWs = null;
let appWsBackoff = 1000;
let latestVersion = '';
let ptyReconnecting = false;
let ptyBandwidthExceeded = false;

// Wing detail state
let currentWingId = null;
let wingPastSessions = {};  // wingId → {sessions:[], offset:0, hasMore:true}

// Chat state
let chatWs = null;
let chatSessionId = null;
let chatObserver = null;
let chatInstance = null;
let pendingHistory = null;

// DOM refs
const detailOverlay = document.getElementById('detail-overlay');
const detailBackdrop = document.getElementById('detail-backdrop');
const detailDialog = document.getElementById('detail-dialog');
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
const wingDetailSection = document.getElementById('wing-detail-section');
const wingDetailContent = document.getElementById('wing-detail-content');

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
var EGG_ORDER_KEY = 'wt_egg_order';
var TERM_THUMB_PREFIX = 'wt_termthumb_';
var WING_SESSIONS_PREFIX = 'wt_wing_sessions_';

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

    // Mark initial page load as home so back button works
    if (!location.hash.startsWith('#s/') && !location.hash.startsWith('#w/')) {
        history.replaceState({ view: 'home' }, '', location.pathname);
    }

    // Event handlers
    homeBtn.addEventListener('click', showHome);
    newSessionBtn.addEventListener('click', showPalette);
    userInfo.addEventListener('click', showAccountModal);
    userInfo.style.cursor = 'pointer';
    headerTitle.addEventListener('click', function() {
        if (ptySessionId) showSessionInfo();
    });

    chatDeleteBtn.addEventListener('click', function () {
        if (chatSessionId) {
            var cached = getCachedSessions().filter(function (s) { return s.id !== chatSessionId; });
            setCachedSessions(cached);
            var chatSess = sessionsData.find(function(s) { return s.id === chatSessionId; });
            var cWing = chatSess && wingsData.find(function(w) { return w.id === chatSess.wing_id; });
            if (cWing) {
                fetch('/api/app/wings/' + encodeURIComponent(cWing.wing_id) + '/sessions/' + chatSessionId, { method: 'DELETE' });
            }
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
                cyclePaletteAgent();
            } else {
                tabCompletePalette();
            }
        }
        if (e.key === '`') {
            e.preventDefault();
            cyclePaletteWing();
        }
    });

    // Chat link in palette footer (hidden for now)
    var chatLink = document.getElementById('palette-chat-link');
    if (chatLink) {
        chatLink.addEventListener('click', function(e) {
            e.preventDefault();
            var agent = currentPaletteAgent();
            hidePalette();
            launchChat(agent);
        });
    }

    window.addEventListener('resize', function () {
        if (term && fitAddon) fitAddon.fit();
    });

    initTerminal();
    loadHome();
    setInterval(loadHome, 30000);
    connectAppWS();

    // Deep link: #s/<sessionId> auto-attaches to session
    var hashMatch = location.hash.match(/^#s\/(.+)$/);
    if (hashMatch) {
        var deepSessionId = hashMatch[1];
        history.replaceState({ view: 'home' }, '', location.pathname);
        switchToSession(deepSessionId);
    }
    // Deep link: #w/<wingId> opens wing detail page
    var wingMatch = location.hash.match(/^#w\/(.+)$/);
    if (wingMatch) {
        navigateToWingDetail(wingMatch[1]);
    }
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
        if (orderMap.hasOwnProperty(w.wing_id)) {
            known.push(w);
        } else {
            unknown.push(w);
        }
    });
    known.sort(function(a, b) { return orderMap[a.wing_id] - orderMap[b.wing_id]; });
    return known.concat(unknown);
}
function getEggOrder() {
    try { var raw = localStorage.getItem(EGG_ORDER_KEY); return raw ? JSON.parse(raw) : []; }
    catch (e) { return []; }
}
function setEggOrder(order) {
    try { localStorage.setItem(EGG_ORDER_KEY, JSON.stringify(order)); } catch (e) {}
}
function sortSessionsByOrder(sessions) {
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

// === Browser identity key ===

var IDENTITY_PUBKEY_KEY = 'wt_identity_pubkey';
var IDENTITY_PRIVKEY_KEY = 'wt_identity_privkey';

function getOrCreateIdentityKey() {
    try {
        var storedPub = localStorage.getItem(IDENTITY_PUBKEY_KEY);
        var storedPriv = localStorage.getItem(IDENTITY_PRIVKEY_KEY);
        if (storedPub && storedPriv) return { pub: storedPub, priv: b64ToBytes(storedPriv) };
        var priv = x25519.utils.randomSecretKey();
        localStorage.setItem(IDENTITY_PRIVKEY_KEY, bytesToB64(priv));
        var pub = bytesToB64(x25519.getPublicKey(priv));
        localStorage.setItem(IDENTITY_PUBKEY_KEY, pub);
        return { pub: pub, priv: priv };
    } catch (e) { return { pub: '', priv: null }; }
}

var identityKey = getOrCreateIdentityKey();
var identityPubKey = identityKey.pub;

// === Copyable helper ===

function setupCopyable(container) {
    container.querySelectorAll('.copyable').forEach(function(el) {
        el.style.cursor = 'pointer';
        el.addEventListener('click', function() {
            navigator.clipboard.writeText(el.dataset.copy || el.textContent);
            var orig = el.textContent;
            el.textContent = 'copied';
            setTimeout(function() { el.textContent = orig; }, 1200);
        });
    });
}

// === Agent icons ===

var AGENT_ICONS = {
    claude: '<svg class="agent-icon" viewBox="0 0 16 16" fill="currentColor"><path d="m3.127 10.604 3.135-1.76.053-.153-.053-.085H6.11l-.525-.032-1.791-.048-1.554-.065-1.505-.08-.38-.081L0 7.832l.036-.234.32-.214.455.04 1.009.069 1.513.105 1.097.064 1.626.17h.259l.036-.105-.089-.065-.068-.064-1.566-1.062-1.695-1.121-.887-.646-.48-.327-.243-.306-.104-.67.435-.48.585.04.15.04.593.456 1.267.981 1.654 1.218.242.202.097-.068.012-.049-.109-.181-.9-1.626-.96-1.655-.428-.686-.113-.411a2 2 0 0 1-.068-.484l.496-.674L4.446 0l.662.089.279.242.411.94.666 1.48 1.033 2.014.302.597.162.553.06.17h.105v-.097l.085-1.134.157-1.392.154-1.792.052-.504.25-.605.497-.327.387.186.319.456-.045.294-.19 1.23-.37 1.93-.243 1.29h.142l.161-.16.654-.868 1.097-1.372.484-.545.565-.601.363-.287h.686l.505.751-.226.775-.707.895-.585.759-.839 1.13-.524.904.048.072.125-.012 1.897-.403 1.024-.186 1.223-.21.553.258.06.263-.218.536-1.307.323-1.533.307-2.284.54-.028.02.032.04 1.029.098.44.024h1.077l2.005.15.525.346.315.424-.053.323-.807.411-3.631-.863-.872-.218h-.12v.073l.726.71 1.331 1.202 1.667 1.55.084.383-.214.302-.226-.032-1.464-1.101-.565-.497-1.28-1.077h-.084v.113l.295.432 1.557 2.34.08.718-.112.234-.404.141-.444-.08-.911-1.28-.94-1.44-.759-1.291-.093.053-.448 4.821-.21.246-.484.186-.403-.307-.214-.496.214-.98.258-1.28.21-1.016.19-1.263.112-.42-.008-.028-.092.012-.953 1.307-1.448 1.957-1.146 1.227-.274.109-.477-.247.045-.44.266-.39 1.586-2.018.956-1.25.617-.723-.004-.105h-.036l-4.212 2.736-.75.096-.324-.302.04-.496.154-.162 1.267-.871z"/></svg>',
    codex: '<svg class="agent-icon" viewBox="0 0 16 16" fill="currentColor"><path d="M14.949 6.547a3.94 3.94 0 0 0-.348-3.273 4.11 4.11 0 0 0-4.4-1.934A4.1 4.1 0 0 0 8.423.2 4.15 4.15 0 0 0 6.305.086a4.1 4.1 0 0 0-1.891.948 4.04 4.04 0 0 0-1.158 1.753 4.1 4.1 0 0 0-1.563.679A4 4 0 0 0 .554 4.72a3.99 3.99 0 0 0 .502 4.731 3.94 3.94 0 0 0 .346 3.274 4.11 4.11 0 0 0 4.402 1.933c.382.425.852.764 1.377.995.526.231 1.095.35 1.67.346 1.78.002 3.358-1.132 3.901-2.804a4.1 4.1 0 0 0 1.563-.68 4 4 0 0 0 1.14-1.253 3.99 3.99 0 0 0-.506-4.716m-6.097 8.406a3.05 3.05 0 0 1-1.945-.694l.096-.054 3.23-1.838a.53.53 0 0 0 .265-.455v-4.49l1.366.778q.02.011.025.035v3.722c-.003 1.653-1.361 2.992-3.037 2.996m-6.53-2.75a2.95 2.95 0 0 1-.36-2.01l.095.057L5.29 12.09a.53.53 0 0 0 .527 0l3.949-2.246v1.555a.05.05 0 0 1-.022.041L6.473 13.3c-1.454.826-3.311.335-4.15-1.098m-.85-6.94A3.02 3.02 0 0 1 3.07 3.949v3.785a.51.51 0 0 0 .262.451l3.93 2.237-1.366.779a.05.05 0 0 1-.048 0L2.585 9.342a2.98 2.98 0 0 1-1.113-4.094zm11.216 2.571L8.747 5.576l1.362-.776a.05.05 0 0 1 .048 0l3.265 1.86a3 3 0 0 1 1.173 1.207 2.96 2.96 0 0 1-.27 3.2 3.05 3.05 0 0 1-1.36.997V8.279a.52.52 0 0 0-.276-.445m1.36-2.015-.097-.057-3.226-1.855a.53.53 0 0 0-.53 0L6.249 6.153V4.598a.04.04 0 0 1 .019-.04L9.533 2.7a3.07 3.07 0 0 1 3.257.139c.474.325.843.778 1.066 1.303.223.526.289 1.103.191 1.664zM5.503 8.575 4.139 7.8a.05.05 0 0 1-.026-.037V4.049c0-.57.166-1.127.476-1.607s.752-.864 1.275-1.105a3.08 3.08 0 0 1 3.234.41l-.096.054-3.23 1.838a.53.53 0 0 0-.265.455zm.742-1.577 1.758-1 1.762 1v2l-1.755 1-1.762-1z"/></svg>',
    ollama: '<svg class="agent-icon" viewBox="0 0 24 24" fill="currentColor"><path d="M16.361 10.26a.894.894 0 0 0-.558.47l-.072.148.001.207c0 .193.004.217.059.353.076.193.152.312.291.448.24.238.51.3.872.205a.86.86 0 0 0 .517-.436.752.752 0 0 0 .08-.498c-.064-.453-.33-.782-.724-.897a1.06 1.06 0 0 0-.466 0zm-9.203.005c-.305.096-.533.32-.65.639a1.187 1.187 0 0 0-.06.52c.057.309.31.59.598.667.362.095.632.033.872-.205.14-.136.215-.255.291-.448.055-.136.059-.16.059-.353l.001-.207-.072-.148a.894.894 0 0 0-.565-.472 1.02 1.02 0 0 0-.474.007m4.184 2c-.131.071-.223.25-.195.383.031.143.157.288.353.407.105.063.112.072.117.136.004.038-.01.146-.029.243-.02.094-.036.194-.036.222.002.074.07.195.143.253.064.052.076.054.255.059.164.005.198.001.264-.03.169-.082.212-.234.15-.525-.052-.243-.042-.28.087-.355.137-.08.281-.219.324-.314a.365.365 0 0 0-.175-.48.394.394 0 0 0-.181-.033c-.126 0-.207.03-.355.124l-.085.053-.053-.032c-.219-.13-.259-.145-.391-.143a.396.396 0 0 0-.193.032m.39-2.195c-.373.036-.475.05-.654.086-.291.06-.68.195-.951.328-.94.46-1.589 1.226-1.787 2.114-.04.176-.045.234-.045.53 0 .294.005.357.043.524.264 1.16 1.332 2.017 2.714 2.173.3.033 1.596.033 1.896 0 1.11-.125 2.064-.727 2.493-1.571.114-.226.169-.372.22-.602.039-.167.044-.23.044-.523 0-.297-.005-.355-.045-.531-.288-1.29-1.539-2.304-3.072-2.497a6.873 6.873 0 0 0-.855-.031m.645.937a3.283 3.283 0 0 1 1.44.514c.223.148.537.458.671.662.166.251.26.508.303.82.02.143.01.251-.043.482-.08.345-.332.705-.672.957a3.115 3.115 0 0 1-.689.348c-.382.122-.632.144-1.525.138-.582-.006-.686-.01-.853-.042-.57-.107-1.022-.334-1.35-.68-.264-.28-.385-.535-.45-.946-.03-.192.025-.509.137-.776.136-.326.488-.73.836-.963.403-.269.934-.46 1.422-.512.187-.02.586-.02.773-.002m-5.503-11a1.653 1.653 0 0 0-.683.298C5.617.74 5.173 1.666 4.985 2.819c-.07.436-.119 1.04-.119 1.503 0 .544.064 1.24.155 1.721.02.107.031.202.023.208a8.12 8.12 0 0 1-.187.152 5.324 5.324 0 0 0-.949 1.02 5.49 5.49 0 0 0-.94 2.339 6.625 6.625 0 0 0-.023 1.357c.091.78.325 1.438.727 2.04l.13.195-.037.064c-.269.452-.498 1.105-.605 1.732-.084.496-.095.629-.095 1.294 0 .67.009.803.088 1.266.095.555.288 1.143.503 1.534.071.128.243.393.264.407.007.003-.014.067-.046.141a7.405 7.405 0 0 0-.548 1.873c-.062.417-.071.552-.071.991 0 .56.031.832.148 1.279L3.42 24h1.478l-.05-.091c-.297-.552-.325-1.575-.068-2.597.117-.472.25-.819.498-1.296l.148-.29v-.177c0-.165-.003-.184-.057-.293a.915.915 0 0 0-.194-.25 1.74 1.74 0 0 1-.385-.543c-.424-.92-.506-2.286-.208-3.451.124-.486.329-.918.544-1.154a.787.787 0 0 0 .223-.531c0-.195-.07-.355-.224-.522a3.136 3.136 0 0 1-.817-1.729c-.14-.96.114-2.005.69-2.834.563-.814 1.353-1.336 2.237-1.475.199-.033.57-.028.776.01.226.04.367.028.512-.041.179-.085.268-.19.374-.431.093-.215.165-.333.36-.576.234-.29.46-.489.822-.729.413-.27.884-.467 1.352-.561.17-.035.25-.04.569-.04.319 0 .398.005.569.04a4.07 4.07 0 0 1 1.914.997c.117.109.398.457.488.602.034.057.095.177.132.267.105.241.195.346.374.43.14.068.286.082.503.045.343-.058.607-.053.943.016 1.144.23 2.14 1.173 2.581 2.437.385 1.108.276 2.267-.296 3.153-.097.15-.193.27-.333.419-.301.322-.301.722-.001 1.053.493.539.801 1.866.708 3.036-.062.772-.26 1.463-.533 1.854a2.096 2.096 0 0 1-.224.258.916.916 0 0 0-.194.25c-.054.109-.057.128-.057.293v.178l.148.29c.248.476.38.823.498 1.295.253 1.008.231 2.01-.059 2.581a.845.845 0 0 0-.044.098c0 .006.329.009.732.009h.73l.02-.074.036-.134c.019-.076.057-.3.088-.516.029-.217.029-1.016 0-1.258-.11-.875-.295-1.57-.597-2.226-.032-.074-.053-.138-.046-.141.008-.005.057-.074.108-.152.376-.569.607-1.284.724-2.228.031-.26.031-1.378 0-1.628-.083-.645-.182-1.082-.348-1.525a6.083 6.083 0 0 0-.329-.7l-.038-.064.131-.194c.402-.604.636-1.262.727-2.04a6.625 6.625 0 0 0-.024-1.358 5.512 5.512 0 0 0-.939-2.339 5.325 5.325 0 0 0-.95-1.02 8.097 8.097 0 0 1-.186-.152.692.692 0 0 1 .023-.208c.208-1.087.201-2.443-.017-3.503-.19-.924-.535-1.658-.98-2.082-.354-.338-.716-.482-1.15-.455-.996.059-1.8 1.205-2.116 3.01a6.805 6.805 0 0 0-.097.726c0 .036-.007.066-.015.066a.96.96 0 0 1-.149-.078A4.857 4.857 0 0 0 12 3.03c-.832 0-1.687.243-2.456.698a.958.958 0 0 1-.148.078c-.008 0-.015-.03-.015-.066a6.71 6.71 0 0 0-.097-.725C8.997 1.392 8.337.319 7.46.048a2.096 2.096 0 0 0-.585-.041m.293 1.402c.248.197.523.759.682 1.388.03.113.06.244.069.292.007.047.026.152.041.233.067.365.098.76.102 1.24l.002.475-.12.175-.118.178h-.278c-.324 0-.646.041-.954.124l-.238.06c-.033.007-.038-.003-.057-.144a8.438 8.438 0 0 1 .016-2.323c.124-.788.413-1.501.696-1.711.067-.05.079-.049.157.013m9.825-.012c.17.126.358.46.498.888.28.854.36 2.028.212 3.145-.019.14-.024.151-.057.144l-.238-.06a3.693 3.693 0 0 0-.954-.124h-.278l-.119-.178-.119-.175.002-.474c.004-.669.066-1.19.214-1.772.157-.623.434-1.185.68-1.382.078-.062.09-.063.159-.012z"/></svg>',
    gemini: '<svg class="agent-icon" viewBox="0 0 24 24" fill="currentColor"><path d="M11.04 19.32Q12 21.51 12 24q0-2.49.93-4.68.96-2.19 2.58-3.81t3.81-2.55Q21.51 12 24 12q-2.49 0-4.68-.93a12.3 12.3 0 0 1-3.81-2.58 12.3 12.3 0 0 1-2.58-3.81Q12 2.49 12 0q0 2.49-.96 4.68-.93 2.19-2.55 3.81a12.3 12.3 0 0 1-3.81 2.58Q2.49 12 0 12q2.49 0 4.68.96 2.19.93 3.81 2.55t2.55 3.81"/></svg>',
    cursor: '<svg class="agent-icon" viewBox="85 65 345 380" fill="currentColor"><path d="m415.035 156.35-151.503-87.4695c-4.865-2.8094-10.868-2.8094-15.733 0l-151.4969 87.4695c-4.0897 2.362-6.6146 6.729-6.6146 11.459v176.383c0 4.73 2.5249 9.097 6.6146 11.458l151.5039 87.47c4.865 2.809 10.868 2.809 15.733 0l151.504-87.47c4.089-2.361 6.614-6.728 6.614-11.458v-176.383c0-4.73-2.525-9.097-6.614-11.459zm-9.516 18.528-146.255 253.32c-.988 1.707-3.599 1.01-3.599-.967v-165.872c0-3.314-1.771-6.379-4.644-8.044l-143.645-82.932c-1.707-.988-1.01-3.599.968-3.599h292.509c4.154 0 6.75 4.503 4.673 8.101h-.007z"/></svg>',
};

function agentIcon(name) {
    return AGENT_ICONS[name] || '';
}

function agentWithIcon(name) {
    return agentIcon(name) + escapeHtml(name);
}

// === Reconnect Banner ===

var reconnectBannerTimer = null;
function showReconnectBanner() {
    if (reconnectBannerTimer) return; // already pending
    reconnectBannerTimer = setTimeout(function() {
        var banner = document.getElementById('reconnect-banner');
        if (banner) banner.style.display = '';
    }, 2000);
}

function hideReconnectBanner() {
    if (reconnectBannerTimer) { clearTimeout(reconnectBannerTimer); reconnectBannerTimer = null; }
    var banner = document.getElementById('reconnect-banner');
    if (banner) banner.style.display = 'none';
}

// === Dashboard WebSocket (real-time wing status) ===

function connectAppWS() {
    if (appWs) { try { appWs.close(); } catch(e) {} }
    var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    appWs = new WebSocket(proto + '//' + location.host + '/ws/app');
    appWs.onopen = function() {
        appWsBackoff = 1000;
        hideReconnectBanner();
        // Re-fetch state after reconnect
        loadHome();
    };
    appWs.onmessage = function(e) {
        try {
            var msg = JSON.parse(e.data);
            if (msg.type === 'relay.restart') {
                showReconnectBanner();
                appWs = null;
                setTimeout(connectAppWS, 500);
                return;
            }
            applyWingEvent(msg);
        } catch(err) {}
    };
    appWs.onclose = function() {
        appWs = null;
        showReconnectBanner();
        setTimeout(connectAppWS, appWsBackoff);
        appWsBackoff = Math.min(appWsBackoff * 2, 10000);
    };
    appWs.onerror = function() { appWs.close(); };
}

function applyWingEvent(ev) {
    var needsFullRender = false;
    if (ev.type === 'wing.online') {
        var found = false;
        wingsData.forEach(function(w) {
            if (w.wing_id === ev.wing_id) {
                w.online = true;
                // Only clear updating flag if version actually changed
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
                found = true;
            }
        });
        if (!found) {
            // New wing goes to end — needs full render to add DOM node
            wingsData.push({
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
            });
            needsFullRender = true;
        }
    } else if (ev.type === 'wing.offline') {
        wingsData.forEach(function(w) {
            if (w.wing_id === ev.wing_id) {
                w.online = false;
            }
        });
    }

    rebuildAgentLists();
    setCachedWings(wingsData.map(function(w) {
        return { wing_id: w.wing_id, hostname: w.hostname, id: w.id, platform: w.platform, version: w.version, agents: w.agents, labels: w.labels, projects: w.projects, wing_label: w.wing_label };
    }));
    updateHeaderStatus();
    if (activeView === 'home') {
        if (needsFullRender) {
            renderDashboard();
        } else {
            updateWingCardStatus(ev.wing_id);
        }
        pingWingDot(ev.wing_id);
    } else if (activeView === 'wing-detail' && currentWingId === ev.wing_id) {
        renderWingDetailPage(currentWingId);
    }
    if (commandPalette.style.display !== 'none') {
        updatePaletteState(true);
    }

    // Wing just came online — immediately fetch its sessions
    if (ev.type === 'wing.online' && ev.wing_id) {
        fetchWingSessions(ev.wing_id).then(function(sessions) {
            if (sessions.length > 0) {
                // Add new wing's sessions to existing sessions from other wings
                var otherSessions = sessionsData.filter(function(s) {
                    return s.wing_id !== sessions[0].wing_id;
                });
                mergeWingSessions(otherSessions.concat(sessions));
                renderSidebar();
                if (activeView === 'home') renderDashboard();
            }
        });
    }
}

function pingWingDot(wingId) {
    requestAnimationFrame(function() {
        var card = wingStatusEl.querySelector('.wing-box[data-wing-id="' + wingId + '"]');
        if (!card) return;
        var dot = card.querySelector('.wing-dot');
        if (!dot) return;
        dot.classList.remove('dot-ping');
        void dot.offsetWidth;
        dot.classList.add('dot-ping');
    });
}

// Update a single wing card's status dot without full re-render
function updateWingCardStatus(wingId) {
    var card = wingStatusEl.querySelector('.wing-box[data-wing-id="' + wingId + '"]');
    if (!card) {
        // Card not in DOM — need full render
        renderDashboard();
        return;
    }
    var w = wingsData.find(function(w) { return w.wing_id === wingId; });
    if (!w) return;
    var dot = card.querySelector('.wing-dot');
    if (dot) {
        dot.classList.toggle('dot-live', w.online !== false);
        dot.classList.toggle('dot-offline', w.online === false);
    }
}

// Update header status dot to reflect wing connectivity
function updateHeaderStatus() {
    var indicator = document.getElementById('wing-indicator');
    if (!indicator) return;
    var anyOnline = wingsData.some(function(w) { return w.online !== false; });
    indicator.classList.toggle('dot-live', anyOnline);
    indicator.classList.toggle('dot-offline', !anyOnline);
    indicator.style.display = wingsData.length > 0 ? '' : 'none';
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
            allProjects.push({ name: p.name, path: p.path, wingId: w.id });
        });
    });
}

// === Data Loading ===

// fetchWingSessions fetches active sessions for a single wing by wing_id.
async function fetchWingSessions(wingId) {
    try {
        var resp = await fetch('/api/app/wings/' + encodeURIComponent(wingId) + '/sessions?active=true');
        if (resp.ok) return await resp.json() || [];
    } catch (e) {}
    return [];
}

// mergeWingSessions replaces sessionsData with live data, deduped by session id.
function mergeWingSessions(allSessions) {
    var seen = {};
    var deduped = [];
    allSessions.forEach(function(s) {
        if (!seen[s.id]) {
            seen[s.id] = true;
            deduped.push(s);
        }
    });
    sessionsData = sortSessionsByOrder(deduped);
    setEggOrder(sessionsData.map(function(s) { return s.id; }));
}

async function loadHome() {
    var wings = [];
    try {
        var wingsResp = await fetch('/api/app/wings');
        if (wingsResp.ok) wings = await wingsResp.json() || [];
    } catch (e) {
        wings = [];
    }

    // Merge: API wings are online, cached wings not in API become offline
    wings.forEach(function (w) { w.online = true; });
    var apiWings = {};
    wings.forEach(function(w) { apiWings[w.wing_id] = true; });
    // Preserve in-memory wings not in API response (rollout/reconnecting) as offline
    wingsData.forEach(function(ew) {
        if (!apiWings[ew.wing_id]) {
            ew.online = false;
            wings.push(ew);
        }
    });
    wingsData = sortWingsByOrder(wings);

    // Extract latest_version from any wing response
    wingsData.forEach(function(w) {
        if (w.latest_version) latestVersion = w.latest_version;
    });

    // Cache for next load (only essential fields)
    setCachedWings(wingsData.map(function (w) {
        return { wing_id: w.wing_id, hostname: w.hostname, id: w.id, platform: w.platform, version: w.version, agents: w.agents, labels: w.labels, projects: w.projects, wing_label: w.wing_label };
    }));

    rebuildAgentLists();
    updateHeaderStatus();

    // Fetch sessions per-wing for all online wings
    var onlineWings = wingsData.filter(function(w) { return w.online !== false && w.wing_id; });
    var sessionPromises = onlineWings.map(function(w) { return fetchWingSessions(w.wing_id); });
    var results = await Promise.all(sessionPromises);
    var allSessions = [];
    results.forEach(function(r) { allSessions = allSessions.concat(r); });
    mergeWingSessions(allSessions);

    // Sync terminal bell attention flags from wing
    sessionsData.forEach(function(s) {
        if (s.needs_attention && s.id !== ptySessionId) {
            setNotification(s.id);
        } else if (!s.needs_attention && sessionNotifications[s.id]) {
            clearNotification(s.id);
        }
    });

    renderSidebar();
    if (activeView === 'home') renderDashboard();
    if (activeView === 'wing-detail' && currentWingId) renderWingDetailPage(currentWingId);

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

function wingDisplayName(wing) {
    if (!wing) return '';
    return wing.wing_label || wing.hostname || wing.wing_id.substring(0, 8);
}
function wingNameById(wingId) {
    var wing = wingsData.find(function(w) { return w.id === wingId; });
    return wing ? wingDisplayName(wing) : '';
}

function sessionTitle(agent, wingId) {
    var name = wingNameById(wingId);
    if (name) return name + ' \u00b7 ' + agent;
    return agent || '';
}

function renderSidebar() {
    var tabs = sessionsData.filter(function(s) { return (s.kind || 'terminal') !== 'chat'; }).map(function(s) {
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
            e.dataTransfer.setData('text/plain', card.dataset.wingId);
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
            if (dragSrc.compareDocumentPosition(card) & Node.DOCUMENT_POSITION_FOLLOWING) {
                grid.insertBefore(dragSrc, card.nextSibling);
            } else {
                grid.insertBefore(dragSrc, card);
            }
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
            if (touchSrc.compareDocumentPosition(targetCard) & Node.DOCUMENT_POSITION_FOLLOWING) {
                grid.insertBefore(touchSrc, targetCard.nextSibling);
            } else {
                grid.insertBefore(touchSrc, targetCard);
            }
            saveWingOrder();
        }
        touchSrc = null;
    }, { passive: true });
}

function saveWingOrder() {
    var order = [];
    wingStatusEl.querySelectorAll('.wing-box').forEach(function(card) {
        if (card.dataset.wingId) order.push(card.dataset.wingId);
    });
    setWingOrder(order);
    // Sync wingsData to match DOM order
    var byWing = {};
    wingsData.forEach(function(w) { byWing[w.wing_id] = w; });
    var reordered = [];
    order.forEach(function(mid) { if (byWing[mid]) reordered.push(byWing[mid]); });
    // Add any not in order (shouldn't happen, but defensive)
    wingsData.forEach(function(w) { if (order.indexOf(w.wing_id) === -1) reordered.push(w); });
    wingsData = reordered;
}

function setupEggDrag() {
    var grid = sessionsList.querySelector('.egg-grid');
    if (!grid) return;
    var cards = grid.querySelectorAll('.egg-box');
    var dragSrc = null;

    cards.forEach(function(card) {
        card.setAttribute('draggable', 'true');
        card.addEventListener('dragstart', function(e) {
            dragSrc = card;
            card.classList.add('dragging');
            e.dataTransfer.effectAllowed = 'move';
            e.dataTransfer.setData('text/plain', card.dataset.sid);
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
            if (dragSrc.compareDocumentPosition(card) & Node.DOCUMENT_POSITION_FOLLOWING) {
                grid.insertBefore(dragSrc, card.nextSibling);
            } else {
                grid.insertBefore(dragSrc, card);
            }
            saveEggOrder();
        });
    });

    // Touch drag (mobile)
    var touchSrc = null;

    cards.forEach(function(card) {
        card.addEventListener('touchstart', function(e) {
            if (e.target.closest('.egg-delete')) return;
            touchSrc = card;
            card.classList.add('dragging');
        }, { passive: true });
    });

    grid.addEventListener('touchmove', function(e) {
        if (!touchSrc) return;
        e.preventDefault();
        var touch = e.touches[0];
        var target = document.elementFromPoint(touch.clientX, touch.clientY);
        var targetCard = target ? target.closest('.egg-box') : null;
        cards.forEach(function(c) { c.classList.remove('drag-over'); });
        if (targetCard && targetCard !== touchSrc) {
            targetCard.classList.add('drag-over');
        }
    }, { passive: false });

    grid.addEventListener('touchend', function(e) {
        if (!touchSrc) return;
        var touch = e.changedTouches[0];
        var target = document.elementFromPoint(touch.clientX, touch.clientY);
        var targetCard = target ? target.closest('.egg-box') : null;
        cards.forEach(function(c) { c.classList.remove('drag-over'); });
        touchSrc.classList.remove('dragging');
        if (targetCard && targetCard !== touchSrc) {
            if (touchSrc.compareDocumentPosition(targetCard) & Node.DOCUMENT_POSITION_FOLLOWING) {
                grid.insertBefore(touchSrc, targetCard.nextSibling);
            } else {
                grid.insertBefore(touchSrc, targetCard);
            }
            saveEggOrder();
        }
        touchSrc = null;
    }, { passive: true });
}

function saveEggOrder() {
    var order = [];
    sessionsList.querySelectorAll('.egg-box').forEach(function(card) {
        if (card.dataset.sid) order.push(card.dataset.sid);
    });
    setEggOrder(order);
    // Sync sessionsData to match DOM order
    var byId = {};
    sessionsData.forEach(function(s) { byId[s.id] = s; });
    var reordered = [];
    order.forEach(function(sid) { if (byId[sid]) reordered.push(byId[sid]); });
    sessionsData.forEach(function(s) { if (order.indexOf(s.id) === -1) reordered.push(s); });
    sessionsData = reordered;
}

function showAccountModal() {
    if (!currentUser) return;
    var tier = currentUser.tier || 'free';
    var email = currentUser.email || '';
    var provider = currentUser.provider || '';

    var pubKeyShort = identityPubKey ? identityPubKey.substring(0, 16) + '...' : 'none';
    var html = '<h3>account</h3>' +
        '<div class="detail-row"><span class="detail-key">name</span><span class="detail-val">' + escapeHtml(currentUser.display_name || '') + '</span></div>' +
        (email ? '<div class="detail-row"><span class="detail-key">email</span><span class="detail-val">' + escapeHtml(email) + '</span></div>' : '') +
        '<div class="detail-row"><span class="detail-key">login</span><span class="detail-val">' + escapeHtml(provider) + '</span></div>' +
        '<div class="detail-row"><span class="detail-key">tier</span><span class="detail-val">' + escapeHtml(tier) + '</span></div>' +
        '<div class="detail-row"><span class="detail-key">browser key</span><span class="detail-val copyable" data-copy="' + escapeHtml(identityPubKey) + '">' + escapeHtml(pubKeyShort) + '</span></div>';

    if (tier === 'free') {
        html += '<div class="detail-actions"><button class="btn-sm btn-accent" id="account-upgrade">give me pro</button></div>';
    } else {
        html += '<div class="detail-actions"><button class="btn-sm" id="account-downgrade" style="color:var(--text-dim)">cancel pro</button></div>';
    }

    html += '<div class="detail-actions" style="margin-top:12px"><button class="btn-sm btn-danger" id="account-logout">log out</button></div>';

    // Org section placeholder — filled async
    html += '<div id="account-org-section" style="margin-top:16px;border-top:1px solid var(--border);padding-top:12px">' +
        '<span class="text-dim">loading orgs...</span></div>';

    detailDialog.innerHTML = html;
    detailOverlay.classList.add('open');

    var upgradeBtn = document.getElementById('account-upgrade');
    if (upgradeBtn) {
        upgradeBtn.addEventListener('click', function() {
            upgradeBtn.textContent = 'upgrading...';
            upgradeBtn.disabled = true;
            fetch('/api/app/upgrade', { method: 'POST' })
                .then(function(r) { return r.json(); })
                .then(function(data) {
                    if (data.tier) currentUser.tier = data.tier;
                    upgradeBtn.textContent = 'done — you are pro';
                })
                .catch(function() { upgradeBtn.textContent = 'failed'; upgradeBtn.disabled = false; });
        });
    }

    var downgradeBtn = document.getElementById('account-downgrade');
    if (downgradeBtn) {
        downgradeBtn.addEventListener('click', function() {
            downgradeBtn.textContent = 'canceling...';
            downgradeBtn.disabled = true;
            fetch('/api/app/downgrade', { method: 'POST' })
                .then(function(r) { return r.json(); })
                .then(function(data) {
                    if (data.tier) currentUser.tier = data.tier;
                    downgradeBtn.textContent = 'done — ' + (data.tier || 'free');
                })
                .catch(function() { downgradeBtn.textContent = 'failed'; downgradeBtn.disabled = false; });
        });
    }

    var logoutBtn = document.getElementById('account-logout');
    if (logoutBtn) {
        logoutBtn.addEventListener('click', function() {
            fetch('/auth/logout', { method: 'POST' }).then(function() {
                window.location.href = '/';
            });
        });
    }

    // Load org section async
    loadAccountOrgSection();
}

function loadAccountOrgSection() {
    var container = document.getElementById('account-org-section');
    if (!container) return;

    fetch('/api/orgs')
        .then(function(r) { return r.json(); })
        .then(function(orgs) {
            if (!orgs || orgs.length === 0) {
                renderNoOrg(container);
            } else {
                renderOrg(container, orgs[0]);
            }
        })
        .catch(function() {
            container.innerHTML = '<span class="text-dim">failed to load orgs</span>';
        });
}

function renderNoOrg(container) {
    container.innerHTML =
        '<h3 style="margin:0 0 8px">org</h3>' +
        '<div style="display:flex;gap:8px;align-items:center">' +
            '<input type="text" id="org-create-name" placeholder="team name" style="flex:1;padding:4px 8px;background:var(--bg-card);border:1px solid var(--border);color:var(--text);border-radius:4px">' +
            '<button class="btn-sm btn-accent" id="org-create-btn">create</button>' +
        '</div>';

    document.getElementById('org-create-btn').addEventListener('click', function() {
        var btn = this;
        var name = document.getElementById('org-create-name').value.trim();
        if (!name) return;
        btn.textContent = 'creating...';
        btn.disabled = true;
        fetch('/api/orgs', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ name: name })
        })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.error) { btn.textContent = 'failed'; btn.disabled = false; return; }
            btn.textContent = 'done';
            loadAccountOrgSection();
        })
        .catch(function() { btn.textContent = 'failed'; btn.disabled = false; });
    });
}

function renderOrg(container, org) {
    var html = '<h3 style="margin:0 0 8px">' + escapeHtml(org.name) + '</h3>';

    if (!org.is_owner) {
        html += '<div class="detail-row"><span class="detail-val text-dim">member</span></div>';
        container.innerHTML = html;
        return;
    }

    if (!org.has_subscription) {
        // No subscription — show "give me seats"
        html += '<div class="detail-row"><span class="detail-val text-dim">no active plan</span></div>' +
            '<div style="display:flex;gap:8px;align-items:center;margin-top:8px">' +
                '<input type="number" id="org-seats-input" min="1" value="1" style="width:60px;padding:4px 8px;background:var(--bg-card);border:1px solid var(--border);color:var(--text);border-radius:4px">' +
                '<span class="text-dim" style="font-size:12px">seats</span>' +
                '<button class="btn-sm btn-accent" id="org-give-seats-btn">give me seats</button>' +
            '</div>' +
            '<div class="text-dim" style="font-size:11px;margin-top:4px">1 seat includes you. each additional seat adds one team member.</div>';
        container.innerHTML = html;

        document.getElementById('org-give-seats-btn').addEventListener('click', function() {
            var btn = this;
            var seats = parseInt(document.getElementById('org-seats-input').value) || 1;
            btn.textContent = 'working...';
            btn.disabled = true;
            fetch('/api/orgs/' + org.slug + '/upgrade', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ seats: seats })
            })
            .then(function(r) { return r.json(); })
            .then(function(data) {
                if (data.error) { btn.textContent = 'failed'; btn.disabled = false; return; }
                btn.textContent = 'done';
                loadAccountOrgSection();
            })
            .catch(function() { btn.textContent = 'failed'; btn.disabled = false; });
        });
        return;
    }

    // Has subscription — show seat usage, add seats, invite, members, cancel
    html += '<div class="detail-row"><span class="detail-key">plan</span><span class="detail-val">' + escapeHtml(org.plan || 'team') + '</span></div>' +
        '<div class="detail-row"><span class="detail-key">seats</span><span class="detail-val">' + (org.seats_used || 0) + '/' + (org.seats_total || 0) + ' used</span></div>';

    // Add seats
    html += '<div style="display:flex;gap:8px;align-items:center;margin-top:8px">' +
        '<input type="number" id="org-add-seats-input" min="' + ((org.seats_total || 0) + 1) + '" value="' + ((org.seats_total || 0) + 1) + '" style="width:60px;padding:4px 8px;background:var(--bg-card);border:1px solid var(--border);color:var(--text);border-radius:4px">' +
        '<span class="text-dim" style="font-size:12px">new total</span>' +
        '<button class="btn-sm btn-accent" id="org-add-seats-btn">add seats</button>' +
    '</div>';

    // Invite
    html += '<div style="display:flex;gap:8px;align-items:center;margin-top:8px">' +
        '<input type="email" id="org-invite-email" placeholder="email" style="flex:1;padding:4px 8px;background:var(--bg-card);border:1px solid var(--border);color:var(--text);border-radius:4px">' +
        '<button class="btn-sm btn-accent" id="org-invite-btn">invite</button>' +
    '</div>';

    // Members placeholder
    html += '<div id="org-members-list" style="margin-top:8px"><span class="text-dim">loading members...</span></div>';

    // Cancel
    html += '<div class="detail-actions" style="margin-top:12px"><button class="btn-sm" id="org-cancel-btn" style="color:var(--text-dim)">cancel subscription</button></div>';

    container.innerHTML = html;

    // Wire add seats
    document.getElementById('org-add-seats-btn').addEventListener('click', function() {
        var btn = this;
        var seats = parseInt(document.getElementById('org-add-seats-input').value);
        if (!seats || seats <= (org.seats_total || 0)) return;
        btn.textContent = 'working...';
        btn.disabled = true;
        fetch('/api/orgs/' + org.slug + '/upgrade', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ seats: seats })
        })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.error) { btn.textContent = 'failed'; btn.disabled = false; return; }
            btn.textContent = 'done';
            loadAccountOrgSection();
        })
        .catch(function() { btn.textContent = 'failed'; btn.disabled = false; });
    });

    // Wire invite
    document.getElementById('org-invite-btn').addEventListener('click', function() {
        var btn = this;
        var emailInput = document.getElementById('org-invite-email');
        var invEmail = emailInput.value.trim();
        if (!invEmail) return;
        btn.textContent = 'working...';
        btn.disabled = true;
        fetch('/api/orgs/' + org.slug + '/invite', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ emails: [invEmail] })
        })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.error) { btn.textContent = 'failed'; btn.disabled = false; return; }
            btn.textContent = 'invited';
            emailInput.value = '';
            loadOrgMembers(org);
        })
        .catch(function() { btn.textContent = 'failed'; btn.disabled = false; });
    });

    // Wire cancel
    document.getElementById('org-cancel-btn').addEventListener('click', function() {
        var btn = this;
        btn.textContent = 'canceling...';
        btn.disabled = true;
        fetch('/api/orgs/' + org.slug + '/cancel', { method: 'POST' })
        .then(function(r) { return r.json(); })
        .then(function(data) {
            if (data.error) { btn.textContent = 'failed'; btn.disabled = false; return; }
            btn.textContent = 'done';
            loadAccountOrgSection();
        })
        .catch(function() { btn.textContent = 'failed'; btn.disabled = false; });
    });

    // Load members
    loadOrgMembers(org);
}

function loadOrgMembers(org) {
    var list = document.getElementById('org-members-list');
    if (!list) return;

    fetch('/api/orgs/' + org.slug + '/members')
        .then(function(r) { return r.json(); })
        .then(function(data) {
            var html = '<div style="font-size:12px;color:var(--text-dim);margin-bottom:4px">members</div>';
            var members = data.members || [];
            for (var i = 0; i < members.length; i++) {
                var m = members[i];
                var display = m.email || m.display_name || m.user_id;
                html += '<div style="display:flex;justify-content:space-between;align-items:center;padding:2px 0">' +
                    '<span>' + escapeHtml(display) + ' <span class="text-dim">(' + escapeHtml(m.role) + ')</span></span>';
                if (m.role !== 'owner' && org.is_owner) {
                    html += '<button class="btn-sm btn-danger org-remove-member" data-uid="' + escapeHtml(m.user_id) + '" style="font-size:11px;padding:1px 6px">remove</button>';
                }
                html += '</div>';
            }
            var invites = data.invites || [];
            for (var j = 0; j < invites.length; j++) {
                html += '<div style="padding:2px 0"><span class="text-dim">' + escapeHtml(invites[j].email) + ' (pending)</span></div>';
            }
            list.innerHTML = html;

            // Wire remove buttons
            var removeBtns = list.querySelectorAll('.org-remove-member');
            removeBtns.forEach(function(btn) {
                btn.addEventListener('click', function() {
                    var uid = this.getAttribute('data-uid');
                    this.textContent = '...';
                    this.disabled = true;
                    var self = this;
                    fetch('/api/orgs/' + org.slug + '/members/' + uid, { method: 'DELETE' })
                    .then(function(r) { return r.json(); })
                    .then(function() { loadOrgMembers(org); })
                    .catch(function() { self.textContent = 'failed'; self.disabled = false; });
                });
            });
        })
        .catch(function() {
            list.innerHTML = '<span class="text-dim">failed to load members</span>';
        });
}

function hideDetailModal() {
    detailOverlay.classList.remove('open');
    detailDialog.innerHTML = '';
}

function navigateToWingDetail(wingId, pushHistory) {
    activeView = 'wing-detail';
    currentWingId = wingId;
    homeSection.style.display = 'none';
    terminalSection.style.display = 'none';
    chatSection.style.display = 'none';
    wingDetailSection.style.display = '';
    detachPTY();
    destroyChat();
    headerTitle.textContent = '';
    ptyStatus.textContent = '';
    renderWingDetailPage(wingId);
    if (pushHistory !== false) {
        history.pushState({ view: 'wing-detail', wingId: wingId }, '', '#w/' + wingId);
    }
}

function semverCompare(a, b) {
    var pa = (a || '').match(/^v?(\d+)\.(\d+)\.(\d+)/);
    var pb = (b || '').match(/^v?(\d+)\.(\d+)\.(\d+)/);
    if (!pa || !pb) return 0;
    for (var i = 1; i <= 3; i++) {
        var diff = parseInt(pa[i]) - parseInt(pb[i]);
        if (diff !== 0) return diff;
    }
    return 0;
}

function renderWingDetailPage(wingId) {
    // Skip full re-render if search input is focused (preserves cursor)
    var searchEl = document.getElementById('wd-search');
    if (searchEl && document.activeElement === searchEl) {
        // Just update active sessions in place
        updateWingDetailSessions(wingId);
        return;
    }

    var w = wingsData.find(function(w) { return w.wing_id === wingId; });

    // Check if wing is in the process of updating (60s grace period)
    var isUpdating = w && w.updating_at && (Date.now() - w.updating_at < 60000);
    if (!isUpdating && w && w.updating_at) {
        // Grace period expired — clear stale flag
        delete w.updating_at;
    }

    if (!w || isUpdating) {
        var msg = isUpdating
            ? '<span class="text-dim">updating... wing will reconnect shortly</span>'
            : '<span class="text-dim">wing not found</span>';
        wingDetailContent.innerHTML = '<div class="wd-page"><div class="wd-header"><a class="wd-back" id="wd-back">back</a>' + msg + '</div></div>';
        document.getElementById('wd-back').addEventListener('click', function() { showHome(); });
        if (isUpdating) {
            setTimeout(function() {
                if (activeView === 'wing-detail' && currentWingId === wingId) {
                    renderWingDetailPage(wingId);
                }
            }, 3000);
        }
        return;
    }

    var name = wingDisplayName(w);
    var isOnline = w.online !== false;
    var ver = w.version || '';
    var updateAvailable = !w.updating_at && latestVersion && ver && semverCompare(latestVersion, ver) > 0;

    // Public key
    var pubKeyHtml = '';
    if (w.public_key) {
        var pubKeyShort = w.public_key.substring(0, 16) + '...';
        pubKeyHtml = '<span class="detail-val text-dim copyable" data-copy="' + escapeHtml(w.public_key) + '">' + escapeHtml(pubKeyShort) + '</span>';
    } else {
        pubKeyHtml = '<span class="detail-val text-dim">none</span>';
    }

    // Projects
    var projects = (w.projects || []).slice();
    projects.sort(function(a, b) { return (b.mod_time || 0) - (a.mod_time || 0); });
    var maxProjects = 8;
    var visibleProjects = projects.slice(0, maxProjects);
    var projList = visibleProjects.map(function(p) {
        return '<div class="detail-subitem">' + escapeHtml(p.name) + ' <span class="text-dim">' + escapeHtml(shortenPath(p.path)) + '</span></div>';
    }).join('');
    if (projects.length > maxProjects) {
        projList += '<div class="detail-projects-more">+' + (projects.length - maxProjects) + ' more</div>';
    }
    if (!projList) projList = '<span class="text-dim">none</span>';

    // Scope
    var scopeHtml = w.org_id ? escapeHtml(w.org_id) : 'personal';

    // Active sessions
    var activeSessions = sessionsData.filter(function(s) { return s.wing_id === w.id; });
    var activeHtml = '';
    if (activeSessions.length > 0) {
        activeHtml = '<div class="wd-section"><h3 class="section-label">active sessions</h3><div class="wd-sessions" id="wd-active-sessions">';
        activeHtml += renderActiveSessionRows(activeSessions);
        activeHtml += '</div></div>';
    }

    var html =
        '<div class="wd-page">' +
        '<div class="wd-header">' +
            '<a class="wd-back" id="wd-back">back</a>' +
        '</div>' +
        (updateAvailable ? '<div class="wd-update-banner" id="wd-update">' +
            escapeHtml(latestVersion) + ' available (you have ' + escapeHtml(ver) + ') <span class="wd-update-action">update now</span>' +
        '</div>' : '') +
        '<div class="wd-hero">' +
            '<div class="wd-hero-top">' +
                '<span class="session-dot ' + (isOnline ? 'live' : 'offline') + '"></span>' +
                '<span class="wd-name" id="wd-name" title="click to rename">' + escapeHtml(name) + '</span>' +
                (w.wing_label ? '<a class="wd-clear-label" id="wd-delete-label" title="clear name">x</a>' : '') +
                (!isOnline ? '<a class="wd-dismiss-link" id="wd-dismiss">remove</a>' : '') +
            '</div>' +
        '</div>' +
        (isOnline ? '<div class="wd-palette">' +
            '<input id="wd-search" type="text" class="wd-search" placeholder="start a session..." autocomplete="off" spellcheck="false">' +
            '<div id="wd-search-results" class="wd-search-results"></div>' +
            '<div id="wd-search-status" class="wd-search-status"></div>' +
        '</div>' : '') +
        activeHtml +
        '<div class="wd-section"><h3 class="section-label">session history</h3><div id="wd-past-sessions"><span class="text-dim">' + (isOnline ? 'loading...' : 'wing offline') + '</span></div></div>' +
        '<div class="wd-info">' +
            '<div class="detail-row"><span class="detail-key">scope</span><span class="detail-val">' + scopeHtml + '</span></div>' +
            '<div class="detail-row"><span class="detail-key">platform</span><span class="detail-val">' + escapeHtml(w.platform || 'unknown') + '</span></div>' +
            '<div class="detail-row"><span class="detail-key">version</span><span class="detail-val">' + escapeHtml(ver || 'unknown') + '</span></div>' +
            '<div class="detail-row"><span class="detail-key">agents</span><span class="detail-val">' + escapeHtml((w.agents || []).join(', ') || 'none') + '</span></div>' +
            '<div class="detail-row"><span class="detail-key">public key</span>' + pubKeyHtml + '</div>' +
            '<div class="detail-row"><span class="detail-key">projects</span><div class="detail-val">' + projList + '</div></div>' +
        '</div>' +
        '</div>';

    wingDetailContent.innerHTML = html;
    setupCopyable(wingDetailContent);

    // Back
    document.getElementById('wd-back').addEventListener('click', function() { showHome(); });

    // Inline rename
    var nameEl = document.getElementById('wd-name');
    nameEl.addEventListener('click', function() {
        var current = w.wing_label || w.hostname || w.wing_id.substring(0, 8);
        var input = document.createElement('input');
        input.type = 'text';
        input.className = 'wd-name-input';
        input.value = current;
        nameEl.replaceWith(input);
        input.focus();
        input.select();
        function save() {
            var val = input.value.trim();
            if (val && val !== current) {
                fetch('/api/app/wings/' + encodeURIComponent(wingId) + '/label', {
                    method: 'PUT',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ label: val })
                }).then(function() {
                    w.wing_label = val;
                    renderWingDetailPage(wingId);
                });
            } else {
                renderWingDetailPage(wingId);
            }
        }
        input.addEventListener('blur', save);
        input.addEventListener('keydown', function(e) {
            if (e.key === 'Enter') { e.preventDefault(); input.blur(); }
            if (e.key === 'Escape') { e.preventDefault(); renderWingDetailPage(wingId); }
        });
    });

    // Delete label
    var delLabelBtn = document.getElementById('wd-delete-label');
    if (delLabelBtn) {
        delLabelBtn.addEventListener('click', function(e) {
            e.stopPropagation();
            fetch('/api/app/wings/' + encodeURIComponent(wingId) + '/label', { method: 'DELETE' })
                .then(function() {
                    delete w.wing_label;
                    renderWingDetailPage(wingId);
                });
        });
    }

    // Update banner
    var updateBtn = document.getElementById('wd-update');
    if (updateBtn) {
        updateBtn.addEventListener('click', function() {
            updateBtn.innerHTML = 'updating...';
            fetch('/api/app/wings/' + encodeURIComponent(w.wing_id) + '/update', { method: 'POST' })
                .then(function() {
                    w.updating_at = Date.now();
                    renderWingDetailPage(wingId);
                })
                .catch(function() { updateBtn.innerHTML = 'update failed'; });
        });
    }

    // Dismiss link
    var dismissBtn = document.getElementById('wd-dismiss');
    if (dismissBtn) {
        dismissBtn.addEventListener('click', function() {
            wingsData = wingsData.filter(function(ww) { return ww.wing_id !== wingId; });
            setCachedWings(wingsData.map(function(ww) {
                return { wing_id: ww.wing_id, hostname: ww.hostname, platform: ww.platform, version: ww.version, agents: ww.agents, labels: ww.labels, projects: ww.projects, online: ww.online, wing_label: ww.wing_label };
            }));
            showHome();
        });
    }

    // Active session rows
    wireActiveSessionRows();

    // Fetch past sessions
    if (isOnline) {
        loadWingPastSessions(wingId, 0);
    }

    // Inline palette
    if (isOnline) {
        setupWingPalette(w);
    }
}

function renderActiveSessionRows(sessions) {
    return sessions.map(function(s) {
        var sName = projectName(s.cwd);
        var sDot = s.status === 'active' ? 'live' : 'detached';
        var kind = s.kind || 'terminal';
        var auditBadge = s.audit ? '<span class="wd-audit-badge">audit</span>' : '';
        return '<div class="wd-session-row" data-sid="' + s.id + '" data-kind="' + kind + '" data-agent="' + escapeHtml(s.agent || 'claude') + '">' +
            '<span class="session-dot ' + sDot + '"></span>' +
            '<span class="wd-session-name">' + escapeHtml(sName) + ' \u00b7 ' + agentWithIcon(s.agent || '?') + '</span>' +
            auditBadge +
            '<button class="wd-kill-btn" data-sid="' + s.id + '" title="kill session">x</button>' +
        '</div>';
    }).join('');
}

function wireActiveSessionRows() {
    wingDetailContent.querySelectorAll('.wd-session-row').forEach(function(row) {
        row.addEventListener('click', function(e) {
            if (e.target.classList.contains('wd-kill-btn')) return;
            var sid = row.dataset.sid;
            var kind = row.dataset.kind;
            var agent = row.dataset.agent;
            if (kind === 'chat') {
                window._openChat(sid, agent);
            } else {
                switchToSession(sid);
            }
        });
    });
    wingDetailContent.querySelectorAll('.wd-kill-btn').forEach(function(btn) {
        btn.addEventListener('click', function(e) {
            e.stopPropagation();
            if (btn.dataset.confirming) {
                var sid = btn.dataset.sid;
                var wingId = currentWingId;
                btn.disabled = true;
                btn.textContent = '...';
                fetch('/api/app/wings/' + encodeURIComponent(wingId) + '/sessions/' + encodeURIComponent(sid), { method: 'DELETE' })
                    .then(function() {
                        sessionsData = sessionsData.filter(function(s) { return s.id !== sid; });
                        updateWingDetailSessions(wingId);
                        loadWingPastSessions(wingId, 0);
                    });
            } else {
                btn.dataset.confirming = '1';
                btn.textContent = 'sure?';
                btn.classList.add('confirming');
                setTimeout(function() {
                    delete btn.dataset.confirming;
                    btn.textContent = 'x';
                    btn.classList.remove('confirming');
                }, 3000);
            }
        });
    });
}

function updateWingDetailSessions(wingId) {
    var w = wingsData.find(function(w) { return w.wing_id === wingId; });
    if (!w) return;
    var container = document.getElementById('wd-active-sessions');
    var activeSessions = sessionsData.filter(function(s) { return s.wing_id === w.id; });
    if (container) {
        container.innerHTML = renderActiveSessionRows(activeSessions);
        wireActiveSessionRows();
    }
}

function setupWingPalette(wing) {
    var searchEl = document.getElementById('wd-search');
    var resultsEl = document.getElementById('wd-search-results');
    var statusEl = document.getElementById('wd-search-status');
    if (!searchEl || !resultsEl || !statusEl) return;

    var wpAgentIndex = 0;
    var wpSelectedIndex = 0;
    var wpDirCache = [];
    var wpDirCacheDir = '';
    var wpDirTimer = null;
    var wpDirAbort = null;
    var wpHomeDirCache = [];

    var agents = wing.agents || ['claude'];
    var lastAgent = getLastTermAgent();
    var idx = agents.indexOf(lastAgent);
    wpAgentIndex = idx >= 0 ? idx : 0;

    function currentAgent() { return agents[wpAgentIndex % agents.length]; }

    function renderStatus() {
        statusEl.innerHTML = '<span class="accent">' + agentWithIcon(currentAgent()) + '</span>' +
            (agents.length > 1 ? ' <span class="text-dim">(shift+tab to switch)</span>' : '');
    }
    renderStatus();

    // Pre-cache home dir
    if (wing.wing_id) {
        fetch('/api/app/wings/' + encodeURIComponent(wing.wing_id) + '/ls?path=' + encodeURIComponent('~/')).then(function(r) {
            return r.json();
        }).then(function(entries) {
            if (entries && Array.isArray(entries)) {
                wpHomeDirCache = entries.map(function(e) {
                    return { name: e.name, path: e.path, isDir: e.is_dir };
                });
            }
        }).catch(function() {});
    }

    function renderResults(filter) {
        var wingId = wing.id || '';
        var wingProjects = wingId
            ? allProjects.filter(function(p) { return p.wingId === wingId; })
            : allProjects;

        var items = [];
        var lower = filter ? filter.toLowerCase() : '';
        var filtered = lower
            ? wingProjects.filter(function(p) {
                return p.name.toLowerCase().indexOf(lower) !== -1 ||
                       p.path.toLowerCase().indexOf(lower) !== -1;
            })
            : wingProjects.slice();

        filtered.sort(function(a, b) {
            var ca = nestedRepoCount(a.path, wingProjects);
            var cb = nestedRepoCount(b.path, wingProjects);
            if (ca !== cb) return cb - ca;
            return a.name.localeCompare(b.name);
        });

        filtered.forEach(function(p) {
            items.push({ name: p.name, path: p.path, isDir: true });
        });

        // Include cached home dir entries
        var seenPaths = {};
        items.forEach(function(it) { seenPaths[it.path] = true; });
        var homeExtras = wpHomeDirCache.filter(function(e) {
            if (seenPaths[e.path]) return false;
            return !lower || e.name.toLowerCase().indexOf(lower) !== -1 ||
                   e.path.toLowerCase().indexOf(lower) !== -1;
        });
        homeExtras.sort(function(a, b) {
            var ca = nestedRepoCount(a.path, wingProjects);
            var cb = nestedRepoCount(b.path, wingProjects);
            if (ca !== cb) return cb - ca;
            return a.name.localeCompare(b.name);
        });
        homeExtras.forEach(function(e) {
            items.push({ name: e.name, path: e.path, isDir: e.isDir });
        });

        renderItems(items);
    }

    function renderItems(items) {
        wpSelectedIndex = 0;
        if (items.length === 0) { resultsEl.innerHTML = ''; return; }

        resultsEl.innerHTML = items.map(function(item, i) {
            var icon = item.isDir ? '/' : '';
            return '<div class="palette-item' + (i === 0 ? ' selected' : '') + '" data-path="' + escapeHtml(item.path) + '" data-index="' + i + '">' +
                '<span class="palette-name">' + escapeHtml(item.name) + icon + '</span>' +
                (item.path ? '<span class="palette-path">' + escapeHtml(shortenPath(item.path)) + '</span>' : '') +
            '</div>';
        }).join('');

        resultsEl.querySelectorAll('.palette-item').forEach(function(item) {
            item.addEventListener('click', function() { launch(item.dataset.path); });
            item.addEventListener('mouseenter', function() {
                resultsEl.querySelectorAll('.palette-item').forEach(function(el) { el.classList.remove('selected'); });
                item.classList.add('selected');
                wpSelectedIndex = parseInt(item.dataset.index);
            });
        });
    }

    function wpFilterCached(prefix) {
        var items = wpDirCache;
        if (prefix) {
            items = items.filter(function(e) { return e.name.toLowerCase().indexOf(prefix) === 0; });
        }
        return items;
    }

    function wpDebouncedDirList(value) {
        if (wpDirTimer) clearTimeout(wpDirTimer);
        if (wpDirAbort) { wpDirAbort.abort(); wpDirAbort = null; }

        if (!value || (value.charAt(0) !== '/' && value.charAt(0) !== '~')) {
            wpDirCache = [];
            wpDirCacheDir = '';
            renderResults(value);
            return;
        }

        var parsed = dirParent(value);
        if (wpDirCacheDir && wpDirCacheDir === parsed.dir) {
            renderItems(wpFilterCached(parsed.prefix));
            return;
        }
        if (wpDirCache.length > 0) {
            renderItems(wpFilterCached(parsed.prefix));
        }
        wpDirTimer = setTimeout(function() { wpFetchDirList(parsed.dir); }, 150);
    }

    function wpFetchDirList(dirPath) {
        if (wpDirAbort) wpDirAbort.abort();
        wpDirAbort = new AbortController();

        fetch('/api/app/wings/' + encodeURIComponent(wing.wing_id) + '/ls?path=' + encodeURIComponent(dirPath), {
            signal: wpDirAbort.signal
        }).then(function(r) { return r.json(); }).then(function(entries) {
            var currentParsed = dirParent(searchEl.value);
            if (currentParsed.dir !== dirPath) return;

            if (!entries || !Array.isArray(entries)) {
                wpDirCache = [];
                wpDirCacheDir = '';
                renderItems([]);
                return;
            }
            var items = entries.map(function(e) {
                return { name: e.name, path: e.path, isDir: e.is_dir };
            });
            items.sort(function(a, b) {
                if (a.isDir !== b.isDir) return a.isDir ? -1 : 1;
                var ca = nestedRepoCount(a.path, allProjects);
                var cb = nestedRepoCount(b.path, allProjects);
                if (ca !== cb) return cb - ca;
                return a.name.localeCompare(b.name);
            });
            var absDirPath = dirPath;
            if (items.length > 0 && items[0].path) {
                absDirPath = items[0].path.replace(/\/[^\/]+$/, '');
            }
            var dirLabel = shortenPath(absDirPath).replace(/\/$/, '') || absDirPath;
            items.unshift({ name: dirLabel, path: absDirPath, isDir: true });
            wpDirCache = items;
            wpDirCacheDir = dirPath;
            renderItems(wpFilterCached(currentParsed.prefix));
        }).catch(function(err) {
            if (err && err.name === 'AbortError') return;
        });
    }

    function navigate(dir) {
        var items = resultsEl.querySelectorAll('.palette-item');
        if (items.length === 0) return;
        items[wpSelectedIndex].classList.remove('selected');
        wpSelectedIndex = (wpSelectedIndex + dir + items.length) % items.length;
        items[wpSelectedIndex].classList.add('selected');
        items[wpSelectedIndex].scrollIntoView({ block: 'nearest' });
    }

    function tabComplete() {
        var selected = resultsEl.querySelector('.palette-item.selected');
        if (!selected) return;
        var path = selected.dataset.path;
        if (!path) return;
        var short = shortenPath(path);
        var nameEl = selected.querySelector('.palette-name');
        var isDir = nameEl && nameEl.textContent.slice(-1) === '/';
        if (isDir) {
            searchEl.value = short + '/';
            wpDebouncedDirList(searchEl.value);
        } else {
            searchEl.value = short;
        }
    }

    function launch(cwd) {
        var agent = currentAgent();
        var validCwd = (cwd && cwd.charAt(0) === '/') ? cwd : '';
        setLastTermAgent(agent);
        showTerminal();
        connectPTY(agent, validCwd, wing.id);
    }

    // Show initial results (projects list)
    renderResults('');

    searchEl.addEventListener('input', function() {
        wpDebouncedDirList(searchEl.value);
    });

    searchEl.addEventListener('keydown', function(e) {
        if (e.key === 'Enter') {
            e.preventDefault();
            var selected = resultsEl.querySelector('.palette-item.selected');
            if (selected) launch(selected.dataset.path);
        }
        if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
            e.preventDefault();
            navigate(e.key === 'ArrowDown' ? 1 : -1);
        }
        if (e.key === 'Tab') {
            e.preventDefault();
            if (e.shiftKey) {
                // Cycle agent
                if (agents.length > 1) {
                    wpAgentIndex = (wpAgentIndex + 1) % agents.length;
                    renderStatus();
                }
            } else {
                tabComplete();
            }
        }
        if (e.key === '`') {
            e.preventDefault();
            // ` cycles agent (same as shift+tab) since wing is fixed
            if (agents.length > 1) {
                wpAgentIndex = (wpAgentIndex + 1) % agents.length;
                renderStatus();
            }
        }
    });
}

function loadWingPastSessions(wingId, offset) {
    var limit = 20;
    var container = document.getElementById('wd-past-sessions');
    if (!container) return;

    // Show cached data immediately
    if (offset === 0) {
        var cached = getCachedWingSessions(wingId);
        if (cached && cached.length > 0) {
            renderPastSessions(container, wingId, cached, true);
        }
    }

    fetch('/api/app/wings/' + encodeURIComponent(wingId) + '/sessions?offset=' + offset + '&limit=' + limit)
        .then(function(r) {
            if (!r.ok) throw new Error('fetch failed');
            return r.json();
        })
        .then(function(data) {
            var sessions = data.sessions || [];
            if (offset === 0) {
                wingPastSessions[wingId] = { sessions: sessions, offset: offset, hasMore: sessions.length >= limit };
                setCachedWingSessions(wingId, sessions);
            } else {
                var existing = wingPastSessions[wingId] || { sessions: [], offset: 0, hasMore: true };
                existing.sessions = existing.sessions.concat(sessions);
                existing.offset = offset;
                existing.hasMore = sessions.length >= limit;
                wingPastSessions[wingId] = existing;
            }
            if (container && currentWingId === wingId) {
                renderPastSessions(container, wingId, wingPastSessions[wingId].sessions, wingPastSessions[wingId].hasMore);
            }
        })
        .catch(function() {
            if (container && currentWingId === wingId && offset === 0) {
                var cached = getCachedWingSessions(wingId);
                if (!cached || cached.length === 0) {
                    container.innerHTML = '<span class="text-dim">could not reach wing - it may be reconnecting</span>';
                }
            }
        });
}

function renderPastSessions(container, wingId, sessions, hasMore) {
    if (!sessions || sessions.length === 0) {
        container.innerHTML = '<span class="text-dim">no audited sessions</span>';
        return;
    }
    var html = sessions.map(function(s) {
        var name = s.cwd ? projectName(s.cwd) : s.session_id.substring(0, 8);
        var startStr = s.started_at ? formatRelativeTime(s.started_at * 1000) : '';
        var auditBadge = s.audit ? '<span class="wd-audit-badge">audit</span>' : '';
        var auditBtns = s.audit
            ? '<button class="btn-sm wd-replay-btn" data-sid="' + escapeHtml(s.session_id) + '">replay</button>' +
              '<button class="btn-sm wd-keylog-btn" data-sid="' + escapeHtml(s.session_id) + '">keylog</button>'
            : '';
        return '<div class="wd-past-row">' +
            '<span class="wd-past-name">' + escapeHtml(name) + ' \u00b7 ' + escapeHtml(s.agent || '?') + '</span>' +
            '<span class="wd-past-time text-dim">' + startStr + '</span>' +
            auditBadge +
            auditBtns +
        '</div>';
    }).join('');

    if (hasMore) {
        html += '<button class="btn-sm wd-load-more" id="wd-load-more">load more</button>';
    }
    container.innerHTML = html;

    // Wire load more
    var loadMoreBtn = document.getElementById('wd-load-more');
    if (loadMoreBtn) {
        loadMoreBtn.addEventListener('click', function() {
            var state = wingPastSessions[wingId] || { sessions: [], offset: 0 };
            loadWingPastSessions(wingId, state.sessions.length);
        });
    }

    // Wire replay buttons
    container.querySelectorAll('.wd-replay-btn').forEach(function(btn) {
        btn.addEventListener('click', function() {
            openAuditReplay(wingId, btn.dataset.sid);
        });
    });
    container.querySelectorAll('.wd-keylog-btn').forEach(function(btn) {
        btn.addEventListener('click', function() {
            openAuditKeylog(wingId, btn.dataset.sid);
        });
    });
}

function getCachedWingSessions(wingId) {
    try { var raw = localStorage.getItem(WING_SESSIONS_PREFIX + wingId); return raw ? JSON.parse(raw) : null; }
    catch (e) { return null; }
}
function setCachedWingSessions(wingId, sessions) {
    try { localStorage.setItem(WING_SESSIONS_PREFIX + wingId, JSON.stringify(sessions)); } catch (e) {}
}

function formatRelativeTime(ms) {
    var diff = Date.now() - ms;
    if (diff < 60000) return 'just now';
    if (diff < 3600000) return Math.floor(diff / 60000) + 'm ago';
    if (diff < 86400000) return Math.floor(diff / 3600000) + 'h ago';
    return Math.floor(diff / 86400000) + 'd ago';
}

// === Audit Replay ===

function openAuditReplay(wingId, sessionId) {
    var overlay = document.getElementById('audit-overlay');
    var termEl = document.getElementById('audit-terminal');
    var playBtn = document.getElementById('audit-play');
    var timeEl = document.getElementById('audit-time');
    var speedInput = document.getElementById('audit-speed');
    var speedLabel = document.getElementById('audit-speed-label');
    var downloadBtn = document.getElementById('audit-download');
    var closeBtn = document.getElementById('audit-close');

    overlay.style.display = '';
    termEl.innerHTML = '';

    // Show speed controls (may have been hidden by keylog)
    speedInput.style.display = '';
    speedLabel.style.display = '';
    timeEl.style.display = '';
    downloadBtn.style.display = 'none';

    var auditCols = 120, auditRows = 40;
    var auditTerm = null;
    var auditFit = null;
    var ndjsonHeader = '';

    var frames = [];
    var playing = false;
    var playTimer = null;
    var frameIndex = 0;
    var speed = 1;

    function initTerm() {
        if (auditTerm) return;
        auditTerm = new Terminal({ fontSize: 14, cols: auditCols, rows: auditRows, theme: { background: '#0d0d1a' }, convertEol: false });
        auditFit = new FitAddon();
        auditTerm.loadAddon(auditFit);
        auditTerm.open(termEl);
    }

    // Fetch audit data via SSE
    var es = new EventSource('/api/app/wings/' + encodeURIComponent(wingId) + '/sessions/' + encodeURIComponent(sessionId) + '/audit?kind=pty');
    es.addEventListener('chunk', function(e) {
        if (!e.data) return;
        try {
            var parsed = JSON.parse(e.data);
            if (Array.isArray(parsed)) {
                frames.push(parsed);
            } else if (parsed.width) {
                auditCols = parsed.width;
                auditRows = parsed.height;
                ndjsonHeader = e.data;
            }
        } catch (ex) {}
    });
    es.addEventListener('done', function() {
        es.close();
        if (frames.length > 0) {
            playBtn.textContent = 'play';
            playBtn.disabled = false;
            downloadBtn.style.display = '';
        } else {
            playBtn.textContent = 'no data';
            playBtn.disabled = true;
        }
    });
    es.addEventListener('error', function(e) {
        if (e.data) {
            playBtn.textContent = 'error';
        }
        es.close();
    });
    es.onerror = function() { es.close(); };

    playBtn.textContent = 'loading...';
    playBtn.disabled = true;

    function decodeBase64UTF8(b64) {
        var bin = atob(b64);
        var bytes = new Uint8Array(bin.length);
        for (var i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
        return new TextDecoder().decode(bytes);
    }

    function playFrame() {
        if (frameIndex >= frames.length) {
            playing = false;
            playBtn.textContent = 'replay';
            return;
        }
        if (!auditTerm) initTerm();
        var f = frames[frameIndex];
        if (f[1] === 'r') {
            // Resize event: f[2] = "COLSxROWS"
            var parts = f[2].split('x');
            var newCols = parseInt(parts[0]);
            var newRows = parseInt(parts[1]);
            if (newCols > 0 && newRows > 0) {
                auditTerm.resize(newCols, newRows);
            }
        } else {
            // Output event: f[2] = base64 data
            var data = f[2];
            try { data = decodeBase64UTF8(data); } catch (e) { /* already plain text */ }
            auditTerm.write(data);
        }
        frameIndex++;
        var elapsed = f[0];
        timeEl.textContent = formatAuditTime(elapsed);

        if (frameIndex < frames.length) {
            var delay = (frames[frameIndex][0] - f[0]) * 1000 / speed;
            delay = Math.min(delay, 2000); // cap at 2s
            playTimer = setTimeout(playFrame, delay);
        } else {
            playing = false;
            playBtn.textContent = 'replay';
        }
    }

    playBtn.onclick = function() {
        if (playing) {
            playing = false;
            clearTimeout(playTimer);
            playBtn.textContent = 'play';
        } else {
            if (frameIndex >= frames.length) {
                frameIndex = 0;
                if (auditTerm) auditTerm.reset();
            }
            playing = true;
            playBtn.textContent = 'pause';
            playFrame();
        }
    };

    speedInput.oninput = function() {
        speed = parseInt(speedInput.value) || 1;
        speedLabel.textContent = speed + 'x';
    };

    downloadBtn.onclick = function() {
        var text = (ndjsonHeader || '{"version":2,"width":' + auditCols + ',"height":' + auditRows + '}') + '\n';
        for (var i = 0; i < frames.length; i++) {
            text += JSON.stringify(frames[i]) + '\n';
        }
        var blob = new Blob([text], { type: 'text/plain' });
        var a = document.createElement('a');
        a.href = URL.createObjectURL(blob);
        a.download = 'audit-' + sessionId + '.cast';
        a.click();
        URL.revokeObjectURL(a.href);
    };

    closeBtn.onclick = function() {
        playing = false;
        clearTimeout(playTimer);
        es.close();
        if (auditTerm) auditTerm.dispose();
        downloadBtn.style.display = 'none';
        overlay.style.display = 'none';
    };

    // Close on backdrop click
    document.getElementById('audit-backdrop').onclick = closeBtn.onclick;
}

function openAuditKeylog(wingId, sessionId) {
    var overlay = document.getElementById('audit-overlay');
    var termEl = document.getElementById('audit-terminal');
    var playBtn = document.getElementById('audit-play');
    var timeEl = document.getElementById('audit-time');
    var speedInput = document.getElementById('audit-speed');
    var speedLabel = document.getElementById('audit-speed-label');
    var downloadBtn = document.getElementById('audit-download');
    var closeBtn = document.getElementById('audit-close');

    overlay.style.display = '';
    termEl.innerHTML = '<pre class="audit-keylog" style="color:#ccc;font-size:13px;padding:12px;overflow:auto;height:100%;margin:0;white-space:pre-wrap;"></pre>';
    var pre = termEl.querySelector('pre');
    playBtn.style.display = 'none';
    speedInput.style.display = 'none';
    speedLabel.style.display = 'none';
    timeEl.style.display = 'none';
    downloadBtn.style.display = 'none';

    var es = new EventSource('/api/app/wings/' + encodeURIComponent(wingId) + '/sessions/' + encodeURIComponent(sessionId) + '/audit?kind=keylog');
    es.addEventListener('chunk', function(e) {
        if (e.data) pre.textContent += e.data + '\n';
    });
    es.addEventListener('done', function() {
        es.close();
        if (!pre.textContent) {
            pre.textContent = 'no keylog data';
        } else {
            downloadBtn.style.display = '';
        }
    });
    es.addEventListener('error', function() { es.close(); });
    es.onerror = function() { es.close(); };

    downloadBtn.onclick = function() {
        var blob = new Blob([pre.textContent], { type: 'text/plain' });
        var a = document.createElement('a');
        a.href = URL.createObjectURL(blob);
        a.download = 'keylog-' + sessionId + '.log';
        a.click();
        URL.revokeObjectURL(a.href);
    };

    closeBtn.onclick = function() {
        es.close();
        downloadBtn.style.display = 'none';
        overlay.style.display = 'none';
        playBtn.style.display = '';
        speedInput.style.display = '';
        speedLabel.style.display = '';
        timeEl.style.display = '';
    };
    document.getElementById('audit-backdrop').onclick = closeBtn.onclick;
}

function formatAuditTime(secs) {
    var m = Math.floor(secs / 60);
    var s = Math.floor(secs % 60);
    return m + ':' + (s < 10 ? '0' : '') + s;
}

function showEggDetail(sessionId) {
    var s = sessionsData.find(function(s) { return s.id === sessionId; });
    if (!s) return;
    var name = projectName(s.cwd);
    var kind = s.kind || 'terminal';
    var wingName = '';
    if (s.wing_id) {
        var wing = wingsData.find(function(w) { return w.id === s.wing_id; });
        if (wing) wingName = wingDisplayName(wing);
    }
    var cwdDisplay = s.cwd ? shortenPath(s.cwd) : '~';

    // Build config summary if available
    var configSummary = '';
    var configYaml = s.egg_config || '';
    if (configYaml) {
        var isoMatch = configYaml.match(/isolation:\s*(\S+)/);
        var mountCount = (configYaml.match(/^\s*-\s+~/gm) || []).length;
        var denyCount = (configYaml.match(/deny:/g) || []).length > 0 ? (configYaml.match(/^\s+-\s+~/gm) || []).length : 0;
        var isoLevel = isoMatch ? isoMatch[1] : '?';
        var parts = [isoLevel];
        if (mountCount > 0) parts.push(mountCount + ' mount' + (mountCount > 1 ? 's' : ''));
        if (denyCount > 0) parts.push(denyCount + ' denied');
        configSummary =
            '<div class="detail-row"><span class="detail-key">config</span>' +
            '<span class="detail-val copyable" data-copy="' + escapeHtml(configYaml) + '" title="click to copy full YAML">' +
            escapeHtml(parts.join(' | ')) + '</span></div>';
    }

    detailDialog.innerHTML =
        '<h3>' + escapeHtml(name) + ' &middot; ' + escapeHtml(s.agent || '?') + '</h3>' +
        '<div class="detail-row"><span class="detail-key">session</span><span class="detail-val text-dim">' + escapeHtml(s.id) + '</span></div>' +
        '<div class="detail-row"><span class="detail-key">wing</span><span class="detail-val">' + escapeHtml(wingName || 'unknown') + '</span></div>' +
        '<div class="detail-row"><span class="detail-key">type</span><span class="detail-val">' + escapeHtml(kind) + '</span></div>' +
        '<div class="detail-row"><span class="detail-key">agent</span><span class="detail-val">' + escapeHtml(s.agent || '?') + '</span></div>' +
        '<div class="detail-row"><span class="detail-key">cwd</span><span class="detail-val text-dim">' + escapeHtml(cwdDisplay) + '</span></div>' +
        '<div class="detail-row"><span class="detail-key">status</span><span class="detail-val">' + escapeHtml(s.status || 'unknown') + '</span></div>' +
        configSummary +
        '<div class="detail-actions">' +
            '<button class="btn-sm btn-accent" id="detail-egg-connect">connect</button>' +
            '<button class="btn-sm btn-danger" id="detail-egg-delete">delete session</button>' +
        '</div>';

    setupCopyable(detailDialog);
    detailOverlay.classList.add('open');

    document.getElementById('detail-egg-connect').addEventListener('click', function() {
        hideDetailModal();
        if (kind === 'chat') {
            window._openChat(sessionId, s.agent || 'claude');
        } else {
            switchToSession(sessionId);
        }
    });

    var delBtn = document.getElementById('detail-egg-delete');
    delBtn.addEventListener('click', function() {
        if (delBtn.dataset.armed) {
            hideDetailModal();
            window._deleteSession(sessionId);
        } else {
            delBtn.dataset.armed = '1';
            delBtn.textContent = 'are you sure?';
            delBtn.classList.add('btn-armed');
        }
    });
}

function showSessionInfo() {
    var s = sessionsData.find(function(s) { return s.id === ptySessionId; });
    var w = ptyWingId ? wingsData.find(function(w) { return w.id === ptyWingId; }) : null;
    if (!s && !w) return;

    var wingName = w ? wingDisplayName(w) : 'unknown';
    var agent = s ? (s.agent || '?') : '?';
    var cwdDisplay = s && s.cwd ? shortenPath(s.cwd) : '~';

    // Wing info
    var wingVersion = w ? (w.version || 'unknown') : 'unknown';
    var wingPlatform = w ? (w.platform || 'unknown') : 'unknown';
    var wingAgents = w ? (w.agents || []).join(', ') || 'none' : 'unknown';
    var isOnline = w ? w.online !== false : false;
    var dotClass = isOnline ? 'live' : 'offline';

    // Egg config summary
    var configSummary = '';
    if (s && s.egg_config) {
        var isoMatch = s.egg_config.match(/isolation:\s*(\S+)/);
        var isoLevel = isoMatch ? isoMatch[1] : '?';
        configSummary = '<div class="detail-row"><span class="detail-key">isolation</span>' +
            '<span class="detail-val copyable" data-copy="' + escapeHtml(s.egg_config) + '" title="click to copy full YAML">' +
            escapeHtml(isoLevel) + '</span></div>';
    }

    // E2E status
    var e2eStatus = e2eKey ? 'active' : 'none';

    detailDialog.innerHTML =
        '<h3><span class="detail-connection-dot ' + dotClass + '"></span>' + escapeHtml(wingName) + ' &middot; ' + escapeHtml(agent) + '</h3>' +
        '<div class="detail-row"><span class="detail-key">session</span><span class="detail-val text-dim">' + escapeHtml(ptySessionId || '') + '</span></div>' +
        '<div class="detail-row"><span class="detail-key">cwd</span><span class="detail-val text-dim">' + escapeHtml(cwdDisplay) + '</span></div>' +
        '<div class="detail-row"><span class="detail-key">e2e</span><span class="detail-val">' + e2eStatus + '</span></div>' +
        configSummary +
        '<div class="detail-row" style="margin-top:12px"><span class="detail-key" style="font-weight:600">wing</span></div>' +
        '<div class="detail-row"><span class="detail-key">wing</span><span class="detail-val">' + escapeHtml(wingName) + '</span></div>' +
        '<div class="detail-row"><span class="detail-key">version</span><span class="detail-val">' + escapeHtml(wingVersion) + '</span></div>' +
        '<div class="detail-row"><span class="detail-key">platform</span><span class="detail-val">' + escapeHtml(wingPlatform) + '</span></div>' +
        '<div class="detail-row"><span class="detail-key">agents</span><span class="detail-val">' + escapeHtml(wingAgents) + '</span></div>';

    setupCopyable(detailDialog);
    detailOverlay.classList.add('open');
}

// Wire up detail modal close
detailBackdrop.addEventListener('click', hideDetailModal);
document.addEventListener('keydown', function(e) {
    if (e.key === 'Escape' && detailOverlay.classList.contains('open')) {
        e.stopImmediatePropagation();
        hideDetailModal();
    }
});

function renderDashboard() {
    // Wing boxes
    if (wingsData.length > 0) {
        var wingHtml = '<h3 class="section-label">wings</h3><div class="wing-grid">';
        wingHtml += wingsData.map(function(w) {
            var name = wingDisplayName(w);
            var dotClass = w.online !== false ? 'dot-live' : 'dot-offline';
            var projectCount = (w.projects || []).length;
            var plat = w.platform === 'darwin' ? 'mac' : (w.platform || '');
            return '<div class="wing-box" draggable="true" data-wing-id="' + escapeHtml(w.wing_id || '') + '">' +
                '<div class="wing-box-top">' +
                    '<span class="wing-dot ' + dotClass + '"></span>' +
                    '<span class="wing-name">' + escapeHtml(name) + '</span>' +
                '</div>' +
                '<span class="wing-agents">' + (w.agents || []).map(function(a) { return agentIcon(a) || escapeHtml(a); }).join(' ') + '</span>' +
                '<div class="wing-statusbar">' +
                    '<span>' + escapeHtml(plat) + '</span>' +
                    (projectCount ? '<span>' + projectCount + ' proj</span>' : '<span></span>') +
                '</div>' +
            '</div>';
        }).join('');
        wingHtml += '</div>';
        wingStatusEl.innerHTML = wingHtml;

        setupWingDrag();

        // Wire wing box click → wing detail page
        wingStatusEl.querySelectorAll('.wing-box').forEach(function(box) {
            box.addEventListener('click', function(e) {
                if (e.target.closest('.box-menu-btn')) return;
                var mid = box.dataset.wingId;
                navigateToWingDetail(mid);
            });
            box.style.cursor = 'pointer';
        });
    } else {
        wingStatusEl.innerHTML = '';
    }

    // Egg boxes (sessions)
    var hasSessions = sessionsData.length > 0;
    var hasWings = wingsData.length > 0;
    emptyState.style.display = hasSessions ? 'none' : '';
    var noWingsEl = document.getElementById('empty-no-wings');
    var noSessionsEl = document.getElementById('empty-no-sessions');
    if (noWingsEl) noWingsEl.style.display = (!hasSessions && !hasWings) ? '' : 'none';
    if (noSessionsEl) noSessionsEl.style.display = (!hasSessions && hasWings) ? '' : 'none';

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

        var previewHtml = '';
        if (kind === 'chat') {
            previewHtml = '<div class="chat-icon">T</div>';
        } else {
            var thumbUrl = '';
            try { thumbUrl = localStorage.getItem(TERM_THUMB_PREFIX + s.id) || ''; } catch(e) {}
            if (thumbUrl) previewHtml = '<img src="' + thumbUrl + '" alt="">';
        }

        var wingName = '';
        if (s.wing_id) {
            var wing = wingsData.find(function(w) { return w.id === s.wing_id; });
            if (wing) wingName = wingDisplayName(wing);
        }

        return '<div class="egg-box" data-sid="' + s.id + '" data-kind="' + kind + '" data-agent="' + escapeHtml(s.agent || 'claude') + '">' +
            '<div class="egg-preview">' + previewHtml + '</div>' +
            '<div class="egg-footer">' +
                '<span class="session-dot ' + dotClass + '"></span>' +
                '<span class="egg-label">' + escapeHtml(name) + ' \u00b7 ' + agentWithIcon(s.agent || '?') +
                    (needsAttention ? ' \u00b7 !' : '') + '</span>' +
                '<button class="box-menu-btn" title="details">\u22ef</button>' +
                '<button class="btn-sm btn-danger egg-delete" data-sid="' + s.id + '">x</button>' +
            '</div>' +
            (wingName ? '<div class="egg-wing">' + escapeHtml(wingName) + '</div>' : '') +
        '</div>';
    }).join('');
    eggHtml += '</div>';
    sessionsList.innerHTML = eggHtml;

    sessionsList.querySelectorAll('.egg-box').forEach(function(card) {
        card.addEventListener('click', function(e) {
            if (e.target.closest('.box-menu-btn, .egg-delete')) return;
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

    // Wire egg X buttons — click once to arm, click again to delete
    sessionsList.querySelectorAll('.egg-delete').forEach(function(btn) {
        btn.addEventListener('click', function(e) {
            e.stopPropagation();
            if (btn.dataset.armed) {
                window._deleteSession(btn.dataset.sid);
            } else {
                btn.dataset.armed = '1';
                btn.textContent = 'sure?';
                btn.classList.add('btn-armed');
                setTimeout(function() {
                    btn.dataset.armed = '';
                    btn.textContent = 'x';
                    btn.classList.remove('btn-armed');
                }, 3000);
            }
        });
    });

    // Wire egg menu buttons
    sessionsList.querySelectorAll('.egg-box .box-menu-btn').forEach(function(btn) {
        btn.addEventListener('click', function(e) {
            e.stopPropagation();
            var sid = btn.closest('.egg-box').dataset.sid;
            showEggDetail(sid);
        });
    });

    setupEggDrag();
}

// === Command Palette ===

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

function cyclePaletteAgent(dir) {
    var agents = currentPaletteAgents();
    if (agents.length <= 1) return;
    paletteAgentIndex = (paletteAgentIndex + (dir || 1) + agents.length) % agents.length;
    renderPaletteStatus();
}

function onlineWings() {
    return wingsData.filter(function(w) { return w.online !== false; });
}

var homeDirCache = []; // pre-cached ~/  entries

function showPalette() {
    commandPalette.style.display = '';
    paletteSearch.value = '';
    paletteSearch.focus();
    updatePaletteState();
    // Pre-cache home dir entries in background
    var wing = currentPaletteWing();
    if (wing && homeDirCache.length === 0) {
        fetch('/api/app/wings/' + encodeURIComponent(wing.wing_id) + '/ls?path=' + encodeURIComponent('~/')).then(function(r) {
            return r.json();
        }).then(function(entries) {
            if (entries && Array.isArray(entries)) {
                homeDirCache = entries.map(function(e) {
                    return { name: e.name, path: e.path, isDir: e.is_dir };
                });
            }
        }).catch(function() {});
    }
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
            var last = getLastTermAgent();
            var idx = agents.indexOf(last);
            paletteAgentIndex = idx >= 0 ? idx : 0;
        }
        renderPaletteStatus();
        if (!isOpen) {
            renderPaletteResults(paletteSearch.value);
        }
    } else {
        paletteStatus.innerHTML = '<span class="palette-waiting-text">no wings online</span>';
        paletteResults.innerHTML = '<div class="palette-waiting-msg">' +
            '<div class="waiting-dot"></div>' +
            '<div>no wings online</div>' +
            '<div class="palette-waiting-hint"><a href="https://wingthing.ai/install" target="_blank">install wt</a>, then <code>wt login</code> and <code>wt start</code></div>' +
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
    homeDirCache = [];
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

function renderPaletteStatus() {
    var wing = currentPaletteWing();
    var wingName = wing ? wingDisplayName(wing) : '?';
    var agent = currentPaletteAgent();
    paletteStatus.innerHTML = '<span class="accent">' + escapeHtml(wingName) + '</span> &middot; ' +
        'terminal &middot; <span class="accent">' + agentWithIcon(agent) + '</span>';
}

// Count how many git repos from the projects list are at or under a given path.
function nestedRepoCount(dirPath, projects) {
    var prefix = dirPath + '/';
    var count = 0;
    for (var i = 0; i < projects.length; i++) {
        var p = projects[i].path;
        if (p === dirPath || p.indexOf(prefix) === 0) count++;
    }
    return count;
}

function renderPaletteResults(filter) {
    var wing = currentPaletteWing();
    var wingId = wing ? wing.id : '';
    var wingProjects = wingId
        ? allProjects.filter(function(p) { return p.wingId === wingId; })
        : allProjects;

    var items = [];

    // Filter projects: substring match on name or full path
    var lower = filter ? filter.toLowerCase() : '';
    var filtered = lower
        ? wingProjects.filter(function(p) {
            return p.name.toLowerCase().indexOf(lower) !== -1 ||
                   p.path.toLowerCase().indexOf(lower) !== -1;
        })
        : wingProjects.slice();

    // Sort by nested git repo count (dirs with more repos inside rank first)
    filtered.sort(function(a, b) {
        var ca = nestedRepoCount(a.path, wingProjects);
        var cb = nestedRepoCount(b.path, wingProjects);
        if (ca !== cb) return cb - ca;
        return a.name.localeCompare(b.name);
    });

    filtered.forEach(function(p) {
        items.push({ name: p.name, path: p.path, isDir: true });
    });

    // Also include cached home dir entries
    var seenPaths = {};
    items.forEach(function(it) { seenPaths[it.path] = true; });
    var homeExtras = homeDirCache.filter(function(e) {
        if (seenPaths[e.path]) return false;
        return !lower || e.name.toLowerCase().indexOf(lower) !== -1 ||
               e.path.toLowerCase().indexOf(lower) !== -1;
    });
    // Sort home extras by nested repo count too
    homeExtras.sort(function(a, b) {
        var ca = nestedRepoCount(a.path, wingProjects);
        var cb = nestedRepoCount(b.path, wingProjects);
        if (ca !== cb) return cb - ca;
        return a.name.localeCompare(b.name);
    });
    homeExtras.forEach(function(e) {
        items.push({ name: e.name, path: e.path, isDir: e.isDir });
    });

    renderPaletteItems(items);
}

function renderPaletteItems(items) {
    paletteSelectedIndex = 0;

    if (items.length === 0) {
        paletteResults.innerHTML = '';
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

    fetch('/api/app/wings/' + encodeURIComponent(wing.wing_id) + '/ls?path=' + encodeURIComponent(dirPath), {
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
        // Sort: dirs first, then by nested git repo count (most repos first), then alphabetical
        items.sort(function(a, b) {
            if (a.isDir !== b.isDir) return a.isDir ? -1 : 1;
            var ca = nestedRepoCount(a.path, allProjects);
            var cb = nestedRepoCount(b.path, allProjects);
            if (ca !== cb) return cb - ca;
            return a.name.localeCompare(b.name);
        });
        // Prepend the directory itself so e.g. "~" shows "~/" at top
        // Derive absolute path from first entry (wing returns absolute paths)
        var absDirPath = dirPath;
        if (items.length > 0 && items[0].path) {
            absDirPath = items[0].path.replace(/\/[^\/]+$/, '');
        }
        var dirLabel = shortenPath(absDirPath).replace(/\/$/, '') || absDirPath;
        items.unshift({ name: dirLabel, path: absDirPath, isDir: true });

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

function tabCompletePalette() {
    var selected = paletteResults.querySelector('.palette-item.selected');
    if (!selected) return;
    var path = selected.dataset.path;
    if (!path) return;
    var short = shortenPath(path);
    // Check if item is a directory (has trailing / in display name)
    var nameEl = selected.querySelector('.palette-name');
    var isDir = nameEl && nameEl.textContent.slice(-1) === '/';
    if (isDir) {
        paletteSearch.value = short + '/';
        debouncedDirList(paletteSearch.value);
    } else {
        paletteSearch.value = short;
    }
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
    setLastTermAgent(agent);
    showTerminal();
    connectPTY(agent, validCwd, wingId);
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

function sendAttentionAck(sessionId) {
    if (!sessionId || !ptyWs || ptyWs.readyState !== WebSocket.OPEN) return;
    ptyWs.send(JSON.stringify({ type: 'pty.attention_ack', session_id: sessionId }));
}

function isViewingSession(sessionId) {
    return activeView === 'terminal' && sessionId === ptySessionId &&
           document.visibilityState === 'visible';
}

function setNotification(sessionId) {
    if (!sessionId) return;

    // If user is actively viewing this session in the foreground, auto-ack immediately
    if (isViewingSession(sessionId)) {
        sendAttentionAck(sessionId);
        return;
    }

    if (sessionNotifications[sessionId]) return;
    sessionNotifications[sessionId] = true;
    renderSidebar();
    if (activeView === 'home') renderDashboard();

    // Browser notification — request permission on first ping, not page load
    if (document.hidden && 'Notification' in window) {
        if (Notification.permission === 'granted') {
            new Notification('wingthing', { body: 'A session needs your attention' });
        } else if (Notification.permission === 'default') {
            Notification.requestPermission().then(function(p) {
                if (p === 'granted') {
                    new Notification('wingthing', { body: 'A session needs your attention' });
                }
            });
        }
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
    sendAttentionAck(sessionId);
    renderSidebar();
    if (activeView === 'home') renderDashboard();
    if (!Object.keys(sessionNotifications).length) {
        document.title = 'wingthing';
    }
}

// When tab becomes visible, ack any notification for the active session
document.addEventListener('visibilitychange', function() {
    if (document.visibilityState === 'visible' && activeView === 'terminal' && ptySessionId) {
        if (sessionNotifications[ptySessionId]) {
            clearNotification(ptySessionId);
        }
    }
});

// === Navigation ===

function showHome(pushHistory) {
    activeView = 'home';
    homeSection.style.display = '';
    terminalSection.style.display = 'none';
    chatSection.style.display = 'none';
    wingDetailSection.style.display = 'none';
    currentWingId = null;
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
    if (pushHistory !== false) {
        history.pushState({ view: 'home' }, '', location.pathname);
    }
}

function showTerminal() {
    activeView = 'terminal';
    homeSection.style.display = 'none';
    terminalSection.style.display = '';
    chatSection.style.display = 'none';
    wingDetailSection.style.display = 'none';
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
    wingDetailSection.style.display = 'none';
}

function switchToSession(sessionId, pushHistory) {
    detachPTY();
    showTerminal();
    attachPTY(sessionId);
    if (pushHistory !== false) {
        history.pushState({ view: 'terminal', sessionId: sessionId }, '', '#s/' + sessionId);
    }
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
    ptyWingId = null;
    e2eKey = null;

}

// Expose for inline onclick
window._openChat = function (sessionId, agent) {
    showChat();
    resumeChat(sessionId, agent);
};

window._deleteSession = function (sessionId) {
    // Find wing wing_id for fly-replay routing
    var sess = sessionsData.find(function(s) { return s.id === sessionId; });
    var wingId = '';
    if (sess) {
        var wing = wingsData.find(function(w) { return w.id === sess.wing_id; });
        if (wing) wingId = wing.wing_id;
    }
    var cached = getCachedSessions().filter(function (s) { return s.id !== sessionId; });
    setCachedSessions(cached);
    // Remove from sessionsData and egg order immediately to prevent stale order
    sessionsData = sessionsData.filter(function(s) { return s.id !== sessionId; });
    setEggOrder(sessionsData.map(function(s) { return s.id; }));
    clearTermBuffer(sessionId);
    delete sessionNotifications[sessionId];
    if (activeView === 'home') renderDashboard();
    renderSidebar();
    var url = '/api/app/wings/' + encodeURIComponent(wingId) + '/sessions/' + sessionId;
    fetch(url, { method: 'DELETE' }).then(function () {
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
    var chatWing = onlineWings()[0];
    if (chatWing) url += '?wing_id=' + encodeURIComponent(chatWing.id);
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
    var chatSess = sessionsData.find(function(s) { return s.id === sessionId; });
    if (chatSess && chatSess.wing_id) url += '?wing_id=' + encodeURIComponent(chatSess.wing_id);
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
            case 'bandwidth.exceeded':
                chatStatus.textContent = 'bandwidth exceeded';
                chatContainer.classList.remove('thinking');
                if (chatObserver) { chatObserver.error(new Error('Bandwidth limit exceeded. Upgrade to pro for higher limits.')); chatObserver = null; }
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
        var dpr = window.devicePixelRatio || 1;
        var W = 480, H = 260;
        var c = document.createElement('canvas');
        c.width = W * dpr; c.height = H * dpr;
        var ctx = c.getContext('2d');
        ctx.scale(dpr, dpr);
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

        localStorage.setItem(TERM_THUMB_PREFIX + ptySessionId, c.toDataURL('image/webp', 0.6));
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
    if (!identityKey.priv) return null;
    var wingPubBytes = b64ToBytes(wingPublicKeyB64);
    var shared = x25519.getSharedSecret(identityKey.priv, wingPubBytes);
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

function showReplayOverlay() {
    var overlay = document.getElementById('replay-overlay');
    var fill = document.getElementById('replay-fill');
    overlay.style.display = '';
    overlay.classList.remove('fade-out');
    fill.style.width = '0%';
    // Animate bar: fast to 70%, then slow crawl
    setTimeout(function () { fill.style.width = '70%'; fill.style.transition = 'width 0.8s ease-out'; }, 20);
    setTimeout(function () { fill.style.transition = 'width 4s linear'; fill.style.width = '95%'; }, 850);
    // Safety: if replay never arrives, hide after 5s
    setTimeout(function () { if (overlay.style.display !== 'none') hideReplayOverlay(); }, 5000);
}

function hideReplayOverlay() {
    var overlay = document.getElementById('replay-overlay');
    var fill = document.getElementById('replay-fill');
    fill.style.transition = 'width 0.1s linear';
    fill.style.width = '100%';
    setTimeout(function () {
        overlay.classList.add('fade-out');
        setTimeout(function () { overlay.style.display = 'none'; }, 200);
    }, 80);
}

function setupPTYHandlers(ws, reattach) {
    var pendingOutput = [];
    var keyReady = false;
    var replayDone = !reattach; // false during reattach until flushReplay completes

    function gunzip(data) {
        var ds = new DecompressionStream('gzip');
        var writer = ds.writable.getWriter();
        writer.write(data);
        writer.close();
        var reader = ds.readable.getReader();
        var chunks = [];
        function pump() {
            return reader.read().then(function (result) {
                if (result.done) {
                    var total = 0;
                    for (var i = 0; i < chunks.length; i++) total += chunks[i].length;
                    var out = new Uint8Array(total);
                    var off = 0;
                    for (var i = 0; i < chunks.length; i++) { out.set(chunks[i], off); off += chunks[i].length; }
                    return out;
                }
                chunks.push(new Uint8Array(result.value));
                return pump();
            });
        }
        return pump();
    }

    function processOutput(dataStr, compressed) {
        e2eDecrypt(dataStr).then(function (bytes) {
            if (ws !== ptyWs) return;
            return compressed ? gunzip(bytes) : bytes;
        }).then(function (bytes) {
            if (!bytes || ws !== ptyWs) return;
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

    // Flush pending output for reattach: decrypt each frame, skip failures (old-key
    // frames may be mixed in with new-key replay), concatenate survivors, write once.
    // Output arriving during async decrypt is buffered (replayDone=false) and drained after.
    function flushReplay() {
        var pending = pendingOutput;
        pendingOutput = [];
        if (pending.length === 0) { term.reset(); replayDone = true; hideReplayOverlay(); return; }
        Promise.all(pending.map(function (item) {
            // item is { data, compressed } or legacy string
            var dataStr = typeof item === 'string' ? item : item.data;
            var isCompressed = typeof item === 'object' && item.compressed;
            return e2eDecrypt(dataStr).then(function (bytes) {
                return isCompressed ? gunzip(bytes) : bytes;
            }).catch(function () { return null; });
        })).then(function (chunks) {
            if (ws !== ptyWs) return;
            var good = chunks.filter(function (c) { return c !== null; });
            if (good.length === 0) { replayDone = true; hideReplayOverlay(); return; }
            var total = 0;
            for (var i = 0; i < good.length; i++) total += good[i].length;
            var combined = new Uint8Array(total);
            var off = 0;
            for (var i = 0; i < good.length; i++) { combined.set(good[i], off); off += good[i].length; }
            term.reset();
            term.write(combined, function () {
                hideReplayOverlay();
                term.focus();
                // Drain any output that arrived during async flush
                replayDone = true;
                var queued = pendingOutput;
                pendingOutput = [];
                queued.forEach(processOutput);
            });
        }).catch(function () { replayDone = true; hideReplayOverlay(); });
    }

    ws.onmessage = function (e) {
        if (ws !== ptyWs) return; // stale WebSocket
        var msg = JSON.parse(e.data);
        switch (msg.type) {
            case 'relay.restart':
                // Server shutting down — attempt auto-reattach if we have an active session
                if (ptySessionId) {
                    var sid = ptySessionId;
                    ptyReconnecting = true;
                    ptyStatus.textContent = 'reconnecting...';
                    showReconnectBanner();
                    setTimeout(function () { ptyReconnectAttach(sid); }, 1000);
                }
                return;

            case 'pty.started':
                ptySessionId = msg.session_id;
                headerTitle.textContent = sessionTitle(msg.agent, ptyWingId);
                if (!reattach) {
                    history.pushState({ view: 'terminal', sessionId: msg.session_id }, '', '#s/' + msg.session_id);
                }

                if (msg.public_key) {
                    deriveE2EKey(msg.public_key).then(function (key) {
                        if (ws !== ptyWs) return;
                        e2eKey = key;
                        keyReady = true;
                        ptyStatus.textContent = key ? '\uD83D\uDD12' : '';
                        if (reattach) { flushReplay(); } else { pendingOutput.forEach(processOutput); pendingOutput = []; }
                    }).catch(function () {
                        if (ws !== ptyWs) return;
                        keyReady = true;
                        ptyStatus.textContent = '';
                        if (reattach) { flushReplay(); } else { pendingOutput.forEach(processOutput); pendingOutput = []; }
                    });
                } else {
                    keyReady = true;
                    ptyStatus.textContent = '';
                }

                if (!reattach) {
                    term.clear();
                }
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
                if (!keyReady || !replayDone) {
                    pendingOutput.push({ data: msg.data, compressed: !!msg.compressed });
                } else {
                    processOutput(msg.data, !!msg.compressed);
                }
                break;

            case 'pty.exited':
                // Accept exited with error even if session never started (spawn failure)
                if (ptySessionId && msg.session_id !== ptySessionId) break;
                if (!ptySessionId && !msg.error) break;
                headerTitle.textContent = '';
                if (msg.session_id) clearTermBuffer(msg.session_id);
                clearNotification(msg.session_id);
                ptySessionId = null;
                e2eKey = null;
            
                if (msg.error) {
                    ptyStatus.textContent = 'crashed';
                    term.writeln('\r\n\x1b[31;1m--- egg crashed ---\x1b[0m');
                    term.writeln('\x1b[2m' + msg.error.replace(/\n/g, '\r\n') + '\x1b[0m');
                    term.writeln('');
                    term.writeln('\x1b[33mPlease report this bug: https://github.com/ehrlich-b/wingthing/issues\x1b[0m');
                } else {
                    ptyStatus.textContent = 'exited';
                    term.writeln('\r\n\x1b[2m--- session ended ---\x1b[0m');
                }
                // Auto-cleanup dead session from cache and relay
                if (msg.session_id) window._deleteSession(msg.session_id);
                renderSidebar();
                loadHome();
                break;

            case 'bandwidth.exceeded':
                ptyBandwidthExceeded = true;
                ptyStatus.textContent = 'bandwidth exceeded';
                headerTitle.textContent = '';
                term.writeln('\r\n\x1b[33;1m--- bandwidth limit reached ---\x1b[0m');
                term.writeln('\x1b[2mYour free tier monthly bandwidth has been exceeded.\x1b[0m');
                term.writeln('');
                term.writeln('Upgrade to pro for higher limits:');
                term.writeln('  \x1b[36m' + location.origin + '/account\x1b[0m');
                term.writeln('');
                ptySessionId = null;
                ptyWingId = null;
                e2eKey = null;
            
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
        // Don't reconnect if bandwidth exceeded — user needs to upgrade
        if (ptyBandwidthExceeded) {
            ptyBandwidthExceeded = false;
            return;
        }
        // Auto-reattach if we had an active session and aren't already reconnecting
        if (ptySessionId && !ptyReconnecting) {
            var sid = ptySessionId;
            ptyReconnecting = true;
            ptyStatus.textContent = 'reconnecting...';
            showReconnectBanner();
            setTimeout(function () { ptyReconnectAttach(sid); }, 1000);
            return;
        }
        if (!ptyReconnecting) {
            ptyStatus.textContent = '';
            ptySessionId = null;
            ptyWingId = null;
            renderSidebar();
        }
    };

    ws.onerror = function () {
        if (ws !== ptyWs) return;
        if (!ptyReconnecting) ptyStatus.textContent = 'error';
    };
}

function connectPTY(agent, cwd, wingId) {
    // Detach any existing PTY connection first
    detachPTY();
    ptyBandwidthExceeded = false;

    // Clear terminal immediately so stale output from previous session isn't visible
    term.clear();

    // Track which wing this session is on
    ptyWingId = wingId || (onlineWings()[0] || {}).id || null;

    var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    var url = proto + '//' + location.host + '/ws/pty';
    if (ptyWingId) url += '?wing_id=' + encodeURIComponent(ptyWingId);

    headerTitle.textContent = 'connecting...';
    ptyStatus.textContent = '';

    e2eKey = null;

    ptyWs = new WebSocket(url);
    ptyWs.onopen = function () {
        headerTitle.textContent = 'starting ' + agent + '...';
        var startMsg = {
            type: 'pty.start',
            agent: agent,
            cols: term.cols,
            rows: term.rows,
            public_key: identityPubKey,
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

    showReplayOverlay();
    clearNotification(sessionId);

    // Close old WebSocket if still open
    if (ptyWs) { try { ptyWs.close(); } catch(e) {} }

    // Find session info for header
    var sess = sessionsData.find(function(s) { return s.id === sessionId; });
    ptyWingId = sess ? sess.wing_id : null;
    headerTitle.textContent = sess ? sessionTitle(sess.agent || '?', sess.wing_id) : 'reconnecting...';
    ptyStatus.textContent = '';

    // Add wing_id for cross-edge fly-replay routing
    if (ptyWingId) url += '?wing_id=' + encodeURIComponent(ptyWingId);
    else if (sessionId) url += '?session_id=' + encodeURIComponent(sessionId);

    ptyWs = new WebSocket(url);
    ptyWs.onopen = function () {
        ptyWs.send(JSON.stringify({ type: 'pty.attach', session_id: sessionId, public_key: identityPubKey }));
    };

    setupPTYHandlers(ptyWs, true);
}

function disconnectPTY() {
    ptyReconnecting = false;
    if (ptyWs && ptyWs.readyState === WebSocket.OPEN && ptySessionId) {
        ptyWs.send(JSON.stringify({ type: 'pty.kill', session_id: ptySessionId }));
    }
    if (ptyWs) { ptyWs.close(); ptyWs = null; }
    ptySessionId = null;
    ptyWingId = null;
    e2eKey = null;

    ptyStatus.textContent = '';
    headerTitle.textContent = '';
}

// Auto-reattach after relay restart or unexpected disconnect.
// Retries up to 3 times with exponential backoff (1s, 2s, 4s).
function ptyReconnectAttach(sessionId, attempt) {
    attempt = attempt || 0;
    if (attempt >= 3) {
        ptyReconnecting = false;
        ptyStatus.textContent = 'session lost';
        headerTitle.textContent = '';
        ptySessionId = null;
        ptyWingId = null;
        hideReconnectBanner();
        renderSidebar();
        return;
    }

    var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    var url = proto + '//' + location.host + '/ws/pty';
    if (ptyWingId) url += '?wing_id=' + encodeURIComponent(ptyWingId);
    else url += '?session_id=' + encodeURIComponent(sessionId);

    if (ptyWs) { try { ptyWs.close(); } catch(e) {} }

    ptyWs = new WebSocket(url);
    ptyWs.onopen = function () {
        ptyWs.send(JSON.stringify({ type: 'pty.attach', session_id: sessionId, public_key: identityPubKey }));
    };

    var origOnclose = null;
    setupPTYHandlers(ptyWs, true);

    // Override onclose for retry logic
    var innerWs = ptyWs;
    var origClose = innerWs.onclose;
    innerWs.onclose = function () {
        if (innerWs !== ptyWs) return;
        var delay = 1000 * Math.pow(2, attempt);
        setTimeout(function () { ptyReconnectAttach(sessionId, attempt + 1); }, delay);
    };

    // On successful reattach (pty.started received), clear reconnecting state
    var origMsg = innerWs.onmessage;
    innerWs.onmessage = function (e) {
        if (innerWs !== ptyWs) return;
        var msg = JSON.parse(e.data);
        if (msg.type === 'pty.started') {
            ptyReconnecting = false;
            hideReconnectBanner();
        }
        if (msg.type === 'error') {
            // Session not found — stop retrying
            ptyReconnecting = false;
            ptyStatus.textContent = 'session lost';
            headerTitle.textContent = '';
            ptySessionId = null;
            ptyWingId = null;
            hideReconnectBanner();
            renderSidebar();
            if (innerWs) { try { innerWs.close(); } catch(ex) {} }
            return;
        }
        origMsg.call(innerWs, e);
    };
}

// === Helpers ===

function escapeHtml(str) {
    var div = document.createElement('div');
    div.textContent = str;
    return div.innerHTML;
}

// === Browser history (back/forward) ===

window.addEventListener('popstate', function(e) {
    var state = e.state;
    if (!state || state.view === 'home') {
        showHome(false);
    } else if (state.view === 'terminal' && state.sessionId) {
        switchToSession(state.sessionId, false);
    } else if (state.view === 'wing-detail' && state.wingId) {
        navigateToWingDetail(state.wingId, false);
    }
});

if ('serviceWorker' in navigator) {
    navigator.serviceWorker.register('sw.js').catch(function () {});
}

init();
