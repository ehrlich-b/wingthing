// Synthetic encrypted wing peer for the production app. No external socket or
// provider account is used. The browser driver owns the durable prompt ledger.
import { x25519 } from '@noble/curves/ed25519.js';
import { hkdf } from '@noble/hashes/hkdf.js';
import { sha256 } from '@noble/hashes/sha2.js';
import { gcm } from '@noble/ciphers/aes.js';

const bytes = text => Uint8Array.from(atob(text), c => c.charCodeAt(0));
const base64 = data => btoa(Array.from(data, c => String.fromCharCode(c)).join(''));
const privateKey = new Uint8Array(32).fill(43);
const publicKey = base64(x25519.getPublicKey(privateKey));
const sockets = [];
class FixtureSocket {
    static CONNECTING = 0; static OPEN = 1; static CLOSING = 2; static CLOSED = 3;
    constructor(url) {
        this.url = url; this.readyState = 0; sockets.push(this);
        setTimeout(() => { this.readyState = 1; this.onopen?.({}); }, 0);
    }
    async send(value) {
        const outer = JSON.parse(value);
        if (outer.type !== 'tunnel.req') throw Error('Unexpected socket action: ' + outer.type);
        const key = hkdf(sha256, x25519.getSharedSecret(privateKey, bytes(outer.sender_pub)), new Uint8Array(32), new TextEncoder().encode('wt-tunnel'), 32);
        const encrypted = bytes(outer.payload);
        const inner = JSON.parse(new TextDecoder().decode(gcm(key, encrypted.slice(0, 12)).decrypt(encrypted.slice(12))));
        const result = await window.sendFixtureRequest({ wingId: outer.wing_id, ...inner });
        const iv = crypto.getRandomValues(new Uint8Array(12));
        const cipher = gcm(key, iv).encrypt(new TextEncoder().encode(JSON.stringify(result)));
        const payload = new Uint8Array(iv.length + cipher.length); payload.set(iv); payload.set(cipher, iv.length);
        this.onmessage?.({ data: JSON.stringify({ type: 'tunnel.res', request_id: outer.request_id, payload: base64(payload) }) });
    }
    close() { this.readyState = 3; this.onclose?.({}); }
}
if (typeof window !== 'undefined') {
window.WebSocket = FixtureSocket;
window.sendFixture = {
    publicKey,
    offline() {
        for (const socket of sockets.filter(s => s.readyState === 1)) socket.onmessage?.({ data: JSON.stringify({ type: 'wing.offline', wing_id: 'machine-a' }) });
    },
};

}
