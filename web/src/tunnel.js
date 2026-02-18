import { x25519 } from '@noble/curves/ed25519.js';
import { hkdf } from '@noble/hashes/hkdf.js';
import { sha256 } from '@noble/hashes/sha2.js';
import { gcm } from '@noble/ciphers/aes.js';
import { b64ToBytes, bytesToB64, b64urlToBytes, bytesToB64url } from './helpers.js';
import { S } from './state.js';
import { identityKey, identityPubKey } from './crypto.js';

// --- Crypto (pure JS — works without crypto.subtle / secure context) ---

function deriveE2ETunnelKey(wingPublicKeyB64) {
    if (S.tunnelKeys[wingPublicKeyB64]) return S.tunnelKeys[wingPublicKeyB64];
    if (!identityKey.priv) return null;
    var wingPubBytes = b64ToBytes(wingPublicKeyB64);
    var shared = x25519.getSharedSecret(identityKey.priv, wingPubBytes);
    var salt = new Uint8Array(32);
    var enc = new TextEncoder();
    var key = hkdf(sha256, shared, salt, enc.encode('wt-tunnel'), 32);
    S.tunnelKeys[wingPublicKeyB64] = key;
    return key;
}

function tunnelEncrypt(key, plaintext) {
    var enc = new TextEncoder();
    var iv = crypto.getRandomValues(new Uint8Array(12));
    var cipher = gcm(key, iv);
    var ciphertext = cipher.encrypt(enc.encode(plaintext));
    var result = new Uint8Array(iv.length + ciphertext.length);
    result.set(iv);
    result.set(ciphertext, iv.length);
    return bytesToB64(result);
}

function tunnelDecrypt(key, encoded) {
    var data = b64ToBytes(encoded);
    var iv = data.slice(0, 12);
    var ciphertext = data.slice(12);
    var cipher = gcm(key, iv);
    var plaintext = cipher.decrypt(ciphertext);
    return new TextDecoder().decode(plaintext);
}

// crypto.randomUUID requires secure context in some browsers
function randomUUID() {
    if (typeof crypto.randomUUID === 'function') {
        try { return crypto.randomUUID(); } catch (e) {}
    }
    var b = crypto.getRandomValues(new Uint8Array(16));
    b[6] = (b[6] & 0x0f) | 0x40;
    b[8] = (b[8] & 0x3f) | 0x80;
    var h = Array.from(b, function(v) { return v.toString(16).padStart(2, '0'); }).join('');
    return h.slice(0,8)+'-'+h.slice(8,12)+'-'+h.slice(12,16)+'-'+h.slice(16,20)+'-'+h.slice(20);
}

// --- Auth Token Persistence ---

var AUTH_TOKENS_KEY = 'wt_auth_tokens';

export function saveTunnelAuthTokens() {
    try { sessionStorage.setItem(AUTH_TOKENS_KEY, JSON.stringify(S.tunnelAuthTokens)); } catch (e) {}
}

export function loadTunnelAuthTokens() {
    try {
        var raw = sessionStorage.getItem(AUTH_TOKENS_KEY);
        // Migrate from localStorage if present
        if (!raw) {
            raw = localStorage.getItem(AUTH_TOKENS_KEY);
            if (raw) { localStorage.removeItem(AUTH_TOKENS_KEY); }
        }
        if (raw) {
            var tokens = JSON.parse(raw);
            for (var k in tokens) { S.tunnelAuthTokens[k] = tokens[k]; }
        }
    } catch (e) {}
}

// --- Ephemeral Connection Pool ---
//
// Opens a WS per wing on demand, batches concurrent requests over it,
// closes after idle. Limits total concurrent WS connections.

var pool = {
    maxOpen: 4,
    maxQueue: 8,
    idleMs: 3000,
    conns: {},       // wingId → { ws, pending: Map<rid, handler>, idleTimer }
    openCount: 0,
    waitQueue: [],   // [{ wingId, doSend: fn(conn), resolve, reject }]
    backoff: {},     // wingId → { until: timestamp, delay: ms }
};

