import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

// One browser regression for every list view, using its mounted production
// markup and the production patcher, with no dependency on poll timing.
export async function assertListViewRefresh(page, { root, go, checked }) {
    const source = fs.readFileSync(path.join(root, 'web/src/shell/patch.js'), 'utf8')
        .replace('export function patchRegion', 'function patchRegion');
    for (const [view, hash, region, rows, focus, focusedIndex] of [
        ['Home', '#', '.pp-col-main', '.pp-row-session', 'a', 1],
        ['Sessions', '#sessions', '.pp-col-main', '[data-k="session-group/work"] .pp-srow', null, 7],
        ['Machines', '#machines', '.pp-col-main', '.pp-machine', 'li a', 1],
        ['Machines sessions', '#machines', '[data-machine="studio"]', 'li', 'a', 2],
        ['Machine', '#machine/studio', '#wing-detail-content', '.wd-session-row', null, 7],
    ]) {
        await go(page, hash);
        await page.locator(region).locator(rows).first().waitFor({ state: 'attached' });
        const results = await page.locator(region).evaluate((region, { source, rows, focus, focusedIndex }) => {
            const patch = eval(source + '; patchRegion');
            const original = region.innerHTML;
            const results = [];
            for (const fallback of [false, true]) {
                // Also pin the fallback used by browsers without moveBefore.
                const descriptor = Object.getOwnPropertyDescriptor(Element.prototype, 'moveBefore');
                if (fallback) Object.defineProperty(Element.prototype, 'moveBefore', { value: undefined, configurable: true });
                try {
                    patch(region, original);
                    const mounted = [...region.querySelectorAll(rows)];
                    if (!mounted.length || mounted.some(row => !row.dataset.key)) throw Error('Every list row needs a stable data-key');
                    const row = mounted[Math.min(focusedIndex, mounted.length - 1)];
                    const parent = row.parentElement;
                    const target = focus ? row.querySelector(focus) : row;
                    const text = [...(function* walk(node) {
                        if (node.nodeType === Node.TEXT_NODE && node.textContent.trim()) yield node;
                        for (const child of node.childNodes) yield* walk(child);
                    })(target)][0];
                    target.focus({ preventScroll: true });
                    const selection = getSelection();
                    selection.setBaseAndExtent(text, 0, text, Math.min(4, text.length));
                    const selected = selection.toString();
                    let blurs = 0;
                    target.addEventListener('blur', () => blurs++);
                    const observe = step => ({
                        step, fallback,
                        retained: [...region.querySelectorAll(rows)].find(node => node.dataset.key === row.dataset.key) === row,
                        focused: document.activeElement === target,
                        parentRetained: row.parentElement === parent,
                        selectionRetained: selection.anchorNode === text && selection.toString() === selected,
                        blurs,
                    });
                    const next = region.cloneNode(true);
                    const arrival = next.querySelector(rows).cloneNode(true);
                    arrival.dataset.key += '/earlier';
                    if (arrival.hasAttribute('data-k')) arrival.dataset.k = arrival.dataset.key;
                    for (const link of [arrival, ...arrival.querySelectorAll('a')]) {
                        if (link.hasAttribute('href')) link.setAttribute('href', link.getAttribute('href') + '-earlier');
                    }
                    const first = next.querySelector(rows);
                    first.before(arrival);
                    // Render the incoming inventory's normal preview/overflow
                    // boundary too: the last preview row must stay mounted.
                    for (const list of next.querySelectorAll('[data-session-list]')) {
                        const preview = list.querySelector(':scope > ul') || list;
                        const items = [...list.querySelectorAll('.pp-srow, .wd-session-row, [data-session-row]')];
                        const limit = Number(list.getAttribute('data-session-list')) || 8;
                        const details = list.querySelector('details');
                        if (!details || items.length <= limit) continue;
                        const overflow = details.querySelector('ul') || details;
                        for (const item of items.slice(0, limit)) preview.insertBefore(item, preview === list ? details : null);
                        for (const item of items.slice(limit)) overflow.append(item);
                    }
                    patch(region, next.innerHTML);
                    if (![...region.querySelectorAll(rows)].some(node => node.dataset.key === arrival.dataset.key)) throw Error('Arrival was dropped');
                    results.push(observe('insert-earlier'));
                    patch(region, original);
                    results.push(observe('remove-earlier'));
                    const reordered = region.cloneNode(true);
                    const orderedRows = [...reordered.querySelectorAll(rows)];
                    orderedRows[0].before(orderedRows[Math.min(1, orderedRows.length - 1)]);
                    patch(region, reordered.innerHTML);
                    results.push(observe('reorder'));
                    patch(region, original);
                } finally {
                    if (descriptor) Object.defineProperty(Element.prototype, 'moveBefore', descriptor);
                    else delete Element.prototype.moveBefore;
                }
            }
            return results;
        }, { source, rows, focus, focusedIndex });
        for (const result of results) assert.deepEqual(result, {
            step: result.step, fallback: result.fallback,
            retained: true, focused: true, parentRetained: true, selectionRetained: true, blurs: 0,
        }, view + ': ' + JSON.stringify(result));
        checked('list-focus-' + view + '-' + page.viewportSize().width, results);
    }
}
