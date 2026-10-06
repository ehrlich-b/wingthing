import test from 'node:test';
import assert from 'node:assert/strict';

import { formatSessionTitle, sessionDisplayName, validSessionName } from '../src/helpers.js';

test('session labels prefer durable names and fall back to the project directory', function() {
    assert.equal(sessionDisplayName({ name: 'support-case-42', cwd: '/opt/wingthing/support' }), 'support-case-42');
    assert.equal(sessionDisplayName({ cwd: '/opt/wingthing/support' }), 'support');
});

test('an unnamed session shows the title its agent set, and a chosen name still wins', function() {
    assert.equal(sessionDisplayName({ title: 'Math question', cwd: '/opt/wingthing/eng' }), 'Math question');
    assert.equal(sessionDisplayName({ name: 'case-42', title: 'Math question', cwd: '/opt/wingthing/eng' }), 'case-42');
    assert.equal(sessionDisplayName({ title: '  ', cwd: '/opt/wingthing/eng' }), 'eng');
});

test('browser session-name validation matches the wing contract', function() {
    assert.equal(validSessionName('support-case-42'), true);
    assert.equal(validSessionName('case_123.notes'), true);
    assert.equal(validSessionName('-hidden'), false);
    assert.equal(validSessionName('two words'), false);
    assert.equal(validSessionName('x'.repeat(65)), false);
});

test('session titles use the persisted agent without leaving a dangling separator', function() {
    assert.equal(
        formatSessionTitle({ name: 'support', agent: 'claude' }, '', 'shared-wing'),
        'support \u00b7 claude'
    );
    assert.equal(formatSessionTitle({ name: 'support' }, '', 'shared-wing'), 'support');
});
