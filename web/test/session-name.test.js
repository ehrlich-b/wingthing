import test from 'node:test';
import assert from 'node:assert/strict';

import { sessionDisplayName, validSessionName } from '../src/helpers.js';

test('session labels prefer durable names and fall back to the project directory', function() {
    assert.equal(sessionDisplayName({ name: 'SLIDE-5730', cwd: '/opt/wingthing/support' }), 'SLIDE-5730');
    assert.equal(sessionDisplayName({ cwd: '/opt/wingthing/support' }), 'support');
});

test('browser session-name validation matches the wing contract', function() {
    assert.equal(validSessionName('SLIDE-5730'), true);
    assert.equal(validSessionName('case_123.notes'), true);
    assert.equal(validSessionName('-hidden'), false);
    assert.equal(validSessionName('two words'), false);
    assert.equal(validSessionName('x'.repeat(65)), false);
});
