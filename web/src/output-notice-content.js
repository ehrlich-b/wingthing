// Shared presentation; each terminal owns its attachment and recovery action.
export function createOutputNotice(onReload, decryptionFailed) {
    var notice = document.createElement('dialog');
    notice.className = 'output-notice';
    notice.tabIndex = -1;
    notice.setAttribute('aria-label', 'Output incomplete');
    var text = document.createElement('p');
    notice.appendChild(text);
    updateOutputNotice(notice, decryptionFailed);
    var reload = document.createElement('button');
    reload.type = 'button';
    reload.className = 'btn-sm';
    reload.textContent = 'Reload';
    reload.addEventListener('click', onReload);
    notice.appendChild(reload);
    return notice;
}

export function updateOutputNotice(notice, decryptionFailed) {
    if (decryptionFailed || !notice.outputFailure) {
        notice.outputFailure = decryptionFailed ? "Can't decrypt output." : 'Some terminal output could not be decoded.';
    }
    notice.querySelector('p').textContent = 'Output incomplete. ' + notice.outputFailure +
        (notice.outputEnded ? ' The session has ended.' : ' Reload to restore the session.');
}

export function endOutputNotice(notice) {
    notice.outputEnded = true;
    updateOutputNotice(notice);
    var reload = notice.querySelector('button');
    if (reload) reload.remove();
}
