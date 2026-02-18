import { S, DOM } from './state.js';
import { e2eDecrypt, deriveE2EKey } from './crypto.js';
import { identityPubKey } from './crypto.js';
import { saveTermBuffer, clearTermBuffer } from './terminal.js';
import { checkForNotification, setNotification, clearNotification } from './notify.js';
import { showReconnectBanner, hideReconnectBanner } from './dashboard.js';
import { renderSidebar } from './render.js';
import { loadHome } from './data.js';
import { showHome } from './nav.js';
import { wingDisplayName, b64urlToBytes, bytesToB64url, bytesToB64 } from './helpers.js';
import { saveTunnelAuthTokens } from './tunnel.js';

function sessionTitle(agent, wingId) {
    var wing = S.wingsData.find(function(w) { return w.wing_id === wingId; });
    var name = wing ? wingDisplayName(wing) : '';
    if (name) return name + ' \u00b7 ' + agent;
    return agent || '';
}

function onlineWings() {
    return S.wingsData.filter(function(w) { return w.online !== false; });
}

function showReplayOverlay() {
    var overlay = document.getElementById('replay-overlay');
    var fill = document.getElementById('replay-fill');
    overlay.style.display = '';
    overlay.classList.remove('fade-out');
    fill.style.width = '0%';
    setTimeout(function () { fill.style.width = '70%'; fill.style.transition = 'width 0.8s ease-out'; }, 20);
    setTimeout(function () { fill.style.transition = 'width 4s linear'; fill.style.width = '95%'; }, 850);
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
    // Scroll touch proxy to bottom after session content loads
    if (S.touchProxyScrollToBottom) S.touchProxyScrollToBottom();
}

function showPasskeyOverlay() {
    var overlay = document.getElementById('passkey-overlay');
    if (!overlay) return;
    overlay.style.display = '';
    // Reset button in case showPasskeySetupOverlay changed it
    var btn = overlay.querySelector('button');
    if (btn) {
        btn.textContent = 'authenticate with passkey';
        btn.onclick = null;
    }
}

function hidePasskeyOverlay() {
    var overlay = document.getElementById('passkey-overlay');
    if (overlay) overlay.style.display = 'none';
}

function showPasskeySetupOverlay() {
    var overlay = document.getElementById('passkey-overlay');
    if (!overlay) return;
    overlay.style.display = '';
    var btn = overlay.querySelector('button');
    if (btn) {
        btn.textContent = 'set up passkey';
        btn.onclick = function() {
            location.hash = '#account';
        };
    }
}

export async function handlePTYPasskey() {
    var msg = S.pendingPasskeyChallenge;
    if (!msg || !S.ptyWs) return;
    S.pendingPasskeyChallenge = null;

    var allowCredentials = [];
    try {
        var resp = await fetch('/api/app/passkey');
        if (resp.ok) {
            var creds = await resp.json();
            allowCredentials = creds.filter(function(c) { return c.credential_id; }).map(function(c) {
                return { type: 'public-key', id: b64urlToBytes(c.credential_id) };
            });
        }
    } catch (e) {}

    // No registered passkeys — show setup overlay instead of broken WebAuthn
    if (allowCredentials.length === 0) {
        showPasskeySetupOverlay();
        return;
    }

    var challenge = b64urlToBytes(msg.challenge);
    var opts = {
        publicKey: {
            challenge: challenge,
            rpId: location.hostname,
            userVerification: 'preferred',
            timeout: 60000
        }
    };
    if (allowCredentials.length > 0) opts.publicKey.allowCredentials = allowCredentials;

    try {
        var credential = await navigator.credentials.get(opts);
        S.ptyWs.send(JSON.stringify({
            type: 'passkey.response',
            session_id: msg.session_id,
            credential_id: bytesToB64url(new Uint8Array(credential.rawId)),
            authenticator_data: bytesToB64(new Uint8Array(credential.response.authenticatorData)),
            client_data_json: bytesToB64(new Uint8Array(credential.response.clientDataJSON)),
            signature: bytesToB64(new Uint8Array(credential.response.signature))
        }));
    } catch (e) {
        showPasskeyOverlay();
    }
}

