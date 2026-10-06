import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';

const source = readFileSync(new URL('../src/notify.js', import.meta.url), 'utf8')
    .replace(/^import .*;\n/gm, '').replace(/^export /gm, '')
    .replaceAll('import.meta.env.BASE_URL', "'/app/'")
    .replace(/\bimport\(/g, 'loadModule(');

function notificationHarness(persistent = false) {
    const notifications = [], opened = [];
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
        navigator: persistent ? { serviceWorker: { getRegistration: async () => ({
            showNotification: async (title, options) => notifications.push({ title, options }),
        }) } } : {},
        window: { focus() { focused = true; } },
        loadModule: async () => ({
            openConversationReference: reference => opened.push({ wingId: reference.wingId, sessionId: 'new-execution' }),
            openConversationTranscript: (conversation, wingId) => opened.push({ wingId, sessionId: conversation.session_id, conversationId: conversation.conversation_id, rootId: conversation.root_conversation_id }),
            switchToSession: (sessionId, _history, wingId) => opened.push({ wingId, sessionId }),
        }),
    });
    vm.runInContext(source, context, { filename: 'notify.js' });
    return { context, notifications, opened, focused: () => focused };
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
    assert.deepEqual(h.opened, [{ wingId: 'linux', sessionId: 'session' }]);
});
