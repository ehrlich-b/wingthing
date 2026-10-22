// Realistic session inventory over local encrypted peers for the production browser.
import { x25519 } from '@noble/curves/ed25519.js';
import { hkdf } from '@noble/hashes/hkdf.js';
import { sha256 } from '@noble/hashes/sha2.js';
import { gcm } from '@noble/ciphers/aes.js';

if (typeof window !== 'undefined' && window.top === window) {
const bytes = value => Uint8Array.from(atob(value), c => c.charCodeAt(0));
const base64 = value => btoa(Array.from(value, byte => String.fromCharCode(byte)).join(''));
const privateKey = new Uint8Array(32).fill(43);
const publicKey = base64(x25519.getPublicKey(privateKey));
const scenario = window.sessionsScenario || 'sessions';
const names = ['Design workstation for the autumn release', 'Research laptop with the external display', 'Travel notebook for the field engineering team'];
const wings = names.map((name, i) => ({ wing_id: 'machine-' + i, public_key: publicKey, user_id: 'fixture-owner', owner: 'You', name, online: i < 2 }));
const tasks = ['Review account permission changes before release', 'Fix checkout redirects for returning customers', 'Approve the accessibility remediation proposal', 'Improve keyboard navigation in the project switcher', 'Add refund retry coverage for interrupted payments', 'Prepare release notes for the autumn desktop update', 'Investigate long-running integration test failures', 'Refine mobile typography for translated project names', 'Check upload progress with an unreliable connection', 'Document the new first-run setup for self-hosted teams', 'Simplify workspace selection for returning developers', 'Update regression coverage for notification preferences', 'Summarize customer feedback on deployment recovery', 'Verify export permissions for shared project reports', 'Improve error messages for the account invitation flow', 'Review the documentation screenshots for screen readers', 'Test background reconnect after network changes', 'Prepare the field engineering incident checklist', 'Audit signed download links and expiry handling', 'Review the dashboard performance trace', 'Validate command approval copy with long project names', 'Confirm the retained workspace export instructions', 'Check mobile terminal selection after reconnect'];
const inventory = tasks.map((name, i) => ({ id: 'task-' + i, name, wing_id: 'machine-' + (i < 11 ? 0 : i < 17 ? 1 : 2), agent: i % 2 ? 'codex' : 'claude', cwd: '/fixture/projects/autumn-customer-release', user_id: 'fixture-owner', status: i < 3 ? 'blocked' : i < 9 || i >= 17 ? 'working' : i % 2 ? 'completed' : 'idle' }));
inventory.unshift({ id: 'pinned-wing', name: 'wing', wing_id: 'machine-0', agent: 'claude', user_id: 'fixture-owner', pinned: true, role: 'coordinator', status: 'working' });
if (scenario === 'empty') inventory.length = 0;
if (scenario === 'one') inventory.splice(2);
const calls = [], sockets = [];
const unreachable = new Set();
function lifecycle(id, after = 0, wingId = 'machine-0') {
    const item = inventory.find(s => s.id === id && s.wing_id === wingId);
    const status = item?.status || 'idle';
    return { session_id: id, provider_session_id: 'provider-' + wingId + '-' + id, agent: item?.agent || 'claude', status, state: status === 'blocked' ? 'needs_input' : status, state_source: 'claude_hook', process_alive: true, ready: status === 'idle', reason: status === 'blocked' ? 'Review the proposed changes before continuing.' : '', cursor: 1, head_cursor: 1,
        events: after ? [] : [{ sequence: 1, type: 'message', role: 'assistant', text: item?.pinned ? 'The release brief is ready. Three decisions need your attention.' : status === 'blocked' ? 'Review the proposed changes before continuing.' : 'Checking release coverage and preparing the next update.', timestamp: '2026-10-21T12:00:00Z' }] };
}
const sessions = wingId => inventory.filter(s => s.wing_id === wingId).map(s => ({ ...s, session_id: s.id, lifecycle: lifecycle(s.id, 0, wingId) }));
function answer(wingId, inner) {
    calls.push({ wingId, ...inner });
    if (unreachable.has(wingId)) return { error: 'unreachable' };
    const wing = wings.find(w => w.wing_id === wingId);
    if (!wing?.online) return { error: 'wing offline' };
    if (inner.type === 'sessions.inventory') return { version: 'wingthing.session-inventory.v1', wing_id: wingId, complete: true, sessions: [], sources: [] };
    if (inner.type === 'wing.info') return { hostname: wing.name, platform: 'linux', agents: ['claude'], projects: [] };
    if (inner.type === 'sessions.list') return { sessions: sessions(wingId) };
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
localStorage.setItem('wt_wings', JSON.stringify(wings.map(w => ({ ...w, hostname: w.name, online: undefined, last_seen: '2026-10-21T12:00:00Z' }))));
localStorage.setItem('wt_sessions', JSON.stringify(wings.flatMap(w => sessions(w.wing_id))));
window.WebSocket = FixtureSocket;
window.sessionsFixture = {
    calls, wings,
    discovery: () => wings.filter(w => w.online),
    unreachable(wingId) { unreachable.add(wingId); announce(wingId, 'wing.config'); },
};

}