function setupPTYHandlers(ws, reattach) {
    var pendingOutput = [];
    var keyReady = false;
    var replayDone = !reattach;

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
            if (ws !== S.ptyWs) return;
            return compressed ? gunzip(bytes) : bytes;
        }).then(function (bytes) {
            if (!bytes || ws !== S.ptyWs) return;
            S.term.write(bytes);
            saveTermBuffer();
            try {
                var text = new TextDecoder().decode(bytes);
                if (checkForNotification(text)) {
                    setNotification(S.ptySessionId);
                }
            } catch (ex) {}
        }).catch(function (err) {
            console.error('decrypt error, dropping frame:', err);
        });
    }

    function flushReplay() {
        var pending = pendingOutput;
        pendingOutput = [];
        if (pending.length === 0) { S.term.reset(); replayDone = true; hideReplayOverlay(); return; }
        Promise.all(pending.map(function (item) {
            var dataStr = typeof item === 'string' ? item : item.data;
            var isCompressed = typeof item === 'object' && item.compressed;
            return e2eDecrypt(dataStr).then(function (bytes) {
                return isCompressed ? gunzip(bytes) : bytes;
            }).catch(function () { return null; });
        })).then(function (chunks) {
            if (ws !== S.ptyWs) return;
            var good = chunks.filter(function (c) { return c !== null; });
            if (good.length === 0) { replayDone = true; hideReplayOverlay(); return; }
            var total = 0;
            for (var i = 0; i < good.length; i++) total += good[i].length;
            var combined = new Uint8Array(total);
            var off = 0;
            for (var i = 0; i < good.length; i++) { combined.set(good[i], off); off += good[i].length; }
            S.term.reset();
            S.term.write(combined, function () {
                hideReplayOverlay();
                S.term.focus();
                replayDone = true;
                var queued = pendingOutput;
                pendingOutput = [];
                queued.forEach(processOutput);
            });
        }).catch(function () { replayDone = true; hideReplayOverlay(); });
    }

    ws.onmessage = function (e) {
        if (ws !== S.ptyWs) return;
        var msg = JSON.parse(e.data);
        switch (msg.type) {
            case 'relay.restart':
                if (S.ptySessionId) {
                    var sid = S.ptySessionId;
                    S.ptyReconnecting = true;
                    DOM.ptyStatus.textContent = 'reconnecting...';
                    showReconnectBanner();
                    setTimeout(function () { ptyReconnectAttach(sid); }, 1000);
                }
                return;

            case 'wing.offline':
                if (S.ptySessionId) {
                    var sid = S.ptySessionId;
                    S.ptyReconnecting = true;
                    DOM.ptyStatus.textContent = 'wing restarting...';
                    showReconnectBanner('wing restarting...');
                    setTimeout(function () { ptyReconnectAttach(sid); }, 1000);
                }
                return;

            case 'passkey.challenge':
                S.pendingPasskeyChallenge = msg;
                showPasskeyOverlay();
                return;

            case 'pty.started':
                if (reattach && S.ptySessionId && msg.session_id !== S.ptySessionId) {
                    console.warn('pty.started for wrong session:', msg.session_id, 'expected:', S.ptySessionId);
                    break;
                }
                S.ptySessionId = msg.session_id;
                DOM.headerTitle.textContent = sessionTitle(msg.agent, S.ptyWingId);
                DOM.sessionCloseBtn.style.display = '';
                hidePasskeyOverlay();
                if (msg.auth_token && S.ptyWingId) {
                    S.tunnelAuthTokens[S.ptyWingId] = msg.auth_token;
                    saveTunnelAuthTokens();
                }
                if (!reattach) {
                    history.pushState({ view: 'terminal', sessionId: msg.session_id }, '', '#s/' + msg.session_id);
                }

                if (msg.public_key) {
                    deriveE2EKey(msg.public_key).then(function (key) {
                        if (ws !== S.ptyWs) return;
                        S.e2eKey = key;
                        keyReady = true;
                        DOM.ptyStatus.textContent = key ? '\uD83D\uDD12' : '';
                        if (reattach) { flushReplay(); } else { pendingOutput.forEach(processOutput); pendingOutput = []; }
                    }).catch(function () {
                        if (ws !== S.ptyWs) return;
                        keyReady = true;
                        DOM.ptyStatus.textContent = '';
                        if (reattach) { flushReplay(); } else { pendingOutput.forEach(processOutput); pendingOutput = []; }
                    });
                } else {
                    keyReady = true;
                    DOM.ptyStatus.textContent = '';
                }

                if (!reattach) {
                    S.term.clear();
                }
                S.term.focus();
                renderSidebar();
                loadHome();

                S.term.onResize(function (size) {
                    if (S.ptyWs && S.ptyWs.readyState === WebSocket.OPEN && S.ptySessionId) {
                        S.ptyWs.send(JSON.stringify({ type: 'pty.resize', session_id: S.ptySessionId, cols: size.cols, rows: size.rows }));
                    }
                });
                S.fitAddon.fit();
                // Always send resize on session load — fitAddon.fit() only triggers
                // onResize when dimensions change, but the remote PTY may have stale
                // dimensions from a different machine/window.
                if (S.ptyWs && S.ptyWs.readyState === WebSocket.OPEN && S.ptySessionId) {
                    S.ptyWs.send(JSON.stringify({ type: 'pty.resize', session_id: S.ptySessionId, cols: S.term.cols, rows: S.term.rows }));
                }
                break;

            case 'pty.output':
                if (msg.session_id && S.ptySessionId && msg.session_id !== S.ptySessionId) {
                    console.warn('pty.output for wrong session:', msg.session_id, 'expected:', S.ptySessionId);
                    break;
                }
                if (!keyReady || !replayDone) {
                    pendingOutput.push({ data: msg.data, compressed: !!msg.compressed });
                } else {
                    processOutput(msg.data, !!msg.compressed);
                }
                break;

            case 'pty.exited':
                if (S.ptySessionId && msg.session_id !== S.ptySessionId) break;
                if (!S.ptySessionId && !msg.error) break;
                DOM.headerTitle.textContent = '';
                DOM.sessionCloseBtn.style.display = 'none';
                if (msg.session_id) clearTermBuffer(msg.session_id);
                clearNotification(msg.session_id);
                S.ptySessionId = null;
                S.e2eKey = null;

                if (msg.error) {
                    DOM.ptyStatus.textContent = 'crashed';
                    S.term.writeln('\r\n\x1b[31;1m--- egg crashed ---\x1b[0m');
                    S.term.writeln('\x1b[2m' + msg.error.replace(/\n/g, '\r\n') + '\x1b[0m');
                    S.term.writeln('');
                    S.term.writeln('\x1b[33mPlease report this bug: https://github.com/ehrlich-b/wingthing/issues\x1b[0m');
                } else {
                    DOM.ptyStatus.textContent = 'exited';
                    S.term.writeln('\r\n\x1b[2m--- session ended ---\x1b[0m');
                }
                if (msg.session_id) window._deleteSession(msg.session_id);
                renderSidebar();
                loadHome();
                break;

            case 'bandwidth.exceeded':
                S.ptyBandwidthExceeded = true;
                DOM.ptyStatus.textContent = 'bandwidth exceeded';
                DOM.headerTitle.textContent = '';
                DOM.sessionCloseBtn.style.display = 'none';
                S.term.writeln('\r\n\x1b[33;1m--- bandwidth limit reached ---\x1b[0m');
                S.term.writeln('\x1b[2mYour free tier monthly bandwidth has been exceeded.\x1b[0m');
                S.term.writeln('');
                S.term.writeln('Upgrade to pro for higher limits:');
                S.term.writeln('  \x1b[36m' + location.origin + '/account\x1b[0m');
                S.term.writeln('');
                S.ptySessionId = null;
                S.ptyWingId = null;
                S.e2eKey = null;

                renderSidebar();
                loadHome();
                break;

            case 'error':
                DOM.ptyStatus.textContent = msg.message;
                break;
        }
    };

    ws.onclose = function () {
        if (ws !== S.ptyWs) return;
        if (S.ptyBandwidthExceeded) {
            S.ptyBandwidthExceeded = false;
            return;
        }
        if (S.ptySessionId && !S.ptyReconnecting) {
            var sid = S.ptySessionId;
            S.ptyReconnecting = true;
            DOM.ptyStatus.textContent = 'reconnecting...';
            showReconnectBanner();
            setTimeout(function () { ptyReconnectAttach(sid); }, 1000);
            return;
        }
        if (!S.ptyReconnecting) {
            DOM.ptyStatus.textContent = '';
            S.ptySessionId = null;
            S.ptyWingId = null;
            renderSidebar();
        }
    };

    ws.onerror = function () {
        if (ws !== S.ptyWs) return;
        if (!S.ptyReconnecting) DOM.ptyStatus.textContent = 'error';
    };
}

