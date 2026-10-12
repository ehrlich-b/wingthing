import test from 'node:test';
import assert from 'node:assert/strict';
import { createForegroundRefresh } from '../src/phone-refresh.js';

test('foreground reads coalesce and never refresh while hidden', async () => {
    let visible = false, now = 1000, calls = 0, finish;
    const refresh = createForegroundRefresh(() => { calls++; return new Promise(resolve => { finish = resolve; }); }, () => visible, () => now);
    assert.equal(await refresh(), false);
    assert.equal(calls, 0);
    visible = true;
    const first = refresh();
    assert.equal(refresh(), first);
    await Promise.resolve();
    assert.equal(calls, 1);
    finish();
    assert.equal(await first, true);
    assert.equal(await refresh(), false, 'pageshow and visibilitychange share a single read');
    now += 1001;
    const next = refresh();
    await Promise.resolve();
    finish();
    await next;
    assert.equal(calls, 2);
});

test('failed foreground reads permit a later explicit retry', async () => {
    let now = 1, calls = 0;
    const refresh = createForegroundRefresh(() => { calls++; if (calls === 1) throw new Error('offline'); }, () => true, () => now);
    await assert.rejects(refresh(), /offline/);
    now += 1001;
    assert.equal(await refresh(), true);
    assert.equal(calls, 2);
});
