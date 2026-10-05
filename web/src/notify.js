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

export function setNotification(sessionId, wingId) {
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
            fireOSNotification(sessionId, wingId);
        } else if (Notification.permission === 'default') {
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

function fireOSNotification(sessionId, wingId) {
    var n = new Notification('wingthing', { body: 'A session needs your attention' });
    n.onclick = function() {
        window.focus();
        // Lazy import to avoid circular dependency (nav.js imports from notify.js)
        import('./nav.js').then(function(mod) { mod.switchToSession(sessionId, undefined, wingId); });
    };
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
