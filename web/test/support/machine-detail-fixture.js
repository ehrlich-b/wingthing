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
const wings = Array.from({ length: 2 }, (_, i) => ({ wing_id: 'machine-' + i, public_key: publicKey, user_id: 'fixture-owner', owner: 'You', name: names[i] || 'Development computer ' + i, online: i === 0 }));
const sessionNames = ['Prepare the customer reporting migration and verify backward compatibility', 'Investigate the mobile notification delivery delay on slow connections', 'Review accessibility of the account settings and keyboard navigation', 'Implement incremental search for large historical session inventories', 'Validate encrypted reconnect after workstation sleep and network changes', 'Write onboarding guidance for the documentation publishing workflow', 'Audit sandbox permission boundaries for nested project directories', 'Finish release notes and integration checks for the autumn release'];
const count = window.machineSessionCount ?? 8;
const sessionsFor = wingId => sessionNames.slice(0, count).map((name, i) => ({ session_id: 'session-' + i, id: 'session-' + i, wing_id: wingId, name, agent: i % 2 ? 'codex' : 'claude', cwd: '/fixture/projects/customer-reporting-service', user_id: 'fixture-owner', swept: true, lifecycle: { ...lifecycle('session-' + i, 0, wingId), status: i % 3 ? 'idle' : 'working' } }));
const calls = [], sockets = [], closedPeers = [];
const unreachable = new Set();
const failures = window.presenceErrors || {};
if (window.presenceUnnamed) wings.push({ online: false });
function lifecycle(id, after = 0, wingId = 'machine-0') {
    return { session_id: id, provider_session_id: 'provider-' + wingId + '-' + id, agent: 'claude', status: wingId === 'machine-1' ? 'idle' : 'working', state: wingId === 'machine-1' ? 'idle' : 'working', state_source: 'claude_hook', process_alive: true, ready: wingId === 'machine-1', cursor: 1, head_cursor: 1,
        events: after ? [] : [{ sequence: 1, type: 'message', role: 'assistant', text: 'Checking the release notes and preparing the next update.', timestamp: '2026-01-01T12:00:00Z' }] };
}
function answer(wingId, inner) {
    calls.push({ wingId, ...inner });
    if (failures[wingId]) return { error: failures[wingId] };
    if (unreachable.has(wingId)) return { error: 'unreachable' };
    const wing = wings.find(w => w.wing_id === wingId);
    if (!wing?.online) return { error: 'wing offline' };
    if (inner.type === 'sessions.inventory') return { version: 'wingthing.session-inventory.v1', wing_id: wingId, complete: true, sessions: [], sources: [] };
    if (inner.type === 'wing.info') return { hostname: wing.name, platform: 'linux', agents: ['claude', 'codex'], version: '0.148.0', projects: [{name: 'Customer reporting', path: '/fixture/projects/customer-reporting-service'}] };
    if (inner.type === 'sessions.list') return { sessions: sessionsFor(wingId) };
    if (inner.type === 'sessions.history') return { sessions: [] };
    if (inner.type === 'allow.list') return { allowed: [{ user_id: 'fixture-owner', email: 'owner@example.test' }] };
    if (inner.type === 'paths.list') return { paths: ['/fixture/projects/customer-reporting-service'] };
    if (inner.type === 'dir.list') return { entries: [] };
    if (inner.type === 'session.control' && inner.operation === 'session_read') return { session: inner.arguments.session, lifecycle: lifecycle(inner.arguments.session, inner.arguments.after_cursor, wingId) };
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
localStorage.setItem('wt_wings', JSON.stringify(wings.map(w => ({ ...w, hostname: w.name, platform: 'linux', agents: ['claude', 'codex'], version: '0.148.0', projects: [{name: 'Customer reporting', path: '/fixture/projects/customer-reporting-service'}], online: undefined, last_seen: '2026-01-01T12:00:00Z' }))));
localStorage.setItem('wt_sessions', JSON.stringify(wings.flatMap(w => sessionsFor(w.wing_id))));
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
