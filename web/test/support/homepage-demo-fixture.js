// Fictional, credential-free peer for the homepage recording. The production
// app renders normally; discovery and encrypted wing replies stay in memory.
import { x25519 } from '@noble/curves/ed25519.js';
import { hkdf } from '@noble/hashes/hkdf.js';
import { sha256 } from '@noble/hashes/sha2.js';
import { gcm } from '@noble/ciphers/aes.js';

const bytes = value => Uint8Array.from(atob(value), c => c.charCodeAt(0));
const base64 = value => btoa(Array.from(value, byte => String.fromCharCode(byte)).join(''));
const secret = new Uint8Array(32).fill(41);
const publicKey = base64(x25519.getPublicKey(secret));
const names = ['migrate-sessions-table-without-downtime', 'add-rate-limits-to-the-public-api', 'improve-keyboard-navigation-in-settings', 'document-the-backup-and-restore-workflow', 'reduce-startup-time-for-large-projects', 'check-accessibility-of-the-sign-in-page', 'add-regression-coverage-for-export', 'simplify-release-notes-for-the-next-version'];
const stamp = () => new Date().toISOString();
let count = 8, prompted = false;
const calls = [];
const wings = ['studio', 'laptop'].map(wing_id => ({ wing_id, public_key: publicKey, user_id: 'example-owner', owner: 'You', online: true }));
const events = [
    { sequence: 1, type: 'message', role: 'user', text: 'Migrate the sessions table without downtime. Check the existing rows before switching reads.', timestamp: stamp() },
    { sequence: 2, type: 'message', role: 'assistant', text: 'The new index is ready. All 18,204 rows are backfilled and verified. The migration tests pass. Ready to switch reads to the new table.', timestamp: stamp() },
    { sequence: 3, type: 'turn_completed', source: 'claude_hook', state: 'idle', timestamp: stamp() },
];

function lifecycle(id, after = 0, limit = 100) {
    const own = id === 'example-0' ? events : [{ sequence: 1, type: 'message', role: 'assistant', text: 'The changes are ready to review.', timestamp: stamp() }];
    const page = own.filter(event => event.sequence > after).slice(0, limit);
    return { session_id: id, provider_session_id: 'provider-' + id, agent: 'claude', status: 'idle', state: 'idle', state_source: 'claude_hook', state_cursor: own.at(-1).sequence,
        process_alive: true, ready: true, cursor: page.at(-1)?.sequence || after, head_cursor: own.at(-1).sequence, has_more: page.at(-1)?.sequence < own.at(-1).sequence, events: page };
}

function answer(wingId, inner) {
    calls.push({ wingId, ...inner });
    if (inner.type === 'wing.info') return { hostname: wingId === 'studio' ? 'Studio' : 'Laptop', platform: 'linux', agents: ['claude', 'codex'], projects: [{ name: 'sample-app', path: '/projects/sample-app' }] };
    if (inner.type === 'sessions.list') return { sessions: names.slice(0, count).flatMap((name, index) => (index % 2 === 0 ? 'studio' : 'laptop') !== wingId ? [] : [{ session_id: 'example-' + index, name, agent: 'claude', cwd: '/projects/sample-app', user_id: 'example-owner', lifecycle: lifecycle('example-' + index, 0, 1) }]) };
    if (inner.type === 'session.control') {
        if (inner.operation === 'terminal_read') return { ansi: 'sample-app\n\nMigration verified: 18,204 / 18,204 rows\nTests: 24 passed\nReady for the next instruction.' };
        if (inner.operation === 'session_read') return { session: inner.arguments.session, lifecycle: lifecycle(inner.arguments.session, inner.arguments.after_cursor, inner.arguments.limit) };
        if (inner.operation === 'session_prompt') {
            if (wingId !== 'studio' || inner.arguments.session !== 'example-0' || prompted) throw Error('Unexpected demo prompt target or duplicate');
            prompted = true;
            events.push({ sequence: 4, type: 'message', role: 'user', text: inner.arguments.input, timestamp: stamp() },
                { sequence: 5, type: 'message', role: 'assistant', text: 'Reads now use the new table. All migration tests pass. The rollback path is documented.', timestamp: stamp() },
                { sequence: 6, type: 'turn_completed', source: 'claude_hook', state: 'idle', timestamp: stamp() });
            return { receipt: { status: 'native_receipt_observed', native_receipt_observed: true } };
        }
    }
    throw Error('Unexpected demo request: ' + inner.type + ' ' + (inner.operation || ''));
}

class DemoSocket {
    static CONNECTING = 0; static OPEN = 1; static CLOSING = 2; static CLOSED = 3;
    constructor(url) {
        this.url = url; this.readyState = 0;
        setTimeout(() => { this.readyState = 1; this.onopen?.({}); }, 0);
    }
    send(value) {
        const outer = JSON.parse(value);
        if (outer.type !== 'tunnel.req') throw Error('Unexpected demo socket request');
        const key = hkdf(sha256, x25519.getSharedSecret(secret, bytes(outer.sender_pub)), new Uint8Array(32), new TextEncoder().encode('wt-tunnel'), 32);
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
if (typeof window !== 'undefined') {
    window.WebSocket = DemoSocket;
    window.homepageDemo = { wings, calls, setCount(value) { count = value; } };
}
