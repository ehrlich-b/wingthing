// Patch keyed elements in place so reads do not replace focused inputs,
// terminal mounts, scroll containers, or the surrounding frame.
const key = node => node.nodeType === 1 ? node.getAttribute('data-k') || node.id || node.getAttribute('data-machine') || node.getAttribute('href') || '' : '';
const compatible = (a, b) => a && a.nodeType === b.nodeType && (a.nodeType !== 1 || a.tagName === b.tagName) && key(a) === key(b);

function retainSessionLayout(current, next) {
    // The first eight rows and the disclosure have different DOM parents.
    // Keep surviving rows in their mounted order and parent; only arrivals
    // fill vacant preview slots. Promoting or displacing a surviving row
    // would detach it, losing the reader's focus and selection.
    const arrivals = new Map([...next.querySelectorAll('.pp-srow')].map(row => [key(row), row]));
    const preview = [], overflow = [];
    for (const row of current.querySelectorAll('.pp-srow')) {
        const updated = arrivals.get(key(row));
        if (!updated) continue;
        (row.parentElement === current ? preview : overflow).push(updated);
        arrivals.delete(key(row));
    }
    for (const row of arrivals.values()) (preview.length < 8 ? preview : overflow).push(row);
    let details = next.querySelector('.pp-more-sessions');
    for (const row of [...preview, ...overflow]) row.remove();
    if (overflow.length) {
        if (!details) {
            details = next.ownerDocument.createElement('details');
            details.className = 'pp-more-sessions';
            details.append(next.ownerDocument.createElement('summary'));
            next.append(details);
        }
        details.querySelector('summary').textContent = '+' + overflow.length + ' more';
        details.append(...overflow);
    } else if (details) {
        details.remove();
        details = null;
    }
    for (const row of preview) next.insertBefore(row, details);
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
    // Remove departed keyed siblings first. Moving an unchanged group ahead
    // of a departed one would detach its focused descendants in the browser.
    for (const child of [...current.childNodes]) {
        if (key(child) && !wanted.some(node => compatible(child, node))) child.remove();
    }
    let cursor = current.firstChild;
    for (const child of wanted) {
        const identity = key(child);
        let match = compatible(cursor, child) ? cursor : identity ? [...current.childNodes].find(n => !retained.has(n) && compatible(n, child)) : null;
        if (!match) {
            match = child.cloneNode(true);
            current.insertBefore(match, cursor);
        } else if (match !== cursor) current.insertBefore(match, cursor);
        patchNode(match, child);
        retained.add(match);
        cursor = match.nextSibling;
    }
    for (const child of [...current.childNodes]) if (!retained.has(child)) child.remove();
}

export function patchRegion(current, html) {
    const template = document.createElement('template');
    template.innerHTML = html;
    patchChildren(current, template.content);
}
