import test from 'node:test';
import assert from 'node:assert/strict';
import { workTitle, projectLabel, statusMarkup, matchExcerpt } from '../src/presentation.js';

test('display identity hides paths and opaque IDs without changing request identity', () => {
    const row = { origin_wing_id: 'machine-a', session_id: 'a'.repeat(32), name: '/private/work', cwd: '/private/Projects/widget' };
    assert.equal(workTitle(row), 'Untitled session');
    assert.equal(projectLabel(row), 'widget');
    assert.equal(row.session_id, 'a'.repeat(32));
    assert.equal(workTitle({ name: 'Fix DNS lookup' }), 'Fix DNS lookup');
    assert.equal(workTitle({ name: 'a'.repeat(32) }), 'Untitled session');
    assert.equal(workTitle({ name: 'Coordinator', pinned: true, role: 'coordinator' }), 'wing');
});
test('status has a named state and distinct symbol without color', () => {
    for (const [status, name, glyph] of [['working','Working','●'],['blocked','Needs you','△'],['idle','Idle','○'],['done','Done','✓'],['failed','Failed','⊗']]) {
        assert.match(statusMarkup(status), new RegExp(name));
        assert.ok(statusMarkup(status).includes(glyph));
    }
    assert.match(statusMarkup('working', true), /Last seen/);
    assert.match(statusMarkup('unexpected'), /Unknown/);
});
test('matching excerpts render hostile text safely and treat search punctuation literally', () => {
    assert.equal(matchExcerpt('<script>DNS</script>', 'DNS'), '&lt;script&gt;<mark>DNS</mark>&lt;/script&gt;');
    assert.equal(matchExcerpt('a+b then [test]', 'a+b [test]'), '<mark>a+b</mark> then <mark>[test]</mark>');
});
