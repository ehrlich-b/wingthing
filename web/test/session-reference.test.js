import test from 'node:test';
import assert from 'node:assert/strict';
import { sessionResourceKey, readSessionContent, writeSessionContent, clearSessionContent, notificationForSession, changeSessionNotification, terminalReferenceMatches, orderSessionReferences } from '../src/session-reference.js';
import { filterSessionInventory } from '../src/session-inventory.js';
import { createCanvasSessionState } from '../src/canvas-session-state.js';

function storageWith(items) {
    const values = new Map(Object.entries(items || {}));
    return { values, getItem: key => values.get(key) ?? null, setItem: (key, value) => values.set(key, value), removeItem: key => values.delete(key) };
}

const mac = { id: 'same-provider-id', wing_id: 'mac', swept: true, agent: 'claude' };
const linux = { ...mac, wing_id: 'linux' };

test('saved row order addresses exact wings and uses legacy positions only for unique IDs', () => {
    const unique = { ...mac, id: 'legacy-unique' };
    const sessions = [mac, unique, linux];
    assert.deepEqual(orderSessionReferences(sessions, [sessionResourceKey(linux), sessionResourceKey(mac)]), [linux, mac, unique]);
    assert.deepEqual(orderSessionReferences(sessions, [mac.id, unique.id]), [unique, mac, linux]);
    assert.deepEqual(sessions, [mac, unique, linux]);
});

test('terminal text and thumbnails for equal IDs stay on their source wing; legacy content is dormant', () => {
    const storage = storageWith({ 'wt_termbuf_same-provider-id': 'unattributed old terminal', 'wt_termthumb_same-provider-id': 'unattributed old image' });
    for (const prefix of ['wt_termbuf_', 'wt_termthumb_']) {
        assert.equal(readSessionContent(storage, prefix, mac.wing_id, mac.id), null);
        assert.equal(readSessionContent(storage, prefix, linux.wing_id, linux.id), null);
        writeSessionContent(storage, prefix, mac.wing_id, mac.id, 'Mac content');
        writeSessionContent(storage, prefix, linux.wing_id, linux.id, 'Linux content');
        assert.equal(readSessionContent(storage, prefix, mac.wing_id, mac.id), 'Mac content');
        assert.equal(readSessionContent(storage, prefix, linux.wing_id, linux.id), 'Linux content');
    }
    clearSessionContent(storage, ['wt_termbuf_', 'wt_termthumb_'], mac.wing_id, mac.id);
    assert.equal(readSessionContent(storage, 'wt_termbuf_', mac.wing_id, mac.id), null);
    assert.equal(readSessionContent(storage, 'wt_termthumb_', mac.wing_id, mac.id), null);
    assert.equal(readSessionContent(storage, 'wt_termbuf_', linux.wing_id, linux.id), 'Linux content');
    assert.equal(readSessionContent(storage, 'wt_termthumb_', linux.wing_id, linux.id), 'Linux content');
    assert.equal(storage.getItem('wt_termbuf_same-provider-id'), 'unattributed old terminal');
    assert.equal(writeSessionContent(storage, 'wt_termbuf_', undefined, mac.id, 'ambiguous'), false);
    clearSessionContent(storage, ['wt_termbuf_'], undefined, mac.id);
    assert.equal(readSessionContent(storage, 'wt_termbuf_', linux.wing_id, linux.id), 'Linux content');
});

test('attention marking, filtering and clearing preserve an equal-ID sibling', () => {
    const notifications = { [mac.id]: true }; // Old/unqualified attention cannot identify either wing.
    const wings = [{ wing_id: 'mac', online: true }, { wing_id: 'linux', online: true }];
    assert.deepEqual(filterSessionInventory([mac, linux], wings, notifications, { status: 'attention' }), []);
    assert.equal(changeSessionNotification(notifications, mac.id, undefined, true), false);
    assert.equal(changeSessionNotification(notifications, mac.id, mac.wing_id, true), true);
    assert.equal(notificationForSession(notifications, linux), false);
    assert.deepEqual(filterSessionInventory([mac, linux], wings, notifications, { status: 'attention' }), [mac]);
    changeSessionNotification(notifications, linux.id, linux.wing_id, true);
    assert.equal(changeSessionNotification(notifications, mac.id, mac.wing_id, false), true);
    assert.equal(notificationForSession(notifications, mac), false);
    assert.equal(notificationForSession(notifications, linux), true);
    assert.deepEqual(filterSessionInventory([mac, linux], wings, notifications, { status: 'attention' }), [linux]);
    assert.equal(changeSessionNotification(notifications, mac.id, undefined, false), false);
    assert.equal(notificationForSession(notifications, linux), true);
});

test('delayed terminal work cannot write to an equal-ID sibling or a replacement socket', () => {
    const socket = {}, replacement = {};
    const active = { ptySessionId: mac.id, ptyWingId: mac.wing_id, ptyWs: socket };
    assert.equal(terminalReferenceMatches(active, mac.id, mac.wing_id, socket), true);
    assert.equal(terminalReferenceMatches({ ...active, ptyWingId: linux.wing_id }, mac.id, mac.wing_id, socket), false);
    assert.equal(terminalReferenceMatches({ ...active, ptyWs: replacement }, mac.id, mac.wing_id, socket), false);
    assert.equal(terminalReferenceMatches({ ...active, ptySessionId: 'next-session' }, mac.id, mac.wing_id, socket), false);
    assert.equal(terminalReferenceMatches({ ...active, ptyWingId: undefined }, mac.id, undefined, socket), false);
});

test('canvas creation, startup rekey, focus, saved geometry and close address one wing', () => {
    const canvas = createCanvasSessionState();
    const child = { ...linux, col: 8, row: 2, cellW: 20, cellH: 12 };
    const pending = { id: 'pending-start', wingId: mac.wing_id, col: 0, row: 1, cellW: 10, cellH: 8 };
    const linuxKey = canvas.put(child);
    const tempKey = canvas.put(pending);
    canvas.focus(tempKey);
    const macKey = canvas.rekey(tempKey, mac.id);
    assert.equal(macKey, sessionResourceKey(mac));
    assert.equal(canvas.focusedKey, macKey);
    assert.equal(canvas.find(mac.id), null);
    assert.equal(canvas.find(mac.id, 'missing'), null);
    assert.equal(canvas.find(mac.id, linux.wing_id), child);
    assert.equal(canvas.find(mac.id, mac.wing_id), pending);
    assert.deepEqual(canvas.layout(), {
        [linuxKey]: { col: 8, row: 2, cellW: 20, cellH: 12 },
        [macKey]: { col: 0, row: 1, cellW: 10, cellH: 8 },
    });
    canvas.focus(linuxKey);
    assert.equal(canvas.remove(macKey), pending);
    assert.equal(canvas.focusedKey, linuxKey);
    assert.equal(canvas.find(child.id, child.wing_id), child);
    assert.equal(canvas.rekey(macKey, 'late-start'), null);
    assert.deepEqual(Object.keys(canvas.layout()), [linuxKey]);
    canvas.remove(linuxKey);
    assert.equal(canvas.focusedKey, null);
    assert.equal(canvas.find(child.id), null);
});
