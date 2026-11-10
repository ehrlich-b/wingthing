// Screen captures for the web app and PWA look. Real built roost + encrypted
// fixture machines, same harness as reach-share.mjs. No provider, OAuth or
// public publication. Fixtures are not live proof.
import fs from 'node:fs';
import assert from 'node:assert/strict';
import path from 'node:path';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { spawn, spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { removeState } from './fixture-state.mjs';
import { x25519 } from '../../web/node_modules/@noble/curves/ed25519.js';
import { hkdf } from '../../web/node_modules/@noble/hashes/hkdf.js';
import { sha256 } from '../../web/node_modules/@noble/hashes/sha2.js';
import { gcm } from '../../web/node_modules/@noble/ciphers/aes.js';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const shots = process.env.WT_LOOK_SHOTS || path.join(root, '.scratch/look');
const shellMode = process.env.WT_LOOK_SHELL || '';
const only = (process.env.WT_LOOK_ONLY || '').split(',').filter(Boolean);
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright');
fs.mkdirSync(shots, { recursive: true });
fs.mkdirSync(path.join(root, '.scratch'), { recursive: true });
const state = fs.mkdtempSync(path.join(tmpdir(), 'wt'));
// Shell runs need only one listener. Let the OS choose a free port so a
// concurrent look run cannot supply this fixture's health or inventory.
async function availablePort() {
    const probe = createServer();
    await new Promise((resolve, reject) => {
        probe.once('error', reject);
        probe.listen(0, '127.0.0.1', resolve);
    });
    const port = probe.address().port;
    await new Promise((resolve, reject) => probe.close(error => error ? reject(error) : resolve()));
    return port;
}
const port = Number(process.env.WT_LOOK_PORT || (shellMode ? await availablePort() : 18191));
const base = 'http://127.0.0.1:' + port;
const env = { HTTP_PROXY:'http://127.0.0.1:1', HTTPS_PROXY:'http://127.0.0.1:1', NO_PROXY:'127.0.0.1,localhost', PATH: process.env.PATH, TMPDIR: process.env.TMPDIR, HOME: path.join(state, 'home'), WINGTHING_DIR: state };
fs.mkdirSync(env.HOME);
fs.writeFileSync(path.join(state, 'wing.yaml'), 'label: Laptop\n', { mode: 0o600 });
const log = fs.openSync(path.join(shots, 'fixture.log'), 'w');
const roost = spawn('nice', ['-n', '10', process.env.WT_LOOK_BIN || path.join(root, 'wt'), 'roost', 'start', '--foreground', '--dev', '--addr', '127.0.0.1:' + port], { cwd: root, env: shellMode ? {...env, WT_ROOST_READY_FD:'3'} : env, stdio: shellMode ? ['ignore', log, log, 'pipe'] : ['ignore', log, log] });
let roostReady = '', roostStartupError;
roost.on('error', error => { roostStartupError = error; });
roost.stdio[3]?.on('data', data => { roostReady += data; });
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
const sockets = new Map();
const sent = [];
const captures = [];
const overflow = [];
const checks = [];
const checked = (name, evidence) => { checks.push({name, evidence}); console.log('CHECK ' + name); };
const publicBase = 'http://127.0.0.1:' + (port + 2);
const publicServer = shellMode ? null : spawn('nice', ['-n', '10', path.join(root, '.scratch/look-public'), '127.0.0.1:' + (port + 2)], { cwd: root, env, stdio: ['ignore', log, log] });

function writeIndex(complete) {
    fs.writeFileSync(path.join(shots, 'capture.json'), JSON.stringify({ complete, captures, overflow, checks }, null, 2) + '\n');
    fs.writeFileSync(path.join(shots, 'index.html'), '<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>wingthing captures</title><style>body{font:16px system-ui;margin:24px}ul{display:grid;grid-template-columns:repeat(auto-fit,minmax(280px,1fr));gap:24px;padding:0;list-style:none}img{width:100%;border:1px solid #888}a{overflow-wrap:anywhere}</style><h1>wingthing captures</h1><p>' + (complete ? 'Complete' : 'Incomplete') + ' · ' + captures.length + ' images · ' + overflow.length + ' overflow failures</p><ul>' + captures.map(({file}) => '<li><a href="' + file + '">' + file + '<br><img loading="lazy" src="' + file + '" alt="' + file + '"></a></li>').join('') + '</ul></html>\n');
}

function encrypt(key, value) {
    const iv = crypto.getRandomValues(new Uint8Array(12));
    const data = gcm(key, iv).encrypt(new TextEncoder().encode(JSON.stringify(value)));
    return Buffer.concat([iv, data]).toString('base64');
}

const at = minutes => new Date(Date.now() - minutes * 60000).toISOString();
const msg = (sequence, role, text, minutes) => ({ sequence, type: 'message', role, text, timestamp: at(minutes) });
const row = (id, name, agent, status, project, extra = {}) => ({
    session_id: id, name, agent, cwd: '/Users/example/code/' + project, user_id: 'local', email: 'owner@example.test',
    output: { epoch: 'look', cursor: 0 },
    lifecycle: { status, state: status === 'blocked' ? 'needs_input' : status, state_source: agent === 'claude' ? 'claude_hook' : 'codex_hook', state_cursor: 1 },
    ...extra,
});
const machines = {
    studio: { name: 'Studio', rows: [
        { ...row('wing', 'wing', 'claude', 'working', 'wingthing'), pinned: true, role: 'coordinator' },
        row('redirect', 'fix-login-redirect', 'claude', 'blocked', 'wingthing'),
        row('migrate', 'migrate-sessions-table', 'claude', 'working', 'wingthing'),
    ] },
    macbook: { name: 'MacBook', rows: [
        row('ratelimit', 'add-rate-limit-to-api', 'codex', 'working', 'wingthing'),
        row('notes', 'write-release-notes', 'claude', 'idle', 'wingthing'),
    ] },
    mini: { name: 'Mini', rows: [
        row('playwright', 'bump-playwright', 'codex', 'completed', 'wingthing'),
        row('docs', 'docs-cleanup', 'claude', 'idle', 'docs'),
    ] },
};
const events = {
    wing: [
        msg(1, 'user', 'Fix the redirect loop after login on /settings. Deploy to staging once tests pass.', 21),
        msg(5, 'assistant', 'Deploy script is ready on Studio and needs your go. The rate limit work on MacBook is still going, so I would deploy the redirect fix on its own.', 1),
    ],
    redirect: [
        msg(1, 'user', 'Fix the redirect loop after login on /settings. Deploy to staging once tests pass.', 21),
        msg(2, 'assistant', 'Reproduced it. Logging in from /settings returns to /, then / sends the request back to /settings.', 17),
        msg(3, 'assistant', 'Changed the redirect handler to keep the return path in the session cookie and read it once after login. Added a test for the loop.', 9),
        msg(4, 'assistant', 'Tests pass, 41 of 41. Asking to run ./scripts/deploy.sh staging on Studio.', 2),
        { sequence: 5, type: 'input_requested', state: 'needs_input', reason: 'Run ./scripts/deploy.sh staging', timestamp: at(2) },
    ],
    migrate: [
        msg(1, 'user', 'Add an index on sessions.user_id and backfill the new column on staging.', 40),
        msg(2, 'assistant', 'Wrote migration 0042. It adds the column, then the index concurrently so writes keep going.', 22),
        msg(3, 'assistant', 'Migration 0042 applied on the staging database. Backfilling 18,204 rows, about four minutes left.', 6),
    ],
    ratelimit: [msg(1, 'assistant', 'Added token-bucket middleware: 100 requests a minute per key, 429 with Retry-After. Three files changed.', 13)],
    notes: [msg(1, 'assistant', 'Draft is in CHANGELOG.md. Waiting for your edits.', 14)],
    playwright: [msg(1, 'assistant', 'Done. Playwright 1.49 to 1.50, all 212 tests green.', 41)],
    docs: [msg(1, 'assistant', 'Rewrote the pairing guide. Two sections left.', 120)],
};
const screens = {
    migrate: 'psql staging -f migrations/0042_sessions_user.sql\r\nALTER TABLE\r\nCREATE INDEX\r\n\r\nbackfill: 9,860 / 18,204 rows  (54%)\r\nbackfill: 11,402 / 18,204 rows (63%)\r\n',
    redirect: 'go test ./auth/...\r\nok   wingthing/auth   2.41s\r\n\r\n> Tests pass (41/41). Run ./scripts/deploy.sh staging?\r\n  1. Yes\r\n  2. No, and tell Claude what to do differently\r\n',
};
const fallback = name => [msg(1, 'assistant', name + ' is running.', 30)];

async function machine(id) {
    const fixture = machines[id];
    const token = 'local-look-' + id;
    const seed = spawnSync('python3', ['-c', 'import sqlite3,sys\nc=sqlite3.connect(sys.argv[1]);c.execute("INSERT INTO device_tokens(token,user_id,device_id,expires_at) VALUES (?, ?, ?, datetime(\'now\',\'+1 hour\'))",(sys.argv[2],"local",sys.argv[3]));c.commit()', path.join(state, 'roost.db'), token, id], { encoding: 'utf8' });
    if (seed.status !== 0) throw Error(seed.stderr);
    const priv = x25519.utils.randomSecretKey();
    const pub = Buffer.from(x25519.getPublicKey(priv)).toString('base64');
    const socket = new WebSocket(base.replace('http:', 'ws:') + '/ws/wing?token=' + encodeURIComponent(token));
    sockets.set(id, socket);
    await new Promise((resolve, reject) => {
        socket.onopen = () => socket.send(JSON.stringify({ type: 'wing.register', wing_id: id, public_key: pub, agents: ['claude', 'codex'], locked: false, purpose_binding: true }));
        socket.onerror = reject;
        socket.onmessage = e => { if (JSON.parse(e.data).type === 'registered') resolve(); };
    });
    socket.onmessage = e => {
        const outer = JSON.parse(e.data);
        if (shellMode && (outer.type === 'pty.start' || outer.type === 'pty.attach')) {
            if (outer.type === 'pty.start') fixture.rows.push(row(outer.session_id, 'fixture-new-session', outer.agent, 'working', 'wingthing'));
            socket.send(JSON.stringify({ type:'pty.started', session_id:outer.session_id, agent:outer.agent || 'claude', public_key:pub, controller_id:outer.controller_id }));
            return;
        }
        if (outer.type !== 'tunnel.req') return;
        const key = hkdf(sha256, x25519.getSharedSecret(priv, Buffer.from(outer.sender_pub, 'base64')), new Uint8Array(32), new TextEncoder().encode('wt-tunnel'), 32);
        const encrypted = Buffer.from(outer.payload, 'base64');
        const inner = JSON.parse(new TextDecoder().decode(gcm(key, encrypted.subarray(0, 12)).decrypt(encrypted.subarray(12))));
        let response;
        if (inner.type === 'wing.info') response = { hostname: fixture.name, wing_label: fixture.name, platform: 'darwin', agents: ['claude', 'codex'], projects: [{ name: 'wingthing', path: '/Users/example/code/wingthing' }], capabilities: ['session.rename.v1'] };
        else if (inner.type === 'sessions.list') response = { sessions: fixture.rows };
        else if (inner.type === 'session.control' && inner.operation === 'session_read') {
            const selected = fixture.rows.find(item => item.session_id === inner.arguments.session);
            const list = selected && (events[selected.session_id] || fallback(selected.name));
            response = selected ? { session: selected.session_id, lifecycle: { ...selected.lifecycle, session_id: selected.session_id, agent: selected.agent, provider_session_id: 'provider-' + selected.session_id, process_alive: true, ready: true, cursor: list.length, head_cursor: list.length, has_more: false, report_cursor: list.length, reported_at: at(1), report: { goal: 'Fix the redirect loop and deploy to staging', update: 'Deploy script is ready on Studio and needs your go.', presence: 'awake' }, events: list } } : { error: 'Session not found' };
        }
        else if (inner.type === 'session.control' && inner.operation === 'terminal_read') response = { result: { ansi: (screens[inner.arguments.session] || '$ ').replace(/\r/g, '') } };
        else if (inner.type === 'session.control' && inner.operation === 'terminal_send') { sent.push({ machine: id, ...inner.arguments }); response = { session: inner.arguments.session, bytes_sent: inner.arguments.input.length }; }
        else if (inner.type === 'dir.list') response = { entries: [{ name:'wingthing', path:'/Users/example/code/wingthing', is_dir:true }] };
        else if (inner.type === 'pty.resize' || inner.type === 'pty.detach') response = {};
        else if (inner.type === 'sessions.history') response = { sessions: [] };
        else if (inner.type === 'conversations.list') response = { conversations: [] };
        else response = { error: 'unsupported fixture request' };
        socket.send(JSON.stringify({ type: 'tunnel.res', request_id: outer.request_id, payload: encrypt(key, response) }));
    };
}

async function shoot(page, name, suffix) {
    if (only.length && !only.includes(name)) return;
    await page.waitForTimeout(shellMode ? 100 : 700);
    if (name === '02-sessions') {
        const pinned = await page.locator('.pp-pin').innerText();
        assert.ok(pinned.includes(events.wing[1].text));
        const timestamp = await page.evaluate(at => new Date(at).toLocaleTimeString([], {hour:'numeric', minute:'2-digit'}), events.wing[1].timestamp);
        assert.ok(pinned.includes(timestamp));
        checked(name + suffix + '-original-pinned-update', {pinned, timestamp});
    }
    assert.doesNotMatch(await page.locator('body').innerText(), /progress receipt/i);
    const file = name + suffix + '.png';
    await page.screenshot({ path: path.join(shots, file) });
    const dimensions = await page.evaluate(() => ({ viewport: innerWidth, width: Math.max(document.documentElement.scrollWidth, document.body?.scrollWidth || 0) }));
    captures.push({ file, url: page.url(), ...dimensions });
    if (dimensions.viewport === 390 && dimensions.width > dimensions.viewport) {
        overflow.push({ file, ...dimensions });
        console.error('OVERFLOW ' + file + ' ' + dimensions.width + ' > ' + dimensions.viewport);
    }
    writeIndex(false);
    console.log('SHOT ' + file);
}
async function checkPublic(page, name, suffix) {
    assert.equal(await page.locator('a[href*="github.com"]').count(), 0);
    assert.ok(await page.getByRole('link', {name:'GitLab source', exact:true}).count());
    if (page.viewportSize().width === 390) {
        const nav = await page.locator('.site-nav').evaluate(el => {
            const box = selector => el.querySelector(selector).getBoundingClientRect().toJSON();
            return { logo:box('.logo'), action:box('.nav-cta'), links:[...el.querySelectorAll('.nav-secondary a')].map(a=>a.getBoundingClientRect().toJSON()) };
        });
        assert.ok(nav.action.top < nav.logo.bottom && nav.logo.top < nav.action.bottom);
        assert.equal(new Set(nav.links.map(r => Math.round(r.top))).size, 1);
        assert.ok(nav.links.every(r => r.top >= nav.action.bottom && r.right <= 390));
        checked(name + suffix + '-navigation', nav);
    }
    if (name === '09-landing') {
        const visible = await page.locator('.product-preview').evaluate(el => {
            const r = el.getBoundingClientRect();
            return {top:r.top, bottom:r.bottom, capabilitiesTop:document.querySelector('.how').getBoundingClientRect().top};
        });
        assert.ok(visible.top > 0 && visible.bottom > visible.top && visible.bottom < visible.capabilitiesTop);
        await page.getByRole('link', {name:'Install wingthing', exact:true}).last().isVisible().then(assert.ok);
        assert.equal(await page.locator('video').count(), 0);
        checked(name + suffix + '-illustrative-preview', visible);
    }
    if (name === '10-install') {
        await page.evaluate(() => Object.defineProperty(navigator, 'clipboard', {configurable:true, value:{writeText:text=>{window.copiedCommand=text; return Promise.resolve();}}}));
        await page.locator('.inst-copy').click();
        assert.equal(await page.evaluate(() => window.copiedCommand), 'curl -fsSL https://wingthing.ai/install.sh | sh');
        checked(name + suffix + '-exact-copy', 'curl -fsSL https://wingthing.ai/install.sh | sh');
    }
    if (name === '12-patterns') {
        const first = page.locator('.pattern').first();
        assert.equal(await first.getAttribute('data-route'), 'local-human');
        const guide = await first.getByRole('link', {name:'read setup guide'}).boundingBox();
        if (page.viewportSize().width === 390) assert.ok(guide.y >= 0 && guide.y + guide.height <= 844, JSON.stringify(guide));
        checked(name + suffix + '-local-guide-first', guide);
    }
    if (name === '11-docs' && page.viewportSize().width === 390) {
        const result = await page.evaluate(() => {
            const widths = [];
            for (let y=0; y<document.documentElement.scrollHeight; y+=600) {
                window.scrollTo(0,y); widths.push(Math.max(document.documentElement.scrollWidth,document.body.scrollWidth));
            }
            const tables = [...document.querySelectorAll('.docs-table-scroll')].map(el => {
                el.scrollLeft=el.scrollWidth;
                const wrapper=el.getBoundingClientRect(), last=el.querySelector('tr:last-child > :last-child').getBoundingClientRect();
                const result={container:el.clientWidth, content:el.scrollWidth, rightReachable:last.right <= wrapper.right+1, bounded:wrapper.left>=0 && wrapper.right<=innerWidth};
                el.scrollLeft=0;return result;
            });
            window.scrollTo(0,0);return {widths,tables};
        });
        assert.ok(result.widths.length > 10 && result.widths.every(w=>w<=390));
        assert.ok(result.tables.length && result.tables.every(t=>t.bounded && t.rightReachable));
        checked(name + suffix + '-full-document-tables', result);
    }
}
async function checkWingSource(page, suffix, automatic = false) {
    const evidence = await page.evaluate(() => {
        const transcript = document.querySelector('.pp-conversation'), bounds = transcript.getBoundingClientRect();
        const rows = [...transcript.querySelectorAll('[data-source]')];
        // Inspect rendered text fragments independently of the product's line
        // grouping, including inline code and the report's own identity.
        const fragments = element => {
            const walker = document.createTreeWalker(element, NodeFilter.SHOW_TEXT), result = [];
            while (walker.nextNode()) {
                if (!walker.currentNode.textContent.trim()) continue;
                const range = document.createRange(); range.selectNodeContents(walker.currentNode);
                result.push(...[...range.getClientRects()].map(r => ({top:r.top,bottom:r.bottom,text:walker.currentNode.textContent})));
            }
            return result;
        };
        const reports = rows.map(row => ({source:row.dataset.source, identity:row.querySelector('.pp-who').getBoundingClientRect().toJSON(), body:fragments(row.querySelector('.pp-say')), text:fragments(row)}));
        const first = reports.find(row => row.body.some(r => r.top >= bounds.top - 1 && r.bottom <= bounds.bottom + 1));
        const visible = reports.flatMap(row => row.text).filter(r => r.bottom > bounds.top && r.top < bounds.bottom);
        const label = document.querySelector('[data-thread-source]');
        return {source:first && first.identity.top < bounds.top - 1 ? first.source : '', label:label.textContent, continued:label.hasAttribute('data-continued'), headingBottom:document.querySelector('.pp-thread-heading').getBoundingClientRect().bottom, top:bounds.top, firstBody:first?.body.filter(r=>r.top>=bounds.top-1 && r.bottom<=bounds.bottom+1), firstText:Math.min(...visible.map(r=>r.top)), sliced:visible.filter(r=>r.top < bounds.top-1), lineHeight:parseFloat(getComputedStyle(transcript).lineHeight), scrollTop:transcript.scrollTop};
    });
    assert.equal(evidence.label, evidence.source ? evidence.source + ' · continued' : 'Today', JSON.stringify(evidence));
    assert.equal(evidence.continued, !!evidence.source);
    assert.ok(evidence.headingBottom <= evidence.top + 1);
    if (evidence.continued) assert.ok(evidence.firstBody.length, JSON.stringify(evidence));
    if (automatic) {
        assert.equal(evidence.sliced.length, 0, JSON.stringify(evidence));
        assert.ok(evidence.firstText >= evidence.top - 1 && evidence.firstText <= evidence.top + evidence.lineHeight, JSON.stringify(evidence));
    }
    checked('01-wing' + suffix + '-report-source', evidence);
}
async function checkWingSourceScrolling(page, suffix) {
    const original = await page.locator('.pp-conversation').evaluate(el => el.scrollTop);
    const sources = new Set();
    const rows = await page.locator('.pp-conversation [data-source]').count();
    for (let i=0; i<rows; i++) {
        for (const inset of [0, 20, 'body']) {
            const position = await page.locator('.pp-conversation').evaluate((el, {i,inset}) => {
                const row=el.querySelectorAll('[data-source]')[i];
                const offset=inset==='body' ? row.querySelector('.pp-say').getBoundingClientRect().top-row.getBoundingClientRect().top : inset;
                const target=el.scrollTop+row.getBoundingClientRect().top-el.getBoundingClientRect().top+offset;
                el.scrollTop=target;
                return {reachable:Math.abs(el.scrollTop-target)<1, top:el.scrollTop};
            }, {i,inset});
            if (!position.reachable) continue;
            await page.waitForTimeout(60);
            assert.equal(await page.locator('.pp-conversation').evaluate(el=>el.scrollTop),position.top);
            await checkWingSource(page, suffix + '-scroll-' + i + '-' + inset);
            const label = page.locator('[data-thread-source]');
            if (!inset) assert.equal(await label.getAttribute('data-continued'), null);
            if (await label.getAttribute('data-continued') !== null) sources.add(await label.innerText());
        }
    }
    assert.ok(sources.size >= 2);
    await page.locator('.pp-conversation').evaluate((el, top) => {el.scrollTop=top;}, original);
    await page.waitForTimeout(60);
}
async function checkStatusMarkers(page, name, suffix) {
    const selector = name==='01-wing' ? '.pp-conversation .pp-who .pp-st' : '.pp-srow .pp-st';
    const evidence = await page.locator(selector).evaluateAll(badges => badges.map(badge => {
        const box=badge.getBoundingClientRect(), marker=getComputedStyle(badge,'::before');
        const text=document.createRange();text.selectNodeContents(badge);const label=text.getBoundingClientRect();
        return {label:badge.textContent, marker:marker.content, visibility:getComputedStyle(badge).visibility, display:getComputedStyle(badge).display, markerWidth:parseFloat(marker.width), markerHeight:marker.lineHeight, markerVisibility:marker.visibility, top:box.top, bottom:box.bottom, left:box.left, right:box.right, textLeft:label.left, textRight:label.right};
    }));
    const cdp = await page.context().newCDPSession(page);
    try {
        const {root} = await cdp.send('DOM.getDocument');
        const {nodeIds} = await cdp.send('DOM.querySelectorAll',{nodeId:root.nodeId,selector});
        for (const [index,nodeId] of nodeIds.entries()) {
            const {node} = await cdp.send('DOM.describeNode',{nodeId});
            const marker = node.pseudoElements.find(n=>n.pseudoType==='before');
            const {model} = await cdp.send('DOM.getBoxModel',{backendNodeId:marker.backendNodeId});
            evidence[index].markerBounds=model.content;
        }
    } finally { await cdp.detach(); }
    assert.ok(evidence.length >= 3);
    for (const badge of evidence) {
        assert.equal(badge.visibility,'visible'); assert.equal(badge.markerVisibility,'visible');
        assert.ok(badge.markerBounds[0]>=badge.left-1 && badge.markerBounds[2]<=badge.right+1 && badge.markerBounds[1]>=badge.top-1 && badge.markerBounds[5]<=badge.bottom+1, JSON.stringify(badge));
        assert.ok(badge.markerWidth>0 && badge.textLeft-badge.left>=badge.markerWidth-1 && badge.textRight<=badge.right+1,JSON.stringify(badge));
        if (badge.label==='Working') assert.equal(badge.marker,'"•"');
        if (badge.label==='Needs you') assert.equal(badge.marker,'"!"');
    }
    checked(name+suffix+'-status-markers',evidence);
}
async function checkConversation(page, name, suffix) {
    if (process.env.WT_LOOK_LEGACY) return;
    const identity = await page.locator('.pp-hdr').innerText();
    assert.ok(identity.includes('Studio') && identity.includes('Claude'));
    if(name === '03-session') {
        assert.ok(identity.includes('migrate-sessions-table'));
        if(page.viewportSize().width===390) assert.equal(await page.locator('.pp-terminal').evaluate(el=>el.open),false);
    }
    const geometry = await page.evaluate(() => {
        const rect = selector => {const r=document.querySelector(selector).getBoundingClientRect();return {top:r.top,bottom:r.bottom};};
        return {header:rect('.pp-hdr'), composer:rect('.pp-composer'), transcript:rect('.pp-conversation'), height:innerHeight, scrollY};
    });
    assert.ok(geometry.header.top>=0 && geometry.header.bottom<geometry.height);
    assert.ok(geometry.composer.top>=geometry.header.bottom && geometry.composer.bottom<=geometry.height-(page.viewportSize().width===390 ? 50 : 0));
    assert.ok(geometry.transcript.bottom<=geometry.composer.top+1);
    if (name === '01-wing') await checkWingSource(page, suffix, true);
    if (name === '03-session' && page.viewportSize().width === 1440) {
        assert.equal(await page.locator('.pp-terminal summary').count(), 0);
        assert.ok(await page.getByRole('button',{name:'Take over terminal',exact:true}).isVisible());
        if (await page.locator('.pp-conversation .pp-row').count() === 3) {
            const layout = await page.evaluate(() => {
                const messages=document.querySelector('.pp-conversation .pp-kick').getBoundingClientRect();
                const terminal=document.querySelector('.pp-terminal .pp-kick').getBoundingClientRect();
                const last=document.querySelector('.pp-conversation .pp-row:last-child').getBoundingClientRect();
                const composer=document.querySelector('.pp-composer').getBoundingClientRect();
                const row=document.querySelector('.pp-row-you'), who=row.querySelector('.pp-who').getBoundingClientRect();
                return {messages:messages.top,terminal:terminal.top,gap:composer.top-last.bottom,inset:who.left-row.getBoundingClientRect().left};
            });
            assert.equal(layout.messages,layout.terminal, JSON.stringify(layout));
            assert.ok(layout.gap>=0 && layout.gap<=32, JSON.stringify(layout));
            assert.ok(layout.inset>=12, JSON.stringify(layout));
            checked(name + suffix + '-short-desktop-layout', layout);
        }
    }
    checked(name + suffix + '-identity-and-composer', {identity,...geometry});
}
async function checkConversationRefresh(page, name, suffix) {
    if(process.env.WT_LOOK_LEGACY) return;
    const id=name==='01-wing' ? 'wing' : 'migrate', original=events[id].slice();
    const append = text => events[id].push(msg(Math.max(...events[id].map(e=>e.sequence))+1, 'assistant', text, 0));
    const advance = () => page.evaluate(() => { const now=Date.now; Date.now=()=>now()+11000; });
    try {
        for(let i=0;i<9;i++) append('Earlier progress receipt ' + i + '. The session is continuing its current task. This example update provides enough history to read back through the conversation without losing the machine identity.');
        await advance();
        await page.locator('.pp-conversation .pp-say').filter({hasText:'Earlier progress receipt 8.'}).waitFor({state:'attached'});
        const before = await page.locator('.pp-conversation').evaluate(el=>{el.scrollTop=37;return {top:el.scrollTop,height:el.clientHeight,content:el.scrollHeight};});
        assert.ok(before.content>before.height+48);
        append('New progress receipt while reading earlier messages.');await advance();
        await page.locator('.pp-conversation .pp-say').filter({hasText:'New progress receipt while reading earlier messages.'}).waitFor({state:'attached'});
        assert.equal(await page.locator('.pp-conversation').evaluate(el=>el.scrollTop),before.top);
        await page.locator('.pp-conversation').evaluate(el=>{el.scrollTop=el.scrollHeight;});
        append('Latest progress receipt at the end of the conversation.');await advance();
        await page.locator('.pp-conversation .pp-say').filter({hasText:'Latest progress receipt at the end of the conversation.'}).waitFor({state:'attached'});
        const after = await page.locator('.pp-conversation').evaluate(el=>({top:el.scrollTop,height:el.clientHeight,content:el.scrollHeight}));
        assert.ok(after.content-after.top-after.height<=2);
        await checkConversation(page,name,suffix);
        if(name==='03-session') {
            if(page.viewportSize().width===390) await page.locator('.pp-terminal summary').click();
            const takeover=page.getByRole('button',{name:'Take over terminal',exact:true});
            await takeover.scrollIntoViewIfNeeded();assert.ok(await takeover.isVisible());
            if(page.viewportSize().width===390) await page.locator('.pp-terminal summary').click();
            await page.evaluate(()=>{location.hash='#sessions';});
            await page.locator('.pp-srow[href="#session/studio/migrate"]').click();
            await page.getByRole('heading',{name:'migrate-sessions-table',exact:true}).waitFor();
            await checkConversation(page,name,suffix+'-from-sessions');
        }
        if(name==='01-wing') {
            const latest = await page.locator('.pp-say').filter({hasText:'Latest progress receipt at the end of the conversation.'}).boundingBox();
            assert.ok(latest.y >= 0 && latest.y+latest.height <= page.viewportSize().height);
            const action = await page.getByRole('link',{name:'Answer in terminal',exact:true}).boundingBox();
            const viewport = await page.locator('.pp-conversation').boundingBox();
            assert.ok(action.y >= viewport.y && action.y+action.height <= viewport.y+viewport.height);
            await page.screenshot({path:path.join(shots, 'follow-latest'+suffix+'.png')});
            const dimensions = await page.evaluate(() => ({viewport:innerWidth,width:Math.max(document.documentElement.scrollWidth,document.body.scrollWidth)}));
            captures.push({file:'follow-latest'+suffix+'.png',url:page.url(),...dimensions});
            if (dimensions.viewport===390 && dimensions.width>390) overflow.push({file:'follow-latest'+suffix+'.png',...dimensions});
            await checkWingSourceScrolling(page,suffix);
        }
        checked(name+suffix+'-refresh-position', {before,after,readerStayed:true,followLatest:true});
    } finally {
        events[id]=original;
        // A full navigation discards the appended client thread and its cursor.
        // Resetting the server array alone would leave test updates cached.
        await go(page, name==='01-wing' ? '' : '#session/studio/migrate');
    }
}
async function go(page, hash, hashOnly = false) {
    if (hashOnly) await page.evaluate(hash => { location.hash = hash; }, hash);
    else await page.goto(base + '/app/' + hash);
    await page.waitForLoadState('networkidle').catch(() => {});
    await page.waitForTimeout(shellMode ? 100 : 900);
    if (shellMode) {
        const count=[...sockets.keys()].reduce((total,id)=>total+machines[id].rows.filter(s=>!(s.pinned && s.role==='coordinator')).length,0);
        await page.waitForFunction(expected => document.querySelector('.pp-nav-sessions small')?.textContent === String(expected.sessions || '') && document.querySelector('.pp-nav-machines small')?.textContent === String(expected.machines || ''), {sessions:count,machines:sockets.size});
        await page.waitForFunction(expected => document.querySelectorAll('.pp-side-squares .pp-sq-connected').length === expected, Math.min(sockets.size, 3));
    }
}

// R1 runs the production bundle against the existing encrypted-machine fixture.
async function assertShell(page, label) {
    await page.locator('#paper').waitFor({state:'visible'});
    const result = await page.evaluate(() => {
        const box = selector => { const r=document.querySelector(selector).getBoundingClientRect(); return {x:r.x,y:r.y,width:r.width,height:r.height}; };
        return {hash:location.hash,sessions:document.querySelector('.pp-nav-sessions small')?.textContent,machines:document.querySelector('.pp-nav-machines small')?.textContent,paper:box('#paper'),nav:box(innerWidth===390?'.pp-tabs':'.pp-side'),old:['#sidebar','#main-header','#home-section'].map(box),width:innerWidth,height:innerHeight,scrollWidth:document.documentElement.scrollWidth};
    });
    assert.ok(result.paper.width>0 && result.paper.height>0,JSON.stringify(result));
    const n=result.nav;
    assert.ok(n.width>0 && n.height>0 && n.x>=0 && n.y>=0 && n.x+n.width<=result.width+1 && n.y+n.height<=result.height+1,JSON.stringify(result));
    for (const old of result.old) assert.equal(old.width*old.height,0,JSON.stringify(result));
    assert.ok(result.scrollWidth<=result.width,JSON.stringify(result));
    checked(label,result);
}
async function assertDisclosureRefresh(page, disclosure, label) {
    const patchSource = fs.readFileSync(path.join(root,'web/src/shell/patch.js'),'utf8').replace('export function patchRegion','function patchRegion');
    const results = [];
    for (const open of [true, false]) {
        await disclosure.locator('summary').click();
        assert.equal(await disclosure.evaluate(node => node.open), open);
        // Exercise the production patcher on the mounted view's default-closed
        // markup directly, keeping this regression independent of inventory polls.
        const result = await disclosure.evaluate((node, source) => {
            const region = node.parentElement, next = region.cloneNode(true);
            next.querySelectorAll('details').forEach(details => details.removeAttribute('open'));
            const title = next.querySelector('details a b') || next.querySelector('details a');
            title.textContent += ' updated';
            node.querySelector('summary').focus();
            const scroll = document.querySelector('.pp-main').scrollTop;
            const patch = eval(source+'; patchRegion');
            patch(region, next.innerHTML);
            return {
                retained: node === region.querySelector('details'),
                open: node.open,
                focused: document.activeElement === node.querySelector('summary'),
                scrollRetained: document.querySelector('.pp-main').scrollTop === scroll,
                updated: region.textContent.includes(title.textContent),
            };
        }, patchSource);
        assert.deepEqual(result, {retained:true, open, focused:true, scrollRetained:true, updated:true});
        results.push(result);
        if (open) await shoot(page, label, '-'+page.viewportSize().width);
    }
    checked(label+'-'+page.viewportSize().width, results);
}
async function assertSessionGroupRefresh(page) {
    const patchSource = fs.readFileSync(path.join(root, 'web/src/shell/patch.js'), 'utf8').replace('export function patchRegion', 'function patchRegion');
    const result = await page.locator('.pp-col-main').evaluate((region, source) => {
        const patch = eval(source + '; patchRegion');
        const group = title => [...region.querySelectorAll('.pp-group')].find(node => node.querySelector('.pp-kick span').textContent === title);
        const complete = region.innerHTML;
        group('Needs you').remove();
        const withoutEarlier = region.innerHTML;
        const working = group('Working'), row = working.querySelector('.pp-srow');
        const disclosure = working.querySelector('details');
        disclosure.open = true;
        row.focus();
        const selection = getSelection(), text = row.querySelector('b').firstChild;
        selection.setBaseAndExtent(text, 0, text, 4);
        const selected = selection.toString();
        let blurs = 0;
        row.addEventListener('blur', () => blurs++);
        const observe = () => ({
            groupRetained: group('Working') === working,
            rowRetained: group('Working').querySelector('.pp-srow') === row,
            focused: document.activeElement === row,
            disclosureRetained: group('Working').querySelector('details') === disclosure,
            expanded: disclosure.open,
            selectionRetained: selection.anchorNode === text && selection.toString() === selected,
            blurs,
        });
        patch(region, complete);
        const inserted = observe();
        patch(region, withoutEarlier);
        const removed = observe();
        patch(region, complete);
        return { inserted, removed, restored: observe() };
    }, patchSource);
    const expected = {groupRetained:true, rowRetained:true, focused:true, disclosureRetained:true, expanded:true, selectionRetained:true, blurs:0};
    assert.deepEqual(result, {inserted:expected, removed:expected, restored:expected});
    checked('session-group-refresh-' + page.viewportSize().width, result);
    // Discard the probe's local disclosure state before the navigation checks.
    await go(page, '#sessions');
}
async function assertSessionRowRefresh(page) {
    const patchSource = fs.readFileSync(path.join(root, 'web/src/shell/patch.js'), 'utf8').replace('export function patchRegion', 'function patchRegion');
    const results = await page.locator('.pp-col-main').evaluate((region, source) => {
        const patch = eval(source + '; patchRegion');
        const sample = region.querySelector('[data-k="session-group/work"]').cloneNode(true);
        const rows = [...sample.querySelectorAll('.pp-srow')];
        if (rows.length < 10) throw Error('Boundary fixture requires ten working sessions');
        const arrival = rows[0].cloneNode(true);
        arrival.dataset.k = 'studio/earlier-arrival';
        arrival.href = '#session/studio/earlier-arrival';
        const markup = list => {
            const group = sample.cloneNode(true);
            group.querySelectorAll('.pp-srow, details').forEach(node => node.remove());
            group.querySelector('[data-group-count]').textContent = String(list.length);
            group.append(...list.slice(0, 8).map(row => row.cloneNode(true)));
            if (list.length > 8) {
                const details = document.createElement('details');
                details.className = 'pp-more-sessions';
                details.innerHTML = '<summary>+' + (list.length - 8) + ' more</summary>';
                details.append(...list.slice(8).map(row => row.cloneNode(true)));
                group.append(details);
            }
            return group.outerHTML;
        };
        const results = [];
        for (const [name, size, focusedIndex, open, change] of [
            ['create-overflow', 8, 7, false, list => [arrival, ...list]],
            ['closed-overflow', 10, 7, false, list => [arrival, ...list]],
            ['open-overflow', 10, 7, true, list => [arrival, ...list]],
            ['remove-preview', 10, 8, true, list => list.slice(1)],
            ['reordered-inventory', 10, 7, true, list => list.slice().reverse()],
        ]) {
            const before = rows.slice(0, size);
            // Mount a fresh production group before each independent probe.
            region.innerHTML = markup(before);
            const group = region.firstElementChild;
            const row = group.querySelectorAll('.pp-srow')[focusedIndex];
            const details = group.querySelector('details');
            if (details) details.open = open;
            const originals = [...group.querySelectorAll('.pp-srow')].map(node => ({node, parent:node.parentElement}));
            const changed = change(before);
            const survivors = originals.filter(({node}) => changed.some(next => next.dataset.k === node.dataset.k));
            row.focus();
            const selection = getSelection(), text = row.querySelector('b').firstChild;
            selection.setBaseAndExtent(text, 0, text, 4);
            const selected = selection.toString(), scroll = document.querySelector('.pp-main').scrollTop;
            let blurs = 0;
            row.addEventListener('blur', () => blurs++);
            for (const [step, list] of [['changed', changed], ['restored', before]]) {
                patch(region, markup(list));
                const retained = survivors;
                results.push({
                    name, step,
                    focused: document.activeElement === row,
                    sameRowsAndParents: retained.every(({node, parent}) => node.isConnected && node.parentElement === parent),
                    sameOrder: retained.every(({node}, i) => !i || !!(retained[i-1].node.compareDocumentPosition(node) & Node.DOCUMENT_POSITION_FOLLOWING)),
                    selectionRetained: selection.anchorNode === text && selection.toString() === selected,
                    scrollRetained: document.querySelector('.pp-main').scrollTop === scroll,
                    disclosureRetained: !details || group.querySelector('details') === details && details.open === open,
                    inventoryMatches: group.querySelectorAll('.pp-srow').length === list.length && list.every(next => [...group.querySelectorAll('.pp-srow')].some(node => node.dataset.k === next.dataset.k)),
                    blurs,
                });
            }
        }
        return results;
    }, patchSource);
    for (const result of results) assert.deepEqual(result, {
        name:result.name, step:result.step, focused:true, sameRowsAndParents:true, sameOrder:true,
        selectionRetained:true, scrollRetained:true, disclosureRetained:true, inventoryMatches:true, blurs:0,
    });
    checked('session-row-boundary-refresh-' + page.viewportSize().width, results);
    await go(page, '#sessions');
}
async function runShell(browser) {
    const original=machines.studio.rows.slice(), pages=[], contexts=[], errors=[];
    for (const viewport of [{width:1440,height:900},{width:390,height:844}]) {
        const context=await browser.newContext({viewport}); contexts.push(context);
        const page=await context.newPage(); pages.push(page);
        page.on('pageerror',error=>{ errors.push(String(error)); console.error('PAGEERROR '+error); });
        page.on('dialog',dialog=>{ errors.push('Unexpected dialog: '+dialog.message()); dialog.dismiss(); });
        await page.route('**/*',route=>new URL(route.request().url()).origin===base?route.continue():route.abort());
        // The roost starts a local wing too. Keep this scenario's inventory
        // limited to its synthetic machines, including a true zero state.
        await page.route('**/api/app/wings', async route => {
            const response=await route.fetch();
            const entries=await response.json();
            await route.fulfill({response,json:entries.filter(w=>Object.hasOwn(machines,w.wing_id))});
        });
    }
    if (shellMode==='routes') for (const page of pages) {
        await go(page,'#'); await assertShell(page,'zero-machines-'+page.viewportSize().width);
        await shoot(page,'shell-volume-0','-'+page.viewportSize().width);
    }
    machines.studio.rows=[original[2]]; await machine('studio');
    for (const page of pages) {
        await go(page,'#sessions'); await page.locator('.pp-srow[href="#session/studio/migrate"]').waitFor();
        if (shellMode==='routes') await shoot(page,'shell-volume-1','-'+page.viewportSize().width);
    }
    if (shellMode==='routes' || shellMode==='refresh') {
        machines.studio.rows=[...original,...Array.from({length:18},(_,i)=>row('long-'+i,'Improve organization invitation recovery for interrupted workspace migration '+(i+1),'claude',i<9?'blocked':'working','wingthing'))];
        if (shellMode==='routes') for (const id of ['macbook','mini']) await machine(id);
        // Change fixture inventories across a full load, so the assertion
        // does not depend on the application's thirty-second data poll.
        for (const page of pages) await go(page,'#sessions');
        if (shellMode==='routes') for (let i=0;i<5;i++) {
            const id='research-'+i; machines[id]={name:'Research workstation for extended migration projects '+(i+1),rows:[]}; await machine(id);
            if (i===2) for (const page of pages) {
                await go(page,'#machines'); await page.locator('[data-machine="research-2"]').waitFor();
                await shoot(page,'shell-volume-plan-limit-6','-'+page.viewportSize().width);
            }
        }
        for (const page of pages) await go(page,'#sessions');
        const routes=['#','#sessions','#machines','#account','#new','#s/redirect?wing=studio','#chat/redirect?wing=studio','#w/studio','#bogus','#new/studio','#machine/studio','#approve/studio/redirect','#needs','#decisions','#session/studio/migrate','#%broken'];
        for (const page of pages) {
            if (shellMode==='refresh') {
                await assertSessionGroupRefresh(page);
                await assertSessionRowRefresh(page);
                continue;
            }
            for (const [index,hash] of routes.entries()) {
                // Route through the app as a user does. Reopening all eight
                // tunnels on every hash would exhaust the roost's WS burst.
                await go(page,hash,true); await assertShell(page,hash+'-'+page.viewportSize().width);
                const expected=/^#(s\/|chat\/|approve\/)/.test(hash)?'#session/studio/redirect':hash==='#w/studio'?'#machine/studio':['#','#bogus','#needs','#decisions','#%broken'].includes(hash)?'':hash;
                assert.equal(await page.evaluate(()=>location.hash),expected);
                await shoot(page,'shell-route-'+index,'-'+page.viewportSize().width);
            }
            await go(page,'#sessions');
            await assertSessionGroupRefresh(page);
            await assertSessionRowRefresh(page);
            await go(page,'#');
            await page.locator('#pp-needs .pp-more').click();
            await page.locator('#pp-filter').waitFor();
            assert.equal(await page.evaluate(()=>location.hash),'#sessions');
            const waiting = page.locator('.pp-group').filter({has:page.locator('.pp-kick', {hasText:'Needs you'})});
            assert.equal(await waiting.locator('.pp-srow').count(),10);
            const waitingOverflow = waiting.locator('details.pp-more-sessions');
            const waitingRow = waitingOverflow.locator('.pp-srow').last();
            const waitingHref = await waitingRow.getAttribute('href');
            await assertDisclosureRefresh(page,waitingOverflow,'shell-waiting-expanded');
            await waitingOverflow.locator('summary').click();
            await waitingRow.click();
            await page.waitForFunction(hash=>location.hash===hash,waitingHref);
            await assertShell(page,'home-overflow-answer-'+page.viewportSize().width);
            checked('home-overflow-reachable-'+page.viewportSize().width,{waiting:10,opened:waitingHref});
            await go(page,'#machines');
            await assertDisclosureRefresh(page,page.locator('[data-machine="studio"] details.pp-more-sessions'),'shell-machines-expanded');
            await go(page,'#sessions'); const input=page.locator('#pp-filter'); await input.fill('migration'); await input.focus();
            await page.evaluate(()=>{ window.savedFilter=document.querySelector('#pp-filter'); window.savedHeader=document.querySelector('.pp-hdr'); window.savedFirst=document.querySelector('.pp-srow'); });
            await page.waitForTimeout(2200);
            assert.ok(await page.evaluate(()=>window.savedFilter===document.activeElement && window.savedHeader===document.querySelector('.pp-hdr') && window.savedFirst===document.querySelector('.pp-srow')));
            assert.equal(await input.inputValue(),'migration'); checked('retained-nodes-'+page.viewportSize().width,'Filter, header and row retained across data ticks');
            await page.evaluate(()=>document.querySelector('#home-btn').click()); await assertShell(page,'showHome-'+page.viewportSize().width);
            assert.equal(await page.evaluate(()=>location.hash),'');
            await page.evaluate(()=>{location.hash='#machines';}); await page.waitForTimeout(150);
            await page.evaluate(()=>{location.hash='#account';}); await page.waitForTimeout(150);
            await page.goBack(); await page.waitForTimeout(150); await assertShell(page,'back-'+page.viewportSize().width);
            await page.goForward(); await page.waitForTimeout(150); await assertShell(page,'forward-'+page.viewportSize().width);
        }
    } else if (shellMode==='launch') {
        for (const page of pages) {
            await go(page,'#new/studio'); await page.getByRole('button',{name:'Choose project',exact:true}).click();
            await page.locator('#palette-search').fill('/Users/example/code/wingthing');
            await page.waitForTimeout(500);
            await page.locator('.palette-item[data-path="/Users/example/code/wingthing"]').click();
            await page.waitForFunction(()=>/^#session\//.test(location.hash)); await page.locator('#paper .xterm').waitFor({state:'visible'});
            await assertShell(page,'launch-'+page.viewportSize().width);
            assert.ok(await page.evaluate(()=>document.querySelector('#paper .pp-live').contains(document.querySelector('#terminal-section'))));
            await shoot(page,'shell-started','-'+page.viewportSize().width);
            await go(page,'#session/studio/migrate');
            if (page.viewportSize().width===390) await page.locator('.pp-terminal').evaluate(el=>el.open=true);
            await page.getByRole('button',{name:'Take over terminal',exact:true}).click();
            await page.locator('#paper .pp-live .xterm').waitFor({state:'visible'}); await assertShell(page,'takeover-'+page.viewportSize().width);
            await page.evaluate(()=>{location.hash='#sessions';}); await page.locator('#pp-filter').waitFor();
            assert.equal(await page.locator('#paper #terminal-section').count(),0);
        }
    } else throw Error('Unknown shell fixture mode');
    assert.deepEqual(errors,[]); checked('no-page-errors-or-dialogs',errors);
    for (const context of contexts) await context.close();
}

// A real one-time invitation from a second, non-dev roost's own wing (the
// same setup as browser-join.mjs), compared but never confirmed: the terminal
// never gets Enter, so nothing is paired.
const joinState = fs.mkdtempSync(path.join(tmpdir(), 'wt'));
const joinBase = 'http://127.0.0.1:' + (port + 1);
const joinEnv = { PATH: process.env.PATH, TMPDIR: process.env.TMPDIR, HOME: path.join(state, 'join-home'), WINGTHING_DIR: joinState, WT_BASE_URL: joinBase };
fs.mkdirSync(joinEnv.HOME);
fs.writeFileSync(path.join(joinState, 'wing.yaml'), 'label: Studio\n', { mode: 0o600 });
let joinRoost;
async function joinShot(page, suffix) {
    if (!joinRoost) {
        joinRoost = spawn('nice', ['-n', '10', process.env.WT_LOOK_BIN || path.join(root, 'wt'), 'roost', 'start', '--foreground', '--addr', '127.0.0.1:' + (port + 1)], { cwd: root, env: joinEnv, stdio: ['ignore', log, log] });
        for (let i = 0; i < 150; i++) { try { if ((await fetch(joinBase + '/health')).ok) break; } catch {} await sleep(100); }
        for (let i = 0; i < 150 && !fs.existsSync(path.join(joinState, 'phone-control.json')); i++) await sleep(100);
        if (!fs.existsSync(path.join(joinState, 'phone-control.json'))) throw Error('Pair fixture control did not become ready');
    }
    const child = spawn('nice', ['-n', '10', process.env.WT_LOOK_BIN || path.join(root, 'wt'), 'pair', '--show-link'], { cwd: root, env: joinEnv, stdio: ['pipe', 'pipe', 'pipe'] });
    let output = '', errors = '', link;
    child.stdout.on('data', b => output += b); child.stderr.on('data', b => errors += b);
    try {
        for (let i = 0; i < 100 && !link; i++) { link = output.match(/wingthing:\/\/pair#v1z\.[A-Za-z0-9_-]+/)?.[0]; if (!link) await sleep(100); }
        if (!link) throw Error('wt pair did not issue a link: ' + errors + ' (exit ' + child.exitCode + ')');
        await page.goto(joinBase + '/app/#pair');
        await page.getByRole('heading', {name:'Join from this browser'}).waitFor();
        await page.locator('#bp-link').fill(link);
        await page.locator('#bp-name').fill('Browser');
        await page.getByRole('button', {name:'Check invitation'}).click();
        await page.getByRole('button', {name:'Match · Join'}).waitFor({ timeout: 15000 });
        await shoot(page, '05-pair-match', suffix);
        await page.getByRole('button', {name:'Cancel', exact:true}).click();
    } finally { child.kill('SIGINT'); }
}

const viewports = [{ width: 1440, height: 900, suffix: '-desktop' }, { width: 390, height: 844, suffix: '-phone' }];
let browser;
let complete = false;
try {
    if (shellMode) {
        // The child's readiness pipe proves this roost owns the listener,
        // even if another process claims the port between probing and spawn.
        for (let i = 0; i < 150 && roostReady !== 'ready\n'; i++) {
            if (roostStartupError || roost.exitCode !== null || roost.signalCode !== null) break;
            await sleep(100);
        }
        if (roostReady !== 'ready\n') throw Error('Fixture roost did not become ready: ' + (roostStartupError || fs.readFileSync(path.join(shots, 'fixture.log'), 'utf8')));
        checked('fixture-roost-ready', {base, pid:roost.pid});
    } else {
        for (let i = 0; i < 100; i++) { try { if ((await fetch(base + '/health')).ok) break; } catch {} await sleep(100); }
    }
    browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH, args: ['--disable-background-networking','--disable-component-update','--disable-default-apps','--disable-sync','--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE 127.0.0.1'] });
    if (shellMode) {
        await runShell(browser);
        complete = true;
    } else {
    const contexts = [];
    for (const v of viewports) contexts.push(await browser.newContext({ viewport: { width: v.width, height: v.height }, colorScheme: 'light' }));
    const dark = [{ width: 1440, height: 900, suffix: '-desktop-dark' }, { width: 390, height: 844, suffix: '-phone-dark' }];
    for (const v of dark) contexts.push(await browser.newContext({ viewport: { width: v.width, height: v.height }, colorScheme: 'dark' }));
    const themes = [...viewports, ...dark];
    // Public HTML routes in internal/relay/server.go and pages.go. /self-host
    // redirects to /docs; markdown and download endpoints are not HTML pages.
    for (let i = 0; i < 100; i++) { try { if ((await fetch(publicBase + '/health')).ok) break; } catch {} await sleep(100); }
    const publicPages = [['09-landing', '/'], ['10-install', '/install'], ['11-docs', '/docs'], ['12-patterns', '/patterns'], ['13-login', '/login'], ['14-terms', '/terms'], ['15-privacy', '/privacy'], ['16-abuse', '/abuse']];
    const publicOnly = only.length > 0 && only.every(name => publicPages.some(([page]) => page === name));
    for (const [index, v] of themes.entries()) {
        const page = await contexts[index].newPage();
        const videoRequests = [];
        page.on('request', request => {
            if (['/hero.mp4', '/app/demo/homepage.webm'].includes(new URL(request.url()).pathname)) videoRequests.push(request.url());
        });
        // Block external resources and navigation, including any account links.
        await page.route('**/*', route => new URL(route.request().url()).origin === publicBase ? route.continue() : route.abort());
        for (const [name, route] of publicPages) {
            if (only.length && !only.includes(name)) continue;
            const response = await page.goto(publicBase + route);
            if (!response?.ok()) throw Error('Public page failed: ' + route);
            if (new URL(page.url()).pathname !== route) throw Error('Public page redirected: ' + route);
            await page.evaluate(() => document.fonts.ready);
            await checkPublic(page, name, v.suffix);
            await shoot(page, name, v.suffix);
            if (name === '09-landing') {
                assert.deepEqual(videoRequests, [], 'Homepage requested video before interaction');
                checked('09-landing' + v.suffix + '-no-video-requests', 'Homepage rendered with no video requests');
            }
        }
        await page.close();
    }
    const appThemes = publicOnly ? [] : themes;
    // Empty state: signed in, no machine has connected yet.
    for (const [index, v] of appThemes.entries()) {
        const page = await contexts[index].newPage();
        await go(page, '');
        await shoot(page, '08-empty', v.suffix);
        await page.close();
    }
    for (const id of publicOnly ? [] : Object.keys(machines)) await machine(id);
    for (const [index, v] of appThemes.entries()) {
        const page = await contexts[index].newPage();
        page.on('pageerror', error => console.log('PAGEERROR ' + error));
        await go(page, '');
        await page.waitForSelector('[data-wing-id="studio"]', { timeout: 15000 }).catch(() => {});
        await page.waitForTimeout(1500);
        await checkConversation(page, '01-wing', v.suffix);
        await checkStatusMarkers(page,'01-wing',v.suffix);
        await shoot(page, '01-wing', v.suffix);
        await checkConversationRefresh(page,'01-wing',v.suffix);
        await go(page, '#sessions');
        await checkStatusMarkers(page,'02-sessions',v.suffix);
        await shoot(page, '02-sessions', v.suffix);
        await go(page, process.env.WT_LOOK_LEGACY ? '#chat/migrate?wing=studio' : '#session/studio/migrate');
        await checkConversation(page, '03-session', v.suffix);
        await shoot(page, '03-session', v.suffix);
        await checkConversationRefresh(page,'03-session',v.suffix);
        await go(page, process.env.WT_LOOK_LEGACY ? '#chat/redirect?wing=studio' : '#approve/studio/redirect');
        await shoot(page, '04-approve', v.suffix);
        if (!process.env.WT_LOOK_LEGACY) {
            await page.getByRole('heading', {name:'Answer in terminal',exact:true}).waitFor();
            await page.getByRole('button', {name:'Answer in terminal',exact:true}).waitFor();
            if (await page.locator('[data-answer]').count()) throw Error('browser offered an approval decision');
            if (sent.length) throw Error('browser guessed terminal input: ' + JSON.stringify(sent));
            const marks = await page.evaluate(() => document.querySelectorAll('#paper .pp-st-need, #paper .pp-hot').length);
            if (!marks) throw Error('terminal fallback incorrectly cleared attention');
            await shoot(page, '04-answer-in-terminal', v.suffix);
        }
        await go(page, '#pair');
        await page.getByRole('heading', {name:'Join from this browser'}).waitFor();
        const pairing = await page.evaluate(() => {
            const rect = selector => document.querySelector(selector).getBoundingClientRect().toJSON();
            return {command:rect('.bp-instruction pre'), invitation:rect('label[for="bp-link"]'), name:rect('#bp-name'), submit:rect('#bp-form button'), commands:document.querySelectorAll('.bp-layout pre').length};
        });
        assert.ok(pairing.command.bottom < pairing.invitation.top);
        assert.equal(pairing.commands, 1);
        if(v.width === 390 || page.viewportSize().width === 390) assert.ok(pairing.submit.bottom <= 844, JSON.stringify(pairing));
        checked('05-pair' + v.suffix + '-instruction-before-form', pairing);
        await shoot(page, '05-pair', v.suffix);
        // Leaving Pair by hash (Back or an app link) must load the app again.
        await go(page, '#machines');
        await page.getByRole('heading', {name:'Machines', exact:true}).waitFor({ timeout: 10000 });
        if (!process.env.WT_LOOK_LEGACY && (!only.length || only.includes('05-pair-match'))) await joinShot(page, v.suffix);
        await go(page, '#machines');
        await shoot(page, '07-machines', v.suffix);
        await page.close();
    }
    // Mini goes to sleep: its socket closes and the app keeps its last-seen rows.
    sockets.get('mini')?.close();
    if (!publicOnly) await sleep(1500);
    for (const [index, v] of appThemes.entries()) {
        const page = await contexts[index].newPage();
        await go(page, '#sessions');
        await page.waitForTimeout(1500);
        await shoot(page, '06-offline', v.suffix);
        await page.close();
    }
    // Phone app with no network: the worker's constant disconnected page.
    for (const [index, v] of appThemes.entries()) {
        const page = await contexts[index].newPage();
        await go(page, '');
        await page.evaluate(() => navigator.serviceWorker && navigator.serviceWorker.ready).catch(() => {});
        await contexts[index].setOffline(true);
        try { await page.reload({ timeout: 8000 }); await shoot(page, '06-pwa-offline', v.suffix); }
        catch (error) { throw Error('Offline capture failed: ' + v.suffix, { cause: error }); }
        await contexts[index].setOffline(false);
        await page.close();
    }
    const expected = only.length ? (only.length + (publicOnly ? 0 : 1)) * themes.length : 80;
    if (captures.length !== expected) throw Error('Incomplete capture: ' + captures.length + ' of ' + expected);
    complete = true;
    if (overflow.length) throw Error('Horizontal overflow in ' + overflow.length + ' phone captures');
    }
} finally {
    writeIndex(complete);
    if (browser) await browser.close();
    for (const socket of sockets.values()) try { socket.close(); } catch {}
    roost.kill('SIGTERM');
    if (joinRoost) joinRoost.kill('SIGTERM');
    publicServer?.kill('SIGTERM');
    await removeState(joinState);
    await removeState(state);
    fs.closeSync(log);
}