function poolWsUrl(wingId) {
    var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    return proto + '//' + location.host + '/ws/relay?wing_id=' + encodeURIComponent(wingId);
}

// Get or open a connection for wingId. Returns promise that resolves with conn.
function acquireConn(wingId) {
    var conn = pool.conns[wingId];
    if (conn && conn.ws.readyState === WebSocket.OPEN) {
        // Reuse — cancel idle timer
        if (conn.idleTimer) { clearTimeout(conn.idleTimer); conn.idleTimer = null; }
        return Promise.resolve(conn);
    }
    if (conn && conn.ws.readyState === WebSocket.CONNECTING) {
        // Wait for it to open
        return new Promise(function(resolve, reject) {
            conn._waiters = conn._waiters || [];
            conn._waiters.push({ resolve: resolve, reject: reject });
        });
    }
    // Per-wing backoff after failure
    var bo = pool.backoff[wingId];
    if (bo && Date.now() < bo.until) {
        return Promise.reject(new Error('tunnel ws backoff'));
    }
    // Need a new connection
    if (pool.openCount < pool.maxOpen) {
        return openConn(wingId);
    }
    // At capacity — queue (with cap)
    if (pool.waitQueue.length >= pool.maxQueue) {
        return Promise.reject(new Error('tunnel queue full'));
    }
    return new Promise(function(resolve, reject) {
        pool.waitQueue.push({ wingId: wingId, resolve: resolve, reject: reject });
    });
}

function openConn(wingId) {
    pool.openCount++;
    var ws = new WebSocket(poolWsUrl(wingId));
    var conn = { ws: ws, pending: {}, idleTimer: null, _waiters: [] };
    pool.conns[wingId] = conn;

    return new Promise(function(resolve, reject) {
        ws.onopen = function() {
            // Connection succeeded — clear backoff
            delete pool.backoff[wingId];
            resolve(conn);
            // Wake anyone waiting for this wing
            var waiters = conn._waiters || [];
            conn._waiters = [];
            waiters.forEach(function(w) { w.resolve(conn); });
        };
        ws.onerror = function() {
            // Connection failed — set exponential backoff
            var prev = pool.backoff[wingId];
            var delay = prev ? Math.min(prev.delay * 2, 10000) : 1000;
            pool.backoff[wingId] = { until: Date.now() + delay, delay: delay };
            // Reject all waiters
            var waiters = conn._waiters || [];
            conn._waiters = [];
            var err = new Error('tunnel ws failed');
            waiters.forEach(function(w) { w.reject(err); });
            reject(err);
            cleanupConn(wingId, conn);
        };
        ws.onclose = function() {
            // Reject any still-pending requests on this conn
            var pendingIds = Object.keys(conn.pending);
            pendingIds.forEach(function(rid) {
                var h = conn.pending[rid];
                delete conn.pending[rid];
                if (h.reject) h.reject(new Error('tunnel ws closed'));
            });
            cleanupConn(wingId, conn);
        };
        ws.onmessage = function(e) {
            var msg = JSON.parse(e.data);
            if (msg.type === 'tunnel.res') {
                var h = conn.pending[msg.request_id];
                if (h) {
                    delete conn.pending[msg.request_id];
                    h.resolve(msg);
                    checkIdle(wingId, conn);
                }
            } else if (msg.type === 'tunnel.stream') {
                var h = conn.pending[msg.request_id];
                if (h && h.onStream) {
                    h.onStream(msg);
                    if (msg.done) {
                        delete conn.pending[msg.request_id];
                        h.resolve(msg);
                        checkIdle(wingId, conn);
                    }
                }
            }
        };
    });
}

function cleanupConn(wingId, conn) {
    if (pool.conns[wingId] === conn) {
        delete pool.conns[wingId];
        pool.openCount--;
        if (conn.idleTimer) { clearTimeout(conn.idleTimer); conn.idleTimer = null; }
    }
    drainQueue();
}

