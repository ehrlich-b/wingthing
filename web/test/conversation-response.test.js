import test from 'node:test';
import assert from 'node:assert/strict';
import { applyPromptReceipt, canStartFreshLaunch, conversationResponseCurrent, conversationTaskAvailability, readPendingLaunch, planLaunchStart, launchFieldsFromPending } from '../src/conversation-response.js';

test('known pre-Send writer rejection releases request identity and restores draft', function() {
    var pending = { request_id: 'old-request', input: 'Keep this draft', status: 'pending' };
    var rejected = applyPromptReceipt(pending, { status: 'not_sent', definitely_not_sent: true, reason: 'Another writer owns input' });
    assert.equal(rejected.pending, null);
    assert.equal(rejected.draft, pending.input);
    assert.match(rejected.notice, /Another writer/);
    var deliberate = { request_id: 'fresh-request', input: rejected.draft };
    assert.notEqual(deliberate.request_id, pending.request_id);
    assert.equal(applyPromptReceipt(deliberate, { status: 'native_receipt_observed', native_receipt_observed: true }).draft, '');
});

test('zero bytes, lost ack, and incomplete not-sent proof keep the same uncertain request', function() {
    var pending = { request_id: 'must-not-resend', input: 'work' };
    for (var receipt of [{ status: 'unconfirmed', transport_bytes_enqueued: 0 }, { status: 'not_sent' }, { status: 'not_sent', definitely_not_sent: false }, { status: 'unconfirmed', definitely_not_sent: true }]) {
        var result = applyPromptReceipt(pending, receipt);
        assert.equal(result.pending.request_id, pending.request_id);
        assert.equal(result.draft, null);
    }
});

test('only confirmed failed launch offers a fresh launch identity', function() {
    assert.equal(canStartFreshLaunch({ launch_state: 'failed' }), true);
    for (var state of ['starting', 'started', 'unknown']) assert.equal(canStartFreshLaunch({ launch_state: state }), false);
    assert.equal(canStartFreshLaunch(null), false);
});

test('late native reader result is discarded after another view or route is selected', async function() {
    var requested = { userId: 'owner', hash: '#conversation/a?wing=w', viewGeneration: 5 };
    var current = { ...requested };
    var response = Promise.resolve('old response');
    current.viewGeneration++;
    assert.equal(conversationResponseCurrent(requested, current), false);
    await response;
    assert.equal(conversationResponseCurrent(requested, current), false);
    assert.equal(conversationResponseCurrent(requested, { ...requested, hash: '#conversation/b?wing=w' }), false);
    assert.equal(conversationResponseCurrent(requested, requested), true);
});

test('missing current lifecycle stays unavailable; missing earlier history is a separate inspectable issue', function() {
    var task = { conversation: { launch_state: 'started' }, lifecycle_error: 'Provider archive is missing' };
    var unavailable = conversationTaskAvailability(task, '');
    assert.deepEqual([unavailable.state, unavailable.error, unavailable.live], ['unavailable', task.lifecycle_error, false]);
    var now = 100000;
    var history = conversationTaskAvailability({ conversation: task.conversation, lifecycle: { agent: 'claude', state: 'completed', state_source: 'claude_hook', process_alive: true, ready: true }, history_unavailable: true }, '', { observedAt: now - 1, now: now });
    assert.deepEqual([history.state, history.error], ['turn_completed', '']);
    assert.match(history.historyIssue, /earlier execution history is unavailable/);
});

// Launch identity: each test drives the same read -> plan -> persist cycle the
// launch form uses, with a fake dispatcher standing in for agent_start.
function ids() {
    var n = 0;
    var next = function() { n += 1; return 'req-' + n; };
    next.count = function() { return n; };
    return next;
}

function submitLaunch(storage, fields, newId, sent) {
    var plan = planLaunchStart(fields, readPendingLaunch(storage.value), newId);
    if (plan.action !== 'send') return plan;
    storage.value = JSON.stringify(plan.pending);
    sent.push(plan.args);
    return plan;
}

var parentFields = { wingId: 'wing-a', label: 'personal-parent', cwd: '/work', model: '', parentConversationId: '' };

test('old saved launch shape migrates and an identical retry keeps its request identity and target', function() {
    var storage = { value: JSON.stringify({ requestId: 'old-req', spec: JSON.stringify({ agent: 'claude', label: 'personal-parent', cwd: '/work', conversation_role: 'parent', request_id: '' }), wingId: 'wing-a' }) };
    var stored = readPendingLaunch(storage.value);
    assert.deepEqual(launchFieldsFromPending(stored.pending), parentFields);
    var newId = ids(), sent = [];
    var plan = submitLaunch(storage, parentFields, newId, sent);
    assert.equal(plan.action, 'send');
    assert.deepEqual(sent, [{ agent: 'claude', label: 'personal-parent', cwd: '/work', conversation_role: 'parent', request_id: 'old-req' }]);
    assert.equal(newId.count(), 0);
    var child = { requestId: 'old-child', spec: JSON.stringify({ agent: 'claude', label: 'research', cwd: '/work', conversation_role: 'child', request_id: '', parent_conversation_id: 'conv-1' }), wingId: 'wing-a' };
    var childPlan = planLaunchStart({ wingId: 'wing-a', label: 'research', cwd: '/work', parentConversationId: 'conv-1' }, readPendingLaunch(JSON.stringify(child)), newId);
    assert.equal(childPlan.args.request_id, 'old-child');
    assert.equal(childPlan.args.parent_conversation_id, 'conv-1');
    var changed = planLaunchStart({ ...parentFields, wingId: 'wing-b' }, stored, newId);
    assert.equal(changed.action, 'refused');
    assert.equal(newId.count(), 0);
    var unknownPlaceholder = JSON.stringify({ requestId: 'old-req', spec: JSON.stringify({ agent: 'claude', label: 'x', cwd: '/w', conversation_role: 'parent', request_id: 'other' }), wingId: 'wing-a' });
    assert.ok(readPendingLaunch(unknownPlaceholder).malformed);
});

