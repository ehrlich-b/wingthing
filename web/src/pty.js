import { S, DOM } from './state.js';
import { findSessionResource } from './session-inventory.js';
import { terminalReferenceMatches } from './session-reference.js';
import { sessionRoute } from './session-route.js';
import { e2eDecrypt, deriveE2EKey } from './crypto.js';
import { identityPubKey } from './crypto.js';
import { saveTermBuffer, clearTermBuffer } from './terminal.js';
import { checkForNotification, setNotification, clearNotification } from './notify.js';
import { showReconnectBanner, hideReconnectBanner } from './dashboard.js';
import { renderSidebar } from './render.js';
import { loadHome } from './data.js';
import { showHome } from './nav.js';
import { wingDisplayName, formatSessionTitle, b64urlToBytes, bytesToB64url, bytesToB64 } from './helpers.js';
import { saveTunnelAuthTokens, sendTunnelRequest } from './tunnel.js';
import { handlePreview, setPreviewSession, discardPreview } from './preview.js';
import { initWebRTC, completeMigration, cleanupPeer, cleanupSession, sendViaDC } from './webrtc.js';
import { safePreviewURL } from './security.js';
import { refreshSessionFilesButton, hideSessionFiles } from './session-files.js';
import { resumeAckMatches } from './pty-resume.js';
import { terminalControlFailure, terminalControlOptions, ptyAttachRequest, currentTerminalResize, renderTerminalControlNotice } from './terminal-attachment.js';
import { acknowledgeSessionCompletions } from './session-completion.js';
import { browserLocalStorage } from './storage-scope.js';

var PTY_PING_INTERVAL_MS = 5000;
var PTY_LIVENESS_TIMEOUT_MS = 12000;

function clearTerminalControl() {
    S.ptyControllerId = null;
    S.ptyInputBlocked = false;
    renderTerminalControlNotice(document.getElementById('terminal-control-notice'), null);
}

function handleTerminalControlError(ws, sessionId, wingId, message) {
    var failure = terminalControlFailure(message);
    if (!failure || !sessionId || !wingId || !S.currentUser || S.currentUser.release_channel !== 'preview') return false;
    S.ptyControllerId = null;
    S.ptyInputBlocked = true;
    S.ptyReconnecting = false;
    if (S._resizeDispose) { S._resizeDispose.dispose(); S._resizeDispose = null; }
    cleanupSession(sessionId, wingId);
    hideReconnectBanner();
    hideReplayOverlay();
    DOM.ptyStatus.textContent = failure.kind === 'busy' ? 'input in use' : 'control changed';
    var wing = S.wingsData.find(function(item) { return item.wing_id === wingId; });
    renderTerminalControlNotice(document.getElementById('terminal-control-notice'), failure, wing, S.currentUser, function(mode) {
        if (ws !== S.ptyWs || wingId !== S.ptyWingId) return;
        var currentWing = S.wingsData.find(function(item) { return item.wing_id === wingId; });
        var options = terminalControlOptions(S.currentUser, currentWing, mode);
        if (!options) return;
        detachPTY();
        attachPTY(sessionId, 1, wingId, undefined, options);
    });
    return true;
}

function setSessionActions(active) {
    DOM.terminalCopyBtn.style.display = active ? '' : 'none';
    if (!active) DOM.terminalCopyBtn.disabled = true;
    // Leaving a terminal must not leave its end-session control in the header.
    if (!active) {
        DOM.sessionCloseBtn.style.display = 'none';
        delete DOM.sessionCloseBtn.dataset.confirm;
        DOM.sessionCloseBtn.textContent = 'x';
    }
    refreshSessionFilesButton();
    if (!active) hideSessionFiles();
}

