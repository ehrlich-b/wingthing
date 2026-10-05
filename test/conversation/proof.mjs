// Actual browser tunnel -> egg -> injected stdio MCP -> two child eggs.
// The provider is a clearly labeled protocol fixture; no model credentials.
//
// --expect-nested-proxy-block: state inside the parent's writable workspace, so
//   the parent keeps the direct in-sandbox MCP server and the nested child egg
//   is denied its proxy bind (the regression this transport addresses).
// --state-root <dir>: Wingthing state in a fresh directory under <dir>, outside
//   /tmp and the workspace, with the OS-account HOME as the sandbox write-deny
//   root. The parent's injected client then reaches the host mailbox broker,
//   which launches both children outside the parent sandbox.
//
// The receiver binary is an explicit path (a fresh build or a frozen
// crossbuild), never a PATH lookup. It must be a regular executable reporting
// the preview channel; --expect-sha256 pins its exact bytes.
import assert from 'node:assert/strict';
import { generateKeyPairSync, diffieHellman, createPublicKey, hkdfSync, randomBytes, createCipheriv, createDecipheriv, createHash } from 'node:crypto';
import { spawn, execFile, execFileSync } from 'node:child_process';
import { mkdtemp, mkdir, copyFile, chmod, readFile, writeFile, realpath, lstat, readdir, rm, open } from 'node:fs/promises';
import { constants as fsConstants } from 'node:fs';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import path from 'node:path';

const supplied = process.argv[2] || '';
if (!supplied.includes('/')) throw new Error(`receiver binary must be an explicit path, not a PATH lookup: ${JSON.stringify(supplied)}`);
const binary = path.resolve(supplied);
const binaryInfo = await lstat(binary);
if (!binaryInfo.isFile() || (binaryInfo.mode & 0o111) === 0) throw new Error(`receiver ${binary} must be a regular executable file (symlinks refused)`);
const reportedVersion = execFileSync(binary, ['--version'], { env: { PATH: '/usr/bin:/bin', HOME: '/nonexistent' }, timeout: 10000 }).toString();
if (!/^wt-preview version \S+-preview\./m.test(reportedVersion)) throw new Error(`receiver ${binary} is not a preview-channel build: ${reportedVersion.trim()}`);
const binarySHA256 = createHash('sha256').update(await readFile(binary)).digest('hex');
const expectedSHAIndex = process.argv.indexOf('--expect-sha256');
if (expectedSHAIndex > 0 && process.argv[expectedSHAIndex + 1]?.toLowerCase() !== binarySHA256) throw new Error(`receiver ${binary} sha256 ${binarySHA256} differs from the frozen binary`);
const expectProxyBlock = process.argv.includes('--expect-nested-proxy-block');
const stateRootIndex = process.argv.indexOf('--state-root');
const stateRoot = stateRootIndex > 0 ? process.argv[stateRootIndex + 1] : '';
if (stateRootIndex > 0 && !stateRoot) throw new Error('--state-root requires a directory');
if (stateRoot && expectProxyBlock) throw new Error('--state-root and --expect-nested-proxy-block select different layouts');
const scratch = await mkdtemp('/tmp/wtc-');
const workspace = path.join(scratch, 'workspace');
const bin = path.join(scratch, 'bin');
let state = path.join(workspace, 'preview-state');
let home = path.join(scratch, 'provider-home');
if (stateRoot) {
    // Protected host state: inside the OS-account HOME deny root, never under a
    // temporary directory or the workspace the parent provider can write.
    home = await realpath(process.env.HOME || '');
    const root = await realpath(path.resolve(stateRoot));
    state = await mkdtemp(path.join(root, 'wtc-state-'));
    const temporary = await Promise.all(['/tmp', '/private/tmp', tmpdir()].map(dir => realpath(dir).catch(() => dir)));
    const inside = (parent, child) => child === parent || child.startsWith(parent + path.sep);
    if (temporary.some(dir => inside(dir, state)) || inside(await realpath(scratch), state)) throw new Error(`state ${state} must be outside temporary directories and the workspace`);
    if (!inside(home, state)) throw new Error(`state ${state} must be inside the OS-account HOME ${home}`);
}
for (const directory of [workspace, state, home, bin]) await mkdir(directory, { recursive: true, mode: 0o700 });
let providerData = '';
if (stateRoot) {
    // The host mailbox requires the provider data home D outside the protected
    // state S. A fake D in the scratch directory is selected through the
    // existing private binding file inside S; no real provider home is used.
    providerData = path.join(await realpath(scratch), 'provider-data');
    await mkdir(providerData, { mode: 0o700 });
    // Claim this freshly created fixture state before making it nonempty;
    // preview startup intentionally refuses unmarked nonempty directories.
    await writeFile(path.join(state, '.release-channel'), 'preview\n', { mode: 0o600, flag: 'wx' });
    await writeFile(path.join(state, '.provider-home'), providerData + '\n', { mode: 0o600, flag: 'wx' });
}
await copyFile(new URL('./fake_claude.py', import.meta.url), path.join(bin, 'claude'));
await chmod(path.join(bin, 'claude'), 0o700);
const allocator = createServer();
await new Promise(resolve => allocator.listen(0, '127.0.0.1', resolve));
const port = allocator.address().port;
await new Promise(resolve => allocator.close(resolve));
const env = { HOME: home, PATH: bin + ':/usr/bin:/bin:/usr/sbin:/sbin', WINGTHING_DIR: state, TMPDIR: process.env.TMPDIR || '/tmp', LANG: 'en_US.UTF-8', WT_BASE_URL: `http://127.0.0.1:${port}` };
const roost = spawn(binary, ['roost', 'start', '--foreground', '--addr', `127.0.0.1:${port}`, '--paths', workspace], { env, cwd: workspace, stdio: ['ignore', 'pipe', 'pipe'] });
let logs = '';
roost.stdout.on('data', data => { logs += data.toString(); });
roost.stderr.on('data', data => { logs += data.toString(); });
const base = `http://127.0.0.1:${port}`;
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
let socket;
let wing;
let user;
let sessions = [];
let proof;
let cleanup;
const runWT = args => new Promise((resolve, reject) => execFile(binary, args, { env, cwd: workspace, timeout: 30000 }, (error, stdout, stderr) => error ? reject(new Error(`${args.join(' ')}: ${error.message}\n${stderr}`)) : resolve(stdout)));
let finished = false;
let restartedBrokerPid;
async function waitFor(check, message, attempts = 100) {
    for (let i = 0; i < attempts && !finished; i++) {
        const value = await check().catch(() => undefined);
        if (value) return value;
        await delay(100);
    }
    throw new Error(message);
}
const processGone = pid => { try { process.kill(pid, 0); return false; } catch (error) { return error.code === 'ESRCH'; } };

