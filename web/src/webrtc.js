import { S } from './state.js';
import { sendTunnelRequest } from './tunnel.js';
import { e2eDecrypt } from './crypto.js';
import { saveTermBuffer } from './terminal.js';
import { checkForNotification, setNotification } from './notify.js';

// Per-wing peer connections and per-session data channels
var peers = {};      // wingId -> RTCPeerConnection
var dataChannels = {}; // sessionId -> RTCDataChannel
var dcBuffers = {};    // sessionId -> [] (buffered messages before migration confirmed)

// Whether each session is actively using DC for I/O
// Exported so pty.js and terminal.js can check
export var dcActive = {};

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

/**
 * Initiate a WebRTC connection to a wing for a given session.
 * Sends a webrtc.offer via the encrypted tunnel and sets up the DataChannel.
 * If p2pOnly is true and connection fails, writes an error to the terminal.
 */
export async function initWebRTC(wingId, sessionId, p2pOnly) {
    console.log('[P2P] wing ' + wingId.slice(0, 8) + ' supports p2p, initiating WebRTC for session ' + sessionId);

    function p2pOnlyError(reason) {
        if (!p2pOnly) return;
        console.log('[P2P] p2p_only mode — connection failed: ' + reason);
        if (S.term) {
            S.term.writeln('\r\n\x1b[31;1m--- P2P connection failed ---\x1b[0m');
            S.term.writeln('\x1b[2mThis wing requires a direct P2P connection (p2p_only mode).\x1b[0m');
            S.term.writeln('\x1b[2m' + reason + '\x1b[0m');
            S.term.writeln('');
            S.term.writeln('\x1b[33mEnsure you are on the same network as the wing, or configure STUN/TURN servers.\x1b[0m');
        }
    }

    try {
        // Look up ICE servers from wing info cache
        var iceServers = [];
        var wingData = S.wingsData.find(function(w) { return w.wing_id === wingId; });
        if (wingData && wingData.ice_servers) {
            iceServers = wingData.ice_servers;
        }

        var config = { iceServers: iceServers };
        console.log('[P2P] RTCPeerConnection created (host-only ICE, ' + iceServers.length + ' STUN servers)');

        var pc = new RTCPeerConnection(config);
        peers[wingId] = pc;

        // Create data channel with session-specific label
        var dc = pc.createDataChannel('pty:' + sessionId);
        dataChannels[sessionId] = dc;
        dcBuffers[sessionId] = [];

        dc.onopen = function() {
            console.log('[P2P] data channel \'pty:' + sessionId + '\' state: open');
            // Send pty.migrate via relay WS to tell wing to swap output
            if (S.ptyWs && S.ptyWs.readyState === WebSocket.OPEN) {
                console.log('[P2P] sending pty.migrate via relay WS for session ' + sessionId);
                S.ptyWs.send(JSON.stringify({
                    type: 'pty.migrate',
                    session_id: sessionId
                }));
            }
        };

        dc.onclose = function() {
            console.log('[P2P] data channel \'pty:' + sessionId + '\' closed — falling back to relay WS');
            delete dataChannels[sessionId];
            delete dcBuffers[sessionId];
            if (dcActive[sessionId]) {
                delete dcActive[sessionId];
                console.log('[P2P] session ' + sessionId + ' FALLBACK — input+output back on relay WS');
            }
        };

        dc.onerror = function(e) {
            console.log('[P2P] data channel \'pty:' + sessionId + '\' error:', e);
        };

        // Handle incoming messages on the DC (output from wing)
        dc.onmessage = function(e) {
            if (!dcActive[sessionId]) {
                // Buffer until migration confirmed
                dcBuffers[sessionId] = dcBuffers[sessionId] || [];
                dcBuffers[sessionId].push(e.data);
                return;
            }
            handleDCMessage(sessionId, e.data);
        };

        pc.onconnectionstatechange = function() {
            console.log('[P2P] RTCPeerConnection state: ' + pc.connectionState);
            if (pc.connectionState === 'failed') {
                console.log('[P2P] RTCPeerConnection failed (state: failed) — staying on relay');
                p2pOnlyError('WebRTC connection failed — no route to wing.');
                cleanupPeer(wingId);
            }
        };

        pc.onicecandidate = function(e) {
            if (e.candidate) {
                console.log('[P2P] ICE candidate: ' + e.candidate.type + ' ' + (e.candidate.address || '') + ':' + (e.candidate.port || '') + ' ' + (e.candidate.protocol || ''));
            }
        };

        // Wait for ICE gathering to complete before sending offer
        var offer = await pc.createOffer();
        var gatherComplete = new Promise(function(resolve) {
            pc.onicegatheringstatechange = function() {
                if (pc.iceGatheringState === 'complete') {
                    var candidates = 0;
                    // Count candidates from local description
                    if (pc.localDescription && pc.localDescription.sdp) {
                        candidates = (pc.localDescription.sdp.match(/a=candidate/g) || []).length;
                    }
                    console.log('[P2P] ICE gathering complete, ' + candidates + ' candidates');
                    if (candidates === 0) {
                        console.log('[P2P] ICE gathering complete, 0 candidates — no route to wing, staying on relay');
                    }
                    resolve();
                }
            };
        });
        await pc.setLocalDescription(offer);
        await gatherComplete;

        if (!pc.localDescription || !pc.localDescription.sdp) {
            console.log('[P2P] no local description after ICE gathering');
            p2pOnlyError('ICE gathering produced no local description.');
            cleanupPeer(wingId);
            return;
        }

        // Send offer via encrypted tunnel
        var sdp = pc.localDescription.sdp;
        console.log('[P2P] sending webrtc.offer via tunnel (sdp: ' + sdp.length + ' bytes)');

        var resp = await sendTunnelRequest(wingId, { type: 'webrtc.offer', sdp: sdp });
        if (resp.error) {
            console.log('[P2P] webrtc.offer REJECTED by wing: "' + resp.error + '"');
            p2pOnlyError('Wing rejected P2P offer: ' + resp.error);
            cleanupPeer(wingId);
            return;
        }

        if (!resp.sdp) {
            console.log('[P2P] webrtc.offer got no answer SDP');
            p2pOnlyError('Wing returned no answer SDP.');
            cleanupPeer(wingId);
            return;
        }

        console.log('[P2P] received answer (sdp: ' + resp.sdp.length + ' bytes), setting remote description');
        await pc.setRemoteDescription({ type: 'answer', sdp: resp.sdp });

    } catch (err) {
        console.log('[P2P] WebRTC init error:', err);
        p2pOnlyError('WebRTC initialization error: ' + err.message);
        cleanupPeer(wingId);
    }
}

