import { S, DOM } from './state.js';
import { renderSidebar } from './render.js';
import { renderDashboard } from './render.js';

export function checkForNotification(text) {
    var tail = text.slice(-300);
    if (/Allow .+\?/.test(tail)) return true;
    if (/\[Y\/n\]\s*$/.test(tail)) return true;
    if (/\[y\/N\]\s*$/.test(tail)) return true;
    if (/Press Enter/i.test(tail)) return true;
    if (/approve|permission|confirm/i.test(tail) && /\?\s*$/.test(tail)) return true;
    return false;
}

export function sendAttentionAck(sessionId) {
    if (!sessionId || !S.ptyWs || S.ptyWs.readyState !== WebSocket.OPEN) return;
    S.ptyWs.send(JSON.stringify({ type: 'pty.attention_ack', session_id: sessionId }));
}

function isViewingSession(sessionId) {
    return S.activeView === 'terminal' && sessionId === S.ptySessionId &&
           document.visibilityState === 'visible';
}

export function setNotification(sessionId) {
    if (!sessionId) return;

    if (isViewingSession(sessionId)) {
        sendAttentionAck(sessionId);
        return;
    }

    if (S.sessionNotifications[sessionId]) return;
    S.sessionNotifications[sessionId] = true;
    renderSidebar();
    if (S.activeView === 'home') renderDashboard();

    if (document.hidden && 'Notification' in window) {
        if (Notification.permission === 'granted') {
            new Notification('wingthing', { body: 'A session needs your attention' });
        } else if (Notification.permission === 'default') {
            Notification.requestPermission().then(function(p) {
                if (p === 'granted') {
                    new Notification('wingthing', { body: 'A session needs your attention' });
                }
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

export function clearNotification(sessionId) {
    if (!sessionId || !S.sessionNotifications[sessionId]) return;
    delete S.sessionNotifications[sessionId];
    sendAttentionAck(sessionId);
    renderSidebar();
    if (S.activeView === 'home') renderDashboard();
    if (!Object.keys(S.sessionNotifications).length) {
        document.title = 'wingthing';
    }
}

export function initNotifyListeners() {
    document.addEventListener('visibilitychange', function() {
        if (document.visibilityState === 'visible' && S.activeView === 'terminal' && S.ptySessionId) {
            if (S.sessionNotifications[S.ptySessionId]) {
                clearNotification(S.ptySessionId);
            }
        }
    });
}