// Host broker restart while the parent waits at its restart marker. The old
// broker stops; a mutation request stamped with its epoch is left in the
// mailbox (as if published just before the stop); a new broker process starts
// for the same registration. The new epoch must refuse that request as not
// dispatched, and nothing is redispatched automatically.
async function restartBroker(parentSession) {
    const marker = await waitFor(async () => (await readdir(workspace)).find(name => name.startsWith('.fixture-await-restart-')), 'parent never reached its broker restart point', 1500);
    const providerID = marker.slice('.fixture-await-restart-'.length);
    const brokerDir = path.join(state, 'conversation-brokers', parentSession);
    const status = JSON.parse(await runWT(['conversation', 'transport', parentSession, '--json']));
    assert.equal(status.host_ready, true, 'broker not ready before restart');
    const readyPath = path.join(status.mailbox, 'host-ready.json');
    const oldEpoch = JSON.parse(await readFile(readyPath, 'utf8')).epoch;
    const oldPid = Number((await readFile(path.join(brokerDir, 'broker.pid'), 'utf8')).trim());
    process.kill(oldPid, 'SIGTERM');
    await waitFor(async () => processGone(oldPid), `broker ${oldPid} did not stop`, 300);
    const staleID = randomBytes(16).toString('hex');
    const staleCall = { method: 'tools/call', params: { name: 'agent_start', arguments: { agent: 'claude', cwd: workspace, label: 'fixture-stale-epoch', request_id: 'fixture-stale-epoch' } } };
    await writeFile(path.join(status.mailbox, `request.${staleID}.json`), JSON.stringify({ version: 1, request_id: staleID, epoch: oldEpoch, created_at: Math.floor(Date.now() / 1000), payload: staleCall }), { mode: 0o600, flag: 'wx' });
    const log = await open(path.join(brokerDir, 'broker.log'), 'a', 0o600);
    const next = spawn(binary, ['conversation', 'broker', parentSession], { env, cwd: workspace, detached: true, stdio: ['ignore', log.fd, log.fd] });
    restartedBrokerPid = next.pid;
    next.unref();
    await log.close();
    const ready = await waitFor(async () => { const value = JSON.parse(await readFile(readyPath, 'utf8')); return value.host_ready && value.epoch !== oldEpoch && value; }, 'restarted broker published no new epoch', 300);
    const responsePath = path.join(status.mailbox, `response.${staleID}.json`);
    const stale = await waitFor(async () => JSON.parse(await readFile(responsePath, 'utf8')), 'stale-epoch request received no response', 300);
    assert.equal(stale.epoch, ready.epoch);
    assert.equal(stale.dispatched, false);
    assert.equal(stale.outcome, 'not_dispatched');
    assert.match(stale.error, /restarted before accepting/);
    await rm(responsePath);
    const newPid = Number((await readFile(path.join(brokerDir, 'broker.pid'), 'utf8')).trim());
    assert.equal(newPid, next.pid);
    await writeFile(path.join(workspace, '.fixture-restarted-' + providerID), '', { mode: 0o600 });
    return { old_pid: oldPid, new_pid: newPid, old_epoch: oldEpoch, new_epoch: ready.epoch, stale_epoch_request: { dispatched: stale.dispatched, outcome: stale.outcome } };
}

