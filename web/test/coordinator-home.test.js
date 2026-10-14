import test from 'node:test';
import assert from 'node:assert/strict';
import { pinnedCoordinators, coordinatorHomeSummary } from '../src/coordinator-home-state.js';
import { emptyConversationState, applyConversationRead } from '../src/conversation-state.js';
import { sessionActivity, timelineMarkup, safeActivityLink } from '../src/session-activity.js';

const now = Date.parse('2026-10-13T02:00:00Z');
const wing = { wing_id: 'machine-a', online: true };
const session = { id: 'same-id', wing_id: wing.wing_id, name: 'wing', pinned: true, role: 'coordinator', lifecycle_seen_at: now };
const lifecycle = { process_alive: true, status: 'idle', state_cursor: 3, report_cursor: 3, reported_at: new Date(now).toISOString(), report: { goal: 'Finish the feature', update: 'Tests pass', next_action: 'Review screenshots', presence: 'asleep', last_wake: new Date(now - 30000).toISOString(), next_wake: new Date(now + 30000).toISOString() } };

test('only explicit pins lead Home and qualified colliding IDs remain separate', () => {
    const rows = [session, { ...session, wing_id: 'machine-b' }, { ...session, id: 'named-only', pinned: false }, { ...session, id: 'ordinary', role: '' }];
    assert.equal(pinnedCoordinators(rows, [wing, { wing_id: 'machine-b' }], { id: 'owner' }).length, 2);
    assert.equal(pinnedCoordinators(rows, [wing], { id: 'owner', relay_allowed: false }).length, 0);
    assert.equal(pinnedCoordinators(rows, [{ ...wing, tunnel_error: 'not_allowed' }], { id: 'owner' }).length, 0);
});

test('heartbeat separates fresh report, native work, stale reads, process exit and overdue wake', () => {
    const summary = (view = lifecycle, evidence = {}, host = wing, at = now) => coordinatorHomeSummary(session, host, { lifecycle: view, observedAt: now, reportSequence: view.report_cursor, ...evidence }, at);
    assert.equal(summary().presence, 'asleep');
    assert.equal(summary({ ...lifecycle, status: 'working' }).presence, 'awake');
    assert.equal(summary({ ...lifecycle, status: 'blocked' }).presence, 'awake');
    assert.equal(summary({ ...lifecycle, process_alive: false }).presence, 'stopped');
    assert.equal(summary(lifecycle, { cached: true }).status, 'unknown');
    assert.equal(summary(lifecycle, { readError: 'Lost connection' }).presence, 'unknown');
    assert.equal(summary(lifecycle, {}, { ...wing, online: false }).presence, 'offline');
    assert.equal(summary(lifecycle, {}, wing, now + 60000).presence, 'unknown');
    assert.equal(summary({ ...lifecycle, report: { ...lifecycle.report, next_wake: new Date(now - 1000).toISOString() } }).presence, 'overdue');
    assert.equal(summary({ ...lifecycle, reported_at: new Date(now - 60000).toISOString() }).presence, 'unknown');
    assert.equal(summary({ ...lifecycle, state_cursor: 4 }).presence, 'unknown');
    assert.equal(summary({ ...lifecycle, report: {} }).nextWake, null);
});

test('durable journal replay produces state, approval, artifact and link activity without duplicates', () => {
    const events = [
        { sequence: 1, type: 'prompt_submitted', state: 'working', timestamp: new Date(now).toISOString() },
        { sequence: 2, type: 'session_report', report: { ...lifecycle.report, artifacts: [{ label: '<script>report</script>', path: 'report.md' }], links: [{ label: 'Docs', url: 'https://example.com/docs' }, { label: 'bad', url: 'javascript:alert(1)' }] }, timestamp: new Date(now).toISOString() },
        { sequence: 3, type: 'input_requested', state: 'needs_input', reason: 'Confirm release' },
    ];
    const view = { session_id: session.id, ...lifecycle, cursor: 3, events };
    let state = applyConversationRead(emptyConversationState(), view, { expected: { sessionId: session.id } });
    state = applyConversationRead(state, view, { expected: { sessionId: session.id } });
    assert.equal(sessionActivity(state.messages).length, 3);
    const html = timelineMarkup(state.messages);
    assert.match(html, /Approval \/ input requested/);
    assert.match(html, /data-artifact-path="report.md"/);
    assert.match(html, /https:\/\/example.com\/docs/);
    assert.doesNotMatch(html, /<script>|javascript:/);
    assert.match(html, /&lt;script&gt;/);
    const rejected = applyConversationRead(state, { ...view, session_id: 'other' }, { expected: { sessionId: session.id } });
    assert.ok(rejected.identityError);
    assert.equal(sessionActivity(rejected.messages).length, 3);
});

test('links reject unsafe schemes and embedded credentials', () => {
    for (const url of ['javascript:alert(1)', 'data:text/html,bad', '/relative', 'https://user:password@example.com']) assert.equal(safeActivityLink(url), '');
    assert.equal(safeActivityLink('https://example.com/docs'), 'https://example.com/docs');
});

test('stopped coordinator retains history but makes no wake or work promise',()=>{
 const v=coordinatorHomeSummary(session,wing,{lifecycle:{...lifecycle,process_alive:false,status:'exited'},observedAt:now},now);
 assert.equal(v.presence,'stopped');assert.equal(v.nextWake,null);assert.equal(v.historicalUpdate,true);assert.match(v.nextAction,/Inspect/);assert.match(v.detail,/restart/);
});
test('recorded approval directs answers to the terminal',()=>{
 const html=timelineMarkup([{type:'activity',eventType:'input_requested',state:'needs_input',reason:'Confirm release',sequence:1}]);
 assert.match(html,/Answer in terminal/);assert.match(html,/recorded request/);assert.doesNotMatch(html,/needs_input|data-review-request/);
});
