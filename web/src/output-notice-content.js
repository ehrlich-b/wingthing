// Shared presentation; each terminal owns its attachment and recovery action.
export function createOutputNotice(onReload) {
    var notice = document.createElement('dialog');
    notice.className = 'output-notice';
    notice.tabIndex = -1;
    notice.setAttribute('aria-label', 'Output incomplete');
    var text = document.createElement('p');
    text.textContent = 'Output incomplete. Some terminal output could not be decoded. Reload to restore the session.';
    notice.appendChild(text);
    var reload = document.createElement('button');
    reload.type = 'button';
    reload.className = 'btn-sm';
    reload.textContent = 'Reload';
    reload.addEventListener('click', onReload);
    notice.appendChild(reload);
    return notice;
}

export function endOutputNotice(notice) {
    notice.querySelector('p').textContent = 'Output incomplete. Some terminal output could not be decoded. The session has ended.';
    var reload = notice.querySelector('button');
    if (reload) reload.remove();
}