/**
 * Called when pty.migrated is received — flush buffered DC messages and mark session active.
 */
export function completeMigration(wingId, sessionId) {
    dcActive[sessionId] = true;
    var buffered = dcBuffers[sessionId] || [];
    delete dcBuffers[sessionId];
    console.log('[P2P] received pty.migrated for session ' + sessionId);
    console.log('[P2P] flushing ' + buffered.length + ' buffered DC messages');
    for (var i = 0; i < buffered.length; i++) {
        handleDCMessage(sessionId, buffered[i]);
    }
    console.log('[P2P] session ' + sessionId + ' MIGRATED — input+output now on DataChannel');
}

/**
 * Process a message received on the DataChannel.
 * Messages are JSON-encoded, same format as relay WS messages.
 */
function handleDCMessage(sessionId, rawData) {
    try {
        var msg = JSON.parse(rawData);
        switch (msg.type) {
            case 'pty.output':
                if (msg.session_id && S.ptySessionId && msg.session_id !== S.ptySessionId) return;
                processP2POutput(msg.data, !!msg.compressed);
                break;
            case 'pty.exited':
                // Forward to WS handler by dispatching on the WS
                if (S.ptyWs) {
                    S.ptyWs.dispatchEvent(new MessageEvent('message', { data: rawData }));
                }
                break;
            case 'pty.preview':
                if (S.ptyWs) {
                    S.ptyWs.dispatchEvent(new MessageEvent('message', { data: rawData }));
                }
                break;
            default:
                // Forward any unrecognized message types to WS handler
                if (S.ptyWs) {
                    S.ptyWs.dispatchEvent(new MessageEvent('message', { data: rawData }));
                }
        }
    } catch (err) {
        console.error('[P2P] DC message parse error:', err);
    }
}

/**
 * Process encrypted PTY output received over P2P DataChannel.
 * Same decryption as relay path.
 */
function processP2POutput(dataStr, compressed) {
    e2eDecrypt(dataStr).then(function(bytes) {
        return compressed ? gunzip(bytes) : bytes;
    }).then(function(bytes) {
        if (!bytes) return;
        S.term.write(bytes);
        saveTermBuffer();
        try {
            var text = new TextDecoder().decode(bytes);
            if (checkForNotification(text)) {
                setNotification(S.ptySessionId);
            }
        } catch (ex) {}
    }).catch(function(err) {
        console.error('[P2P] decrypt error, dropping frame:', err);
    });
}

/**
 * Try to send a message via DataChannel. Returns true if sent, false if DC not available.
 */
export function sendViaDC(sessionId, msg) {
    if (!dcActive[sessionId]) return false;
    var dc = dataChannels[sessionId];
    if (!dc || dc.readyState !== 'open') {
        if (dc) console.log('[P2P] sendViaDC(' + sessionId + '): DC not open (state: ' + dc.readyState + '), falling back to WS');
        return false;
    }
    var data = typeof msg === 'string' ? msg : JSON.stringify(msg);
    try {
        dc.send(data);
        return true;
    } catch (err) {
        console.log('[P2P] sendViaDC error:', err);
        return false;
    }
}

/**
 * Clean up peer connection and all associated data channels for a wing.
 */
export function cleanupPeer(wingId) {
    var pc = peers[wingId];
    if (!pc) return;

    var dcCount = 0, pcCount = 1;
    // Clean up any data channels for this wing
    for (var sid in dataChannels) {
        delete dataChannels[sid];
        delete dcActive[sid];
        delete dcBuffers[sid];
        dcCount++;
    }

    pc.close();
    delete peers[wingId];
    console.log('[P2P] peer cleanup for wing ' + wingId.slice(0, 8) + ' (' + dcCount + ' DC, ' + pcCount + ' PC closed)');
}

/**
 * Clean up a specific session's DC (on detach/disconnect).
 */
export function cleanupSession(sessionId) {
    var dc = dataChannels[sessionId];
    if (dc) {
        dc.close();
        delete dataChannels[sessionId];
    }
    delete dcActive[sessionId];
    delete dcBuffers[sessionId];
}
