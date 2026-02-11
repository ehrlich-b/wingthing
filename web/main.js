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
let latestVersion = '';

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
        var stored = localStorage.getItem(IDENTITY_PUBKEY_KEY);
        if (stored) return stored;
        var priv = x25519.utils.randomSecretKey();
        localStorage.setItem(IDENTITY_PRIVKEY_KEY, bytesToB64(priv));
        var pub = bytesToB64(x25519.getPublicKey(priv));
        localStorage.setItem(IDENTITY_PUBKEY_KEY, pub);
        return pub;
    } catch (e) { return ''; }
}

var identityPubKey = getOrCreateIdentityKey();

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
                w.version = ev.version || w.version;
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
                version: ev.version || '',
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
        return { machine_id: w.machine_id, id: w.id, platform: w.platform, version: w.version, agents: w.agents, labels: w.labels, projects: w.projects };
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
        // Relay unreachable — render from cache
        sessions = [];
        wings = [];
    }

    // Merge live sessions with cache (preserve existing order)
    if (sessions.length > 0) {
        var liveSessionMap = {};
        sessions.forEach(function(s) { liveSessionMap[s.id] = s; });
        var cachedSessions = getCachedSessions();
        var cachedSessionMap = {};
        cachedSessions.forEach(function(s) { cachedSessionMap[s.id] = s; });
        // Preserve existing sessionsData order, update in place
        var seenSess = {};
        var mergedSessions = [];
        sessionsData.forEach(function(existing) {
            if (liveSessionMap[existing.id]) {
                mergedSessions.push(liveSessionMap[existing.id]);
            } else if (cachedSessionMap[existing.id]) {
                cachedSessionMap[existing.id].status = 'detached';
                mergedSessions.push(cachedSessionMap[existing.id]);
            }
            seenSess[existing.id] = true;
        });
        // Append new sessions not in current order
        sessions.forEach(function(s) {
            if (!seenSess[s.id]) { mergedSessions.push(s); seenSess[s.id] = true; }
        });
        sessionsData = sortSessionsByOrder(mergedSessions);
        setEggOrder(sessionsData.map(function(s) { return s.id; }));
        setCachedSessions(sessionsData);
    } else {
        // API empty (relay restarted, wing hasn't reclaimed yet) — keep cached
        var cachedSessions = getCachedSessions();
        if (cachedSessions.length > 0) {
            cachedSessions.forEach(function(s) { s.status = 'detached'; });
            sessionsData = sortSessionsByOrder(cachedSessions);
        } else {
            sessionsData = [];
        }
    }

    // Merge live wings with cached wings (stable by machine_id, preserve existing order)
    var cached = getCachedWings();
    var liveMap = {};
    wings.forEach(function (w) {
        w.online = true;
        liveMap[w.machine_id] = w;
    });
    var cachedMap = {};
    cached.forEach(function (w) {
        w.online = false;
        cachedMap[w.machine_id] = w;
    });
    // Start from existing wingsData order, update in place
    var seen = {};
    var merged = [];
    wingsData.forEach(function (existing) {
        var mid = existing.machine_id;
        if (liveMap[mid]) {
            merged.push(liveMap[mid]);
        } else if (cachedMap[mid]) {
            merged.push(cachedMap[mid]);
        } else {
            existing.online = false;
            merged.push(existing);
        }
        seen[mid] = true;
    });
    // Append any new wings not in current order
    wings.forEach(function (w) {
        if (!seen[w.machine_id]) { merged.push(w); seen[w.machine_id] = true; }
    });
    cached.forEach(function (w) {
        if (!seen[w.machine_id]) { merged.push(w); seen[w.machine_id] = true; }
    });
    wingsData = sortWingsByOrder(merged);

    // Extract latest_version from any wing response
    wingsData.forEach(function(w) {
        if (w.latest_version) latestVersion = w.latest_version;
    });

    // Cache for next load (only essential fields)
    setCachedWings(wingsData.map(function (w) {
        return { machine_id: w.machine_id, id: w.id, platform: w.platform, version: w.version, agents: w.agents, labels: w.labels, projects: w.projects };
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

function hideDetailModal() {
    detailOverlay.classList.remove('open');
    detailDialog.innerHTML = '';
}

function showWingDetail(machineId) {
    var w = wingsData.find(function(w) { return w.machine_id === machineId; });
    if (!w) return;
    var name = w.machine_id || w.id.substring(0, 8);
    var isOnline = w.online !== false;
    var dotClass = isOnline ? 'live' : 'offline';

    // Wing ID: truncated to 8 chars, copyable
    var wingIdShort = w.id ? w.id.substring(0, 8) + '...' : 'none';
    var wingIdHtml = w.id
        ? '<span class="detail-val text-dim copyable" data-copy="' + escapeHtml(w.id) + '">' + escapeHtml(wingIdShort) + '</span>'
        : '<span class="detail-val text-dim">none</span>';

    // Version with update hint
    var versionHtml = escapeHtml(w.version || 'unknown');
    var updateAvailable = latestVersion && w.version && w.version !== latestVersion;
    if (updateAvailable) {
        versionHtml += '<span class="detail-update-hint">(update available)</span>';
    }

    // Public key: truncated to 16 chars, copyable
    var pubKeyHtml;
    if (w.public_key) {
        var pubKeyShort = w.public_key.substring(0, 16) + '...';
        pubKeyHtml = '<span class="detail-val text-dim copyable" data-copy="' + escapeHtml(w.public_key) + '">' + escapeHtml(pubKeyShort) + '</span>';
    } else {
        pubKeyHtml = '<span class="detail-val text-dim">none</span>';
    }

    // My key (browser identity)
    var myKeyHtml;
    if (identityPubKey) {
        var myKeyShort = identityPubKey.substring(0, 16) + '...';
        myKeyHtml = '<span class="detail-val text-dim copyable" data-copy="' + escapeHtml(identityPubKey) + '">' + escapeHtml(myKeyShort) + '</span>';
    } else {
        myKeyHtml = '<span class="detail-val text-dim">none</span>';
    }

    // Projects: sorted by mod_time desc, show first 8
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

    detailDialog.innerHTML =
        '<h3><span class="detail-connection-dot ' + dotClass + '"></span>' + escapeHtml(name) + '</h3>' +
        '<div class="detail-row"><span class="detail-key">wing id</span>' + wingIdHtml + '</div>' +
        '<div class="detail-row"><span class="detail-key">platform</span><span class="detail-val">' + escapeHtml(w.platform || 'unknown') + '</span></div>' +
        '<div class="detail-row"><span class="detail-key">version</span><span class="detail-val">' + versionHtml + '</span></div>' +
        '<div class="detail-row"><span class="detail-key">agents</span><span class="detail-val">' + escapeHtml((w.agents || []).join(', ') || 'none') + '</span></div>' +
        '<div class="detail-row"><span class="detail-key">labels</span><span class="detail-val">' + escapeHtml((w.labels || []).join(', ') || 'none') + '</span></div>' +
        '<div class="detail-row"><span class="detail-key">public key</span>' + pubKeyHtml + '</div>' +
        '<div class="detail-row"><span class="detail-key">my key</span>' + myKeyHtml + '</div>' +
        '<div class="detail-row"><span class="detail-key">projects</span><div class="detail-val">' + projList + '</div></div>' +
        (isOnline && updateAvailable ? '<div class="detail-actions"><button class="btn-sm btn-accent" id="detail-wing-update">update wing</button></div>' : '');

    setupCopyable(detailDialog);
    detailOverlay.classList.add('open');

    var updateBtn = document.getElementById('detail-wing-update');
    if (updateBtn) {
        updateBtn.addEventListener('click', function() {
            updateBtn.textContent = 'updating...';
            updateBtn.disabled = true;
            fetch('/api/app/wings/' + w.id + '/update', { method: 'POST' })
                .then(function(r) { return r.json(); })
                .then(function() { updateBtn.textContent = 'sent'; })
                .catch(function() { updateBtn.textContent = 'failed'; updateBtn.disabled = false; });
        });
    }
}

function showEggDetail(sessionId) {
    var s = sessionsData.find(function(s) { return s.id === sessionId; });
    if (!s) return;
    var name = projectName(s.cwd);
    var kind = s.kind || 'terminal';
    var wingName = '';
    if (s.wing_id) {
        var wing = wingsData.find(function(w) { return w.id === s.wing_id; });
        if (wing) wingName = wing.machine_id || '';
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
            var name = w.machine_id || w.id.substring(0, 8);
            var dotClass = w.online !== false ? 'dot-live' : 'dot-offline';
            var projectCount = (w.projects || []).length;
            var plat = w.platform === 'darwin' ? 'mac' : (w.platform || '');
            return '<div class="wing-box" draggable="true" data-machine-id="' + escapeHtml(w.machine_id || '') + '">' +
                '<div class="wing-box-top">' +
                    '<span class="wing-dot ' + dotClass + '"></span>' +
                    '<span class="wing-name">' + escapeHtml(name) + '</span>' +
                    '<button class="box-menu-btn" title="details">\u22ef</button>' +
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

        // Wire wing menu buttons
        wingStatusEl.querySelectorAll('.wing-box .box-menu-btn').forEach(function(btn) {
            btn.addEventListener('click', function(e) {
                e.stopPropagation();
                var mid = btn.closest('.wing-box').dataset.machineId;
                showWingDetail(mid);
            });
        });
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
            if (wing) wingName = wing.machine_id || '';
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
        escapeHtml(paletteMode) + ' &middot; <span class="accent">' + agentWithIcon(agent) + '</span>';
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
    // Remove from sessionsData and egg order immediately to prevent stale order
    sessionsData = sessionsData.filter(function(s) { return s.id !== sessionId; });
    setEggOrder(sessionsData.map(function(s) { return s.id; }));
    clearTermBuffer(sessionId);
    delete sessionNotifications[sessionId];
    if (activeView === 'home') renderDashboard();
    renderSidebar();
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
                // Accept exited with error even if session never started (spawn failure)
                if (ptySessionId && msg.session_id !== ptySessionId) break;
                if (!ptySessionId && !msg.error) break;
                headerTitle.textContent = '';
                if (msg.session_id) clearTermBuffer(msg.session_id);
                clearNotification(msg.session_id);
                ptySessionId = null;
                e2eKey = null;
                ephemeralPrivKey = null;
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
