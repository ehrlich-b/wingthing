import assert from 'node:assert/strict';
import test from 'node:test';

import { applyWingEventMetadata } from '../src/wing-event.js';

test('wing lifecycle events apply explicit false and zero capability values', () => {
    const wing = {
        public_key: 'old-key',
        locked: true,
        allowed_count: 3,
        purpose_binding: true,
        direct_mcp: true,
        hosted_relay: 'allow',
    };

    applyWingEventMetadata(wing, {
        public_key: 'new-key',
        locked: false,
        allowed_count: 0,
        purpose_binding: false,
        direct_mcp: false,
        hosted_relay: 'deny',
    });

    assert.deepEqual(wing, {
        public_key: 'new-key',
        locked: false,
        allowed_count: 0,
        purpose_binding: false,
        direct_mcp: false,
        hosted_relay: 'deny',
    });
});

test('events from older relays preserve capability values they omit', () => {
    const wing = {
        locked: true,
        allowed_count: 3,
        purpose_binding: true,
        direct_mcp: true,
        hosted_relay: 'deny',
    };

    applyWingEventMetadata(wing, { public_key: '' });

    assert.deepEqual(wing, {
        locked: true,
        allowed_count: 3,
        purpose_binding: true,
        direct_mcp: true,
        hosted_relay: 'deny',
    });
});
