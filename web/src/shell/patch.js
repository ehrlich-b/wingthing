// Patch keyed elements in place so reads do not replace focused inputs,
// terminal mounts, scroll containers, or the surrounding frame.
const key = node => node.nodeType === 1 ? node.getAttribute('data-k') || node.id || node.getAttribute('data-machine') || node.getAttribute('href') || '' : '';
const compatible = (a, b) => a && a.nodeType === b.nodeType && (a.nodeType !== 1 || a.tagName === b.tagName) && key(a) === key(b);

function patchNode(current, next) {
    if (current.nodeType !== 1) {
        if (current.nodeValue !== next.nodeValue) current.nodeValue = next.nodeValue;
        return;
    }
    // These engines own their children and listeners.
    if (current.hasAttribute('data-engine')) return;
    for (const attr of [...current.attributes]) if (!next.hasAttribute(attr.name)) current.removeAttribute(attr.name);
    for (const attr of next.attributes) if (current.getAttribute(attr.name) !== attr.value) current.setAttribute(attr.name, attr.value);
    patchChildren(current, next);
}

function patchChildren(current, next) {
    const wanted = [...next.childNodes];
    const retained = new Set();
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
