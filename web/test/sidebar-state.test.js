import test from 'node:test';
import assert from 'node:assert/strict';
import { sidebarHarness, session } from './support/browser-state.mjs';

test('100 sidebar render/remove cycles replace tabs and keep one click action', () => {
    const h = sidebarHarness();
    let previous;
    for (let cycle = 0; cycle < 100; cycle++) {
        h.S.sessionsData = [session()];
        h.render();
        const tab = h.tabs.children[0];
        assert.notEqual(tab, previous);
        if (previous) assert.equal(previous.parentNode, null);
        tab.dispatch('click');
        assert.equal(h.actions.length, cycle + 1);
        assert.deepEqual(h.actions.at(-1), ['mac', 'same-id']);
        assert.equal(tab.listeners.click.length, 1);
        previous = tab;
        h.S.sessionsData = [];
        h.render();
        assert.equal(h.tabs.children.length, 0);
    }
});

test('sidebar keeps qualified session identity, current selection and inventory guards', () => {
    const h = sidebarHarness();
    h.S.sessionsData = [session(), session('same-id', 'linux'), session('cached', 'mac', false)];
    h.S.wingsData.push({ wing_id: 'linux', online: true });
    h.render();
    h.tabs.children[1].dispatch('click');
    assert.deepEqual(h.actions, [['linux', 'same-id']]);
    h.tabs.children[2].dispatch('click');
    assert.equal(h.actions.length, 1);
    h.S.ptySessionId = 'same-id'; h.S.ptyWingId = 'mac'; h.S.activeView = 'terminal';
    h.tabs.children[0].dispatch('click');
    assert.equal(h.actions.length, 1);
});

test('sidebar keyboard navigation and activation retain focus across rendering', () => {
    const h = sidebarHarness();
    h.S.sessionsData = [session('a'), session('b')]; h.render();
    h.tabs.children[0].focus();
    h.tabs.children[0].dispatch('keydown', 'ArrowDown');
    assert.equal(h.doc.activeElement, h.tabs.children[1]);
    h.render();
    assert.equal(h.doc.activeElement, h.tabs.children[1]);
    assert.equal(h.tabs.children[1].dispatch('keydown', 'Enter').prevented, true);
    assert.deepEqual(h.actions, [['mac', 'b']]);
    h.tabs.children[0].children[0].dispatch('click');
    assert.deepEqual(h.renames, [['mac', 'a']]);
    assert.equal(h.actions.length, 1);
});

test('base reproduction: a removed tab can still dispatch a deleted session', () => {
    const h = sidebarHarness();
    h.S.sessionsData = [session()]; h.render();
    const stale = h.tabs.children[0];
    h.S.sessionsData = []; h.render();
    stale.dispatch('click');
    assert.deepEqual(h.actions, [['mac', 'same-id']]);
    // Reattaching the retained node also revives its per-tab callback.
    stale.parentNode = h.tabs; h.tabs.children.push(stale);
    stale.dispatch('click');
    assert.equal(h.actions.length, 2);
});
