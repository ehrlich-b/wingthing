import test from 'node:test';
import assert from 'node:assert/strict';

import { historyResumeState } from '../src/session-resume.js';

test('history resume is offered only to the exact session owner', function() {
    var session = { user_id: 'alice', resumable: true };
    assert.equal(historyResumeState(session, { id: 'alice' }, true).available, true);
    assert.deepEqual(historyResumeState(session, { id: 'admin' }, true), {
        available: false,
        reason: 'Only the session owner can resume it',
    });
});

test('history resume exposes capability and provider reasons', function() {
    var session = { user_id: 'alice', resumable: false, resume_unavailable_reason: 'conversation was not captured' };
    assert.equal(historyResumeState(session, { id: 'alice' }, false).reason, 'Update this wing to resume provider sessions');
    assert.equal(historyResumeState(session, { id: 'alice' }, true).reason, 'conversation was not captured');
});
