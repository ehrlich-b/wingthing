import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

// One browser regression for every list view, using its mounted production
// markup and the production patcher, with no dependency on poll timing.
export async function assertListViewRefresh(page, { root, go, checked }) {
    const source = fs.readFileSync(path.join(root, 'web/src/shell/patch.js'), 'utf8')
        .replace('export function patchRegion', 'function patchRegion');
    for (const [view, hash, region, rows, focus] of [
        ['Home', '#', '.home-section[data-k="working"] .home-list', '.home-row', null],
        ['Sessions', '#sessions', '[data-k="session-group/work"]', '.pp-srow', null],
        ['Machines', '#machines', '.pp-col-main', '.pp-machine', 'li a'],
        ['Machine', '#machine/studio', '#wd-active-sessions', '.wd-session-row', null],
    ]) {
        await go(page, hash);
        await page.locator(region).locator(rows).nth(2).waitFor({ state: 'attached' });
        const results = await page.locator(region).evaluate((region, {source, rows, focus}) => {
            const patch = eval(source + '; patchRegion');
            const original = region.innerHTML;
            const templates = Object.fromEntries([...region.querySelectorAll(rows)].slice(0, 3).map((row, i) => {
                const copy = row.cloneNode(true), key = 'abc'[i];
                copy.dataset.key = key;
                if (copy.hasAttribute('data-k')) copy.dataset.k = key;
                return [key, copy.outerHTML];
            }));
            const results = [];
            for (const fallback of [false, true]) {
                const descriptor = Object.getOwnPropertyDescriptor(Element.prototype, 'moveBefore');
                if (fallback) Object.defineProperty(Element.prototype, 'moveBefore', {value: undefined, configurable: true});
                try {
                    for (const [before, after] of [['bc', 'abc'], ['abc', 'cba'], ['abc', 'bc']]) {
                        patch(region, [...before].map(key => templates[key]).join(''));
                        const row = region.querySelector('[data-key="b"]');
                        const target = focus ? row.querySelector(focus) : row;
                        target.focus({preventScroll: true});
                        if (document.activeElement !== target) throw Error('Probe did not focus b');
                        let blurs = 0;
                        target.addEventListener('blur', () => blurs++);
                        patch(region, [...after].map(key => templates[key]).join(''));
                        results.push({before, after, fallback,
                            order: [...region.querySelectorAll(rows)].map(row => row.dataset.key || row.dataset.k).join(''),
                            focused: document.activeElement === target,
                            retained: region.querySelector('[data-key="b"]') === row, blurs});
                    }
                } finally {
                    if (descriptor) Object.defineProperty(Element.prototype, 'moveBefore', descriptor);
                    else delete Element.prototype.moveBefore;
                    patch(region, original);
                }
            }
            return results;
        }, {source, rows, focus});
        for (const result of results) assert.deepEqual(result, {
            before: result.before, after: result.after, fallback: result.fallback,
            order: result.after, focused: true, retained: true, blurs: 0,
        }, view + ': ' + JSON.stringify(result));
        checked('list-order-' + view + '-' + page.viewportSize().width, results);
    }
    for (const [view, hash, region, rows, focus, focusedIndex] of [
        ['Home', '#', '.home-section[data-k="working"] .home-list', '.home-row', null, 1],
        ['Sessions', '#sessions', '.pp-col-main', '[data-k="session-group/work"] .pp-srow', null, 7],
        ['Machines', '#machines', '.pp-col-main', '.pp-machine', 'li a', 1],
        ['Machines sessions', '#machines', '[data-machine="studio"]', 'li', 'a', 2],
        ['Machine', '#machine/studio', '#wing-detail-content', '.wd-session-row', null, 7],
        ['Sessions list', '#sessions', '[data-k="session-group/work"]', '.pp-srow', null, 8],
        ['Machines session list', '#machines', '[data-machine="studio"] [data-session-list]', 'li', 'a', 3],
        ['Machine session list', '#machine/studio', '#wd-active-sessions', '.wd-session-row', null, 8],
    ]) {
        await go(page, hash);
        await page.locator(region).locator(rows).first().waitFor({ state: 'attached' });
        const results = await page.locator(region).evaluate((region, { source, rows, focus, focusedIndex }) => {
            const patch = eval(source + '; patchRegion');
            const original = region.innerHTML;
            const results = [];
            // Model the renderer's eight-row (or three-row) boundary for
            // both whole-view refreshes and direct list refreshes.
            const rebalance = next => {
                const lists = [...next.querySelectorAll('[data-session-list]')];
                if (next.matches('[data-session-list]')) lists.unshift(next);
                for (const list of lists) {
                    const preview = list.querySelector(':scope > ul') || list;
                    const items = [...list.querySelectorAll('.pp-srow, .wd-session-row, [data-session-row]')];
                    const limit = Number(list.getAttribute('data-session-list')) || 8;
                    const details = list.querySelector('details');
                    if (!details || items.length <= limit) continue;
                    const overflow = details.querySelector('ul') || details;
                    for (const item of items.slice(0, limit)) preview.insertBefore(item, preview === list ? details : null);
                    for (const item of items.slice(limit)) overflow.append(item);
                }
            };
            for (const fallback of [false, true]) {
                // Also pin the fallback used by browsers without moveBefore.
                const descriptor = Object.getOwnPropertyDescriptor(Element.prototype, 'moveBefore');
                if (fallback) Object.defineProperty(Element.prototype, 'moveBefore', { value: undefined, configurable: true });
                try {
                    patch(region, original);
                    const mounted = [...region.querySelectorAll(rows)];
                    if (!mounted.length || mounted.some(row => !(row.dataset.key || row.dataset.k))) throw Error('Every list row needs a stable data-key');
                    const row = mounted[Math.min(focusedIndex, mounted.length - 1)];
                    const parent = row.parentElement;
                    const target = focus ? row.querySelector(focus) : row;
                    for (let ancestor = target.parentElement; ancestor && region.contains(ancestor); ancestor = ancestor.parentElement) {
                        if (ancestor.tagName === 'DETAILS') ancestor.open = true;
                    }
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
                        retained: [...region.querySelectorAll(rows)].find(node => (node.dataset.key || node.dataset.k) === (row.dataset.key || row.dataset.k)) === row,
                        focused: document.activeElement === target,
                        parentRetained: row.parentElement === parent,
                        selectionRetained: selection.anchorNode === text && selection.toString() === selected,
                        blurs,
                    });
                    const next = region.cloneNode(true);
                    let expectedOrder;
                    const apply = next => {
                        expectedOrder = [...next.querySelectorAll(rows)].map(row => row.dataset.key || row.dataset.k);
                        patch(region, next.innerHTML);
                        if (JSON.stringify([...region.querySelectorAll(rows)].map(row => row.dataset.key || row.dataset.k)) !== JSON.stringify(expectedOrder)) {
                            throw Error('Incoming row order was not preserved: ' + JSON.stringify({ expectedOrder, actual: [...region.querySelectorAll(rows)].map(row => row.dataset.key || row.dataset.k) }));
                        }
                    };
                    const arrival = next.querySelector(rows).cloneNode(true);
                    arrival.dataset.key = (arrival.dataset.key || arrival.dataset.k) + '/earlier';
                    if (arrival.hasAttribute('data-k')) arrival.dataset.k = arrival.dataset.key;
                    for (const link of [arrival, ...arrival.querySelectorAll('a')]) {
                        if (link.hasAttribute('href')) link.setAttribute('href', link.getAttribute('href') + '-earlier');
                    }
                    const first = next.querySelector(rows);
                    first.before(arrival);
                    // Render the incoming inventory's normal preview/overflow
                    // boundary too: the last preview row must stay mounted.
                    rebalance(next);
                    apply(next);
                    if (![...region.querySelectorAll(rows)].some(node => node.dataset.key === arrival.dataset.key)) throw Error('Arrival was dropped');
                    results.push(observe('insert-earlier'));
                    patch(region, original);
                    results.push(observe('remove-earlier'));
                    const reordered = region.cloneNode(true);
                    const orderedRows = [...reordered.querySelectorAll(rows)];
                    orderedRows[0].before(orderedRows[Math.min(1, orderedRows.length - 1)]);
                    apply(reordered);
                    results.push(observe('reorder'));
                    patch(region, original);
                    // A pending stop can finish while the ninth row is
                    // focused inside expanded overflow. Its renderer now
                    // puts that row in the first eight; keep its live node.
                    const departed = region.cloneNode(true);
                    departed.querySelector(rows).remove();
                    rebalance(departed);
                    apply(departed);
                    results.push(observe('remove-first'));
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