function checkIdle(wingId, conn) {
    if (Object.keys(conn.pending).length > 0) return;
    // No pending requests — start idle timer
    if (conn.idleTimer) clearTimeout(conn.idleTimer);
    conn.idleTimer = setTimeout(function() {
        if (pool.conns[wingId] === conn && Object.keys(conn.pending).length === 0) {
            try { conn.ws.close(); } catch(e) {}
            cleanupConn(wingId, conn);
        }
    }, pool.idleMs);
}

function drainQueue() {
    while (pool.waitQueue.length > 0 && pool.openCount < pool.maxOpen) {
        var next = pool.waitQueue.shift();
        // Check if a conn already exists for this wing (another queued item may have opened it)
        var existing = pool.conns[next.wingId];
        if (existing && existing.ws.readyState === WebSocket.OPEN) {
            if (existing.idleTimer) { clearTimeout(existing.idleTimer); existing.idleTimer = null; }
            next.resolve(existing);
        } else {
            openConn(next.wingId).then(next.resolve).catch(next.reject);
        }
    }
}

// Close all connections for a specific wing (called on wing.offline)
export function tunnelCloseWing(wingId) {
    var conn = pool.conns[wingId];
    if (conn) {
        try { conn.ws.close(); } catch(e) {}
        // cleanupConn will be called by onclose handler
    }
}

// --- Rate Limiter (token bucket: 5 req/sec, burst 5) ---

var _bucket = { tokens: 5, max: 5, rate: 5, last: Date.now() };

function acquireToken() {
    var now = Date.now();
    _bucket.tokens = Math.min(_bucket.max, _bucket.tokens + (now - _bucket.last) / 1000 * _bucket.rate);
    _bucket.last = now;
    if (_bucket.tokens >= 1) { _bucket.tokens--; return Promise.resolve(); }
    var waitMs = (1 - _bucket.tokens) / _bucket.rate * 1000;
    _bucket.tokens = 0;
    return new Promise(function(resolve) { setTimeout(resolve, waitMs); });
}

// --- Public API ---

export async function sendTunnelRequest(wingId, innerMsg, opts, _depth) {
    if ((_depth || 0) >= 3) throw new Error('passkey retry limit exceeded');
    await acquireToken();
    var wing = S.wingsData.find(function(w) { return w.wing_id === wingId; });
    if (!wing || !wing.public_key) throw new Error('wing not found or no public key');

    var key = deriveE2ETunnelKey(wing.public_key);
    if (!key) throw new Error('could not derive tunnel key');

    var token = S.tunnelAuthTokens[wingId];
    if (token) innerMsg.auth_token = token;

    var requestId = randomUUID();
    var payload = tunnelEncrypt(key, JSON.stringify(innerMsg));

    var conn = await acquireConn(wingId);

    return new Promise(function(resolve, reject) {
        conn.pending[requestId] = { resolve: resolve, reject: reject };
        conn.ws.send(JSON.stringify({
            type: 'tunnel.req',
            wing_id: wingId,
            request_id: requestId,
            sender_pub: identityPubKey,
            payload: payload
        }));
        setTimeout(function() {
            if (conn.pending[requestId]) {
                delete conn.pending[requestId];
                reject(new Error('tunnel request timeout'));
                checkIdle(wingId, conn);
            }
        }, 30000);
    }).then(async function(msg) {
        var decrypted = tunnelDecrypt(key, msg.payload);
        var result = JSON.parse(decrypted);

        if (result.error === 'passkey_required') {
            if (!(opts && opts.skipPasskey)) {
                try {
                    var authToken = await handleTunnelPasskey(wingId, wing.public_key);
                    if (authToken) {
                        S.tunnelAuthTokens[wingId] = authToken;
                        saveTunnelAuthTokens();
                        innerMsg.auth_token = authToken;
                        return sendTunnelRequest(wingId, innerMsg, opts, (_depth || 0) + 1);
                    }
                } catch (passkeyErr) {
                    if (passkeyErr.noPasskeys) {
                        var noKeyErr = new Error('no_passkeys_configured');
                        noKeyErr.noPasskeys = true;
                        throw noKeyErr;
                    }
                    // Other passkey errors fall through to passkey_required
                }
            }
            var err = new Error('passkey_required');
            err.metadata = result;
            throw err;
        }

        if (result.error) throw new Error(result.error);
        return result;
    });
}