export function connectPTY(agent, cwd, wingId) {
    detachPTY();
    S.ptyBandwidthExceeded = false;

    S.term.clear();

    S.ptyWingId = wingId || (onlineWings()[0] || {}).wing_id || null;

    var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    var url = proto + '//' + location.host + '/ws/pty';
    if (S.ptyWingId) url += '?wing_id=' + encodeURIComponent(S.ptyWingId);

    DOM.headerTitle.textContent = 'connecting...';
    DOM.ptyStatus.textContent = '';

    S.e2eKey = null;

    S.ptyWs = new WebSocket(url);
    S.ptyWs.onopen = function () {
        DOM.headerTitle.textContent = 'starting ' + agent + '...';
        var startMsg = {
            type: 'pty.start',
            agent: agent,
            cols: S.term.cols,
            rows: S.term.rows,
            public_key: identityPubKey,
        };
        if (cwd) startMsg.cwd = cwd;
        if (wingId) startMsg.wing_id = wingId;
        if (wingId && S.tunnelAuthTokens[wingId]) startMsg.auth_token = S.tunnelAuthTokens[wingId];
        S.ptyWs.send(JSON.stringify(startMsg));
    };

    setupPTYHandlers(S.ptyWs, false);
}

export function attachPTY(sessionId) {
    var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    var url = proto + '//' + location.host + '/ws/pty';

    showReplayOverlay();
    clearNotification(sessionId);

    if (S.ptyWs) { try { S.ptyWs.close(); } catch(e) {} }

    var sess = S.sessionsData.find(function(s) { return s.id === sessionId; });
    S.ptyWingId = sess ? sess.wing_id : null;
    DOM.headerTitle.textContent = sess ? sessionTitle(sess.agent || '?', sess.wing_id) : 'reconnecting...';
    DOM.ptyStatus.textContent = '';

    if (S.ptyWingId) url += '?wing_id=' + encodeURIComponent(S.ptyWingId);
    else if (sessionId) url += '?session_id=' + encodeURIComponent(sessionId);

    S.ptyWs = new WebSocket(url);
    S.ptyWs.onopen = function () {
        var msg = { type: 'pty.attach', session_id: sessionId, public_key: identityPubKey };
        if (S.ptyWingId) msg.wing_id = S.ptyWingId;
        if (S.ptyWingId && S.tunnelAuthTokens[S.ptyWingId]) msg.auth_token = S.tunnelAuthTokens[S.ptyWingId];
        if (S.term) { msg.cols = S.term.cols; msg.rows = S.term.rows; }
        S.ptyWs.send(JSON.stringify(msg));
    };

    setupPTYHandlers(S.ptyWs, true);
}

