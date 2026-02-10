import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import '@xterm/xterm/css/xterm.css';
import { x25519 } from '@noble/curves/ed25519.js';

// State
let currentMode = 'wings';
let ptyWs = null;
let ptySessionId = null;
let term = null;
let fitAddon = null;
let ctrlActive = false;
let altActive = false;
let currentUser = null;
let e2eKey = null; // CryptoKey for AES-GCM
let ephemeralPrivKey = null; // Uint8Array(32)

// DOM refs
const userInfo = document.getElementById('user-info');
const wingsSection = document.getElementById('wings-section');
const wingsList = document.getElementById('wings-list');
const noWings = document.getElementById('no-wings');
const terminalSection = document.getElementById('terminal-section');
const terminalContainer = document.getElementById('terminal-container');
const agentSelect = document.getElementById('agent-select');
const connectBtn = document.getElementById('connect-btn');
const ptyStatus = document.getElementById('pty-status');

// Init
async function init() {
    // Check auth
    try {
        var resp = await fetch('/api/app/me');
        if (resp.status === 401) {
            window.location.href = '/login?next=/app/';
            return;
        }
        currentUser = await resp.json();
        userInfo.textContent = currentUser.display_name || 'user';
    } catch (e) {
        window.location.href = '/login?next=/app/';
        return;
    }

    // Mode tabs
    document.querySelectorAll('.tab').forEach(function (tab) {
        tab.addEventListener('click', function () {
            switchMode(tab.dataset.mode);
        });
    });

    connectBtn.addEventListener('click', function () {
        if (ptyWs && ptyWs.readyState === WebSocket.OPEN) {
            disconnectPTY();
        } else {
            connectPTY();
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

    window.addEventListener('resize', function () {
        if (term && fitAddon) {
            fitAddon.fit();
        }
    });

    initTerminal();
    loadWings();

    // Refresh wings every 10s
    setInterval(loadWings, 10000);
}

function switchMode(mode) {
    currentMode = mode;
    document.querySelectorAll('.tab').forEach(function (t) {
        t.classList.toggle('active', t.dataset.mode === mode);
    });
    wingsSection.style.display = mode === 'wings' ? '' : 'none';
    terminalSection.style.display = mode === 'terminal' ? '' : 'none';
    if (mode === 'terminal' && term) {
        fitAddon.fit();
        term.focus();
    }
}

// Wings dashboard
async function loadWings() {
    try {
        var resp = await fetch('/api/app/wings');
        if (resp.status === 401) return;
        var wings = await resp.json();

        // Also load sessions for reconnect buttons
        var sessions = [];
        try {
            var sessResp = await fetch('/api/app/sessions');
            if (sessResp.ok) sessions = await sessResp.json();
        } catch (e) {}
        var detached = (sessions || []).filter(function (s) { return s.status === 'detached'; });

        if ((!wings || wings.length === 0) && detached.length === 0) {
            wingsList.innerHTML = '';
            noWings.style.display = '';
            return;
        }

        noWings.style.display = 'none';
        var html = '';

        // Detached sessions first
        if (detached.length > 0) {
            html += '<div class="detached-sessions">';
            html += detached.map(function (s) {
                return '<div class="wing-card detached">' +
                    '<div class="wing-header">' +
                        '<span class="wing-machine">' + escapeHtml(s.agent || 'session') + '</span>' +
                        '<span class="wing-status detached">detached</span>' +
                    '</div>' +
                    '<div class="wing-agents"><span class="badge">session ' + escapeHtml(s.id) + '</span></div>' +
                    '<div class="wing-actions">' +
                        '<button class="wing-connect-btn" onclick="window._reattachSession(\'' + s.id + '\')">reconnect</button>' +
                        '<button class="wing-delete-btn" onclick="window._deleteSession(\'' + s.id + '\')">delete</button>' +
                    '</div>' +
                '</div>';
            }).join('');
            html += '</div>';
        }

        // Online wings
        html += (wings || []).map(function (w) {
            var agentBadges = (w.agents || []).map(function (a) {
                return '<span class="badge">' + escapeHtml(a) + '</span>';
            }).join('');
            var labelBadges = (w.labels || []).map(function (l) {
                return '<span class="badge label">' + escapeHtml(l) + '</span>';
            }).join('');
            return '<div class="wing-card" data-wing-id="' + w.id + '">' +
                '<div class="wing-header">' +
                    '<span class="wing-machine">' + escapeHtml(w.machine_id) + '</span>' +
                    '<span class="wing-status online">online</span>' +
                '</div>' +
                '<div class="wing-agents">' + agentBadges + '</div>' +
                (labelBadges ? '<div class="wing-labels">' + labelBadges + '</div>' : '') +
                '<button class="wing-connect-btn" onclick="window._connectToWing(\'' + w.id + '\')">terminal</button>' +
            '</div>';
        }).join('');

        wingsList.innerHTML = html;
    } catch (e) {
        console.error('load wings:', e);
    }
}

// Expose for inline onclick
window._connectToWing = function (wingId) {
    switchMode('terminal');
    connectPTY();
};

window._reattachSession = function (sessionId) {
    switchMode('terminal');
    attachPTY(sessionId);
};

window._deleteSession = function (sessionId) {
    fetch('/api/app/sessions/' + sessionId, { method: 'DELETE' }).then(function () {
        loadWings();
    });
};

// Terminal
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
    term.loadAddon(fitAddon);
    term.open(terminalContainer);
    fitAddon.fit();

    term.writeln('\x1b[1;35mwingthing terminal\x1b[0m');
    term.writeln('select an agent and click connect.\r\n');

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

// E2E crypto helpers
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

    // HKDF-SHA256, salt = 32 zero bytes, info = "wt-pty"
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
    // iv || ciphertext+tag
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

function connectPTY() {
    var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    var url = proto + '//' + location.host + '/ws/pty';

    ptyStatus.textContent = 'connecting...';
    connectBtn.disabled = true;

    // Generate ephemeral X25519 keypair for E2E
    ephemeralPrivKey = x25519.utils.randomPrivateKey();
    var ephemeralPubKey = x25519.getPublicKey(ephemeralPrivKey);
    var pubKeyB64 = bytesToB64(ephemeralPubKey);

    e2eKey = null;

    ptyWs = new WebSocket(url);

    ptyWs.onopen = function () {
        ptyStatus.textContent = 'starting ' + agentSelect.value + '...';
        ptyWs.send(JSON.stringify({
            type: 'pty.start',
            agent: agentSelect.value,
            cols: term.cols,
            rows: term.rows,
            public_key: pubKeyB64,
        }));
    };

    ptyWs.onmessage = function (e) {
        var msg = JSON.parse(e.data);
        switch (msg.type) {
            case 'pty.started':
                ptySessionId = msg.session_id;
                // Derive shared key if wing sent its public key
                if (msg.public_key) {
                    deriveE2EKey(msg.public_key).then(function (key) {
                        e2eKey = key;
                        if (key) {
                            ptyStatus.textContent = msg.agent + ' (encrypted)';
                        } else {
                            ptyStatus.textContent = msg.agent + ' (live)';
                        }
                    }).catch(function () {
                        ptyStatus.textContent = msg.agent + ' (live)';
                    });
                } else {
                    ptyStatus.textContent = msg.agent + ' (live)';
                }
                connectBtn.textContent = 'disconnect';
                connectBtn.disabled = false;
                term.clear();
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
                }).catch(function (err) {
                    console.error('decrypt error:', err);
                    // Fall back to raw base64
                    var binary = atob(msg.data);
                    var bytes = new Uint8Array(binary.length);
                    for (var i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
                    term.write(bytes);
                });
                break;

            case 'pty.exited':
                ptyStatus.textContent = 'exited (code ' + msg.exit_code + ')';
                ptySessionId = null;
                e2eKey = null;
                ephemeralPrivKey = null;
                connectBtn.textContent = 'connect';
                connectBtn.disabled = false;
                term.writeln('\r\n\x1b[1;31m--- session ended ---\x1b[0m');
                break;

            case 'error':
                ptyStatus.textContent = msg.message;
                connectBtn.disabled = false;
                break;
        }
    };

    ptyWs.onclose = function () {
        ptyStatus.textContent = 'disconnected';
        ptySessionId = null;
        connectBtn.textContent = 'connect';
        connectBtn.disabled = false;
    };

    ptyWs.onerror = function () {
        ptyStatus.textContent = 'connection error';
        connectBtn.disabled = false;
    };
}

function attachPTY(sessionId) {
    var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    var url = proto + '//' + location.host + '/ws/pty';

    ptyStatus.textContent = 'reconnecting...';
    connectBtn.disabled = true;

    // Generate new ephemeral keypair for re-keyed E2E
    ephemeralPrivKey = x25519.utils.randomPrivateKey();
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

    ptyWs.onmessage = function (e) {
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
                connectBtn.textContent = 'disconnect';
                connectBtn.disabled = false;
                term.clear();
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
                }).catch(function (err) {
                    console.error('decrypt error:', err);
                    var binary = atob(msg.data);
                    var bytes = new Uint8Array(binary.length);
                    for (var i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
                    term.write(bytes);
                });
                break;

            case 'pty.exited':
                ptyStatus.textContent = 'exited (code ' + msg.exit_code + ')';
                ptySessionId = null;
                e2eKey = null;
                ephemeralPrivKey = null;
                connectBtn.textContent = 'connect';
                connectBtn.disabled = false;
                term.writeln('\r\n\x1b[1;31m--- session ended ---\x1b[0m');
                break;

            case 'error':
                ptyStatus.textContent = msg.message;
                connectBtn.disabled = false;
                break;
        }
    };

    ptyWs.onclose = function () {
        ptyStatus.textContent = 'disconnected';
        ptySessionId = null;
        connectBtn.textContent = 'connect';
        connectBtn.disabled = false;
    };

    ptyWs.onerror = function () {
        ptyStatus.textContent = 'connection error';
        connectBtn.disabled = false;
    };
}

function disconnectPTY() {
    // Send kill to terminate the PTY process on the wing
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
    connectBtn.textContent = 'connect';
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
