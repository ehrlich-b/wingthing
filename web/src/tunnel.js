import { x25519 } from '@noble/curves/ed25519.js';
import { b64ToBytes, bytesToB64, b64urlToBytes, bytesToB64url } from './helpers.js';
import { S } from './state.js';
import { identityKey, identityPubKey } from './crypto.js';

async function deriveE2ETunnelKey(wingPublicKeyB64) {
    if (S.tunnelKeys[wingPublicKeyB64]) return S.tunnelKeys[wingPublicKeyB64];
    if (!identityKey.priv) return null;
    var wingPubBytes = b64ToBytes(wingPublicKeyB64);
    var shared = x25519.getSharedSecret(identityKey.priv, wingPubBytes);
    var salt = new Uint8Array(32);
    var keyMaterial = await crypto.subtle.importKey('raw', shared, 'HKDF', false, ['deriveKey']);
    var enc = new TextEncoder();
    var key = await crypto.subtle.deriveKey(
        { name: 'HKDF', hash: 'SHA-256', salt: salt, info: enc.encode('wt-tunnel') },
        keyMaterial,
        { name: 'AES-GCM', length: 256 },
        false,
        ['encrypt', 'decrypt']
    );
    S.tunnelKeys[wingPublicKeyB64] = key;
    return key;
}

async function tunnelEncrypt(key, plaintext) {
    var enc = new TextEncoder();
    var iv = crypto.getRandomValues(new Uint8Array(12));
    var ciphertext = await crypto.subtle.encrypt({ name: 'AES-GCM', iv: iv }, key, enc.encode(plaintext));
    var result = new Uint8Array(iv.length + ciphertext.byteLength);
    result.set(iv);
    result.set(new Uint8Array(ciphertext), iv.length);
    return bytesToB64(result);
}

async function tunnelDecrypt(key, encoded) {
    var data = b64ToBytes(encoded);
    var iv = data.slice(0, 12);
    var ciphertext = data.slice(12);
    var plaintext = await crypto.subtle.decrypt({ name: 'AES-GCM', iv: iv }, key, ciphertext);
    return new TextDecoder().decode(plaintext);
}

function ensureTunnelWs(wingId, _retry) {
    _retry = _retry || 0;
    var ws = S.tunnelWsMap[wingId];
    if (ws && ws.readyState === WebSocket.OPEN) return Promise.resolve();
    if (ws && ws.readyState === WebSocket.CONNECTING) {
        return new Promise(function(resolve) {
            var orig = ws.onopen;
            ws.onopen = function() { if (orig) orig(); resolve(); };
        });
    }
    return new Promise(function(resolve, reject) {
        var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
        ws = new WebSocket(proto + '//' + location.host + '/ws/pty?wing_id=' + encodeURIComponent(wingId));
        S.tunnelWsMap[wingId] = ws;
        ws.onopen = resolve;
        ws.onerror = function() {
            if (S.tunnelWsMap[wingId] === ws) delete S.tunnelWsMap[wingId];
            if (_retry < 2) {
                setTimeout(function() {
                    ensureTunnelWs(wingId, _retry + 1).then(resolve).catch(reject);
                }, 1000 * (_retry + 1));
            } else {
                reject(new Error('tunnel ws failed'));
            }
        };
        ws.onclose = function() {
            if (S.tunnelWsMap[wingId] === ws) delete S.tunnelWsMap[wingId];
        };
        ws.onmessage = function(e) {
            var msg = JSON.parse(e.data);
            if (msg.type === 'tunnel.res') {
                var p = S.tunnelPending[msg.request_id];
                if (p) {
                    delete S.tunnelPending[msg.request_id];
                    p.resolve(msg);
                }
            } else if (msg.type === 'tunnel.stream') {
                var p = S.tunnelPending[msg.request_id];
                if (p && p.onStream) {
                    p.onStream(msg);
                    if (msg.done) {
                        delete S.tunnelPending[msg.request_id];
                        p.resolve(msg);
                    }
                }
            }
        };
    });
}

