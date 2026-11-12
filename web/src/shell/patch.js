// Patch keyed elements in place so reads do not replace focused inputs,
// terminal mounts, scroll containers, or the surrounding frame.
const key = node => node.nodeType === 1 ? node.getAttribute('data-key') || node.getAttribute('data-k') || node.id || node.getAttribute('data-machine') || node.getAttribute('href') || '' : '';
const compatible = (a, b) => a && a.nodeType === b.nodeType && (a.nodeType !== 1 || a.tagName === b.tagName) && key(a) === key(b);
const move = (parent, node, before) => {
    if (parent.moveBefore && node.isConnected) parent.moveBefore(node, before);
    else parent.insertBefore(node, before);
};

function retainSessionLayout(current, next) {
    // Keep the incoming order. Only adjust the preview boundary when moving
    // the reader's row between parents would detach it and lose focus.
    const selector = '.pp-srow, .wd-session-row, [data-session-row]';
    const rows = [...next.querySelectorAll(selector)];
    const currentPreview = current.querySelector(':scope > ul') || current;
    const nextPreview = next.querySelector(':scope > ul') || next;
    let limit = Number(current.getAttribute('data-session-list')) || 8;
    const selection = current.ownerDocument.getSelection();
    const mountedRows = [...current.querySelectorAll(selector)];
    const reader = mountedRows.find(row => row.contains(current.ownerDocument.activeElement)) ||
        mountedRows.find(row => selection && (row.contains(selection.anchorNode) || row.contains(selection.focusNode)));
    const index = reader ? rows.findIndex(row => key(row) === key(reader)) : -1;
    if (index !== -1) {
        limit = reader.parentElement === currentPreview ? Math.max(limit, index + 1) : Math.min(limit, index);
    }
    const preview = rows.slice(0, limit), overflow = rows.slice(limit);
    let details = next.querySelector(':scope > .pp-more-sessions, :scope > .wd-more');
    for (const row of [...preview, ...overflow]) row.remove();
    if (overflow.length) {
        if (!details) {
            details = next.ownerDocument.createElement('details');
            details.className = current.querySelector('details')?.className || 'pp-more-sessions';
            details.append(next.ownerDocument.createElement('summary'));
            if (nextPreview !== next) details.append(next.ownerDocument.createElement('ul'));
            next.append(details);
        }
        details.querySelector('summary').textContent = '+' + overflow.length + ' more';
        (details.querySelector('ul') || details).append(...overflow);
    } else if (details) {
        details.remove();
        details = null;
    }
    for (const row of preview) {
        if (nextPreview === next) next.insertBefore(row, details);
        else nextPreview.append(row);
    }
    // Reuse surviving keyed rows even when their preview/overflow parent
    // changes. The boundary above guarantees the reader's row never moves.
    const mounted = new Map(mountedRows.map(row => [key(row), row]));
    let currentDetails = current.querySelector(':scope > .pp-more-sessions, :scope > .wd-more');
    if (details && !currentDetails) {
        currentDetails = details.cloneNode(true);
        currentDetails.querySelectorAll(selector).forEach(row => row.remove());
        current.append(currentDetails);
    }
    for (const row of rows) {
        const live = mounted.get(key(row));
        const parent = preview.includes(row) ? currentPreview : currentDetails.querySelector('ul') || currentDetails;
        if (live && live.parentElement !== parent) move(parent, live, parent === current ? currentDetails : null);
    }
}

function patchNode(current, next) {
    if (current.nodeType !== 1) {
        if (current.nodeValue !== next.nodeValue) current.nodeValue = next.nodeValue;
        return;
    }
    // These engines own their children and listeners.
    if (current.hasAttribute('data-engine')) return;
    if (current.hasAttribute('data-session-list') && next.hasAttribute('data-session-list')) retainSessionLayout(current, next);
    // Disclosure state belongs to the reader after the element is mounted.
    const readerOwned = name => current.tagName === 'DETAILS' && name === 'open';
    for (const attr of [...current.attributes]) if (!readerOwned(attr.name) && !next.hasAttribute(attr.name)) current.removeAttribute(attr.name);
    for (const attr of next.attributes) if (!readerOwned(attr.name) && current.getAttribute(attr.name) !== attr.value) current.setAttribute(attr.name, attr.value);
    patchChildren(current, next);
}

function patchChildren(current, next) {
    const wanted = [...next.childNodes];
    const retained = new Set();
    const selection = current.ownerDocument.getSelection();
    const focused = current.ownerDocument.activeElement;
    const ownsReader = node => node.contains(focused) ||
        !current.contains(focused) && selection && (node.contains(selection.anchorNode) || node.contains(selection.focusNode));
    let cursor = current.firstChild;
    for (const child of wanted) {
        let match = compatible(cursor, child) ? cursor : [...current.childNodes].find(n =>
            !retained.has(n) && compatible(n, child));
        if (!match) {
            match = child.cloneNode(true);
            current.insertBefore(match, cursor);
        } else if (match !== cursor) {
            // Never detach the reader's focused or selected subtree. Even
            // moveBefore preserves focus but can reset a DOM text selection.
            // Leave the reader's row mounted and advance past it. Later
            // incoming siblings move around it to reach their final order.
            if (!ownsReader(match)) move(current, match, cursor);
        }
        patchNode(match, child);
        retained.add(match);
        cursor = match.nextSibling;
    }
    for (const child of [...current.childNodes]) if (!retained.has(child)) child.remove();
}

export function patchRegion(current, html) {
    // Apply the same layout and disclosure policies when the caller patches
    // a list itself as when that list is nested inside a larger region.
    const next = current.cloneNode(false);
    next.innerHTML = html;
    patchNode(current, next);
}