export function detachPTY() {
    if (S.ptyWs) {
        if (S.ptySessionId && S.ptyWs.readyState === WebSocket.OPEN) {
            S.ptyWs.send(JSON.stringify({ type: 'pty.detach', session_id: S.ptySessionId }));
        }
        S.ptyWs.close();
        S.ptyWs = null;
    }
    S.ptySessionId = null;
    S.ptyWingId = null;
    S.e2eKey = null;
}

export function disconnectPTY() {
    S.ptyReconnecting = false;
    if (S.ptyWs && S.ptyWs.readyState === WebSocket.OPEN && S.ptySessionId) {
        S.ptyWs.send(JSON.stringify({ type: 'pty.kill', session_id: S.ptySessionId }));
    }
    if (S.ptyWs) { S.ptyWs.close(); S.ptyWs = null; }
    S.ptySessionId = null;
    S.ptyWingId = null;
    S.e2eKey = null;

    DOM.ptyStatus.textContent = '';
    DOM.headerTitle.textContent = '';
    DOM.sessionCloseBtn.style.display = 'none';
}

var MAX_RECONNECT_ATTEMPTS = 10;

function ptyReconnectAttach(sessionId, attempt) {
    attempt = attempt || 0;
    if (attempt >= MAX_RECONNECT_ATTEMPTS) {
        S.ptyReconnecting = false;
        DOM.ptyStatus.textContent = 'session lost';
        showReconnectBanner('connection lost', true);
        return;
    }

    var attemptText = 'reconnecting (' + (attempt + 1) + '/' + MAX_RECONNECT_ATTEMPTS + ')...';
    DOM.ptyStatus.textContent = attemptText;
    showReconnectBanner(attemptText);

    var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    var url = proto + '//' + location.host + '/ws/pty';
    if (S.ptyWingId) url += '?wing_id=' + encodeURIComponent(S.ptyWingId);
    else url += '?session_id=' + encodeURIComponent(sessionId);

    if (S.ptyWs) { try { S.ptyWs.close(); } catch(e) {} }

    S.ptyWs = new WebSocket(url);
    S.ptyWs.onopen = function () {
        var msg = { type: 'pty.attach', session_id: sessionId, public_key: identityPubKey };
        if (S.ptyWingId) msg.wing_id = S.ptyWingId;
        if (S.ptyWingId && S.tunnelAuthTokens[S.ptyWingId]) msg.auth_token = S.tunnelAuthTokens[S.ptyWingId];
        if (S.term) { msg.cols = S.term.cols; msg.rows = S.term.rows; }
        S.ptyWs.send(JSON.stringify(msg));
    };

    setupPTYHandlers(S.ptyWs, true);

    var innerWs = S.ptyWs;
    innerWs.onclose = function () {
        if (innerWs !== S.ptyWs) return;
        var delay = Math.min(1000 * Math.pow(2, attempt), 30000);
        setTimeout(function () { ptyReconnectAttach(sessionId, attempt + 1); }, delay);
    };

    var origMsg = innerWs.onmessage;
    innerWs.onmessage = function (e) {
        if (innerWs !== S.ptyWs) return;
        var msg = JSON.parse(e.data);
        if (msg.type === 'pty.started') {
            S.ptyReconnecting = false;
            hideReconnectBanner();
        }
        if (msg.type === 'error') {
            S.ptyReconnecting = false;
            DOM.ptyStatus.textContent = 'session lost';
            showReconnectBanner('connection lost', true);
            if (innerWs) { try { innerWs.close(); } catch(ex) {} }
            return;
        }
        origMsg.call(innerWs, e);
    };
}

export function retryReconnect() {
    if (!S.ptySessionId) return;
    S.ptyReconnecting = true;
    ptyReconnectAttach(S.ptySessionId, 0);
}
