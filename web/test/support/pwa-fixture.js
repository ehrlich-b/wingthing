// Credential-free wing fixture. The production build boots normally and uses
// its real encrypted tunnel; only authenticated discovery and the wing peer
// are supplied locally. No provider or production state is contacted.
import { x25519 } from '@noble/curves/ed25519.js';
import { hkdf } from '@noble/hashes/hkdf.js';
import { sha256 } from '@noble/hashes/sha2.js';
import { gcm } from '@noble/ciphers/aes.js';

const bytes = value => Uint8Array.from(atob(value), c => c.charCodeAt(0));
const base64 = value => btoa(Array.from(value, byte => String.fromCharCode(byte)).join(''));
const privateKey = new Uint8Array(32).fill(37);
const publicKey = base64(x25519.getPublicKey(privateKey));
const now = Date.now();
const stamp = offset => new Date(now + offset).toISOString();
const calls = [];
const events = [
    { sequence: 1, type: 'session_ready', source: 'claude_hook', state: 'idle', timestamp: stamp(-120000) },
    { sequence: 2, type: 'message', role: 'user', text: 'Make the daily check-in ready to review.', timestamp: stamp(-115000) },
    { sequence: 3, type: 'prompt_submitted', source: 'claude_hook', state: 'working', timestamp: stamp(-110000) },
    { sequence: 4, type: 'input_requested', source: 'claude_hook', state: 'needs_input', reason: 'Review the release notes before publishing.', timestamp: stamp(-90000) },
    { sequence: 5, type: 'session_report', source: 'wing_report', timestamp: stamp(-5000), report: {
        goal: 'Keep the next release moving, with one clear daily check-in.',
        update: 'The test suite passed. I collected the changes and open decisions in a short release brief.',
        next_action: 'Review the release brief. I’ll check the remaining sessions and report back.',
        presence: 'awake', last_wake: stamp(-110000), next_wake: stamp(3600000),
        artifacts: [{ label: 'Release brief.md', path: 'reports/release-brief.md' }],
        links: [{ label: 'Product documentation', url: 'https://example.com/docs' }],
    } },
    { sequence: 6, type: 'prompt_submitted', source: 'claude_hook', state: 'working', timestamp: stamp(-2000) },
    { sequence: 7, type: 'message', role: 'assistant', text: 'I’m checking the remaining sessions. The release brief is ready whenever you are.', timestamp: stamp(-1000) },
];
let status = 'working', processAlive = true, ready = false;
let failReads = false, offline = false;
const wings = ['machine-a', 'machine-b'].map(wing_id => ({ wing_id, public_key: publicKey, user_id: 'fixture-owner', owner: 'You', online: true }));

function lifecycle(wingId, after = 0, limit = 100) {
    const ownEvents = wingId === 'machine-a' ? events : [{ sequence: 1, type: 'message', role: 'assistant', text: 'This is the separate session on the second machine.', timestamp: stamp(-1000) }];
    const reportEvent = events.filter(event => event.report).at(-1);
    const page = ownEvents.filter(event => event.sequence > after).slice(0, limit);
    return { session_id: 'same-id', agent: 'claude', provider_session_id: 'provider-' + wingId, status: wingId === 'machine-a' ? status : 'idle',
        state: wingId === 'machine-a' ? status === 'blocked' ? 'needs_input' : status === 'exited' ? 'stopped' : status : 'idle', state_source: 'claude_hook',
        state_cursor: wingId === 'machine-a' ? ownEvents.filter(event => event.state).at(-1)?.sequence || 1 : 1,
        process_alive: wingId === 'machine-a' ? processAlive : true, ready: wingId === 'machine-a' ? ready : true,
        cursor: page.at(-1)?.sequence || after, head_cursor: ownEvents.at(-1).sequence, has_more: page.at(-1)?.sequence < ownEvents.at(-1).sequence,
        events: page, ...(wingId === 'machine-a' ? { report: reportEvent.report, reported_at: reportEvent.timestamp, report_cursor: reportEvent.sequence } : {}) };
}

