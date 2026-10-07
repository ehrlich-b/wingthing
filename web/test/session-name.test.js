import test from 'node:test';
import assert from 'node:assert/strict';

import { formatSessionTitle, sessionDisplayName, validSessionName } from '../src/helpers.js';

test('session labels prefer durable names and fall back to the project directory', function() {
    assert.equal(sessionDisplayName({ name: 'support-case-42', cwd: '/opt/wingthing/support' }), 'support-case-42');
    assert.equal(sessionDisplayName({ cwd: '/opt/wingthing/support' }), 'support');
});

test('browser session-name validation matches the wing contract', function() {
    assert.equal(validSessionName('support-case-42'), true);
    assert.equal(validSessionName('case_123.notes'), true);
    assert.equal(validSessionName('-hidden'), false);
    assert.equal(validSessionName('two words'), false);
    assert.equal(validSessionName('x'.repeat(65)), false);
});

test('session titles name the session alone and fall back only without one', function() {
    assert.equal(formatSessionTitle({ name: 'support', agent: 'claude' }, '', 'shared-wing'), 'support');
    assert.equal(formatSessionTitle({ agent: 'claude', cwd: '/opt/wingthing/support' }, 'claude', 'shared-wing'), 'support');
    assert.equal(formatSessionTitle(undefined, 'claude', 'shared-wing'), 'shared-wing');
    assert.equal(formatSessionTitle(undefined, 'claude', ''), 'claude');
});
