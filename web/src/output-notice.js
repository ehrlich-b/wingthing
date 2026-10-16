import { S } from './state.js';
import { retryReconnect } from './pty.js';

export function clearOutputIncomplete(recoveredSocket) {
    var notice = document.getElementById('pty-output-notice');
    // A successful replay may clear an earlier connection's notice, but never
    // one raised by a corrupt chunk in that same replay.
    if (notice && (!recoveredSocket || notice.outputSocket !== recoveredSocket)) notice.remove();
}

export function showOutputIncomplete(socket, sessionId, wingId) {
    if (S.ptyWs !== socket || S.ptySessionId !== sessionId || S.ptyWingId !== wingId) return;
    clearOutputIncomplete();
    var notice = document.createElement('dialog');
    notice.id = 'pty-output-notice';
    notice.outputSocket = socket;
    notice.tabIndex = -1;
    notice.setAttribute('aria-label', 'Output incomplete');
    var text = document.createElement('p');
    text.textContent = 'Output incomplete. Some terminal output could not be decoded. Reload to restore the session.';
    notice.appendChild(text);
    var reload = document.createElement('button');
    reload.type = 'button';
    reload.className = 'btn-sm';
    reload.textContent = 'Reload';
    reload.addEventListener('click', function () {
        if (!notice.isConnected || S.ptySessionId !== sessionId || S.ptyWingId !== wingId) return;
        notice.focus();
        text.textContent = 'Output incomplete. Reloading session…';
        retryReconnect();
    });
    notice.appendChild(reload);
    // Keep keyboard focus out of xterm while the notice is open, including
    // reconnect/key setup, without changing the terminal's input lifecycle.
    notice.addEventListener('cancel', function (event) { event.preventDefault(); });
    document.body.appendChild(notice);
    notice.showModal();
    notice.focus();
}