function answer(wingId, inner) {
    calls.push({ wingId, ...inner });
    if (inner.type === 'wing.info') return { hostname: wingId === 'machine-a' ? 'Workstation' : 'Studio', platform: wingId === 'machine-a' ? 'linux' : 'darwin', agents: ['claude', 'codex'], projects: [{ name: 'release', path: '/fixture/release' }], capabilities: ['session.file_download.v1'] };
    if (inner.type === 'sessions.inventory' || inner.type === 'sessions.search') {
        const route = (id, kind = 'direct') => ({wing_id:id,kind,healthy:true,observed_at:new Date().toISOString(),machine_label:id==='machine-a'?'Workstation':'Studio'});
        const own = { origin_wing_id:wingId, session_id:'same-id',name:wingId==='machine-a'?'wing':'Research',agent:'claude',machine_label:wingId==='machine-a'?'Workstation':'Studio',live:true,status:'idle',pinned:wingId==='machine-a',role:wingId==='machine-a'?'coordinator':'', routes:[route(wingId)] };
        const rows = [own];
        if (wingId==='machine-a' && inner.arguments.include_remotes) rows.push({...own,origin_wing_id:'machine-b',name:'Research',machine_label:'Studio',pinned:false,role:'',routes:[route('machine-a','via')]});
        return { version:'wingthing.session-inventory.v1',wing_id:wingId,sessions:rows,complete:true,sources:[],generated_at:new Date().toISOString() };
    }
    if (inner.type === 'sessions.list') return { sessions: [{ session_id: 'same-id', name: wingId === 'machine-a' ? 'wing' : 'Research', agent: 'claude', cwd: '/fixture/release', user_id: 'fixture-owner', pinned: wingId === 'machine-a', role: wingId === 'machine-a' ? 'coordinator' : '', lifecycle: lifecycle(wingId, 0, 1) }] };
    if (inner.type === 'session.control') {
        if (inner.operation === 'session_action') {
            const args = inner.arguments;
            if (args.operation !== 'session_read') throw Error('Unexpected fixture mutation');
            return {origin_wing_id:args.origin_wing_id,session_id:args.session_id,route:{wing_id:wingId,kind:wingId===args.origin_wing_id?'direct':'via'},result:{session:args.session_id,lifecycle:lifecycle(args.origin_wing_id,args.arguments.after_cursor,args.arguments.limit)}};
        }
        if (inner.operation !== 'session_read') throw Error('Unexpected mutation: ' + inner.operation);
        if (failReads) return { error: 'Fixture wing could not read the coordinator' };
        if (inner.arguments.session !== 'same-id') throw Error('Unexpected session target');
        return { session: 'same-id', lifecycle: lifecycle(wingId, inner.arguments.after_cursor, inner.arguments.limit) };
    }
    throw Error('Unexpected fixture request: ' + inner.type);
}

const sockets = [];
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
if (typeof window !== 'undefined') {
window.WebSocket = FixtureSocket;
window.coordinatorFixture = {
    calls, publicKey, wings,
    discovery() { return wings.map(wing => ({ ...wing, online: wing.wing_id === 'machine-a' ? !offline : true })); },
    transition(next, report) {
        status = next; processAlive = next !== 'exited'; ready = next === 'idle';
        events.push({ sequence: events.length + 1, type: next === 'blocked' ? 'input_requested' : next === 'exited' ? 'session_exit' : next === 'idle' ? 'turn_completed' : 'prompt_submitted', source: 'claude_hook', state: next === 'blocked' ? 'needs_input' : next === 'exited' ? 'stopped' : next, timestamp: new Date().toISOString(), reason: next === 'blocked' ? 'Confirm the proposed release.' : '' });
        if (report) events.push({ sequence: events.length + 1, type: 'session_report', source: 'wing_report', timestamp: new Date().toISOString(), report: { ...events.filter(event => event.report).at(-1).report, ...report } });
    },
    fail(value) { failReads = value; },
    offline(value) {
        offline = value;
        for (const socket of sockets.filter(socket => socket.url.endsWith('/ws/app') && socket.readyState === 1)) socket.onmessage?.({ data: JSON.stringify({ type: value ? 'wing.offline' : 'wing.online', wing_id: 'machine-a', public_key: publicKey }) });
    },
};

}
