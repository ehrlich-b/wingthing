import test from 'node:test';
import assert from 'node:assert/strict';

import { resumeAckMatches } from '../src/pty-resume.js';

test('provider resume requires an exact acknowledgement', function() {
    assert.equal(resumeAckMatches('old-session', { resumed_from_session_id: 'old-session' }), true);
    assert.equal(resumeAckMatches('old-session', { session_id: 'fresh-session' }), false);
    assert.equal(resumeAckMatches('old-session', { resumed_from_session_id: 'different-session' }), false);
});

test('ordinary starts do not require a resume acknowledgement', function() {
    assert.equal(resumeAckMatches('', { session_id: 'fresh-session' }), true);
    assert.equal(resumeAckMatches(null, { session_id: 'fresh-session' }), true);
});
