import './reach-share.css';
import { S } from './state.js';
import { escapeHtml, wingDisplayName, sessionDisplayName } from './helpers.js';
import { sendTunnelRequest } from './tunnel.js';
import { dormantPreviewEnabled, selectReachRoute, ReachPreview, SharePreview, connectionMeasurement } from './reach-share-state.js';

let panel;
let tab = 'connection';
let chosenWing = '';
let measurement = null;
let measuring = false;
let reach;
let share;
let selectedResource = null;
let identity = { provider: 'google', subject: 'fixture-alice', name: 'Alice' };
let previewOpen = false;
let audience = 'owner';
let network = { lan: true, tailnet: false, relay: true };
let inputText = '';
let inputSession = '';
let lastStatus = '';
const esc = escapeHtml;
const chip = '<span class="reach-chip">Dormant · local dev fixture</span>';

export function initReachShare() {
    panel = document.getElementById('reach-share');
    if (!panel) return;
    reach = new ReachPreview();
    share = new SharePreview(S.currentUser.id);
    render();
}
export function refreshReachShare() {
    if (!panel) return;
    // Inventory probes must not erase a draft or move focus while typing.
    if (panel.contains(document.activeElement)) return;
    render();
}
function currentWing() { return S.wingsData.find(w => w.wing_id === chosenWing); }
function label(id) { const w = S.wingsData.find(w => w.wing_id === id); return w ? wingDisplayName(w) || id : id; }
function options() {
    return S.wingsData.map(w => '<option value="' + esc(w.wing_id) + '"' + (w.wing_id === chosenWing ? ' selected' : '') + '>' + esc(wingDisplayName(w) || w.wing_id) + '</option>').join('');
}
function field(title, value) { return '<div class="reach-kv"><strong>' + title + '</strong><span>' + value + '</span></div>'; }
function dormantNotice() { return '<p class="reach-notice">Paid route preview. These controls exercise local fixture state only. No account registration, email, public URL, terminal input or SSH connection is created.</p>'; }
function nav() {
    const tabs = [['connection', 'Connection'], ['privacy', 'Privacy']];
    if (dormantPreviewEnabled(S.currentUser)) tabs.push(['reach', 'Reach preview'], ['sharing', 'Private sharing'], ['ssh', 'SSH preview']);
    if (audience === 'alice') return '<div class="reach-tabs">' + chip + '<span>Private viewer fixture</span></div>';
    return '<div class="reach-tabs" role="tablist" aria-label="Connection and access">' + tabs.map(([id, name]) => '<button type="button" role="tab" aria-selected="' + (tab === id) + '" data-reach-tab="' + id + '">' + name + '</button>').join('') + (['reach', 'sharing', 'ssh'].includes(tab) ? chip : '') + '</div>';
}
function render() {
    if (!panel) return;
    if (!chosenWing && S.wingsData.length) chosenWing = S.wingsData[0].wing_id;
    // Client flags cannot enable a runtime capability: these tabs have none.
    if (!dormantPreviewEnabled(S.currentUser) && ['reach', 'sharing', 'ssh'].includes(tab)) tab = 'connection';
    if (!panel.dataset.open) {
        panel.innerHTML = '<div class="reach-toolbar"><button id="reach-open" class="btn-sm" type="button">Connection &amp; privacy</button>' +
            (dormantPreviewEnabled(S.currentUser) ? '<span class="reach-chip">Reach &amp; share · dormant preview</span>' : '') + '</div>';
        panel.querySelector('#reach-open').onclick = () => { panel.dataset.open = '1'; render(); };
        return;
    }
    let body = '';
    const wingSelector = '<label for="reach-wing">Owning machine</label><select id="reach-wing">' + options() + '</select>';
    if (tab === 'connection') {
        const w = currentWing();
        body = '<h2>Connection to your wing</h2>' + wingSelector +
            field('Route', 'Browser → this roost → selected wing. The browser control channel uses the encrypted tunnel; physical network type is not inferred.') +
            field('Machine state', !w ? 'Select a machine' : w.online === false ? 'Offline · last known inventory is stale. Session state is unknown.' : w.tunnel_error ? 'Unreachable or locked · ' + esc(w.tunnel_error) : w.online === true ? 'Wing connected to this roost' : 'Checking connection') +
            '<button id="reach-measure"' + (!w || w.online !== true || measuring ? ' disabled' : '') + '>' + (measuring ? 'Measuring…' : 'Measure connection') + '</button>' +
            '<div id="reach-measurement" class="reach-status" role="status">' + (measurement && measurement.wingId === chosenWing ? measurement.error ? esc(measurement.error) : '<strong>' + measurement.ms + ' ms</strong> · encrypted wing.info request round trip<br>Measured ' + esc(new Date(measurement.measuredAt).toLocaleTimeString()) + ' · includes request processing; varies by route and load.' : 'No measurement yet. No latency promise.') + '</div>' +
            '<p>Your existing LAN / tailnet pairing stays free. Phone pairing and owner relay registration are separate permissions.</p>';
    } else if (tab === 'privacy') {
        body = '<h2>What the relay can see</h2><div class="reach-grid">' +
            field('Encrypted to the selected wing', 'Terminal input/output and tunnel data such as sessions, paths and configuration. Credentials sent inside the tunnel stay encrypted in transit.') +
            field('Visible to transport', 'Account ownership, wing IDs and public keys, IPs, timing, traffic sizes and public handshake messages.') +
            field('Visible to the wing', 'Its session content and commands. Model providers see the inference requests their CLI sends.') +
            field('Browser trust boundary', 'The relay serves this JavaScript. Initial wing-key trust is TOFU; persistent wing keys do not provide forward secrecy against later compromise.') + '</div>' +
            '<p class="reach-notice">A compromised relay can interrupt availability. Planned HTTP preview gateways may read preview requests and responses; phone encryption claims do not apply to HTTP previews.</p>';
    } else if (tab === 'reach') {
        const w = reach.wing(chosenWing || 'unselected');
        const route = selectReachRoute(w, network);
        body = chip + '<h2>Reach the same machine</h2>' + dormantNotice() + wingSelector + '<div class="reach-grid"><div>' +
            field('Owner registration', w.registered ? esc(label(chosenWing)) + ' · fixture registration retained. This does not pair a phone.' : 'Owner registers this wing once; paired phone credentials remain wing-specific.') +
            '<button id="reach-register">Preview registration</button><label for="reach-network">Network fixture</label><select id="reach-network"><option value="lan"' + (network.lan ? ' selected' : '') + '>LAN · Free</option><option value="tailnet"' + (network.tailnet ? ' selected' : '') + '>Tailnet · Free</option><option value="cellular"' + (!network.lan && !network.tailnet ? ' selected' : '') + '>Cellular · simulated relay</option></select>' +
            '<label for="reach-failure">Failure fixture</label><select id="reach-failure"><option value="healthy">Healthy</option><option value="asleep"' + (!w.awake ? ' selected' : '') + '>Wing asleep</option><option value="relay"' + (!network.relay ? ' selected' : '') + '>Relay interrupted</option><option value="expired"' + (!w.pro ? ' selected' : '') + '>Pro expired</option><option value="pin"' + (!w.pinMatches ? ' selected' : '') + '>Fingerprint mismatch</option><option value="revoked"' + (w.revoked ? ' selected' : '') + '>Device revoked</option></select></div><div>' +
            field('Selected route', esc(route.reason)) + field('Selected session', esc(inputSession ? sessionDisplayName(S.sessionsData.find(s => s.id === inputSession && s.wing_id === chosenWing) || { id: inputSession }) : 'Choose a session below. Selection remains on its owning wing.')) +
            '<p>No reconnect resends input. Unknown delivery requires checking the original receipt.</p></div></div>' +
            '<label for="reach-session">Exact session on ' + esc(label(chosenWing)) + '</label><select id="reach-session"><option value="">Select session</option>' + S.sessionsData.filter(s => s.wing_id === chosenWing).map(s => '<option value="' + esc(s.id) + '"' + (s.id === inputSession ? ' selected' : '') + '>' + esc(sessionDisplayName(s) + ' · ' + s.id) + '</option>').join('') + '</select>' +
            '<label for="reach-input">Message draft · never sent to a terminal</label><textarea id="reach-input"' + (reach.pending ? ' disabled' : '') + '>' + esc(inputText) + '</textarea>' +
            '<button id="reach-send"' + (!route.route || !inputSession || reach.pending ? ' disabled' : '') + '>Simulate lost acknowledgement</button><button id="reach-reconnect">Reconnect reads only</button><button id="reach-receipt"' + (!reach.pending ? ' disabled' : '') + '>Check original fixture receipt</button>' +
            '<div id="reach-delivery" class="reach-status" role="status">' + (reach.pending ? esc(reach.pending.status) + ' · ' + esc(label(reach.pending.wingId)) + ' / ' + esc(reach.pending.sessionId) + '<br>Original intent ' + esc(reach.pending.intentId) + ' · simulated submissions: ' + reach.submissions : 'Draft retained locally in this view. No input has been submitted.') + '</div>';
    } else if (tab === 'sharing') {
        body = renderSharing(wingSelector);
    } else if (tab === 'ssh') {
        body = chip + '<h2>SSH through Wingthing</h2>' + dormantNotice() + wingSelector +
            '<label for="reach-os-user">Authorized OS principal</label><input id="reach-os-user" placeholder="OS account on this machine" autocomplete="off">' +
            field('Authority', 'An OS shell is broader than a phone session grant. A paired phone credential cannot authorize SSH. Review the machine, OS principal and an explicit shell grant before connecting.') +
            '<button id="reach-ssh">Review planned command</button><div id="reach-ssh-command" class="reach-status" role="status">No SSH connection. Free saved OpenSSH profiles remain available through wt attach --remote HOST SESSION_ID.</div>';
    }
    document.body.classList.toggle('reach-alice-preview', audience === 'alice');
    panel.innerHTML = '<section class="reach-panel" aria-label="Connection and private access">' + nav() + body + '<div id="reach-action-status" role="status">' + esc(lastStatus) + '</div>' + (audience === 'alice' ? '' : '<button id="reach-close">Close details</button>') + '</section>';
    bind();
}
function renderSharing(wingSelector) {
    if (audience === 'alice') {
        const result = selectedResource && share.access(selectedResource.key, identity);
        return chip + '<h2>Private preview</h2><p>Signed in as ' + esc(identity.name) + ' · ' + esc(identity.provider) + ' · verified fixture subject</p>' +
            (previewOpen && result && result.status === 'allowed' ? selectedResource.kind === 'app' ? '<div class="reach-preview-app"><h3>Pairing demo</h3><p>Private app preview fixture · port ' + selectedResource.port + '</p><p>This dormant app surface has no machine inventory, credentials or terminal controls.</p></div>' : '<div class="reach-preview-app"><h3>Observe selected session</h3><p>' + esc(selectedResource.sessionId) + ' · read-only fixture · no live transcript</p><button disabled>Send terminal input</button></div>' : '<div class="reach-status" role="status">' + esc(result ? result.message : 'Access denied') + '</div>') +
            '<p>Planned HTTP gateway can see preview traffic. This is separate from the encrypted phone channel.</p><button id="share-owner">Return to owner fixture</button>';
    }
    let resource = selectedResource;
    const resources = Array.from(share.resources.values());
    const grants = Array.from(share.grants.values()).filter(g => resource && g.resourceKey === resource.key);
    return chip + '<h2>Publish privately &amp; grant exact access</h2>' + dormantNotice() + '<div class="reach-grid"><div>' + wingSelector +
        '<label for="share-port">Local port</label><input id="share-port" type="number" min="1" max="65535" value="3000"><button id="share-publish">Preview private publication</button>' +
        field('Default access', 'Owner only. Possession of the planned URL is insufficient. Publishing an app does not share a terminal.') + '</div><div>' +
        '<label for="share-resource">Qualified resource</label><select id="share-resource"><option value="">Select a resource</option>' + resources.map(r => '<option value="' + esc(r.key) + '"' + (r === resource ? ' selected' : '') + '>' + esc(label(r.wingId) + ' · ' + (r.kind === 'app' ? 'port ' + r.port : 'session ' + r.sessionId)) + '</option>').join('') + '</select>' +
        field('Private route draft', resource ? esc(label(resource.wingId) + ' · ' + resource.id) + '<br>Local draft identifier · no public URL exists' : 'Publish a fixture resource to review access.') +
        '<label for="share-provider">Alice’s verified identity</label><select id="share-provider"><option value="google"' + (identity.provider === 'google' ? ' selected' : '') + '>Google · Alice · fixture-alice</option><option value="github"' + (identity.provider === 'github' ? ' selected' : '') + '>GitHub · Alice · fixture-alice</option></select>' +
        '<button id="share-invite"' + (!resource ? ' disabled' : '') + '>Preview invitation grant</button><button id="share-alice"' + (!resource ? ' disabled' : '') + '>Open as invited Alice</button></div></div>' +
        '<h3>Access to this resource</h3><div id="share-grants">' + (grants.length ? grants.map(g => '<div class="reach-grant"><div><strong>' + esc(g.identity.name) + '</strong> · ' + esc(g.identity.provider) + '<br>' + esc(g.role) + ' · ' + (g.active ? 'active' : 'revoked') + ' · ' + g.streams + ' fixture viewers</div><button data-revoke="' + esc(g.key) + '"' + (!g.active ? ' disabled' : '') + '>Revoke Alice</button></div>').join('') : '<p>No invited viewers. Owner only.</p>') + '</div>' +
        '<p>Revocation closes fixture viewers and refuses later opens. Previously seen or downloaded content cannot be erased.</p>' +
        '<label for="share-session">Separate live-session grant · read-only default</label><select id="share-session"><option value="">Select exact session</option>' + S.sessionsData.map(s => '<option value="' + esc(JSON.stringify([s.wing_id, s.id])) + '">' + esc(label(s.wing_id) + ' · ' + sessionDisplayName(s) + ' · ' + s.id) + '</option>').join('') + '</select><button id="share-observe">Preview observer grant</button>' +
        (resource && resource.kind === 'session' ? '<div class="reach-status">Observe selected session · no input. Controller authorization is separate.<br><button disabled>Send terminal input</button></div>' : '') +
        '<label for="share-failure">Preview failure</label><select id="share-failure"><option value="healthy">Healthy</option><option value="app">App not listening</option><option value="wrong">Wrong identity</option><option value="expired">Pro expired</option></select><button id="share-check">Check fixture access</button><div id="share-failure-result" class="reach-status" role="status">Identity → exact resource grant → upstream availability.</div>';
}
function action(fn) { try { fn(); } catch (e) { lastStatus = e.message; } render(); }
function bind() {
    panel.querySelectorAll('[data-reach-tab]').forEach(button => button.onclick = () => { tab = button.dataset.reachTab; lastStatus = ''; audience = 'owner'; render(); });
    const close = panel.querySelector('#reach-close');
    if (close) close.onclick = () => { delete panel.dataset.open; render(); };
    const on = (id, fn) => { const el = panel.querySelector('#' + id); if (el) el.onclick = fn; };
    const change = (id, fn) => { const el = panel.querySelector('#' + id); if (el) el.onchange = fn; };
    change('reach-wing', e => { chosenWing = e.target.value; inputSession = ''; measurement = null; lastStatus = ''; render(); });
    on('reach-measure', async () => {
        const wingId = chosenWing;
        measuring = true; render();
        const start = performance.now();
        try {
            await sendTunnelRequest(wingId, { type: 'wing.info' }, { skipPasskey: true });
            measurement = { ...connectionMeasurement(start, performance.now(), 'encrypted roost tunnel', Date.now()), wingId };
        } catch (e) { measurement = { wingId, error: 'Connection measurement failed: ' + e.message + '. Retry after verifying this wing.' }; }
        finally { measuring = false; render(); }
    });
    on('reach-register', () => action(() => { reach.register(chosenWing); lastStatus = 'Fixture registration retained for this wing. Phone pairing unchanged.'; }));
    change('reach-network', e => { network.lan = e.target.value === 'lan'; network.tailnet = e.target.value === 'tailnet'; render(); });
    change('reach-failure', e => { const w = reach.wing(chosenWing); w.awake = e.target.value !== 'asleep'; w.pro = e.target.value !== 'expired'; w.pinMatches = e.target.value !== 'pin'; w.revoked = e.target.value === 'revoked'; network.relay = e.target.value !== 'relay'; render(); });
    change('reach-session', e => { inputSession = e.target.value; render(); });
    const input = panel.querySelector('#reach-input'); if (input) input.oninput = e => { inputText = e.target.value; };
    on('reach-send', () => action(() => { reach.beginInput(chosenWing, inputSession, inputText, crypto.randomUUID()); lastStatus = 'Simulated acknowledgement lost. The immutable original input remains unknown.'; }));
    on('reach-reconnect', () => action(() => { reach.reconnect(); lastStatus = 'Read-only reconnect. Simulated submissions remain ' + reach.submissions + '. No input replay.'; }));
    on('reach-receipt', () => action(() => { reach.checkReceipt({ ...reach.pending, status: 'acknowledged' }); lastStatus = 'Original fixture receipt matched. This does not establish real terminal delivery.'; }));
    on('share-publish', () => action(() => { selectedResource = share.publish(S.currentUser.id, chosenWing, Number(panel.querySelector('#share-port').value)); lastStatus = 'Private publication draft retained. No network route was opened.'; }));
    change('share-resource', e => { selectedResource = share.resources.get(e.target.value) || null; render(); });
    change('share-provider', e => { identity = { ...identity, provider: e.target.value }; render(); });
    on('share-invite', () => action(() => { share.invite(S.currentUser.id, selectedResource.key, identity, selectedResource.kind === 'app' ? 'view' : 'observer'); lastStatus = 'Fixture identity grant created. No invitation email or OAuth request.'; }));
    on('share-alice', () => action(() => { const result = share.open(selectedResource.key, identity); previewOpen = result.status === 'allowed'; audience = 'alice'; lastStatus = ''; }));
    on('share-owner', () => { audience = 'owner'; render(); });
    panel.querySelectorAll('[data-revoke]').forEach(button => button.onclick = () => action(() => { const closed = share.revoke(S.currentUser.id, button.dataset.revoke); previewOpen = false; lastStatus = 'Access revoked · ' + closed + ' active fixture viewer(s) closed.'; }));
    on('share-observe', () => action(() => { const raw = panel.querySelector('#share-session').value; if (!raw) throw new Error('Choose an exact session'); const [wingId, sessionId] = JSON.parse(raw); selectedResource = share.session(S.currentUser.id, wingId, sessionId); share.invite(S.currentUser.id, selectedResource.key, identity, 'observer'); lastStatus = 'Separate observer fixture grant. Terminal input remains disabled.'; }));
    on('share-check', () => {
        const failure = panel.querySelector('#share-failure').value;
        if (selectedResource) selectedResource.up = failure !== 'app';
        const result = share.access(selectedResource && selectedResource.key, failure === 'wrong' ? { provider: identity.provider, subject: 'fixture-other' } : identity, failure !== 'expired');
        panel.querySelector('#share-failure-result').textContent = result.message || 'Fixture access allowed to this resource only.';
    });
    on('reach-ssh', () => {
        const user = panel.querySelector('#reach-os-user').value.trim();
        const target = currentWing();
        panel.querySelector('#reach-ssh-command').textContent = !target || !/^[a-z_][a-z0-9_-]*$/i.test(user) ? 'Select a machine and valid OS principal.' : 'Planned: wt ssh ' + label(chosenWing) + ' --user ' + user + '\nExact wing: ' + chosenWing + '\nRequires owner shell grant and pinned host verification. Transport is dormant; no shell opened.';
    });
}