export async function sendTunnelStream(wingId, innerMsg, onChunk) {
    var wing = S.wingsData.find(function(w) { return w.wing_id === wingId; });
    if (!wing || !wing.public_key) throw new Error('wing not found or no public key');

    var key = deriveE2ETunnelKey(wing.public_key);
    if (!key) throw new Error('could not derive tunnel key');

    var token = S.tunnelAuthTokens[wingId];
    if (token) innerMsg.auth_token = token;

    var requestId = randomUUID();
    var payload = tunnelEncrypt(key, JSON.stringify(innerMsg));

    var conn = await acquireConn(wingId);

    return new Promise(function(resolve, reject) {
        conn.pending[requestId] = {
            resolve: resolve,
            reject: reject,
            onStream: async function(msg) {
                try {
                    var decrypted = tunnelDecrypt(key, msg.payload);
                    var chunk = JSON.parse(decrypted);
                    onChunk(chunk);
                } catch (e) { console.error('tunnel stream decrypt error:', e); }
            }
        };
        conn.ws.send(JSON.stringify({
            type: 'tunnel.req',
            wing_id: wingId,
            request_id: requestId,
            sender_pub: identityPubKey,
            payload: payload
        }));
        setTimeout(function() {
            if (conn.pending[requestId]) {
                delete conn.pending[requestId];
                reject(new Error('tunnel stream timeout'));
                checkIdle(wingId, conn);
            }
        }, 120000);
    });
}

async function handleTunnelPasskey(wingId, wingPubKey) {
    try {
        // Fetch user's registered credential IDs so password managers (LastPass etc) trigger
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

        // No registered passkeys — throw distinct error so UI can show setup message
        if (allowCredentials.length === 0) {
            var noKeyErr = new Error('no_passkeys');
            noKeyErr.noPasskeys = true;
            throw noKeyErr;
        }

        var challenge = crypto.getRandomValues(new Uint8Array(32));
        var getOpts = {
            publicKey: {
                challenge: challenge,
                rpId: location.hostname,
                userVerification: 'preferred',
                timeout: 60000
            }
        };
        if (allowCredentials.length > 0) {
            getOpts.publicKey.allowCredentials = allowCredentials;
        }
        var credential = await navigator.credentials.get(getOpts);

        var key = deriveE2ETunnelKey(wingPubKey);
        var requestId = randomUUID();
        var innerMsg = {
            type: 'passkey.auth',
            credential_id: bytesToB64url(new Uint8Array(credential.rawId)),
            authenticator_data: bytesToB64(new Uint8Array(credential.response.authenticatorData)),
            client_data_json: bytesToB64(new Uint8Array(credential.response.clientDataJSON)),
            signature: bytesToB64(new Uint8Array(credential.response.signature))
        };
        var payload = tunnelEncrypt(key, JSON.stringify(innerMsg));

        var conn = await acquireConn(wingId);

        return new Promise(function(resolve, reject) {
            conn.pending[requestId] = {
                resolve: async function(msg) {
                    try {
                        var decrypted = tunnelDecrypt(key, msg.payload);
                        var result = JSON.parse(decrypted);
                        resolve(result.auth_token || null);
                    } catch (e) { resolve(null); }
                    checkIdle(wingId, conn);
                },
                reject: function() { resolve(null); checkIdle(wingId, conn); }
            };
            conn.ws.send(JSON.stringify({
                type: 'tunnel.req',
                wing_id: wingId,
                request_id: requestId,
                sender_pub: identityPubKey,
                payload: payload
            }));
            setTimeout(function() {
                if (conn.pending[requestId]) {
                    delete conn.pending[requestId];
                    resolve(null);
                    checkIdle(wingId, conn);
                }
            }, 60000);
        });
    } catch (e) {
        if (e.noPasskeys) throw e;
        console.error('passkey auth failed:', e);
        return null;
    }
}
