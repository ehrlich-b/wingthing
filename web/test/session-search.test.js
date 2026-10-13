import test from 'node:test';
import assert from 'node:assert/strict';
import { mergeSearchInventories, searchRoute, createSearchController } from '../src/session-search-state.js';

const now = Date.now();
const route = (wing_id, kind, healthy = true) => ({ wing_id, kind, healthy, observed_at: new Date(now - 1000).toISOString(), machine_label: wing_id });
const session = (origin, routes) => ({ origin_wing_id: origin, session_id: 'same-short-id', routes, match_fields: ['transcript'] });

test('search keeps overlapping IDs distinct, merges proven duplicates and selects a fresh route', () => {
    const via = session('desktop', [route('laptop', 'via')]);
    const direct = session('desktop', [route('desktop', 'direct')]);
    const other = session('laptop', [route('laptop', 'direct')]);
    const merged = mergeSearchInventories([{ complete: true, sessions: [via, other], sources: [] }, { complete: true, sessions: [direct], sources: [] }]);
    assert.equal(merged.sessions.length, 2);
    assert.equal(merged.complete, true);
    const desktop = merged.sessions.find(row => row.origin_wing_id === 'desktop');
    assert.equal(desktop.routes.length, 2);
    const wings = [{ wing_id: 'desktop', online: true }, { wing_id: 'laptop', online: true }];
    assert.equal(searchRoute(desktop, wings, now).kind, 'direct');
    wings[0].online = false;
    assert.equal(searchRoute(desktop, wings, now).kind, 'via');
    assert.equal(searchRoute(desktop, wings, now + 60000), null);
    assert.equal(searchRoute({ session_id: 'same-short-id', routes: direct.routes }, wings, now), null);
    assert.equal(searchRoute(session('desktop', [route('wrong-origin', 'direct')]), [{ wing_id: 'wrong-origin', online: true }], now), null);
});

test('failures, unknown identity and result bounds cannot claim complete coverage', () => {
    assert.equal(mergeSearchInventories([], [{ healthy: false }]).complete, false);
    const unknown = mergeSearchInventories([{ complete: true, sessions: [session('', [])] }]);
    assert.equal(unknown.complete, false);
    assert.equal(unknown.sessions.length, 0);
    const bounded = mergeSearchInventories([{ complete: true, sessions: Array.from({ length: 201 }, (_, n) => session(`wing-${n}`, [])) }]);
    assert.equal(bounded.complete, false);
    assert.equal(bounded.sessions.length, 200);
});

test('new input and owner changes retire delayed search success and error', async () => {
    let finishFirst, finishSecond;
    const updates = [];
    const controller = createSearchController(query => new Promise((resolve, reject) => {
        if (query === 'first') finishFirst = resolve; else finishSecond = reject;
    }), state => updates.push(state));
    const first = controller.run('first');
    controller.retire();
    const second = controller.run('second');
    finishFirst({ sessions: [{ name: 'retired' }], complete: true });
    await first;
    assert.equal(updates.some(update => update.sessions?.[0]?.name === 'retired'), false);
    controller.retire();
    finishSecond(new Error('retired error'));
    await second;
    assert.equal(updates.some(update => update.error), false);
});