export async function sendTunnelRequest(wingId, innerMsg) {
    var wing = S.wingsData.find(function(w) { return w.wing_id === wingId; });
    if (!wing || !wing.public_key) throw new Error('wing not found or no public key');

    var key = await deriveE2ETunnelKey(wing.public_key);
    if (!key) throw new Error('could not derive tunnel key');

    var token = S.tunnelAuthTokens[wingId];
    if (token) innerMsg.auth_token = token;

    var requestId = crypto.randomUUID();
    var payload = await tunnelEncrypt(key, JSON.stringify(innerMsg));

    await ensureTunnelWs(wingId);

    return new Promise(function(resolve, reject) {
        S.tunnelPending[requestId] = { resolve: resolve, reject: reject };
        S.tunnelWsMap[wingId].send(JSON.stringify({
            type: 'tunnel.req',
            wing_id: wingId,
            request_id: requestId,
            sender_pub: identityPubKey,
            payload: payload
        }));
        setTimeout(function() {
            if (S.tunnelPending[requestId]) {
                delete S.tunnelPending[requestId];
                reject(new Error('tunnel request timeout'));
            }
        }, 30000);
    }).then(async function(msg) {
        var decrypted = await tunnelDecrypt(key, msg.payload);
        var result = JSON.parse(decrypted);

        if (result.error === 'passkey_required' && result.challenge) {
            var authToken = await handleTunnelPasskey(wingId, wing.public_key, result.challenge);
            if (authToken) {
                S.tunnelAuthTokens[wingId] = authToken;
                innerMsg.auth_token = authToken;
                return sendTunnelRequest(wingId, innerMsg);
            }
            throw new Error('passkey authentication failed');
        }

        if (result.error) throw new Error(result.error);
        return result;
    });
}

export async function sendTunnelStream(wingId, innerMsg, onChunk) {
    var wing = S.wingsData.find(function(w) { return w.wing_id === wingId; });
    if (!wing || !wing.public_key) throw new Error('wing not found or no public key');

    var key = await deriveE2ETunnelKey(wing.public_key);
    if (!key) throw new Error('could not derive tunnel key');

    var token = S.tunnelAuthTokens[wingId];
    if (token) innerMsg.auth_token = token;

    var requestId = crypto.randomUUID();
    var payload = await tunnelEncrypt(key, JSON.stringify(innerMsg));

    await ensureTunnelWs(wingId);

    return new Promise(function(resolve, reject) {
        S.tunnelPending[requestId] = {
            resolve: resolve,
            reject: reject,
            onStream: async function(msg) {
                try {
                    var decrypted = await tunnelDecrypt(key, msg.payload);
                    var chunk = JSON.parse(decrypted);
                    onChunk(chunk);
                } catch (e) { console.error('tunnel stream decrypt error:', e); }
            }
        };
        S.tunnelWsMap[wingId].send(JSON.stringify({
            type: 'tunnel.req',
            wing_id: wingId,
            request_id: requestId,
            sender_pub: identityPubKey,
            payload: payload
        }));
        setTimeout(function() {
            if (S.tunnelPending[requestId]) {
                delete S.tunnelPending[requestId];
                reject(new Error('tunnel stream timeout'));
            }
        }, 120000);
    });
}

async function handleTunnelPasskey(wingId, wingPubKey, challenge) {
    try {
        var credential = await navigator.credentials.get({
            publicKey: {
                challenge: b64urlToBytes(challenge),
                rpId: location.hostname,
                userVerification: 'preferred',
                timeout: 60000
            }
        });

        var key = await deriveE2ETunnelKey(wingPubKey);
        var requestId = crypto.randomUUID();
        var innerMsg = {
            type: 'passkey.auth',
            credential_id: bytesToB64url(new Uint8Array(credential.rawId)),
            authenticator_data: bytesToB64(new Uint8Array(credential.response.authenticatorData)),
            client_data_json: bytesToB64(new Uint8Array(credential.response.clientDataJSON)),
            signature: bytesToB64(new Uint8Array(credential.response.signature))
        };
        var payload = await tunnelEncrypt(key, JSON.stringify(innerMsg));

        await ensureTunnelWs(wingId);

        return new Promise(function(resolve, reject) {
            S.tunnelPending[requestId] = {
                resolve: async function(msg) {
                    try {
                        var decrypted = await tunnelDecrypt(key, msg.payload);
                        var result = JSON.parse(decrypted);
                        resolve(result.auth_token || null);
                    } catch (e) { resolve(null); }
                },
                reject: function() { resolve(null); }
            };
            S.tunnelWsMap[wingId].send(JSON.stringify({
                type: 'tunnel.req',
                wing_id: wingId,
                request_id: requestId,
                sender_pub: identityPubKey,
                payload: payload
            }));
            setTimeout(function() {
                if (S.tunnelPending[requestId]) {
                    delete S.tunnelPending[requestId];
                    resolve(null);
                }
            }, 60000);
        });
    } catch (e) {
        console.error('passkey auth failed:', e);
        return null;
    }
}
