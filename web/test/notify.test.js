import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';
import { findSessionResource, sessionIsViewed } from '../src/session-inventory.js';
import { sessionResourceKey, changeSessionNotification } from '../src/session-reference.js';
import { paperRoute } from '../src/paper-state.js';

const source = readFileSync(new URL('../src/notify.js', import.meta.url), 'utf8')
    .replace(/^import .*;\n/gm, '').replace(/^export /gm, '')
    .replaceAll('import.meta.env.BASE_URL', "'/app/'")
    .replace(/\bimport\(/g, 'loadModule(');

function notificationHarness(persistent = false) {
    const notifications = [], opened = [];
    const listeners = {}, intervals = new Map(), frames = [], acknowledged = [];
    const main = { dataset: { view: 'session', hash: '#session/linux/session' } };
    const paper = { hidden: false, querySelector: () => main };
    let focused = false;
    class Notification {
        constructor(title, options) {
            this.title = title;
            this.options = options;
            this.data = options.data;
            notifications.push(this);
        }
    }
    const context = vm.createContext({
        Notification,
        S: { activeView: 'home', sessionsData: [{ id: 'session', wing_id: 'linux' }, { id: 'session', wing_id: 'mac' }],
            currentUser: { id: 'owner' }, sessionNotifications: {}, ptySessionId: null, ptyWingId: null },
        document: { title: 'wingthing', visibilityState: 'visible', hidden: false,
            getElementById: () => paper, addEventListener: (type, listener) => { listeners[type] = listener; } },
        paperRoute, findSessionResource, sessionIsViewed, sessionResourceKey, changeSessionNotification,
        chatSnapshot: () => null, renderSidebar() {}, renderDashboard() {},
        acknowledgeSessionCompletions: (_storage, _user, sessions) => acknowledged.push(...sessions),
        browserLocalStorage: () => null, WebSocket: { OPEN: 1 },
        setInterval: callback => { const id = intervals.size + 1; intervals.set(id, callback); return id; },
        clearInterval: id => intervals.delete(id),
        location: { hash: '' },
        navigator: persistent ? { serviceWorker: { getRegistration: async () => ({
            showNotification: async (title, options) => notifications.push({ title, options }),
        }) } } : {},
        window: { focus() { focused = true; }, addEventListener() {} },
        loadModule: async () => ({
            openConversationReference: reference => opened.push({ wingId: reference.wingId, sessionId: 'new-execution' }),
            openConversationTranscript: (conversation, wingId) => opened.push({ wingId, sessionId: conversation.session_id, conversationId: conversation.conversation_id, rootId: conversation.root_conversation_id }),
            switchToSession: (sessionId, _history, wingId) => opened.push({ wingId, sessionId }),
        }),
    });
    vm.runInContext(source, context, { filename: 'notify.js' });
    return { context, notifications, opened, listeners, intervals, frames, acknowledged, paper, main, focused: () => focused };
}

test('page and persistent coordinator notifications keep confidential titles off the lock screen', async () => {
    for (const persistent of [false, true]) {
        const h = notificationHarness(persistent);
        await h.context.fireOSNotification('alerted-execution', 'mac', { conversationId: 'logical', title: 'Confidential acquisition' });
        const notification = h.notifications[0];
        assert.equal(notification.title, 'wingthing');
        assert.equal(notification.options.body, 'A coordinator task needs your attention');
        assert.doesNotMatch(JSON.stringify(notification.options), /Confidential acquisition/);
        assert.equal(notification.options.data.sessionId, 'alerted-execution');
        assert.equal(notification.options.data.wingId, 'mac');
    }
});

test('coordinator notification clicks open the alerted execution after the logical task advances', async () => {
    const h = notificationHarness();
    const conversation = { conversationId: 'child', rootConversationId: 'root', title: 'Private child' };
    await h.context.fireOSNotification('alerted-execution', 'mac', conversation);
    h.notifications[0].onclick();
    await new Promise(resolve => setImmediate(resolve));
    assert.deepEqual(h.opened, [{ wingId: 'mac', sessionId: 'alerted-execution', conversationId: 'child', rootId: 'root' }]);
    assert.equal(h.focused(), true);
});

test('ordinary session notifications still open the qualified alerted session', async () => {
    const h = notificationHarness();
    await h.context.fireOSNotification('session', 'linux');
    assert.equal(h.notifications[0].options.body, 'A session needs your attention');
    h.notifications[0].onclick();
    await new Promise(resolve => setImmediate(resolve));
    assert.equal(h.context.location.hash, '#session/linux/session');
    assert.deepEqual(h.opened, []);
    assert.equal(h.focused(), true);
});

test('visible paper sessions suppress new alerts and acknowledge attached terminal attention', () => {
    const h = notificationHarness(), { S, location } = h.context;
    location.hash = h.main.dataset.hash;
    h.context.setNotification('session', 'linux');
    assert.deepEqual(Object.keys(S.sessionNotifications), []);
    assert.equal(h.intervals.size, 0);
    Object.assign(S, { ptySessionId: 'session', ptyWingId: 'linux', ptyWs: { readyState: 1, send: frame => h.frames.push(JSON.parse(frame)) } });
    h.context.setNotification('session', 'linux');
    assert.deepEqual(h.frames, [{ type: 'pty.attention_ack', session_id: 'session' }]);
    h.context.setNotification('session', 'mac');
    assert.equal(S.sessionNotifications[sessionResourceKey({ id: 'session', wing_id: 'mac' })], true);
});

test('hidden, stale, missing and unavailable paper pages retain attention', () => {
    for (const alter of [
        h => { h.context.document.visibilityState = 'hidden'; },
        h => { h.paper.hidden = true; },
        h => { h.main.dataset.hash = '#sessions'; },
        h => { h.main.dataset.view = 'sessions'; },
        h => { h.context.S.sessionsData = []; },
        h => { h.context.S.currentUser.relay_allowed = false; },
        h => { h.context.S.activeView = 'account'; },
    ]) {
        const h = notificationHarness();
        h.context.location.hash = h.main.dataset.hash;
        alter(h);
        h.context.setNotification('session', 'linux');
        assert.equal(Object.keys(h.context.S.sessionNotifications).length, 1);
        assert.equal(h.intervals.size, 1);
    }
});

test('returning to a visible paper session clears its alert and stops title flashing', () => {
    const h = notificationHarness();
    h.context.location.hash = h.main.dataset.hash;
    h.context.document.visibilityState = 'hidden';
    h.context.setNotification('session', 'linux');
    assert.equal(h.intervals.size, 1);
    h.context.initNotifyListeners();
    h.listeners.visibilitychange();
    assert.equal(Object.keys(h.context.S.sessionNotifications).length, 1);
    h.context.document.visibilityState = 'visible';
    h.listeners.visibilitychange();
    assert.equal(Object.keys(h.context.S.sessionNotifications).length, 0);
    assert.equal(h.intervals.size, 0);
    assert.equal(h.context.document.title, 'wingthing');
    assert.equal(h.acknowledged[0].wing_id, 'linux');
});
