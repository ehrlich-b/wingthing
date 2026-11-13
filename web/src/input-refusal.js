// Navigation must not hide the outcome of an input already being encrypted.
export function showInputRefusal(sessionId, wingId, message, doc = document) {
    let container = doc.getElementById('input-refusals');
    if (!container) {
        container = doc.createElement('div');
        container.id = 'input-refusals';
        container.className = 'input-refusals';
        const visible = doc.createElement('div');
        const more = doc.createElement('details');
        more.appendChild(doc.createElement('summary'));
        container.append(visible, more);
        doc.body.appendChild(container);
    }
    const refresh = () => {
        const notices = [...container.querySelectorAll('.browser-open-toast')];
        const [visible, more] = container.children;
        notices.forEach((item, index) => (index < 3 ? visible : more).appendChild(item));
        more.hidden = notices.length <= 3;
        more.firstChild.textContent = '+' + Math.max(0, notices.length - 3) + ' more';
        if (!notices.length) container.remove();
    };
    const id = 'input-refusal-' + JSON.stringify([wingId, sessionId]);
    let notice = doc.getElementById(id);
    if (!notice) {
        notice = doc.createElement('div');
        notice.id = id;
        notice.className = 'browser-open-toast';
        notice.setAttribute('role', 'alert');
        const text = doc.createElement('span');
        const link = doc.createElement('a');
        link.href = '#session/' + encodeURIComponent(wingId) + '/' + encodeURIComponent(sessionId);
        link.textContent = wingId + ' / ' + sessionId;
        const dismiss = doc.createElement('button');
        dismiss.className = 'browser-open-dismiss';
        dismiss.textContent = '×';
        dismiss.setAttribute('aria-label', 'Dismiss input warning');
        dismiss.onclick = () => {
            notice.remove();
            refresh();
        };
        notice.append(text, link, dismiss);
        container.firstChild.appendChild(notice);
    }
    notice.firstChild.textContent = message;
    refresh();
}