function showBrowserOpenToast(url, sessionId) {
    var existing = document.getElementById('browser-open-toast');
    if (existing) existing.remove();

    var toast = document.createElement('div');
    toast.id = 'browser-open-toast';
    toast.className = 'browser-open-toast';
    var label = document.createElement('span');
    label.className = 'browser-open-text';
    label.textContent = 'session wants to open: ';
    toast.appendChild(label);

    var safeURL = safePreviewURL(url);
    if (safeURL) {
        var anchor = document.createElement('a');
        anchor.href = safeURL;
        anchor.target = '_blank';
        anchor.rel = 'noopener noreferrer';
        anchor.textContent = url;
        toast.appendChild(anchor);
    } else {
        var blocked = document.createElement('span');
        blocked.className = 'browser-open-blocked';
        blocked.textContent = url + ' (unsupported URL scheme)';
        toast.appendChild(blocked);
    }

    var dismiss = document.createElement('button');
    dismiss.className = 'browser-open-dismiss';
    dismiss.textContent = '\u00d7';
    toast.appendChild(dismiss);
    document.body.appendChild(toast);

    var dismissTimer = setTimeout(function() { toast.remove(); }, 15000);

    toast.querySelector('.browser-open-dismiss').addEventListener('click', function() {
        clearTimeout(dismissTimer);
        toast.remove();
    });

    var anchor = toast.querySelector('a');
    if (anchor) {
        anchor.addEventListener('click', function() {
            clearTimeout(dismissTimer);
            toast.remove();
        });
    }
}

function sessionTitle(agent, wingId, session) {
    var wing = S.wingsData.find(function(w) { return w.wing_id === wingId; });
    var name = wing ? wingDisplayName(wing) : '';
    return formatSessionTitle(session, agent, name);
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
            rpId: msg.rp_id || location.hostname,
            userVerification: 'required',
            timeout: 60000
        }
    };
    if (allowCredentials.length > 0) opts.publicKey.allowCredentials = allowCredentials;

    try {
        var credential = await navigator.credentials.get(opts);
        S.ptyWs.send(JSON.stringify({
            type: 'passkey.response',
            session_id: msg.session_id,
            viewer_id: msg.viewer_id || '',
            credential_id: bytesToB64url(new Uint8Array(credential.rawId)),
            authenticator_data: bytesToB64(new Uint8Array(credential.response.authenticatorData)),
            client_data_json: bytesToB64(new Uint8Array(credential.response.clientDataJSON)),
            signature: bytesToB64(new Uint8Array(credential.response.signature))
        }));
    } catch (e) {
        showPasskeyOverlay();
    }
}