// The server pages lifecycle reads at 200 events; collect every page.
async function readAllLifecycle(control, session) {
    let after = 0, first = null, events = [];
    for (let page = 0; page < 50; page++) {
        const read = await control('session_read', { session, after_cursor: after, limit: 200 });
        first = first || read;
        events = events.concat(read.lifecycle.events);
        if (!read.lifecycle.has_more) break;
        after = read.lifecycle.cursor;
    }
    first.lifecycle.events = events;
    return first;
}

try {
    for (let i = 0; i < 100; i++) {
        try {
            user = await (await fetch(base + '/api/app/me')).json();
            const roster = await (await fetch(base + '/api/app/wings')).json();
            wing = (Array.isArray(roster) ? roster : roster.wings || [])[0];
            if (wing?.public_key) break;
        } catch {}
        await delay(100);
    }
    assert.ok(wing?.public_key, 'isolated local roost did not register its wing');
    const keys = generateKeyPairSync('x25519');
    const publicRaw = keys.publicKey.export({ type: 'spki', format: 'der' }).subarray(-32);
    const peer = createPublicKey({ key: Buffer.concat([Buffer.from('302a300506032b656e032100', 'hex'), Buffer.from(wing.public_key, 'base64')]), type: 'spki', format: 'der' });
    const shared = diffieHellman({ privateKey: keys.privateKey, publicKey: peer });
    const key = Buffer.from(hkdfSync('sha256', shared, Buffer.alloc(32), Buffer.from('wt-tunnel'), 32));
    function encrypt(value) {
        const iv = randomBytes(12);
        const cipher = createCipheriv('aes-256-gcm', key, iv);
        return Buffer.concat([iv, cipher.update(JSON.stringify(value)), cipher.final(), cipher.getAuthTag()]).toString('base64');
    }
    function decrypt(value) {
        const data = Buffer.from(value, 'base64');
        const decipher = createDecipheriv('aes-256-gcm', key, data.subarray(0, 12));
        decipher.setAuthTag(data.subarray(-16));
        return JSON.parse(Buffer.concat([decipher.update(data.subarray(12, -16)), decipher.final()]).toString());
    }
    socket = new WebSocket(`ws://127.0.0.1:${port}/ws/relay?wing_id=${wing.wing_id}`);
    await new Promise((resolve, reject) => { socket.addEventListener('open', resolve, { once: true }); socket.addEventListener('error', reject, { once: true }); });
    let sequence = 0;
    const pending = new Map();
    socket.addEventListener('message', event => {
        const message = JSON.parse(event.data);
        const waiter = pending.get(message.request_id);
        if (waiter && message.type === 'tunnel.res') { pending.delete(message.request_id); waiter.resolve(decrypt(message.payload)); }
    });
    async function request(inner) {
        const request_id = 'fixture-' + (++sequence);
        const result = await new Promise((resolve, reject) => {
            const timeout = setTimeout(() => { pending.delete(request_id); reject(new Error('fixture tunnel timeout')); }, 30000);
            pending.set(request_id, { resolve: value => { clearTimeout(timeout); resolve(value); } });
            socket.send(JSON.stringify({ type: 'tunnel.req', wing_id: wing.wing_id, request_id, purpose: 'wing-control', sender_pub: publicRaw.toString('base64'), payload: encrypt(inner) }));
        });
        if (result.error) throw new Error(result.error);
        return result;
    }
    const control = (operation, args) => request({ type: 'session.control', operation, arguments: args });
    cleanup = async () => {
        // Exact owned cleanup: this fresh roost's inventory holds only fixture
        // executions; anything outside the fixture workspace is left untouched.
        const inventory = await request({ type: 'sessions.list' });
        for (const item of inventory.sessions || []) {
            if (item.cwd && !(item.cwd === workspace || item.cwd.startsWith(workspace + path.sep))) { console.error('Fixture cleanup skipped non-fixture session', item.session_id); continue; }
            await request({ type: 'pty.kill', session_id: item.session_id });
        }
    };
    const launchArgs = { agent: 'claude', cwd: workspace, label: 'fixture-parent', conversation_role: 'parent', request_id: 'fixture-parent-intent' };
    const parent = await control('agent_start', launchArgs);
    sessions.push(parent.session);
    const replay = await control('agent_start', launchArgs);
    assert.equal(replay.session, parent.session);
    assert.equal(replay.reused, true);
    // Runs beside the wait below; settles to its proof or its error.
    const restart = stateRoot ? restartBroker(parent.session).catch(error => error) : null;
    let done = await control('session_wait', { session: parent.session, state: 'completed', timeout_seconds: expectProxyBlock ? 10 : 25 });
    for (let round = 0; !expectProxyBlock && !done.matched && done.lifecycle?.process_alive && round < 4; round++) {
        done = await control('session_wait', { session: parent.session, state: 'completed', timeout_seconds: 25 });
    }
    if (expectProxyBlock) {
        assert.equal(done.matched, false);
        assert.equal(done.lifecycle.state, 'failed');
        const blockedTree = await control('conversation_read', { conversation_id: parent.conversation_id });
        const child = blockedTree.tasks.find(task => task.conversation.parent_conversation_id === parent.conversation_id)?.conversation;
        assert.ok(child, 'bound MCP never reserved a linked child');
        const diagnostic = await readFile(path.join(state, 'eggs', child.session_id, 'egg.failed.log'), 'utf8');
        assert.match(diagnostic, /proxy listen: listen tcp4 127\.0\.0\.1:0: bind: operation not permitted/);
        const native = await control('session_read', { session: parent.session, after_cursor: 0, limit: 100 });
        assert.ok(native.lifecycle.events.some(event => event.text === 'FIXTURE_BOUND_MCP_INITIALIZED'));
        for (const session of [parent.session, child.session_id]) {
            assert.equal((await readFile(path.join(state, 'eggs', session, 'egg.owner'), 'utf8')).split('\n')[0], user.id);
        }
        proof = { provider: 'disposable native protocol fixture; no model invocation', automatic_parent_mcp_initialized: true, child_launch_reserved_by_bound_mcp: true, same_browser_owner: true, full_two_child_orchestration: false, blocker: 'nested child egg network proxy loopback bind denied by existing parent sandbox', diagnostic_path: path.join(state, 'eggs', child.session_id, 'egg.failed.log'), parent_conversation_id: parent.conversation_id, stable_or_org_state_touched: false, scratch };
        console.log(JSON.stringify(proof, null, 2));
        await writeFile(path.join(scratch, 'blocked-proof.json'), JSON.stringify(proof, null, 2), { mode: 0o600 });
    } else {
    if (!done.matched) {
        const failure = await readFile(path.join(workspace, '.fixture-error.json'), 'utf8').catch(() => 'no fixture error artifact');
        throw new Error('parent did not observe two children: ' + JSON.stringify(done.lifecycle) + '\n' + failure);
    }
    const restarted = restart && await restart;
    if (restarted instanceof Error) throw restarted;
    const tree = await control('conversation_read', { conversation_id: parent.conversation_id });
    assert.equal(tree.tasks.length, 3);
    const children = tree.tasks.filter(task => task.conversation.parent_conversation_id === parent.conversation_id);
    assert.equal(children.length, 2);
    sessions = tree.tasks.map(task => task.conversation.session_id);
    const native = await readAllLifecycle(control, parent.session);
    assert.ok(native.lifecycle.events.some(event => event.text === 'FIXTURE_PARENT_ORCHESTRATED_TWO_CHILDREN'));
    const evidenceText = native.lifecycle.events.map(event => event.text || '').find(text => text.startsWith('FIXTURE_EVIDENCE:'));
    assert.ok(evidenceText, 'parent recorded no orchestration evidence');
    const evidence = JSON.parse(evidenceText.slice('FIXTURE_EVIDENCE:'.length));
    assert.equal(evidence.transport, stateRoot ? 'host_mailbox' : 'direct_stdio');
    assert.equal(evidence.parent_conversation_id, parent.conversation_id);
    assert.deepEqual(evidence.children.map(child => child.session).sort(), children.map(task => task.conversation.session_id).sort());
    for (const child of evidence.children) {
        // Exactly one native delivery per child, including after the parent's
        // same-ID replay and its reconnected client's replay.
        const read = await readAllLifecycle(control, child.session);
        const suffix = child.prompt_request_id.slice(-1);
        const input = `fixture-work-${suffix}\nfixture second line ${suffix}`;
        assert.equal(read.lifecycle.events.filter(event => event.type === 'message' && event.role === 'user' && event.text === input).length, 1, `${child.session} received ${JSON.stringify(input)} other than exactly once`);
        assert.ok(read.lifecycle.events.some(event => event.text === 'fixture-result:' + input));
        assert.equal(read.lifecycle.provider_session_id, child.provider_session_id);
        // Raw-mode child: the two-line prompt arrived as one bracketed paste and
        // one submit, never as a second turn.
        const raw = JSON.parse(await readFile(path.join(workspace, `.fixture-input-${child.provider_session_id}.json`), 'utf8'));
        assert.equal(raw.tty, true);
        assert.equal(raw.icanon, false);
        assert.equal(raw.icrnl, false);
        assert.equal(raw.bracketed_paste_enabled, true);
        assert.deepEqual(raw.submissions, [input]);
    }
    let transport;
    let brokerPid;
    if (stateRoot) {
        transport = JSON.parse(await runWT(['conversation', 'transport', parent.session, '--json']));
        assert.equal(transport.session_id, parent.session);
        assert.equal(transport.conversation_id, parent.conversation_id);
        assert.equal(transport.principal, (await readFile(path.join(state, 'eggs', parent.session, 'session.principal'), 'utf8')).trim());
        assert.equal(transport.host_ready, true, 'host mailbox not ready while the parent is alive');
        assert.equal(transport.provider_session_id, evidence.parent_provider_session_id);
        assert.match(transport.trust, /same-owner workspace mailbox.*not a sealed caller transport/);
        const actor = `conversation:${parent.conversation_id}:execution:${parent.session}`;
        assert.equal(transport.audit_actor, actor);
        assert.deepEqual(evidence.restart, { launch_reused: true, prompt_retried_without_resend: true });
        assert.equal(restarted.new_pid, Number((await readFile(path.join(state, 'conversation-brokers', parent.session, 'broker.pid'), 'utf8')).trim()));
        // Host-side audit: every broker-dispatched call names this exact
        // execution and the captured principal. Launches and prompts: two
        // originals, two same-client replays, one after client reconnect and one
        // after broker restart. The stale-epoch request left no record.
        const audit = (await readFile(path.join(state, 'mcp-audit.log'), 'utf8')).trim().split('\n').map(line => JSON.parse(line));
        const brokered = audit.filter(record => String(record.actor || '').startsWith('conversation:'));
        assert.ok(brokered.every(record => record.actor === actor && record.principal === transport.principal), 'broker audit names another actor or principal');
        const count = (tool, decision) => brokered.filter(record => record.tool === tool && record.decision === decision).length;
        assert.equal(count('agent_start', 'allowed'), 6);
        assert.equal(count('session_prompt', 'allowed'), 6);
        assert.equal(count('conversation_checkpoint', 'allowed'), 1);
        assert.equal(count('terminal_send', 'error'), 1);
        assert.equal(count('session_status', 'error'), 1);
        assert.ok(!brokered.some(record => record.tool === 'agent_start' && record.decision !== 'allowed'), 'a brokered launch failed');
        assert.ok(!transport.tools.includes('terminal_send') && transport.tools.includes('session_prompt'));
        brokerPid = Number((await readFile(path.join(state, 'conversation-brokers', parent.session, 'broker.pid'), 'utf8')).trim());
        assert.ok(brokerPid > 0);
        assert.deepEqual(evidence.reconnect, { launch_reused: true, prompt_retried_without_resend: true });
    }
    const inventory = await request({ type: 'sessions.list' });
    for (const session of sessions) {
        const item = inventory.sessions.find(item => item.session_id === session);
        assert.ok(item, 'actual egg missing from browser inventory');
        assert.equal(item.user_id, user.id, 'child owner differs from browser parent');
        assert.equal(item.root_conversation_id, parent.conversation_id);
    }
    const child = children[0].conversation;
    await request({ type: 'sessions.rename', session_id: child.session_id, name: 'fixture-renamed-child' });
    const renamed = await request({ type: 'sessions.list' });
    assert.equal(renamed.sessions.find(item => item.session_id === child.session_id).name, 'fixture-renamed-child');
    const reread = await control('conversation_read', { conversation_id: parent.conversation_id });
    assert.equal(reread.next_cursor, tree.next_cursor, 'parent read replay created a duplicate state delivery');
    assert.match(reread.conversation.checkpoint, /both linked native child results/);
    // This fixture runs only against its disposable personal roost. Stop each
    // recorded egg explicitly; do not send lifecycle requests to stable state.
    for (const session of sessions) await request({ type: 'pty.kill', session_id: session });
    let brokerStopped = false;
    if (stateRoot) {
        // The broker serves only while its registered parent execution lives.
        const brokerLog = path.join(state, 'conversation-brokers', parent.session, 'broker.log');
        await waitFor(async () => (await readFile(brokerLog, 'utf8')).includes('wingthing host mailbox: parent execution ended'), 'host broker kept serving after its parent ended', 100);
        const after = JSON.parse(await runWT(['conversation', 'transport', parent.session, '--json']));
        assert.equal(after.host_ready, false);
        brokerStopped = true;
    }
    proof = { provider: 'disposable native protocol fixture; no model invocation', transport: evidence.transport, wing_id: wing.wing_id, browser_owner: user.id, principal: transport?.principal, parent_conversation_id: parent.conversation_id, parent_session_id: parent.session, parent_provider_session_id: evidence.parent_provider_session_id, children: evidence.children, broker_pid: brokerPid, broker_stopped_with_parent: brokerStopped, mailbox_trust: transport?.trust, session_ids: sessions, automatic_parent_mcp: true, child_native_results: 2, exactly_one_native_delivery_per_child: true, launch_and_prompt_replay_after_client_reconnect: Boolean(evidence.reconnect), broker_restart: restarted || null, launch_and_prompt_replay_after_broker_restart: Boolean(evidence.restart), raw_multiline_single_submit_per_child: true, host_audit_exact_execution_actor: Boolean(stateRoot), state_removed_on_success: Boolean(stateRoot), replay_created_no_duplicate: true, same_browser_owner: true, rename_from_browser: true, checkpoint_persisted: true, stable_or_org_state_touched: false, provider_data_home: providerData || null, scratch, state };
    console.log(JSON.stringify(proof, null, 2));
    await writeFile(path.join(scratch, 'proof.json'), JSON.stringify(proof, null, 2), { mode: 0o600 });
    }
} catch (error) {
    console.error(error.stack || error);
    console.error('Fixture scratch:', scratch);
    process.exitCode = 1;
} finally {
    finished = true;
    if (cleanup) { try { await cleanup(); } catch (error) { console.error('Disposable fixture cleanup:', error.message); process.exitCode = 1; } }
    if (socket) socket.close();
    // The fixture started this broker process itself; stop exactly that PID.
    if (restartedBrokerPid && !processGone(restartedBrokerPid)) process.kill(restartedBrokerPid, 'SIGTERM');
    roost.kill('SIGTERM');
    await writeFile(path.join(scratch, 'roost.log'), logs, { mode: 0o600 });
    await Promise.race([new Promise(resolve => roost.once('exit', resolve)), delay(3000)]);
    // The state directory this run created inside HOME is removed after a
    // passing run; a failing run keeps it (and the /tmp scratch) for diagnosis.
    if (stateRoot && process.exitCode !== 1 && path.basename(state).startsWith('wtc-state-')) await rm(state, { recursive: true, force: true });
    else if (stateRoot) console.error('Fixture state kept:', state);
}
