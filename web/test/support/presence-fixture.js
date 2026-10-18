// Local encrypted peers for the production browser, with explicit socket loss.
import { x25519 } from '@noble/curves/ed25519.js';
import { hkdf } from '@noble/hashes/hkdf.js';
import { sha256 } from '@noble/hashes/sha2.js';
import { gcm } from '@noble/ciphers/aes.js';

if (typeof window !== 'undefined' && window.top === window) {
const bytes = value => Uint8Array.from(atob(value), c => c.charCodeAt(0));
const base64 = value => btoa(Array.from(value, byte => String.fromCharCode(byte)).join(''));
const privateKey = new Uint8Array(32).fill(43);
const publicKey = base64(x25519.getPublicKey(privateKey));
const names = ['Design workstation for the autumn release', 'Research laptop with the external display', 'Build server for customer integration checks', 'Studio computer for documentation previews', 'Travel notebook for the field engineering team', 'Shared testing machine for accessibility checks', 'Development desktop for the reporting service', 'Release validation computer for the mobile app'];
const wings = Array.from({ length: window.presenceCount ?? 8 }, (_, i) => ({ wing_id: 'machine-' + i, public_key: publicKey, user_id: 'fixture-owner', owner: 'You', name: names[i] || 'Development computer ' + i, online: i < 2 }));
const calls = [], sockets = [], closedPeers = [];
const unreachable = new Set();
function lifecycle(id, after = 0) {
    return { session_id: id, agent: 'claude', status: 'working', state: 'working', state_source: 'claude_hook', process_alive: true, ready: false, cursor: 1, head_cursor: 1,
        events: after ? [] : [{ sequence: 1, type: 'message', role: 'assistant', text: 'Checking the release notes and preparing the next update.', timestamp: '2026-01-01T12:00:00Z' }] };
}
function answer(wingId, inner) {
    calls.push({ wingId, ...inner });
    if (unreachable.has(wingId)) return { error: 'unreachable' };
    const wing = wings.find(w => w.wing_id === wingId);
    if (!wing?.online) return { error: 'wing offline' };
    if (inner.type === 'sessions.inventory') return { version: 'wingthing.session-inventory.v1', wing_id: wingId, complete: true, sessions: [], sources: [] };
    if (inner.type === 'wing.info') return { hostname: wing.name, platform: 'linux', agents: ['claude'], projects: [] };
    if (inner.type === 'sessions.list') return { sessions: ['same-id', ...(wingId === 'machine-0' ? ['coordinator'] : [])].map(id => ({ session_id: id, name: id === 'coordinator' ? 'wing' : 'Release notes and integration readiness checks', agent: 'claude', cwd: '/fixture/release', user_id: 'fixture-owner', pinned: id === 'coordinator', role: id === 'coordinator' ? 'coordinator' : '', lifecycle: lifecycle(id) })) };
    if (inner.type === 'session.control' && inner.operation === 'session_read') return { session: inner.arguments.session, lifecycle: lifecycle(inner.arguments.session, inner.arguments.after_cursor) };
    if (inner.type === 'session.control' && inner.operation === 'terminal_read') return { ansi: 'Reviewing release notes…' };
    throw Error('Unexpected runtime operation: ' + JSON.stringify(inner));
}
class FixtureSocket {
    static CONNECTING = 0; static OPEN = 1; static CLOSING = 2; static CLOSED = 3;
    constructor(url) {
        this.url = url; this.readyState = 0; sockets.push(this);
        setTimeout(() => { this.readyState = 1; this.onopen?.({}); }, 0);
    }
    send(value) {
        const outer = JSON.parse(value);
        if (outer.type !== 'tunnel.req') throw Error('Unexpected socket mutation: ' + outer.type);
        const key = hkdf(sha256, x25519.getSharedSecret(privateKey, bytes(outer.sender_pub)), new Uint8Array(32), new TextEncoder().encode('wt-tunnel'), 32);
        const encrypted = bytes(outer.payload);
        const inner = JSON.parse(new TextDecoder().decode(gcm(key, encrypted.slice(0, 12)).decrypt(encrypted.slice(12))));
        const result = answer(outer.wing_id, inner);
        const iv = crypto.getRandomValues(new Uint8Array(12));
        const cipher = gcm(key, iv).encrypt(new TextEncoder().encode(JSON.stringify(result)));
        const payload = new Uint8Array(iv.length + cipher.length); payload.set(iv); payload.set(cipher, iv.length);
        setTimeout(() => this.onmessage?.({ data: JSON.stringify({ type: 'tunnel.res', request_id: outer.request_id, payload: base64(payload) }) }), 0);
    }
    close() { this.readyState = 3; this.onclose?.({}); }
}
function announce(wingId, type) {
    for (const socket of sockets.filter(s => s.url.endsWith('/ws/app') && s.readyState === 1)) socket.onmessage?.({ data: JSON.stringify({ type, wing_id: wingId, public_key: publicKey }) });
}
// A returning owner's inventory includes remembered offline computers.
localStorage.setItem('wt_cache_owner_v1', 'fixture-owner');
localStorage.setItem('wt_wings', JSON.stringify(wings.map(w => ({ ...w, hostname: w.name, online: undefined, last_seen: '2026-01-01T12:00:00Z' }))));
localStorage.setItem('wt_sessions', JSON.stringify(wings.flatMap(w => ['same-id', ...(w.wing_id === 'machine-0' ? ['coordinator'] : [])].map(id => ({ id, wing_id: w.wing_id, name: id === 'coordinator' ? 'wing' : 'Release notes and integration readiness checks', agent: 'claude', user_id: 'fixture-owner', pinned: id === 'coordinator', role: id === 'coordinator' ? 'coordinator' : '', lifecycle: lifecycle(id) })))));
window.WebSocket = FixtureSocket;
window.presenceFixture = {
    calls, wings, closedPeers,
    discovery: () => wings.filter(w => w.online),
    disconnect(wingId) {
        wings.find(w => w.wing_id === wingId).online = false;
        for (const socket of sockets.filter(s => new URL(s.url).searchParams.get('wing_id') === wingId && s.readyState === 1)) { closedPeers.push(wingId); socket.close(); }
        announce(wingId, 'wing.offline');
    },
    restore(wingId) { wings.find(w => w.wing_id === wingId).online = true; unreachable.delete(wingId); announce(wingId, 'wing.online'); },
    unreachable(wingId) { unreachable.add(wingId); announce(wingId, 'wing.config'); },
};

}
