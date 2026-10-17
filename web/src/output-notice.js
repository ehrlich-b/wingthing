import { S } from './state.js';
import { retryReconnect } from './pty.js';
import { createOutputNotice, endOutputNotice } from './output-notice-content.js';

export function clearOutputIncomplete(recoveredSocket) {
    var notice = document.getElementById('pty-output-notice');
    // A successful replay may clear an earlier connection's notice, but never
    // one raised by a corrupt chunk in that same replay.
    if (notice && (!recoveredSocket || notice.outputSocket !== recoveredSocket)) {
        notice.outputControls.forEach(function (control) { control.inert = false; });
        notice.remove();
    }
}

export function endOutputIncomplete() {
    var notice = document.getElementById('pty-output-notice');
    if (!notice) return;
    endOutputNotice(notice);
    // The ended session cannot accept input, but its output remains readable.
    notice.outputControls = notice.outputControls.filter(function (control) {
        if (control.id !== 'terminal-container') return true;
        control.inert = false;
        return false;
    });
}

export function showOutputIncomplete(socket, sessionId, wingId, decryptionFailed) {
    if (S.ptyWs !== socket || S.ptySessionId !== sessionId || S.ptyWingId !== wingId) return;
    clearOutputIncomplete();
    var notice = createOutputNotice(function () {
        if (!notice.isConnected || S.ptySessionId !== sessionId || S.ptyWingId !== wingId) return;
        notice.focus();
        notice.querySelector('p').textContent = 'Output incomplete. Reloading session…';
        retryReconnect();
    }, decryptionFailed);
    notice.id = 'pty-output-notice';
    notice.outputSocket = socket;
    // Keep terminal input inaccessible through reconnect/key setup while leaving
    // authentication and navigation available. Preserve any existing inert UI.
    notice.outputControls = Array.from(document.querySelectorAll('#terminal-container, #modifier-keys, #type-overlay'))
        .filter(function (control) { return !control.inert; });
    notice.outputControls.forEach(function (control) { control.inert = true; });
    document.getElementById('terminal-section').before(notice);
    notice.show();
    notice.focus();
}