test('valid same-attempt retry reuses one nonempty request identity', function() {
    var storage = { value: null }, newId = ids(), sent = [];
    submitLaunch(storage, parentFields, newId, sent);
    submitLaunch(storage, { ...parentFields }, newId, sent);
    assert.equal(sent.length, 2);
    assert.equal(sent[0].request_id, 'req-1');
    assert.equal(sent[1].request_id, 'req-1');
    assert.equal(newId.count(), 1);
    assert.deepEqual(sent[0], { agent: 'claude', label: 'personal-parent', cwd: '/work', conversation_role: 'parent', request_id: 'req-1' });
    assert.throws(function() { planLaunchStart(parentFields, null, function() { return ''; }); });
});

test('changed launch after an uncertain result is refused without dispatch', function() {
    var storage = { value: null }, newId = ids(), sent = [];
    submitLaunch(storage, parentFields, newId, sent);
    var before = storage.value;
    for (var change of [{ label: 'other' }, { cwd: '/elsewhere' }, { wingId: 'wing-b' }, { model: 'claude-opus-5-5' }, { parentConversationId: 'conv-9' }]) {
        var plan = submitLaunch(storage, { ...parentFields, ...change }, newId, sent);
        assert.equal(plan.action, 'refused');
        assert.equal(plan.pending.requestId, 'req-1');
    }
    assert.equal(sent.length, 1);
    assert.equal(storage.value, before);
    assert.equal(newId.count(), 1);
});

test('confirmed failure needs an explicit fresh attempt for the same launch', function() {
    var storage = { value: null }, newId = ids(), sent = [];
    var plan = submitLaunch(storage, parentFields, newId, sent);
    assert.equal(canStartFreshLaunch({ launch_state: 'failed' }), true);
    storage.value = JSON.stringify({ ...plan.pending, confirmedFailed: true });
    assert.equal(submitLaunch(storage, parentFields, newId, sent).args.request_id, 'req-1');
    storage.value = null; // explicit "New attempt after confirmed failure"
    assert.equal(submitLaunch(storage, parentFields, newId, sent).args.request_id, 'req-2');
    storage.value = JSON.stringify({ ...JSON.parse(storage.value), confirmedFailed: true });
    assert.equal(submitLaunch(storage, { ...parentFields, label: 'renamed' }, newId, sent).args.request_id, 'req-3');
    assert.equal(canStartFreshLaunch({ launch_state: 'reserved' }), false);
});

test('malformed stored launch records fail safely until explicitly discarded', function() {
    var spec = JSON.stringify({ agent: 'claude', label: 'p', cwd: '/w', conversation_role: 'parent' });
    var records = ['{not json', '[]', '"text"', JSON.stringify({ requestId: 'r1' }), JSON.stringify({ requestId: 'r1', spec: 5, wingId: 'w' }), JSON.stringify({ requestId: 'r1', spec: spec }),
        JSON.stringify({ requestId: 'r1', spec: spec, wingId: 3 }), JSON.stringify({ requestId: 'r1', spec: '{}', wingId: 'w' }), JSON.stringify({ requestId: 'r1', spec: '{bad', wingId: 'w' }),
        JSON.stringify({ requestId: 'r1', spec: JSON.stringify({ agent: 'claude', label: 'p', cwd: '/w', conversation_role: 'parent', extra: 1 }), wingId: 'w' }),
        JSON.stringify({ requestId: 'r1', spec: JSON.stringify({ agent: 'claude', label: 'p', cwd: '/w', conversation_role: 'child' }), wingId: 'w' }),
        JSON.stringify({ requestId: ' r1', spec: spec, wingId: 'w' }), JSON.stringify({ requestId: 7, spec: spec, wingId: 'w' })];
    for (var text of records) {
        var storage = { value: text }, newId = ids(), sent = [];
        var plan = submitLaunch(storage, parentFields, newId, sent);
        assert.equal(plan.action, 'malformed', text);
        assert.equal(sent.length, 0);
        assert.equal(newId.count(), 0);
        assert.equal(storage.value, text);
    }
    assert.equal(readPendingLaunch(JSON.stringify({ requestId: 'r1' })).malformed.requestId, 'r1');
    assert.equal(readPendingLaunch(null), null);
    assert.equal(readPendingLaunch('null'), null);
    var discarded = { value: null }, sent2 = [];
    assert.equal(submitLaunch(discarded, parentFields, ids(), sent2).args.request_id, 'req-1');
});

test('optional model is forwarded through agent_start only when named and is part of the attempt', function() {
    var storage = { value: null }, newId = ids(), sent = [];
    submitLaunch(storage, { ...parentFields, model: ' claude-opus-5-5 ' }, newId, sent);
    assert.deepEqual(sent[0], { agent: 'claude', label: 'personal-parent', cwd: '/work', conversation_role: 'parent', model: 'claude-opus-5-5', request_id: 'req-1' });
    assert.equal(launchFieldsFromPending(readPendingLaunch(storage.value).pending).model, 'claude-opus-5-5');
    assert.equal(submitLaunch(storage, { ...parentFields, model: 'claude-opus-5-5' }, newId, sent).args.request_id, 'req-1');
    assert.equal(submitLaunch(storage, parentFields, newId, sent).action, 'refused');
    var blank = { value: null };
    submitLaunch(blank, { ...parentFields, model: '   ' }, ids(), sent);
    assert.equal('model' in sent[sent.length - 1], false);
});
