import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';
import { createCanvasSessionState } from '../src/canvas-session-state.js';
import { sessionResourceKey } from '../src/session-reference.js';

const oldKey = 'wt_canvas_layout', newKey = 'wt_canvas_layout_v2';
const geometry = { col: -4, row: 7, cellW: 21, cellH: 14 };
const mac = { id: 'shared', wing_id: 'mac' }, linux = { id: 'shared', wing_id: 'linux' };
const unique = { id: 'unique', wing_id: 'mac' };
const source = readFileSync(new URL('../src/canvas.js', import.meta.url), 'utf8')
    .replace(/^import .*;\n/gm, '').replace(/^export /gm, '');

function harness(items, sessions) {
    const values = new Map(Object.entries(items));
    const storage = {
        getItem: key => values.get(key) ?? null,
        setItem: (key, value) => values.set(key, value),
        removeItem: key => values.delete(key),
    };
    const context = vm.createContext({ createCanvasSessionState, sessionResourceKey, localStorage: storage, S: { sessionsData: sessions } });
    vm.runInContext(source, context, { filename: 'canvas.js' });
    return { values, storage, load: () => JSON.parse(JSON.stringify(context.loadCanvasLayout())) };
}

test('legacy canvas geometry migrates once for unambiguous wing-qualified sessions', () => {
    const h = harness({ [oldKey]: JSON.stringify({ unique: geometry }) }, [unique]);
    const expected = { [sessionResourceKey(unique)]: geometry };
    assert.deepEqual(h.load(), expected);
    assert.deepEqual(JSON.parse(h.values.get(newKey)), expected);
    assert.equal(h.values.has(oldKey), false);
    assert.deepEqual(h.load(), expected);
});

test('ambiguous, missing, and unqualified legacy sessions are dropped quietly', () => {
    const h = harness({ [oldKey]: JSON.stringify({ shared: geometry, missing: geometry, unqualified: geometry, unique: geometry }) },
        [mac, linux, unique, { id: 'unqualified' }]);
    assert.deepEqual(h.load(), { [sessionResourceKey(unique)]: geometry });
    assert.equal(h.values.has(oldKey), false);
    // A later inventory cannot resurrect geometry discarded as ambiguous.
    assert.deepEqual(h.load(), { [sessionResourceKey(unique)]: geometry });
});

test('migration preserves newer geometry and rejects malformed legacy values', () => {
    const newer = { ...geometry, col: 15 };
    const h = harness({ [oldKey]: JSON.stringify({ unique: geometry, shared: null }), [newKey]: JSON.stringify({ [sessionResourceKey(unique)]: newer }) }, [unique, mac]);
    assert.deepEqual(h.load(), { [sessionResourceKey(unique)]: newer });
    assert.equal(h.values.has(oldKey), false);
    for (const raw of ['{invalid', 'null', '[]', '42', JSON.stringify({ unique: { ...geometry, cellW: -1 } })]) {
        const invalid = harness({ [oldKey]: raw }, [unique]);
        assert.deepEqual(invalid.load(), {});
        assert.equal(invalid.values.has(oldKey), false);
    }
});

test('canvas layout loading never throws on malformed current data or unavailable storage', () => {
    for (const raw of ['{invalid', 'null', '[]', '42']) {
        assert.deepEqual(harness({ [newKey]: raw }, [unique]).load(), {});
    }
    for (const method of ['getItem', 'setItem', 'removeItem']) {
        const h = harness({ [oldKey]: JSON.stringify({ unique: geometry }) }, [unique]);
        h.storage[method] = () => { throw new Error('Storage unavailable'); };
        assert.doesNotThrow(h.load);
    }
});
