import { S, DOM } from './state.js';
import { renderSidebar } from './render.js';
import { renderDashboard } from './render.js';
import { sessionResourceKey, changeSessionNotification } from './session-reference.js';
import { findSessionResource } from './session-inventory.js';

var notifyChannel = null;

export function checkForNotification(text) {
    var tail = text.slice(-300);
    if (/Allow .+\?/.test(tail)) return true;
    if (/\[Y\/n\]\s*$/.test(tail)) return true;
    if (/\[y\/N\]\s*$/.test(tail)) return true;
    if (/Press Enter/i.test(tail)) return true;
    if (/approve|permission|confirm/i.test(tail) && /\?\s*$/.test(tail)) return true;
    return false;
}

function notificationWing(sessionId, wingId) {
    if (wingId) return wingId;
    var session = findSessionResource(S.sessionsData, sessionId);
    return session && session.wing_id;
}

export function sendAttentionAck(sessionId, wingId) {
    wingId = notificationWing(sessionId, wingId);
    if (!wingId || sessionId !== S.ptySessionId || wingId !== S.ptyWingId || !S.ptyWs || S.ptyWs.readyState !== WebSocket.OPEN) return;
    S.ptyWs.send(JSON.stringify({ type: 'pty.attention_ack', session_id: sessionId }));
}

function isViewingSession(sessionId, wingId) {
    return S.activeView === 'terminal' && sessionId === S.ptySessionId && wingId === S.ptyWingId &&
           document.visibilityState === 'visible';
}

export function setNotification(sessionId, wingId, conversation) {
    wingId = notificationWing(sessionId, wingId);
    if (!sessionId || !wingId) return;

    // If actively viewing this session, just ack — we're already looking at it.
    // Other tabs get the event via their own dashboard WebSocket and show the dot.
    if (isViewingSession(sessionId, wingId)) {
        sendAttentionAck(sessionId, wingId);
        return;
    }

    if (!changeSessionNotification(S.sessionNotifications, sessionId, wingId, true)) return;
    renderSidebar();
    if (S.activeView === 'home') renderDashboard();

    // Broadcast to other tabs so they update UI without firing their own OS notification.
    if (notifyChannel) {
        notifyChannel.postMessage({ type: 'notify', sessionId: sessionId, wingId: wingId });
    }

    if (document.hidden && 'Notification' in window) {
        if (Notification.permission === 'granted') {
            fireOSNotification(sessionId, wingId, conversation);
        } else if (!conversation && Notification.permission === 'default') {
            Notification.requestPermission().then(function(p) {
                if (p === 'granted') fireOSNotification(sessionId, wingId);
            });
        }
    }

    if (!S.titleFlashTimer) {
        var on = true;
        S.titleFlashTimer = setInterval(function() {
            document.title = on ? '(!) wingthing' : 'wingthing';
            on = !on;
            if (!Object.keys(S.sessionNotifications).length) {
                clearInterval(S.titleFlashTimer);
                S.titleFlashTimer = null;
                document.title = 'wingthing';
            }
        }, 1000);
    }
}

async function fireOSNotification(sessionId, wingId, conversation) {
    var options = { body: conversation && conversation.title ? conversation.title + ' needs your attention' : 'A session needs your attention',
        tag: JSON.stringify([wingId, sessionId]), data: { sessionId: sessionId, wingId: wingId, conversationId: conversation && conversation.conversationId || '' } };
    // Home Screen Safari requires persistent service-worker notifications.
    try {
        var registration = 'serviceWorker' in navigator && await navigator.serviceWorker.getRegistration(import.meta.env.BASE_URL);
        if (registration) { await registration.showNotification('wingthing', options); return; }
    } catch (error) { /* Other browsers can still use the page notification. */ }
    try {
        var n = new Notification('wingthing', options);
        n.onclick = function() {
            window.focus();
            if (conversation && conversation.conversationId) {
                import('./conversation-view.js').then(function(mod) { mod.openConversationReference({ wingId: wingId, conversationId: conversation.conversationId }); });
            } else {
                // Lazy import to avoid the nav/notify cycle.
                import('./nav.js').then(function(mod) { mod.switchToSession(sessionId, undefined, wingId); });
            }
        };
    } catch (error) { /* Visible badges remain available without OS support. */ }
}

export function clearNotification(sessionId, wingId) {
    wingId = notificationWing(sessionId, wingId);
    if (!sessionId || !wingId) return;
    if (!changeSessionNotification(S.sessionNotifications, sessionId, wingId, false)) return;
    sendAttentionAck(sessionId, wingId);
    renderSidebar();
    if (S.activeView === 'home') renderDashboard();
    if (!Object.keys(S.sessionNotifications).length) {
        document.title = 'wingthing';
    }
    if (notifyChannel) {
        notifyChannel.postMessage({ type: 'clear', sessionId: sessionId, wingId: wingId });
    }
}

export function initNotifyListeners() {
    document.addEventListener('visibilitychange', function() {
        if (document.visibilityState === 'visible' && S.activeView === 'terminal' && S.ptySessionId) {
            if (S.sessionNotifications[sessionResourceKey({ id: S.ptySessionId, wingId: S.ptyWingId })]) {
                clearNotification(S.ptySessionId, S.ptyWingId);
            }
        }
    });

    // Multi-tab dedup: only the tab that receives the WebSocket event fires the OS notification.
    // Other tabs just update their UI state.
    if ('BroadcastChannel' in window) {
        notifyChannel = new BroadcastChannel('wt-notifications-v2:' + (S.currentUser && S.currentUser.id || 'unknown'));
        notifyChannel.onmessage = function(ev) {
            var d = ev.data;
            if (!d || typeof d.wingId !== 'string' || !d.wingId || typeof d.sessionId !== 'string' || !d.sessionId) return;
            if (d.type === 'notify') {
                if (changeSessionNotification(S.sessionNotifications, d.sessionId, d.wingId, true)) {
                    renderSidebar();
                    if (S.activeView === 'home') renderDashboard();
                }
            } else if (d.type === 'clear') {
                if (changeSessionNotification(S.sessionNotifications, d.sessionId, d.wingId, false)) {
                    renderSidebar();
                    if (S.activeView === 'home') renderDashboard();
                    if (!Object.keys(S.sessionNotifications).length) {
                        document.title = 'wingthing';
                    }
                }
            }
        };
    }
}