function setupPTYHandlers(ws, reattach, requestedSessionId) {
    var connectedWingId = S.ptyWingId;
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
                    setNotification(S.ptySessionId, S.ptyWingId);
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
                if (ws !== S.ptyWs) return;
                hideReplayOverlay();
                S.term.focus();
                replayDone = true;
                var queued = pendingOutput;
                pendingOutput = [];
                queued.forEach(processOutput);
            });
        }).catch(function () { if (ws === S.ptyWs) { replayDone = true; hideReplayOverlay(); } });
    }

    ws.onmessage = function (e) {
        if (ws !== S.ptyWs) return;
        var msg = JSON.parse(e.data);
        switch (msg.type) {
            case 'pty.pong':
                lastPong = Date.now();
                return;

            case 'relay.restart':
                if (S.ptySessionId) {
                    var sid = S.ptySessionId;
                    S.ptyReconnecting = true;
                    DOM.ptyStatus.textContent = 'reconnecting...';
                    showReconnectBanner();
                    setTimeout(function () {
                        if (terminalReferenceMatches(S, sid, connectedWingId, ws)) ptyReconnectAttach(sid, 0, connectedWingId);
                    }, 1000);
                }
                return;

            case 'wing.offline':
                if (S.ptySessionId) {
                    var sid = S.ptySessionId;
                    S.ptyReconnecting = true;
                    DOM.ptyStatus.textContent = 'wing restarting...';
                    showReconnectBanner('wing restarting...');
                    setTimeout(function () {
                        if (terminalReferenceMatches(S, sid, connectedWingId, ws)) ptyReconnectAttach(sid, 0, connectedWingId);
                    }, 1000);
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
                var requestedResumeId = S.pendingResumeSessionId;
                if (!reattach && !resumeAckMatches(requestedResumeId, msg)) {
                    S.pendingResumeSessionId = null;
                    setPreviewSession(null);
                    DOM.headerTitle.textContent = 'resume unavailable';
                    DOM.ptyStatus.textContent = 'resume was not confirmed';
                    S.term.writeln('\r\n\x1b[31;1mCould not resume that provider session.\x1b[0m');
                    S.term.writeln('\x1b[2mThe newly started session is being stopped.\x1b[0m');
                    S.ptyWs = null;
                    if (msg.session_id && S.ptyWingId) {
                        sendTunnelRequest(S.ptyWingId, { type: 'pty.kill', session_id: msg.session_id })
                            .catch(function() {
                                DOM.ptyStatus.textContent = 'resume failed; could not stop new session';
                            })
                            .finally(function() { try { ws.close(); } catch (e) {} });
                    } else {
                        try { ws.close(); } catch (e) {}
                    }
                    return;
                }
                S.pendingResumeSessionId = null;
                S.ptySessionId = msg.session_id;
                clearTerminalControl();
                S.ptyControllerId = typeof msg.controller_id === 'string' ? msg.controller_id : null;
                setPreviewSession(msg.session_id, S.ptyWingId);
                var activeSession = findSessionResource(S.sessionsData, msg.session_id, S.ptyWingId);
                if (document.visibilityState === 'visible') acknowledgeSessionCompletions(browserLocalStorage(), S.currentUser && S.currentUser.id, [activeSession || { id: msg.session_id, wing_id: S.ptyWingId }]);
                if (S.spectating) {
                    var who = activeSession && activeSession.email ? activeSession.email : '';
                    DOM.headerTitle.textContent = 'watching' + (who ? ' ' + who : '') + ' · ' + (msg.agent || '?');
                } else {
                    DOM.headerTitle.textContent = sessionTitle(msg.agent, S.ptyWingId, activeSession);
                }
                DOM.sessionCloseBtn.style.display = '';
                setSessionActions(true);
                hidePasskeyOverlay();
                if (msg.auth_token && S.ptyWingId) {
                    S.tunnelAuthTokens[S.ptyWingId] = msg.auth_token;
                    saveTunnelAuthTokens();
                }
                if (!reattach) {
                    history.pushState({ view: 'terminal', sessionId: msg.session_id, wingId: S.ptyWingId }, '', sessionRoute(msg.session_id, S.ptyWingId));
                }

                if (msg.public_key) {
                    deriveE2EKey(msg.public_key, S.ptyWingId).then(function (key) {
                        if (ws !== S.ptyWs) return;
                        S.e2eKey = key;
                        keyReady = true;
                        DOM.ptyStatus.textContent = key ? '\uD83D\uDD12' : '';
                        if (reattach) { flushReplay(); } else { pendingOutput.forEach(processOutput); pendingOutput = []; }
                    }).catch(function (err) {
                        if (ws !== S.ptyWs) return;
                        DOM.ptyStatus.textContent = err && err.message ? err.message : 'wing identity verification failed';
                        ws.close();
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

                // Dispose previous resize listener to prevent accumulation across sessions
                if (S._resizeDispose) { S._resizeDispose.dispose(); S._resizeDispose = null; }
                var resizeBinding = { sessionId: msg.session_id, wingId: connectedWingId, socket: ws, controllerId: S.ptyControllerId };
                S._resizeDispose = S.term.onResize(function (size) {
                    var msg = currentTerminalResize(resizeBinding, S, size);
                    if (!msg) return;
                    // P2P: try DataChannel first
                    if (sendViaDC(resizeBinding.sessionId, msg, resizeBinding.wingId)) return;
                    sendTunnelRequest(resizeBinding.wingId, msg).catch(function() {});
                });
                S.fitAddon.fit();
                // Always send resize on session load — fitAddon.fit() only triggers
                // onResize when dimensions change, but the remote PTY may have stale
                // dimensions from a different machine/window.
                var initialResize = currentTerminalResize(resizeBinding, S, { cols: S.term.cols, rows: S.term.rows });
                if (initialResize) sendTunnelRequest(resizeBinding.wingId, initialResize).catch(function() {});

                // P2P: check if wing supports P2P and initiate WebRTC
                if (S.ptyWingId && !reattach) {
                    var wingP2P = S.wingsData.find(function(w) { return w.wing_id === S.ptyWingId; });
                    if (wingP2P && wingP2P.p2p) {
                        var p2pOnly = wingP2P.connection_mode === 'p2p_only';
                        initWebRTC(S.ptyWingId, S.ptySessionId, p2pOnly);
                    }
                }
                break;

            case 'pty.migrated':
                if (msg.session_id === S.ptySessionId && S.ptyWingId) {
                    completeMigration(S.ptyWingId, msg.session_id);
                }
                break;

            case 'pty.fallback':
                if (msg.session_id === S.ptySessionId) {
                    cleanupSession(msg.session_id, S.ptyWingId);
                    console.log('[P2P] session ' + msg.session_id + ' FALLBACK — input+output back on relay WS');
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
                discardPreview(msg.session_id, S.ptyWingId);
                DOM.headerTitle.textContent = '';
                DOM.sessionCloseBtn.style.display = 'none';
                if (msg.session_id) clearTermBuffer(msg.session_id, S.ptyWingId);
                clearNotification(msg.session_id, S.ptyWingId);
                clearTerminalControl();
                S.ptySessionId = null;
                S.e2eKey = null;
                setSessionActions(false);

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
                // The wing already exited; remove local state without sending a
                // redundant pty.kill back to it.
                if (msg.session_id) window._deleteSession(msg.session_id, true, S.ptyWingId);
                renderSidebar();
                loadHome();
                break;

            case 'bandwidth.exceeded':
                clearTerminalControl();
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
                setSessionActions(false);

                renderSidebar();
                loadHome();
                break;

            case 'pty.browser_open':
                showBrowserOpenToast(msg.url, msg.session_id);
                break;

            case 'pty.preview':
                if (msg.session_id !== S.ptySessionId) break;
                e2eDecrypt(msg.data).then(function(bytes) {
                    if (!terminalReferenceMatches(S, msg.session_id, connectedWingId, ws)) return;
                    var preview = JSON.parse(new TextDecoder().decode(bytes));
                    handlePreview(preview);
                }).catch(function(err) {
                    console.error('preview decrypt error:', err);
                });
                break;

            case 'error':
                if (msg.session_id && requestedSessionId && msg.session_id !== requestedSessionId) break;
                if (msg.controller_id && S.ptyControllerId && msg.controller_id !== S.ptyControllerId) break;
                if (handleTerminalControlError(ws, msg.session_id || requestedSessionId || S.ptySessionId, connectedWingId, msg.message)) break;
                DOM.ptyStatus.textContent = msg.message;
                break;
        }
    };

    function reconnectAfterLoss(delay) {
        var sid = S.ptySessionId;
        S.ptyReconnecting = true;
        DOM.ptyStatus.textContent = 'reconnecting...';
        showReconnectBanner();
        setTimeout(function () {
            if (terminalReferenceMatches(S, sid, connectedWingId, ws)) ptyReconnectAttach(sid, 0, connectedWingId);
        }, delay);
    }

    // A link can die without a close event, and the browser would wait on it
    // forever. Ping the relay; once it has answered, silence means reconnect.
    var lastPong = 0;
    var liveness = setInterval(function () {
        if (ws !== S.ptyWs || ws.readyState > WebSocket.OPEN) { clearInterval(liveness); return; }
        if (ws.readyState !== WebSocket.OPEN) return;
        if (lastPong && Date.now() - lastPong > PTY_LIVENESS_TIMEOUT_MS) {
            clearInterval(liveness);
            if (S.ptySessionId && !S.ptyReconnecting && !S.ptyInputBlocked) reconnectAfterLoss(0);
            ws.close();
            return;
        }
        ws.send(JSON.stringify({ type: 'pty.ping' }));
    }, PTY_PING_INTERVAL_MS);

    ws.onclose = function () {
        clearInterval(liveness);
        if (ws !== S.ptyWs) return;
        if (S.ptyInputBlocked) return;
        if (S.ptyBandwidthExceeded) {
            S.ptyBandwidthExceeded = false;
            return;
        }
        if (S.ptySessionId && !S.ptyReconnecting) {
            reconnectAfterLoss(1000);
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

export function connectPTY(agent, cwd, wingId, resumeSessionId) {
    if (resumeSessionId) {
        var resumeWing = S.wingsData.find(function(wing) { return wing.wing_id === wingId; });
        var canResume = resumeWing && Array.isArray(resumeWing.capabilities) &&
            resumeWing.capabilities.indexOf('session.provider_resume.v1') !== -1;
        if (!canResume) {
            DOM.headerTitle.textContent = 'resume unavailable';
            DOM.ptyStatus.textContent = 'update this wing to resume provider sessions';
            return false;
        }
    }
    detachPTY();
    setPreviewSession(null);
    S.ptyBandwidthExceeded = false;
    S.pendingResumeSessionId = resumeSessionId || null;

    S.term.reset();

    S.ptyWingId = wingId || (onlineWings()[0] || {}).wing_id || null;

    var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    var url = proto + '//' + location.host + '/ws/pty';
    if (S.ptyWingId) url += '?wing_id=' + encodeURIComponent(S.ptyWingId);

    DOM.headerTitle.textContent = 'connecting...';
    DOM.ptyStatus.textContent = '';

    S.e2eKey = null;

    var connectedWingId = S.ptyWingId;
    var socket = new WebSocket(url);
    S.ptyWs = socket;
    socket.onopen = function () {
        if (socket !== S.ptyWs || connectedWingId !== S.ptyWingId) return;
        DOM.headerTitle.textContent = 'starting ' + agent + '...';
        var startMsg = {
            type: 'pty.start',
            agent: agent,
            cols: S.term.cols,
            rows: S.term.rows,
            public_key: identityPubKey,
        };
        if (cwd) startMsg.cwd = cwd;
        if (connectedWingId) startMsg.wing_id = connectedWingId;
        if (resumeSessionId) startMsg.resume_session_id = resumeSessionId;
        if (connectedWingId && S.tunnelAuthTokens[connectedWingId]) startMsg.auth_token = S.tunnelAuthTokens[connectedWingId];
        socket.send(JSON.stringify(startMsg));
    };

    setupPTYHandlers(S.ptyWs, false);
    return true;
}

var attachRequestId = 0;

export function attachPTY(sessionId, _retries, wingId, requestId, options) {
    options = options || {};
    var sess = findSessionResource(S.sessionsData, sessionId, wingId);
    if (!sess && S.sessionsData.filter(function(s) { return s.id === sessionId; }).length > 1) {
        DOM.ptyStatus.textContent = 'Choose the execution wing for this session';
        return false;
    }
    if (requestId === undefined) {
        requestId = ++attachRequestId;
        if (S.term) S.term.reset();
    }
    if (requestId !== attachRequestId) return false;
    clearTerminalControl();
    S.ptyReconnecting = false;
    setPreviewSession(sessionId, wingId || (sess && sess.wing_id));

    // Deep link before data loaded — wait for session or a spectate-enabled wing
    if (!sess && !_retries) {
        showReplayOverlay();
        DOM.headerTitle.textContent = 'connecting...';
        var attempts = 0;
        var timer = setInterval(function() {
            if (requestId !== attachRequestId) { clearInterval(timer); return; }
            var s = findSessionResource(S.sessionsData, sessionId, wingId);
            var hasSpectateWing = !wingId && S.wingsData.some(function(w) { return w.online !== false && w.spectate; });
            attempts++;
            if (s || hasSpectateWing || attempts > 20) {
                clearInterval(timer);
                attachPTY(sessionId, attempts, wingId, requestId, options);
            }
        }, 500);
        return;
    }

    var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    var url = proto + '//' + location.host + '/ws/pty';

    showReplayOverlay();
    clearNotification(sessionId, wingId || (sess && sess.wing_id));

    if (S.ptyWs) { try { S.ptyWs.close(); } catch(e) {} }

    S.ptyWingId = wingId || (sess ? sess.wing_id : null);

    // Auto-spectate: if this is another user's session and the wing has spectate enabled
    var isOtherUser = sess && sess.user_id && S.currentUser && sess.user_id !== S.currentUser.id;
    var wing = S.wingsData.find(function(w) { return w.wing_id === S.ptyWingId; });
    if (options.spectate && (!wing || !wing.spectate)) {
        DOM.ptyStatus.textContent = 'Observation is not enabled for this wing';
        return false;
    }
    var explicitTakeover = !!(options.takeover && S.currentUser && S.currentUser.release_channel === 'preview');
    S.spectating = !!(((isOtherUser && !explicitTakeover) || options.spectate) && wing && wing.spectate);

    // Session not in our list (spectator deep link, path ACLs, etc.) —
    // find a spectate-enabled wing and attach directly. The wing will
    // accept if it has the session running.
    if (!sess && !S.spectating && !wingId) {
        var spectateWing = S.wingsData.find(function(w) { return w.online !== false && w.spectate; });
        if (spectateWing) {
            S.ptyWingId = spectateWing.wing_id;
            S.spectating = true;
        }
    }

    var watchLabel = 'watching';
    if (S.spectating && sess && sess.email) watchLabel = 'watching ' + sess.email;
    DOM.headerTitle.textContent = S.spectating ? watchLabel : (sess ? sessionTitle(sess.agent || '?', sess.wing_id, sess) : 'reconnecting...');
    DOM.ptyStatus.textContent = '';

    if (S.ptyWingId) url += '?wing_id=' + encodeURIComponent(S.ptyWingId);
    else if (sessionId) url += '?session_id=' + encodeURIComponent(sessionId);

    var connectedWingId = S.ptyWingId;
    var socket = new WebSocket(url);
    S.ptyWs = socket;
    socket.onopen = function () {
        if (socket !== S.ptyWs || connectedWingId !== S.ptyWingId) return;
        var msg = ptyAttachRequest(sessionId, connectedWingId, identityPubKey, {
            spectate: S.spectating,
            takeover: explicitTakeover,
            authToken: S.tunnelAuthTokens[connectedWingId],
            cols: S.term && S.term.cols, rows: S.term && S.term.rows
        });
        socket.send(JSON.stringify(msg));
    };

    setupPTYHandlers(S.ptyWs, true, sessionId);
}

export function detachPTY() {
    attachRequestId++;
    S.ptyReconnecting = false;
    if (S._resizeDispose) { S._resizeDispose.dispose(); S._resizeDispose = null; }
    if (S.ptySessionId) cleanupSession(S.ptySessionId, S.ptyWingId);
    if (S.ptyWingId) cleanupPeer(S.ptyWingId);
    if (S.ptyWs) {
        if (S.ptySessionId && S.ptyWs.readyState === WebSocket.OPEN) {
            var detach = { type: 'pty.detach', session_id: S.ptySessionId };
            if (S.ptyControllerId) detach.controller_id = S.ptyControllerId;
            S.ptyWs.send(JSON.stringify(detach));
        }
        S.ptyWs.close();
        S.ptyWs = null;
    }
    S.ptySessionId = null;
    S.ptyWingId = null;
    S.e2eKey = null;
    S.spectating = false;
    S.pendingResumeSessionId = null;
    clearTerminalControl();
    setPreviewSession(null);
    setSessionActions(false);
}

export function disconnectPTY() {
    attachRequestId++;
    S.ptyReconnecting = false;
    if (S._resizeDispose) { S._resizeDispose.dispose(); S._resizeDispose = null; }
    if (S.ptySessionId) {
        cleanupSession(S.ptySessionId, S.ptyWingId);
        discardPreview(S.ptySessionId, S.ptyWingId);
    }
    if (S.ptyWingId) cleanupPeer(S.ptyWingId);
    var killFinished = Promise.resolve();
    if (S.ptyWingId && S.ptySessionId) {
        killFinished = sendTunnelRequest(S.ptyWingId, { type: 'pty.kill', session_id: S.ptySessionId })
            .catch(function() {});
    }
    if (S.ptyWs) { S.ptyWs.close(); S.ptyWs = null; }
    S.ptySessionId = null;
    S.ptyWingId = null;
    S.e2eKey = null;
    S.spectating = false;
    S.pendingResumeSessionId = null;
    clearTerminalControl();

    DOM.ptyStatus.textContent = '';
    DOM.headerTitle.textContent = '';
    DOM.sessionCloseBtn.style.display = 'none';
    setSessionActions(false);
    return killFinished;
}

var MAX_RECONNECT_ATTEMPTS = 10;

function ptyReconnectAttach(sessionId, attempt, wingId) {
    if (!S.ptyReconnecting || S.ptySessionId !== sessionId || !wingId || S.ptyWingId !== wingId) return;
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
    url += '?wing_id=' + encodeURIComponent(wingId);

    if (S.ptyWs) { try { S.ptyWs.close(); } catch(e) {} }
    clearTerminalControl();

    var socket = new WebSocket(url);
    S.ptyWs = socket;
    socket.onopen = function () {
        if (!terminalReferenceMatches(S, sessionId, wingId, socket)) return;
        // Reconnect requests never repeat a prior explicit takeover.
        var msg = ptyAttachRequest(sessionId, wingId, identityPubKey, {
            spectate: S.spectating, authToken: S.tunnelAuthTokens[wingId],
            cols: S.term && S.term.cols, rows: S.term && S.term.rows
        });
        socket.send(JSON.stringify(msg));
    };

    setupPTYHandlers(S.ptyWs, true, sessionId);

    var innerWs = S.ptyWs;
    innerWs.onclose = function () {
        if (innerWs !== S.ptyWs) return;
        if (S.ptyInputBlocked) return;
        // A socket that already recovered starts a fresh cycle when it drops again;
        // ptyReconnectAttach ignores calls once the previous cycle has finished.
        var retrying = S.ptyReconnecting;
        S.ptyReconnecting = true;
        var next = retrying ? attempt + 1 : 0;
        var delay = retrying ? Math.min(1000 * Math.pow(2, attempt), 30000) : 1000;
        setTimeout(function () {
            if (terminalReferenceMatches(S, sessionId, wingId, innerWs)) ptyReconnectAttach(sessionId, next, wingId);
        }, delay);
    };

    var origMsg = innerWs.onmessage;
    innerWs.onmessage = function (e) {
        if (innerWs !== S.ptyWs) return;
        var msg = JSON.parse(e.data);
        if (msg.type === 'pty.started' && msg.session_id === sessionId) {
            S.ptyReconnecting = false;
            hideReconnectBanner();
        }
        if (msg.type === 'error') {
            if (terminalControlFailure(msg.message) && S.currentUser && S.currentUser.release_channel === 'preview') {
                origMsg.call(innerWs, e);
                return;
            }
            // Don't give up — close WS so onclose fires and retries
            if (innerWs) { try { innerWs.close(); } catch(ex) {} }
            return;
        }
        origMsg.call(innerWs, e);
    };
}

export function retryReconnect() {
    if (!S.ptySessionId) return;
    S.ptyReconnecting = true;
    ptyReconnectAttach(S.ptySessionId, 0, S.ptyWingId);
}
