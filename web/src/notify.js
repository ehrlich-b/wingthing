import { S, DOM } from './state.js';
import { renderSidebar } from './render.js';
import { renderDashboard } from './render.js';

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

    // Broadcast to other tabs so they update UI without firing their own OS notification.
    if (notifyChannel) {
        notifyChannel.postMessage({ type: 'notify', sessionId: sessionId });
    }

    if (document.hidden && 'Notification' in window) {
        if (Notification.permission === 'granted') {
            fireOSNotification(sessionId);
        } else if (Notification.permission === 'default') {
            Notification.requestPermission().then(function(p) {
                if (p === 'granted') fireOSNotification(sessionId);
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

function fireOSNotification(sessionId) {
    var n = new Notification('wingthing', { body: 'A session needs your attention' });
    n.onclick = function() {
        window.focus();
        // Lazy import to avoid circular dependency (nav.js imports from notify.js)
        import('./nav.js').then(function(mod) { mod.switchToSession(sessionId); });
    };
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
    if (notifyChannel) {
        notifyChannel.postMessage({ type: 'clear', sessionId: sessionId });
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

    // Multi-tab dedup: only the tab that receives the WebSocket event fires the OS notification.
    // Other tabs just update their UI state.
    if ('BroadcastChannel' in window) {
        notifyChannel = new BroadcastChannel('wt-notifications');
        notifyChannel.onmessage = function(ev) {
            var d = ev.data;
            if (d.type === 'notify') {
                if (!S.sessionNotifications[d.sessionId]) {
                    S.sessionNotifications[d.sessionId] = true;
                    renderSidebar();
                    if (S.activeView === 'home') renderDashboard();
                }
            } else if (d.type === 'clear') {
                if (S.sessionNotifications[d.sessionId]) {
                    delete S.sessionNotifications[d.sessionId];
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
