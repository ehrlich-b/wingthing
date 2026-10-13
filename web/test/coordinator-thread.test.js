import test from 'node:test';
import assert from 'node:assert/strict';
import { coordinatorThreadMarkup } from '../src/coordinator-thread.js';

test('coordinator thread renders native messages, report file chips and timestamps without invented receipts', () => {
    const html = coordinatorThreadMarkup([
        { type: 'user', content: 'Check <img src=x>', timestamp: '2026-10-13T04:00:00Z' },
        { type: 'activity', report: { update: 'Ready', artifacts: [{ label: 'Brief.md', path: 'reports/brief.md' }] }, timestamp: '2026-10-13T04:01:00Z' },
        { type: 'assistant', content: 'Here’s the brief.', timestamp: '2026-10-13T04:02:00Z' },
    ], {});
    assert.match(html, /thread-you/); assert.match(html, /thread-wing/);
    assert.match(html, /<time>/); assert.match(html, /thread-day/);
    assert.match(html, /data-artifact-path="reports\/brief.md"/);
    assert.match(html, /&lt;img/); assert.doesNotMatch(html, /<img|Read \d/);
});

test('home keeps six messages while viewer preserves its bounded journal', () => {
    const messages = Array.from({ length: 10 }, (_, i) => ({ type: 'assistant', content: `Message ${i}` }));
    assert.equal((coordinatorThreadMarkup(messages, {}).match(/thread-bubble/g) || []).length, 6);
    assert.equal((coordinatorThreadMarkup(messages, {}, 400).match(/thread-bubble/g) || []).length, 10);
});
