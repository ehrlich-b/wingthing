import { x25519 } from '@noble/curves/ed25519.js';
import { hkdf } from '@noble/hashes/hkdf.js';
import { sha256 } from '@noble/hashes/sha2.js';
import { hmac } from '@noble/hashes/hmac.js';
import { gcm } from '@noble/ciphers/aes.js';
import { loadPake } from './pake/loader.js';
import wasmURL from './pake/pake.wasm?url';
import { emojis } from './pake/emoji.js';
import { signatureDER } from './browser-device.js';
const utf8 = new TextEncoder(), text = new TextDecoder();
export const toB64 = bytes => btoa(Array.from(bytes, b => String.fromCharCode(b)).join(''));
export const fromB64 = value => Uint8Array.from(atob(value), c => c.charCodeAt(0));
export const fingerprint = key => Array.from(sha256(fromB64(key)), b => b.toString(16).padStart(2, '0')).join('');
function encrypt(key, value) {
    const iv = crypto.getRandomValues(new Uint8Array(12));
    return toB64(Uint8Array.of(...iv, ...gcm(key, iv).encrypt(utf8.encode(JSON.stringify(value)))));
}
function decrypt(key, value) {
    const wire = fromB64(value);
    return JSON.parse(text.decode(gcm(key, wire.subarray(0, 12)).decrypt(wire.subarray(12))));
}
export async function parseInvitation(link) {
    if (link.length > 8192) throw new Error('invalid');
    const url = new URL(link.trim());
    if (url.protocol !== 'wingthing:' || url.hostname !== 'pair' || !url.hash.startsWith('#v1z.')) throw new Error('invalid');
    const packed = fromB64(url.hash.slice(5).replaceAll('-', '+').replaceAll('_', '/'));
    const reader = new Blob([packed]).stream().pipeThrough(new DecompressionStream('deflate-raw')).getReader();
    let size = 0, parts = [];
    try {
        for (;;) {
            const { value, done } = await reader.read();
            if (done) break;
            size += value.length;
            if (size > 16384) throw new Error('invalid');
            parts.push(value);
        }
    } finally { await reader.cancel(); }
    const bytes = new Uint8Array(size); let offset = 0;
    for (const part of parts) { bytes.set(part, offset); offset += part.length; }
    const a = JSON.parse(text.decode(bytes));
    if (!Array.isArray(a) || a.length !== 9 || a[0] !== 1 ||
        !a.slice(1, 4).every(v => typeof v === 'string' && v.length > 0 && v.length <= 100) ||
        fromB64(a[3]).length !== 32 || !a.slice(5, 8).every(v => typeof v === 'string' && v.length <= 2048) ||
        !a[6] || !a[7] || !Number.isSafeInteger(a[8])) throw new Error('invalid');
    const q = { wing_id: a[1], name: a[2], key: a[3], relay: a[5], id: a[6], secret: a[7], expires_at: a[8] * 1000 };
    if (Date.now() >= q.expires_at) throw new Error('expired');
    return q;
}
export class PhoneCarrier {
    constructor(wing, key) {
        this.wing = wing;
        const privateKey = x25519.utils.randomSecretKey();
        this.sender = toB64(x25519.getPublicKey(privateKey));
        this.shared = x25519.getSharedSecret(privateKey, fromB64(key));
        privateKey.fill(0);
        this.key = hkdf(sha256, this.shared, new Uint8Array(32), utf8.encode('wt-phone-carrier-v1'), 32);
        this.deviceKey = hkdf(sha256, this.shared, new Uint8Array(32), utf8.encode('wt-phone-v1'), 32);
        this.shared.fill(0); this.shared = null;
        this.pending = new Map();
    }
    async open() {
        const url = new URL('/ws/browser-phone', location.origin);
        url.protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
        url.searchParams.set('wing_id', this.wing);
        const socket = this.socket = new WebSocket(url);
        await new Promise((resolve, reject) => {
            const timer = setTimeout(() => { socket.close(); reject(new Error('unreachable')); }, 10000);
            socket.onopen = () => { clearTimeout(timer); resolve(); };
            socket.onerror = () => { clearTimeout(timer); reject(new Error('unreachable')); };
            socket.onclose = () => { clearTimeout(timer); reject(new Error('unreachable')); };
        });
        socket.onmessage = event => {
            let reply;
            try { reply = JSON.parse(event.data); } catch { this.close(); return; }
            const pending = this.pending.get(reply.request_id);
            if (!pending) return;
            this.pending.delete(reply.request_id); clearTimeout(pending.timer);
            try {
                const box = JSON.parse(reply.payload);
                if (box.error) throw new Error(box.error);
                const inner = decrypt(this.key, box.payload);
                if (inner.error) throw new Error(inner.error);
                pending.resolve(inner);
            } catch (error) { pending.reject(error); }
        };
        socket.onclose = socket.onerror = () => this.fail();
    }
    fail() {
        for (const p of this.pending.values()) { clearTimeout(p.timer); p.reject(new Error('unreachable')); }
        this.pending.clear();
    }
    async call(inner) {
        if (this.socket?.readyState !== WebSocket.OPEN) throw new Error('unreachable');
        const request_id = crypto.randomUUID();
        const payload = JSON.stringify({ op: 'phone.box', sender_pub: this.sender, payload: encrypt(this.key, inner) });
        return new Promise((resolve, reject) => {
            const timer = setTimeout(() => { this.pending.delete(request_id); reject(new Error('unreachable')); }, 12000);
            this.pending.set(request_id, { resolve, reject, timer });
            this.socket.send(JSON.stringify({ type: 'tunnel.req', purpose: 'phone', wing_id: this.wing, request_id, payload }));
        });
    }
    close() {
        this.fail(); this.socket?.close(); this.key?.fill(0); this.deviceKey?.fill(0);
    }
}
let pakePromise;
export class PairExchange {
    constructor(q, carrier) { this.q = q; this.carrier = carrier; }
    async start(publicKey, name) {
        pakePromise ||= fetch(wasmURL).then(r => { if (!r.ok) throw new Error('unreachable'); return r.arrayBuffer(); }).then(loadPake);
        const pake = await pakePromise;
        const ex = pake.start({ alice: true, secret: this.q.secret, wingIdentity: `wt-wing-v1:${this.q.wing_id}:${fingerprint(this.q.key)}:${this.q.id}` });
        this.q.secret = '';
        let shared;
        try {
            const alice = toB64(ex.message);
            const response = await this.carrier.call({ op: 'pair.start', id: this.q.id, message: alice });
            if (response.key !== this.q.key) throw new Error('mismatch');
            shared = ex.finish(fromB64(response.message));
            const transcript = `wt-pair-v1\n${this.q.wing_id}\n${this.q.key}\n${this.q.id}\n${alice}\n${response.message}`;
            this.key = hkdf(sha256, shared, sha256(utf8.encode(transcript)), utf8.encode('wt-pair-v1'), 32);
            this.emoji = Array.from(hmac(sha256, this.key, utf8.encode('wt-emoji-v1')).subarray(0, 4), b => emojis[b]).join(' ');
            const proof = await this.send('prove', { name, public_key: toB64(publicKey), scope: 'inventory' });
            if (proof.emoji !== this.emoji || proof.scope !== 'inventory') throw new Error('mismatch');
            return proof;
        } finally { ex.close(); shared?.fill(0); }
    }
    async send(op, fields = {}) {
        const r = await this.carrier.call({ op: 'pair.' + op, id: this.q.id, payload: encrypt(this.key, { op, id: this.q.id, ...fields }) });
        return decrypt(this.key, r.payload);
    }
    close() { this.key?.fill(0); this.q.secret = ''; }
}
export class InventoryConnection {
    constructor(record, carrier) { this.record = record; this.carrier = carrier; this.sequence = 0; }
    async auth() {
        const r = await this.carrier.call({ op: 'device.hello', sender_pub: this.carrier.sender });
        const hello = decrypt(this.carrier.deviceKey, r.payload);
        if (hello.wing_id !== this.record.wing_id || hello.key !== this.record.key || hello.connection_id !== r.connection_id) throw new Error('mismatch');
        this.id = hello.connection_id;
        const message = `wt-device-v1\n${this.record.wing_id}\n${this.id}\n${hello.challenge}\n${this.carrier.sender}`;
        const signature = signatureDER(await crypto.subtle.sign({ name: 'ECDSA', hash: 'SHA-256' }, this.record.signingKey, utf8.encode(message)));
        const response = await this.carrier.call({ op: 'device.auth', connection_id: this.id, payload: encrypt(this.carrier.deviceKey, { connection_id: this.id, device_id: this.record.device_id, credential: this.record.credential, signature: toB64(signature) }) });
        const ack = decrypt(this.carrier.deviceKey, response.payload);
        if (ack.connection_id !== this.id || ack.sequence !== 0 || ack.ok !== true) throw new Error('bad_proof');
    }
    async command(op, input = '') {
        const sequence = ++this.sequence;
        const r = await this.carrier.call({ op: 'device.command', connection_id: this.id, payload: encrypt(this.carrier.deviceKey, { connection_id: this.id, sequence, op, input }) });
        const reply = decrypt(this.carrier.deviceKey, r.payload);
        if (reply.connection_id !== this.id || reply.sequence !== sequence) throw new Error('replay');
        if (reply.error) throw new Error(reply.error);
        return reply.result;
    }
}
