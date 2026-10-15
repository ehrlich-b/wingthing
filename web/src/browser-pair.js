import './browser-pair.css';
import { loadDevice, listDevices, forgetDevice, forgetCandidate, saveDevice, activateDevice, prepareSigningKey } from './browser-device.js';
import { parseInvitation, fingerprint, PhoneCarrier, PairExchange, InventoryConnection } from './browser-pair-protocol.js';
import { emojiLabel } from './pake/emoji.js';
const escape = value => String(value ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const labels = {
    expired: ['Invitation expired', 'Run wt pair --show-link on the machine for a fresh invitation.'],
    mismatch: ['Identity mismatch', 'The fingerprint or labels did not match. Cancel in the terminal and run wt pair again.'],
    cancelled: ['Pairing cancelled', 'Pairing stopped in this browser. Run wt pair --show-link to try again.'],
    unreachable: ['Machine unreachable', 'Check that the machine is running and registered with this relay, then try again.'],
    wing_update_required: ['Machine needs a newer wingthing', 'Update wingthing on this machine and restart its wing, then run wt pair --show-link again. Browser pairing requires inventory-only access.'],
    denied: ['Pairing refused', 'This machine refused the requested access. Browser pairing requires inventory-only access. Run wt pair --show-link again.'],
    storage: ['Browser storage unavailable', 'Pairing stopped because site storage could not be verified. Enable site storage, then run wt pair --show-link again.'],
    revoked: ['Access ended', 'This browser’s access was revoked. Pair again to restore it.'],
    invalid: ['Invitation not recognized', 'Paste the full invitation from wt pair --show-link.'],
};
function errorState(error) {
    if (['bad_proof', 'bad_key', 'emoji_mismatch'].includes(error.message)) return 'mismatch';
    if (['used', 'cancelled'].includes(error.message)) return 'cancelled';
    return labels[error.message] ? error.message : 'unreachable';
}
export async function initBrowserPair() {
    const imported = location.hash.startsWith('#v1z.') ? 'wingthing://pair' + location.hash : '';
    history.replaceState(null, '', location.pathname + '#pair');
    document.title = 'Pair a machine · wingthing';
    document.body.className = 'browser-pair';
    document.body.innerHTML = '<header class="bp-header"><a href="./">wingthing</a></header><main class="bp-layout" data-state="paste"><div class="bp-title"><h1>Pair a machine</h1><span class="bp-secondary">Join from this browser · inventory only</span></div><aside><h2 class="bp-kicker">Then, in that terminal</h2><p>The terminal shows four labels and a fingerprint. If they match this page, press Enter there. Ctrl-C cancels.</p><h2 class="bp-kicker">What joining allows</h2><p>This browser can list the machine and its sessions. It cannot type into a terminal, start or stop sessions, or answer requests.</p></aside><section id="bp-card" aria-label="Join a machine"></section></main>';
    const card = document.getElementById('bp-card');
    let state = 'paste', q, carrier, exchange, signing, record, inventory, poll, expiry, generation = 0;
    let busy = false;
    const tabID = globalThis.crypto?.randomUUID?.();
    const assertCurrent = n => { if (n !== generation) throw new Error('cancelled'); };
    const wipe = () => { clearInterval(poll); clearInterval(expiry); carrier?.close(); exchange?.close(); carrier = exchange = signing = inventory = null; if (q) q.secret = ''; };
    const bind = (id, fn) => card.querySelector('#' + id)?.addEventListener('click', fn);
    function status(kind) {
        return '<p class="bp-status" role="status">' + escape(labels[kind][1]) + '</p>';
    }
    const kicker = (left, right = '') => '<p class="bp-kicker"><span>' + left + '</span>' + (right ? '<span>' + right + '</span>' : '') + '</p>';
    function render(kind = state) {
        state = kind;
        document.querySelector('.bp-layout').dataset.state = kind;
        if (kind === 'paste') {
            card.innerHTML = kicker('This browser', 'Step 1 of 2') + '<h2>Join from this browser</h2><form id="bp-form"><div class="bp-instruction"><p>On the computer you want to add</p><pre><span aria-hidden="true">$ </span>wt pair --show-link</pre><p class="bp-secondary">Paste the one-time link here. It expires after five minutes.</p></div><label for="bp-link">Full invitation</label><textarea id="bp-link" rows="4" placeholder="wingthing://pair#v1z.…" autocomplete="off" spellcheck="false" required></textarea><label for="bp-name">Browser name</label><input id="bp-name" value="Browser" maxlength="80" required autocomplete="off"><p class="bp-secondary">Requested access: inventory only.</p><button class="bp-primary" type="submit">Check invitation</button></form><div id="bp-saved"></div>';
            card.querySelector('#bp-form').onsubmit = async event => {
                event.preventDefault();
                const name = card.querySelector('#bp-name').value.trim();
                const link = card.querySelector('#bp-link').value;
                card.querySelector('#bp-link').value = '';
                await join(link, name);
            };
            listDevices().then(devices => {
                if (state !== 'paste') return;
                const area = card.querySelector('#bp-saved');
                for (const saved of devices.filter(d => d.device_id)) {
                    const button = document.createElement('button'); button.textContent = saved.ready ? 'Open ' + saved.name : 'Forget incomplete pairing on ' + saved.name;
                    button.onclick = saved.ready ? () => resume(saved) : async () => { record = saved; await forget(); }; area.append(button);
                }
            }).catch(() => { if (state === 'paste') card.querySelector('#bp-saved').textContent = 'Site storage is required to join.'; });
            return;
        }
        if (kind === 'checking') {
            card.innerHTML = kicker('This browser', 'Step 1 of 2') + '<h2>Checking invitation…</h2><p role="status">Checking the machine and browser storage.</p><div class="bp-actions"><button id="bp-cancel">Cancel</button></div>';
        } else if (kind === 'compare' || kind === 'waiting') {
            const fp = fingerprint(q.key).match(/.{4}/g).map(group => '<span>' + group + '</span>').join(' ');
            card.innerHTML = kicker('This browser', 'Step 2 of 2') + '<h2>Pair ' + escape(q.name) + '</h2><p>Compare the four labels and the fingerprint with the terminal on ' + escape(q.name) + '.</p><ol class="bp-labels" aria-label="Comparison labels">' + exchange.emoji.split(' ').map(glyph => '<li>' + escape(emojiLabel(glyph)) + '</li>').join('') + '</ol>' + kicker('Fingerprint') + '<p class="bp-fingerprint">' + fp + '</p>' + kicker('This browser will be able to') + '<p>List the sessions and machines on ' + escape(q.name) + '. Inventory only: no terminal input, no requests.</p>' + (kind === 'compare' ? '<div class="bp-actions"><button id="bp-match" class="bp-primary">Match · Join</button><button id="bp-differ">They differ</button></div><p class="bp-secondary">Then press Enter in the terminal. Nothing is paired until both sides agree.</p>' : '<p class="bp-wait" role="status">Waiting for Enter in the terminal on ' + escape(q.name) + '…</p>') + '<div class="bp-foot"><span id="bp-expiry" class="bp-secondary"></span><button id="bp-cancel" class="bp-link">Cancel</button></div>';
            bind('bp-match', confirm); bind('bp-differ', () => cancel('mismatch'));
            updateExpiry();
        } else if (kind === 'connected') {
            card.innerHTML = kicker('Paired') + '<h2 id="bp-machine-name">Paired machine</h2><p class="bp-ok" role="status">✓ Connected · Inventory only</p><h3>Machines</h3><div id="bp-machines">Loading machines…</div><h3>Sessions</h3><div id="bp-sessions">Loading sessions…</div><p class="bp-secondary">Updated from this machine every few seconds. Access lasts until it is revoked with wt devices or forgotten here.</p><button id="bp-forget">Forget</button>';
            bind('bp-forget', forget);
        } else {
            const label = labels[kind] || labels.unreachable;
            const incomplete = record && record.ready !== true;
            const message = incomplete ? '<p class="bp-status" role="status">This browser has an incomplete pairing. Forget it here, then run wt pair --show-link to try again.</p>' : status(kind);
            card.innerHTML = kicker('Not paired') + '<h2>' + label[0] + '</h2>' + message + '<div class="bp-actions"><button id="bp-again" class="bp-primary">Pair again</button>' + (record ? '<button id="bp-forget">' + (incomplete ? 'Forget incomplete pairing' : 'Forget') + '</button>' : '') + '</div>';
            bind('bp-again', () => { wipe(); record = q = null; render('paste'); }); bind('bp-forget', forget);
        }
        bind('bp-cancel', () => cancel('cancelled'));
    }
    function updateExpiry() {
        if (!q) return;
        const seconds = Math.max(0, Math.ceil((q.expires_at - Date.now()) / 1000));
        const label = card.querySelector('#bp-expiry');
        if (label) label.textContent = `Expires in ${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}`;
        if (!seconds && ['compare', 'waiting'].includes(state)) cancel('expired');
    }
    async function cancel(kind) {
        generation++;
        clearInterval(poll); clearInterval(expiry);
        render(kind);
        const old = exchange, oldCarrier = carrier;
        exchange = carrier = signing = inventory = null;
        if (q) q.secret = '';
        try { if (old?.key) await old.send('cancel'); } catch {}
        old?.close(); oldCarrier?.close();
    }
    async function join(link, name) {
        const n = ++generation;
        wipe(); record = null; render('checking');
        try {
            let parsed;
            try { parsed = await parseInvitation(link); } catch (error) { throw new Error(error.message === 'expired' ? 'expired' : 'invalid'); }
            assertCurrent(n); q = parsed;
            let relay;
            try { relay = new URL(q.relay); } catch { throw new Error('unreachable'); }
            if (relay.username || relay.password || relay.hash || (relay.protocol !== 'https:' && !(relay.protocol === 'http:' && ['localhost', '127.0.0.1', '[::1]'].includes(relay.hostname)))) throw new Error('unreachable');
            let pinned = await loadDevice(q.wing_id); assertCurrent(n);
            if (pinned?.tab_id === tabID && pinned.ready !== true) {
                await forgetCandidate(pinned); assertCurrent(n);
                pinned = await loadDevice(q.wing_id); assertCurrent(n);
            }
            if (pinned && pinned.key !== q.key) throw new Error('mismatch');
            if (pinned?.ready === true) { record = pinned; throw new Error('cancelled'); }
            if (pinned) { record = pinned; throw new Error('cancelled'); }
            const prepared = await prepareSigningKey(); assertCurrent(n); signing = prepared;
            carrier = new PhoneCarrier(q.wing_id, q.key);
            await carrier.open(); assertCurrent(n);
            exchange = new PairExchange(q, carrier);
            await exchange.start(signing.publicKey, name); assertCurrent(n);
            render('compare');
            expiry = setInterval(updateExpiry, 1000);
        } catch (error) { if (n === generation) await cancel(errorState(error)); }
    }
    async function confirm() {
        if (busy || state !== 'compare') return;
        const n = generation;
        busy = true; render('waiting');
        let candidate;
        try {
            let result = await exchange.send('confirm', { emoji: exchange.emoji }); assertCurrent(n);
            while (result.op !== 'prepared') {
                if (Date.now() >= q.expires_at) throw new Error('expired');
                await new Promise(resolve => setTimeout(resolve, 600)); assertCurrent(n);
                result = await exchange.send('status'); assertCurrent(n);
                if (result.state === 'cancelled') throw new Error('cancelled');
            }
            if (result.key !== q.key || result.wing_id !== q.wing_id || result.scope !== 'inventory' || !result.device_id || !result.credential) throw new Error('mismatch');
            candidate = { wing_id: q.wing_id, key: q.key, name: q.name, device_id: result.device_id, credential: result.credential, scope: 'inventory', signingKey: signing.signingKey, ready: false, tab_id: tabID };
            try { await saveDevice(candidate); } catch { throw new Error('storage'); }
            if (n !== generation) { await forgetCandidate(candidate); throw new Error('cancelled'); }
            const committed = await exchange.send('commit'); assertCurrent(n);
            if (committed.op !== 'paired' || committed.device_id !== candidate.device_id || committed.scope !== 'inventory') throw new Error('mismatch');
            await activateDevice(candidate); assertCurrent(n);
            candidate.ready = true;
            record = candidate;
            exchange.close(); exchange = null; signing = null; q = null; clearInterval(expiry);
            await connect(record, n);
        } catch (error) {
            if (candidate && (!candidate.ready || n !== generation || error.message === 'storage')) { try { await forgetCandidate(candidate); } catch {} }
            if (n === generation) await cancel(errorState(error));
        } finally { busy = false; }
    }
    async function resume(saved) {
        const n = ++generation; wipe(); record = saved; render('checking');
        try {
            if (saved.ready !== true) throw new Error('cancelled');
            carrier = new PhoneCarrier(saved.wing_id, saved.key); await carrier.open(); assertCurrent(n);
            await connect(saved, n);
        } catch (error) { if (n === generation) { wipe(); render(errorState(error)); } }
    }
    async function connect(saved, n) {
        inventory = new InventoryConnection(saved, carrier); await inventory.auth(); assertCurrent(n);
        render('connected');
        await refresh(n);

    }
    async function refresh(n) {
        if (busy && state !== 'connected') return;
        clearInterval(poll); // Never send simultaneous sequence-bearing commands.
        try {
            const result = await inventory.command('inventory'); assertCurrent(n);
            if (state !== 'connected') return;
            if (result.version !== 'wingthing.session-inventory.v1' || result.wing_id !== record.wing_id || !Array.isArray(result.sources) || !Array.isArray(result.sessions)) throw new Error('mismatch');
            card.querySelector('#bp-machine-name').textContent = result.machine_label;
            card.querySelector('#bp-machines').innerHTML = '<ul>' + result.sources.map(source => '<li><strong>' + escape(source.machine_label) + '</strong><span>' + (source.healthy ? 'Available' : 'Unavailable') + '</span></li>').join('') + '</ul>';
            const rows = result.sessions;
            const sessions = card.querySelector('#bp-sessions');
            sessions.innerHTML = rows.length ? '<ul>' + rows.map(row => '<li><strong>' + escape(row.name || 'Untitled session') + '</strong><span>' + escape([row.agent, row.status].filter(Boolean).join(' · ')) + '</span></li>').join('') + '</ul>' : '<p>No sessions on ' + escape(result.machine_label) + '.</p>';
            poll = setTimeout(() => refresh(n), 2000);
        } catch (error) { if (n === generation) { wipe(); render(errorState(error)); } }
    }
    async function forget() {
        generation++; wipe();
        try { await forgetDevice(record.wing_id, record.device_id); record = null; render('paste'); }
        catch { render('storage'); }
    }
    window.addEventListener('pagehide', () => { generation++; if (exchange) exchange.send('cancel').catch(() => {}); wipe(); });
    render('paste');
    if (imported) await join(imported, 'Browser');
}
