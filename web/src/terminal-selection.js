export function cleanTerminalSelection(text) {
    if (!text) return '';
    return text
        .split(/\r?\n/)
        .map(function(line) { return line.replace(/[ \t]+$/, ''); })
        .join('\n')
        .replace(/^(?:[ \t]*\n)+/, '')
        .replace(/(?:\n[ \t]*)+$/, '');
}

export function terminalClipboardAvailable(nav, doc) {
    nav = nav || globalThis.navigator;
    doc = doc || globalThis.document;
    return !!(doc && typeof doc.execCommand === 'function') ||
        !!(nav && nav.clipboard && typeof nav.clipboard.writeText === 'function');
}

function copyWithExecCommand(text, doc) {
    if (!doc || typeof doc.execCommand !== 'function' || !doc.body || !doc.createElement) return false;
    var active = doc.activeElement;
    var input = doc.createElement('textarea');
    input.value = text;
    input.setAttribute('readonly', '');
    input.style.position = 'fixed';
    input.style.opacity = '0';
    doc.body.appendChild(input);
    input.focus();
    input.select();
    var copied = false;
    try { copied = doc.execCommand('copy'); } catch (e) {}
    input.remove();
    if (active && typeof active.focus === 'function') active.focus();
    return copied;
}

export function copyTextWithFallback(text, nav, doc) {
    nav = nav || globalThis.navigator;
    doc = doc || globalThis.document;
    if (copyWithExecCommand(text, doc)) return Promise.resolve(true);
    if (!nav || !nav.clipboard || typeof nav.clipboard.writeText !== 'function') return Promise.resolve(false);
    return Promise.resolve(nav.clipboard.writeText(text)).then(function() { return true; }).catch(function() { return false; });
}
